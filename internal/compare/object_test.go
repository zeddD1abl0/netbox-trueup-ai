package compare_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/compare"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/dcim"
)

// t1g is an interface's type, 1000BASE-T.
var t1g = map[string]model.Value{"type": model.Choice("1000base-t")}

// other returns d as another source's definition.
func other(d compare.Definition) compare.Definition {
	d.Source = "other"
	return d
}

// objectsOf compares the definitions with NetBox, and groups the results
// into objects.
func objectsOf(t *testing.T, defs []compare.Definition, observed []compare.Observed) []compare.Object {
	t.Helper()
	objects, err := compare.Objects(dcim.Specs(), run(t, defs, observed))
	if err != nil {
		t.Fatal(err)
	}
	return objects
}

// objectRows gives an object's rows as rows gives a result's.
func objectRows(o compare.Object) []string {
	return rows(compare.Result{Rows: o.Rows})
}

// columnsOf names an object's columns: each definition's source, outcome
// and differences, then NetBox's object's ID.
func columnsOf(o compare.Object) []string {
	var out []string
	for i, c := range o.Columns {
		if c.Kind == compare.NetBoxColumn {
			out = append(out, "#"+itoa(c.ID))
			continue
		}
		d := o.Definitions[i]
		out = append(out, c.Source+" "+string(d.Outcome)+" "+itoa(int64(d.Differences)))
	}
	return out
}

func TestGroup(t *testing.T) {
	t.Parallel()
	defs := []compare.Definition{
		manufacturer("Dell", "dell"),
		deviceType("Dell", "R670", "r670", nil),
		other(manufacturer("dell", "dell")),
		// As NetBox would store it, the same model.
		other(deviceType("Dell", "R670 ", "r670", nil)),
		deviceType("Dell", "R670", "r670-2", nil),
		other(manufacturer("Dell", "")),
	}
	got, err := compare.Group(dcim.Specs(), defs)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]int{{0, 5}, {1, 3, 4}, {2}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Group (-want +got):\n%s", diff)
	}

	unknown := deviceType("Dell", "R670", "r670", nil)
	unknown.Object.Type = "dcim.nothing"
	if _, err := compare.Group(dcim.Specs(), []compare.Definition{unknown}); err == nil || !strings.Contains(err.Error(), "no spec") {
		t.Errorf("a definition of a type without a spec: %v", err)
	}
}

func TestObjectsAcrossSources(t *testing.T) {
	t.Parallel()
	mine := withChildren(map[string]model.Children{
		"interfaces": {child("interface", "eth0", map[string]model.Value{"type": model.Choice("1000base-t")})},
	})
	theirs := other(withChildren(map[string]model.Children{
		"interfaces": {
			child("interface", "eth0", map[string]model.Value{"type": model.Choice("10gbase-t")}),
			child("interface", "iDRAC", map[string]model.Value{"type": model.Choice("1000base-t")}),
		},
	}))
	theirs.Object.Fields["u_height"] = model.NewDecimal(2, 0)
	observed := append(nbR670(),
		nbChild(20, 10, "interface", "eth0", map[string]model.Value{"type": model.Choice("1000base-t")}),
		nbChild(21, 10, "interface", "eth9", map[string]model.Value{"type": model.Choice("1000base-t")}),
	)
	objects := objectsOf(t, []compare.Definition{manufacturer("Dell", "dell"), mine, other(manufacturer("Dell", "dell")), theirs}, observed)
	if len(objects) != 2 {
		t.Fatalf("%d objects, want 2", len(objects))
	}
	dell, r670 := objects[0], objects[1]
	if !dell.InStep() || len(dell.Columns) != 3 {
		t.Errorf("Dell, derived by both: %s with %d differences and %d columns, want in step with 3", dell.Outcome, dell.Differences, len(dell.Columns))
	}
	want := []string{
		`manufacturer: dcim.manufacturer #1 name="Dell" dcim.manufacturer #1 name="Dell" dcim.manufacturer #1 name="Dell" managed`,
		`model: "R670" "R670" "R670" managed`,
		`slug: "r670" "r670" "r670" managed`,
		`part_number: unset unset ""`,
		`u_height: 1 2 1 differs managed`,
		`exclude_from_utilization: unset unset false`,
		`is_full_depth: unset unset true`,
		`subdevice_role: unset unset null`,
		`airflow: unset unset null`,
		`cooling_method: unset unset null`,
		`weight: unset unset null`,
		`weight_unit: unset unset null`,
		`end_of_life: unset unset null`,
		`description: unset unset ""`,
		`comments: unset unset ""`,
		`interfaces["eth0"]: true true true managed #20`,
		`interfaces["eth0"].type: "1000base-t" "10gbase-t" "1000base-t" differs managed`,
		// Only the other source has it.
		`interfaces["iDRAC"]: false true false differs managed`,
		`interfaces["iDRAC"].type: unset "1000base-t" unset managed`,
		// Only NetBox has it.
		`interfaces["eth9"]: false false true differs #21`,
	}
	if diff := cmp.Diff(want, objectRows(r670)); diff != "" {
		t.Errorf("R670's rows (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"library candidate 0", "other candidate 3", "#10"}, columnsOf(r670)); diff != "" {
		t.Errorf("R670's columns (-want +got):\n%s", diff)
	}
	if r670.Outcome != compare.Candidate || r670.Differences != 3 || r670.InStep() {
		t.Errorf("R670: %s with %d differences, want a candidate with 3", r670.Outcome, r670.Differences)
	}
}

func TestObjectsOfOneSourceAreItsResults(t *testing.T) {
	t.Parallel()
	defs := []compare.Definition{
		manufacturer("Dell", "dell"),
		withChildren(map[string]model.Children{"interfaces": {child("interface", "eth0", t1g)}}),
	}
	results := run(t, defs, nbR670())
	objects, err := compare.Objects(dcim.Specs(), results)
	if err != nil {
		t.Fatal(err)
	}
	for i, o := range objects {
		r := results[i]
		if o.Outcome != r.Outcome || o.Differences != r.Differences || o.NetBoxID != r.NetBoxID || len(o.Columns) != len(r.Columns) {
			t.Errorf("object %d: %s, %d differences, #%d, %d columns; its result %s, %d, #%d, %d",
				i, o.Outcome, o.Differences, o.NetBoxID, len(o.Columns), r.Outcome, r.Differences, r.NetBoxID, len(r.Columns))
		}
		if diff := cmp.Diff(rows(r), objectRows(o)); diff != "" {
			t.Errorf("object %d's rows (-result +object):\n%s", i, diff)
		}
	}
}

func TestMergeOfOne(t *testing.T) {
	t.Parallel()
	// Merging one comparison by the general way gives its rows, which
	// Objects takes as they are.
	def := withChildren(map[string]model.Children{
		"interfaces": {child("interface", "eth0", t1g), child("interface", "eth1", t1g)},
		"rear_ports": {child("rearport", "RP1", lc)},
		"front_ports": {
			frontPort("FP1", 1, mapping(1, "RP1", 1)),
			frontPort("FP2", 1, mapping(1, "RP1", 1)),
		},
	})
	observed := append(nbR670(),
		nbChild(20, 10, "interface", "eth1", t1g),
		nbChild(21, 10, "interface", "eth9", t1g),
		nbChild(22, 10, "interface", "eth3", t1g),
		nbChild(30, 10, "rearport", "RP1", lc),
		nbFrontPort(40, 10, "FP1", 1, nbMapping(1, 30, 1), nbMapping(2, 30, 1)),
		nbFrontPort(41, 10, "FP7", 1),
	)
	for _, observed := range [][]compare.Observed{observed, nil} {
		results := run(t, []compare.Definition{def}, observed)
		got, err := compare.MergeRows(dcim.Specs(), results, []int{0})
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(rows(results[0]), rows(compare.Result{Rows: got})); diff != "" {
			t.Errorf("merging one comparison (-its rows +merged):\n%s", diff)
		}
	}
}

func TestObjectChildrenCells(t *testing.T) {
	t.Parallel()
	// A front port one source has and the other hasn't: the other's cell
	// on its row is false, and on its mapping's, whose parent it hasn't
	// got, unset.
	mine := withChildren(map[string]model.Children{
		"rear_ports":  {child("rearport", "RP1", lc)},
		"front_ports": {frontPort("FP1", 1, mapping(1, "RP1", 1))},
	})
	theirs := other(withChildren(map[string]model.Children{
		"rear_ports": {child("rearport", "RP1", lc)},
	}))
	objects := objectsOf(t, []compare.Definition{mine, theirs}, nil)
	var got []string
	for _, row := range objectRows(objects[0]) {
		if strings.HasPrefix(row, "front_ports") {
			got = append(got, row)
		}
	}
	want := []string{
		`front_ports["FP1"]: true false differs managed`,
		`front_ports["FP1"].type: "lc" unset managed`,
		`front_ports["FP1"].positions: 1 unset managed`,
		`front_ports["FP1"].rear_ports[1]: true unset managed`,
		`front_ports["FP1"].rear_ports[1].rear_port: dcim.rearporttemplate name="RP1" unset managed`,
		`front_ports["FP1"].rear_ports[1].rear_port_position: 1 unset managed`,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FP1's rows (-want +got):\n%s", diff)
	}
	if o := objects[0]; o.Outcome != compare.Absent || o.Differences != 1 {
		t.Errorf("an object NetBox hasn't got, whose sources differ by a front port: %s with %d differences, want absent with 1", o.Outcome, o.Differences)
	}

	// Both have the front port, one without its mapping: the other's cell
	// on the mapping's row is false.
	theirs = other(withChildren(map[string]model.Children{
		"rear_ports":  {child("rearport", "RP1", lc)},
		"front_ports": {frontPort("FP1", 1)},
	}))
	got = nil
	for _, row := range objectRows(objectsOf(t, []compare.Definition{mine, theirs}, nil)[0]) {
		if strings.HasPrefix(row, `front_ports["FP1"].rear_ports[1]:`) {
			got = append(got, row)
		}
	}
	if diff := cmp.Diff([]string{`front_ports["FP1"].rear_ports[1]: true false differs managed`}, got); diff != "" {
		t.Errorf("the mapping's row (-want +got):\n%s", diff)
	}
}

func TestObjectCollisionsOnce(t *testing.T) {
	t.Parallel()
	// NetBox holds eth0 twice: each definition reports both, and the
	// object each once.
	def := withChildren(map[string]model.Children{"interfaces": {child("interface", "eth0", t1g)}})
	observed := append(nbR670(),
		nbChild(20, 10, "interface", "eth0", t1g),
		nbChild(21, 10, "interface", "eth0", t1g),
	)
	o := objectsOf(t, []compare.Definition{def, other(def)}, observed)[0]
	var got []string
	for _, c := range o.Collisions {
		got = append(got, c.Path.String()+" #"+itoa(c.ID))
	}
	if diff := cmp.Diff([]string{`interfaces["eth0"] #20`, `interfaces["eth0"] #21`}, got); diff != "" {
		t.Errorf("the object's collisions (-want +got):\n%s", diff)
	}
	if len(o.Definitions[0].Collisions) != 2 || len(o.Definitions[1].Collisions) != 2 {
		t.Errorf("the definitions' collisions: %d and %d, want 2 each", len(o.Definitions[0].Collisions), len(o.Definitions[1].Collisions))
	}
}

func TestObjectRowOrder(t *testing.T) {
	t.Parallel()
	// The children some definition has, in the order of the first that
	// has each, column by column; then those only NetBox has.
	mine := withChildren(map[string]model.Children{
		"interfaces": {child("interface", "eth2", t1g), child("interface", "eth0", t1g)},
	})
	theirs := other(withChildren(map[string]model.Children{
		"interfaces": {child("interface", "eth9", nil), child("interface", "eth0", t1g), child("interface", "eth1", t1g)},
	}))
	observed := append(nbR670(),
		nbChild(20, 10, "interface", "eth5", t1g),
		nbChild(21, 10, "interface", "eth9", t1g),
		nbChild(22, 10, "interface", "eth4", t1g),
	)
	var got []string
	for _, row := range objectRows(objectsOf(t, []compare.Definition{mine, theirs}, observed)[0]) {
		if i := strings.Index(row, ":"); strings.HasPrefix(row, "interfaces") && !strings.Contains(row[:i], ".") {
			got = append(got, row[:i])
		}
	}
	want := []string{`interfaces["eth2"]`, `interfaces["eth0"]`, `interfaces["eth9"]`, `interfaces["eth1"]`, `interfaces["eth4"]`, `interfaces["eth5"]`}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("the interfaces' order (-want +got):\n%s", diff)
	}
}

func TestObjectDefinedTwice(t *testing.T) {
	t.Parallel()
	a := deviceType("Dell", "R670", "r670", nil)
	b := deviceType("Dell", "R670", "r670", nil)
	b.Path = "device-types/Dell/r670-copy.yaml"
	objects := objectsOf(t, []compare.Definition{manufacturer("Dell", "dell"), a, other(a), b}, nbR670())
	o := objects[1]
	if o.Outcome != compare.Conflict || len(o.Definitions) != 3 || !slicesEqual(o.DuplicatedIn, []string{source}) {
		t.Errorf("a device type the library defines twice: %s with %d definitions, duplicated in %q; want a conflict with 3, in %q",
			o.Outcome, len(o.Definitions), o.DuplicatedIn, source)
	}
	for _, d := range o.Definitions {
		if !d.InStep() {
			t.Errorf("%s's definition %s is %s; each definition keeps its own outcome", d.Columns[0].Source, d.Columns[0].Path, d.Outcome)
		}
	}
}

func slicesEqual(a, b []string) bool {
	return cmp.Equal(a, b)
}

// clashesOf names an object's clashes: rule, values, its columns, and whom
// with.
func clashesOf(o compare.Object) []string {
	var out []string
	for _, c := range o.Clashes {
		var with []string
		for _, w := range c.With {
			with = append(with, w.Column.Source+":"+identityText(w.Identity))
		}
		vals, _ := json.Marshal(c.Values)
		cols, _ := json.Marshal(c.Columns)
		out = append(out, strings.Join(c.Rule, "+")+" "+string(vals)+" "+string(cols)+" with "+strings.Join(with, ","))
	}
	return out
}

func identityText(identity map[string]model.Value) string {
	data, _ := json.Marshal(identity)
	return string(data)
}

func TestObjectsClash(t *testing.T) {
	t.Parallel()
	r680 := nbDeviceType(11, 1, "Dell", "R680", "r680", nil)
	tests := []struct {
		name     string
		defs     []compare.Definition
		observed []compare.Observed
		// want are each object's clashes, by its index.
		want map[int][]string
	}{
		{
			name: "two device types NetBox has, both given a slug no NetBox device type has",
			defs: []compare.Definition{
				deviceType("Dell", "R670", "new", nil),
				other(deviceType("Dell", "R680", "new", nil)),
			},
			observed: append(nbR670(), r680),
			want: map[int][]string{
				0: {`manufacturer+slug {"manufacturer":{"id":1,"name":"Dell"},"slug":"new"} [0] with other:{"manufacturer":{"name":"Dell"},"model":"R680"}`},
				1: {`manufacturer+slug {"manufacturer":{"id":1,"name":"Dell"},"slug":"new"} [0] with library:{"manufacturer":{"name":"Dell"},"model":"R670"}`},
			},
		},
		{
			name: "the slug the R670 NetBox has holds already: the R680's comparison collides with it",
			defs: []compare.Definition{
				deviceType("Dell", "R670", "r670", nil),
				other(deviceType("Dell", "R680", "r670", nil)),
			},
			observed: append(nbR670(), r680),
		},
		{
			name: "within one source, NetBox having neither",
			defs: []compare.Definition{
				deviceType("Dell", "R670", "same", nil),
				deviceType("Dell", "R680", "same", nil),
			},
			want: map[int][]string{
				0: {`manufacturer+slug {"manufacturer":{"name":"Dell"},"slug":"same"} [0] with library:{"manufacturer":{"name":"Dell"},"model":"R680"}`},
				1: {`manufacturer+slug {"manufacturer":{"name":"Dell"},"slug":"same"} [0] with library:{"manufacturer":{"name":"Dell"},"model":"R670"}`},
			},
		},
		{
			name: "another manufacturer's device types don't share the rule",
			defs: []compare.Definition{
				deviceType("Dell", "R670", "same", nil),
				deviceType("HPE", "R680", "same", nil),
			},
		},
		{
			name: "derived slugs of manufacturers NetBox hasn't got would both be created",
			defs: []compare.Definition{
				manufacturer("NETGEAR", "netgear"),
				other(manufacturer("Netgear", "netgear")),
			},
			want: map[int][]string{
				0: {`slug {"slug":"netgear"} [0] with other:{"name":"Netgear"}`},
				1: {`slug {"slug":"netgear"} [0] with library:{"name":"NETGEAR"}`},
			},
		},
		{
			name: "a derived slug of a manufacturer NetBox has isn't written: NetBox's other collides",
			defs: []compare.Definition{
				manufacturer("NETGEAR", "netgear"),
				manufacturer("Netgear", "netgear"),
			},
			observed: []compare.Observed{nbManufacturer(1, "NETGEAR", "netgear")},
		},
		{
			name: "a derived slug of a manufacturer NetBox has, where NetBox holds another",
			defs: []compare.Definition{
				manufacturer("NETGEAR", "netgear"),
				manufacturer("Netgear", "netgear-2"),
			},
			observed: []compare.Observed{nbManufacturer(1, "NETGEAR", "netgear-2")},
		},
		{
			name: "a derived slug of a manufacturer NetBox has isn't written, though another derives it too",
			defs: []compare.Definition{
				manufacturer("NETGEAR", "x"),
				manufacturer("Netgear", "x"),
			},
			observed: []compare.Observed{nbManufacturer(1, "NETGEAR", "netgear-2")},
		},
		{
			name: "an unset slug holds nothing",
			defs: []compare.Definition{
				manufacturer("NETGEAR", ""),
				manufacturer("Netgear", ""),
			},
		},
		{
			name: "three objects, one of them defined by both sources",
			defs: []compare.Definition{
				deviceType("Dell", "R670", "same", nil),
				other(deviceType("Dell", "R670", "same", nil)),
				other(deviceType("Dell", "R680", "same", nil)),
				deviceType("Dell", "R690", "same", nil),
			},
			want: map[int][]string{
				0: {`manufacturer+slug {"manufacturer":{"name":"Dell"},"slug":"same"} [0,1] with other:{"manufacturer":{"name":"Dell"},"model":"R680"},library:{"manufacturer":{"name":"Dell"},"model":"R690"}`},
				1: {`manufacturer+slug {"manufacturer":{"name":"Dell"},"slug":"same"} [0] with library:{"manufacturer":{"name":"Dell"},"model":"R670"},other:{"manufacturer":{"name":"Dell"},"model":"R670"},library:{"manufacturer":{"name":"Dell"},"model":"R690"}`},
				2: {`manufacturer+slug {"manufacturer":{"name":"Dell"},"slug":"same"} [0] with library:{"manufacturer":{"name":"Dell"},"model":"R670"},other:{"manufacturer":{"name":"Dell"},"model":"R670"},other:{"manufacturer":{"name":"Dell"},"model":"R680"}`},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for i, o := range objectsOf(t, tt.defs, tt.observed) {
				if diff := cmp.Diff(tt.want[i], clashesOf(o)); diff != "" {
					t.Errorf("object %d's clashes (-want +got):\n%s", i, diff)
				}
				if len(o.Clashes) > 0 && o.Outcome != compare.Conflict {
					t.Errorf("object %d clashes, but is %s", i, o.Outcome)
				}
			}
		})
	}
}

func TestObjectUnresolvedTakesTheObjectsOutcome(t *testing.T) {
	t.Parallel()
	// Dell is absent, but clashes with Dell Inc., so the R670's reference
	// to it says it is a conflict; the definition's own says absent.
	objects := objectsOf(t, []compare.Definition{
		manufacturer("Dell", "dell"),
		other(manufacturer("Dell Inc", "dell")),
		deviceType("Dell", "R670", "r670", nil),
	}, nil)
	r670 := objects[2]
	if len(r670.Unresolved) != 1 || r670.Unresolved[0].Outcome != compare.Conflict {
		t.Fatalf("R670's unresolved references: %+v, want Dell's, a conflict", r670.Unresolved)
	}
	if own := r670.Definitions[0].Unresolved[0].Outcome; own != compare.Absent {
		t.Errorf("the definition's own reference is %s, want absent, its source's definition of Dell", own)
	}
}

func TestObjectsRefused(t *testing.T) {
	t.Parallel()
	ifaces := map[string]model.Value{"interfaces": model.Children{child("interface", "eth0", t1g), child("interface", "eth1", t1g)}}
	results := run(t, []compare.Definition{deviceType("Dell", "R670", "r670", ifaces), other(deviceType("Dell", "R670", "r670", ifaces))}, nbR670())
	tests := []struct {
		name   string
		change func([]compare.Result)
		want   string
	}{
		{"different NetBox objects", func(r []compare.Result) {
			r[1].NetBoxID, r[1].Columns[1].ID = 99, 99
		}, "different NetBox objects"},
		{"a candidate and an absent definition", func(r []compare.Result) {
			r[1].Outcome, r[1].NetBoxID, r[1].Columns = compare.Absent, 0, r[1].Columns[:1]
			for j := range r[1].Rows {
				r[1].Rows[j].Cells = r[1].Rows[j].Cells[:1]
			}
		}, "different NetBox objects"},
		{"NetBox's cells differ", func(r []compare.Result) {
			r[1].Rows[4].Cells[1] = model.NewDecimal(3, 0)
		}, "give NetBox's u_height differently"},
		{"a row short of a cell", func(r []compare.Result) {
			r[1].Rows[0].Cells = r[1].Rows[0].Cells[:1]
		}, "with 1 cells for 2 columns"},
		{"a candidate without a NetBox object", func(r []compare.Result) {
			r[1].NetBoxID, r[1].Columns = 0, r[1].Columns[:1]
			for j := range r[1].Rows {
				r[1].Rows[j].Cells = r[1].Rows[j].Cells[:1]
			}
		}, "is candidate, though its NetBox object is #0"},
		{"an outcome Compare doesn't give", func(r []compare.Result) {
			r[0].Outcome = "linked"
		}, `the outcome "linked"`},
		{"a row twice", func(r []compare.Result) {
			r[1].Rows = append(r[1].Rows, r[1].Rows[0])
		}, "has the row manufacturer twice"},
		{"a child's field before the child", func(r []compare.Result) {
			field := r[1].Rows[len(r[1].Rows)-1]
			r[1].Rows = append([]compare.Row{field}, r[1].Rows[:len(r[1].Rows)-1]...)
		}, `has the row interfaces["eth1"].type before its child's`},
		{"a child's field among another's", func(r []compare.Result) {
			// eth0's row, eth0's type, eth1's row, eth1's type: swap the
			// types.
			n := len(r[1].Rows)
			r[1].Rows[n-3], r[1].Rows[n-1] = r[1].Rows[n-1], r[1].Rows[n-3]
		}, `has the row interfaces["eth1"].type among those of interfaces["eth0"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			copied := make([]compare.Result, len(results))
			for i, r := range results {
				r.Columns = append([]compare.Column(nil), r.Columns...)
				r.Rows = append([]compare.Row(nil), r.Rows...)
				for j := range r.Rows {
					r.Rows[j].Cells = append([]model.Value(nil), r.Rows[j].Cells...)
				}
				copied[i] = r
			}
			tt.change(copied)
			_, err := compare.Objects(dcim.Specs(), copied)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Objects: %v, want an error saying %q", err, tt.want)
			}
		})
	}
}

func TestObjectJSON(t *testing.T) {
	t.Parallel()
	theirs := other(deviceType("Dell", "R670", "r670", nil))
	theirs.Object.Fields["u_height"] = model.NewDecimal(2, 0)
	theirs.Unmapped = nil
	mine := deviceType("Dell", "R670", "r670", nil)
	mine.Unmapped = nil
	objects := objectsOf(t, []compare.Definition{mine, theirs, other(deviceType("Dell", "R680", "r670", nil))}, nbR670())
	data, err := json.Marshal(objects[0])
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"type":          "dcim.devicetype",
		"identity":      map[string]any{"manufacturer": map[string]any{"name": "Dell"}, "model": "R670"},
		"outcome":       "candidate",
		"in_step":       false,
		"netbox_id":     10.0,
		"differences":   1.0,
		"collisions":    []any{},
		"unresolved":    []any{},
		"duplicated_in": []any{},
		"clashes":       []any{},
		"columns": []any{
			map[string]any{
				"kind": "definition", "source": "library", "path": "device-types/Dell/r670.yaml", "derived_from": []any{}, "unmapped": map[string]any{},
				"outcome": "candidate", "in_step": true, "differences": 0.0, "collisions": []any{},
			},
			map[string]any{
				"kind": "definition", "source": "other", "path": "device-types/Dell/r670.yaml", "derived_from": []any{}, "unmapped": map[string]any{},
				"outcome": "candidate", "in_step": false, "differences": 1.0, "collisions": []any{},
			},
			map[string]any{"kind": "netbox", "netbox_id": 10.0},
		},
	}
	gotRows := got["rows"]
	delete(got, "rows")
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("the object's JSON (-want +got):\n%s", diff)
	}
	rowsList, _ := gotRows.([]any)
	if len(rowsList) != len(objects[0].Rows) {
		t.Fatalf("%d rows in JSON, %d in the object", len(rowsList), len(objects[0].Rows))
	}
	uHeight := map[string]any{
		"path": []any{"u_height"}, "cells": []any{map[string]any{"value": 1.0}, map[string]any{"value": 2.0}, map[string]any{"value": 1.0}},
		"differs": true, "managed": true, "derived": false, "netbox_id": nil,
	}
	if diff := cmp.Diff(uHeight, rowsList[4]); diff != "" {
		t.Errorf("u_height's row (-want +got):\n%s", diff)
	}

	// A clash, with whom.
	data, err = json.Marshal(objects[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if clashes, _ := got["clashes"].([]any); got["outcome"] != "conflict" || len(clashes) != 0 {
		// The R680's slug is the R670's in NetBox: NetBox's comparison
		// says so, as a collision, not a clash.
		t.Errorf("the R680, whose slug NetBox's R670 holds: %s, clashes %v", got["outcome"], got["clashes"])
	}
}

// TestPathJSONIsEncodingJSONs checks that a path is written as
// encoding/json writes the list of its steps, whatever its names and keys
// hold.
func TestPathJSONIsEncodingJSONs(t *testing.T) {
	t.Parallel()
	odd := []string{"eth0", "", "a<b>&c", "a<b", "a>b", "a&b", "\u2028", "line\nbreak", "tab\t", "\x01", "quote\"back\\", "caf\u00e9", "\xff\xfe", "\u007f"}
	var paths []compare.Path
	for _, name := range odd {
		paths = append(paths,
			compare.Path{{Field: name}},
			compare.Path{{Field: "interfaces"}, {Key: map[string]model.Value{"name": model.String(name)}}, {Field: "type"}},
			compare.Path{{Field: "interfaces"}, {Key: map[string]model.Value{name: model.String("x")}}},
			compare.Path{{Field: "front_ports"}, {Key: map[string]model.Value{"name": model.String(name)}}, {Field: "rear_ports"}, {Key: map[string]model.Value{"position": model.Integer(7)}}},
			compare.Path{{Field: "x"}, {Key: map[string]model.Value{"b": model.String(name), "a": model.Integer(1)}}},
			compare.Path{{Field: "x"}, {Key: map[string]model.Value{"ref": model.Reference{Type: "dcim.rearporttemplate", Key: map[string]model.Value{"name": model.String(name)}}}}},
			compare.Path{{Field: "x"}, {Key: map[string]model.Value{"n": nil}}},
		)
	}
	paths = append(paths, compare.Path{}, nil)
	for _, p := range paths {
		steps := make([]any, len(p))
		for i, s := range p {
			if s.Key != nil {
				steps[i] = s.Key
			} else {
				steps[i] = s.Field
			}
		}
		want, err := json.Marshal(steps)
		if err != nil {
			t.Fatal(err)
		}
		// Directly too, since encoding/json escapes what a method gives it
		// again.
		direct, err := p.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) || string(direct) != string(want) {
			t.Errorf("path %#v:\n got %s\n and %s\nwant %s", p, got, direct, want)
		}
	}
}
