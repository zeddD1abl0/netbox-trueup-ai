package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/filelock"
)

// Each kind keeps a source's cache in a directory of its own, named for the
// source, below a root that is normally <data_dir>/cache/sources. The cache
// is disposable: deleting it costs a fetch.
//
// The root also holds lockDir, the sources' fetch locks. Source names can't
// start with a dot, so it is never a source's cache.
const lockDir = ".locks"

// ErrFetching means another TrueUp process held a source's fetch lock for
// longer than the wait allowed.
var ErrFetching = errors.New("another TrueUp process is fetching the source")

// CacheDir returns the directory below root that holds a source's cache.
func CacheDir(root, name string) string {
	return filepath.Join(root, name)
}

// LockCache takes the fetch lock of a source's cache below root, and returns
// the function that releases it. A fetch holds it until the fetch is
// recorded, so that fetches of one source, in any process, happen one after
// another, and the cache and the database agree on the latest. Removing a
// source's cache takes it too. It waits up to timeout for another holder to
// let go, calling onWait, if it isn't nil, once it has waited a second, and
// returns an error wrapping ErrFetching if the holder doesn't.
//
// The lock is on a file in root/.locks, which PruneCache leaves alone, so
// that it is never removed while held.
func LockCache(ctx context.Context, root, name string, timeout time.Duration, onWait func()) (unlock func() error, err error) {
	dir := filepath.Join(root, lockDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the directory of the sources' fetch locks: %w", err)
	}
	lock, err := filelock.Exclusive(ctx, filepath.Join(dir, name), timeout, onWait)
	switch {
	case errors.Is(err, filelock.ErrTimeout):
		return nil, fmt.Errorf("%w, and has been for over %s", ErrFetching, timeout)
	case err != nil:
		return nil, fmt.Errorf("lock the source's cache: %w", err)
	}
	return lock.Unlock, nil
}

// CachedSources returns the names of the sources that have a cache below
// root, sorted. A root that doesn't exist yet holds none.
func CachedSources(ctx context.Context, root string) ([]string, error) {
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
	var names []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// PruneCache removes the caches below root of sources not named in keep,
// and returns the names it removed. It leaves the fetch locks. Callers hold
// the fetch lock of each source whose cache it may remove, so that it never
// removes a cache that another process is fetching into.
func PruneCache(ctx context.Context, root string, keep []string) (removed []string, err error) {
	names, err := CachedSources(ctx, root)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return removed, errors.Join(append(errs, err)...)
		}
		if slices.Contains(keep, name) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			errs = append(errs, fmt.Errorf("remove the cache of source %q: %w", name, err))
			continue
		}
		removed = append(removed, name)
	}
	return removed, errors.Join(errs...)
}
