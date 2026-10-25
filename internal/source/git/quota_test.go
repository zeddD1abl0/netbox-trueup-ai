package git

import (
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/osfs"
)

func TestQuota(t *testing.T) {
	t.Parallel()
	q := &quota{limit: 10, maxObjects: 100}
	fsys := quotaFS{Filesystem: osfs.New(t.TempDir()), q: q}
	f, err := fsys.Create("a")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write([]byte("12345678")); err != nil {
		t.Fatalf("a write within the limit: %v", err)
	}
	if _, err := f.Write([]byte("123")); !errors.Is(err, errTooLarge) {
		t.Errorf("a write past the limit = %v, want errTooLarge", err)
	}
	if _, err := f.WriteAt([]byte("1"), 0); !errors.Is(err, errTooLarge) {
		t.Errorf("a write after the limit = %v, want errTooLarge", err)
	}
	if got := q.bytes(); got != 8 {
		t.Errorf("written = %d, want 8", got)
	}
	if !errors.Is(q.err(), errTooLarge) {
		t.Errorf("err = %v, want errTooLarge", q.err())
	}

	// Files in a chrooted directory, or opened for writing, count too.
	q = &quota{limit: 4, maxObjects: 100}
	fsys = quotaFS{Filesystem: osfs.New(t.TempDir()), q: q}
	if err := fsys.MkdirAll("sub", 0o700); err != nil {
		t.Fatal(err)
	}
	sub, err := fsys.Chroot("sub")
	if err != nil {
		t.Fatal(err)
	}
	g, err := sub.OpenFile("b", os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	if _, err := g.Write([]byte("12345")); !errors.Is(err, errTooLarge) {
		t.Errorf("a write through a chroot past the limit = %v, want errTooLarge", err)
	}
}

func TestQuotaPassesOnWhatGoGitLooksFor(t *testing.T) {
	t.Parallel()
	base := osfs.New(t.TempDir())
	fsys := quotaFS{Filesystem: base, q: &quota{limit: 1 << 20}}
	if got, want := fsys.Capabilities(), billy.Capabilities(base); got != want {
		t.Errorf("Capabilities = %v, want the base filesystem's %v", got, want)
	}
	f, err := fsys.Create("ref")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := fsys.Chmod("ref", 0o444); err != nil {
		t.Errorf("Chmod: %v", err)
	}
	locker, ok := f.(billy.Locker)
	if !ok {
		t.Fatal("files can't be locked")
	}
	if err := locker.Lock(); err != nil {
		t.Errorf("Lock: %v", err)
	}
	if err := locker.Unlock(); err != nil {
		t.Errorf("Unlock: %v", err)
	}
}

func TestQuotaChecksThePackHeader(t *testing.T) {
	t.Parallel()
	header := func(objects uint32) []byte {
		b := append([]byte("PACK"), 0, 0, 0, 2)
		return binary.BigEndian.AppendUint32(b, objects)
	}
	tests := []struct {
		name    string
		prefix  string // of the temporary file
		writes  [][]byte
		wantErr bool
	}{
		{name: "few objects", prefix: "tmp_pack_", writes: [][]byte{header(3)}},
		{name: "too many objects", prefix: "tmp_pack_", writes: [][]byte{header(4)}, wantErr: true},
		{name: "too many, written a byte at a time", prefix: "tmp_pack_", writes: splitBytes(header(4)), wantErr: true},
		{name: "not a pack", prefix: "tmp_pack_", writes: [][]byte{[]byte("not a pack at all")}},
		{name: "not the pack file", prefix: "tmp_obj_", writes: [][]byte{header(4)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			q := &quota{limit: 1 << 20, maxObjects: 3}
			fsys := quotaFS{Filesystem: osfs.New(t.TempDir()), q: q}
			f, err := fsys.TempFile("", tt.prefix)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			for _, w := range tt.writes {
				if _, err = f.Write(w); err != nil {
					break
				}
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("write error = %v, want an error: %v", err, tt.wantErr)
			}
			if tt.wantErr && (q.err() == nil || !strings.Contains(q.err().Error(), "the server sent 4 objects, more than the 3")) {
				t.Errorf("quota error = %v", q.err())
			}
		})
	}
}

func splitBytes(b []byte) [][]byte {
	out := make([][]byte, len(b))
	for i := range b {
		out[i] = b[i : i+1]
	}
	return out
}
