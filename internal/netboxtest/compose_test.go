package netboxtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// TestComposeFile checks the real compose.yaml: that netboxtest can run it,
// and that it runs the NetBox release the project targets.
func TestComposeFile(t *testing.T) {
	t.Parallel()
	st, order, err := parseStack(composeFile)
	if err != nil {
		t.Fatalf("parseStack(compose.yaml): %v", err)
	}
	if diff := cmp.Diff([]string{"postgres", "valkey", netboxService}, order); diff != "" {
		t.Errorf("start order (-want +got):\n%s", diff)
	}

	// Moving to another NetBox release is a decision (docs/adr/0003), not an
	// image update: Version and the Makefile's NETBOX_VERSION move with it.
	image := st.Services[netboxService].Image
	if want := "docker.io/netboxcommunity/netbox:v" + Version + "-"; !strings.HasPrefix(image, want) {
		t.Errorf("NetBox image = %q, want %s<netbox-docker version>@sha256:<digest>, to match netboxtest.Version", image, want)
	}
	if diff := cmp.Diff([]string{"8080/tcp"}, st.Services[netboxService].containerPorts()); diff != "" {
		t.Errorf("NetBox container ports (-want +got):\n%s", diff)
	}

	username, password, err := Superuser()
	if err != nil || username == "" || password == "" {
		t.Errorf("Superuser() = %q, %q, %v; want a user name and password", username, password, err)
	}

	// netbox-docker's default, "*", would let a page that rebinds its own name
	// to 127.0.0.1 reach NetBox from a browser.
	if hosts := st.Services[netboxService].Environment["ALLOWED_HOSTS"]; hosts == "" || strings.Contains(hosts, "*") {
		t.Errorf("NetBox's ALLOWED_HOSTS = %q, want only names for this machine", hosts)
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

func TestParseStackAcceptsValidStack(t *testing.T) {
	t.Parallel()
	st, order, err := parseStack([]byte(validStack))
	if err != nil {
		t.Fatalf("parseStack: %v", err)
	}
	if diff := cmp.Diff([]string{"db", "netbox"}, order); diff != "" {
		t.Errorf("start order (-want +got):\n%s", diff)
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
			want: "service netbox: needs exactly one entry in ports",
		},
		{
			name: "NetBox without a superuser password",
			old:  "      SUPERUSER_PASSWORD: insecure-test-only\n", new: "",
			want: "environment needs SUPERUSER_PASSWORD",
		},
		{
			name: "no NetBox service",
			old:  "  netbox:\n", new: "  web:\n",
			want: "no netbox service",
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
			_, _, err := parseStack([]byte(doc))
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

func TestLoopbackBindings(t *testing.T) {
	t.Parallel()
	got := loopbackBindings([]string{"8080/tcp", "5432/tcp"})
	if len(got) != 2 {
		t.Fatalf("loopbackBindings returned %d ports, want 2", len(got))
	}
	for port, bindings := range got {
		if len(bindings) != 1 || bindings[0].HostIP.String() != "127.0.0.1" || bindings[0].HostPort != "" {
			t.Errorf("port %v bindings = %+v, want one binding to 127.0.0.1 on a random port", port, bindings)
		}
	}
}
