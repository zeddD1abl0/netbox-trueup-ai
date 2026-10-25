package git

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/storage"
)

// faultyStorage fails object lookups as a damaged disk could.
type faultyStorage struct {
	storage.Storer
	sizeErr, hasErr error
}

func (s faultyStorage) EncodedObjectSize(plumbing.Hash) (int64, error) { return 0, s.sizeErr }
func (s faultyStorage) HasEncodedObject(plumbing.Hash) error           { return s.hasErr }

// A blob reads as left out for being too large only when the store is sure
// it doesn't hold it: an error reading the store must never make a file
// quietly skipped.
func TestSizeOfAMissingBlob(t *testing.T) {
	t.Parallel()
	ioErr := &fs.PathError{Op: "read", Path: "objects/pack/pack-x.idx", Err: errors.New("input/output error")}
	tests := []struct {
		name     string
		sizeErr  error
		hasErr   error
		filtered bool
		want     int64
		wantErr  string
	}{
		{name: "left out by the filter", sizeErr: plumbing.ErrObjectNotFound, hasErr: plumbing.ErrObjectNotFound, filtered: true, want: -1},
		{name: "missing without a filter", sizeErr: plumbing.ErrObjectNotFound, hasErr: plumbing.ErrObjectNotFound, wantErr: "is missing from the clone"},
		{name: "there, but its size can't be read", sizeErr: plumbing.ErrObjectNotFound, filtered: true, wantErr: "is in the clone, but its size can't be read"},
		{name: "a lookup that fails", sizeErr: plumbing.ErrObjectNotFound, hasErr: ioErr, filtered: true, wantErr: "input/output error"},
		{name: "a size that can't be read", sizeErr: ioErr, filtered: true, wantErr: "input/output error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := newStorage(osfs.New(t.TempDir()))
			c := &content{
				repo:     &gogit.Repository{Storer: faultyStorage{Storer: base, sizeErr: tt.sizeErr, hasErr: tt.hasErr}},
				filtered: tt.filtered,
				cacheDir: "/cache/lib",
			}
			got, err := c.size(blobHash("x"))
			if tt.wantErr == "" {
				if err != nil || got != tt.want {
					t.Errorf("size = %d, %v; want %d", got, err, tt.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "delete /cache/lib to fetch the source again") {
				t.Errorf("size = %d, %v; want an error saying %q and how to recover", got, err, tt.wantErr)
			}
		})
	}
}
