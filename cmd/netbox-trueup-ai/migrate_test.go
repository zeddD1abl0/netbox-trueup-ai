package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	_ "modernc.org/sqlite" // to change a database as no release would

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/engine"
)

// postgresURL is a PostgreSQL URL with a password that no output may repeat.
const postgresURL = "postgres://trueup:s3cret@db.example.com/trueup" //nolint:gosec // G101: a fake password in a test fixture

func TestMigrateText(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	env := []string{"TRUEUP_DATA_DIR=" + dataDir}
	path := filepath.Join(dataDir, engine.DefaultFile)

	r := runCLI(t, nil, env, "migrate")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	want := "Database:  SQLite, " + path + "\nSchema:    version 1, was 0\nApplied:   00001_sources.sql\n"
	if diff := cmp.Diff(want, r.stdout); diff != "" {
		t.Errorf("migrate (-want +got):\n%s", diff)
	}
	if !strings.Contains(r.stderr, `msg="applied a database migration" version=1 migration=00001_sources.sql`) {
		t.Errorf("stderr doesn't log the migration:\n%s", r.stderr)
	}

	r = runCLI(t, nil, env, "migrate")
	want = "Database:  SQLite, " + path + "\nSchema:    version 1, up to date\n"
	if r.code != exitOK || r.stdout != want {
		t.Errorf("migrate again: exit code %d, stdout %q, want %q", r.code, r.stdout, want)
	}
}

func TestMigrateJSON(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "elsewhere", "trueup.db")
	env := []string{"TRUEUP_DATA_DIR=" + t.TempDir(), "TRUEUP_DATABASE_URL=" + sqliteURL(path)}

	for i, want := range []migrateJSON{
		{Engine: "sqlite", Location: path, VersionBefore: 0, Version: 1, Applied: []migrationJSON{{Version: 1, Name: "00001_sources.sql"}}},
		{Engine: "sqlite", Location: path, VersionBefore: 1, Version: 1, Applied: []migrationJSON{}},
	} {
		r := runCLI(t, nil, env, "migrate", "-o", "json")
		if r.code != exitOK {
			t.Fatalf("run %d: exit code = %d\nstderr: %s", i, r.code, r.stderr)
		}
		var got migrateJSON
		if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
			t.Fatalf("migrate -o json printed %q: %v", r.stdout, err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("run %d (-want +got):\n%s", i, diff)
		}
		// The field names are public interface.
		for _, field := range []string{`"engine"`, `"location"`, `"version_before"`, `"version"`, `"applied"`} {
			if !strings.Contains(r.stdout, field) {
				t.Errorf("migrate -o json has no %s:\n%s", field, r.stdout)
			}
		}
	}
}

func TestMigrateFailures(t *testing.T) {
	t.Parallel()

	// A database whose schema a newer release made.
	newerDir := t.TempDir()
	if r := runCLI(t, nil, []string{"TRUEUP_DATA_DIR=" + newerDir}, "migrate"); r.code != exitOK {
		t.Fatalf("migrate: %s", r.stderr)
	}
	db, err := sql.Open("sqlite", filepath.Join(newerDir, engine.DefaultFile))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(t.Context(), "INSERT INTO goose_db_version (version_id, is_applied) VALUES (2, 1)")
	if closeErr := db.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}

	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		env        []string
		wantCode   int
		wantStderr string
	}{
		{
			name: "PostgreSQL before it is supported", env: []string{"TRUEUP_DATA_DIR=" + t.TempDir(), "TRUEUP_DATABASE_URL=" + postgresURL},
			wantCode: exitConfig, wantStderr: "Error: database.url names a PostgreSQL database, which this release can't use yet: unset database.url to use SQLite in the data directory\n",
		},
		{
			name: "a relative sqlite URL", env: []string{"TRUEUP_DATA_DIR=" + t.TempDir(), "TRUEUP_DATABASE_URL=sqlite:trueup.db"},
			wantCode: exitConfig, wantStderr: "must give the database file's absolute path",
		},
		{
			name: "a schema newer than this release", env: []string{"TRUEUP_DATA_DIR=" + newerDir},
			wantCode: exitFailure, wantStderr: "Error: open the database: the database's schema is at version 2, newer than this release of TrueUp knows (1): use a newer release\n",
		},
		{
			name: "a data directory that can't hold the database", env: []string{"TRUEUP_DATA_DIR=" + notADir},
			wantCode: exitFailure, wantStderr: "Error: open the database: create the database's directory: mkdir " + notADir + ": not a directory\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := runCLI(t, nil, tt.env, "migrate", "-o", "json")
			if r.code != tt.wantCode || r.stdout != "" || !strings.Contains(r.stderr, tt.wantStderr) {
				t.Errorf("exit code %d, stdout %q, stderr %q; want %d, nothing, and %q", r.code, r.stdout, r.stderr, tt.wantCode, tt.wantStderr)
			}
			if strings.Contains(r.stderr, "s3cret") {
				t.Errorf("stderr repeats the database's password: %s", r.stderr)
			}
		})
	}
}

// sqliteURL returns the sqlite URL of the file at the absolute path:
// sqlite:///tmp/x.db, or sqlite:///C:/x.db on Windows.
func sqliteURL(path string) string {
	return "sqlite:///" + strings.TrimPrefix(filepath.ToSlash(path), "/")
}

// records returns what the database in dataDir records about sources, and
// the snapshot of each source that has one.
func records(t *testing.T, dataDir string) ([]store.Source, map[string]store.Snapshot) {
	t.Helper()
	s, _, err := engine.Open(t.Context(), engine.Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	sources, err := s.Sources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	snaps := map[string]store.Snapshot{}
	for _, src := range sources {
		if src.Snapshot == nil {
			continue
		}
		if snaps[src.Name], err = s.Snapshot(t.Context(), src.Name); err != nil {
			t.Fatal(err)
		}
	}
	return sources, snaps
}
