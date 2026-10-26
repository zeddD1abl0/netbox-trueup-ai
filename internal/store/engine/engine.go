// Package engine opens TrueUp's database with the engine that database.url
// names: SQLite, by default in the data directory, and from roadmap step 7
// PostgreSQL. It is the only package that knows which engines exist, so the
// rest of TrueUp works with store.Store alone.
package engine

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"path/filepath"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/sqlite"
)

// DefaultFile is the SQLite database's file in the data directory, used when
// database.url isn't set.
const DefaultFile = "netbox-trueup-ai.db"

// Config says which database to open.
type Config struct {
	// URL is database.url, or unset for SQLite in DataDir.
	URL config.Secret
	// DataDir is data_dir, an absolute path.
	DataDir string
	// Logger gets a record of each migration applied. Nil means no
	// logging.
	Logger *slog.Logger
}

// ConfigError means database.url names a database this release can't open,
// such as one on an engine it doesn't support yet. It is a problem with the
// configuration rather than with the database.
type ConfigError struct{ msg string }

func (e *ConfigError) Error() string { return e.msg }

// Open opens the database, creating it if needed, and applies the
// migrations it lacks, under the engine's lock. It refuses a schema newer
// than this release knows, with a *store.SchemaTooNewError. Close the store
// when done.
func Open(ctx context.Context, cfg Config) (store.Store, store.Migrated, error) {
	path, err := sqlitePath(cfg)
	if err != nil {
		return nil, store.Migrated{}, err
	}
	s, migrated, err := sqlite.Open(ctx, sqlite.Options{Path: path, Logger: cfg.Logger})
	if err != nil {
		return nil, migrated, err
	}
	return s, migrated, nil
}

// sqlitePath returns the SQLite database's file. It never repeats the URL,
// which can hold a password.
func sqlitePath(cfg Config) (string, error) {
	if !cfg.URL.IsSet() {
		if !filepath.IsAbs(cfg.DataDir) {
			return "", errors.New("the data directory must be an absolute path")
		}
		return filepath.Join(cfg.DataDir, DefaultFile), nil
	}
	raw := cfg.URL.Reveal()
	u, err := url.Parse(raw)
	if err != nil {
		return "", &ConfigError{"database.url is not a valid URL"}
	}
	switch u.Scheme {
	case "sqlite":
		path, problem := config.SQLitePath(raw)
		if problem != "" {
			return "", &ConfigError{"database.url " + problem}
		}
		return path, nil
	case "postgres", "postgresql":
		return "", &ConfigError{"database.url names a PostgreSQL database, which this release can't use yet: unset database.url to use SQLite in the data directory"}
	default:
		return "", &ConfigError{"database.url must be a sqlite, postgres or postgresql URL"}
	}
}
