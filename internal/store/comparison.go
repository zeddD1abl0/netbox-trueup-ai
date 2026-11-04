package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrSnapshotChanged means a source's recorded snapshot isn't the version a
// comparison read: the source was fetched again while it was compared, was
// removed, or was never fetched into this database. Nothing is recorded.
var ErrSnapshotChanged = errors.New("the source's recorded snapshot isn't the version compared")

// ComparisonKey says which definition a comparison is of.
type ComparisonKey struct {
	// ObjectType is the definition's type, such as dcim.devicetype.
	ObjectType string
	// Path is the file that defines the object, or "" for an object the
	// format derives, such as a manufacturer that devicetype-library only
	// names.
	Path string
	// Identity is the definition's identity, as JSON: what tells a derived
	// object from the others of its type.
	Identity string
}

// Comparison is the latest comparison of one definition with NetBox.
type Comparison struct {
	ComparisonKey
	// Outcome is the comparison's outcome, such as candidate.
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
	// SourceVersion is the version of the source compared, which must be
	// that of its recorded snapshot.
	SourceVersion string
	// ComparedAt is when the comparison was made.
	ComparedAt time.Time
	// NetBoxURL and NetBoxVersion are the NetBox compared with, and its
	// release.
	NetBoxURL, NetBoxVersion string
	// Defined lists every definition the source holds at SourceVersion,
	// compared or not. Comparisons of definitions it doesn't list are
	// removed, but for those in KeptPaths, and those it lists but
	// Comparisons doesn't are kept.
	Defined []ComparisonKey
	// KeptPaths are files the source still holds but couldn't read this
	// time, such as files that are invalid: they define nothing, so the
	// comparisons recorded of definitions in them are kept as they were.
	KeptPaths []string
	// Comparisons are the comparisons made, which replace those recorded
	// before.
	Comparisons []Comparison
}

// RecordedComparison is a comparison as the database holds it, with the run
// that made it.
type RecordedComparison struct {
	Comparison
	Source, SourceVersion    string
	ComparedAt               time.Time
	NetBoxURL, NetBoxVersion string
}

// Prepare checks a run and returns it as an engine records it: its time in
// UTC to the microsecond, as PostgreSQL keeps one. Every engine calls it
// first, so that all of them store the same. Text that a source or NetBox
// supplies reaches here as JSON, which holds neither NUL bytes nor invalid
// UTF-8.
func (r ComparisonRun) Prepare() (ComparisonRun, error) {
	if r.Source == "" || !isText(r.Source) {
		return ComparisonRun{}, fmt.Errorf("a comparison must name its source as text, not %q", r.Source)
	}
	for _, field := range []struct{ name, value string }{
		{"version", r.SourceVersion}, {"NetBox URL", r.NetBoxURL}, {"NetBox version", r.NetBoxVersion},
	} {
		if field.value == "" || !isText(field.value) {
			return ComparisonRun{}, fmt.Errorf("source %s: a comparison's %s must be text, not %q", r.Source, field.name, field.value)
		}
	}
	if r.ComparedAt.IsZero() {
		return ComparisonRun{}, fmt.Errorf("source %s: a comparison must say when it was made", r.Source)
	}
	r.ComparedAt = r.ComparedAt.UTC().Truncate(time.Microsecond)
	keptPaths := make(map[string]bool, len(r.KeptPaths))
	for _, p := range r.KeptPaths {
		if p == "" || !isText(p) {
			return ComparisonRun{}, fmt.Errorf("source %s: a kept path %q isn't a path", r.Source, p)
		}
		keptPaths[p] = true
	}
	defined := make(map[ComparisonKey]bool, len(r.Defined))
	for _, k := range r.Defined {
		if err := k.check(); err != nil {
			return ComparisonRun{}, fmt.Errorf("source %s: %w", r.Source, err)
		}
		defined[k] = true
	}
	compared := make(map[ComparisonKey]bool, len(r.Comparisons))
	for _, c := range r.Comparisons {
		switch {
		case !defined[c.ComparisonKey]:
			return ComparisonRun{}, fmt.Errorf("source %s: a comparison of %s, which Defined doesn't list", r.Source, c.ComparisonKey)
		case compared[c.ComparisonKey]:
			return ComparisonRun{}, fmt.Errorf("source %s: two comparisons of %s", r.Source, c.ComparisonKey)
		case keptPaths[c.Path]:
			return ComparisonRun{}, fmt.Errorf("source %s: a comparison of %s, whose file it keeps as it couldn't be read", r.Source, c.ComparisonKey)
		case c.Outcome == "" || !isText(c.Outcome):
			return ComparisonRun{}, fmt.Errorf("source %s: the comparison of %s has no outcome", r.Source, c.ComparisonKey)
		case c.NetBoxID < 0 || c.Differences < 0:
			return ComparisonRun{}, fmt.Errorf("source %s: the comparison of %s has a negative NetBox ID or count", r.Source, c.ComparisonKey)
		case !json.Valid(c.Result) || !isText(string(c.Result)):
			return ComparisonRun{}, fmt.Errorf("source %s: the comparison of %s isn't JSON text", r.Source, c.ComparisonKey)
		}
		compared[c.ComparisonKey] = true
	}
	return r, nil
}

func (k ComparisonKey) check() error {
	switch {
	case k.ObjectType == "" || !isText(k.ObjectType):
		return fmt.Errorf("a definition's object type %q isn't text", k.ObjectType)
	case !isText(k.Path):
		return fmt.Errorf("a definition's path %q isn't text", k.Path)
	case !json.Valid([]byte(k.Identity)) || !isText(k.Identity):
		return fmt.Errorf("a definition's identity %q isn't JSON text", k.Identity)
	}
	return nil
}

// String names the definition, for an error.
func (k ComparisonKey) String() string {
	if k.Path != "" {
		return fmt.Sprintf("%s %q", k.ObjectType, k.Path)
	}
	return fmt.Sprintf("%s %s", k.ObjectType, k.Identity)
}

// Kept returns the keys of the definitions whose recorded comparisons a run
// keeps as they are: those defined but not compared. Every other comparison
// of the source is replaced or removed, but for those in KeptPaths.
func (r ComparisonRun) Kept() map[ComparisonKey]bool {
	kept := make(map[ComparisonKey]bool, len(r.Defined))
	for _, k := range r.Defined {
		kept[k] = true
	}
	for _, c := range r.Comparisons {
		delete(kept, c.ComparisonKey)
	}
	return kept
}
