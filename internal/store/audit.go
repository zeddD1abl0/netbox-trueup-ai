package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/audit"
)

// SourceState is what a source's audit events compare: its configuration
// as its latest fetch found it, which a failed fetch records too, and its
// snapshot's version, the URL fetched and the ref resolved, each "" if it has
// none.
type SourceState struct {
	Config      SourceConfig
	Version     string
	SnapshotURL string
	SnapshotRef string
}

// sourceDetails is the details of a source added: its configuration, and
// the version its fetch found, if it succeeded.
type sourceDetails struct {
	Kind    string  `json:"kind"`
	URL     string  `json:"url"`
	Ref     string  `json:"ref"`
	Version *string `json:"version,omitempty"`
}

// configDetails is a source's configuration, before or after a change.
type configDetails struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
	Ref  string `json:"ref"`
}

// FetchEvents returns the audit events that recording f records, as by's
// actions, given the source's state before it, or nil if the database holds
// no record of the source. f is a fetch as Prepare returns it.
//
//   - A source with no record is added, with its configuration, and the
//     version its fetch found, if it succeeded.
//   - A source whose kind, URL or ref differ from its record is
//     reconfigured, with the configuration before and after. A failed fetch
//     records them too, since the record takes them.
//   - A source with a record whose fetch succeeded with another version
//     than its snapshot's, or its first, has a new version, with both and
//     the ref the version was resolved from.
//
// A fetch that changes none of these records no event.
func FetchEvents(by audit.By, before *SourceState, f Fetch) ([]audit.Event, error) {
	event := func(action audit.Action, details any) (audit.Event, error) {
		b, err := json.Marshal(details)
		if err != nil {
			return audit.Event{}, fmt.Errorf("the details of %s: %w", action, err)
		}
		return audit.Event{By: by, Action: action, Result: audit.Succeeded, Source: f.Source.Name, Details: b}, nil
	}
	succeeded := f.Error == ""
	if before == nil {
		d := sourceDetails{Kind: f.Source.Kind, URL: f.Source.URL, Ref: f.Source.Ref}
		if succeeded {
			d.Version = &f.Version
		}
		e, err := event(audit.SourceAdded, d)
		if err != nil {
			return nil, err
		}
		return []audit.Event{e}, nil
	}
	var events []audit.Event
	if was := before.Config; was.Kind != f.Source.Kind || was.URL != f.Source.URL || was.Ref != f.Source.Ref {
		e, err := event(audit.SourceReconfigured, struct {
			Before configDetails `json:"before"`
			After  configDetails `json:"after"`
		}{
			configDetails{Kind: was.Kind, URL: was.URL, Ref: was.Ref},
			configDetails{Kind: f.Source.Kind, URL: f.Source.URL, Ref: f.Source.Ref},
		})
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	if succeeded && f.Version != before.Version {
		var was *string
		if before.Version != "" {
			was = &before.Version
		}
		e, err := event(audit.SourceNewVersion, struct {
			Before *string `json:"before"`
			After  string  `json:"after"`
			Ref    string  `json:"ref"`
		}{was, f.Version, f.Ref})
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}

// snapshotDetails is the snapshot a source held when it was removed: the
// version, the URL it was fetched from, which a later failed fetch may have
// changed in the configuration, and the ref it was resolved from.
type snapshotDetails struct {
	URL     string `json:"url"`
	Ref     string `json:"ref"`
	Version string `json:"version"`
}

// RemovedEvent returns the audit event of removing the records of a source
// no longer configured, as by's action, with its configuration as its
// latest fetch found it, and its snapshot, if it had one.
func RemovedEvent(by audit.By, name string, before SourceState) (audit.Event, error) {
	d := struct {
		configDetails
		Snapshot *snapshotDetails `json:"snapshot,omitempty"`
	}{configDetails: configDetails{Kind: before.Config.Kind, URL: before.Config.URL, Ref: before.Config.Ref}}
	if before.Version != "" {
		d.Snapshot = &snapshotDetails{URL: before.SnapshotURL, Ref: before.SnapshotRef, Version: before.Version}
	}
	b, err := json.Marshal(d)
	if err != nil {
		return audit.Event{}, fmt.Errorf("the details of %s: %w", audit.SourceRemoved, err)
	}
	return audit.Event{By: by, Action: audit.SourceRemoved, Result: audit.Succeeded, Source: name, Details: b}, nil
}

// PrepareEvent checks an audit event and returns it as an engine records
// it: its text with invalid UTF-8 and NUL bytes replaced, since some of it,
// such as a host's name or an error, comes from outside TrueUp, and its
// details as compact JSON, "{}" for none. Every engine calls it first, so
// that all of them store the same. It fails where PrepareBy does; for a
// result it doesn't know; an action that isn't an object and a verb; an
// error on an event that didn't fail, or none on one that did; a negative
// completion, or a started event that completes one; a negative NetBox ID;
// or details that aren't a JSON object. What a completion completes is
// checked as it is recorded (CheckCompletion).
func PrepareEvent(e audit.Event) (audit.Event, error) {
	by, err := PrepareBy(e.By)
	if err != nil {
		return audit.Event{}, err
	}
	e.By = by
	switch {
	case !e.Action.WellFormed():
		return audit.Event{}, fmt.Errorf("an audit event's action %q isn't an object and a verb, such as source.added", e.Action)
	case !e.Result.Known():
		return audit.Event{}, fmt.Errorf("audit event %s has no known result, %q", e.Action, e.Result)
	case (e.Result == audit.Failed) != (e.Error != ""):
		return audit.Event{}, fmt.Errorf("audit event %s must say why if, and only if, it failed", e.Action)
	case e.Completes < 0 || (e.Completes > 0 && e.Result == audit.Started):
		return audit.Event{}, fmt.Errorf("audit event %s can complete only an event recorded before it, and only with a result", e.Action)
	case e.NetBoxID < 0:
		return audit.Event{}, fmt.Errorf("audit event %s has a negative NetBox ID", e.Action)
	}
	details := []byte("{}")
	if e.Details != nil {
		var b bytes.Buffer
		if err := json.Compact(&b, e.Details); err != nil || b.Len() == 0 || b.Bytes()[0] != '{' || !utf8.Valid(b.Bytes()) {
			return audit.Event{}, fmt.Errorf("audit event %s's details aren't a JSON object in UTF-8", e.Action)
		}
		details = b.Bytes()
	}
	e.Details = details
	for _, s := range []*string{&e.Error, &e.Source, &e.ObjectType, &e.Object, &e.NetBoxURL, &e.NetBoxBranch} {
		*s = cleanText(*s)
	}
	return e, nil
}

// PrepareBy checks where an action came from, as PrepareEvent does, and
// returns it with its text cleaned as PrepareEvent cleans it. Methods that
// record events check it first, so that a missing actor fails even where
// there is nothing to record.
func PrepareBy(by audit.By) (audit.By, error) {
	switch {
	case !by.Actor.Kind.Known():
		return audit.By{}, fmt.Errorf("an audit event's actor is of no known kind, %q", by.Actor.Kind)
	case by.Actor.Name == "" && by.Actor.ID == "":
		return audit.By{}, errors.New("an audit event's actor has neither a name nor an ID")
	case !by.Through.Known():
		return audit.By{}, fmt.Errorf("an audit event came through no known channel, %q", by.Through)
	}
	for _, s := range []*string{&by.Actor.Name, &by.Actor.ID, &by.Actor.Host, &by.Actor.SudoUser, &by.Actor.SudoUserID, &by.ClientAddress} {
		*s = cleanText(*s)
	}
	return by, nil
}

// CheckCompletion checks that an event that completes another, as
// PrepareEvent returns it, completes an event recorded as started, of the
// same action, given that event's action and result as recorded, or found
// false if none is. An engine calls it in the transaction that records the
// completion; a unique index lets a started event be completed once.
func CheckCompletion(e audit.Event, found bool, action audit.Action, result audit.Result) error {
	switch {
	case !found:
		return fmt.Errorf("audit event %s completes event %d, which isn't recorded", e.Action, e.Completes)
	case result != audit.Started:
		return fmt.Errorf("audit event %s completes event %d, which wasn't recorded as started", e.Action, e.Completes)
	case action != e.Action:
		return fmt.Errorf("audit event %s completes event %d, of another action, %s", e.Action, e.Completes, action)
	}
	return nil
}

// maxLimit is the most events a filter's limit names; a greater one lists
// them all, as no database holds more, and an engine can add one to it.
const maxLimit = math.MaxInt32

// PrepareFilter checks a filter of audit events and returns it as an engine
// reads it: its times in UTC, rounded up to the microsecond, since events
// are recorded to the microsecond, so that "at Since or later" and "before
// Until" choose the same events either way, and a limit beyond maxLimit as
// none. It fails for a negative limit, and an actor or source that isn't
// text.
func PrepareFilter(f audit.Filter) (audit.Filter, error) {
	if f.Limit < 0 {
		return audit.Filter{}, fmt.Errorf("a limit of %d events isn't one", f.Limit)
	}
	if f.Limit > maxLimit {
		f.Limit = 0
	}
	for _, field := range []struct{ name, value string }{{"actor", f.Actor}, {"source", f.Source}} {
		// Events hold text alone (PrepareEvent), so such a filter would
		// match nothing, and PostgreSQL refuses it.
		if !isText(field.value) {
			return audit.Filter{}, fmt.Errorf("an audit filter's %s %q isn't UTF-8 text", field.name, field.value)
		}
	}
	for _, t := range []*time.Time{&f.Since, &f.Until} {
		if t.IsZero() {
			continue
		}
		u := t.UTC()
		down := u.Truncate(time.Microsecond)
		if down.Before(u) {
			down = down.Add(time.Microsecond)
		}
		*t = down
	}
	return f, nil
}
