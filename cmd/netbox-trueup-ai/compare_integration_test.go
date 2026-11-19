//go:build integration

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/engine"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/postgres/pgtest"
)

var (
	parityLibrary = flag.String("parity-library", "",
		"the absolute path of a checkout of devicetype-library, to sample TestCompareParity's device types from, instead of the test library")
	paritySample = flag.Int("parity-sample", 200, "how many device types TestCompareParity samples from -parity-library")
)

// testLibrary holds the files copied from devicetype-library.
const testLibrary = "../../internal/format/devicetypelib/testdata/library"

// libraryTree reads a devicetype-library checkout's schemas and device
// types, by their paths in it, and returns them with the device types for
// NetBox to import: if sample isn't 0, that many, spread evenly through
// them, or else all but those left out.
func libraryTree(t *testing.T, root string, sample int, leftOut ...string) (files map[string]string, imported []string) {
	t.Helper()
	files = map[string]string{}
	var deviceTypes []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch dir, ext := path.Dir(rel), path.Ext(rel); {
		case path.Dir(dir) == "device-types" && (ext == ".yaml" || ext == ".yml"):
			deviceTypes = append(deviceTypes, rel)
		case dir == "schema" && ext == ".json":
			files[rel] = ""
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(deviceTypes)
	imported = slices.DeleteFunc(slices.Clone(deviceTypes), func(p string) bool { return slices.Contains(leftOut, p) })
	if len(imported) != len(deviceTypes)-len(leftOut) {
		t.Fatalf("the library hasn't got every device type left out: %v", leftOut)
	}
	if sample > 0 && sample < len(deviceTypes) {
		imported = make([]string, 0, sample)
		for i := range sample {
			imported = append(imported, deviceTypes[i*len(deviceTypes)/sample])
		}
	}
	for _, p := range deviceTypes {
		files[p] = ""
	}
	for p := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p))) //nolint:gosec // G304: a test reads the library it is pointed at.
		if err != nil {
			t.Fatal(err)
		}
		files[p] = string(data)
	}
	return files, imported
}

// parityEnv configures a source at repo, the NetBox and the PostgreSQL that
// TestMain started, and a data directory of the test's own.
func parityEnv(t *testing.T, repo string) []string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "token", nb.Token.Reveal()+"\n")
	return []string{
		"TRUEUP_DATA_DIR=" + t.TempDir(),
		"TRUEUP_SOURCES=" + `[{"name":"lib","kind":"git","url":"` + repo + `","format":"devicetype-library"}]`,
		"TRUEUP_NETBOX_URL=" + nb.URL,
		"TRUEUP_NETBOX_TOKEN_FILE=" + filepath.Join(dir, "token"),
		"TRUEUP_DATABASE_URL=" + pg.URL(pgtest.Superuser, pgtest.SuperuserPassword, pg.NewDatabase(t)),
	}
}

// importLibrary creates some of the source's device types, by their
// paths, and their manufacturers, through NetBox's own YAML import, as a
// person following devicetype-library's instructions does: each
// manufacturer with the slug NetBox's UI would give it, or, where two would
// share one, another, and then each device type file as it is. It fails the
// test for each file NetBox refuses, since TrueUp read every one as valid.
// It returns the keys of the definitions imported (objectJSON.key).
func importLibrary(t *testing.T, env []string, configure func(*app), files map[string]string, paths []string) map[string]bool {
	t.Helper()
	r := runCLIApp(t, nil, configure, env, "list", "-o", "json")
	if r.code != exitOK {
		t.Fatalf("list: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	var listed listResult
	if err := json.Unmarshal([]byte(r.stdout), &listed); err != nil {
		t.Fatal(err)
	}
	imported := map[string]bool{}
	makers := map[string]bool{}
	for _, d := range listed.Sources[0].Definitions {
		if d.Type == "dcim.devicetype" && d.Path != nil && slices.Contains(paths, *d.Path) {
			imported[*d.Path] = true
			maker, _ := d.Identity["manufacturer"].(map[string]any)
			name, _ := maker["name"].(string)
			makers[name] = true
		}
	}
	if len(imported) != len(paths) {
		t.Fatalf("list defines %d of the %d device types to import", len(imported), len(paths))
	}
	var b strings.Builder
	slugs := map[string]bool{}
	for _, d := range listed.Sources[0].Definitions {
		name, _ := d.Fields["name"].(string)
		if d.Type != "dcim.manufacturer" || !makers[name] {
			continue
		}
		identity, err := json.Marshal(map[string]string{"name": name})
		if err != nil {
			t.Fatal(err)
		}
		imported[string(identity)] = true
		slug, _ := d.Fields["slug"].(string)
		for slugs[slug] {
			slug += "-2"
		}
		slugs[slug] = true
		// JSON's strings are YAML's double-quoted scalars.
		quotedName, _ := json.Marshal(name)
		quoted, _ := json.Marshal(slug)
		b.WriteString("- name: " + string(quotedName) + "\n  slug: " + string(quoted) + "\n")
	}
	if err := nb.Import(t.Context(), "/dcim/manufacturers/import/", b.String()); err != nil {
		t.Fatalf("import the manufacturers: %v", err)
	}
	for _, p := range paths {
		importDeviceType(t, p, files[p])
	}
	return imported
}

func importDeviceType(t *testing.T, p, content string) {
	t.Helper()
	if !strings.HasPrefix(content, "---") {
		content = "---\n" + content
	}
	if err := nb.Import(t.Context(), "/dcim/device-types/import/", content); err != nil {
		t.Errorf("NetBox's import refuses %s, which TrueUp reads as valid: %v", p, err)
	}
}

// notInStep lists the objects compared that aren't in step with NetBox, as
// their outcome, model or name, and the fields that differ.
func notInStep(r compareResult) []string {
	out := []string{}
	for _, o := range r.Objects {
		if o.InStep {
			continue
		}
		line := o.Outcome + " " + o.name()
		var fields []string
		for _, row := range o.Rows {
			if row.Differs && row.Managed {
				fields = append(fields, pathText(row.Path))
			}
		}
		if len(fields) > 0 {
			line += ": " + strings.Join(fields, ", ")
		}
		out = append(out, line)
	}
	return out
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

// netboxID returns the ID of the NetBox object that the object named name
// matches.
func netboxID(t *testing.T, r compareResult, typ, name string) int {
	t.Helper()
	for _, o := range r.Objects {
		if o.Type == typ && o.name() == name && o.NetBoxID != nil {
			return *o.NetBoxID
		}
	}
	t.Fatalf("no %s %q matches a NetBox object", typ, name)
	return 0
}

// childID returns the ID of NetBox's component template that the device
// type modelName's child of that name in collection matches.
func childID(t *testing.T, r compareResult, modelName, collection, name string) int {
	t.Helper()
	for _, o := range r.Objects {
		if o.Type != "dcim.devicetype" || o.name() != modelName {
			continue
		}
		for _, row := range o.Rows {
			if len(row.Path) == 2 && row.Path[0] == collection && row.NetBoxID != nil {
				if key, _ := row.Path[1].(map[string]any); key["name"] == name {
					return *row.NetBoxID
				}
			}
		}
	}
	t.Fatalf("NetBox's %s has no %s %q that compare matched", modelName, collection, name)
	return 0
}

func compareJSONRun(t *testing.T, env []string, configure func(*app), args ...string) (compareResult, int) {
	t.Helper()
	r := runCLIApp(t, nil, configure, env, append([]string{"compare", "-o", "json"}, args...)...)
	if r.code != exitOK && r.code != exitNotInStep {
		t.Fatalf("compare: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	return decodeCompare(t, r.stdout), r.code
}

// TestCompareParity imports device types through NetBox's own YAML import,
// and checks that compare finds them in step, and that changing a field in
// NetBox makes exactly that difference appear.
func TestCompareParity(t *testing.T) {
	// The source holds the whole library, and NetBox some of it: of the
	// test library, all but a device type of a manufacturer it has, and
	// one of a manufacturer it hasn't.
	var files map[string]string
	var paths []string
	if *parityLibrary != "" {
		files, paths = libraryTree(t, *parityLibrary, *paritySample)
	} else {
		files, paths = libraryTree(t, testLibrary, 0, "device-types/Generic/shelf-2he.yaml", "device-types/Ubiquiti/UVC-G4-INS.yaml")
	}
	repo, pool, _ := gitRepoWith(t, files)
	env := parityEnv(t, repo)
	configure := trust(pool)
	fetched(t, pool, env)
	imported := importLibrary(t, env, configure, files, paths)

	// By default, compare compares what was imported, all in step, and
	// what collides with it, which can only be a conflict, such as a
	// manufacturer not imported whose slug is one imported. The rest is
	// counted.
	got, code := compareJSONRun(t, env, configure, "--exit-code")
	compared, colliding := map[string]bool{}, 0
	for _, o := range got.Objects {
		k := o.key(t)
		compared[k] = true
		switch {
		case imported[k] && !o.InStep:
			t.Errorf("%s, imported, is %s, not in step: %v", k, o.Outcome, notInStep(compareResult{Objects: []objectJSON{o}}))
		case !imported[k] && o.Outcome != "conflict":
			t.Errorf("%s, not imported, is compared, as a %s", k, o.Outcome)
		case !imported[k]:
			colliding++
		}
	}
	for k := range imported {
		if !compared[k] {
			t.Errorf("%s, imported, isn't compared", k)
		}
	}
	src := got.Sources[0]
	if wantCode := map[bool]int{true: exitOK, false: exitNotInStep}[colliding == 0]; code != wantCode || src.Compared != len(got.Objects) || src.Definitions <= src.Compared {
		t.Fatalf("after NetBox's import, compare --exit-code exits with %d, want %d, comparing %d of %d definitions",
			code, wantCode, src.Compared, src.Definitions)
	}
	if *parityLibrary == "" && colliding > 0 {
		t.Errorf("%d definitions not imported are compared", colliding)
	}
	t.Logf("%d definitions imported, %d not imported but colliding with them, %d not compared", len(imported), colliding, src.Definitions-src.Compared)
	t.Logf("%d definitions in step with what NetBox's import made of them", len(got.Objects))
	checkRecorded(t, env, got)
	if *parityLibrary != "" {
		return
	}

	huawei := netboxID(t, got, "dcim.manufacturer", "Huawei")
	ne8000 := netboxID(t, got, "dcim.devicetype", "NE8000-X8")
	deviceType, manufacturer := "/api/dcim/device-types/"+strconv.Itoa(ne8000)+"/", "/api/dcim/manufacturers/"+strconv.Itoa(huawei)+"/"
	template := func(endpoint, modelName, collection, name string) string {
		return "/api/dcim/" + endpoint + "/" + strconv.Itoa(childID(t, got, modelName, collection, name)) + "/"
	}
	iface := template("interface-templates", "GS105", "interfaces", "GigabitEthernet/1")
	outlet := template("power-outlet-templates", "TEST-1000", "power_outlets", "Power Outlet 2")
	port1 := childID(t, got, "TEST-1000", "power_ports", "Power Port 1")
	port2 := childID(t, got, "TEST-1000", "power_ports", "Power Port 2")
	test1000 := netboxID(t, got, "dcim.devicetype", "TEST-1000")
	rear1 := childID(t, got, "TEST-1000", "rear_ports", "Rear Port 1")
	front1 := template("front-port-templates", "TEST-1000", "front_ports", "Front Port 1")
	// A rear port only NetBox has, for a mapping to point to.
	var rear4 struct {
		ID int `json:"id"`
	}
	if err := nb.Post(t.Context(), "/api/dcim/rear-port-templates/",
		map[string]any{"device_type": test1000, "name": "Rear Port 4", "type": "8p8c"}, &rear4); err != nil {
		t.Fatal(err)
	}
	mapped := func(rear int) map[string]any {
		return map[string]any{"rear_ports": []any{map[string]any{"position": 1, "rear_port": rear, "rear_port_position": 1}}}
	}
	for _, tt := range []struct {
		name           string
		path           string
		change, undo   map[string]any
		wantNotInStep  []string
		wantExitStatus int
	}{
		{
			"a part number", deviceType,
			map[string]any{"part_number": "02351931"},
			map[string]any{"part_number": "02351930"},
			[]string{"candidate NE8000-X8: part_number"},
			exitNotInStep,
		},
		{
			"a height, a decimal", deviceType,
			map[string]any{"u_height": 15.5},
			map[string]any{"u_height": 16},
			[]string{"candidate NE8000-X8: u_height"},
			exitNotInStep,
		},
		{
			"airflow, a choice, made blank", deviceType,
			map[string]any{"airflow": ""},
			map[string]any{"airflow": "front-to-rear"},
			[]string{"candidate NE8000-X8: airflow"},
			exitNotInStep,
		},
		// The definition's model no longer matches, but its slug is still
		// the device type's.
		{
			"a model", deviceType,
			map[string]any{"model": "NE8000-X8 (renamed)"},
			map[string]any{"model": "NE8000-X8"},
			[]string{"conflict NE8000-X8"},
			exitNotInStep,
		},
		// Fields the definition doesn't set aren't managed.
		{
			"a field the definition leaves out", deviceType,
			map[string]any{"description": "from NetBox"},
			map[string]any{"description": ""},
			[]string{},
			exitOK,
		},
		// The library doesn't give a manufacturer's slug: TrueUp derives it,
		// to create the manufacturer only.
		{
			"a manufacturer's slug", manufacturer,
			map[string]any{"slug": "huawei-technologies"},
			map[string]any{"slug": "huawei"},
			[]string{},
			exitOK,
		},
		{
			"a manufacturer's description", manufacturer,
			map[string]any{"description": "from NetBox"},
			map[string]any{"description": ""},
			[]string{},
			exitOK,
		},
		{
			"an interface's type", iface,
			map[string]any{"type": "10gbase-t"},
			map[string]any{"type": "1000base-t"},
			[]string{`candidate GS105: interfaces["GigabitEthernet/1"].type`},
			exitNotInStep,
		},
		// A field of a template the definition leaves out.
		{
			"an interface's description", iface,
			map[string]any{"description": "from NetBox"},
			map[string]any{"description": ""},
			[]string{},
			exitOK,
		},
		// A reference to another template of the device type, by its name.
		{
			"an outlet's power port", outlet,
			map[string]any{"power_port": port2},
			map[string]any{"power_port": port1},
			[]string{`candidate TEST-1000: power_outlets["Power Outlet 2"].power_port`},
			exitNotInStep,
		},
		{
			"a rear port's positions", template("rear-port-templates", "TEST-1000", "rear_ports", "Rear Port 1"),
			map[string]any{"positions": 2},
			map[string]any{"positions": 1},
			[]string{`candidate TEST-1000: rear_ports["Rear Port 1"].positions`},
			exitNotInStep,
		},
		// A mapping, nested in its front port, by position, and its rear
		// port by name.
		{
			"a mapping's rear port", front1,
			mapped(rear4.ID),
			mapped(rear1),
			[]string{`candidate TEST-1000: front_ports["Front Port 1"].rear_ports[1].rear_port`},
			exitNotInStep,
		},
	} {
		if err := nb.Patch(t.Context(), tt.path, tt.change, nil); err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		got, code := compareJSONRun(t, env, configure, "--exit-code")
		if diff := cmp.Diff(tt.wantNotInStep, notInStep(got)); diff != "" || code != tt.wantExitStatus {
			t.Errorf("changing %s in NetBox: compare --exit-code exits with %d, want %d, and finds (-want +got):\n%s", tt.name, code, tt.wantExitStatus, diff)
		}
		if err := nb.Patch(t.Context(), tt.path, tt.undo, nil); err != nil {
			t.Fatalf("%s: undo: %v", tt.name, err)
		}
	}

	// A front port only NetBox has, mapped to a rear port only NetBox has,
	// is listed, but TrueUp leaves it alone, mappings and all.
	var front4 struct {
		ID int `json:"id"`
	}
	if err := nb.Post(t.Context(), "/api/dcim/front-port-templates/",
		map[string]any{"device_type": test1000, "name": "Front Port 4", "type": "8p8c", "rear_ports": mapped(rear4.ID)["rear_ports"]}, &front4); err != nil {
		t.Fatal(err)
	}
	got, code = compareJSONRun(t, env, configure, "--exit-code")
	if diff := cmp.Diff([]string{}, notInStep(got)); diff != "" || code != exitOK {
		t.Errorf("adding a front port in NetBox: compare --exit-code exits with %d, and finds (-want +got):\n%s", code, diff)
	}
	if id := childID(t, got, "TEST-1000", "front_ports", "Front Port 4"); id != front4.ID {
		t.Errorf("the front port added is listed as #%d, want #%d", id, front4.ID)
	}
	for _, path := range []string{"/api/dcim/front-port-templates/" + strconv.Itoa(front4.ID) + "/", "/api/dcim/rear-port-templates/" + strconv.Itoa(rear4.ID) + "/"} {
		if err := nb.Delete(t.Context(), path); err != nil {
			t.Fatal(err)
		}
	}

	// A front port deleted from NetBox, which deletes its mapping too, is
	// one difference, until it is created again.
	if err := nb.Delete(t.Context(), template("front-port-templates", "TEST-1000", "front_ports", "Front Port 3")); err != nil {
		t.Fatal(err)
	}
	got, _ = compareJSONRun(t, env, configure)
	if diff := cmp.Diff([]string{`candidate TEST-1000: front_ports["Front Port 3"]`}, notInStep(got)); diff != "" {
		t.Errorf("deleting a front port in NetBox: compare finds (-want +got):\n%s", diff)
	}
	for _, o := range got.Objects {
		if o.name() == "TEST-1000" && o.Differences != 1 {
			t.Errorf("deleting a front port in NetBox: TEST-1000 has %d differences, want 1", o.Differences)
		}
	}
	rear3 := childID(t, got, "TEST-1000", "rear_ports", "Rear Port 3")
	if err := nb.Post(t.Context(), "/api/dcim/front-port-templates/",
		map[string]any{"device_type": test1000, "name": "Front Port 3", "type": "8p8c", "rear_ports": mapped(rear3)["rear_ports"]}, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = compareJSONRun(t, env, configure)
	if diff := cmp.Diff([]string{}, notInStep(got)); diff != "" {
		t.Errorf("after creating the front port again, compare finds (-want +got):\n%s", diff)
	}

	// A template only NetBox has is listed, but TrueUp leaves it alone, so
	// the device type is still in step.
	gs105ID := netboxID(t, got, "dcim.devicetype", "GS105")
	var added struct {
		ID int `json:"id"`
	}
	if err := nb.Post(t.Context(), "/api/dcim/interface-templates/",
		map[string]any{"device_type": gs105ID, "name": "GigabitEthernet/9", "type": "1000base-t"}, &added); err != nil {
		t.Fatal(err)
	}
	got, code = compareJSONRun(t, env, configure, "--exit-code")
	if diff := cmp.Diff([]string{}, notInStep(got)); diff != "" || code != exitOK {
		t.Errorf("adding an interface in NetBox: compare --exit-code exits with %d, and finds (-want +got):\n%s", code, diff)
	}
	if id := childID(t, got, "GS105", "interfaces", "GigabitEthernet/9"); id != added.ID {
		t.Errorf("the interface added is listed as #%d, want #%d", id, added.ID)
	}
	if err := nb.Delete(t.Context(), "/api/dcim/interface-templates/"+strconv.Itoa(added.ID)+"/"); err != nil {
		t.Fatal(err)
	}

	// A template deleted from NetBox is one difference, whatever fields it
	// has, until it is created again.
	console := template("console-port-templates", "TEST-1000", "console_ports", "Console Port 3")
	if err := nb.Delete(t.Context(), console); err != nil {
		t.Fatal(err)
	}
	got, _ = compareJSONRun(t, env, configure)
	if diff := cmp.Diff([]string{`candidate TEST-1000: console_ports["Console Port 3"]`}, notInStep(got)); diff != "" {
		t.Errorf("deleting a console port in NetBox: compare finds (-want +got):\n%s", diff)
	}
	if err := nb.Post(t.Context(), "/api/dcim/console-port-templates/",
		map[string]any{"device_type": test1000, "name": "Console Port 3", "type": "de-9"}, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = compareJSONRun(t, env, configure)
	if diff := cmp.Diff([]string{}, notInStep(got)); diff != "" {
		t.Errorf("after creating the console port again, compare finds (-want +got):\n%s", diff)
	}

	// A device type deleted from NetBox is no longer compared by default,
	// and its comparison goes, but compared by its model, it is absent;
	// until it is imported again.
	gs105 := "/api/dcim/device-types/" + strconv.Itoa(gs105ID) + "/"
	if err := nb.Delete(t.Context(), gs105); err != nil {
		t.Fatal(err)
	}
	before := len(got.Objects)
	got, _ = compareJSONRun(t, env, configure)
	if diff := cmp.Diff([]string{}, notInStep(got)); diff != "" || len(got.Objects) != before-1 {
		t.Errorf("deleting a device type in NetBox: compare compares %d of %d, and finds (-want +got):\n%s", len(got.Objects), before, diff)
	}
	checkRecorded(t, env, got)
	selected, _ := compareJSONRun(t, env, configure, "--model", "GS105")
	if diff := cmp.Diff([]string{"absent GS105"}, notInStep(selected)); diff != "" {
		t.Errorf("deleting a device type in NetBox: compare --model GS105 finds (-want +got):\n%s", diff)
	}
	importDeviceType(t, "device-types/Netgear/GS105.yaml", files["device-types/Netgear/GS105.yaml"])
	got, _ = compareJSONRun(t, env, configure)
	if diff := cmp.Diff([]string{}, notInStep(got)); diff != "" || len(got.Objects) != before {
		t.Errorf("after importing the device type again, compare compares %d of %d, and finds (-want +got):\n%s", len(got.Objects), before, diff)
	}
	checkRecorded(t, env, got)
}

// checkRecorded checks that the database, PostgreSQL here, records each
// object as compare printed it.
func checkRecorded(t *testing.T, env []string, got compareResult) {
	t.Helper()
	var databaseURL string
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "TRUEUP_DATABASE_URL="); ok {
			databaseURL = v
		}
	}
	s, _, err := engine.Open(t.Context(), engine.Config{URL: config.NewSecret(databaseURL), DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	recorded, err := s.Comparisons(t.Context(), "lib")
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded) != len(got.Objects) {
		t.Errorf("PostgreSQL records %d comparisons, want %d", len(recorded), len(got.Objects))
	}
	outcomes := map[string]string{}
	for _, o := range got.Objects {
		outcomes[o.key(t)] = o.Outcome
	}
	for _, c := range recorded {
		k := c.Path
		if k == "" {
			k = c.Identity
		}
		switch {
		case c.Outcome != outcomes[k] || c.NetBoxVersion != "4.7.2":
			t.Errorf("recorded %+v of %s, want what compare printed: %q", c.Comparison, k, outcomes[k])
		case c.Outcome == "candidate" && (c.NetBoxID == 0 || c.Differences != 0):
			t.Errorf("recorded %+v of %s, want a candidate in step", c.Comparison, k)
		}
	}
}
