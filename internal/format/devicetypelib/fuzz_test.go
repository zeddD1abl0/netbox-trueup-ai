package devicetypelib

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/compare"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/dcim"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source/sourcetest"
)

// FuzzReadDeviceType checks that no file makes the adapter panic, or give a
// definition that NetBox's rules would refuse, or that doesn't read back
// from its JSON exactly as it was, with the library's schemas or without
// them, and that every problem says where it is and stays short.
func FuzzReadDeviceType(f *testing.F) {
	seeds, err := filepath.Glob("testdata/library/device-types/*/*")
	if err != nil {
		f.Fatal(err)
	}
	for _, p := range seeds {
		data, err := os.ReadFile(p) //nolint:gosec // G304: the package's own testdata.
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte(valid + "airflow: front-to-rear\nweight: 1.5\nweight_unit: kg\ncomments: \"\\u202e\"\n"))
	f.Add([]byte("manufacturer: \" Acme\"\nmodel: 010\nslug: yes\nu_height: 1:30\nis_full_depth: on\n"))
	f.Add([]byte(valid + "power-ports:\n  - name: PSU1\n    type: iec-60320-c14\n    maximum_draw: 2.0\n" +
		"power-outlets:\n  - name: O1\n    type: iec-60320-c13\n    power_port: PSU1\n  - name: O2\n    type: iec-60320-c13\n    power_port: ' PSU1'\n" +
		"interfaces:\n  - name: eth0\n    type: 1000base-t\n    bridge: eth1\n  - name: 'eth0 '\n    type: 1000base-t\n" +
		"console-ports:\n  - name: con\n    type: rj-45\n    _is_power_source: 'yes'\n"))
	f.Add([]byte(valid + "port-mappings:\n  - front_port: F1\n    front_port_position: 1\n    rear_port: R1\n    rear_port_position: 2\n" +
		"  - front_port: F2\n    front_port_position: 1\n    rear_port: R1\n    rear_port_position: 2\n" +
		"  - front_port: F2\n    front_port_position: 1\n    rear_port: R2\n    rear_port_position: 1\n" +
		"front-ports:\n  - name: F1\n    type: lc\n    positions: 1\n  - name: F2\n    type: lc\n    positions: 2\n" +
		"rear-ports:\n  - name: R1\n    type: mpo\n    positions: 2\n    _is_power_source: true\n  - name: R2\n    type: lc\n    positions: 1\n" +
		"inventory-items:\n  - name: CPU\n"))
	// A mapping that leaves its positions out takes NetBox's import's
	// default, 1, which the second file's other mapping takes too.
	ports := valid + "front-ports:\n  - name: F1\n    type: lc\n    positions: 2\n" +
		"rear-ports:\n  - name: R1\n    type: mpo\n    positions: 2\n" +
		"port-mappings:\n  - front_port: F1\n    rear_port: R1\n  - front_port: F1\n    front_port_position: 2\n    rear_port: R1\n"
	f.Add([]byte(ports + "    rear_port_position: 2\n"))
	f.Add([]byte(ports + "    rear_port_position: 1\n"))
	readerOf := func(fsys fstest.MapFS) *reader {
		schema, err := compileSchema(f.Context(), sourcetest.Snapshot(f, "lib", fsys))
		if err != nil {
			f.Fatal(err)
		}
		return newReader(schema)
	}
	// The library's own schemas, and one that takes any mapping, as a
	// source's own might: the format must still refuse what NetBox's import
	// refuses, such as a mapping that takes the rear port position another
	// takes by leaving its own out.
	lenientFS := librarySchemas(f)
	lenientFS["schema/devicetype.json"] = &fstest.MapFile{Data: []byte(lenientSchema)}
	strict, lenient := readerOf(librarySchemas(f)), readerOf(lenientFS)
	f.Fuzz(func(t *testing.T, data []byte) {
		// A file the library's schemas take reads just the same with the
		// lenient one, since only the schemas tell the readers apart.
		if !checkRead(t, strict, data) {
			checkRead(t, lenient, data)
		}
	})
}

// checkRead reads a file and checks that each problem says where it is and
// stays short, and that a valid file gives a definition NetBox's rules
// would take, which reads back from its JSON as it was. It reports whether
// the file is valid.
func checkRead(t *testing.T, r *reader, data []byte) bool {
	t.Helper()
	const p = "device-types/Acme/box.yaml"
	def, problems := r.read(p, data)
	valid := true
	for _, pr := range problems {
		if pr.Path != p || pr.Reason == "" || len(pr.String()) > 1000 || pr.Severity != format.Error && pr.Severity != format.Warning {
			t.Fatalf("problem %+v", pr)
		}
		valid = valid && pr.Severity != format.Error
	}
	if !valid {
		return false
	}
	checkMappings(t, r, def.Object)
	for name, v := range def.Object.Fields {
		if col := r.deviceType.Collection(name); col != nil {
			checkChildren(t, r, def.Object, col, v)
			continue
		}
		field := r.deviceType.Field(name)
		if field == nil {
			t.Fatalf("a valid file gives %s, which isn't a field", name)
		}
		// An airflow NetBox doesn't ship with is a warning.
		if err := field.Validate(v); err != nil && name != "airflow" {
			t.Fatalf("a valid file gives %s, which NetBox refuses: %v", v, err)
		}
		if !model.Equal(field.Normalize(v), v) {
			t.Fatalf("a valid file gives %s for %s, not normalized", v, name)
		}
	}
	for _, name := range r.deviceType.Identity {
		if def.Identity[name] == nil {
			t.Fatalf("a valid file has no %s", name)
		}
	}
	checkRoundTrip(t, def)
	return true
}

// specs are the specs a definition's JSON is read back through.
var specs = dcim.Specs()

// checkRoundTrip checks that a definition reads back from its JSON exactly
// as it was, as TrueUp's database keeps it: not only Equal, but with the
// same kinds of value, children in the same order.
func checkRoundTrip(t *testing.T, def format.Definition) {
	t.Helper()
	data, err := format.EncodeDefinition(specs, def)
	if err != nil {
		t.Fatalf("a valid file's definition: %v", err)
	}
	back, err := format.UnmarshalDefinition(specs, data)
	if err != nil {
		t.Fatalf("a valid file's definition: %v", err)
	}
	if diff := cmp.Diff(def, back, cmp.AllowUnexported(model.Decimal{}, model.Date{}), cmpopts.EquateEmpty()); diff != "" {
		t.Fatalf("a valid file's definition reads back from its JSON otherwise (-read +back):\n%s", diff)
	}
	// So do its unique values, which the database keeps beside it.
	cut, err := compare.UniqueValues(specs, def)
	if err != nil {
		t.Fatalf("a valid file's definition's unique values: %v", err)
	}
	if data, err = format.EncodeDefinition(specs, cut); err != nil {
		t.Fatalf("a valid file's definition's unique values: %v", err)
	}
	if back, err = format.UnmarshalDefinition(specs, data); err != nil {
		t.Fatalf("a valid file's definition's unique values: %v", err)
	}
	if diff := cmp.Diff(cut, back, cmp.AllowUnexported(model.Decimal{}, model.Date{}), cmpopts.EquateEmpty()); diff != "" {
		t.Fatalf("a valid file's unique values read back from their JSON otherwise (-cut +back):\n%s", diff)
	}
}

// checkChildren checks that the children a valid file gives in a collection
// are as NetBox's import would create them: fields of their spec, valid and
// normalized, with every field NetBox needs, keys unique, references to
// other templates naming ones the device type has, and nested children
// checked the same way.
func checkChildren(t *testing.T, r *reader, dt model.Object, col *model.Collection, v model.Value) {
	t.Helper()
	children, ok := v.(model.Children)
	if !ok {
		t.Fatalf("a valid file gives %s as %s, not children", col.Name, v.Kind())
	}
	spec := r.specs[col.Type]
	keys := map[model.Value]bool{}
	for _, child := range children {
		if child.Type != col.Type {
			t.Fatalf("a valid file gives a %s in %s", child.Type, col.Name)
		}
		for name, v := range child.Fields {
			f := spec.Field(name)
			if nested := spec.Collection(name); f == nil && nested != nil && nested.Nested {
				checkChildren(t, r, dt, nested, v)
				continue
			}
			switch {
			case f == nil || name == col.Parent:
				t.Fatalf("a valid file gives %s's %s, which isn't a field it sets", col.Name, name)
			case f.Validate(v) != nil:
				t.Fatalf("a valid file gives %s's %s %s, which NetBox refuses: %v", col.Name, name, v, f.Validate(v))
			case !model.Equal(f.Normalize(v), v):
				t.Fatalf("a valid file gives %s's %s %s, not normalized", col.Name, name, v)
			}
			if ref, isRef := v.(model.Reference); isRef && !hasChild(dt, r.deviceType, ref) {
				t.Fatalf("a valid file's %s refers to %s, which the device type hasn't got", col.Name, ref)
			}
		}
		for _, f := range spec.Fields {
			if _, set := child.Fields[f.Name]; !set && f.Name != col.Parent && f.ImportNeeds() {
				t.Fatalf("a valid file gives one of %s without %s", col.Name, f.Name)
			}
		}
		// Every collection here is keyed by one field, which a valid file
		// always gives, or the format fills, as a mapping's position.
		key := child.Fields[col.Key[0]]
		if key == nil {
			t.Fatalf("a valid file gives one of %s without its key, %s", col.Name, col.Key[0])
		}
		if keys[key] {
			t.Fatalf("a valid file gives two of %s with the key %s", col.Name, key)
		}
		keys[key] = true
	}
}

// checkMappings checks that the port mappings a valid file gives are as
// NetBox's import would take them: each to a position of its rear port, and
// none to a rear port's position another maps to.
func checkMappings(t *testing.T, r *reader, dt model.Object) {
	t.Helper()
	// What NetBox's import gives a position the file leaves out.
	defaultPositions, ok1 := r.specs["dcim.rearporttemplate"].Field("positions").ImportedDefault().(model.Integer)
	defaultRearPosition, ok2 := r.specs["dcim.porttemplatemapping"].Field("rear_port_position").ImportedDefault().(model.Integer)
	if !ok1 || !ok2 {
		t.Fatal("the specs give no whole number for a position left out")
	}
	positions := map[model.Value]model.Integer{}
	rears, _ := dt.Fields["rear_ports"].(model.Children)
	for _, rp := range rears {
		n, set := rp.Fields["positions"].(model.Integer)
		if !set {
			n = defaultPositions
		}
		positions[rp.Fields["name"]] = n
	}
	type slot struct{ port, position model.Value }
	taken := map[slot]bool{}
	fronts, _ := dt.Fields["front_ports"].(model.Children)
	for _, fp := range fronts {
		mappings, _ := fp.Fields["rear_ports"].(model.Children)
		for _, m := range mappings {
			ref, _ := m.Fields["rear_port"].(model.Reference)
			position, set := m.Fields["rear_port_position"].(model.Integer)
			if !set {
				position = defaultRearPosition
			}
			if position > positions[ref.Key["name"]] {
				t.Fatalf("a valid file maps %s to position %d of %s, which has %d", fp.Fields["name"], position, ref, positions[ref.Key["name"]])
			}
			if s := (slot{ref.Key["name"], position}); taken[s] {
				t.Fatalf("a valid file maps two front ports to position %d of %s", position, ref)
			} else {
				taken[s] = true
			}
		}
	}
}

// hasChild reports whether obj, of spec's type, holds a child that ref
// names by its collection's key.
func hasChild(obj model.Object, spec *model.Spec, ref model.Reference) bool {
	for _, col := range spec.Children {
		if col.Type != ref.Type {
			continue
		}
		children, _ := obj.Fields[col.Name].(model.Children)
		for _, c := range children {
			if model.Equal(c.Fields["name"], ref.Key["name"]) {
				return true
			}
		}
	}
	return false
}
