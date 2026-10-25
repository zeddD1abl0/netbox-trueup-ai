package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

// node describes an entry of a fake source's content.
type node struct {
	name     string
	typ      EntryType
	content  string
	size     int64 // when not 0, the size the entry claims instead of len(content)
	children []node
}

func file(name, content string) node { return node{name: name, typ: EntryFile, content: content} }

func dir(name string, children ...node) node {
	return node{name: name, typ: EntryDir, children: children}
}

func link(name string) node { return node{name: name, typ: EntrySymlink} }

// fakeContent is Content held in memory. It records the directories read,
// and whether it was committed and closed.
type fakeContent struct {
	mu        sync.Mutex
	dirs      map[string][]Entry
	files     map[string]string
	readDirs  []string
	committed bool
	commitErr error // what Commit returns
	closed    bool
}

func newFake(children ...node) *fakeContent {
	c := &fakeContent{dirs: map[string][]Entry{}, files: map[string]string{}}
	c.add("root", children)
	return c
}

func (c *fakeContent) add(id string, children []node) {
	entries := []Entry{} // a directory with no entries reads as empty
	for i, n := range children {
		childID := id + "/" + strconv.Itoa(i)
		e := Entry{Name: n.name, Type: n.typ, ID: childID}
		switch n.typ {
		case EntryDir:
			c.add(childID, n.children)
		case EntryFile:
			c.files[childID] = n.content
			e.Size = int64(len(n.content))
			if n.size != 0 {
				e.Size = n.size
			}
		case EntrySymlink, EntrySubmodule, EntryOther:
		}
		entries = append(entries, e)
	}
	c.dirs[id] = entries
}

func (c *fakeContent) Version() string { return "v1" }
func (c *fakeContent) Ref() string     { return "main" }
func (c *fakeContent) Root() string    { return "root" }

func (c *fakeContent) ReadDir(_ context.Context, id string) ([]Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDirs = append(c.readDirs, id)
	entries, ok := c.dirs[id]
	if !ok {
		return nil, errors.New("no such directory")
	}
	return slices.Clone(entries), nil
}

func (c *fakeContent) Open(_ context.Context, id string) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	content, ok := c.files[id]
	if !ok {
		return nil, errors.New("object not found")
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func (c *fakeContent) Commit(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.commitErr != nil {
		return c.commitErr
	}
	c.committed = true
	return nil
}

func (c *fakeContent) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

// fakeKind hands out a fakeContent.
type fakeKind struct {
	content *fakeContent
	err     error
}

func (k fakeKind) Fetch(context.Context, Spec) (Content, error) {
	if k.err != nil {
		return nil, k.err
	}
	return k.content, nil
}

func (k fakeKind) Open(ctx context.Context, spec Spec) (Content, error) {
	return k.Fetch(ctx, spec)
}

var testLimits = config.SourceLimits{MaxFileSize: 64, MaxFiles: 100, FetchTimeout: time.Minute, MaxFetchSize: 1 << 30}

func spec(t *testing.T, include, exclude []string) Spec {
	t.Helper()
	s, err := NewSpec(config.Source{Name: "lib", Kind: "git", Include: include, Exclude: exclude, Limits: testLimits})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// library is a small source shaped like devicetype-library.
func library() *fakeContent {
	return newFake(
		file("README.md", "readme"),
		dir("device-types",
			dir("Juniper", file("MX204.yaml", "model: MX204"), file("EX2300.yml", "model: EX2300"), link("latest.yaml")),
			dir("Cisco", file("C9300.yaml", "model: C9300")),
		),
		dir("elevation-images", dir("Juniper", file("mx204.front.png", strings.Repeat("x", 100)))),
		dir("schema", file("devicetype.json", "{}")),
	)
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		include      []string
		exclude      []string
		wantFiles    []string
		wantSkipped  []Skipped
		wantReadDirs []string // when set, the directories the walk must read
	}{
		{
			name: "everything",
			wantFiles: []string{
				"README.md", "device-types/Cisco/C9300.yaml", "device-types/Juniper/EX2300.yml",
				"device-types/Juniper/MX204.yaml", "schema/devicetype.json",
			},
			wantSkipped: []Skipped{
				{Path: "device-types/Juniper/latest.yaml", Reason: Symlink},
				{Path: "elevation-images/Juniper/mx204.front.png", Reason: TooLarge},
			},
		},
		{
			name:      "include narrows, and only what it chooses is reported",
			include:   []string{"device-types/Juniper/*.yaml"},
			wantFiles: []string{"device-types/Juniper/MX204.yaml"},
			wantSkipped: []Skipped{
				{Path: "device-types/Juniper/latest.yaml", Reason: Symlink},
			},
			// Directories no pattern could match below are never read.
			wantReadDirs: []string{"root", "root/1", "root/1/0"},
		},
		{
			name:         "several includes, and a directory chooses everything in it",
			include:      []string{"device-types/Cisco", "schema/*.json"},
			wantFiles:    []string{"device-types/Cisco/C9300.yaml", "schema/devicetype.json"},
			wantReadDirs: []string{"root", "root/1", "root/1/1", "root/3"},
		},
		{
			name:         "an excluded directory is never read",
			exclude:      []string{"elevation-images", "device-types/Juniper"},
			wantFiles:    []string{"README.md", "device-types/Cisco/C9300.yaml", "schema/devicetype.json"},
			wantReadDirs: []string{"root", "root/1", "root/1/1", "root/3"},
		},
		{
			name:      "exclude wins over include",
			include:   []string{"device-types/**"},
			exclude:   []string{"**/*.yml", "**/latest.yaml"},
			wantFiles: []string{"device-types/Cisco/C9300.yaml", "device-types/Juniper/MX204.yaml"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			content := library()
			snap, err := Fetch(t.Context(), fakeKind{content: content}, spec(t, tt.include, tt.exclude))
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			var paths []string
			for _, f := range snap.Files {
				paths = append(paths, f.Path)
			}
			if diff := cmp.Diff(tt.wantFiles, paths); diff != "" {
				t.Errorf("files (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantSkipped, snap.Skipped); diff != "" {
				t.Errorf("skipped (-want +got):\n%s", diff)
			}
			if tt.wantReadDirs != nil {
				if diff := cmp.Diff(tt.wantReadDirs, content.readDirs); diff != "" {
					t.Errorf("directories read (-want +got):\n%s", diff)
				}
			}
			if !content.committed {
				t.Error("Fetch didn't commit the content")
			}
			if snap.Source != "lib" || snap.Version != "v1" || snap.Ref != "main" {
				t.Errorf("snapshot is of %s at %s from %s, want lib at v1 from main", snap.Source, snap.Version, snap.Ref)
			}
			if err := snap.Close(); err != nil || !content.closed {
				t.Errorf("Close: %v, content closed: %v", err, content.closed)
			}
		})
	}
}

func TestSnapshotFiles(t *testing.T) {
	t.Parallel()

	content := library()
	snap, err := Fetch(t.Context(), fakeKind{content: content}, spec(t, []string{"device-types/Juniper/MX204.yaml"}, nil))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	want := []File{{Path: "device-types/Juniper/MX204.yaml", Size: 12, Digest: sha("model: MX204")}}
	if diff := cmp.Diff(want, snap.Files); diff != "" {
		t.Errorf("files (-want +got):\n%s", diff)
	}

	data, err := snap.ReadFile(t.Context(), "device-types/Juniper/MX204.yaml")
	if err != nil || string(data) != "model: MX204" {
		t.Errorf("ReadFile = %q, %v; want the file's content", data, err)
	}
	// Only listed files can be read, whatever the content holds.
	for _, p := range []string{"device-types/Cisco/C9300.yaml", "../README.md", "/README.md", ""} {
		if _, err := snap.ReadFile(t.Context(), p); err == nil || !strings.Contains(err.Error(), "has no file") {
			t.Errorf("ReadFile(%q) = %v, want an error saying there is no such file", p, err)
		}
	}
	// Content that changes after listing is refused.
	content.mu.Lock()
	content.files["root/1/0/0"] = "model: MX205"
	content.mu.Unlock()
	if _, err := snap.ReadFile(t.Context(), "device-types/Juniper/MX204.yaml"); err == nil || !strings.Contains(err.Error(), "changed in the cache") {
		t.Errorf("ReadFile of changed content = %v, want an error saying it changed", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := snap.ReadFile(ctx, "device-types/Juniper/MX204.yaml"); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadFile with a cancelled context = %v, want context.Canceled", err)
	}
}

func TestSnapshotSkipsUnsafeAndOtherEntries(t *testing.T) {
	t.Parallel()

	content := newFake(
		file("", "empty name"),
		file(".", "dot"),
		dir("..", file("passwd", "escaped")),
		dir(".git", file("config", "git config")),
		dir(".GIT", file("config", "git config")),
		file("a/b.yaml", "slash in a name"),
		file(`a\b.yaml`, "backslash in a name"),
		file("nul\x00.yaml", "NUL"),
		file("esc\x1b[31m.yaml", "terminal escape"),
		file("rtl\u202eyaml.exe", "bidirectional override"),
		file("bad\xff.yaml", "invalid UTF-8"),
		file("twice.yaml", "first"),
		file("twice.yaml", "second"),
		file("ok.yaml", "fine"),
		node{name: "module", typ: EntrySubmodule},
		node{name: "fifo", typ: EntryOther},
		link("link.yaml"),
		file("big.yaml", strings.Repeat("x", 65)),
		node{name: "not-fetched.png", typ: EntryFile, size: -1},
		file("exactly-the-limit.yaml", strings.Repeat("x", 64)),
	)
	snap, err := Fetch(t.Context(), fakeKind{content: content}, spec(t, nil, nil))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var paths []string
	for _, f := range snap.Files {
		paths = append(paths, f.Path)
	}
	if diff := cmp.Diff([]string{"exactly-the-limit.yaml", "ok.yaml"}, paths); diff != "" {
		t.Errorf("files (-want +got):\n%s", diff)
	}
	want := []Skipped{
		{Path: "", Reason: UnsafePath},
		{Path: ".", Reason: UnsafePath},
		{Path: "..", Reason: UnsafePath},
		{Path: ".GIT", Reason: UnsafePath},
		{Path: ".git", Reason: UnsafePath},
		{Path: "a/b.yaml", Reason: UnsafePath},
		{Path: `a\b.yaml`, Reason: UnsafePath},
		{Path: "bad\xff.yaml", Reason: UnsafePath},
		{Path: "big.yaml", Reason: TooLarge},
		{Path: "esc\x1b[31m.yaml", Reason: UnsafePath},
		{Path: "fifo", Reason: NotAFile},
		{Path: "link.yaml", Reason: Symlink},
		{Path: "module", Reason: Submodule},
		{Path: "not-fetched.png", Reason: TooLarge},
		{Path: "nul\x00.yaml", Reason: UnsafePath},
		{Path: "rtl\u202eyaml.exe", Reason: UnsafePath},
		{Path: "twice.yaml", Reason: UnsafePath},
	}
	if diff := cmp.Diff(want, snap.Skipped); diff != "" {
		t.Errorf("skipped (-want +got):\n%s", diff)
	}
	// Nothing below an unsafe directory is read.
	for _, id := range content.readDirs {
		if id != "root" {
			t.Errorf("read directory %s, below an unsafe name", id)
		}
	}
}

func TestSnapshotFixedLimits(t *testing.T) {
	t.Parallel()

	deep := file("leaf.yaml", "x")
	for i := range 4 {
		deep = dir("d"+strconv.Itoa(i), deep)
	}
	tests := []struct {
		name        string
		content     *fakeContent
		limits      walkLimits
		wantErr     string
		wantSkipped []Skipped
	}{
		{
			name:        "too deep",
			content:     newFake(deep),
			limits:      walkLimits{entries: 100, depth: 4, pathLen: 100},
			wantSkipped: []Skipped{{Path: "d3/d2/d1/d0/leaf.yaml", Reason: UnsafePath}},
		},
		{
			name:    "deep enough",
			content: newFake(deep),
			limits:  walkLimits{entries: 100, depth: 5, pathLen: 100},
		},
		{
			name:        "too long",
			content:     newFake(file(strings.Repeat("x", 11), "x")),
			limits:      walkLimits{entries: 100, depth: 5, pathLen: 10},
			wantSkipped: []Skipped{{Path: strings.Repeat("x", 11), Reason: UnsafePath}},
		},
		{
			name:    "too many entries",
			content: newFake(dir("a", link("1"), link("2")), link("3")),
			limits:  walkLimits{entries: 3, depth: 5, pathLen: 100},
			wantErr: "more than 3 entries",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			snap, err := newSnapshot(t.Context(), spec(t, nil, []string{"**/[0-9]"}), tt.content, tt.limits)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("newSnapshot error = %v, want one containing %q", err, tt.wantErr)
				}
				if !tt.content.closed {
					t.Error("the content wasn't closed after an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("newSnapshot: %v", err)
			}
			if diff := cmp.Diff(tt.wantSkipped, snap.Skipped); diff != "" {
				t.Errorf("skipped (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSnapshotErrors(t *testing.T) {
	t.Parallel()

	tooMany := spec(t, nil, nil)
	tooMany.Limits.MaxFiles = 2
	kindErr := errors.New("the kind failed")
	tests := []struct {
		name    string
		kind    fakeKind
		spec    Spec
		wantErr string
		wantIs  error
	}{
		{
			name:    "more files than max_files",
			kind:    fakeKind{content: newFake(file("a", "1"), file("b", "2"), file("c", "3"))},
			spec:    tooMany,
			wantErr: "the source holds more than 2 files, its max_files",
		},
		{
			name: "content longer than its entry says",
			kind: fakeKind{content: newFake(node{name: "a.yaml", typ: EntryFile, content: "longer", size: 3})},
			spec: spec(t, nil, nil), wantErr: "file a.yaml holds 6 bytes, but its entry says 3",
		},
		{
			name: "content that inflates past max_file_size",
			kind: fakeKind{content: newFake(node{name: "bomb.yaml", typ: EntryFile, content: strings.Repeat("x", 1000), size: 10})},
			spec: spec(t, nil, nil), wantErr: "file bomb.yaml holds more than its entry says, and more than max_file_size, 64 bytes",
		},
		{
			name: "a directory that can't be read",
			kind: fakeKind{content: func() *fakeContent {
				c := newFake(dir("a"))
				delete(c.dirs, "root/0")
				return c
			}()},
			spec: spec(t, nil, nil), wantErr: "read directory a: no such directory",
		},
		{
			name: "a file that can't be read",
			kind: fakeKind{content: func() *fakeContent {
				c := newFake(file("esc\x1b.yaml", "x"))
				delete(c.files, "root/0")
				return c
			}()},
			spec: spec(t, []string{"*"}, nil), wantErr: "",
		},
		{
			name: "the kind fails",
			kind: fakeKind{err: kindErr}, spec: spec(t, nil, nil), wantIs: kindErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Fetch(t.Context(), tt.kind, tt.spec)
			switch {
			case tt.wantIs != nil:
				if !errors.Is(err, tt.wantIs) {
					t.Fatalf("Fetch error = %v, want %v", err, tt.wantIs)
				}
				return
			case tt.wantErr == "":
				// An unsafe name is skipped before anything reads it.
				if err != nil {
					t.Fatalf("Fetch: %v", err)
				}
				return
			case err == nil || !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("Fetch error = %v, want one containing %q", err, tt.wantErr)
			}
			if !tt.kind.content.closed || tt.kind.content.committed {
				t.Errorf("after an error, the content was closed: %v, committed: %v; want closed, not committed",
					tt.kind.content.closed, tt.kind.content.committed)
			}
			var tooMany *TooManyFilesError
			if strings.Contains(tt.wantErr, "max_files") && !errors.As(err, &tooMany) {
				t.Errorf("Fetch error = %T, want *TooManyFilesError", err)
			}
		})
	}
}

// A version that can't be kept fails the fetch, and its snapshot is closed.
func TestFetchFailsWhenCommitFails(t *testing.T) {
	t.Parallel()
	content := library()
	content.commitErr = errors.New("disk full")
	_, err := Fetch(t.Context(), fakeKind{content: content}, spec(t, nil, nil))
	if err == nil || !strings.Contains(err.Error(), "keep version v1 in the cache: disk full") {
		t.Errorf("Fetch = %v, want the commit's error", err)
	}
	if !content.closed {
		t.Error("the content wasn't closed")
	}
}

func TestErrorsQuoteUnsafePaths(t *testing.T) {
	t.Parallel()

	c := newFake(dir("dir with space", file("a.yaml", "x")))
	delete(c.files, "root/0/0")
	_, err := Fetch(t.Context(), fakeKind{content: c}, spec(t, nil, nil))
	if err == nil || !strings.Contains(err.Error(), `open file "dir with space/a.yaml": object not found`) {
		t.Errorf("Fetch error = %v, want the path quoted", err)
	}
}

func TestFetchStopsWhenCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	content := library()
	if _, err := Fetch(ctx, fakeKind{content: content}, spec(t, nil, nil)); !errors.Is(err, context.Canceled) {
		t.Errorf("Fetch error = %v, want context.Canceled", err)
	}
	if !content.closed {
		t.Error("the content wasn't closed")
	}
}

func TestOpen(t *testing.T) {
	t.Parallel()

	content := library()
	snap, err := Open(t.Context(), fakeKind{content: content}, spec(t, []string{"schema"}, nil))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if content.committed {
		t.Error("Open committed the content")
	}
	if len(snap.Files) != 1 || snap.Files[0].Path != "schema/devicetype.json" {
		t.Errorf("Open listed %+v, want schema/devicetype.json", snap.Files)
	}
	if _, err := Open(t.Context(), fakeKind{err: ErrNotFetched}, spec(t, nil, nil)); !errors.Is(err, ErrNotFetched) {
		t.Errorf("Open error = %v, want ErrNotFetched", err)
	}
}

func TestNewSpec(t *testing.T) {
	t.Parallel()

	s, err := NewSpec(config.Source{
		Name: "lib", Kind: "git", URL: "https://example.com/lib.git", Ref: "main",
		Include: []string{"device-types/**"}, Exclude: []string{"**/*.png"}, Limits: testLimits,
	})
	if err != nil {
		t.Fatalf("NewSpec: %v", err)
	}
	if s.Name != "lib" || s.URL != "https://example.com/lib.git" || s.Ref != "main" || s.Limits != testLimits ||
		len(s.Include) != 1 || s.Include[0].String() != "device-types/**" || len(s.Exclude) != 1 {
		t.Errorf("NewSpec = %+v", s)
	}
	for name, src := range map[string]config.Source{
		"a bad pattern": {Name: "lib", Include: []string{"/abs"}, Limits: testLimits},
		"no limits":     {Name: "lib"},
	} {
		if _, err := NewSpec(src); err == nil {
			t.Errorf("NewSpec with %s succeeded", name)
		}
	}
}
