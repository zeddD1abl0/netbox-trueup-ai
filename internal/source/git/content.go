package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
)

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
	tree, err := object.GetTree(c.repo.Storer, h)
	if err != nil {
		return nil, fmt.Errorf("read tree %s: %w", id, err)
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
// -1 if a filtered fetch left it out for being too large.
func (c *content) size(h plumbing.Hash) (int64, error) {
	size, err := c.repo.Storer.EncodedObjectSize(h)
	switch {
	case errors.Is(err, plumbing.ErrObjectNotFound) && c.filtered:
		return -1, nil
	case err != nil:
		// Without a filter, every blob was fetched: the clone is broken.
		return 0, fmt.Errorf("read the size of blob %s: %w", h, err)
	}
	return size, nil
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
		return nil, fmt.Errorf("read blob %s: %w", id, err)
	}
	r, err := obj.Reader()
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", id, err)
	}
	return r, nil
}

func (c *content) Close() error {
	return c.repo.Close()
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
