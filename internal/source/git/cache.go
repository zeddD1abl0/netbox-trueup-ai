package git

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A source's cache directory holds:
//
//	latest.json     which clone is the cached version, and how it was made
//	<commit>-<n>/   a clone, named for its commit, and unique through n
//	tmp-<n>/        a clone being made, renamed once it is complete
//
// A fetch makes a new clone beside the one latest.json names, and replaces
// latest.json only once its snapshot has passed every check. Unique names
// mean that a clone is never replaced in place, and that fetches running at
// once never touch each other's clones.
const (
	latestFile = "latest.json"
	tmpPrefix  = "tmp-"
	// maxLatestSize bounds latest.json, which is a few hundred bytes.
	maxLatestSize = 64 << 10
	// staleAfter is when a clone that latest.json doesn't name is taken to
	// be left behind by a fetch that crashed, rather than one still running
	// or not yet committed: twice the longest fetch_timeout.
	staleAfter = 4 * time.Hour
)

// cloneName matches the name of a clone's directory: its commit's hash, a
// hyphen and a number.
var cloneName = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})-[0-9]+$`)

// latest says which clone in a source's cache is the cached version, and
// how it was made, so that a later fetch or open can tell whether it still
// fits the source's settings.
type latest struct {
	// Dir is the clone's directory in the source's cache.
	Dir string `json:"dir"`
	// Version is the commit the clone holds.
	Version string `json:"version"`
	// URL is the repository's URL.
	URL string `json:"url"`
	// Requested is the source's ref setting: a branch or tag, or "" for
	// the default branch.
	Requested string `json:"requested"`
	// Ref is the full name of the ref fetched, such as refs/heads/master,
	// and Advertised the hash the clone holds for it: the commit, or for an
	// annotated tag the tag object.
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
func readLatest(ctx context.Context, dir string) (latest, error) {
	var l latest
	if err := ctx.Err(); err != nil {
		return l, err
	}
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
	// Dir names a directory, so it must be nothing but a clone's name.
	if !isHash(l.Version) || !cloneName.MatchString(l.Dir) || !strings.HasPrefix(l.Dir, l.Version+"-") {
		return l, fmt.Errorf("%s names no clone", latestFile)
	}
	return l, nil
}

// writeLatest replaces a source's latest.json in one step, so that a reader
// sees the old file or the new one, never part of one.
func writeLatest(ctx context.Context, dir string, l latest) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
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

// install renames a finished clone from tmp, a directory os.MkdirTemp made
// in the source's cache, to a name of its own, and returns that name.
func install(ctx context.Context, dir, tmp, version string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name := version + "-" + strings.TrimPrefix(filepath.Base(tmp), tmpPrefix)
	if !cloneName.MatchString(name) {
		return "", fmt.Errorf("can't name the clone of %s: %s", version, name)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return "", fmt.Errorf("install the clone of %s: %w", version, err)
	}
	return name, nil
}

// clean removes, from a source's cache, the clone that keep replaced, and
// anything else but latest.json and keep that is stale: left by a fetch that
// crashed. It leaves newer clones alone, since another fetch may be making
// or reading them. It logs what it can't remove rather than failing:
// leftovers cost only disk.
func clean(ctx context.Context, dir, keep, replaced string, now time.Time, log *slog.Logger) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Warn("can't clean the source cache", "dir", dir, "error", err)
		return
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return // cleaning can wait for the next fetch
		}
		name := e.Name()
		if name == latestFile || name == keep {
			continue
		}
		if name != replaced {
			info, err := e.Info()
			switch {
			case errors.Is(err, os.ErrNotExist):
				continue // gone already
			case err != nil:
				log.Warn("can't tell the age of an entry in the source cache, so leaving it", "path", filepath.Join(dir, name), "error", err)
				continue
			case now.Sub(info.ModTime()) < staleAfter:
				continue // perhaps another fetch's
			}
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			log.Warn("can't remove an old clone from the source cache", "path", filepath.Join(dir, name), "error", err)
		}
	}
}
