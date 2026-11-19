package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source/sourcetest"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/engine"
)

// fetchedStore returns a new database in which the source lib was fetched
// at version v1.
func fetchedStore(t *testing.T) store.Store {
	t.Helper()
	db, _, err := engine.Open(t.Context(), engine.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.RecordFetch(t.Context(), store.Fetch{
		Source:    store.SourceConfig{Name: "lib", Kind: "git", URL: "https://git.example.com/lib.git"},
		FetchedAt: time.Now(), Version: "v1",
	}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRecordLoadReportsAChangedSnapshotAsTheSources(t *testing.T) {
	t.Parallel()
	db := fetchedStore(t)
	// Whether the load succeeded or failed, the database refusing it for a
	// snapshot of another version is a problem with that source alone.
	for _, l := range []store.Load{
		{Source: "lib", Version: "v0", Reader: "devicetype-library/1", Settings: "sha256:x", StartedAt: time.Now()},
		{Source: "lib", Version: "v0", Reader: "devicetype-library/1", Settings: "sha256:x", StartedAt: time.Now(), Error: "failed"},
	} {
		err := recordLoad(t.Context(), db, l)
		if _, ok := errors.AsType[sourceProblem](err); !ok || !errors.Is(err, store.ErrSnapshotChanged) {
			t.Errorf("recordLoad of another version = %v, want a sourceProblem wrapping ErrSnapshotChanged", err)
		}
	}
}

// testSource is a source as configured, with its limits.
func testSource() config.Source {
	return config.Source{
		Name: "lib", Kind: config.SourceKindGit, URL: "https://git.example.com/lib.git", Format: config.FormatDeviceTypeLibrary,
		Limits: config.SourceLimits{MaxFileSize: 1 << 20, MaxFiles: 100_000, FetchTimeout: time.Minute, MaxFetchSize: 1 << 30},
	}
}

func TestAnInterruptedLoadIsntRecorded(t *testing.T) {
	t.Parallel()
	db := fetchedStore(t)
	var logs bytes.Buffer
	a := &app{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s := testSource()
	key, err := loadKeyOf(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.loadLocked(ctx, db, nil, s, "v1", key); !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "interrupted while loading source lib") {
		t.Errorf("loadLocked = %v, want it interrupted", err)
	}
	// Not recorded as having failed, so the next load reads it again.
	status, err := db.LoadStatus(t.Context(), "lib")
	if err != nil {
		t.Fatal(err)
	}
	if status.State() != "" {
		t.Errorf("an interrupted load is recorded as %s, %q", status.State(), status.Error)
	}
	if logs.Len() > 0 {
		t.Errorf("logged:\n%s", logs.String())
	}
}

func TestLoadKeys(t *testing.T) {
	t.Parallel()
	keyOf := func(change func(*config.Source)) loadKey {
		t.Helper()
		s := testSource()
		change(&s)
		key, err := loadKeyOf(s)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	base := keyOf(func(*config.Source) {})
	// go test's builds are TrueUp's dev release.
	if base.reader != "devicetype-library/1 (TrueUp dev)" || !strings.HasPrefix(base.settings, "sha256:") {
		t.Errorf("loadKeyOf = %+v", base)
	}
	// Patterns are sets: none and an empty list, their order, and one given
	// twice read the same files.
	for name, change := range map[string]func(*config.Source){
		"no patterns":     func(s *config.Source) { s.Include, s.Exclude = []string{}, []string{} },
		"other order":     func(s *config.Source) { s.Include = []string{"b/**", "a/**"} },
		"a pattern twice": func(s *config.Source) { s.Include = []string{"a/**", "b/**", "a/**"} },
		"timeouts":        func(s *config.Source) { s.Limits.FetchTimeout, s.Limits.MaxFetchSize = time.Hour, 1 },
	} {
		want := base
		if name == "other order" || name == "a pattern twice" {
			want = keyOf(func(s *config.Source) { s.Include = []string{"a/**", "b/**"} })
		}
		if got := keyOf(change); got != want {
			t.Errorf("%s: loadKeyOf = %+v, want %+v", name, got, want)
		}
	}
	// What chooses the files read, or where they come from, changes it.
	for name, change := range map[string]func(*config.Source){
		"url":           func(s *config.Source) { s.URL = "https://git.example.com/other.git" },
		"ref":           func(s *config.Source) { s.Ref = "v2" },
		"include":       func(s *config.Source) { s.Include = []string{"device-types/Dell/**"} },
		"exclude":       func(s *config.Source) { s.Exclude = []string{"device-types/Dell/**"} },
		"max_file_size": func(s *config.Source) { s.Limits.MaxFileSize = 1 << 10 },
		"max_files":     func(s *config.Source) { s.Limits.MaxFiles = 10 },
	} {
		if got := keyOf(change); got.settings == base.settings {
			t.Errorf("changing %s leaves the settings' digest as it was", name)
		}
	}
}

func TestFillLoadSkipsWhatCantBeKept(t *testing.T) {
	t.Parallel()
	acme := format.Definition{
		Object:   model.Object{Type: "dcim.manufacturer", Fields: map[string]model.Value{"name": model.String("Acme")}},
		Identity: map[string]model.Value{"name": model.String("Acme")},
		Path:     "a.yaml",
	}
	// A field of another kind than its spec's doesn't read back.
	broken := acme
	broken.Path = "b.yaml"
	broken.Object.Fields = map[string]model.Value{"name": model.Integer(1)}
	// A second from the same file, which counts once as an invalid file.
	alsoBroken := broken
	alsoBroken.Identity = map[string]model.Value{"name": model.String("Acme 2")}
	// A derived one has no file to make invalid.
	derived := acme
	derived.Path, derived.Object.Fields = "", map[string]model.Value{"name": model.Integer(2)}
	derived.DerivedFrom = []string{"c.yaml", "d.yaml"}
	res := &format.Result{
		Definitions: []format.Definition{acme, broken, alsoBroken, derived},
		Problems: []format.Problem{
			{Severity: format.Warning, Reason: "derived"},
			{Severity: format.Warning, Path: "b.yaml", Line: 3, Reason: "b's own"},
			{Severity: format.Error, Path: "c.yaml", Reason: "c's own"},
		},
		Files: 4, Invalid: 1,
	}
	var load store.Load
	fillLoad(&load, res, map[string]string{"a.yaml": "sha256:" + strings.Repeat("a", 64), "b.yaml": "sha256:" + strings.Repeat("b", 64)})
	if len(load.Definitions) != 1 || load.Definitions[0].Path != "a.yaml" {
		t.Errorf("kept %+v, want a.yaml's alone", load.Definitions)
	}
	if load.Files != 4 || load.InvalidFiles != 2 {
		t.Errorf("files %d, invalid %d; want 4 and 2", load.Files, load.InvalidFiles)
	}
	var got []string
	for _, p := range load.Problems {
		got = append(got, p.Path+": "+p.Reason[:min(len(p.Reason), 39)])
	}
	want := []string{
		": derived",
		": TrueUp can't keep this definition as it",
		"b.yaml: b's own",
		"b.yaml: TrueUp can't keep this definition as it",
		"b.yaml: TrueUp can't keep this definition as it",
		"c.yaml: c's own",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("problems:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestReadCleanly(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		res  format.Result
		want bool
	}{
		{"no problems", format.Result{Files: 2}, true},
		{"warnings", format.Result{Files: 2, Problems: []format.Problem{{Severity: format.Warning, Reason: "x"}}}, true},
		{"an invalid file", format.Result{Files: 2, Invalid: 1, Problems: []format.Problem{{Severity: format.Error, Path: "a.yaml", Reason: "x"}}}, false},
		// Such as a derived definition TrueUp couldn't keep.
		{"an error with no file", format.Result{Files: 2, Problems: []format.Problem{{Severity: format.Error, Reason: "x"}}}, false},
	} {
		if got := readCleanly(&tt.res); got != tt.want {
			t.Errorf("%s: readCleanly = %t, want %t", tt.name, got, tt.want)
		}
	}
}

// vanishing is a store from which a source is removed between reading the
// status of its loads and its definitions.
type vanishing struct {
	store.Store
	status store.LoadStatus
}

func (v vanishing) LoadStatus(context.Context, string) (store.LoadStatus, error) {
	return v.status, nil
}

func (v vanishing) Definitions(context.Context, string, string) (store.SourceDefinitions, error) {
	return store.SourceDefinitions{}, fmt.Errorf("read the definitions of source lib: %w", store.ErrNotFound)
}

func TestASourceRemovedWhileReadIsNotFetched(t *testing.T) {
	t.Parallel()
	s := testSource()
	key, err := loadKeyOf(s)
	if err != nil {
		t.Fatal(err)
	}
	loaded := store.LoadKey{Version: "v1", Reader: key.reader, Settings: key.settings}
	db := vanishing{Store: fetchedStore(t), status: store.LoadStatus{Source: "lib", SnapshotVersion: "v1", Latest: loaded, Loaded: loaded}}
	a := &app{logger: slog.New(slog.DiscardHandler)}
	_, err = a.readLoaded(t.Context(), db, nil, s, "")
	if _, ok := errors.AsType[sourceProblem](err); !ok || !errors.Is(err, source.ErrNotFetched) {
		t.Errorf("readLoaded = %v, want a sourceProblem: the source isn't fetched", err)
	}
}

// refusingFailures is a store whose snapshot changes whenever a failed load
// is recorded, as when another host sharing the database fetched the source.
type refusingFailures struct{ store.Store }

func (r refusingFailures) RecordLoad(ctx context.Context, l store.Load) error {
	if l.Error != "" {
		return store.ErrSnapshotChanged
	}
	return r.Store.RecordLoad(ctx, l)
}

func TestAFailedLoadTheDatabaseRefusesIsTheSources(t *testing.T) {
	t.Parallel()
	db := refusingFailures{fetchedStore(t)}
	var logs bytes.Buffer
	a := &app{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	s := testSource()
	key, err := loadKeyOf(s)
	if err != nil {
		t.Fatal(err)
	}
	// No kinds of source, so the load fails, and recording that fails too:
	// both are said.
	_, err = a.loadLocked(t.Context(), db, nil, s, "v1", key)
	if _, ok := errors.AsType[sourceProblem](err); !ok || !errors.Is(err, store.ErrSnapshotChanged) ||
		!strings.Contains(err.Error(), `TrueUp can't read sources of kind "git"; and recording that failed: `) {
		t.Errorf("loadLocked = %v, want a sourceProblem wrapping ErrSnapshotChanged, after why the load failed", err)
	}
	// The caller reports it, once.
	if logs.Len() > 0 {
		t.Errorf("logged:\n%s", logs.String())
	}
}

// changingFS is a file system whose file at path holds other content once
// it was opened, as a cache whose file changes after it was listed.
type changingFS struct {
	fstest.MapFS
	path  string
	later []byte

	mu    sync.Mutex
	opens int
}

func (c *changingFS) Open(name string) (fs.File, error) {
	if name == c.path {
		c.mu.Lock()
		c.opens++
		changed := c.opens > 1
		c.mu.Unlock()
		if changed {
			return fstest.MapFS{name: {Data: c.later}}.Open(name)
		}
	}
	return c.MapFS.Open(name)
}

func TestAFileTheCacheCantGiveBackIsntRecorded(t *testing.T) {
	t.Parallel()
	db := fetchedStore(t)
	a := &app{logger: slog.New(slog.DiscardHandler)}
	s := testSource()
	key, err := loadKeyOf(s)
	if err != nil {
		t.Fatal(err)
	}
	fsys := &changingFS{
		MapFS: fstest.MapFS{"schema/devicetype.json": {Data: []byte(`{"a": 1}`)}},
		path:  "schema/devicetype.json", later: []byte(`{"a": 2}`),
	}
	kinds := map[string]source.Kind{s.Kind: sourcetest.Kind{FS: fsys, Version: "v1"}}
	_, err = a.loadLocked(t.Context(), db, kinds, s, "v1", key)
	if _, ok := errors.AsType[sourceProblem](err); !ok || !strings.Contains(err.Error(), "changed in the cache since it was listed") {
		t.Errorf("loadLocked = %v, want a sourceProblem saying the file changed in the cache", err)
	}
	// Not recorded as having failed: another host sharing the database, or
	// this one a moment later, may read the version.
	status, err := db.LoadStatus(t.Context(), "lib")
	if err != nil {
		t.Fatal(err)
	}
	if status.State() != "" {
		t.Errorf("a file the cache couldn't give back is recorded as %s, %q", status.State(), status.Error)
	}
}

// fetchedEachRound is a store in which the source is fetched again while
// each of the first two rounds of readLoaded reads it, after which it holds
// definitions this release can't read back.
type fetchedEachRound struct {
	store.Store
	status store.LoadStatus
	reads  int
}

func (f *fetchedEachRound) LoadStatus(context.Context, string) (store.LoadStatus, error) {
	return f.status, nil
}

func (f *fetchedEachRound) Definitions(context.Context, string, string) (store.SourceDefinitions, error) {
	f.reads++
	if f.reads < 3 {
		older := f.status
		older.Loaded = store.LoadKey{Version: "v0", Reader: f.status.Loaded.Reader, Settings: f.status.Loaded.Settings}
		return store.SourceDefinitions{LoadStatus: older}, nil
	}
	return store.SourceDefinitions{LoadStatus: f.status, Definitions: []store.Definition{{
		DefinitionKey: store.DefinitionKey{ObjectType: "dcim.manufacturer", Identity: `{"name":"Acme"}`},
		JSON:          []byte(`{}`),
	}}}, nil
}

func TestTheLastRoundThatCantReadBackLoadsAgain(t *testing.T) {
	t.Parallel()
	s := testSource()
	key, err := loadKeyOf(s)
	if err != nil {
		t.Fatal(err)
	}
	loaded := store.LoadKey{Version: "v1", Reader: key.reader, Settings: key.settings}
	db := &fetchedEachRound{Store: fetchedStore(t), status: store.LoadStatus{Source: "lib", SnapshotVersion: "v1", Latest: loaded, Loaded: loaded}}
	cfg := &config.Config{}
	cfg.Settings.DataDir = t.TempDir()
	a := &app{logger: slog.New(slog.DiscardHandler)}
	// The third round can't read back what it finds, so a fourth loads the
	// source again: here, with no cache, it says the source isn't fetched,
	// rather than that a load it never made can't be read back.
	_, err = a.readLoaded(t.Context(), db, cfg, s, "")
	if _, ok := errors.AsType[sourceProblem](err); !ok || !errors.Is(err, source.ErrNotFetched) {
		t.Errorf("readLoaded = %v, want a sourceProblem from loading the source again", err)
	}
	if db.reads != 3 {
		t.Errorf("read the definitions %d times, want 3", db.reads)
	}
}

func TestALockTheCacheCantTakeIsTheSources(t *testing.T) {
	t.Parallel()
	db := fetchedStore(t)
	cfg := &config.Config{}
	cfg.Settings.DataDir = t.TempDir()
	// A directory where the source's lock file goes, which can't be opened
	// as one.
	if err := os.MkdirAll(filepath.Join(cachePath(cfg), ".locks", "lib", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := &app{logger: slog.New(slog.DiscardHandler)}
	// The source needs a load, so readLoaded takes its lock: failing is a
	// problem with that source alone, which list and compare report beside
	// the others.
	_, err := a.readLoaded(t.Context(), db, cfg, testSource(), "")
	if _, ok := errors.AsType[sourceProblem](err); !ok || !strings.Contains(err.Error(), "lock the source's cache") {
		t.Errorf("readLoaded = %v, want a sourceProblem: the source's cache can't be locked", err)
	}
}

// refusingOnce is a store that refuses the first load recorded, as when
// another host sharing the database fetched the source meanwhile.
type refusingOnce struct {
	store.Store
	refused bool
}

func (r *refusingOnce) RecordLoad(ctx context.Context, l store.Load) error {
	if !r.refused {
		r.refused = true
		return fmt.Errorf("%w: the snapshot is of version %q", store.ErrSnapshotChanged, "v2")
	}
	return r.Store.RecordLoad(ctx, l)
}

func TestALoadTheDatabaseRefusesIsTriedAgain(t *testing.T) {
	t.Parallel()
	repoURL, pool, _ := libraryRepo(t, nil)
	env, dataDir := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library"}]`)
	fetched(t, pool, env)
	inner, _, err := engine.Open(t.Context(), engine.Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	db := &refusingOnce{Store: inner}
	cfg := &config.Config{}
	cfg.Settings.DataDir = dataDir
	// Other settings than the fetch loaded with, so readLoaded loads.
	s := testSource()
	s.URL, s.Exclude = repoURL, []string{"device-types/Huawei"}
	a := &app{logger: slog.New(slog.DiscardHandler)}
	read, err := a.readLoaded(t.Context(), db, cfg, s, "")
	if err != nil || len(read.result.Definitions) != 4 {
		t.Fatalf("readLoaded = %v, want the 4 definitions left once Huawei's are excluded", err)
	}
	if !db.refused {
		t.Error("readLoaded didn't load the source")
	}
}

func TestACacheTheDataDirectoryCantHoldIsTheSources(t *testing.T) {
	t.Parallel()
	db := fetchedStore(t)
	// A file where the data directory goes, so the cache can't be made.
	cfg := &config.Config{}
	cfg.Settings.DataDir = filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(cfg.Settings.DataDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a := &app{logger: slog.New(slog.DiscardHandler)}
	_, err := a.readLoaded(t.Context(), db, cfg, testSource(), "")
	if _, ok := errors.AsType[sourceProblem](err); !ok || !strings.Contains(err.Error(), "create the source cache") {
		t.Errorf("readLoaded = %v, want a sourceProblem: the cache can't be made", err)
	}
}

// failingFailures is a store that can't record a failed load, as when the
// database is locked for too long.
type failingFailures struct{ store.Store }

func (f failingFailures) RecordLoad(ctx context.Context, l store.Load) error {
	if l.Error != "" {
		return errors.New("record the load of source lib: database is locked")
	}
	return f.Store.RecordLoad(ctx, l)
}

func TestAFailedLoadTheDatabaseCantRecordSaysWhyItFailed(t *testing.T) {
	t.Parallel()
	db := failingFailures{fetchedStore(t)}
	a := &app{logger: slog.New(slog.DiscardHandler)}
	s := testSource()
	key, err := loadKeyOf(s)
	if err != nil {
		t.Fatal(err)
	}
	// No kinds of source, so the load fails, and recording that fails too:
	// a fault of the database, which says why the load failed.
	_, err = a.loadLocked(t.Context(), db, nil, s, "v1", key)
	if _, ok := errors.AsType[sourceProblem](err); ok || err == nil ||
		!strings.Contains(err.Error(), `database is locked, recording that the source's files couldn't be read: TrueUp can't read sources of kind "git"`) {
		t.Errorf("loadLocked = %v, want the database's error, saying why the load failed", err)
	}
}

func TestFetchProblems(t *testing.T) {
	t.Parallel()
	failed, unreadable := "connection refused", "the source has no schema/devicetype.json"
	two := 2
	got := fetchProblems(fetchJSON{Sources: []fetchSourceJSON{
		{Name: "a", Error: &failed},
		{Name: "b", LoadError: &unreadable},
		{Name: "c", Definitions: &two},
	}})
	// A load the fetch couldn't make isn't made again: list and compare
	// report it.
	want := map[string]string{"a": "the fetch failed: connection refused", "b": unreadable}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("fetchProblems (-want +got):\n%s", diff)
	}
}
