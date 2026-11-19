package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/sqlite/sqlitedb"
)

// RecordLoad records a load in one transaction, which holds SQLite's write
// lock from its start, so that fetches and removals of the source wait for
// it, or it for them. It deletes the definitions the source no longer
// holds, then writes each of the others, leaving a row that hasn't changed
// as it is.
func (s *Store) RecordLoad(ctx context.Context, load store.Load) error {
	load, err := load.Prepare()
	if err != nil {
		return fmt.Errorf("record a load: %w", err)
	}
	at := load.StartedAt.Format(timeLayout)
	err = s.inTx(ctx, nil, func(q *sqlitedb.Queries) error {
		version, err := q.GetSnapshotVersion(ctx, load.Source)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w: it has no snapshot", store.ErrSnapshotChanged)
		case err != nil:
			return err
		case version != load.Version:
			return fmt.Errorf("%w: the snapshot is of version %q, not %q", store.ErrSnapshotChanged, version, load.Version)
		}
		if load.Error != "" {
			return q.UpsertLoadFailed(ctx, sqlitedb.UpsertLoadFailedParams{
				Source: load.Source, StartedAt: at, Version: load.Version, Reader: load.Reader, Settings: load.Settings,
				Error: sql.NullString{String: load.Error, Valid: true},
			})
		}
		if err := q.UpsertLoadReady(ctx, sqlitedb.UpsertLoadReadyParams{
			Source: load.Source, StartedAt: at, Version: load.Version, Reader: load.Reader, Settings: load.Settings,
			LoadedAt:       sql.NullString{String: at, Valid: true},
			LoadedVersion:  sql.NullString{String: load.Version, Valid: true},
			LoadedReader:   sql.NullString{String: load.Reader, Valid: true},
			LoadedSettings: sql.NullString{String: load.Settings, Valid: true},
			Files:          int64(load.Files),
			InvalidFiles:   int64(load.InvalidFiles),
		}); err != nil {
			return err
		}
		if err := deleteGoneDefinitions(ctx, q, load); err != nil {
			return err
		}
		for i, d := range load.Definitions {
			if err := q.UpsertDefinition(ctx, sqlitedb.UpsertDefinitionParams{
				Source:       load.Source,
				ObjectType:   d.ObjectType,
				Path:         d.Path,
				Identity:     d.Identity,
				Seq:          int64(i),
				Manufacturer: d.Manufacturer,
				Model:        d.Model,
				Slug:         d.Slug,
				PartNumber:   d.PartNumber,
				Search:       d.SearchText(),
				Digest:       d.Digest,
				Definition:   string(d.JSON),
			}); err != nil {
				return fmt.Errorf("the definition of %s: %w", d.DefinitionKey, err)
			}
		}
		if err := q.DeleteDefinitionProblems(ctx, load.Source); err != nil {
			return err
		}
		for i, p := range load.Problems {
			if err := q.InsertDefinitionProblem(ctx, sqlitedb.InsertDefinitionProblemParams{
				Source: load.Source, Seq: int64(i), Severity: p.Severity, Path: p.Path,
				LineNumber: int64(p.Line), ColumnNumber: int64(p.Column), Field: p.Field, Reason: p.Reason,
			}); err != nil {
				return fmt.Errorf("a problem of %q: %w", p.Path, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("record the load of source %s: %w", load.Source, err)
	}
	return nil
}

// deleteGoneDefinitions deletes the source's definitions that a load
// doesn't hold.
func deleteGoneDefinitions(ctx context.Context, q *sqlitedb.Queries, load store.Load) error {
	if len(load.Definitions) == 0 {
		return q.DeleteDefinitions(ctx, load.Source)
	}
	keep := load.Keys()
	keys, err := q.ListDefinitionKeys(ctx, load.Source)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if keep[store.DefinitionKey{ObjectType: k.ObjectType, Path: k.Path, Identity: k.Identity}] {
			continue
		}
		if err := q.DeleteDefinition(ctx, k.ID); err != nil {
			return err
		}
	}
	return nil
}

// LoadStatus returns what is recorded of the loads of a source's snapshot.
func (s *Store) LoadStatus(ctx context.Context, source string) (store.LoadStatus, error) {
	status, err := loadStatus(ctx, s.q, source)
	if err != nil {
		return store.LoadStatus{}, fmt.Errorf("read the loads of source %s: %w", source, err)
	}
	return status, nil
}

func loadStatus(ctx context.Context, q *sqlitedb.Queries, source string) (store.LoadStatus, error) {
	row, err := q.GetLoadStatus(ctx, source)
	if errors.Is(err, sql.ErrNoRows) {
		return store.LoadStatus{}, store.ErrNotFound
	}
	if err != nil {
		return store.LoadStatus{}, err
	}
	status := store.LoadStatus{
		Source: source, SnapshotVersion: row.SnapshotVersion, SnapshotRef: row.SnapshotRef,
		Latest: store.LoadKey{Version: row.Version.String, Reader: row.Reader.String, Settings: row.Settings.String},
		Error:  row.Error.String,
		Loaded: store.LoadKey{Version: row.LoadedVersion.String, Reader: row.LoadedReader.String, Settings: row.LoadedSettings.String},
		Files:  int(row.Files.Int64), InvalidFiles: int(row.InvalidFiles.Int64), Definitions: int(row.Definitions),
	}
	if row.StartedAt.Valid {
		if status.StartedAt, err = parseTime(row.StartedAt.String); err != nil {
			return store.LoadStatus{}, err
		}
	}
	if row.LoadedAt.Valid {
		if status.LoadedAt, err = parseTime(row.LoadedAt.String); err != nil {
			return store.LoadStatus{}, err
		}
	}
	return status, nil
}

// Definitions returns a source's recorded definitions and problems, with
// the status of its loads, in one read-only transaction.
func (s *Store) Definitions(ctx context.Context, source, search string) (store.SourceDefinitions, error) {
	search, err := store.PrepareSearch(search)
	if err != nil {
		return store.SourceDefinitions{}, fmt.Errorf("read the definitions of source %s: %w", source, err)
	}
	var loaded store.SourceDefinitions
	err = s.inTx(ctx, &sql.TxOptions{ReadOnly: true}, func(q *sqlitedb.Queries) error {
		if loaded.LoadStatus, err = loadStatus(ctx, q, source); err != nil {
			return err
		}
		var rows []sqlitedb.ListDefinitionsRow
		if search == "" {
			rows, err = q.ListDefinitions(ctx, source)
		} else {
			var found []sqlitedb.SearchDefinitionsRow
			found, err = q.SearchDefinitions(ctx, sqlitedb.SearchDefinitionsParams{Source: source, Search: search})
			for _, r := range found {
				rows = append(rows, sqlitedb.ListDefinitionsRow(r))
			}
		}
		if err != nil {
			return fmt.Errorf("list its definitions: %w", err)
		}
		loaded.Definitions = make([]store.Definition, len(rows))
		for i, r := range rows {
			loaded.Definitions[i] = store.Definition{
				DefinitionKey: store.DefinitionKey{ObjectType: r.ObjectType, Path: r.Path, Identity: r.Identity},
				ID:            r.ID,
				Manufacturer:  r.Manufacturer, Model: r.Model, Slug: r.Slug, PartNumber: r.PartNumber,
				Digest: r.Digest,
				JSON:   []byte(r.Definition),
			}
		}
		problems, err := q.ListDefinitionProblems(ctx, source)
		if err != nil {
			return fmt.Errorf("list its problems: %w", err)
		}
		loaded.Problems = make([]store.Problem, len(problems))
		for i, p := range problems {
			loaded.Problems[i] = store.Problem{
				Severity: p.Severity, Path: p.Path, Line: int(p.LineNumber), Column: int(p.ColumnNumber), Field: p.Field, Reason: p.Reason,
			}
		}
		return nil
	})
	if err != nil {
		return store.SourceDefinitions{}, fmt.Errorf("read the definitions of source %s: %w", source, err)
	}
	return loaded, nil
}
