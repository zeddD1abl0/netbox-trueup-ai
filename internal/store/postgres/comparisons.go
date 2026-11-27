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

// RecordComparisons records a run's comparisons in one transaction, which
// first locks the source's row, as fetches, loads and removals of the
// source do, so that they take turns, and writes the comparisons in one
// COPY. It reads committed data, whatever the server's default, so that
// once it has the lock it sees the snapshot and load that a fetch or load
// it waited for recorded.
func (s *Store) RecordComparisons(ctx context.Context, run store.ComparisonRun) error {
	run, err := run.Prepare()
	if err != nil {
		return fmt.Errorf("record comparisons: %w", err)
	}
	at := pgtype.Timestamptz{Time: run.ComparedAt, Valid: true}
	err = s.inTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(q *postgresdb.Queries) error {
		if _, err := q.LockSource(ctx, run.Source); errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: it was never fetched", store.ErrSnapshotChanged)
		} else if err != nil {
			return err
		}
		// A statement of its own, after the lock: see LockSource.
		status, err := loadStatus(ctx, q, run.Source)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: it has no snapshot", store.ErrSnapshotChanged)
		} else if err != nil {
			return err
		}
		if err := run.CheckLoad(status); err != nil {
			return err
		}
		target := run.Target()
		if run.Whole {
			err = q.DeleteTargetComparisons(ctx, postgresdb.DeleteTargetComparisonsParams{Source: run.Source, Branch: target})
		} else {
			err = q.DeleteTargetComparisonsOf(ctx, postgresdb.DeleteTargetComparisonsOfParams{Source: run.Source, Branch: target, Ids: run.IDs()})
		}
		if err != nil {
			return err
		}
		if len(run.Comparisons) == 0 {
			return nil
		}
		var name pgtype.Text
		var branchID pgtype.Int8
		if b := run.Branch; b != nil {
			name, branchID = pgtype.Text{String: b.Name, Valid: true}, pgtype.Int8{Int64: b.NetBoxID, Valid: true}
		}
		rows := make([]postgresdb.InsertComparisonsParams, len(run.Comparisons))
		for i, c := range run.Comparisons {
			rows[i] = postgresdb.InsertComparisonsParams{
				Source:         run.Source,
				DefinitionID:   c.DefinitionID,
				Branch:         target,
				BranchName:     name,
				BranchNetboxID: branchID,
				SourceVersion:  run.Loaded.Version,
				ComparedAt:     at,
				NetboxUrl:      run.NetBoxURL,
				NetboxVersion:  run.NetBoxVersion,
				Outcome:        c.Outcome,
				NetboxID:       pgtype.Int8{Int64: c.NetBoxID, Valid: c.NetBoxID != 0},
				Differences:    int64(c.Differences),
				Result:         string(c.Result),
			}
		}
		if _, err := q.InsertComparisons(ctx, rows); err != nil {
			return fmt.Errorf("record the comparisons: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("record the comparisons of source %s: %w", run.Source, err)
	}
	return nil
}

// Comparisons returns the comparisons recorded of a source's definitions.
func (s *Store) Comparisons(ctx context.Context, source string) ([]store.RecordedComparison, error) {
	rows, err := s.q.ListComparisons(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("list the comparisons of source %s: %w", source, withoutUser(err))
	}
	out := make([]store.RecordedComparison, 0, len(rows))
	for _, r := range rows {
		at, err := readTime(r.ComparedAt)
		if err != nil {
			return nil, fmt.Errorf("source %s: a comparison: %w", source, err)
		}
		out = append(out, store.RecordedComparison{
			Comparison: store.Comparison{
				DefinitionID: r.ID,
				Outcome:      r.Outcome,
				NetBoxID:     r.NetboxID.Int64,
				Differences:  int(r.Differences),
				Result:       []byte(r.Result),
			},
			DefinitionKey: store.DefinitionKey{ObjectType: r.ObjectType, Path: r.Path, Identity: r.Identity},
			Source:        source,
			SourceVersion: r.SourceVersion,
			ComparedAt:    at,
			NetBoxURL:     r.NetboxUrl,
			NetBoxVersion: r.NetboxVersion,
			Branch:        recordedBranch(r.Branch, r.BranchName.String, r.BranchNetboxID.Int64),
		})
	}
	return out, nil
}

// recordedBranch returns the branch a comparison was recorded in, as the
// database holds it, or nil for main, whose schema ID is empty.
func recordedBranch(schemaID, name string, id int64) *store.Branch {
	if schemaID == "" {
		return nil
	}
	return &store.Branch{SchemaID: schemaID, Name: name, NetBoxID: id}
}
