package main

import (
	"encoding/json"
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
	denied        []string

	mu      sync.Mutex
	queries map[string][]url.Values // by path
}

func newCompareNetBox(t *testing.T, manufacturers, deviceTypes []map[string]any, denied ...string) *compareNetBox {
	t.Helper()
	f := &compareNetBox{manufacturers: manufacturers, deviceTypes: deviceTypes, denied: denied, queries: map[string][]url.Values{}}
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
		f.mu.Lock()
		f.queries[r.URL.Path] = append(f.queries[r.URL.Path], q)
		f.mu.Unlock()
		var objects []map[string]any
		switch {
		case r.Header.Get("Authorization") != "Bearer "+checkToken:
			answer(http.StatusForbidden, map[string]any{"detail": "Invalid v2 token"})
			return
		case r.URL.Path == "/api/status/":
			answer(http.StatusOK, map[string]any{"netbox-version": "4.7.2"})
			return
		case r.URL.Path == "/api/authentication-check/":
			answer(http.StatusOK, map[string]any{"username": "trueup"})
			return
		case slices.Contains(f.denied, r.URL.Path):
			answer(http.StatusForbidden, map[string]any{"detail": "You do not have permission to perform this action."})
			return
		case r.URL.Path == "/api/dcim/manufacturers/":
			objects = f.manufacturers
		case r.URL.Path == "/api/dcim/device-types/":
			ids := q["manufacturer_id"]
			for _, o := range f.deviceTypes {
				maker, _ := o["manufacturer"].(map[string]any)
				id, _ := maker["id"].(int)
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

// libraryNetBox holds what NetBox's import would make of the test library's
// Netgear GS105 and Huawei NE8000-X8, with the NE8000-X8 changed since, and
// a Netgear whose slug NETGEAR's would take.
func libraryNetBox(t *testing.T, denied ...string) *compareNetBox {
	t.Helper()
	netgear, huawei := nbManufacturer(1, "Netgear", "netgear"), nbManufacturer(2, "Huawei", "huawei-technologies")
	return newCompareNetBox(t, []map[string]any{netgear, huawei}, []map[string]any{
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
lib     master  ` + commit + `  6         3        1          1       1          0        1

SOURCE  OUTCOME    TYPE          MANUFACTURER  MODEL      NETBOX  DIFFERENCES  FROM
lib     conflict   manufacturer  NETGEAR       -          -       -            1 file
lib     candidate  device type   Huawei        NE8000-X8  #11     1            device-types/Huawei/NE8000-X8.yaml
lib     absent     device type   NETGEAR       GS510TLP   -       -            device-types/Netgear/GS510TLP.yml

Conflicts:
  lib: manufacturer "NETGEAR" (1 file): its slug matches NetBox's manufacturer #1, "Netgear"

Differences:
  lib: device type Huawei "NE8000-X8" (device-types/Huawei/NE8000-X8.yaml), NetBox #11:
    FIELD     DEFINITION  NETBOX
    u_height  16          15.5

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
}

// compareResult decodes compare's JSON output, with values as encoding/json
// reads them.
type compareResult struct {
	OK     bool `json:"ok"`
	NetBox struct {
		URL     string `json:"url"`
		Version string `json:"version"`
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
		Path *string `json:"path"`
	} `json:"columns"`
	Rows []struct {
		Path    []string `json:"path"`
		Differs bool     `json:"differs"`
		Managed bool     `json:"managed"`
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

// recordedResults returns the comparisons recorded of a source, each as its
// result decoded, by its path, or its identity for a derived definition.
func recordedResults(t *testing.T, dataDir, source string) map[string]any {
	t.Helper()
	out := map[string]any{}
	for k, c := range recordedComparisons(t, dataDir, source) {
		var v any
		if err := json.Unmarshal(c.Result, &v); err != nil {
			t.Fatal(err)
		}
		out[k] = v
	}
	return out
}

// recordedComparisons returns the comparisons recorded of a source, by path,
// or identity for a derived definition.
func recordedComparisons(t *testing.T, dataDir, source string) map[string]store.RecordedComparison {
	t.Helper()
	s, _, err := engine.Open(t.Context(), engine.Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	recorded, err := s.Comparisons(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.RecordedComparison{}
	for _, c := range recorded {
		k := c.Path
		if k == "" {
			k = c.Identity
		}
		out[k] = c
	}
	return out
}

func TestCompareJSON(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, dataDir, commit, configure := compareEnv(t, nb)
	r := runCLIApp(t, nil, configure, env, "compare", "-o", "json")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	got := decodeCompare(t, r.stdout)
	if !got.OK || got.NetBox.URL != nb.srv.URL || got.NetBox.Version != "4.7.2" || got.ComparedAt == "" || len(got.Warnings) != 0 {
		t.Errorf("compare -o json = %+v", got)
	}
	src := got.Sources[0]
	if !src.OK || *src.Version != commit || src.Definitions != 6 || src.Compared != 6 ||
		src.InStep != 3 || src.Differing != 1 || src.Absent != 1 || src.Conflicts != 1 {
		t.Errorf("the source = %+v", src)
	}
	want := []string{"candidate Huawei 0", "conflict NETGEAR 0", "candidate Netgear 0", "candidate NE8000-X8 1", "candidate GS105 0", "absent GS510TLP 0"}
	if diff := cmp.Diff(want, outcomes(got)); diff != "" {
		t.Errorf("objects (-want +got):\n%s", diff)
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
	recorded := recordedResults(t, dataDir, "lib")
	if len(recorded) != len(got.Objects) {
		t.Errorf("recorded %d comparisons, want %d", len(recorded), len(got.Objects))
	}
	for i, o := range got.Objects {
		k := o.key(t)
		if diff := cmp.Diff(printed.Objects[i], recorded[k]); diff != "" {
			t.Errorf("the comparison recorded of %s (-printed +recorded):\n%s", k, diff)
		}
	}

	r = runCLIApp(t, nil, configure, env, "compare", "-o", "json", "--exit-code")
	if r.code != exitNotInStep || !strings.Contains(r.stderr, "3 of the 6 definitions compared aren't in step with NetBox") {
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
	nb := newCompareNetBox(t, []map[string]any{netgear, huawei}, libraryNetBox(t).deviceTypes)
	nb.deviceTypes[1]["u_height"] = 16
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
	if r := runCLIApp(t, nil, configure, env, "compare"); r.code != exitOK {
		t.Fatalf("compare: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	before := recordedComparisons(t, dataDir, "lib")

	// A name no source defines, perhaps mistyped, leaves the comparison
	// short of what was asked for.
	r := runCLIApp(t, nil, configure, env, "compare", "-o", "json", "--exit-code", "--manufacturer", "Huawei", "--manufacturer", "Nobody")
	if r.code != exitCompareProblems || !strings.Contains(r.stderr, `Error: no source read defines the manufacturer "Nobody"`) {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", r.code, exitCompareProblems, r.stderr)
	}
	got := decodeCompare(t, r.stdout)
	if diff := cmp.Diff([]string{"candidate Huawei 0", "candidate NE8000-X8 1"}, outcomes(got)); diff != "" {
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
	after := recordedComparisons(t, dataDir, "lib")
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
	if got := decodeCompare(t, r.stdout); *got.Sources[0].Version != commit || len(got.Objects) != 6 {
		t.Errorf("compare --fetch = %+v", got)
	}
}

func TestCompareRecordsOnlyTheVersionCompared(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, dataDir, _, configure := compareEnv(t, nb)
	// As if the source were fetched again elsewhere since this cache was:
	// the database's snapshot isn't the version compared.
	recordFetch(t, dataDir, "lib", time.Now())
	r := runCLIApp(t, nil, configure, env, "compare", "-o", "json")
	if r.code != exitCompareProblems {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", r.code, exitCompareProblems, r.stderr)
	}
	got := decodeCompare(t, r.stdout)
	if src := got.Sources[0]; src.OK || src.Error == nil || !strings.Contains(*src.Error, "its comparisons weren't recorded") ||
		!strings.Contains(*src.Error, `the snapshot is of version "abc"`) || len(got.Objects) != 6 {
		t.Errorf("the source = %+v, with %d objects", src, len(got.Objects))
	}
	if recorded := recordedResults(t, dataDir, "lib"); len(recorded) != 0 {
		t.Errorf("recorded %d comparisons of a version the database doesn't hold", len(recorded))
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
		if recorded := recordedResults(t, dataDir, "lib"); len(recorded) != 0 {
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
	if r := runCLIApp(t, nil, configure, env, "compare"); r.code != exitOK {
		t.Fatalf("compare: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	before := recordedComparisons(t, dataDir, "lib")
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
	if after := recordedComparisons(t, dataDir, "lib"); len(after) != len(before) {
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
		`lib: manufacturer "Netgear" (1 file): its name matches NetBox's manufacturer #1, "Netgear"; its name matches NetBox's manufacturer #3, "Netgear"`,
		`lib: device type Netgear "GS105" (device-types/Netgear/GS105.yaml): its manufacturer, "Netgear", could be any of NetBox's manufacturers #1, #3, which NetBox should keep unique; ` +
			`its manufacturer and model match NetBox's device type #10, Netgear "GS105"; its manufacturer and slug match NetBox's device type #10, Netgear "GS105"`,
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("compare doesn't say\n%s\nin:\n%s", want, r.stdout)
		}
	}
}
