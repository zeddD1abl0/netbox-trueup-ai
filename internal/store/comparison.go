package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ErrSnapshotChanged means a source's recorded snapshot isn't the version a
// comparison or a load read: the source was fetched again meanwhile, was
// removed, or was never fetched into this database; or that the definitions
// a comparison read were loaded again meanwhile. Nothing is recorded.
var ErrSnapshotChanged = errors.New("the source's recorded snapshot isn't the version read")

// Outcomes are the outcomes a comparison can record: compare's
// (compare.Outcome), which the database checks too.
var Outcomes = []string{"candidate", "absent", "conflict"}

// Comparison is the latest comparison of one definition with NetBox.
type Comparison struct {
	// DefinitionID is the definition's internal ID (Definition.ID).
	DefinitionID int64
	// Outcome is the comparison's outcome, one of Outcomes.
	Outcome string
	// NetBoxID is the NetBox object the definition matches, or 0.
	NetBoxID int64
	// Differences counts the fields that differ.
	Differences int
	// Result is the whole comparison, as JSON: what `compare --output json`
	// gives for the definition.
	Result []byte
}

// ComparisonRun is the comparisons of one source's definitions, made at once
// against one NetBox, to record.
type ComparisonRun struct {
	Source string
	// Loaded and LoadedAt are the load whose definitions were compared, as
	// LoadStatus gives them: the version of the source compared, which must
	// be that of its recorded snapshot, read by a reader with some
	// settings, and when the load started. It must be the source's latest
	// load that succeeded, so that each comparison is of the definition
	// recorded.
	Loaded   LoadKey
	LoadedAt time.Time
	// ComparedAt is when the comparison was made.
	ComparedAt time.Time
	// NetBoxURL and NetBoxVersion are the NetBox compared with, and its
	// release.
	NetBoxURL, NetBoxVersion string
	// Whole says that the run looked at every definition of the load, and
	// compared those it has comparisons of, leaving the others out, as
	// compare's default scope leaves out those NetBox hasn't got: every
	// other comparison of the source is removed. Otherwise, as for a
	// selection, the source's other comparisons are kept.
	Whole bool
	// Comparisons are the comparisons made, which replace those recorded
	// before.
	Comparisons []Comparison
}

// RecordedComparison is a comparison as the database holds it, with its
// definition's key and the run that made it.
type RecordedComparison struct {
	Comparison
	DefinitionKey
	Source, SourceVersion    string
	ComparedAt               time.Time
	NetBoxURL, NetBoxVersion string
}

// Prepare checks a run and returns it as an engine records it: its times in
// UTC to the microsecond, as PostgreSQL keeps one. Every engine calls it
// first, so that all of them store the same. Text that a source or NetBox
// supplies reaches here as JSON, which holds neither NUL bytes nor invalid
// UTF-8.
func (r ComparisonRun) Prepare() (ComparisonRun, error) {
	if r.Source == "" || !isText(r.Source) {
		return ComparisonRun{}, fmt.Errorf("a comparison must name its source as text, not %q", r.Source)
	}
	for _, field := range []struct{ name, value string }{
		{"version", r.Loaded.Version},
		{"reader", r.Loaded.Reader},
		{"settings", r.Loaded.Settings},
		{"NetBox URL", r.NetBoxURL},
		{"NetBox version", r.NetBoxVersion},
	} {
		if field.value == "" || !isText(field.value) {
			return ComparisonRun{}, fmt.Errorf("source %s: a comparison's %s must be text, not %q", r.Source, field.name, field.value)
		}
	}
	switch {
	case r.ComparedAt.IsZero():
		return ComparisonRun{}, fmt.Errorf("source %s: a comparison must say when it was made", r.Source)
	case r.LoadedAt.IsZero():
		return ComparisonRun{}, fmt.Errorf("source %s: a comparison must say when the definitions compared were loaded", r.Source)
	}
	r.ComparedAt = r.ComparedAt.UTC().Truncate(time.Microsecond)
	r.LoadedAt = r.LoadedAt.UTC().Truncate(time.Microsecond)
	compared := make(map[int64]bool, len(r.Comparisons))
	for _, c := range r.Comparisons {
		switch {
		case c.DefinitionID <= 0:
			return ComparisonRun{}, fmt.Errorf("source %s: a comparison of definition #%d, which isn't an ID", r.Source, c.DefinitionID)
		case compared[c.DefinitionID]:
			return ComparisonRun{}, fmt.Errorf("source %s: two comparisons of definition #%d", r.Source, c.DefinitionID)
		case !slices.Contains(Outcomes, c.Outcome):
			return ComparisonRun{}, fmt.Errorf("source %s: the comparison of definition #%d has the outcome %q", r.Source, c.DefinitionID, c.Outcome)
		case c.NetBoxID < 0 || c.Differences < 0:
			return ComparisonRun{}, fmt.Errorf("source %s: the comparison of definition #%d has a negative NetBox ID or count", r.Source, c.DefinitionID)
		case !json.Valid(c.Result) || !isText(string(c.Result)):
			return ComparisonRun{}, fmt.Errorf("source %s: the comparison of definition #%d isn't JSON text", r.Source, c.DefinitionID)
		}
		compared[c.DefinitionID] = true
	}
	return r, nil
}

// IDs returns the IDs of the definitions compared, in the run's order.
func (r ComparisonRun) IDs() []int64 {
	ids := make([]int64, len(r.Comparisons))
	for i, c := range r.Comparisons {
		ids[i] = c.DefinitionID
	}
	return ids
}

// CheckLoad returns an error wrapping ErrSnapshotChanged if a source's
// recorded snapshot isn't of the version the run compared, or its latest
// load that succeeded isn't the one whose definitions it compared: the
// source was fetched, or loaded again, such as by another release of
// TrueUp, meanwhile. Engines call it once they hold the source's lock.
func (r ComparisonRun) CheckLoad(status LoadStatus) error {
	switch {
	case status.SnapshotVersion != r.Loaded.Version:
		return fmt.Errorf("%w: the snapshot is of version %q, not %q", ErrSnapshotChanged, status.SnapshotVersion, r.Loaded.Version)
	case status.Loaded != r.Loaded || !status.LoadedAt.Equal(r.LoadedAt):
		return fmt.Errorf("%w: the definitions compared were loaded again meanwhile", ErrSnapshotChanged)
	}
	return nil
}
