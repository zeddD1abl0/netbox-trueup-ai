package netboxtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

// branchNetBox is a fake NetBox with netbox-branching. It answers a new
// branch's status with each of statuses in turn, then the last one again,
// and records the X-NetBox-Branch header of each request. A status of
// "hang" answers nothing until the request ends, and "503" and "404" answer
// with those statuses.
type branchNetBox struct {
	t        *testing.T
	statuses []string
	job      string // the answer to /api/core/jobs/

	mu      sync.Mutex
	reads   int
	headers []string
}

func (f *branchNetBox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	if r.Method == http.MethodGet && r.URL.Path == branchesPath+"3/" && f.statuses[min(f.reads, len(f.statuses)-1)] == "hang" {
		f.reads++
		f.mu.Unlock()
		<-r.Context().Done()
		return
	}
	defer f.mu.Unlock()
	f.headers = append(f.headers, r.Method+" "+r.URL.Path+" "+r.Header.Get(branchHeader))
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == branchesPath:
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id": 3, "name": "b", "schema_id": "abcd1234", "status": {"value": "new"}}`)
	case r.Method == http.MethodGet && r.URL.Path == branchesPath+"3/":
		status := f.statuses[min(f.reads, len(f.statuses)-1)]
		f.reads++
		if code, err := strconv.Atoi(status); err == nil {
			w.WriteHeader(code)
			_, _ = fmt.Fprint(w, `{"detail": "not now"}`)
			return
		}
		if status == "" {
			_, _ = fmt.Fprint(w, `{"id": 3, "name": "b", "schema_id": "abcd1234"}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id": 3, "name": "b", "schema_id": "abcd1234", "status": {"value": %q}}`, status)
	case r.Method == http.MethodGet && r.URL.Path == "/api/core/jobs/":
		if got := r.URL.Query(); got.Get("object_type") != "netbox_branching.branch" || got.Get("object_id") != "3" {
			f.t.Errorf("jobs query = %v, want branch 3's", got)
		}
		_, _ = fmt.Fprint(w, f.job)
	default:
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id": 9}`)
	}
}

func newBranchNetBox(t *testing.T, statuses ...string) (*NetBox, *branchNetBox) {
	t.Helper()
	f := &branchNetBox{t: t, statuses: statuses, job: `{"results": []}`}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &NetBox{URL: srv.URL, Token: config.NewSecret(testToken), Profile: Branching}, f
}

// alive stands in for the worker check, which fails once ctx ends, as
// inspecting a container does.
func alive(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("inspect NetBox's worker: %w", err)
	}
	return nil
}

func TestWaitForBranch(t *testing.T) {
	t.Parallel()
	const queued = `{"results": [{"name": "Provision branch", "status": {"value": "pending"}, "error": ""}]}`
	tests := []struct {
		name     string
		statuses []string
		job      string
		alive    func(context.Context) error
		timeout  time.Duration // the caller's
		limit    time.Duration // waitForBranch's own; a minute if 0
		want     string        // in the error; "" for none
		notWant  string        // not in the error
	}{
		{name: "ready at once", statuses: []string{"ready"}},
		{name: "provisioned", statuses: []string{"new", "provisioning", "provisioning", "ready"}},
		{
			name:     "failed",
			statuses: []string{"provisioning", "failed"},
			job:      `{"results": [{"name": "Provision branch", "status": {"value": "errored"}, "error": "ProgrammingError('boom')"}]}`,
			want:     `provisioning the branch failed` + "\n" + `Its job "Provision branch" is "errored", with the error "ProgrammingError('boom')".`,
		},
		{name: "failed without a job", statuses: []string{"failed"}, want: "(NetBox has no job for it)"},
		{name: "unexpected status", statuses: []string{"merged"}, want: `the branch is "merged", not provisioning or ready`},
		{
			name:     "worker stopped",
			statuses: []string{"provisioning"},
			alive:    func(context.Context) error { return errors.New("the worker has stopped") },
			want:     "the worker has stopped",
		},
		{name: "never ready", statuses: []string{"provisioning"}, timeout: time.Second, want: "the branch is still provisioning: context deadline exceeded"},
		{
			name:     "not ready within the limit",
			statuses: []string{"new"},
			job:      queued,
			limit:    500 * time.Millisecond,
			want:     `the branch is still new: it wasn't ready within 500ms` + "\n" + `Its job "Provision branch" is "pending", with the error "".`,
		},
		{name: "no status", statuses: []string{"provisioning", ""}, want: "NetBox's answer about the branch has no status"},
		{name: "ready after a failed look", statuses: []string{"provisioning", "503", "ready"}},
		{
			// A 4xx answer won't change, so the wait stops at it.
			name:     "a look refused",
			statuses: []string{"provisioning", "404"},
			limit:    5 * time.Second,
			want:     "GET /api/plugins/branching/branches/3/: NetBox answered 404",
			notWant:  "the branch is still",
		},
		{
			name:     "looks failing until the limit",
			statuses: []string{"503"},
			limit:    500 * time.Millisecond,
			want:     "the branch is still new: it wasn't ready within 500ms; the last look failed: GET /api/plugins/branching/branches/3/: NetBox answered 503",
		},
		{
			// The deadline passes while NetBox is asked for the branch's status,
			// which isn't a failed look.
			name:     "deadline while reading the status",
			statuses: []string{"hang"},
			timeout:  500 * time.Millisecond,
			want:     "the branch is still new: context deadline exceeded",
			notWant:  "the last look failed",
		},
		{
			// The deadline passes while the worker is being checked, not while
			// waiting for the next look, so the check fails because of it.
			name:     "deadline during the worker check",
			statuses: []string{"provisioning"},
			alive: func(ctx context.Context) error {
				<-ctx.Done()
				return fmt.Errorf("inspect NetBox's worker: %w", ctx.Err())
			},
			timeout: 500 * time.Millisecond,
			want:    "the branch is still new: context deadline exceeded",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			nb, f := newBranchNetBox(t, tt.statuses...)
			if tt.job != "" {
				f.job = tt.job
			}
			check := tt.alive
			if check == nil {
				check = alive
			}
			ctx := t.Context()
			if tt.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.timeout)
				defer cancel()
			}
			b := branchState{Branch: Branch{ID: 3, Name: "b", SchemaID: "abcd1234"}}
			b.Status.Value = "new"
			limit := tt.limit
			if limit == 0 {
				limit = time.Minute
			}
			err := nb.waitForBranch(ctx, b, check, limit)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("waitForBranch: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("waitForBranch error = %v, want it to contain %q", err, tt.want)
			case tt.notWant != "" && strings.Contains(err.Error(), tt.notWant):
				t.Fatalf("waitForBranch error = %v, which shouldn't contain %q", err, tt.notWant)
			}
			if tt.want == "" && f.reads < len(tt.statuses) {
				t.Errorf("waitForBranch returned after %d reads, before the branch was ready", f.reads)
			}
		})
	}
}

func TestCreateBranchRefusesStock(t *testing.T) {
	t.Parallel()
	nb, f := newBranchNetBox(t, "ready")
	nb.Profile = Stock
	_, err := nb.CreateBranch(t.Context(), "b", "")
	if err == nil || !strings.Contains(err.Error(), "runs the stock profile, which has no netbox-branching; run the tests with NETBOXTEST_PROFILE=branching") {
		t.Errorf("CreateBranch on stock = %v, want it refused", err)
	}
	if len(f.headers) != 0 {
		t.Errorf("CreateBranch sent %q to a stock NetBox", f.headers)
	}
}

// TestCreateBranchChecksTheWorkerFirst checks that a stopped worker leaves
// no branch behind that nothing would provision.
func TestCreateBranchChecksTheWorkerFirst(t *testing.T) {
	t.Parallel()
	nb, f := newBranchNetBox(t, "ready")
	stopped := func(context.Context) error { return errors.New("the worker has stopped") }
	_, err := nb.createBranch(t.Context(), "b", "", stopped)
	if err == nil || !strings.Contains(err.Error(), `create branch "b": the worker has stopped`) {
		t.Errorf("createBranch with a stopped worker = %v", err)
	}
	if len(f.headers) != 0 {
		t.Errorf("createBranch sent %q with a stopped worker, want nothing sent", f.headers)
	}
}

// TestCreateBranchNamesWhatItLeaves checks that a branch CreateBranch made,
// but which never became ready, is named in the error, since it stays.
func TestCreateBranchNamesWhatItLeaves(t *testing.T) {
	t.Parallel()
	nb, _ := newBranchNetBox(t, "failed")
	_, err := nb.createBranch(t.Context(), "b", "", alive)
	if err == nil || !strings.Contains(err.Error(), `create branch "b" (ID 3, left in NetBox): provisioning the branch failed`) {
		t.Errorf("createBranch = %v, want it to name the branch it left", err)
	}
}

// TestCreateBranchWithoutAnAnswer checks that a create NetBox didn't answer
// says that NetBox may have made the branch, and one it refused doesn't.
func TestCreateBranchWithoutAnAnswer(t *testing.T) {
	t.Parallel()
	// The server notices the client leave only once it has read the body.
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(hang.Close)
	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"name": ["exists"]}`)
	}))
	t.Cleanup(refuse.Close)
	for _, tt := range []struct {
		url   string
		maybe bool
	}{{hang.URL, true}, {refuse.URL, false}} {
		nb := &NetBox{URL: tt.url, Token: config.NewSecret(testToken), Profile: Branching}
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		_, err := nb.createBranch(ctx, "b", "", alive)
		cancel()
		if err == nil || strings.Contains(err.Error(), "NetBox may have made it anyway") != tt.maybe {
			t.Errorf("createBranch = %v; want it to say NetBox may have made the branch: %v", err, tt.maybe)
		}
	}
}

func TestCreateBranchChecksTheAnswer(t *testing.T) {
	t.Parallel()
	for _, answer := range []string{
		`{"id": 0, "schema_id": "abcd1234"}`,
		`{"id": 3, "schema_id": "abcd1234\r\nX-Other: 1"}`,
		`{"id": 3, "schema_id": "ABCD1234"}`,
		`{"id": 3}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, answer)
		}))
		nb := &NetBox{URL: srv.URL, Token: config.NewSecret(testToken), Profile: Branching}
		_, err := nb.createBranch(t.Context(), "b", "", alive)
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "NetBox made it, but its answer has no usable ID and schema ID") {
			t.Errorf("CreateBranch with the answer %s = %v, want it refused", answer, err)
		}
	}
}

func TestInBranch(t *testing.T) {
	t.Parallel()
	nb, f := newBranchNetBox(t, "ready")
	c := nb.InBranch(Branch{ID: 3, SchemaID: "abcd1234"})
	ctx := t.Context()
	if err := c.Post(ctx, "/api/dcim/manufacturers/", map[string]string{"name": "x"}, nil); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if err := nb.Post(ctx, "/api/dcim/manufacturers/", map[string]string{"name": "x"}, nil); err != nil {
		t.Fatalf("Post in main: %v", err)
	}
	want := []string{"POST /api/dcim/manufacturers/ abcd1234", "POST /api/dcim/manufacturers/ "}
	if strings.Join(f.headers, "|") != strings.Join(want, "|") {
		t.Errorf("requests = %q, want %q", f.headers, want)
	}
}

func TestInBranchRefuses(t *testing.T) {
	t.Parallel()
	nb, f := newBranchNetBox(t, "ready")
	ctx := t.Context()
	tests := []struct {
		name   string
		branch Branch
		want   string
	}{
		{name: "no branch", want: "no branch to send it in"},
		{name: "a schema ID NetBox wouldn't make", branch: Branch{SchemaID: "abcd1234\r\nX-Other: 1"}, want: "isn't a branch's schema ID"},
	}
	for _, tt := range tests {
		c := nb.InBranch(tt.branch)
		for name, call := range map[string]func() error{
			"Post":   func() error { return c.Post(ctx, "/api/dcim/manufacturers/", map[string]string{}, nil) },
			"Patch":  func() error { return c.Patch(ctx, "/api/dcim/manufacturers/1/", map[string]string{}, nil) },
			"Delete": func() error { return c.Delete(ctx, "/api/dcim/manufacturers/1/") },
			"Get":    func() error { return c.Get(ctx, "/api/dcim/manufacturers/", nil, nil) },
		} {
			if err := call(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%s: %s = %v, want an error containing %q", tt.name, name, err, tt.want)
			}
		}
	}
	// netbox-branching serves these from main whatever the branch.
	c := nb.InBranch(Branch{SchemaID: "abcd1234"})
	for _, path := range []string{"/api/status/", "/api/users/users/", "/api/users/tokens/1/", "/api/core/jobs/", "/api/plugins/branching/branches/", "/api/extras/custom-fields/", "/api/extras/webhooks/3/", "/api/extras/bookmarks/", "/api/extras/scripts/upload/", "/api/extras/dashboard/"} {
		for name, call := range map[string]func() error{
			"Post": func() error { return c.Post(ctx, path, map[string]string{}, nil) },
			"Get":  func() error { return c.Get(ctx, path, nil, nil) },
		} {
			if err := call(); err == nil || !strings.Contains(err.Error(), "serves") || !strings.Contains(err.Error(), "from main, whatever the branch") {
				t.Errorf("%s %s in a branch = %v, want it refused", name, path, err)
			}
		}
	}
	if len(f.headers) != 0 {
		t.Errorf("sent %q, want nothing sent", f.headers)
	}
}

// answer is a fake NetBox's answer: a status code and a body.
type answer struct {
	status int
	body   string
}

// statusNetBox answers /api/status/ with each of answers in turn, then the
// last one again. An answer with status 0 answers nothing until the request
// ends.
func statusNetBox(t *testing.T, answers ...answer) *NetBox {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status/" {
			t.Errorf("request for %s, want /api/status/", r.URL.Path)
		}
		mu.Lock()
		a := answers[min(n, len(answers)-1)]
		n++
		mu.Unlock()
		if a.status == 0 {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.status)
		_, _ = fmt.Fprint(w, a.body)
	}))
	t.Cleanup(srv.Close)
	return &NetBox{URL: srv.URL, Token: config.NewSecret(testToken), Profile: Branching}
}

func TestWaitForWorker(t *testing.T) {
	t.Parallel()
	none := answer{http.StatusOK, `{"rq-workers-running": 0}`}
	one := answer{http.StatusOK, `{"rq-workers-running": 1}`}
	failed := answer{http.StatusServiceUnavailable, `{"detail": "starting"}`}
	tests := []struct {
		name    string
		answers []answer
		alive   func(context.Context) error
		want    string // in the error; "" for none
		notWant string
	}{
		{
			// A 4xx answer won't change, so the wait stops at it.
			name:    "refused",
			answers: []answer{{status: http.StatusForbidden, body: `{"detail": "no"}`}},
			want:    "wait for NetBox to count its worker: GET /api/status/: NetBox answered 403",
			notWant: "counted no running worker",
		},
		{
			name:    "worker stopped",
			answers: []answer{none},
			alive:   func(context.Context) error { return errors.New("NetBox's worker has stopped") },
			want:    "wait for NetBox to count its worker: NetBox's worker has stopped",
		},
		{name: "running", answers: []answer{one}},
		{name: "after a failed request and none running", answers: []answer{failed, none, one}},
		{name: "no count", answers: []answer{{status: http.StatusOK, body: `{"netbox-version": "4.7.2"}`}}, want: "doesn't say how many workers are running"},
		{
			name:    "none before the caller's deadline",
			answers: []answer{none},
			want:    "NetBox counted no running worker: context deadline exceeded",
			notWant: "within",
		},
		{
			name:    "requests failing until the caller's deadline",
			answers: []answer{failed},
			want:    "context deadline exceeded; the last request failed: GET /api/status/: NetBox answered 503",
		},
		{
			// The deadline cuts the last request short, which says nothing
			// about NetBox, so the one before it is reported.
			name:    "a request cut short by the deadline",
			answers: []answer{failed, {}},
			want:    "context deadline exceeded; the last request failed: GET /api/status/: NetBox answered 503",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			nb := statusNetBox(t, tt.answers...)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			check := tt.alive
			if check == nil {
				check = alive
			}
			err := nb.waitForWorker(ctx, nil, check)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("waitForWorker: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("waitForWorker error = %v, want it to contain %q", err, tt.want)
			case tt.notWant != "" && strings.Contains(err.Error(), tt.notWant):
				t.Errorf("waitForWorker error = %v, which blames its own limit for the caller's deadline", err)
			}
		})
	}
}
