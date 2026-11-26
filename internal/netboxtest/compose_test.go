package netboxtest

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/go-cmp/cmp"
)

// TestComposeFile checks the real compose.yaml: that netboxtest can run it,
// that it runs the NetBox release the project targets, and that both
// profiles run the same NetBox, configured the same way, apart from
// netbox-branching and its worker.
func TestComposeFile(t *testing.T) {
	t.Parallel()
	st, plans, err := parseStack(composeFile, buildContexts)
	if err != nil {
		t.Fatalf("parseStack(compose.yaml): %v", err)
	}
	want := map[Profile]plan{
		Stock:     {order: []string{"postgres", "valkey", "netbox"}, web: "netbox"},
		Branching: {order: []string{"postgres", "valkey", "netbox-branching", workerService}, web: "netbox-branching"},
	}
	if diff := cmp.Diff(want, plans, cmp.AllowUnexported(plan{})); diff != "" {
		t.Errorf("profiles (-want +got):\n%s", diff)
	}

	// Moving to another NetBox release is a decision (docs/adr/0003), not an
	// image update: Version and the Makefile's NETBOX_VERSION move with it.
	stock := st.Services["netbox"]
	if want := "docker.io/netboxcommunity/netbox:v" + Version + "-"; !strings.HasPrefix(stock.Image, want) {
		t.Errorf("NetBox image = %q, want %s<netbox-docker version>@sha256:<digest>, to match netboxtest.Version", stock.Image, want)
	}
	if diff := cmp.Diff([]string{"8080/tcp"}, stock.containerPorts()); diff != "" {
		t.Errorf("NetBox container ports (-want +got):\n%s", diff)
	}

	username, password, err := Superuser()
	if err != nil || username == "" || password == "" {
		t.Errorf("Superuser() = %q, %q, %v; want a user name and password", username, password, err)
	}

	// netbox-docker's default, "*", would let a page that rebinds its own name
	// to 127.0.0.1 reach NetBox from a browser.
	if hosts := stock.Environment["ALLOWED_HOSTS"]; hosts == "" || strings.Contains(hosts, "*") {
		t.Errorf("NetBox's ALLOWED_HOSTS = %q, want only names for this machine", hosts)
	}

	// The branching image is the stock one with netbox-branching added, so the
	// stock NetBox's image update must move its base too.
	branching, worker := st.Services["netbox-branching"], st.Services[workerService]
	for name, s := range map[string]service{"netbox-branching": branching, workerService: worker} {
		if s.Build == nil || s.Build.Context != "branching" {
			t.Errorf("%s doesn't build from branching/", name)
			continue
		}
		if base, err := dockerfileBase(buildContexts, s.Build.Context); err != nil || base != stock.Image {
			t.Errorf("%s builds on %q, %v; want the stock netbox service's image, %s", name, base, err, stock.Image)
		}
		// Compose files can't share settings without anchors, which netboxtest
		// doesn't read, so the file repeats them.
		if diff := cmp.Diff(stock.Environment, s.Environment); diff != "" {
			t.Errorf("%s's environment differs from netbox's (-netbox +%s):\n%s", name, name, diff)
		}
		if s.User != stock.User {
			t.Errorf("%s runs as %q, netbox as %q", name, s.User, stock.User)
		}
	}
	if diff := cmp.Diff(stock.Ports, branching.Ports); diff != "" {
		t.Errorf("netbox-branching's ports differ from netbox's (-netbox +netbox-branching):\n%s", diff)
	}
	if diff := cmp.Diff(stock.Healthcheck, branching.Healthcheck); diff != "" {
		t.Errorf("netbox-branching's health check differs from netbox's (-netbox +netbox-branching):\n%s", diff)
	}
	if len(worker.Command) == 0 || worker.Command[len(worker.Command)-1] != "rqworker" {
		t.Errorf("%s runs %q, not NetBox's rqworker", workerService, worker.Command)
	}
}

// TestBuildContexts checks that netboxtest builds from the same files as
// Compose, which sends a build context's whole directory to Docker, and
// that every embedded directory is a build context compose.yaml uses.
// Embedding keeps neither file modes nor .dockerignore's meaning, so the
// directories may hold neither executable files nor a .dockerignore.
func TestBuildContexts(t *testing.T) {
	t.Parallel()
	st, _, err := parseStack(composeFile, buildContexts)
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	for _, s := range st.Services {
		if s.Build != nil {
			used[s.Build.Context] = true
		}
	}
	dirs, err := fs.ReadDir(buildContexts, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if !used[d.Name()] {
			t.Errorf("netboxtest embeds %s/, which no service in compose.yaml builds from", d.Name())
		}
		embedded, err := fs.Sub(buildContexts, d.Name())
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(readFiles(t, os.DirFS(d.Name())), readFiles(t, embedded)); diff != "" {
			t.Errorf("%s/: the embedded files differ from those on disk (-disk +embedded):\n%s", d.Name(), diff)
		}
		for _, err := range checkContextFiles(os.DirFS(d.Name())) {
			t.Errorf("%s/: %v", d.Name(), err)
		}
	}
}

// checkContextFiles refuses what netboxtest's build would treat differently
// from Compose's: an executable file, which writeContext writes with mode
// 0644, and a .dockerignore, which writeContext copies like any other file.
// The Dockerfile sets the modes of what it copies, which otherwise follow the
// checkout's under Compose.
func checkContextFiles(fsys fs.FS) []error {
	var errs []error
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch base := path.Base(name); {
		case base == ".dockerignore" || strings.HasSuffix(base, ".dockerignore"):
			errs = append(errs, fmt.Errorf("%s: netboxtest sends every file, so a .dockerignore would make Compose build from other files", name))
		case info.Mode()&0o111 != 0:
			errs = append(errs, fmt.Errorf("%s is executable, which netboxtest's build wouldn't keep; run it through its interpreter instead", name))
		}
		return nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	return errs
}

func TestCheckContextFiles(t *testing.T) {
	t.Parallel()
	ok := fstest.MapFS{"Dockerfile": {Data: []byte("FROM x\n"), Mode: 0o644}, "sub/plugins.py": {Mode: 0o600}}
	if errs := checkContextFiles(ok); len(errs) != 0 {
		t.Errorf("checkContextFiles(ok) = %v", errs)
	}
	bad := fstest.MapFS{
		"Dockerfile":              {Data: []byte("FROM x\n")},
		"setup.sh":                {Mode: 0o755},
		".dockerignore":           {Data: []byte("*.md\n")},
		"Dockerfile.dockerignore": {},
	}
	var got []string
	for _, err := range checkContextFiles(bad) {
		got = append(got, err.Error())
	}
	want := []string{
		".dockerignore: netboxtest sends every file, so a .dockerignore would make Compose build from other files",
		"Dockerfile.dockerignore: netboxtest sends every file, so a .dockerignore would make Compose build from other files",
		"setup.sh is executable, which netboxtest's build wouldn't keep; run it through its interpreter instead",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("checkContextFiles(bad) (-want +got):\n%s", diff)
	}
}

// readFiles returns every file under fsys, by path, with its content.
func readFiles(t *testing.T, fsys fs.FS) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, name)
		files[name] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestBranchingRequirements checks that the branching image installs
// netbox-branching at BranchingVersion, pinned by a hash, and nothing else.
func TestBranchingRequirements(t *testing.T) {
	t.Parallel()
	data, err := fs.ReadFile(buildContexts, "branching/requirements.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, err := branchingPin(string(data))
	if err != nil {
		t.Fatalf("branching/requirements.txt: %v", err)
	}
	if got != BranchingVersion {
		t.Errorf("branching/requirements.txt pins netbox-branching %s, want %s, to match netboxtest.BranchingVersion", got, BranchingVersion)
	}
}

// requirementPin is the one requirement branching/requirements.txt may hold:
// netbox-branching at one version, with a hash for each of the release's
// files that it accepts, as Dependabot writes them. The Dockerfile installs
// the wheel only.
var requirementPin = regexp.MustCompile(`^netboxlabs-netbox-branching==(\S+)( --hash=sha256:[0-9a-f]{64})+$`)

// branchingPin returns the version of netbox-branching that a requirements
// file pins, and fails unless it pins that, by hash, and nothing else.
func branchingPin(file string) (string, error) {
	var requirements []string
	for _, line := range strings.Split(strings.ReplaceAll(file, "\\\n", " "), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			requirements = append(requirements, strings.Join(strings.Fields(line), " "))
		}
	}
	if len(requirements) != 1 || !requirementPin.MatchString(requirements[0]) {
		return "", fmt.Errorf("holds %q, want one requirement: netboxlabs-netbox-branching==<version> --hash=sha256:<hash>", requirements)
	}
	return requirementPin.FindStringSubmatch(requirements[0])[1], nil
}

func TestBranchingPin(t *testing.T) {
	t.Parallel()
	const (
		h1 = "--hash=sha256:774a7feeb21b4f42950b99c8b6107108487ae7341154c0950224e8b66d3f2c17"
		h2 = "--hash=sha256:103c07148e8894cbaaa7d7f622d863c4369de0bb1fdb2c55d821169bb372b735"
	)
	tests := []struct {
		name, file, want string
	}{
		{name: "one hash", file: "# a comment\nnetboxlabs-netbox-branching==1.2.4 \\\n    " + h1 + "\n", want: "1.2.4"},
		{name: "a hash for each file, as Dependabot writes", file: "netboxlabs-netbox-branching==1.2.5 \\\n    " + h1 + " \\\n    " + h2 + "\n", want: "1.2.5"},
		{name: "no hash", file: "netboxlabs-netbox-branching==1.2.4\n"},
		{name: "a range", file: "netboxlabs-netbox-branching>=1.2.4 " + h1 + "\n"},
		{name: "another package", file: "netboxlabs-netbox-branching==1.2.4 " + h1 + "\nrequests==2.0 " + h2 + "\n"},
		{name: "a short hash", file: "netboxlabs-netbox-branching==1.2.4 --hash=sha256:774a\n"},
		{name: "nothing", file: "# empty\n"},
	}
	for _, tt := range tests {
		got, err := branchingPin(tt.file)
		switch {
		case tt.want == "" && err == nil:
			t.Errorf("%s: branchingPin = %q, want an error", tt.name, got)
		case tt.want != "" && (err != nil || got != tt.want):
			t.Errorf("%s: branchingPin = %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}
}

func TestProfileFromEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value   string
		set     bool
		want    Profile
		wantErr string
	}{
		{want: Stock},
		{set: true, want: Stock},
		{value: "stock", set: true, want: Stock},
		{value: "branching", set: true, want: Branching},
		{value: "Branching", set: true, wantErr: `NETBOXTEST_PROFILE="Branching" isn't a profile netboxtest runs; use one of stock, branching`},
		{value: "netbox", set: true, wantErr: "isn't a profile"},
	}
	for _, tt := range tests {
		got, err := profileFromEnv(func(name string) (string, bool) {
			if name != ProfileVariable {
				t.Errorf("looked up %s, want %s", name, ProfileVariable)
			}
			return tt.value, tt.set
		})
		switch {
		case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
			t.Errorf("profileFromEnv(%q, %v) = %q, %v; want an error containing %q", tt.value, tt.set, got, err, tt.wantErr)
		case tt.wantErr == "" && (err != nil || got != tt.want):
			t.Errorf("profileFromEnv(%q, %v) = %q, %v; want %q", tt.value, tt.set, got, err, tt.want)
		}
	}
}

// TestMakefileNetBoxVersion checks that `make reference` fetches the source
// of the NetBox release the tests run.
func TestMakefileNetBoxVersion(t *testing.T) {
	t.Parallel()
	makefile, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^NETBOX_VERSION \?= (\S+)$`).FindSubmatch(makefile)
	if m == nil {
		t.Fatal("the Makefile doesn't set NETBOX_VERSION ?= <version>")
	}
	if got := string(m[1]); got != Version {
		t.Errorf("the Makefile's NETBOX_VERSION = %s, want %s, to match netboxtest.Version", got, Version)
	}
}

// validStack is a small stack that parseStack accepts. Each case in
// TestParseStackRejects breaks one thing in it.
const validStack = `
name: example
services:
  db:
    image: example.com/db:1.0@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    environment:
      DB_PASSWORD: insecure-test-only
    command: ["db", "--fast"]
    expose: ["5432"]
    healthcheck:
      test: ["CMD", "db-ready"]
      start_period: 1m
      start_interval: 1s
      interval: 30s
      timeout: 5s
      retries: 3
  netbox:
    image: example.com/netbox:v4.7.2-1.0.0@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    user: "netbox:root"
    depends_on:
      db:
        condition: service_healthy
    environment:
      SUPERUSER_NAME: admin
      SUPERUSER_PASSWORD: insecure-test-only
    ports:
      - "127.0.0.1:${NETBOX_PORT:-8000}:8080"
    healthcheck:
      test: ["CMD-SHELL", "curl -f http://localhost:8080/login/"]
      start_period: 15m
      start_interval: 2s
      interval: 30s
      timeout: 5s
      retries: 3
`

// validDockerfile is a Dockerfile that dockerfileBase accepts, in testContexts.
const validDockerfile = `# A comment.
FROM example.com/netbox:v4.7.2-1.0.0@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef

COPY requirements.txt /opt/requirements.txt
RUN ["pip", "install", \\
     "--require-hashes", "-r", "/opt/requirements.txt"]
`

// testContexts holds the build contexts the stacks in these tests build from.
var testContexts = fstest.MapFS{
	"web/Dockerfile":       {Data: []byte(validDockerfile)},
	"web/requirements.txt": {Data: []byte("example==1.0\n")},
}

func TestParseStackAcceptsValidStack(t *testing.T) {
	t.Parallel()
	st, plans, err := parseStack([]byte(validStack), testContexts)
	if err != nil {
		t.Fatalf("parseStack: %v", err)
	}
	// Services in no profile run in every profile.
	for _, p := range profiles {
		if diff := cmp.Diff(plan{order: []string{"db", "netbox"}, web: "netbox"}, plans[p], cmp.AllowUnexported(plan{})); diff != "" {
			t.Errorf("profile %s (-want +got):\n%s", p, diff)
		}
	}
	db := st.Services["db"]
	if diff := cmp.Diff([]string{"5432/tcp"}, db.containerPorts()); diff != "" {
		t.Errorf("db container ports (-want +got):\n%s", diff)
	}
	// 1m start period, then 3 failures 35s apart.
	if got, want := db.Healthcheck.readiness(), time.Minute+3*35*time.Second; got != want {
		t.Errorf("db readiness = %v, want %v", got, want)
	}
}

// profileStack is a stack with a NetBox for each profile, one built from a
// Dockerfile, and a worker.
const profileStack = `
services:
  db:
    image: example.com/db:1.0@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    expose: ["5432"]
    healthcheck: {test: ["CMD", "db-ready"], start_period: 1m, start_interval: 1s, interval: 30s, timeout: 5s, retries: 3}
  netbox:
    profiles: ["stock"]
    image: example.com/netbox:v4.7.2-1.0.0@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    depends_on: {db: {condition: service_healthy}}
    environment: {SUPERUSER_NAME: admin, SUPERUSER_PASSWORD: insecure-test-only}
    ports: ["127.0.0.1:${NETBOX_PORT:-8000}:8080"]
    healthcheck: {test: ["CMD", "ready"], start_period: 1m, start_interval: 1s, interval: 30s, timeout: 5s, retries: 3}
  built:
    profiles: ["branching"]
    build: {context: web}
    depends_on: {db: {condition: service_healthy}}
    environment: {SUPERUSER_NAME: admin, SUPERUSER_PASSWORD: insecure-test-only}
    ports: ["127.0.0.1:${NETBOX_PORT:-8000}:8080"]
    healthcheck: {test: ["CMD", "ready"], start_period: 1m, start_interval: 1s, interval: 30s, timeout: 5s, retries: 3}
  worker:
    profiles: ["branching"]
    build: {context: web}
    command: ["rqworker"]
    depends_on: {built: {condition: service_healthy}}
    expose: ["9999"]
    healthcheck: {test: ["CMD", "ready"], start_period: 1m, start_interval: 1s, interval: 30s, timeout: 5s, retries: 3}
`

func TestParseStackAcceptsProfiles(t *testing.T) {
	t.Parallel()
	_, plans, err := parseStack([]byte(profileStack), testContexts)
	if err != nil {
		t.Fatalf("parseStack: %v", err)
	}
	want := map[Profile]plan{
		Stock:     {order: []string{"db", "netbox"}, web: "netbox"},
		Branching: {order: []string{"db", "built", "worker"}, web: "built"},
	}
	if diff := cmp.Diff(want, plans, cmp.AllowUnexported(plan{})); diff != "" {
		t.Errorf("profiles (-want +got):\n%s", diff)
	}
}

func TestParseStackRejectsProfiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		old, new string // replaced once in profileStack
		want     string // in the error
	}{
		{
			name: "unknown profile",
			old:  `profiles: ["stock"]`, new: `profiles: ["plain"]`,
			want: `service netbox: profile "plain" isn't one netboxtest runs (stock, branching)`,
		},
		{
			name: "two profiles",
			old:  `profiles: ["stock"]`, new: `profiles: ["stock", "branching"]`,
			want: "service netbox: is in 2 profiles",
		},
		{
			name: "profile with no NetBox",
			old:  "  netbox:\n    profiles: [\"stock\"]", new: "  netbox:\n    profiles: [\"branching\"]",
			want: "profile stock: needs exactly one service that publishes ports, for NetBox's web server, not 0",
		},
		{
			name: "profiles with two NetBoxes",
			old:  `expose: ["5432"]`, new: `ports: ["127.0.0.1::5432"]`,
			want: "profile stock: needs exactly one service that publishes ports, for NetBox's web server, not 2 (db, netbox)",
		},
		{
			name: "dependency in another profile",
			old:  "depends_on: {built: {condition: service_healthy}}", new: "depends_on: {netbox: {condition: service_healthy}}",
			want: "service worker: depends on netbox, which isn't in every profile it runs in",
		},
		{
			name: "dependency of a shared service in a profile",
			old:  "    expose: [\"5432\"]\n", new: "    expose: [\"5432\"]\n    depends_on: {netbox: {condition: service_healthy}}\n",
			want: "service db: depends on netbox, which isn't in every profile it runs in",
		},
		{
			name: "image and build",
			old:  "    build: {context: web}\n    command", new: "    build: {context: web}\n    image: example.com/x:1@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n    command",
			want: "service worker: has both image and build",
		},
		{
			name: "build option netboxtest doesn't run",
			old:  "build: {context: web}\n    command", new: "build: {context: web, args: {X: y}}\n    command",
			want: "field args not found",
		},
		{
			name: "build context that isn't embedded",
			old:  "build: {context: web}\n    command", new: "build: {context: other}\n    command",
			want: "service worker: build: context other: read its Dockerfile",
		},
		{
			name: "build context outside the directory",
			old:  "build: {context: web}\n    command", new: "build: {context: ../web}\n    command",
			want: `context "../web" must be a directory beside compose.yaml`,
		},
		{
			// testcontainers-go would publish the image's own ports on every interface.
			name: "an empty expose",
			old:  `expose: ["9999"]`, new: `expose: []`,
			want: "service worker: must list its container ports under expose or ports",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(profileStack, tt.old) {
				t.Fatalf("profileStack doesn't contain %q", tt.old)
			}
			_, _, err := parseStack([]byte(strings.Replace(profileStack, tt.old, tt.new, 1)), testContexts)
			if err == nil {
				t.Fatalf("parseStack succeeded, want an error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("parseStack error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestDockerfileBase(t *testing.T) {
	t.Parallel()
	const pinned = "example.com/netbox:v4.7.2-1.0.0@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	base, err := dockerfileBase(testContexts, "web")
	if err != nil || base != pinned {
		t.Errorf("dockerfileBase(valid) = %q, %v; want %q", base, err, pinned)
	}

	tests := []struct {
		name       string
		dockerfile string
		want       string
	}{
		{name: "lower case instructions", dockerfile: "from " + pinned + "\nrun true\n"},
		{name: "no FROM", dockerfile: "# nothing\n", want: "has no FROM"},
		{name: "FROM without a digest", dockerfile: "FROM example.com/netbox:v4.7.2\n", want: "line 1: FROM must name one image, with a tag and a pinned digest"},
		{name: "FROM with a stage name", dockerfile: "FROM " + pinned + " AS base\n", want: "FROM must name one image"},
		{name: "FROM with a platform", dockerfile: "FROM --platform=linux/amd64 " + pinned + "\n", want: "FROM must name one image"},
		{name: "two FROMs", dockerfile: "FROM " + pinned + "\nRUN true\nFROM " + pinned + "\n", want: "line 3: a second FROM"},
		{name: "RUN before FROM", dockerfile: "RUN true\nFROM " + pinned + "\n", want: "line 1: FROM must come first"},
		{name: "ARG", dockerfile: "ARG BASE=x\nFROM " + pinned + "\n", want: "line 1: ARG isn't allowed"},
		{name: "ADD", dockerfile: "FROM " + pinned + "\nADD https://example.com/x /x\n", want: "line 2: ADD isn't allowed"},
		{name: "ENV", dockerfile: "FROM " + pinned + "\nENV X=y\n", want: "line 2: ENV isn't allowed"},
		{name: "syntax directive", dockerfile: "# syntax=docker/dockerfile:1\nFROM " + pinned + "\n", want: "line 1: parser directives aren't allowed"},
		{name: "escape directive", dockerfile: "#escape=`\nFROM " + pinned + "\n", want: "parser directives aren't allowed"},
		{name: "RUN --mount", dockerfile: "FROM " + pinned + "\nRUN --mount=type=cache,target=/root/.cache pip install x\n", want: "line 2: RUN --mount=type=cache,target=/root/.cache isn't allowed"},
		{name: "COPY --from", dockerfile: "FROM " + pinned + "\nCOPY --from=example.com/other:1 /x /x\n", want: "COPY --from=example.com/other:1 isn't allowed"},
		{name: "a flag after a line continuation", dockerfile: "FROM " + pinned + "\nRUN \\\n  --mount=type=cache x\n", want: "line 2: RUN --mount=type=cache isn't allowed"},
		// Docker skips the blank line, so --mount is RUN's flag, not a new instruction.
		{name: "blank line within an instruction", dockerfile: "FROM " + pinned + "\nRUN \\\n\n  --mount=x true\n", want: "line 2: RUN --mount=x isn't allowed"},
		{name: "RUN here-document", dockerfile: "FROM " + pinned + "\nRUN <<EOF\npip install x\nEOF\n", want: "line 2: here-documents (<<) aren't allowed"},
		{name: "here-document after a command", dockerfile: "FROM " + pinned + "\nRUN cat <<-EOF > /x\n", want: "here-documents (<<) aren't allowed"},
		{name: "COPY here-document", dockerfile: "FROM " + pinned + "\nCOPY <<EOF /x\n", want: "here-documents (<<) aren't allowed"},
		{name: "empty RUN", dockerfile: "FROM " + pinned + "\nRUN\n", want: "line 2: RUN has nothing to do"},
		{name: "ends in a line continuation", dockerfile: "FROM " + pinned + "\nRUN true \\\n", want: "ends in the middle of an instruction"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := dockerfileBase(fstest.MapFS{"web/Dockerfile": {Data: []byte(tt.dockerfile)}}, "web")
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("dockerfileBase: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Errorf("dockerfileBase error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
	for _, context := range []string{"Web", "web/sub", "", ".", "-web"} {
		if _, err := dockerfileBase(testContexts, context); err == nil || !strings.Contains(err.Error(), "must be a directory beside compose.yaml") {
			t.Errorf("dockerfileBase(context %q) = %v, want it refused", context, err)
		}
	}
}

func TestParseStackRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		old, new string // replaced once in validStack
		want     string // in the error
	}{
		{
			name: "unknown top-level key",
			old:  "name: example\n", new: "name: example\nvolumes: {data: {}}\n",
			want: "field volumes not found",
		},
		{
			name: "unknown service key",
			old:  `    user: "netbox:root"`, new: `    user: "netbox:root"` + "\n    restart: always",
			want: "field restart not found",
		},
		{
			name: "command as a string",
			old:  `command: ["db", "--fast"]`, new: `command: db --fast`,
			want: "cannot unmarshal",
		},
		{
			name: "environment as a list",
			old:  "    environment:\n      DB_PASSWORD: insecure-test-only", new: "    environment:\n      - DB_PASSWORD=insecure-test-only",
			want: "cannot unmarshal",
		},
		{
			name: "image without a digest",
			old:  "example.com/db:1.0@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", new: "example.com/db:1.0",
			want: `image "example.com/db:1.0" must name a tag and pin a digest`,
		},
		{
			name: "image without a tag",
			old:  "example.com/db:1.0@", new: "example.com/db@",
			want: "must name a tag and pin a digest",
		},
		{
			name: "image with a registry port but no tag",
			old:  "example.com/db:1.0@", new: "localhost:5000/db@",
			want: "must name a tag and pin a digest",
		},
		{
			name: "digest that isn't hex",
			old:  "db:1.0@sha256:0123456789abcdef", new: "db:1.0@sha256:0123456789abcdeg",
			want: "must name a tag and pin a digest",
		},
		{
			name: "variable in an environment value",
			old:  "DB_PASSWORD: insecure-test-only", new: "DB_PASSWORD: ${DB_PASSWORD}",
			want: "environment DB_PASSWORD uses $",
		},
		{
			name: "empty environment value",
			old:  "DB_PASSWORD: insecure-test-only", new: "DB_PASSWORD:",
			want: "environment DB_PASSWORD is empty",
		},
		{
			name: "variable in a command",
			old:  `"--fast"`, new: `"$FLAGS"`,
			want: `"$FLAGS" uses $`,
		},
		{
			name: "variable in a health check",
			old:  `"db-ready"]`, new: `"db-ready", "$HOST"]`,
			want: "healthcheck test uses $",
		},
		{
			name: "dependency that isn't a service",
			old:  "      db:\n        condition", new: "      cache:\n        condition",
			want: "depends on cache, which isn't a service",
		},
		{
			name: "dependency that needn't be healthy",
			old:  "condition: service_healthy", new: "condition: service_started",
			want: "depends_on db needs condition: service_healthy",
		},
		{
			name: "no health check",
			old:  "    expose: [\"5432\"]\n    healthcheck:\n      test: [\"CMD\", \"db-ready\"]\n      start_period: 1m\n      start_interval: 1s\n      interval: 30s\n      timeout: 5s\n      retries: 3\n", new: "    expose: [\"5432\"]\n",
			want: "service db: needs a healthcheck",
		},
		{
			name: "dependency without a health check",
			old:  "    expose: [\"5432\"]\n    healthcheck:\n      test: [\"CMD\", \"db-ready\"]\n      start_period: 1m\n      start_interval: 1s\n      interval: 30s\n      timeout: 5s\n      retries: 3\n", new: "    expose: [\"5432\"]\n",
			want: "depends on db, which has no healthcheck",
		},
		{
			name: "health check that isn't CMD or CMD-SHELL",
			old:  `["CMD", "db-ready"]`, new: `["db-ready"]`,
			want: "healthcheck test must be a list starting with CMD or CMD-SHELL",
		},
		{
			name: "health check without retries",
			old:  "      retries: 3\n  netbox:", new: "  netbox:",
			want: "healthcheck needs start_period, start_interval, interval, timeout and retries",
		},
		{
			name: "duration Go can't parse",
			old:  "start_period: 1m", new: "start_period: a minute",
			want: "invalid duration",
		},
		{
			name: "zero duration",
			old:  "start_period: 1m", new: "start_period: 0s",
			want: `duration "0s" must be positive`,
		},
		{
			name: "service with no ports listed",
			old:  "    expose: [\"5432\"]\n", new: "",
			want: "service db: must list its container ports under expose or ports",
		},
		{
			name: "exposed port that isn't a number",
			old:  `expose: ["5432"]`, new: `expose: ["postgres"]`,
			want: `expose "postgres": "postgres" isn't a TCP port number`,
		},
		{
			name: "port published on every interface",
			old:  `"127.0.0.1:${NETBOX_PORT:-8000}:8080"`, new: `"0.0.0.0:8000:8080"`,
			want: "must publish on 127.0.0.1 only",
		},
		{
			name: "port published without a host address",
			old:  `"127.0.0.1:${NETBOX_PORT:-8000}:8080"`, new: `"8000:8080"`,
			want: "must be 127.0.0.1:HOST_PORT:CONTAINER_PORT",
		},
		{
			name: "port published on IPv6 loopback",
			old:  `"127.0.0.1:${NETBOX_PORT:-8000}:8080"`, new: `"[::1]:8000:8080"`,
			want: "must publish on 127.0.0.1 only",
		},
		{
			name: "published port without a container port",
			old:  `"127.0.0.1:${NETBOX_PORT:-8000}:8080"`, new: `"127.0.0.1:8080"`,
			want: "must be 127.0.0.1:HOST_PORT:CONTAINER_PORT",
		},
		{
			name: "NetBox without a published port",
			old:  "    ports:\n      - \"127.0.0.1:${NETBOX_PORT:-8000}:8080\"\n", new: "    expose: [\"8080\"]\n",
			want: "profile stock: needs exactly one service that publishes ports, for NetBox's web server, not 0",
		},
		{
			name: "NetBox publishing two ports",
			old:  "      - \"127.0.0.1:${NETBOX_PORT:-8000}:8080\"\n", new: "      - \"127.0.0.1:${NETBOX_PORT:-8000}:8080\"\n      - \"127.0.0.1::8081\"\n",
			want: "service netbox: needs exactly one entry in ports",
		},
		{
			name: "NetBox without a superuser password",
			old:  "      SUPERUSER_PASSWORD: insecure-test-only\n", new: "",
			want: "environment needs SUPERUSER_PASSWORD",
		},
		{
			name: "no services",
			old:  validStack, new: "name: example\nservices: {}\n",
			want: "no services",
		},
		{
			name: "dependency cycle",
			old:  "    command:", new: "    depends_on:\n      netbox:\n        condition: service_healthy\n    command:",
			want: "services db, netbox depend on each other in a cycle",
		},
		{
			name: "port published on another loopback address",
			old:  `"127.0.0.1:${NETBOX_PORT:-8000}:8080"`, new: `"127.0.0.2:8000:8080"`,
			want: "must publish on 127.0.0.1 only",
		},
		{
			name: "host port that is neither a number nor a variable with a default",
			old:  `"127.0.0.1:${NETBOX_PORT:-8000}:8080"`, new: `"127.0.0.1:${NETBOX_PORT}:8080"`,
			want: `host port "${NETBOX_PORT}" must be a number, empty, or ${VARIABLE:-default}`,
		},
		{
			name: "number that YAML and Compose read differently",
			old:  "DB_PASSWORD: insecure-test-only", new: "DB_PASSWORD: insecure-test-only\n      DB_PORT: 05432",
			want: `DB_PORT "05432" is a YAML int, not a str; quote it`,
		},
		{
			name: "boolean that YAML and Compose read differently",
			old:  "DB_PASSWORD: insecure-test-only", new: "DB_PASSWORD: insecure-test-only\n      DEBUG: True",
			want: `DEBUG "True" is a YAML bool, not a str; quote it`,
		},
		{
			name: "retries as a string",
			old:  "      retries: 3\n  netbox:", new: "      retries: \"3\"\n  netbox:",
			want: `retries "3" is a YAML str, not a int`,
		},
		{
			name: "alias",
			old:  "      DB_PASSWORD: insecure-test-only", new: "      DB_PASSWORD: &pw insecure-test-only\n      OTHER_PASSWORD: *pw",
			want: "aliases aren't supported",
		},
		{
			name: "second YAML document that doesn't parse",
			old:  validStack, new: validStack + "---\nname: [\n",
			want: "after the first YAML document",
		},
		{
			name: "second YAML document",
			old:  validStack, new: validStack + "---\nname: other\n",
			want: "exactly one YAML document",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(validStack, tt.old) {
				t.Fatalf("validStack doesn't contain %q", tt.old)
			}
			doc := strings.Replace(validStack, tt.old, tt.new, 1)
			_, _, err := parseStack([]byte(doc), testContexts)
			if err == nil {
				t.Fatalf("parseStack succeeded, want an error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("parseStack error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestContainerPort(t *testing.T) {
	t.Parallel()
	for mapping, want := range map[string]int{
		"127.0.0.1:${NETBOX_PORT:-8000}:8080": 8080,
		"127.0.0.1:8000:8080":                 8080,
		"127.0.0.1:0:6379":                    6379, // Docker chooses the host port
		"127.0.0.1::6379":                     6379, // so does Compose
	} {
		if got, err := containerPort(mapping); err != nil || got != want {
			t.Errorf("containerPort(%q) = %d, %v; want %d", mapping, got, err, want)
		}
	}
}

func TestStartOrder(t *testing.T) {
	t.Parallel()
	needs := func(deps ...string) service {
		s := service{DependsOn: map[string]dependency{}}
		for _, d := range deps {
			s.DependsOn[d] = dependency{Condition: "service_healthy"}
		}
		return s
	}
	got, err := startOrder(map[string]service{
		"web":    needs("db", "cache"),
		"worker": needs("web"),
		"db":     needs(),
		"cache":  needs(),
	})
	if err != nil {
		t.Fatalf("startOrder: %v", err)
	}
	if diff := cmp.Diff([]string{"cache", "db", "web", "worker"}, got); diff != "" {
		t.Errorf("startOrder (-want +got):\n%s", diff)
	}
}
