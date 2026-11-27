package netbox

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestConnectChecksBranching(t *testing.T) {
	t.Parallel()

	const (
		installOrOff = "netbox.branching is on, but NetBox doesn't have netbox-branching: install netbox-branching 1.2.x, " +
			"the release line for NetBox 4.7, or turn netbox.branching off"
		onlyLine = ": TrueUp works with netbox-branching 1.2.x only, the release line for NetBox 4.7"
		turnOn   = "NetBox has netbox-branching 1.2.4, but netbox.branching is off, so TrueUp reads and writes main only: " +
			"turn netbox.branching on to work in branches"
		unsupported = ", which TrueUp doesn't work with (it works with 1.2.x only, the release line for NetBox 4.7), " +
			"and netbox.branching is off, so TrueUp reads and writes main only"
	)
	tests := []struct {
		name        string
		plugins     any
		branching   bool
		wantErr     string
		wantWarning string
		wantVersion string
	}{
		{name: "no plugins, branching off", plugins: map[string]any{}},
		{name: "no plugins, branching on", plugins: map[string]any{}, branching: true, wantErr: installOrOff},
		{name: "other plugins, branching on", plugins: map[string]any{"netbox_dns": "1.4.0"}, branching: true, wantErr: installOrOff},
		{name: "plugins not listed, branching off", plugins: omitted{}},
		{
			name: "plugins not listed, branching on", plugins: omitted{}, branching: true,
			wantErr: "netbox.branching is on, but NetBox's status doesn't list its plugins, so TrueUp can't tell whether it has netbox-branching" + onlyLine,
		},
		{name: "1.2.4, branching on", plugins: map[string]any{"netbox_branching": "1.2.4"}, branching: true, wantVersion: "1.2.4"},
		{name: "1.2.0, branching on", plugins: map[string]any{"netbox_branching": "1.2.0", "netbox_dns": nil}, branching: true, wantVersion: "1.2.0"},
		{name: "a release track", plugins: map[string]any{"netbox_branching": "1.2.4-beta"}, branching: true, wantVersion: "1.2.4-beta"},
		{name: "a pre-release", plugins: map[string]any{"netbox_branching": "1.2.5b1"}, branching: true, wantVersion: "1.2.5b1"},
		{name: "a later patch", plugins: map[string]any{"netbox_branching": "1.2.4567"}, branching: true, wantVersion: "1.2.4567"},
		{
			name: "1.3.0, branching on", plugins: map[string]any{"netbox_branching": "1.3.0"}, branching: true,
			wantErr: "netbox.branching is on, but NetBox has netbox-branching 1.3.0" + onlyLine, wantVersion: "1.3.0",
		},
		{
			name: "1.20.0, branching on", plugins: map[string]any{"netbox_branching": "1.20.0"}, branching: true,
			wantErr: "netbox.branching is on, but NetBox has netbox-branching 1.20.0" + onlyLine, wantVersion: "1.20.0",
		},
		{
			name: "1.1.3, branching on", plugins: map[string]any{"netbox_branching": "1.1.3"}, branching: true,
			wantErr: "netbox.branching is on, but NetBox has netbox-branching 1.1.3" + onlyLine, wantVersion: "1.1.3",
		},
		{
			name: "11.2.0, branching on", plugins: map[string]any{"netbox_branching": "11.2.0"}, branching: true,
			wantErr: "netbox.branching is on, but NetBox has netbox-branching 11.2.0" + onlyLine, wantVersion: "11.2.0",
		},
		{
			name: "no version, branching on", plugins: map[string]any{"netbox_branching": nil}, branching: true,
			wantErr: "netbox.branching is on, but NetBox has netbox-branching, without saying which release" + onlyLine,
		},
		{
			name: "a version that isn't one, branching on", plugins: map[string]any{"netbox_branching": "1.2.4\n\x1b[2J"}, branching: true,
			wantErr: `netbox.branching is on, but NetBox has netbox-branching "1.2.4\n\x1b[2J"` + onlyLine, wantVersion: "1.2.4\n\x1b[2J",
		},
		{
			name: "a version that only starts as one", plugins: map[string]any{"netbox_branching": "1.2"}, branching: true,
			wantErr: `netbox.branching is on, but NetBox has netbox-branching "1.2"` + onlyLine, wantVersion: "1.2",
		},
		{
			name: "1.2.4, branching off", plugins: map[string]any{"netbox_branching": "1.2.4"},
			wantWarning: turnOn, wantVersion: "1.2.4",
		},
		{
			name: "1.3.0, branching off", plugins: map[string]any{"netbox_branching": "1.3.0"},
			wantWarning: "NetBox has netbox-branching 1.3.0" + unsupported, wantVersion: "1.3.0",
		},
		{
			name: "no version, branching off", plugins: map[string]any{"netbox_branching": nil},
			wantWarning: "NetBox has netbox-branching, without saying which release, but netbox.branching is off, " +
				"so TrueUp reads and writes main only: turn netbox.branching on to work in branches",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := branchingFake(t)
			f.plugins = tt.plugins
			c := newTestClient(t, f, Options{Branching: tt.branching}, nil)
			err := c.connect(t.Context())
			if tt.wantErr != "" {
				be, ok := errors.AsType[*BranchingError](err)
				if !ok || err.Error() != tt.wantErr {
					t.Fatalf("connect error = %v, want a *BranchingError %q", err, tt.wantErr)
				}
				if be.Version != tt.wantVersion {
					t.Errorf("BranchingError.Version = %q, want %q", be.Version, tt.wantVersion)
				}
				return
			}
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			info := c.Info()
			var wantWarnings []Warning
			if tt.wantWarning != "" {
				wantWarnings = []Warning{{Key: "netbox.branching", Message: tt.wantWarning}}
				if !strings.Contains(c.logs.String(), `"level":"WARN","msg":"NetBox has netbox-branching`) {
					t.Errorf("the warning wasn't logged:\n%s", c.logs)
				}
			}
			if diff := cmp.Diff(wantWarnings, info.Warnings); diff != "" {
				t.Errorf("warnings (-want +got):\n%s", diff)
			}
			if info.Branching != tt.branching || info.BranchingVersion != tt.wantVersion || info.Branch != nil {
				t.Errorf("Info: branching %v, version %q, branch %v; want %v, %q, nil", info.Branching, info.BranchingVersion, info.Branch, tt.branching, tt.wantVersion)
			}
		})
	}
}

// A status whose plugins can't be read is an error, branching on or off.
func TestConnectRefusesPluginsItCantRead(t *testing.T) {
	t.Parallel()

	const (
		notObject = "GET /api/status/: the answer's plugins aren't a JSON object of each plugin's version: "
		notText   = "GET /api/status/: the answer gives netbox_branching's version as neither text nor null: "
	)
	tests := []struct {
		plugins any
		want    string
	}{
		{plugins: nil, want: notObject + `"null"`},
		{plugins: "netbox_branching", want: notObject + `"\"netbox_branching\""`},
		{plugins: []any{"netbox_branching"}, want: notObject + `"[\"netbox_branching\"]"`},
		{plugins: map[string]any{"netbox_branching": 1.2}, want: notText + `"1.2"`},
		{plugins: map[string]any{"netbox_branching": map[string]any{"version": "1.2.4"}}, want: notText + `"{\"version\":\"1.2.4\"}"`},
	}
	for _, tt := range tests {
		for _, branching := range []bool{false, true} {
			f := branchingFake(t)
			f.plugins = tt.plugins
			c := newTestClient(t, f, Options{Branching: branching}, nil)
			if err := c.connect(t.Context()); err == nil || err.Error() != tt.want {
				t.Errorf("plugins %v, branching %v: connect error = %v, want %q", tt.plugins, branching, err, tt.want)
			}
		}
	}
}

// The plugin's version comes from NetBox, so it is cut short, and the token
// removed from it, before it reaches Info or a message.
func TestBranchingVersionIsCleaned(t *testing.T) {
	t.Parallel()
	f := branchingFake(t)
	f.plugins = map[string]any{"netbox_branching": "1.2.4-" + testToken + strings.Repeat("x", 400)}
	c := newTestClient(t, f, Options{Branching: true}, nil)
	err := c.connect(t.Context())
	if err == nil || strings.Contains(err.Error(), testSecret) || !strings.Contains(err.Error(), "[redacted]") || len(err.Error()) > 600 {
		t.Errorf("connect error = %v, want the version cut and redacted", err)
	}
}

// branchingFake is a fake NetBox with netbox-branching 1.2.4.
func branchingFake(t *testing.T) *fakeNetBox {
	t.Helper()
	f := newFake(t)
	f.plugins = map[string]any{"netbox_branching": "1.2.4"}
	return f
}

// testBranches stores branches of every kind a test needs: two ready, two
// that aren't, one without owner, description or last sync.
func testBranches(f *fakeNetBox) {
	f.addBranch(1, "lab", "abcd1234", BranchReady)
	f.addBranch(2, "merging", "efgh5678", "merging")
	f.addBranch(3, "Lab", "ijkl9012", BranchReady)
	f.addBranch(5, "merged", "mnop3456", "merged")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[branchesPath][3]["owner"] = nil
	f.objects[branchesPath][3]["last_sync"] = nil
	delete(f.objects[branchesPath][3], "description")
}

func TestBranches(t *testing.T) {
	t.Parallel()
	f := branchingFake(t)
	testBranches(f)
	c := connected(t, f, Options{Branching: true}, func(c *Client) { c.pageSize = 2 })

	got, err := c.Branches(t.Context())
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	synced := time.Date(2026, 11, 26, 23, 21, 32, 709369000, time.UTC)
	want := []Branch{
		{ID: 1, Name: "lab", SchemaID: "abcd1234", Status: "ready", Description: "branch lab", Owner: "alice", LastSync: synced},
		{ID: 2, Name: "merging", SchemaID: "efgh5678", Status: "merging", Description: "branch merging", Owner: "alice", LastSync: synced},
		{ID: 3, Name: "Lab", SchemaID: "ijkl9012", Status: "ready", Description: "branch Lab", Owner: "alice", LastSync: synced},
		{ID: 5, Name: "merged", SchemaID: "mnop3456", Status: "merged"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Branches (-want +got):\n%s", diff)
	}
	for i, b := range got {
		if b.Ready() != (i == 0 || i == 2) {
			t.Errorf("%s: Ready() = %v", b.Name, b.Ready())
		}
	}
	// Three pages of two: the listing pages as List does.
	var pages int
	for _, r := range f.recorded() {
		if r.path == branchesPath {
			pages++
		}
	}
	if pages != 3 {
		t.Errorf("Branches read %d pages, want 3", pages)
	}
}

// A branch the client can't read is an error, rather than a branch left
// out: its schema ID goes in a header, and the rest in TrueUp's output.
func TestBranchesRefusesBranchesItCantRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(obj map[string]any)
		want   string
	}{
		{"no name", func(o map[string]any) { delete(o, "name") }, "NetBox sent branch #1 without a name"},
		{"an empty name", func(o map[string]any) { o["name"] = "" }, "NetBox sent branch #1 without a name"},
		{"no schema ID", func(o map[string]any) { delete(o, "schema_id") }, "NetBox sent branch #1 without a schema ID of eight lower case letters and digits"},
		{"a schema ID with a line break", func(o map[string]any) { o["schema_id"] = "abcd123\n" }, "NetBox sent branch #1 without a schema ID of eight lower case letters and digits"},
		{"an upper-case schema ID", func(o map[string]any) { o["schema_id"] = "ABCD1234" }, "NetBox sent branch #1 without a schema ID of eight lower case letters and digits"},
		{"a long schema ID", func(o map[string]any) { o["schema_id"] = "abcd12345" }, "NetBox sent branch #1 without a schema ID of eight lower case letters and digits"},
		{"no status", func(o map[string]any) { delete(o, "status") }, "NetBox sent branch #1 without a status"},
		{"a status without a value", func(o map[string]any) { o["status"] = map[string]any{"label": "Ready"} }, "NetBox sent branch #1 without a status"},
		{"a status that is text", func(o map[string]any) { o["status"] = "ready" }, "branch #1 can't be read"},
		{"a last sync that isn't a time", func(o map[string]any) { o["last_sync"] = "yesterday" }, `NetBox sent branch #1 with a last_sync that isn't an RFC 3339 time: "yesterday"`},
		{"an owner that is text", func(o map[string]any) { o["owner"] = "alice" }, "branch #1 can't be read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := branchingFake(t)
			f.addBranch(1, "lab", "abcd1234", BranchReady)
			tt.change(f.objects[branchesPath][0])
			c := connected(t, f, Options{Branching: true}, nil)
			_, err := c.Branches(t.Context())
			if err == nil || !strings.HasPrefix(err.Error(), "GET "+branchesPath+": "+tt.want) {
				t.Errorf("Branches error = %v, want %q", err, tt.want)
			}
		})
	}
}

// Text from NetBox in a branch is cut short, and the token removed from it.
func TestBranchTextIsCleaned(t *testing.T) {
	t.Parallel()
	f := branchingFake(t)
	f.addBranch(1, "lab", "abcd1234", BranchReady)
	f.objects[branchesPath][0]["description"] = "the token is " + testToken + " " + strings.Repeat("y", 400)
	c := connected(t, f, Options{Branching: true}, nil)
	got, err := c.Branches(t.Context())
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if d := got[0].Description; strings.Contains(d, testSecret) || !strings.Contains(d, "[redacted]") || len(d) > 310 {
		t.Errorf("description = %q, want it cut and redacted", d)
	}
}

func TestBranchesDenied(t *testing.T) {
	t.Parallel()
	f := branchingFake(t)
	f.denied[branchesPath] = true
	c := connected(t, f, Options{Branching: true}, nil)
	for name, read := range map[string]func() error{
		"Branches":   func() error { _, err := c.Branches(t.Context()); return err },
		"FindBranch": func() error { _, err := c.FindBranch(t.Context(), "lab"); return err },
	} {
		if err := read(); !errors.Is(err, ErrBranchesDenied) || !isStatus(err, http.StatusForbidden) {
			t.Errorf("%s error = %v, want ErrBranchesDenied with the 403", name, err)
		}
	}
	// A 403 from a proxy or firewall says nothing about permissions.
	f.mu.Lock()
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	f.mu.Unlock()
	if _, err := c.Branches(t.Context()); errors.Is(err, ErrBranchesDenied) || !isStatus(err, http.StatusForbidden) {
		t.Errorf("Branches behind a firewall: error = %v, want a 403 that isn't ErrBranchesDenied", err)
	}
}

func TestFindBranch(t *testing.T) {
	t.Parallel()
	f := branchingFake(t)
	testBranches(f)
	c := connected(t, f, Options{Branching: true}, nil)

	for _, name := range []string{"lab", "Lab", "merging"} {
		b, err := c.FindBranch(t.Context(), name)
		if err != nil || b.Name != name {
			t.Errorf("FindBranch(%q) = %+v, %v", name, b, err)
		}
	}
	for _, name := range []string{"LAB", "la", "lab "} {
		if b, err := c.FindBranch(t.Context(), name); !errors.Is(err, ErrNoBranch) {
			t.Errorf("FindBranch(%q) = %+v, %v; want ErrNoBranch", name, b, err)
		}
	}
	if _, err := c.FindBranch(t.Context(), ""); err == nil {
		t.Error("FindBranch(\"\") succeeded")
	}
	for _, r := range f.recorded() {
		if r.path == branchesPath && len(r.query["name"]) != 1 {
			t.Errorf("FindBranch asked with %q", r.query.Encode())
		}
	}
}

// NetBox ignores a filter it doesn't know, and would list every branch: the
// client checks the name itself.
func TestFindBranchWhenNetBoxIgnoresTheFilter(t *testing.T) {
	t.Parallel()
	f := branchingFake(t)
	testBranches(f)
	f.ignoreFilters = true
	c := connected(t, f, Options{Branching: true}, nil)
	if b, err := c.FindBranch(t.Context(), "Lab"); err != nil || b.ID != 3 {
		t.Errorf("FindBranch(Lab) = %+v, %v; want branch #3", b, err)
	}
	if _, err := c.FindBranch(t.Context(), "nothing"); !errors.Is(err, ErrNoBranch) {
		t.Errorf("FindBranch(nothing) error = %v, want ErrNoBranch", err)
	}
}

func TestFindBranchRefusesTwoOfOneName(t *testing.T) {
	t.Parallel()
	f := branchingFake(t)
	f.addBranch(1, "lab", "abcd1234", BranchReady)
	f.addBranch(2, "lab", "efgh5678", BranchReady)
	c := connected(t, f, Options{Branching: true}, nil)
	want := `GET /api/plugins/branching/branches/: NetBox sent 2 branches named "lab", though names are unique`
	if _, err := c.FindBranch(t.Context(), "lab"); err == nil || err.Error() != want {
		t.Errorf("FindBranch error = %v, want %q", err, want)
	}
}

// A client bound to a branch names it on every request but those that read
// branches; the client it came from names none.
func TestInBranch(t *testing.T) {
	t.Parallel()
	f := branchingFake(t)
	testBranches(f)
	f.add("/api/dcim/device-types/", ids(3), nil)
	f.add("/api/dcim/interface-templates/", []int64{10, 11}, func(id int64) map[string]any {
		return map[string]any{"device_type": map[string]any{"id": id - 9}}
	})
	f.denied["/api/dcim/rear-port-templates/"] = true
	c := connected(t, f, Options{Branching: true}, nil)
	before := len(f.recorded())

	b, err := c.FindBranch(t.Context(), "lab")
	if err != nil {
		t.Fatal(err)
	}
	bc, err := c.InBranch(t.Context(), b)
	if err != nil {
		t.Fatalf("InBranch: %v", err)
	}
	ctx := t.Context()
	if err := bc.List(ctx, "/api/dcim/device-types/", nil, func(json.RawMessage) error { return nil }); err != nil {
		t.Errorf("List in the branch: %v", err)
	}
	if err := bc.ListByParent(ctx, "/api/dcim/interface-templates/", "device_type", []int64{1, 2}, nil, func(json.RawMessage) error { return nil }); err != nil {
		t.Errorf("ListByParent in the branch: %v", err)
	}
	// A type it can't read makes CheckRead check the token again, in the
	// branch too.
	checks, err := bc.CheckRead(ctx, []ObjectType{{Name: "dcim.rearporttemplate", Endpoint: "/api/dcim/rear-port-templates/"}})
	if err != nil || len(checks) != 1 || checks[0].Readable {
		t.Errorf("CheckRead in the branch = %+v, %v", checks, err)
	}
	if _, err := bc.Branches(ctx); err != nil {
		t.Errorf("Branches from the branch: %v", err)
	}
	if _, err := bc.FindBranch(ctx, "merging"); err != nil {
		t.Errorf("FindBranch from the branch: %v", err)
	}
	if err := c.List(ctx, "/api/dcim/device-types/", nil, func(json.RawMessage) error { return nil }); err != nil {
		t.Errorf("List on main: %v", err)
	}

	reqs := f.recorded()[before:]
	var inBranch []string
	probes := 0
	for _, r := range reqs {
		switch {
		case r.hasBranch && r.branch == branchProbe:
			// InBranch's check that the header reaches NetBox.
			probes++
		case r.path == branchesPath && r.hasBranch:
			t.Errorf("%s?%s named branch %q", r.path, r.query.Encode(), r.branch)
		case r.hasBranch && r.branch != "abcd1234":
			t.Errorf("%s named branch %q, want abcd1234", r.path, r.branch)
		case r.hasBranch:
			inBranch = append(inBranch, r.path)
		}
	}
	want := []string{"/api/dcim/device-types/", "/api/dcim/interface-templates/", "/api/dcim/rear-port-templates/", authCheckPath}
	if diff := cmp.Diff(want, inBranch); diff != "" {
		t.Errorf("requests in the branch (-want +got):\n%s", diff)
	}
	if probes != 1 {
		t.Errorf("InBranch checked the header %d times, want once", probes)
	}
	if last := reqs[len(reqs)-1]; last.hasBranch {
		t.Errorf("the main client's last request named branch %q", last.branch)
	}

	if info := bc.Info(); info.Branch == nil || *info.Branch != b {
		t.Errorf("Info().Branch = %v, want %+v", info.Branch, b)
	}
	if c.Info().Branch != nil {
		t.Error("InBranch bound the client it came from")
	}
	// Info's branch is a copy.
	bc.Info().Branch.SchemaID = "zzzzzzzz"
	if bc.Info().Branch.SchemaID != "abcd1234" {
		t.Error("changing Info().Branch changed the client's")
	}
}

// A client bound to a branch shares its parent's limit on requests in
// flight, so that netbox.max_requests caps both together.
func TestInBranchSharesTheLimit(t *testing.T) {
	t.Parallel()
	f := branchingFake(t)
	f.addBranch(1, "lab", "abcd1234", BranchReady)
	f.add("/api/dcim/device-types/", ids(1), nil)
	c := connected(t, f, Options{Branching: true, MaxRequests: 1}, nil)
	bc, err := c.InBranch(t.Context(), Branch{Name: "lab", SchemaID: "abcd1234", Status: BranchReady})
	if err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	inFlight := make(chan struct{}, 2)
	f.mu.Lock()
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/dcim/device-types/" {
			inFlight <- struct{}{}
			<-release
		}
		return false
	}
	f.mu.Unlock()
	var wg sync.WaitGroup
	for _, client := range []*Client{c.Client, bc} {
		wg.Go(func() {
			if err := client.List(t.Context(), "/api/dcim/device-types/", nil, func(json.RawMessage) error { return nil }); err != nil {
				t.Errorf("List: %v", err)
			}
		})
	}
	<-inFlight
	select {
	case <-inFlight:
		t.Error("two requests were in flight at once, with netbox.max_requests 1")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	wg.Wait()
}

// A proxy that drops the branch header would have every read come from main;
// InBranch finds out, and refuses to bind the client.
func TestInBranchWhenTheHeaderIsDropped(t *testing.T) {
	t.Parallel()
	f := branchingFake(t)
	f.addBranch(1, "lab", "abcd1234", BranchReady)
	c := connected(t, f, Options{Branching: true}, nil)
	f.mu.Lock()
	f.handle = func(_ http.ResponseWriter, r *http.Request) bool {
		r.Header.Del(branchHeader)
		return false
	}
	f.mu.Unlock()
	if _, err := c.InBranch(t.Context(), Branch{Name: "lab", SchemaID: "abcd1234", Status: BranchReady}); !errors.Is(err, ErrBranchHeaderIgnored) {
		t.Errorf("InBranch behind a proxy that drops the header: %v, want ErrBranchHeaderIgnored", err)
	}
	// A NetBox that can't be reached for the check fails InBranch too.
	f.mu.Lock()
	f.handle = func(w http.ResponseWriter, _ *http.Request) bool {
		w.WriteHeader(http.StatusBadGateway)
		return true
	}
	f.mu.Unlock()
	if _, err := c.InBranch(t.Context(), Branch{Name: "lab", SchemaID: "abcd1234", Status: BranchReady}); err == nil || errors.Is(err, ErrBranchHeaderIgnored) || !isStatus(err, http.StatusBadGateway) {
		t.Errorf("InBranch with NetBox failing: %v, want the 502", err)
	}
}

func TestInBranchRefuses(t *testing.T) {
	t.Parallel()
	f := branchingFake(t)
	c := connected(t, f, Options{Branching: true}, nil)
	for _, id := range []string{"", "abcd123", "abcd1234\r\nX-Other: 1", "ABCD1234"} {
		if _, err := c.InBranch(t.Context(), Branch{Name: "lab", SchemaID: id, Status: BranchReady}); err == nil {
			t.Errorf("InBranch with schema ID %q succeeded", id)
		}
	}
	for _, status := range []string{"merging", "new", "", "Ready"} {
		if _, err := c.InBranch(t.Context(), Branch{Name: "lab", SchemaID: "abcd1234", Status: status}); err == nil {
			t.Errorf("InBranch with a branch whose status is %q succeeded", status)
		}
	}
	// A client connected with branching off reads main only.
	off := connected(t, f, Options{}, nil)
	if _, err := off.InBranch(t.Context(), Branch{Name: "lab", SchemaID: "abcd1234", Status: BranchReady}); err == nil {
		t.Error("InBranch on a client with branching off succeeded")
	}
}

// A name is kept whole, so that a long one, of characters that take several
// bytes each, is found by its name; one longer than netbox-branching allows
// is refused.
func TestBranchNames(t *testing.T) {
	t.Parallel()
	f := branchingFake(t)
	long := strings.Repeat("\U0001f333", 100) // 400 bytes, more than cut keeps
	f.addBranch(1, long, "abcd1234", BranchReady)
	c := connected(t, f, Options{Branching: true}, nil)
	if b, err := c.FindBranch(t.Context(), long); err != nil || b.Name != long {
		t.Errorf("FindBranch with a name of 100 characters = %+v, %v", b, err)
	}
	f.addBranch(2, long+"x", "efgh5678", BranchReady)
	if _, err := c.Branches(t.Context()); err == nil || !strings.Contains(err.Error(), "NetBox sent branch #2 with a name longer than the 100 characters netbox-branching allows") {
		t.Errorf("Branches with a name of 101 characters: %v", err)
	}
}

// netbox-branching refuses a request in a branch that has gone, or isn't
// ready, with a plain-text 400; the client reports it as such, and doesn't
// retry it.
func TestBranchRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, status, want string
	}{
		{"not ready", "syncing", "Selected branch is not ready."},
		{"deleted", "", "Invalid branch identifier"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := branchingFake(t)
			f.addBranch(1, "lab", "abcd1234", BranchReady)
			f.add("/api/dcim/device-types/", ids(1), nil)
			c := connected(t, f, Options{Branching: true, Retries: 3}, nil)
			bc, err := c.FindBranch(t.Context(), "lab")
			if err != nil {
				t.Fatal(err)
			}
			client, err := c.InBranch(t.Context(), bc)
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			if tt.status == "" {
				delete(f.branches, "abcd1234")
			} else {
				f.branches["abcd1234"] = tt.status
			}
			f.mu.Unlock()
			before := len(f.recorded())

			err = client.List(t.Context(), "/api/dcim/device-types/", nil, func(json.RawMessage) error { return nil })
			re, ok := errors.AsType[*BranchRefusedError](err)
			if !ok || !errors.Is(err, ErrBranchRefused) {
				t.Fatalf("List error = %v, want a *BranchRefusedError", err)
			}
			if re.Message != tt.want || re.Branch != bc || re.Path != "/api/dcim/device-types/" {
				t.Errorf("BranchRefusedError = %+v", re)
			}
			want := `GET /api/dcim/device-types/: NetBox refused branch "lab" (schema ID abcd1234): "` + tt.want +
				`"; it was ready when TrueUp chose it, so someone may be synchronizing, merging or migrating it, or have deleted it`
			if err.Error() != want {
				t.Errorf("error = %q, want %q", err, want)
			}
			if n := len(f.recorded()) - before; n != 1 {
				t.Errorf("%d requests, want 1: the refusal isn't retried", n)
			}
			// CanRead, which takes a 403 as an answer, fails on it.
			if ok, err := client.CanRead(t.Context(), "/api/dcim/device-types/"); ok || !errors.Is(err, ErrBranchRefused) {
				t.Errorf("CanRead = %v, %v; want ErrBranchRefused", ok, err)
			}
		})
	}
}

// Only netbox-branching's own refusal is one: NetBox's JSON 400, a 400 from
// something that isn't NetBox, and a plain-text 400 to a request that named
// no branch are answers like any other.
func TestOtherBadRequestsAreNotBranchRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		answer func(w http.ResponseWriter, f *fakeNetBox)
		branch bool
	}{
		{"NetBox's JSON", func(w http.ResponseWriter, f *fakeNetBox) {
			f.writeJSON(w, http.StatusBadRequest, map[string]any{"device_type_id": []string{"Select a valid choice."}})
		}, true},
		{"a proxy's text", func(w http.ResponseWriter, _ *fakeNetBox) { http.Error(w, "Bad request", http.StatusBadRequest) }, true},
		{"text on main", func(w http.ResponseWriter, f *fakeNetBox) {
			f.writeBadRequest(w, "Invalid branch identifier")
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := branchingFake(t)
			f.addBranch(1, "lab", "abcd1234", BranchReady)
			c := connected(t, f, Options{Branching: true}, nil)
			f.mu.Lock()
			f.handle = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/api/dcim/device-types/" {
					return false
				}
				tt.answer(w, f)
				return true
			}
			f.mu.Unlock()
			client := c.Client
			if tt.branch {
				var err error
				if client, err = c.InBranch(t.Context(), Branch{Name: "lab", SchemaID: "abcd1234", Status: BranchReady}); err != nil {
					t.Fatal(err)
				}
			}
			err := client.List(t.Context(), "/api/dcim/device-types/", url.Values{}, func(json.RawMessage) error { return nil })
			if errors.Is(err, ErrBranchRefused) || !isStatus(err, http.StatusBadRequest) {
				t.Errorf("List error = %v, want a 400 StatusError", err)
			}
		})
	}
}

// The refusal's text comes from NetBox, so it is cut short, and the token
// removed from it.
func TestBranchRefusalIsCleaned(t *testing.T) {
	t.Parallel()
	f := branchingFake(t)
	c := connected(t, f, Options{Branching: true}, nil)
	f.mu.Lock()
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		f.writeBadRequest(w, "no "+testToken+" "+strings.Repeat("z", 400))
		return true
	}
	f.mu.Unlock()
	bc, err := c.InBranch(t.Context(), Branch{Name: "lab", SchemaID: "abcd1234", Status: BranchReady})
	if err != nil {
		t.Fatal(err)
	}
	err = bc.List(t.Context(), "/api/dcim/device-types/", nil, func(json.RawMessage) error { return nil })
	re, ok := errors.AsType[*BranchRefusedError](err)
	if !ok || strings.Contains(err.Error(), testSecret) || !strings.Contains(re.Message, "[redacted]") || len(re.Message) > 310 {
		t.Errorf("List error = %v, want a refusal cut and redacted", err)
	}
}
