package git

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"

	"github.com/go-git/go-billy/v6"
)

// errTooLarge means a fetch tried to write more than its source's
// max_fetch_size.
var errTooLarge = errors.New("the fetch wrote more than max_fetch_size")

// quota counts the bytes a fetch writes to disk, and refuses writes past its
// limit. It also refuses a pack that announces more objects than maxObjects,
// before go-git indexes them: memory and the index grow with the number of
// objects, which a small pack of tiny objects can make huge.
type quota struct {
	limit      int64
	maxObjects uint32

	mu       sync.Mutex
	written  int64
	exceeded bool
	objects  uint32 // the objects a refused pack announced, or 0
}

// take counts n bytes about to be written.
func (q *quota) take(n int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.exceeded || q.written+int64(n) > q.limit {
		q.exceeded = true
		return errTooLarge
	}
	q.written += int64(n)
	return nil
}

// refuse records a pack refused for announcing n objects.
func (q *quota) refuse(n uint32) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.objects = n
	return q.errObjects()
}

func (q *quota) errObjects() error {
	return fmt.Errorf("the server sent %d objects, more than the %d a source may have", q.objects, q.maxObjects)
}

// err returns the error for the write the quota refused, or nil.
func (q *quota) err() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	switch {
	case q.objects != 0:
		return q.errObjects()
	case q.exceeded:
		return errTooLarge
	}
	return nil
}

// bytes returns how many bytes have been written.
func (q *quota) bytes() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.written
}

// quotaFS is a billy filesystem whose writes count against a quota. It
// passes on the optional interfaces go-git looks for, Chmod and Capable, so
// that go-git stores a repository as it would without it.
type quotaFS struct {
	billy.Filesystem
	q *quota
}

var (
	_ billy.Chmod   = quotaFS{}
	_ billy.Capable = quotaFS{}
)

func (fs quotaFS) Create(name string) (billy.File, error) {
	f, err := fs.Filesystem.Create(name)
	return fs.wrap(f, err, false)
}

func (fs quotaFS) OpenFile(name string, flag int, perm fs.FileMode) (billy.File, error) {
	f, err := fs.Filesystem.OpenFile(name, flag, perm)
	return fs.wrap(f, err, false)
}

func (fs quotaFS) TempFile(dir, prefix string) (billy.File, error) {
	f, err := fs.Filesystem.TempFile(dir, prefix)
	// go-git writes a fetched pack, as the server sends it, to a temporary
	// file named tmp_pack_* (newPackWrite in storage/filesystem/dotgit).
	return fs.wrap(f, err, strings.HasPrefix(prefix, "tmp_pack_"))
}

func (fs quotaFS) Chroot(path string) (billy.Filesystem, error) {
	sub, err := fs.Filesystem.Chroot(path)
	if err != nil {
		return nil, err
	}
	return quotaFS{Filesystem: sub, q: fs.q}, nil
}

func (fs quotaFS) Chmod(name string, mode fs.FileMode) error {
	if c, ok := fs.Filesystem.(billy.Chmod); ok {
		return c.Chmod(name, mode)
	}
	return billy.ErrNotSupported
}

func (fs quotaFS) Capabilities() billy.Capability {
	return billy.Capabilities(fs.Filesystem)
}

func (fs quotaFS) wrap(f billy.File, err error, pack bool) (billy.File, error) {
	if err != nil {
		return nil, err
	}
	qf := &quotaFile{File: f, q: fs.q}
	if pack {
		qf.header = make([]byte, 0, packHeaderLen)
	}
	return qf, nil
}

// packHeaderLen is the length of a pack's header: "PACK", the version and
// the number of objects, each a 4-byte big-endian number.
const packHeaderLen = 12

// quotaFile is a file whose writes count against a quota.
type quotaFile struct {
	billy.File
	q *quota
	// header collects the start of a pack, until it is checked; it is nil
	// for other files, and once the header has been checked.
	header []byte
}

// go-git locks reference files it writes, if the file can be locked.
var _ billy.Locker = (*quotaFile)(nil)

func (f *quotaFile) Write(p []byte) (int, error) {
	if err := f.check(p); err != nil {
		return 0, err
	}
	if err := f.q.take(len(p)); err != nil {
		return 0, err
	}
	return f.File.Write(p)
}

func (f *quotaFile) WriteAt(p []byte, off int64) (int, error) {
	// Counting rewrites too can only overcount, which errs the safe way.
	if err := f.q.take(len(p)); err != nil {
		return 0, err
	}
	return f.File.WriteAt(p, off)
}

func (f *quotaFile) Lock() error {
	if l, ok := f.File.(billy.Locker); ok {
		return l.Lock()
	}
	return nil
}

func (f *quotaFile) Unlock() error {
	if l, ok := f.File.(billy.Locker); ok {
		return l.Unlock()
	}
	return nil
}

// check reads a pack's header from the first bytes written, and refuses a
// pack that announces more objects than the quota allows.
func (f *quotaFile) check(p []byte) error {
	if f.header == nil {
		return nil
	}
	need := min(packHeaderLen-len(f.header), len(p))
	f.header = append(f.header, p[:need]...)
	if len(f.header) < packHeaderLen {
		return nil
	}
	header := f.header
	f.header = nil
	if string(header[:4]) != "PACK" {
		return nil // not a pack after all: go-git's parser judges it
	}
	if n := binary.BigEndian.Uint32(header[8:12]); n > f.q.maxObjects {
		return f.q.refuse(n)
	}
	return nil
}
