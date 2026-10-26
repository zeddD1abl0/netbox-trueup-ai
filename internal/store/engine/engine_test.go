package engine

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

// postgresURL is a PostgreSQL URL with a password that no message may repeat.
const postgresURL = "postgres://trueup:s3cret@db.example.com/trueup" //nolint:gosec // G101: a fake password in a test fixture

func TestOpen(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	other := filepath.Join(t.TempDir(), "other.db")
	otherURL := "sqlite:///" + strings.TrimPrefix(filepath.ToSlash(other), "/") // sqlite:///C:/... on Windows

	tests := []struct {
		name      string
		url       string
		dataDir   string
		want      string // the database file
		wantErr   string // a substring of the error
		configErr bool   // the error is a *ConfigError
	}{
		{name: "the default, in the data directory", dataDir: dataDir, want: filepath.Join(dataDir, DefaultFile)},
		{name: "a sqlite URL", url: otherURL, dataDir: dataDir, want: other},
		{name: "a relative data directory", dataDir: "data", wantErr: "the data directory must be an absolute path"},
		{name: "a relative sqlite URL", url: "sqlite:trueup.db", dataDir: dataDir, wantErr: "database.url must give the database file's absolute path", configErr: true},
		{name: "a PostgreSQL URL pgx can't use", url: postgresURL + "?sslmode=bogus", dataDir: dataDir, wantErr: "database.url can't be used to connect to PostgreSQL: sslmode is invalid", configErr: true},
		{name: "a postgresql URL pgx can't use", url: strings.Replace(postgresURL, "postgres:", "postgresql:", 1) + "?connect_timeout=soon", dataDir: dataDir, wantErr: "database.url can't be used to connect to PostgreSQL", configErr: true},
		// Nothing listens on port 1, so connecting fails at once.
		{name: "a PostgreSQL server that can't be reached", url: strings.Replace(postgresURL, "db.example.com", "127.0.0.1:1", 1) + "?sslmode=disable", dataDir: dataDir, wantErr: "connect to the database at postgres://127.0.0.1:1/trueup: "},
		{name: "another engine", url: strings.Replace(postgresURL, "postgres:", "mysql:", 1), dataDir: dataDir, wantErr: "must be a sqlite, postgres or postgresql URL", configErr: true},
		{name: "a URL that doesn't parse", url: strings.Replace(postgresURL, ".com/", ".com:port/", 1), dataDir: dataDir, wantErr: "database.url is not a valid URL", configErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, migrated, err := Open(t.Context(), Config{URL: config.NewSecret(tt.url), DataDir: tt.dataDir})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Open = %v, want an error saying %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "user=") {
					t.Errorf("the error repeats the password or names the user: %v", err)
				}
				if _, ok := errors.AsType[*ConfigError](err); ok != tt.configErr {
					t.Errorf("the error is a *ConfigError: %v, want %v", ok, tt.configErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Error(err)
			}
			if migrated.Engine != "sqlite" || migrated.Location != tt.want || migrated.Version == 0 {
				t.Errorf("Open migrated %+v, want SQLite at %s, at the latest version", migrated, tt.want)
			}
		})
	}
}
