package git

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/cgi" //nolint:gosec // G504: runs git http-backend in tests; httpoxy affected only Go before 1.6.3
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/osfs"
	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/backend"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
)

// testRepo is a bare repository on disk that a test builds with go-git, so
// that its trees can hold anything a hostile repository could.
type testRepo struct {
	t    *testing.T
	repo *gogit.Repository
}

// newRepo makes an empty bare repository root/name.git, whose default
// branch is main.
func newRepo(t *testing.T, root, name string) *testRepo {
	t.Helper()
	repo, err := gogit.PlainInit(filepath.Join(root, name+".git"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return &testRepo{t: t, repo: repo}
}

// entry is an entry of a tree to write.
type entry struct {
	name     string
	mode     filemode.FileMode
	content  string        // for a file, or a symbolic link's target
	children []entry       // for a directory
	hash     plumbing.Hash // for a gitlink, the commit it names
}

func file(name, content string) entry {
	return entry{name: name, mode: filemode.Regular, content: content}
}

func dir(name string, children ...entry) entry {
	return entry{name: name, mode: filemode.Dir, children: children}
}

func (r *testRepo) object(typ plumbing.ObjectType, data []byte) plumbing.Hash {
	r.t.Helper()
	obj := r.repo.Storer.NewEncodedObject()
	obj.SetType(typ)
	w, err := obj.Writer()
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		r.t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		r.t.Fatal(err)
	}
	h, err := r.repo.Storer.SetEncodedObject(obj)
	if err != nil {
		r.t.Fatal(err)
	}
	return h
}

// tree writes a tree object byte by byte, in Git's format, so that it can
// hold names go-git would refuse to write. Entries are sorted as Git sorts
// them, but duplicates stay.
func (r *testRepo) tree(entries ...entry) plumbing.Hash {
	r.t.Helper()
	sortKey := func(e entry) string {
		if e.mode == filemode.Dir {
			return e.name + "/"
		}
		return e.name
	}
	sort.SliceStable(entries, func(i, j int) bool { return sortKey(entries[i]) < sortKey(entries[j]) })
	var buf bytes.Buffer
	for _, e := range entries {
		var h plumbing.Hash
		switch e.mode {
		case filemode.Dir:
			h = r.tree(e.children...)
		case filemode.Submodule:
			h = e.hash
		default:
			h = r.object(plumbing.BlobObject, []byte(e.content))
		}
		fmt.Fprintf(&buf, "%o %s\x00", uint32(e.mode), e.name)
		buf.Write(h.Bytes())
	}
	return r.object(plumbing.TreeObject, buf.Bytes())
}

// commit commits a tree to a branch, on top of the branch's last commit.
func (r *testRepo) commit(branch string, entries ...entry) plumbing.Hash {
	r.t.Helper()
	sig := object.Signature{Name: "Test", Email: "test@example.com", When: time.Unix(1700000000, 0)}
	c := &object.Commit{Author: sig, Committer: sig, Message: "commit\n", TreeHash: r.tree(entries...)}
	name := plumbing.NewBranchReferenceName(branch)
	if ref, err := r.repo.Storer.Reference(name); err == nil {
		c.ParentHashes = []plumbing.Hash{ref.Hash()}
	}
	obj := r.repo.Storer.NewEncodedObject()
	if err := c.Encode(obj); err != nil {
		r.t.Fatal(err)
	}
	h, err := r.repo.Storer.SetEncodedObject(obj)
	if err != nil {
		r.t.Fatal(err)
	}
	r.setRef(name, h)
	return h
}

func (r *testRepo) setRef(name plumbing.ReferenceName, h plumbing.Hash) {
	r.t.Helper()
	if err := r.repo.Storer.SetReference(plumbing.NewHashReference(name, h)); err != nil {
		r.t.Fatal(err)
	}
}

// annotatedTag tags a commit with a tag object.
func (r *testRepo) annotatedTag(name string, target plumbing.Hash) plumbing.Hash {
	r.t.Helper()
	tag := &object.Tag{
		Name: name, Message: "tag\n", TargetType: plumbing.CommitObject, Target: target,
		Tagger: object.Signature{Name: "Test", Email: "test@example.com", When: time.Unix(1700000000, 0)},
	}
	obj := r.repo.Storer.NewEncodedObject()
	if err := tag.Encode(obj); err != nil {
		r.t.Fatal(err)
	}
	h, err := r.repo.Storer.SetEncodedObject(obj)
	if err != nil {
		r.t.Fatal(err)
	}
	r.setRef(plumbing.NewTagReferenceName(name), h)
	return h
}

// random returns n bytes that don't compress.
func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return string(b)
}

// recorder records the requests a server answers.
type recorder struct {
	mu       sync.Mutex
	requests []string
}

// wrap records each request as its method and path, and a POST to
// git-upload-pack as "ls-refs" or "fetch" too, from its body.
func (rec *recorder) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		what := r.Method + " " + r.URL.Path
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			plain := body
			if r.Header.Get("Content-Encoding") == "gzip" {
				if zr, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
					plain, _ = io.ReadAll(zr)
				}
			}
			if bytes.Contains(plain, []byte("command=ls-refs")) {
				what += " ls-refs"
			} else {
				what += " fetch"
			}
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, what)
		rec.mu.Unlock()
		h.ServeHTTP(w, r)
	})
}

func (rec *recorder) all() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return slices.Clone(rec.requests)
}

// fetches counts the requests that fetched objects.
func (rec *recorder) fetches() int {
	n := 0
	for _, r := range rec.all() {
		if strings.HasSuffix(r, " fetch") {
			n++
		}
	}
	return n
}

// server is a Git server over TLS for the repositories in root.
type server struct {
	*httptest.Server
	rec *recorder
}

func startServer(t *testing.T, h http.Handler) *server {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewUnstartedServer(rec.wrap(h))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // handshakes that fail on purpose
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return &server{Server: srv, rec: rec}
}

// goGitServer serves root's repositories with go-git's own server, which
// doesn't offer partial clone.
func goGitServer(t *testing.T, root string) *server {
	t.Helper()
	return startServer(t, backend.New(transport.NewFilesystemLoader(osfs.New(root), false)))
}

// gitServer serves root's repositories with Git's own http-backend, run as
// a CGI program, with partial clone allowed as GitHub allows it. It skips
// the test if git isn't installed.
func gitServer(t *testing.T, root string) *server {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git isn't installed, and this test needs Git's own server")
	}
	return startServer(t, &cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
			"HOME=" + t.TempDir(),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=uploadpack.allowFilter",
			"GIT_CONFIG_VALUE_0=true",
		},
		Stderr: io.Discard,
	})
}

func (s *server) pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())
	return pool
}

// logs collects log records as text.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// newKind returns a Git kind that trusts srv, with a cache of its own, and
// the logs it writes.
func newKind(t *testing.T, srv *server) (*Kind, *logs) {
	t.Helper()
	l := &logs{}
	opts := Options{CacheDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(l, nil))}
	if srv != nil {
		opts.RootCAs = srv.pool()
	}
	return New(opts), l
}

var testLimits = config.SourceLimits{MaxFileSize: 1024, MaxFiles: 100, FetchTimeout: time.Minute, MaxFetchSize: 64 << 20}

// testSpec returns the spec of a source for the repository name on srv.
func testSpec(srv *server, name, ref string) source.Spec {
	return source.Spec{Name: "lib", Kind: config.SourceKindGit, URL: srv.URL + "/" + name + ".git", Ref: ref, Limits: testLimits}
}

// proxy is an HTTP proxy that tunnels CONNECT requests, and records the
// addresses it tunnels to.
type proxy struct {
	*httptest.Server
	mu      sync.Mutex
	tunnels []string
}

func startProxy(t *testing.T) *proxy {
	t.Helper()
	p := &proxy{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "only CONNECT", http.StatusMethodNotAllowed)
			return
		}
		p.mu.Lock()
		p.tunnels = append(p.tunnels, r.Host)
		p.mu.Unlock()
		dst, err := (&net.Dialer{}).DialContext(r.Context(), "tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			_ = dst.Close()
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		go func() { _, _ = io.Copy(dst, conn); _ = dst.Close() }()
		go func() { _, _ = io.Copy(conn, dst); _ = conn.Close() }()
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *proxy) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.tunnels)
}
