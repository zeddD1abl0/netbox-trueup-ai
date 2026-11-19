package format

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/dcim"
)

func TestDefinitionJSON(t *testing.T) {
	t.Parallel()
	specs := dcim.Specs()
	acme := model.Reference{Type: "dcim.manufacturer", Key: map[string]model.Value{"name": model.String("A&B <Acme>")}}
	tests := []struct {
		name string
		def  Definition
		want string
	}{
		{
			name: "derived manufacturer",
			def: Definition{
				Object:      model.Object{Type: "dcim.manufacturer", Fields: map[string]model.Value{"name": model.String("A&B <Acme>"), "slug": model.String("ab-acme")}},
				Identity:    map[string]model.Value{"name": model.String("A&B <Acme>")},
				DerivedFrom: []string{"device-types/Acme/a.yaml", "device-types/Acme/b.yaml"},
				Derived:     []string{"slug"},
			},
			// <, > and & as they are, as list has always written them.
			want: `{"type":"dcim.manufacturer","identity":{"name":"A&B <Acme>"},"path":null,` +
				`"derived_from":["device-types/Acme/a.yaml","device-types/Acme/b.yaml"],"fields":{"name":"A&B <Acme>","slug":"ab-acme"},` +
				`"derived":["slug"],"metadata":{},"unmapped":{}}`,
		},
		{
			name: "device type",
			def: Definition{
				Object: model.Object{Type: "dcim.devicetype", Fields: map[string]model.Value{
					"manufacturer": acme, "model": model.String("Box 1"), "u_height": model.NewDecimal(15, 1), "airflow": model.Choice("front-to-rear"),
					"power_ports": model.Children{{Type: "dcim.powerporttemplate", Fields: map[string]model.Value{"name": model.String("PSU1"), "maximum_draw": model.Integer(500)}}},
					"power_outlets": model.Children{{Type: "dcim.poweroutlettemplate", Fields: map[string]model.Value{
						"name":       model.String("O1"),
						"power_port": model.Reference{Type: "dcim.powerporttemplate", Key: map[string]model.Value{"name": model.String("PSU1")}},
					}}},
				}},
				Identity: map[string]model.Value{"manufacturer": acme, "model": model.String("Box 1")},
				Path:     "device-types/Acme/box-1.yaml",
				Metadata: map[string]model.Value{"is_powered": model.Boolean(true), "note": model.String("x"), "none": model.Null{}, "count": model.Integer(2), "ratio": model.NewDecimal(5, 1)},
				Unmapped: map[string]int{"module-bays": 2},
			},
			// A reference, like children, escapes them in its own JSON.
			want: `{"type":"dcim.devicetype","identity":{"manufacturer":{"name":"A\u0026B \u003cAcme\u003e"},"model":"Box 1"},"path":"device-types/Acme/box-1.yaml",` +
				`"derived_from":[],"fields":{"airflow":"front-to-rear","manufacturer":{"name":"A\u0026B \u003cAcme\u003e"},"model":"Box 1",` +
				`"power_outlets":[{"name":"O1","power_port":{"name":"PSU1"}}],"power_ports":[{"maximum_draw":500,"name":"PSU1"}],"u_height":1.5},` +
				`"derived":[],"metadata":{"count":2,"is_powered":true,"none":null,"note":"x","ratio":0.5},"unmapped":{"module-bays":2}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := EncodeDefinition(specs, tt.def)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, string(got)); diff != "" {
				t.Errorf("EncodeDefinition (-want +got):\n%s", diff)
			}
			back, err := UnmarshalDefinition(specs, got)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.def, back, cmp.AllowUnexported(model.Decimal{}, model.Date{}), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("UnmarshalDefinition (-want +got):\n%s", diff)
			}
		})
	}
}

func TestEncodeDefinitionRefusesWhatDoesntReadBack(t *testing.T) {
	t.Parallel()
	specs := dcim.Specs()
	base := func() Definition {
		return Definition{
			Object:   model.Object{Type: "dcim.manufacturer", Fields: map[string]model.Value{"name": model.String("Acme")}},
			Identity: map[string]model.Value{"name": model.String("Acme")},
			Path:     "manufacturers/acme.yaml",
		}
	}
	for _, tt := range []struct {
		name   string
		change func(*Definition)
		want   string
	}{
		// A whole decimal writes as a whole number, which reads back as an
		// integer.
		{"whole decimal metadata", func(d *Definition) { d.Metadata = map[string]model.Value{"n": model.NewDecimal(10, 1)} }, "doesn't read back as it was written"},
		{"list metadata", func(d *Definition) { d.Metadata = map[string]model.Value{"n": model.Set{}} }, "metadata: n: is a list or an object"},
		{"field of another kind", func(d *Definition) { d.Object.Fields["name"] = model.Integer(1) }, "fields: name: is a number, not text"},
		{"field no spec has", func(d *Definition) { d.Object.Fields["colour"] = model.String("red") }, "fields: colour: isn't a field of a dcim.manufacturer"},
		{"type without a spec", func(d *Definition) { d.Object.Type = "dcim.thing" }, `a definition is of type "dcim.thing", which has no spec`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := base()
			tt.change(&d)
			_, err := EncodeDefinition(specs, d)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("EncodeDefinition = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

func TestUnmarshalDefinitionRefuses(t *testing.T) {
	t.Parallel()
	specs := dcim.Specs()
	const valid = `{"type":"dcim.manufacturer","identity":{"name":"Acme"},"path":null,"derived_from":["a.yaml"],` +
		`"fields":{"name":"Acme","slug":"acme"},"derived":["slug"],"metadata":{},"unmapped":{}}`
	if _, err := UnmarshalDefinition(specs, []byte(valid)); err != nil {
		t.Fatalf("UnmarshalDefinition(%s): %v", valid, err)
	}
	replace := func(old, new string) string {
		if !strings.Contains(valid, old) {
			t.Fatalf("%s isn't in the definition", old)
		}
		return strings.Replace(valid, old, new, 1)
	}
	for _, tt := range []struct {
		json, want string
	}{
		{`{`, "a definition isn't valid JSON"},
		{`[]`, "a definition isn't a JSON object"},
		{replace(`"unmapped":{}`, `"unmapped":{},"extra":1`), `a definition has the key "extra", which TrueUp doesn't write`},
		{replace(`,"unmapped":{}`, ``), "a definition has no unmapped"},
		{replace(`"type":"dcim.manufacturer"`, `"type":1`), "a definition's type isn't text"},
		{replace(`"type":"dcim.manufacturer"`, `"type":"dcim.rack"`), `a definition is of type "dcim.rack", which has no spec`},
		{replace(`"identity":{"name":"Acme"}`, `"identity":{}`), "a dcim.manufacturer definition: its identity has [], not [name]"},
		{replace(`"identity":{"name":"Acme"}`, `"identity":{"name":"Acme","slug":"acme"}`), "its identity has [name slug], not [name]"},
		{replace(`"identity":{"name":"Acme"}`, `"identity":{"name":1}`), "identity: name: is a number, not text"},
		{replace(`"fields":{"name":"Acme","slug":"acme"}`, `"fields":[]`), "fields: a dcim.manufacturer is a list, not an object"},
		{replace(`"path":null`, `"path":""`), "its path is neither a file's nor null"},
		{replace(`"path":null`, `"path":1`), "its path is neither a file's nor null"},
		{replace(`"derived_from":["a.yaml"]`, `"derived_from":null`), "its derived_from isn't a list of text"},
		{replace(`"derived":["slug"]`, `"derived":[1]`), "its derived isn't a list of text"},
		{replace(`"metadata":{}`, `"metadata":null`), "its metadata isn't an object"},
		{replace(`"metadata":{}`, `"metadata":{"n":1e999}`), "metadata: n: "},
		{replace(`"unmapped":{}`, `"unmapped":{"a":"b"}`), "its unmapped isn't an object of counts"},
		{replace(`"unmapped":{}`, `"unmapped":null`), "its unmapped isn't an object of counts"},
	} {
		if _, err := UnmarshalDefinition(specs, []byte(tt.json)); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("UnmarshalDefinition(%s) = %v, want an error containing %q", tt.json, err, tt.want)
		}
	}
}
