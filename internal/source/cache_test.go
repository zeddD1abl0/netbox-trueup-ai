package source

import (
	"os"
	"path/filepath"
	"testing"

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
	removed, err := PruneCache(root, []string{"lib", "new"})
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

	if removed, err := PruneCache(filepath.Join(root, "absent"), nil); err != nil || removed != nil {
		t.Errorf("PruneCache of a root that doesn't exist = %v, %v", removed, err)
	}
}
