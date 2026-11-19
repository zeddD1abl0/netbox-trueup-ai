package model

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// unmarshalSpecs are apiSpecs with a reference within a parent: a link's
// part is another of its thing's children, named by its key in the thing's
// parts.
func unmarshalSpecs() (thing *Spec, all []*Spec) {
	thing, all = apiSpecs()
	link := SpecOf(all, "test.link")
	link.Fields = append(slices.Clone(link.Fields), Field{
		Name: "part", Kind: KindReference, Nullable: true, Ref: &Ref{Types: []string{"test.part"}, Within: "test.thing"},
	})
	return thing, all
}

// unmarshalThing is an object of unmarshalSpecs' thing with a value of
// every kind TrueUp reads back.
func unmarshalThing(t *testing.T) Object {
	t.Helper()
	dell := Reference{Type: "test.maker", Key: map[string]Value{"name": String("Dell")}}
	return Object{Type: "test.thing", Fields: map[string]Value{
		"name":    String(` R670 <&> "x" `),
		"note":    Null{},
		"size":    NewDecimal(-15, 1),
		"count":   Integer(-3),
		"ok":      Boolean(false),
		"airflow": Choice("front-to-rear"),
		"type":    Choice(""),
		"eol":     mustDate(t, "2027-03-31"),
		"maker":   dell,
		"parent":  Reference{Type: "test.thing", ID: 9},
		"tags":    Set{String("b"), String("a")},
		"links": Children{
			{Type: "test.link", Fields: map[string]Value{
				"position": Integer(1),
				"target":   Reference{Type: "test.thing", ID: 4},
				"part":     Reference{Type: "test.part", Key: map[string]Value{"name": String("PSU1")}},
				"notes":    Children{{Type: "test.note", Fields: map[string]Value{"text": String("a")}}},
			}},
			{Type: "test.link", Fields: map[string]Value{"position": Integer(2), "part": Null{}, "notes": Children{}}},
			{Type: "test.link", Fields: map[string]Value{}},
		},
		"parts": Children{{Type: "test.part", Fields: map[string]Value{"name": String("PSU1")}}},
	}}
}

func TestUnmarshalFieldsReadsWhatMarshalWrites(t *testing.T) {
	t.Parallel()
	thing, specs := unmarshalSpecs()
	want := unmarshalThing(t)
	raw, err := json.Marshal(want.Fields)
	if err != nil {
		t.Fatal(err)
	}
	got, err := thing.UnmarshalFields(specs, raw)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly, children in their order, not only as Equal compares.
	if diff := cmp.Diff(want.Fields, got, cmp.AllowUnexported(Decimal{}, Date{}), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("UnmarshalFields (-want +got):\n%s", diff)
	}
	if !(Object{Type: thing.Type, Fields: got}).Equal(want) {
		t.Error("UnmarshalFields gives an object that isn't Equal to the one written")
	}

	// A resolved reference with its key, and a field left out.
	maker := Reference{Type: "test.maker", ID: 7, Key: map[string]Value{"name": String("Dell")}}
	raw, err = json.Marshal(map[string]Value{"maker": maker})
	if err != nil {
		t.Fatal(err)
	}
	got, err = thing.UnmarshalFields(specs, raw)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(map[string]Value{"maker": maker}, got); diff != "" {
		t.Errorf("UnmarshalFields (-want +got):\n%s", diff)
	}
}

func TestUnmarshalFieldsRefuses(t *testing.T) {
	t.Parallel()
	thing, specs := unmarshalSpecs()
	tests := []struct {
		name, json, want string
	}{
		{"not an object", `[]`, "a test.thing is a list, not an object"},
		{"not JSON", `{`, "a test.thing isn't valid JSON"},
		{"unknown field", `{"colour": "red"}`, "colour: isn't a field of a test.thing"},
		{"string as a number", `{"name": 1}`, "name: is a number, not text"},
		{"choice as an object", `{"airflow": {"value": "x"}}`, "airflow: is an object, not text"},
		{"bad date", `{"eol": "2027-02-30"}`, `eol: `},
		{"boolean as text", `{"ok": "true"}`, "ok: is text, not true or false"},
		{"fraction as an integer", `{"count": 1.5}`, `count: "1.5" isn't a whole number`},
		{"integer as text", `{"count": "1"}`, "count: is text, not a number"},
		{"decimal as text", `{"size": "1.5"}`, "size: is text, not a number"},
		{"decimal too long", `{"size": 1.0000000000000000001}`, "size: "},
		{"reference as text", `{"maker": "Dell"}`, "maker: is text, not a reference to a test.maker"},
		{"reference without its key", `{"maker": {}}`, "maker: names a test.maker without its name"},
		{"reference by another field", `{"maker": {"name": "Dell", "slug": "dell"}}`, `maker: names a test.maker by "slug", which isn't part of its key`},
		{"reference with a bad ID", `{"maker": {"id": 0, "name": "Dell"}}`, "maker: its id "},
		{"reference with a bad key", `{"maker": {"name": 5}}`, "maker: name is a number, not text"},
		{"reference to a type without a key", `{"parent": {}}`, "parent: refers to a test.thing, which has no key to name it by"},
		{"generic reference", `{"component": {"id": 4}}`, "component: is a reference whose type TrueUp can't tell from its JSON"},
		{"set as an object", `{"tags": {}}`, "tags: is an object, not a list"},
		{"set element", `{"tags": ["a", 1]}`, "tags: element 1 is a number, not text"},
		{"children as an object", `{"links": {}}`, "links: is an object, not a list"},
		{"child as text", `{"links": ["a"]}`, "links: [0]: a test.link is text, not an object"},
		{"child's field", `{"links": [{"position": 1}, {"colour": 1}]}`, "links: [1]: colour: isn't a field of a test.link"},
		{"nested child's reference", `{"links": [{"part": {"slug": "x"}}]}`, `links: [0]: part: names a test.part by "slug"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := thing.UnmarshalFields(specs, []byte(tt.json))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("UnmarshalFields(%s) = %v, want an error containing %q", tt.json, err, tt.want)
			}
		})
	}
}

func TestUnmarshalFieldsNeedsSpecs(t *testing.T) {
	t.Parallel()
	thing, specs := unmarshalSpecs()
	without := func(typ string) []*Spec {
		return slices.DeleteFunc(slices.Clone(specs), func(s *Spec) bool { return s.Type == typ })
	}
	for _, tt := range []struct {
		specs      []*Spec
		json, want string
	}{
		{without("test.maker"), `{"maker": {"name": "Dell"}}`, "maker: refers to a test.maker, which has no spec"},
		{without("test.link"), `{"links": []}`, "links: holds a test.link, which has no spec"},
		{without("test.part"), `{"links": [{"part": {"name": "x"}}]}`, "part: refers to a test.part, which has no spec"},
	} {
		if _, err := thing.UnmarshalFields(tt.specs, []byte(tt.json)); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("UnmarshalFields(%s) = %v, want an error containing %q", tt.json, err, tt.want)
		}
	}
}
