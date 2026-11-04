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
// first locks the source's row, as fetches and removals of the source do,
// so that they take turns, and writes the comparisons in one COPY. It reads
// committed data, whatever the server's default, so that once it has the
// lock it sees the snapshot that a fetch it waited for recorded.
func (s *Store) RecordComparisons(ctx context.Context, run store.ComparisonRun) error {
	run, err := run.Prepare()
	if err != nil {
		return fmt.Errorf("record comparisons: %w", err)
	}
	kept := run.Kept()
	at := pgtype.Timestamptz{Time: run.ComparedAt, Valid: true}
	err = s.inTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(q *postgresdb.Queries) error {
		if _, err := q.LockSource(ctx, run.Source); errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: it was never fetched", store.ErrSnapshotChanged)
		} else if err != nil {
			return err
		}
		version, err := q.GetSnapshotVersion(ctx, run.Source)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w: it has no snapshot", store.ErrSnapshotChanged)
		case err != nil:
			return err
		case version != run.SourceVersion:
			return fmt.Errorf("%w: the snapshot is of version %q, not %q", store.ErrSnapshotChanged, version, run.SourceVersion)
		}
		switch {
		case run.Partial:
			of := postgresdb.DeleteComparisonsOfParams{Source: run.Source}
			for _, k := range run.Compared() {
				of.ObjectTypes = append(of.ObjectTypes, k.ObjectType)
				of.Paths = append(of.Paths, k.Path)
				of.Identities = append(of.Identities, k.Identity)
			}
			if err := q.DeleteComparisonsOf(ctx, of); err != nil {
				return err
			}
		case len(kept) == 0:
			if err := q.DeleteComparisons(ctx, run.Source); err != nil {
				return err
			}
		default:
			except := postgresdb.DeleteComparisonsExceptParams{Source: run.Source}
			for k := range kept {
				except.ObjectTypes = append(except.ObjectTypes, k.ObjectType)
				except.Paths = append(except.Paths, k.Path)
				except.Identities = append(except.Identities, k.Identity)
			}
			if err := q.DeleteComparisonsExcept(ctx, except); err != nil {
				return err
			}
		}
		if len(run.Comparisons) == 0 {
			return nil
		}
		rows := make([]postgresdb.InsertComparisonsParams, len(run.Comparisons))
		for i, c := range run.Comparisons {
			rows[i] = postgresdb.InsertComparisonsParams{
				Source:        run.Source,
				ObjectType:    c.ObjectType,
				Path:          c.Path,
				Identity:      c.Identity,
				SourceVersion: run.SourceVersion,
				ComparedAt:    at,
				NetboxUrl:     run.NetBoxURL,
				NetboxVersion: run.NetBoxVersion,
				Outcome:       c.Outcome,
				NetboxID:      pgtype.Int8{Int64: c.NetBoxID, Valid: c.NetBoxID != 0},
				Differences:   int64(c.Differences),
				Result:        string(c.Result),
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
