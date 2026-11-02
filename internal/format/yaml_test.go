package format

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseYAML(t *testing.T) {
	t.Parallel()
	root, err := ParseYAML([]byte(`---
manufacturer: Huawei
model: "NE8000-X8"
part_number: 02351930
u_height: 16
weight: 12.21
is_full_depth: yes
airflow: ~
comments: |
  Two lines,
  kept.
interfaces:
  - name: '08'
    mgmt_only: false
  - {name: eth1, type: 1000base-t}
`), DefinitionLimits)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	want := map[string]any{
		"manufacturer":  "Huawei",
		"model":         "NE8000-X8",
		"part_number":   "02351930", // not octal, so text, as PyYAML reads it
		"u_height":      json.Number("16"),
		"weight":        json.Number("12.21"),
		"is_full_depth": true,
		"airflow":       nil,
		"comments":      "Two lines,\nkept.\n",
		"interfaces": []any{
			map[string]any{"name": "08", "mgmt_only": false},
			map[string]any{"name": "eth1", "type": "1000base-t"},
		},
	}
	if diff := cmp.Diff(want, root.JSON()); diff != "" {
		t.Errorf("JSON (-want +got):\n%s", diff)
	}
	if n := root.Get("part_number"); n.Line != 4 || n.Column != 14 || n.Tag != TagStr {
		t.Errorf("part_number is at %d:%d as %s, want 4:14 as text", n.Line, n.Column, n.Tag)
	}
	if n := root.Get("interfaces").Items[1].Get("type"); n.Line != 15 || n.Value != "1000base-t" {
		t.Errorf("interfaces[1].type is %q at line %d, want 1000base-t at 15", n.Value, n.Line)
	}
	if root.Get("missing") != nil || root.Get("model").Get("x") != nil {
		t.Error("Get found a key that isn't there")
	}
	var keys []string
	for _, p := range root.Pairs {
		keys = append(keys, p.Key.Value)
	}
	if keys[0] != "manufacturer" || keys[len(keys)-1] != "interfaces" {
		t.Errorf("keys are %v, want them in the file's order", keys)
	}
}

func TestParseYAMLRefuses(t *testing.T) {
	t.Parallel()
	small := Limits{Size: 1 << 10, Depth: 4, Nodes: 20}
	tests := []struct {
		name   string
		yaml   string
		limits Limits
		want   ParseError // Reason holds a part of the reason
	}{
		{"empty", "", DefinitionLimits, ParseError{Reason: "holds no YAML document"}},
		{"only comments", "# nothing\n", DefinitionLimits, ParseError{Reason: "holds no YAML document"}},
		{"only a marker", "---\n", DefinitionLimits, ParseError{Line: 2, Column: 1, Reason: "holds null, not a mapping"}},
		{"a list", "- a\n", DefinitionLimits, ParseError{Line: 1, Column: 1, Reason: "holds a list, not a mapping"}},
		{"a scalar", "model\n", DefinitionLimits, ParseError{Line: 1, Column: 1, Reason: "holds text, not a mapping"}},
		{"two documents", "a: 1\n---\nb: 2\n", DefinitionLimits, ParseError{Line: 2, Column: 1, Reason: "more than one YAML document"}},
		{"bad YAML", "a: 1\nb: [\n", DefinitionLimits, ParseError{Line: 2, Reason: "isn't valid YAML"}},
		{"bad YAML in a later document", "a: 1\n---\nb: [\n", DefinitionLimits, ParseError{Line: 3, Reason: "isn't valid YAML"}},
		{"too large", "a: " + strings.Repeat("x", 1<<10), small, ParseError{Reason: "more than the 1024 a file of definitions may"}},
		{"too deep", "a: {b: {c: {d: 1}}}\n", small, ParseError{Line: 1, Column: 13, Field: "a.b.c", Reason: "more than 4 levels deep"}},
		{"too many nodes", "a: [" + strings.Repeat("1, ", 19) + "1]\n", small, ParseError{Line: 1, Column: 56, Field: "a[17]", Reason: "more than 20 keys"}},
		{"an anchor", "a: &x 1\nb: 2\n", DefinitionLimits, ParseError{Line: 1, Column: 4, Field: "a", Reason: "anchors"}},
		{"an alias", "a: &x 1\nb: *x\n", DefinitionLimits, ParseError{Line: 1, Column: 4, Field: "a", Reason: "anchors"}},
		{"an alias alone", "b: *x\n", DefinitionLimits, ParseError{Reason: "isn't valid YAML"}},
		{"a merge key", "a: 1\n<<: {b: 2}\n", DefinitionLimits, ParseError{Line: 2, Column: 1, Reason: "merge key"}},
		{"a merge value", "a: <<\n", DefinitionLimits, ParseError{Line: 1, Column: 4, Field: "a", Reason: "merge key"}},
		{"a value indicator", "a: =\n", DefinitionLimits, ParseError{Line: 1, Column: 4, Field: "a", Reason: "value indicator"}},
		{"a tag", "a: !!str 1\n", DefinitionLimits, ParseError{Line: 1, Column: 4, Field: "a", Reason: `tags, such as "!!str"`}},
		{"a local tag", "a: !thing x\n", DefinitionLimits, ParseError{Line: 1, Column: 4, Field: "a", Reason: "tags"}},
		{"a tagged mapping", "a: !!map {b: 1}\n", DefinitionLimits, ParseError{Line: 1, Column: 4, Field: "a", Reason: "tags"}},
		{"a binary", "a: !!binary aGk=\n", DefinitionLimits, ParseError{Line: 1, Column: 4, Field: "a", Reason: "tags"}},
		{"a date", "a: 2024-01-01\n", DefinitionLimits, ParseError{Line: 1, Column: 4, Field: "a", Reason: "reads as a date"}},
		{"infinity", "a: [1, .inf]\n", DefinitionLimits, ParseError{Line: 1, Column: 8, Field: "a[1]", Reason: "isn't a number NetBox can hold"}},
		{"too large a number", "a: 1.0e+400\n", DefinitionLimits, ParseError{Line: 1, Column: 4, Field: "a", Reason: "too large"}},
		{"too large a whole number", "a: 9223372036854775808\n", DefinitionLimits, ParseError{Line: 1, Column: 4, Field: "a", Reason: "too large"}},
		{"a duplicate key", "model: a\nslug: b\nmodel: c\n", DefinitionLimits, ParseError{Line: 3, Column: 1, Field: "model", Reason: "given twice, on lines 1 and 3"}},
		{"a duplicate quoted key", "a: {x: 1, 'x': 2}\n", DefinitionLimits, ParseError{Line: 1, Column: 11, Field: "a.x", Reason: "given twice"}},
		{"a boolean key", "yes: 1\n", DefinitionLimits, ParseError{Line: 1, Column: 1, Reason: `the key "yes" reads as true or false, not text`}},
		{"a number key", "a:\n  1: x\n", DefinitionLimits, ParseError{Line: 2, Column: 3, Field: "a", Reason: "reads as a whole number"}},
		{"a null key", "? \n: x\n", DefinitionLimits, ParseError{Line: 1, Column: 2, Reason: "reads as null"}},
		{"a mapping key", "? {a: 1}\n: x\n", DefinitionLimits, ParseError{Line: 1, Column: 3, Reason: "a key is a mapping or a list"}},
		{"a key with an odd name", "\"a b\": [1, .nan]\n", DefinitionLimits, ParseError{Line: 1, Column: 12, Field: `["a b"][1]`, Reason: "isn't a number"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseYAML([]byte(tt.yaml), tt.limits)
			perr, ok := errors.AsType[*ParseError](err)
			if !ok {
				t.Fatalf("ParseYAML = %v, want a *ParseError", err)
			}
			if !strings.Contains(perr.Reason, tt.want.Reason) {
				t.Errorf("reason = %q, want it to hold %q", perr.Reason, tt.want.Reason)
			}
			got := ParseError{Line: perr.Line, Column: perr.Column, Field: perr.Field, Reason: tt.want.Reason}
			if got != tt.want {
				t.Errorf("error at %d:%d %q, want %d:%d %q (%s)", got.Line, got.Column, got.Field, tt.want.Line, tt.want.Column, tt.want.Field, perr)
			}
		})
	}
}

// TestParseYAMLCutsWhatItQuotes checks that a reason never repeats more than
// a little of a file, whatever it holds.
func TestParseYAMLCutsWhatItQuotes(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("y", 10000)
	for _, doc := range []string{
		"a: !" + long + " x\n",
		long + ": 1\n" + long + ": 2\n",
		"a: [" + strings.Repeat("[", 5000) + "\n",
	} {
		_, err := ParseYAML([]byte(doc), DefinitionLimits)
		if err == nil {
			t.Fatalf("ParseYAML of %d bytes succeeded", len(doc))
		}
		if len(err.Error()) > 400 {
			t.Errorf("error is %d bytes long: %.100s…", len(err.Error()), err)
		}
	}
}

func TestProblemString(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		p    Problem
		want string
	}{
		{Problem{Path: "a.yaml", Line: 3, Column: 9, Field: "x[1].y", Reason: "bad"}, "a.yaml:3:9: x[1].y: bad"},
		{Problem{Path: "a.yaml", Line: 3, Reason: "bad"}, "a.yaml:3: bad"},
		{Problem{Path: "a.yaml", Reason: "bad"}, "a.yaml: bad"},
		{Problem{Reason: "bad"}, "bad"},
	} {
		if got := tt.p.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

func TestQuote(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"short":                    `"short"`,
		strings.Repeat("é", 30):    `"` + strings.Repeat("é", 20) + `"…`,
		"a\x1b[31mred\u202e" + "x": `"a\x1b[31mred\u202ex"`,
	} {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
}
