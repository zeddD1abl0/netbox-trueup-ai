package devicetypelib

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source/sourcetest"
)

var update = flag.Bool("update", false, "rewrite testdata/library.golden.json from what Read makes of testdata/library, and add the format's version to testdata/versions.json")

// TestReadLibrary reads the files copied from devicetype-library and checks
// the result against testdata/library.golden.json: every definition, field
// by field, and every problem.
func TestReadLibrary(t *testing.T) {
	t.Parallel()
	snap := sourcetest.Snapshot(t, "lib", os.DirFS("testdata/library"))
	res, err := Format{}.Read(t.Context(), snap)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const golden = "testdata/library.golden.json"
	if *update {
		if err := os.WriteFile(golden, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("Read (-%s +got); run go test -update to accept:\n%s", golden, diff)
	}
	if res.Files != 20 || res.Invalid != 0 {
		t.Errorf("read %d files, %d invalid; want 20 and none", res.Files, res.Invalid)
	}
}

// TestVersion holds Format.Version to what Read gives for the files copied
// from devicetype-library, as TrueUp keeps it: testdata/versions.json gives
// each version the SHA-256 of every definition as EncodeDefinition writes
// it, and every problem. When what Read gives changes, the version must
// change too, since a database keeps the definitions an older version read
// until the version changes. -update adds the current version, but never
// changes one already there.
func TestVersion(t *testing.T) {
	t.Parallel()
	snap := sourcetest.Snapshot(t, "lib", os.DirFS("testdata/library"))
	res, err := Format{}.Read(t.Context(), snap)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	for _, d := range res.Definitions {
		data, err := format.EncodeDefinition(specs, d)
		if err != nil {
			t.Fatal(err)
		}
		h.Write(append(data, '\n'))
	}
	problems, err := json.Marshal(struct {
		Problems       []format.Problem
		Files, Invalid int
	}{res.Problems, res.Files, res.Invalid})
	if err != nil {
		t.Fatal(err)
	}
	h.Write(problems)
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))

	const file = "testdata/versions.json"
	versions := map[string]string{}
	data, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &versions); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	version := strconv.Itoa(Format{}.Version())
	want, known := versions[version]
	switch {
	case known && want != got:
		t.Errorf("what Read gives for testdata/library has changed, but Format.Version is still %s: raise it, and run go test -update", version)
	case !known && *update:
		versions[version] = got
		data, err := json.MarshalIndent(versions, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	case !known:
		t.Errorf("%s has no version %s: run go test -update", file, version)
	}
}

// TestReadLibraryAsNetBoxReadsIt checks what matters most in the copied
// files, beyond the golden file: values read as PyYAML reads them, metadata
// kept apart, components counted, and manufacturers derived.
func TestReadLibraryAsNetBoxReadsIt(t *testing.T) {
	t.Parallel()
	snap := sourcetest.Snapshot(t, "lib", os.DirFS("testdata/library"))
	res, err := Format{}.Read(t.Context(), snap)
	if err != nil {
		t.Fatal(err)
	}
	defs := map[string]format.Definition{}
	for _, d := range res.Definitions {
		if d.Path != "" {
			defs[d.Path] = d
		} else {
			name, _ := d.Identity["name"].(model.String)
			defs[d.Object.Type+":"+string(name)] = d
		}
	}
	huawei := defs["device-types/Huawei/NE8000-X8.yaml"]
	for field, want := range map[string]model.Value{
		// Not octal, so text, as PyYAML reads it.
		"part_number":   model.String("02351930"),
		"u_height":      model.NewDecimal(16, 0),
		"is_full_depth": model.Boolean(true),
		"airflow":       model.Choice("front-to-rear"),
		"weight":        model.NewDecimal(94, 0),
		"weight_unit":   model.Choice("kg"),
		"manufacturer":  model.Reference{Type: "dcim.manufacturer", Key: map[string]model.Value{"name": model.String("Huawei")}},
	} {
		if got := huawei.Object.Fields[field]; !model.Equal(got, want) {
			t.Errorf("NE8000-X8's %s = %v, want %v", field, got, want)
		}
	}
	if bays, _ := huawei.Object.Fields["module_bays"].(model.Children); len(bays) != 18 || huawei.Unmapped != nil || huawei.Metadata != nil {
		t.Errorf("NE8000-X8 has %d module bays, %v unmapped and metadata %v, want 18 module bays and neither", len(bays), huawei.Unmapped, huawei.Metadata)
	}
	wallBox := defs["device-types/Generic/wall-box-1-utp-plug.yaml"]
	fronts, _ := wallBox.Object.Fields["front_ports"].(model.Children)
	var mappings model.Children
	if len(fronts) == 1 {
		mappings, _ = fronts[0].Fields["rear_ports"].(model.Children)
	}
	if !model.Equal(wallBox.Object.Fields["u_height"], model.NewDecimal(0, 0)) ||
		!model.Equal(wallBox.Metadata["is_powered"], model.Boolean(false)) ||
		len(fronts) != 1 || len(mappings) != 1 {
		t.Errorf("wall box = %+v, want u_height 0, is_powered false as metadata and one front port with one mapping", wallBox)
	}
	if _, ok := wallBox.Object.Fields["is_powered"]; ok {
		t.Error("is_powered is a field, not metadata")
	}
	// Three spellings, three manufacturers; the slugs NetBox's UI makes.
	for name, slug := range map[string]string{
		"Netgear": "netgear", "NETGEAR": "netgear", "GL.iNet": "gl-inet", "Rohde & Schwarz": "rohde-schwarz",
	} {
		m, ok := defs["dcim.manufacturer:"+name]
		if !ok {
			t.Errorf("no manufacturer %q", name)
			continue
		}
		if got := m.Object.Fields["slug"]; !model.Equal(got, model.String(slug)) || !cmp.Equal(m.Derived, []string{"slug"}) {
			t.Errorf("manufacturer %q has slug %v, derived %v; want %q, derived", name, got, m.Derived, slug)
		}
	}
	// Testing/Test-1000.yaml names Generic, whatever its directory.
	if got := defs["dcim.manufacturer:Generic"].DerivedFrom; !cmp.Equal(got, []string{
		"device-types/Generic/shelf-2he.yaml", "device-types/Generic/wall-box-1-utp-plug.yaml", "device-types/Testing/Test-1000.yaml",
	}) {
		t.Errorf("Generic is derived from %v", got)
	}
}

// librarySchemas returns the library's schemas, copied from it.
func librarySchemas(t testing.TB) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	entries, err := os.ReadDir("testdata/library/schema")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(path.Join("testdata/library/schema", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		fsys["schema/"+e.Name()] = &fstest.MapFile{Data: data}
	}
	return fsys
}

// withFiles returns the library's schemas and the device types given, by
// path.
// lenientSchema stands for the library's device type schema, but takes any
// mapping, as a source's own schemas might, so a test reaches what the
// format checks itself.
const lenientSchema = `{"$id": "urn:devicetype-library:device-type", "type": "object"}`

func withFiles(t *testing.T, files map[string]string) fstest.MapFS {
	t.Helper()
	fsys := librarySchemas(t)
	for p, content := range files {
		fsys[p] = &fstest.MapFile{Data: []byte(content)}
	}
	return fsys
}

const valid = "manufacturer: Acme\nmodel: Box 1\nslug: acme-box-1\nu_height: 1\nis_full_depth: true\n"

// TestReadProblems checks what is wrong with files written by hand to break
// one rule each.
func TestReadProblems(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		file string
		want []format.Problem // without severity and path when they're an error in the file
	}{
		{"valid", valid, nil},
		{"not YAML", "manufacturer: [\n", []format.Problem{{Line: 1, Reason: "isn't valid YAML: did not find expected node content"}}},
		{"a duplicate key", valid + "model: Box 2\n", []format.Problem{{Line: 6, Column: 1, Field: "model", Reason: "the key is given twice, on lines 2 and 6"}}},
		{"an alias", "manufacturer: &m Acme\nmodel: *m\n", []format.Problem{{Line: 1, Column: 15, Field: "manufacturer", Reason: "YAML anchors (&) aren't supported"}}},
		{"YAML 1.1's booleans", strings.Replace(valid, "Box 1", "yes", 1), []format.Problem{
			{Line: 2, Column: 8, Field: "model", Reason: "is true or false, but the schema wants text"},
		}},
		{"YAML 1.1's octal", strings.Replace(valid, "Box 1", "010", 1), []format.Problem{
			{Line: 2, Column: 8, Field: "model", Reason: "is a whole number, but the schema wants text"},
		}},
		{"the schema's rules", valid + "airflow: sideways\nweight: 1.001\nextra: 1\n", []format.Problem{
			// The schema requires a weight_unit with a weight, of the
			// whole file.
			{Line: 1, Column: 1, Reason: `needs "weight_unit", which the schema requires`},
			{Line: 6, Column: 10, Field: "airflow", Reason: `"sideways" isn't one of the 10 values the schema allows`},
			{Line: 7, Column: 9, Field: "weight", Reason: "1.001 isn't a multiple of 0.01, as the schema requires"},
			{Line: 8, Column: 1, Field: "extra", Reason: "isn't a key the schema allows here"},
		}},
		{"what NetBox refuses but the schema doesn't", strings.Replace(valid, "u_height: 1", "u_height: 1000", 1), []format.Problem{
			{Line: 4, Column: 11, Field: "u_height", Reason: "1000 has more than 3 digits before the decimal point"},
		}},
		{"a manufacturer with white space", strings.Replace(valid, "Acme", `" Acme"`, 1), []format.Problem{
			{Line: 1, Column: 15, Field: "manufacturer", Reason: `" Acme" has white space at an end: NetBox trims a manufacturer's name, but its import looks for the name as the file gives it, and wouldn't find it`},
		}},
		{"a manufacturer with a NUL", strings.Replace(valid, "Acme", `"Ac\0me"`, 1), []format.Problem{
			{Line: 1, Column: 15, Field: "manufacturer", Reason: `"Ac\x00me" holds a NUL character, which NetBox refuses`},
		}},
		{"a date", strings.Replace(valid, "Box 1", "2024-01-01", 1), []format.Problem{
			{Line: 2, Column: 8, Field: "model", Reason: `"2024-01-01" reads as a date or time, which the format doesn't take: quote it if it is text`},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			const p = "device-types/Acme/box.yaml"
			snap := sourcetest.Snapshot(t, "lib", withFiles(t, map[string]string{p: tt.file}))
			res, err := Format{}.Read(t.Context(), snap)
			if err != nil {
				t.Fatal(err)
			}
			for i := range tt.want {
				tt.want[i].Severity, tt.want[i].Path = format.Error, p
			}
			if diff := cmp.Diff(tt.want, res.Problems); diff != "" {
				t.Errorf("problems (-want +got):\n%s", diff)
			}
			wantDefs := 2 // the device type and its manufacturer
			if tt.want != nil {
				wantDefs = 0
			}
			if len(res.Definitions) != wantDefs || res.Files != 1 || res.Invalid != 1-wantDefs/2 {
				t.Errorf("%d definitions, %d files, %d invalid; want %d, 1 and %d", len(res.Definitions), res.Files, res.Invalid, wantDefs, 1-wantDefs/2)
			}
		})
	}
}

// components is a file with one of each list TrueUp reads.
const components = valid + `subdevice_role: parent
console-ports:
  - name: Console
    type: rj-45
    _is_power_source: true
console-server-ports:
  - name: Port 1
    type: rj-45
    label: P1
power-ports:
  - name: PSU1
    type: iec-60320-c14
    maximum_draw: 500
    allocated_draw: 2.0
  - name: " PSU2 "
    type: iec-60320-c14
power-outlets:
  - name: Outlet 1
    type: iec-60320-c13
    power_port: PSU2
    feed_leg: A
  - name: Outlet 2
    type: iec-60320-c13
    power_port: ""
interfaces:
  - name: eth0
    type: 1000base-t
    mgmt_only: true
    enabled: false
    poe_mode: pse
    poe_type: type2-ieee802.3at
  - name: br0
    type: bridge
    bridge: eth0
module-bays:
  - name: Slot 1
    position: "1"
device-bays:
  - name: Bay 1
    description: left
port-mappings:
  - front_port: F1
    front_port_position: 1
    rear_port: R1
    rear_port_position: 2
  - front_port: F2
    front_port_position: 2
    rear_port: R1
    rear_port_position: 1
front-ports:
  - name: F1
    type: 8p8c
    positions: 1
  - name: F2
    type: lc
    color: 00ff00
    positions: 2.0
    label: Fibre
  - name: F3
    type: lc
    positions: 1
rear-ports:
  - name: R1
    type: 8p8c
    positions: 2
    description: patch
    _is_power_source: false
`

// TestReadComponents checks the component templates read from each list:
// their fields as NetBox's import takes them, keys TrueUp drops, and port
// mappings folded into their front ports, whatever the order of the lists.
func TestReadComponents(t *testing.T) {
	t.Parallel()
	const p = "device-types/Acme/box.yaml"
	res, err := Format{}.Read(t.Context(), sourcetest.Snapshot(t, "lib", withFiles(t, map[string]string{p: components})))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) > 0 || len(res.Definitions) != 2 {
		t.Fatalf("problems %v, %d definitions; want none and 2", res.Problems, len(res.Definitions))
	}
	dt := res.Definitions[1]
	child := func(typ string, fields map[string]model.Value) model.Object {
		return model.Object{Type: "dcim." + typ + "template", Fields: fields}
	}
	s := func(v string) model.Value { return model.String(v) }
	c := func(v string) model.Value { return model.Choice(v) }
	want := map[string]model.Children{
		"console_ports":        {child("consoleport", map[string]model.Value{"name": s("Console"), "type": c("rj-45")})},
		"console_server_ports": {child("consoleserverport", map[string]model.Value{"name": s("Port 1"), "type": c("rj-45"), "label": s("P1")})},
		"power_ports": {
			child("powerport", map[string]model.Value{"name": s("PSU1"), "type": c("iec-60320-c14"), "maximum_draw": model.Integer(500), "allocated_draw": model.Integer(2)}),
			// Trimmed, as NetBox stores it, so an outlet finds it by
			// PSU2.
			child("powerport", map[string]model.Value{"name": s("PSU2"), "type": c("iec-60320-c14")}),
		},
		"power_outlets": {
			child("poweroutlet", map[string]model.Value{
				"name": s("Outlet 1"), "type": c("iec-60320-c13"), "feed_leg": c("A"),
				"power_port": model.Reference{Type: "dcim.powerporttemplate", Key: map[string]model.Value{"name": s("PSU2")}},
			}),
			// NetBox's import takes a blank power port as none.
			child("poweroutlet", map[string]model.Value{"name": s("Outlet 2"), "type": c("iec-60320-c13"), "power_port": model.Null{}}),
		},
		"interfaces": {
			child("interface", map[string]model.Value{
				"name": s("eth0"), "type": c("1000base-t"), "mgmt_only": model.Boolean(true), "enabled": model.Boolean(false),
				"poe_mode": c("pse"), "poe_type": c("type2-ieee802.3at"),
			}),
			// No bridge: NetBox's import ignores it.
			child("interface", map[string]model.Value{"name": s("br0"), "type": c("bridge")}),
		},
		"module_bays": {child("modulebay", map[string]model.Value{"name": s("Slot 1"), "position": s("1")})},
		"device_bays": {child("devicebay", map[string]model.Value{"name": s("Bay 1"), "description": s("left")})},
		"rear_ports":  {child("rearport", map[string]model.Value{"name": s("R1"), "type": c("8p8c"), "positions": model.Integer(2), "description": s("patch")})},
		"front_ports": {
			child("frontport", map[string]model.Value{
				"name": s("F1"), "type": c("8p8c"), "positions": model.Integer(1),
				"rear_ports": model.Children{mappingOf(1, "R1", 2)},
			}),
			// A front port position beyond the front port's is no error to
			// NetBox's import.
			child("frontport", map[string]model.Value{
				"name": s("F2"), "type": c("lc"), "color": s("00ff00"), "positions": model.Integer(2), "label": s("Fibre"),
				"rear_ports": model.Children{mappingOf(2, "R1", 1)},
			}),
			// No mappings: the definition doesn't manage them.
			child("frontport", map[string]model.Value{"name": s("F3"), "type": c("lc"), "positions": model.Integer(1)}),
		},
	}
	for name, children := range want {
		if got := dt.Object.Fields[name]; !model.Equal(got, children) {
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(children)
			t.Errorf("%s = %s, want %s", name, gotJSON, wantJSON)
		}
	}
	if dt.Unmapped != nil {
		t.Errorf("unmapped = %v, want none", dt.Unmapped)
	}
}

// mappingOf is a front port's mapping as the format reads it.
func mappingOf(position int64, rear string, rearPosition int64) model.Object {
	return model.Object{Type: "dcim.porttemplatemapping", Fields: map[string]model.Value{
		"position":           model.Integer(position),
		"rear_port":          model.Reference{Type: "dcim.rearporttemplate", Key: map[string]model.Value{"name": model.String(rear)}},
		"rear_port_position": model.Integer(rearPosition),
	}}
}

// TestReadComponentProblems checks what is wrong with component lists that
// break one rule each, with the library's schema and with one that lets
// anything through.
// portsFile gives a device type rear ports R1, with one position, and R2,
// with two, and front ports F1 and F2, with one each, on lines 6 to 19.
const portsFile = `rear-ports:
  - name: R1
    type: 8p8c
    positions: 1
  - name: R2
    type: lc
    positions: 2
front-ports:
  - name: F1
    type: 8p8c
    positions: 1
  - name: F2
    type: lc
    positions: 1
`

// mappingYAML is one of a file's port mappings, on four lines.
func mappingYAML(front string, frontPosition int, rear string, rearPosition int) string {
	return fmt.Sprintf("  - front_port: %s\n    front_port_position: %d\n    rear_port: %s\n    rear_port_position: %d\n", front, frontPosition, rear, rearPosition)
}

func TestReadComponentProblems(t *testing.T) {
	t.Parallel()
	ports := "power-ports:\n  - name: PSU1\n    type: iec-60320-c14\n"
	outlet := "power-outlets:\n  - name: O1\n    type: iec-60320-c13\n    power_port: PSU1\n"
	tests := []struct {
		name    string
		lenient bool
		file    string
		want    []format.Problem
	}{
		{"an outlet's power port the device type hasn't got", false, valid + ports + "power-outlets:\n  - name: O1\n    type: iec-60320-c13\n    power_port: PSU9\n", []format.Problem{
			{Severity: format.Error, Line: 12, Column: 17, Field: "power-outlets[0].power_port", Reason: `names "PSU9", which isn't one of the device type's power-ports, so NetBox's import wouldn't find it`},
		}},
		// NetBox creates power ports before outlets, whatever the order of
		// the file's keys.
		{"outlets before their power ports", false, valid + "power-outlets:\n  - name: O1\n    type: iec-60320-c13\n    power_port: PSU1\n" + ports, nil},
		{"an outlet's power port with white space", false, valid + ports + "power-outlets:\n  - name: O1\n    type: iec-60320-c13\n    power_port: \" PSU1\"\n", []format.Problem{
			{Severity: format.Error, Line: 12, Column: 17, Field: "power-outlets[0].power_port", Reason: `" PSU1" has white space at an end: NetBox trims a template's name, but its import looks for the name as the file gives it, and wouldn't find it`},
		}},
		{"a name given twice once trimmed", false, valid + "interfaces:\n  - name: eth0\n    type: 1000base-t\n  - name: \"eth0 \"\n    type: 1000base-t\n", []format.Problem{
			{Severity: format.Error, Line: 9, Column: 5, Field: "interfaces[1].name", Reason: `is "eth0", as interfaces[0]'s is, but NetBox keeps a name unique among a device type's interfaces`},
		}},
		{"a draw that isn't whole", false, valid + "power-ports:\n  - name: PSU1\n    type: iec-60320-c14\n    maximum_draw: 2.5\n", []format.Problem{
			{Severity: format.Error, Line: 9, Column: 19, Field: "power-ports[0].maximum_draw", Reason: "is a number, but the schema wants a whole number"},
		}},
		{"a draw NetBox refuses", false, valid + "power-ports:\n  - name: PSU1\n    type: iec-60320-c14\n    maximum_draw: 3000000000\n", []format.Problem{
			{Severity: format.Error, Line: 9, Column: 19, Field: "power-ports[0].maximum_draw", Reason: "3000000000 is more than 2147483647"},
		}},
		// The power port's own error, not the outlet's too.
		{"an outlet's power port with an error of its own", true, valid + "power-ports:\n  - name: PSU1\n    type: bogus\n" + "power-outlets:\n  - name: O1\n    type: iec-60320-c13\n    power_port: PSU1\n", []format.Problem{
			{Severity: format.Error, Line: 8, Column: 11, Field: "power-ports[0].type", Reason: `"bogus" isn't one of its choices`},
		}},
		// A power port with an error of its own still has a name, so an
		// outlet naming another is blamed.
		{"an outlet's missing power port beside one with an error", true, valid + "power-ports:\n  - name: PSU1\n    type: bogus\n" + "power-outlets:\n  - name: O1\n    type: iec-60320-c13\n    power_port: PSU9\n", []format.Problem{
			{Severity: format.Error, Line: 8, Column: 11, Field: "power-ports[0].type", Reason: `"bogus" isn't one of its choices`},
			{Severity: format.Error, Line: 12, Column: 17, Field: "power-outlets[0].power_port", Reason: `names "PSU9", which isn't one of the device type's power-ports, so NetBox's import wouldn't find it`},
		}},
		// The power ports' error, not the outlet's too, since the power
		// port it names may be the one TrueUp can't read.
		{"an outlet's power port in a list that isn't one", true, valid + "power-ports:\n  name: PSU1\n  type: iec-60320-c14\n" + outlet, []format.Problem{
			{Severity: format.Error, Line: 7, Column: 3, Field: "power-ports", Reason: "isn't a list, which devicetype-library's power-ports is"},
		}},
		{"an outlet's power port that isn't a mapping", true, valid + "power-ports:\n  - PSU1\n" + outlet, []format.Problem{
			{Severity: format.Error, Line: 7, Column: 5, Field: "power-ports[0]", Reason: "is text, but each of devicetype-library's power-ports is a mapping"},
		}},
		{"an outlet's power port without a name", true, valid + "power-ports:\n  - type: iec-60320-c14\n" + outlet, []format.Problem{
			{Severity: format.Error, Line: 7, Column: 5, Field: "power-ports[0]", Reason: "has no name, which NetBox needs to create one of a device type's power-ports"},
		}},
		{"a list item that isn't a mapping", true, valid + "device-bays:\n  - Bay 1\n", []format.Problem{
			{Severity: format.Error, Line: 7, Column: 5, Field: "device-bays[0]", Reason: "is text, but each of devicetype-library's device-bays is a mapping"},
		}},
		{"a list item without what NetBox needs", true, valid + "interfaces:\n  - name: eth0\n", []format.Problem{
			{Severity: format.Error, Line: 7, Column: 5, Field: "interfaces[0]", Reason: "has no type, which NetBox needs to create one of a device type's interfaces"},
		}},
		{"a key TrueUp doesn't read", true, valid + "interfaces:\n  - name: eth0\n    type: 1000base-t\n    parent: eth1\n", []format.Problem{
			{Severity: format.Warning, Line: 9, Column: 5, Field: "interfaces[0].parent", Reason: "isn't a key TrueUp reads from devicetype-library's interfaces yet, so it is ignored"},
		}},
		{"a dropped key of the wrong kind", true, valid + "console-ports:\n  - name: con\n    type: rj-45\n    _is_power_source: \"yes\"\n", []format.Problem{
			{Severity: format.Error, Line: 9, Column: 23, Field: "console-ports[0]._is_power_source", Reason: "is text, but devicetype-library's _is_power_source is true or false"},
		}},
		{"a power port named by a number", true, valid + ports + "power-outlets:\n  - name: O1\n    type: iec-60320-c13\n    power_port: 1\n", []format.Problem{
			{Severity: format.Error, Line: 12, Column: 17, Field: "power-outlets[0].power_port", Reason: "is a whole number, but it names another of the device type's templates, as text"},
		}},
		// Port mappings, at line 21 after portsFile; each key's value is at
		// column 17, 26, 16 and 25.
		{"a mapping's front port the device type hasn't got", false, valid + portsFile + "port-mappings:\n" + mappingYAML("F9", 1, "R1", 1), []format.Problem{
			{Severity: format.Error, Line: 21, Column: 17, Field: "port-mappings[0].front_port", Reason: `names "F9", which isn't one of the device type's front-ports, so NetBox's import wouldn't find it`},
		}},
		{"a mapping's rear port the device type hasn't got", false, valid + portsFile + "port-mappings:\n" + mappingYAML("F1", 1, "R9", 1), []format.Problem{
			{Severity: format.Error, Line: 23, Column: 16, Field: "port-mappings[0].rear_port", Reason: `names "R9", which isn't one of the device type's rear-ports, so NetBox's import wouldn't find it`},
		}},
		{"a rear port position beyond the rear port's", false, valid + portsFile + "port-mappings:\n" + mappingYAML("F1", 1, "R1", 2), []format.Problem{
			{Severity: format.Error, Line: 24, Column: 25, Field: "port-mappings[0].rear_port_position", Reason: `is 2, but rear-ports "R1" has only 1 positions, so NetBox's import would refuse it`},
		}},
		// A mapping with an error of its own takes no positions, so the
		// next isn't blamed for F1's position 1 too.
		{"a mapping after one with an error of its own", false, valid + portsFile + "port-mappings:\n" + mappingYAML("F1", 1, "R1", 2) + mappingYAML("F1", 1, "R2", 1), []format.Problem{
			{Severity: format.Error, Line: 24, Column: 25, Field: "port-mappings[0].rear_port_position", Reason: `is 2, but rear-ports "R1" has only 1 positions, so NetBox's import would refuse it`},
		}},
		// One naming a port the file hasn't got has that error of its own.
		{"a mapping after one naming a rear port the file hasn't got", false, valid + portsFile + "port-mappings:\n" + mappingYAML("F1", 1, "R9", 1) + mappingYAML("F1", 1, "R2", 1), []format.Problem{
			{Severity: format.Error, Line: 23, Column: 16, Field: "port-mappings[0].rear_port", Reason: `names "R9", which isn't one of the device type's rear-ports, so NetBox's import wouldn't find it`},
		}},
		// NetBox's import checks only the rear port's positions.
		{"a front port position beyond the front port's", false, valid + portsFile + "port-mappings:\n" + mappingYAML("F1", 2, "R1", 1), nil},
		{"a front port's position mapped twice", false, valid + portsFile + "port-mappings:\n" + mappingYAML("F1", 1, "R2", 1) + mappingYAML("F1", 1, "R2", 2), []format.Problem{
			{Severity: format.Error, Line: 26, Column: 26, Field: "port-mappings[1].front_port_position", Reason: `maps position 1 of front-ports "F1", as port-mappings[0] does, but NetBox maps each position of a front port once`},
		}},
		{"a rear port's position mapped twice", false, valid + portsFile + "port-mappings:\n" + mappingYAML("F1", 1, "R2", 1) + mappingYAML("F2", 1, "R2", 1), []format.Problem{
			{Severity: format.Error, Line: 28, Column: 25, Field: "port-mappings[1].rear_port_position", Reason: `maps to position 1 of rear-ports "R2", as port-mappings[0] does, but NetBox maps each position of a rear port once`},
		}},
		// NetBox creates the ports before their mappings, whatever the
		// order of the file's keys.
		{"mappings before their ports", false, valid + "port-mappings:\n" + mappingYAML("F1", 1, "R1", 1) + portsFile, nil},
		{"a mapping that isn't a mapping", true, valid + portsFile + "port-mappings:\n  - F1\n", []format.Problem{
			{Severity: format.Error, Line: 21, Column: 5, Field: "port-mappings[0]", Reason: "is text, but each of devicetype-library's port-mappings is a mapping"},
		}},
		{"a mapping without its front port", true, valid + portsFile + "port-mappings:\n  - front_port_position: 1\n    rear_port: R1\n    rear_port_position: 1\n", []format.Problem{
			{Severity: format.Error, Line: 21, Column: 5, Field: "port-mappings[0]", Reason: "has no front_port, which NetBox needs to map a front port"},
		}},
		{"a mapping without its ports", true, valid + portsFile + "port-mappings:\n  - front_port_position: 1\n    rear_port_position: 1\n", []format.Problem{
			{Severity: format.Error, Line: 21, Column: 5, Field: "port-mappings[0]", Reason: "has no front_port, which NetBox needs to map a front port"},
			{Severity: format.Error, Line: 21, Column: 5, Field: "port-mappings[0]", Reason: "has no rear_port, which NetBox needs to map a front port"},
		}},
		{"a mapping's port named by a number", true, valid + portsFile + "port-mappings:\n" + mappingYAML("1", 1, "R1", 1), []format.Problem{
			{Severity: format.Error, Line: 21, Column: 17, Field: "port-mappings[0].front_port", Reason: "is a whole number, but it names one of the device type's front-ports, as text"},
		}},
		{"a mapping's port named blank", true, valid + portsFile + "port-mappings:\n" + mappingYAML(`""`, 1, "R1", 1), []format.Problem{
			{Severity: format.Error, Line: 21, Column: 17, Field: "port-mappings[0].front_port", Reason: "is blank, but NetBox's import needs one of the device type's front-ports"},
		}},
		{"a mapping's port with white space", true, valid + portsFile + "port-mappings:\n" + mappingYAML("F1", 1, `" R1"`, 1), []format.Problem{
			{Severity: format.Error, Line: 23, Column: 16, Field: "port-mappings[0].rear_port", Reason: `" R1" has white space at an end: NetBox trims a template's name, but its import looks for the name as the file gives it, and wouldn't find it`},
		}},
		{"a mapping's position NetBox refuses", true, valid + portsFile + "port-mappings:\n" + mappingYAML("F1", 0, "R1", 1), []format.Problem{
			{Severity: format.Error, Line: 22, Column: 26, Field: "port-mappings[0].front_port_position", Reason: "0 is less than 1"},
		}},
		{"a mapping's key TrueUp doesn't read", true, valid + portsFile + "port-mappings:\n" + mappingYAML("F1", 1, "R1", 1) + "    cable: 1\n", []format.Problem{
			{Severity: format.Warning, Line: 25, Column: 5, Field: "port-mappings[0].cable", Reason: "isn't a key TrueUp reads from devicetype-library's port-mappings yet, so it is ignored"},
		}},
		// NetBox's import fills a position left out with 1, so this maps
		// F1's position 1 twice; the error is at the item, which has no
		// position of its own.
		{"a mapping without its front port position", true, valid + portsFile + "port-mappings:\n" + mappingYAML("F1", 1, "R1", 1) + "  - front_port: F1\n    rear_port: R2\n    rear_port_position: 1\n", []format.Problem{
			{Severity: format.Error, Line: 25, Column: 5, Field: "port-mappings[1].front_port_position", Reason: `maps position 1 of front-ports "F1", as port-mappings[0] does, but NetBox maps each position of a front port once`},
		}},
		{"a mapping without its rear port position", true, valid + portsFile + "port-mappings:\n  - front_port: F1\n    front_port_position: 1\n    rear_port: R2\n" + mappingYAML("F2", 1, "R2", 1), []format.Problem{
			{Severity: format.Error, Line: 27, Column: 25, Field: "port-mappings[1].rear_port_position", Reason: `maps to position 1 of rear-ports "R2", as port-mappings[0] does, but NetBox maps each position of a rear port once`},
		}},
		{"a rear port position beyond the positions a rear port leaves out", true, valid + "rear-ports:\n  - name: R1\n    type: 8p8c\nfront-ports:\n  - name: F1\n    type: 8p8c\n    positions: 1\nport-mappings:\n" + mappingYAML("F1", 1, "R1", 2), []format.Problem{
			{Severity: format.Error, Line: 17, Column: 25, Field: "port-mappings[0].rear_port_position", Reason: `is 2, but rear-ports "R1" has only 1 positions, so NetBox's import would refuse it`},
		}},
		{"port mappings that aren't a list", true, valid + "port-mappings: {front_port: F1}\n", []format.Problem{
			{Severity: format.Error, Line: 6, Column: 16, Field: "port-mappings", Reason: "isn't a list, which devicetype-library's port-mappings is"},
		}},
		// The rear port's own error, not the mapping's too.
		{"a mapping's rear port with an error of its own", true, valid + "rear-ports:\n  - name: R1\n    type: bogus\n    positions: 1\nfront-ports:\n  - name: F1\n    type: 8p8c\n    positions: 1\nport-mappings:\n" + mappingYAML("F1", 1, "R1", 3), []format.Problem{
			{Severity: format.Error, Line: 8, Column: 11, Field: "rear-ports[0].type", Reason: `"bogus" isn't one of its choices`},
		}},
		{"a rear port's dropped key of the wrong kind", true, valid + "rear-ports:\n  - name: R1\n    type: 8p8c\n    positions: 1\n    _is_power_source: 1\n", []format.Problem{
			{Severity: format.Error, Line: 10, Column: 23, Field: "rear-ports[0]._is_power_source", Reason: "is a whole number, but devicetype-library's _is_power_source is true or false"},
		}},
		{"inventory items that aren't mappings", true, valid + "inventory-items:\n  - CPU\n  - name: Fan\n  -\n  - [a]\n", []format.Problem{
			{Severity: format.Error, Line: 7, Column: 5, Field: "inventory-items[0]", Reason: "is text, but each of devicetype-library's inventory-items is a mapping"},
			{Severity: format.Error, Line: 9, Column: 4, Field: "inventory-items[2]", Reason: "is null, but each of devicetype-library's inventory-items is a mapping"},
			{Severity: format.Error, Line: 10, Column: 5, Field: "inventory-items[3]", Reason: "is a list, but each of devicetype-library's inventory-items is a mapping"},
		}},
		{"inventory items that aren't a list", true, valid + "inventory-items: CPU\n", []format.Problem{
			{Severity: format.Error, Line: 6, Column: 18, Field: "inventory-items", Reason: "isn't a list, which devicetype-library's inventory-items is"},
		}},
		// Nothing to ignore.
		{"no inventory items", false, valid + "inventory-items: []\n", nil},
		{"inventory items", false, valid + "inventory-items:\n  - name: CPU\n", []format.Problem{
			{Severity: format.Warning, Line: 6, Column: 1, Field: "inventory-items", Reason: "holds inventory items, whose fields TrueUp neither checks nor compares: NetBox has deprecated inventory item templates in favour of modules, and TrueUp doesn't support them"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			const p = "device-types/Acme/box.yaml"
			fsys := withFiles(t, map[string]string{p: tt.file})
			if tt.lenient {
				fsys["schema/devicetype.json"] = &fstest.MapFile{Data: []byte(lenientSchema)}
			}
			res, err := Format{}.Read(t.Context(), sourcetest.Snapshot(t, "lib", fsys))
			if err != nil {
				t.Fatal(err)
			}
			for i := range tt.want {
				tt.want[i].Path = p
			}
			if diff := cmp.Diff(tt.want, res.Problems); diff != "" {
				t.Errorf("problems (-want +got):\n%s", diff)
			}
		})
	}
}

// TestReadWithAMoreLenientSchema checks what the adapter does itself, when
// the schema, which comes from the source, lets through what it shouldn't.
func TestReadWithAMoreLenientSchema(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		file string
		want []format.Problem
	}{
		{"a choice NetBox doesn't ship with, which an administrator can add", valid + "airflow: sideways\n", []format.Problem{
			{Severity: format.Warning, Line: 6, Column: 10, Field: "airflow", Reason: `"sideways" is not one of the choices NetBox ships with, though an administrator can add choices through FIELD_CHOICES["dcim.Device.airflow"]`},
		}},
		{"a choice NetBox doesn't have", valid + "subdevice_role: middle\n", []format.Problem{
			{Severity: format.Error, Line: 6, Column: 17, Field: "subdevice_role", Reason: `"middle" isn't one of its choices`},
		}},
		{"a key the adapter doesn't know", valid + "cooling_method: air\n", []format.Problem{
			{Severity: format.Warning, Line: 6, Column: 1, Field: "cooling_method", Reason: "isn't a key TrueUp reads from devicetype-library yet, so it is ignored"},
		}},
		{"a missing key NetBox needs", "manufacturer: Acme\nmodel: Box\nis_full_depth: true\n", []format.Problem{
			{Severity: format.Error, Line: 1, Column: 1, Reason: "has no slug, which NetBox needs to create a device type"},
			{Severity: format.Error, Line: 1, Column: 1, Reason: "has no u_height, which NetBox needs to create a device type"},
		}},
		{"a number for text", strings.Replace(valid, "Box 1", "12", 1), []format.Problem{
			{Severity: format.Error, Line: 2, Column: 8, Field: "model", Reason: "is a whole number, but NetBox's model is text"},
		}},
		{"a list for a manufacturer", strings.Replace(valid, "Acme", "[Acme]", 1), []format.Problem{
			{Severity: format.Error, Line: 1, Column: 15, Field: "manufacturer", Reason: "is a list, but a manufacturer is given by its name, as text"},
		}},
		{"a number for a manufacturer", strings.Replace(valid, "Acme", "3", 1), []format.Problem{
			{Severity: format.Error, Line: 1, Column: 15, Field: "manufacturer", Reason: "is a whole number, but a manufacturer is given by its name, as text"},
		}},
		{"components that aren't lists", valid + "interfaces: {a: 1}\nis_powered: 1\n", []format.Problem{
			{Severity: format.Error, Line: 6, Column: 13, Field: "interfaces", Reason: "isn't a list, which devicetype-library's interfaces is"},
			{Severity: format.Error, Line: 7, Column: 13, Field: "is_powered", Reason: "isn't true or false, which devicetype-library's is_powered is"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			const p = "device-types/Acme/box.yaml"
			fsys := withFiles(t, map[string]string{p: tt.file})
			fsys["schema/devicetype.json"] = &fstest.MapFile{Data: []byte(lenientSchema)}
			res, err := Format{}.Read(t.Context(), sourcetest.Snapshot(t, "lib", fsys))
			if err != nil {
				t.Fatal(err)
			}
			for i := range tt.want {
				tt.want[i].Path = p
			}
			if diff := cmp.Diff(tt.want, res.Problems); diff != "" {
				t.Errorf("problems (-want +got):\n%s", diff)
			}
		})
	}
}

// TestReadAcrossFiles checks what the adapter reports of files together:
// manufacturers whose slugs would be the same, one it can't make a slug
// for, device types NetBox couldn't hold together, and files where the
// library keeps no device types.
func TestReadAcrossFiles(t *testing.T) {
	t.Parallel()
	file := func(manufacturer, model, slug string) string {
		return "manufacturer: " + manufacturer + "\nmodel: " + model + "\nslug: " + slug + "\nu_height: 1\nis_full_depth: true\n"
	}
	fsys := withFiles(t, map[string]string{
		"device-types/Acme/a.yaml":        file("Acme", "A", "acme-a"),
		"device-types/Acme/b.yml":         file("ACME", "B", "acme-b"),
		"device-types/Acme/c.yaml":        file("Acme", "A", "acme-c"),
		"device-types/Acme/d.yaml":        file("Acme", "D", "acme-a"),
		"device-types/Acme/e.yaml":        file("Acme!", "E", "acme-e"),
		"device-types/Nihon/f.yaml":       file("'\xe6\x97\xa5\xe6\x9c\xac'", "F", "nihon-f"),
		"device-types/Acme/README.md":     "# Acme\n",
		"device-types/Acme/.hidden.yaml":  file("Acme", "H", "acme-h"),
		"device-types/Acme/deep/g.yaml":   file("Acme", "G", "acme-g"),
		"device-types/top.yaml":           file("Acme", "T", "acme-t"),
		"device-types/Acme/old.yaml.bak":  file("Acme", "O", "acme-o"),
		"module-types/Acme/m.yaml":        "not read: module types come later\n",
		"elevation-images/Acme/a.png":     "not read\n",
		"device-types/Acme/f.yaml.yaml.x": "x",
		"device-types/.archive/z.yaml":    file("Archive", "Z", "archive-z"),
	})
	fsys["device-types/Acme/link.yaml"] = &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("a.yaml")}
	fsys["device-types/Acme/notes.txt"] = &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("README.md")}
	res, err := Format{}.Read(t.Context(), sourcetest.Snapshot(t, "lib", fsys))
	if err != nil {
		t.Fatal(err)
	}
	notRead := "isn't read: devicetype-library keeps each device type in device-types/<manufacturer>/, in a file named .yaml or .yml"
	want := []format.Problem{
		{Severity: format.Warning, Reason: `NetBox's UI can't make a slug from the manufacturer name "日本" (can't be blank), so TrueUp can't create the manufacturer: it can only match one NetBox has`},
		{Severity: format.Warning, Reason: `manufacturers "ACME", "Acme" and "Acme!" would all get the slug "acme", which NetBox gives to one manufacturer only: create all but one of them in NetBox with another slug, or spell them alike in the source`},
		// A hidden directory, which the library's tests never read.
		{Severity: format.Warning, Path: "device-types/.archive/z.yaml", Reason: notRead},
		{Severity: format.Warning, Path: "device-types/Acme/.hidden.yaml", Reason: notRead},
		{Severity: format.Warning, Path: "device-types/Acme/README.md", Reason: notRead},
		{Severity: format.Warning, Path: "device-types/Acme/c.yaml", Field: "model", Reason: "device-types/Acme/a.yaml has the same manufacturer and model as this one, which NetBox keeps unique"},
		{Severity: format.Warning, Path: "device-types/Acme/d.yaml", Field: "slug", Reason: "device-types/Acme/a.yaml has the same manufacturer and slug as this one, which NetBox keeps unique"},
		{Severity: format.Warning, Path: "device-types/Acme/deep/g.yaml", Reason: notRead},
		{Severity: format.Warning, Path: "device-types/Acme/f.yaml.yaml.x", Reason: notRead},
		{Severity: format.Error, Path: "device-types/Acme/link.yaml", Reason: "wasn't read: it is a symbolic link, which TrueUp never follows"},
		// Left out, but not a device type, so only a warning.
		{Severity: format.Warning, Path: "device-types/Acme/notes.txt", Reason: "wasn't read: it is a symbolic link, which TrueUp never follows"},
		{Severity: format.Warning, Path: "device-types/Acme/old.yaml.bak", Reason: notRead},
		{Severity: format.Warning, Path: "device-types/top.yaml", Reason: notRead},
	}
	if diff := cmp.Diff(want, res.Problems); diff != "" {
		t.Errorf("problems (-want +got):\n%s", diff)
	}
	var names []string
	for _, d := range res.Definitions {
		if d.Object.Type == "dcim.manufacturer" {
			name, _ := d.Identity["name"].(model.String)
			names = append(names, string(name))
			if _, ok := d.Object.Fields["slug"]; !ok && d.Derived != nil {
				t.Errorf("manufacturer %v has no slug, but derived %v", d.Identity, d.Derived)
			}
		}
	}
	if diff := cmp.Diff([]string{"ACME", "Acme", "Acme!", "\xe6\x97\xa5\xe6\x9c\xac"}, names); diff != "" {
		t.Errorf("manufacturers (-want +got):\n%s", diff)
	}
	// Both duplicates are kept, for grouping to decide.
	if len(res.Definitions) != 4+6 || res.Files != 7 || res.Invalid != 1 {
		t.Errorf("%d definitions, %d files, %d invalid; want 10, 7 and 1", len(res.Definitions), res.Files, res.Invalid)
	}
}

// TestReadReportsWhatWasLeftOut checks that a device type the source holds
// never goes missing without a problem saying so: in a manufacturer's
// directory the source layer left out, in a file named in capitals, or in a
// source that holds none at all.
func TestReadReportsWhatWasLeftOut(t *testing.T) {
	t.Parallel()
	fsys := withFiles(t, map[string]string{
		"device-types/Acme/a.yaml":  valid,
		"device-types/Acme/b.YAML":  valid,
		"device-types/Acme/c.Yml":   valid,
		"device-types/Zeta/notes":   "not read",
		"device-types/README.md":    "# Device types\n",
		"device-types/Acme/d.yaml~": "a backup",
	})
	fsys["device-types/Other"] = &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("Acme")}
	res, err := Format{}.Read(t.Context(), sourcetest.Snapshot(t, "lib", fsys))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range res.Problems {
		got = append(got, string(p.Severity)+" "+p.String())
	}
	notRead := "isn't read: devicetype-library keeps each device type in device-types/<manufacturer>/, in a file named .yaml or .yml"
	want := []string{
		`error device-types/Acme/b.YAML: isn't read: it is named ".YAML", but devicetype-library's tests take only .yaml and .yml, in lower case`,
		`error device-types/Acme/c.Yml: isn't read: it is named ".Yml", but devicetype-library's tests take only .yaml and .yml, in lower case`,
		"warning device-types/Acme/d.yaml~: " + notRead,
		"error device-types/Other: wasn't read: it is a symbolic link, which TrueUp never follows; if it is a manufacturer's directory, none of the device types in it were read",
		"warning device-types/README.md: " + notRead,
		"warning device-types/Zeta/notes: " + notRead,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("problems (-want +got):\n%s", diff)
	}
	if res.Files != 4 || res.Invalid != 3 || len(res.Definitions) != 2 {
		t.Errorf("%d files, %d invalid, %d definitions; want 4, 3 and 2", res.Files, res.Invalid, len(res.Definitions))
	}

	// A source with no device types at all, such as one whose include
	// patterns match none, says so.
	res, err = Format{}.Read(t.Context(), sourcetest.Snapshot(t, "lib", librarySchemas(t)))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != 1 || res.Problems[0].Severity != format.Warning || !strings.Contains(res.Problems[0].Reason, "holds no device types") {
		t.Errorf("problems = %+v, want a warning that there are no device types", res.Problems)
	}
}

// TestReadDuplicatesInListedOrder checks that of two device types NetBox
// couldn't hold together, the one listed later gets the warning: the order
// of paths, in which - comes before /, not of the walk, directory by
// directory.
func TestReadDuplicatesInListedOrder(t *testing.T) {
	t.Parallel()
	fsys := withFiles(t, map[string]string{
		"device-types/A/x.yaml":   valid,
		"device-types/A-B/x.yaml": valid,
	})
	res, err := Format{}.Read(t.Context(), sourcetest.Snapshot(t, "lib", fsys))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Definitions) != 3 || res.Definitions[1].Path != "device-types/A-B/x.yaml" {
		t.Fatalf("definitions = %+v", res.Definitions)
	}
	var got []string
	for _, p := range res.Problems {
		got = append(got, p.String())
	}
	want := []string{
		"device-types/A/x.yaml: model: device-types/A-B/x.yaml has the same manufacturer and model as this one, which NetBox keeps unique",
		"device-types/A/x.yaml: slug: device-types/A-B/x.yaml has the same manufacturer and slug as this one, which NetBox keeps unique",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("problems (-want +got):\n%s", diff)
	}
}

// TestReadNeedsSchemas checks that a source without the library's schemas,
// or with schemas that don't compile, reads nothing.
func TestReadNeedsSchemas(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		change  func(fstest.MapFS)
		limits  config.SourceLimits
		want    string
		include []string // the source's include patterns, if any
	}{
		{"no schemas", func(fsys fstest.MapFS) {
			for p := range fsys {
				if strings.HasPrefix(p, "schema/") {
					delete(fsys, p)
				}
			}
		}, sourcetest.Limits, "the source has no schema/devicetype.json, devicetype-library's schema for device types", nil},
		{"no device type schema", func(fsys fstest.MapFS) { delete(fsys, "schema/devicetype.json") }, sourcetest.Limits, "has no schema/devicetype.json", nil},
		{"a schema that refers to one that is gone", func(fsys fstest.MapFS) { delete(fsys, "schema/components.json") }, sourcetest.Limits, "schema/devicetype.json doesn't compile", nil},
		{"a schema that isn't JSON", func(fsys fstest.MapFS) { fsys["schema/racktype.json"] = &fstest.MapFile{Data: []byte("{")} }, sourcetest.Limits, "schema/racktype.json isn't JSON", nil},
		{"a schema that refers outside", func(fsys fstest.MapFS) {
			fsys["schema/devicetype.json"] = &fstest.MapFile{Data: []byte(`{"$ref": "https://example.com/devicetype.json"}`)}
		}, sourcetest.Limits, "loads no schema from outside the source", nil},
		{"a schema too large to read", func(fstest.MapFS) {}, config.SourceLimits{MaxFileSize: 4096, MaxFiles: 100, FetchTimeout: 1, MaxFetchSize: 1}, "schema/devicetype.json, devicetype-library's schema for device types, wasn't read: it is larger than the source's max_file_size", nil},
		{"a schema it refers to too large to read", func(fstest.MapFS) {}, config.SourceLimits{MaxFileSize: 5000, MaxFiles: 100, FetchTimeout: 1, MaxFetchSize: 1}, "(schema/components.json wasn't read: it is larger than the source's max_file_size; schema/generated_schema.json wasn't read: it is larger than the source's max_file_size; schema/moduletype.json wasn't read: it is larger than the source's max_file_size)", nil},
		{"a schema it refers to that is a link", func(fsys fstest.MapFS) {
			fsys["schema/components.json"] = &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("other.json")}
		}, sourcetest.Limits, "(schema/components.json wasn't read: it is a symbolic link, which TrueUp never follows)", nil},
		{"device types that are a link", func(fsys fstest.MapFS) {
			delete(fsys, "device-types/Acme/box.yaml")
			fsys["device-types"] = &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("elsewhere")}
		}, sourcetest.Limits, "device-types, where devicetype-library keeps its device types, wasn't read: it is a symbolic link", nil},
		// Patterns that choose files inside a link, but not the link
		// itself, still report it.
		{"device types that are a link, with include patterns", func(fsys fstest.MapFS) {
			delete(fsys, "device-types/Acme/box.yaml")
			fsys["device-types"] = &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("elsewhere")}
		}, sourcetest.Limits, "device-types, where devicetype-library keeps its device types, wasn't read: it is a symbolic link", []string{"device-types/Acme/**"}},
		{"schemas that are a link", func(fsys fstest.MapFS) {
			for p := range fsys {
				if strings.HasPrefix(p, "schema/") {
					delete(fsys, p)
				}
			}
			fsys["schema"] = &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("elsewhere")}
		}, sourcetest.Limits, "schema, where devicetype-library keeps its schemas, wasn't read: it is a symbolic link", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fsys := withFiles(t, map[string]string{"device-types/Acme/box.yaml": valid})
			tt.change(fsys)
			// The patterns list gives a source.
			f := Format{}
			s := config.Source{Name: "lib", Kind: "test", Limits: tt.limits}
			if tt.include != nil {
				s.Include = slices.Concat(tt.include, f.Needs())
			}
			spec, err := source.NewSpec(s)
			if err != nil {
				t.Fatal(err)
			}
			spec.Only = sourcetest.Patterns(t, f.Reads()...)
			snap, err := source.Open(t.Context(), sourcetest.Kind{FS: fsys, Version: "v1"}, spec)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = snap.Close() })
			res, err := f.Read(t.Context(), snap)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Read = %+v, %v; want an error holding %q", res, err, tt.want)
			}
		})
	}
}

// deniedFS is a file system that refuses to open some files, so that a test
// can check that Read doesn't read them once the snapshot is taken.
type deniedFS struct {
	fs.FS
	deny map[string]bool
}

func (d deniedFS) Open(name string) (fs.File, error) {
	if d.deny[name] {
		return nil, fmt.Errorf("%s was opened after the snapshot was taken", name)
	}
	return d.FS.Open(name)
}

// TestReadRefusesBeforeReading checks that files too large to parse, or
// schemas too many or too large to compile, are refused from the
// snapshot's list, without being read: a source can list one file that Git
// stores once under many names.
func TestReadRefusesBeforeReading(t *testing.T) {
	t.Parallel()
	limits := config.SourceLimits{MaxFileSize: 2 << 20, MaxFiles: 100, FetchTimeout: 1, MaxFetchSize: 1}
	largeFile := valid + "# " + strings.Repeat("x", format.DefinitionLimits.Size) + "\n"
	manySchemas := map[string]string{"device-types/Acme/box.yaml": valid}
	for i := range format.MaxSchemaFiles {
		manySchemas[fmt.Sprintf("schema/extra-%02d.json", i)] = "{}"
	}
	tests := []struct {
		name    string
		files   map[string]string
		deny    func(string) bool
		wantErr string
		want    format.Problem
	}{
		{
			name:  "a device type too large",
			files: map[string]string{"device-types/Acme/box.yaml": valid, "device-types/Acme/large.yaml": largeFile},
			deny:  func(p string) bool { return p == "device-types/Acme/large.yaml" },
			want: format.Problem{
				Severity: format.Error, Path: "device-types/Acme/large.yaml",
				Reason: fmt.Sprintf("the file holds %d bytes, more than the 262144 a file of definitions may", len(largeFile)),
			},
		},
		{
			name:    "too many schemas",
			files:   manySchemas,
			deny:    func(p string) bool { return strings.HasPrefix(p, "schema/") },
			wantErr: "devicetype-library's schemas: the source holds 38 JSON schemas, more than the 32 TrueUp reads",
		},
		{
			name:    "a schema too large",
			files:   map[string]string{"device-types/Acme/box.yaml": valid, "schema/large.json": strings.Repeat(" ", format.MaxSchemaSize+1)},
			deny:    func(p string) bool { return strings.HasPrefix(p, "schema/") },
			wantErr: "devicetype-library's schemas: schema schema/large.json holds 1048577 bytes, more than the 1048576 TrueUp reads",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fsys := withFiles(t, tt.files)
			deny := map[string]bool{}
			snap := sourcetest.SnapshotOf(t, sourcetest.Kind{FS: deniedFS{FS: fsys, deny: deny}, Version: "v1"}, config.Source{Name: "lib", Kind: "test", Limits: limits})
			// Once the snapshot is taken, the files Read mustn't read can't
			// be read.
			for p := range fsys {
				deny[p] = tt.deny(p)
			}
			res, err := Format{}.Read(t.Context(), snap)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("Read = %+v, %v; want the error %q", res, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(res.Problems, tt.want) || res.Files != 2 || res.Invalid != 1 {
				t.Errorf("Read = %+v; want %+v among its problems, and 1 of 2 files invalid", res, tt.want)
			}
		})
	}
}

// TestReadStopsWhenCancelled checks that Read gives up, reading nothing,
// when its context ends.
func TestReadStopsWhenCancelled(t *testing.T) {
	t.Parallel()
	snap := sourcetest.Snapshot(t, "lib", os.DirFS("testdata/library"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if res, err := (Format{}).Read(ctx, snap); !errors.Is(err, context.Canceled) {
		t.Errorf("Read with a cancelled context = %+v, %v; want context.Canceled", res, err)
	}
}

// TestReadsAndNeeds checks the patterns the format gives commands, against
// the copied library.
func TestReadsAndNeeds(t *testing.T) {
	t.Parallel()
	f := Format{}
	spec, err := source.NewSpec(config.Source{Name: "lib", Kind: "test", Include: append([]string{"device-types/Huawei/**"}, f.Needs()...), Limits: sourcetest.Limits})
	if err != nil {
		t.Fatal(err)
	}
	spec.Only = sourcetest.Patterns(t, f.Reads()...)
	snap, err := source.Open(t.Context(), sourcetest.Kind{FS: os.DirFS("testdata/library"), Version: "v1"}, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Close() }()
	var paths []string
	for _, file := range snap.Files {
		paths = append(paths, file.Path)
	}
	want := []string{
		"device-types/Huawei/NE8000-X8.yaml", "schema/components.json", "schema/devicetype.json",
		"schema/generated_schema.json", "schema/moduletype.json", "schema/racktype.json", "schema/reusable.json",
	}
	if diff := cmp.Diff(want, paths); diff != "" {
		t.Errorf("files (-want +got):\n%s", diff)
	}
	res, err := f.Read(t.Context(), snap)
	if err != nil || res.Files != 1 || len(res.Definitions) != 2 {
		t.Errorf("Read = %+v, %v; want one device type and its manufacturer", res, err)
	}
	if f.Name() != config.FormatDeviceTypeLibrary {
		t.Errorf("Name() = %q", f.Name())
	}
	// The golden file's decimals are exact, never floats.
	golden, err := os.ReadFile("testdata/library.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(golden, []byte(`"weight": 19.18,`)) {
		t.Error("the golden file doesn't give HPE ClearPass-C2000's weight as 19.18")
	}
}
