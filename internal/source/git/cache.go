package git

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A source's cache directory holds:
//
//	latest.json  which clone is the latest, and how it was made
//	<commit>/    a bare clone, named for the commit it holds
//	tmp-*/       a clone in progress, renamed to <commit>/ when done
const (
	latestFile = "latest.json"
	tmpPrefix  = "tmp-"
	// maxLatestSize bounds latest.json, which is a few hundred bytes.
	maxLatestSize = 64 << 10
	// staleAfter is when a clone in progress is taken to be abandoned, by a
	// fetch that crashed: twice the longest fetch_timeout.
	staleAfter = 4 * time.Hour
)

// latest says which clone in a source's cache is the latest, and how it was
// made, so that a later fetch or open can tell whether it still fits the
// source's settings.
type latest struct {
	// Version is the commit the clone holds, which names its directory.
	Version string `json:"version"`
	// URL is the repository's URL.
	URL string `json:"url"`
	// Requested is the source's ref setting: a branch or tag, or "" for
	// the default branch.
	Requested string `json:"requested"`
	// Ref is the full name of the ref fetched, such as refs/heads/master,
	// and Advertised the hash the repository gave it: the commit, or for
	// an annotated tag the tag object.
	Ref        string `json:"ref"`
	Advertised string `json:"advertised"`
	// Filter is the largest file the clone holds, if the fetch left larger
	// ones out, or 0 if it fetched every file.
	Filter int64 `json:"filter"`
}

// covers reports whether a clone filtered at l.Filter holds every file a
// source with the given max_file_size may read.
func (l latest) covers(maxFileSize int64) bool {
	return l.Filter == 0 || l.Filter >= maxFileSize
}

// readLatest reads a source's latest.json. It returns an error wrapping
// fs.ErrNotExist if there is none.
func readLatest(dir string) (latest, error) {
	var l latest
	// security: dir is the source's cache, below data_dir, which the
	// operator configures (B5 in docs/architecture.md); nothing from a
	// repository names it.
	f, err := os.Open(filepath.Clean(filepath.Join(dir, latestFile)))
	if err != nil {
		return l, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxLatestSize+1))
	if err != nil {
		return l, fmt.Errorf("read %s: %w", latestFile, err)
	}
	if len(data) > maxLatestSize {
		return l, fmt.Errorf("%s is larger than %d bytes", latestFile, maxLatestSize)
	}
	if err := json.Unmarshal(data, &l); err != nil {
		return l, fmt.Errorf("read %s: %w", latestFile, err)
	}
	// The version names a directory, so it must be nothing but a hash.
	if !isHash(l.Version) {
		return l, fmt.Errorf("%s names no commit", latestFile)
	}
	return l, nil
}

// writeLatest replaces a source's latest.json in one step, so that a reader
// sees the old file or the new one, never part of one.
func writeLatest(dir string, l latest) (err error) {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("write %s: %w", latestFile, err)
	}
	f, err := os.CreateTemp(dir, tmpPrefix+latestFile+"-")
	if err != nil {
		return fmt.Errorf("write %s: %w", latestFile, err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", latestFile, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("write %s: %w", latestFile, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", latestFile, err)
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, latestFile)); err != nil {
		return fmt.Errorf("write %s: %w", latestFile, err)
	}
	return nil
}

// isHash reports whether s is a SHA-1 or SHA-256 object hash in lowercase
// hexadecimal, as go-git writes them.
func isHash(s string) bool {
	if len(s) != 40 && len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// install moves a finished clone from tmp to the directory named for its
// commit, replacing any clone of the same commit, which can only be one that
// no longer fits the source's settings, or one a concurrent fetch made.
func install(dir, tmp, version string) error {
	final := filepath.Join(dir, version)
	if err := os.RemoveAll(final); err != nil {
		return fmt.Errorf("replace the cached clone of %s: %w", version, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		if _, statErr := os.Stat(final); statErr == nil {
			// A concurrent fetch installed the same commit first.
			return os.RemoveAll(tmp)
		}
		return fmt.Errorf("install the clone of %s: %w", version, err)
	}
	return nil
}

// clean removes everything in a source's cache but latest.json and the
// clone it names, leaving clones in progress unless they are stale. It logs
// what it can't remove rather than failing: leftovers cost only disk.
func clean(dir, keep string, now time.Time, log *slog.Logger) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Warn("can't clean the source cache", "dir", dir, "error", err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		if name == latestFile || name == keep {
			continue
		}
		if strings.HasPrefix(name, tmpPrefix) {
			info, err := e.Info()
			if errors.Is(err, fs.ErrNotExist) || err == nil && now.Sub(info.ModTime()) < staleAfter {
				continue // gone already, or perhaps another fetch's
			}
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			log.Warn("can't remove an old clone from the source cache", "path", filepath.Join(dir, name), "error", err)
		}
	}
}
