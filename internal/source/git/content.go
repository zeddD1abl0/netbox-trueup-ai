package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
)

// maxTreeSize bounds a tree object, which go-git decodes whole into memory
// before the source layer can count its entries. devicetype-library's
// largest, for a directory of 2,900 images, is about 150 KB.
const maxTreeSize = 32 << 20

// content is a commit's tree, read from a bare clone's object store. Nothing
// is checked out, so hooks, filters and submodules never come into play.
type content struct {
	repo    *gogit.Repository
	version string // the commit's hash
	ref     string // the branch or tag, such as master
	root    string // the hash of the commit's tree
	// filtered is set when the clone left out files larger than the
	// source's max_file_size, which then read as missing.
	filtered bool
	// cacheDir is the source's cache directory, and clone the clone's
	// directory in it.
	cacheDir, clone string

	// commit is the latest.json that Commit writes, or nil if there is
	// nothing to write. discard is set for a clone that Fetch made, which
	// Close removes unless it was committed.
	commit  *latest
	discard bool
	maxTree int64 // the largest tree object to read, normally maxTreeSize
	log     *slog.Logger
	now     func() time.Time
}

var _ source.Content = (*content)(nil)

func (c *content) Version() string { return c.version }
func (c *content) Ref() string     { return c.ref }
func (c *content) Root() string    { return c.root }

func (c *content) ReadDir(ctx context.Context, id string) ([]source.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h, err := parseHash(id)
	if err != nil {
		return nil, err
	}
	size, err := c.repo.Storer.EncodedObjectSize(h)
	if err != nil {
		return nil, c.damaged(fmt.Errorf("read the size of tree %s: %w", id, err))
	}
	if size > c.maxTree {
		return nil, fmt.Errorf("tree %s holds %d bytes, more than the %d a directory may", id, size, c.maxTree)
	}
	tree, err := object.GetTree(c.repo.Storer, h)
	if err != nil {
		return nil, c.damaged(fmt.Errorf("read tree %s: %w", id, err))
	}
	entries := make([]source.Entry, 0, len(tree.Entries))
	for _, e := range tree.Entries {
		entry := source.Entry{Name: e.Name, ID: e.Hash.String()}
		switch e.Mode {
		case filemode.Dir:
			entry.Type = source.EntryDir
		case filemode.Regular, filemode.Executable, filemode.Deprecated:
			entry.Type = source.EntryFile
			if entry.Size, err = c.size(e.Hash); err != nil {
				return nil, err
			}
		case filemode.Symlink:
			entry.Type = source.EntrySymlink
		case filemode.Submodule:
			entry.Type = source.EntrySubmodule
		default:
			entry.Type = source.EntryOther
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// size returns a blob's size from the object store, without reading it, or
// -1 if a filtered fetch left it out for being too large. A blob counts as
// left out only when the store is sure it doesn't hold it: an error reading
// the store is an error, never a file to skip.
func (c *content) size(h plumbing.Hash) (int64, error) {
	size, err := c.repo.Storer.EncodedObjectSize(h)
	if err == nil {
		return size, nil
	}
	if !errors.Is(err, plumbing.ErrObjectNotFound) {
		return 0, c.damaged(fmt.Errorf("read the size of blob %s: %w", h, err))
	}
	switch hasErr := c.repo.Storer.HasEncodedObject(h); {
	case hasErr == nil:
		return 0, c.damaged(fmt.Errorf("blob %s is in the clone, but its size can't be read: %w", h, err))
	case !errors.Is(hasErr, plumbing.ErrObjectNotFound):
		return 0, c.damaged(fmt.Errorf("look for blob %s: %w", h, hasErr))
	case c.filtered:
		return -1, nil
	default:
		// Without a filter, every blob was fetched: the clone is broken.
		return 0, c.damaged(fmt.Errorf("blob %s is missing from the clone", h))
	}
}

func (c *content) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h, err := parseHash(id)
	if err != nil {
		return nil, err
	}
	obj, err := c.repo.Storer.EncodedObject(plumbing.BlobObject, h)
	if err != nil {
		return nil, c.damaged(fmt.Errorf("read blob %s: %w", id, err))
	}
	r, err := obj.Reader()
	if err != nil {
		return nil, c.damaged(fmt.Errorf("read blob %s: %w", id, err))
	}
	return r, nil
}

// damaged adds to an error from reading the clone how to recover, should
// the cache be damaged: a fetch reuses a clone of an unchanged commit.
func (c *content) damaged(err error) error {
	return fmt.Errorf("%w; if the cache is damaged, delete %s to fetch the source again", err, c.cacheDir)
}

// Commit makes the clone the source's cached version: it replaces
// latest.json, then removes the clone that latest.json named before.
func (c *content) Commit(ctx context.Context) error {
	if c.commit == nil {
		return nil
	}
	var replaced string
	if prev, err := readLatest(ctx, c.cacheDir); err == nil && prev.Dir != c.commit.Dir {
		replaced = prev.Dir
	}
	if err := writeLatest(ctx, c.cacheDir, *c.commit); err != nil {
		return err
	}
	c.commit, c.discard = nil, false
	clean(ctx, c.cacheDir, c.clone, replaced, c.now(), c.log)
	return nil
}

// Close releases the clone, and removes it if Fetch made it and it wasn't
// committed.
func (c *content) Close() error {
	err := c.repo.Close()
	if c.discard {
		c.discard = false
		if rmErr := os.RemoveAll(filepath.Join(c.cacheDir, c.clone)); rmErr != nil {
			c.log.Warn("can't remove a clone that wasn't kept", "path", filepath.Join(c.cacheDir, c.clone), "error", rmErr)
		}
	}
	return err
}

// parseHash reads an ID that ReadDir or Root gave out.
func parseHash(id string) (plumbing.Hash, error) {
	if !isHash(id) {
		return plumbing.Hash{}, fmt.Errorf("%q is not an object ID", id)
	}
	h, ok := plumbing.FromHex(id)
	if !ok {
		return plumbing.Hash{}, fmt.Errorf("%q is not an object ID", id)
	}
	return h, nil
}

// shortRef returns a ref's name without refs/heads/ or refs/tags/.
func shortRef(name plumbing.ReferenceName) string {
	s := string(name)
	for _, prefix := range []string{"refs/heads/", "refs/tags/"} {
		if rest, ok := strings.CutPrefix(s, prefix); ok {
			return rest
		}
	}
	return s
}
