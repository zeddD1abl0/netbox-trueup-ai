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
func key(path, name string) store.ComparisonKey {
	if path == "" {
		return store.ComparisonKey{ObjectType: "dcim.manufacturer", Identity: fmt.Sprintf(`{"name":%q}`, name)}
	}
	return store.ComparisonKey{ObjectType: "dcim.devicetype", Path: path, Identity: fmt.Sprintf(`{"manufacturer":{"name":"Dell"},"model":%q}`, name)}
}

// comparison returns a comparison of the definition k with an outcome.
func comparison(k store.ComparisonKey, outcome string, id int64) store.Comparison {
	return store.Comparison{
		ComparisonKey: k, Outcome: outcome, NetBoxID: id, Differences: int(id % 3),
		Result: fmt.Appendf(nil, `{"identity":%s,"outcome":%q}`, k.Identity, outcome),
	}
}

func run(source, version string, at time.Time, defined []store.ComparisonKey, comparisons ...store.Comparison) store.ComparisonRun {
	return store.ComparisonRun{
		Source: source, SourceVersion: version, ComparedAt: at,
		NetBoxURL: "https://netbox.example.com/", NetBoxVersion: "4.7.2",
		Defined: defined, Comparisons: comparisons,
	}
}

// recorded returns what Comparisons gives for a run's comparisons.
func recorded(r store.ComparisonRun, cs ...store.Comparison) []store.RecordedComparison {
	out := make([]store.RecordedComparison, len(cs))
	for i, c := range cs {
		out[i] = store.RecordedComparison{
			Comparison: c, Source: r.Source, SourceVersion: r.SourceVersion,
			ComparedAt: r.ComparedAt.UTC().Truncate(time.Microsecond), NetBoxURL: r.NetBoxURL, NetBoxVersion: r.NetBoxVersion,
		}
	}
	return out
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

func testComparisons(t *testing.T, s store.Store) {
	record(t, s, success("lib", "v1", t0, "Dell/a.yaml", "Dell/b.yaml"))
	dell, a, b := key("", "Dell"), key("Dell/a.yaml", "A"), key("Dell/b.yaml", "B")
	defined := []store.ComparisonKey{dell, a, b}
	checkComparisons(t, s, "lib", nil)

	// The first run compares everything, the way it is sorted: by type,
	// then path, then identity.
	r1 := run("lib", "v1", t0.Add(time.Second), defined, comparison(a, "candidate", 10), comparison(dell, "candidate", 3), comparison(b, "absent", 0))
	recordComparisons(t, s, r1)
	checkComparisons(t, s, "lib", recorded(r1, comparison(a, "candidate", 10), comparison(b, "absent", 0), comparison(dell, "candidate", 3)))

	// A run that compares some definitions replaces only theirs.
	r2 := run("lib", "v1", t0.Add(2*time.Second), defined, comparison(b, "conflict", 11))
	recordComparisons(t, s, r2)
	want := slicesConcat(recorded(r1, comparison(a, "candidate", 10)), recorded(r2, comparison(b, "conflict", 11)), recorded(r1, comparison(dell, "candidate", 3)))
	checkComparisons(t, s, "lib", want)

	// A definition the source no longer holds loses its comparison, even
	// when it isn't compared; a file whose identity changed is a new
	// definition.
	record(t, s, success("lib", "v2", t0.Add(3*time.Second), "Dell/b.yaml"))
	renamed := key("Dell/b.yaml", "B2")
	r3 := run("lib", "v2", t0.Add(4*time.Second), []store.ComparisonKey{dell, renamed}, comparison(renamed, "absent", 0))
	recordComparisons(t, s, r3)
	checkComparisons(t, s, "lib", slicesConcat(recorded(r3, comparison(renamed, "absent", 0)), recorded(r1, comparison(dell, "candidate", 3))))

	// A run that compares nothing removes what the source no longer holds.
	r4 := run("lib", "v2", t0.Add(5*time.Second), []store.ComparisonKey{dell})
	recordComparisons(t, s, r4)
	checkComparisons(t, s, "lib", recorded(r1, comparison(dell, "candidate", 3)))

	// Other sources' comparisons are their own.
	record(t, s, success("other", "x1", t0, "Dell/a.yaml"))
	r5 := run("other", "x1", t0.Add(6*time.Second), []store.ComparisonKey{a}, comparison(a, "absent", 0))
	recordComparisons(t, s, r5)
	checkComparisons(t, s, "lib", recorded(r1, comparison(dell, "candidate", 3)))
	checkComparisons(t, s, "other", recorded(r5, comparison(a, "absent", 0)))
}

func slicesConcat(lists ...[]store.RecordedComparison) []store.RecordedComparison {
	var out []store.RecordedComparison
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

func testComparisonsNeedTheSnapshot(t *testing.T, s store.Store) {
	a := key("a.yaml", "A")
	r := run("lib", "v1", t0, []store.ComparisonKey{a}, comparison(a, "absent", 0))
	if err := s.RecordComparisons(t.Context(), r); !errors.Is(err, store.ErrSnapshotChanged) {
		t.Errorf("RecordComparisons of a source never fetched = %v, want ErrSnapshotChanged", err)
	}
	record(t, s, failure("lib", t0, "unreachable"))
	if err := s.RecordComparisons(t.Context(), r); !errors.Is(err, store.ErrSnapshotChanged) {
		t.Errorf("RecordComparisons of a source never fetched successfully = %v, want ErrSnapshotChanged", err)
	}

	record(t, s, success("lib", "v1", t0.Add(time.Second), "a.yaml"))
	recordComparisons(t, s, r)
	before := recorded(r, comparison(a, "absent", 0))

	// A fetch since the comparison read the source: nothing changes.
	record(t, s, success("lib", "v2", t0.Add(2*time.Second), "a.yaml"))
	r.ComparedAt, r.Comparisons = t0.Add(3*time.Second), []store.Comparison{comparison(a, "candidate", 4)}
	if err := s.RecordComparisons(t.Context(), r); !errors.Is(err, store.ErrSnapshotChanged) || !strings.Contains(err.Error(), `"v2"`) {
		t.Errorf("RecordComparisons of an older version = %v, want ErrSnapshotChanged naming v2", err)
	}
	checkComparisons(t, s, "lib", before)

	// A failed fetch keeps the snapshot, so its version is still the one
	// compared.
	record(t, s, failure("lib", t0.Add(4*time.Second), "unreachable"))
	r.SourceVersion = "v2"
	recordComparisons(t, s, r)
	checkComparisons(t, s, "lib", recorded(r, comparison(a, "candidate", 4)))
}

func testComparisonsRemoved(t *testing.T, s store.Store) {
	a := key("a.yaml", "A")
	for _, name := range []string{"gone", "kept"} {
		record(t, s, success(name, "v1", t0, "a.yaml"))
		recordComparisons(t, s, run(name, "v1", t0, []store.ComparisonKey{a}, comparison(a, "absent", 0)))
	}
	removed, err := s.RemoveSources(t.Context(), []string{"kept"})
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
	record(t, s, success("lib", "v1", t0, "a.yaml"))
	a, b := key("a.yaml", "A"), key("b.yaml", "B")
	defined := []store.ComparisonKey{a}
	valid := comparison(a, "absent", 0)
	tests := []struct {
		name string
		run  store.ComparisonRun
	}{
		{"no source", run("", "v1", t0, defined, valid)},
		{"no version", run("lib", "", t0, defined, valid)},
		{"no time", run("lib", "v1", time.Time{}, defined, valid)},
		{"no NetBox", func() store.ComparisonRun { r := run("lib", "v1", t0, defined, valid); r.NetBoxURL = ""; return r }()},
		{"a comparison of what isn't defined", run("lib", "v1", t0, defined, valid, comparison(b, "absent", 0))},
		{"a definition compared twice", run("lib", "v1", t0, defined, valid, valid)},
		{"no outcome", run("lib", "v1", t0, defined, comparison(a, "", 0))},
		{"a negative ID", run("lib", "v1", t0, defined, comparison(a, "candidate", -1))},
		{"a result that isn't JSON", run("lib", "v1", t0, defined, func() store.Comparison { c := valid; c.Result = []byte("{"); return c }())},
		{"an identity that isn't JSON", run("lib", "v1", t0, []store.ComparisonKey{{ObjectType: "dcim.manufacturer", Identity: "Dell"}})},
		{"a path that isn't UTF-8", run("lib", "v1", t0, []store.ComparisonKey{{ObjectType: "dcim.devicetype", Path: "a\xff.yaml", Identity: "{}"}})},
		{"a NUL in the source's name", run("li\x00b", "v1", t0, defined, valid)},
	}
	for _, tt := range tests {
		if err := s.RecordComparisons(t.Context(), tt.run); err == nil || errors.Is(err, store.ErrSnapshotChanged) {
			t.Errorf("%s: RecordComparisons = %v, want an error before anything is read", tt.name, err)
		}
	}
	checkComparisons(t, s, "lib", nil)
}

func testComparisonsSorting(t *testing.T, s store.Store) {
	record(t, s, success("lib", "v1", t0, "x"))
	// By bytes: upper case before lower, and ASCII before the rest.
	keys := []store.ComparisonKey{
		key("dell/a.yaml", "A"), key("Dell/b.yaml", "B"), key("Ébène/a.yaml", "A"), key("Dell/a.yaml", "A"),
		key("", "dell"), key("", "Dell"), key("", "Émile"),
	}
	var cs []store.Comparison
	for _, k := range keys {
		cs = append(cs, comparison(k, "absent", 0))
	}
	r := run("lib", "v1", t0, keys, cs...)
	recordComparisons(t, s, r)
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
	// A comparison of a version races fetches of the next, and removals of
	// the source: each recording either finds the version it compared, and
	// records it whole, or records nothing, and nothing else fails.
	a, b := key("a.yaml", "A"), key("b.yaml", "B")
	defined := []store.ComparisonKey{a, b}
	for i := range 30 {
		at := t0.Add(time.Duration(i) * time.Second)
		version := fmt.Sprintf("v%d", i)
		record(t, s, success("lib", version, at, "a.yaml", "b.yaml"))
		r := run("lib", version, at.Add(time.Millisecond), defined, comparison(a, "candidate", int64(i+1)), comparison(b, "absent", 0))
		var recordErr error
		var wg sync.WaitGroup
		wg.Go(func() { recordErr = s.RecordComparisons(t.Context(), r) })
		wg.Go(func() {
			var err error
			if i%2 == 0 {
				err = s.RecordFetch(t.Context(), success("lib", version+"-next", at.Add(2*time.Millisecond), "a.yaml"))
			} else {
				_, err = s.RemoveSources(t.Context(), nil)
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
		switch {
		case recordErr == nil && i%2 == 0:
			// Recorded before the fetch, which leaves it.
			if diff := cmp.Diff(recorded(r, r.Comparisons...), got, equateEmpty); diff != "" {
				t.Errorf("round %d: the comparisons aren't recorded whole (-want +got):\n%s", i, diff)
			}
		case recordErr == nil:
			// Recorded, then removed with the source.
			if len(got) != 0 {
				t.Errorf("round %d: %d comparisons outlive their source", i, len(got))
			}
		case errors.Is(recordErr, store.ErrSnapshotChanged):
			if i%2 == 0 && len(got) != 0 {
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
	record(t, s, success("lib", "v1", t0, "x"))
	defined := make([]store.ComparisonKey, n)
	cs := make([]store.Comparison, n)
	padding := strings.Repeat("x", 2000)
	for i := range n {
		defined[i] = key(fmt.Sprintf("device-types/M%03d/%05d.yaml", i%400, i), fmt.Sprintf("Model %d", i))
		cs[i] = comparison(defined[i], "absent", 0)
		cs[i].Result = fmt.Appendf(nil, `{"padding":%q,"n":%d}`, padding, i)
	}
	first := run("lib", "v1", t0, defined, cs...)
	recordComparisons(t, s, first)
	second := run("lib", "v1", t0.Add(time.Second), defined, cs[n/2:]...)
	recordComparisons(t, s, second)
	got, err := s.Comparisons(t.Context(), "lib")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("Comparisons returned %d, want %d", len(got), n)
	}
	index := make(map[string]int, n)
	for i, k := range defined {
		index[k.Path] = i
	}
	for _, c := range got {
		i, ok := index[c.Path]
		want := first
		if i >= n/2 {
			want = second
		}
		if !ok || string(c.Result) != string(cs[i].Result) || !c.ComparedAt.Equal(want.ComparedAt) {
			t.Fatalf("the comparison of %s holds %.40s from %s, want %.40s from %s", c.Path, c.Result, c.ComparedAt, cs[i].Result, want.ComparedAt)
		}
	}
}
