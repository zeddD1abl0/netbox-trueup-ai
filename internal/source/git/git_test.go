package git

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/osfs"
	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	formatcfg "github.com/go-git/go-git/v6/plumbing/format/config"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage/filesystem"
	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
)

// servers runs a test against go-git's own server and Git's.
var servers = []struct {
	name  string
	start func(*testing.T, string) *server
}{
	{"go-git", goGitServer},
	{"git", gitServer},
}

// fetch fetches spec with k and returns the snapshot, closed when the test
// ends.
func fetch(t *testing.T, k *Kind, spec source.Spec) *source.Snapshot {
	t.Helper()
	snap, err := source.Fetch(t.Context(), k, spec)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	t.Cleanup(func() { _ = snap.Close() })
	return snap
}

func paths(snap *source.Snapshot) []string {
	var out []string
	for _, f := range snap.Files {
		out = append(out, f.Path)
	}
	return out
}

// blobHash returns the ID Git gives a blob with the given content.
func blobHash(content string) plumbing.Hash {
	h := plumbing.NewHasher(formatcfg.SHA1, plumbing.BlobObject, int64(len(content)))
	_, _ = h.Write([]byte(content))
	return h.Sum()
}

// cached returns the source's latest.json, and the other entries its cache
// holds.
func cached(t *testing.T, k *Kind) (latest, []string) {
	t.Helper()
	dir := source.CacheDir(k.cacheDir, "lib")
	l, err := readLatest(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.Name() != latestFile {
			names = append(names, e.Name())
		}
	}
	return l, names
}

// cachedClone checks that the source's cache holds one clone, of version,
// and returns its directory.
func cachedClone(t *testing.T, k *Kind, version plumbing.Hash) string {
	t.Helper()
	l, others := cached(t, k)
	if l.Version != version.String() || !cmp.Equal(others, []string{l.Dir}) {
		t.Errorf("the cache holds %v, with latest.json naming %s at %s; want only a clone of %s", others, l.Dir, l.Version, version)
	}
	return l.Dir
}

func TestFetch(t *testing.T) {
	t.Parallel()
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			repo := newRepo(t, root, "lib")
			first := repo.commit("main",
				dir("device-types",
					dir("Juniper", file("MX204.yaml", "model: MX204")),
					dir("Cisco", file("C9300.yaml", "model: C9300")),
				),
				file("exactly-the-limit.yaml", random(1024)),
				file("over-the-limit.png", random(1025)),
				entry{name: "run.sh", mode: filemode.Executable, content: "#!/bin/sh\n"},
				entry{name: "latest.yaml", mode: filemode.Symlink, content: "device-types/Juniper/MX204.yaml"},
				entry{name: "vendor", mode: filemode.Submodule, hash: plumbing.NewHash("0123456789abcdef0123456789abcdef01234567")},
			)
			srv := s.start(t, root)
			k, logs := newKind(t, srv)
			spec := testSpec(srv, "lib", "")

			snap := fetch(t, k, spec)
			if snap.Version != first.String() || snap.Ref != "main" {
				t.Errorf("snapshot is %s from %s, want %s from main", snap.Version, snap.Ref, first)
			}
			wantFiles := []string{"device-types/Cisco/C9300.yaml", "device-types/Juniper/MX204.yaml", "exactly-the-limit.yaml", "run.sh"}
			if diff := cmp.Diff(wantFiles, paths(snap)); diff != "" {
				t.Errorf("files (-want +got):\n%s", diff)
			}
			wantSkipped := []source.Skipped{
				{Path: "latest.yaml", Reason: source.Symlink},
				{Path: "over-the-limit.png", Reason: source.TooLarge},
				{Path: "vendor", Reason: source.Submodule},
			}
			if diff := cmp.Diff(wantSkipped, snap.Skipped); diff != "" {
				t.Errorf("skipped (-want +got):\n%s", diff)
			}
			data, err := snap.ReadFile(t.Context(), "device-types/Juniper/MX204.yaml")
			if err != nil || string(data) != "model: MX204" {
				t.Errorf("ReadFile = %q, %v", data, err)
			}
			firstClone := cachedClone(t, k, first)

			// Nothing changed upstream, so nothing is downloaded.
			fetches := srv.rec.fetches()
			snap = fetch(t, k, spec)
			if snap.Version != first.String() || srv.rec.fetches() != fetches {
				t.Errorf("fetching again gave %s after %d downloads, want %s after none", snap.Version, srv.rec.fetches()-fetches, first)
			}
			if got := cachedClone(t, k, first); got != firstClone {
				t.Errorf("fetching again replaced clone %s with %s", firstClone, got)
			}
			if !strings.Contains(logs.String(), "source is unchanged") {
				t.Errorf("logs don't say the source is unchanged:\n%s", logs)
			}

			// A new commit replaces the old one in the cache.
			second := repo.commit("main", file("only.yaml", "new"))
			snap = fetch(t, k, spec)
			if snap.Version != second.String() || !cmp.Equal(paths(snap), []string{"only.yaml"}) {
				t.Errorf("after a new commit, the snapshot is %s with %v, want %s with only.yaml", snap.Version, paths(snap), second)
			}
			cachedClone(t, k, second)
		})
	}
}

// Git's server leaves out files over max_file_size; go-git's can't, so every
// file comes down, and those too large are skipped all the same.
func TestFetchFilter(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		server       string
		start        func(*testing.T, string) *server
		wantFilter   int64
		wantInClone  bool
		wantFallback bool
	}{
		{server: "git", start: gitServer, wantFilter: 1024},
		{server: "go-git", start: goGitServer, wantFilter: 0, wantInClone: true, wantFallback: true},
	} {
		t.Run(tt.server, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			repo := newRepo(t, root, "lib")
			big := random(4096)
			repo.commit("main", file("small.yaml", "x"), file("big.png", big))
			srv := tt.start(t, root)
			k, logs := newKind(t, srv)
			snap := fetch(t, k, testSpec(srv, "lib", ""))

			if diff := cmp.Diff([]source.Skipped{{Path: "big.png", Reason: source.TooLarge}}, snap.Skipped); diff != "" {
				t.Errorf("skipped (-want +got):\n%s", diff)
			}
			l, _ := cached(t, k)
			if l.Filter != tt.wantFilter {
				t.Errorf("the clone's filter = %d, want %d", l.Filter, tt.wantFilter)
			}
			st := filesystem.NewStorage(osfs.New(filepath.Join(source.CacheDir(k.cacheDir, "lib"), l.Dir)), cache.NewObjectLRUDefault())
			defer func() { _ = st.Close() }()
			_, err := st.EncodedObjectSize(blobHash(big))
			if inClone := err == nil; inClone != tt.wantInClone {
				t.Errorf("the large file is in the clone: %v, want %v (%v)", inClone, tt.wantInClone, err)
			}
			if fallback := strings.Contains(logs.String(), "can't leave out large files"); fallback != tt.wantFallback {
				t.Errorf("logged the fallback: %v, want %v:\n%s", fallback, tt.wantFallback, logs)
			}

			// Raising max_file_size needs the files left out, so the next
			// fetch clones again, and until then the cache can't be opened.
			spec := testSpec(srv, "lib", "")
			spec.Limits.MaxFileSize = 8192
			if _, err := k.Open(t.Context(), spec); tt.wantFilter != 0 && !errors.Is(err, source.ErrNotFetched) {
				t.Errorf("Open with a larger max_file_size = %v, want ErrNotFetched", err)
			}
			fetches := srv.rec.fetches()
			snap = fetch(t, k, spec)
			if !cmp.Equal(paths(snap), []string{"big.png", "small.yaml"}) {
				t.Errorf("files = %v, want both", paths(snap))
			}
			if wantFetch := tt.wantFilter != 0; (srv.rec.fetches() > fetches) != wantFetch {
				t.Errorf("fetched again: %v, want %v", srv.rec.fetches() > fetches, wantFetch)
			}
		})
	}
}

func TestFetchRefs(t *testing.T) {
	t.Parallel()
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			repo := newRepo(t, root, "lib")
			mainCommit := repo.commit("main", file("a.yaml", "main"))
			devCommit := repo.commit("dev", file("a.yaml", "dev"))
			repo.setRef(plumbing.NewTagReferenceName("v1"), mainCommit)
			repo.annotatedTag("v2", devCommit)
			// A branch and a tag of the same name: the branch wins, as in
			// git clone --branch.
			repo.setRef(plumbing.NewTagReferenceName("dev"), mainCommit)
			srv := s.start(t, root)

			for _, tt := range []struct {
				ref, wantVersion, wantRef string
			}{
				{ref: "", wantVersion: mainCommit.String(), wantRef: "main"},
				{ref: "dev", wantVersion: devCommit.String(), wantRef: "dev"},
				{ref: "v1", wantVersion: mainCommit.String(), wantRef: "v1"},
				{ref: "v2", wantVersion: devCommit.String(), wantRef: "v2"},
			} {
				k, _ := newKind(t, srv)
				snap := fetch(t, k, testSpec(srv, "lib", tt.ref))
				if snap.Version != tt.wantVersion || snap.Ref != tt.wantRef {
					t.Errorf("ref %q: got %s from %s, want %s from %s", tt.ref, snap.Version, snap.Ref, tt.wantVersion, tt.wantRef)
				}
			}

			k, _ := newKind(t, srv)
			_, err := source.Fetch(t.Context(), k, testSpec(srv, "lib", "nope"))
			if err == nil || !strings.Contains(err.Error(), `the repository has no branch or tag named "nope"`) {
				t.Errorf("Fetch of a missing ref = %v", err)
			}
		})
	}
}

func TestPickRef(t *testing.T) {
	t.Parallel()
	h := plumbing.NewHash("0123456789abcdef0123456789abcdef01234567")
	main := plumbing.NewHashReference("refs/heads/main", h)
	tests := []struct {
		name    string
		refs    []*plumbing.Reference
		want    string
		wantRef plumbing.ReferenceName
		wantErr string
	}{
		{name: "default branch", refs: []*plumbing.Reference{plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main"), main}, wantRef: "refs/heads/main"},
		{name: "no HEAD", refs: []*plumbing.Reference{main}, wantErr: "doesn't say which branch is its default"},
		{name: "a detached HEAD", refs: []*plumbing.Reference{plumbing.NewHashReference(plumbing.HEAD, h), main}, wantErr: "doesn't say which branch is its default"},
		{name: "HEAD on a tag", refs: []*plumbing.Reference{plumbing.NewSymbolicReference(plumbing.HEAD, "refs/tags/v1")}, wantErr: "doesn't say which branch is its default"},
		{name: "HEAD on a bad name", refs: []*plumbing.Reference{plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/../x")}, wantErr: "doesn't say which branch is its default"},
		{name: "an empty default branch", refs: []*plumbing.Reference{plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")}, wantErr: "default branch, main, has no commits"},
		{name: "a zero hash", refs: []*plumbing.Reference{plumbing.NewHashReference("refs/heads/x", plumbing.ZeroHash)}, want: "x", wantErr: `no branch or tag named "x"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			name, _, err := pickRef(tt.refs, tt.want)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("pickRef error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || name != tt.wantRef {
				t.Errorf("pickRef = %s, %v; want %s", name, err, tt.wantRef)
			}
		})
	}
}

// Hostile names reach the source layer, which skips them, and nothing below
// them is read.
func TestFetchHostileTree(t *testing.T) {
	t.Parallel()
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			repo := newRepo(t, root, "lib")
			repo.commit("main",
				dir("..", file("passwd", "x")),
				dir(".git", file("config", "x")),
				file("esc\x1b[2J.yaml", "x"),
				file("twice.yaml", "1"),
				file("twice.yaml", "2"),
				file("ok.yaml", "fine"),
			)
			srv := s.start(t, root)
			k, _ := newKind(t, srv)
			snap, err := source.Fetch(t.Context(), k, testSpec(srv, "lib", ""))
			if err != nil {
				// Refusing the whole tree is safe too, but say so.
				t.Fatalf("Fetch: %v", err)
			}
			defer func() { _ = snap.Close() }()
			if !cmp.Equal(paths(snap), []string{"ok.yaml"}) {
				t.Errorf("files = %v, want only ok.yaml", paths(snap))
			}
			want := []source.Skipped{
				{Path: "..", Reason: source.UnsafePath},
				{Path: ".git", Reason: source.UnsafePath},
				{Path: "esc\x1b[2J.yaml", Reason: source.UnsafePath},
				{Path: "twice.yaml", Reason: source.UnsafePath},
			}
			if diff := cmp.Diff(want, snap.Skipped); diff != "" {
				t.Errorf("skipped (-want +got):\n%s", diff)
			}
		})
	}
}

// A fetch stopped by a limit, or whose snapshot fails one, leaves the
// previous version in the cache, and nothing of its own.
func TestFetchLimits(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		limit   func(k *Kind, spec *source.Spec)
		wantErr string
	}{
		{
			name:    "max_fetch_size",
			limit:   func(_ *Kind, spec *source.Spec) { spec.Limits.MaxFetchSize = 2000 },
			wantErr: "more than max_fetch_size, 2000 bytes",
		},
		{
			name:    "objects: a commit, a tree and three blobs",
			limit:   func(k *Kind, _ *source.Spec) { k.maxObjects = 4 },
			wantErr: "the server sent 5 objects, more than the 4 a source may have",
		},
		{
			name:    "max_files",
			limit:   func(_ *Kind, spec *source.Spec) { spec.Limits.MaxFiles = 2 },
			wantErr: "the source holds more than 2 files, its max_files",
		},
		{
			name:    "the size of a tree",
			limit:   func(k *Kind, _ *source.Spec) { k.maxTree = 100 },
			wantErr: "bytes, more than the 100 a directory may",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			repo := newRepo(t, root, "lib")
			first := repo.commit("main", file("a.yaml", "x"))
			srv := goGitServer(t, root)
			k, _ := newKind(t, srv)
			fetch(t, k, testSpec(srv, "lib", ""))
			repo.commit("main", file("a.yaml", random(1000)), file("b.yaml", random(1000)), file("c.yaml", random(1000)))

			spec := testSpec(srv, "lib", "")
			tt.limit(k, &spec)
			_, err := source.Fetch(t.Context(), k, spec)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Fetch = %v, want %q", err, tt.wantErr)
			}
			cachedClone(t, k, first)
			snap, err := source.Open(t.Context(), k, testSpec(srv, "lib", ""))
			if err != nil || snap.Version != first.String() {
				t.Fatalf("Open after a failed fetch = %v, want the previous version", err)
			}
			_ = snap.Close()
		})
	}
}

// A repository's list of refs is limited, whichever way the server sends
// it: protocol v2's ls-refs, as both servers here speak it.
func TestFetchLimitsRefs(t *testing.T) {
	t.Parallel()
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			repo := newRepo(t, root, "lib")
			commit := repo.commit("main", file("a.yaml", "x"))
			for i := range 50 {
				repo.setRef(plumbing.NewBranchReferenceName(fmt.Sprintf("branch-%02d", i)), commit)
			}
			srv := s.start(t, root)
			k, _ := newKind(t, srv)
			k.maxRefs = 1000
			_, err := source.Fetch(t.Context(), k, testSpec(srv, "lib", ""))
			if err == nil || !strings.Contains(err.Error(), "the repository's list of branches and tags is larger than 1000 bytes") {
				t.Errorf("Fetch = %v, want the list of refs refused", err)
			}
			k.maxRefs = maxRefsSize
			fetch(t, k, testSpec(srv, "lib", ""))
		})
	}
}

// latest.json records the hash the clone holds for the ref: for an
// annotated tag, the tag object, which is what the repository advertises.
func TestAdvertisedComesFromTheClone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := newRepo(t, root, "lib")
	commit := repo.commit("main", file("a.yaml", "x"))
	tag := repo.annotatedTag("v1", commit)
	srv := goGitServer(t, root)
	k, _ := newKind(t, srv)
	fetch(t, k, testSpec(srv, "lib", "v1"))
	l, _ := cached(t, k)
	if l.Version != commit.String() || l.Advertised != tag.String() || l.Ref != "refs/tags/v1" {
		t.Errorf("latest.json = %+v, want version %s advertised as %s", l, commit, tag)
	}
}

func TestFetchTimeout(t *testing.T) {
	t.Parallel()
	srv := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never answers
	}))
	k, _ := newKind(t, srv)
	spec := testSpec(srv, "lib", "")
	spec.Limits.FetchTimeout = 200 * time.Millisecond
	start := time.Now()
	_, err := source.Fetch(t.Context(), k, spec)
	if err == nil || !strings.Contains(err.Error(), "the fetch took longer than fetch_timeout, 200ms") {
		t.Errorf("Fetch = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the fetch stopped after %v", elapsed)
	}

	// A context cancelled by the caller isn't the source's timeout.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := source.Fetch(ctx, k, testSpec(srv, "lib", "")); err == nil || strings.Contains(err.Error(), "fetch_timeout") {
		t.Errorf("Fetch with a cancelled context = %v, want an error that isn't about fetch_timeout", err)
	}
}

func TestTLS(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	newRepo(t, root, "lib").commit("main", file("a.yaml", "x"))
	srv := goGitServer(t, root)

	// Without the server's CA, its certificate doesn't verify.
	k, _ := newKind(t, nil)
	_, err := source.Fetch(t.Context(), k, testSpec(srv, "lib", ""))
	var verify *tls.CertificateVerificationError
	if !errors.As(err, &verify) {
		t.Errorf("Fetch = %v, want a certificate verification error", err)
	}
	if n := len(srv.rec.all()); n != 0 {
		t.Errorf("the server answered %d requests over an unverified connection", n)
	}

	tr, ok := k.transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil || tr.TLSClientConfig.InsecureSkipVerify || tr.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Errorf("TLS config = %+v, want verification on and TLS 1.2 or later", tr.TLSClientConfig)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	newRepo(t, root, "lib").commit("main", file("a.yaml", "x"))
	target := goGitServer(t, root)
	srv := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.RequestURI(), http.StatusMovedPermanently) //nolint:gosec // G710: a test server that redirects on purpose
	}))
	pool := srv.pool()
	pool.AddCert(target.Certificate())
	k := New(Options{CacheDir: t.TempDir(), RootCAs: pool})

	_, err := source.Fetch(t.Context(), k, testSpec(srv, "lib", ""))
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Errorf("Fetch = %v, want an error about the redirect", err)
	}
	if got := target.rec.all(); len(got) != 0 {
		t.Errorf("the redirect's target answered %v", got)
	}
}

func TestProxy(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	newRepo(t, root, "lib").commit("main", file("a.yaml", "x"))
	srv := goGitServer(t, root)
	p := startProxy(t)
	proxyURL, err := url.Parse(p.URL)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := newKind(t, srv)
	k.transport = newTransport(srv.pool(), func(*http.Request) (*url.URL, error) { return proxyURL, nil })

	fetch(t, k, testSpec(srv, "lib", ""))
	if seen := p.seen(); len(seen) == 0 || seen[0] != srv.Listener.Addr().String() {
		t.Errorf("the proxy tunnelled to %v, want %s", seen, srv.Listener.Addr())
	}

	// Only https may go through a proxy, which tunnels it without seeing
	// the request.
	proxy := httpsProxy(func(*http.Request) (*url.URL, error) { return proxyURL, nil })
	for target, want := range map[string]*url.URL{"https://example.com/lib.git": proxyURL, "http://example.com/lib.git": nil} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := proxy(req); got != want || err != nil {
			t.Errorf("proxy for %s = %v, %v; want %v", target, got, err, want)
		}
	}
}

func TestOnlyHTTPS(t *testing.T) {
	t.Parallel()
	k, _ := newKind(t, nil)
	for url, want := range map[string]string{
		"ssh://git@example.com/lib.git":     "an ssh repository needs credentials, which TrueUp doesn't support yet",
		"git@example.com:lib.git":           "an ssh repository needs credentials, which TrueUp doesn't support yet",
		"http://example.com/lib.git":        "the URL must use https or ssh",
		"file:///srv/lib.git":               "the URL must use https or ssh",
		"https://user@example.com/lib.git":  "the URL must not contain a user name or password",
		"https://example.com/lib.git?x=tok": "the URL must not have a query or fragment",
	} {
		spec := source.Spec{Name: "lib", URL: url, Limits: testLimits}
		if _, err := k.Fetch(t.Context(), spec); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Fetch of %s = %v, want %q", url, err, want)
		}
	}
	if _, err := os.Stat(source.CacheDir(k.cacheDir, "lib")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused URL made a cache: %v", err)
	}

	// Should a URL reach go-git anyway, it refuses every scheme but https.
	for _, scheme := range []string{"http", "ssh", "git", "file"} {
		_, err := refused(scheme).Handshake(t.Context(), nil)
		if !errors.Is(err, errScheme) || !strings.Contains(err.Error(), "not "+scheme) {
			t.Errorf("the %s transport = %v, want a refusal", scheme, err)
		}
	}
}

func TestOpen(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := newRepo(t, root, "lib")
	commit := repo.commit("main", file("a.yaml", "x"))
	srv := goGitServer(t, root)
	k, _ := newKind(t, srv)
	spec := testSpec(srv, "lib", "")

	if _, err := k.Open(t.Context(), spec); !errors.Is(err, source.ErrNotFetched) {
		t.Errorf("Open before a fetch = %v, want ErrNotFetched", err)
	}
	fetch(t, k, spec)

	srv.Close() // Open needs no network
	snap, err := source.Open(t.Context(), k, spec)
	if err != nil || snap.Version != commit.String() || !cmp.Equal(paths(snap), []string{"a.yaml"}) {
		t.Fatalf("Open = %v, %v", snap, err)
	}
	_ = snap.Close()

	for name, change := range map[string]func(*source.Spec){
		"another URL": func(s *source.Spec) { s.URL += "x" },
		"another ref": func(s *source.Spec) { s.Ref = "dev" },
	} {
		s := spec
		change(&s)
		if _, err := k.Open(t.Context(), s); !errors.Is(err, source.ErrNotFetched) {
			t.Errorf("Open with %s = %v, want ErrNotFetched", name, err)
		}
	}
}

// Content a fetch made counts only once committed; closed without that, it
// is discarded.
func TestFetchWithoutCommit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := newRepo(t, root, "lib")
	first := repo.commit("main", file("a.yaml", "x"))
	srv := goGitServer(t, root)
	k, _ := newKind(t, srv)
	spec := testSpec(srv, "lib", "")
	fetch(t, k, spec)
	firstClone := cachedClone(t, k, first)

	repo.commit("main", file("a.yaml", "y"))
	c, err := k.Fetch(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if got := cachedClone(t, k, first); got != firstClone {
		t.Errorf("the cache holds %s, want %s", got, firstClone)
	}
	snap, err := source.Open(t.Context(), k, spec)
	if err != nil || snap.Version != first.String() {
		t.Fatalf("Open = %v, want the first version", err)
	}
	_ = snap.Close()
}

// A cache that can't be read costs a fetch, never a failure.
func TestBrokenCache(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := newRepo(t, root, "lib")
	commit := repo.commit("main", file("a.yaml", "x"))
	srv := goGitServer(t, root)
	spec := testSpec(srv, "lib", "")

	for name, breakIt := range map[string]func(t *testing.T, dir string){
		"latest.json isn't JSON": func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, latestFile), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"latest.json names a path": func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, latestFile), []byte(`{"version":"../../etc"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"the clone is gone": func(t *testing.T, dir string) {
			l, err := readLatest(t.Context(), dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(dir, l.Dir)); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			k, logs := newKind(t, srv)
			fetch(t, k, spec)
			breakIt(t, source.CacheDir(k.cacheDir, "lib"))
			_, err := k.Open(t.Context(), spec)
			if err == nil {
				t.Error("Open of a broken cache succeeded")
			}
			if notFetched := errors.Is(err, source.ErrNotFetched); notFetched != (name == "the clone is gone") {
				t.Errorf("Open = %v: that is ErrNotFetched: %v, want it only when the clone is gone", err, notFetched)
			}
			if snap := fetch(t, k, spec); snap.Version != commit.String() {
				t.Errorf("version = %s, want %s", snap.Version, commit)
			}
			if !strings.Contains(logs.String(), "level=WARN") {
				t.Errorf("no warning about the broken cache:\n%s", logs)
			}
		})
	}
}

// Git's own server answers as hosting services do for a missing repository;
// go-git's answers 500.
func TestRepositoryErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	newRepo(t, root, "empty")
	srv := gitServer(t, root)
	k, _ := newKind(t, srv)
	for name, want := range map[string]string{
		"missing": "the repository wasn't found, or needs credentials, which TrueUp doesn't support yet",
		"empty":   "the repository is empty",
	} {
		_, err := source.Fetch(t.Context(), k, testSpec(srv, name, ""))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Fetch of the %s repository = %v, want %q", name, err, want)
		}
	}
}

func TestCut(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", maxErrorText) // two bytes each
	got := cut(long)
	if len(got) > maxErrorText+len("…") || !strings.HasSuffix(got, "…") || !strings.HasPrefix(long, strings.TrimSuffix(got, "…")) {
		t.Errorf("cut gave %d bytes, %q…", len(got), got[:10])
	}
	if cut("short") != "short" {
		t.Error("cut changed a short text")
	}
	err := cutError{errors.Join(errors.New(long), transport.ErrRepositoryNotFound)}
	if !errors.Is(err, transport.ErrRepositoryNotFound) {
		t.Error("cutError doesn't unwrap")
	}
}

func TestRedactURL(t *testing.T) {
	t.Parallel()
	withUserInfo := (&url.URL{Scheme: "https", User: url.UserPassword("user", "keykeykeykey"), Host: "example.com", Path: "/lib.git"}).String()
	for in, want := range map[string]string{
		"https://example.com/lib.git":   "https://example.com/lib.git",
		withUserInfo:                    "https://example.com/lib.git",
		"ssh://git@example.com/lib.git": "ssh://example.com/lib.git",
		"https://example.com:bad/":      "(a URL that can't be read)",
	} {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// A clone of a repository that isn't on a server, for the tests below.
func localClone(t *testing.T) *gogit.Repository {
	t.Helper()
	root := t.TempDir()
	repo := newRepo(t, root, "lib")
	repo.commit("main", file("a.yaml", "x"))
	return repo.repo
}

func TestCommitOf(t *testing.T) {
	t.Parallel()
	repo := localClone(t)
	if _, err := commitOf(repo, "refs/heads/nope"); err == nil {
		t.Error("commitOf a missing ref succeeded")
	}
	blob := blobHash("x")
	if err := repo.Storer.SetReference(plumbing.NewHashReference("refs/heads/blob", blob)); err != nil {
		t.Fatal(err)
	}
	if _, err := commitOf(repo, "refs/heads/blob"); err == nil || !strings.Contains(err.Error(), "points to a blob, not a commit") {
		t.Errorf("commitOf a blob = %v", err)
	}
}

func TestNewTrustsTheSystemByDefault(t *testing.T) {
	t.Parallel()
	k := New(Options{})
	tr, ok := k.transport.(*http.Transport)
	if !ok || tr.TLSClientConfig.RootCAs != nil {
		t.Errorf("RootCAs = %v, want nil, for the system's", tr.TLSClientConfig.RootCAs)
	}
	pool := x509.NewCertPool()
	k = New(Options{RootCAs: pool})
	if tr, _ := k.transport.(*http.Transport); tr.TLSClientConfig.RootCAs != pool {
		t.Error("RootCAs isn't used")
	}
}
