package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Each kind keeps a source's cache in a directory of its own, named for the
// source, below a root that is normally <data_dir>/cache/sources. The cache
// is disposable: deleting it costs a fetch.

// CacheDir returns the directory below root that holds a source's cache.
func CacheDir(root, name string) string {
	return filepath.Join(root, name)
}

// PruneCache removes the caches below root of sources not named in keep,
// and returns the names it removed. A root that doesn't exist yet holds
// nothing to remove.
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
