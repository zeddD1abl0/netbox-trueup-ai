package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/audit"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/postgres/postgresdb"
)

// sourceState reads what a source's audit events compare, locking the
// source's row until the transaction ends, so that another fetch's record
// waits for this one, and then reads what this one recorded. It returns nil
// if the database holds no record of the source.
func sourceState(ctx context.Context, q *postgresdb.Queries, name string) (*store.SourceState, error) {
	// Lock in one statement, read in the next: a statement that waited
	// for a row's lock re-reads only that row.
	cfg, err := q.GetSourceConfig(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read its record: %w", err)
	}
	state := &store.SourceState{Config: store.SourceConfig{Name: name, Kind: cfg.Kind, URL: cfg.Url, Ref: cfg.Ref}}
	snap, err := q.GetSnapshot(ctx, name)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("read its snapshot: %w", err)
	default:
		state.Version, state.SnapshotURL, state.SnapshotRef = snap.Version, snap.Url, snap.Ref
	}
	return state, nil
}

// fetchedSourceState returns what a fetch's audit events compare, as
// sourceState does. A source with no record gets one now, as the fetch
// records it, so that another transaction recording the same source's first
// fetch waits for this one rather than recording it as added too; if
// another recorded it first, this one waits for that and reads it.
func fetchedSourceState(ctx context.Context, q *postgresdb.Queries, f store.Fetch) (*store.SourceState, error) {
	// The second try finds the row another transaction recorded first; a
	// third covers a removal that took that row between the two. A record
	// that keeps changing past that is reported, not chased.
	for range 3 {
		state, err := sourceState(ctx, q, f.Source.Name)
		if err != nil || state != nil {
			return state, err
		}
		n, err := q.InsertSourceIfAbsent(ctx, postgresdb.InsertSourceIfAbsentParams{
			Name:      f.Source.Name,
			Kind:      f.Source.Kind,
			Url:       f.Source.URL,
			Ref:       f.Source.Ref,
			FetchedAt: pgtype.Timestamptz{Time: f.FetchedAt, Valid: true},
			Error:     pgtype.Text{String: f.Error, Valid: f.Error != ""},
		})
		if err != nil {
			return nil, fmt.Errorf("record it: %w", err)
		}
		if n == 1 {
			return nil, nil
		}
	}
	return nil, errors.New("its record kept changing while it was recorded; try again")
}

// insertEvents records events, as PrepareEvent returns them, at one time.
func insertEvents(ctx context.Context, q *postgresdb.Queries, events []audit.Event) error {
	at := pgtype.Timestamptz{Time: time.Now().UTC().Truncate(time.Microsecond), Valid: true}
	for _, e := range events {
		e, err := store.PrepareEvent(e)
		if err != nil {
			return err
		}
		if e.Completes > 0 {
			started, err := q.GetStartedEvent(ctx, e.Completes)
			found := !errors.Is(err, pgx.ErrNoRows)
			if err != nil && found {
				return fmt.Errorf("read audit event %d: %w", e.Completes, err)
			}
			if err := store.CheckCompletion(e, found, audit.Action(started.Action), audit.Result(started.Result)); err != nil {
				return err
			}
		}
		if err := q.InsertAuditEvent(ctx, postgresdb.InsertAuditEventParams{
			OccurredAt:    at,
			ActorKind:     string(e.Actor.Kind),
			Actor:         e.Actor.Name,
			ActorID:       e.Actor.ID,
			Host:          e.Actor.Host,
			SudoUser:      nullText(e.Actor.SudoUser),
			SudoUserID:    nullText(e.Actor.SudoUserID),
			Through:       string(e.Through),
			ClientAddress: nullText(e.ClientAddress),
			Action:        string(e.Action),
			Result:        string(e.Result),
			Completes:     nullInt8(e.Completes),
			Error:         nullText(e.Error),
			Source:        nullText(e.Source),
			ObjectType:    nullText(e.ObjectType),
			Object:        nullText(e.Object),
			NetboxUrl:     nullText(e.NetBoxURL),
			NetboxBranch:  nullText(e.NetBoxBranch),
			NetboxID:      nullInt8(e.NetBoxID),
			Details:       string(e.Details),
		}); err != nil {
			return fmt.Errorf("record audit event %s: %w", e.Action, err)
		}
	}
	return nil
}

// RecordEvents records audit events in one transaction.
func (s *Store) RecordEvents(ctx context.Context, events []audit.Event) error {
	for _, e := range events {
		if _, err := store.PrepareEvent(e); err != nil {
			return fmt.Errorf("record audit events: %w", err)
		}
	}
	if len(events) == 0 {
		return nil
	}
	if err := s.inTx(ctx, pgx.TxOptions{}, func(q *postgresdb.Queries) error {
		return insertEvents(ctx, q, events)
	}); err != nil {
		return fmt.Errorf("record audit events: %w", err)
	}
	return nil
}

// Events returns the latest audit events a filter chooses, oldest first.
func (s *Store) Events(ctx context.Context, f audit.Filter) (audit.Page, error) {
	f, err := store.PrepareFilter(f)
	if err != nil {
		return audit.Page{}, fmt.Errorf("list audit events: %w", err)
	}
	params := postgresdb.ListAuditEventsParams{
		Since:   pgtype.Timestamptz{Time: f.Since, Valid: !f.Since.IsZero()},
		Until:   pgtype.Timestamptz{Time: f.Until, Valid: !f.Until.IsZero()},
		Actor:   nullText(f.Actor),
		Source:  nullText(f.Source),
		Actions: make([]string, len(f.Actions)),
	}
	for i, a := range f.Actions {
		params.Actions[i] = string(a)
	}
	if f.Limit > 0 {
		params.Max = pgtype.Int8{Int64: int64(f.Limit) + 1, Valid: true}
	}
	rows, err := s.q.ListAuditEvents(ctx, params)
	if err != nil {
		return audit.Page{}, fmt.Errorf("list audit events: %w", withoutUser(err))
	}
	var page audit.Page
	if f.Limit > 0 && len(rows) > f.Limit {
		page.More, rows = true, rows[:f.Limit]
	}
	page.Events = make([]audit.Recorded, len(rows))
	for i, r := range rows {
		at, err := readTime(r.OccurredAt)
		if err != nil {
			return audit.Page{}, fmt.Errorf("list audit events: event %d: %w", r.ID, err)
		}
		page.Events[i] = audit.Recorded{ID: r.ID, OccurredAt: at, Event: audit.Event{
			By: audit.By{
				Actor: audit.Actor{
					Kind: audit.ActorKind(r.ActorKind), Name: r.Actor, ID: r.ActorID, Host: r.Host, SudoUser: r.SudoUser.String, SudoUserID: r.SudoUserID.String,
				},
				Through:       audit.Through(r.Through),
				ClientAddress: r.ClientAddress.String,
			},
			Action:       audit.Action(r.Action),
			Result:       audit.Result(r.Result),
			Completes:    r.Completes.Int64,
			Error:        r.Error.String,
			Source:       r.Source.String,
			ObjectType:   r.ObjectType.String,
			Object:       r.Object.String,
			NetBoxURL:    r.NetboxUrl.String,
			NetBoxBranch: r.NetboxBranch.String,
			NetBoxID:     r.NetboxID.Int64,
			Details:      json.RawMessage(r.Details),
		}}
	}
	slices.Reverse(page.Events)
	return page, nil
}

func nullText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func nullInt8(n int64) pgtype.Int8 { return pgtype.Int8{Int64: n, Valid: n != 0} }
