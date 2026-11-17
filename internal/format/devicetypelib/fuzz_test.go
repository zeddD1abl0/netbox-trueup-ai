package devicetypelib

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source/sourcetest"
)

// FuzzReadDeviceType checks that no file makes the adapter panic, or give a
// definition that NetBox's rules would refuse, and that every problem says
// where it is and stays short.
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
	snap := sourcetest.Snapshot(f, "lib", librarySchemas(f))
	schema, err := compileSchema(f.Context(), snap)
	if err != nil {
		f.Fatal(err)
	}
	r := newReader(schema)
	f.Fuzz(func(t *testing.T, data []byte) {
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
			return
		}
		checkMappings(t, def.Object)
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
	})
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
			if _, set := child.Fields[f.Name]; !set && f.Name != col.Parent && (f.Required || f.ImportRequired) {
				t.Fatalf("a valid file gives one of %s without %s", col.Name, f.Name)
			}
		}
		// Every collection here is keyed by one field.
		key := child.Fields[col.Key[0]]
		if keys[key] {
			t.Fatalf("a valid file gives two of %s with the key %s", col.Name, key)
		}
		keys[key] = true
	}
}

// checkMappings checks that the port mappings a valid file gives are as
// NetBox's import would take them: each to a position of its rear port, and
// none to a rear port's position another maps to.
func checkMappings(t *testing.T, dt model.Object) {
	t.Helper()
	positions := map[model.Value]model.Integer{}
	rears, _ := dt.Fields["rear_ports"].(model.Children)
	for _, rp := range rears {
		n, _ := rp.Fields["positions"].(model.Integer)
		positions[rp.Fields["name"]] = max(n, 1)
	}
	type slot struct{ port, position model.Value }
	taken := map[slot]bool{}
	fronts, _ := dt.Fields["front_ports"].(model.Children)
	for _, fp := range fronts {
		mappings, _ := fp.Fields["rear_ports"].(model.Children)
		for _, m := range mappings {
			ref, _ := m.Fields["rear_port"].(model.Reference)
			position, _ := m.Fields["rear_port_position"].(model.Integer)
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
