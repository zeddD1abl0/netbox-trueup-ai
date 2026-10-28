//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/postgres/pgtest"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/storetest"
)

// srv is the PostgreSQL that TestMain starts. Each test has a database of
// its own on it.
var srv *pgtest.Server

func TestMain(m *testing.M) {
	if url := os.Getenv(helperURL); url != "" {
		os.Exit(interruptedOpen(url))
	}
	os.Exit(pgtest.Run(m, &srv))
}

// helperURL names, for a test binary that TestAnInterruptedProcess runs, the
// database to open as an interrupted command does.
const helperURL = "TRUEUP_PGTEST_INTERRUPTED_OPEN"

// interruptedOpen opens the database at url with a migration it must wait to
// apply, until its context ends, and exits at once, as the CLI does when it
// is interrupted.
func interruptedOpen(url string) int {
	files, err := migrationsWith("00002_x.sql", "-- +goose Up\n")
	if err != nil {
		fmt.Println(err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err = open(ctx, Options{URL: config.NewSecret(url)}, files, time.Minute)
	fmt.Println(err)
	return 0
}

// openURL opens the database at url, and closes it when the test ends.
func openURL(t *testing.T, url config.Secret) *Store {
	t.Helper()
	s, _, err := Open(t.Context(), Options{URL: url})
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

// query returns the first column of every row a query returns, as text.
func query(t *testing.T, pool *pgxpool.Pool, q string, args ...any) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprint(values[0]))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return out
}

// withMigration returns this release's migrations and one more.
func withMigration(t *testing.T, name, content string) fs.FS {
	t.Helper()
	files, err := migrationsWith(name, content)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func migrationsWith(name, content string) (fs.FS, error) {
	files := fstest.MapFS{name: {Data: []byte(content)}}
	err := fs.WalkDir(migrationFiles, "migrations", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := migrationFiles.ReadFile(p)
		files[filepath.Base(p)] = &fstest.MapFile{Data: data}
		return err
	})
	return files, err
}

func failedFetch() store.Fetch {
	return store.Fetch{
		Source:    store.SourceConfig{Name: "lib", Kind: "git", URL: "https://git.example.com/lib.git"},
		FetchedAt: time.Now(),
		Error:     "down",
	}
}

// tables lists the tables and views in a schema, sorted.
func tables(t *testing.T, pool *pgxpool.Pool, schema string) []string {
	t.Helper()
	return query(t, pool, "SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relkind IN ('r', 'v') ORDER BY c.relname", schema)
}

// comment returns the comment on TrueUp's schema, or "<nil>".
func comment(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	return query(t, pool, "SELECT obj_description(oid, 'pg_namespace') FROM pg_namespace WHERE nspname = $1", Schema)
}

// newRole creates a role that can log in, and returns its URL for database.
func newRole(t *testing.T, database string) (role string, url config.Secret) {
	t.Helper()
	role = "role_" + database
	const rolePassword = "role-password" // a dummy, for a disposable server
	srv.Exec(t, "postgres", fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", role, rolePassword))
	return role, config.NewSecret(srv.URL(role, rolePassword, database))
}

func TestStore(t *testing.T) {
	t.Parallel()
	storetest.Run(t, func(t *testing.T) store.Store { return openURL(t, srv.NewURL(t)) })
}

func TestMigrations(t *testing.T) {
	t.Parallel()
	database := srv.NewDatabase(t)
	url := config.NewSecret(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database))
	wantLocation := "postgres://127.0.0.1:" + srv.Port + "/" + database
	for i, want := range []store.Migrated{
		{Engine: Engine, Location: wantLocation, VersionBefore: 0, Version: 1, Applied: []store.Migration{{Version: 1, Name: "00001_sources.sql"}}},
		{Engine: Engine, Location: wantLocation, VersionBefore: 1, Version: 1},
	} {
		s, got, err := Open(t.Context(), Options{URL: url})
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		closeStore(t, s)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("open %d migrated (-want +got):\n%s", i, diff)
		}
	}

	pool := srv.Pool(t, database)
	want := []string{"goose_db_version", "snapshot_files", "snapshot_skipped", "snapshots", "sources"}
	if got := tables(t, pool, Schema); !slices.Equal(got, want) {
		t.Errorf("tables in %s = %v, want %v", Schema, got, want)
	}
	if got := tables(t, pool, "public"); len(got) != 0 {
		t.Errorf("tables in public = %v, want none", got)
	}
	if got := comment(t, pool); !slices.Equal(got, []string{schemaMark}) {
		t.Errorf("the schema's comment = %v, want TrueUp's mark", got)
	}
}

func TestNewerSchemaRefused(t *testing.T) {
	t.Parallel()
	database := srv.NewDatabase(t)
	url := config.NewSecret(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database))
	closeStore(t, openURL(t, url))
	srv.Exec(t, database, "INSERT INTO netbox_trueup_ai.goose_db_version (version_id, is_applied) VALUES (2, true)")

	_, migrated, err := Open(t.Context(), Options{URL: url})
	var tooNew *store.SchemaTooNewError
	if !errors.As(err, &tooNew) || tooNew.Version != 2 || tooNew.Known != 1 {
		t.Fatalf("Open = %v, want a SchemaTooNewError for version 2", err)
	}
	if migrated.VersionBefore != 2 || len(migrated.Applied) != 0 {
		t.Errorf("migrated = %+v, want version 2 found and nothing applied", migrated)
	}
}

func TestFailedMigration(t *testing.T) {
	t.Parallel()
	database := srv.NewDatabase(t)
	files := withMigration(t, "00002_two.sql",
		// The second statement fails, so the first must be undone too.
		"-- +goose Up\nCREATE TABLE two (x integer);\nINSERT INTO nowhere VALUES (1);\n")
	url := config.NewSecret(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database))
	_, migrated, err := open(t.Context(), Options{URL: url}, files, time.Second)
	if err == nil || !strings.Contains(err.Error(), "migrate the database's schema from version 0: migration 00002_two.sql failed: ") {
		t.Fatalf("Open = %v, want an error from the failed migration", err)
	}
	if diff := cmp.Diff([]store.Migration{{Version: 1, Name: "00001_sources.sql"}}, migrated.Applied); diff != "" {
		t.Errorf("applied (-want +got):\n%s", diff)
	}
	pool := srv.Pool(t, database)
	if got := tables(t, pool, Schema); slices.Contains(got, "two") || !slices.Contains(got, "sources") {
		t.Errorf("tables after the failed migration = %v, want only the first migration's", got)
	}
	if v := query(t, pool, "SELECT max(version_id) FROM netbox_trueup_ai.goose_db_version"); !slices.Equal(v, []string{"1"}) {
		t.Errorf("schema version = %v, want 1", v)
	}
}

func TestConcurrentOpeners(t *testing.T) {
	t.Parallel()
	database := srv.NewDatabase(t)
	url := config.NewSecret(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database))
	const openers = 8
	var wg sync.WaitGroup
	results := make([]store.Migrated, openers)
	errs := make([]error, openers)
	stores := make([]*Store, openers)
	start := time.Now()
	// Each keeps its store open until all have opened theirs, as processes
	// that start at once do.
	for i := range openers {
		wg.Go(func() { stores[i], results[i], errs[i] = Open(t.Context(), Options{URL: url}) })
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
	if n := query(t, srv.Pool(t, database), "SELECT count(*) FROM netbox_trueup_ai.goose_db_version WHERE version_id = 1"); !slices.Equal(n, []string{"1"}) {
		t.Errorf("migration 1 is recorded %v times, want once", n)
	}
}

func TestAMigrationWaitsForOpenStores(t *testing.T) {
	t.Parallel()
	url := srv.NewURL(t)
	older, _, err := Open(t.Context(), Options{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	files := withMigration(t, "00002_more.sql", "-- +goose Up\nALTER TABLE sources ADD COLUMN note text;\n")

	// A newer release can't migrate while an older one has the database
	// open, and gives up after its wait.
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	started := time.Now()
	_, _, err = open(t.Context(), Options{URL: url, Logger: logger}, files, 1500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "other TrueUp processes have had the database open for over 1.5s, so it can't be migrated") ||
		!errors.Is(err, errTimeout) {
		t.Errorf("migrating while a store is open = %v, want it to wait and give up", err)
	}
	if waited := time.Since(started); waited < 1500*time.Millisecond {
		t.Errorf("it gave up after %v", waited)
	}
	if !strings.Contains(logs.String(), "waiting for other TrueUp processes to close the database, to migrate it") {
		t.Errorf("waiting wasn't logged:\n%s", logs.String())
	}
	// The open store still works on the schema it knows.
	if err := older.RecordFetch(t.Context(), failedFetch()); err != nil {
		t.Errorf("RecordFetch on the open store: %v", err)
	}

	// While a migration waits, new processes wait for it, rather than keep
	// it waiting for ever, and the open store keeps working, new
	// connections included.
	type result struct {
		s        *Store
		migrated store.Migrated
		err      error
	}
	done := make(chan result)
	go func() {
		s, migrated, err := open(t.Context(), Options{URL: url}, files, 10*time.Second)
		done <- result{s, migrated, err}
	}()
	time.Sleep(300 * time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if _, _, err := Open(ctx, Options{URL: url}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("opening while a migration waits = %v, want it to wait for the migration", err)
	}
	older.pool.Reset()
	if err := older.RecordFetch(t.Context(), failedFetch()); err != nil {
		t.Errorf("RecordFetch on a new connection while a migration waits: %v", err)
	}

	// Once the older store closes, the migration goes ahead, and the older
	// release then refuses the database.
	closeStore(t, older)
	r := <-done
	if r.err != nil {
		t.Fatalf("migrating once the store closed: %v", r.err)
	}
	defer closeStore(t, r.s)
	if diff := cmp.Diff([]store.Migration{{Version: 2, Name: "00002_more.sql"}}, r.migrated.Applied); diff != "" {
		t.Errorf("applied (-want +got):\n%s", diff)
	}
	if _, _, err := Open(t.Context(), Options{URL: url}); !errors.As(err, new(*store.SchemaTooNewError)) {
		t.Errorf("the older release opening the migrated database = %v, want a SchemaTooNewError", err)
	}
}

func TestOpeningWaitsForAMigration(t *testing.T) {
	t.Parallel()
	database := srv.NewDatabase(t)
	url := config.NewSecret(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database))
	// As another process does while it migrates.
	migrating, err := pgx.Connect(t.Context(), url.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrating.Close(context.Background()) }()
	if err := exclusive(gateKey).acquire(t.Context(), migrating, 0, nil); err != nil {
		t.Fatal(err)
	}

	_, _, err = open(t.Context(), Options{URL: url}, withMigration(t, "00002_x.sql", "-- +goose Up\n"), 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "another TrueUp process has been migrating the database, or waiting to, for over 300ms") ||
		!errors.Is(err, errTimeout) {
		t.Errorf("Open during a migration = %v, want it to wait and give up", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, _, err := Open(ctx, Options{URL: url}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Open during a migration = %v, want it to wait until the context ends", err)
	}
	if got := tables(t, srv.Pool(t, database), Schema); len(got) != 0 {
		t.Errorf("Opens that waited created %v", got)
	}
	if err := exclusive(gateKey).release(t.Context(), migrating); err != nil {
		t.Fatal(err)
	}
	closeStore(t, openURL(t, url))
}

func TestAWaitEndsOnTheServerToo(t *testing.T) {
	t.Parallel()
	database := srv.NewDatabase(t)
	url := config.NewSecret(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database))
	openURL(t, url) // an older store, open throughout
	files := withMigration(t, "00002_x.sql", "-- +goose Up\n")

	// A migration interrupted while it waits for the exclusive lock leaves
	// no request in the lock's queue, which would keep stores from opening
	// until its lock_timeout.
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if _, _, err := open(ctx, Options{URL: url}, files, time.Minute); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Open = %v, want it to wait until the context ends", err)
	}
	pool := srv.Pool(t, database)
	const waiting = "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND database = (SELECT oid FROM pg_database WHERE datname = current_database()) AND NOT granted"
	deadline := time.Now().Add(2 * time.Second)
	for n := query(t, pool, waiting); !slices.Equal(n, []string{"0"}); n = query(t, pool, waiting) {
		if time.Now().After(deadline) {
			t.Fatalf("%v requests for advisory locks still wait on the server", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	s, _, err := Open(ctx, Options{URL: url})
	if err != nil {
		t.Fatalf("Open after the interrupted migration: %v", err)
	}
	closeStore(t, s)
}

func TestAStoreThatLosesItsOwnConnection(t *testing.T) {
	t.Parallel()
	files := withMigration(t, "00002_more.sql", "-- +goose Up\nALTER TABLE sources ADD COLUMN note text;\n")

	// lose opens a store in a new database, gives it a pooled connection,
	// and ends the session of the connection that holds its lock, as a
	// network failure or a database administrator could.
	lose := func(t *testing.T) (*Store, config.Secret) {
		t.Helper()
		database := srv.NewDatabase(t)
		url := config.NewSecret(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database))
		s, _, err := Open(t.Context(), Options{URL: url})
		if err != nil {
			t.Fatal(err)
		}
		// Close can't release a lock that went with its session, and
		// says so.
		t.Cleanup(func() {
			if err := s.Close(); err == nil || !strings.Contains(err.Error(), "the connection that held off migrations broke while the database was open: ") {
				t.Errorf("Close = %v, want it to say the lock's connection broke", err)
			}
		})
		if _, err := s.Sources(t.Context()); err != nil {
			t.Fatal(err)
		}
		var ended bool
		if err := srv.Pool(t, database).QueryRow(t.Context(), "SELECT pg_terminate_backend($1)", s.lock.PgConn().PID()).Scan(&ended); err != nil || !ended {
			t.Fatalf("end the lock's session: %v, %v", ended, err)
		}
		return s, url
	}

	t.Run("a migration waits for its pooled connections", func(t *testing.T) {
		t.Parallel()
		s, url := lose(t)
		if _, _, err := open(t.Context(), Options{URL: url}, files, time.Second); !errors.Is(err, errTimeout) {
			t.Fatalf("migrating while the store has a pooled connection = %v, want it to wait and give up", err)
		}
		if err := s.RecordFetch(t.Context(), failedFetch()); err != nil {
			t.Errorf("RecordFetch on the pooled connection: %v", err)
		}

		// While the migration waits, the store can't open another
		// connection.
		done := make(chan error)
		go func() {
			migrated, _, err := open(t.Context(), Options{URL: url}, files, 10*time.Second)
			if err == nil {
				closeStore(t, migrated)
			}
			done <- err
		}()
		time.Sleep(300 * time.Millisecond)
		held, err := s.pool.Acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Sources(t.Context()); err == nil || !strings.Contains(err.Error(), "another TrueUp process is migrating the database") {
			t.Errorf("Sources on a new connection while a migration waits = %v, want it refused", err)
		}
		held.Release()
		s.pool.Reset()
		if err := <-done; err != nil {
			t.Errorf("migrating once the pooled connections closed: %v", err)
		}
	})

	t.Run("its connections check the schema", func(t *testing.T) {
		t.Parallel()
		s, url := lose(t)
		s.pool.Reset()
		migrated, _, err := open(t.Context(), Options{URL: url}, files, 5*time.Second)
		if err != nil {
			t.Fatalf("migrating once the pooled connections closed: %v", err)
		}
		closeStore(t, migrated)
		if _, err := s.Sources(t.Context()); err == nil || !strings.Contains(err.Error(), "the database's schema changed from version 1 to 2 while TrueUp had it open") {
			t.Errorf("Sources after the migration = %v, want it refused", err)
		}
	})
}

func TestOwnership(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup string // SQL a database administrator, or another application, ran first
		want  string // a substring of the error, or "" to open
	}{
		{
			name: "another application's schema", setup: "CREATE SCHEMA netbox_trueup_ai; CREATE TABLE netbox_trueup_ai.dashboards (id integer)",
			want: "the schema netbox_trueup_ai in the database postgres://127.0.0.1:",
		},
		{
			name: "another application's schema with a view", setup: "CREATE SCHEMA netbox_trueup_ai; CREATE VIEW netbox_trueup_ai.v AS SELECT 1 AS x",
			want: "holds another application's tables, not TrueUp's",
		},
		{name: "an empty schema made for TrueUp", setup: "CREATE SCHEMA netbox_trueup_ai"},
		{name: "a first migration that didn't finish", setup: "CREATE SCHEMA netbox_trueup_ai; CREATE TABLE netbox_trueup_ai.goose_db_version (id integer PRIMARY KEY GENERATED BY DEFAULT AS IDENTITY, version_id bigint NOT NULL, is_applied boolean NOT NULL, tstamp timestamp NOT NULL DEFAULT now()); INSERT INTO netbox_trueup_ai.goose_db_version (version_id, is_applied) VALUES (0, true)"},
		{name: "other applications' tables in other schemas", setup: "CREATE TABLE public.dashboards (id integer); CREATE SCHEMA app; CREATE TABLE app.sources (x integer)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			database := srv.NewDatabase(t)
			srv.Exec(t, database, tt.setup)
			pool := srv.Pool(t, database)
			before := tables(t, pool, Schema)

			s, _, err := Open(t.Context(), Options{URL: config.NewSecret(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database))})
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				closeStore(t, s)
				if got := comment(t, pool); !slices.Equal(got, []string{schemaMark}) {
					t.Errorf("the schema's comment = %v, want TrueUp's mark", got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Open = %v, want an error saying %q", err, tt.want)
			}
			if after := tables(t, pool, Schema); !slices.Equal(before, after) {
				t.Errorf("Open changed the schema's tables from %v to %v", before, after)
			}
			if got := comment(t, pool); !slices.Equal(got, []string{"<nil>"}) {
				t.Errorf("the schema's comment = %v, want none", got)
			}
		})
	}
}

func TestPrivileges(t *testing.T) {
	t.Parallel()
	database := srv.NewDatabase(t)
	role, url := newRole(t, database)

	// A role that may connect, but not create schemas.
	_, _, err := Open(t.Context(), Options{URL: url})
	if err == nil || !strings.Contains(err.Error(), "create the schema netbox_trueup_ai: ") ||
		!strings.Contains(err.Error(), "grant TrueUp's role CREATE on the database, or create the schema for it") {
		t.Fatalf("Open without the privilege to create a schema = %v", err)
	}
	if got := tables(t, srv.Pool(t, database), Schema); len(got) != 0 {
		t.Errorf("Open created %v", got)
	}

	// A database administrator creates the schema for it.
	srv.Exec(t, database, "CREATE SCHEMA netbox_trueup_ai AUTHORIZATION "+role)
	s := openURL(t, url)
	if err := s.RecordFetch(t.Context(), failedFetch()); err != nil {
		t.Errorf("RecordFetch: %v", err)
	}
}

func TestEncoding(t *testing.T) {
	t.Parallel()
	database := srv.NewDatabase(t) + "_latin1"
	srv.Exec(t, "postgres", "CREATE DATABASE "+database+" TEMPLATE template0 ENCODING 'LATIN1' LOCALE 'C'")
	_, _, err := Open(t.Context(), Options{URL: config.NewSecret(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database))})
	if err == nil || !strings.Contains(err.Error(), "holds text as LATIN1, but TrueUp needs UTF8") {
		t.Errorf("Open on a LATIN1 database = %v", err)
	}
}

func TestSearchPath(t *testing.T) {
	t.Parallel()
	database := srv.NewDatabase(t)
	role, url := newRole(t, database)
	srv.Exec(t, "postgres", "ALTER DATABASE "+database+" OWNER TO "+role)
	// Everything that could point TrueUp elsewhere: the role's setting, the
	// URL's options, and tables of the same names in public, which TrueUp
	// must neither use nor change.
	srv.Exec(t, "postgres", "ALTER ROLE "+role+" SET search_path = public")
	srv.Exec(t, database, "CREATE TABLE public.sources (x integer); CREATE TABLE public.goose_db_version (x integer); GRANT ALL ON public.sources, public.goose_db_version TO "+role)
	url = config.NewSecret(url.Reveal() + "&options=-c%20search_path%3Dpublic")

	s := openURL(t, url)
	if err := s.RecordFetch(t.Context(), failedFetch()); err != nil {
		t.Fatalf("RecordFetch: %v", err)
	}
	pool := srv.Pool(t, database)
	if n := query(t, pool, "SELECT count(*) FROM netbox_trueup_ai.sources"); !slices.Equal(n, []string{"1"}) {
		t.Errorf("TrueUp's sources table holds %v rows, want 1", n)
	}
	for _, table := range []string{"public.sources", "public.goose_db_version"} {
		if n := query(t, pool, "SELECT count(*) FROM "+table); !slices.Equal(n, []string{"0"}) {
			t.Errorf("%s holds %v rows, want none", table, n)
		}
	}
}

// advisoryLocks lists the advisory locks in the database, granted or waited
// for, as "key mode granted".
func advisoryLocks(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	return query(t, pool, `
SELECT objid || ' ' || mode || ' ' || granted FROM pg_locks
WHERE locktype = 'advisory' AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY 1`)
}

// relay forwards connections to target, delaying each piece of data as a
// network does, and returns its address. What a process sends as it opens a
// connection is lost if the process has closed the connection, as it does
// when it exits, before the delay is over: the network would not have
// carried it yet.
func relay(t *testing.T, target string, delay time.Duration) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	pipe := func(dst, src net.Conn) {
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				time.Sleep(delay)
				if _, werr := dst.Write(buf[:n]); werr != nil {
					err = werr
				}
			}
			if err != nil {
				_ = dst.Close()
				_ = src.Close()
				return
			}
		}
	}
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				time.Sleep(delay)
				first := make([]byte, 32*1024)
				n, err := client.Read(first)
				if err != nil {
					_ = client.Close()
					return
				}
				// A client that waits for an answer is still there; one that
				// has gone reads as the end of the connection at once.
				_ = client.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
				more, err := client.Read(first[n:])
				if more == 0 && errors.Is(err, io.EOF) {
					_ = client.Close()
					return
				}
				_ = client.SetReadDeadline(time.Time{})
				var d net.Dialer
				server, err := d.DialContext(t.Context(), "tcp", target)
				if err != nil {
					_ = client.Close()
					return
				}
				if _, err := server.Write(first[:n+more]); err != nil {
					_ = client.Close()
					_ = server.Close()
					return
				}
				go pipe(server, client)
				pipe(client, server)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestAnInterruptedProcess(t *testing.T) {
	t.Parallel()
	database := srv.NewDatabase(t)
	openURL(t, config.NewSecret(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database))) // an older store, open throughout
	host, port, err := net.SplitHostPort(relay(t, net.JoinHostPort(srv.Host, srv.Port), 20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	viaRelay := strings.Replace(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database), srv.Host+":"+srv.Port, host+":"+port, 1)

	// A newer release, interrupted while it waits to migrate, holding the
	// gate, exits at once. Its wait ends on the server too, though the
	// cancel request takes a while to cross the network.
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$") //nolint:gosec // G204: the test binary runs itself as a helper
	// The race detector waits a second as a process exits, which a release
	// build doesn't.
	cmd.Env = append(os.Environ(), helperURL+"="+viaRelay, "GORACE=atexit_sleep_ms=0")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "waiting for the database's lock: context deadline exceeded") {
		t.Fatalf("the interrupted process: %v\n%s", err, out)
	}
	pool := srv.Pool(t, database)
	deadline := time.Now().Add(3 * time.Second)
	for {
		locks := advisoryLocks(t, pool)
		if slices.Equal(locks, []string{"1 ShareLock true"}) { // the older store's
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the interrupted process exited, the database's advisory locks are %v, want only the older store's", locks)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestAMigrationWhoseLockConnectionBreaks(t *testing.T) {
	t.Parallel()
	database := srv.NewDatabase(t)
	url := config.NewSecret(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database))
	seed := openURL(t, url)
	if err := seed.RecordFetch(t.Context(), failedFetch()); err != nil {
		t.Fatal(err)
	}
	closeStore(t, seed)
	// A migration that isn't safe to apply twice, and takes a while.
	files := withMigration(t, "00002_data.sql", "-- +goose Up\nUPDATE sources SET url = url || '/x';\nSELECT pg_sleep(2);\n")
	pool := srv.Pool(t, database)

	first := make(chan error, 1)
	go func() {
		s, _, err := open(t.Context(), Options{URL: url}, files, 10*time.Second)
		if err == nil {
			closeStore(t, s)
		}
		first <- err
	}()
	// Once the migration runs, end the session that holds the process's
	// locks, as a network failure would.
	var pid int32
	deadline := time.Now().Add(5 * time.Second)
	for running := []string(nil); len(running) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("the migration didn't start")
		}
		time.Sleep(20 * time.Millisecond)
		running = query(t, pool, "SELECT pid FROM pg_stat_activity WHERE datname = current_database() AND query LIKE '%pg_sleep(2)%' AND query NOT LIKE '%pg_stat_activity%'")
	}
	if err := pool.QueryRow(t.Context(), "SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND objid = 1 AND mode = 'ExclusiveLock' AND granted AND database = (SELECT oid FROM pg_database WHERE datname = current_database())").Scan(&pid); err != nil {
		t.Fatalf("find the migrating process's lock connection: %v", err)
	}
	var ended bool
	if err := pool.QueryRow(t.Context(), "SELECT pg_terminate_backend($1)", pid).Scan(&ended); err != nil || !ended {
		t.Fatalf("end the migrating process's lock connection: %v, %v", ended, err)
	}

	// Another process, which now gets past the gate, waits for goose's lock
	// and finds the migration applied.
	second, migrated, err := open(t.Context(), Options{URL: url}, files, 10*time.Second)
	if err != nil {
		t.Fatalf("the second process: %v", err)
	}
	closeStore(t, second)
	if len(migrated.Applied) != 0 || migrated.Version != 2 {
		t.Errorf("the second process migrated %+v, want nothing applied, at version 2", migrated)
	}
	if err := <-first; err == nil || !strings.Contains(err.Error(), "the migrations were applied, but the connection that held the database's lock broke meanwhile") {
		t.Errorf("the first process = %v, want it to say it lost its lock after migrating", err)
	}
	if got := query(t, pool, "SELECT url FROM netbox_trueup_ai.sources"); !slices.Equal(got, []string{"https://git.example.com/lib.git/x"}) {
		t.Errorf("the migrated URL = %v, want it changed once", got)
	}
	if n := query(t, pool, "SELECT count(*) FROM netbox_trueup_ai.goose_db_version WHERE version_id = 2"); !slices.Equal(n, []string{"1"}) {
		t.Errorf("migration 2 is recorded %v times, want once", n)
	}
}

func TestServerTimeouts(t *testing.T) {
	t.Parallel()

	t.Run("idle_session_timeout doesn't end a store's lock", func(t *testing.T) {
		t.Parallel()
		database := srv.NewDatabase(t)
		srv.Exec(t, "postgres", "ALTER DATABASE "+database+" SET idle_session_timeout = '300ms'")
		url := config.NewSecret(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database))
		s := openURL(t, url)
		time.Sleep(time.Second)
		files := withMigration(t, "00002_x.sql", "-- +goose Up\n")
		if _, _, err := open(t.Context(), Options{URL: url}, files, time.Second); !errors.Is(err, errTimeout) {
			t.Errorf("migrating while an idle store is open = %v, want it to wait and give up", err)
		}
		if _, err := s.Sources(t.Context()); err != nil {
			t.Errorf("Sources on the idle store: %v", err)
		}
	})

	t.Run("statement_timeout doesn't cut waits or migrations short", func(t *testing.T) {
		t.Parallel()
		database := srv.NewDatabase(t)
		srv.Exec(t, "postgres", "ALTER DATABASE "+database+" SET statement_timeout = '200ms'")
		url := config.NewSecret(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database))
		migrating, err := pgx.Connect(t.Context(), url.Reveal())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = migrating.Close(context.Background()) }()
		if err := exclusive(gateKey).acquire(t.Context(), migrating, 0, nil); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, _, err = open(t.Context(), Options{URL: url}, withMigration(t, "00002_x.sql", "-- +goose Up\n"), time.Second)
		if !errors.Is(err, errTimeout) || time.Since(start) < time.Second {
			t.Errorf("Open while the gate is held = %v after %v, want it to wait its second and give up", err, time.Since(start))
		}
		if err := exclusive(gateKey).release(t.Context(), migrating); err != nil {
			t.Fatal(err)
		}
		slow := withMigration(t, "00002_slow.sql", "-- +goose Up\nSELECT pg_sleep(0.5);\n")
		s, _, err := open(t.Context(), Options{URL: url}, slow, time.Second)
		if err != nil {
			t.Fatalf("a migration that takes longer than statement_timeout: %v", err)
		}
		closeStore(t, s)
	})

	t.Run("a migration waits only so long for a table", func(t *testing.T) {
		t.Parallel()
		database := srv.NewDatabase(t)
		url := config.NewSecret(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database))
		closeStore(t, openURL(t, url))
		// Another session reads the table, as pg_dump does.
		reader, err := pgx.Connect(t.Context(), url.Reveal())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = reader.Close(context.Background()) }()
		tx, err := reader.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(t.Context(), "LOCK TABLE netbox_trueup_ai.sources IN ACCESS SHARE MODE"); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		files := withMigration(t, "00002_more.sql", "-- +goose Up\nALTER TABLE sources ADD COLUMN note text;\n")
		_, _, err = open(ctx, Options{URL: url}, files, time.Second)
		if err == nil || !strings.Contains(err.Error(), "migration 00002_more.sql waited over 1s for a table that another session holds, such as a pg_dump") {
			t.Errorf("migrating while another session reads the table = %v", err)
		}
	})
}

func TestOtherRolesInTheSchema(t *testing.T) {
	t.Parallel()
	// plant is SQL another role runs: functions that would win a call of
	// TrueUp's, were it to look in the schema, by matching better than the
	// catalog's. Each records that it ran.
	const plant = `
CREATE FUNCTION netbox_trueup_ai.obj_description(oid, text) RETURNS text LANGUAGE sql
    AS $$ INSERT INTO public.calls VALUES ('obj_description') RETURNING 'netbox-trueup-ai' $$;
CREATE FUNCTION netbox_trueup_ai.pg_try_advisory_lock(text, text) RETURNS boolean LANGUAGE sql
    AS $$ INSERT INTO public.calls VALUES ('pg_try_advisory_lock') RETURNING true $$;
CREATE FUNCTION netbox_trueup_ai.pg_try_advisory_lock_shared(text, text) RETURNS boolean LANGUAGE sql
    AS $$ INSERT INTO public.calls VALUES ('pg_try_advisory_lock_shared') RETURNING true $$;
CREATE FUNCTION netbox_trueup_ai.to_regclass(text) RETURNS regclass LANGUAGE sql
    AS $$ INSERT INTO public.calls VALUES ('to_regclass') RETURNING NULL::regclass $$;`
	tests := []struct {
		name  string
		setup func(trueup, other string) string // SQL a database administrator, or the other role, ran first
		want  string
	}{
		{
			name: "a schema another role made first",
			setup: func(_, other string) string {
				return "SET ROLE " + other + "; CREATE SCHEMA netbox_trueup_ai; GRANT ALL ON SCHEMA netbox_trueup_ai TO PUBLIC;" + plant + " RESET ROLE"
			},
			want: "belongs to another role than the one TrueUp connects as, which could make TrueUp run its code",
		},
		{
			name: "that schema, given to TrueUp's role afterwards",
			setup: func(trueup, other string) string {
				return "SET ROLE " + other + "; CREATE SCHEMA netbox_trueup_ai;" + plant + " RESET ROLE; ALTER SCHEMA netbox_trueup_ai OWNER TO " + trueup
			},
			want: "holds objects that another role owns, which could make TrueUp run that role's code",
		},
		{
			name: "TrueUp's schema, which another role may create objects in",
			setup: func(trueup, other string) string {
				return "CREATE SCHEMA netbox_trueup_ai AUTHORIZATION " + trueup + "; GRANT CREATE ON SCHEMA netbox_trueup_ai TO " + other
			},
			want: "other roles may create objects in the schema netbox_trueup_ai",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			database := srv.NewDatabase(t)
			trueup, url := newRole(t, database)
			other := "other_" + database
			srv.Exec(t, "postgres", "CREATE ROLE "+other)
			srv.Exec(t, "postgres", "GRANT CREATE ON DATABASE "+database+" TO "+other+", "+trueup)
			srv.Exec(t, database, "CREATE TABLE public.calls (what text); GRANT INSERT ON public.calls TO PUBLIC")
			srv.Exec(t, database, tt.setup(trueup, other))

			_, _, err := Open(t.Context(), Options{URL: url})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Open = %v, want an error saying %q", err, tt.want)
			}
			pool := srv.Pool(t, database)
			if calls := query(t, pool, "SELECT what FROM public.calls"); len(calls) != 0 {
				t.Errorf("TrueUp ran the other role's functions: %v", calls)
			}
			if got := comment(t, pool); slices.Equal(got, []string{schemaMark}) {
				t.Error("Open marked the schema as TrueUp's")
			}
		})
	}
}
