package source

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestPruneCache(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"lib", "old", "gone"} {
		if err := os.MkdirAll(filepath.Join(CacheDir(root, name), "x"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "stray-file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err := PruneCache(t.Context(), root, []string{"lib", "new"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"gone", "old"}, removed); diff != "" {
		t.Errorf("removed (-want +got):\n%s", diff)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if diff := cmp.Diff([]string{"lib", "stray-file"}, left); diff != "" {
		t.Errorf("left (-want +got):\n%s", diff)
	}

	if removed, err := PruneCache(t.Context(), filepath.Join(root, "absent"), nil); err != nil || removed != nil {
		t.Errorf("PruneCache of a root that doesn't exist = %v, %v", removed, err)
	}
}

func TestLockCache(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	unlock, err := LockCache(t.Context(), root, "lib", time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Another fetch of the source waits, and gives up; one of another
	// source doesn't wait.
	if _, err := LockCache(t.Context(), root, "lib", 200*time.Millisecond, nil); !errors.Is(err, ErrFetching) ||
		err.Error() != "another TrueUp process is fetching the source, and has been for over 200ms" {
		t.Errorf("a second lock on lib = %v, want it to give up", err)
	}
	// A source whose name looks like a lock file's has a cache of its own.
	if err := os.MkdirAll(CacheDir(root, "lib.lock"), 0o700); err != nil {
		t.Errorf("the cache of a source named lib.lock: %v", err)
	}
	other, err := LockCache(t.Context(), root, "other", 0, nil)
	if err != nil {
		t.Fatalf("a lock on another source: %v", err)
	}
	if err := other(); err != nil {
		t.Error(err)
	}

	// Pruning leaves the locks.
	if removed, err := PruneCache(t.Context(), root, nil); err != nil || !slices.Equal(removed, []string{"lib.lock"}) {
		t.Errorf("PruneCache = %v, %v; want only lib.lock's cache removed", removed, err)
	}
	if _, err := os.Stat(filepath.Join(root, lockDir, "lib")); err != nil {
		t.Errorf("lib's lock after pruning: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	again, err := LockCache(t.Context(), root, "lib", 0, nil)
	if err != nil {
		t.Fatalf("a lock on lib once released: %v", err)
	}
	if err := again(); err != nil {
		t.Error(err)
	}
}
