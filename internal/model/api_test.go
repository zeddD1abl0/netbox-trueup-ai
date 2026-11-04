package model

import (
	"strings"
	"testing"
)

// apiSpecs are specs with a field of every kind and a reference of every
// shape, for DecodeAPI.
func apiSpecs() (thing *Spec, all []*Spec) {
	maker := &Spec{
		Type: "test.maker", Endpoint: "/api/test/makers/", Identity: []string{"name"},
		Unique: []Unique{{Fields: []string{"name"}}},
		Fields: []Field{{Name: "name", Kind: KindString, Required: true}},
	}
	thing = &Spec{
		Type: "test.thing", Endpoint: "/api/test/things/",
		Fields: []Field{
			{Name: "name", Kind: KindString},
			{Name: "note", Kind: KindString, Nullable: true},
			{Name: "size", Kind: KindDecimal, Digits: 4, Places: 1},
			{Name: "count", Kind: KindInteger},
			{Name: "ok", Kind: KindBoolean},
			{Name: "airflow", Kind: KindChoice, Nullable: true, Blank: true, BlankIsNull: true},
			{Name: "type", Kind: KindChoice, Blank: true},
			{Name: "eol", Kind: KindDate, Nullable: true},
			{Name: "maker", Kind: KindReference, Ref: &Ref{Types: []string{"test.maker"}}},
			{Name: "parent", Kind: KindReference, Nullable: true, Ref: &Ref{Types: []string{"test.thing"}, Shape: ShapeID}},
			{
				Name: "component", Kind: KindReference, Nullable: true,
				Ref: &Ref{Types: []string{"test.a", "test.b"}, Shape: ShapeTypeAndID, TypeField: "component_type", IDField: "component_id"},
			},
			{Name: "tags", Kind: KindSet, Elem: &Field{Kind: KindString}},
		},
	}
	return thing, []*Spec{maker, thing}
}

// thingAnswer is an answer for apiSpecs' thing, with fields replaced or,
// given as "", removed.
func thingAnswer(replace map[string]string) string {
	fields := []struct{ name, value string }{
		{"id", `17`},
		{"url", `"http://192.0.2.1/api/test/things/17/"`},
		{"name", `" R670 "`},
		{"note", `null`},
		{"size", `1.0`},
		{"count", `-3`},
		{"ok", `true`},
		{"airflow", `{"value": "front-to-rear", "label": "Front to rear"}`},
		{"type", `null`},
		{"eol", `"2027-03-31"`},
		{"maker", `{"id": 5, "url": "x", "display": "Dell", "name": "Dell", "slug": "dell"}`},
		{"parent", `9`},
		{"component_type", `"test.b"`},
		{"component_id", `4`},
		{"tags", `["a", "b"]`},
	}
	var parts []string
	for _, f := range fields {
		v, ok := replace[f.name]
		if !ok {
			v = f.value
		}
		if v != "" {
			parts = append(parts, `"`+f.name+`": `+v)
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func TestDecodeAPI(t *testing.T) {
	t.Parallel()
	thing, specs := apiSpecs()
	id, obj, err := thing.DecodeAPI(specs, []byte(thingAnswer(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if id != 17 {
		t.Errorf("id = %d, want 17", id)
	}
	want := Object{Type: "test.thing", Fields: map[string]Value{
		"name":      String(" R670 "), // not normalized
		"note":      Null{},
		"size":      NewDecimal(1, 0),
		"count":     Integer(-3),
		"ok":        Boolean(true),
		"airflow":   Choice("front-to-rear"),
		"type":      Choice(""), // the blank choice, which NetBox reads as null
		"eol":       mustDate(t, "2027-03-31"),
		"maker":     Reference{Type: "test.maker", ID: 5, Key: map[string]Value{"name": String("Dell")}},
		"parent":    Reference{Type: "test.thing", ID: 9},
		"component": Reference{Type: "test.b", ID: 4},
		"tags":      Set{String("b"), String("a")},
	}}
	if !obj.Equal(want) {
		t.Errorf("DecodeAPI = %v, want %v", obj.Fields, want.Fields)
	}

	_, obj, err = thing.DecodeAPI(specs, []byte(thingAnswer(map[string]string{
		"airflow": `null`, "eol": `null`, "parent": `null`, "component_type": `null`, "component_id": `null`,
		"size": `1.5e1`, "tags": `[]`,
	})))
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]Value{
		"airflow": Null{}, "eol": Null{}, "parent": Null{}, "component": Null{}, "size": NewDecimal(15, 0), "tags": Set{},
	} {
		if !Equal(obj.Fields[name], v) {
			t.Errorf("%s = %s, want %s", name, describe(obj.Fields[name]), v)
		}
	}
}

func TestDecodeAPIRefuses(t *testing.T) {
	t.Parallel()
	thing, specs := apiSpecs()
	tests := []struct {
		name    string
		answer  string
		wantErr string
	}{
		{"not an object", `[1]`, "isn't a JSON object"},
		{"null", `null`, "isn't a JSON object"},
		{"not JSON", `{"id": 1,`, "isn't a JSON object"},
		{"no id", thingAnswer(map[string]string{"id": ""}), "id: isn't in the answer"},
		{"id zero", thingAnswer(map[string]string{"id": `0`}), `id: "0" isn't an ID`},
		{"id negative", thingAnswer(map[string]string{"id": `-1`}), `isn't an ID`},
		{"id a string", thingAnswer(map[string]string{"id": `"17"`}), "id: is text, not an ID"},
		{"id a fraction", thingAnswer(map[string]string{"id": `17.0`}), `"17.0" isn't an ID`},
		{"id too large", thingAnswer(map[string]string{"id": `9223372036854775808`}), "isn't an ID"},
		{"missing field", thingAnswer(map[string]string{"count": ""}), "test.thing #17: count: isn't in the answer"},
		{"null where it can't be", thingAnswer(map[string]string{"name": `null`}), "name: is null, which it can't be"},
		{"string as number", thingAnswer(map[string]string{"name": `5`}), "name: is a number, not text"},
		{"decimal as string", thingAnswer(map[string]string{"size": `"1.0"`}), "size: is text, not a number"},
		{"decimal too precise", thingAnswer(map[string]string{"size": `1.0000000000000000001`}), "size: "},
		{"integer with a point", thingAnswer(map[string]string{"count": `2.0`}), `count: "2.0" isn't a whole number`},
		{"integer as string", thingAnswer(map[string]string{"count": `"2"`}), "count: is text, not a number"},
		{"boolean as string", thingAnswer(map[string]string{"ok": `"true"`}), "ok: is text, not true or false"},
		{"choice as bare value", thingAnswer(map[string]string{"airflow": `"front-to-rear"`}), "airflow: is text, not a choice's value and label"},
		{"choice without value", thingAnswer(map[string]string{"airflow": `{"label": "x"}`}), "airflow: its value is missing, not text"},
		{"choice value a number", thingAnswer(map[string]string{"airflow": `{"value": 1}`}), "airflow: its value is a number, not text"},
		{"choice value in other case", thingAnswer(map[string]string{"airflow": `{"VALUE": "front-to-rear"}`}), "airflow: its value is missing, not text"},
		{"date not a date", thingAnswer(map[string]string{"eol": `"2027-02-30"`}), `eol: "2027-02-30" isn't a date`},
		{"reference as ID", thingAnswer(map[string]string{"maker": `5`}), "maker: is a number, not a reference to a test.maker"},
		{"reference without id", thingAnswer(map[string]string{"maker": `{"name": "Dell"}`}), "maker: its id isn't in the answer"},
		{"reference without key", thingAnswer(map[string]string{"maker": `{"id": 5, "slug": "dell"}`}), "maker: refers to a test.maker without its name"},
		{"reference key of the wrong kind", thingAnswer(map[string]string{"maker": `{"id": 5, "name": 1}`}), "maker: name is a number, not text"},
		{"ID reference nested", thingAnswer(map[string]string{"parent": `{"id": 9}`}), "parent: is an object, not an ID"},
		{"generic reference to another type", thingAnswer(map[string]string{"component_type": `"test.c"`}), `component: component_type is "test.c", which it can't refer to`},
		{"generic reference half null", thingAnswer(map[string]string{"component_id": `null`}), "component: component_id is null, not an ID"},
		{"generic reference missing", thingAnswer(map[string]string{"component_type": ""}), "component: component_type or component_id isn't in the answer"},
		{"set not a list", thingAnswer(map[string]string{"tags": `"a"`}), "tags: is text, not a list"},
		{"set element", thingAnswer(map[string]string{"tags": `["a", 1]`}), "tags: element 1 is a number, not text"},
	}
	for _, tt := range tests {
		_, _, err := thing.DecodeAPI(specs, []byte(tt.answer))
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("%s: DecodeAPI(%s) = %v, want an error with %q", tt.name, tt.answer, err, tt.wantErr)
		}
	}
}

func TestDecodeAPIReferenceToATypeWithoutASpec(t *testing.T) {
	t.Parallel()
	thing, specs := apiSpecs()
	_, _, err := thing.DecodeAPI(specs[1:], []byte(thingAnswer(nil)))
	if err == nil || !strings.Contains(err.Error(), "maker: refers to a test.maker, which has no spec") {
		t.Errorf("DecodeAPI without the maker's spec = %v", err)
	}
}

func TestDecodeAPIQuotesWhatNetBoxSends(t *testing.T) {
	t.Parallel()
	thing, specs := apiSpecs()
	long := `"` + strings.Repeat("9", 100) + `"`
	_, _, err := thing.DecodeAPI(specs, []byte(thingAnswer(map[string]string{"eol": long})))
	if err == nil || len(err.Error()) > 120 {
		t.Errorf("DecodeAPI = %v, want an error that cuts the value short", err)
	}
}
