//go:build integration

package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/netboxtest"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/engine"
)

// branchTestFile is a device type of a manufacturer no other test creates,
// so that what NetBox holds of it is this test's alone.
const branchTestFile = "---\nmanufacturer: Branch Test Co\nmodel: BT-1\nslug: branch-test-co-bt-1\nu_height: 2\nis_full_depth: false\n"

// TestBranchesInNetBox runs netbox branches, netbox check and compare against
// the NetBox TestMain started: with netbox-branching, in a branch that holds
// what main hasn't got; and without it, where turning branching on is an
// error.
func TestBranchesInNetBox(t *testing.T) {
	t.Parallel()
	repo, pool, _ := libraryRepo(t, map[string]string{"device-types/Branch Test Co/BT-1.yaml": branchTestFile})
	env := parityEnv(t, repo, false)
	configure := trust(pool)
	fetched(t, pool, env)
	on := append(slices.Clone(env), "TRUEUP_NETBOX_BRANCHING=true")

	if r := runCLIApp(t, nil, configure, env, "compare", "--branch", "cmd-lab"); r.code != exitUsage {
		t.Errorf("compare --branch with branching off: exit code %d, want %d\nstderr: %s", r.code, exitUsage, r.stderr)
	}
	r := runCLIApp(t, nil, configure, env, "netbox", "check", "-o", "json")
	var check checkJSON
	if err := json.Unmarshal([]byte(r.stdout), &check); err != nil {
		t.Fatalf("netbox check -o json printed %q: %v\nstderr: %s", r.stdout, err, r.stderr)
	}
	if nb.Profile != netboxtest.Branching {
		if len(check.Warnings) != 0 || check.BranchingVersion != nil {
			t.Errorf("netbox check on a NetBox without netbox-branching = %+v, want no warning", check)
		}
		r := runCLIApp(t, nil, configure, on, "netbox", "branches")
		if r.code != exitFailure || !strings.Contains(r.stderr, "netbox.branching is on, but NetBox doesn't have netbox-branching") {
			t.Errorf("netbox branches with branching on: exit code %d, stderr %q", r.code, r.stderr)
		}
		return
	}
	if len(check.Warnings) != 1 || check.Warnings[0].Key != "netbox.branching" || check.BranchingVersion == nil || *check.BranchingVersion != netboxtest.BranchingVersion {
		t.Errorf("netbox check with branching off = %+v, want netbox-branching %s and a warning", check, netboxtest.BranchingVersion)
	}

	// A branch holding the manufacturer and a device type that differs from
	// the source's, which main hasn't got.
	ctx := t.Context()
	made, err := nb.CreateBranch(ctx, "cmd-lab", "TrueUp's command tests")
	if err != nil {
		t.Fatal(err)
	}
	in := nb.InBranch(made)
	var mfr struct{ ID int }
	if err := in.Post(ctx, "/api/dcim/manufacturers/", map[string]any{"name": "Branch Test Co", "slug": "branch-test-co"}, &mfr); err != nil {
		t.Fatal(err)
	}
	if err := in.Post(ctx, "/api/dcim/device-types/", map[string]any{
		"manufacturer": mfr.ID, "model": "BT-1", "slug": "branch-test-co-bt-1", "u_height": 1, "is_full_depth": false,
	}, nil); err != nil {
		t.Fatal(err)
	}

	r = runCLIApp(t, nil, configure, on, "netbox", "branches", "-o", "json")
	var branches branchesJSON
	if err := json.Unmarshal([]byte(r.stdout), &branches); err != nil {
		t.Fatalf("netbox branches -o json printed %q: %v\nstderr: %s", r.stdout, err, r.stderr)
	}
	found := false
	for _, b := range branches.Branches {
		found = found || (b.Name == "cmd-lab" && b.SchemaID == made.SchemaID && b.Target)
	}
	if !found || branches.Main || !branches.Branching {
		t.Errorf("netbox branches = %+v, want cmd-lab as a target, and not main", branches)
	}

	r = runCLIApp(t, nil, configure, on, "netbox", "check", "--branch", "cmd-lab", "-o", "json")
	check = checkJSON{}
	if err := json.Unmarshal([]byte(r.stdout), &check); err != nil {
		t.Fatalf("netbox check --branch -o json printed %q: %v\nstderr: %s", r.stdout, err, r.stderr)
	}
	if r.code != exitOK || !check.OK || check.Branch == nil || check.Branch.SchemaID != made.SchemaID || len(check.ObjectTypes) != len(readTypes(true)) {
		t.Errorf("netbox check --branch: exit code %d, %+v", r.code, check)
	}

	// In main, the source's manufacturer and device type are absent; in the
	// branch, candidates, the device type differing in height.
	sel := []string{"--manufacturer", "Branch Test Co"}
	inMain, code := compareJSONRun(t, on, configure, sel...)
	if code != exitOK || inMain.NetBox.Branch != nil || len(inMain.Objects) != 2 {
		t.Fatalf("compare in main: exit code %d, %d objects, branch %+v", code, len(inMain.Objects), inMain.NetBox.Branch)
	}
	inBranch, code := compareJSONRun(t, on, configure, append(sel, "--branch", "cmd-lab")...)
	if code != exitOK || inBranch.NetBox.Branch == nil || inBranch.NetBox.Branch.SchemaID != made.SchemaID {
		t.Fatalf("compare in the branch: exit code %d, branch %+v", code, inBranch.NetBox.Branch)
	}
	for _, o := range inMain.Objects {
		if o.Outcome != "absent" {
			t.Errorf("in main, %s is %s, want absent", o.name(), o.Outcome)
		}
	}
	for _, o := range inBranch.Objects {
		if o.Outcome != "candidate" || (o.name() == "BT-1") != !o.InStep {
			t.Errorf("in the branch, %s is %s, in step %v; want a candidate, the device type differing", o.name(), o.Outcome, o.InStep)
		}
	}

	// PostgreSQL holds both, main's and the branch's, side by side.
	var databaseURL string
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "TRUEUP_DATABASE_URL="); ok {
			databaseURL = v
		}
	}
	s, _, err := engine.Open(ctx, engine.Config{URL: config.NewSecret(databaseURL), DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	recorded, err := s.Comparisons(ctx, "lib")
	if err != nil {
		t.Fatal(err)
	}
	byTarget := map[string]int{}
	for _, c := range recorded {
		target := "main"
		if c.Branch != nil {
			if *c.Branch != (store.Branch{SchemaID: made.SchemaID, Name: "cmd-lab", NetBoxID: int64(made.ID)}) {
				t.Errorf("recorded in branch %+v", c.Branch)
			}
			target = c.Branch.SchemaID
		}
		byTarget[target]++
	}
	if byTarget["main"] != 2 || byTarget[made.SchemaID] != 2 || len(byTarget) != 2 {
		t.Errorf("recorded comparisons by target: %v, want 2 in main and 2 in %s", byTarget, made.SchemaID)
	}
}
