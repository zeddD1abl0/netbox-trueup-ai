package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/audit"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/sqlite/sqlitedb"
)

// sourceState reads what a source's audit events compare, or returns nil
// if the database holds no record of the source. In a write transaction it
// stays so until the transaction ends, since SQLite runs one at a time.
func sourceState(ctx context.Context, q *sqlitedb.Queries, name string) (*store.SourceState, error) {
	cfg, err := q.GetSourceConfig(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read its record: %w", err)
	}
	state := &store.SourceState{Config: store.SourceConfig{Name: name, Kind: cfg.Kind, URL: cfg.Url, Ref: cfg.Ref}}
	snap, err := q.GetSnapshot(ctx, name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("read its snapshot: %w", err)
	default:
		state.Version = snap.Version
	}
	return state, nil
}

// insertEvents records events, as PrepareEvent returns them, at one time.
func insertEvents(ctx context.Context, q *sqlitedb.Queries, events []audit.Event) error {
	at := time.Now().UTC().Format(timeLayout)
	for _, e := range events {
		e, err := store.PrepareEvent(e)
		if err != nil {
			return err
		}
		if err := q.InsertAuditEvent(ctx, sqlitedb.InsertAuditEventParams{
			OccurredAt:    at,
			ActorKind:     string(e.Actor.Kind),
			Actor:         e.Actor.Name,
			ActorID:       e.Actor.ID,
			Host:          e.Actor.Host,
			SudoUser:      nullString(e.Actor.SudoUser),
			Through:       string(e.Through),
			ClientAddress: nullString(e.ClientAddress),
			Action:        string(e.Action),
			Result:        string(e.Result),
			Completes:     nullInt(e.Completes),
			Error:         nullString(e.Error),
			Source:        nullString(e.Source),
			ObjectType:    nullString(e.ObjectType),
			Object:        nullString(e.Object),
			NetboxUrl:     nullString(e.NetBoxURL),
			NetboxBranch:  nullString(e.NetBoxBranch),
			NetboxID:      nullInt(e.NetBoxID),
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
	if err := s.inTx(ctx, nil, func(q *sqlitedb.Queries) error {
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
	params := sqlitedb.ListAuditEventsParams{Max: -1}
	if f.Limit > 0 {
		params.Max = int64(f.Limit) + 1
	}
	if !f.Since.IsZero() {
		params.Since = f.Since.Format(timeLayout)
	}
	if !f.Until.IsZero() {
		params.Until = f.Until.Format(timeLayout)
	}
	if f.Actor != "" {
		params.Actor = f.Actor
	}
	if f.Source != "" {
		params.Source = f.Source
	}
	if len(f.Actions) > 0 {
		b, err := json.Marshal(f.Actions)
		if err != nil {
			return audit.Page{}, fmt.Errorf("list audit events: %w", err)
		}
		params.Actions = string(b)
	}
	rows, err := s.q.ListAuditEvents(ctx, params)
	if err != nil {
		return audit.Page{}, fmt.Errorf("list audit events: %w", err)
	}
	var page audit.Page
	if f.Limit > 0 && len(rows) > f.Limit {
		page.More, rows = true, rows[:f.Limit]
	}
	page.Events = make([]audit.Recorded, len(rows))
	for i, r := range rows {
		at, err := parseTime(r.OccurredAt)
		if err != nil {
			return audit.Page{}, fmt.Errorf("list audit events: event %d: %w", r.ID, err)
		}
		page.Events[i] = audit.Recorded{ID: r.ID, OccurredAt: at, Event: audit.Event{
			By: audit.By{
				Actor: audit.Actor{
					Kind: audit.ActorKind(r.ActorKind), Name: r.Actor, ID: r.ActorID, Host: r.Host, SudoUser: r.SudoUser.String,
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

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func nullInt(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: n != 0} }
