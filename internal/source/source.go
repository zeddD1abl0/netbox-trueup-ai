// Package source reads the files of sources, the places definitions come
// from (docs/adr/0009-source-kinds-and-format-adapters.md).
//
// Each kind of source, such as Git, fetches content its own way, in a package
// below this one, and offers it as Content. This package turns content into a
// Snapshot by the same rules for every kind:
//
//   - every name is checked, and paths are confined to the source: no
//     absolute paths, no . or .. segments, no separators or control
//     characters inside a name;
//   - include and exclude patterns choose the files;
//   - symbolic links, Git submodules and anything else that isn't a plain
//     file are skipped;
//   - limits on file size and file count apply before any file is parsed.
//
// Nothing outside this package and the kinds knows which kind made a
// snapshot. Everything a source holds, file names included, is untrusted
// input (boundary B1 in docs/architecture.md).
package source

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/glob"
)

// Spec says what to fetch from one source, and which of its files to read.
type Spec struct {
	// Name is the source's name, which the configuration keeps safe to use
	// as a directory name.
	Name string
	// Kind is how the source is fetched, such as config.SourceKindGit.
	Kind string
	// URL is where the source is, such as a Git repository's URL.
	URL string
	// Ref is the Git branch or tag to follow, or "" for the default branch.
	Ref string
	// Include chooses the files to read; none means every file. Exclude
	// leaves files out, even ones Include chooses.
	Include, Exclude []glob.Pattern
	Limits           config.SourceLimits
}

// NewSpec returns the Spec for a source from the configuration.
func NewSpec(s config.Source) (Spec, error) {
	spec := Spec{Name: s.Name, Kind: s.Kind, URL: s.URL, Ref: s.Ref, Limits: s.Limits}
	var err error
	if spec.Include, err = compile(s.Include); err != nil {
		return Spec{}, fmt.Errorf("source %s: include: %w", s.Name, err)
	}
	if spec.Exclude, err = compile(s.Exclude); err != nil {
		return Spec{}, fmt.Errorf("source %s: exclude: %w", s.Name, err)
	}
	if s.Limits.MaxFileSize <= 0 || s.Limits.MaxFiles <= 0 || s.Limits.FetchTimeout <= 0 || s.Limits.MaxFetchSize <= 0 {
		return Spec{}, fmt.Errorf("source %s has no limits: settings must be loaded before sources are read", s.Name)
	}
	return spec, nil
}

func compile(patterns []string) ([]glob.Pattern, error) {
	out := make([]glob.Pattern, 0, len(patterns))
	for _, text := range patterns {
		p, err := glob.Compile(text)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Kind fetches one kind of source.
type Kind interface {
	// Fetch fetches the source's latest version into the cache and returns
	// its content. The content replaces the cached version only when it is
	// committed, once a snapshot of it has passed every check, so that a
	// version that fails them leaves the previous one in place. Closing
	// content that wasn't committed discards it.
	Fetch(ctx context.Context, spec Spec) (Content, error)
	// Open returns the cached version, as the last committed fetch left
	// it, without the network. It returns an error wrapping ErrNotFetched
	// if there is none, or if the cache no longer fits the spec.
	Open(ctx context.Context, spec Spec) (Content, error)
}

// ErrNotFetched means a source has no content in the cache: it hasn't been
// fetched, or its cache was removed.
var ErrNotFetched = errors.New("the source hasn't been fetched")

// Content is one version of a source's content, as its kind offers it. A
// kind reports what it stores without judging it: this package checks every
// name and entry.
type Content interface {
	// Version identifies the content, such as a Git commit's hash.
	Version() string
	// Ref names what the version was resolved from, such as the Git branch
	// master, or is "" for kinds without one.
	Ref() string
	// Root returns the ID of the top directory.
	Root() string
	// ReadDir returns the entries of the directory with the given ID.
	ReadDir(ctx context.Context, id string) ([]Entry, error)
	// Open returns the content of the file with the given ID.
	Open(ctx context.Context, id string) (io.ReadCloser, error)
	// Commit makes the content the source's cached version, replacing the
	// previous one. Content from Open is the cached version already, and
	// committing it again does nothing.
	Commit(ctx context.Context) error
	// Close releases what the content holds open, and discards content
	// that Fetch returned and that wasn't committed.
	Close() error
}

// Entry is an entry in a directory, as a kind stores it.
type Entry struct {
	// Name is the entry's name within its directory, unchecked.
	Name string
	Type EntryType
	// Size is a file's size in bytes, or -1 when the kind didn't fetch the
	// file because it is larger than the source's MaxFileSize.
	Size int64
	// ID lets the kind find the entry's content, such as a Git object's
	// hash.
	ID string
}

// EntryType says what an entry is.
type EntryType int

// Entry types.
const (
	EntryFile EntryType = iota
	EntryDir
	EntrySymlink
	EntrySubmodule // a Git submodule, or gitlink
	EntryOther     // anything else, such as a device
)

// File is a file in a snapshot.
type File struct {
	// Path is the file's path from the top of the source, its segments
	// separated by slashes.
	Path string
	// Size is the file's size in bytes.
	Size int64
	// Digest is the SHA-256 of the file's content, as "sha256:" and 64
	// lowercase hexadecimal digits.
	Digest string
}

// Skipped is an entry that patterns choose but a snapshot leaves out.
type Skipped struct {
	// Path is the entry's path. For an unsafe path, it is the path as far
	// as it could be read, and may hold any characters: escape it before
	// showing it.
	Path   string
	Reason Reason
}

// Reason says why an entry was skipped. Its values are part of the CLI's
// JSON output.
type Reason string

// Reasons for skipping an entry.
const (
	// TooLarge is a file larger than the source's MaxFileSize.
	TooLarge Reason = "too_large"
	// Symlink is a symbolic link, which is never followed.
	Symlink Reason = "symlink"
	// Submodule is a Git submodule, which is never fetched.
	Submodule Reason = "submodule"
	// UnsafePath is an entry whose name could mislead or escape: empty, .
	// or .., holding a separator, a control character or invalid UTF-8,
	// named .git, given twice in a directory, or too deep or too long.
	// Nothing below an unsafe directory is read.
	UnsafePath Reason = "unsafe_path"
	// NotAFile is an entry that is neither a file, a directory, a symbolic
	// link nor a submodule.
	NotAFile Reason = "not_a_file"
)

// TooManyFilesError means a source holds more files than its MaxFiles
// allows, once its patterns apply.
type TooManyFilesError struct {
	Limit int
}

func (e *TooManyFilesError) Error() string {
	return fmt.Sprintf("the source holds more than %d files, its max_files: narrow it with include or exclude patterns, or raise max_files", e.Limit)
}
