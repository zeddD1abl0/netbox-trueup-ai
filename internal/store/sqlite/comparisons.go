package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/sqlite/sqlitedb"
)

// RecordComparisons records a run's comparisons in one transaction, which
// holds SQLite's write lock from its start, so that fetches, loads and
// removals of the source wait for it, or it for them.
func (s *Store) RecordComparisons(ctx context.Context, run store.ComparisonRun) error {
	run, err := run.Prepare()
	if err != nil {
		return fmt.Errorf("record comparisons: %w", err)
	}
	at := run.ComparedAt.Format(timeLayout)
	err = s.inTx(ctx, nil, func(q *sqlitedb.Queries) error {
		status, err := loadStatus(ctx, q, run.Source)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: it has no snapshot", store.ErrSnapshotChanged)
		} else if err != nil {
			return err
		}
		if err := run.CheckLoad(status); err != nil {
			return err
		}
		if run.Whole {
			if err := q.DeleteComparisons(ctx, run.Source); err != nil {
				return err
			}
		} else {
			for _, c := range run.Comparisons {
				if err := q.DeleteComparison(ctx, sqlitedb.DeleteComparisonParams{Source: run.Source, DefinitionID: c.DefinitionID}); err != nil {
					return err
				}
			}
		}
		for _, c := range run.Comparisons {
			if err := q.InsertComparison(ctx, sqlitedb.InsertComparisonParams{
				Source:        run.Source,
				DefinitionID:  c.DefinitionID,
				SourceVersion: run.Loaded.Version,
				ComparedAt:    at,
				NetboxUrl:     run.NetBoxURL,
				NetboxVersion: run.NetBoxVersion,
				Outcome:       c.Outcome,
				NetboxID:      sql.NullInt64{Int64: c.NetBoxID, Valid: c.NetBoxID != 0},
				Differences:   int64(c.Differences),
				Result:        string(c.Result),
			}); err != nil {
				return fmt.Errorf("the comparison of definition #%d: %w", c.DefinitionID, err)
			}
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
		return nil, fmt.Errorf("list the comparisons of source %s: %w", source, err)
	}
	out := make([]store.RecordedComparison, 0, len(rows))
	for _, r := range rows {
		at, err := parseTime(r.ComparedAt)
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
		})
	}
	return out, nil
}
