package spectest

import (
	"strings"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/openapi"
)

// genericSchema is a made-up schema of one object type with a generic
// reference, component, which NetBox's API carries as component_type and
// component_id and gives, read-only, as the object referred to, with no
// type, as it carried an inventory item template's component. No spec
// package has a generic reference yet, so this is where Contract's handling
// of them is tested.
const genericSchema = `{
  "openapi": "3.0.3",
  "info": {"version": "4.7.2"},
  "paths": {
    "/api/test/things/": {
      "get": {"responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/PaginatedThingList"}}}}}},
      "post": {"requestBody": {"content": {"application/json": {"schema": {"oneOf": [
        {"$ref": "#/components/schemas/ThingRequest"},
        {"type": "array", "items": {"$ref": "#/components/schemas/ThingRequest"}}
      ]}}}}}
    },
    "/api/test/things/{id}/": {
      "patch": {"requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/PatchedThingRequest"}}}}}
    }
  },
  "components": {"schemas": {
    "PaginatedThingList": {"type": "object", "properties": {"results": {"type": "array", "items": {"$ref": "#/components/schemas/Thing"}}}},
    "Thing": {"type": "object", "properties": {
      "id": {"type": "integer", "readOnly": true},
      "name": {"type": "string", "maxLength": 64, "minLength": 1},
      "component_type": {"type": "string", "nullable": true},
      "component_id": {"type": "integer", "nullable": true},
      "component": {"nullable": true, "readOnly": true}
    }},
    "ThingRequest": {"type": "object", "required": ["name"], "properties": {
      "name": {"type": "string", "maxLength": 64, "minLength": 1},
      "component_type": {"type": "string", "nullable": true},
      "component_id": {"type": "integer", "nullable": true}
    }},
    "PatchedThingRequest": {"type": "object", "properties": {
      "name": {"type": "string", "maxLength": 64, "minLength": 1},
      "component_type": {"type": "string", "nullable": true},
      "component_id": {"type": "integer", "nullable": true}
    }}
  }}
}`

// genericSpecs returns a spec of genericSchema's object type, and of the
// type its component refers to.
func genericSpecs() []*model.Spec {
	return []*model.Spec{
		{
			Type: "test.part", Endpoint: "", Identity: []string{"name"},
			Fields: []model.Field{{Name: "name", Kind: model.KindString, Required: true}},
		},
		{
			Type:     "test.thing",
			Endpoint: "/api/test/things/",
			Fields: []model.Field{
				{Name: "name", Kind: model.KindString, Required: true, MaxLength: 64},
				{
					Name: "component", Kind: model.KindReference, Nullable: true, Default: model.Null{},
					Ref: &model.Ref{
						Types: []string{"test.part"}, Shape: model.ShapeTypeAndID,
						TypeField: "component_type", IDField: "component_id",
					},
				},
			},
			Ignored: []model.Ignored{{Name: "id", Reason: "its ID"}},
		},
	}
}

func loadGeneric(t *testing.T) *openapi.Document {
	t.Helper()
	d, err := openapi.Load([]byte(genericSchema))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestContractGenericReferences checks that Contract takes a generic
// reference as NetBox's API carries one, and reports each way a spec or the
// schema can disagree about it.
func TestContractGenericReferences(t *testing.T) {
	t.Parallel()
	if err := Contract(loadGeneric(t), genericSpecs()); err != nil {
		t.Fatalf("Contract of a generic reference as NetBox carries it: %v", err)
	}
	field := func(specs []*model.Spec, name string) *model.Field { return specs[1].Field(name) }
	tests := []struct {
		name    string
		breaks  func(s []*model.Spec)
		changes func(d *openapi.Document)
		want    string
	}{
		{
			name:   "its fields misnamed",
			breaks: func(s []*model.Spec) { field(s, "component").Ref.TypeField = "component_kind" },
			want:   "component (component_kind): NetBox's API doesn't give it",
		},
		{
			name: "a field that isn't one",
			breaks: func(s []*model.Spec) {
				f := field(s, "name")
				f.Kind, f.Ref = model.KindReference, &model.Ref{
					Types: []string{"test.part"}, Shape: model.ShapeTypeAndID, TypeField: "component_type", IDField: "component_id",
				}
				s[1].Fields = s[1].Fields[:1]
			},
			want: "test.thing.name: NetBox's API gives it as a field it writes too",
		},
		// Read-only fields have no request property, which once crashed
		// Contract when it described what NetBox's API takes.
		{
			name: "a read-only ID reference that isn't",
			breaks: func(s []*model.Spec) {
				f := field(s, "component")
				f.ReadOnly, f.Default, f.Nullable = true, nil, true
				f.Ref = &model.Ref{Types: []string{"test.part"}, Shape: model.ShapeID}
				s[1].Ignored = append(s[1].Ignored, model.Ignored{Name: "component_type", Reason: "x"}, model.Ignored{Name: "component_id", Reason: "x"})
			},
			want: "test.thing.component: its spec says NetBox's API carries it as an ID, but it gives a schema with no type and takes nothing",
		},
		{
			name: "its type taken as a number",
			changes: func(d *openapi.Document) {
				d.Components.Schemas["ThingRequest"].Properties["component_type"].Type = "integer"
			},
			want: "test.thing.component: NetBox's API takes component_type as integer, not string",
		},
		{
			name: "its ID given as text",
			changes: func(d *openapi.Document) {
				d.Components.Schemas["Thing"].Properties["component_id"].Type = "string"
			},
			want: "test.thing.component: NetBox's API gives component_id as string, not integer",
		},
		{
			name: "itself taken",
			changes: func(d *openapi.Document) {
				d.Components.Schemas["ThingRequest"].Properties["component"] = &openapi.Schema{Type: "integer"}
			},
			want: "test.thing.component: NetBox's API takes it as itself, not as component_type and component_id",
		},
	}
	for _, tt := range tests {
		s, d := genericSpecs(), loadGeneric(t)
		if tt.breaks != nil {
			tt.breaks(s)
		}
		if tt.changes != nil {
			tt.changes(d)
		}
		err := Contract(d, s)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Contract = %v\nwant a problem with %q", tt.name, err, tt.want)
		}
	}
}
