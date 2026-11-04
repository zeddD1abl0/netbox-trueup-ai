package compare_test

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/compare"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/dcim"
)

const source = "library"

// manufacturer is a derived manufacturer definition, with the slug NetBox's
// UI would make, or none if slug is "".
func manufacturer(name, slug string) compare.Definition {
	d := format.Definition{
		Object:      model.Object{Type: "dcim.manufacturer", Fields: map[string]model.Value{"name": model.String(name)}},
		Identity:    map[string]model.Value{"name": model.String(name)},
		DerivedFrom: []string{"device-types/" + name + "/a.yaml"},
	}
	if slug != "" {
		d.Object.Fields["slug"] = model.String(slug)
		d.Derived = []string{"slug"}
	}
	return compare.Definition{Source: source, Definition: d}
}

func manufacturerRef(name string) model.Reference {
	return model.Reference{Type: "dcim.manufacturer", Key: map[string]model.Value{"name": model.String(name)}}
}

// deviceType is a device type definition, with fields added or replaced.
func deviceType(maker, modelName, slug string, fields map[string]model.Value) compare.Definition {
	ref := manufacturerRef(maker)
	d := format.Definition{
		Object: model.Object{Type: "dcim.devicetype", Fields: map[string]model.Value{
			"manufacturer": ref, "model": model.String(modelName), "slug": model.String(slug), "u_height": model.NewDecimal(1, 0),
		}},
		Identity: map[string]model.Value{"manufacturer": ref, "model": model.String(modelName)},
		Path:     "device-types/" + maker + "/" + slug + ".yaml",
		Unmapped: map[string]int{"interfaces": 2},
	}
	maps.Copy(d.Object.Fields, fields)
	return compare.Definition{Source: source, Definition: d}
}

// nbManufacturer is a manufacturer as NetBox's API gives it.
func nbManufacturer(id int64, name, slug string) compare.Observed {
	return compare.Observed{ID: id, Object: model.Object{Type: "dcim.manufacturer", Fields: map[string]model.Value{
		"name": model.String(name), "slug": model.String(slug), "description": model.String(""), "comments": model.String(""),
	}}}
}

// nbDeviceType is a device type as NetBox's API gives it, with fields
// replaced.
func nbDeviceType(id, makerID int64, maker, modelName, slug string, fields map[string]model.Value) compare.Observed {
	o := compare.Observed{ID: id, Object: model.Object{Type: "dcim.devicetype", Fields: map[string]model.Value{
		"manufacturer":             model.Reference{Type: "dcim.manufacturer", ID: makerID, Key: map[string]model.Value{"name": model.String(maker)}},
		"model":                    model.String(modelName),
		"slug":                     model.String(slug),
		"part_number":              model.String(""),
		"u_height":                 model.NewDecimal(10, 1),
		"exclude_from_utilization": model.Boolean(false),
		"is_full_depth":            model.Boolean(true),
		"subdevice_role":           model.Null{},
		"airflow":                  model.Null{},
		"cooling_method":           model.Null{},
		"weight":                   model.Null{},
		"weight_unit":              model.Null{},
		"end_of_life":              model.Null{},
		"description":              model.String(""),
		"comments":                 model.String(""),
	}}}
	maps.Copy(o.Object.Fields, fields)
	return o
}

func run(t *testing.T, defs []compare.Definition, observed []compare.Observed) []compare.Result {
	t.Helper()
	results, err := compare.Compare(dcim.Specs(), defs, observed)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(defs) {
		t.Fatalf("Compare returned %d results for %d definitions", len(results), len(defs))
	}
	return results
}

// summary is what a test checks of a result.
type summary struct {
	Outcome     compare.Outcome
	NetBoxID    int64
	Differences int
	Collisions  []string // "rule:id"
	Unresolved  []string // "field:outcome"
	Differing   []string // paths of the rows that differ and are managed
}

func summarize(r compare.Result) summary {
	s := summary{Outcome: r.Outcome, NetBoxID: r.NetBoxID, Differences: r.Differences}
	for _, c := range r.Collisions {
		s.Collisions = append(s.Collisions, strings.Join(c.Rule, ",")+":"+itoa(c.ID))
	}
	for _, u := range r.Unresolved {
		s.Unresolved = append(s.Unresolved, u.Field+":"+string(u.Outcome))
		if len(u.Candidates) > 0 && len(u.Candidates) < 2 {
			s.Unresolved = append(s.Unresolved, "one candidate, which isn't ambiguous")
		}
	}
	for _, row := range r.Rows {
		if row.Differs && row.Managed {
			s.Differing = append(s.Differing, row.Path.String())
		}
	}
	return s
}

func itoa(i int64) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func TestManufacturers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		def      compare.Definition
		observed []compare.Observed
		want     summary
	}{
		{
			"name matches", manufacturer("Dell", "dell"),
			[]compare.Observed{nbManufacturer(3, "Dell", "dell")},
			summary{Outcome: compare.Candidate, NetBoxID: 3},
		},
		{
			// The derived slug isn't managed: importing leaves NetBox's.
			"name matches, slug differs", manufacturer("Dell", "dell"),
			[]compare.Observed{nbManufacturer(3, "Dell", "dell-emc")},
			summary{Outcome: compare.Candidate, NetBoxID: 3},
		},
		{
			// Importing links to Dell and never writes the slug, so it
			// can't collide with Dell EMC's.
			"name matches one, derived slug another", manufacturer("Dell", "dell"),
			[]compare.Observed{nbManufacturer(3, "Dell", "dell-inc"), nbManufacturer(4, "Dell EMC", "dell")},
			summary{Outcome: compare.Candidate, NetBoxID: 3},
		},
		{
			// Creating it would fail on the slug.
			"only the derived slug matches", manufacturer("NETGEAR", "netgear"),
			[]compare.Observed{nbManufacturer(7, "Netgear", "netgear")},
			summary{Outcome: compare.Conflict, Collisions: []string{"slug:7"}},
		},
		{
			"nothing matches", manufacturer("Arista", "arista"),
			[]compare.Observed{nbManufacturer(3, "Dell", "dell")},
			summary{Outcome: compare.Absent},
		},
		{
			"no slug could be derived", manufacturer("中文", ""),
			[]compare.Observed{nbManufacturer(3, "Dell", "dell")},
			summary{Outcome: compare.Absent},
		},
		{
			"case counts", manufacturer("dell", "dell-2"),
			[]compare.Observed{nbManufacturer(3, "Dell", "dell")},
			summary{Outcome: compare.Absent},
		},
		{
			// NetBox's API and UI trim names, so only another tool could
			// store this one, and NetBox would take "Dell" beside it.
			"white space in NetBox counts", manufacturer("Dell", "dell-2"),
			[]compare.Observed{nbManufacturer(3, "Dell ", "dell")},
			summary{Outcome: compare.Absent},
		},
		{
			// NetBox's constraints forbid this, so its answer can't say
			// which one the definition is.
			"two NetBox objects with one name", manufacturer("Dell", "dell"),
			[]compare.Observed{nbManufacturer(3, "Dell", "dell"), nbManufacturer(9, "Dell", "dell-2")},
			summary{Outcome: compare.Conflict, Collisions: []string{"name:3", "name:9", "slug:3"}},
		},
	}
	for _, tt := range tests {
		got := summarize(run(t, []compare.Definition{tt.def}, tt.observed)[0])
		if diff := cmp.Diff(tt.want, got); diff != "" {
			t.Errorf("%s: (-want +got)\n%s", tt.name, diff)
		}
	}
}

func TestDeviceTypes(t *testing.T) {
	t.Parallel()
	dell := nbManufacturer(3, "Dell", "dell")
	tests := []struct {
		name     string
		def      compare.Definition
		observed []compare.Observed
		want     summary
	}{
		{
			"model matches, nothing differs", deviceType("Dell", "R670", "r670", nil),
			[]compare.Observed{dell, nbDeviceType(10, 3, "Dell", "R670", "r670", nil)},
			summary{Outcome: compare.Candidate, NetBoxID: 10},
		},
		{
			"a field differs", deviceType("Dell", "R670", "r670", map[string]model.Value{"u_height": model.NewDecimal(2, 0)}),
			[]compare.Observed{dell, nbDeviceType(10, 3, "Dell", "R670", "r670", nil)},
			summary{Outcome: compare.Candidate, NetBoxID: 10, Differences: 1, Differing: []string{"u_height"}},
		},
		{
			// The library's slug is read, not derived, so it is managed.
			"the slug differs", deviceType("Dell", "R670", "r670", nil),
			[]compare.Observed{dell, nbDeviceType(10, 3, "Dell", "R670", "poweredge-r670", nil)},
			summary{Outcome: compare.Candidate, NetBoxID: 10, Differences: 1, Differing: []string{"slug"}},
		},
		{
			"a blank choice is null", deviceType("Dell", "R670", "r670", map[string]model.Value{"airflow": model.Choice("")}),
			[]compare.Observed{dell, nbDeviceType(10, 3, "Dell", "R670", "r670", nil)},
			summary{Outcome: compare.Candidate, NetBoxID: 10},
		},
		{
			"a choice NetBox doesn't have", deviceType("Dell", "R670", "r670", map[string]model.Value{"airflow": model.Choice("front-to-rear")}),
			[]compare.Observed{dell, nbDeviceType(10, 3, "Dell", "R670", "r670", nil)},
			summary{Outcome: compare.Candidate, NetBoxID: 10, Differences: 1, Differing: []string{"airflow"}},
		},
		{
			"the definition is normalized", deviceType("Dell", " R670\t", "r670", map[string]model.Value{"description": model.String("A server ")}),
			[]compare.Observed{dell, nbDeviceType(10, 3, "Dell", "R670", "r670", map[string]model.Value{"description": model.String("A server")})},
			summary{Outcome: compare.Candidate, NetBoxID: 10},
		},
		{
			"model matches one, slug another", deviceType("Dell", "R670", "r670", nil),
			[]compare.Observed{
				dell,
				nbDeviceType(10, 3, "Dell", "R670", "poweredge-r670", nil),
				nbDeviceType(11, 3, "Dell", "PowerEdge R670", "r670", nil),
			},
			summary{Outcome: compare.Conflict, NetBoxID: 10, Differences: 1, Collisions: []string{"manufacturer,slug:11"}, Differing: []string{"slug"}},
		},
		{
			"only the slug matches", deviceType("Dell", "R670", "r670", nil),
			[]compare.Observed{dell, nbDeviceType(11, 3, "Dell", "PowerEdge R670", "r670", nil)},
			summary{Outcome: compare.Conflict, Collisions: []string{"manufacturer,slug:11"}},
		},
		{
			"another manufacturer's model", deviceType("Dell", "R670", "r670", nil),
			[]compare.Observed{dell, nbManufacturer(4, "HPE", "hpe"), nbDeviceType(12, 4, "HPE", "R670", "r670", nil)},
			summary{Outcome: compare.Absent},
		},
		{
			"case counts", deviceType("Dell", "r670", "r670-2", nil),
			[]compare.Observed{dell, nbDeviceType(10, 3, "Dell", "R670", "r670", nil)},
			summary{Outcome: compare.Absent},
		},
		{
			"the manufacturer isn't in NetBox", deviceType("Arista", "7050", "7050", nil),
			[]compare.Observed{dell},
			summary{Outcome: compare.Absent, Unresolved: []string{"manufacturer:"}},
		},
		{
			// NetBox's constraint forbids two manufacturers of one name, so
			// its answer can't say which the device type's is.
			"the manufacturer is in NetBox twice", deviceType("Dell", "R670", "r670", nil),
			[]compare.Observed{dell, nbManufacturer(4, "Dell", "dell-2"), nbDeviceType(10, 3, "Dell", "R670", "r670", nil)},
			summary{Outcome: compare.Conflict, Collisions: []string{"manufacturer,model:10", "manufacturer,slug:10"}, Unresolved: []string{"manufacturer:"}},
		},
		{
			"the manufacturer is in NetBox twice, without the device type", deviceType("Dell", "R770", "r770", nil),
			[]compare.Observed{dell, nbManufacturer(4, "Dell", "dell-2"), nbDeviceType(10, 3, "Dell", "R670", "r670", nil)},
			summary{Outcome: compare.Conflict, Unresolved: []string{"manufacturer:"}},
		},
	}
	for _, tt := range tests {
		got := summarize(run(t, []compare.Definition{tt.def}, tt.observed)[0])
		if diff := cmp.Diff(tt.want, got); diff != "" {
			t.Errorf("%s: (-want +got)\n%s", tt.name, diff)
		}
	}
}

func TestUnresolvedReferenceGetsItsDefinitionsOutcome(t *testing.T) {
	t.Parallel()
	results := run(t,
		[]compare.Definition{
			manufacturer("NETGEAR", "netgear"),
			manufacturer("Arista", "arista"),
			deviceType("NETGEAR", "GS105", "gs105", nil),
			deviceType("Arista", "7050", "7050", nil),
			deviceType("Juniper", "MX5", "mx5", nil),
		},
		[]compare.Observed{nbManufacturer(7, "Netgear", "netgear")},
	)
	var got []string
	for _, r := range results[2:] {
		got = append(got, summarize(r).Unresolved...)
	}
	want := []string{"manufacturer:conflict", "manufacturer:absent", "manufacturer:"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("unresolved (-want +got)\n%s", diff)
	}
}

func TestRows(t *testing.T) {
	t.Parallel()
	results := run(t,
		[]compare.Definition{
			manufacturer("Dell", "dell"),
			deviceType("Dell", "R670", "r670", map[string]model.Value{"u_height": model.NewDecimal(2, 0)}),
			deviceType("Dell", "R770", "r770", nil),
		},
		[]compare.Observed{nbManufacturer(3, "Dell", "dell-emc"), nbDeviceType(10, 3, "Dell", "R670", "r670", nil)},
	)
	rows := map[string]compare.Row{}
	for _, row := range results[0].Rows {
		rows[row.Path.String()] = row
	}
	if slug := rows["slug"]; !slug.Derived || slug.Managed || !slug.Differs {
		t.Errorf("a candidate's derived slug: %+v, want derived, differing and not managed", slug)
	}
	if desc := rows["description"]; desc.Managed || desc.Cells[0] != nil || !model.Equal(desc.Cells[1], model.String("")) {
		t.Errorf("a field only NetBox sets: %+v, want it shown and not managed", desc)
	}

	var paths []string
	for _, row := range results[1].Rows {
		paths = append(paths, row.Path.String())
		if len(row.Cells) != 2 {
			t.Errorf("row %s has %d cells, want 2", row.Path, len(row.Cells))
		}
	}
	// Every field, in the spec's order, since NetBox sets them all.
	var want []string
	for _, s := range dcim.Specs() {
		if s.Type == "dcim.devicetype" {
			for _, f := range s.Fields {
				want = append(want, f.Name)
			}
		}
	}
	if diff := cmp.Diff(want, paths); diff != "" {
		t.Errorf("a candidate's rows (-want +got)\n%s", diff)
	}
	if ref, _ := results[1].Rows[0].Cells[0].(model.Reference); ref.ID != 3 {
		t.Errorf("the definition's manufacturer = %v, want it resolved to #3", ref)
	}

	// An absent device type has the definition's column alone, and a row
	// only for each field it sets.
	if len(results[2].Columns) != 1 || len(results[2].Rows) != 4 {
		t.Errorf("an absent device type has %d columns and %d rows, want 1 and 4", len(results[2].Columns), len(results[2].Rows))
	}
	for _, row := range results[2].Rows {
		if !row.Managed || row.Differs {
			t.Errorf("an absent device type's %s: %+v, want managed and not differing", row.Path, row)
		}
	}
}

func TestCompareFails(t *testing.T) {
	t.Parallel()
	dell := nbManufacturer(3, "Dell", "dell")
	unknown := deviceType("Dell", "R670", "r670", nil)
	unknown.Object.Type = "dcim.rack"
	extra := deviceType("Dell", "R670", "r670", map[string]model.Value{"colour": model.String("red")})
	without := func(o compare.Observed, name string) compare.Observed {
		o.Object.Fields = maps.Clone(o.Object.Fields)
		delete(o.Object.Fields, name)
		return o
	}
	with := func(o compare.Observed, name string, v model.Value) compare.Observed {
		o.Object.Fields = maps.Clone(o.Object.Fields)
		o.Object.Fields[name] = v
		return o
	}
	nbDT := nbDeviceType(10, 3, "Dell", "R670", "r670", nil)
	defWith := func(name string, v model.Value) compare.Definition {
		d := deviceType("Dell", "R670", "r670", nil)
		d.Object.Fields[name] = v
		if _, ok := d.Identity[name]; ok {
			d.Identity = maps.Clone(d.Identity)
			d.Identity[name] = v
		}
		return d
	}
	identityOff := deviceType("Dell", "R670", "r670", nil)
	identityOff.Identity = map[string]model.Value{"manufacturer": manufacturerRef("Dell"), "model": model.String("R770")}
	noModel := deviceType("Dell", "R670", "r670", nil)
	delete(noModel.Object.Fields, "model")
	derivedExtra := manufacturer("Dell", "dell")
	derivedExtra.Object.Fields["colour"] = model.String("red")
	nbExtra := nbDeviceType(10, 3, "Dell", "R670", "r670", map[string]model.Value{"colour": model.String("red")})
	nbUnknown := dell
	nbUnknown.Object = model.Object{Type: "dcim.rack", Fields: nil}
	tests := []struct {
		name     string
		defs     []compare.Definition
		observed []compare.Observed
		wantErr  string
	}{
		{"an object listed twice", nil, []compare.Observed{dell, dell}, "NetBox listed dcim.manufacturer #3 twice"},
		{"an object of a type without a spec", nil, []compare.Observed{nbUnknown}, `is a "dcim.rack", which has no spec`},
		{"an object without an ID", nil, []compare.Observed{nbManufacturer(0, "Dell", "dell")}, "has the ID 0"},
		{"an object with a field the spec doesn't have", nil, []compare.Observed{dell, nbExtra}, `#10 sets "colour"`},
		{"a definition of a type without a spec", []compare.Definition{unknown}, nil, `is a "dcim.rack", which has no spec`},
		{"a definition with a field the spec doesn't have", []compare.Definition{extra}, nil, `sets "colour"`},
		{"an object without a field", nil, []compare.Observed{dell, without(nbDT, "u_height")}, "dcim.devicetype #10 has no u_height"},
		{"an object with null where it can't be", nil, []compare.Observed{dell, with(nbDT, "model", model.Null{})}, "#10 model is null, which it can't be"},
		{"an object with a value of another kind", nil, []compare.Observed{dell, with(nbDT, "u_height", model.String("1"))}, "#10 u_height is a string, not a decimal"},
		{"an object referring to another type", nil, []compare.Observed{dell, with(nbDT, "manufacturer", model.Reference{Type: "dcim.platform", ID: 3})}, `#10 manufacturer refers to a "dcim.platform", which it can't`},
		{"an object referring to no ID", nil, []compare.Observed{dell, with(nbDT, "manufacturer", manufacturerRef("Dell"))}, "#10 manufacturer refers to no NetBox object"},
		{"a definition with a value of another kind", []compare.Definition{defWith("u_height", model.Integer(1))}, nil, "u_height is a integer, not a decimal"},
		{"a definition referring by ID", []compare.Definition{defWith("manufacturer", model.Reference{Type: "dcim.manufacturer", ID: 999})}, nil, "manufacturer refers to a dcim.manufacturer other than by its natural key"},
		{"a definition referring by nothing", []compare.Definition{defWith("manufacturer", model.Reference{Type: "dcim.manufacturer"})}, nil, "other than by its natural key"},
		{"a definition referring by another key", []compare.Definition{defWith("manufacturer", model.Reference{Type: "dcim.manufacturer", Key: map[string]model.Value{"slug": model.String("dell")}})}, nil, "refers to a dcim.manufacturer without its name"},
		{"a definition whose identity says otherwise", []compare.Definition{identityOff}, nil, `sets model to "R670", but its identity says "R770"`},
		{"a definition without an identity field", []compare.Definition{noModel}, nil, "doesn't set model, which is part of its identity"},
		{
			"a derived definition with a field the spec doesn't have",
			[]compare.Definition{derivedExtra},
			nil,
			`library's definition dcim.manufacturer name "Dell" sets "colour"`,
		},
	}
	for _, tt := range tests {
		_, err := compare.Compare(dcim.Specs(), tt.defs, tt.observed)
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("%s: Compare = %v, want an error with %q", tt.name, err, tt.wantErr)
		}
	}
}

func TestJSON(t *testing.T) {
	t.Parallel()
	results := run(t,
		[]compare.Definition{manufacturer("Dell", "dell"), deviceType("Arista", "7050", "7050", nil)},
		[]compare.Observed{nbManufacturer(3, "Dell", "dell-emc")},
	)
	got, err := json.Marshal(results[0])
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"dcim.manufacturer","identity":{"name":"Dell"},"outcome":"candidate","in_step":true,"netbox_id":3,"differences":0,` +
		`"collisions":[],"unresolved":[],` +
		`"columns":[{"kind":"definition","source":"library","path":null,"derived_from":["device-types/Dell/a.yaml"],"unmapped":{}},{"kind":"netbox","netbox_id":3}],` +
		`"rows":[{"path":["name"],"cells":[{"value":"Dell"},{"value":"Dell"}],"differs":false,"managed":true,"derived":false},` +
		`{"path":["slug"],"cells":[{"value":"dell"},{"value":"dell-emc"}],"differs":true,"managed":false,"derived":true},` +
		`{"path":["description"],"cells":[{},{"value":""}],"differs":false,"managed":false,"derived":false},` +
		`{"path":["comments"],"cells":[{},{"value":""}],"differs":false,"managed":false,"derived":false}]}`
	if string(got) != want {
		t.Errorf("JSON:\n got %s\nwant %s", got, want)
	}

	got, err = json.Marshal(results[1])
	if err != nil {
		t.Fatal(err)
	}
	want = `{"type":"dcim.devicetype","identity":{"manufacturer":{"name":"Arista"},"model":"7050"},"outcome":"absent","in_step":false,"netbox_id":null,"differences":0,` +
		`"collisions":[],"unresolved":[{"field":"manufacturer","type":"dcim.manufacturer","key":{"name":"Arista"},"netbox_ids":[],"outcome":null}],` +
		`"columns":[{"kind":"definition","source":"library","path":"device-types/Arista/7050.yaml","derived_from":[],"unmapped":{"interfaces":2}}],` +
		`"rows":[{"path":["manufacturer"],"cells":[{"value":{"name":"Arista"}}],"differs":false,"managed":true,"derived":false},` +
		`{"path":["model"],"cells":[{"value":"7050"}],"differs":false,"managed":true,"derived":false},` +
		`{"path":["slug"],"cells":[{"value":"7050"}],"differs":false,"managed":true,"derived":false},` +
		`{"path":["u_height"],"cells":[{"value":1}],"differs":false,"managed":true,"derived":false}]}`
	if string(got) != want {
		t.Errorf("JSON:\n got %s\nwant %s", got, want)
	}
}
