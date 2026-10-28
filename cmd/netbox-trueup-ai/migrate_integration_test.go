//go:build integration

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/postgres/pgtest"
)

// pg is the PostgreSQL that TestMain starts.
var pg *pgtest.Server

func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, &pg))
}

func TestMigratePostgreSQL(t *testing.T) {
	t.Parallel()
	database := pg.NewDatabase(t)
	env := []string{"TRUEUP_DATA_DIR=" + t.TempDir(), "TRUEUP_DATABASE_URL=" + pg.URL(pgtest.Superuser, pgtest.SuperuserPassword, database)}
	location := "postgres://127.0.0.1:" + pg.Port + "/" + database

	r := runCLI(t, nil, env, "migrate")
	want := "Database:  PostgreSQL, " + location + "\nSchema:    version 1, was 0\nApplied:   00001_sources.sql\n"
	if r.code != exitOK || r.stdout != want {
		t.Errorf("migrate: exit code %d, stdout %q, want %q\nstderr: %s", r.code, r.stdout, want, r.stderr)
	}
	if !strings.Contains(r.stderr, `msg="applied a database migration" version=1 migration=00001_sources.sql`) {
		t.Errorf("stderr doesn't log the migration:\n%s", r.stderr)
	}

	r = runCLI(t, nil, env, "migrate", "-o", "json")
	if r.code != exitOK {
		t.Fatalf("migrate -o json: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	var got migrateJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("migrate -o json printed %q: %v", r.stdout, err)
	}
	wantJSON := migrateJSON{Engine: "postgres", Location: location, VersionBefore: 1, Version: 1, Applied: []migrationJSON{}}
	if diff := cmp.Diff(wantJSON, got); diff != "" {
		t.Errorf("migrate -o json (-want +got):\n%s", diff)
	}
	for _, out := range []string{r.stdout, r.stderr} {
		if strings.Contains(out, pgtest.SuperuserPassword) || strings.Contains(out, pgtest.Superuser+"@") || strings.Contains(out, "user=") {
			t.Errorf("output names the database's user or password: %s", out)
		}
	}
}
