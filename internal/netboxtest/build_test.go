package netboxtest

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
)

func TestWriteContext(t *testing.T) {
	t.Parallel()
	contexts := fstest.MapFS{
		"web/Dockerfile":    {Data: []byte("FROM x\n")},
		"web/sub/config.py": {Data: []byte("A = 1\n")},
		"other/Dockerfile":  {Data: []byte("FROM y\n")},
	}
	dir, tag, err := writeContext(t.Context(), contexts, "web", "")
	if err != nil {
		t.Fatalf("writeContext: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if diff := cmp.Diff(map[string]string{"Dockerfile": "FROM x\n", "sub/config.py": "A = 1\n"}, readFiles(t, os.DirFS(dir))); diff != "" {
		t.Errorf("written files (-want +got):\n%s", diff)
	}

	dir2, tag2, err := writeContext(t.Context(), contexts, "web", "")
	if err != nil || tag != tag2 || len(tag) != 12 || dir2 == dir {
		t.Errorf("writeContext again: dir %q, tag %q, %v; want a new directory and the tag %q", dir2, tag2, err, tag)
	}
	_ = os.RemoveAll(dir2)
	for name, change := range map[string]func(fstest.MapFS){
		"a file's content": func(m fstest.MapFS) { m["web/sub/config.py"] = &fstest.MapFile{Data: []byte("A = 2\n")} },
		"a new file":       func(m fstest.MapFS) { m["web/sub2/config.py"] = &fstest.MapFile{} },
		"a file's name":    func(m fstest.MapFS) { m["web/sub/conf.py"] = m["web/sub/config.py"]; delete(m, "web/sub/config.py") },
	} {
		changed := fstest.MapFS{}
		for k, v := range contexts {
			changed[k] = v
		}
		change(changed)
		d, other, err := writeContext(t.Context(), changed, "web", "")
		_ = os.RemoveAll(d)
		if err != nil || other == tag {
			t.Errorf("writeContext's tag = %q, %v after changing %s; want a tag other than %q", other, err, name, tag)
		}
	}
	if _, _, err := writeContext(t.Context(), contexts, "missing", ""); err == nil {
		t.Error("writeContext of a missing context succeeded")
	}
	// A failure after the directory exists removes it.
	root := t.TempDir()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := writeContext(canceled, contexts, "web", root); !errors.Is(err, context.Canceled) {
		t.Errorf("writeContext with a canceled context = %v, want it canceled", err)
	}
	if left, err := os.ReadDir(root); err != nil || len(left) != 0 {
		t.Errorf("writeContext left %v behind in its root, %v", left, err)
	}
}

func TestTailWriter(t *testing.T) {
	t.Parallel()
	w := &tailWriter{keep: 2}
	if got := w.report(); got != "\n(the build wrote no log)" {
		t.Errorf("empty report = %q", got)
	}
	for _, chunk := range []string{"one\ntw", "o\nthree\n", "four"} {
		if n, err := w.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if got, want := w.report(), "\nThe end of its build log:\n  | three\n  | four"; got != want {
		t.Errorf("report = %q, want %q", got, want)
	}
	if _, _ = w.Write([]byte("\n")); !strings.HasSuffix(w.report(), "  | three\n  | four") {
		t.Errorf("report after the last line ended = %q", w.report())
	}
}

func TestTailWriterClipsLongLines(t *testing.T) {
	t.Parallel()
	w := &tailWriter{keep: 2}
	long := strings.Repeat("x", 10*maxLine)
	_, _ = w.Write([]byte(long + "\n" + long))
	if got := w.report(); len(got) > 3*maxLine || !strings.Contains(got, "  | …xxx") {
		t.Errorf("report of two %d-byte lines is %d bytes, want each clipped to its last %d", len(long), len(got), maxLine)
	}
	if len(w.partial) > maxLine+len("…") {
		t.Errorf("tailWriter holds %d bytes of an unfinished line, want at most %d", len(w.partial), maxLine)
	}
}

func TestClipKeepsWholeCharacters(t *testing.T) {
	t.Parallel()
	// Three bytes each, so the cut lands inside one.
	got := clip(strings.Repeat("…", maxLine))
	if !utf8.ValidString(got) || !strings.HasPrefix(got, "……") || len(got) > maxLine+len("…") {
		t.Errorf("clip = %d bytes, valid UTF-8 %v, starting %q", len(got), utf8.ValidString(got), got[:min(len(got), 9)])
	}
}

func TestQuoteLines(t *testing.T) {
	t.Parallel()
	got := quoteLines([]string{"::add-mask::secret", "\x1b[31mred\x1b[0m\tok", "a\rb\u0085c\x7f", ""})
	want := "  | ::add-mask::secret\n  | [31mred[0m\tok\n  | abc\n  | "
	if got != want {
		t.Errorf("quoteLines = %q, want %q", got, want)
	}
}
