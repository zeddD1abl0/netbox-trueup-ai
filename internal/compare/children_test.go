package compare_test

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/compare"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/dcim"
)

// child is a component template as a definition gives it, of a type such
// as interface, with its name and fields.
func child(typ, name string, fields map[string]model.Value) model.Object {
	o := model.Object{Type: "dcim." + typ + "template", Fields: map[string]model.Value{"name": model.String(name)}}
	maps.Copy(o.Fields, fields)
	return o
}

// nbChild is a component template of device type dt as NetBox's API gives
// it: each field the spec's default, as DecodeAPI reads it, unless fields
// replaces it.
func nbChild(id, dt int64, typ, name string, fields map[string]model.Value) compare.Observed {
	s := model.SpecOf(dcim.Specs(), "dcim."+typ+"template")
	o := compare.Observed{ID: id, Object: model.Object{Type: s.Type, Fields: map[string]model.Value{}}}
	for _, f := range s.Fields {
		if f.Default != nil {
			o.Object.Fields[f.Name] = f.Default
		}
	}
	o.Object.Fields["device_type"] = model.Reference{Type: "dcim.devicetype", ID: dt}
	o.Object.Fields["name"] = model.String(name)
	maps.Copy(o.Object.Fields, fields)
	return o
}

func powerPort(name string) model.Reference {
	return model.Reference{Type: "dcim.powerporttemplate", Key: map[string]model.Value{"name": model.String(name)}}
}

// withChildren is a device type definition of Dell's R670, with children.
func withChildren(children map[string]model.Children) compare.Definition {
	fields := map[string]model.Value{}
	for name, c := range children {
		fields[name] = c
	}
	d := deviceType("Dell", "R670", "r670", fields)
	d.Unmapped = nil
	return d
}

// nbR670 is NetBox's Dell and its R670, as device type #10.
func nbR670() []compare.Observed {
	return []compare.Observed{nbManufacturer(1, "Dell", "dell"), nbDeviceType(10, 1, "Dell", "R670", "r670", nil)}
}

// rows gives a result's rows as path: cells, with each row's flags.
func rows(r compare.Result) []string {
	var out []string
	for _, row := range r.Rows {
		var b strings.Builder
		b.WriteString(row.Path.String() + ":")
		for _, c := range row.Cells {
			if c == nil {
				b.WriteString(" unset")
			} else {
				b.WriteString(" " + c.String())
			}
		}
		if row.Differs {
			b.WriteString(" differs")
		}
		if row.Managed {
			b.WriteString(" managed")
		}
		if row.NetBoxID != 0 {
			b.WriteString(" #" + itoa(row.NetBoxID))
		}
		out = append(out, b.String())
	}
	return out
}

// childRows are the rows of a result whose paths go into a collection.
func childRows(r compare.Result) []string {
	return slices.DeleteFunc(rows(r), func(s string) bool { return !strings.Contains(s, "[") })
}

func TestChildren(t *testing.T) {
	t.Parallel()
	c := func(v string) model.Value { return model.Choice(v) }
	def := withChildren(map[string]model.Children{
		"interfaces": {
			child("interface", "eth0", map[string]model.Value{"type": c("1000base-t"), "mgmt_only": model.Boolean(true)}),
			child("interface", "eth1", map[string]model.Value{"type": c("1000base-t")}),
		},
		"power_ports": {child("powerport", "PSU1", map[string]model.Value{"type": c("iec-60320-c14"), "maximum_draw": model.Integer(500)})},
		"power_outlets": {
			child("poweroutlet", "O1", map[string]model.Value{"type": c("iec-60320-c13"), "power_port": powerPort("PSU1")}),
			child("poweroutlet", "O2", map[string]model.Value{"type": c("iec-60320-c13"), "power_port": powerPort("PSU1")}),
		},
	})
	observed := append(nbR670(),
		// mgmt_only differs; enabled isn't what creating one gives, though
		// the definition doesn't manage it.
		nbChild(20, 10, "interface", "eth0", map[string]model.Value{"type": c("1000base-t"), "enabled": model.Boolean(false)}),
		// Only NetBox has it; a description at the default isn't shown.
		nbChild(21, 10, "interface", "eth2", map[string]model.Value{"type": c("1000base-t")}),
		nbChild(30, 10, "powerport", "PSU1", map[string]model.Value{"type": c("iec-60320-c14"), "maximum_draw": model.Integer(500)}),
		nbChild(31, 10, "powerport", "PSU2", map[string]model.Value{"type": c("iec-60320-c14")}),
		// The same power port, by ID; and another.
		nbChild(40, 10, "poweroutlet", "O1", map[string]model.Value{"type": c("iec-60320-c13"), "power_port": model.Reference{Type: "dcim.powerporttemplate", ID: 30}}),
		nbChild(41, 10, "poweroutlet", "O2", map[string]model.Value{"type": c("iec-60320-c13"), "power_port": model.Reference{Type: "dcim.powerporttemplate", ID: 31}}),
	)
	r := run(t, []compare.Definition{def}, observed)[0]
	want := []string{
		`power_ports["PSU1"]: true true managed #30`,
		`power_ports["PSU1"].type: "iec-60320-c14" "iec-60320-c14" managed`,
		`power_ports["PSU1"].maximum_draw: 500 500 managed`,
		`power_ports["PSU2"]: false true differs #31`,
		`power_outlets["O1"]: true true managed #40`,
		`power_outlets["O1"].type: "iec-60320-c13" "iec-60320-c13" managed`,
		`power_outlets["O1"].power_port: dcim.powerporttemplate name="PSU1" dcim.powerporttemplate #30 name="PSU1" managed`,
		`power_outlets["O2"]: true true managed #41`,
		`power_outlets["O2"].type: "iec-60320-c13" "iec-60320-c13" managed`,
		`power_outlets["O2"].power_port: dcim.powerporttemplate name="PSU1" dcim.powerporttemplate #31 name="PSU2" differs managed`,
		`interfaces["eth0"]: true true managed #20`,
		`interfaces["eth0"].type: "1000base-t" "1000base-t" managed`,
		`interfaces["eth0"].enabled: unset false`,
		`interfaces["eth0"].mgmt_only: true false differs managed`,
		`interfaces["eth1"]: true false differs managed`,
		`interfaces["eth1"].type: "1000base-t" unset managed`,
		`interfaces["eth2"]: false true differs #21`,
	}
	if diff := cmp.Diff(want, childRows(r)); diff != "" {
		t.Errorf("rows (-want +got):\n%s", diff)
	}
	// O2's power port, eth0's mgmt_only, and eth1, which NetBox lacks;
	// not PSU2 or eth2, which only NetBox has.
	if r.Outcome != compare.Candidate || r.Differences != 3 || r.InStep() {
		t.Errorf("%s with %d differences, want a candidate with 3", r.Outcome, r.Differences)
	}
}

func TestChildrenOfAnAbsentDeviceType(t *testing.T) {
	t.Parallel()
	def := withChildren(map[string]model.Children{
		"device_bays": {child("devicebay", "Bay 1", map[string]model.Value{"label": model.String("1")})},
	})
	r := run(t, []compare.Definition{def}, []compare.Observed{nbManufacturer(1, "Dell", "dell")})[0]
	want := []string{`device_bays["Bay 1"]: true managed`, `device_bays["Bay 1"].label: "1" managed`}
	if diff := cmp.Diff(want, childRows(r)); diff != "" {
		t.Errorf("rows (-want +got):\n%s", diff)
	}
	if r.Outcome != compare.Absent || r.Differences != 0 {
		t.Errorf("%s with %d differences, want absent with none", r.Outcome, r.Differences)
	}
}

func TestChildrenInStep(t *testing.T) {
	t.Parallel()
	c := func(v string) model.Value { return model.Choice(v) }
	def := withChildren(map[string]model.Children{
		"console_ports": {child("consoleport", "con", map[string]model.Value{"type": c("rj-45")})},
		"module_bays":   {child("modulebay", "Slot 1", map[string]model.Value{"position": model.String("1")})},
	})
	observed := append(nbR670(),
		nbChild(20, 10, "consoleport", "con", map[string]model.Value{"type": c("rj-45")}),
		nbChild(21, 10, "modulebay", "Slot 1", map[string]model.Value{"position": model.String("1")}),
		// NetBox has more, which TrueUp leaves alone.
		nbChild(22, 10, "modulebay", "Slot 2", nil),
	)
	r := run(t, []compare.Definition{def}, observed)[0]
	if !r.InStep() {
		t.Errorf("%s with %d differences, want in step:\n%s", r.Outcome, r.Differences, strings.Join(rows(r), "\n"))
	}
}

func TestChildrenNetBoxHasTwiceAreAConflict(t *testing.T) {
	t.Parallel()
	def := withChildren(map[string]model.Children{
		"interfaces": {child("interface", "eth0", map[string]model.Value{"type": model.Choice("1000base-t")})},
	})
	observed := append(nbR670(),
		nbChild(21, 10, "interface", "eth0", map[string]model.Value{"type": model.Choice("1000base-t")}),
		nbChild(20, 10, "interface", "eth0", map[string]model.Value{"type": model.Choice("10gbase-t")}),
	)
	r := run(t, []compare.Definition{def}, observed)[0]
	if r.Outcome != compare.Conflict || r.NetBoxID != 10 {
		t.Errorf("%s matching #%d, want a conflict matching #10", r.Outcome, r.NetBoxID)
	}
	var got []string
	for _, c := range r.Collisions {
		got = append(got, c.Path.String()+" "+strings.Join(c.Rule, ",")+":"+itoa(c.ID))
	}
	if want := []string{`interfaces["eth0"] device_type,name:20`, `interfaces["eth0"] device_type,name:21`}; !cmp.Equal(got, want) {
		t.Errorf("collisions %v, want %v", got, want)
	}
	// NetBox can't say which is the definition's, so neither is compared.
	if want := []string{`interfaces["eth0"]: true true managed`, `interfaces["eth0"].type: "1000base-t" unset managed`}; !cmp.Equal(childRows(r), want) {
		t.Errorf("rows %v, want %v", childRows(r), want)
	}
}

func TestChildrenAsJSON(t *testing.T) {
	t.Parallel()
	def := withChildren(map[string]model.Children{
		"interfaces": {child("interface", "eth0", map[string]model.Value{"type": model.Choice("1000base-t")})},
	})
	observed := append(nbR670(),
		nbChild(20, 10, "interface", "eth0", map[string]model.Value{"type": model.Choice("10gbase-t")}),
		nbChild(21, 10, "interface", "eth0", map[string]model.Value{"type": model.Choice("10gbase-t")}),
	)
	r := run(t, []compare.Definition{def}, observed[:3])[0]
	var got struct {
		Rows []json.RawMessage `json:"rows"`
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`{"path":["interfaces",{"name":"eth0"}],"cells":[{"value":true},{"value":true}],"differs":false,"managed":true,"derived":false,"netbox_id":20}`,
		`{"path":["interfaces",{"name":"eth0"},"type"],"cells":[{"value":"1000base-t"},{"value":"10gbase-t"}],"differs":true,"managed":true,"derived":false,"netbox_id":null}`,
	}
	if n := len(got.Rows); n < 2 || !cmp.Equal([]string{string(got.Rows[n-2]), string(got.Rows[n-1])}, want) {
		t.Errorf("rows %s, want them to end with %s", got.Rows, want)
	}
	// A child's collision says where it is.
	r = run(t, []compare.Definition{def}, observed)[0]
	b, err = json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"collisions":[{"rule":["device_type","name"],"netbox_id":20,"identity":{"name":"eth0"},"path":["interfaces",{"name":"eth0"}]}`) {
		t.Errorf("JSON %s", b)
	}
}

// TestChildrenRefused checks what Compare refuses, rather than guess.
func TestChildrenRefused(t *testing.T) {
	t.Parallel()
	iface := func(name string) model.Object {
		return child("interface", name, map[string]model.Value{"type": model.Choice("1000base-t")})
	}
	tests := []struct {
		name     string
		children map[string]model.Children
		observed []compare.Observed
		want     string
	}{
		{"a child of another type", map[string]model.Children{"interfaces": {child("devicebay", "x", nil)}}, nil, `interfaces[0] is a "dcim.devicebaytemplate", not a dcim.interfacetemplate`},
		{"a child that sets its parent", map[string]model.Children{"interfaces": {child("interface", "eth0", map[string]model.Value{
			"type": model.Choice("1000base-t"), "device_type": model.Reference{Type: "dcim.devicetype", ID: 10},
		})}}, nil, "interfaces[0] sets device_type, which refers to its parent"},
		{"a child without its key", map[string]model.Children{"interfaces": {{Type: "dcim.interfacetemplate", Fields: map[string]model.Value{"type": model.Choice("1000base-t")}}}}, nil, "interfaces[0] hasn't got its key, name"},
		{"two children with one key, once trimmed", map[string]model.Children{"interfaces": {iface("eth0"), iface("eth0 ")}}, nil, "interfaces[1] has the key of interfaces[0]"},
		{"a reference to a child the definition hasn't got", map[string]model.Children{"power_outlets": {child("poweroutlet", "O1", map[string]model.Value{"power_port": powerPort("PSU1")})}}, nil, `power_outlets[0] power_port refers to dcim.powerporttemplate name="PSU1", which the definition hasn't got`},
		// NetBox's import looks for the name as given, and stores names
		// trimmed, so this names nothing NetBox could hold.
		{"a reference by a key NetBox would store otherwise", map[string]model.Children{
			"power_ports":   {child("powerport", "PSU1", nil)},
			"power_outlets": {child("poweroutlet", "O1", map[string]model.Value{"power_port": powerPort(" PSU1")})},
		}, nil, `power_outlets[0] power_port refers to dcim.powerporttemplate name=" PSU1", by a key NetBox would store otherwise`},
		{"a child field of the wrong kind", map[string]model.Children{"interfaces": {child("interface", "eth0", map[string]model.Value{"type": model.String("1000base-t")})}}, nil, "interfaces[0] type is a string, not a choice"},
		{"a NetBox child of a device type not listed", nil, []compare.Observed{nbChild(20, 99, "interface", "eth0", map[string]model.Value{"type": model.Choice("1000base-t")})}, "NetBox listed children of dcim.devicetype #99, but not the dcim.devicetype itself"},
		{"a NetBox child of no device type", nil, []compare.Observed{nbChild(20, 10, "interface", "eth0", map[string]model.Value{"type": model.Choice("1000base-t"), "device_type": model.Null{}})}, "NetBox's dcim.interfacetemplate #20 belongs to none of the objects that hold its type"},
	}
	for _, tt := range tests {
		def := withChildren(tt.children)
		_, err := compare.Compare(dcim.Specs(), []compare.Definition{def}, append(nbR670(), tt.observed...))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Compare = %v, want an error holding %q", tt.name, err, tt.want)
		}
	}
}

// TestChildrenOfATypeWithoutASpec checks that a collection whose children
// have no spec fails, rather than panics, whether the definition has
// children there or not.
func TestChildrenOfATypeWithoutASpec(t *testing.T) {
	t.Parallel()
	all := dcim.Specs()
	specs := []*model.Spec{model.SpecOf(all, "dcim.manufacturer"), model.SpecOf(all, "dcim.devicetype")}
	for _, def := range []compare.Definition{
		deviceType("Dell", "R670", "r670", nil),
		withChildren(map[string]model.Children{"device_bays": {child("devicebay", "Bay 1", nil)}}),
	} {
		_, err := compare.Compare(specs, []compare.Definition{def}, nbR670())
		if err == nil || !strings.Contains(err.Error(), "has no spec") {
			t.Errorf("Compare = %v, want an error saying a collection's type has no spec", err)
		}
	}
}

func TestMatched(t *testing.T) {
	t.Parallel()
	defs := []compare.Definition{
		manufacturer("Dell", "dell"),
		deviceType("Dell", "R670", "r670", nil),
		// A conflict on its slug, but its identity matches #11.
		deviceType("Dell", "R770", "r670", nil),
		deviceType("Dell", "R870", "r870", nil),
	}
	observed := append(nbR670(), nbDeviceType(11, 1, "Dell", "R770", "r770", nil))
	got, err := compare.Matched(dcim.Specs(), defs, observed, "dcim.devicetype")
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{10, 11}; !cmp.Equal(got, want) {
		t.Errorf("Matched = %v, want %v", got, want)
	}
	// What Compare matches.
	for i, r := range run(t, defs, observed)[1:] {
		if (r.NetBoxID != 0) != (i < 2) {
			t.Errorf("definition %d matches #%d", i+1, r.NetBoxID)
		}
	}
}
