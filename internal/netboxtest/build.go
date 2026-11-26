package netboxtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// builtImageRepo prefixes the name of each image netboxtest builds, followed
// by the build context's name, such as trueup-netbox-branching. Compose names
// the images it builds after the project and service instead.
const builtImageRepo = "trueup-netbox-"

// writeContext writes a build context's files into a new temporary
// directory under root, or the system's if root is empty, for Docker to build
// from, and returns the directory, which the caller removes, and a tag named
// for the files' content: the first 12 hex digits of a SHA-256 over each
// file's path and content. If it fails, it removes the directory itself.
//
// testcontainers-go archives a directory afresh for each attempt at a build,
// and it tries a failed one again; an archive it is handed would be read
// from where the failed attempt left it. Docker's build cache looks at the
// files' content, not their times, so it holds across directories.
func writeContext(ctx context.Context, contexts fs.FS, context, root string) (dir, tag string, err error) {
	src, err := fs.Sub(contexts, context)
	if err != nil {
		return "", "", fmt.Errorf("build context %s: %w", context, err)
	}
	tmp, err := os.MkdirTemp(root, "netboxtest-build-")
	if err != nil {
		return "", "", fmt.Errorf("build context %s: %w", context, err)
	}
	// tmp, not dir: a failed return has already emptied dir.
	defer func() {
		if err != nil {
			_ = os.RemoveAll(tmp)
		}
	}()
	sum := sha256.New()
	err = fs.WalkDir(src, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(tmp, filepath.FromSlash(name))
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		data, err := fs.ReadFile(src, name)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(sum, "%s\x00%d\x00", name, len(data))
		_, _ = sum.Write(data)
		return os.WriteFile(target, data, 0o644) //nolint:gosec // G306: Docker reads these files, as Compose's build would.
	})
	if err != nil {
		return "", "", fmt.Errorf("build context %s: %w", context, err)
	}
	return tmp, hex.EncodeToString(sum.Sum(nil))[:12], nil
}

// tailWriter keeps the last lines written to it, such as the end of an image
// build's log, to explain a failed build.
type tailWriter struct {
	keep int

	mu      sync.Mutex
	lines   []string
	partial string
}

// maxLine is how much of a line tailWriter keeps, from its end, so that output
// without newlines, such as a progress bar's, can't fill memory or an error.
const maxLine = 2048

// clip returns the end of a line, at most maxLine bytes of it, starting at a
// whole UTF-8 character.
func clip(line string) string {
	if len(line) <= maxLine {
		return line
	}
	end := line[len(line)-maxLine:]
	for i := 0; i < len(end) && i < utf8.UTFMax; i++ {
		if utf8.RuneStart(end[i]) {
			return "…" + end[i:]
		}
	}
	return "…" + end
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	text := w.partial + string(p)
	lines := strings.Split(text, "\n")
	w.partial = clip(lines[len(lines)-1])
	for _, line := range lines[:len(lines)-1] {
		w.lines = append(w.lines, clip(line))
		if len(w.lines) > w.keep {
			w.lines = w.lines[1:]
		}
	}
	return len(p), nil
}

// report returns the end of the log, to append to an error.
func (w *tailWriter) report() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	lines := w.lines
	if w.partial != "" {
		lines = append(slices.Clone(lines), w.partial)
	}
	if len(lines) == 0 {
		return "\n(the build wrote no log)"
	}
	if len(lines) > w.keep {
		lines = lines[len(lines)-w.keep:]
	}
	return "\nThe end of its build log:\n" + quoteLines(lines)
}

// quoteLines sets lines from a container or a build apart from netboxtest's
// own text: each is indented, so that none starts a line of go test's output,
// where a CI service may read it as a command (GitHub Actions' ::add-mask::,
// say), and control characters, such as a terminal's escapes, are dropped.
// What NetBox and its plugins log isn't trusted.
func quoteLines(lines []string) string {
	quoted := make([]string, len(lines))
	for i, line := range lines {
		quoted[i] = "  | " + strings.Map(func(r rune) rune {
			if r == '\t' || (r >= 0x20 && r != 0x7f && (r < 0x80 || r > 0x9f)) {
				return r
			}
			return -1
		}, line)
	}
	return strings.Join(quoted, "\n")
}
