package compare_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/compare"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/dcim"
)

// rearPort is a rear port template as a definition refers to it, by name.
func rearPort(name string) model.Reference {
	return model.Reference{Type: "dcim.rearporttemplate", Key: map[string]model.Value{"name": model.String(name)}}
}

// mapping is a front port's mapping as a definition gives it: its position
// on the front port to a rear port's position.
func mapping(position int64, rear string, rearPosition int64) model.Object {
	return model.Object{Type: "dcim.porttemplatemapping", Fields: map[string]model.Value{
		"position": model.Integer(position), "rear_port": rearPort(rear), "rear_port_position": model.Integer(rearPosition),
	}}
}

// nbMapping is a front port's mapping as NetBox's API gives it, with the
// rear port by ID.
func nbMapping(position int64, rear int64, rearPosition int64) model.Object {
	return model.Object{Type: "dcim.porttemplatemapping", Fields: map[string]model.Value{
		"position": model.Integer(position), "rear_port": model.Reference{Type: "dcim.rearporttemplate", ID: rear},
		"rear_port_position": model.Integer(rearPosition),
	}}
}

// lc is a port's type, LC.
var lc = map[string]model.Value{"type": model.Choice("lc")}

// frontPort is a front port template as a definition gives it, of type LC,
// with its mappings.
func frontPort(name string, positions int64, mappings ...model.Object) model.Object {
	return child("frontport", name, map[string]model.Value{
		"type": model.Choice("lc"), "positions": model.Integer(positions), "rear_ports": model.Children(mappings),
	})
}

// nbFrontPort is a front port template of device type dt as NetBox's API
// gives it, of type LC, with its mappings.
func nbFrontPort(id, dt int64, name string, positions int64, mappings ...model.Object) compare.Observed {
	return nbChild(id, dt, "frontport", name, map[string]model.Value{
		"type": model.Choice("lc"), "positions": model.Integer(positions), "rear_ports": model.Children(mappings),
	})
}

func TestPortMappings(t *testing.T) {
	t.Parallel()
	positions := func(n int64) map[string]model.Value {
		return map[string]model.Value{"type": model.Choice("lc"), "positions": model.Integer(n)}
	}
	def := withChildren(map[string]model.Children{
		"rear_ports": {child("rearport", "RP1", positions(2)), child("rearport", "RP2", positions(1)), child("rearport", "RP3", positions(1))},
		"front_ports": {
			frontPort("FP1", 2, mapping(1, "RP1", 1), mapping(2, "RP1", 2)),
			frontPort("FP2", 2, mapping(1, "RP2", 1)),
			frontPort("FP3", 1, mapping(1, "RP3", 1)),
		},
	})
	observed := append(nbR670(),
		nbChild(30, 10, "rearport", "RP1", positions(2)),
		nbChild(31, 10, "rearport", "RP2", positions(1)),
		nbChild(32, 10, "rearport", "RP3", positions(1)),
		// Only NetBox has it.
		nbChild(33, 10, "rearport", "RP4", positions(1)),
		// In step, the mappings in another order.
		nbFrontPort(40, 10, "FP1", 2, nbMapping(2, 30, 2), nbMapping(1, 30, 1)),
		// Its first mapping repointed to RP3, and a second only NetBox has.
		nbFrontPort(41, 10, "FP2", 2, nbMapping(1, 32, 1), nbMapping(2, 33, 1)),
		// FP3 is missing; FP4 only NetBox has, mappings and all.
		nbFrontPort(43, 10, "FP4", 1, nbMapping(1, 31, 1)),
	)
	r := run(t, []compare.Definition{def}, observed)[0]
	want := []string{
		`rear_ports["RP1"]: true true managed #30`,
		`rear_ports["RP1"].type: "lc" "lc" managed`,
		`rear_ports["RP1"].positions: 2 2 managed`,
		`rear_ports["RP2"]: true true managed #31`,
		`rear_ports["RP2"].type: "lc" "lc" managed`,
		`rear_ports["RP2"].positions: 1 1 managed`,
		`rear_ports["RP3"]: true true managed #32`,
		`rear_ports["RP3"].type: "lc" "lc" managed`,
		`rear_ports["RP3"].positions: 1 1 managed`,
		`rear_ports["RP4"]: false true differs #33`,
		`front_ports["FP1"]: true true managed #40`,
		`front_ports["FP1"].type: "lc" "lc" managed`,
		`front_ports["FP1"].positions: 2 2 managed`,
		`front_ports["FP1"].rear_ports[1]: true true managed`,
		`front_ports["FP1"].rear_ports[1].rear_port: dcim.rearporttemplate name="RP1" dcim.rearporttemplate #30 name="RP1" managed`,
		`front_ports["FP1"].rear_ports[1].rear_port_position: 1 1 managed`,
		`front_ports["FP1"].rear_ports[2]: true true managed`,
		`front_ports["FP1"].rear_ports[2].rear_port: dcim.rearporttemplate name="RP1" dcim.rearporttemplate #30 name="RP1" managed`,
		`front_ports["FP1"].rear_ports[2].rear_port_position: 2 2 managed`,
		`front_ports["FP2"]: true true managed #41`,
		`front_ports["FP2"].type: "lc" "lc" managed`,
		`front_ports["FP2"].positions: 2 2 managed`,
		`front_ports["FP2"].rear_ports[1]: true true managed`,
		`front_ports["FP2"].rear_ports[1].rear_port: dcim.rearporttemplate name="RP2" dcim.rearporttemplate #32 name="RP3" differs managed`,
		`front_ports["FP2"].rear_ports[1].rear_port_position: 1 1 managed`,
		`front_ports["FP2"].rear_ports[2]: false true differs`,
		// A front port NetBox lacks is one difference: NetBox's cells of its
		// mappings are unset, not false.
		`front_ports["FP3"]: true false differs managed`,
		`front_ports["FP3"].type: "lc" unset managed`,
		`front_ports["FP3"].positions: 1 unset managed`,
		`front_ports["FP3"].rear_ports[1]: true unset managed`,
		`front_ports["FP3"].rear_ports[1].rear_port: dcim.rearporttemplate name="RP3" unset managed`,
		`front_ports["FP3"].rear_ports[1].rear_port_position: 1 unset managed`,
		`front_ports["FP4"]: false true differs #43`,
	}
	if diff := cmp.Diff(want, childRows(r)); diff != "" {
		t.Errorf("rows (-want +got):\n%s", diff)
	}
	// FP2's first mapping, and FP3; not RP4, FP2's second mapping or FP4,
	// which only NetBox has.
	if r.Outcome != compare.Candidate || r.Differences != 2 {
		t.Errorf("%s with %d differences, want a candidate with 2", r.Outcome, r.Differences)
	}
}

func TestPortMappingsInStep(t *testing.T) {
	t.Parallel()
	def := withChildren(map[string]model.Children{
		"rear_ports":  {child("rearport", "RP1", map[string]model.Value{"type": model.Choice("mpo"), "positions": model.Integer(2)})},
		"front_ports": {frontPort("FP1", 1, mapping(1, "RP1", 2)), frontPort("FP2", 1)},
	})
	observed := append(nbR670(),
		nbChild(30, 10, "rearport", "RP1", map[string]model.Value{"type": model.Choice("mpo"), "positions": model.Integer(2)}),
		nbFrontPort(40, 10, "FP1", 1, nbMapping(1, 30, 2)),
		// A mapping the definition leaves out isn't managed.
		nbFrontPort(41, 10, "FP2", 1, nbMapping(1, 30, 1)),
	)
	r := run(t, []compare.Definition{def}, observed)[0]
	if !r.InStep() {
		t.Errorf("%s with %d differences, want in step:\n%s", r.Outcome, r.Differences, strings.Join(rows(r), "\n"))
	}
}

// TestPortMappingToAnotherDeviceType checks that a mapping to a rear port
// of another device type, which NetBox's API takes though its model
// forbids it, keeps only its ID, and so differs.
func TestPortMappingToAnotherDeviceType(t *testing.T) {
	t.Parallel()
	def := withChildren(map[string]model.Children{
		"rear_ports":  {child("rearport", "RP1", lc)},
		"front_ports": {frontPort("FP1", 1, mapping(1, "RP1", 1))},
	})
	observed := append(nbR670(),
		nbDeviceType(11, 1, "Dell", "R770", "r770", nil),
		nbChild(30, 10, "rearport", "RP1", lc),
		nbChild(50, 11, "rearport", "RP1", lc),
		nbFrontPort(40, 10, "FP1", 1, nbMapping(1, 50, 1)),
	)
	r := run(t, []compare.Definition{def}, observed)[0]
	want := `front_ports["FP1"].rear_ports[1].rear_port: dcim.rearporttemplate name="RP1" dcim.rearporttemplate #50 differs managed`
	if got := childRows(r); !strings.Contains(strings.Join(got, "\n"), want) || r.Differences != 1 {
		t.Errorf("%d differences in rows\n%s\nwant 1, in %s", r.Differences, strings.Join(got, "\n"), want)
	}
}

// TestPortMappingsNetBoxHasTwice checks that two mappings of one NetBox
// front port at one position, which NetBox's constraint forbids, make the
// device type a conflict, without NetBox IDs, and that neither is compared;
// and that a front port NetBox has twice leaves its mappings uncompared.
func TestPortMappingsNetBoxHasTwice(t *testing.T) {
	t.Parallel()
	def := withChildren(map[string]model.Children{
		"rear_ports":  {child("rearport", "RP1", lc)},
		"front_ports": {frontPort("FP1", 1, mapping(1, "RP1", 1)), frontPort("FP2", 1, mapping(1, "RP1", 1))},
	})
	observed := append(nbR670(),
		nbChild(30, 10, "rearport", "RP1", lc),
		nbFrontPort(40, 10, "FP1", 1, nbMapping(1, 30, 1), nbMapping(1, 30, 2)),
		nbFrontPort(42, 10, "FP2", 1, nbMapping(1, 30, 1)),
		nbFrontPort(41, 10, "FP2", 1, nbMapping(1, 30, 1)),
		// Its mappings aren't compared, so they make no collision.
		nbFrontPort(43, 10, "FP9", 1, nbMapping(1, 30, 1), nbMapping(1, 30, 1)),
	)
	r := run(t, []compare.Definition{def}, observed)[0]
	if r.Outcome != compare.Conflict || r.NetBoxID != 10 {
		t.Errorf("%s matching #%d, want a conflict matching #10", r.Outcome, r.NetBoxID)
	}
	var got []string
	for _, c := range r.Collisions {
		got = append(got, c.Path.String()+" "+strings.Join(c.Rule, ",")+":"+itoa(c.ID))
	}
	want := []string{
		`front_ports["FP2"] device_type,name:41`,
		`front_ports["FP2"] device_type,name:42`,
		`front_ports["FP1"].rear_ports[1] position:0`,
		`front_ports["FP1"].rear_ports[1] position:0`,
	}
	if !cmp.Equal(got, want) {
		t.Errorf("collisions %v, want %v", got, want)
	}
	wantRows := []string{
		`rear_ports["RP1"]: true true managed #30`,
		`rear_ports["RP1"].type: "lc" "lc" managed`,
		`front_ports["FP1"]: true true managed #40`,
		`front_ports["FP1"].type: "lc" "lc" managed`,
		`front_ports["FP1"].positions: 1 1 managed`,
		`front_ports["FP1"].rear_ports[1]: true true managed`,
		`front_ports["FP1"].rear_ports[1].rear_port: dcim.rearporttemplate name="RP1" unset managed`,
		`front_ports["FP1"].rear_ports[1].rear_port_position: 1 unset managed`,
		`front_ports["FP2"]: true true managed`,
		`front_ports["FP2"].type: "lc" unset managed`,
		`front_ports["FP2"].positions: 1 unset managed`,
		`front_ports["FP2"].rear_ports[1]: true unset managed`,
		`front_ports["FP2"].rear_ports[1].rear_port: dcim.rearporttemplate name="RP1" unset managed`,
		`front_ports["FP2"].rear_ports[1].rear_port_position: 1 unset managed`,
		`front_ports["FP9"]: false true differs #43`,
	}
	if diff := cmp.Diff(wantRows, childRows(r)); diff != "" {
		t.Errorf("rows (-want +got):\n%s", diff)
	}
	// A nested child's collision has no NetBox ID.
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `{"rule":["position"],"netbox_id":null,"identity":{"position":1},"path":["front_ports",{"name":"FP1"},"rear_ports",{"position":1}]}`) {
		t.Errorf("JSON %s", b)
	}
}

func TestPortMappingsAsJSON(t *testing.T) {
	t.Parallel()
	def := withChildren(map[string]model.Children{
		"rear_ports":  {child("rearport", "RP1", lc)},
		"front_ports": {frontPort("FP1", 1, mapping(1, "RP1", 1))},
	})
	observed := append(nbR670(), nbChild(30, 10, "rearport", "RP1", lc), nbFrontPort(40, 10, "FP1", 1, nbMapping(1, 30, 1)))
	b, err := json.Marshal(run(t, []compare.Definition{def}, observed)[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`{"path":["front_ports",{"name":"FP1"},"rear_ports",{"position":1}],"cells":[{"value":true},{"value":true}],"differs":false,"managed":true,"derived":false,"netbox_id":null}`,
		`{"path":["front_ports",{"name":"FP1"},"rear_ports",{"position":1},"rear_port"],"cells":[{"value":{"name":"RP1"}},{"value":{"id":30,"name":"RP1"}}],"differs":false,"managed":true,"derived":false,"netbox_id":null}`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON %s\nwant it to hold %s", b, want)
		}
	}
}

// TestPortMappingsRefused checks what Compare refuses in nested children,
// rather than guess.
func TestPortMappingsRefused(t *testing.T) {
	t.Parallel()
	rp := child("rearport", "RP1", lc)
	withMappings := func(mappings ...model.Object) map[string]model.Children {
		return map[string]model.Children{"rear_ports": {rp}, "front_ports": {frontPort("FP1", 2, mappings...)}}
	}
	nested := func(v model.Value) map[string]model.Children {
		fp := frontPort("FP1", 1)
		fp.Fields["rear_ports"] = v
		return map[string]model.Children{"rear_ports": {rp}, "front_ports": {fp}}
	}
	other := mapping(1, "RP1", 1)
	other.Fields["front_port"] = model.String("FP1")
	tests := []struct {
		name     string
		children map[string]model.Children
		observed []compare.Observed
		want     string
	}{
		{
			"two mappings at one position", withMappings(mapping(1, "RP1", 1), mapping(1, "RP1", 2)), nil,
			"front_ports[0].rear_ports[1] has the key of front_ports[0].rear_ports[0], which NetBox keeps unique among a dcim.frontporttemplate's rear_ports",
		},
		{
			"a mapping to a rear port the definition hasn't got", withMappings(mapping(1, "RP9", 1)), nil,
			`front_ports[0].rear_ports[0] rear_port refers to dcim.rearporttemplate name="RP9", which the definition hasn't got`,
		},
		{
			"a mapping to a rear port by a name NetBox would store otherwise", withMappings(mapping(1, "RP1 ", 1)), nil,
			`front_ports[0].rear_ports[0] rear_port refers to dcim.rearporttemplate name="RP1 ", by a key NetBox would store otherwise`,
		},
		{"a mapping without its position", withMappings(model.Object{Type: "dcim.porttemplatemapping", Fields: map[string]model.Value{"rear_port": rearPort("RP1")}}), nil, "front_ports[0].rear_ports[0] hasn't got its key, position"},
		{"a mapping of another type", withMappings(child("interface", "x", nil)), nil, `front_ports[0].rear_ports[0] is a "dcim.interfacetemplate", not a dcim.porttemplatemapping`},
		{"a mapping with a field its spec hasn't got", withMappings(other), nil, `front_ports[0].rear_ports[0] sets "front_port", which the spec of dcim.porttemplatemapping doesn't describe`},
		{"mappings that aren't children", nested(model.String("RP1")), nil, `sets front_ports[0].rear_ports to "RP1", not children`},
		{"a NetBox front port without its mappings", nil, []compare.Observed{func() compare.Observed {
			o := nbFrontPort(40, 10, "FP1", 1)
			delete(o.Object.Fields, "rear_ports")
			return o
		}()}, "NetBox's dcim.frontporttemplate #40 has no rear_ports"},
		{"a NetBox front port whose mappings aren't children", nil, []compare.Observed{func() compare.Observed {
			o := nbFrontPort(40, 10, "FP1", 1)
			o.Object.Fields["rear_ports"] = model.Null{}
			return o
		}()}, "NetBox's dcim.frontporttemplate #40 sets rear_ports to null, not children"},
		{"a NetBox mapping of another type", nil, []compare.Observed{nbFrontPort(40, 10, "FP1", 1, model.Object{Type: "dcim.interfacetemplate"})}, `NetBox's dcim.frontporttemplate #40 rear_ports[0] is a "dcim.interfacetemplate", not a dcim.porttemplatemapping`},
		{"a NetBox mapping without its rear port", nil, []compare.Observed{func() compare.Observed {
			m := nbMapping(1, 30, 1)
			delete(m.Fields, "rear_port")
			return nbFrontPort(40, 10, "FP1", 1, m)
		}()}, "NetBox's dcim.frontporttemplate #40 rear_ports[0] has no rear_port"},
		{"a NetBox mapping to no rear port", nil, []compare.Observed{nbFrontPort(40, 10, "FP1", 1, nbMapping(1, 0, 1))}, "rear_ports[0] rear_port refers to no NetBox object"},
		{"a NetBox mapping's position of the wrong kind", nil, []compare.Observed{func() compare.Observed {
			m := nbMapping(1, 30, 1)
			m.Fields["position"] = model.String("1")
			return nbFrontPort(40, 10, "FP1", 1, m)
		}()}, "rear_ports[0] position is a string, not a integer"},
		{"a NetBox mapping with a field its spec hasn't got", nil, []compare.Observed{func() compare.Observed {
			m := nbMapping(1, 30, 1)
			m.Fields["cable"] = model.Integer(1)
			return nbFrontPort(40, 10, "FP1", 1, m)
		}()}, `rear_ports[0] sets "cable", which the spec of dcim.porttemplatemapping doesn't describe`},
		{"a mapping listed on its own", nil, []compare.Observed{{ID: 60, Object: nbMapping(1, 30, 1)}}, "NetBox's dcim.porttemplatemapping #60 belongs to none of the objects that hold its type, and listed on its own, it isn't one that a parent carries nested"},
	}
	for _, tt := range tests {
		def := withChildren(tt.children)
		_, err := compare.Compare(dcim.Specs(), []compare.Definition{def}, append(nbR670(), tt.observed...))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Compare = %v, want an error holding %q", tt.name, err, tt.want)
		}
	}
}

// TestNestedCollectionsBelowTheRoot checks that a definition's child may
// hold only nested collections, whose children NetBox gives with it: TrueUp
// reads no others below the object the comparison starts from.
func TestNestedCollectionsBelowTheRoot(t *testing.T) {
	t.Parallel()
	specs := dcim.Specs()
	for i, s := range specs {
		if s.Type == "dcim.frontporttemplate" {
			fp := *s
			fp.Children = append([]model.Collection{}, s.Children...)
			fp.Children[0].Nested = false
			specs[i] = &fp
		}
	}
	def := withChildren(map[string]model.Children{
		"rear_ports":  {child("rearport", "RP1", lc)},
		"front_ports": {frontPort("FP1", 1, mapping(1, "RP1", 1))},
	})
	_, err := compare.Compare(specs, []compare.Definition{def}, nbR670())
	want := "has front_ports[0].rear_ports, a collection of a dcim.frontporttemplate's with an endpoint of its own, which TrueUp doesn't compare"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Compare = %v, want an error holding %q", err, want)
	}
}

// TestCollectionsBelowTheRootOfItsOwnType checks that a child of the same
// type as the object compared may hold only nested collections too: a tree,
// such as locations, whose children are of their parent's type.
func TestCollectionsBelowTheRootOfItsOwnType(t *testing.T) {
	t.Parallel()
	specs := dcim.Specs()
	for i, s := range specs {
		if s.Type == "dcim.devicetype" {
			dt := *s
			dt.Children = append(slices.Clone(s.Children), model.Collection{
				Name: "subtypes", Type: "dcim.devicetype", Parent: "manufacturer", Key: []string{"model"},
			})
			specs[i] = &dt
		}
	}
	def := withChildren(map[string]model.Children{
		"subtypes": {{Type: "dcim.devicetype", Fields: map[string]model.Value{"subtypes": model.Children{}}}},
	})
	_, err := compare.Compare(specs, []compare.Definition{def}, []compare.Observed{nbManufacturer(1, "Dell", "dell")})
	want := "has subtypes[0].subtypes, a collection of a dcim.devicetype's with an endpoint of its own, which TrueUp doesn't compare"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Compare = %v, want an error holding %q", err, want)
	}
}
