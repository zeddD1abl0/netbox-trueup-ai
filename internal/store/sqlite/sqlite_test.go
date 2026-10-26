package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/go-cmp/cmp"
	sqlitedriver "modernc.org/sqlite"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/filelock"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/storetest"
)

// openNew opens a new database in a directory of the test's own, and
// closes it when the test ends.
func openNew(t *testing.T) *Store {
	t.Helper()
	s, _, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "trueup.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { closeStore(t, s) })
	return s
}

// closeStore closes a store, which must be closed only once.
func closeStore(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// rawDB opens the database at dbPath as no release of TrueUp would: without
// its checks or its lock, to look at the database or change it.
func rawDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	connector, err := sqlitedriver.NewConnector(dsn(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// openForeign opens a database file as another program would, with the
// driver's defaults.
func openForeign(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// query returns the first column of every row a query returns, as text.
func query(t *testing.T, db *sql.DB, q string) []string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// withMigration returns this release's migrations and one more.
func withMigration(t *testing.T, name, content string) fs.FS {
	t.Helper()
	files := fstest.MapFS{name: {Data: []byte(content)}}
	err := fs.WalkDir(migrationFiles, "migrations", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := migrationFiles.ReadFile(p)
		files[filepath.Base(p)] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func failedFetch() store.Fetch {
	return store.Fetch{
		Source:    store.SourceConfig{Name: "lib", Kind: "git", URL: "https://git.example.com/lib.git"},
		FetchedAt: time.Now(),
		Error:     "down",
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStore(t *testing.T) {
	t.Parallel()
	storetest.Run(t, func(t *testing.T) store.Store {
		return openNew(t)
	})
}

func TestMigrations(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "trueup.db")
	s, migrated, err := Open(t.Context(), Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := store.Migrated{
		Engine: "sqlite", Location: dbPath, VersionBefore: 0, Version: 1,
		Applied: []store.Migration{{Version: 1, Name: "00001_sources.sql"}},
	}
	if diff := cmp.Diff(want, migrated); diff != "" {
		t.Errorf("opening a new database (-want +got):\n%s", diff)
	}
	tables := query(t, s.db, "SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name")
	wantTables := []string{"goose_db_version", "snapshot_files", "snapshot_skipped", "snapshots", "sources", "sqlite_sequence"}
	if diff := cmp.Diff(wantTables, tables); diff != "" {
		t.Errorf("tables (-want +got):\n%s", diff)
	}
	if id := query(t, s.db, "PRAGMA application_id"); !slices.Equal(id, []string{"1414681936"}) {
		t.Errorf("application_id = %v, want TrueUp's", id)
	}
	closeStore(t, s)

	// Opening it again applies nothing.
	again, migrated, err := Open(t.Context(), Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open again: %v", err)
	}
	closeStore(t, again)
	want.VersionBefore, want.Applied = 1, nil
	if diff := cmp.Diff(want, migrated); diff != "" {
		t.Errorf("opening it again (-want +got):\n%s", diff)
	}
}

func TestNewerSchemaRefused(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "trueup.db")
	s, _, err := Open(t.Context(), Options{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	closeStore(t, s)
	db := rawDB(t, dbPath)
	if _, err := db.ExecContext(t.Context(), "INSERT INTO goose_db_version (version_id, is_applied) VALUES (7, 1)"); err != nil {
		t.Fatal(err)
	}

	_, _, err = Open(t.Context(), Options{Path: dbPath})
	var tooNew *store.SchemaTooNewError
	if !errors.As(err, &tooNew) || tooNew.Version != 7 || tooNew.Known != 1 {
		t.Fatalf("Open = %v, want a SchemaTooNewError for version 7 against 1", err)
	}
	if v := query(t, db, "SELECT MAX(version_id) FROM goose_db_version"); !slices.Equal(v, []string{"7"}) {
		t.Errorf("the schema's version after refusing = %v, want it left at 7", v)
	}
}

func TestFailedMigration(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "trueup.db")
	files := withMigration(t, "00002_two.sql",
		// The second statement fails, so the first must be undone too.
		"-- +goose Up\nCREATE TABLE two (x INTEGER) STRICT;\nINSERT INTO nowhere VALUES (1);\n")
	_, migrated, err := open(t.Context(), Options{Path: dbPath}, files, time.Second)
	if err == nil || !strings.Contains(err.Error(), "migrate the database's schema from version 0: migration 00002_two.sql failed: ") {
		t.Fatalf("Open = %v, want an error from the failed migration", err)
	}
	if diff := cmp.Diff([]store.Migration{{Version: 1, Name: "00001_sources.sql"}}, migrated.Applied); diff != "" {
		t.Errorf("applied (-want +got):\n%s", diff)
	}
	db := rawDB(t, dbPath)
	if tables := query(t, db, "SELECT name FROM sqlite_schema WHERE name IN ('sources', 'two')"); !slices.Equal(tables, []string{"sources"}) {
		t.Errorf("tables after the failed migration = %v, want only the first migration's", tables)
	}
	if v := query(t, db, "SELECT MAX(version_id) FROM goose_db_version"); !slices.Equal(v, []string{"1"}) {
		t.Errorf("schema version = %v, want 1", v)
	}
}

func TestConcurrentOpeners(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "new", "trueup.db")
	const openers = 8
	var wg sync.WaitGroup
	results := make([]store.Migrated, openers)
	errs := make([]error, openers)
	stores := make([]*Store, openers)
	start := time.Now()
	// Each keeps its store open until all have opened theirs, as processes
	// that start at once do.
	for i := range openers {
		wg.Go(func() { stores[i], results[i], errs[i] = Open(t.Context(), Options{Path: dbPath}) })
	}
	wg.Wait()
	t.Logf("%d openers took %v", openers, time.Since(start))
	applied := 0
	for i := range openers {
		if errs[i] != nil {
			t.Errorf("opener %d: %v", i, errs[i])
			continue
		}
		closeStore(t, stores[i])
		if results[i].Version != 1 {
			t.Errorf("opener %d found version %d", i, results[i].Version)
		}
		applied += len(results[i].Applied)
	}
	if applied != 1 {
		t.Errorf("the openers applied %d migrations between them, want 1", applied)
	}
	if n := query(t, rawDB(t, dbPath), "SELECT COUNT(*) FROM goose_db_version WHERE version_id = 1"); !slices.Equal(n, []string{"1"}) {
		t.Errorf("migration 1 is recorded %v times, want once", n)
	}
}

func TestAMigrationWaitsForOpenStores(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "trueup.db")
	s, _, err := Open(t.Context(), Options{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	files := withMigration(t, "00002_more.sql", "-- +goose Up\nALTER TABLE sources ADD COLUMN note TEXT;\n")

	// A newer release can't migrate while an older one has the database
	// open.
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	started := time.Now()
	_, _, err = open(t.Context(), Options{Path: dbPath, Logger: logger}, files, 1500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "other TrueUp processes have had the database open for over 1.5s, so it can't be migrated") {
		t.Errorf("migrating while a store is open = %v, want it to wait and give up", err)
	}
	if waited := time.Since(started); waited < 1500*time.Millisecond {
		t.Errorf("it gave up after %v", waited)
	}
	if !strings.Contains(logs.String(), "waiting for other TrueUp processes to close the database, to migrate it") {
		t.Errorf("waiting wasn't logged:\n%s", logs.String())
	}
	// The open store still works on the schema it knows.
	if err := s.RecordFetch(t.Context(), failedFetch()); err != nil {
		t.Errorf("RecordFetch on the open store: %v", err)
	}

	// Once it closes, the migration goes ahead, and the older release then
	// refuses the database.
	closeStore(t, s)
	newer, migrated, err := open(t.Context(), Options{Path: dbPath}, files, time.Second)
	if err != nil {
		t.Fatalf("migrating once the store closed: %v", err)
	}
	defer closeStore(t, newer)
	if diff := cmp.Diff([]store.Migration{{Version: 2, Name: "00002_more.sql"}}, migrated.Applied); diff != "" {
		t.Errorf("applied (-want +got):\n%s", diff)
	}
	if _, _, err := Open(t.Context(), Options{Path: dbPath}); !errors.As(err, new(*store.SchemaTooNewError)) {
		t.Errorf("the older release opening the migrated database = %v, want a SchemaTooNewError", err)
	}
}

func TestOpeningWaitsForAMigration(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "trueup.db")
	if err := createFile(t.Context(), dbPath, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	// As another process does while it migrates.
	migrating, err := filelock.Exclusive(t.Context(), dbPath+lockSuffix, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = open(t.Context(), Options{Path: dbPath}, withMigration(t, "00002_x.sql", "-- +goose Up\n"), 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "another TrueUp process has been migrating the database for over 300ms") {
		t.Errorf("Open during a migration = %v, want it to wait and give up", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, _, err := Open(ctx, Options{Path: dbPath}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Open during a migration = %v, want it to wait until the context ends", err)
	}
	if err := migrating.Unlock(); err != nil {
		t.Fatal(err)
	}
	s, _, err := Open(t.Context(), Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open once the migration is over: %v", err)
	}
	closeStore(t, s)
}

func TestFileModes(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows doesn't use Unix file modes")
	}
	dir := filepath.Join(t.TempDir(), "data")
	dbPath := filepath.Join(dir, "trueup.db")
	s, _, err := Open(t.Context(), Options{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	if err := s.RecordFetch(t.Context(), failedFetch()); err != nil {
		t.Fatal(err)
	}
	// While the store is open, SQLite keeps the -wal and -shm files.
	want := map[string]os.FileMode{dir: 0o700 | os.ModeDir, dbPath: 0o600, dbPath + "-wal": 0o600, dbPath + "-shm": 0o600, dbPath + lockSuffix: 0o600}
	for p, mode := range want {
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("stat %s: %v", p, err)
			continue
		}
		if info.Mode() != mode {
			t.Errorf("%s has mode %v, want %v", filepath.Base(p), info.Mode(), mode)
		}
	}
}

func TestExistingFiles(t *testing.T) {
	t.Parallel()

	// foreign returns a database another program made, as SQLite makes
	// them: in its default rollback journal mode.
	foreign := func(t *testing.T, stmts ...string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "other.db")
		db := openForeign(t, p)
		for _, q := range stmts {
			if _, err := db.ExecContext(t.Context(), q); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		return p
	}

	tests := []struct {
		name string
		path func(t *testing.T) string
		want string // a substring of the error, or "" to open
	}{
		{name: "an empty file", path: func(t *testing.T) string { return writeFile(t, "") }},
		{name: "a database TrueUp began but didn't migrate", path: func(t *testing.T) string {
			return foreign(t, "CREATE TABLE goose_db_version (id INTEGER PRIMARY KEY AUTOINCREMENT, version_id INTEGER NOT NULL, is_applied INTEGER NOT NULL, tstamp TIMESTAMP DEFAULT (datetime('now')))",
				"INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1)")
		}},
		{name: "another application's database", path: func(t *testing.T) string { return foreign(t, "CREATE TABLE dashboards (id INTEGER)") }, want: "holds another application's database, not TrueUp's"},
		{name: "another application's marked database", path: func(t *testing.T) string { return foreign(t, "PRAGMA application_id = 42") }, want: "holds another application's database, not TrueUp's"},
		{name: "a directory", path: func(t *testing.T) string { return t.TempDir() }, want: "isn't a file"},
		{name: "a relative path", path: func(*testing.T) string { return "trueup.db" }, want: "must be absolute"},
		{name: "a file that isn't a database", path: func(t *testing.T) string {
			return writeFile(t, strings.Repeat("not a database, but more than a header's worth of text, to be sure of it", 20))
		}, want: "file is not a database"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, _, err := Open(t.Context(), Options{Path: tt.path(t)})
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				closeStore(t, s)
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Open = %v, want an error saying %q", err, tt.want)
			}
		})
	}

	t.Run("another application's database is left as it was", func(t *testing.T) {
		t.Parallel()
		other := foreign(t, "CREATE TABLE dashboards (id INTEGER)")
		if _, _, err := Open(t.Context(), Options{Path: other}); err == nil {
			t.Fatal("Open of another application's database succeeded")
		}
		db := openForeign(t, other)
		if mode := query(t, db, "PRAGMA journal_mode"); !slices.Equal(mode, []string{"delete"}) {
			t.Errorf("the other database's journal mode = %v, want it left at delete", mode)
		}
		if tables := query(t, db, "SELECT name FROM sqlite_schema ORDER BY name"); !slices.Equal(tables, []string{"dashboards"}) {
			t.Errorf("the other database's tables = %v, want only its own", tables)
		}
	})
}

func TestWorldReadableWarning(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows doesn't use Unix file modes")
	}
	dbPath := writeFile(t, "")
	if err := os.Chmod(dbPath, 0o644); err != nil { //nolint:gosec // G302: the mode the test warns about
		t.Fatal(err)
	}
	var logs bytes.Buffer
	s, _, err := Open(t.Context(), Options{Path: dbPath, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	closeStore(t, s)
	if !strings.Contains(logs.String(), "every user on this host can read the database file") {
		t.Errorf("no warning about the file's mode:\n%s", logs.String())
	}
	if info, err := os.Stat(dbPath); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("the file's mode changed: %v, %v", info.Mode(), err)
	}
}

func TestPathsThatNeedEscaping(t *testing.T) {
	t.Parallel()
	name := "a dir?x=1#frag%41&mode=ro"
	if runtime.GOOS == "windows" {
		name = "a dir#frag%41&mode=ro" // no ? on Windows
	}
	dir := filepath.Join(t.TempDir(), name)
	dbPath := filepath.Join(dir, "trueup.db")
	s, _, err := Open(t.Context(), Options{Path: dbPath})
	if err != nil {
		t.Fatalf("open a database whose path holds URI syntax: %v", err)
	}
	if err := s.RecordFetch(t.Context(), failedFetch()); err != nil {
		t.Fatal(err)
	}
	closeStore(t, s)
	entries, err := os.ReadDir(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		t.Errorf("the database's parent holds %v, want only %q", entries, name)
	}
	if info, err := os.Stat(dbPath); err != nil || info.Size() == 0 {
		t.Errorf("the database file at %s: %v, %v", dbPath, info, err)
	}
}

func TestConnectionSettings(t *testing.T) {
	t.Parallel()
	s := openNew(t)
	// Each connection in the pool gets the settings, not just the first.
	var conns []*sql.Conn
	for range 3 {
		conn, err := s.db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
	}
	want := map[string]string{"journal_mode": "wal", "foreign_keys": "1", "synchronous": "2", "busy_timeout": "30000"}
	for i, conn := range conns {
		for pragma, value := range want {
			var got string
			if err := conn.QueryRowContext(t.Context(), "PRAGMA "+pragma).Scan(&got); err != nil {
				t.Fatalf("PRAGMA %s: %v", pragma, err)
			}
			if got != value {
				t.Errorf("connection %d: %s = %s, want %s", i, pragma, got, value)
			}
		}
		_ = conn.Close()
	}

	// Foreign keys hold.
	_, err := s.db.ExecContext(t.Context(),
		"INSERT INTO snapshots (source, url, version, ref, fetched_at) VALUES ('nowhere', '', 'v1', '', '2026-10-26T00:00:00.000000Z')")
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Errorf("a snapshot of an unknown source = %v, want a foreign key error", err)
	}
	// So does the time format.
	_, err = s.db.ExecContext(t.Context(),
		"INSERT INTO sources (name, kind, url, ref, fetched_at) VALUES ('lib', 'git', '', '', '2026-10-26 00:00:00')")
	if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Errorf("a time in another format = %v, want a check error", err)
	}
}

func TestBrokenForeignKeysRefusedOnEveryOpen(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "trueup.db")
	s, _, err := Open(t.Context(), Options{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	closeStore(t, s)
	// Another program, or a migration, with foreign keys off.
	conn, err := rawDB(t, dbPath).Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"PRAGMA foreign_keys = OFF",
		"INSERT INTO snapshot_files (source, path, size, digest) VALUES ('nowhere', 'a.yaml', 1, 'sha256:" + strings.Repeat("0", 64) + "')",
	} {
		if _, err := conn.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	_ = conn.Close()
	for i := range 2 {
		if _, _, err := Open(t.Context(), Options{Path: dbPath}); err == nil || !strings.Contains(err.Error(), "the database holds 1 rows that break foreign keys, in table snapshot_files") {
			t.Errorf("open %d = %v, want the broken row refused", i, err)
		}
	}
}

func TestAMigrationsSettingsDontOutliveIt(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "trueup.db")
	files := withMigration(t, "00002_keys_off.sql", "-- +goose NO TRANSACTION\n-- +goose Up\nPRAGMA foreign_keys = OFF;\n")
	s, _, err := open(t.Context(), Options{Path: dbPath}, files, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	for range 3 {
		_, err := s.db.ExecContext(t.Context(),
			"INSERT INTO snapshots (source, url, version, ref, fetched_at) VALUES ('nowhere', '', 'v1', '', '2026-10-26T00:00:00.000000Z')")
		if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			t.Fatalf("after a migration that turned foreign keys off, an orphan = %v, want a foreign key error", err)
		}
	}
}
