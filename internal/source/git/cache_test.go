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
	if _, err := readLatest(t.Context(), dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("readLatest with no file = %v, want fs.ErrNotExist", err)
	}
	version := strings.Repeat("ab", 20)
	want := latest{
		Dir: version + "-123", Version: version, URL: "https://example.com/lib.git", Requested: "",
		Ref: "refs/heads/main", Advertised: strings.Repeat("cd", 20), Filter: 1 << 20,
	}
	if err := writeLatest(t.Context(), dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := readLatest(t.Context(), dir)
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
		"too large":              strings.Repeat(" ", maxLatestSize+1),
		"no version":             `{"dir": "` + version + `-1", "url": "https://example.com/lib.git"}`,
		"a version with dots":    `{"dir": "x-1", "version": "../../../../etc/passwd"}`,
		"upper case":             `{"dir": "` + strings.ToUpper(version) + `-1", "version": "` + strings.ToUpper(version) + `"}`,
		"a directory with dots":  `{"dir": "../../etc", "version": "` + version + `"}`,
		"another commit's clone": `{"dir": "` + strings.Repeat("ef", 20) + `-1", "version": "` + version + `"}`,
		"no directory":           `{"version": "` + version + `"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, latestFile), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readLatest(t.Context(), dir); err == nil {
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
	keep, replaced := strings.Repeat("a", 40)+"-1", strings.Repeat("b", 40)+"-2"
	other, stale := strings.Repeat("c", 40)+"-3", strings.Repeat("d", 40)+"-4"
	for _, name := range []string{keep, replaced, other, stale, "tmp-fresh", "tmp-stale", "tmp-latest.json-x"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, latestFile), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-staleAfter - time.Minute)
	for _, name := range []string{stale, "tmp-stale", "tmp-latest.json-x"} {
		if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	// The clone replaced goes at once; others only once stale, since
	// another fetch may be making or reading them.
	clean(t.Context(), dir, keep, replaced, now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if diff := cmp.Diff([]string{keep, other, latestFile, "tmp-fresh"}, got); diff != "" {
		t.Errorf("after cleaning (-want +got):\n%s", diff)
	}
}

func TestInstall(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	version := strings.Repeat("a", 40)
	var names []string
	for _, step := range []string{"first", "again"} {
		tmp, err := os.MkdirTemp(dir, tmpPrefix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, "config"), []byte(step), 0o600); err != nil {
			t.Fatal(err)
		}
		name, err := install(t.Context(), dir, tmp, version)
		if err != nil {
			t.Fatalf("install %s: %v", step, err)
		}
		if !cloneName.MatchString(name) || !strings.HasPrefix(name, version+"-") {
			t.Errorf("install named the clone %q", name)
		}
		data, err := os.ReadFile(filepath.Clean(filepath.Join(dir, name, "config")))
		if err != nil || string(data) != step {
			t.Errorf("after installing %s, the clone holds %q, %v", step, data, err)
		}
		names = append(names, name)
	}
	// Each clone of a commit gets a directory of its own, so installing
	// one never replaces another in place.
	if names[0] == names[1] {
		t.Errorf("both clones are named %s", names[0])
	}
	if _, err := os.Stat(filepath.Join(dir, names[0])); err != nil {
		t.Errorf("the first clone is gone: %v", err)
	}
}
