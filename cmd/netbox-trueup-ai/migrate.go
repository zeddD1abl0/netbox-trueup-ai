package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/engine"
)

func newMigrateCommand(a *app) *cobra.Command {
	var out output
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Create the database, or bring its schema up to date",
		Long: `Open TrueUp's database, creating it if it doesn't exist, and apply the
schema migrations it lacks. Unless database.url names another, the database
is SQLite, in the file ` + engine.DefaultFile + ` in the data directory.

Commands that use the database, such as source fetch, migrate it when they
open it, so running migrate is optional: it does the same as a step of its
own, such as after installing a new release. Migrations only go forward, so
back up the database before upgrading to be able to go back. Processes that
open the database at once take turns, and never apply a migration twice.

Exit codes:
  0  The schema is up to date: migrations were applied, or none were needed.
  1  The database couldn't be opened or migrated, or its schema is newer
     than this release knows.
  3  The configuration is invalid, or database.url names a database this
     release can't use.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig(cmd)
			if err != nil {
				return err
			}
			s, migrated, err := a.openStore(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			if err := s.Close(); err != nil {
				return err
			}
			result := newMigrateJSON(migrated)
			if out == outputJSON {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			return writeMigrateText(cmd.OutOrStdout(), result)
		},
	}
	addOutputFlag(cmd, &out)
	return cmd
}

// openStore opens TrueUp's database, applying the migrations it lacks.
func (a *app) openStore(ctx context.Context, cfg *config.Config) (store.Store, store.Migrated, error) {
	s, migrated, err := engine.Open(ctx, engine.Config{
		URL:     cfg.Settings.Database.URL,
		DataDir: cfg.Settings.DataDir,
		Logger:  a.logger,
	})
	switch {
	case err == nil:
		return s, migrated, nil
	case ctx.Err() != nil:
		return nil, migrated, fmt.Errorf("interrupted while opening the database: %w", ctx.Err())
	}
	if _, ok := errors.AsType[*engine.ConfigError](err); ok {
		return nil, migrated, configError{err}
	}
	return nil, migrated, fmt.Errorf("open the database: %w", err)
}

// migrateJSON is the result of migrate, printed as text or JSON. Its JSON
// fields are part of the CLI's public interface (ADR 0018).
type migrateJSON struct {
	Engine        string          `json:"engine"`
	Location      string          `json:"location"`
	VersionBefore int64           `json:"version_before"`
	Version       int64           `json:"version"`
	Applied       []migrationJSON `json:"applied"`
}

type migrationJSON struct {
	Version int64  `json:"version"`
	Name    string `json:"name"`
}

func newMigrateJSON(m store.Migrated) migrateJSON {
	r := migrateJSON{
		Engine:        m.Engine,
		Location:      m.Location,
		VersionBefore: m.VersionBefore,
		Version:       m.Version,
		Applied:       []migrationJSON{},
	}
	for _, a := range m.Applied {
		r.Applied = append(r.Applied, migrationJSON{Version: a.Version, Name: a.Name})
	}
	return r
}

// engineNames are the engines' names for text output.
var engineNames = map[string]string{"sqlite": "SQLite", "postgres": "PostgreSQL"}

func writeMigrateText(w io.Writer, r migrateJSON) error {
	var b strings.Builder
	name := engineNames[r.Engine]
	if name == "" {
		name = r.Engine
	}
	fmt.Fprintf(&b, "Database:  %s, %s\n", name, cell(r.Location))
	if len(r.Applied) == 0 {
		fmt.Fprintf(&b, "Schema:    version %d, up to date\n", r.Version)
	} else {
		fmt.Fprintf(&b, "Schema:    version %d, was %d\n", r.Version, r.VersionBefore)
		for i, m := range r.Applied {
			label := ""
			if i == 0 {
				label = "Applied:"
			}
			fmt.Fprintf(&b, "%-11s%s\n", label, cell(m.Name))
		}
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
