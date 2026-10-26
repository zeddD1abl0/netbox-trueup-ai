package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/filelock"
)

// Each kind keeps a source's cache in a directory of its own, named for the
// source, below a root that is normally <data_dir>/cache/sources. The cache
// is disposable: deleting it costs a fetch.

// CacheDir returns the directory below root that holds a source's cache.
func CacheDir(root, name string) string {
	return filepath.Join(root, name)
}

// LockCache takes the fetch lock of a source's cache below root, and returns
// the function that releases it. A fetch holds it until the fetch is
// recorded, so that fetches of one source, in any process, happen one after
// another, and the cache and the database agree on the latest. It waits up
// to timeout for another fetch to finish, calling onWait, if it isn't nil,
// once it has waited a second.
//
// The lock is on a file beside the source's directory, <name>.lock, which
// PruneCache leaves alone, so that it is never removed while held.
func LockCache(ctx context.Context, root, name string, timeout time.Duration, onWait func()) (unlock func() error, err error) {
	lock, err := filelock.Exclusive(ctx, filepath.Join(root, name+".lock"), timeout, onWait)
	switch {
	case errors.Is(err, filelock.ErrTimeout):
		return nil, fmt.Errorf("another TrueUp process has been fetching the source for over %s", timeout)
	case err != nil:
		return nil, fmt.Errorf("lock the source's cache: %w", err)
	}
	return lock.Unlock, nil
}

// PruneCache removes the caches below root of sources not named in keep,
// and returns the names it removed. A root that doesn't exist yet holds
// nothing to remove. It removes directories only, and leaves the sources'
// lock files.
func PruneCache(ctx context.Context, root string, keep []string) (removed []string, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the source cache: %w", err)
	}
	var errs []error
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return removed, errors.Join(append(errs, err)...)
		}
		if !e.IsDir() || slices.Contains(keep, e.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			errs = append(errs, fmt.Errorf("remove the cache of source %q: %w", e.Name(), err))
			continue
		}
		removed = append(removed, e.Name())
	}
	return removed, errors.Join(errs...)
}
