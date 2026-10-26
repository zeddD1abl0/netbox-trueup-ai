//go:build integration

package engine

import (
	"os"
	"strings"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/postgres/pgtest"
)

// srv is the PostgreSQL that TestMain starts.
var srv *pgtest.Server

func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, &srv))
}

func TestOpenPostgreSQL(t *testing.T) {
	t.Parallel()
	for _, scheme := range []string{"postgres", "postgresql"} {
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()
			database := srv.NewDatabase(t)
			url := config.NewSecret(strings.Replace(srv.URL(pgtest.Superuser, pgtest.SuperuserPassword, database), "postgres:", scheme+":", 1))
			s, migrated, err := Open(t.Context(), Config{URL: url, DataDir: t.TempDir()})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Error(err)
			}
			want := "postgres://127.0.0.1:" + srv.Port + "/" + database
			if migrated.Engine != "postgres" || migrated.Location != want || migrated.Version == 0 {
				t.Errorf("Open migrated %+v, want PostgreSQL at %s, at the latest version", migrated, want)
			}
		})
	}
}
