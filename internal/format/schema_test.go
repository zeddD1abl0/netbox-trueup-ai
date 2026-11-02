package format

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// testSchemas is a small set of schemas that refer to each other by $id, as
// devicetype-library's do.
var testSchemas = []SchemaFile{
	{Path: "schema/thing.json", Data: []byte(`{
		"$id": "urn:test:thing",
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"name": {"type": "string", "maxLength": 8, "pattern": "^[a-z]+$"},
			"size": {"type": "number", "minimum": 0, "multipleOf": 0.5},
			"weight": {"$ref": "urn:test:common#/definitions/weight"},
			"kind": {"$ref": "urn:test:common#/definitions/kind"},
			"flag": {"type": "boolean"},
			"parts": {"type": "array", "items": {"$ref": "urn:test:common#/definitions/part"}}
		},
		"required": ["name", "size"],
		"dependentRequired": {"weight": ["kind"]},
		"additionalProperties": false
	}`)},
	{Path: "schema/common.json", Data: []byte(`{
		"$id": "urn:test:common",
		"definitions": {
			"weight": {"type": "number", "multipleOf": 0.01},
			"kind": {"enum": ["a", "b", "c"]},
			"part": {
				"type": "object",
				"properties": {"name": {"type": "string"}, "count": {"type": "integer", "maximum": 10}},
				"required": ["name"],
				"additionalProperties": false
			}
		}
	}`)},
}

func compileTest(t *testing.T) *Schema {
	t.Helper()
	s, err := CompileSchema(testSchemas, "schema/thing.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestValidate(t *testing.T) {
	t.Parallel()
	s := compileTest(t)
	tests := []struct {
		name string
		yaml string
		want []Problem
	}{
		{"valid", "name: abc\nsize: 1.5\nweight: 12.21\nkind: a\nparts: [{name: x, count: 10}]\n", nil},
		// Exact decimals: 12.21 is a multiple of 0.01, though in floats it isn't.
		{"exact multiples", "name: abc\nsize: 0\nweight: 0.07\nkind: b\n", nil},
		{"several problems, in the order of the file", "name: ABC\nsize: -1\nextra: 1\n", []Problem{
			{Line: 1, Column: 7, Field: "name", Reason: `"ABC" doesn't match the schema's pattern "^[a-z]+$"`},
			{Line: 2, Column: 7, Field: "size", Reason: "-1 is less than the schema's minimum, 0"},
			{Line: 3, Column: 1, Field: "extra", Reason: "isn't a key the schema allows here"},
		}},
		{"missing keys", "flag: true\n", []Problem{
			{Line: 1, Column: 1, Reason: `needs "name" and "size", which the schema requires`},
		}},
		{"types", "name: 12\nsize: abc\nflag: 'yes'\nparts: {}\n", []Problem{
			{Line: 1, Column: 7, Field: "name", Reason: "is a whole number, but the schema wants text"},
			{Line: 2, Column: 7, Field: "size", Reason: "is text, but the schema wants a number"},
			{Line: 3, Column: 7, Field: "flag", Reason: "is text, but the schema wants true or false"},
			{Line: 4, Column: 8, Field: "parts", Reason: "is a mapping, but the schema wants a list"},
		}},
		{"multiples, lengths and enums", "name: abcdefghij\nsize: 0.7\nweight: 0.001\nkind: d\n", []Problem{
			{Line: 1, Column: 7, Field: "name", Reason: "has 10 characters, more than the schema's 8"},
			{Line: 2, Column: 7, Field: "size", Reason: "0.7 isn't a multiple of 0.5, as the schema requires"},
			{Line: 3, Column: 9, Field: "weight", Reason: "0.001 isn't a multiple of 0.01, as the schema requires"},
			{Line: 4, Column: 7, Field: "kind", Reason: `"d" isn't one of the 3 values the schema allows`},
		}},
		{"dependent keys", "name: a\nsize: 1\nweight: 1\n", []Problem{
			{Line: 1, Column: 1, Reason: `needs "kind", which the schema requires with "weight"`},
		}},
		{"in a list", "name: a\nsize: 1\nparts:\n  - name: x\n  - count: 11\n    other: 1\n", []Problem{
			{Line: 5, Column: 5, Field: "parts[1]", Reason: `needs "name", which the schema requires`},
			{Line: 5, Column: 12, Field: "parts[1].count", Reason: "11 is more than the schema's maximum, 10"},
			{Line: 6, Column: 5, Field: "parts[1].other", Reason: "isn't a key the schema allows here"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, err := ParseYAML([]byte(tt.yaml), DefinitionLimits)
			if err != nil {
				t.Fatal(err)
			}
			for i := range tt.want {
				tt.want[i].Severity, tt.want[i].Path = Error, "x.yaml"
			}
			if diff := cmp.Diff(tt.want, s.Validate("x.yaml", root)); diff != "" {
				t.Errorf("problems (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateCountsWhatItLeavesOut(t *testing.T) {
	t.Parallel()
	s := compileTest(t)
	var b strings.Builder
	b.WriteString("name: a\nsize: 1\n")
	for i := range 30 {
		b.WriteString("extra" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ": 1\n")
	}
	root, err := ParseYAML([]byte(b.String()), DefinitionLimits)
	if err != nil {
		t.Fatal(err)
	}
	problems := s.Validate("x.yaml", root)
	if len(problems) != MaxSchemaProblems {
		t.Fatalf("Validate gave %d problems, want %d", len(problems), MaxSchemaProblems)
	}
	if last := problems[len(problems)-1]; last.Reason != "and 11 more ways it doesn't satisfy the schema" || last.Line != 0 {
		t.Errorf("last problem = %+v, want one counting the other 11", last)
	}
}

func TestCompileSchemaRefuses(t *testing.T) {
	t.Parallel()
	thing := testSchemas[0]
	tests := []struct {
		name  string
		files []SchemaFile
		root  string
		want  string
	}{
		{"no root", testSchemas[1:], "schema/thing.json", "has no schema schema/thing.json"},
		{"a missing reference", testSchemas[:1], "schema/thing.json", "doesn't compile"},
		{"a local file", []SchemaFile{{Path: "s.json", Data: []byte(`{"$ref": "file:///etc/hostname"}`)}}, "s.json", "loads no schema from outside the source"},
		// Against a urn: base, a relative reference names the schema itself.
		{"a relative file", []SchemaFile{{Path: "s.json", Data: []byte(`{"$ref": "other.json#/definitions/x"}`)}}, "s.json", "doesn't compile"},
		{"a URL", []SchemaFile{{Path: "s.json", Data: []byte(`{"$ref": "https://example.com/s.json"}`)}}, "s.json", "loads no schema from outside the source"},
		{"a meta-schema", []SchemaFile{{Path: "s.json", Data: []byte(`{"$schema": "https://example.com/meta", "type": "object"}`)}}, "s.json", "loads no schema from outside the source"},
		{"an id twice", []SchemaFile{thing, {Path: "schema/copy.json", Data: thing.Data}}, "schema/thing.json", `schema/thing.json and schema/copy.json have the same $id, "urn:test:thing"`},
		{"not JSON", []SchemaFile{thing, {Path: "schema/bad.json", Data: []byte(`{"a": `)}}, "schema/thing.json", "schema/bad.json isn't JSON"},
		{"an invalid schema", []SchemaFile{{Path: "s.json", Data: []byte(`{"type": 12}`)}}, "s.json", "doesn't compile"},
		{"too large", []SchemaFile{{Path: "s.json", Data: []byte(strings.Repeat(" ", MaxSchemaSize+1))}}, "s.json", "more than the 1048576 TrueUp reads"},
		{"too many", make([]SchemaFile, MaxSchemaFiles+1), "s.json", "holds 33 JSON schemas"},
		// jsonschema v6.0.3 panics hashing such a number to check that an
		// enum's values are unique, when the meta-schema says they must be.
		{"a number too large", []SchemaFile{{Path: "s.json", Data: []byte(`{"$schema": "http://json-schema.org/draft-04/schema#", "enum": [1e9999999` + strings.Repeat(", 0", 25) + `]}`)}}, "s.json", `holds the number "1e9999999"`},
		{"a number too small", []SchemaFile{{Path: "s.json", Data: []byte(`{"minimum": 1e-400}`)}}, "s.json", `holds the number "1e-400"`},
		{"a number too large deep down", []SchemaFile{{Path: "s.json", Data: []byte(`{"properties": {"a": {"enum": [{"x": [1E+999999]}]}}}`)}}, "s.json", `holds the number "1E+999999"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := CompileSchema(tt.files, tt.root)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("CompileSchema = %v, want an error holding %q", err, tt.want)
			}
		})
	}
}

// TestCompileSchemaKeepsErrors checks that jsonschema's errors are wrapped,
// though their text is cut short.
func TestCompileSchemaKeepsErrors(t *testing.T) {
	t.Parallel()
	_, err := CompileSchema([]SchemaFile{{Path: "s.json", Data: []byte(`{"a": ` + strings.Repeat(`"x", `, 400) + `}`)}}, "s.json")
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Errorf("CompileSchema = %v, want a *json.SyntaxError in it", err)
	}
	if err != nil && len(err.Error()) > 600 {
		t.Errorf("the error is %d bytes long", len(err.Error()))
	}
}

// TestCompileSchemaWithoutID checks that a schema without an $id is still
// known by its path, and can be the root.
func TestCompileSchemaWithoutID(t *testing.T) {
	t.Parallel()
	s, err := CompileSchema([]SchemaFile{{Path: "s.json", Data: []byte(`{"type": "object", "required": ["a"]}`)}}, "s.json")
	if err != nil {
		t.Fatal(err)
	}
	root, err := ParseYAML([]byte("b: 1\n"), DefinitionLimits)
	if err != nil {
		t.Fatal(err)
	}
	if problems := s.Validate("x.yaml", root); len(problems) != 1 || !strings.Contains(problems[0].Reason, `needs "a"`) {
		t.Errorf("Validate = %+v, want the missing key", problems)
	}
}

// TestValidateReferenceLoop checks that a schema whose relative reference
// names itself, as one does against a urn: base, fails every file with a
// reason, rather than looping or passing it.
func TestValidateReferenceLoop(t *testing.T) {
	t.Parallel()
	s, err := CompileSchema([]SchemaFile{{Path: "s.json", Data: []byte(`{"$ref": "other.json"}`)}}, "s.json")
	if err != nil {
		t.Fatal(err)
	}
	root, err := ParseYAML([]byte("a: 1\n"), DefinitionLimits)
	if err != nil {
		t.Fatal(err)
	}
	problems := s.Validate("x.yaml", root)
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "references loop") {
		t.Errorf("Validate = %+v, want one problem saying the references loop", problems)
	}
}

// TestValidateSurvivesAPanic checks that a panic in the validator fails only
// the file. A Node made by hand, which ParseYAML never makes, can carry a
// number whose exponent makes jsonschema v6.0.3 panic; later releases
// report it as an error instead.
func TestValidateSurvivesAPanic(t *testing.T) {
	t.Parallel()
	s, err := CompileSchema([]SchemaFile{{Path: "s.json", Data: []byte(`{"properties": {"a": {"minimum": 0}}}`)}}, "s.json")
	if err != nil {
		t.Fatal(err)
	}
	root := &Node{Kind: Mapping, Line: 1, Column: 1, Pairs: []Pair{{
		Key:   &Node{Kind: Scalar, Tag: TagStr, Value: "a", Line: 1, Column: 1},
		Value: &Node{Kind: Scalar, Tag: TagFloat, Value: "1e9999999", Line: 1, Column: 4},
	}}}
	problems := s.Validate("x.yaml", root)
	if len(problems) != 1 || problems[0].Severity != Error {
		t.Errorf("Validate = %+v, want one error", problems)
	}
}
