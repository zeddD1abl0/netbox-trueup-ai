package model

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	t.Parallel()
	trimmed := Field{Name: "model", Kind: KindString, Trim: true}
	choice := Field{Name: "airflow", Kind: KindChoice, Blank: true, Nullable: true, BlankIsNull: true}
	tests := []struct {
		name  string
		field Field
		in    Value
		want  Value
	}{
		{"trims spaces", trimmed, String("  PowerEdge R670 \t\n"), String("PowerEdge R670")},
		{"trims what Python trims", trimmed, String("\x1c\u00a0\u3000R670\u2029\x1f\u0085"), String("R670")},
		{"keeps inner spaces", trimmed, String("a  b"), String("a  b")},
		{"keeps zero-width spaces, which Python keeps", trimmed, String("\u200bR670"), String("\u200bR670")},
		{"leaves an untrimmed field", Field{Kind: KindString}, String(" a "), String(" a ")},
		{"blank choice reads as null", choice, Choice(""), Null{}},
		{"choice", choice, Choice("front-to-rear"), Choice("front-to-rear")},
		{"blank choice without BlankIsNull", Field{Kind: KindChoice, Blank: true}, Choice(""), Choice("")},
		{"blank string with BlankIsNull", Field{Kind: KindString, Trim: true, Blank: true, Nullable: true, BlankIsNull: true}, String("  "), Null{}},
		{"null", choice, Null{}, Null{}},
		{"decimal", Field{Kind: KindDecimal}, NewDecimal(10, 1), NewDecimal(1, 0)},
		{"set elements", Field{Kind: KindSet, Elem: &trimmed}, Set{String(" a "), String("b ")}, Set{String("a"), String("b")}},
		{"unset", trimmed, nil, nil},
	}
	for _, tt := range tests {
		got := tt.field.Normalize(tt.in)
		if !Equal(got, tt.want) {
			t.Errorf("%s: Normalize(%s) = %s, want %s", tt.name, describe(tt.in), describe(got), describe(tt.want))
		}
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	slug := Field{Name: "slug", Kind: KindString, MaxLength: 5, Pattern: "[-a-zA-Z0-9_]+", Trim: true}
	uHeight := Field{
		Name: "u_height", Kind: KindDecimal, Digits: 4, Places: 1,
		Min: NewDecimal(0, 0), Step: NewDecimal(5, 1), Default: NewDecimal(1, 0),
	}
	weight := Field{Name: "weight", Kind: KindDecimal, Digits: 8, Places: 2, Nullable: true}
	airflow := Field{
		Name: "airflow", Kind: KindChoice, Blank: true, Nullable: true, BlankIsNull: true,
		Choices: &Choices{Values: []string{"front-to-rear", "passive"}, Configurable: "dcim.Device.airflow"},
	}
	role := Field{Name: "subdevice_role", Kind: KindChoice, Choices: &Choices{Values: []string{"parent", "child"}}}
	positions := Field{Name: "positions", Kind: KindInteger, Min: Integer(1), Max: Integer(1024)}
	manufacturer := Field{Name: "manufacturer", Kind: KindReference, Ref: &Ref{Types: []string{"dcim.manufacturer"}}}
	tags := Field{Name: "tags", Kind: KindSet, Elem: &Field{Kind: KindReference, Ref: &Ref{Types: []string{"extras.tag"}}}}

	tests := []struct {
		field Field
		in    Value
		want  string // part of the error, or empty for none
	}{
		{slug, String("r-670"), ""},
		{slug, String("r670"), ""},
		{slug, String("r6700x"), "has 6 characters, more than 5"},
		{slug, String("ä"), `"ä" doesn't match`},
		{slug, String("a\n"), "doesn't match"},
		{slug, String(""), "can't be blank"},
		{slug, Null{}, "can't be null"},
		{slug, Integer(1), "is an integer, not a string"},
		{slug, nil, "has no value"},
		{Field{Name: "d", Kind: KindString, Blank: true, MaxLength: 2}, String(""), ""},
		{Field{Name: "d", Kind: KindString, MaxLength: 2}, String("äö"), ""},

		{uHeight, NewDecimal(15, 1), ""},
		{uHeight, NewDecimal(0, 0), ""},
		{uHeight, NewDecimal(9995, 1), ""},
		{uHeight, NewDecimal(125, 2), "has more than 1 decimal places"},
		{uHeight, NewDecimal(1000, 0), "more than 3 digits before the decimal point"},
		{uHeight, NewDecimal(-5, 1), "is less than 0"},
		{uHeight, NewDecimal(11, 1), "isn't a multiple of 0.5"},
		{weight, NewDecimal(99999999, 2), ""},
		{weight, NewDecimal(-99999999, 2), ""},
		{weight, NewDecimal(1000000, 0), "more than 6 digits before"},
		{weight, NewDecimal(1, 3), "more than 2 decimal places"},
		{weight, Null{}, ""},

		{airflow, Choice("passive"), ""},
		{airflow, Choice(""), ""},
		{airflow, Null{}, ""},
		{role, Choice("parent"), ""},
		{role, Choice(""), "can't be blank"},
		{role, Choice("sibling"), `"sibling" isn't one of its choices`},
		{role, String("parent"), "is a string, not a choice"},

		{positions, Integer(1), ""},
		{positions, Integer(1024), ""},
		{positions, Integer(0), "is less than 1"},
		{positions, Integer(1025), "is more than 1024"},

		{manufacturer, Reference{Type: "dcim.manufacturer", ID: 3}, ""},
		{manufacturer, Reference{Type: "dcim.platform", ID: 3}, "refers to a dcim.platform, which it can't"},
		{tags, Set{Reference{Type: "extras.tag", ID: 1}}, ""},
		{tags, Set{Reference{Type: "extras.tag", ID: 1}, String("x")}, `element "x" is a string, not a reference`},
	}
	for _, tt := range tests {
		err := tt.field.Validate(tt.in)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%s.Validate(%s): %v", tt.field.Name, describe(tt.in), err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("%s.Validate(%s) = %v, want an error with %q", tt.field.Name, describe(tt.in), err, tt.want)
		case err != nil && !strings.HasPrefix(err.Error(), tt.field.Name+": "):
			t.Errorf("%s.Validate(%s) = %v, which doesn't name the field", tt.field.Name, describe(tt.in), err)
		}
	}

	// A choice an administrator could have added is told apart, so that a
	// caller can warn rather than refuse.
	err := airflow.Validate(Choice("bottom-to-left"))
	if !errors.Is(err, ErrConfigurableChoice) || !strings.Contains(err.Error(), "dcim.Device.airflow") {
		t.Errorf("airflow.Validate of a choice it doesn't ship with = %v, want ErrConfigurableChoice naming its key", err)
	}
	if err := role.Validate(Choice("sibling")); errors.Is(err, ErrConfigurableChoice) {
		t.Errorf("a field whose choices can't change reported ErrConfigurableChoice: %v", err)
	}
}

func TestEqual(t *testing.T) {
	t.Parallel()
	mfr := func(id int64, name string) Reference {
		r := Reference{Type: "dcim.manufacturer", ID: id}
		if name != "" {
			r.Key = map[string]Value{"name": String(name)}
		}
		return r
	}
	child := func(name string) Object {
		return Object{Type: "dcim.interfacetemplate", Fields: map[string]Value{"name": String(name)}}
	}
	tests := []struct {
		a, b Value
		want bool
	}{
		{String("a"), String("a"), true},
		{String("a"), String("b"), false},
		{String("a"), Choice("a"), false},
		{NewDecimal(1, 0), NewDecimal(10, 1), true},
		{NewDecimal(1, 0), Integer(1), false},
		{Null{}, Null{}, true},
		{Null{}, String(""), false},
		{nil, nil, true},
		{nil, Null{}, false},
		{String(""), nil, false},
		{mfr(3, ""), mfr(3, ""), true},
		{mfr(3, "Dell"), mfr(4, "Dell"), false},
		{mfr(3, "Dell"), mfr(3, "Dell EMC"), true},
		{mfr(0, "Dell"), mfr(3, "Dell"), true},
		{mfr(0, "Dell"), mfr(0, "HPE"), false},
		{mfr(3, ""), Reference{Type: "dcim.platform", ID: 3}, false},
		{Set{String("a"), String("b")}, Set{String("b"), String("a")}, true},
		{Set{String("a"), String("a")}, Set{String("a"), String("b")}, false},
		{Set{String("a")}, Set{String("a"), String("a")}, false},
		{Set{}, Set(nil), true},
		{Children{child("eth0"), child("eth1")}, Children{child("eth1"), child("eth0")}, true},
		{Children{child("eth0")}, Children{child("eth1")}, false},
		{Children{child("eth0")}, Children{{Type: "dcim.interfacetemplate", Fields: map[string]Value{"name": String("eth0"), "label": String("")}}}, false},
	}
	for _, tt := range tests {
		if got := Equal(tt.a, tt.b); got != tt.want {
			t.Errorf("Equal(%s, %s) = %t, want %t", describe(tt.a), describe(tt.b), got, tt.want)
		}
		if got := Equal(tt.b, tt.a); got != tt.want {
			t.Errorf("Equal(%s, %s) = %t, want %t", describe(tt.b), describe(tt.a), got, tt.want)
		}
	}
}

func TestValueStrings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		v    Value
		want string
	}{
		{String("a b"), `"a b"`},
		{Integer(-3), "-3"},
		{Boolean(true), "true"},
		{Choice("parent"), `"parent"`},
		{Null{}, "null"},
		{NewDecimal(5, 1), "0.5"},
		{
			Reference{Type: "dcim.devicetype", ID: 7, Key: map[string]Value{"model": String("R670"), "manufacturer": Reference{Type: "dcim.manufacturer", Key: map[string]Value{"name": String("Dell")}}}},
			`dcim.devicetype #7 manufacturer=dcim.manufacturer name="Dell" model="R670"`,
		},
		{Set{String("a"), Integer(1)}, `["a", 1]`},
		{Children{{}, {}}, "2 children"},
	}
	for _, tt := range tests {
		if got := tt.v.String(); got != tt.want {
			t.Errorf("String() = %s, want %s", got, tt.want)
		}
	}
	for k := KindString; k <= KindNull; k++ {
		if strings.HasPrefix(k.String(), "Kind(") {
			t.Errorf("kind %d has no name", k)
		}
	}
	if got := Kind(99).String(); got != "Kind(99)" {
		t.Errorf("Kind(99).String() = %s", got)
	}
}
