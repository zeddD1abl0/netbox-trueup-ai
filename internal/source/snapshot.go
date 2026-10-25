package source

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/glob"
)

// walkLimits are fixed limits on walking a source's content, whatever its
// settings. They stop a hostile source from exhausting memory or the stack
// with entries that patterns would never choose.
type walkLimits struct {
	entries int // entries of any type in all the directories walked
	depth   int // segments in a path
	pathLen int // bytes in a path
}

var fixedLimits = walkLimits{entries: 1000000, depth: 128, pathLen: 4096}

// Snapshot is a source's content at one version: the files that its
// patterns choose and its limits allow. Close it when done.
type Snapshot struct {
	// Source is the source's name.
	Source string
	// Version identifies the content, such as a Git commit's hash.
	Version string
	// Ref names what the version was resolved from, such as the Git branch
	// master, or is "" for kinds without one.
	Ref string
	// Files are the files to read, sorted by path.
	Files []File
	// Skipped are the entries that the patterns choose but the snapshot
	// leaves out, sorted by path.
	Skipped []Skipped

	content     Content
	byPath      map[string]listed
	maxFileSize int64
}

// listed is a file in Files, with its ID in the content.
type listed struct {
	File
	id string
}

// Fetch fetches the source's latest version, using kind, and returns a
// snapshot of it. The version replaces the cached one only if its snapshot
// passes every check: if it fails one, such as max_files, the cache keeps
// the previous version.
func Fetch(ctx context.Context, kind Kind, spec Spec) (*Snapshot, error) {
	content, err := kind.Fetch(ctx, spec)
	if err != nil {
		return nil, err
	}
	snap, err := newSnapshot(ctx, spec, content, fixedLimits)
	if err != nil {
		return nil, err
	}
	if err := content.Commit(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("keep version %s in the cache: %w", snap.Version, err), snap.Close())
	}
	return snap, nil
}

// Open returns a snapshot of the source's cached version, as the last
// successful Fetch left it, without the network. It returns an error
// wrapping ErrNotFetched if there is none.
func Open(ctx context.Context, kind Kind, spec Spec) (*Snapshot, error) {
	content, err := kind.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return newSnapshot(ctx, spec, content, fixedLimits)
}

// newSnapshot walks content and lists its files. It takes ownership of
// content, closing it if it fails.
func newSnapshot(ctx context.Context, spec Spec, content Content, limits walkLimits) (_ *Snapshot, err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, content.Close())
		}
	}()
	w := &walker{spec: spec, limits: limits, content: content}
	if err := w.dir(ctx, content.Root(), "", 0); err != nil {
		return nil, err
	}
	s := &Snapshot{
		Source:      spec.Name,
		Version:     content.Version(),
		Ref:         content.Ref(),
		Files:       make([]File, 0, len(w.files)),
		Skipped:     w.skipped,
		content:     content,
		byPath:      make(map[string]listed, len(w.files)),
		maxFileSize: spec.Limits.MaxFileSize,
	}
	slices.SortFunc(w.files, func(a, b found) int { return strings.Compare(a.path, b.path) })
	slices.SortFunc(s.Skipped, func(a, b Skipped) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), strings.Compare(string(a.Reason), string(b.Reason)))
	})
	// Read every file once, to give its exact size and digest. Its entry's
	// size could be wrong, since the kind takes it from what the source
	// says.
	for _, f := range w.files {
		h := sha256.New()
		n, err := s.copy(ctx, h, f.path, f.id)
		if err != nil {
			return nil, err
		}
		if n != f.size {
			return nil, fmt.Errorf("file %s holds %d bytes, but its entry says %d", quote(f.path), n, f.size)
		}
		file := File{Path: f.path, Size: n, Digest: "sha256:" + hex.EncodeToString(h.Sum(nil))}
		s.Files = append(s.Files, file)
		s.byPath[f.path] = listed{File: file, id: f.id}
	}
	return s, nil
}

// ReadFile returns the content of one of the snapshot's files. It refuses a
// path that isn't among Files, and content that no longer matches the
// file's size and digest.
func (s *Snapshot) ReadFile(ctx context.Context, path string) ([]byte, error) {
	file, ok := s.byPath[path]
	if !ok {
		return nil, fmt.Errorf("source %s has no file %s", s.Source, quote(path))
	}
	var buf bytes.Buffer
	buf.Grow(int(file.Size)) // at most max_file_size, which is at most 64 MiB
	if _, err := s.copy(ctx, &buf, path, file.id); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(buf.Bytes())
	if int64(buf.Len()) != file.Size || "sha256:"+hex.EncodeToString(sum[:]) != file.Digest {
		return nil, fmt.Errorf("file %s of source %s changed in the cache since it was listed", quote(path), s.Source)
	}
	return buf.Bytes(), nil
}

// Close releases what the snapshot holds open.
func (s *Snapshot) Close() error {
	return s.content.Close()
}

// copy copies a file's content to dst, and refuses more than the size
// limit.
func (s *Snapshot) copy(ctx context.Context, dst io.Writer, path, id string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	rc, err := s.content.Open(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("open file %s: %w", quote(path), err)
	}
	// security: content is stored compressed, so its size is only known
	// once read; the limit stops an object that inflates to far more than
	// its entry says.
	n, err := io.Copy(dst, io.LimitReader(rc, s.maxFileSize+1))
	if closeErr := rc.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, fmt.Errorf("read file %s: %w", quote(path), err)
	}
	if n > s.maxFileSize {
		return 0, fmt.Errorf("file %s holds more than its entry says, and more than max_file_size, %d bytes", quote(path), s.maxFileSize)
	}
	return n, nil
}

// found is a file the walk chose.
type found struct {
	path, id string
	size     int64
}

// walker walks a source's content, choosing files by the spec's patterns
// and limits.
type walker struct {
	spec    Spec
	limits  walkLimits
	content Content
	entries int
	files   []found
	skipped []Skipped
}

// dir walks the directory with the given ID, at dirPath ("" for the top),
// whose path has depth segments.
func (w *walker) dir(ctx context.Context, id, dirPath string, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := w.content.ReadDir(ctx, id)
	if err != nil {
		if dirPath == "" {
			return fmt.Errorf("read the top directory: %w", err)
		}
		return fmt.Errorf("read directory %s: %w", quote(dirPath), err)
	}
	w.entries += len(entries)
	if w.entries > w.limits.entries {
		return fmt.Errorf("the source holds more than %d entries, files and directories together", w.limits.entries)
	}
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	twice := map[string]bool{} // names given twice, reported once each
	for i := 1; i < len(entries); i++ {
		if entries[i].Name == entries[i-1].Name {
			twice[entries[i].Name] = false
		}
	}
	for _, e := range entries {
		p := e.Name
		if dirPath != "" {
			p = dirPath + "/" + e.Name
		}
		// A directory matters if the patterns could choose something in
		// it; anything else only if they choose it.
		if e.Type == EntryDir && !w.mayChooseBelow(p) || e.Type != EntryDir && !w.chosen(p) {
			continue
		}
		reported, dup := twice[e.Name]
		switch {
		case dup:
			if !reported {
				twice[e.Name] = true
				w.skip(p, UnsafePath)
			}
			continue
		case !safeName(e.Name) || depth+1 > w.limits.depth || len(p) > w.limits.pathLen:
			w.skip(p, UnsafePath)
			continue
		}
		switch e.Type {
		case EntryDir:
			if err := w.dir(ctx, e.ID, p, depth+1); err != nil {
				return err
			}
		case EntryFile:
			if e.Size < 0 || e.Size > w.spec.Limits.MaxFileSize {
				w.skip(p, TooLarge)
				continue
			}
			if len(w.files) == w.spec.Limits.MaxFiles {
				return &TooManyFilesError{Limit: w.spec.Limits.MaxFiles}
			}
			w.files = append(w.files, found{path: p, id: e.ID, size: e.Size})
		case EntrySymlink:
			w.skip(p, Symlink)
		case EntrySubmodule:
			w.skip(p, Submodule)
		case EntryOther:
			w.skip(p, NotAFile)
		}
	}
	return nil
}

func (w *walker) skip(p string, reason Reason) {
	w.skipped = append(w.skipped, Skipped{Path: p, Reason: reason})
}

// chosen reports whether the patterns choose a path.
func (w *walker) chosen(p string) bool {
	return (len(w.spec.Include) == 0 || glob.MatchAny(w.spec.Include, p)) && !glob.MatchAny(w.spec.Exclude, p)
}

// mayChooseBelow reports whether the patterns could choose a path in the
// directory dir. An exclude pattern that matches a directory leaves out
// everything in it.
func (w *walker) mayChooseBelow(dir string) bool {
	if glob.MatchAny(w.spec.Exclude, dir) {
		return false
	}
	if len(w.spec.Include) == 0 {
		return true
	}
	return slices.ContainsFunc(w.spec.Include, func(p glob.Pattern) bool { return p.MatchBelow(dir) })
}

// safeName reports whether a name can be part of a path without misleading
// or escaping: not empty, . or .., without a separator, a NUL, a control or
// format character, or invalid UTF-8, and not .git in any case.
func safeName(name string) bool {
	switch {
	case name == "" || name == "." || name == "..",
		strings.EqualFold(name, ".git"),
		!utf8.ValidString(name):
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool {
		return r == '/' || r == '\\' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	})
}

// quote returns a path for an error message: quoted, with anything that
// could mislead a terminal escaped, when it holds such characters.
func quote(p string) string {
	if safePath(p) {
		return p
	}
	return strconv.Quote(p)
}

func safePath(p string) bool {
	for name := range strings.SplitSeq(p, "/") {
		if !safeName(name) || strings.ContainsAny(name, ` "'`) {
			return false
		}
	}
	return true
}
