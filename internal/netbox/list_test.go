package netbox

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const templates = "/api/dcim/interface-templates/"

// collect lists endpoint and returns the IDs it was given.
func collect(t *testing.T, c testClient, endpoint string, query url.Values) []int64 {
	t.Helper()
	var got []int64
	err := c.List(t.Context(), endpoint, query, func(raw json.RawMessage) error {
		id, err := objectID(raw)
		got = append(got, id)
		return err
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return got
}

// starts returns the start of each listing request to endpoint.
func starts(f *fakeNetBox, endpoint string) []string {
	var out []string
	for _, r := range f.recorded() {
		if r.path == endpoint {
			out = append(out, r.query.Get("start"))
		}
	}
	return out
}

func TestListPages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		objects     int
		pageSize    int
		maxPageSize int // NetBox's MAX_PAGE_SIZE
		wantStarts  []string
	}{
		{name: "pages", objects: 25, pageSize: 10, maxPageSize: 1000, wantStarts: []string{"0", "11", "21"}},
		{name: "a full last page needs one more request", objects: 20, pageSize: 10, maxPageSize: 1000, wantStarts: []string{"0", "11", "21"}},
		{name: "one page", objects: 3, pageSize: 10, maxPageSize: 1000, wantStarts: []string{"0"}},
		{name: "nothing", objects: 0, pageSize: 10, maxPageSize: 1000, wantStarts: []string{"0"}},
		{name: "NetBox's MAX_PAGE_SIZE is lower", objects: 10, pageSize: 10, maxPageSize: 4, wantStarts: []string{"0", "5", "9"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFake(t)
			f.maxPageSize = tt.maxPageSize
			f.objects[templates] = []map[string]any{}
			f.add(templates, ids(tt.objects), nil)
			c := connected(t, f, Options{}, func(c *Client) { c.pageSize = tt.pageSize })

			got := collect(t, c, templates, url.Values{"name__isw": {"object"}})
			if want := ids(tt.objects); !slices.Equal(want, got) {
				t.Errorf("IDs = %v, want %v", got, want)
			}
			if diff := cmp.Diff(tt.wantStarts, starts(f, templates)); diff != "" {
				t.Errorf("starts (-want +got):\n%s", diff)
			}
			for _, r := range f.recorded() {
				if r.path == templates && (r.query.Get("name__isw") != "object" || r.query.Get("limit") != "10") {
					t.Errorf("a page was asked for with %v", r.query)
				}
			}
		})
	}
}

// Objects added between pages, before the cursor, don't upset the listing.
func TestListWhileObjectsChange(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.add(templates, []int64{2, 4, 6, 8}, nil)
	pages := 0
	// handle runs without the fake's lock, before the fake answers.
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == templates {
			if pages++; pages == 2 {
				f.add(templates, []int64{1, 3}, nil) // below the cursor
			}
		}
		return false
	}
	c := connected(t, f, Options{}, func(c *Client) { c.pageSize = 2 })
	got := collect(t, c, templates, nil)
	if diff := cmp.Diff([]int64{2, 4, 6, 8}, got); diff != "" {
		t.Errorf("IDs (-want +got):\n%s", diff)
	}
}

func TestListStopsWhenFnFails(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.add(templates, ids(25), nil)
	c := connected(t, f, Options{}, func(c *Client) { c.pageSize = 10 })
	stop := errors.New("stop")
	n := 0
	err := c.List(t.Context(), templates, nil, func(json.RawMessage) error {
		if n++; n == 12 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || n != 12 {
		t.Errorf("List = %v after %d objects, want stop after 12", err, n)
	}
	if got := starts(f, templates); len(got) != 2 {
		t.Errorf("asked for pages from %v, want two pages", got)
	}
}

func TestListRejectsArguments(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	c := connected(t, f, Options{}, nil)
	tests := []struct {
		endpoint string
		query    url.Values
		want     string
	}{
		{endpoint: "dcim/interface-templates/", want: "an endpoint is an API path"},
		{endpoint: "/api/dcim/interface-templates", want: "an endpoint is an API path"},
		{endpoint: "/api/dcim/interface-templates/?brief=1", want: "an endpoint is an API path"},
		{endpoint: templates, query: url.Values{"offset": {"10"}}, want: "query must not set offset"},
		{endpoint: templates, query: url.Values{"ordering": {"name"}}, want: "query must not set ordering"},
		{endpoint: templates, query: url.Values{"start": {"5"}}, want: "query must not set start"},
		{endpoint: templates, query: url.Values{"limit": {"5"}}, want: "query must not set limit"},
	}
	for _, tt := range tests {
		err := c.List(t.Context(), tt.endpoint, tt.query, func(json.RawMessage) error { return nil })
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("List(%q, %v) = %v, want an error containing %q", tt.endpoint, tt.query, err, tt.want)
		}
	}
	if got := f.paths(); len(got) != 2 {
		t.Errorf("requests = %v, want only those of connecting", got)
	}
}

// A page that breaks what cursor pagination promises is refused, rather
// than listing objects twice, or forever.
func TestListRefusesBrokenPages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		page string
		want string
	}{
		{name: "IDs out of order", page: `{"next": null, "results": [{"id": 3}, {"id": 2}]}`, want: "NetBox sent object 2 after 3"},
		{name: "an ID twice", page: `{"next": null, "results": [{"id": 3}, {"id": 3}]}`, want: "NetBox sent object 3 after 3"},
		{name: "an ID before start", page: `{"next": "x", "results": [{"id": 1}, {"id": 2}]}`, want: "NetBox sent object 1 after 2"},
		{name: "no ID", page: `{"next": null, "results": [{"name": "eth0"}]}`, want: "NetBox sent an object without a positive id"},
		{name: "a zero ID", page: `{"next": null, "results": [{"id": 0}]}`, want: "NetBox sent an object without a positive id"},
		{name: "a fractional ID", page: `{"next": null, "results": [{"id": 1.5}]}`, want: "an object in the answer can't be read"},
		{name: "not an object", page: `{"next": null, "results": [7]}`, want: "an object in the answer can't be read"},
		{name: "no results", page: `{"next": null, "count": 0}`, want: "the answer has no results list"},
		{name: "results that aren't a list", page: `{"next": null, "results": {}}`, want: "decode the answer"},
		{name: "an empty page linking to another", page: `{"next": "x", "results": []}`, want: "NetBox sent an empty page that links to another"},
		{name: "more than asked for", page: `{"next": null, "results": [{"id": 3}, {"id": 4}, {"id": 5}]}`, want: "NetBox sent 3 objects, more than the 2 asked for"},
		{name: "the largest ID, and more", page: `{"next": "x", "results": [{"id": 9223372036854775807}]}`, want: "NetBox links to a page after the largest possible ID"},
		{name: "not JSON", page: `{"next": null, "results": [`, want: "decode the answer"},
		{name: "no next field", page: `{"results": [{"id": 3}]}`, want: "the answer has no next field"},
		{name: "a next that isn't a link", page: `{"next": 5, "results": [{"id": 3}]}`, want: "the answer's next field is neither a link nor null"},
		{name: "a full page without a next link", page: `{"next": null, "results": [{"id": 3}, {"id": 4}]}`, want: "NetBox sent a full page of 2 objects without a link to the next"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFake(t)
			pages := 0
			f.handle = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != templates {
					return false
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("API-Version", "4.7")
				if pages++; pages == 1 && strings.Contains(tt.name, "before start") {
					_, _ = w.Write([]byte(`{"next": "x", "results": [{"id": 1}, {"id": 2}]}`))
					return true
				}
				_, _ = w.Write([]byte(tt.page))
				return true
			}
			c := connected(t, f, Options{}, func(c *Client) { c.pageSize = 2 })
			err := c.List(t.Context(), templates, nil, func(json.RawMessage) error { return nil })
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("List = %v, want an error containing %q", err, tt.want)
			}
			if pages > 2 {
				t.Errorf("asked for %d pages", pages)
			}
		})
	}
	if math.MaxInt64 != 9223372036854775807 {
		t.Fatal("the largest ID above is wrong")
	}
}

// deviceType makes objects that belong to device type id%3+1.
func deviceType(id int64) map[string]any {
	return map[string]any{"device_type": map[string]any{"id": id%3 + 1, "model": "Model"}}
}

func TestListByParent(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.add(templates, ids(30), deviceType)
	c := connected(t, f, Options{}, func(c *Client) {
		c.pageSize = 4
		c.maxFilterBytes = len("device_type_id=1&") // one ID per request
	})

	var got []int64
	err := c.ListByParent(t.Context(), templates, "device_type", []int64{3, 1, 3}, url.Values{"name__isw": {"object"}}, func(raw json.RawMessage) error {
		id, err := parentID(raw, "device_type")
		got = append(got, id)
		return err
	})
	if err != nil {
		t.Fatalf("ListByParent: %v", err)
	}
	if len(got) != 20 || slices.Contains(got, 2) {
		t.Errorf("listed %d objects of device types %v, want 20 of 1 and 3", len(got), slices.Compact(slices.Sorted(slices.Values(got))))
	}
	var filters [][]string
	for _, r := range f.recorded() {
		if r.path == templates {
			if r.query.Get("name__isw") != "object" {
				t.Errorf("query lost name__isw: %v", r.query)
			}
			if r.query.Get("start") == "0" {
				filters = append(filters, r.query["device_type_id"])
			}
		}
	}
	if diff := cmp.Diff([][]string{{"1"}, {"3"}}, filters); diff != "" {
		t.Errorf("batches (-want +got):\n%s", diff)
	}
}

func TestListByParentChecksParents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		ignore bool
		fields func(int64) map[string]any
		want   string
	}{
		{
			name: "NetBox ignores the filter", ignore: true, fields: deviceType,
			want: "NetBox sent an object whose device_type is 2, which wasn't asked for: is device_type_id a filter of " + templates + "?",
		},
		{name: "no parent", ignore: true, want: "NetBox sent an object without device_type: if the query sets fields or omit, keep device_type"},
		{
			name: "a parent without an ID", ignore: true,
			fields: func(int64) map[string]any { return map[string]any{"device_type": "Model"} },
			want:   "NetBox sent an object whose device_type has no id",
		},
		{
			name: "a null parent", ignore: true,
			fields: func(int64) map[string]any { return map[string]any{"device_type": nil} },
			want:   "NetBox sent an object without device_type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFake(t)
			f.ignoreFilters = tt.ignore
			f.add(templates, ids(3), tt.fields)
			c := connected(t, f, Options{}, nil)
			err := c.ListByParent(t.Context(), templates, "device_type", []int64{1}, nil, func(json.RawMessage) error { return nil })
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ListByParent = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

func TestListByParentRejectsArguments(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.add(templates, ids(3), deviceType)
	c := connected(t, f, Options{}, nil)
	tests := []struct {
		parent  string
		parents []int64
		query   url.Values
		want    string
	}{
		{parent: "device-type", parents: []int64{1}, want: `"device-type" isn't a field name`},
		{parent: "device_type", parents: []int64{0, 1}, want: "parent IDs must be positive, not 0"},
		{parent: "device_type", parents: []int64{1}, query: url.Values{"device_type_id": {"2"}}, want: "query must not set device_type_id"},
		{parent: "device_type", parents: []int64{1}, query: url.Values{"offset": {"2"}}, want: "query must not set offset"},
		{parent: "device_type", parents: []int64{1}, query: url.Values{"brief": {"true"}}, want: "query must not set brief, whose objects leave out device_type"},
	}
	for _, tt := range tests {
		err := c.ListByParent(t.Context(), templates, tt.parent, tt.parents, tt.query, func(json.RawMessage) error { return nil })
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("ListByParent(%q, %v, %v) = %v, want an error containing %q", tt.parent, tt.parents, tt.query, err, tt.want)
		}
	}
	// No parents, no requests.
	if err := c.ListByParent(t.Context(), templates, "device_type", nil, nil, func(json.RawMessage) error { return nil }); err != nil {
		t.Errorf("ListByParent with no parents: %v", err)
	}
	if got := f.paths(); len(got) != 2 {
		t.Errorf("requests = %v, want only those of connecting", got)
	}
}

func TestBatches(t *testing.T) {
	t.Parallel()

	// Each "f=N&" is 4 bytes for one digit, 5 for two.
	tests := []struct {
		ids   []int64
		limit int
		want  [][]int64
	}{
		{ids: nil, limit: 100, want: nil},
		{ids: []int64{1, 2, 3}, limit: 100, want: [][]int64{{1, 2, 3}}},
		{ids: []int64{1, 2, 3}, limit: 8, want: [][]int64{{1, 2}, {3}}},
		{ids: []int64{1, 2, 10, 11}, limit: 9, want: [][]int64{{1, 2}, {10}, {11}}},
		{ids: []int64{123456}, limit: 1, want: [][]int64{{123456}}},
	}
	for _, tt := range tests {
		if diff := cmp.Diff(tt.want, batches("f", tt.ids, tt.limit)); diff != "" {
			t.Errorf("batches(%v, %d) (-want +got):\n%s", tt.ids, tt.limit, diff)
		}
	}
	// With the real limit, a batch of large IDs stays well under 4094 bytes.
	many := make([]int64, 1000)
	for i := range many {
		many[i] = 1_000_000_000 + int64(i)
	}
	for _, b := range batches("inventory_item_template_id", many, maxFilterBytes) {
		q := url.Values{}
		for _, id := range b {
			q.Add("inventory_item_template_id", strconv.FormatInt(id, 10))
		}
		if n := len(q.Encode()); n > maxFilterBytes {
			t.Errorf("a batch of %d IDs takes %d bytes, over %d", len(b), n, maxFilterBytes)
		}
	}
}

func TestCanRead(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.add("/api/dcim/manufacturers/", ids(2), nil)
	f.denied["/api/dcim/device-types/"] = true
	c := connected(t, f, Options{}, nil)

	if ok, err := c.CanRead(t.Context(), "/api/dcim/manufacturers/"); !ok || err != nil {
		t.Errorf("CanRead(manufacturers) = %v, %v; want true", ok, err)
	}
	if ok, err := c.CanRead(t.Context(), "/api/dcim/device-types/"); ok || err != nil {
		t.Errorf("CanRead(device types) = %v, %v; want false", ok, err)
	}
	if ok, err := c.CanRead(t.Context(), "/api/dcim/racks/"); ok || !isStatus(err, http.StatusNotFound) {
		t.Errorf("CanRead(racks) = %v, %v; want a 404 error", ok, err)
	}
	// A 403 from a proxy or firewall says nothing about permissions.
	f.mu.Lock()
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	f.mu.Unlock()
	if ok, err := c.CanRead(t.Context(), "/api/dcim/manufacturers/"); ok || !isStatus(err, http.StatusForbidden) {
		t.Errorf("CanRead(manufacturers) behind a firewall = %v, %v; want a 403 error", ok, err)
	}
	for _, r := range f.recorded() {
		if r.path == "/api/dcim/manufacturers/" && r.query.Encode() != "brief=true&limit=1&start=0" {
			t.Errorf("CanRead asked with %q", r.query.Encode())
		}
	}
}

func TestCheckRead(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	for _, rt := range ReadTypes() {
		f.objects[rt.Endpoint] = []map[string]any{}
	}
	f.denied["/api/dcim/device-types/"] = true
	f.denied["/api/dcim/inventory-item-templates/"] = true
	c := connected(t, f, Options{}, nil)

	got, err := c.CheckRead(t.Context(), ReadTypes())
	if err != nil {
		t.Fatalf("CheckRead: %v", err)
	}
	var denied []string
	for i, r := range got {
		if r.Type != ReadTypes()[i] {
			t.Errorf("result %d is for %v, want %v", i, r.Type, ReadTypes()[i])
		}
		if !r.Readable {
			denied = append(denied, r.Type.Name)
		}
	}
	if diff := cmp.Diff([]string{"dcim.devicetype", "dcim.inventoryitemtemplate"}, denied); diff != "" {
		t.Errorf("unreadable types (-want +got):\n%s", diff)
	}
	checks := 0
	for _, p := range f.paths() {
		if p == authCheckPath {
			checks++
		}
	}
	if checks != 2 {
		t.Errorf("checked the token %d times, want twice: on connecting, and after a 403", checks)
	}
}

// When several types fail, the first error says how many others did.
func TestCheckReadReportsEveryFailure(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	c := connected(t, f, Options{}, nil)
	_, err := c.CheckRead(t.Context(), ReadTypes()[:3]) // the fake has none of them: 404
	if !isStatus(err, http.StatusNotFound) || !strings.Contains(err.Error(), "(and 2 other object types failed too)") {
		t.Errorf("CheckRead error = %v, want a 404 and 2 others", err)
	}
	_, err = c.CheckRead(t.Context(), ReadTypes()[:2])
	if !strings.Contains(err.Error(), "(and 1 other object type failed too)") {
		t.Errorf("CheckRead error = %v, want a 404 and 1 other", err)
	}
}

// A token that stops working partway is reported as refused, not as lacking
// every permission.
func TestCheckReadWhenTheTokenExpires(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	c := connected(t, f, Options{}, nil)
	f.mu.Lock()
	f.authorization = "Bearer nbt_another.token"
	f.mu.Unlock()
	_, err := c.CheckRead(t.Context(), ReadTypes())
	if !errors.Is(err, ErrTokenRejected) {
		t.Errorf("CheckRead error = %v, want ErrTokenRejected", err)
	}
}

func TestReadTypes(t *testing.T) {
	t.Parallel()
	types := ReadTypes()
	if len(types) != 12 || types[0].Name != "dcim.manufacturer" || types[1].Name != "dcim.devicetype" {
		t.Errorf("ReadTypes = %v, want manufacturers, device types and 10 component templates", types)
	}
	for _, rt := range types {
		if err := checkListing(rt.Endpoint, nil); err != nil {
			t.Errorf("%s: %v", rt.Name, err)
		}
	}
	types[0].Name = "changed"
	if ReadTypes()[0].Name != "dcim.manufacturer" {
		t.Error("changing what ReadTypes returned changed the list")
	}
}
