package dcim

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/openapi"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/spectest"
)

// cloneSpecs copies specs deeply enough that a test can change any field of
// any spec without touching the package's.
func cloneSpecs(specs []*model.Spec) map[string]*model.Spec {
	out := map[string]*model.Spec{}
	for _, s := range specs {
		c := *s
		c.Fields = slices.Clone(s.Fields)
		for i := range c.Fields {
			f := &c.Fields[i]
			if f.Choices != nil {
				ch := *f.Choices
				ch.Values = slices.Clone(ch.Values)
				f.Choices = &ch
			}
			if f.Ref != nil {
				r := *f.Ref
				r.Types = slices.Clone(r.Types)
				f.Ref = &r
			}
		}
		c.Children = slices.Clone(s.Children)
		c.Ignored = slices.Clone(s.Ignored)
		c.Sources = slices.Clone(s.Sources)
		out[s.Type] = &c
	}
	return out
}

func listOf(specs map[string]*model.Spec) []*model.Spec {
	var out []*model.Spec
	for _, s := range withPrototypes() {
		out = append(out, specs[s.Type])
	}
	return out
}

func loadSnapshot(t *testing.T) *openapi.Document {
	t.Helper()
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := openapi.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestContractFinds breaks the specs one way at a time and checks that
// spectest.Contract reports each against the schema.
func TestContractFinds(t *testing.T) {
	t.Parallel()
	doc := loadSnapshot(t)
	type specs = map[string]*model.Spec
	field := func(s specs, typ, name string) *model.Field { return s["dcim."+typ].Field(name) }
	tests := []struct {
		name   string
		breaks func(s specs)
		want   string
	}{
		{"a field NetBox hasn't got", func(s specs) { field(s, "devicetype", "part_number").Name = "part_no" }, "dcim.devicetype.part_no: NetBox's API doesn't give it"},
		{"a field neither described nor ignored", func(s specs) {
			d := s["dcim.devicetype"]
			d.Fields = slices.DeleteFunc(d.Fields, func(f model.Field) bool { return f.Name == "comments" })
		}, "NetBox's API has comments, which the spec neither describes nor ignores"},
		{"an ignored field NetBox hasn't got", func(s specs) {
			s["dcim.manufacturer"].Ignored = append(s["dcim.manufacturer"].Ignored, model.Ignored{Name: "colour", Reason: "x"})
		}, "it ignores colour, which NetBox's API hasn't got"},
		{"a writable field read-only", func(s specs) {
			f := field(s, "devicetype", "comments")
			f.ReadOnly, f.Default = true, nil
		}, "dcim.devicetype.comments: it is read-only, but NetBox's API writes it"},
		{"a required field optional", func(s specs) { field(s, "devicetype", "model").Required = false }, "dcim.devicetype.model: its spec says required is false, NetBox's API true"},
		{"an optional field required", func(s specs) { field(s, "manufacturer", "description").Required = true }, "its spec says required is true, NetBox's API false"},
		{"a nullable field not", func(s specs) { field(s, "devicetype", "weight").Nullable = false }, "dcim.devicetype.weight: its spec says nullable is false, NetBox's API true"},
		{"a field nullable that isn't", func(s specs) { field(s, "devicetype", "model").Nullable = true }, "dcim.devicetype.model: its spec says nullable is true"},
		{"another length", func(s specs) { field(s, "devicetype", "model").MaxLength = 99 }, "its spec says it has at most 99 characters, NetBox's API 100"},
		{"no length", func(s specs) { field(s, "manufacturer", "description").MaxLength = 0 }, "at most 0 characters, NetBox's API 200"},
		{"another pattern", func(s specs) { field(s, "devicetype", "slug").Pattern = "[-a-z0-9_]+" }, `its spec's pattern is "^[-a-z0-9_]+$", NetBox's API's "^[-a-zA-Z0-9_]+$"`},
		{"no pattern", func(s specs) { field(s, "manufacturer", "slug").Pattern = "" }, `its spec's pattern is "", NetBox's API's`},
		{"a blank field not", func(s specs) { field(s, "devicetype", "description").Blank = false }, "dcim.devicetype.description: its spec says blank is false, NetBox's API true"},
		{"a field blank that isn't", func(s specs) { field(s, "manufacturer", "name").Blank = true }, "dcim.manufacturer.name: its spec says blank is true, NetBox's API false"},
		{"a decimal as a string", func(s specs) { field(s, "devicetype", "u_height").Kind = model.KindString }, "dcim.devicetype.u_height: its spec says it is a string, but NetBox's API gives number"},
		{"a string as a boolean", func(s specs) { field(s, "devicetype", "model").Kind = model.KindBoolean }, "its spec says it is boolean, but NetBox's API gives string"},
		{"a string as a date", func(s specs) { field(s, "devicetype", "part_number").Kind = model.KindDate }, "its spec says it is date, but NetBox's API gives string"},
		{"a decimal as an integer", func(s specs) { field(s, "devicetype", "weight").Kind = model.KindInteger }, "its spec says it is integer, but NetBox's API gives number"},
		{"a date as a string", func(s specs) { field(s, "devicetype", "end_of_life").Kind = model.KindString }, "its spec says it is a string, but NetBox's API gives string date"},
		{"more digits", func(s specs) { field(s, "devicetype", "u_height").Digits = 5 }, "dcim.devicetype.u_height: its spec's maximum is 10000 (exclusive true), NetBox's API's 1000"},
		{"a looser minimum", func(s specs) { field(s, "devicetype", "u_height").Min = model.NewDecimal(-1, 0) }, "its spec's minimum is -1 (exclusive false), NetBox's API's 0.0 (exclusive false)"},
		{"a tighter minimum without a source", func(s specs) { field(s, "rearporttemplate", "positions").Min = model.Integer(2) }, "dcim.rearporttemplate.positions: its spec's minimum 2 is tighter than NetBox's API's 1, so it needs TighterBounds"},
		{"a tighter minimum whose source is gone", func(s specs) { field(s, "devicetype", "weight").TighterBounds = model.Source{} }, "dcim.devicetype.weight: its spec's minimum 0 is tighter than NetBox's API's -1000000"},
		// A Source for something else doesn't excuse a tighter bound.
		{"a tighter minimum with another source", func(s specs) { field(s, "devicetype", "u_height").Min = model.NewDecimal(10, 0) }, "dcim.devicetype.u_height: its spec's minimum 10 is tighter than NetBox's API's 0.0, so it needs TighterBounds"},
		{"tighter bounds that aren't", func(s specs) {
			f := field(s, "frontporttemplate", "positions")
			f.TighterBounds = model.Source{File: "dcim/models/device_component_templates.py", Symbol: "FrontPortTemplate"}
		}, "dcim.frontporttemplate.positions: it cites TighterBounds, but its bounds are no tighter than NetBox's API's"},
		{"an exclusive bound made inclusive", func(s specs) { field(s, "devicetype", "weight").Max = model.NewDecimal(1000000, 0) }, "dcim.devicetype.weight: its spec's maximum is 1000000 (exclusive false), NetBox's API's 1000000 (exclusive true)"},
		{"no minimum", func(s specs) { field(s, "devicetype", "u_height").Min = nil }, "its spec's minimum is -1000 (exclusive true), NetBox's API's 0.0 (exclusive false)"},
		{"fewer places", func(s specs) { field(s, "devicetype", "weight").Places = 1 }, "dcim.devicetype.weight: its spec's maximum is 10000000"},
		{"a missing choice", func(s specs) {
			c := field(s, "devicetype", "airflow").Choices
			c.Values = c.Values[1:]
		}, "dcim.devicetype.airflow: its choices are"},
		{"a choice not blank", func(s specs) {
			f := field(s, "devicetype", "airflow")
			f.Blank, f.BlankIsNull = false, false
		}, "dcim.devicetype.airflow: its spec says blank is false, NetBox's API true"},
		{"a choice as a string", func(s specs) {
			f := field(s, "devicetype", "airflow")
			f.Kind, f.Choices, f.BlankIsNull = model.KindString, nil, false
		}, "dcim.devicetype.airflow: its spec says it is a string, but NetBox's API gives object"},
		{"a reference to another type", func(s specs) {
			field(s, "inventoryitemtemplate", "manufacturer").Ref.Types = []string{"dcim.devicetype"}
		}, "it refers to a dcim.devicetype, which NetBox's API gives as BriefDeviceType, but it gives BriefManufacturer"},
		{"a nested reference as an ID", func(s specs) { field(s, "devicetype", "manufacturer").Ref.Shape = model.ShapeID }, "dcim.devicetype.manufacturer: its spec says NetBox's API carries it as an ID, but it gives object"},
		{"an ID reference as nested", func(s specs) { field(s, "porttemplatemapping", "rear_port").Ref.Shape = model.ShapeNested }, "dcim.porttemplatemapping.rear_port: its spec says it refers to a dcim.rearporttemplate"},
		{"another default", func(s specs) { field(s, "devicetype", "u_height").Default = model.NewDecimal(2, 0) }, "dcim.devicetype.u_height: its spec's default is 2, NetBox's API's 1"},
		{"another integer default", func(s specs) { field(s, "porttemplatemapping", "rear_port_position").Default = model.Integer(2) }, "rear_port_position: its spec's default is 2, NetBox's API's 1"},
		{"another integer bound", func(s specs) { field(s, "frontporttemplate", "positions").Max = model.Integer(2048) }, "dcim.frontporttemplate.positions: its spec's maximum is 2048"},
		{"no integer bound", func(s specs) { field(s, "rearporttemplate", "positions").Min = nil }, "dcim.rearporttemplate.positions: its spec's minimum is none, NetBox's API's 1"},
		{"a bound NetBox hasn't got", func(s specs) { field(s, "porttemplatemapping", "position").Min = model.Integer(1) }, "dcim.porttemplatemapping.position: its spec's minimum is 1, NetBox's API's none"},
		{"a colour that isn't blank", func(s specs) { field(s, "frontporttemplate", "color").Blank = false }, "dcim.frontporttemplate.color: its spec says blank is false, NetBox's API true"},
		{"a generic reference's fields misnamed", func(s specs) { field(s, "inventoryitemtemplate", "component").Ref.TypeField = "component_kind" }, "component (component_kind): NetBox's API doesn't give it"},
		{"a generic reference that isn't", func(s specs) {
			f := field(s, "inventoryitemtemplate", "manufacturer")
			f.Ref.Shape, f.Ref.TypeField, f.Ref.IDField = model.ShapeTypeAndID, "manufacturer_type", "manufacturer_id"
		}, "dcim.inventoryitemtemplate.manufacturer: NetBox's API gives it as a field it writes too"},
		{"a nested collection NetBox hasn't got", func(s specs) { s["dcim.frontporttemplate"].Children[0].Name = "mappings" }, "dcim.frontporttemplate.mappings: NetBox's API doesn't carry the collection in mappings"},
		{"a nested child's field", func(s specs) { field(s, "porttemplatemapping", "position").Name = "front_port_position" }, "dcim.porttemplatemapping.front_port_position: NetBox's API doesn't give it"},
		{"a set of the wrong references", func(s specs) {
			d := s["dcim.devicetype"]
			d.Ignored = slices.DeleteFunc(d.Ignored, func(ig model.Ignored) bool { return ig.Name == "tags" })
			d.Fields = append(d.Fields, model.Field{
				Name: "tags", Kind: model.KindSet, Default: model.Set{},
				Elem: &model.Field{Kind: model.KindReference, Ref: &model.Ref{Types: []string{"dcim.manufacturer"}}},
			})
		}, "dcim.devicetype.tags[]: its spec says it refers to a dcim.manufacturer, which NetBox's API gives as BriefManufacturer, but it gives NestedTag"},
		{"a set that isn't", func(s specs) {
			f := field(s, "devicetype", "comments")
			f.Kind, f.Trim, f.Blank, f.MaxLength, f.Default, f.Elem = model.KindSet, false, false, 0, model.Set{}, &model.Field{Kind: model.KindString}
		}, "dcim.devicetype.comments: its spec says it is a set, but NetBox's API gives string and takes string"},
		// Read-only fields have no request property, which once crashed
		// Contract when it described what NetBox's API takes.
		{"a read-only set that isn't", func(s specs) {
			d := s["dcim.devicetype"]
			d.Ignored = slices.DeleteFunc(d.Ignored, func(ig model.Ignored) bool { return ig.Name == "device_count" })
			d.Fields = append(d.Fields, model.Field{Name: "device_count", Kind: model.KindSet, ReadOnly: true, Elem: &model.Field{Kind: model.KindString}})
		}, "dcim.devicetype.device_count: its spec says it is a set, but NetBox's API gives integer and takes nothing"},
		{"a read-only ID reference that isn't", func(s specs) {
			d := s["dcim.inventoryitemtemplate"]
			f := d.Field("component")
			f.ReadOnly, f.Default, f.Source = true, nil, model.Source{}
			f.Ref = &model.Ref{Types: []string{"dcim.frontporttemplate"}, Within: "dcim.devicetype", Shape: model.ShapeID}
			d.Ignored = append(d.Ignored, model.Ignored{Name: "component_type", Reason: "x"}, model.Ignored{Name: "component_id", Reason: "x"})
		}, "dcim.inventoryitemtemplate.component: its spec says NetBox's API carries it as an ID, but it gives a schema with no type and takes nothing"},
		{"an endpoint NetBox hasn't got", func(s specs) { s["dcim.manufacturer"].Endpoint = "/api/dcim/makers/" }, "dcim.manufacturer: the schema has no GET /api/dcim/makers/"},
	}
	for _, tt := range tests {
		s := cloneSpecs(withPrototypes())
		tt.breaks(s)
		err := spectest.Contract(doc, listOf(s))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Contract = %v\nwant a problem with %q", tt.name, err, tt.want)
		}
	}
	if err := spectest.Contract(doc, listOf(cloneSpecs(withPrototypes()))); err != nil {
		t.Errorf("Contract of cloned specs: %v", err)
	}
}

// TestSourcesFinds checks that a citation NetBox's source doesn't bear out
// is reported.
func TestSourcesFinds(t *testing.T) {
	t.Parallel()
	root, err := spectest.ReferenceSource(spectest.Release(loadSnapshot(t)))
	switch {
	case errors.Is(err, spectest.ErrNoReference):
		t.Skip(err)
	case err != nil:
		t.Fatal(err)
	}
	type specs = map[string]*model.Spec
	cite := func(src model.Source) func(s specs) {
		return func(s specs) { s["dcim.devicetype"].Sources = append(s["dcim.devicetype"].Sources, src) }
	}
	tests := []struct {
		name   string
		breaks func(s specs)
		want   string
	}{
		{"a class it hasn't got", cite(model.Source{File: "dcim/models/devices.py", Symbol: "DeviceKind"}), "doesn't define DeviceKind"},
		{"a method it hasn't got", cite(model.Source{File: "dcim/models/devices.py", Symbol: "DeviceType.validate"}), "doesn't define validate"},
		// Manufacturer has no clean method, though DeviceType and Device,
		// in the same file, have.
		{"a method of another class", cite(model.Source{File: "dcim/models/devices.py", Symbol: "Manufacturer.clean"}), "dcim/models/devices.py doesn't define clean where the citation says"},
		{"a file it hasn't got", cite(model.Source{File: "dcim/models/racks2.py", Symbol: "Rack"}), "cites Rack in dcim/models/racks2.py"},
		{"a file outside it", cite(model.Source{File: "../../../go.mod", Symbol: "module"}), "cites module in ../../../go.mod"},
		{
			"a configurable choice that isn't marked", func(s specs) { s["dcim.devicetype"].Field("airflow").Choices.Configurable = "" },
			`dcim.devicetype.airflow: its FIELD_CHOICES key is "", but DeviceAirflowChoices's is "dcim.Device.airflow"`,
		},
		{
			"a choice marked configurable that isn't", func(s specs) {
				s["dcim.devicetype"].Field("cooling_method").Choices.Configurable = "dcim.Device.cooling_method"
			},
			`dcim.devicetype.cooling_method: its FIELD_CHOICES key is "dcim.Device.cooling_method", but CoolingMethodChoices's is ""`,
		},
		{
			"another FIELD_CHOICES key", func(s specs) { s["dcim.devicetype"].Field("airflow").Choices.Configurable = "dcim.DeviceType.airflow" },
			`its FIELD_CHOICES key is "dcim.DeviceType.airflow", but DeviceAirflowChoices's is "dcim.Device.airflow"`,
		},
		{"a choice field citing something else", func(s specs) {
			s["dcim.devicetype"].Field("subdevice_role").Source = model.Source{File: "dcim/models/devices.py", Symbol: "DeviceType"}
		}, "dcim.devicetype.subdevice_role: it cites DeviceType, which isn't a choice set"},
	}
	for _, tt := range tests {
		s := cloneSpecs(withPrototypes())
		tt.breaks(s)
		err := spectest.Sources(root, listOf(s))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Sources = %v, want a problem with %q", tt.name, err, tt.want)
		}
	}
	if err := spectest.Sources(root, withPrototypes()); err != nil {
		t.Errorf("Sources of the specs: %v", err)
	}
}

// TestContractFindsNetBoxChanges changes the schema the way a new NetBox
// release might, one way at a time, and checks that spectest.Contract
// reports each against the unchanged specs.
func TestContractFindsNetBoxChanges(t *testing.T) {
	t.Parallel()
	type doc = *openapi.Document
	prop := func(d doc, component, name string) *openapi.Schema {
		return d.Components.Schemas[component].Properties[name]
	}
	tests := []struct {
		name    string
		changes func(d doc)
		want    string
	}{
		{"a new field", func(d doc) {
			d.Components.Schemas["DeviceType"].Properties["colour"] = &openapi.Schema{Type: "string"}
		}, "dcim.devicetype: NetBox's API has colour, which the spec neither describes nor ignores"},
		{"a new field it only takes", func(d doc) {
			d.Components.Schemas["ManufacturerRequest"].Properties["logo"] = &openapi.Schema{Type: "string"}
		}, "dcim.manufacturer: NetBox's API has logo, which the spec neither describes nor ignores"},
		{"a field gone", func(d doc) {
			delete(d.Components.Schemas["DeviceType"].Properties, "part_number")
		}, "dcim.devicetype.part_number: NetBox's API doesn't give it"},
		{"a field no longer written", func(d doc) {
			delete(d.Components.Schemas["PatchedWritableDeviceTypeRequest"].Properties, "comments")
		}, "dcim.devicetype.comments: it is writable, but NetBox's API doesn't take it when creating and patching"},
		{"a field now required", func(d doc) {
			r := d.Components.Schemas["WritableDeviceTypeRequest"]
			r.Required = append(r.Required, "part_number")
		}, "dcim.devicetype.part_number: its spec says required is false, NetBox's API true"},
		{"a longer field", func(d doc) {
			n := 150
			prop(d, "DeviceType", "model").MaxLength = &n
		}, "dcim.devicetype.model: its spec says it has at most 100 characters, NetBox's API 150"},
		{"a new choice", func(d doc) {
			v := prop(d, "DeviceType", "cooling_method").Properties["value"]
			v.Enum = append(v.Enum, []byte(`"oil"`))
		}, "dcim.devicetype.cooling_method: its choices are [air liquid hybrid immersion], NetBox's API's [air liquid hybrid immersion oil]"},
		{"a choice taken but not given", func(d doc) {
			w := prop(d, "WritableDeviceTypeRequest", "cooling_method")
			w.Enum = append(w.Enum, []byte(`"oil"`))
		}, "NetBox's API takes the choices [air liquid hybrid immersion oil] but gives [air liquid hybrid immersion]"},
		{"a number taken as a string", func(d doc) {
			prop(d, "WritableDeviceTypeRequest", "u_height").Type = "string"
		}, "dcim.devicetype.u_height: NetBox's API takes it as string double but gives it as number double"},
		{"a date taken as a date-time", func(d doc) {
			prop(d, "WritableDeviceTypeRequest", "end_of_life").Format = "date-time"
		}, "dcim.devicetype.end_of_life: NetBox's API takes it as string date-time but gives it as string date"},
		{"a boolean taken as a string", func(d doc) {
			prop(d, "WritableDeviceTypeRequest", "is_full_depth").Type = "string"
		}, "dcim.devicetype.is_full_depth: NetBox's API takes it as string  but gives it as boolean"},
		{"a reference that takes only an ID", func(d doc) {
			prop(d, "WritableDeviceTypeRequest", "manufacturer").OneOf = []*openapi.Schema{{Type: "integer"}}
		}, "dcim.devicetype.manufacturer: NetBox's API doesn't take it as an ID or a brief Manufacturer"},
		{"another default", func(d doc) {
			prop(d, "DeviceType", "u_height").Default = []byte("2.0")
		}, "dcim.devicetype.u_height: its spec's default is 1, NetBox's API's 2"},
		{"no longer nullable", func(d doc) {
			prop(d, "DeviceType", "end_of_life").Nullable = false
		}, "dcim.devicetype.end_of_life: its spec says nullable is true, NetBox's API false"},
		{"a nested collection gone", func(d doc) {
			delete(d.Components.Schemas["WritableFrontPortTemplateRequest"].Properties, "rear_ports")
		}, "dcim.frontporttemplate.rear_ports: NetBox's API doesn't carry the collection in rear_ports"},
		{"an ignored field now required", func(d doc) {
			r := d.Components.Schemas["WritableDeviceTypeRequest"]
			r.Required = append(r.Required, "default_platform")
		}, "dcim.devicetype: it ignores default_platform, which NetBox's API requires when creating one"},
		{"a nested collection now required", func(d doc) {
			r := d.Components.Schemas["WritableFrontPortTemplateRequest"]
			r.Required = append(r.Required, "rear_ports")
		}, "dcim.frontporttemplate.rear_ports: NetBox's API requires the collection when creating one"},
		{
			"a nested collection now nullable", func(d doc) { prop(d, "WritableFrontPortTemplateRequest", "rear_ports").Nullable = true },
			"dcim.frontporttemplate.rear_ports: NetBox's API carries the collection in a field that is nullable or read-only",
		},
		{
			"a nested collection no longer a list", func(d doc) { prop(d, "FrontPortTemplate", "rear_ports").Type = "object" },
			"dcim.frontporttemplate.rear_ports: NetBox's API carries the collection as a object, not a list",
		},
		{
			"a string taken as a number", func(d doc) { prop(d, "WritableDeviceTypeRequest", "model").Type = "integer" },
			"dcim.devicetype.model: NetBox's API gives it as a string but takes it as integer",
		},
		{"a string taken longer", func(d doc) {
			n := 99
			prop(d, "WritableDeviceTypeRequest", "model").MaxLength = &n
		}, "dcim.devicetype.model: NetBox's API takes it with at most 99 characters"},
		{
			"a generic reference's type taken as a number", func(d doc) { prop(d, "InventoryItemTemplateRequest", "component_type").Type = "integer" },
			"dcim.inventoryitemtemplate.component: NetBox's API takes component_type as integer, not string",
		},
		{"a field only patching takes", func(d doc) {
			d.Components.Schemas["PatchedWritableDeviceTypeRequest"].Properties["colour"] = &openapi.Schema{Type: "string"}
		}, "dcim.devicetype: NetBox's API has colour, which the spec neither describes nor ignores"},
		{"a nested child's new field", func(d doc) {
			d.Components.Schemas["FrontPortTemplateMapping"].Properties["cable"] = &openapi.Schema{Type: "integer"}
		}, "dcim.porttemplatemapping: NetBox's API has cable, which the spec neither describes nor ignores"},
	}
	// A field only patching takes can be ignored.
	d := loadSnapshot(t)
	d.Components.Schemas["PatchedWritableDeviceTypeRequest"].Properties["colour"] = &openapi.Schema{Type: "string"}
	s := cloneSpecs(withPrototypes())
	s["dcim.devicetype"].Ignored = append(s["dcim.devicetype"].Ignored, model.Ignored{Name: "colour", Reason: "only patched"})
	if err := spectest.Contract(d, listOf(s)); err != nil {
		t.Errorf("Contract with a field only patching takes, ignored: %v", err)
	}
	// A null default, where the spec's is null too, agrees.
	d = loadSnapshot(t)
	prop(d, "DeviceType", "end_of_life").Default = []byte("null")
	if err := spectest.Contract(d, withPrototypes()); err != nil {
		t.Errorf("Contract with a null default for end_of_life: %v", err)
	}
	for _, tt := range tests {
		d := loadSnapshot(t)
		tt.changes(d)
		err := spectest.Contract(d, withPrototypes())
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Contract = %v\nwant a problem with %q", tt.name, err, tt.want)
		}
	}
}
