package model

import (
	"strings"
	"testing"
)

// testSpecs returns a consistent set of made-up specs, fresh each time,
// shaped like dcim's: a thing made by a maker, with ports that carry links
// nested in them, and items in a tree that can refer to a port.
func testSpecs() map[string]*Spec {
	src := []Source{{File: "test/models.py", Symbol: "Thing"}}
	name := Field{Name: "name", Kind: KindString, Required: true, MaxLength: 64, Trim: true}
	thingRef := func(name string) Field {
		return Field{Name: name, Kind: KindReference, Required: true, CreateOnly: true, Ref: &Ref{Types: []string{"test.thing"}}}
	}
	return map[string]*Spec{
		"maker": {
			Type: "test.maker", Endpoint: "/api/test/makers/",
			Identity: []string{"name"},
			Unique:   []Unique{{Fields: []string{"name"}}, {Fields: []string{"slug"}}},
			Fields: []Field{
				name,
				{Name: "slug", Kind: KindString, Required: true, MaxLength: 64, Pattern: "[a-z0-9-]+", Trim: true},
			},
			Ignored: []Ignored{{Name: "id", Reason: "read-only"}},
			Sources: src,
		},
		"thing": {
			Type: "test.thing", Endpoint: "/api/test/things/",
			DependsOn: []string{"test.maker"},
			Identity:  []string{"maker", "model"},
			Unique:    []Unique{{Fields: []string{"maker", "model"}, Source: Source{File: "test/models.py", Symbol: "Thing.Meta"}}},
			Fields: []Field{
				{Name: "maker", Kind: KindReference, Required: true, Ref: &Ref{Types: []string{"test.maker"}}},
				{Name: "model", Kind: KindString, Required: true, MaxLength: 100, Trim: true},
				{
					Name: "height", Kind: KindDecimal, Digits: 4, Places: 1, Min: NewDecimal(0, 0), Step: NewDecimal(5, 1),
					Default: NewDecimal(1, 0), ImportRequired: true, Source: Source{File: "test/models.py", Symbol: "Thing.clean"},
				},
				{Name: "full_depth", Kind: KindBoolean, Default: Boolean(true), ImportDefault: Boolean(false)},
				{
					Name: "role", Kind: KindChoice, Nullable: true, Blank: true, BlankIsNull: true, Default: Null{},
					Choices: &Choices{Values: []string{"parent", "child"}, Configurable: "test.Thing.role"},
					Source:  Source{File: "test/choices.py", Symbol: "ThingRoleChoices"},
				},
				{Name: "end", Kind: KindDate, Nullable: true, Default: Null{}},
				{Name: "tags", Kind: KindSet, Default: Set{}, Elem: &Field{Kind: KindString, MaxLength: 10}},
			},
			Children: []Collection{
				{Name: "ports", Type: "test.port", Parent: "thing", Key: []string{"name"}},
				{Name: "items", Type: "test.item", Parent: "thing", Key: []string{"name"}, Gap: "top-level items have a null parent"},
			},
			Sources: src,
		},
		"port": {
			Type: "test.port", Endpoint: "/api/test/ports/",
			DependsOn: []string{"test.thing"},
			Unique:    []Unique{{Fields: []string{"thing", "name"}}},
			Fields: []Field{
				thingRef("thing"),
				name,
				{Name: "positions", Kind: KindInteger, Min: Integer(1), Max: Integer(1024), Default: Integer(1)},
			},
			Children: []Collection{{Name: "links", Type: "test.link", Key: []string{"position"}, Nested: true, Source: Source{File: "test/utils.py", Symbol: "reconcile"}}},
			Sources:  src,
		},
		"link": {
			Type: "test.link",
			Fields: []Field{
				{Name: "position", Kind: KindInteger, Required: true},
				{Name: "port", Kind: KindReference, Required: true, Ref: &Ref{Types: []string{"test.port"}, Within: "test.thing", Shape: ShapeID}},
			},
			Unique:  []Unique{{Fields: []string{"port", "position"}}},
			Sources: src,
		},
		"item": {
			Type: "test.item", Endpoint: "/api/test/items/",
			DependsOn: []string{"test.thing"},
			Unique:    []Unique{{Fields: []string{"thing", "parent", "name"}, Gap: "parent can be null"}},
			Fields: []Field{
				thingRef("thing"),
				{Name: "parent", Kind: KindReference, Nullable: true, Default: Null{}, Ref: &Ref{Types: []string{"test.item"}, Within: "test.thing", Shape: ShapeID}},
				name,
				{
					Name: "component", Kind: KindReference, Nullable: true, Default: Null{},
					Ref:    &Ref{Types: []string{"test.port"}, Within: "test.thing", Shape: ShapeTypeAndID, TypeField: "component_type", IDField: "component_id"},
					Source: Source{File: "test/constants.py", Symbol: "COMPONENTS"},
				},
			},
			Children: []Collection{{Name: "child_items", Type: "test.item", Parent: "parent", Key: []string{"name"}, Shared: []string{"thing"}}},
			Sources:  src,
		},
	}
}

func list(specs map[string]*Spec) []*Spec {
	return []*Spec{specs["maker"], specs["thing"], specs["port"], specs["link"], specs["item"]}
}

func TestCheckConsistent(t *testing.T) {
	t.Parallel()
	if err := Check(list(testSpecs())); err != nil {
		t.Errorf("Check of consistent specs:\n%v", err)
	}
	if err := Check(nil); err != nil {
		t.Errorf("Check(nil) = %v", err)
	}
}

// TestCheckFinds breaks consistent specs one way at a time and checks that
// Check reports each.
func TestCheckFinds(t *testing.T) {
	t.Parallel()
	type specs = map[string]*Spec
	field := func(s *Spec, name string) *Field { return s.Field(name) }
	src := []Source{{File: "test/models.py", Symbol: "Thing.clean"}}
	tests := []struct {
		name   string
		breaks func(s specs) []*Spec
		want   string
	}{
		{"a type twice", func(s specs) []*Spec { return append(list(s), s["maker"]) }, "test.maker: there are two specs for it"},
		{"a bad type name", func(s specs) []*Spec { s["maker"].Type = "Test.Maker"; return list(s) }, `its type "Test.Maker" isn't app_label.model`},
		{"an endpoint twice", func(s specs) []*Spec { s["port"].Endpoint = "/api/test/makers/"; return list(s) }, "its endpoint /api/test/makers/ is test.maker's too"},
		{"a bad endpoint", func(s specs) []*Spec { s["maker"].Endpoint = "/dcim/makers"; return list(s) }, `its endpoint "/dcim/makers" isn't /api/<app>/<objects>/`},
		{"a nested type with an endpoint", func(s specs) []*Spec { s["link"].Endpoint = "/api/test/links/"; return list(s) }, "test.link: it has an endpoint, but a collection nests it"},
		{"a type with no endpoint that nothing nests", func(s specs) []*Spec { s["port"].Endpoint = ""; return list(s) }, "test.port: it has no endpoint, and no collection nests it"},
		{"a child with no endpoint in a collection that isn't nested", func(s specs) []*Spec { s["port"].Endpoint = ""; return list(s) }, "the collection ports holds test.port, which has no endpoint"},
		{"no sources", func(s specs) []*Spec { s["maker"].Sources = nil; return list(s) }, "test.maker: it cites no NetBox source"},
		{"a bad source", func(s specs) []*Spec { s["maker"].Sources[0].File = "/etc/passwd"; return list(s) }, `it cites "Thing" in "/etc/passwd", which isn't a Python file`},
		{"a bad field source", func(s specs) []*Spec { field(s["thing"], "height").Source.Symbol = "Thing clean"; return list(s) }, `height: it cites "Thing clean"`},
		{"a bad rule source", func(s specs) []*Spec { s["thing"].Unique[0].Source.File = "test/../x.py"; return list(s) }, "the uniqueness rule [maker model]: it cites"},
		{"a field twice", func(s specs) []*Spec {
			s["maker"].Fields = append(s["maker"].Fields, s["maker"].Fields[0])
			return list(s)
		}, "test.maker: it names name twice"},
		{"a field ignored too", func(s specs) []*Spec { s["maker"].Ignored[0].Name = "slug"; return list(s) }, "test.maker: it names slug twice"},
		{"a bad field name", func(s specs) []*Spec { s["maker"].Fields[0].Name = "Name"; return list(s) }, `"Name" isn't a field name`},
		{"a generic reference's field twice", func(s specs) []*Spec { field(s["item"], "component").Ref.IDField = "name"; return list(s) }, "test.item: it names name twice"},
		{"an ignored field without a reason", func(s specs) []*Spec { s["maker"].Ignored[0].Reason = ""; return list(s) }, "it ignores id without saying why"},
		{"no identity", func(s specs) []*Spec { s["maker"].Identity = nil; return list(s) }, "test.maker: it has no identity, and isn't a child type"},
		{"an identity that isn't unique", func(s specs) []*Spec { s["thing"].Identity = []string{"model"}; return list(s) }, "its identity [model] isn't one of its uniqueness rules"},
		{"an identity of no field", func(s specs) []*Spec {
			s["maker"].Identity = []string{"title"}
			s["maker"].Unique = append(s["maker"].Unique, Unique{Fields: []string{"title"}})
			return list(s)
		}, "its identity names title, which isn't one of its fields"},
		{"a rule of no field", func(s specs) []*Spec { s["port"].Unique[0].Fields = []string{"thing", "label"}; return list(s) }, "a uniqueness rule names label"},
		{"a rule of no fields", func(s specs) []*Spec { s["port"].Unique = append(s["port"].Unique, Unique{}); return list(s) }, "a uniqueness rule has no fields"},
		{"a rule twice", func(s specs) []*Spec {
			s["maker"].Unique = append(s["maker"].Unique, Unique{Fields: []string{"name"}})
			return list(s)
		}, "it has the uniqueness rule [name] twice"},
		{"a dependency on itself", func(s specs) []*Spec { s["maker"].DependsOn = []string{"test.maker"}; return list(s) }, "test.maker: it depends on itself"},
		{"a dependency on no spec", func(s specs) []*Spec { s["maker"].DependsOn = []string{"test.vendor"}; return list(s) }, "it depends on test.vendor, which has no spec"},
		{"a dependency twice", func(s specs) []*Spec { s["thing"].DependsOn = []string{"test.maker", "test.maker"}; return list(s) }, "it depends on test.maker twice"},
		{"a reference that isn't a dependency", func(s specs) []*Spec { s["thing"].DependsOn = nil; return list(s) }, "maker refers to a test.maker, which it doesn't depend on"},
		{"a dependency cycle", func(s specs) []*Spec { s["maker"].DependsOn = []string{"test.thing"}; return list(s) }, "its dependencies are in a cycle: test.maker -> test.thing -> test.maker"},
		{"a field of no kind", func(s specs) []*Spec { field(s["thing"], "end").Kind = KindNull; return list(s) }, "end has kind null, which a field can't have"},
		{"a field of children", func(s specs) []*Spec { field(s["thing"], "end").Kind = KindChildren; return list(s) }, "end has kind children"},
		{"a read-only field with a default", func(s specs) []*Spec { field(s["thing"], "end").ReadOnly = true; return list(s) }, "end is read-only, so it can't be required or have a default"},
		{"a required field with a default", func(s specs) []*Spec { field(s["thing"], "end").Required = true; return list(s) }, "end is required, so it can't have a default"},
		{"a required field the import requires", func(s specs) []*Spec {
			f := field(s["thing"], "height")
			f.Required, f.Default = true, nil
			return list(s)
		}, "height is required, so it can't have a default"},
		{"an optional field without a default", func(s specs) []*Spec { field(s["thing"], "end").Default = nil; return list(s) }, "end isn't required, so it needs the default"},
		{"a default that isn't a choice", func(s specs) []*Spec { field(s["thing"], "role").Default = Choice("sibling"); return list(s) }, `role has the default "sibling"`},
		{"a null default of a field that can't be null", func(s specs) []*Spec { field(s["thing"], "full_depth").Default = Null{}; return list(s) }, "full_depth has the default null, which can't be null"},
		{"an import default of the wrong kind", func(s specs) []*Spec { field(s["thing"], "full_depth").ImportDefault = String("no"); return list(s) }, `has the import default "no", which is a string, not a boolean`},
		{"a default too precise", func(s specs) []*Spec { field(s["thing"], "height").Default = NewDecimal(125, 2); return list(s) }, "has more than 1 decimal places"},
		{"a length on a number", func(s specs) []*Spec { field(s["port"], "positions").MaxLength = 3; return list(s) }, "positions isn't a string, so it can't have a length"},
		{"trimming a boolean", func(s specs) []*Spec { field(s["thing"], "full_depth").Trim = true; return list(s) }, "full_depth isn't a string"},
		{"places on a string", func(s specs) []*Spec { field(s["thing"], "model").Places = 1; return list(s) }, "model isn't a decimal"},
		{"bounds on a string", func(s specs) []*Spec { field(s["thing"], "model").Min = Integer(1); return list(s) }, "model isn't a number, so it can't have bounds"},
		{"a blank number", func(s specs) []*Spec { field(s["port"], "positions").Blank = true; return list(s) }, "positions isn't a string or choice, so it can't be blank"},
		{"blank as null for a field that can't be null", func(s specs) []*Spec { field(s["thing"], "role").Nullable = false; return list(s) }, "role reads blank as null, so it must be blank and nullable"},
		{"a choice without choices", func(s specs) []*Spec { field(s["thing"], "role").Choices = nil; return list(s) }, "role must have choices if, and only if, it is a choice"},
		{"choices on a string", func(s specs) []*Spec {
			field(s["thing"], "model").Choices = &Choices{Values: []string{"a"}}
			return list(s)
		}, "model must have choices if"},
		{"a reference without a Ref", func(s specs) []*Spec { field(s["thing"], "maker").Ref = nil; return list(s) }, "maker must have a Ref if"},
		{"a set without elements", func(s specs) []*Spec { field(s["thing"], "tags").Elem = nil; return list(s) }, "tags must have an element type if"},
		{"a negative length", func(s specs) []*Spec { field(s["thing"], "model").MaxLength = -1; return list(s) }, "model has a negative length"},
		{"a pattern that doesn't compile", func(s specs) []*Spec { field(s["maker"], "slug").Pattern = "[a-z"; return list(s) }, "slug has a pattern that doesn't compile"},
		{"no digits", func(s specs) []*Spec { field(s["thing"], "height").Digits = 0; return list(s) }, "height has 0 digits and 1 places"},
		{"more places than digits", func(s specs) []*Spec { field(s["thing"], "height").Places = 5; return list(s) }, "height has 4 digits and 5 places"},
		{"a negative step", func(s specs) []*Spec { field(s["thing"], "height").Step = NewDecimal(-5, 1); return list(s) }, "height has a step that isn't a positive decimal"},
		{"a step that isn't a decimal", func(s specs) []*Spec { field(s["thing"], "height").Step = Integer(1); return list(s) }, "height has a step that isn't a positive decimal"},
		{"a bound of the wrong kind", func(s specs) []*Spec { field(s["thing"], "height").Min = Integer(0); return list(s) }, "height has a bound 0 that isn't a decimal"},
		{"a minimum above the maximum", func(s specs) []*Spec { field(s["port"], "positions").Min = Integer(2000); return list(s) }, "positions has a minimum 2000 above its maximum 1024"},
		{"no choices", func(s specs) []*Spec { field(s["thing"], "role").Choices.Values = nil; return list(s) }, "role has no choices: is it missing from the generated choices?"},
		{"the blank choice listed", func(s specs) []*Spec {
			c := field(s["thing"], "role").Choices
			c.Values = append(c.Values, "")
			return list(s)
		}, "role lists the blank choice"},
		{"a choice twice", func(s specs) []*Spec {
			c := field(s["thing"], "role").Choices
			c.Values = append(c.Values, "child")
			return list(s)
		}, `role lists the choice "child" twice`},
		{"a bad FIELD_CHOICES key", func(s specs) []*Spec { field(s["thing"], "role").Choices.Configurable = "Thing.role"; return list(s) }, `role names "Thing.role" as its FIELD_CHOICES key`},
		{"a reference to no type", func(s specs) []*Spec { field(s["thing"], "maker").Ref.Types = nil; return list(s) }, "maker refers to no type"},
		{"a reference to no spec", func(s specs) []*Spec { field(s["thing"], "maker").Ref.Types = []string{"test.vendor"}; return list(s) }, "maker refers to test.vendor, which has no spec"},
		{"a reference to a type twice", func(s specs) []*Spec {
			field(s["item"], "component").Ref.Types = []string{"test.port", "test.port"}
			return list(s)
		}, "component refers to test.port twice"},
		{"a reference to a type with no identity", func(s specs) []*Spec { field(s["link"], "port").Ref.Within = ""; return list(s) }, "port refers to test.port, which has no identity to name it by"},
		{"a generic reference without its fields", func(s specs) []*Spec { field(s["item"], "component").Ref.TypeField = ""; return list(s) }, "component is a generic reference without its type and ID fields"},
		{"a nested reference with a type field", func(s specs) []*Spec { field(s["thing"], "maker").Ref.TypeField = "maker_type"; return list(s) }, "maker has type and ID fields, which only a generic reference has"},
		{"an unknown shape", func(s specs) []*Spec { field(s["thing"], "maker").Ref.Shape = 9; return list(s) }, "maker has an unknown shape 9"},
		{"a reference within no spec", func(s specs) []*Spec { field(s["link"], "port").Ref.Within = "test.box"; return list(s) }, "port refers within test.box, which has no spec"},
		{"a reference within a parent without such children", func(s specs) []*Spec { field(s["link"], "port").Ref.Within = "test.maker"; return list(s) }, "port refers to a test.port within a test.maker, which has no collection of them"},
		{"a set of sets", func(s specs) []*Spec {
			field(s["thing"], "tags").Elem = &Field{Kind: KindSet, Elem: &Field{Kind: KindString}}
			return list(s)
		}, "tags is a set of sets"},
		{"a set of broken elements", func(s specs) []*Spec { field(s["thing"], "tags").Elem = &Field{Kind: KindChoice}; return list(s) }, "tags's elements must have choices"},
		{"a collection of no spec", func(s specs) []*Spec { s["thing"].Children[0].Type = "test.socket"; return list(s) }, "the collection ports holds test.socket, which has no spec"},
		{"a bad collection source", func(s specs) []*Spec { s["thing"].Children[0].Source = Source{File: "x", Symbol: "y"}; return list(s) }, "the collection ports: it cites"},
		{"a nested collection with a parent field", func(s specs) []*Spec { s["port"].Children[0].Parent = "port"; return list(s) }, "the collection links is nested, so its children have no field for their parent"},
		{"a parent field that isn't a field", func(s specs) []*Spec { s["thing"].Children[0].Parent = "owner"; return list(s) }, "the collection ports names owner as its children's parent field, which isn't one of its fields"},
		{"a parent field that refers elsewhere", func(s specs) []*Spec { s["thing"].Children[0].Parent = "name"; return list(s) }, "names name as its children's parent field, which isn't a reference to a test.thing"},
		{"a collection without a key", func(s specs) []*Spec { s["thing"].Children[0].Key = nil; return list(s) }, "the collection ports has no key"},
		{"a key of no field", func(s specs) []*Spec { s["thing"].Children[0].Key = []string{"label"}; return list(s) }, "has the key field label, which isn't one of its fields of test.port"},
		{"a key with the parent field", func(s specs) []*Spec { s["thing"].Children[0].Key = []string{"thing", "name"}; return list(s) }, "the collection ports has its parent field thing in its key"},
		{"a set of references that isn't a dependency", func(s specs) []*Spec {
			field(s["thing"], "tags").Elem = &Field{Kind: KindReference, Ref: &Ref{Types: []string{"test.port"}}}
			return list(s)
		}, "tags refers to a test.port, which it doesn't depend on"},
		{"a reference within a type that doesn't hold it", func(s specs) []*Spec {
			s["maker"].Fields = append(s["maker"].Fields, Field{
				Name: "port", Kind: KindReference, Nullable: true, Default: Null{},
				Ref: &Ref{Types: []string{"test.port"}, Within: "test.thing", Shape: ShapeID},
			})
			return list(s)
		}, "port refers within a test.thing, which doesn't hold a test.maker"},
		{
			"a rule over a nullable field without a gap", func(s specs) []*Spec { s["item"].Unique[0].Gap = ""; return list(s) },
			"the uniqueness rule [thing parent name] has no gap, though parent can be null",
		},
		{
			"an identity with a gap", func(s specs) []*Spec { s["maker"].Unique[0].Gap = "sometimes"; return list(s) },
			"its identity [name] is a uniqueness rule with a gap",
		},
		{
			"a key no rule backs", func(s specs) []*Spec { s["thing"].Children[0].Key = []string{"positions"}; return list(s) },
			"the collection ports has no gap, but no uniqueness rule of test.port's makes its key [positions] unique",
		},
		{
			"a nested collection without a source or gap", func(s specs) []*Spec { s["port"].Children[0].Source = Source{}; return list(s) },
			"the collection links is nested and has no gap, so it needs a Source",
		},
		{
			"a generic reference without a source", func(s specs) []*Spec { field(s["item"], "component").Source = Source{}; return list(s) },
			"component is a generic reference, whose types NetBox's schema doesn't list, so it needs a Source",
		},
		{
			"a choice field without a source", func(s specs) []*Spec { field(s["thing"], "role").Source = Source{}; return list(s) },
			"role is a choice field, so it needs a Source citing its choice set",
		},
		{"a key backed by a rule that doesn't hold among the children", func(s specs) []*Spec {
			field(s["item"], "thing").Nullable = true
			return list(s)
		}, "the collection child_items has no gap, but no uniqueness rule of test.item's makes its key [name] unique"},
		{
			"tighter bounds on a string", func(s specs) []*Spec { field(s["thing"], "model").TighterBounds = src[0]; return list(s) },
			"model isn't a number, so it can't have bounds",
		},
		{"tighter bounds that aren't", func(s specs) []*Spec {
			f := field(s["port"], "positions")
			f.Min, f.Max, f.TighterBounds = nil, nil, src[0]
			return list(s)
		}, "positions cites TighterBounds, but has no bounds"},
		{
			"a bad bounds source", func(s specs) []*Spec {
				field(s["port"], "positions").TighterBounds = Source{File: "x", Symbol: "y"}
				return list(s)
			},
			"positions's bounds: it cites",
		},
		{"a shared field neither has", func(s specs) []*Spec { s["item"].Children[0].Shared = []string{"label"}; return list(s) }, "shares label, which isn't a field of both"},
		{"a shared field the parent hasn't", func(s specs) []*Spec { s["thing"].Children[0].Shared = []string{"positions"}; return list(s) }, "shares positions, which isn't a field of both test.thing and test.port"},
		{"a shared parent field", func(s specs) []*Spec { s["item"].Children[0].Shared = []string{"parent"}; return list(s) }, "the collection child_items shares its parent field parent"},
	}
	for _, tt := range tests {
		err := Check(tt.breaks(testSpecs()))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Check = %v, want a problem with %q", tt.name, err, tt.want)
		}
	}
}
