// Package storetest holds the tests that every store engine must pass, so
// that the engines behave alike. Each engine's tests call Run with a
// function that opens a new, empty store of that engine, migrated to the
// latest schema.
//
// The tests cover what store.Store promises, not how an engine keeps it:
// that is for the engine's own tests, such as its migrations, locking and
// files.
package storetest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
)

// Open returns a new, empty store, and closes it when the test ends.
type Open func(t *testing.T) store.Store

// Run runs every test against stores that open returns, each test with a
// store of its own, in parallel.
func Run(t *testing.T, open Open) {
	t.Helper()
	tests := []struct {
		name string
		test func(*testing.T, store.Store)
	}{
		{"a successful fetch", testSuccess},
		{"a failed fetch", testFailure},
		{"a failed fetch keeps the snapshot", testFailureKeepsSnapshot},
		{"each snapshot replaces the lists", testReplace},
		{"each fetch replaces the last, whatever its time", testLatestRecorded},
		{"removing sources", testRemove},
		{"text from sources", testText},
		{"invalid fetches record nothing", testInvalid},
		{"times", testTimes},
		{"names and paths sort as bytes", testSorting},
		{"concurrent fetches", testConcurrent},
		{"snapshots read while fetches are recorded", testReadsAgree},
		{"removing sources while they are recorded", testRemoveWhileRecording},
		{"a large file list", testLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.test(t, open(t))
		})
	}
}

// t0 is when the first fetch in a test starts.
var t0 = time.Date(2026, 10, 26, 1, 2, 3, 456789000, time.UTC)

// equateEmpty lets an engine return an empty list as nil or not.
var equateEmpty = cmpopts.EquateEmpty()

func sourceConfig(name string) store.SourceConfig {
	return store.SourceConfig{Name: name, Kind: "git", URL: "https://git.example.com/" + name + ".git", Ref: "main"}
}

// success returns a successful fetch of a source at version, holding files
// with the given paths, whose content depends on the version.
func success(name, version string, at time.Time, paths ...string) store.Fetch {
	f := store.Fetch{Source: sourceConfig(name), FetchedAt: at, Version: version, Ref: "main"}
	for _, p := range paths {
		f.Files = append(f.Files, file(p, version))
	}
	return f
}

func file(path, version string) store.File {
	content := path + "@" + version
	sum := sha256.Sum256([]byte(content))
	return store.File{Path: path, Size: int64(len(content)), Digest: "sha256:" + hex.EncodeToString(sum[:])}
}

func failure(name string, at time.Time, msg string) store.Fetch {
	return store.Fetch{Source: sourceConfig(name), FetchedAt: at, Error: msg}
}

// snapshotOf is the snapshot that RecordFetch(f) records, with its files
// sorted by path.
func snapshotOf(f store.Fetch) store.Snapshot {
	files := slices.Clone(f.Files)
	slices.SortFunc(files, func(a, b store.File) int { return strings.Compare(a.Path, b.Path) })
	return store.Snapshot{
		Source: f.Source.Name, URL: f.Source.URL, Version: f.Version, Ref: f.Ref,
		FetchedAt: f.FetchedAt.UTC().Truncate(time.Microsecond), Files: files, Skipped: f.Skipped,
	}
}

// summary is a snapshot as Sources returns it, without its lists.
func summary(snap store.Snapshot) *store.Snapshot {
	snap.Files, snap.Skipped = nil, nil
	return &snap
}

func record(t *testing.T, s store.Store, f store.Fetch) {
	t.Helper()
	if err := s.RecordFetch(t.Context(), f); err != nil {
		t.Fatalf("RecordFetch: %v", err)
	}
}

func sources(t *testing.T, s store.Store) []store.Source {
	t.Helper()
	got, err := s.Sources(t.Context())
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	return got
}

func snapshot(t *testing.T, s store.Store, name string) store.Snapshot {
	t.Helper()
	got, err := s.Snapshot(t.Context(), name)
	if err != nil {
		t.Fatalf("Snapshot(%q): %v", name, err)
	}
	return got
}

func checkSnapshot(t *testing.T, s store.Store, want store.Snapshot) {
	t.Helper()
	if diff := cmp.Diff(want, snapshot(t, s, want.Source), equateEmpty); diff != "" {
		t.Errorf("Snapshot(%q) (-want +got):\n%s", want.Source, diff)
	}
}

func checkNoSnapshot(t *testing.T, s store.Store, name string) {
	t.Helper()
	if snap, err := s.Snapshot(t.Context(), name); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Snapshot(%q) = %+v, %v; want an error wrapping ErrNotFound", name, snap, err)
	}
}

func testSuccess(t *testing.T, s store.Store) {
	f := success("lib", "v1", t0, "a.yaml", "b/c.yaml")
	f.Skipped = []store.Skipped{{Path: "big.png", Reason: "too_large"}, {Path: "link", Reason: "symlink"}}
	record(t, s, f)

	want := []store.Source{{SourceConfig: sourceConfig("lib"), FetchedAt: t0, Snapshot: summary(snapshotOf(f))}}
	if diff := cmp.Diff(want, sources(t, s), equateEmpty); diff != "" {
		t.Errorf("Sources (-want +got):\n%s", diff)
	}
	checkSnapshot(t, s, snapshotOf(f))
}

func testFailure(t *testing.T, s store.Store) {
	record(t, s, failure("lib", t0, "can't reach git.example.com"))

	want := []store.Source{{SourceConfig: sourceConfig("lib"), FetchedAt: t0, Error: "can't reach git.example.com"}}
	if diff := cmp.Diff(want, sources(t, s), equateEmpty); diff != "" {
		t.Errorf("Sources (-want +got):\n%s", diff)
	}
	checkNoSnapshot(t, s, "lib")
}

func testFailureKeepsSnapshot(t *testing.T, s store.Store) {
	ok := success("lib", "v1", t0, "a.yaml")
	record(t, s, ok)
	failed := failure("lib", t0.Add(time.Hour), "the source holds more than 1 files, its max_files")
	failed.Source.URL = "https://git.example.com/moved.git"
	record(t, s, failed)

	want := []store.Source{{
		SourceConfig: failed.Source,
		FetchedAt:    failed.FetchedAt,
		Error:        failed.Error,
		Snapshot:     summary(snapshotOf(ok)),
	}}
	if diff := cmp.Diff(want, sources(t, s), equateEmpty); diff != "" {
		t.Errorf("Sources (-want +got):\n%s", diff)
	}
	checkSnapshot(t, s, snapshotOf(ok))
}

func testReplace(t *testing.T, s store.Store) {
	steps := []struct {
		name    string
		version string
		files   []string
		skipped []store.Skipped
	}{
		{"first", "v1", []string{"a.yaml", "b.yaml", "c.yaml"}, []store.Skipped{{Path: "x.png", Reason: "too_large"}}},
		{"files changed, added and gone", "v2", []string{"b.yaml", "d.yaml"}, nil},
		{"no files", "v3", nil, []store.Skipped{{Path: "y", Reason: "symlink"}, {Path: "z", Reason: "submodule"}}},
		{"the same version again", "v3", []string{"e.yaml"}, nil},
	}
	for i, step := range steps {
		f := success("lib", step.version, t0.Add(time.Duration(i)*time.Minute), step.files...)
		f.Skipped = step.skipped
		record(t, s, f)
		if diff := cmp.Diff(snapshotOf(f), snapshot(t, s, "lib"), equateEmpty); diff != "" {
			t.Errorf("after the %s snapshot (-want +got):\n%s", step.name, diff)
		}
	}
}

func testLatestRecorded(t *testing.T, s store.Store) {
	// Fetches are recorded in the order they happen, and their times only
	// say when. A clock that went back, after one ran ahead, changes
	// nothing.
	ahead := t0.Add(365 * 24 * time.Hour)
	tests := []struct {
		name          string
		first, second store.Fetch // recorded in this order
		wantSnapshot  store.Fetch
	}{
		{"a success after one with a later time", success("a", "v2", ahead, "new.yaml"), success("a", "v1", t0, "old.yaml"), success("a", "v1", t0, "old.yaml")},
		{"a failure after a success with a later time", success("b", "v2", ahead, "new.yaml"), failure("b", t0, "down"), success("b", "v2", ahead, "new.yaml")},
		{"two fetches at the same time", success("c", "v1", t0, "one.yaml"), success("c", "v2", t0, "two.yaml"), success("c", "v2", t0, "two.yaml")},
	}
	for _, tt := range tests {
		record(t, s, tt.first)
		record(t, s, tt.second)
	}
	got := sources(t, s)
	if len(got) != len(tests) {
		t.Fatalf("Sources returned %d sources, want %d", len(got), len(tests))
	}
	for i, tt := range tests {
		snap := snapshotOf(tt.wantSnapshot)
		want := store.Source{SourceConfig: tt.second.Source, FetchedAt: tt.second.FetchedAt, Error: tt.second.Error, Snapshot: summary(snap)}
		if diff := cmp.Diff(want, got[i], equateEmpty); diff != "" {
			t.Errorf("%s: source (-want +got):\n%s", tt.name, diff)
		}
		checkSnapshot(t, s, snap)
	}
}

func testRemove(t *testing.T, s store.Store) {
	for _, name := range []string{"a", "b", "c"} {
		record(t, s, success(name, "v1", t0, name+".yaml"))
	}
	record(t, s, failure("d", t0, "down"))

	removed, err := s.RemoveSources(t.Context(), []string{"b", "not-recorded"})
	if err != nil {
		t.Fatalf("RemoveSources: %v", err)
	}
	if diff := cmp.Diff([]string{"a", "c", "d"}, removed); diff != "" {
		t.Errorf("RemoveSources removed (-want +got):\n%s", diff)
	}
	if got := sources(t, s); len(got) != 1 || got[0].Name != "b" {
		t.Errorf("Sources = %+v, want only b", got)
	}
	checkNoSnapshot(t, s, "a")

	// A source configured again starts afresh, with nothing of its old
	// lists left over.
	again := success("a", "v2", t0.Add(time.Hour), "other.yaml")
	record(t, s, again)
	checkSnapshot(t, s, snapshotOf(again))

	if removed, err := s.RemoveSources(t.Context(), []string{"a", "b"}); err != nil || len(removed) != 0 {
		t.Errorf("RemoveSources with every source kept = %v, %v; want none removed", removed, err)
	}
	if removed, err := s.RemoveSources(t.Context(), nil); err != nil || !cmp.Equal(removed, []string{"a", "b"}) {
		t.Errorf("RemoveSources with none kept = %v, %v; want a and b removed", removed, err)
	}
	if got := sources(t, s); len(got) != 0 {
		t.Errorf("Sources after removing them all = %+v", got)
	}
}

func testText(t *testing.T, s store.Store) {
	f := success("lib", "v1", t0, "é/ü.yaml", "emoji-🙂.yaml", "with space.yaml")
	f.Ref = "main\xff"
	// Unsafe paths keep their bytes, and two entries can share a path.
	f.Skipped = []store.Skipped{
		{Path: "bad-\xff\xfe.yaml", Reason: "unsafe_path"},
		{Path: "esc-\x1b[31m.yaml", Reason: "unsafe_path"},
		{Path: "nul-\x00.yaml", Reason: "unsafe_path"},
		{Path: "a/b", Reason: "unsafe_path"},
		{Path: "a/b", Reason: "unsafe_path"},
	}
	record(t, s, f)
	want := snapshotOf(f)
	want.Ref = "main�"
	checkSnapshot(t, s, want)

	// Error text from a server is made valid UTF-8 without NUL bytes.
	record(t, s, failure("other", t0, "server said \xff\x00 and \x1b[2J"))
	got := sources(t, s)
	if len(got) != 2 || got[1].Error != "server said �� and \x1b[2J" {
		t.Errorf("Sources = %+v, want other's error with invalid UTF-8 and NUL replaced", got)
	}
}

func testInvalid(t *testing.T, s store.Store) {
	valid := func() store.Fetch { return success("lib", "v1", t0, "a.yaml", "b.yaml") }
	tests := []struct {
		name   string
		change func(*store.Fetch)
	}{
		{"no source name", func(f *store.Fetch) { f.Source.Name = "" }},
		{"a source name that isn't UTF-8", func(f *store.Fetch) { f.Source.Name = "lib\xff" }},
		{"a URL with a NUL byte", func(f *store.Fetch) { f.Source.URL += "\x00" }},
		{"no time", func(f *store.Fetch) { f.FetchedAt = time.Time{} }},
		{"a success without a version", func(f *store.Fetch) { f.Version = "" }},
		{"a failure with a snapshot", func(f *store.Fetch) { f.Error = "down" }},
		{"a file without a path", func(f *store.Fetch) { f.Files[0].Path = "" }},
		{"a file path that isn't UTF-8", func(f *store.Fetch) { f.Files[0].Path = "a\xff.yaml" }},
		{"a negative size", func(f *store.Fetch) { f.Files[0].Size = -1 }},
		{"a digest that isn't SHA-256", func(f *store.Fetch) { f.Files[0].Digest = "md5:0123" }},
		{"an upper-case digest", func(f *store.Fetch) { f.Files[0].Digest = strings.ToUpper(f.Files[0].Digest) }},
		// Caught by the database, not by Prepare: the source row written
		// first must be rolled back with the rest.
		{"a path listed twice", func(f *store.Fetch) { f.Files[1] = f.Files[0] }},
	}
	for _, tt := range tests {
		f := valid()
		tt.change(&f)
		if err := s.RecordFetch(t.Context(), f); err == nil {
			t.Errorf("%s: RecordFetch succeeded", tt.name)
		}
	}
	if got := sources(t, s); len(got) != 0 {
		t.Errorf("Sources after only invalid fetches = %+v, want none", got)
	}
	checkNoSnapshot(t, s, "lib")
}

func testTimes(t *testing.T, s store.Store) {
	at := time.Date(2026, 10, 26, 11, 2, 3, 123456789, time.FixedZone("AEST", 10*60*60))
	record(t, s, success("lib", "v1", at))
	got := sources(t, s)
	if len(got) != 1 {
		t.Fatalf("Sources = %+v", got)
	}
	want := time.Date(2026, 10, 26, 1, 2, 3, 123456000, time.UTC)
	for name, ts := range map[string]time.Time{"fetch": got[0].FetchedAt, "snapshot": got[0].Snapshot.FetchedAt} {
		if !ts.Equal(want) || ts.Location() != time.UTC {
			t.Errorf("the %s's time = %v, want %v: in UTC, to the microsecond", name, ts, want)
		}
	}
}

func testSorting(t *testing.T, s store.Store) {
	// Byte order, as the source layer sorts, which differs from the
	// collation of most locales: punctuation and case count.
	names := []string{"b", "a_b", "a.b", "a-b", "a"}
	for _, name := range names {
		record(t, s, success(name, "v1", t0))
	}
	var got []string
	for _, src := range sources(t, s) {
		got = append(got, src.Name)
	}
	if diff := cmp.Diff([]string{"a", "a-b", "a.b", "a_b", "b"}, got); diff != "" {
		t.Errorf("Sources' order (-want +got):\n%s", diff)
	}

	f := success("lib", "v1", t0, "a.yaml", "a/b.yaml", "B.yaml", "é.yaml", "a-b.yaml", "_.yaml")
	record(t, s, f)
	var paths []string
	for _, file := range snapshot(t, s, "lib").Files {
		paths = append(paths, file.Path)
	}
	if diff := cmp.Diff([]string{"B.yaml", "_.yaml", "a-b.yaml", "a.yaml", "a/b.yaml", "é.yaml"}, paths); diff != "" {
		t.Errorf("files' order (-want +got):\n%s", diff)
	}
}

func testConcurrent(t *testing.T, s store.Store) {
	const workers, perWorker = 8, 5
	names := []string{"s0", "s1", "s2", "s3"}
	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)
	for w := range workers {
		wg.Go(func() {
			for i := range perWorker {
				name := names[(w+i)%len(names)]
				// Every fetch has a time of its own, so that the latest is
				// clear.
				at := t0.Add(time.Duration(w*perWorker+i) * time.Second)
				version := fmt.Sprintf("v-%d-%d", w, i)
				if err := s.RecordFetch(t.Context(), success(name, version, at, "x.yaml", "y.yaml", version+".yaml")); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("RecordFetch: %v", err)
	}
	for _, src := range sources(t, s) {
		if src.Snapshot == nil {
			t.Fatalf("source %s has no snapshot", src.Name)
		}
		if !src.FetchedAt.Equal(src.Snapshot.FetchedAt) {
			t.Errorf("source %s: latest fetch at %v, but its snapshot from %v", src.Name, src.FetchedAt, src.Snapshot.FetchedAt)
		}
		// The snapshot's lists are its own version's, not a mix.
		version := src.Snapshot.Version
		want := success(src.Name, version, src.Snapshot.FetchedAt, "x.yaml", "y.yaml", version+".yaml")
		checkSnapshot(t, s, snapshotOf(want))
	}
}

func testReadsAgree(t *testing.T, s store.Store) {
	// A snapshot is read as of one moment, so that its version, time and
	// lists come from one fetch, even while others are recorded.
	paths := make([]string, 200)
	for i := range paths {
		paths[i] = fmt.Sprintf("f%03d.yaml", i)
	}
	fetch := func(i int) store.Fetch {
		return success("lib", fmt.Sprintf("v%d", i), t0.Add(time.Duration(i)*time.Second), paths...)
	}
	record(t, s, fetch(0))
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Go(func() {
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := s.RecordFetch(t.Context(), fetch(i)); err != nil {
				t.Errorf("RecordFetch: %v", err)
				return
			}
		}
	})
	defer wg.Wait()
	defer close(stop)
	start := time.Now()
	versions := map[string]bool{}
	for reads := 0; reads < 20 || time.Since(start) < time.Second; reads++ {
		snap := snapshot(t, s, "lib")
		versions[snap.Version] = true
		var i int
		if _, err := fmt.Sscanf(snap.Version, "v%d", &i); err != nil {
			t.Fatalf("Snapshot returned version %q", snap.Version)
		}
		if diff := cmp.Diff(snapshotOf(fetch(i)), snap, equateEmpty); diff != "" {
			t.Fatalf("read %d: a snapshot mixes fetches (-want +got):\n%s", reads, diff)
		}
	}
	// Otherwise no read overlapped a write, and the test proved nothing.
	if len(versions) < 3 {
		t.Errorf("the reads saw %d versions, want at least 3", len(versions))
	}
}

func testRemoveWhileRecording(t *testing.T, s store.Store) {
	// RemoveSources and RecordFetch can run at once, as when a process with
	// an older configuration fetches a source that another process no longer
	// configures. Neither fails, and the source is either removed or
	// recorded whole.
	for i := range 30 {
		at := t0.Add(time.Duration(i) * time.Second)
		record(t, s, success("gone", fmt.Sprintf("v%d", i), at, "a.yaml", "b.yaml"))
		again := success("gone", fmt.Sprintf("v%d-again", i), at.Add(time.Millisecond), "b.yaml", "c.yaml")
		var wg sync.WaitGroup
		wg.Go(func() {
			if err := s.RecordFetch(t.Context(), again); err != nil {
				t.Errorf("round %d: RecordFetch: %v", i, err)
			}
		})
		wg.Go(func() {
			if _, err := s.RemoveSources(t.Context(), []string{"kept"}); err != nil {
				t.Errorf("round %d: RemoveSources: %v", i, err)
			}
		})
		wg.Wait()
		got, err := s.Snapshot(t.Context(), "gone")
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			t.Fatalf("round %d: Snapshot: %v", i, err)
		default:
			if diff := cmp.Diff(snapshotOf(again), got, equateEmpty); diff != "" {
				t.Errorf("round %d: the source is neither removed nor recorded whole (-want +got):\n%s", i, diff)
			}
		}
	}
}

func testLarge(t *testing.T, s store.Store) {
	// More than devicetype-library holds.
	paths := make([]string, 20000)
	for i := range paths {
		paths[i] = fmt.Sprintf("device-types/m%03d/d%05d.yaml", i%400, i)
	}
	for i, version := range []string{"v1", "v2"} {
		f := success("lib", version, t0.Add(time.Duration(i)*time.Minute), paths...)
		start := time.Now()
		record(t, s, f)
		t.Logf("recording %d files took %v", len(paths), time.Since(start))
		got := snapshot(t, s, "lib")
		if len(got.Files) != len(paths) {
			t.Fatalf("Snapshot holds %d files, want %d", len(got.Files), len(paths))
		}
		if diff := cmp.Diff(snapshotOf(f), got, equateEmpty); diff != "" {
			t.Errorf("Snapshot of %s (-want +got):\n%s", version, diff)
		}
	}
}
