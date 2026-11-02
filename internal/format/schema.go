package format

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// SchemaFile is a JSON schema a source holds, such as devicetype-library's
// schema/devicetype.json.
type SchemaFile struct {
	Path string
	Data []byte
}

// Limits on a source's JSON schemas, which come from the source too. Every
// one of them is read, since any could be the one another refers to.
const (
	MaxSchemaFiles = 32
	MaxSchemaSize  = 1 << 20
)

// Schema is a compiled JSON schema, which validates files of definitions.
type Schema struct {
	schema *jsonschema.Schema
}

// CompileSchema compiles the schema in the file at root, with the others it
// refers to among files. Each file is known by its $id, as JSON Schema's
// own references name it, such as urn:devicetype-library:device-type, or,
// if it has none, by a name made from its path.
//
// Nothing is ever loaded from elsewhere: a reference to anything but the
// files, such as file:///etc/passwd or an https URL, makes the schema fail
// to compile. jsonschema's own loader would read local files. Schemas are
// read as draft 2020-12 unless they say otherwise, without asserting
// formats or content, and their patterns are Go's regular expressions,
// which run in linear time.
func CompileSchema(files []SchemaFile, root string) (*Schema, error) {
	if len(files) > MaxSchemaFiles {
		return nil, fmt.Errorf("the source holds %d JSON schemas, more than the %d TrueUp reads", len(files), MaxSchemaFiles)
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(refuseLoader{})
	c.DefaultDraft(jsonschema.Draft2020)
	ids := map[string]string{} // URL -> path
	rootURL := ""
	for _, f := range files {
		if len(f.Data) > MaxSchemaSize {
			return nil, fmt.Errorf("schema %s holds %d bytes, more than the %d TrueUp reads", f.Path, len(f.Data), MaxSchemaSize)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(f.Data))
		if err != nil {
			return nil, fmt.Errorf("schema %s isn't JSON: %s", f.Path, cut(err.Error()))
		}
		id := schemaID(doc, f.Path)
		if other, ok := ids[id]; ok {
			return nil, fmt.Errorf("schemas %s and %s have the same $id, %s", other, f.Path, quote(id))
		}
		ids[id] = f.Path
		if err := c.AddResource(id, doc); err != nil {
			return nil, fmt.Errorf("schema %s: %s", f.Path, cut(err.Error()))
		}
		if f.Path == root {
			rootURL = id
		}
	}
	if rootURL == "" {
		return nil, fmt.Errorf("the source has no schema %s", root)
	}
	s, err := c.Compile(rootURL)
	if err != nil {
		return nil, fmt.Errorf("schema %s doesn't compile: %s", root, cut(err.Error()))
	}
	return &Schema{schema: s}, nil
}

// schemaID returns the URL a schema is known by: its $id, if that is an
// absolute URL without a fragment, or else one made from its path.
func schemaID(doc any, path string) string {
	if obj, ok := doc.(map[string]any); ok {
		if id, ok := obj["$id"].(string); ok {
			if u, err := url.Parse(id); err == nil && u.IsAbs() && u.Fragment == "" && !strings.HasSuffix(id, "#") {
				return id
			}
		}
	}
	return "urn:x-netbox-trueup-ai:source-file:" + url.PathEscape(path)
}

// refuseLoader refuses to load anything, so that a schema can refer only to
// the files CompileSchema was given.
type refuseLoader struct{}

func (refuseLoader) Load(u string) (any, error) {
	return nil, fmt.Errorf("TrueUp loads no schema from outside the source, such as %s", quote(u))
}

// cut cuts jsonschema's error text, which can quote a schema, short.
func cut(msg string) string {
	const limit = 500
	if len(msg) > limit {
		return strings.ToValidUTF8(msg[:limit], "") + "…"
	}
	return msg
}

// MaxSchemaProblems is the most problems Validate reports for one file.
// Files that fail the schema in more ways get a last problem that counts the
// rest.
const MaxSchemaProblems = 20

// Validate validates a file's root node, from ParseYAML, against the
// schema, and returns what it finds wrong, each at the node it concerns.
// The reasons are TrueUp's own, quoting values cut short, rather than
// jsonschema's, which list every value an enum allows.
func (s *Schema) Validate(path string, root *Node) (problems []Problem) {
	// security: jsonschema v6.0.3 panics on a number whose exponent big.Rat
	// can't hold, such as 1e9999999. ParseYAML reads every number into a
	// float64 first, so none reaches it; a panic from a schema and a file
	// that are both hostile still fails only the file.
	defer func() {
		if r := recover(); r != nil {
			problems = []Problem{{Severity: Error, Path: path, Reason: cut(fmt.Sprintf("the JSON Schema validator failed on the file: %v", r))}}
		}
	}()
	err := s.schema.Validate(root.JSON())
	if err == nil {
		return nil
	}
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return []Problem{{Severity: Error, Path: path, Reason: "doesn't satisfy the schema: " + cut(err.Error())}}
	}
	for _, leaf := range leaves(verr, nil) {
		problems = append(problems, schemaProblems(path, root, leaf)...)
	}
	slices.SortStableFunc(problems, func(a, b Problem) int {
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return a.Column - b.Column
	})
	problems = slices.CompactFunc(problems, func(a, b Problem) bool { return a == b })
	if len(problems) > MaxSchemaProblems {
		rest := len(problems) - MaxSchemaProblems + 1
		problems = append(problems[:MaxSchemaProblems-1], Problem{
			Severity: Error, Path: path, Reason: fmt.Sprintf("and %d more ways it doesn't satisfy the schema", rest),
		})
	}
	return problems
}

// leaves returns the errors in the tree that have no causes: the checks
// that failed, rather than the groups, such as allOf, that hold them.
func leaves(e *jsonschema.ValidationError, out []*jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(e.Causes) == 0 {
		return append(out, e)
	}
	for _, c := range e.Causes {
		out = leaves(c, out)
	}
	return out
}

// schemaProblems describes one failed check, at the node it concerns.
func schemaProblems(path string, root *Node, e *jsonschema.ValidationError) []Problem {
	n, field := locate(root, e.InstanceLocation)
	at := func(n *Node, field, reason string) Problem {
		return Problem{Severity: Error, Path: path, Line: n.Line, Column: n.Column, Field: field, Reason: reason}
	}
	switch k := e.ErrorKind.(type) {
	case *kind.AdditionalProperties:
		// One problem for each key, at the key.
		var out []Problem
		for _, name := range k.Properties {
			key, keyField := n, join(field, name)
			for _, p := range n.Pairs {
				if p.Key.Value == name {
					key = p.Key
				}
			}
			out = append(out, at(key, keyField, "isn't a key the schema allows here"))
		}
		return out
	case *kind.Required:
		return []Problem{at(n, field, "needs "+listQuoted(k.Missing)+", which the schema requires")}
	case *kind.DependentRequired:
		return []Problem{at(n, field, fmt.Sprintf("needs %s, which the schema requires with %s", listQuoted(k.Missing), quote(k.Prop)))}
	case *kind.Dependency:
		return []Problem{at(n, field, fmt.Sprintf("needs %s, which the schema requires with %s", listQuoted(k.Missing), quote(k.Prop)))}
	}
	return []Problem{at(n, field, describeFailure(n, e.ErrorKind))}
}

// describeFailure says why a value fails a check.
func describeFailure(n *Node, k jsonschema.ErrorKind) string {
	switch k := k.(type) {
	case *kind.Type:
		var want []string
		for _, w := range k.Want {
			want = append(want, jsonTypeName(w))
		}
		return fmt.Sprintf("is %s, but the schema wants %s", describeNode(n), strings.Join(want, " or "))
	case *kind.Enum:
		if len(k.Want) == 1 {
			return fmt.Sprintf("%s isn't %s, the one value the schema allows", show(n), display(k.Want[0]))
		}
		return fmt.Sprintf("%s isn't one of the %d values the schema allows", show(n), len(k.Want))
	case *kind.Const:
		return fmt.Sprintf("%s isn't %s, the one value the schema allows", show(n), display(k.Want))
	case *kind.MinLength:
		return fmt.Sprintf("has %d characters, fewer than the schema's %d", k.Got, k.Want)
	case *kind.MaxLength:
		return fmt.Sprintf("has %d characters, more than the schema's %d", k.Got, k.Want)
	case *kind.Pattern:
		return fmt.Sprintf("%s doesn't match the schema's pattern %s", show(n), quote(k.Want))
	case *kind.Minimum:
		return fmt.Sprintf("%s is less than the schema's minimum, %s", show(n), rat(k.Want))
	case *kind.Maximum:
		return fmt.Sprintf("%s is more than the schema's maximum, %s", show(n), rat(k.Want))
	case *kind.ExclusiveMinimum:
		return fmt.Sprintf("%s isn't more than %s, as the schema requires", show(n), rat(k.Want))
	case *kind.ExclusiveMaximum:
		return fmt.Sprintf("%s isn't less than %s, as the schema requires", show(n), rat(k.Want))
	case *kind.MultipleOf:
		return fmt.Sprintf("%s isn't a multiple of %s, as the schema requires", show(n), rat(k.Want))
	case *kind.MinItems:
		return fmt.Sprintf("has %d items, fewer than the schema's %d", k.Got, k.Want)
	case *kind.MaxItems:
		return fmt.Sprintf("has %d items, more than the schema's %d", k.Got, k.Want)
	case *kind.UniqueItems:
		return fmt.Sprintf("items %d and %d are the same, which the schema doesn't allow", k.Duplicates[0]+1, k.Duplicates[1]+1)
	case *kind.FalseSchema:
		return "isn't allowed by the schema"
	case *kind.OneOf:
		return "matches more than one of the schema's choices (oneOf)"
	case *kind.Not:
		return "matches what the schema forbids (not)"
	case *kind.RefCycle:
		return "can't be checked: the schema's references loop through " + quote(k.URL)
	}
	if path := k.KeywordPath(); len(path) > 0 {
		return "doesn't satisfy the schema's " + quote(strings.Join(path, "/"))
	}
	return "doesn't satisfy the schema"
}

// locate returns the node at an instance location, a list of keys and
// indexes from the root, and its field path, such as interfaces[3].type.
// It stops at the deepest node it finds.
func locate(root *Node, location []string) (*Node, string) {
	n, field := root, ""
	for _, token := range location {
		switch n.Kind {
		case Mapping:
			v := n.Get(token)
			if v == nil {
				return n, field
			}
			n, field = v, join(field, token)
		case Sequence:
			i, err := strconv.Atoi(token)
			if err != nil || i < 0 || i >= len(n.Items) {
				return n, field
			}
			n, field = n.Items[i], field+"["+token+"]"
		default:
			return n, field
		}
	}
	return n, field
}

// show quotes a scalar's value, cut short, for a reason; it describes
// anything else.
func show(n *Node) string {
	if n.Kind != Scalar {
		return describeNode(n)
	}
	switch n.Tag {
	case TagStr:
		return quote(n.Value)
	case TagNull:
		return "null"
	default:
		return n.Value
	}
}

// display writes a value from a schema, cut short.
func display(v any) string {
	switch v := v.(type) {
	case string:
		return quote(v)
	case nil:
		return "null"
	case map[string]any:
		return "a mapping"
	case []any:
		return "a list"
	}
	s := fmt.Sprint(v)
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return s
}

// rat writes a number from a schema as a decimal, if it is one, or as a
// fraction otherwise.
func rat(r *big.Rat) string {
	if r == nil {
		return "?"
	}
	if r.IsInt() {
		return r.Num().String()
	}
	for places := 1; places <= 20; places++ {
		s := r.FloatString(places)
		if back, ok := new(big.Rat).SetString(s); ok && back.Cmp(r) == 0 {
			return s
		}
	}
	return r.RatString()
}

func jsonTypeName(t string) string {
	switch t {
	case "string":
		return "text"
	case "integer":
		return "a whole number"
	case "number":
		return "a number"
	case "boolean":
		return "true or false"
	case "object":
		return "a mapping"
	case "array":
		return "a list"
	case "null":
		return "null"
	}
	return quote(t)
}

// listQuoted quotes names and lists them: "a", "a and b", or "a, b and c".
func listQuoted(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = quote(name)
	}
	switch len(quoted) {
	case 0:
		return "nothing"
	case 1:
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}
