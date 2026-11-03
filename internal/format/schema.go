package format

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
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
	if err := checkSchemaCount(len(files)); err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(refuseLoader{})
	c.DefaultDraft(jsonschema.Draft2020)
	ids := map[string]string{} // URL -> path
	rootURL := ""
	for _, f := range files {
		if err := checkSchemaSize(f.Path, int64(len(f.Data))); err != nil {
			return nil, err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(f.Data))
		if err != nil {
			return nil, fmt.Errorf("schema %s isn't JSON: %w", f.Path, cutError{err})
		}
		if n, ok := hugeNumber(doc); ok {
			return nil, fmt.Errorf("schema %s holds the number %s, which is beyond what TrueUp reads", f.Path, Quote(n))
		}
		id := schemaID(doc, f.Path)
		if other, ok := ids[id]; ok {
			return nil, fmt.Errorf("schemas %s and %s have the same $id, %s", other, f.Path, Quote(id))
		}
		ids[id] = f.Path
		if err := guard(func() error { return c.AddResource(id, doc) }); err != nil {
			return nil, fmt.Errorf("schema %s: %w", f.Path, cutError{err})
		}
		if f.Path == root {
			rootURL = id
		}
	}
	if rootURL == "" {
		return nil, fmt.Errorf("the source has no schema %s", root)
	}
	var s *jsonschema.Schema
	if err := guard(func() (err error) { s, err = c.Compile(rootURL); return err }); err != nil {
		return nil, fmt.Errorf("schema %s doesn't compile: %w", root, cutError{err})
	}
	return &Schema{schema: s}, nil
}

// guard calls one of jsonschema's functions, and returns a panic in it as an
// error.
//
// security: jsonschema v6.0.3 panics on a number whose exponent big.Rat
// can't hold, such as 1e9999999, as when it checks that an enum's values are
// unique. ParseYAML reads every number of a file into a float64, and
// CompileSchema refuses a schema with a number a float64 can't hold, so none
// should reach it; but a number can be hostile in other ways, such as a
// mantissa a megabyte long, and this is the guard that holds whatever the
// cause. It covers only jsonschema's own code, so that a bug of TrueUp's
// still panics, where it can be seen.
func guard(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the JSON Schema library failed: %s", cutText(fmt.Sprint(r), 500))
		}
	}()
	return f()
}

// CheckSchemaFiles returns the error CompileSchema would give for schema
// files too many or too large, from a snapshot's list of them, so that a
// caller can refuse them before reading them: a source can list one file
// that Git stores once under many names, and reading them all would cost
// far more than fetching them did.
func CheckSchemaFiles(files []source.File) error {
	if err := checkSchemaCount(len(files)); err != nil {
		return err
	}
	for _, f := range files {
		if err := checkSchemaSize(f.Path, f.Size); err != nil {
			return err
		}
	}
	return nil
}

func checkSchemaCount(n int) error {
	if n > MaxSchemaFiles {
		return fmt.Errorf("the source holds %d JSON schemas, more than the %d TrueUp reads", n, MaxSchemaFiles)
	}
	return nil
}

func checkSchemaSize(path string, size int64) error {
	if size > MaxSchemaSize {
		return fmt.Errorf("schema %s holds %d bytes, more than the %d TrueUp reads", path, size, MaxSchemaSize)
	}
	return nil
}

// hugeNumber returns a number in a schema that a float64 can't hold, such as
// 1e9999999 or 1e-400, which would make jsonschema v6.0.3 panic, or that is
// written with more than 100 characters, if there is one. A zero holds,
// whatever its exponent.
func hugeNumber(doc any) (string, bool) {
	switch v := doc.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		mantissa, _, _ := strings.Cut(strings.ToLower(string(v)), "e")
		if err != nil || len(v) > 100 || math.IsInf(f, 0) || f == 0 && strings.ContainsAny(mantissa, "123456789") {
			return string(v), true
		}
	case map[string]any:
		for _, e := range v {
			if n, ok := hugeNumber(e); ok {
				return n, true
			}
		}
	case []any:
		for _, e := range v {
			if n, ok := hugeNumber(e); ok {
				return n, true
			}
		}
	}
	return "", false
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
	return nil, fmt.Errorf("TrueUp loads no schema from outside the source, such as %s", Quote(u))
}

// cutError is an error of jsonschema's, whose text can quote a schema, with
// its text cut short.
type cutError struct{ err error }

func (e cutError) Error() string { return cutText(e.err.Error(), 500) }
func (e cutError) Unwrap() error { return e.err }

// MaxSchemaProblems is the most problems Validate reports for one file.
// Files that fail the schema in more ways get a last problem that counts the
// rest.
const MaxSchemaProblems = 20

// Validate validates a file's root node, from ParseYAML, against the
// schema, and returns what it finds wrong, each at the node it concerns.
// The reasons are TrueUp's own, quoting values cut short, rather than
// jsonschema's, which list every value an enum allows.
func (s *Schema) Validate(path string, root *Node) []Problem {
	doc := root.JSON()
	var invalid error
	if err := guard(func() error { invalid = s.schema.Validate(doc); return nil }); err != nil {
		return []Problem{{Severity: Error, Path: path, Reason: "can't be checked against the schema: " + err.Error()}}
	}
	err := invalid
	if err == nil {
		return nil
	}
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return []Problem{{Severity: Error, Path: path, Reason: "doesn't satisfy the schema: " + cutText(err.Error(), 500)}}
	}
	var problems []Problem
	for _, leaf := range leaves(verr, nil) {
		problems = append(problems, schemaProblems(path, root, leaf)...)
	}
	// Sorted by everything, so that the same problem twice, from checks
	// such as anyOf's that meet at one node, is next to itself.
	slices.SortFunc(problems, func(a, b Problem) int {
		return cmp.Or(cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column),
			cmp.Compare(a.Field, b.Field), cmp.Compare(a.Reason, b.Reason))
	})
	problems = slices.Compact(problems)
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
			key, keyField := n, JoinField(field, name)
			for _, p := range n.Pairs {
				if p.Key.Value == name {
					key = p.Key
				}
			}
			out = append(out, at(key, keyField, "isn't a key the schema allows here"))
		}
		return out
	case *kind.Required:
		return []Problem{at(n, field, "needs "+ListQuoted(k.Missing)+", which the schema requires")}
	case *kind.DependentRequired:
		return []Problem{at(n, field, fmt.Sprintf("needs %s, which the schema requires with %s", ListQuoted(k.Missing), Quote(k.Prop)))}
	case *kind.Dependency:
		return []Problem{at(n, field, fmt.Sprintf("needs %s, which the schema requires with %s", ListQuoted(k.Missing), Quote(k.Prop)))}
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
		return fmt.Sprintf("is %s, but the schema wants %s", Describe(n), strings.Join(want, " or "))
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
		return fmt.Sprintf("%s doesn't match the schema's pattern %s", show(n), Quote(k.Want))
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
		return "can't be checked: the schema's references loop through " + Quote(k.URL)
	}
	if path := k.KeywordPath(); len(path) > 0 {
		return "doesn't satisfy the schema's " + Quote(strings.Join(path, "/"))
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
			n, field = v, JoinField(field, token)
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
		return Describe(n)
	}
	switch n.Tag {
	case TagStr:
		return Quote(n.Value)
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
		return Quote(v)
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
	return Quote(t)
}

// ListQuoted quotes names, as Quote does, and lists them: "a", "a and b",
// or "a, b and c", or nothing for none.
func ListQuoted(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = Quote(name)
	}
	switch len(quoted) {
	case 0:
		return "nothing"
	case 1:
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}
