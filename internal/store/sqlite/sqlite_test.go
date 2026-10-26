package sqlite

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/storetest"
)

// lockHelperEnv, when set, makes the test binary hold the migration lock on
// the file it names, as another process would, until it is killed.
const lockHelperEnv = "SQLITE_TEST_HOLD_LOCK"

func TestMain(m *testing.M) {
	if path := os.Getenv(lockHelperEnv); path != "" {
		os.Exit(holdLock(path))
	}
	os.Exit(m.Run())
}

// holdLock takes the lock on path, says so on stdout, and holds it until
// stdin closes or the process is killed.
func holdLock(path string) int {
	unlock, err := lockFile(context.Background(), path, time.Minute, slog.New(slog.DiscardHandler))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = unlock()
	return 0
}

// open opens a new database in a directory of the test's own.
func open(t *testing.T) (*Store, store.Migrated, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trueup.db")
	s, migrated, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { closeStore(t, s) })
	return s, migrated, path
}

func closeStore(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// rawDB opens the database at path without the store, to look at it or
// change it as no release of TrueUp would.
func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := openDB(t.Context(), path)
	if err != nil {
		t.Fatalf("open the database directly: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestStore(t *testing.T) {
	t.Parallel()
	storetest.Run(t, func(t *testing.T) store.Store {
		s, _, _ := open(t)
		return s
	})
}

func TestMigrations(t *testing.T) {
	t.Parallel()
	s, migrated, path := open(t)
	want := store.Migrated{
		Engine: "sqlite", Location: path, VersionBefore: 0, Version: 1,
		Applied: []store.Migration{{Version: 1, Name: "00001_sources.sql"}},
	}
	if diff := cmp.Diff(want, migrated); diff != "" {
		t.Errorf("opening a new database (-want +got):\n%s", diff)
	}
	var tables []string
	rows, err := s.db.QueryContext(t.Context(), "SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	wantTables := []string{"goose_db_version", "snapshot_files", "snapshot_skipped", "snapshots", "sources", "sqlite_sequence"}
	if diff := cmp.Diff(wantTables, tables); diff != "" {
		t.Errorf("tables (-want +got):\n%s", diff)
	}
	closeStore(t, s)

	// Opening it again applies nothing.
	again, migrated, err := Open(t.Context(), Options{Path: path})
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
	s, _, path := open(t)
	closeStore(t, s)
	db := rawDB(t, path)
	if _, err := db.ExecContext(t.Context(), "INSERT INTO goose_db_version (version_id, is_applied) VALUES (7, 1)"); err != nil {
		t.Fatal(err)
	}

	_, _, err := Open(t.Context(), Options{Path: path})
	var tooNew *store.SchemaTooNewError
	if !errors.As(err, &tooNew) || tooNew.Version != 7 || tooNew.Known != 1 {
		t.Fatalf("Open = %v, want a SchemaTooNewError for version 7 against 1", err)
	}
	var version int64
	if err := db.QueryRowContext(t.Context(), "SELECT MAX(version_id) FROM goose_db_version").Scan(&version); err != nil || version != 7 {
		t.Errorf("the schema's version after refusing = %d, %v; want it left at 7", version, err)
	}
}

func TestFailedMigration(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "trueup.db")
	if err := createFile(t.Context(), path, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, path)
	files := fstest.MapFS{
		"00001_one.sql": {Data: []byte("-- +goose Up\nCREATE TABLE one (x INTEGER) STRICT;\n")},
		// The second statement fails, so the first must be undone too.
		"00002_two.sql": {Data: []byte("-- +goose Up\nCREATE TABLE two (x INTEGER) STRICT;\nINSERT INTO nowhere VALUES (1);\n")},
	}
	migrated, err := migrateFrom(t.Context(), db, path, files, time.Second, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "migrate the database's schema from version 0: migration 00002_two.sql failed: ") {
		t.Fatalf("migrating = %v, want an error from the failed migration", err)
	}
	if diff := cmp.Diff([]store.Migration{{Version: 1, Name: "00001_one.sql"}}, migrated.Applied); diff != "" {
		t.Errorf("applied (-want +got):\n%s", diff)
	}
	var tables []string
	rows, err := db.QueryContext(t.Context(), "SELECT name FROM sqlite_schema WHERE name IN ('one', 'two') ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if !slices.Equal(tables, []string{"one"}) {
		t.Errorf("tables after the failed migration = %v, want only one", tables)
	}
	var version int64
	if err := db.QueryRowContext(t.Context(), "SELECT MAX(version_id) FROM goose_db_version").Scan(&version); err != nil || version != 1 {
		t.Errorf("schema version = %d, %v; want 1", version, err)
	}
}

func TestConcurrentOpeners(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "new", "trueup.db")
	const openers = 8
	var wg sync.WaitGroup
	results := make([]store.Migrated, openers)
	errs := make([]error, openers)
	stores := make([]*Store, openers)
	for i := range openers {
		wg.Go(func() { stores[i], results[i], errs[i] = Open(t.Context(), Options{Path: path}) })
	}
	wg.Wait()
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
	var rows int
	if err := rawDB(t, path).QueryRowContext(t.Context(), "SELECT COUNT(*) FROM goose_db_version WHERE version_id = 1").Scan(&rows); err != nil || rows != 1 {
		t.Errorf("migration 1 is recorded %d times, %v; want once", rows, err)
	}
}

func TestLock(t *testing.T) {
	t.Parallel()
	discard := slog.New(slog.DiscardHandler)
	path := filepath.Join(t.TempDir(), "trueup.db")
	unlock, err := lockFile(t.Context(), path+lockSuffix, time.Second, discard)
	if err != nil {
		t.Fatal(err)
	}

	// Another open, in this process as in another, waits for the lock and
	// gives up.
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	start := time.Now()
	_, err = lockFile(t.Context(), path+lockSuffix, 1500*time.Millisecond, logger)
	if err == nil || !strings.Contains(err.Error(), "has held the database's migration lock for 1.5s") {
		t.Errorf("a second lock = %v, want it to give up", err)
	}
	if waited := time.Since(start); waited < 1500*time.Millisecond {
		t.Errorf("the second lock gave up after %v", waited)
	}
	if !strings.Contains(logs.String(), "waiting for another TrueUp process to finish migrating the database") {
		t.Errorf("waiting wasn't logged:\n%s", logs.String())
	}

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, _, err := Open(ctx, Options{Path: path}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Open while the lock is held = %v, want it to wait until the context ends", err)
	}

	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	s, _, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatalf("Open once the lock is free: %v", err)
	}
	closeStore(t, s)
}

func TestLockReleasedWhenHolderDies(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "trueup.db")
	helper := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$") //nolint:gosec // G204: the test binary itself
	helper.Env = append(os.Environ(), lockHelperEnv+"="+path+lockSuffix)
	stdin, err := helper.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	helper.Stderr = os.Stderr
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("the helper said %q, %v", line, err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if _, _, err := Open(ctx, Options{Path: path}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Open while another process holds the lock = %v, want it to wait", err)
	}

	// Killed, the helper can't release the lock itself.
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	s, _, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatalf("Open after the lock's holder died: %v", err)
	}
	closeStore(t, s)
}

func TestFileModes(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows doesn't use Unix file modes")
	}
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "trueup.db")
	s, err := openAndWrite(t, path)
	if err != nil {
		t.Fatal(err)
	}
	// While the store is open, SQLite keeps the -wal and -shm files.
	want := map[string]os.FileMode{dir: 0o700 | os.ModeDir, path: 0o600, path + "-wal": 0o600, path + "-shm": 0o600, path + lockSuffix: 0o600}
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
	closeStore(t, s)
}

// openAndWrite opens the database at path and records a fetch in it.
func openAndWrite(t *testing.T, path string) (*Store, error) {
	t.Helper()
	s, _, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		return nil, err
	}
	err = s.RecordFetch(t.Context(), store.Fetch{
		Source:    store.SourceConfig{Name: "lib", Kind: "git", URL: "https://git.example.com/lib.git"},
		FetchedAt: time.Now(),
		Error:     "down",
	})
	if err != nil {
		closeStore(t, s)
		return nil, err
	}
	return s, nil
}

func TestExistingFiles(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows doesn't use Unix file modes")
	}

	// An empty file is an empty database. One that every user can read is
	// used, with a warning.
	path := filepath.Join(t.TempDir(), "trueup.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // G302: the mode the test warns about
		t.Fatal(err)
	}
	var logs bytes.Buffer
	s, _, err := Open(t.Context(), Options{Path: path, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	closeStore(t, s)
	if !strings.Contains(logs.String(), "every user on this host can read the database file") {
		t.Errorf("no warning about the file's mode:\n%s", logs.String())
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("the file's mode changed: %v, %v", info.Mode(), err)
	}

	tests := []struct {
		name, path, want string
	}{
		{"a directory", t.TempDir(), "isn't a file"},
		{"a relative path", "trueup.db", "must be absolute"},
		{"a file that isn't a database", writeFile(t, "not a database, but more than a header's worth of text, to be sure of it"), "file is not a database"},
	}
	for _, tt := range tests {
		if _, _, err := Open(t.Context(), Options{Path: tt.path}); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Open = %v, want an error saying %q", tt.name, err, tt.want)
		}
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte(strings.Repeat(content, 20)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPathsThatNeedEscaping(t *testing.T) {
	t.Parallel()
	name := "a dir?x=1#frag%41&mode=ro"
	if runtime.GOOS == "windows" {
		name = "a dir#frag%41&mode=ro" // no ? on Windows
	}
	dir := filepath.Join(t.TempDir(), name)
	path := filepath.Join(dir, "trueup.db")
	s, err := openAndWrite(t, path)
	if err != nil {
		t.Fatalf("open a database whose path holds URI syntax: %v", err)
	}
	closeStore(t, s)
	entries, err := os.ReadDir(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		t.Errorf("the database's parent holds %v, want only %q", entries, name)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Errorf("the database file at %s: %v, %v", path, info, err)
	}
}

func TestConnectionSettings(t *testing.T) {
	t.Parallel()
	s, _, _ := open(t)
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
