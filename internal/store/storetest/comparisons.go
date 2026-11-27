package storetest

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
)

// key returns the key of a device type's definition in a file, or of a
// derived manufacturer if path is "".
func key(path, name string) store.DefinitionKey {
	if path == "" {
		return store.DefinitionKey{ObjectType: "dcim.manufacturer", Identity: fmt.Sprintf(`{"name":%q}`, name)}
	}
	return store.DefinitionKey{ObjectType: "dcim.devicetype", Path: path, Identity: fmt.Sprintf(`{"manufacturer":{"name":"Dell"},"model":%q}`, name)}
}

// loaded is a load recorded: its status, and the IDs of its definitions by
// their keys.
type loaded struct {
	status store.LoadStatus
	ids    map[store.DefinitionKey]int64
}

// loadDefinitions records a fetch of source at version, of the files of the
// definitions given, and a load of them, which starts at at.
func loadDefinitions(t *testing.T, s store.Store, source, version string, at time.Time, defs ...store.Definition) loaded {
	t.Helper()
	var paths []string
	for _, d := range defs {
		if d.Path != "" {
			paths = append(paths, d.Path)
		}
	}
	record(t, s, success(source, version, at, paths...))
	return reload(t, s, source, version, at, defs...)
}

// reload records a load of the definitions given from source's snapshot of
// version.
func reload(t *testing.T, s store.Store, source, version string, at time.Time, defs ...store.Definition) loaded {
	t.Helper()
	recordLoad(t, s, loadOf(source, version, at, defs...))
	read := definitions(t, s, source, "")
	return loaded{status: read.LoadStatus, ids: idsOf(t, read.Definitions)}
}

// compared is a comparison with its definition's key.
type compared struct {
	key store.DefinitionKey
	store.Comparison
}

// comparison returns a comparison of the definition k of a load, with an
// outcome.
func (l loaded) comparison(k store.DefinitionKey, outcome string, netboxID int64) compared {
	return compared{key: k, Comparison: store.Comparison{
		DefinitionID: l.ids[k], Outcome: outcome, NetBoxID: netboxID, Differences: int(netboxID % 3),
		Result: fmt.Appendf(nil, `{"identity":%s,"outcome":%q}`, k.Identity, outcome),
	}}
}

// run returns a run of comparisons of a load's definitions, at at, of the
// whole load or not.
func (l loaded) run(at time.Time, whole bool, cs ...compared) store.ComparisonRun {
	r := store.ComparisonRun{
		Source: l.status.Source, Loaded: l.status.Loaded, LoadedAt: l.status.LoadedAt, ComparedAt: at,
		NetBoxURL: "https://netbox.example.com/", NetBoxVersion: "4.7.2", Whole: whole,
	}
	for _, c := range cs {
		r.Comparisons = append(r.Comparisons, c.Comparison)
	}
	return r
}

// recorded returns what Comparisons gives for a run's comparisons.
func recorded(r store.ComparisonRun, cs ...compared) []store.RecordedComparison {
	out := make([]store.RecordedComparison, len(cs))
	for i, c := range cs {
		out[i] = store.RecordedComparison{
			Comparison: c.Comparison, DefinitionKey: c.key, Source: r.Source, SourceVersion: r.Loaded.Version,
			ComparedAt: r.ComparedAt.UTC().Truncate(time.Microsecond), NetBoxURL: r.NetBoxURL, NetBoxVersion: r.NetBoxVersion,
		}
		if r.Branch != nil {
			b := *r.Branch
			out[i].Branch = &b
		}
	}
	return out
}

// inBranch returns r made in a branch.
func inBranch(r store.ComparisonRun, schemaID, name string, id int64) store.ComparisonRun {
	r.Branch = &store.Branch{SchemaID: schemaID, Name: name, NetBoxID: id}
	return r
}

func recordComparisons(t *testing.T, s store.Store, r store.ComparisonRun) {
	t.Helper()
	if err := s.RecordComparisons(t.Context(), r); err != nil {
		t.Fatalf("RecordComparisons: %v", err)
	}
}

func checkComparisons(t *testing.T, s store.Store, source string, want []store.RecordedComparison) {
	t.Helper()
	got, err := s.Comparisons(t.Context(), source)
	if err != nil {
		t.Fatalf("Comparisons(%q): %v", source, err)
	}
	if diff := cmp.Diff(want, got, equateEmpty); diff != "" {
		t.Errorf("Comparisons(%q) (-want +got):\n%s", source, diff)
	}
}

func slicesConcat(lists ...[]store.RecordedComparison) []store.RecordedComparison {
	var out []store.RecordedComparison
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

func testComparisons(t *testing.T, s store.Store) {
	dell, a, b := key("", "Dell"), key("Dell/a.yaml", "A"), key("Dell/b.yaml", "B")
	v1 := loadDefinitions(t, s, "lib", "v1", t0,
		definition("", "Dell", "v1"), definition("Dell/a.yaml", "A", "v1"), definition("Dell/b.yaml", "B", "v1"))
	checkComparisons(t, s, "lib", nil)

	// A whole run, of every definition, the way it is sorted: by type,
	// then path, then identity.
	r1 := v1.run(t0.Add(time.Second), true, v1.comparison(a, "candidate", 10), v1.comparison(dell, "candidate", 3), v1.comparison(b, "absent", 0))
	recordComparisons(t, s, r1)
	checkComparisons(t, s, "lib", recorded(r1, v1.comparison(a, "candidate", 10), v1.comparison(b, "absent", 0), v1.comparison(dell, "candidate", 3)))

	// A run of a selection replaces only the comparisons of what it
	// compared.
	r2 := v1.run(t0.Add(2*time.Second), false, v1.comparison(b, "conflict", 11))
	recordComparisons(t, s, r2)
	checkComparisons(t, s, "lib", slicesConcat(
		recorded(r1, v1.comparison(a, "candidate", 10)), recorded(r2, v1.comparison(b, "conflict", 11)), recorded(r1, v1.comparison(dell, "candidate", 3))))

	// A whole run that compares only some definitions, as the default
	// scope compares only what NetBox has, removes the others'.
	r3 := v1.run(t0.Add(3*time.Second), true, v1.comparison(a, "candidate", 10))
	recordComparisons(t, s, r3)
	checkComparisons(t, s, "lib", recorded(r3, v1.comparison(a, "candidate", 10)))
	r4 := v1.run(t0.Add(4*time.Second), false, v1.comparison(dell, "absent", 0))
	recordComparisons(t, s, r4)
	checkComparisons(t, s, "lib", slicesConcat(recorded(r3, v1.comparison(a, "candidate", 10)), recorded(r4, v1.comparison(dell, "absent", 0))))

	// A load removes the comparisons of the definitions it removes, such
	// as a file whose identity changed, which is a new definition, and
	// keeps the others', which keep their IDs.
	renamed := key("Dell/a.yaml", "A2")
	v2 := loadDefinitions(t, s, "lib", "v2", t0.Add(5*time.Second), definition("", "Dell", "v2"), definition("Dell/a.yaml", "A2", "v2"))
	if v2.ids[dell] != v1.ids[dell] {
		t.Fatalf("the load gave Dell the ID %d, not %d", v2.ids[dell], v1.ids[dell])
	}
	checkComparisons(t, s, "lib", recorded(r4, v1.comparison(dell, "absent", 0)))
	r5 := v2.run(t0.Add(6*time.Second), false, v2.comparison(renamed, "absent", 0))
	recordComparisons(t, s, r5)
	checkComparisons(t, s, "lib", slicesConcat(recorded(r5, v2.comparison(renamed, "absent", 0)), recorded(r4, v1.comparison(dell, "absent", 0))))

	// Other sources' comparisons are their own, whole runs included.
	other := loadDefinitions(t, s, "other", "x1", t0, definition("Dell/a.yaml", "A", "x1"))
	r6 := other.run(t0.Add(7*time.Second), true, other.comparison(a, "absent", 0))
	recordComparisons(t, s, r6)
	recordComparisons(t, s, v2.run(t0.Add(8*time.Second), false))
	checkComparisons(t, s, "other", recorded(r6, other.comparison(a, "absent", 0)))

	// A whole run that compares nothing removes every comparison of the
	// source, and only of the source.
	recordComparisons(t, s, v2.run(t0.Add(9*time.Second), true))
	checkComparisons(t, s, "lib", nil)
	checkComparisons(t, s, "other", recorded(r6, other.comparison(a, "absent", 0)))

	// A load that holds no definitions removes every comparison.
	reload(t, s, "other", "x1", t0.Add(10*time.Second))
	checkComparisons(t, s, "other", nil)
}

// Comparisons in main and in each branch are kept apart: a run replaces, and
// a whole run removes, only those of its own target, while a load removes
// those of the definitions it removes in every target.
func testComparisonsByTarget(t *testing.T, s store.Store) {
	dell, a, b := key("", "Dell"), key("Dell/a.yaml", "A"), key("Dell/b.yaml", "B")
	v1 := loadDefinitions(t, s, "lib", "v1", t0,
		definition("", "Dell", "v1"), definition("Dell/a.yaml", "A", "v1"), definition("Dell/b.yaml", "B", "v1"))

	main1 := v1.run(t0.Add(time.Second), true, v1.comparison(a, "absent", 0), v1.comparison(dell, "candidate", 3))
	recordComparisons(t, s, main1)
	lab1 := inBranch(v1.run(t0.Add(2*time.Second), true, v1.comparison(a, "candidate", 10), v1.comparison(b, "candidate", 11)), "lab00001", "lab", 7)
	recordComparisons(t, s, lab1)
	// Schema IDs sort by bytes, digits first, after main's ''.
	other1 := inBranch(v1.run(t0.Add(3*time.Second), false, v1.comparison(dell, "conflict", 3)), "0ther000", "other", 8)
	recordComparisons(t, s, other1)
	mainOnly := recorded(main1, v1.comparison(a, "absent", 0), v1.comparison(dell, "candidate", 3))
	checkComparisons(t, s, "lib", slicesConcat(
		mainOnly,
		recorded(other1, v1.comparison(dell, "conflict", 3)),
		recorded(lab1, v1.comparison(a, "candidate", 10), v1.comparison(b, "candidate", 11)),
	))

	// A whole run in a branch removes only that branch's others; a
	// selection in it, renamed since, replaces only what it compared, with
	// the name it has now.
	lab2 := inBranch(v1.run(t0.Add(4*time.Second), true, v1.comparison(b, "candidate", 11)), "lab00001", "lab", 7)
	recordComparisons(t, s, lab2)
	lab3 := inBranch(v1.run(t0.Add(5*time.Second), false, v1.comparison(a, "absent", 0)), "lab00001", "lab-renamed", 7)
	recordComparisons(t, s, lab3)
	checkComparisons(t, s, "lib", slicesConcat(
		mainOnly,
		recorded(other1, v1.comparison(dell, "conflict", 3)),
		recorded(lab3, v1.comparison(a, "absent", 0)), recorded(lab2, v1.comparison(b, "candidate", 11)),
	))

	// A whole run in main that compares nothing removes main's alone.
	recordComparisons(t, s, v1.run(t0.Add(6*time.Second), true))
	checkComparisons(t, s, "lib", slicesConcat(
		recorded(other1, v1.comparison(dell, "conflict", 3)),
		recorded(lab3, v1.comparison(a, "absent", 0)), recorded(lab2, v1.comparison(b, "candidate", 11)),
	))

	// A load removes the comparisons of the definitions it removes in every
	// target.
	loadDefinitions(t, s, "lib", "v2", t0.Add(7*time.Second), definition("", "Dell", "v2"), definition("Dell/b.yaml", "B", "v2"))
	checkComparisons(t, s, "lib", slicesConcat(
		recorded(other1, v1.comparison(dell, "conflict", 3)), recorded(lab2, v1.comparison(b, "candidate", 11)),
	))
}

// CheckComparisonsSchema checks that an engine's schema refuses what Prepare
// refuses of a comparison's branch, in rows try inserts straight into the
// comparisons table, so that no other way of writing one records a branch
// without its name and ID, or main with them.
func CheckComparisonsSchema(t *testing.T, s store.Store, try func(statement string) error) {
	t.Helper()
	l := loadDefinitions(t, s, "lib", "v1", t0, definition("a.yaml", "A", "v1"))
	id := l.ids[key("a.yaml", "A")]
	insert := func(branch, name, netboxID string) string {
		return fmt.Sprintf(`INSERT INTO comparisons (
			source, definition_id, branch, branch_name, branch_netbox_id, source_version, compared_at,
			netbox_url, netbox_version, outcome, netbox_id, differences, result
		) VALUES ('lib', %d, %s, %s, %s, 'v1', '2026-10-26T01:02:04.456789Z', 'https://netbox.example.com/', '4.7.2', 'absent', NULL, 0, '{}')`,
			id, branch, name, netboxID)
	}
	for _, refused := range []struct{ name, branch, branchName, netboxID string }{
		{"a branch without a name", "'abcd1234'", "NULL", "7"},
		{"a branch with an empty name", "'abcd1234'", "''", "7"},
		{"a branch without an ID", "'abcd1234'", "'lab'", "NULL"},
		{"a branch with the ID 0", "'abcd1234'", "'lab'", "0"},
		{"a short schema ID", "'abcd123'", "'lab'", "7"},
		{"an upper-case schema ID", "'ABCD1234'", "'lab'", "7"},
		{"main with a branch's name", "''", "'lab'", "NULL"},
		{"main with a branch's ID", "''", "NULL", "7"},
	} {
		if err := try(insert(refused.branch, refused.branchName, refused.netboxID)); err == nil {
			t.Errorf("the schema took a comparison of %s", refused.name)
		}
	}
	// The rows are otherwise good.
	for _, row := range [][3]string{{"''", "NULL", "NULL"}, {"'abcd1234'", "'lab'", "7"}} {
		if err := try(insert(row[0], row[1], row[2])); err != nil {
			t.Errorf("the schema refused a good comparison: %v", err)
		}
	}
}

func testComparisonsNeedTheSnapshot(t *testing.T, s store.Store) {
	a := key("a.yaml", "A")
	never := loaded{status: store.LoadStatus{Source: "lib", Loaded: store.LoadKey{Version: "v1", Reader: reader, Settings: settings}, LoadedAt: t0}}
	r := never.run(t0, false)
	if err := s.RecordComparisons(t.Context(), r); !errors.Is(err, store.ErrSnapshotChanged) {
		t.Errorf("RecordComparisons of a source never fetched = %v, want ErrSnapshotChanged", err)
	}
	record(t, s, failure("lib", t0, "unreachable"))
	if err := s.RecordComparisons(t.Context(), r); !errors.Is(err, store.ErrSnapshotChanged) {
		t.Errorf("RecordComparisons of a source never fetched successfully = %v, want ErrSnapshotChanged", err)
	}
	record(t, s, success("lib", "v1", t0.Add(time.Second), "a.yaml"))
	if err := s.RecordComparisons(t.Context(), r); !errors.Is(err, store.ErrSnapshotChanged) {
		t.Errorf("RecordComparisons of a source never loaded = %v, want ErrSnapshotChanged", err)
	}

	v1 := reload(t, s, "lib", "v1", t0.Add(time.Second), definition("a.yaml", "A", "v1"))
	r = v1.run(t0.Add(2*time.Second), false, v1.comparison(a, "absent", 0))
	recordComparisons(t, s, r)
	before := recorded(r, v1.comparison(a, "absent", 0))

	// A fetch since the comparison read the source: nothing changes.
	record(t, s, success("lib", "v2", t0.Add(3*time.Second), "a.yaml"))
	r2 := v1.run(t0.Add(4*time.Second), false, v1.comparison(a, "candidate", 4))
	if err := s.RecordComparisons(t.Context(), r2); !errors.Is(err, store.ErrSnapshotChanged) || !strings.Contains(err.Error(), `"v2"`) {
		t.Errorf("RecordComparisons of an older version = %v, want ErrSnapshotChanged naming v2", err)
	}
	checkComparisons(t, s, "lib", before)

	// A failed fetch keeps the snapshot, so its version is still the one
	// loaded and compared.
	v2 := reload(t, s, "lib", "v2", t0.Add(5*time.Second), definition("a.yaml", "A", "v2"))
	record(t, s, failure("lib", t0.Add(6*time.Second), "unreachable"))
	r3 := v2.run(t0.Add(7*time.Second), false, v2.comparison(a, "candidate", 4))
	recordComparisons(t, s, r3)
	checkComparisons(t, s, "lib", recorded(r3, v2.comparison(a, "candidate", 4)))
}

func testComparisonsNeedTheLoad(t *testing.T, s store.Store) {
	a, b := key("a.yaml", "A"), key("b.yaml", "B")
	v1 := loadDefinitions(t, s, "lib", "v1", t0, definition("a.yaml", "A", "v1"), definition("b.yaml", "B", "v1"))
	r := v1.run(t0.Add(time.Second), true, v1.comparison(a, "absent", 0), v1.comparison(b, "absent", 0))
	recordComparisons(t, s, r)
	before := recorded(r, v1.comparison(a, "absent", 0), v1.comparison(b, "absent", 0))

	// The same version loaded again, such as by another release of TrueUp
	// or other settings of the source: the definitions compared may not be
	// those recorded, so nothing changes.
	for _, again := range []store.Load{
		func() store.Load {
			l := loadOf("lib", "v1", t0.Add(2*time.Second), definition("a.yaml", "A", "v1"))
			return l
		}(),
		func() store.Load {
			l := loadOf("lib", "v1", t0.Add(3*time.Second), definition("a.yaml", "A", "v1"), definition("b.yaml", "B", "v1"))
			l.Reader = "devicetype-library/2"
			return l
		}(),
	} {
		recordLoad(t, s, again)
		late := v1.run(t0.Add(4*time.Second), true, v1.comparison(a, "candidate", 5))
		if err := s.RecordComparisons(t.Context(), late); !errors.Is(err, store.ErrSnapshotChanged) || !strings.Contains(err.Error(), "loaded again") {
			t.Errorf("RecordComparisons of definitions loaded again = %v, want ErrSnapshotChanged saying so", err)
		}
	}
	// The first load removed b, and its comparison.
	checkComparisons(t, s, "lib", before[:1])

	// A load that failed leaves the definitions recorded, so they can
	// still be compared.
	now := reload(t, s, "lib", "v1", t0.Add(5*time.Second), definition("a.yaml", "A", "v1"))
	failed := loadOf("lib", "v1", t0.Add(6*time.Second))
	failed.Error = "the cache is gone"
	recordLoad(t, s, failed)
	r2 := now.run(t0.Add(7*time.Second), true, now.comparison(a, "candidate", 5))
	recordComparisons(t, s, r2)
	checkComparisons(t, s, "lib", recorded(r2, now.comparison(a, "candidate", 5)))

	// A comparison must be of one of the source's own definitions.
	other := loadDefinitions(t, s, "other", "x1", t0, definition("a.yaml", "A", "x1"))
	stray := now.run(t0.Add(8*time.Second), false, other.comparison(a, "absent", 0))
	if err := s.RecordComparisons(t.Context(), stray); err == nil || errors.Is(err, store.ErrSnapshotChanged) {
		t.Errorf("RecordComparisons of another source's definition = %v, want an error", err)
	}
	checkComparisons(t, s, "lib", recorded(r2, now.comparison(a, "candidate", 5)))
	checkComparisons(t, s, "other", nil)
}

func testComparisonsRemoved(t *testing.T, s store.Store) {
	a := key("a.yaml", "A")
	for _, name := range []string{"gone", "kept"} {
		l := loadDefinitions(t, s, name, "v1", t0, definition("a.yaml", "A", "v1"))
		recordComparisons(t, s, l.run(t0, true, l.comparison(a, "absent", 0)))
	}
	removed, err := s.RemoveSources(t.Context(), By, []string{"kept"})
	if err != nil {
		t.Fatalf("RemoveSources: %v", err)
	}
	if diff := cmp.Diff([]string{"gone"}, removed); diff != "" {
		t.Errorf("RemoveSources (-want +got):\n%s", diff)
	}
	checkComparisons(t, s, "gone", nil)
	if got, err := s.Comparisons(t.Context(), "kept"); err != nil || len(got) != 1 {
		t.Errorf("Comparisons of the source kept = %d, %v; want 1", len(got), err)
	}
}

func testInvalidComparisons(t *testing.T, s store.Store) {
	a := key("a.yaml", "A")
	l := loadDefinitions(t, s, "lib", "v1", t0, definition("a.yaml", "A", "v1"))
	valid := l.comparison(a, "absent", 0)
	with := func(change func(*store.ComparisonRun)) store.ComparisonRun {
		r := l.run(t0, false, valid)
		change(&r)
		return r
	}
	withComparison := func(change func(*store.Comparison)) store.ComparisonRun {
		c := valid
		change(&c.Comparison)
		return l.run(t0, false, c)
	}
	tests := []struct {
		name string
		run  store.ComparisonRun
	}{
		{"no source", with(func(r *store.ComparisonRun) { r.Source = "" })},
		{"no version", with(func(r *store.ComparisonRun) { r.Loaded.Version = "" })},
		{"no reader", with(func(r *store.ComparisonRun) { r.Loaded.Reader = "" })},
		{"no settings", with(func(r *store.ComparisonRun) { r.Loaded.Settings = "" })},
		{"no time", with(func(r *store.ComparisonRun) { r.ComparedAt = time.Time{} })},
		{"no time of the load", with(func(r *store.ComparisonRun) { r.LoadedAt = time.Time{} })},
		{"no NetBox", with(func(r *store.ComparisonRun) { r.NetBoxURL = "" })},
		{"no NetBox release", with(func(r *store.ComparisonRun) { r.NetBoxVersion = "" })},
		{"a definition compared twice", l.run(t0, false, valid, valid)},
		{"no definition", withComparison(func(c *store.Comparison) { c.DefinitionID = 0 })},
		{"no outcome", withComparison(func(c *store.Comparison) { c.Outcome = "" })},
		{"an outcome compare doesn't give", withComparison(func(c *store.Comparison) { c.Outcome = "Candidate" })},
		{"a negative NetBox ID", withComparison(func(c *store.Comparison) { c.NetBoxID = -1 })},
		{"a negative count", withComparison(func(c *store.Comparison) { c.Differences = -1 })},
		{"a result that isn't JSON", withComparison(func(c *store.Comparison) { c.Result = []byte("{") })},
		{"a NUL in the source's name", with(func(r *store.ComparisonRun) { r.Source = "li\x00b" })},
		{"a reader that isn't UTF-8", with(func(r *store.ComparisonRun) { r.Loaded.Reader = "r\xff" })},
		{"a branch without a schema ID", with(func(r *store.ComparisonRun) { *r = inBranch(*r, "", "lab", 7) })},
		{"a branch with a short schema ID", with(func(r *store.ComparisonRun) { *r = inBranch(*r, "lab0001", "lab", 7) })},
		{"a branch with an upper-case schema ID", with(func(r *store.ComparisonRun) { *r = inBranch(*r, "LAB00001", "lab", 7) })},
		{"a branch with a line break in its schema ID", with(func(r *store.ComparisonRun) { *r = inBranch(*r, "lab0001\n", "lab", 7) })},
		{"a branch without a name", with(func(r *store.ComparisonRun) { *r = inBranch(*r, "lab00001", "", 7) })},
		{"a branch whose name isn't UTF-8", with(func(r *store.ComparisonRun) { *r = inBranch(*r, "lab00001", "l\xffb", 7) })},
		{"a branch without a NetBox ID", with(func(r *store.ComparisonRun) { *r = inBranch(*r, "lab00001", "lab", 0) })},
		{"a branch with a negative NetBox ID", with(func(r *store.ComparisonRun) { *r = inBranch(*r, "lab00001", "lab", -7) })},
	}
	for _, tt := range tests {
		if err := s.RecordComparisons(t.Context(), tt.run); err == nil || errors.Is(err, store.ErrSnapshotChanged) {
			t.Errorf("%s: RecordComparisons = %v, want an error before anything is read", tt.name, err)
		}
	}
	checkComparisons(t, s, "lib", nil)
}

func testComparisonsSorting(t *testing.T, s store.Store) {
	// By bytes: upper case before lower, and ASCII before the rest.
	keys := []store.DefinitionKey{
		key("dell/a.yaml", "A"), key("Dell/b.yaml", "B"), key("Ébène/a.yaml", "A"), key("Dell/a.yaml", "A"),
		key("", "dell"), key("", "Dell"), key("", "Émile"),
	}
	var defs []store.Definition
	for _, k := range keys {
		var name string
		if k.Path == "" {
			name = strings.TrimSuffix(strings.TrimPrefix(k.Identity, `{"name":"`), `"}`)
		} else {
			name = strings.TrimSuffix(strings.TrimPrefix(k.Identity, `{"manufacturer":{"name":"Dell"},"model":"`), `"}`)
		}
		defs = append(defs, definition(k.Path, name, "v1"))
	}
	l := loadDefinitions(t, s, "lib", "v1", t0, defs...)
	var cs []compared
	for _, k := range keys {
		cs = append(cs, l.comparison(k, "absent", 0))
	}
	recordComparisons(t, s, l.run(t0, true, cs...))
	got, err := s.Comparisons(t.Context(), "lib")
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, c := range got {
		order = append(order, c.ObjectType+" "+c.Path+" "+c.Identity)
	}
	want := []string{
		`dcim.devicetype Dell/a.yaml {"manufacturer":{"name":"Dell"},"model":"A"}`,
		`dcim.devicetype Dell/b.yaml {"manufacturer":{"name":"Dell"},"model":"B"}`,
		`dcim.devicetype dell/a.yaml {"manufacturer":{"name":"Dell"},"model":"A"}`,
		`dcim.devicetype Ébène/a.yaml {"manufacturer":{"name":"Dell"},"model":"A"}`,
		`dcim.manufacturer  {"name":"Dell"}`,
		`dcim.manufacturer  {"name":"dell"}`,
		`dcim.manufacturer  {"name":"Émile"}`,
	}
	if diff := cmp.Diff(want, order); diff != "" {
		t.Errorf("Comparisons' order (-want +got):\n%s", diff)
	}
}

func testComparisonsWhileFetching(t *testing.T, s store.Store) {
	// A comparison of a version races fetches of the next, loads of the
	// same version again, and removals of the source: each recording
	// either finds the load it compared, and records it whole, or records
	// nothing, and nothing else fails.
	a, b := key("a.yaml", "A"), key("b.yaml", "B")
	for i := range 30 {
		// Each round starts afresh.
		if _, err := s.RemoveSources(t.Context(), By, nil); err != nil {
			t.Fatalf("round %d: RemoveSources: %v", i, err)
		}
		at := t0.Add(time.Duration(i) * time.Second)
		version := fmt.Sprintf("v%d", i)
		l := loadDefinitions(t, s, "lib", version, at, definition("a.yaml", "A", version), definition("b.yaml", "B", version))
		r := l.run(at.Add(time.Millisecond), true, l.comparison(a, "candidate", int64(i+1)), l.comparison(b, "absent", 0))
		var recordErr error
		var wg sync.WaitGroup
		wg.Go(func() { recordErr = s.RecordComparisons(t.Context(), r) })
		wg.Go(func() {
			var err error
			switch i % 3 {
			case 0:
				err = s.RecordFetch(t.Context(), By, success("lib", version+"-next", at.Add(2*time.Millisecond), "a.yaml"))
			case 1:
				// Loaded again, without b.
				err = s.RecordLoad(t.Context(), loadOf("lib", version, at.Add(2*time.Millisecond), definition("a.yaml", "A", version)))
			default:
				_, err = s.RemoveSources(t.Context(), By, nil)
			}
			if err != nil {
				t.Errorf("round %d: %v", i, err)
			}
		})
		wg.Wait()
		got, err := s.Comparisons(t.Context(), "lib")
		if err != nil {
			t.Fatalf("round %d: Comparisons: %v", i, err)
		}
		all := recorded(r, l.comparison(a, "candidate", int64(i+1)), l.comparison(b, "absent", 0))
		switch {
		case recordErr == nil && i%3 == 0:
			// Recorded before the fetch, which leaves it.
			if diff := cmp.Diff(all, got, equateEmpty); diff != "" {
				t.Errorf("round %d: the comparisons aren't recorded whole (-want +got):\n%s", i, diff)
			}
		case recordErr == nil && i%3 == 1:
			// Recorded before the load, which removed b's.
			if diff := cmp.Diff(all[:1], got, equateEmpty); diff != "" {
				t.Errorf("round %d: the comparisons recorded before the load (-want +got):\n%s", i, diff)
			}
		case recordErr == nil:
			// Recorded, then removed with the source.
			if len(got) != 0 {
				t.Errorf("round %d: %d comparisons outlive their source", i, len(got))
			}
		case errors.Is(recordErr, store.ErrSnapshotChanged):
			if i%3 != 2 && len(got) != 0 {
				t.Errorf("round %d: %d comparisons recorded, though RecordComparisons failed", i, len(got))
			}
		default:
			t.Errorf("round %d: RecordComparisons: %v", i, recordErr)
		}
	}
}

func testLargeComparisons(t *testing.T, s store.Store) {
	// Results of the size a device type's comparison has, recorded whole,
	// then half of them again, which keeps the other half.
	const n = 3000
	defs := make([]store.Definition, n)
	keys := make([]store.DefinitionKey, n)
	for i := range n {
		defs[i] = definition(fmt.Sprintf("device-types/M%03d/%05d.yaml", i%400, i), fmt.Sprintf("Model %d", i), "v1")
		keys[i] = defs[i].DefinitionKey
	}
	l := loadDefinitions(t, s, "lib", "v1", t0, defs...)
	cs := make([]compared, n)
	padding := strings.Repeat("x", 2000)
	for i, k := range keys {
		cs[i] = l.comparison(k, "absent", 0)
		cs[i].Result = fmt.Appendf(nil, `{"padding":%q,"n":%d}`, padding, i)
	}
	first := l.run(t0, true, cs...)
	recordComparisons(t, s, first)
	second := l.run(t0.Add(time.Second), false, cs[n/2:]...)
	recordComparisons(t, s, second)
	got, err := s.Comparisons(t.Context(), "lib")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("Comparisons returned %d, want %d", len(got), n)
	}
	index := make(map[string]int, n)
	for i, k := range keys {
		index[k.Path] = i
	}
	for _, c := range got {
		i, ok := index[c.Path]
		want := first
		if i >= n/2 {
			want = second
		}
		if !ok || c.DefinitionID != l.ids[keys[i]] || string(c.Result) != string(cs[i].Result) || !c.ComparedAt.Equal(want.ComparedAt) {
			t.Fatalf("the comparison of %s holds %.40s from %s, want %.40s from %s", c.Path, c.Result, c.ComparedAt, cs[i].Result, want.ComparedAt)
		}
	}
}
