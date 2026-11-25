package storetest

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
)

// reader and settings are what the loads in these tests say read them.
const (
	reader   = "devicetype-library/1"
	settings = "sha256:5e771265"
)

// definition returns a definition of a Dell device type in a file, or of
// the manufacturer name, derived, if path is "", whose JSON says which load
// gave it.
func definition(path, name, load string) store.Definition {
	d := store.Definition{
		DefinitionKey: key(path, name),
		JSON:          fmt.Appendf(nil, `{"name":%q,"load":%q}`, name, load),
		UniqueValues:  fmt.Appendf(nil, `{"name":%q}`, name),
	}
	if path == "" {
		d.Manufacturer, d.Slug = name, strings.ToLower(name)
		return d
	}
	d.Manufacturer, d.Model, d.Slug, d.PartNumber = "Dell", name, "dell-"+strings.ToLower(name), "PN-"+name
	d.Digest = file(path, load).Digest
	return d
}

// loadOf returns a load of a source at version that succeeded, its files
// those of its definitions.
func loadOf(source, version string, at time.Time, defs ...store.Definition) store.Load {
	l := store.Load{Source: source, Version: version, Reader: reader, Settings: settings, StartedAt: at, Definitions: defs}
	for _, d := range defs {
		if d.Path != "" {
			l.Files++
		}
	}
	return l
}

func recordLoad(t *testing.T, s store.Store, l store.Load) {
	t.Helper()
	if err := s.RecordLoad(t.Context(), l); err != nil {
		t.Fatalf("RecordLoad: %v", err)
	}
}

func loadStatus(t *testing.T, s store.Store, source string) store.LoadStatus {
	t.Helper()
	status, err := s.LoadStatus(t.Context(), source)
	if err != nil {
		t.Fatalf("LoadStatus(%q): %v", source, err)
	}
	return status
}

func definitions(t *testing.T, s store.Store, source, search string) store.SourceDefinitions {
	t.Helper()
	loaded, err := s.Definitions(t.Context(), source, search)
	if err != nil {
		t.Fatalf("Definitions(%q, %q): %v", source, search, err)
	}
	status := loadStatus(t, s, source)
	if diff := cmp.Diff(status, loaded.LoadStatus); diff != "" {
		t.Errorf("Definitions(%q, %q) gives another status than LoadStatus (-LoadStatus +Definitions):\n%s", source, search, diff)
	}
	return loaded
}

// withoutIDs compares definitions apart from their IDs.
var withoutIDs = cmpopts.IgnoreFields(store.Definition{}, "ID")

// idsOf returns the IDs of definitions by their keys, and checks that each
// has one of its own.
func idsOf(t *testing.T, defs []store.Definition) map[store.DefinitionKey]int64 {
	t.Helper()
	ids := make(map[store.DefinitionKey]int64, len(defs))
	seen := map[int64]bool{}
	for _, d := range defs {
		if d.ID <= 0 || seen[d.ID] {
			t.Fatalf("%s has the ID %d, which is not its own", d.DefinitionKey, d.ID)
		}
		seen[d.ID] = true
		ids[d.DefinitionKey] = d.ID
	}
	return ids
}

func testLoads(t *testing.T, s store.Store) {
	record(t, s, success("lib", "v1", t0, "Dell/a.yaml", "Dell/b.yaml", "Dell/bad.yaml"))
	want := store.LoadStatus{Source: "lib", SnapshotVersion: "v1", SnapshotRef: "main"}
	if diff := cmp.Diff(want, loadStatus(t, s, "lib")); diff != "" {
		t.Errorf("LoadStatus before any load (-want +got):\n%s", diff)
	}
	if want.Current(reader, settings) || want.Failed(reader, settings) || want.State() != "" {
		t.Errorf("before any load: Current %t, Failed %t, State %q", want.Current(reader, settings), want.Failed(reader, settings), want.State())
	}
	if diff := cmp.Diff(store.SourceDefinitions{LoadStatus: want}, definitions(t, s, "lib", ""), equateEmpty); diff != "" {
		t.Errorf("Definitions before any load (-want +got):\n%s", diff)
	}

	// Definitions come back in the load's order, not sorted, with their
	// problems.
	started := t0.Add(time.Second)
	l := loadOf("lib", "v1", started, definition("Dell/b.yaml", "B", "1"), definition("", "Dell", "1"), definition("Dell/a.yaml", "A", "1"))
	l.Files, l.InvalidFiles = 3, 1
	l.Problems = []store.Problem{
		{Severity: "warning", Reason: `manufacturers "DELL" and "Dell" would both get the slug "dell"`},
		{Severity: "error", Path: "Dell/bad.yaml", Line: 3, Column: 7, Field: "u_height", Reason: "isn't a number"},
		{Severity: "warning", Path: "Dell/b.yaml", Reason: "has inventory items"},
	}
	recordLoad(t, s, l)
	loaded := store.LoadKey{Version: "v1", Reader: reader, Settings: settings}
	want.Latest, want.Loaded, want.StartedAt, want.LoadedAt = loaded, loaded, started.Truncate(time.Microsecond), started.Truncate(time.Microsecond)
	want.Files, want.InvalidFiles, want.Definitions = 3, 1, 3
	got := definitions(t, s, "lib", "")
	if diff := cmp.Diff(store.SourceDefinitions{LoadStatus: want, Definitions: l.Definitions, Problems: l.Problems}, got, equateEmpty, withoutIDs); diff != "" {
		t.Errorf("Definitions (-want +got):\n%s", diff)
	}
	idsOf(t, got.Definitions)
	if got.State() != store.LoadReady || got.Failed(reader, settings) {
		t.Errorf("State = %q, Failed = %t; want ready and false", got.State(), got.Failed(reader, settings))
	}
	for _, tt := range []struct {
		reader, settings string
		want             bool
	}{{reader, settings, true}, {"devicetype-library/2", settings, false}, {reader, "sha256:other", false}} {
		if got := got.Current(tt.reader, tt.settings); got != tt.want {
			t.Errorf("Current(%q, %q) = %t, want %t", tt.reader, tt.settings, got, tt.want)
		}
	}

	// A later fetch leaves them, but they are no longer of the snapshot.
	record(t, s, success("lib", "v2", t0.Add(time.Minute), "Dell/a.yaml"))
	got = definitions(t, s, "lib", "")
	if got.SnapshotVersion != "v2" || got.Loaded.Version != "v1" || len(got.Definitions) != 3 || len(got.Problems) != 3 {
		t.Errorf("after a fetch: snapshot %s, loaded %s, %d definitions, %d problems; want v2, v1, 3 and 3",
			got.SnapshotVersion, got.Loaded.Version, len(got.Definitions), len(got.Problems))
	}
	if got.Current(reader, settings) {
		t.Error("Current after a fetch of another version")
	}
}

func testLoadsKeepIDs(t *testing.T, s store.Store) {
	record(t, s, success("lib", "v1", t0, "a.yaml", "b.yaml", "c.yaml"))
	recordLoad(t, s, loadOf("lib", "v1", t0, definition("", "Dell", "1"), definition("a.yaml", "A", "1"),
		definition("b.yaml", "B", "1"), definition("c.yaml", "C", "1")))
	first := idsOf(t, definitions(t, s, "lib", "").Definitions)

	// The next version changes b, drops c, and adds d, in another order.
	// Those still defined keep their IDs; d gets a new one, never c's.
	record(t, s, success("lib", "v2", t0.Add(time.Minute), "a.yaml", "b.yaml", "d.yaml"))
	l := loadOf("lib", "v2", t0.Add(time.Minute), definition("b.yaml", "B", "2"), definition("a.yaml", "A", "1"),
		definition("d.yaml", "D", "2"), definition("", "Dell", "1"))
	recordLoad(t, s, l)
	got := definitions(t, s, "lib", "")
	if diff := cmp.Diff(l.Definitions, got.Definitions, withoutIDs); diff != "" {
		t.Errorf("Definitions after the second load (-want +got):\n%s", diff)
	}
	second := idsOf(t, got.Definitions)
	for _, k := range []store.DefinitionKey{key("", "Dell"), key("a.yaml", "A"), key("b.yaml", "B")} {
		if first[k] != second[k] {
			t.Errorf("%s had the ID %d, and now has %d", k, first[k], second[k])
		}
	}
	if d := second[key("d.yaml", "D")]; slices.Contains(slices.Collect(maps.Values(first)), d) {
		t.Errorf("d.yaml has the ID %d, which another definition had", d)
	}

	// The same object in another file is another definition, and a load
	// with none removes them all; the next to define them gets new IDs.
	moved := definition("a2.yaml", "A", "3")
	recordLoad(t, s, loadOf("lib", "v2", t0.Add(2*time.Minute), moved))
	if id := idsOf(t, definitions(t, s, "lib", "").Definitions)[moved.DefinitionKey]; id == second[key("a.yaml", "A")] {
		t.Errorf("a definition moved to another file kept its ID, %d", id)
	}
	recordLoad(t, s, loadOf("lib", "v2", t0.Add(3*time.Minute)))
	if got := definitions(t, s, "lib", ""); len(got.Definitions) != 0 || got.State() != store.LoadReady {
		t.Errorf("after a load of nothing: %d definitions, state %q", len(got.Definitions), got.State())
	}
	recordLoad(t, s, loadOf("lib", "v2", t0.Add(4*time.Minute), definition("a.yaml", "A", "1")))
	if id := idsOf(t, definitions(t, s, "lib", "").Definitions)[key("a.yaml", "A")]; id == first[key("a.yaml", "A")] {
		t.Errorf("a.yaml, defined again, got the ID %d it had before it went", id)
	}
}

func testLoadFailure(t *testing.T, s store.Store) {
	record(t, s, success("lib", "v1", t0, "a.yaml"))
	ok := loadOf("lib", "v1", t0, definition("a.yaml", "A", "1"))
	ok.Problems = []store.Problem{{Severity: "warning", Path: "a.yaml", Reason: "x"}}
	recordLoad(t, s, ok)
	before := definitions(t, s, "lib", "")

	// A failed load records the attempt, and leaves what the last load
	// that succeeded recorded.
	failed := store.Load{
		Source: "lib", Version: "v1", Reader: "devicetype-library/2", Settings: "sha256:other",
		StartedAt: t0.Add(time.Hour), Error: "schema/devicetype.json doesn't compile",
	}
	recordLoad(t, s, failed)
	want := before
	want.Latest = store.LoadKey{Version: "v1", Reader: "devicetype-library/2", Settings: "sha256:other"}
	want.StartedAt, want.Error = failed.StartedAt, failed.Error
	got := definitions(t, s, "lib", "")
	if diff := cmp.Diff(want, got, equateEmpty); diff != "" {
		t.Errorf("Definitions after a failed load (-want +got):\n%s", diff)
	}
	// What failed is what a load by that reader with those settings would
	// read; the definitions held are still those of the load before.
	if got.State() != store.LoadFailed || !got.Failed("devicetype-library/2", "sha256:other") || got.Failed(reader, settings) ||
		!got.Current(reader, settings) {
		t.Errorf("after a failed load: State %q, Failed %t and %t, Current %t; want failed, true, false, true", got.State(),
			got.Failed("devicetype-library/2", "sha256:other"), got.Failed(reader, settings), got.Current(reader, settings))
	}
	// Once the source is fetched again, it failed with another version.
	record(t, s, success("lib", "v2", t0.Add(90*time.Minute), "a.yaml"))
	if got := loadStatus(t, s, "lib"); got.Failed("devicetype-library/2", "sha256:other") {
		t.Error("a load of v1 that failed counts as a failure of v2")
	}

	// The next that succeeds clears the error.
	recordLoad(t, s, loadOf("lib", "v2", t0.Add(2*time.Hour)))
	if got := loadStatus(t, s, "lib"); got.State() != store.LoadReady || got.Error != "" {
		t.Errorf("after a load that succeeded: state %q, error %q", got.State(), got.Error)
	}

	// A first load that fails records the attempt, and nothing loaded.
	record(t, s, success("other", "v1", t0, "a.yaml"))
	recordLoad(t, s, store.Load{Source: "other", Version: "v1", Reader: reader, Settings: settings, StartedAt: t0, Error: "failed"})
	if got := loadStatus(t, s, "other"); got.State() != store.LoadFailed || !got.Failed(reader, settings) || got.Current(reader, settings) || got.Loaded != (store.LoadKey{}) {
		t.Errorf("after a first load that failed: %+v", got)
	}
}

func testLoadsNeedTheSnapshot(t *testing.T, s store.Store) {
	ctx := t.Context()
	if _, err := s.LoadStatus(ctx, "lib"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("LoadStatus of a source never fetched: %v, want ErrNotFound", err)
	}
	if _, err := s.Definitions(ctx, "lib", ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Definitions of a source never fetched: %v, want ErrNotFound", err)
	}
	if err := s.RecordLoad(ctx, loadOf("lib", "v1", t0)); !errors.Is(err, store.ErrSnapshotChanged) {
		t.Errorf("RecordLoad of a source never fetched: %v, want ErrSnapshotChanged", err)
	}
	record(t, s, failure("lib", t0, "unreachable"))
	if _, err := s.LoadStatus(ctx, "lib"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("LoadStatus of a source with no snapshot: %v, want ErrNotFound", err)
	}
	if err := s.RecordLoad(ctx, loadOf("lib", "v1", t0)); !errors.Is(err, store.ErrSnapshotChanged) {
		t.Errorf("RecordLoad of a source with no snapshot: %v, want ErrSnapshotChanged", err)
	}

	// A load of another version than the snapshot's records nothing, even
	// a failure.
	record(t, s, success("lib", "v2", t0.Add(time.Second), "a.yaml"))
	for _, l := range []store.Load{
		loadOf("lib", "v1", t0, definition("a.yaml", "A", "1")),
		{Source: "lib", Version: "v1", Reader: reader, Settings: settings, StartedAt: t0, Error: "failed"},
	} {
		if err := s.RecordLoad(ctx, l); !errors.Is(err, store.ErrSnapshotChanged) {
			t.Errorf("RecordLoad of another version: %v, want ErrSnapshotChanged", err)
		}
	}
	if diff := cmp.Diff(store.SourceDefinitions{LoadStatus: store.LoadStatus{Source: "lib", SnapshotVersion: "v2", SnapshotRef: "main"}},
		definitions(t, s, "lib", ""), equateEmpty); diff != "" {
		t.Errorf("Definitions after loads of another version (-want +got):\n%s", diff)
	}
}

func testDefinitionReads(t *testing.T, s store.Store) {
	for _, read := range []func() error{
		func() error { _, err := s.UniqueValues(t.Context(), "lib"); return err },
		func() error { _, err := s.DefinitionsByID(t.Context(), "lib", []int64{1}); return err },
	} {
		if err := read(); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("reading the definitions of a source never fetched = %v, want ErrNotFound", err)
		}
	}
	dell, a, b := definition("", "Dell", "v1"), definition("Dell/a.yaml", "A", "v1"), definition("Dell/b.yaml", "B", "v1")
	record(t, s, success("lib", "v1", t0, "Dell/a.yaml", "Dell/b.yaml"))
	load := loadOf("lib", "v1", t0, dell, a, b)
	load.Problems = []store.Problem{{Severity: "warning", Path: "Dell/a.yaml", Reason: "odd"}}
	recordLoad(t, s, load)
	other := loadDefinitions(t, s, "other", "x1", t0, definition("Dell/a.yaml", "A", "x1"))
	whole := definitions(t, s, "lib", "")
	ids := idsOf(t, whole.Definitions)

	// Unique values: every definition, without its JSON, with the
	// problems, as they were at one moment.
	unique, err := s.UniqueValues(t.Context(), "lib")
	if err != nil {
		t.Fatal(err)
	}
	want := whole
	want.Definitions = slices.Clone(whole.Definitions)
	for i := range want.Definitions {
		want.Definitions[i].JSON = nil
	}
	if diff := cmp.Diff(want, unique, equateEmpty); diff != "" {
		t.Errorf("UniqueValues (-want +got):\n%s", diff)
	}

	// By ID: those of the source's definitions named, whole, in the
	// format's order; not another source's, nor IDs of nothing; and no
	// problems.
	byID, err := s.DefinitionsByID(t.Context(), "lib", []int64{ids[b.DefinitionKey], 0, other.ids[a.DefinitionKey], ids[dell.DefinitionKey], 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	want = store.SourceDefinitions{LoadStatus: whole.LoadStatus, Definitions: []store.Definition{whole.Definitions[0], whole.Definitions[2]}}
	if diff := cmp.Diff(want, byID, equateEmpty); diff != "" {
		t.Errorf("DefinitionsByID (-want +got):\n%s", diff)
	}
	for _, none := range [][]int64{nil, {}} {
		byID, err := s.DefinitionsByID(t.Context(), "lib", none)
		if err != nil || len(byID.Definitions) != 0 || byID.LoadStatus != whole.LoadStatus {
			t.Errorf("DefinitionsByID(%v) = %+v, %v; want the status alone", none, byID, err)
		}
	}
}

func testLoadsRemoved(t *testing.T, s store.Store) {
	for _, name := range []string{"a", "b"} {
		record(t, s, success(name, "v1", t0, "x.yaml"))
		l := loadOf(name, "v1", t0, definition("x.yaml", "X", name))
		l.Problems = []store.Problem{{Severity: "warning", Path: "x.yaml", Reason: "x"}}
		recordLoad(t, s, l)
	}
	// The source a's latest load failed, and is removed all the same.
	recordLoad(t, s, store.Load{Source: "a", Version: "v1", Reader: reader, Settings: settings, StartedAt: t0.Add(time.Second), Error: "failed"})
	removed, err := s.RemoveSources(t.Context(), By, []string{"b"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"a"}, removed); diff != "" {
		t.Errorf("RemoveSources (-want +got):\n%s", diff)
	}
	if _, err := s.Definitions(t.Context(), "a", ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Definitions of a removed source: %v, want ErrNotFound", err)
	}
	if got := definitions(t, s, "b", ""); len(got.Definitions) != 1 || len(got.Problems) != 1 {
		t.Errorf("the source kept has %d definitions and %d problems, want 1 and 1", len(got.Definitions), len(got.Problems))
	}

	// Fetched again, it starts with nothing loaded.
	record(t, s, success("a", "v1", t0.Add(time.Minute), "x.yaml"))
	if diff := cmp.Diff(store.SourceDefinitions{LoadStatus: store.LoadStatus{Source: "a", SnapshotVersion: "v1", SnapshotRef: "main"}},
		definitions(t, s, "a", ""), equateEmpty); diff != "" {
		t.Errorf("Definitions of a source fetched again (-want +got):\n%s", diff)
	}
}

func testInvalidLoads(t *testing.T, s store.Store) {
	record(t, s, success("lib", "v1", t0, "a.yaml"))
	good := func() store.Load {
		l := loadOf("lib", "v1", t0, definition("a.yaml", "A", "1"), definition("", "Dell", "1"))
		l.Problems = []store.Problem{{Severity: "warning", Path: "a.yaml", Reason: "x"}}
		return l
	}
	tests := []struct {
		name   string
		change func(*store.Load)
	}{
		{"no source", func(l *store.Load) { l.Source = "" }},
		{"no version", func(l *store.Load) { l.Version = "" }},
		{"no reader", func(l *store.Load) { l.Reader = "" }},
		{"reader not text", func(l *store.Load) { l.Reader = "a\xff" }},
		{"no settings", func(l *store.Load) { l.Settings = "" }},
		{"no time", func(l *store.Load) { l.StartedAt = time.Time{} }},
		{"failed, with definitions", func(l *store.Load) { l.Error = "failed" }},
		{"more invalid files than files", func(l *store.Load) { l.InvalidFiles = l.Files + 1 }},
		{"negative files", func(l *store.Load) { l.Files = -1 }},
		{"a definition twice", func(l *store.Load) { l.Definitions = append(l.Definitions, l.Definitions[0]) }},
		{"no object type", func(l *store.Load) { l.Definitions[0].ObjectType = "" }},
		{"identity not JSON", func(l *store.Load) { l.Definitions[0].Identity = "{" }},
		{"definition not JSON", func(l *store.Load) { l.Definitions[0].JSON = []byte("{") }},
		{"unique values not JSON", func(l *store.Load) { l.Definitions[0].UniqueValues = []byte("{") }},
		{"no unique values", func(l *store.Load) { l.Definitions[0].UniqueValues = nil }},
		{"a file without a digest", func(l *store.Load) { l.Definitions[0].Digest = "" }},
		{"a bad digest", func(l *store.Load) { l.Definitions[0].Digest = "sha256:x" }},
		{"a derived definition with a digest", func(l *store.Load) { l.Definitions[1].Digest = l.Definitions[0].Digest }},
		{"a model not text", func(l *store.Load) { l.Definitions[0].Model = "\x00" }},
		{"an unknown severity", func(l *store.Load) { l.Problems[0].Severity = "info" }},
		{"a negative line", func(l *store.Load) { l.Problems[0].Line = -1 }},
		{"a problem without a reason", func(l *store.Load) { l.Problems[0].Reason = "" }},
	}
	for _, tt := range tests {
		l := good()
		tt.change(&l)
		if err := s.RecordLoad(t.Context(), l); err == nil {
			t.Errorf("%s: RecordLoad succeeded", tt.name)
		}
	}
	if diff := cmp.Diff(store.SourceDefinitions{LoadStatus: store.LoadStatus{Source: "lib", SnapshotVersion: "v1", SnapshotRef: "main"}},
		definitions(t, s, "lib", ""), equateEmpty); diff != "" {
		t.Errorf("Definitions after invalid loads (-want +got):\n%s", diff)
	}
	recordLoad(t, s, good())
}

func testLoadText(t *testing.T, s store.Store) {
	// What a problem quotes from a file, and a load's error, can hold
	// what PostgreSQL's text can't, which is replaced, as a fetch's error
	// is.
	record(t, s, success("lib", "v1", t0, "a.yaml"))
	l := loadOf("lib", "v1", t0)
	l.Problems = []store.Problem{{Severity: "error", Path: "a.yaml", Field: "x\x00y", Reason: "bad \xff value \x00"}}
	recordLoad(t, s, l)
	want := []store.Problem{{Severity: "error", Path: "a.yaml", Field: "x�y", Reason: "bad � value �"}}
	if diff := cmp.Diff(want, definitions(t, s, "lib", "").Problems); diff != "" {
		t.Errorf("Problems (-want +got):\n%s", diff)
	}
	recordLoad(t, s, store.Load{Source: "lib", Version: "v1", Reader: reader, Settings: settings, StartedAt: t0, Error: "can't \xff\x00"})
	if got := loadStatus(t, s, "lib").Error; got != "can't ��" {
		t.Errorf("Error = %q", got)
	}
}

func testSearch(t *testing.T, s store.Store) {
	for _, name := range []string{"lib", "other"} {
		record(t, s, success(name, "v1", t0, "r640.yaml", "switch.yaml", "abc.yaml"))
	}
	r640 := definition("r640.yaml", "PowerEdge R640", "1")
	r640.PartNumber = "PE-R640"
	sw := definition("switch.yaml", "UniFi 100%_Switch", "1")
	sw.Manufacturer, sw.PartNumber = "Ubiquiti", ""
	abc := definition("abc.yaml", "Größe", "1")
	abc.Manufacturer = "ÄBC"
	dell := definition("", "Dell", "1")
	l := loadOf("lib", "v1", t0, dell, r640, sw, abc)
	l.Problems = []store.Problem{{Severity: "warning", Path: "abc.yaml", Reason: "x"}}
	recordLoad(t, s, l)
	recordLoad(t, s, loadOf("other", "v1", t0, definition("r640.yaml", "PowerEdge R640", "1")))

	for _, tt := range []struct {
		search string
		want   []store.Definition
	}{
		{"", []store.Definition{dell, r640, sw, abc}},
		// The manufacturer, model and part number, ignoring case.
		{"dell", []store.Definition{dell, r640}},
		{"r640", []store.Definition{r640}},
		{"POWEREDGE r6", []store.Definition{r640}},
		{"pe-r640", []store.Definition{r640}},
		{"äbc", []store.Definition{abc}},
		{"GRÖ", []store.Definition{abc}},
		// Not the slug, and never across two of them.
		{"dell-poweredge", nil},
		{"dellpoweredge", nil},
		// % and _ are themselves.
		{"100%_s", []store.Definition{sw}},
		{"%", []store.Definition{sw}},
		{"_", []store.Definition{sw}},
		{"0%s", nil},
		{"r6_0", nil},
		{"x", nil},
	} {
		got := definitions(t, s, "lib", tt.search)
		if diff := cmp.Diff(tt.want, got.Definitions, equateEmpty, withoutIDs); diff != "" {
			t.Errorf("Definitions(%q) (-want +got):\n%s", tt.search, diff)
		}
		// Every problem, whatever the search.
		if len(got.Problems) != 1 {
			t.Errorf("Definitions(%q) gives %d problems, want 1", tt.search, len(got.Problems))
		}
	}
	for _, search := range []string{"dell\npoweredge", "a\tb", "\x00", "a\xff"} {
		if _, err := s.Definitions(t.Context(), "lib", search); err == nil {
			t.Errorf("Definitions(%q) succeeded", search)
		}
	}
}

func testLargeLoad(t *testing.T, s store.Store) {
	// Definitions of the size devicetype-library's device types have, more
	// than PostgreSQL's store writes in one statement, loaded twice, the
	// second time with half of them changed: every one keeps its ID. The
	// whole library, 14,000, takes about a second on SQLite, but half a
	// minute with the race detector.
	const n = 2500
	record(t, s, success("lib", "v1", t0, "x"))
	padding := strings.Repeat("x", 1000)
	defs := make([]store.Definition, n)
	for i := range n {
		defs[i] = definition(fmt.Sprintf("device-types/M%03d/%05d.yaml", i%400, i), fmt.Sprintf("Model %d", i), "1")
		defs[i].JSON = fmt.Appendf(nil, `{"padding":%q,"n":%d}`, padding, i)
	}
	recordLoad(t, s, loadOf("lib", "v1", t0, defs...))
	first := idsOf(t, definitions(t, s, "lib", "").Definitions)
	for i := 0; i < n; i += 2 {
		defs[i].JSON = fmt.Appendf(nil, `{"padding":%q,"n":%d,"changed":true}`, padding, i)
	}
	recordLoad(t, s, loadOf("lib", "v1", t0.Add(time.Second), defs...))
	got := definitions(t, s, "lib", "")
	// Compared one by one, since cmp takes minutes over so many.
	if len(got.Definitions) != n {
		t.Fatalf("Definitions gives %d, want %d", len(got.Definitions), n)
	}
	for i, d := range got.Definitions {
		id := d.ID
		d.ID = 0
		if !reflect.DeepEqual(defs[i], d) {
			t.Fatalf("definition %d is %+v, want %+v", i, d, defs[i])
		}
		if id != first[d.DefinitionKey] {
			t.Fatalf("definition %d had the ID %d, and now has %d", i, first[d.DefinitionKey], id)
		}
	}
	if found := definitions(t, s, "lib", "model 2499").Definitions; len(found) != 1 || found[0].Model != "Model 2499" {
		t.Errorf("searching for model 2499 found %d", len(found))
	}
}

func testLoadsReadsAgree(t *testing.T, s store.Store) {
	// Loads that replace what the last recorded race reads: each read sees
	// one load's definitions, all of them, and its status.
	record(t, s, success("lib", "v1", t0, "a.yaml", "b.yaml", "c.yaml"))
	loadN := func(i int) store.Load {
		var defs []store.Definition
		for _, p := range []string{"a.yaml", "b.yaml", "c.yaml"}[:i%3+1] {
			defs = append(defs, definition(p, strings.ToUpper(p[:1]), strconv.Itoa(i)))
		}
		l := loadOf("lib", "v1", t0.Add(time.Duration(i)*time.Second), defs...)
		l.Reader = "devicetype-library/" + strconv.Itoa(i)
		l.Problems = make([]store.Problem, i%4)
		for j := range l.Problems {
			l.Problems[j] = store.Problem{Severity: "warning", Path: "a.yaml", Reason: strconv.Itoa(i)}
		}
		return l
	}
	recordLoad(t, s, loadN(0))
	const loads = 40
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 1; i <= loads; i++ {
			if err := s.RecordLoad(t.Context(), loadN(i)); err != nil {
				t.Errorf("RecordLoad %d: %v", i, err)
				return
			}
		}
	})
	wg.Go(func() {
		for range 4 * loads {
			got, err := s.Definitions(t.Context(), "lib", "")
			if err != nil {
				t.Errorf("Definitions: %v", err)
				return
			}
			i, err := strconv.Atoi(strings.TrimPrefix(got.Loaded.Reader, "devicetype-library/"))
			if err != nil {
				t.Errorf("Definitions gives the reader %q", got.Loaded.Reader)
				return
			}
			want := loadN(i)
			if diff := cmp.Diff(want.Definitions, got.Definitions, withoutIDs); diff != "" {
				t.Errorf("Definitions of load %d (-want +got):\n%s", i, diff)
				return
			}
			if diff := cmp.Diff(want.Problems, got.Problems, equateEmpty); diff != "" {
				t.Errorf("problems of load %d (-want +got):\n%s", i, diff)
				return
			}
		}
	})
	wg.Wait()
}

func testLoadsWhileFetching(t *testing.T, s store.Store) {
	// A load of a version races fetches of the next, and removals of the
	// source: each recording either finds the version it read, and records
	// it whole, or records nothing, and nothing else fails.
	for i := range 30 {
		at := t0.Add(time.Duration(i) * time.Second)
		version := fmt.Sprintf("v%d", i)
		record(t, s, success("lib", version, at, "a.yaml", "b.yaml"))
		l := loadOf("lib", version, at, definition("a.yaml", "A", version), definition("b.yaml", "B", version))
		var loadErr error
		var wg sync.WaitGroup
		wg.Go(func() { loadErr = s.RecordLoad(t.Context(), l) })
		wg.Go(func() {
			var err error
			if i%2 == 0 {
				err = s.RecordFetch(t.Context(), By, success("lib", version+"-next", at.Add(time.Millisecond), "a.yaml"))
			} else {
				_, err = s.RemoveSources(t.Context(), By, nil)
			}
			if err != nil {
				t.Errorf("round %d: %v", i, err)
			}
		})
		wg.Wait()
		got, err := s.Definitions(t.Context(), "lib", "")
		switch {
		case i%2 == 1:
			// Recorded or not, it went with the source.
			if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("round %d: Definitions of a removed source: %v", i, err)
			}
			if loadErr != nil && !errors.Is(loadErr, store.ErrSnapshotChanged) {
				t.Errorf("round %d: RecordLoad: %v", i, loadErr)
			}
		case err != nil:
			t.Fatalf("round %d: Definitions: %v", i, err)
		case loadErr == nil:
			if got.Loaded.Version != version || len(got.Definitions) != 2 {
				t.Errorf("round %d: the load of %s isn't recorded whole: version %q, %d definitions", i, version, got.Loaded.Version, len(got.Definitions))
			}
		case errors.Is(loadErr, store.ErrSnapshotChanged):
			if got.Loaded.Version == version {
				t.Errorf("round %d: the load of %s is recorded, though RecordLoad failed", i, version)
			}
		default:
			t.Errorf("round %d: RecordLoad: %v", i, loadErr)
		}
	}
}

// SQL is what the migration tests need of an engine to fill a database at
// an older schema directly, since the store's methods write what the latest
// schema holds: a statement run, and bytes written as a literal.
type SQL struct {
	Exec  func(t *testing.T, statement string)
	Bytes func(b []byte) string
}

// recordInSQL records f, in SQL that db runs, as RecordFetch did before the
// audit log: a store at an older schema can't record a fetch, since
// RecordFetch records audit events too.
func recordInSQL(t *testing.T, db SQL, f store.Fetch) {
	t.Helper()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	at := quote(f.FetchedAt.UTC().Format("2006-01-02T15:04:05.000000Z"))
	fetchErr := "NULL"
	if f.Error != "" {
		fetchErr = quote(f.Error)
	}
	src := f.Source
	db.Exec(t, fmt.Sprintf(`INSERT INTO sources (name, kind, url, ref, fetched_at, error) VALUES (%s, %s, %s, %s, %s, %s)`,
		quote(src.Name), quote(src.Kind), quote(src.URL), quote(src.Ref), at, fetchErr))
	if f.Error != "" {
		return
	}
	db.Exec(t, fmt.Sprintf(`INSERT INTO snapshots (source, url, version, ref, fetched_at) VALUES (%s, %s, %s, %s, %s)`,
		quote(src.Name), quote(src.URL), quote(f.Version), quote(f.Ref), at))
	for _, file := range f.Files {
		db.Exec(t, fmt.Sprintf(`INSERT INTO snapshot_files (source, path, size, digest) VALUES (%s, %s, %d, %s)`,
			quote(src.Name), quote(file.Path), file.Size, quote(file.Digest)))
	}
	for i, sk := range f.Skipped {
		db.Exec(t, fmt.Sprintf(`INSERT INTO snapshot_skipped (source, seq, path, reason) VALUES (%s, %d, %s, %s)`,
			quote(src.Name), i, db.Bytes([]byte(sk.Path)), quote(sk.Reason)))
	}
}

// FillForMigration records, in a store at schema version 2, what
// migrations since must keep: sources with and without a snapshot, and the
// snapshot's lists. It returns a function that checks that a store,
// migrated since, holds all of it as it was, that a source starts with
// nothing loaded, and that no comparison is left, since comparisons
// recorded then are of no definition: each engine's test records one, in
// SQL, since RecordComparisons records them otherwise now.
func FillForMigration(t *testing.T, s store.Store, db SQL) (check func(*testing.T, store.Store)) {
	t.Helper()
	ok := success("lib", "v1", t0, "Dell/a.yaml", "Dell/b.yaml")
	ok.Skipped = []store.Skipped{{Path: "big.png", Reason: "too_large"}}
	recordInSQL(t, db, ok)
	recordInSQL(t, db, failure("down", t0, "unreachable"))
	db.Exec(t, `INSERT INTO comparisons (
		source, object_type, path, identity, source_version, compared_at,
		netbox_url, netbox_version, outcome, netbox_id, differences, result
	) VALUES (
		'lib', 'dcim.devicetype', 'Dell/a.yaml', '{"manufacturer":{"name":"Dell"},"model":"A"}', 'v1', '2026-10-26T01:02:04.456789Z',
		'https://netbox.example.com/', '4.7.2', 'candidate', 4, 1, '{}'
	)`)
	sources, err := s.Sources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return func(t *testing.T, s store.Store) {
		t.Helper()
		got, err := s.Sources(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(sources, got, equateEmpty); diff != "" {
			t.Errorf("Sources after migrating (-before +after):\n%s", diff)
		}
		checkSnapshot(t, s, snapshotOf(ok))
		// Comparisons recorded before definitions were are of no
		// definition, and go: the next compare makes them again.
		checkComparisons(t, s, "lib", nil)
		if diff := cmp.Diff(store.LoadStatus{Source: "lib", SnapshotVersion: "v1", SnapshotRef: "main"}, loadStatus(t, s, "lib")); diff != "" {
			t.Errorf("LoadStatus after migrating (-want +got):\n%s", diff)
		}
		recordLoad(t, s, loadOf("lib", "v1", t0.Add(time.Minute), definition("Dell/a.yaml", "A", "v1")))
	}
}

// FillForComparisonsMigration records, in a store at schema version 3, in
// SQL that exec runs, since the store records them otherwise now: a load of
// a source's definitions, and comparisons of some of them, of a definition
// it hasn't got, and with an outcome compare doesn't give. It returns a
// function that checks that a store, migrated since, keeps the comparisons
// of the definitions it has, with their outcomes, by their IDs, and
// removes the others; that the definitions' unique values read as empty;
// and that a load of them keeps their IDs, and their comparisons.
func FillForComparisonsMigration(t *testing.T, s store.Store, db SQL) (check func(*testing.T, store.Store)) {
	t.Helper()
	recordInSQL(t, db, success("lib", "v1", t0, "Dell/a.yaml", "Dell/b.yaml"))
	const at = "2026-10-26T01:02:03.456789Z"
	db.Exec(t, fmt.Sprintf(`INSERT INTO definition_loads (
		source, started_at, version, reader, settings, error,
		loaded_at, loaded_version, loaded_reader, loaded_settings, files, invalid_files
	) VALUES ('lib', '%[1]s', 'v1', '%[2]s', '%[3]s', NULL, '%[1]s', 'v1', '%[2]s', '%[3]s', 2, 0)`, at, reader, settings))
	defs := []store.Definition{definition("", "Dell", "v1"), definition("Dell/a.yaml", "A", "v1"), definition("Dell/b.yaml", "B", "v1")}
	for i, d := range defs {
		// As a load records it, but for its unique values, so that a load
		// rewrites the row for them alone.
		db.Exec(t, fmt.Sprintf(`INSERT INTO definitions (
			source, object_type, path, identity, seq, manufacturer, model, slug, part_number, search, digest, definition
		) VALUES ('lib', '%s', '%s', '%s', %d, '%s', '%s', '%s', '%s', '%s', '%s', '%s')`,
			d.ObjectType, d.Path, d.Identity, i, d.Manufacturer, d.Model, d.Slug, d.PartNumber, d.SearchText(), d.Digest, d.JSON))
	}
	for _, c := range []struct{ path, identity, outcome, netboxID, result string }{
		{"", `{"name":"Dell"}`, "candidate", "3", `{"n":1}`},
		{"Dell/a.yaml", `{"manufacturer":{"name":"Dell"},"model":"A"}`, "unknown", "NULL", `{"n":2}`},
		{"Dell/b.yaml", `{"manufacturer":{"name":"Dell"},"model":"B"}`, "absent", "NULL", `{"n":3}`},
		{"Dell/gone.yaml", `{"manufacturer":{"name":"Dell"},"model":"Gone"}`, "absent", "NULL", `{"n":4}`},
	} {
		typ := "dcim.devicetype"
		if c.path == "" {
			typ = "dcim.manufacturer"
		}
		db.Exec(t, fmt.Sprintf(`INSERT INTO comparisons (
			source, object_type, path, identity, source_version, compared_at,
			netbox_url, netbox_version, outcome, netbox_id, differences, result
		) VALUES ('lib', '%s', '%s', '%s', 'v1', '2026-10-26T01:02:04.456789Z', 'https://netbox.example.com/', '4.7.2', '%s', %s, 0, '%s')`,
			typ, c.path, c.identity, c.outcome, c.netboxID, c.result))
	}
	return func(t *testing.T, s store.Store) {
		t.Helper()
		unique, err := s.UniqueValues(t.Context(), "lib")
		if err != nil {
			t.Fatal(err)
		}
		ids := idsOf(t, unique.Definitions)
		for _, d := range unique.Definitions {
			if len(d.UniqueValues) != 0 {
				t.Errorf("%s has the unique values %s after migrating, want none", d.DefinitionKey, d.UniqueValues)
			}
		}
		l := loaded{status: unique.LoadStatus, ids: ids}
		r := l.run(time.Date(2026, 10, 26, 1, 2, 4, 456789000, time.UTC), false,
			l.comparison(defs[2].DefinitionKey, "absent", 0), l.comparison(defs[0].DefinitionKey, "candidate", 3))
		want := recorded(r, l.comparison(defs[2].DefinitionKey, "absent", 0), l.comparison(defs[0].DefinitionKey, "candidate", 3))
		want[0].Result, want[0].Differences = []byte(`{"n":3}`), 0
		want[1].Result, want[1].Differences = []byte(`{"n":1}`), 0
		checkComparisons(t, s, "lib", want)

		// A load writes the unique values, keeping the IDs, and so the
		// comparisons.
		again := reload(t, s, "lib", "v1", t0.Add(time.Minute), defs...)
		if diff := cmp.Diff(ids, again.ids); diff != "" {
			t.Errorf("the IDs after a load (-before +after):\n%s", diff)
		}
		unique, err = s.UniqueValues(t.Context(), "lib")
		if err != nil {
			t.Fatal(err)
		}
		for i, d := range unique.Definitions {
			if string(d.UniqueValues) != string(defs[i].UniqueValues) {
				t.Errorf("%s has the unique values %s after a load, want %s", d.DefinitionKey, d.UniqueValues, defs[i].UniqueValues)
			}
		}
		checkComparisons(t, s, "lib", want)
	}
}

// MigrationsUpTo returns an engine's migrations, from the directory
// migrations in files, up to version, for opening a database at that
// version, and the migrations after it.
func MigrationsUpTo(t *testing.T, files fs.FS, version int64) (fs.FS, []store.Migration) {
	t.Helper()
	entries, err := fs.ReadDir(files, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	upTo := fstest.MapFS{}
	var after []store.Migration
	for _, e := range entries {
		v, err := strconv.ParseInt(strings.SplitN(e.Name(), "_", 2)[0], 10, 64)
		if err != nil {
			t.Fatalf("migration %s: %v", e.Name(), err)
		}
		if v > version {
			after = append(after, store.Migration{Version: v, Name: e.Name()})
			continue
		}
		data, err := fs.ReadFile(files, "migrations/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		upTo[e.Name()] = &fstest.MapFile{Data: data}
	}
	return upTo, after
}
