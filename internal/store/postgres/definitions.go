package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/postgres/postgresdb"
)

// upsertBatch is how many definitions RecordLoad sends in one statement:
// about a megabyte of devicetype-library's.
const upsertBatch = 1000

// RecordLoad records a load in one transaction, which first locks the
// source's row, as fetches, removals and recordings of the source's
// comparisons do, so that they take turns. It deletes the definitions the
// source no longer holds, then writes the others in batches, leaving a row
// that hasn't changed as it is. It reads committed data, whatever the
// server's default, so that once it has the lock it sees the snapshot that
// a fetch it waited for recorded.
func (s *Store) RecordLoad(ctx context.Context, load store.Load) error {
	load, err := load.Prepare()
	if err != nil {
		return fmt.Errorf("record a load: %w", err)
	}
	at := pgtype.Timestamptz{Time: load.StartedAt, Valid: true}
	err = s.inTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(q *postgresdb.Queries) error {
		if _, err := q.LockSource(ctx, load.Source); errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: it was never fetched", store.ErrSnapshotChanged)
		} else if err != nil {
			return err
		}
		version, err := q.GetSnapshotVersion(ctx, load.Source)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w: it has no snapshot", store.ErrSnapshotChanged)
		case err != nil:
			return err
		case version != load.Version:
			return fmt.Errorf("%w: the snapshot is of version %q, not %q", store.ErrSnapshotChanged, version, load.Version)
		}
		if load.Error != "" {
			return q.UpsertLoadFailed(ctx, postgresdb.UpsertLoadFailedParams{
				Source: load.Source, StartedAt: at, Version: load.Version, Reader: load.Reader, Settings: load.Settings,
				Error: pgtype.Text{String: load.Error, Valid: true},
			})
		}
		if err := q.UpsertLoadReady(ctx, postgresdb.UpsertLoadReadyParams{
			Source: load.Source, StartedAt: at, Version: load.Version, Reader: load.Reader, Settings: load.Settings,
			Files: int64(load.Files), InvalidFiles: int64(load.InvalidFiles),
		}); err != nil {
			return err
		}
		if err := deleteGoneDefinitions(ctx, q, load); err != nil {
			return err
		}
		for start := 0; start < len(load.Definitions); start += upsertBatch {
			batch := load.Definitions[start:min(start+upsertBatch, len(load.Definitions))]
			p := postgresdb.UpsertDefinitionsParams{Source: load.Source}
			for i, d := range batch {
				p.ObjectTypes = append(p.ObjectTypes, d.ObjectType)
				p.Paths = append(p.Paths, d.Path)
				p.Identities = append(p.Identities, d.Identity)
				p.Seqs = append(p.Seqs, int64(start+i))
				p.Manufacturers = append(p.Manufacturers, d.Manufacturer)
				p.Models = append(p.Models, d.Model)
				p.Slugs = append(p.Slugs, d.Slug)
				p.PartNumbers = append(p.PartNumbers, d.PartNumber)
				p.Searches = append(p.Searches, d.SearchText())
				p.Digests = append(p.Digests, d.Digest)
				p.Definitions = append(p.Definitions, string(d.JSON))
				p.UniqueValues = append(p.UniqueValues, string(d.UniqueValues))
			}
			if err := q.UpsertDefinitions(ctx, p); err != nil {
				return fmt.Errorf("record definitions %d to %d: %w", start+1, start+len(batch), err)
			}
		}
		if err := q.DeleteDefinitionProblems(ctx, load.Source); err != nil {
			return err
		}
		if len(load.Problems) == 0 {
			return nil
		}
		rows := make([]postgresdb.InsertDefinitionProblemsParams, len(load.Problems))
		for i, p := range load.Problems {
			rows[i] = postgresdb.InsertDefinitionProblemsParams{
				Source: load.Source, Seq: int64(i), Severity: p.Severity, Path: p.Path,
				LineNumber: int64(p.Line), ColumnNumber: int64(p.Column), Field: p.Field, Reason: p.Reason,
			}
		}
		if _, err := q.InsertDefinitionProblems(ctx, rows); err != nil {
			return fmt.Errorf("record the problems: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("record the load of source %s: %w", load.Source, withoutUser(err))
	}
	return nil
}

// deleteGoneDefinitions deletes the source's definitions that a load
// doesn't hold, with their comparisons, by their IDs, worked out here rather than by a NOT IN over
// every definition, which PostgreSQL can't always hash.
func deleteGoneDefinitions(ctx context.Context, q *postgresdb.Queries, load store.Load) error {
	if len(load.Definitions) == 0 {
		if err := q.DeleteComparisons(ctx, load.Source); err != nil {
			return err
		}
		return q.DeleteDefinitions(ctx, load.Source)
	}
	keep := load.Keys()
	keys, err := q.ListDefinitionKeys(ctx, load.Source)
	if err != nil {
		return err
	}
	var gone []int64
	for _, k := range keys {
		if !keep[store.DefinitionKey{ObjectType: k.ObjectType, Path: k.Path, Identity: k.Identity}] {
			gone = append(gone, k.ID)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	if err := q.DeleteComparisonsOf(ctx, postgresdb.DeleteComparisonsOfParams{Source: load.Source, Ids: gone}); err != nil {
		return err
	}
	return q.DeleteDefinitionsByID(ctx, gone)
}

// LoadStatus returns what is recorded of the loads of a source's snapshot.
func (s *Store) LoadStatus(ctx context.Context, source string) (store.LoadStatus, error) {
	status, err := loadStatus(ctx, s.q, source)
	if err != nil {
		return store.LoadStatus{}, fmt.Errorf("read the loads of source %s: %w", source, withoutUser(err))
	}
	return status, nil
}

func loadStatus(ctx context.Context, q *postgresdb.Queries, source string) (store.LoadStatus, error) {
	row, err := q.GetLoadStatus(ctx, source)
	if errors.Is(err, pgx.ErrNoRows) {
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
		if status.StartedAt, err = readTime(row.StartedAt); err != nil {
			return store.LoadStatus{}, err
		}
	}
	if row.LoadedAt.Valid {
		if status.LoadedAt, err = readTime(row.LoadedAt); err != nil {
			return store.LoadStatus{}, err
		}
	}
	return status, nil
}

// Definitions returns a source's recorded definitions and problems, with
// the status of its loads, in one read-only transaction at REPEATABLE READ,
// so that all of them are as they were at one moment.
func (s *Store) Definitions(ctx context.Context, source, search string) (store.SourceDefinitions, error) {
	search, err := store.PrepareSearch(search)
	if err != nil {
		return store.SourceDefinitions{}, fmt.Errorf("read the definitions of source %s: %w", source, err)
	}
	return s.readDefinitions(ctx, source, true, func(q *postgresdb.Queries) ([]store.Definition, error) {
		var rows []postgresdb.ListDefinitionsRow
		if search == "" {
			rows, err = q.ListDefinitions(ctx, source)
		} else {
			var found []postgresdb.SearchDefinitionsRow
			found, err = q.SearchDefinitions(ctx, postgresdb.SearchDefinitionsParams{Source: source, Search: search})
			for _, r := range found {
				rows = append(rows, postgresdb.ListDefinitionsRow(r))
			}
		}
		return wholeDefinitions(rows), err
	})
}

// UniqueValues returns a source's recorded definitions, each with its
// unique values but not the whole of it, and its problems, with the status
// of its loads, in one read-only transaction at REPEATABLE READ.
func (s *Store) UniqueValues(ctx context.Context, source string) (store.SourceDefinitions, error) {
	return s.readDefinitions(ctx, source, true, func(q *postgresdb.Queries) ([]store.Definition, error) {
		rows, err := q.ListUniqueValues(ctx, source)
		defs := make([]store.Definition, len(rows))
		for i, r := range rows {
			defs[i] = store.Definition{
				DefinitionKey: store.DefinitionKey{ObjectType: r.ObjectType, Path: r.Path, Identity: r.Identity},
				ID:            r.ID,
				Manufacturer:  r.Manufacturer, Model: r.Model, Slug: r.Slug, PartNumber: r.PartNumber,
				Digest:       r.Digest,
				UniqueValues: []byte(r.UniqueValues),
			}
		}
		return defs, err
	})
}

// DefinitionsByID returns those of a source's recorded definitions with the
// IDs given, with the status of its loads, in one read-only transaction at
// REPEATABLE READ.
func (s *Store) DefinitionsByID(ctx context.Context, source string, ids []int64) (store.SourceDefinitions, error) {
	if ids == nil {
		ids = []int64{}
	}
	return s.readDefinitions(ctx, source, false, func(q *postgresdb.Queries) ([]store.Definition, error) {
		rows, err := q.ListDefinitionsByID(ctx, postgresdb.ListDefinitionsByIDParams{Source: source, Ids: ids})
		whole := make([]postgresdb.ListDefinitionsRow, len(rows))
		for i, r := range rows {
			whole[i] = postgresdb.ListDefinitionsRow(r)
		}
		return wholeDefinitions(whole), err
	})
}

// wholeDefinitions returns the definitions of rows.
func wholeDefinitions(rows []postgresdb.ListDefinitionsRow) []store.Definition {
	defs := make([]store.Definition, len(rows))
	for i, r := range rows {
		defs[i] = store.Definition{
			DefinitionKey: store.DefinitionKey{ObjectType: r.ObjectType, Path: r.Path, Identity: r.Identity},
			ID:            r.ID,
			Manufacturer:  r.Manufacturer, Model: r.Model, Slug: r.Slug, PartNumber: r.PartNumber,
			Digest:       r.Digest,
			JSON:         []byte(r.Definition),
			UniqueValues: []byte(r.UniqueValues),
		}
	}
	return defs
}

// readDefinitions reads the status of a source's loads, the definitions
// list returns, and its problems if withProblems, in one read-only
// transaction at REPEATABLE READ, so that all of them are as they were at
// one moment.
func (s *Store) readDefinitions(ctx context.Context, source string, withProblems bool,
	list func(*postgresdb.Queries) ([]store.Definition, error),
) (store.SourceDefinitions, error) {
	var loaded store.SourceDefinitions
	err := s.inTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(q *postgresdb.Queries) error {
		var err error
		if loaded.LoadStatus, err = loadStatus(ctx, q, source); err != nil {
			return err
		}
		if loaded.Definitions, err = list(q); err != nil {
			return fmt.Errorf("list its definitions: %w", err)
		}
		if !withProblems {
			return nil
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
		return store.SourceDefinitions{}, fmt.Errorf("read the definitions of source %s: %w", source, withoutUser(err))
	}
	return loaded, nil
}
