package main

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
)

const branchesPath = "/api/plugins/branching/branches/"

// branchingNetBox is a fake NetBox with netbox-branching 1.2.4 and four
// branches: two ready, one being merged, and one merged, and the second
// ready one without an owner, a description or a last sync.
func branchingNetBox(t *testing.T, denied ...string) *compareNetBox {
	t.Helper()
	nb := newCompareNetBox(t, nil, nil, denied...)
	withBranches(nb)
	return nb
}

// withBranches gives a fake NetBox netbox-branching 1.2.4 and the branches
// branchingNetBox has.
func withBranches(nb *compareNetBox) {
	bare := nbBranch(3, "Bare", "ijkl9012", "ready")
	bare["owner"], bare["last_sync"], bare["description"] = nil, nil, ""
	nb.plugins = map[string]any{"netbox_branching": "1.2.4"}
	nb.branches = []map[string]any{
		nbBranch(1, "lab", "abcd1234", "ready"),
		nbBranch(2, "busy", "efgh5678", "merging"),
		bare,
		nbBranch(5, "done", "mnop3456", "merged"),
	}
}

// branchingEnv points TrueUp at nb with netbox.branching on, and the extra
// variables given.
func branchingEnv(t *testing.T, nb *compareNetBox, extra ...string) []string {
	t.Helper()
	return append(append(checkEnv(t, nb.srv, checkToken), "TRUEUP_NETBOX_BRANCHING=true"), extra...)
}

func TestNetBoxBranchesText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  []string
		args []string
		want string
	}{
		{
			name: "main protected",
			want: `Branching:  on, netbox-branching 1.2.4; main is protected (netbox.protect_main), so runs use branches only

TARGET  NAME  SCHEMA ID  OWNER  LAST SYNC             DESCRIPTION
branch  lab   abcd1234   alice  2026-11-26T23:21:32Z  branch lab
branch  Bare  ijkl9012   -      -                     -

2 other branches aren't ready: 1 merged, 1 merging. --all lists them.
`,
		},
		{
			name: "main not protected",
			env:  []string{"TRUEUP_NETBOX_PROTECT_MAIN=false"},
			want: `Branching:  on, netbox-branching 1.2.4; main isn't protected (netbox.protect_main), so runs may use main or a branch

TARGET  NAME  SCHEMA ID  OWNER  LAST SYNC             DESCRIPTION
main    -     -          -      -                     -
branch  lab   abcd1234   alice  2026-11-26T23:21:32Z  branch lab
branch  Bare  ijkl9012   -      -                     -

2 other branches aren't ready: 1 merged, 1 merging. --all lists them.
`,
		},
		{
			name: "every branch",
			args: []string{"--all"},
			want: `Branching:  on, netbox-branching 1.2.4; main is protected (netbox.protect_main), so runs use branches only

TARGET  NAME  SCHEMA ID  STATUS   OWNER  LAST SYNC             DESCRIPTION
branch  lab   abcd1234   ready    alice  2026-11-26T23:21:32Z  branch lab
-       busy  efgh5678   merging  alice  2026-11-26T23:21:32Z  branch busy
branch  Bare  ijkl9012   ready    -      -                     -
-       done  mnop3456   merged   alice  2026-11-26T23:21:32Z  branch done
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			nb := branchingNetBox(t)
			r := runCLI(t, nil, branchingEnv(t, nb, tt.env...), append([]string{"netbox", "branches"}, tt.args...)...)
			if r.code != exitOK || r.stderr != "" {
				t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
			}
			want := "NetBox:     " + nb.srv.URL + ", release 4.7.2\n" + tt.want
			if diff := cmp.Diff(want, r.stdout); diff != "" {
				t.Errorf("netbox branches (-want +got):\n%s", diff)
			}
			// The branches are read from main, without the header.
			if got := nb.branchesRead(branchesPath); !slices.Equal(got, []string{"main"}) {
				t.Errorf("the branches were read as %q, want once from main", got)
			}
		})
	}
}

func TestNetBoxBranchesJSON(t *testing.T) {
	t.Parallel()
	nb := branchingNetBox(t)
	r := runCLI(t, nil, branchingEnv(t, nb), "netbox", "branches", "-o", "json")
	if r.code != exitOK || r.stderr != "" {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	var got branchesJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("netbox branches -o json printed %q: %v", r.stdout, err)
	}
	version, alice := "1.2.4", "alice"
	synced := time.Date(2026, 11, 26, 23, 21, 32, 709369000, time.UTC)
	want := branchesJSON{
		URL: nb.srv.URL, NetBoxVersion: "4.7.2", Branching: true, BranchingVersion: &version, ProtectMain: true,
		Branches: []branchJSON{
			{ID: 1, Name: "lab", SchemaID: "abcd1234", Status: "ready", Target: true, Owner: &alice, Description: "branch lab", LastSync: &synced},
			{ID: 3, Name: "Bare", SchemaID: "ijkl9012", Status: "ready", Target: true},
		},
		NotReady: map[string]int{"merging": 1, "merged": 1},
		Warnings: []warningJSON{},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("netbox branches -o json (-want +got):\n%s", diff)
	}
	// The field names are public interface.
	for _, field := range []string{
		`"url"`, `"netbox_version"`, `"branching": true`, `"branching_version": "1.2.4"`, `"protect_main": true`, `"main": false`,
		`"branches"`, `"id"`, `"name"`, `"schema_id"`, `"status"`, `"target"`, `"owner": null`, `"description"`, `"last_sync": null`,
		`"not_ready"`, `"warnings": []`,
	} {
		if !strings.Contains(r.stdout, field) {
			t.Errorf("netbox branches -o json has no %s:\n%s", field, r.stdout)
		}
	}
}

// With branching off, main is the only target, and NetBox's branches aren't
// read: the token needn't view them.
func TestNetBoxBranchesWithBranchingOff(t *testing.T) {
	t.Parallel()
	nb := branchingNetBox(t, branchesPath)
	r := runCLI(t, nil, checkEnv(t, nb.srv, checkToken), "netbox", "branches")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	want := "NetBox:     " + nb.srv.URL + `, release 4.7.2
Branching:  off (netbox.branching), so main is the only target

TARGET  NAME  SCHEMA ID  OWNER  LAST SYNC  DESCRIPTION
main    -     -          -      -          -

Warnings:
  NetBox has netbox-branching 1.2.4, but netbox.branching is off, so TrueUp reads and writes main only: turn netbox.branching on to work in branches
`
	if diff := cmp.Diff(want, r.stdout); diff != "" {
		t.Errorf("netbox branches (-want +got):\n%s", diff)
	}
	if got := nb.branchesRead(branchesPath); len(got) != 0 {
		t.Errorf("the branches were read %d times, with branching off", len(got))
	}

	r = runCLI(t, nil, checkEnv(t, nb.srv, checkToken), "netbox", "branches", "-o", "json")
	var got branchesJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("netbox branches -o json printed %q: %v", r.stdout, err)
	}
	if !got.Main || got.Branching || len(got.Branches) != 0 || len(got.Warnings) != 1 || got.Warnings[0].Key != "netbox.branching" {
		t.Errorf("netbox branches -o json = %+v, want main alone, with the warning about netbox.branching", got)
	}
}

func TestNetBoxBranchesNoneReady(t *testing.T) {
	t.Parallel()
	nb := branchingNetBox(t)
	nb.branches = nb.branches[3:]
	r := runCLI(t, nil, branchingEnv(t, nb), "netbox", "branches")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	want := "NetBox:     " + nb.srv.URL + `, release 4.7.2
Branching:  on, netbox-branching 1.2.4; main is protected (netbox.protect_main), so runs use branches only

No branch is ready: create one in NetBox, or wait until one is.

1 other branch isn't ready: 1 merged. --all lists them.
`
	if diff := cmp.Diff(want, r.stdout); diff != "" {
		t.Errorf("netbox branches (-want +got):\n%s", diff)
	}
}

func TestNetBoxBranchesFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		nb         func(t *testing.T) *compareNetBox
		wantStderr string
	}{
		{
			name:       "no netbox-branching",
			nb:         func(t *testing.T) *compareNetBox { return newCompareNetBox(t, nil, nil) },
			wantStderr: "netbox.branching is on, but NetBox's status doesn't list its plugins, so TrueUp can't tell whether it has netbox-branching",
		},
		{
			name: "netbox-branching of another release",
			nb: func(t *testing.T) *compareNetBox {
				nb := branchingNetBox(t)
				nb.plugins["netbox_branching"] = "1.3.0"
				return nb
			},
			wantStderr: "netbox.branching is on, but NetBox has netbox-branching 1.3.0: TrueUp works with netbox-branching 1.2.x only",
		},
		{
			name: "a token that can't view branches",
			nb:   func(t *testing.T) *compareNetBox { return branchingNetBox(t, branchesPath) },
			wantStderr: "Error: list NetBox's branches: the token can't view NetBox's branches: grant its user the view permission on " +
				"netbox_branching.branch: GET /api/plugins/branching/branches/: NetBox answered 403 Forbidden",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := runCLI(t, nil, branchingEnv(t, tt.nb(t)), "netbox", "branches", "-o", "json")
			if r.code != exitFailure || r.stdout != "" || !strings.Contains(r.stderr, tt.wantStderr) {
				t.Errorf("exit code %d, stdout %q, stderr %q; want %d, nothing, and %q", r.code, r.stdout, r.stderr, exitFailure, tt.wantStderr)
			}
		})
	}
}

func TestNetBoxCheckWithBranching(t *testing.T) {
	t.Parallel()
	nb := branchingNetBox(t)
	r := runCLI(t, nil, branchingEnv(t, nb), "netbox", "check")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "Token:      v2, for user trueup\nBranching:  on, netbox-branching 1.2.4\n\nOBJECT TYPE") ||
		!strings.Contains(r.stdout, "\nnetbox_branching.branch         /api/plugins/branching/branches/          yes\n") {
		t.Errorf("netbox check doesn't show branching and the branches' row:\n%s", r.stdout)
	}

	// Through a branch: every object type is read in it, but the branches.
	nb = branchingNetBox(t)
	r = runCLI(t, nil, branchingEnv(t, nb), "netbox", "check", "--branch", " lab ", "-o", "json")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	var got checkJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("netbox check -o json printed %q: %v", r.stdout, err)
	}
	if !got.Branching || got.BranchingVersion == nil || *got.BranchingVersion != "1.2.4" ||
		got.Branch == nil || *got.Branch != (branchRefJSON{ID: 1, Name: "lab", SchemaID: "abcd1234"}) || !got.OK {
		t.Errorf("netbox check -o json = %+v", got)
	}
	for _, rt := range readTypes(true) {
		want := []string{"abcd1234"}
		if rt.Endpoint == branchesPath {
			want = []string{"main", "main"} // finding the branch, and checking the token may list them
		}
		if read := nb.branchesRead(rt.Endpoint); !slices.Equal(read, want) {
			t.Errorf("%s was read as %q, want %q", rt.Endpoint, read, want)
		}
	}
	for _, field := range []string{`"branching": true`, `"branching_version": "1.2.4"`, `"branch": {`, `"schema_id": "abcd1234"`} {
		if !strings.Contains(r.stdout, field) {
			t.Errorf("netbox check -o json has no %s:\n%s", field, r.stdout)
		}
	}

	r = runCLI(t, nil, branchingEnv(t, branchingNetBox(t)), "netbox", "check", "--branch", "lab")
	if !strings.Contains(r.stdout, "Branching:  on, netbox-branching 1.2.4\nBranch:     lab (abcd1234), ready\n") {
		t.Errorf("netbox check --branch doesn't name the branch:\n%s", r.stdout)
	}

	// With branching off, neither the branches nor their row.
	r = runCLI(t, nil, checkEnv(t, branchingNetBox(t).srv, checkToken), "netbox", "check", "-o", "json")
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("netbox check -o json printed %q: %v", r.stdout, err)
	}
	if got.Branching || got.Branch != nil || len(got.ObjectTypes) != len(readTypes(false)) || len(got.Warnings) != 1 || got.Warnings[0].Key != "netbox.branching" {
		t.Errorf("netbox check -o json with branching off = %+v", got)
	}
}

func TestNetBoxCheckCantViewBranches(t *testing.T) {
	t.Parallel()
	nb := branchingNetBox(t, branchesPath)
	r := runCLI(t, nil, branchingEnv(t, nb), "netbox", "check")
	want := "Error: the token can't read 1 of the 12 object types TrueUp reads: grant its user the view permission on netbox_branching.branch\n"
	if r.code != exitCannotRead || r.stderr != want {
		t.Errorf("exit code %d, stderr %q; want %d and %q", r.code, r.stderr, exitCannotRead, want)
	}
}

// --branch names a branch that must exist and be ready, and needs
// netbox.branching on, for netbox check and compare alike.
func TestBranchFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		branching  bool
		branch     string
		wantCode   int
		wantStderr string
	}{
		{"branching off", false, "lab", exitUsage, "Error: --branch reads through a NetBox branch, which needs netbox.branching on\n"},
		{"white space alone", true, " \t", exitUsage, "Error: --branch needs a branch's name, not \" \\t\"\n"},
		{
			"a branch NetBox hasn't got", true, "LAB", exitFailure,
			`Error: NetBox has no branch "LAB": 'netbox-trueup-ai netbox branches' lists those TrueUp can use` + "\n",
		},
		{
			"a branch that isn't ready", true, "busy", exitFailure,
			`Error: branch "busy" is merging, not ready: TrueUp reads only branches that are ready; 'netbox-trueup-ai netbox branches' lists those it can use` + "\n",
		},
	}
	for _, tt := range tests {
		for _, command := range []string{"netbox check", "compare"} {
			t.Run(command+", "+tt.name, func(t *testing.T) {
				t.Parallel()
				nb := libraryNetBox(t)
				withBranches(nb)
				env, dataDir, _, configure := compareEnv(t, nb)
				if tt.branching {
					env = append(env, "TRUEUP_NETBOX_BRANCHING=true")
				}
				args := append(strings.Fields(command), "--branch", tt.branch)
				r := runCLIApp(t, nil, configure, env, args...)
				if r.code != tt.wantCode || r.stdout != "" || !strings.HasPrefix(r.stderr, tt.wantStderr) {
					t.Errorf("exit code %d, stdout %q, stderr %q; want %d, nothing, and %q", r.code, r.stdout, r.stderr, tt.wantCode, tt.wantStderr)
				}
				for _, path := range []string{"/api/dcim/manufacturers/", "/api/dcim/device-types/"} {
					if read := nb.branchesRead(path); len(read) != 0 {
						t.Errorf("%s was read as %q", path, read)
					}
				}
				if recorded := recordedComparisons(t, dataDir); len(recorded) != 0 {
					t.Errorf("recorded %d comparisons", len(recorded))
				}
			})
		}
	}
}

func TestCompareInABranch(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	withBranches(nb)
	// The branch has Netgear and its GS105 only.
	nb.branchManufacturers, nb.branchDeviceTypes = nb.manufacturers[:1], nb.deviceTypes[:1]
	env, dataDir, _, configure := compareEnv(t, nb)
	env = append(env, "TRUEUP_NETBOX_BRANCHING=true")

	r := runCLIApp(t, nil, configure, env, "compare", "-o", "json")
	if r.code != exitOK {
		t.Fatalf("compare in main: exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	inMain := decodeCompare(t, r.stdout)
	if inMain.NetBox.Branch != nil || !strings.Contains(r.stdout, `"branch": null`) {
		t.Errorf("compare in main names branch %+v", inMain.NetBox.Branch)
	}
	mainRecorded := recordedComparisons(t, dataDir)

	r = runCLIApp(t, nil, configure, env, "compare", "--branch", "lab", "-o", "json")
	if r.code != exitOK {
		t.Fatalf("compare in the branch: exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	inBranch := decodeCompare(t, r.stdout)
	if b := inBranch.NetBox.Branch; b == nil || *b != (branchRefJSON{ID: 1, Name: "lab", SchemaID: "abcd1234"}) {
		t.Errorf("compare --branch names branch %+v", b)
	}
	if len(inBranch.Objects) >= len(inMain.Objects) || len(inBranch.Objects) == 0 {
		t.Errorf("compared %d objects in the branch and %d in main, want fewer in the branch, which has less", len(inBranch.Objects), len(inMain.Objects))
	}
	// Every read of NetBox's objects in the second run named the branch.
	for _, path := range []string{"/api/dcim/manufacturers/", "/api/dcim/device-types/", "/api/dcim/interface-templates/"} {
		read := nb.branchesRead(path)
		if len(read) < 2 || read[0] != "main" || read[len(read)-1] != "abcd1234" {
			t.Errorf("%s was read as %q", path, read)
		}
	}

	// Main's comparisons stay, and the branch's are recorded beside them.
	all := recordedComparisons(t, dataDir)
	branchCompared := 0
	for k, c := range all {
		if c.Branch == nil {
			if diff := cmp.Diff(mainRecorded[k], c); diff != "" {
				t.Errorf("main's comparison of %s changed (-before +after):\n%s", k, diff)
			}
			continue
		}
		branchCompared++
		if *c.Branch != (store.Branch{SchemaID: "abcd1234", Name: "lab", NetBoxID: 1}) || !strings.HasPrefix(k, "abcd1234:") {
			t.Errorf("%s was recorded in branch %+v", k, c.Branch)
		}
	}
	if len(all)-branchCompared != len(mainRecorded) || branchCompared != inBranch.Sources[0].Compared {
		t.Errorf("recorded %d comparisons in main and %d in the branch, want %d and %d",
			len(all)-branchCompared, branchCompared, len(mainRecorded), inBranch.Sources[0].Compared)
	}

	// The text says what was compared.
	r = runCLIApp(t, nil, configure, env, "compare")
	if !strings.HasPrefix(r.stdout, "NetBox:  "+nb.srv.URL+", release 4.7.2, main\n") {
		t.Errorf("compare in main with branching on doesn't say main:\n%s", r.stdout)
	}
	r = runCLIApp(t, nil, configure, env, "compare", "--branch", "lab")
	if !strings.HasPrefix(r.stdout, "NetBox:  "+nb.srv.URL+`, release 4.7.2, branch "lab" (abcd1234)`+"\n") {
		t.Errorf("compare --branch doesn't name the branch:\n%s", r.stdout)
	}
}

// A branch that stops being ready while compare reads it, as when a person
// starts merging it, ends the command, and records nothing.
func TestCompareWhenTheBranchStopsBeingReady(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	withBranches(nb)
	nb.branchManufacturers, nb.branchDeviceTypes = nb.manufacturers, nb.deviceTypes
	var once sync.Once
	nb.onManufacturers = func() { once.Do(func() { nb.setBranchStatus("abcd1234", "merging") }) }
	env, dataDir, _, configure := compareEnv(t, nb)
	env = append(env, "TRUEUP_NETBOX_BRANCHING=true")

	r := runCLIApp(t, nil, configure, env, "compare", "--branch", "lab")
	want := `NetBox refused branch "lab" (schema ID abcd1234): "Selected branch is not ready."; it was ready when TrueUp chose it, ` +
		"so someone may be synchronizing, merging or migrating it, or have deleted it: 'netbox-trueup-ai netbox branches --all' shows its status\n"
	if r.code != exitFailure || r.stdout != "" || !strings.HasSuffix(r.stderr, want) {
		t.Errorf("exit code %d, stdout %q, stderr %q; want %d, nothing, and %q", r.code, r.stdout, r.stderr, exitFailure, want)
	}
	if recorded := recordedComparisons(t, dataDir); len(recorded) != 0 {
		t.Errorf("recorded %d comparisons", len(recorded))
	}
}
