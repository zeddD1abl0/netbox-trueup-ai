package main

import (
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/osfs"
	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/backend"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/google/go-cmp/cmp"
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
	// The cache of a source no longer configured is removed.
	if err := os.MkdirAll(filepath.Join(dataDir, "cache", "sources", "old", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
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
}
