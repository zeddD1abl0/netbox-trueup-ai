// Package store keeps TrueUp's state in a database: SQLite on a single node,
// or PostgreSQL for high availability
// (docs/adr/0011-store-state-with-sqlc-and-goose.md).
//
// Store is what the rest of TrueUp uses. Each engine implements it in a
// package below this one, with its own goose migrations and sqlc queries,
// and package engine opens the one that database.url names, so that nothing
// outside the store depends on which engine is in use. Package storetest
// holds the tests that every engine must pass.
//
// The database is inside TrueUp's trust boundary (B6 in
// docs/architecture.md), but some of what it holds comes from outside it,
// such as the paths and errors of a fetch. Escape them before showing them.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Store is TrueUp's database. It is safe for concurrent use, and several
// processes can use one database at once.
type Store interface {
	// RecordFetch records a fetch of a source as its latest, all of it or
	// nothing. A fetch that succeeded replaces the source's snapshot and
	// its file list. One that failed records only the attempt and its
	// error, and leaves the snapshot as it was, as the source's cache does.
	// Fetches of a source must be recorded in the order they happened, as
	// they are when each holds the source's fetch lock (source.LockCache)
	// until it is recorded.
	RecordFetch(ctx context.Context, f Fetch) error
	// Sources returns every recorded source, sorted by name, each with its
	// latest fetch and its snapshot without the file list.
	Sources(ctx context.Context) ([]Source, error)
	// Snapshot returns a source's snapshot with its file list. It returns
	// an error wrapping ErrNotFound if the source has none.
	Snapshot(ctx context.Context, source string) (Snapshot, error)
	// RemoveSources removes what is recorded about every source not named
	// in keep, snapshots included, and returns the names it removed,
	// sorted.
	RemoveSources(ctx context.Context, keep []string) ([]string, error)
	// Close closes the database.
	Close() error
}

// ErrNotFound means the database holds nothing of what was asked for.
var ErrNotFound = errors.New("not recorded")

// SourceConfig is a source as configured.
type SourceConfig struct {
	Name string
	Kind string
	URL  string
	// Ref is the branch or tag to follow, or "" for the default branch.
	Ref string
}

// Fetch is one fetch of a source, to record.
type Fetch struct {
	// Source is the source as configured when it was fetched.
	Source SourceConfig
	// FetchedAt is when the fetch started. It orders the fetches of a
	// source.
	FetchedAt time.Time
	// Error says why the fetch failed, or is "" if it succeeded.
	Error string
	// Version, Ref, Files and Skipped are the snapshot a successful fetch
	// took: the version, such as a Git commit's hash, what it was resolved
	// from, such as the branch master, the files, and the entries left
	// out. A failed fetch has none of them.
	Version string
	Ref     string
	Files   []File
	Skipped []Skipped
}

// File is a file in a snapshot.
type File struct {
	// Path is the file's path from the top of the source, its segments
	// separated by slashes.
	Path string
	Size int64
	// Digest is the SHA-256 of the file's content, as "sha256:" and 64
	// lowercase hexadecimal digits.
	Digest string
}

// Skipped is an entry that a snapshot's patterns choose but the snapshot
// leaves out.
type Skipped struct {
	// Path is the entry's path, as far as it could be read. It can hold
	// any bytes, invalid UTF-8 included, so escape it before showing it.
	Path string
	// Reason says why it was left out, such as "too_large".
	Reason string
}

// Source is what is recorded about a source.
type Source struct {
	// SourceConfig is the source as its latest fetch found it configured.
	SourceConfig
	// FetchedAt is when the latest fetch started, and Error why it failed,
	// or "" if it succeeded.
	FetchedAt time.Time
	Error     string
	// Snapshot is the latest snapshot, without its Files and Skipped, or
	// nil if no fetch of the source has succeeded. Store.Snapshot returns
	// it with them.
	Snapshot *Snapshot
}

// Snapshot is the latest snapshot of a source.
type Snapshot struct {
	Source string
	// URL is the URL fetched.
	URL     string
	Version string
	Ref     string
	// FetchedAt is when the fetch that took the snapshot started.
	FetchedAt time.Time
	// Files are sorted by path, and Skipped in the order of the fetch that
	// took the snapshot.
	Files   []File
	Skipped []Skipped
}

// Prepare checks a fetch and returns it as an engine records it: its time in
// UTC to the microsecond, as precise as PostgreSQL keeps one, and its error,
// version and resolved ref with invalid UTF-8 and NUL bytes replaced, since
// they are text that a source or a server can supply and PostgreSQL's text
// can't hold either. Every engine calls it first, so that all of them store
// the same.
func (f Fetch) Prepare() (Fetch, error) {
	if f.Source.Name == "" {
		return Fetch{}, errors.New("a fetch must name its source")
	}
	for _, field := range []struct{ name, value string }{
		{"name", f.Source.Name}, {"kind", f.Source.Kind}, {"url", f.Source.URL}, {"ref", f.Source.Ref},
	} {
		if !isText(field.value) {
			return Fetch{}, fmt.Errorf("source %s: its %s isn't UTF-8 text", printable(f.Source.Name), field.name)
		}
	}
	if f.FetchedAt.IsZero() {
		return Fetch{}, fmt.Errorf("source %s: a fetch must say when it started", f.Source.Name)
	}
	f.FetchedAt = f.FetchedAt.UTC().Truncate(time.Microsecond)
	f.Error, f.Version, f.Ref = cleanText(f.Error), cleanText(f.Version), cleanText(f.Ref)

	if f.Error != "" {
		if f.Version != "" || f.Ref != "" || len(f.Files) > 0 || len(f.Skipped) > 0 {
			return Fetch{}, fmt.Errorf("source %s: a failed fetch has no snapshot", f.Source.Name)
		}
		return f, nil
	}
	if f.Version == "" {
		return Fetch{}, fmt.Errorf("source %s: a successful fetch must give its version", f.Source.Name)
	}
	for _, file := range f.Files {
		switch {
		case file.Path == "" || !isText(file.Path):
			return Fetch{}, fmt.Errorf("source %s: a file's path %q isn't a path", f.Source.Name, file.Path)
		case file.Size < 0:
			return Fetch{}, fmt.Errorf("source %s: file %q has a negative size", f.Source.Name, file.Path)
		case !validDigest(file.Digest):
			return Fetch{}, fmt.Errorf("source %s: file %q has no SHA-256 digest", f.Source.Name, file.Path)
		}
	}
	skipped := make([]Skipped, len(f.Skipped))
	for i, s := range f.Skipped {
		skipped[i] = Skipped{Path: s.Path, Reason: cleanText(s.Reason)}
	}
	f.Skipped = skipped
	return f, nil
}

// isText reports whether s is valid UTF-8 without NUL bytes, which both
// engines can store as text.
func isText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// cleanText replaces invalid UTF-8 and NUL bytes in s with U+FFFD.
func cleanText(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "�")
}

// printable quotes s if it isn't plain text, for an error message.
func printable(s string) string {
	if isText(s) {
		return s
	}
	return fmt.Sprintf("%q", s)
}

func validDigest(d string) bool {
	hex, ok := strings.CutPrefix(d, "sha256:")
	if !ok || len(hex) != 64 {
		return false
	}
	return !strings.ContainsFunc(hex, func(r rune) bool { return (r < '0' || r > '9') && (r < 'a' || r > 'f') })
}

// SchemaTooNewError means the database's schema is newer than this release
// of TrueUp knows, so it may have tables or rules this release would break.
type SchemaTooNewError struct {
	// Version is the database's schema version, and Known the newest this
	// release knows.
	Version, Known int64
}

func (e *SchemaTooNewError) Error() string {
	return fmt.Sprintf("the database's schema is at version %d, newer than this release of TrueUp knows (%d): use a newer release", e.Version, e.Known)
}

// Migrated says what opening a database did to its schema.
type Migrated struct {
	// Engine is the database engine: "sqlite" or "postgres".
	Engine string
	// Location is where the database is: a SQLite database's file, or a
	// PostgreSQL database's URL with its hosts, ports and name alone, never
	// its user name or password.
	Location string
	// VersionBefore and Version are the schema's version before and after
	// opening: 0 before for a new database.
	VersionBefore, Version int64
	// Applied are the migrations applied, in order.
	Applied []Migration
}

// Migration is a schema migration.
type Migration struct {
	Version int64
	// Name is the migration's file name, such as "00001_sources.sql".
	Name string
}
