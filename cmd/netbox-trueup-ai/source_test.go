package main

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/osfs"
	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/backend"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/engine"
)

// gitRepo serves a repository with go-git's own server over TLS, and returns
// its URL, the certificates that trust the server, and its commit.
func gitRepo(t *testing.T) (repoURL string, pool *x509.CertPool, commit string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "lib")
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"device-types/Juniper/MX204.yaml": "model: MX204\n",
		"device-types/Cisco/C9300.yaml":   "model: C9300\n",
		"elevation-images/mx204.png":      strings.Repeat("x", 2048),
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"device-types", "elevation-images"} {
		if _, err := wt.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	sig := &object.Signature{Name: "Test", Email: "test@example.com", When: time.Unix(1700000000, 0)}
	h, err := wt.Commit("add device types\n", &gogit.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		t.Fatal(err)
	}
	_ = repo.Close()

	srv := httptest.NewUnstartedServer(backend.New(transport.NewFilesystemLoader(osfs.New(root), false)))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	pool = x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv.URL + "/lib", pool, h.String()
}

// sourcesEnv configures sources as JSON, and a data directory of the test's
// own.
func sourcesEnv(t *testing.T, sources string) (env []string, dataDir string) {
	t.Helper()
	dataDir = t.TempDir()
	return []string{"TRUEUP_DATA_DIR=" + dataDir, "TRUEUP_SOURCES=" + sources}, dataDir
}

func trust(pool *x509.CertPool) func(*app) {
	return func(a *app) { a.sourceRootCAs = pool }
}

func TestSourceFetchText(t *testing.T) {
	t.Parallel()
	repoURL, pool, commit := gitRepo(t)
	env, _ := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library","max_file_size":"1KiB"}]`)
	r := runCLIApp(t, nil, trust(pool), env, "source", "fetch")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	want := `SOURCE  REF     VERSION                                   FILES  SKIPPED
lib     master  ` + commit + `  2      1

Skipped files (--output json lists them all):
  lib: 1 larger than max_file_size, such as elevation-images/mx204.png
`
	if diff := cmp.Diff(want, r.stdout); diff != "" {
		t.Errorf("source fetch (-want +got):\n%s", diff)
	}
	if !strings.Contains(r.stderr, `msg="fetched source" source=lib`) {
		t.Errorf("stderr doesn't log the fetch:\n%s", r.stderr)
	}
}

func TestSourceFetchJSON(t *testing.T) {
	t.Parallel()
	repoURL, pool, commit := gitRepo(t)
	env, dataDir := sourcesEnv(t, `[
		{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library","include":["device-types/Juniper"]},
		{"name":"private","kind":"git","url":"git@git.example.com:defs.git","format":"devicetype-library"}
	]`)
	// The cache and records of a source no longer configured are removed.
	if err := os.MkdirAll(filepath.Join(dataDir, "cache", "sources", "old", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	recordOld(t, dataDir)
	before := time.Now()
	r := runCLIApp(t, nil, trust(pool), env, "source", "fetch", "-o", "json")
	if r.code != exitFetchFailed {
		t.Errorf("exit code = %d, want %d\nstderr: %s", r.code, exitFetchFailed, r.stderr)
	}
	if !strings.HasSuffix(r.stderr, "Error: 1 of the 2 sources couldn't be fetched: private\n") {
		t.Errorf("stderr = %q", r.stderr)
	}

	var got fetchJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("source fetch -o json printed %q: %v", r.stdout, err)
	}
	ref, files, sshErr := "master", 1, "an ssh repository needs credentials, which TrueUp doesn't support yet: use the repository's https URL"
	want := fetchJSON{Sources: []fetchSourceJSON{
		{Name: "lib", Kind: "git", URL: repoURL, Ref: &ref, OK: true, Version: &commit, Files: &files, Skipped: []skippedJSON{}},
		{Name: "private", Kind: "git", URL: "git@git.example.com:defs.git", Skipped: []skippedJSON{}, Error: &sshErr},
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("source fetch -o json (-want +got):\n%s", diff)
	}
	// The field names are public interface.
	for _, field := range []string{`"ok": false`, `"sources"`, `"name"`, `"kind"`, `"url"`, `"ref": null`, `"version": null`, `"files": null`, `"skipped": []`, `"error"`} {
		if !strings.Contains(r.stdout, field) {
			t.Errorf("source fetch -o json has no %s:\n%s", field, r.stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "cache", "sources", "old")); !os.IsNotExist(err) {
		t.Errorf("the old source's cache is still there: %v", err)
	}

	// Both fetches are recorded, the failed one with its error.
	sources, snaps := records(t, dataDir)
	if len(sources) != 2 || sources[0].Name != "lib" || sources[1].Name != "private" {
		t.Fatalf("the database records %+v, want lib and private", sources)
	}
	for _, src := range sources {
		if src.FetchedAt.Before(before.Add(-time.Second)) || src.FetchedAt.After(time.Now()) {
			t.Errorf("source %s was fetched at %v, not during the command", src.Name, src.FetchedAt)
		}
	}
	if sources[0].Error != "" || sources[1].Error != sshErr || sources[1].Snapshot != nil {
		t.Errorf("the database records lib's error %q, and private's %q with snapshot %+v", sources[0].Error, sources[1].Error, sources[1].Snapshot)
	}
	wantFiles := []store.File{{
		Path: "device-types/Juniper/MX204.yaml", Size: 13,
		Digest: "sha256:" + fmt.Sprintf("%x", sha256.Sum256([]byte("model: MX204\n"))),
	}}
	if snap := snaps["lib"]; snap.Version != commit || snap.Ref != "master" || snap.URL != repoURL || !cmp.Equal(snap.Files, wantFiles) || len(snap.Skipped) != 0 {
		t.Errorf("lib's recorded snapshot = %+v, want version %s with %+v", snap, commit, wantFiles)
	}
}

// recordOld records a fetch of a source named old in the database in
// dataDir.
func recordOld(t *testing.T, dataDir string) {
	t.Helper()
	recordFetch(t, dataDir, "old", time.Now().Add(-time.Hour))
}

// recordFetch records a fetch of the named source at a time, in the
// database in dataDir.
func recordFetch(t *testing.T, dataDir, name string, at time.Time) {
	t.Helper()
	s, _, err := engine.Open(t.Context(), engine.Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	err = s.RecordFetch(t.Context(), store.Fetch{
		Source:    store.SourceConfig{Name: name, Kind: "git", URL: "https://git.example.com/" + name + ".git"},
		FetchedAt: at, Version: "abc",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSourceFetchAfterTheClockWentBack(t *testing.T) {
	t.Parallel()
	repoURL, pool, commit := gitRepo(t)
	env, dataDir := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library"}]`)
	// As if the clock had run an hour ahead for another fetch.
	recordFetch(t, dataDir, "lib", time.Now().Add(time.Hour))

	before := time.Now()
	r := runCLIApp(t, nil, trust(pool), env, "source", "fetch")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	sources, snaps := records(t, dataDir)
	if len(sources) != 1 || sources[0].FetchedAt.Before(before.Add(-time.Second)) || sources[0].FetchedAt.After(time.Now()) || snaps["lib"].Version != commit {
		t.Errorf("the database records %+v with snapshot %+v, want this fetch of %s", sources, snaps["lib"], commit)
	}
}

func TestSourceFetchWaitsForAnotherFetch(t *testing.T) {
	t.Parallel()
	repoURL, pool, commit := gitRepo(t)
	env, dataDir := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library"}]`)
	root := filepath.Join(dataDir, "cache", "sources")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	// Another process is fetching lib, and finishes once this one has said
	// it is waiting, which it does after a second. A fixed delay instead
	// raced with creating and migrating the database, which can take longer
	// on a slow CI runner.
	unlock, err := source.LockCache(t.Context(), root, "lib", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	waiting := &signalWriter{want: "waiting for another TrueUp process to finish fetching the source", seen: make(chan struct{})}
	released := make(chan struct{})
	go func() {
		defer close(released)
		select {
		case <-waiting.seen:
		case <-time.After(time.Minute):
		}
		_ = unlock()
	}()
	t.Cleanup(func() { <-released })

	r := runCLIApp(t, nil, func(a *app) {
		trust(pool)(a)
		a.stderr = io.MultiWriter(a.stderr, waiting)
	}, env, "source", "fetch")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, `msg="waiting for another TrueUp process to finish fetching the source" source=lib`) {
		t.Errorf("stderr doesn't say it waited:\n%s", r.stderr)
	}
	if _, snaps := records(t, dataDir); snaps["lib"].Version != commit {
		t.Errorf("lib's recorded snapshot = %+v, want %s", snaps["lib"], commit)
	}
}

// signalWriter closes seen the first time something written to it holds
// want.
type signalWriter struct {
	want string
	seen chan struct{}
	once sync.Once
}

func (w *signalWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.want) {
		w.once.Do(func() { close(w.seen) })
	}
	return len(p), nil
}

func TestSourceFetchLeavesASourceAnotherProcessFetches(t *testing.T) {
	t.Parallel()
	repoURL, pool, _ := gitRepo(t)
	env, dataDir := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library"}]`)
	root := filepath.Join(dataDir, "cache", "sources")
	if err := os.MkdirAll(filepath.Join(root, "old", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	recordOld(t, dataDir)
	// Another process, whose configuration still has old, is fetching it.
	unlock, err := source.LockCache(t.Context(), root, "old", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := runCLIApp(t, nil, trust(pool), env, "source", "fetch")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "another TrueUp process is fetching a source no longer configured") {
		t.Errorf("stderr doesn't say it left old alone:\n%s", r.stderr)
	}
	if sources, _ := records(t, dataDir); len(sources) != 2 {
		t.Errorf("the database records %+v, want lib and old", sources)
	}
	if _, err := os.Stat(filepath.Join(root, "old")); err != nil {
		t.Errorf("old's cache: %v", err)
	}

	// Once that fetch is over, the next run removes old.
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if r := runCLIApp(t, nil, trust(pool), env, "source", "fetch"); r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	if sources, _ := records(t, dataDir); len(sources) != 1 || sources[0].Name != "lib" {
		t.Errorf("the database records %+v, want only lib", sources)
	}
	if _, err := os.Stat(filepath.Join(root, "old")); !os.IsNotExist(err) {
		t.Errorf("old's cache is still there: %v", err)
	}
}

func TestSourceFetchRecordsSkippedFiles(t *testing.T) {
	t.Parallel()
	repoURL, pool, _ := gitRepo(t)
	env, dataDir := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library","max_file_size":"1KiB"}]`)
	if r := runCLIApp(t, nil, trust(pool), env, "source", "fetch"); r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	_, snaps := records(t, dataDir)
	want := []store.Skipped{{Path: "elevation-images/mx204.png", Reason: "too_large"}}
	if snap := snaps["lib"]; len(snap.Files) != 2 || !cmp.Equal(snap.Skipped, want) {
		t.Errorf("lib's recorded snapshot = %+v, want 2 files and %+v skipped", snap, want)
	}
}

func TestSourceFetchSkippedPathsAreEscaped(t *testing.T) {
	t.Parallel()
	got := &strings.Builder{}
	ref, version, files := "main\x1b[2J", "abc", 0
	err := writeFetchText(got, fetchJSON{Sources: []fetchSourceJSON{{
		Name: "lib", OK: true, Ref: &ref, Version: &version, Files: &files,
		Skipped: []skippedJSON{{Path: "esc\x1b[31m.yaml", Reason: "unsafe_path"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(got.String(), '\x1b') {
		t.Errorf("text output holds an escape character:\n%q", got.String())
	}
	if !strings.Contains(got.String(), `"esc\x1b[31m.yaml"`) || !strings.Contains(got.String(), `"main\x1b[2J"`) {
		t.Errorf("text output doesn't quote what it escapes:\n%s", got.String())
	}
}

func TestSourceFetchChoosesSources(t *testing.T) {
	t.Parallel()
	repoURL, pool, _ := gitRepo(t)
	env, _ := sourcesEnv(t, `[
		{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library"},
		{"name":"private","kind":"git","url":"git@git.example.com:defs.git","format":"devicetype-library"}
	]`)
	r := runCLIApp(t, nil, trust(pool), env, "source", "fetch", "lib", "-o", "json")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	var got fetchJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 1 || got.Sources[0].Name != "lib" || !got.OK {
		t.Errorf("fetched %+v, want only lib", got)
	}

	r = runCLIApp(t, nil, trust(pool), env, "source", "fetch", "lib", "nope")
	if r.code != exitUsage || !strings.Contains(r.stderr, `"nope" isn't a configured source: the sources are lib, private`) || r.stdout != "" {
		t.Errorf("an unknown source: exit code %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
}

func TestSourceFetchFailures(t *testing.T) {
	t.Parallel()

	r := runCLI(t, nil, []string{"TRUEUP_DATA_DIR=" + t.TempDir()}, "source", "fetch")
	if r.code != exitConfig || !strings.Contains(r.stderr, "no sources are configured") {
		t.Errorf("with no sources: exit code %d, stderr %q", r.code, r.stderr)
	}

	// A data directory that can't hold the cache stops the command.
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r = runCLI(t, nil, []string{
		"TRUEUP_DATA_DIR=" + notADir,
		`TRUEUP_SOURCES=[{"name":"lib","kind":"git","url":"https://example.com/lib.git","format":"devicetype-library"}]`,
	}, "source", "fetch", "-o", "json")
	if r.code != exitFailure || r.stdout != "" || !strings.Contains(r.stderr, "create the source cache in the data directory") {
		t.Errorf("with a bad data directory: exit code %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}

	// A cache whose fetch locks can't be taken stops the command, rather
	// than failing each source.
	brokenDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(brokenDir, "cache", "sources"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "cache", "sources", ".locks"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r = runCLI(t, nil, []string{
		"TRUEUP_DATA_DIR=" + brokenDir,
		`TRUEUP_SOURCES=[{"name":"lib","kind":"git","url":"https://example.com/lib.git","format":"devicetype-library"}]`,
	}, "source", "fetch", "-o", "json")
	if r.code != exitFailure || r.stdout != "" || !strings.Contains(r.stderr, "source lib: create the directory of the sources' fetch locks") {
		t.Errorf("with a broken lock directory: exit code %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}

	// A database this release can't use stops the command before it
	// fetches anything.
	r = runCLI(t, nil, []string{
		"TRUEUP_DATA_DIR=" + t.TempDir(),
		"TRUEUP_DATABASE_URL=" + postgresURL + "?sslmode=bogus",
		`TRUEUP_SOURCES=[{"name":"lib","kind":"git","url":"https://example.com/lib.git","format":"devicetype-library"}]`,
	}, "source", "fetch", "-o", "json")
	if r.code != exitConfig || r.stdout != "" || !strings.Contains(r.stderr, "can't be used to connect to PostgreSQL") || strings.Contains(r.stderr, "example.com/lib") {
		t.Errorf("with PostgreSQL: exit code %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
}
