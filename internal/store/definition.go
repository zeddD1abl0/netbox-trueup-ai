package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// DefinitionKey says which of a source's definitions a record is of: the
// definition itself, or its comparison.
type DefinitionKey struct {
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

func (k DefinitionKey) check() error {
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
func (k DefinitionKey) String() string {
	if k.Path != "" {
		return fmt.Sprintf("%s %q", k.ObjectType, k.Path)
	}
	return fmt.Sprintf("%s %s", k.ObjectType, k.Identity)
}

// The states of a source's latest load (LoadStatus.State).
const (
	// LoadReady means the latest load succeeded: the definitions recorded
	// are those it read.
	LoadReady = "ready"
	// LoadFailed means the latest load failed, and left the definitions
	// recorded before it.
	LoadFailed = "failed"
)

// LoadKey says what a load read: the version of a source's snapshot, by a
// format and its version (format.Reader), such as devicetype-library/1,
// with the source's settings that chose the files read, such as a digest
// of them.
type LoadKey struct {
	Version, Reader, Settings string
}

// Load is a load of a source's snapshot into the database: what its format
// read from the snapshot's files, to record
// (docs/adr/0029-keep-a-lookup-table-of-definitions.md).
type Load struct {
	Source string
	// Version is the version of the snapshot read, which must be that of
	// the source's recorded snapshot.
	Version string
	// Reader names the format that read it and its version
	// (format.Reader), such as devicetype-library/1, and Settings the
	// source's settings that chose the files read, such as a digest of
	// them: what changes when they do.
	Reader, Settings string
	// StartedAt is when the load started.
	StartedAt time.Time
	// Error says why the load failed, or is "" if it succeeded. A failed
	// load has no counts, definitions or problems, and leaves those
	// recorded before it.
	Error string
	// Files is how many files the format took as definitions, and
	// InvalidFiles how many of them it skipped with an error.
	Files, InvalidFiles int
	// Definitions are what the format read, in its order, which replace
	// those recorded before.
	Definitions []Definition
	// Problems are what is wrong with the files, in the format's order,
	// which replace those recorded before.
	Problems []Problem
}

// Definition is one of a source's definitions, as the database keeps it.
type Definition struct {
	DefinitionKey
	// ID is the definition's internal ID, which reads give and recording
	// ignores. It is never reused, and a load keeps it for as long as the
	// source defines the same object in the same file.
	ID int64
	// Manufacturer, Model, Slug and PartNumber are what the definition is
	// found and shown by, or "" where it has none: a device type's
	// manufacturer's name, model, slug and part number, or a manufacturer's
	// name and slug.
	Manufacturer, Model, Slug, PartNumber string
	// Digest is the SHA-256 of the file that defines it, as a snapshot's
	// files give it, or "" for a definition the format derives.
	Digest string
	// JSON is the definition as `list --output json` gives it
	// (format.MarshalDefinition). A read of unique values leaves it nil.
	JSON []byte
	// UniqueValues is the definition cut down to what matching it with
	// NetBox reads (compare.UniqueValues), as JSON of the same form, so
	// that compare can tell which definitions NetBox has without reading
	// them whole (docs/adr/0030-compare-what-netbox-has-by-default.md). A
	// database that a release before them migrated holds "" until the
	// source is loaded again.
	UniqueValues []byte
}

// Problem is something wrong with a file a load read, or with what its
// format derived from several files.
type Problem struct {
	// Severity is error or warning.
	Severity string
	// Path is the file, or "" for a problem with what was derived from
	// several files.
	Path string
	// Line and Column are where in the file, from 1, or 0 for none.
	Line, Column int
	// Field is where in the file's structure, such as interfaces[3].type,
	// or "".
	Field  string
	Reason string
}

// LoadStatus is what is recorded of the loads of a source's snapshot. A
// load in progress isn't recorded: the process running it knows.
type LoadStatus struct {
	Source string
	// SnapshotVersion and SnapshotRef are those of the source's recorded
	// snapshot.
	SnapshotVersion, SnapshotRef string
	// Latest is what the latest load read, StartedAt when it started, and
	// Error why it failed, or "" if it succeeded. They are zero if no load
	// is recorded.
	Latest    LoadKey
	StartedAt time.Time
	Error     string
	// Loaded is what the latest load that succeeded read, whose
	// definitions are recorded, and LoadedAt when it started, or they are
	// zero if none has. Files and InvalidFiles are its counts, and
	// Definitions how many definitions it recorded.
	Loaded                           LoadKey
	LoadedAt                         time.Time
	Files, InvalidFiles, Definitions int
}

// State is the state of the latest load: LoadReady, LoadFailed, or "" if
// none is recorded.
func (s LoadStatus) State() string {
	switch {
	case s.Latest == LoadKey{}:
		return ""
	case s.Error != "":
		return LoadFailed
	}
	return LoadReady
}

// Current reports whether the definitions recorded are those that reader
// read from the source's recorded snapshot, with the settings given.
func (s LoadStatus) Current(reader, settings string) bool {
	return s.SnapshotVersion != "" && s.Loaded == LoadKey{s.SnapshotVersion, reader, settings}
}

// Failed reports whether the latest load read the source's recorded
// snapshot with reader and the settings given, as a load now would, and
// failed.
func (s LoadStatus) Failed(reader, settings string) bool {
	return s.Error != "" && s.SnapshotVersion != "" && s.Latest == LoadKey{s.SnapshotVersion, reader, settings}
}

// SourceDefinitions is a source's recorded definitions and problems, with
// the status of the loads that recorded them, as they were at one moment.
type SourceDefinitions struct {
	LoadStatus
	Definitions []Definition
	Problems    []Problem
}

// Prepare checks a load and returns it as an engine records it: its time in
// UTC to the microsecond, as PostgreSQL keeps one, and the text of its
// error and problems with invalid UTF-8 and NUL bytes replaced, since they
// can quote a source's files, which PostgreSQL's text can't always hold.
// Every engine calls it first, so that all of them store the same.
func (l Load) Prepare() (Load, error) {
	if l.Source == "" || !isText(l.Source) {
		return Load{}, fmt.Errorf("a load must name its source as text, not %q", l.Source)
	}
	for _, field := range []struct{ name, value string }{{"version", l.Version}, {"reader", l.Reader}, {"settings", l.Settings}} {
		if field.value == "" || !isText(field.value) {
			return Load{}, fmt.Errorf("source %s: a load's %s must be text, not %q", l.Source, field.name, field.value)
		}
	}
	if l.StartedAt.IsZero() {
		return Load{}, fmt.Errorf("source %s: a load must say when it started", l.Source)
	}
	l.StartedAt = l.StartedAt.UTC().Truncate(time.Microsecond)
	l.Error = cleanText(l.Error)
	if l.Error != "" {
		if l.Files != 0 || l.InvalidFiles != 0 || len(l.Definitions) > 0 || len(l.Problems) > 0 {
			return Load{}, fmt.Errorf("source %s: a failed load has no definitions", l.Source)
		}
		return l, nil
	}
	if l.Files < 0 || l.InvalidFiles < 0 || l.InvalidFiles > l.Files {
		return Load{}, fmt.Errorf("source %s: a load can't have %d invalid files of %d", l.Source, l.InvalidFiles, l.Files)
	}
	seen := make(map[DefinitionKey]bool, len(l.Definitions))
	for _, d := range l.Definitions {
		if err := d.check(); err != nil {
			return Load{}, fmt.Errorf("source %s: %w", l.Source, err)
		}
		if seen[d.DefinitionKey] {
			return Load{}, fmt.Errorf("source %s: two definitions of %s", l.Source, d.DefinitionKey)
		}
		seen[d.DefinitionKey] = true
	}
	problems := make([]Problem, len(l.Problems))
	for i, p := range l.Problems {
		switch {
		case p.Severity != "error" && p.Severity != "warning":
			return Load{}, fmt.Errorf("source %s: a problem's severity is %q, not error or warning", l.Source, p.Severity)
		case p.Line < 0 || p.Column < 0:
			return Load{}, fmt.Errorf("source %s: a problem of %q is at line %d, column %d", l.Source, p.Path, p.Line, p.Column)
		case p.Reason == "":
			return Load{}, fmt.Errorf("source %s: a problem of %q gives no reason", l.Source, p.Path)
		}
		problems[i] = Problem{
			Severity: p.Severity, Path: cleanText(p.Path), Line: p.Line, Column: p.Column,
			Field: cleanText(p.Field), Reason: cleanText(p.Reason),
		}
	}
	l.Problems = problems
	return l, nil
}

// Keys returns the keys of the definitions a load holds, which keep their
// rows; the source's other definitions are deleted.
func (l Load) Keys() map[DefinitionKey]bool {
	keys := make(map[DefinitionKey]bool, len(l.Definitions))
	for _, d := range l.Definitions {
		keys[d.DefinitionKey] = true
	}
	return keys
}

func (d Definition) check() error {
	if err := d.DefinitionKey.check(); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"manufacturer", d.Manufacturer}, {"model", d.Model}, {"slug", d.Slug}, {"part number", d.PartNumber},
	} {
		if !isText(field.value) {
			return fmt.Errorf("%s: its %s %q isn't text", d.DefinitionKey, field.name, field.value)
		}
	}
	switch {
	case d.Path == "" && d.Digest != "":
		return fmt.Errorf("%s: a definition the format derives has no file to give a digest", d.DefinitionKey)
	case d.Path != "" && !validDigest(d.Digest):
		return fmt.Errorf("%s: its file has no SHA-256 digest", d.DefinitionKey)
	case !json.Valid(d.JSON) || !isText(string(d.JSON)):
		return fmt.Errorf("%s: the definition isn't JSON text", d.DefinitionKey)
	case !json.Valid(d.UniqueValues) || !isText(string(d.UniqueValues)):
		return fmt.Errorf("%s: its unique values aren't JSON text", d.DefinitionKey)
	}
	return nil
}

// SearchText is what a search looks in: a definition's manufacturer, model
// and part number, in lower case, one per line. A search can't hold a line
// break, so it never matches across two of them.
func (d Definition) SearchText() string {
	return strings.ToLower(d.Manufacturer + "\n" + d.Model + "\n" + d.PartNumber)
}

// PrepareSearch returns a search as an engine looks for it in SearchText:
// in lower case. A search that holds a control character, such as a line
// break, is refused.
func PrepareSearch(search string) (string, error) {
	if strings.ContainsFunc(search, unicode.IsControl) {
		return "", errors.New("a search can't hold control characters, such as a line break")
	}
	if !isText(search) {
		return "", errors.New("a search must be UTF-8 text")
	}
	return strings.ToLower(search), nil
}
