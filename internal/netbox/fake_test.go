package netbox

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

// Fake tokens for a fake NetBox. Low entropy, so that secret scanners don't
// mistake them for real ones, and errors and logs must never repeat them.
const (
	testKey     = "keykeykeykey"
	testSecret  = "secretsecretsecretsecretsecretsecret0000"
	testToken   = "nbt_" + testKey + "." + testSecret
	testV1Token = "v1tokenv1tokenv1tokenv1tokenv1tokenv1tok"
)

// fakeNetBox stands in for NetBox 4.7 in unit tests. It serves /api/status/,
// /api/authentication-check/, and cursor-paginated listings of objects, as
// NetBoxPagination does, and records every request. Any method but GET fails
// the test: the client must never send one.
type fakeNetBox struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// What it answers with. Tests change these before the client connects,
	// or under mu.
	version       string // netbox-version
	apiVersion    string // the API-Version header
	authorization string // the Authorization header it accepts
	statusOpen    bool   // /api/status/ answers a request without a token, as with LOGIN_REQUIRED off
	maxPageSize   int
	ignoreFilters bool                        // ignores _id and name filters, as NetBox ignores unknown ones
	plugins       any                         // /api/status/'s plugins, or omitted{} to leave them out
	branches      map[string]string           // netbox-branching's branches, by schema ID, to their status
	objects       map[string][]map[string]any // by endpoint, in ID order
	denied        map[string]bool             // endpoints that answer 403
	// handle, if set, answers a request first; it returns false to leave
	// the request to the fake.
	handle func(w http.ResponseWriter, r *http.Request) bool

	requests []request
}

// request is what the fake recorded of one request.
type request struct {
	path          string
	query         url.Values
	authorization string
	accept        string
	userAgent     string
	// branch is the X-NetBox-Branch header, and hasBranch whether there
	// was one.
	branch    string
	hasBranch bool
}

// omitted, as a fake's plugins, leaves plugins out of /api/status/.
type omitted struct{}

func newFake(t *testing.T) *fakeNetBox {
	t.Helper()
	f := &fakeNetBox{
		t:             t,
		version:       "4.7.2",
		apiVersion:    "4.7",
		authorization: "Bearer " + testToken,
		maxPageSize:   1000,
		objects:       map[string][]map[string]any{},
		denied:        map[string]bool{},
		plugins:       map[string]any{},
		branches:      map[string]string{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNetBox) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		f.t.Errorf("the client sent %s %s, but it must only ever send GET", r.Method, r.URL.Path)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, hasBranch := r.Header[http.CanonicalHeaderKey(branchHeader)]
	f.mu.Lock()
	f.requests = append(f.requests, request{
		path: r.URL.Path, query: r.URL.Query(), authorization: r.Header.Get("Authorization"),
		accept: r.Header.Get("Accept"), userAgent: r.Header.Get("User-Agent"),
		branch: r.Header.Get(branchHeader), hasBranch: hasBranch,
	})
	handle := f.handle
	f.mu.Unlock()
	if handle != nil && handle(w, r) {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	// As netbox-branching's middleware does, before anything else but
	// /api/status/, a branch the request names must exist and be ready.
	// handle may have changed the request's headers, as a proxy can.
	if _, named := r.Header[http.CanonicalHeaderKey(branchHeader)]; named && r.URL.Path != statusPath {
		switch status, ok := f.branches[r.Header.Get(branchHeader)]; {
		case !ok:
			f.writeBadRequest(w, "Invalid branch identifier")
			return
		case status != BranchReady:
			f.writeBadRequest(w, "Selected branch is not ready.")
			return
		}
	}
	// As in NetBox, a token it refuses fails every request, since REST
	// framework authenticates before it checks permissions. Only a request
	// without a token can see /api/status/ unauthenticated.
	auth := r.Header.Get("Authorization")
	authorized := auth == f.authorization
	switch {
	case r.URL.Path == statusPath && (authorized || (auth == "" && f.statusOpen)):
		status := map[string]any{"netbox-version": f.version, "netbox-full-version": f.version, "plugins": f.plugins}
		if _, omit := f.plugins.(omitted); omit {
			delete(status, "plugins")
		}
		f.writeJSON(w, http.StatusOK, status)
	case auth == "":
		f.writeJSON(w, http.StatusForbidden, map[string]any{"detail": "Authentication credentials were not provided."})
	case !authorized:
		f.writeJSON(w, http.StatusForbidden, map[string]any{"detail": "Invalid v2 token"})
	case r.URL.Path == authCheckPath:
		f.writeJSON(w, http.StatusOK, map[string]any{"id": 1, "username": "trueup"})
	case f.denied[r.URL.Path]:
		f.writeJSON(w, http.StatusForbidden, map[string]any{"detail": "You do not have permission to perform this action."})
	case f.objects[r.URL.Path] != nil:
		f.list(w, r)
	default:
		f.writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Not found."})
	}
}

// list answers as NetBoxPagination does in cursor mode: objects with IDs from
// start, at most limit of them, capped at MAX_PAGE_SIZE, and a next link if
// the page is full.
func (f *fakeNetBox) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	start, err := strconv.ParseInt(q.Get("start"), 10, 64)
	if err != nil {
		f.t.Errorf("listing %s without a start: %s", r.URL.Path, r.URL.RawQuery)
		f.writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "no start"})
		return
	}
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit == 0 || limit > f.maxPageSize {
		limit = f.maxPageSize
	}
	var results []map[string]any
	for _, obj := range f.objects[r.URL.Path] {
		if id, _ := obj["id"].(int64); id >= start && f.matches(obj, q) && len(results) < limit {
			results = append(results, obj)
		}
	}
	var next any
	if len(results) == limit {
		last, _ := results[len(results)-1]["id"].(int64)
		q.Set("start", strconv.FormatInt(last+1, 10))
		next = "http://" + r.Host + r.URL.Path + "?" + q.Encode()
	}
	if results == nil {
		results = []map[string]any{}
	}
	f.writeJSON(w, http.StatusOK, map[string]any{"count": nil, "next": next, "previous": nil, "results": results})
}

// matches applies the _id filters, and name, in q to obj, unless the fake
// ignores them. name is exact, case included, as NetBox's is for
// netbox-branching's branches (BranchFilterSet in netbox_branching/filtersets.py).
func (f *fakeNetBox) matches(obj map[string]any, q url.Values) bool {
	if f.ignoreFilters {
		return true
	}
	for key, values := range q {
		if key == "name" {
			if name, _ := obj["name"].(string); !slices.Contains(values, name) {
				return false
			}
			continue
		}
		field, isFilter := strings.CutSuffix(key, "_id")
		if !isFilter {
			continue
		}
		ref, _ := obj[field].(map[string]any)
		id, _ := ref["id"].(int64)
		if !slices.Contains(values, strconv.FormatInt(id, 10)) {
			return false
		}
	}
	return true
}

func (f *fakeNetBox) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("API-Version", f.apiVersion)
	w.Header().Set("X-Request-ID", "11111111-2222-3333-4444-555555555555")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		f.t.Errorf("fake NetBox: encode answer: %v", err)
	}
}

// writeBadRequest answers as Django's HttpResponseBadRequest does, which
// netbox-branching's middleware answers with: 400, HTML's content type,
// though the body is plain text, and the headers NetBox's own middleware
// adds.
func (f *fakeNetBox) writeBadRequest(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("API-Version", f.apiVersion)
	w.Header().Set("X-Request-ID", "11111111-2222-3333-4444-555555555555")
	w.WriteHeader(http.StatusBadRequest)
	if _, err := io.WriteString(w, text); err != nil {
		f.t.Errorf("fake NetBox: write answer: %v", err)
	}
}

// addBranch stores a branch of netbox-branching, as its list gives it, which
// the middleware knows by its schema ID.
func (f *fakeNetBox) addBranch(id int64, name, schemaID, status string) {
	f.add(branchesPath, []int64{id}, func(int64) map[string]any {
		return map[string]any{
			"url": "http://netbox.example.com" + branchesPath + strconv.FormatInt(id, 10) + "/", "display": name,
			"name": name, "status": map[string]any{"value": status, "label": status},
			"owner":       map[string]any{"id": 1, "username": "alice", "display": "alice"},
			"description": "branch " + name, "schema_id": schemaID, "last_sync": "2026-11-26T23:21:32.709369Z",
			"merged_time": nil, "merged_by": nil, "comments": "", "tags": []any{}, "custom_fields": map[string]any{},
		}
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branches[schemaID] = status
}

// add stores objects at endpoint with the given IDs, each with a name and
// the fields that fields returns for it.
func (f *fakeNetBox) add(endpoint string, ids []int64, fields func(id int64) map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		obj := map[string]any{"id": id, "name": "object " + strconv.FormatInt(id, 10)}
		if fields != nil {
			for k, v := range fields(id) {
				obj[k] = v
			}
		}
		f.objects[endpoint] = append(f.objects[endpoint], obj)
	}
	slices.SortFunc(f.objects[endpoint], func(a, b map[string]any) int {
		return cmp.Compare(a["id"].(int64), b["id"].(int64)) //nolint:forcetypeassert // the fake stores int64 IDs
	})
}

func (f *fakeNetBox) recorded() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// paths returns the path of each request so far.
func (f *fakeNetBox) paths() []string {
	var out []string
	for _, r := range f.recorded() {
		out = append(out, r.path)
	}
	return out
}

// isStatus reports whether err is an answer with code.
func isStatus(err error, code int) bool {
	se, ok := errors.AsType[*StatusError](err)
	return ok && se.StatusCode == code
}

// ids returns 1 to n.
func ids(n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i + 1)
	}
	return out
}

// testClient is a client for a fake NetBox, with what it logged and the
// waits it would have made between retries.
type testClient struct {
	*Client
	logs   *bytes.Buffer
	sleeps *[]time.Duration
}

// newTestClient makes a client for f with opts, where URL, Token and Logger
// default to f's, a token f accepts, and a debug logger. tweak, if not nil,
// changes the client before it connects. It doesn't connect: call connect.
func newTestClient(t *testing.T, f *fakeNetBox, opts Options, tweak func(*Client)) testClient {
	t.Helper()
	if opts.URL == "" {
		opts.URL = f.srv.URL
	}
	if !opts.Token.IsSet() {
		opts.Token = config.NewSecret(testToken)
	}
	logs := &bytes.Buffer{}
	var mu sync.Mutex
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewJSONHandler(&lockedWriter{w: logs, mu: &mu}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	c, err := newClient(opts)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	sleeps := &[]time.Duration{}
	c.sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		*sleeps = append(*sleeps, d)
		mu.Unlock()
		return ctx.Err()
	}
	if tweak != nil {
		tweak(c)
	}
	return testClient{Client: c, logs: logs, sleeps: sleeps}
}

// connected is newTestClient followed by a successful connect.
func connected(t *testing.T, f *fakeNetBox, opts Options, tweak func(*Client)) testClient {
	t.Helper()
	c := newTestClient(t, f, opts, tweak)
	if err := c.connect(t.Context()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return c
}

// lockedWriter serializes writes from concurrent requests' logs.
type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
