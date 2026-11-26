//go:build integration

package netboxtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/testcontainers/testcontainers-go/network"
)

// nb is the NetBox that TestMain starts for every test in the package.
var nb *NetBox

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	// Longer than the 15m45s compose.yaml's health checks allow, so that
	// they, not this, report a NetBox that never becomes ready.
	ctx, cancel := context.WithTimeoutCause(context.Background(), 25*time.Minute,
		errors.New("NetBox didn't start within 25 minutes"))
	defer cancel()
	var err error
	nb, err = Start(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start NetBox:", err)
		if cause := context.Cause(ctx); cause != nil {
			fmt.Fprintln(os.Stderr, "Cause:", cause)
		}
		return 1
	}

	code := m.Run()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stopCancel()
	if err := nb.Stop(stopCtx); err != nil {
		fmt.Fprintln(os.Stderr, "stop NetBox:", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

// TestStatusAndToken is the smoke test: NetBox reports the target version,
// and only the provisioned v2 token gets in. LOGIN_REQUIRED is on in
// netbox-docker's image, so /api/status/ refuses a request that isn't
// authenticated, and a 200 proves the token was accepted.
func TestStatusAndToken(t *testing.T) {
	t.Parallel()
	token := nb.Token.Reveal()
	key, _, ok := strings.Cut(strings.TrimPrefix(token, "nbt_"), ".")
	if !strings.HasPrefix(token, "nbt_") || !ok {
		t.Fatalf("token isn't in the v2 form nbt_<key>.<token>")
	}

	tests := []struct {
		name          string
		authorization string
		want          int
	}{
		{name: "v2 token", authorization: "Bearer " + token, want: http.StatusOK},
		{name: "no token", want: http.StatusForbidden},
		{name: "right key, wrong secret", authorization: "Bearer nbt_" + key + "." + strings.Repeat("0", 40), want: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			status, body := getStatus(t, tt.authorization)
			if status != tt.want {
				t.Fatalf("GET /api/status/ = %d, want %d: %q", status, tt.want, snippet(body))
			}
			if status != http.StatusOK {
				return
			}
			var st struct {
				Version string `json:"netbox-version"`
			}
			if err := json.Unmarshal(body, &st); err != nil {
				t.Fatalf("decode /api/status/: %v", err)
			}
			if st.Version != Version {
				t.Errorf("NetBox reports version %q, want %q", st.Version, Version)
			}
		})
	}
}

// TestStartReportsFailure starts a stack whose only service dies at once.
// The error must say which service failed and end with its log, and start
// must remove what it started without an error of its own.
func TestStartReportsFailure(t *testing.T) {
	t.Parallel()
	st, _, err := parseStack(composeFile, buildContexts)
	if err != nil {
		t.Fatal(err)
	}
	broken := fmt.Sprintf(`
services:
  netbox:
    image: %s
    command: ["sh", "-c", "echo netboxtest-was-here; exit 3"]
    environment:
      SUPERUSER_NAME: admin
      SUPERUSER_PASSWORD: insecure-test-only
    ports: ["127.0.0.1:0:6379"]
    healthcheck:
      test: ["CMD", "true"]
      start_period: 1m
      start_interval: 1s
      interval: 30s
      timeout: 5s
      retries: 3
`, st.Services["valkey"].Image)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	got, err := start(ctx, []byte(broken), buildContexts, Stock)
	if err == nil {
		_ = got.Stop(ctx)
		t.Fatal("start succeeded with a service that exits")
	}
	for _, want := range []string{"start netbox", "container exited with code 3", "The end of its log:", "netboxtest-was-here"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("start error doesn't contain %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "clean up after a failed start") {
		t.Errorf("start couldn't clean up after itself:\n%v", err)
	}
}

func getStatus(t *testing.T, authorization string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, nb.URL+"/api/status/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	status, body, err := do(req)
	if err != nil {
		t.Fatalf("GET /api/status/: %v", err)
	}
	return status, body
}

// TestProfile checks that NetBox runs the profile asked for: netbox-branching
// at BranchingVersion and a worker in Branching, neither in Stock.
func TestProfile(t *testing.T) {
	t.Parallel()
	var st struct {
		Plugins map[string]string `json:"plugins"`
		Workers *int              `json:"rq-workers-running"`
	}
	if err := nb.Get(t.Context(), "/api/status/", nil, &st); err != nil {
		t.Fatal(err)
	}
	wantPlugins, wantWorkers := map[string]string{}, 0
	if nb.Profile == Branching {
		wantPlugins, wantWorkers = map[string]string{"netbox_branching": BranchingVersion}, 1
	}
	if diff := cmp.Diff(wantPlugins, st.Plugins); diff != "" {
		t.Errorf("the %s profile's plugins (-want +got):\n%s", nb.Profile, diff)
	}
	if st.Workers == nil || *st.Workers != wantWorkers {
		t.Errorf("the %s profile runs %v workers, want %d", nb.Profile, st.Workers, wantWorkers)
	}
}

// names counts the names unique makes, so that each is new within the test
// binary, whose NetBox the tests share, even with -count.
var names atomic.Int64

// unique returns a name for test data, with prefix, that no other call in
// this test binary returns.
func unique(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, names.Add(1))
}

// requireBranching skips a test unless NetBox runs the Branching profile.
// CI runs the integration tests with each profile.
func requireBranching(t *testing.T) {
	t.Helper()
	if nb.Profile != Branching {
		t.Skipf("needs netbox-branching: run with %s=%s", ProfileVariable, Branching)
	}
}

// TestBranch creates a branch, and checks that what a test writes in it
// stays in it.
func TestBranch(t *testing.T) {
	t.Parallel()
	requireBranching(t)
	// A branch here is ready in seconds; without a deadline, one that never
	// is would hold up every test in the binary until go test's timeout.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	name, slug := unique("netboxtest-branch"), unique("branch-only")
	b, err := nb.CreateBranch(ctx, name, "TestBranch in internal/netboxtest")
	if err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	var state branchState
	if err := nb.Get(ctx, fmt.Sprintf("%s%d/", branchesPath, b.ID), nil, &state); err != nil {
		t.Fatal(err)
	}
	if state.Status.Value != "ready" || state.SchemaID != b.SchemaID || state.Name != name {
		t.Errorf("the branch is %+v, want %s, ready, with schema ID %s", state, name, b.SchemaID)
	}

	in := nb.InBranch(b)
	var created struct {
		ID int `json:"id"`
	}
	if err := in.Post(ctx, "/api/dcim/manufacturers/", map[string]string{"name": slug, "slug": slug}, &created); err != nil {
		t.Fatalf("Post in the branch: %v", err)
	}
	query := url.Values{"slug": {slug}}
	var inBranch, inMain struct {
		Results []struct {
			ID int `json:"id"`
		} `json:"results"`
	}
	if err := in.Get(ctx, "/api/dcim/manufacturers/", query, &inBranch); err != nil {
		t.Fatalf("Get in the branch: %v", err)
	}
	if len(inBranch.Results) != 1 || inBranch.Results[0].ID != created.ID {
		t.Errorf("the branch has manufacturers %+v, want the one created there, %d", inBranch.Results, created.ID)
	}
	if err := nb.Get(ctx, "/api/dcim/manufacturers/", query, &inMain); err != nil {
		t.Fatalf("Get in main: %v", err)
	}
	if len(inMain.Results) != 0 {
		t.Errorf("main has manufacturers %+v, want none: it was created in the branch", inMain.Results)
	}
}

// TestCreateBranchReportsStoppedWorker checks that CreateBranch fails, with
// the worker's log, when the worker has stopped, rather than waiting for a
// branch nothing provisions. A container that exits at once stands in for the
// worker, as stopping the real one would fail the tests that share it.
func TestCreateBranchReportsStoppedWorker(t *testing.T) {
	t.Parallel()
	requireBranching(t)
	st, _, err := parseStack(composeFile, buildContexts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	nw, err := network.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = nw.Remove(context.WithoutCancel(ctx)) }()
	dead := service{
		Image:       st.Services["valkey"].Image,
		Command:     []string{"sh", "-c", "echo worker-was-here; exit 3"},
		Expose:      []string{"6379"},
		Healthcheck: &healthcheck{Test: []string{"CMD", "true"}, StartPeriod: duration(time.Minute), StartInterval: duration(time.Second), Interval: duration(time.Second), Timeout: duration(time.Second), Retries: 1},
	}
	ctr, err := startService(ctx, nw, workerService, dead, buildContexts)
	if ctr != nil {
		defer func() { _ = ctr.Terminate(context.WithoutCancel(ctx)) }()
	}
	if err == nil || ctr == nil {
		t.Fatalf("the stand-in worker didn't stop: %v", err)
	}

	stopped := &NetBox{URL: nb.URL, Token: nb.Token, Profile: Branching, services: []startedService{{name: workerService, container: ctr}}}
	name := unique("netboxtest-stopped-worker")
	_, err = stopped.CreateBranch(ctx, name, "TestCreateBranchReportsStoppedWorker in internal/netboxtest")
	if err == nil {
		t.Fatal("CreateBranch succeeded with a stopped worker")
	}
	var left struct {
		Count int `json:"count"`
	}
	if err := nb.Get(ctx, branchesPath, url.Values{"name": {name}}, &left); err != nil || left.Count != 0 {
		t.Errorf("CreateBranch left %d branches named %s, %v; want none, since nothing would provision them", left.Count, name, err)
	}
	for _, want := range []string{fmt.Sprintf("create branch %q", name), "NetBox's worker, which provisions branches, has stopped", "Its state is exited", "The end of its log:", "worker-was-here"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("CreateBranch error doesn't contain %q:\n%v", want, err)
		}
	}
}
