// Package sourcetest offers sources for tests: the files of a file system,
// such as a testdata directory or an fstest.MapFS, as a source kind, so that
// tests of what reads snapshots go through the source layer's own checks.
package sourcetest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"testing"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/glob"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
)

// Kind is a source kind whose every source holds the files of FS, at
// Version. Fetching and opening are the same, and committing does nothing.
type Kind struct {
	FS      fs.FS
	Version string
}

// Fetch returns the content of k.FS.
func (k Kind) Fetch(ctx context.Context, spec source.Spec) (source.Content, error) {
	return k.Open(ctx, spec)
}

// Open returns the content of k.FS, or an error wrapping
// source.ErrNotFetched if k.FS is nil.
func (k Kind) Open(context.Context, source.Spec) (source.Content, error) {
	if k.FS == nil {
		return nil, fmt.Errorf("sourcetest: %w", source.ErrNotFetched)
	}
	return content{fsys: k.FS, version: k.Version}, nil
}

type content struct {
	fsys    fs.FS
	version string
}

func (c content) Version() string { return c.version }
func (c content) Ref() string     { return "main" }
func (c content) Root() string    { return "." }

func (c content) ReadDir(_ context.Context, id string) ([]source.Entry, error) {
	entries, err := fs.ReadDir(c.fsys, id)
	if err != nil {
		return nil, err
	}
	out := make([]source.Entry, 0, len(entries))
	for _, e := range entries {
		entry := source.Entry{Name: e.Name(), ID: path.Join(id, e.Name())}
		switch {
		case e.Type().IsDir():
			entry.Type = source.EntryDir
		case e.Type()&fs.ModeSymlink != 0:
			entry.Type = source.EntrySymlink
		case e.Type().IsRegular():
			info, err := e.Info()
			if err != nil {
				return nil, err
			}
			entry.Type, entry.Size = source.EntryFile, info.Size()
		default:
			entry.Type = source.EntryOther
		}
		out = append(out, entry)
	}
	return out, nil
}

func (c content) Open(_ context.Context, id string) (io.ReadCloser, error) {
	return c.fsys.Open(id)
}

func (content) Commit(context.Context) error { return nil }
func (content) Close() error                 { return nil }

// Limits are generous limits for tests' sources.
var Limits = config.SourceLimits{MaxFileSize: 1 << 20, MaxFiles: 1000, FetchTimeout: time.Hour, MaxFetchSize: 1 << 30}

// Snapshot returns a snapshot of fsys's files as a source called name would
// hold them, chosen by include patterns, or all of them if there are none.
func Snapshot(t testing.TB, name string, fsys fs.FS, include ...string) *source.Snapshot {
	t.Helper()
	return SnapshotOf(t, Kind{FS: fsys, Version: "v1"}, config.Source{Name: name, Kind: "test", Include: include, Limits: Limits})
}

// SnapshotOf returns a snapshot of the source as kind holds it, closed when
// the test ends.
func SnapshotOf(t testing.TB, kind source.Kind, s config.Source) *source.Snapshot {
	t.Helper()
	spec, err := source.NewSpec(s)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := source.Open(t.Context(), kind, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snap.Close(); err != nil && !errors.Is(err, fs.ErrClosed) {
			t.Error(err)
		}
	})
	return snap
}

// Patterns compiles patterns, such as for source.Spec.Only.
func Patterns(t testing.TB, texts ...string) []glob.Pattern {
	t.Helper()
	out, err := glob.CompileAll(texts)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
