package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/engine"
)

// oursGS105 is the library's GS105, as another source defines it: the same
// but for its depth, and an interface the library hasn't got.
func oursGS105(t *testing.T) string {
	t.Helper()
	gs105 := libraryFiles(t, nil)["device-types/Netgear/GS105.yaml"]
	ours := strings.Replace(gs105, "is_full_depth: false", "is_full_depth: true", 1)
	ours = strings.Replace(ours, "  - name: GigabitEthernet/5\n    type: 1000base-t\n",
		"  - name: GigabitEthernet/5\n    type: 1000base-t\n  - name: GigabitEthernet/6\n    type: 1000base-t\n", 1)
	if ours == gs105 || !strings.Contains(ours, "GigabitEthernet/6") || !strings.Contains(ours, "is_full_depth: true") {
		t.Fatalf("GS105's file changed: %s", gs105)
	}
	return ours
}

// gs105As is the library's GS105, as another model of Netgear's with
// another slug.
func gs105As(t *testing.T, model, slug string) string {
	t.Helper()
	gs105 := libraryFiles(t, nil)["device-types/Netgear/GS105.yaml"]
	out := strings.Replace(strings.Replace(gs105, "model: GS105", "model: "+model, 1), "slug: netgear-gs105", "slug: "+slug, 1)
	if !strings.Contains(out, "model: "+model+"\n") || !strings.Contains(out, "slug: "+slug+"\n") {
		t.Fatalf("GS105's file changed: %s", gs105)
	}
	return out
}

// twoSourcesEnv configures two sources from one repository: lib, the test
// library and extra, and ours, the files in device-types/Ours/; both
// fetched; and NetBox.
func twoSourcesEnv(t *testing.T, nb *compareNetBox, extra, ours map[string]string) (env []string, dataDir string, configure func(*app)) {
	t.Helper()
	files := map[string]string{}
	for k, v := range extra {
		files[k] = v
	}
	for k, v := range ours {
		files["device-types/Ours/"+k] = v
	}
	repo, pool, _ := libraryRepo(t, files)
	env, dataDir = sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repo+`","format":"devicetype-library","exclude":["device-types/Ours/**"]},`+
		`{"name":"ours","kind":"git","url":"`+repo+`","format":"devicetype-library","include":["device-types/Ours/**"]}]`)
	fetched(t, pool, env)
	return append(env, checkEnv(t, nb.srv, checkToken)...), dataDir, trust(pool)
}

// recordedOf returns the comparisons recorded of a source, by path, or
// identity for a derived definition, each as its outcome.
func recordedOf(t *testing.T, dataDir, source string) map[string]string {
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
	out := map[string]string{}
	for _, c := range recorded {
		k := c.Path
		if k == "" {
			k = c.Identity
		}
		out[k] = c.Outcome
	}
	return out
}

// columnsText lists an object's columns: each definition's source and
// outcome, then NetBox's.
func columnsText(o objectJSON) string {
	var out []string
	for _, c := range o.Columns {
		if c.Kind == "netbox" {
			out = append(out, "netbox")
			continue
		}
		out = append(out, c.Source+" "+c.Outcome)
	}
	return strings.Join(out, ", ")
}

// TestCompareAcrossSources checks that compare groups two sources'
// definitions into objects, compares each definition with NetBox, and the
// object across both and NetBox: the library's GS105 is NetBox's, the
// other source's differs in a field and an interface. Netgear's GS108,
// which NetBox hasn't got, is compared too, since the other source's
// definition of it collides with NetBox's GS105; and so recorded as absent.
func TestCompareAcrossSources(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, dataDir, configure := twoSourcesEnv(t, nb,
		map[string]string{"device-types/Netgear/GS108.yaml": gs105As(t, "GS108", "netgear-gs108")},
		map[string]string{"GS105.yaml": oursGS105(t), "GS108.yaml": gs105As(t, "GS108", "netgear-gs105")})

	r := runCLIApp(t, nil, configure, env, "compare", "-o", "json", "--exit-code")
	if r.code != exitNotInStep || !strings.Contains(r.stderr, "4 of the 6 objects compared aren't in step with NetBox") {
		t.Fatalf("compare --exit-code: exit code %d, want %d\nstderr: %s", r.code, exitNotInStep, r.stderr)
	}
	got := decodeCompare(t, r.stdout)
	var objects []string
	for _, o := range got.Objects {
		objects = append(objects, o.Outcome+" "+o.name()+": "+columnsText(o))
	}
	want := []string{
		"candidate Huawei: lib candidate, netbox",
		"conflict NETGEAR: lib conflict",
		"candidate Netgear: lib candidate, ours candidate, netbox",
		"candidate NE8000-X8: lib candidate, netbox",
		"candidate GS105: lib candidate, ours candidate, netbox",
		"conflict GS108: lib absent, ours conflict",
	}
	if diff := cmp.Diff(want, objects); diff != "" {
		t.Errorf("objects (-want +got):\n%s", diff)
	}
	var counts struct {
		Counts compareCountsJSON `json:"counts"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &counts); err != nil {
		t.Fatal(err)
	}
	if want := (compareCountsJSON{Compared: 6, InStep: 2, Differing: 2, Conflicts: 2}); counts.Counts != want {
		t.Errorf("counts = %+v, want %+v", counts.Counts, want)
	}
	for i, want := range []string{"lib 6 3 1 1 1", "ours 3 1 1 0 1"} {
		s := got.Sources[i]
		if got := strings.Join([]string{s.Name, strconv.Itoa(s.Compared), strconv.Itoa(s.InStep), strconv.Itoa(s.Differing), strconv.Itoa(s.Absent), strconv.Itoa(s.Conflicts)}, " "); got != want {
			t.Errorf("source %d counts %s, want %s: by definition", i, got, want)
		}
	}

	// GS105: the library's in step, the other's differing, and the object
	// differs where they do.
	gs105 := got.Objects[4]
	if gs105.Differences != 2 || gs105.InStep {
		t.Errorf("GS105 has %d differences, in step %t; want 2, not in step", gs105.Differences, gs105.InStep)
	}
	var differing []string
	for _, row := range gs105.Rows {
		if row.Differs && row.Managed {
			differing = append(differing, pathText(row.Path))
		}
	}
	if diff := cmp.Diff([]string{"is_full_depth", `interfaces["GigabitEthernet/6"]`}, differing); diff != "" {
		t.Errorf("GS105's differences (-want +got):\n%s", diff)
	}

	// Each definition is recorded alone, the library's GS108 too.
	if diff := cmp.Diff(map[string]string{
		`{"name":"Huawei"}`: "candidate", `{"name":"NETGEAR"}`: "conflict", `{"name":"Netgear"}`: "candidate",
		"device-types/Huawei/NE8000-X8.yaml": "candidate", "device-types/Netgear/GS105.yaml": "candidate", "device-types/Netgear/GS108.yaml": "absent",
	}, recordedOf(t, dataDir, "lib")); diff != "" {
		t.Errorf("the library's records (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]string{
		`{"name":"Netgear"}`: "candidate", "device-types/Ours/GS105.yaml": "candidate", "device-types/Ours/GS108.yaml": "conflict",
	}, recordedOf(t, dataDir, "ours")); diff != "" {
		t.Errorf("the other source's records (-want +got):\n%s", diff)
	}

	// Text.
	r = runCLIApp(t, nil, configure, env, "compare")
	if r.code != exitOK {
		t.Fatalf("compare: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	for _, want := range []string{
		"\n6 objects compared: 2 in step, 2 differing, 0 absent, 2 in conflict.\n",
		"\ncandidate  device type   Netgear       GS105      #10     2            lib     device-types/Netgear/GS105.yaml\n" +
			"                                                                       ours    device-types/Ours/GS105.yaml\n",
		`  device type Netgear "GS108": ours (device-types/Ours/GS108.yaml): its manufacturer and slug match NetBox's device type #10, Netgear "GS105"` + "\n",
		`  device type Netgear "GS105", NetBox #10, in lib (device-types/Netgear/GS105.yaml) and ours (device-types/Ours/GS105.yaml):` + "\n" +
			"    FIELD                            lib      ours     NETBOX\n" +
			"    is_full_depth                    false    true     false\n" +
			`    interfaces["GigabitEthernet/6"]  missing  present  missing` + "\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("compare doesn't say\n%s\nin:\n%s", want, r.stdout)
		}
	}

	// Compared alone, the library's GS108 matches nothing in NetBox, and
	// its record goes; the other source's are kept.
	r = runCLIApp(t, nil, configure, env, "compare", "lib", "-o", "json")
	if r.code != exitOK {
		t.Fatalf("compare lib: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	if _, ok := recordedOf(t, dataDir, "lib")["device-types/Netgear/GS108.yaml"]; ok {
		t.Error("compare lib kept the library's comparison of GS108")
	}
	if n := len(recordedOf(t, dataDir, "ours")); n != 3 {
		t.Errorf("compare lib left %d of the other source's comparisons, want 3", n)
	}
}

// TestCompareDefinedTwice checks that a source defining an object twice
// makes it a conflict, with a column for each definition.
func TestCompareDefinedTwice(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	gs105 := libraryFiles(t, nil)["device-types/Netgear/GS105.yaml"]
	env, _, configure := twoSourcesEnv(t, nb, nil, map[string]string{"GS105.yaml": gs105, "GS105-copy.yaml": gs105})
	r := runCLIApp(t, nil, configure, env, "compare", "-o", "json")
	if r.code != exitOK {
		t.Fatalf("compare: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	var printed struct {
		Objects []struct {
			Identity struct {
				Model string `json:"model"`
			} `json:"identity"`
			Outcome      string   `json:"outcome"`
			DuplicatedIn []string `json:"duplicated_in"`
			Columns      []struct {
				Source  string `json:"source"`
				Outcome string `json:"outcome"`
				InStep  bool   `json:"in_step"`
			} `json:"columns"`
		} `json:"objects"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &printed); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(printed.Objects, func(o struct {
		Identity struct {
			Model string `json:"model"`
		} `json:"identity"`
		Outcome      string   `json:"outcome"`
		DuplicatedIn []string `json:"duplicated_in"`
		Columns      []struct {
			Source  string `json:"source"`
			Outcome string `json:"outcome"`
			InStep  bool   `json:"in_step"`
		} `json:"columns"`
	},
	) bool {
		return o.Identity.Model == "GS105"
	})
	if i < 0 {
		t.Fatalf("no GS105 in %s", r.stdout)
	}
	o := printed.Objects[i]
	if o.Outcome != "conflict" || !slices.Equal(o.DuplicatedIn, []string{"ours"}) || len(o.Columns) != 4 || !o.Columns[1].InStep || !o.Columns[2].InStep {
		t.Errorf("GS105, which ours defines twice: %+v", o)
	}

	r = runCLIApp(t, nil, configure, env, "compare")
	want := `  device type Netgear "GS105": ours defines it twice, in device-types/Ours/GS105-copy.yaml and device-types/Ours/GS105.yaml` + "\n"
	if !strings.Contains(r.stdout, want) {
		t.Errorf("compare doesn't say\n%s\nin:\n%s", want, r.stdout)
	}
}

// TestCompareClashAcrossSources checks that two device types NetBox hasn't
// got, which two sources would both create with one slug, clash.
func TestCompareClashAcrossSources(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, _, configure := twoSourcesEnv(t, nb,
		map[string]string{"device-types/Netgear/GS108.yaml": gs105As(t, "GS108", "netgear-gs108")},
		map[string]string{"GS108v2.yaml": gs105As(t, "GS108v2", "netgear-gs108")})
	r := runCLIApp(t, nil, configure, env, "compare", "--model", "GS108", "--model", "GS108v2")
	if r.code != exitOK {
		t.Fatalf("compare: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	for _, want := range []string{
		`  device type Netgear "GS108": it and device type Netgear "GS108v2" (ours, device-types/Ours/GS108v2.yaml) would both have the manufacturer "Netgear" and the slug "netgear-gs108", which NetBox keeps unique` + "\n",
		`  device type Netgear "GS108v2": it and device type Netgear "GS108" (lib, device-types/Netgear/GS108.yaml) would both have the manufacturer "Netgear" and the slug "netgear-gs108", which NetBox keeps unique` + "\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("compare doesn't say\n%s\nin:\n%s", want, r.stdout)
		}
	}
	r = runCLIApp(t, nil, configure, env, "compare", "--model", "GS108", "--model", "GS108v2", "-o", "json")
	var printed struct {
		Objects []struct {
			Outcome string `json:"outcome"`
			Clashes []struct {
				Rule    []string        `json:"rule"`
				Values  json.RawMessage `json:"values"`
				Columns []int           `json:"columns"`
				With    []struct {
					Source string `json:"source"`
					Path   string `json:"path"`
				} `json:"with"`
			} `json:"clashes"`
		} `json:"objects"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &printed); err != nil {
		t.Fatal(err)
	}
	var clashes []string
	for _, o := range printed.Objects {
		for _, c := range o.Clashes {
			var values bytes.Buffer
			if err := json.Compact(&values, c.Values); err != nil {
				t.Fatal(err)
			}
			clashes = append(clashes, o.Outcome+" "+strings.Join(c.Rule, ",")+" "+values.String()+" "+strconv.Itoa(len(c.Columns))+" "+c.With[0].Source+" "+c.With[0].Path)
		}
	}
	want := []string{
		`conflict manufacturer,slug {"manufacturer":{"id":1,"name":"Netgear"},"slug":"netgear-gs108"} 1 ours device-types/Ours/GS108v2.yaml`,
		`conflict manufacturer,slug {"manufacturer":{"id":1,"name":"Netgear"},"slug":"netgear-gs108"} 1 lib device-types/Netgear/GS108.yaml`,
	}
	if diff := cmp.Diff(want, clashes); diff != "" {
		t.Errorf("clashes (-want +got):\n%s", diff)
	}
}

// TestCompareASourceLoadedAgainLeavesTheOthers checks that a source loaded
// again between compare's two reads drops its columns, and the other
// source's are compared and recorded as usual.
func TestCompareASourceLoadedAgainLeavesTheOthers(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	// GS108 is compared only for ours' definition, which collides with
	// GS105 in NetBox: with ours gone, the library's isn't compared.
	env, dataDir, configure := twoSourcesEnv(t, nb,
		map[string]string{"device-types/Netgear/GS108.yaml": gs105As(t, "GS108", "netgear-gs108")},
		map[string]string{"GS105.yaml": oursGS105(t), "GS108.yaml": gs105As(t, "GS108", "netgear-gs105")})
	loaded := sourceDefinitions(t, dataDir, "ours")
	var once sync.Once
	nb.onManufacturers = func() {
		once.Do(func() {
			recordLoadIn(t, dataDir, store.Load{
				Source: "ours", Version: loaded.Loaded.Version, Reader: loaded.Loaded.Reader, Settings: loaded.Loaded.Settings, StartedAt: time.Now(),
				Files: loaded.Files, InvalidFiles: loaded.InvalidFiles, Definitions: loaded.Definitions,
			})
		})
	}
	r := runCLIApp(t, nil, configure, env, "compare", "-o", "json")
	if r.code != exitCompareProblems {
		t.Fatalf("exit code %d, want %d\nstderr: %s", r.code, exitCompareProblems, r.stderr)
	}
	got := decodeCompare(t, r.stdout)
	if src := got.Sources[1]; src.OK || src.Error == nil || !strings.Contains(*src.Error, "loaded again while it was compared") {
		t.Errorf("the other source = %+v", src)
	}
	for _, o := range got.Objects {
		if c := columnsText(o); strings.Contains(c, "ours") || o.name() == "GS108" {
			t.Errorf("%s is compared, with the columns %s", o.name(), c)
		}
	}
	if gs105 := got.Objects[4]; gs105.name() != "GS105" || !gs105.InStep {
		t.Errorf("GS105 = %s, in step %t, want the library's, in step", gs105.name(), gs105.InStep)
	}
	if n := len(recordedOf(t, dataDir, "lib")); n != 5 {
		t.Errorf("recorded %d of the library's comparisons, want 5", n)
	}
	if n := len(recordedOf(t, dataDir, "ours")); n != 0 {
		t.Errorf("recorded %d of the other source's comparisons, want none", n)
	}
}

// sourceDefinitions returns a source's definitions as the database holds
// them.
func sourceDefinitions(t *testing.T, dataDir, source string) store.SourceDefinitions {
	t.Helper()
	s, _, err := engine.Open(t.Context(), engine.Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	defs, err := s.Definitions(t.Context(), source, "")
	if err != nil {
		t.Fatal(err)
	}
	return defs
}

// pathText writes a row's path from compare's JSON as the text output does:
// fields joined by dots, and a child's key, of one field, in brackets, such
// as interfaces["eth0"].type or front_ports["F1"].rear_ports[1].
func pathText(path []any) string {
	var b strings.Builder
	for i, step := range path {
		switch step := step.(type) {
		case string:
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString(step)
		case map[string]any:
			for _, v := range step {
				if text, ok := v.(string); ok {
					b.WriteString("[" + strconv.Quote(text) + "]")
				} else {
					b.WriteString("[" + fmt.Sprint(v) + "]")
				}
			}
		}
	}
	return b.String()
}

// TestChooseWhole checks that choosing one definition of an object chooses
// every other of it, in each source's order, and leaves the other objects
// as they were chosen.
func TestChooseWhole(t *testing.T) {
	t.Parallel()
	compared := []sourceCompared{
		{chosen: []int{2}, rows: make([]store.Definition, 4)},
		{chosen: nil, rows: make([]store.Definition, 3)},
	}
	groups := [][]definitionAt{
		{{0, 0}, {1, 2}},
		{{0, 2}, {1, 1}, {0, 3}},
		{{0, 1}, {1, 0}},
	}
	chooseWhole(compared, groups)
	if got := [][]int{compared[0].chosen, compared[1].chosen}; !cmp.Equal(got, [][]int{{2, 3}, {1}}) {
		t.Errorf("chosen %v, want [[2 3] [1]]", got)
	}
}

// TestPresenceText checks how a child's row's cell is written: present,
// missing, or "-" where that version hasn't the child's parent.
func TestPresenceText(t *testing.T) {
	t.Parallel()
	for v, want := range map[model.Value]string{model.Boolean(true): "present", model.Boolean(false): "missing", nil: "-"} {
		if got := presenceText(v); got != want {
			t.Errorf("presenceText(%v) = %q, want %q", v, got, want)
		}
	}
}
