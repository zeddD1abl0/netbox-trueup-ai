package main

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/compare"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/engine"
)

// compareNetBox is a fake NetBox 4.7 holding manufacturers and device types,
// which it lists as NetBox does, with cursor pagination and the
// manufacturer_id filter, answering 403 for the endpoints in denied. It
// records the query of every listing.
type compareNetBox struct {
	srv           *httptest.Server
	manufacturers []map[string]any
	deviceTypes   []map[string]any
	// templates are the component templates, by endpoint.
	templates map[string][]map[string]any
	denied    []string
	// refused is an endpoint NetBox refuses to list, as when a device type
	// asked for no longer exists.
	refused string
	// onManufacturers, if set, runs before manufacturers are listed, once
	// compare has read the sources' definitions.
	onManufacturers func()

	// With plugins set, /api/status/ lists them. branches are
	// netbox-branching's, as its list gives them; a request naming one by
	// its schema ID reads branchManufacturers and branchDeviceTypes, and
	// netbox-branching's middleware refuses it unless the branch is ready.
	plugins             map[string]any
	branches            []map[string]any
	branchManufacturers []map[string]any
	branchDeviceTypes   []map[string]any

	mu      sync.Mutex
	queries map[string][]url.Values // by path
	// branchHeaders holds each request's X-NetBox-Branch header, or "main"
	// for none, by path.
	branchHeaders map[string][]string
}

// nbBranch is a branch as netbox-branching's API gives it.
func nbBranch(id int, name, schemaID, status string) map[string]any {
	return map[string]any{
		"id": id, "url": "http://netbox.example.com/api/plugins/branching/branches/" + strconv.Itoa(id) + "/", "display": name,
		"name": name, "status": choice(status), "owner": map[string]any{"id": 1, "username": "alice"},
		"description": "branch " + name, "schema_id": schemaID, "last_sync": "2026-11-26T23:21:32.709369Z",
	}
}

// setBranchStatus changes a branch's status, as netbox-branching does when a
// person synchronizes or merges it.
func (f *compareNetBox) setBranchStatus(schemaID, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.branches {
		if b["schema_id"] == schemaID {
			b["status"] = choice(status)
		}
	}
}

// branchStatus returns the status of the branch with schemaID, or "" if there
// is none. f.mu must be held.
func (f *compareNetBox) branchStatus(schemaID string) string {
	for _, b := range f.branches {
		if b["schema_id"] == schemaID {
			status, _ := b["status"].(map[string]any)
			value, _ := status["value"].(string)
			return value
		}
	}
	return ""
}

// branchesRead returns the X-NetBox-Branch header of each request for path,
// or "main" for a request without one.
func (f *compareNetBox) branchesRead(path string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.branchHeaders[path])
}

func newCompareNetBox(t *testing.T, manufacturers, deviceTypes []map[string]any, denied ...string) *compareNetBox {
	t.Helper()
	f := &compareNetBox{
		manufacturers: manufacturers, deviceTypes: deviceTypes, denied: denied,
		queries: map[string][]url.Values{}, branchHeaders: map[string][]string{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("compare sent %s %s", r.Method, r.URL.Path)
		}
		answer := func(status int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("API-Version", "4.7")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v)
		}
		q := r.URL.Query()
		branch, inBranch := r.Header[http.CanonicalHeaderKey("X-NetBox-Branch")]
		header := "main"
		if inBranch {
			header = strings.Join(branch, ",")
		}
		f.mu.Lock()
		f.queries[r.URL.Path] = append(f.queries[r.URL.Path], q)
		f.branchHeaders[r.URL.Path] = append(f.branchHeaders[r.URL.Path], header)
		status := f.branchStatus(header)
		f.mu.Unlock()
		// As netbox-branching's middleware does, before anything but
		// /api/status/, with Django's plain-text 400.
		if inBranch && r.URL.Path != "/api/status/" && status != "ready" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("API-Version", "4.7")
			w.WriteHeader(http.StatusBadRequest)
			if status == "" {
				_, _ = io.WriteString(w, "Invalid branch identifier")
			} else {
				_, _ = io.WriteString(w, "Selected branch is not ready.")
			}
			return
		}
		manufacturers, deviceTypes := f.manufacturers, f.deviceTypes
		if inBranch {
			manufacturers, deviceTypes = f.branchManufacturers, f.branchDeviceTypes
		}
		var objects []map[string]any
		switch {
		case r.Header.Get("Authorization") != "Bearer "+checkToken:
			answer(http.StatusForbidden, map[string]any{"detail": "Invalid v2 token"})
			return
		case r.URL.Path == "/api/status/":
			status := map[string]any{"netbox-version": "4.7.2"}
			if f.plugins != nil {
				status["plugins"] = f.plugins
			}
			answer(http.StatusOK, status)
			return
		case r.URL.Path == "/api/authentication-check/":
			answer(http.StatusOK, map[string]any{"username": "trueup"})
			return
		case slices.Contains(f.denied, r.URL.Path):
			answer(http.StatusForbidden, map[string]any{"detail": "You do not have permission to perform this action."})
			return
		case r.URL.Path == f.refused:
			answer(http.StatusBadRequest, map[string]any{"device_type_id": []string{"Select a valid choice. 11 is not one of the available choices."}})
			return
		case r.URL.Path == "/api/dcim/manufacturers/":
			if f.onManufacturers != nil {
				f.onManufacturers()
			}
			objects = manufacturers
		case r.URL.Path == "/api/plugins/branching/branches/" && f.plugins != nil:
			f.mu.Lock()
			for _, b := range f.branches {
				if names, ok := q["name"]; !ok || slices.Contains(names, b["name"].(string)) { //nolint:forcetypeassert // the fake's own branches
					objects = append(objects, maps.Clone(b))
				}
			}
			f.mu.Unlock()
		case r.URL.Path == "/api/dcim/device-types/":
			ids := q["manufacturer_id"]
			for _, o := range deviceTypes {
				maker, _ := o["manufacturer"].(map[string]any)
				id, _ := maker["id"].(int)
				if len(ids) == 0 || slices.Contains(ids, strconv.Itoa(id)) {
					objects = append(objects, o)
				}
			}
		case strings.HasSuffix(r.URL.Path, "-templates/") && strings.HasPrefix(r.URL.Path, "/api/dcim/"):
			ids := q["device_type_id"]
			for _, o := range f.templates[r.URL.Path] {
				dt, _ := o["device_type"].(map[string]any)
				id, _ := dt["id"].(int)
				if len(ids) == 0 || slices.Contains(ids, strconv.Itoa(id)) {
					objects = append(objects, o)
				}
			}
		default:
			answer(http.StatusNotFound, map[string]any{"detail": "Not found."})
			return
		}
		start, _ := strconv.Atoi(q.Get("start"))
		results := []map[string]any{}
		for _, o := range objects {
			if id, _ := o["id"].(int); id >= start {
				results = append(results, o)
			}
		}
		answer(http.StatusOK, map[string]any{"count": nil, "next": nil, "previous": nil, "results": results})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *compareNetBox) listed(path string) []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.queries[path])
}

// nbManufacturer is a manufacturer as NetBox's API gives it.
func nbManufacturer(id int, name, slug string) map[string]any {
	return map[string]any{
		"id": id, "url": "http://192.0.2.1/api/dcim/manufacturers/" + strconv.Itoa(id) + "/", "display": name,
		"name": name, "slug": slug, "description": "", "comments": "", "tags": []any{}, "devicetype_count": 1,
	}
}

// nbDeviceType is a device type as NetBox's API gives it, with fields
// replaced.
func nbDeviceType(id int, maker map[string]any, modelName, slug string, fields map[string]any) map[string]any {
	o := map[string]any{
		"id": id, "display": modelName,
		"manufacturer": map[string]any{
			"id": maker["id"], "url": maker["url"], "display": maker["name"], "name": maker["name"], "slug": maker["slug"], "description": "",
		},
		"model": modelName, "slug": slug, "part_number": "", "u_height": 1.0, "exclude_from_utilization": false,
		"is_full_depth": true, "subdevice_role": nil, "airflow": nil, "cooling_method": nil, "weight": nil,
		"weight_unit": nil, "end_of_life": nil, "description": "", "comments": "", "tags": []any{}, "device_count": 0,
	}
	maps.Copy(o, fields)
	return o
}

// templateFields are the fields of each kind of component template, by
// endpoint, as NetBox's import leaves those a file doesn't set.
var templateFields = map[string]map[string]any{
	"/api/dcim/power-port-templates/": {"type": nil, "maximum_draw": nil, "allocated_draw": nil},
	"/api/dcim/interface-templates/": {
		"type": nil, "channels": nil, "channel_id": nil, "enabled": true, "mgmt_only": false,
		"parent": nil, "bridge": nil, "poe_mode": nil, "poe_type": nil, "rf_role": nil,
	},
	"/api/dcim/module-bay-templates/": {"position": "", "enabled": true, "module_bay_types": []any{}},
}

// nbTemplate is a component template as NetBox's API gives it, from
// endpoint, of device type dt, with fields replaced.
func nbTemplate(endpoint string, id int, dt map[string]any, name string, fields map[string]any) map[string]any {
	o := map[string]any{
		"id": id, "display": name, "module_type": nil, "name": name, "label": "", "description": "",
		"device_type": map[string]any{
			"id": dt["id"], "display": dt["model"], "manufacturer": dt["manufacturer"], "model": dt["model"], "slug": dt["slug"], "description": "",
		},
	}
	maps.Copy(o, templateFields[endpoint])
	maps.Copy(o, fields)
	return o
}

// choice is a choice field's value as NetBox's API gives it.
func choice(value string) map[string]any {
	return map[string]any{"value": value, "label": value}
}

// libraryNetBox holds what NetBox's import would make of the test library's
// Netgear GS105 and Huawei NE8000-X8, with the NE8000-X8 changed since: its
// height, a module bay deleted and another added. And a Netgear whose slug
// NETGEAR's would take.
func libraryNetBox(t *testing.T, denied ...string) *compareNetBox {
	t.Helper()
	netgear, huawei := nbManufacturer(1, "Netgear", "netgear"), nbManufacturer(2, "Huawei", "huawei-technologies")
	nb := newCompareNetBox(t, []map[string]any{netgear, huawei}, []map[string]any{
		nbDeviceType(10, netgear, "GS105", "netgear-gs105", map[string]any{
			"is_full_depth": false,
			"comments":      "[Gigabit Unmanaged Switch Series GS105](https://www.netgear.com/business/wired/switches/unmanaged/gs105/)",
		}),
		nbDeviceType(11, huawei, "NE8000-X8", "huawei-ne8000-x8", map[string]any{
			"part_number": "02351930", "u_height": 15.5, "airflow": map[string]any{"value": "front-to-rear", "label": "Front to rear"},
			"comments": "[Huawei NE8000 X8](https://e.huawei.com/en/products/routers/ne8000)",
			"weight":   94.0, "weight_unit": map[string]any{"value": "kg", "label": "Kilograms"},
			"description": "edited in NetBox",
		}),
	}, denied...)
	gs105, ne8000 := nb.deviceTypes[0], nb.deviceTypes[1]
	const ports, ifaces, bays = "/api/dcim/power-port-templates/", "/api/dcim/interface-templates/", "/api/dcim/module-bay-templates/"
	nb.templates = map[string][]map[string]any{
		ports: {nbTemplate(ports, 100, gs105, "PSU0", map[string]any{"type": choice("dc-terminal")})},
	}
	for i := 1; i <= 5; i++ {
		nb.templates[ifaces] = append(nb.templates[ifaces], nbTemplate(ifaces, 100+i, gs105, "GigabitEthernet/"+strconv.Itoa(i), map[string]any{"type": choice("1000base-t")}))
	}
	// The library's bays, but LPU 8, and Fan 1, which it hasn't got.
	type bay struct{ name, position string }
	var all []bay
	for i := 1; i <= 7; i++ {
		all = append(all, bay{"LPU " + strconv.Itoa(i), strconv.Itoa(i)})
	}
	all = append(all, bay{"MPU 9", "9"}, bay{"MPU 10", "10"})
	for i := 1; i <= 8; i++ {
		all = append(all, bay{"SFU " + strconv.Itoa(i), "SFU" + strconv.Itoa(i)})
	}
	all = append(all, bay{"Fan 1", ""})
	for i, b := range all {
		nb.templates[bays] = append(nb.templates[bays], nbTemplate(bays, 200+i, ne8000, b.name, map[string]any{"position": b.position}))
	}
	return nb
}

// compareEnv configures the test library as a source, fetched, and NetBox.
func compareEnv(t *testing.T, nb *compareNetBox) (env []string, dataDir, commit string, configure func(*app)) {
	t.Helper()
	repo, pool, commit := libraryRepo(t, nil)
	env, dataDir = sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repo+`","format":"devicetype-library"}]`)
	fetched(t, pool, env)
	return append(env, checkEnv(t, nb.srv, checkToken)...), dataDir, commit, trust(pool)
}

func TestCompareText(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, _, commit, configure := compareEnv(t, nb)
	r := runCLIApp(t, nil, configure, env, "compare")
	if r.code != exitOK || r.stderr != "" {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	want := `NetBox:  ` + nb.srv.URL + `, release 4.7.2

SOURCE  REF     VERSION                                   COMPARED  IN STEP  DIFFERING  ABSENT  CONFLICTS  INVALID  WARNINGS
lib     master  ` + commit + `  5         3        1          0       1          0        1

5 objects compared: 3 in step, 1 differing, 0 absent, 1 in conflict.

1 definition NetBox hasn't got wasn't compared: --all compares every definition, and --manufacturer or --model those named.

OUTCOME    TYPE          MANUFACTURER  MODEL      NETBOX  DIFFERENCES  SOURCE  FROM
conflict   manufacturer  NETGEAR       -          -       -            lib     1 file
candidate  device type   Huawei        NE8000-X8  #11     2            lib     device-types/Huawei/NE8000-X8.yaml

Conflicts:
  manufacturer "NETGEAR": lib (1 file): its slug matches NetBox's manufacturer #1, "Netgear"

Differences:
  device type Huawei "NE8000-X8", NetBox #11, in lib (device-types/Huawei/NE8000-X8.yaml):
    FIELD                 lib      NETBOX
    u_height              16       15.5
    module_bays["LPU 8"]  present  missing

Only in NetBox, which TrueUp leaves as they are:
  device type Huawei "NE8000-X8", NetBox #11: module_bays["Fan 1"] (#217)

Warnings:
  lib: manufacturers "NETGEAR" and "Netgear" would both get the slug "netgear", which NetBox gives to one manufacturer only: create one of them in NetBox with another slug, or spell them alike in the source
`
	if diff := cmp.Diff(want, r.stdout); diff != "" {
		t.Errorf("compare (-want +got):\n%s", diff)
	}

	// Every manufacturer, and the device types of those the source names
	// that NetBox has, with the fields the specs describe.
	if got := nb.listed("/api/dcim/manufacturers/"); len(got) != 1 || got[0].Get("fields") != "id,name,slug,description,comments" {
		t.Errorf("manufacturers listed with %v", got)
	}
	got := nb.listed("/api/dcim/device-types/")
	if len(got) != 1 || !slices.Equal(got[0]["manufacturer_id"], []string{"1", "2"}) ||
		got[0].Get("fields") != "id,manufacturer,model,slug,part_number,u_height,exclude_from_utilization,is_full_depth,subdevice_role,airflow,cooling_method,weight,weight_unit,end_of_life,description,comments" {
		t.Errorf("device types listed with %v", got)
	}
	// The templates of the device types matched, with their specs' fields.
	for endpoint, fields := range map[string]string{
		"/api/dcim/console-port-templates/":        "id,device_type,name,label,type,description",
		"/api/dcim/console-server-port-templates/": "id,device_type,name,label,type,description",
		"/api/dcim/power-port-templates/":          "id,device_type,name,label,type,maximum_draw,allocated_draw,description",
		"/api/dcim/power-outlet-templates/":        "id,device_type,name,label,type,color,power_port,feed_leg,description",
		"/api/dcim/interface-templates/":           "id,device_type,name,label,type,channels,channel_id,enabled,mgmt_only,description,parent,bridge,poe_mode,poe_type,rf_role",
		"/api/dcim/rear-port-templates/":           "id,device_type,name,label,type,color,positions,description",
		// A front port's mappings, nested in it.
		"/api/dcim/front-port-templates/": "id,device_type,name,label,type,color,positions,description,rear_ports",
		"/api/dcim/module-bay-templates/": "id,device_type,name,label,position,enabled,description",
		"/api/dcim/device-bay-templates/": "id,device_type,name,label,enabled,description",
	} {
		got := nb.listed(endpoint)
		if len(got) != 1 || !slices.Equal(got[0]["device_type_id"], []string{"10", "11"}) || got[0].Get("fields") != fields {
			t.Errorf("%s listed with %v", endpoint, got)
		}
	}
}

// compareResult decodes compare's JSON output, with values as encoding/json
// reads them.
type compareResult struct {
	OK     bool   `json:"ok"`
	Scope  string `json:"scope"`
	NetBox struct {
		URL     string         `json:"url"`
		Version string         `json:"version"`
		Branch  *branchRefJSON `json:"branch"`
	} `json:"netbox"`
	ComparedAt string `json:"compared_at"`
	Sources    []struct {
		Name        string  `json:"name"`
		Version     *string `json:"version"`
		OK          bool    `json:"ok"`
		Error       *string `json:"error"`
		Definitions int     `json:"definitions"`
		Compared    int     `json:"compared"`
		InStep      int     `json:"in_step"`
		Differing   int     `json:"differing"`
		Absent      int     `json:"absent"`
		Conflicts   int     `json:"conflicts"`
	} `json:"sources"`
	Objects  []objectJSON  `json:"objects"`
	Warnings []warningJSON `json:"warnings"`
}

// objectJSON is what the tests check of a comparison in compare's JSON.
type objectJSON struct {
	Type     string `json:"type"`
	Identity struct {
		Name         string `json:"name"`
		Model        string `json:"model"`
		Manufacturer struct {
			Name string `json:"name"`
		} `json:"manufacturer"`
	} `json:"identity"`
	Outcome     string `json:"outcome"`
	InStep      bool   `json:"in_step"`
	NetBoxID    *int   `json:"netbox_id"`
	Differences int    `json:"differences"`
	Unresolved  []struct {
		Field   string  `json:"field"`
		Outcome *string `json:"outcome"`
	} `json:"unresolved"`
	Columns []struct {
		Kind    string  `json:"kind"`
		Source  string  `json:"source"`
		Path    *string `json:"path"`
		Outcome string  `json:"outcome"`
	} `json:"columns"`
	Rows []struct {
		// Path's steps are a field's name, or a child's key as an object.
		Path     []any `json:"path"`
		Differs  bool  `json:"differs"`
		Managed  bool  `json:"managed"`
		NetBoxID *int  `json:"netbox_id"`
	} `json:"rows"`
}

// name is an object's model, or a manufacturer's name.
func (o objectJSON) name() string {
	if o.Identity.Model != "" {
		return o.Identity.Model
	}
	return o.Identity.Name
}

// key is the key recordedResults gives the object: its path, or its
// identity for a manufacturer derived from files.
func (o objectJSON) key(t *testing.T) string {
	t.Helper()
	if p := o.Columns[0].Path; p != nil {
		return *p
	}
	identity, err := json.Marshal(map[string]string{"name": o.Identity.Name})
	if err != nil {
		t.Fatal(err)
	}
	return string(identity)
}

func decodeCompare(t *testing.T, stdout string) compareResult {
	t.Helper()
	var got compareResult
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("compare -o json printed %q: %v", stdout, err)
	}
	return got
}

// outcomes lists each object as its outcome, model or name, and difference
// count.
func outcomes(r compareResult) []string {
	var out []string
	for _, o := range r.Objects {
		out = append(out, o.Outcome+" "+o.name()+" "+strconv.Itoa(o.Differences))
	}
	return out
}

// recordOf returns what the database records of an object compare printed
// as JSON, which has one definition: the object without what only an
// object has, its sources defining it more than once, its clashes, and its
// definition column's own comparison, which is the record's.
func recordOf(t *testing.T, object any) any {
	t.Helper()
	data, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	var o map[string]any
	if err := json.Unmarshal(data, &o); err != nil {
		t.Fatal(err)
	}
	columns, _ := o["columns"].([]any)
	if len(columns) == 0 {
		t.Fatalf("an object without columns: %s", data)
	}
	for i, c := range columns {
		column, _ := c.(map[string]any)
		if column["kind"] != "definition" {
			continue
		}
		if i > 0 {
			t.Fatalf("an object of more than one definition: %s", data)
		}
		for _, f := range []string{"outcome", "in_step", "differences", "collisions"} {
			delete(column, f)
		}
	}
	delete(o, "duplicated_in")
	delete(o, "clashes")
	return o
}

// recordedResults returns the comparisons recorded of the source lib, each
// as its result decoded, by its path, or its identity for a derived
// definition.
func recordedResults(t *testing.T, dataDir string) map[string]any {
	t.Helper()
	out := map[string]any{}
	for k, c := range recordedComparisons(t, dataDir) {
		var v any
		if err := json.Unmarshal(c.Result, &v); err != nil {
			t.Fatal(err)
		}
		out[k] = v
	}
	return out
}

// recordedComparisons returns the comparisons recorded of the source lib, by
// path, or identity for a derived definition, after the schema ID and a
// colon for a branch's.
func recordedComparisons(t *testing.T, dataDir string) map[string]store.RecordedComparison {
	t.Helper()
	s, _, err := engine.Open(t.Context(), engine.Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	recorded, err := s.Comparisons(t.Context(), "lib")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.RecordedComparison{}
	for _, c := range recorded {
		k := c.Path
		if k == "" {
			k = c.Identity
		}
		if c.Branch != nil {
			k = c.Branch.SchemaID + ":" + k
		}
		out[k] = c
	}
	return out
}

func TestCompareJSON(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, dataDir, commit, configure := compareEnv(t, nb)
	r := runCLIApp(t, nil, configure, env, "compare", "--all", "-o", "json")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	got := decodeCompare(t, r.stdout)
	if !got.OK || got.Scope != "all" || got.NetBox.URL != nb.srv.URL || got.NetBox.Version != "4.7.2" || got.ComparedAt == "" || len(got.Warnings) != 0 {
		t.Errorf("compare -o json = %+v", got)
	}
	src := got.Sources[0]
	if !src.OK || *src.Version != commit || src.Definitions != 6 || src.Compared != 6 ||
		src.InStep != 3 || src.Differing != 1 || src.Absent != 1 || src.Conflicts != 1 {
		t.Errorf("the source = %+v", src)
	}
	want := []string{"candidate Huawei 0", "conflict NETGEAR 0", "candidate Netgear 0", "candidate NE8000-X8 2", "candidate GS105 0", "absent GS510TLP 0"}
	if diff := cmp.Diff(want, outcomes(got)); diff != "" {
		t.Errorf("objects (-want +got):\n%s", diff)
	}
	// A child's rows: whether each version has it, with NetBox's ID, and
	// its fields, by a path through its key.
	var printedRows struct {
		Objects []struct {
			Rows []json.RawMessage `json:"rows"`
		} `json:"objects"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &printedRows); err != nil {
		t.Fatal(err)
	}
	var gs105 []string
	for _, row := range printedRows.Objects[4].Rows {
		var compact bytes.Buffer
		if err := json.Compact(&compact, row); err != nil {
			t.Fatal(err)
		}
		gs105 = append(gs105, compact.String())
	}
	for _, want := range []string{
		`{"path":["power_ports",{"name":"PSU0"}],"cells":[{"value":true},{"value":true}],"differs":false,"managed":true,"derived":false,"netbox_id":100}`,
		`{"path":["power_ports",{"name":"PSU0"},"type"],"cells":[{"value":"dc-terminal"},{"value":"dc-terminal"}],"differs":false,"managed":true,"derived":false,"netbox_id":null}`,
	} {
		if !slices.Contains(gs105, want) {
			t.Errorf("GS105's rows have no %s:\n%s", want, strings.Join(gs105, "\n"))
		}
	}
	// The device type whose manufacturer isn't in NetBox says what its
	// manufacturer's definition found.
	if u := got.Objects[5].Unresolved; len(u) != 1 || u[0].Field != "manufacturer" || u[0].Outcome == nil || *u[0].Outcome != "conflict" {
		t.Errorf("GS510TLP's unresolved references = %+v", u)
	}

	// The database records each object as it is printed.
	var printed struct {
		Objects []any `json:"objects"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &printed); err != nil {
		t.Fatal(err)
	}
	recorded := recordedResults(t, dataDir)
	if len(recorded) != len(got.Objects) {
		t.Errorf("recorded %d comparisons, want %d", len(recorded), len(got.Objects))
	}
	for i, o := range got.Objects {
		k := o.key(t)
		if diff := cmp.Diff(recordOf(t, printed.Objects[i]), recorded[k]); diff != "" {
			t.Errorf("the comparison recorded of %s (-printed +recorded):\n%s", k, diff)
		}
	}

	// --exit-code counts only what was compared: by default, not the
	// definition NetBox hasn't got.
	r = runCLIApp(t, nil, configure, env, "compare", "-o", "json", "--exit-code")
	if r.code != exitNotInStep || !strings.Contains(r.stderr, "2 of the 5 objects compared aren't in step with NetBox") {
		t.Errorf("compare --exit-code: exit code %d, stderr %q", r.code, r.stderr)
	}
	if decodeCompare(t, r.stdout).Sources == nil {
		t.Error("compare --exit-code printed no result")
	}
}

func TestCompareInStep(t *testing.T) {
	t.Parallel()
	// Everything the source defines, as NetBox's import would make it.
	netgear, huawei := nbManufacturer(1, "Netgear", "netgear"), nbManufacturer(2, "Huawei", "huawei")
	lib := libraryNetBox(t)
	nb := newCompareNetBox(t, []map[string]any{netgear, huawei}, lib.deviceTypes)
	nb.deviceTypes[1]["u_height"] = 16
	nb.templates = lib.templates
	for _, bay := range nb.templates["/api/dcim/module-bay-templates/"] {
		if bay["name"] == "Fan 1" {
			bay["name"], bay["display"], bay["position"] = "LPU 8", "LPU 8", "8"
		}
	}
	env, _, _, configure := compareEnv(t, nb)
	r := runCLIApp(t, nil, configure, env, "compare", "--exit-code", "--manufacturer", "Huawei", "--manufacturer", "Netgear")
	if r.code != exitOK || !strings.Contains(r.stdout, "lib     master") || strings.Contains(r.stdout, "OUTCOME") {
		t.Errorf("compare --exit-code with everything in step: exit code %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
}

func TestCompareManufacturers(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, dataDir, _, configure := compareEnv(t, nb)
	if r := runCLIApp(t, nil, configure, env, "compare", "--all"); r.code != exitOK {
		t.Fatalf("compare: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	before := recordedComparisons(t, dataDir)

	// A name no source defines, perhaps mistyped, leaves the comparison
	// short of what was asked for.
	r := runCLIApp(t, nil, configure, env, "compare", "-o", "json", "--exit-code", "--manufacturer", "Huawei", "--manufacturer", "Nobody", "--manufacturer", "Nobody")
	if r.code != exitCompareProblems || !strings.Contains(r.stderr, `Error: no source read defines the manufacturer "Nobody"`) {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", r.code, exitCompareProblems, r.stderr)
	}
	got := decodeCompare(t, r.stdout)
	if diff := cmp.Diff([]string{"candidate Huawei 0", "candidate NE8000-X8 2"}, outcomes(got)); diff != "" {
		t.Errorf("objects (-want +got):\n%s", diff)
	}
	if src := got.Sources[0]; src.Definitions != 6 || src.Compared != 2 {
		t.Errorf("the source = %+v, want 6 definitions, 2 compared", src)
	}
	wantWarning := `no source read defines the manufacturer "Nobody"`
	if len(got.Warnings) != 1 || got.Warnings[0].Message != wantWarning || !strings.Contains(r.stderr, "Nobody") {
		t.Errorf("warnings = %+v, stderr %q", got.Warnings, r.stderr)
	}
	// Only Huawei's device types were read.
	listed := nb.listed("/api/dcim/device-types/")
	if ids := listed[len(listed)-1]["manufacturer_id"]; !slices.Equal(ids, []string{"2"}) {
		t.Errorf("device types listed for manufacturers %v, want Huawei's alone", ids)
	}
	// Huawei's are recorded again, and the others keep theirs.
	after := recordedComparisons(t, dataDir)
	if len(after) != len(before) {
		t.Fatalf("recorded %d comparisons, then %d", len(before), len(after))
	}
	for k, c := range before {
		again := k == "device-types/Huawei/NE8000-X8.yaml" || k == `{"name":"Huawei"}`
		if recorded := !c.ComparedAt.Equal(after[k].ComparedAt); recorded != again {
			t.Errorf("the comparison of %s was recorded again: %t, want %t", k, recorded, again)
		}
	}
}

func TestCompareFetch(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	repo, pool, commit := libraryRepo(t, nil)
	env, _ := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repo+`","format":"devicetype-library"}]`)
	env = append(env, checkEnv(t, nb.srv, checkToken)...)
	r := runCLIApp(t, nil, trust(pool), env, "compare", "--fetch", "-o", "json")
	if r.code != exitOK {
		t.Fatalf("compare --fetch: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	if got := decodeCompare(t, r.stdout); *got.Sources[0].Version != commit || len(got.Objects) != 5 {
		t.Errorf("compare --fetch = %+v", got)
	}
}

func TestCompareRecordsOnlyTheVersionCompared(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, dataDir, _, configure := compareEnv(t, nb)
	// As if the source were fetched again elsewhere while compare reads
	// NetBox, having read the definitions of the version before: the
	// database's snapshot isn't the version compared.
	var once sync.Once
	nb.onManufacturers = func() {
		once.Do(func() {
			if err := recordFetchErr(t, dataDir, "lib", time.Now()); err != nil {
				t.Error(err)
			}
		})
	}
	r := runCLIApp(t, nil, configure, env, "compare", "-o", "json")
	if r.code != exitCompareProblems {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", r.code, exitCompareProblems, r.stderr)
	}
	got := decodeCompare(t, r.stdout)
	if src := got.Sources[0]; src.OK || src.Error == nil || !strings.Contains(*src.Error, "its comparisons were not recorded") ||
		!strings.Contains(*src.Error, `the snapshot is of version "abc"`) || len(got.Objects) != 5 {
		t.Errorf("the source = %+v, with %d objects", src, len(got.Objects))
	}
	if recorded := recordedResults(t, dataDir); len(recorded) != 0 {
		t.Errorf("recorded %d comparisons of a version the database doesn't hold", len(recorded))
	}
}

func TestCompareRecordsByTheKeysTheDatabaseHolds(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, dataDir, _, configure := compareEnv(t, nb)
	// The database holds each definition by its identity as JSON, written
	// with a space here, as no encoder of TrueUp's would: comparisons are
	// recorded by the keys the database holds, not by keys made again.
	loaded := storedDefinitions(t, dataDir)
	l := store.Load{
		Source: "lib", Version: loaded.Loaded.Version, Reader: loaded.Loaded.Reader, Settings: loaded.Loaded.Settings, StartedAt: time.Now(),
		Files: loaded.Files, InvalidFiles: loaded.InvalidFiles, Definitions: loaded.Definitions,
	}
	for i := range l.Definitions {
		l.Definitions[i].Identity = strings.Replace(l.Definitions[i].Identity, `":`, `": `, 1)
	}
	recordLoadIn(t, dataDir, l)
	if r := runCLIApp(t, nil, configure, env, "compare", "--all", "-o", "json"); r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	recorded := recordedComparisons(t, dataDir)
	if len(recorded) != len(l.Definitions) {
		t.Fatalf("recorded %d comparisons, want %d", len(recorded), len(l.Definitions))
	}
	for _, d := range l.Definitions {
		k := d.Path
		if k == "" {
			k = d.Identity
		}
		if c, ok := recorded[k]; !ok || c.DefinitionKey != d.DefinitionKey {
			t.Errorf("the comparison of %s is recorded by %+v, want %+v", k, c.DefinitionKey, d.DefinitionKey)
		}
	}
}

func TestCompareAfterAnotherFetch(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, dataDir, commit, configure := compareEnv(t, nb)
	// As if the source were fetched again elsewhere into this database,
	// though not into this cache: the version the database records can't
	// be read, so the source isn't compared, and nothing is recorded.
	recordFetch(t, dataDir, "lib", time.Now())
	r := runCLIApp(t, nil, configure, env, "compare", "-o", "json")
	if r.code != exitCompareProblems {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", r.code, exitCompareProblems, r.stderr)
	}
	got := decodeCompare(t, r.stdout)
	want := "the cache holds version " + commit + ", but the database records abc: run 'netbox-trueup-ai source fetch'"
	if src := got.Sources[0]; src.OK || src.Error == nil || *src.Error != want || len(got.Objects) != 0 {
		t.Errorf("the source = %+v, with %d objects; want the error %q", src, len(got.Objects), want)
	}
	if recorded := recordedResults(t, dataDir); len(recorded) != 0 {
		t.Errorf("recorded %d comparisons of a version the database doesn't hold", len(recorded))
	}
	// This host's cache is the problem, which another host sharing the
	// database mightn't have, so the attempt isn't recorded.
	if loaded := storedDefinitions(t, dataDir); loaded.State() != store.LoadReady || loaded.Loaded.Version != commit {
		t.Errorf("the database's last load is %s, %q, keeping version %q; want ready, keeping %s's definitions", loaded.State(), loaded.Error, loaded.Loaded.Version, commit)
	}
}

func TestCompareFailures(t *testing.T) {
	t.Parallel()
	brokenManufacturer := nbManufacturer(1, "Netgear", "netgear")
	delete(brokenManufacturer, "slug")
	tests := []struct {
		name       string
		nb         *compareNetBox
		args       []string
		unset      string
		wantCode   int
		wantStderr string
	}{
		{
			name: "a source that isn't configured", nb: libraryNetBox(t), args: []string{"elsewhere"},
			wantCode: exitUsage, wantStderr: "elsewhere",
		},
		{
			name: "no NetBox", nb: libraryNetBox(t), unset: "TRUEUP_NETBOX_URL",
			wantCode: exitConfig, wantStderr: "netbox.url isn't set",
		},
		{
			name: "manufacturers the token can't read", nb: libraryNetBox(t, "/api/dcim/manufacturers/"),
			wantCode: exitFailure, wantStderr: "the token can't read dcim.manufacturer (GET /api/dcim/manufacturers/: NetBox answered 403 Forbidden",
		},
		{
			name: "device types the token can't read", nb: libraryNetBox(t, "/api/dcim/device-types/"),
			wantCode: exitFailure, wantStderr: "'netbox-trueup-ai netbox check' lists what it can read",
		},
		{
			name: "templates the token can't read", nb: libraryNetBox(t, "/api/dcim/interface-templates/"),
			wantCode: exitFailure, wantStderr: "the token can't read dcim.interfacetemplate (GET /api/dcim/interface-templates/: NetBox answered 403 Forbidden",
		},
		{
			name: "a device type deleted while its templates are read", nb: refusing(libraryNetBox(t), "/api/dcim/module-bay-templates/"),
			wantCode: exitFailure, wantStderr: "read dcim.modulebaytemplate from NetBox: GET /api/dcim/module-bay-templates/: NetBox answered 400 Bad Request",
		},
		{
			name: "the advice after NetBox refuses a listing", nb: refusing(libraryNetBox(t), "/api/dcim/interface-templates/"),
			wantCode: exitFailure, wantStderr: "; if a device type was deleted while TrueUp read NetBox, compare again",
		},
		{
			name: "an object NetBox sends that can't be read", nb: newCompareNetBox(t, []map[string]any{brokenManufacturer}, nil),
			wantCode: exitFailure, wantStderr: "NetBox's answer can't be read: dcim.manufacturer #1: slug: isn't in the answer",
		},
	}
	for _, tt := range tests {
		env, dataDir, _, configure := compareEnv(t, tt.nb)
		if tt.unset != "" {
			env = slices.DeleteFunc(env, func(e string) bool { return strings.HasPrefix(e, tt.unset+"=") })
		}
		r := runCLIApp(t, nil, configure, env, append([]string{"compare", "-o", "json"}, tt.args...)...)
		if r.code != tt.wantCode || r.stdout != "" || !strings.Contains(r.stderr, tt.wantStderr) {
			t.Errorf("%s: exit code %d, stdout %q, stderr %q; want %d and %q", tt.name, r.code, r.stdout, r.stderr, tt.wantCode, tt.wantStderr)
		}
		if recorded := recordedResults(t, dataDir); len(recorded) != 0 {
			t.Errorf("%s: recorded %d comparisons", tt.name, len(recorded))
		}
	}
}

func TestCompareSourceNeverFetched(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	repo, _, _ := libraryRepo(t, nil)
	env, _ := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repo+`","format":"devicetype-library"}]`)
	r := runCLI(t, nil, append(env, checkEnv(t, nb.srv, checkToken)...), "compare", "-o", "json")
	if r.code != exitCompareProblems || !strings.Contains(r.stderr, "1 of the 1 sources couldn't be read or recorded: lib") {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	got := decodeCompare(t, r.stdout)
	if src := got.Sources[0]; got.OK || src.Error == nil || !strings.Contains(*src.Error, "run 'netbox-trueup-ai source fetch', or use --fetch") || len(got.Objects) != 0 {
		t.Errorf("compare = %+v", got)
	}
}

func TestCompareEscapes(t *testing.T) {
	t.Parallel()
	// NetBox's text reaches the terminal escaped.
	netgear := nbManufacturer(1, "Net\x1b[31mgear", "netgear")
	huawei := nbManufacturer(2, "Huawei", "huawei")
	nb := newCompareNetBox(t, []map[string]any{netgear, huawei}, []map[string]any{
		nbDeviceType(11, huawei, "NE8000-X8", "huawei-ne8000-x8", map[string]any{"comments": "\u202e\x1b]0;evil\x07"}),
	})
	env, _, _, configure := compareEnv(t, nb)
	r := runCLIApp(t, nil, configure, env, "compare")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	if strings.ContainsAny(r.stdout, "\x1b\x07\u202e") {
		t.Errorf("compare printed control characters:\n%q", r.stdout)
	}
	for _, want := range []string{`"Net\x1b[31mgear"`, `"\u202e\x1b]0;evil\a"`} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("compare doesn't show %s escaped:\n%s", want, r.stdout)
		}
	}
}

func TestCompareNothingChosen(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, dataDir, _, configure := compareEnv(t, nb)
	if r := runCLIApp(t, nil, configure, env, "compare", "--all"); r.code != exitOK {
		t.Fatalf("compare: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	before := recordedComparisons(t, dataDir)
	listed := len(nb.listed("/api/dcim/manufacturers/"))

	// NetBox is checked, but nothing is listed, and what was recorded stays.
	r := runCLIApp(t, nil, configure, env, "compare", "-o", "json", "--manufacturer", "Nobody")
	if r.code != exitCompareProblems {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", r.code, exitCompareProblems, r.stderr)
	}
	if got := decodeCompare(t, r.stdout); len(got.Objects) != 0 || got.Sources[0].Compared != 0 || got.NetBox.Version != "4.7.2" {
		t.Errorf("compare = %+v", got)
	}
	if n := len(nb.listed("/api/dcim/manufacturers/")); n != listed {
		t.Errorf("listed manufacturers %d more times, with nothing to compare", n-listed)
	}
	if after := recordedComparisons(t, dataDir); len(after) != len(before) {
		t.Errorf("recorded %d comparisons, then %d", len(before), len(after))
	}
}

func TestCompareChecksNetBoxBeforeFetching(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	repo, pool, _ := libraryRepo(t, nil)
	env, dataDir := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repo+`","format":"devicetype-library"}]`)
	env = append(env, checkEnv(t, nb.srv, "nbt_key.wrong")...)
	r := runCLIApp(t, nil, trust(pool), env, "compare", "--fetch")
	if r.code != exitFailure || r.stdout != "" || !strings.Contains(r.stderr, "NetBox refused the token") {
		t.Fatalf("compare --fetch with a token NetBox refuses: exit code %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	if entries, err := os.ReadDir(filepath.Join(dataDir, "cache", "sources")); err == nil && len(entries) > 0 {
		t.Errorf("the source was fetched before NetBox was checked: %v", entries)
	}
}

func TestCompareAManufacturerNetBoxHoldsTwice(t *testing.T) {
	t.Parallel()
	// NetBox's own rules forbid this, so NetBox can't say which Netgear the
	// definitions are.
	netgear, again := nbManufacturer(1, "Netgear", "netgear"), nbManufacturer(3, "Netgear", "netgear-2")
	nb := newCompareNetBox(t, []map[string]any{netgear, nbManufacturer(2, "Huawei", "huawei"), again}, libraryNetBox(t).deviceTypes)
	env, _, _, configure := compareEnv(t, nb)
	r := runCLIApp(t, nil, configure, env, "compare", "--manufacturer", "Netgear")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	for _, want := range []string{
		`manufacturer "Netgear": lib (1 file): its name matches NetBox's manufacturer #1, "Netgear"; its name matches NetBox's manufacturer #3, "Netgear"`,
		`device type Netgear "GS105": lib (device-types/Netgear/GS105.yaml): its manufacturer, "Netgear", could be any of NetBox's manufacturers #1, #3, which NetBox should keep unique; ` +
			`its manufacturer and model match NetBox's device type #10, Netgear "GS105"`,
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("compare doesn't say\n%s\nin:\n%s", want, r.stdout)
		}
	}
}

func TestCompareRecordsWhatTheSourceHoldsNow(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, dataDir, _, configure := compareEnv(t, nb)
	if r := runCLIApp(t, nil, configure, env, "compare", "--all"); r.code != exitOK {
		t.Fatalf("compare: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	before := recordedComparisons(t, dataDir)

	// The next version breaks GS105's file, which then defines nothing, and
	// removes Huawei's only device type.
	files := libraryFiles(t, map[string]string{"device-types/Netgear/GS105.yaml": "manufacturer: Netgear\nmodel: [GS105\n"})
	delete(files, "device-types/Huawei/NE8000-X8.yaml")
	broken, pool, _ := gitRepoWith(t, files)
	env = slices.DeleteFunc(slices.Clone(env), func(e string) bool { return strings.HasPrefix(e, "TRUEUP_SOURCES=") })
	env = append(env, `TRUEUP_SOURCES=[{"name":"lib","kind":"git","url":"`+broken+`","format":"devicetype-library"}]`)
	r := runCLIApp(t, nil, trust(pool), env, "compare", "--fetch", "--all")
	if r.code != exitCompareProblems || !strings.Contains(r.stdout, "Invalid files:\n  lib: device-types/Netgear/GS105.yaml") {
		t.Fatalf("compare --fetch: exit code %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	// A file that no longer reads defines nothing, as one the source no
	// longer holds doesn't: the invalid file is reported, and neither keeps
	// a comparison that would pass for current.
	after := recordedComparisons(t, dataDir)
	for _, gone := range []string{"device-types/Netgear/GS105.yaml", "device-types/Huawei/NE8000-X8.yaml", `{"name":"Huawei"}`} {
		if _, ok := after[gone]; ok {
			t.Errorf("the comparison of %s, which the source no longer holds, is kept", gone)
		}
	}
	if c := after["device-types/Netgear/GS510TLP.yml"]; c.ComparedAt.Equal(before["device-types/Netgear/GS510TLP.yml"].ComparedAt) {
		t.Error("the comparison of a file that still reads wasn't recorded again")
	}

	// A manufacturer an invalid file might define is said to be possible.
	r = runCLIApp(t, nil, trust(pool), env, "compare", "--manufacturer", "Nobody")
	if !strings.Contains(r.stdout, `no source read defines the manufacturer "Nobody"; some files are invalid, and one of them may`) {
		t.Errorf("compare --manufacturer with invalid files:\n%s", r.stdout)
	}
	// The error after the result says so once: the warning's last clause
	// repeats what it says of the invalid files.
	if !strings.Contains(r.stderr, `manufacturer=Nobody`) || strings.Contains(r.stderr, "one of them may") ||
		!strings.HasSuffix(r.stderr, `1 file is invalid; no source read defines the manufacturer "Nobody"`+"\n") {
		t.Errorf("compare --manufacturer with invalid files: stderr %q", r.stderr)
	}
}

// TestCompareJSONIsWrittenAsWriteJSONWould checks that writing compare's
// result a piece at a time gives what writing it whole gives.
func TestCompareJSONIsWrittenAsWriteJSONWould(t *testing.T) {
	t.Parallel()
	msg := "a <warning> & more"
	for _, r := range []compareJSON{
		{},
		{Sources: []compareSourceJSON{}, Objects: []objectOut{}, Warnings: []warningJSON{}},
		{
			OK:         true,
			NetBox:     compareNetBoxJSON{URL: "https://netbox.example.com", Version: "4.7.2"},
			ComparedAt: time.Date(2026, 11, 16, 1, 2, 3, 456000, time.UTC),
			Sources:    []compareSourceJSON{{Name: "lib", Error: &msg, Problems: []problemJSON{{Severity: "warning", Reason: "<x>"}}}},
			Counts:     compareCountsJSON{Compared: 2, Conflicts: 2},
			Objects: []objectOut{
				{head: json.RawMessage(`{"type":"dcim.devicetype","empty":{},"list":[],"rows":`), rows: json.RawMessage(`[{"path":["interfaces",{"name":"a\u003cb"}],"cells":[{},{"value":true}]}]`)},
				{head: json.RawMessage(`{"b":1,"rows":`), rows: json.RawMessage(`[]`)},
			},
			Warnings: []warningJSON{{Message: msg}},
		},
	} {
		var whole, pieces bytes.Buffer
		if err := writeJSON(&whole, r); err != nil {
			t.Fatal(err)
		}
		if err := writeCompareJSON(&pieces, r); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(whole.String(), pieces.String()); diff != "" {
			t.Errorf("writeCompareJSON (-writeJSON +got):\n%s", diff)
		}
	}
}

// TestCompareTemplatesNetBoxHoldsMoreThanOnce checks how compare reports a
// device type NetBox holds a template of three times, which NetBox's rules
// forbid.
func TestCompareTemplatesNetBoxHoldsMoreThanOnce(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	const bays = "/api/dcim/module-bay-templates/"
	ne8000 := nb.deviceTypes[1]
	nb.templates[bays] = append(nb.templates[bays],
		nbTemplate(bays, 300, ne8000, "LPU 1", map[string]any{"position": "1"}),
		nbTemplate(bays, 301, ne8000, "LPU 1", map[string]any{"position": "1"}))
	env, _, _, configure := compareEnv(t, nb)
	r := runCLIApp(t, nil, configure, env, "compare", "--manufacturer", "Huawei")
	want := `device type Huawei "NE8000-X8": lib (device-types/Huawei/NE8000-X8.yaml): NetBox's #11 has module_bays["LPU 1"] 3 times, as #200, #300, #301, which NetBox should keep unique, so none is compared`
	if r.code != exitOK || !strings.Contains(r.stdout, want) {
		t.Errorf("exit code %d, stdout:\n%s\nwant it to hold %s", r.code, r.stdout, want)
	}
}

func TestValueText(t *testing.T) {
	t.Parallel()
	port := model.Reference{Type: "dcim.powerporttemplate", ID: 7, Key: map[string]model.Value{"name": model.String("PSU1")}}
	tests := []struct {
		v    model.Value
		want string
	}{
		{nil, "not set"},
		{model.String("x"), `"x"`},
		{port, `"PSU1" (#7)`},
		{model.Reference{Type: "dcim.powerporttemplate", Key: port.Key}, `"PSU1"`},
		// Another device type's, by its ID alone.
		{model.Reference{Type: "dcim.powerporttemplate", ID: 9}, "#9"},
	}
	for _, tt := range tests {
		if got := valueText(tt.v); got != tt.want {
			t.Errorf("valueText(%v) = %q, want %q", tt.v, got, tt.want)
		}
	}
}

// TestCheckResults checks that results Compare shouldn't give are refused,
// in any source, before any source's comparisons are recorded.
func TestCheckResults(t *testing.T) {
	t.Parallel()
	const dt = "dcim.devicetype"
	def := func(src, path string) compare.Definition {
		return compare.Definition{Source: src, Definition: format.Definition{Object: model.Object{Type: dt}, Path: path}}
	}
	res := func(src, path string, o compare.Outcome) compare.Result {
		return compare.Result{Type: dt, Outcome: o, Columns: []compare.Column{{Kind: compare.DefinitionColumn, Source: src, Path: path}}}
	}
	version := "v1"
	source := func(name string, paths ...string) sourceCompared {
		sc := sourceCompared{row: &compareSourceJSON{Name: name, Version: &version}}
		for i, p := range paths {
			sc.rows = append(sc.rows, store.Definition{DefinitionKey: store.DefinitionKey{ObjectType: dt, Path: p}, ID: int64(i + 1)})
			sc.chosen = append(sc.chosen, i)
			sc.whole = append(sc.whole, def(name, p))
		}
		return sc
	}
	unread := sourceCompared{row: &compareSourceJSON{Name: "c"}}
	two := []sourceCompared{source("a", "a.yaml"), unread, source("b", "b1.yaml", "b2.yaml")}
	tests := []struct {
		name    string
		results []compare.Result
		want    string
	}{
		{"one for each definition", []compare.Result{res("a", "a.yaml", compare.Candidate), res("b", "b1.yaml", compare.Absent), res("b", "b2.yaml", compare.Conflict)}, ""},
		// The first source's are fine; the second's aren't.
		{
			"an outcome Compare doesn't give",
			[]compare.Result{res("a", "a.yaml", compare.Candidate), res("b", "b1.yaml", compare.Absent), res("b", "b2.yaml", "bogus")},
			`compare with NetBox: the comparison of dcim.devicetype "b2.yaml" has the outcome "bogus"`,
		},
		{
			"another definition's",
			[]compare.Result{res("a", "a.yaml", compare.Candidate), res("b", "b2.yaml", compare.Absent), res("b", "b1.yaml", compare.Absent)},
			"compare with NetBox: result 0 of source b is of another definition",
		},
		{
			"no columns",
			[]compare.Result{res("a", "a.yaml", compare.Candidate), {Type: dt, Outcome: compare.Absent}, res("b", "b2.yaml", compare.Absent)},
			"compare with NetBox: result 0 of source b is of another definition",
		},
		{
			"too few",
			[]compare.Result{res("a", "a.yaml", compare.Candidate), res("b", "b1.yaml", compare.Absent)},
			"compare with NetBox: 1 results for the 2 definitions of source b",
		},
		{
			"too many",
			[]compare.Result{res("a", "a.yaml", compare.Candidate), res("b", "b1.yaml", compare.Absent), res("b", "b2.yaml", compare.Absent), res("b", "b3.yaml", compare.Absent)},
			"compare with NetBox: 1 results of no definition",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ""
			if err := checkResults(two, tt.results, scopeSelection); err != nil {
				got = err.Error()
			}
			if got != tt.want {
				t.Errorf("checkResults = %q, want %q", got, tt.want)
			}
		})
	}

	// By default, a definition is absent exactly where its unique values
	// matched nothing in NetBox, as another definition of an object NetBox
	// has.
	results := []compare.Result{res("a", "a.yaml", compare.Candidate), res("b", "b1.yaml", compare.Absent), res("b", "b2.yaml", compare.Conflict)}
	for _, tt := range []struct {
		matched [][]bool
		want    string
	}{
		{[][]bool{{true}, nil, {false, true}}, ""},
		{[][]bool{{true}, nil, {true, true}}, `compare with NetBox: dcim.devicetype "b1.yaml" is absent, though its unique values matched NetBox; please report it`},
		{[][]bool{{false}, nil, {false, true}}, `compare with NetBox: dcim.devicetype "a.yaml" is candidate, though its unique values matched nothing in NetBox; please report it`},
		{[][]bool{{true}, nil, {false, false}}, `compare with NetBox: dcim.devicetype "b2.yaml" is conflict, though its unique values matched nothing in NetBox; please report it`},
		{[][]bool{{true}, nil, {false}}, `compare with NetBox: source b has 2 definitions chosen, 2 read and 1 matched; please report it`},
	} {
		sources := slices.Clone(two)
		for j := range sources {
			sources[j].matched = tt.matched[j]
		}
		got := ""
		if err := checkResults(sources, results, scopeNetBox); err != nil {
			got = err.Error()
		}
		if got != tt.want {
			t.Errorf("checkResults by default, matched %v = %q, want %q", tt.matched, got, tt.want)
		}
	}
	if err := checkResults(two, results, scopeAll); err != nil {
		t.Errorf("checkResults of every definition = %v, want nil", err)
	}
}

// TestComparePortMappings checks that compare reads each front port's
// mappings from NetBox, nested in it, and compares them by position: the
// library's wall box maps its front port's position 1 to its rear port's
// position 1, and NetBox maps it to position 2, and has a mapping at
// position 2 that the library hasn't got.
func TestComparePortMappings(t *testing.T) {
	t.Parallel()
	const wallBox = "device-types/Generic/wall-box-1-utp-plug.yaml"
	data, err := os.ReadFile(filepath.Join("../../internal/format/devicetypelib/testdata/library", filepath.FromSlash(wallBox)))
	if err != nil {
		t.Fatal(err)
	}
	generic := nbManufacturer(3, "Generic", "generic")
	nb := newCompareNetBox(t, []map[string]any{generic}, []map[string]any{
		nbDeviceType(12, generic, "Wall box, 1 UTP plug", "generic-wall-box-1-utp-plug", map[string]any{"u_height": 0.0, "is_full_depth": false}),
	})
	dt := nb.deviceTypes[0]
	const rears, fronts = "/api/dcim/rear-port-templates/", "/api/dcim/front-port-templates/"
	nb.templates = map[string][]map[string]any{
		rears: {nbTemplate(rears, 300, dt, "Port 1", map[string]any{"type": choice("8p8c"), "color": "", "positions": 2})},
		fronts: {nbTemplate(fronts, 400, dt, "Port 1", map[string]any{
			"type": choice("8p8c"), "color": "", "positions": 1,
			"rear_ports": []any{
				map[string]any{"position": 1, "rear_port": 300, "rear_port_position": 2},
				map[string]any{"position": 2, "rear_port": 300, "rear_port_position": 1},
			},
		})},
	}
	repo, pool, _ := libraryRepo(t, map[string]string{wallBox: string(data)})
	env, _ := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repo+`","format":"devicetype-library"}]`)
	fetched(t, pool, env)
	env = append(env, checkEnv(t, nb.srv, checkToken)...)

	r := runCLIApp(t, nil, trust(pool), env, "compare")
	if r.code != exitOK || r.stderr != "" {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	for _, want := range []string{
		"\n    FIELD                                                   lib  NETBOX\n" +
			"    rear_ports[\"Port 1\"].positions                          1    2\n" +
			"    front_ports[\"Port 1\"].rear_ports[1].rear_port_position  1    2\n",
		// No NetBox ID: NetBox gives a mapping none.
		`NetBox #12: front_ports["Port 1"].rear_ports[2]` + "\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("compare printed\n%s\nwant it to hold\n%s", r.stdout, want)
		}
	}
	r = runCLIApp(t, nil, trust(pool), env, "compare", "-o", "json")
	var got compareResult
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("compare -o json printed %q: %v", r.stdout, err)
	}
	for _, want := range []string{
		`{"path":["front_ports",{"name":"Port 1"},"rear_ports",{"position":1},"rear_port"],"cells":[{"value":{"name":"Port 1"}},{"value":{"id":300,"name":"Port 1"}}],"differs":false,"managed":true,"derived":false,"netbox_id":null}`,
		`{"path":["front_ports",{"name":"Port 1"},"rear_ports",{"position":2}],"cells":[{"value":false},{"value":true}],"differs":true,"managed":false,"derived":false,"netbox_id":null}`,
	} {
		if !strings.Contains(compactJSON(t, r.stdout), want) {
			t.Errorf("compare -o json printed\n%s\nwant it to hold\n%s", r.stdout, want)
		}
	}
}

// compactJSON is JSON without white space between tokens.
func compactJSON(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// refusing makes nb refuse to list endpoint.
func refusing(nb *compareNetBox, endpoint string) *compareNetBox {
	nb.refused = endpoint
	return nb
}
