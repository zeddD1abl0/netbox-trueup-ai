package git

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestLatestFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := readLatest(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("readLatest with no file = %v, want fs.ErrNotExist", err)
	}
	want := latest{
		Version: strings.Repeat("ab", 20), URL: "https://example.com/lib.git", Requested: "",
		Ref: "refs/heads/main", Advertised: strings.Repeat("cd", 20), Filter: 1 << 20,
	}
	if err := writeLatest(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := readLatest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("latest (-want +got):\n%s", diff)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Errorf("the directory holds %v, %v; want only %s", entries, err, latestFile)
	}

	for name, content := range map[string]string{
		"too large":           strings.Repeat(" ", maxLatestSize+1),
		"no version":          `{"url": "https://example.com/lib.git"}`,
		"a version with dots": `{"version": "../../../../etc/passwd"}`,
		"upper case":          `{"version": "` + strings.Repeat("AB", 20) + `"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, latestFile), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readLatest(dir); err == nil {
			t.Errorf("readLatest of a file with %s succeeded", name)
		}
	}
}

func TestIsHash(t *testing.T) {
	t.Parallel()
	for s, want := range map[string]bool{
		strings.Repeat("a", 40): true,
		strings.Repeat("0", 64): true,
		strings.Repeat("a", 39): false,
		strings.Repeat("A", 40): false,
		strings.Repeat("g", 40): false,
		"":                      false,
	} {
		if got := isHash(s); got != want {
			t.Errorf("isHash(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestClean(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now()
	keep := strings.Repeat("a", 40)
	for _, name := range []string{keep, strings.Repeat("b", 40), "tmp-fresh", "tmp-stale", "tmp-latest.json-x"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, latestFile), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-staleAfter - time.Minute)
	for _, name := range []string{"tmp-stale", "tmp-latest.json-x"} {
		if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	clean(dir, keep, now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if diff := cmp.Diff([]string{keep, latestFile, "tmp-fresh"}, got); diff != "" {
		t.Errorf("after cleaning (-want +got):\n%s", diff)
	}
}

func TestInstall(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	version := strings.Repeat("a", 40)
	for _, step := range []string{"first", "again"} {
		tmp, err := os.MkdirTemp(dir, tmpPrefix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, "config"), []byte(step), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := install(dir, tmp, version); err != nil {
			t.Fatalf("install %s: %v", step, err)
		}
		data, err := os.ReadFile(filepath.Clean(filepath.Join(dir, version, "config")))
		if err != nil || string(data) != step {
			t.Errorf("after installing %s, the clone holds %q, %v", step, data, err)
		}
	}
}
