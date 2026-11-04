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
// holds SQLite's write lock from its start, so that fetches and removals of
// the source wait for it, or it for them.
func (s *Store) RecordComparisons(ctx context.Context, run store.ComparisonRun) error {
	run, err := run.Prepare()
	if err != nil {
		return fmt.Errorf("record comparisons: %w", err)
	}
	kept := run.Kept()
	at := run.ComparedAt.Format(timeLayout)
	err = s.inTx(ctx, nil, func(q *sqlitedb.Queries) error {
		version, err := q.GetSnapshotVersion(ctx, run.Source)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w: it has no snapshot", store.ErrSnapshotChanged)
		case err != nil:
			return err
		case version != run.SourceVersion:
			return fmt.Errorf("%w: the snapshot is of version %q, not %q", store.ErrSnapshotChanged, version, run.SourceVersion)
		}
		if len(kept) == 0 && !run.Partial {
			if err := q.DeleteComparisons(ctx, run.Source); err != nil {
				return err
			}
		} else {
			compared := map[store.ComparisonKey]bool{}
			for _, k := range run.Compared() {
				compared[k] = true
			}
			keys, err := q.ListComparisonKeys(ctx, run.Source)
			if err != nil {
				return err
			}
			for _, k := range keys {
				key := store.ComparisonKey{ObjectType: k.ObjectType, Path: k.Path, Identity: k.Identity}
				if !compared[key] && (kept[key] || run.Partial) {
					continue
				}
				if err := q.DeleteComparison(ctx, sqlitedb.DeleteComparisonParams{
					Source: run.Source, ObjectType: k.ObjectType, Path: k.Path, Identity: k.Identity,
				}); err != nil {
					return err
				}
			}
		}
		for _, c := range run.Comparisons {
			if err := q.InsertComparison(ctx, sqlitedb.InsertComparisonParams{
				Source:        run.Source,
				ObjectType:    c.ObjectType,
				Path:          c.Path,
				Identity:      c.Identity,
				SourceVersion: run.SourceVersion,
				ComparedAt:    at,
				NetboxUrl:     run.NetBoxURL,
				NetboxVersion: run.NetBoxVersion,
				Outcome:       c.Outcome,
				NetboxID:      sql.NullInt64{Int64: c.NetBoxID, Valid: c.NetBoxID != 0},
				Differences:   int64(c.Differences),
				Result:        string(c.Result),
			}); err != nil {
				return fmt.Errorf("the comparison of %s: %w", c.ComparisonKey, err)
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
				ComparisonKey: store.ComparisonKey{ObjectType: r.ObjectType, Path: r.Path, Identity: r.Identity},
				Outcome:       r.Outcome,
				NetBoxID:      r.NetboxID.Int64,
				Differences:   int(r.Differences),
				Result:        []byte(r.Result),
			},
			Source:        source,
			SourceVersion: r.SourceVersion,
			ComparedAt:    at,
			NetBoxURL:     r.NetboxUrl,
			NetBoxVersion: r.NetboxVersion,
		})
	}
	return out, nil
}
