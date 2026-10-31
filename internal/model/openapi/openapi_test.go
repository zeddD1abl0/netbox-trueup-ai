package openapi

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// fixture is a small schema shaped like NetBox's: a list endpoint with its
// page, response and request components, a nullable reference wrapped in
// allOf, a choice field, and a component nothing uses.
const fixture = `{
  "openapi": "3.0.3",
  "info": {"title": "NetBox REST API", "version": "4.7.2-Docker-5.1.1 (4.7)"},
  "paths": {
    "/api/test/things/": {
      "get": {
        "operationId": "things_list",
        "parameters": [{"in": "query", "name": "q"}],
        "responses": {
          "200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/PaginatedThingList"}}}},
          "403": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}}}
        }
      },
      "post": {
        "requestBody": {"content": {"application/json": {"schema": {"oneOf": [
          {"$ref": "#/components/schemas/WritableThingRequest"},
          {"type": "array", "items": {"$ref": "#/components/schemas/WritableThingRequest"}}
        ]}}}},
        "responses": {"201": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Thing"}}}}}
      },
      "put": {"requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Bulk"}}}}},
      "delete": {"responses": {"204": {}}}
    },
    "/api/test/things/{id}/": {
      "patch": {"requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/PatchedWritableThingRequest"}}}}}
    },
    "/api/test/others/": {"get": {"responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Unused"}}}}}}}
  },
  "components": {"schemas": {
    "PaginatedThingList": {"type": "object", "properties": {"count": {"type": "integer"}, "results": {"type": "array", "items": {"$ref": "#/components/schemas/Thing"}}}},
    "Thing": {"type": "object", "properties": {
      "id": {"type": "integer", "readOnly": true, "maximum": 9223372036854775807},
      "maker": {"allOf": [{"$ref": "#/components/schemas/BriefMaker"}], "nullable": true},
      "height": {"type": "number", "format": "double", "minimum": 0.0, "maximum": 1000, "exclusiveMaximum": true, "default": 1.0},
      "role": {"type": "object", "properties": {"value": {"enum": ["parent", "child", "", null], "type": "string"}, "label": {"type": "string", "enum": ["Parent", "Child"]}}, "nullable": true},
      "note": {"type": "string", "description": "<b>bold</b> & more"}
    }},
    "BriefMaker": {"type": "object", "readOnly": true, "properties": {"id": {"type": "integer"}, "role": {"type": "object", "properties": {"value": {"enum": ["x"]}}}}},
    "WritableThingRequest": {"type": "object", "properties": {"role": {"enum": ["parent", "child", "", null], "type": "string", "nullable": true}}, "required": ["role"]},
    "PatchedWritableThingRequest": {"type": "object", "properties": {"role": {"enum": ["parent", "child", "", null], "type": "string", "nullable": true}}},
    "Bulk": {"type": "object"},
    "Error": {"type": "object"},
    "Unused": {"type": "object"}
  }}
}`

func load(t *testing.T, data string) *Document {
	t.Helper()
	d, err := Load([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestLoad(t *testing.T) {
	t.Parallel()
	if d := load(t, fixture); d.Info.Version != "4.7.2-Docker-5.1.1 (4.7)" || len(d.Components.Schemas) != 8 {
		t.Errorf("Load read version %q and %d components", d.Info.Version, len(d.Components.Schemas))
	}
	for _, bad := range []string{`{`, `[]`, `{"openapi": "3.1.0"}`, `{"openapi": 3}`, `{}`} {
		if _, err := Load([]byte(bad)); err == nil {
			t.Errorf("Load(%s) gave no error", bad)
		}
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	d := load(t, fixture)
	s, name, err := d.Resolve(&Schema{Ref: "#/components/schemas/Thing"})
	if err != nil || name != "Thing" || s.Properties["id"] == nil {
		t.Errorf("Resolve(Thing) = %v, %q, %v", s, name, err)
	}
	if s, name, err := d.Resolve(&Schema{Type: "string"}); err != nil || name != "" || s.Type != "string" {
		t.Errorf("Resolve of an inline schema = %v, %q, %v", s, name, err)
	}
	// A chain of exactly maxRefs $refs resolves; one more doesn't.
	chain := func(n int) *Schema {
		d.Components.Schemas[fmt.Sprintf("C%d", n)] = &Schema{Type: "string"}
		for i := n - 1; i >= 1; i-- {
			d.Components.Schemas[fmt.Sprintf("C%d", i)] = &Schema{Ref: fmt.Sprintf("#/components/schemas/C%d", i+1)}
		}
		return &Schema{Ref: "#/components/schemas/C1"}
	}
	if s, name, err := d.Resolve(chain(maxRefs)); err != nil || s.Type != "string" || name != fmt.Sprintf("C%d", maxRefs) {
		t.Errorf("Resolve of a chain of %d $refs = %v, %q, %v", maxRefs, s, name, err)
	}
	if _, _, err := d.Resolve(chain(maxRefs + 1)); err == nil {
		t.Errorf("Resolve of a chain of %d $refs gave no error", maxRefs+1)
	}
	d.Components.Schemas["A"] = &Schema{Ref: "#/components/schemas/B"}
	d.Components.Schemas["B"] = &Schema{Ref: "#/components/schemas/A"}
	for _, ref := range []string{"#/components/schemas/Missing", "other.json#/Thing", "#/paths/x", "#/components/schemas/A"} {
		if _, _, err := d.Resolve(&Schema{Ref: ref}); err == nil {
			t.Errorf("Resolve(%s) gave no error", ref)
		}
	}
	if _, _, err := d.Resolve(nil); err == nil {
		t.Errorf("Resolve(nil) gave no error")
	}
}

func TestUnwrap(t *testing.T) {
	t.Parallel()
	d := load(t, fixture)
	thing := d.Components.Schemas["Thing"]
	// drf-spectacular puts a nullable reference's nullable beside an allOf,
	// not in the component, which a first prototype missed.
	p, err := d.Unwrap(thing.Properties["maker"])
	if err != nil || p.Component != "BriefMaker" || !p.Nullable || !p.ReadOnly || p.Type != "object" {
		t.Errorf("Unwrap(maker) = %+v, %v; want BriefMaker, nullable, read-only from the component", p, err)
	}
	p, err = d.Unwrap(thing.Properties["role"])
	if err != nil || p.Component != "" || !p.Nullable || p.ReadOnly {
		t.Errorf("Unwrap(role) = %+v, %v", p, err)
	}
	if _, err := d.Unwrap(&Schema{AllOf: []*Schema{{Ref: "#/components/schemas/Missing"}}}); err == nil {
		t.Errorf("Unwrap of a broken allOf gave no error")
	}
	if _, err := d.Unwrap(nil); err == nil {
		t.Errorf("Unwrap(nil) gave no error")
	}
}

func TestEndpoint(t *testing.T) {
	t.Parallel()
	e, err := load(t, fixture).Endpoint("/api/test/things/")
	if err != nil {
		t.Fatal(err)
	}
	got := []string{e.ResponseName, e.RequestName, e.PatchName}
	if diff := cmp.Diff([]string{"Thing", "WritableThingRequest", "PatchedWritableThingRequest"}, got); diff != "" {
		t.Errorf("Endpoint components (-want +got):\n%s", diff)
	}
	if !slices.Equal(e.Request.Required, []string{"role"}) {
		t.Errorf("the request component's required = %v", e.Request.Required)
	}

	breaks := map[string]func(d *Document){
		"no endpoint": func(d *Document) { delete(d.Paths, "/api/test/things/") },
		"no list":     func(d *Document) { delete(d.Paths["/api/test/things/"], "get") },
		"no results":  func(d *Document) { delete(d.Components.Schemas["PaginatedThingList"].Properties, "results") },
		"no create":   func(d *Document) { delete(d.Paths["/api/test/things/"], "post") },
		"one at a time": func(d *Document) {
			body(d, "/api/test/things/", "post").OneOf = body(d, "/api/test/things/", "post").OneOf[:1]
		},
		"no patch":        func(d *Document) { delete(d.Paths, "/api/test/things/{id}/") },
		"no JSON":         func(d *Document) { delete(d.Paths["/api/test/things/"]["get"].Responses, "200") },
		"a broken result": func(d *Document) { delete(d.Components.Schemas, "Thing") },
	}
	for name, breakIt := range breaks {
		d := load(t, fixture)
		breakIt(d)
		if _, err := d.Endpoint("/api/test/things/"); err == nil {
			t.Errorf("%s: Endpoint gave no error", name)
		}
	}
}

func body(d *Document, path, method string) *Schema {
	return d.Paths[path][method].RequestBody.Content["application/json"].Schema
}

func TestEnumValues(t *testing.T) {
	t.Parallel()
	values, blank, null, err := EnumValues(&Schema{Enum: []json.RawMessage{[]byte(`"a"`), []byte(`""`), []byte(` null`), []byte(`"b"`)}})
	if err != nil || !slices.Equal(values, []string{"a", "b"}) || !blank || !null {
		t.Errorf("EnumValues = %v, %t, %t, %v", values, blank, null, err)
	}
	values, blank, null, err = EnumValues(&Schema{Enum: []json.RawMessage{[]byte(`"a"`)}})
	if err != nil || !slices.Equal(values, []string{"a"}) || blank || null {
		t.Errorf("EnumValues without blank or null = %v, %t, %t, %v", values, blank, null, err)
	}
	if _, _, _, err := EnumValues(&Schema{Enum: []json.RawMessage{[]byte(`10`)}}); err == nil {
		t.Errorf("EnumValues of a number gave no error")
	}
}

func TestChoices(t *testing.T) {
	t.Parallel()
	sets, err := load(t, fixture).Choices()
	if err != nil {
		t.Fatal(err)
	}
	// BriefMaker's role is left out: brief components repeat others.
	want := []ChoiceSet{{Key: "Thing.role", Values: []string{"parent", "child"}}}
	if diff := cmp.Diff(want, sets); diff != "" {
		t.Errorf("Choices (-want +got):\n%s", diff)
	}
	for _, name := range []string{"BriefX", "NestedX", "PaginatedXList", "PatchedXRequest", "WritableXRequest", "XRequest"} {
		if isResponse(name) {
			t.Errorf("isResponse(%s) = true", name)
		}
	}
	if !isResponse("DeviceType") || !isResponse("FrontPortTemplateMapping") {
		t.Errorf("isResponse refuses a response component")
	}
}

func TestTrim(t *testing.T) {
	t.Parallel()
	trimmed, err := Trim([]byte(fixture), []string{"/api/test/things/"})
	if err != nil {
		t.Fatal(err)
	}
	d := load(t, string(trimmed))
	var paths, schemas []string
	for p, ops := range d.Paths {
		for m := range ops {
			paths = append(paths, m+" "+p)
		}
	}
	for s := range d.Components.Schemas {
		schemas = append(schemas, s)
	}
	slices.Sort(paths)
	slices.Sort(schemas)
	// Only what Endpoint reads is kept: no PUT or DELETE, no error
	// responses, and no component only those used.
	if diff := cmp.Diff([]string{"get /api/test/things/", "patch /api/test/things/{id}/", "post /api/test/things/"}, paths); diff != "" {
		t.Errorf("Trim kept these paths (-want +got):\n%s", diff)
	}
	want := []string{"BriefMaker", "PaginatedThingList", "PatchedWritableThingRequest", "Thing", "WritableThingRequest"}
	if diff := cmp.Diff(want, schemas); diff != "" {
		t.Errorf("Trim kept these components (-want +got):\n%s", diff)
	}
	if _, err := d.Endpoint("/api/test/things/"); err != nil {
		t.Errorf("Endpoint in the trimmed schema: %v", err)
	}
	if d.Paths["/api/test/things/"]["get"].Responses["403"].Content != nil {
		t.Errorf("Trim kept an error response")
	}
	text := string(trimmed)
	for _, kept := range []string{`"maximum": 9223372036854775807`, `"minimum": 0.0`, `"default": 1.0`, `<b>bold</b> & more`, "\n  \"components\": {"} {
		if !strings.Contains(text, kept) {
			t.Errorf("the trimmed schema hasn't got %q as written", kept)
		}
	}
	if strings.Contains(text, "operationId") || strings.Contains(text, "parameters") {
		t.Errorf("Trim kept an operation's ID or parameters")
	}
	again, err := Trim(trimmed, []string{"/api/test/things/"})
	if err != nil || string(again) != text {
		t.Errorf("trimming a trimmed schema changed it: %v", err)
	}

	if _, err := Trim([]byte(fixture), []string{"/api/test/missing/"}); err == nil {
		t.Errorf("Trim of an endpoint the schema hasn't got gave no error")
	}
	dangling := strings.Replace(fixture, `"BriefMaker": {`, `"BriefMakerX": {`, 1)
	if _, err := Trim([]byte(dangling), []string{"/api/test/things/"}); err == nil {
		t.Errorf("Trim of a schema with a dangling $ref gave no error")
	}
	if _, err := Trim([]byte("{"), nil); err == nil {
		t.Errorf("Trim of broken JSON gave no error")
	}
}
