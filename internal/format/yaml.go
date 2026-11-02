package format

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// Limits bound what reading one YAML file may cost. go-yaml builds a file's
// whole node tree before anything can count it, at about 100 bytes of memory
// for each byte of a hostile file, so Size is the bound that matters; Depth
// and Nodes bound what the tree costs once built.
type Limits struct {
	// Size is the most bytes the file may hold.
	Size int
	// Depth is the most levels of nodes the file may nest, the top mapping
	// and the keys and values at the bottom included: a mapping of a list
	// of mappings of scalars has 4.
	Depth int
	// Nodes is the most mappings, lists, keys and values the file may hold.
	Nodes int
}

// DefinitionLimits are the limits on a file of definitions: far above
// devicetype-library's largest device type, of 18.7 KB, 4 levels and 2,229
// nodes, and far below what would make reading it costly
// (docs/adr/0024-read-source-files-as-pyyaml-does.md).
var DefinitionLimits = Limits{Size: 256 << 10, Depth: 16, Nodes: 50000}

// NodeKind is the kind of a YAML node.
type NodeKind uint8

// Node kinds.
const (
	Mapping NodeKind = iota + 1
	Sequence
	Scalar
)

// Node is a node of a YAML file as ParseYAML reads it: a mapping, a list or
// a scalar, with where it starts in the file.
type Node struct {
	Kind NodeKind
	// Line and Column are where the node starts, from 1.
	Line, Column int
	// Tag is a scalar's type, as PyYAML's SafeLoader resolves it.
	Tag Tag
	// Value is a scalar's value in its canonical form: a string's text, a
	// whole number's decimal digits, a number's shortest decimal form,
	// "true" or "false", or "" for null.
	Value string
	// Pairs are a mapping's keys and values, in the file's order. Every key
	// is a string scalar, and no two are the same.
	Pairs []Pair
	// Items are a list's items.
	Items []*Node
}

// Pair is a key and its value in a mapping.
type Pair struct {
	Key, Value *Node
}

// Get returns the value of key in a mapping, or nil if the mapping has no
// such key or n isn't a mapping.
func (n *Node) Get(key string) *Node {
	if n == nil {
		return nil
	}
	for _, p := range n.Pairs {
		if p.Key.Value == key {
			return p.Value
		}
	}
	return nil
}

// JSON returns the node as encoding/json would decode the same data with
// UseNumber, for validating against a JSON schema: map[string]any, []any,
// string, json.Number, bool or nil. Numbers stay exact.
func (n *Node) JSON() any {
	switch n.Kind {
	case Mapping:
		m := make(map[string]any, len(n.Pairs))
		for _, p := range n.Pairs {
			m[p.Key.Value] = p.Value.JSON()
		}
		return m
	case Sequence:
		s := make([]any, len(n.Items))
		for i, item := range n.Items {
			s[i] = item.JSON()
		}
		return s
	default:
		return n.scalarJSON()
	}
}

func (n *Node) scalarJSON() any {
	switch n.Tag {
	case TagInt, TagFloat:
		return json.Number(n.Value)
	case TagBool:
		return n.Value == "true"
	case TagNull:
		return nil
	default:
		return n.Value
	}
}

// ParseError is a reason a YAML file can't be read, and where in it.
type ParseError struct {
	// Line and Column are where, from 1, or 0 when the problem concerns
	// the whole file.
	Line, Column int
	// Field is where in the file's structure, such as interfaces[3].name,
	// or "" for the file itself.
	Field  string
	Reason string
}

func (e *ParseError) Error() string {
	var b strings.Builder
	if e.Line > 0 {
		fmt.Fprintf(&b, "line %d", e.Line)
		if e.Column > 0 {
			fmt.Fprintf(&b, ", column %d", e.Column)
		}
		b.WriteString(": ")
	}
	if e.Field != "" {
		b.WriteString(e.Field)
		b.WriteString(": ")
	}
	b.WriteString(e.Reason)
	return b.String()
}

// Problem returns the error as a problem with the file at path.
func (e *ParseError) Problem(path string) Problem {
	return Problem{Severity: Error, Path: path, Line: e.Line, Column: e.Column, Field: e.Field, Reason: e.Reason}
}

// ParseYAML reads one YAML file of definitions, which must hold one
// document, a mapping, within limits. It reads every scalar as PyYAML's
// SafeLoader does, which NetBox's own YAML import and devicetype-library's
// tests use, rather than by YAML 1.2's rules: yes is true, 010 is 8, and 1e3
// is text (docs/adr/0024-read-source-files-as-pyyaml-does.md).
//
// It refuses what devicetype-library never uses and what would make a file
// mean something other than it seems or cost more than it looks: anchors,
// aliases and merge keys, tags, keys that aren't text or that a mapping
// gives twice, more than one document, and text that isn't UTF-8. Its error
// is a *ParseError.
func ParseYAML(data []byte, limits Limits) (*Node, error) {
	if len(data) > limits.Size {
		return nil, &ParseError{Reason: fmt.Sprintf("the file holds %d bytes, more than the %d a file of definitions may", len(data), limits.Size)}
	}
	// The byte order mark, if any, is dropped, so that go-yaml's columns
	// count from the line's first character, as text's do.
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		return nil, &ParseError{Reason: "the file isn't UTF-8 text"}
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &ParseError{Reason: "the file holds no YAML document"}
		}
		return nil, parseError(err)
	}
	var next yaml.Node
	switch err := dec.Decode(&next); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return nil, parseError(err)
	default:
		return nil, &ParseError{Line: next.Line, Column: next.Column, Reason: "the file holds more than one YAML document"}
	}
	if len(doc.Content) != 1 {
		return nil, &ParseError{Reason: "the file holds no YAML document"}
	}
	r := reader{limits: limits, lines: newLines(data)}
	root, err := r.node(doc.Content[0], "", 1)
	if err != nil {
		return nil, err
	}
	if root.Kind != Mapping {
		return nil, &ParseError{Line: root.Line, Column: root.Column, Reason: "the file holds " + Describe(root) + ", not a mapping of keys to values"}
	}
	return root, nil
}

// goYAMLLine finds the line in go-yaml's errors, such as "yaml: line 3: did
// not find expected key".
var goYAMLLine = regexp.MustCompile(`\Ayaml: line ([0-9]+): `)

// parseError turns an error of go-yaml's parser into a *ParseError. go-yaml's
// messages describe the syntax, not the content, but are cut short anyway.
func parseError(err error) *ParseError {
	msg := err.Error()
	e := &ParseError{}
	if m := goYAMLLine.FindStringSubmatch(msg); m != nil {
		e.Line, _ = strconv.Atoi(m[1])
		msg = msg[len(m[0]):]
	}
	e.Reason = "isn't valid YAML: " + cutText(strings.TrimPrefix(msg, "yaml: "), 200)
	return e
}

// reader turns go-yaml's nodes into Nodes, counting them.
type reader struct {
	limits Limits
	nodes  int
	lines  *lines
}

func (r *reader) node(n *yaml.Node, field string, depth int) (*Node, error) {
	fail := func(format string, args ...any) error {
		return &ParseError{Line: n.Line, Column: n.Column, Field: field, Reason: fmt.Sprintf(format, args...)}
	}
	r.nodes++
	switch {
	case r.nodes > r.limits.Nodes:
		return nil, fail("the file holds more than %d keys, values and items, which a file of definitions may", r.limits.Nodes)
	case depth > r.limits.Depth:
		return nil, fail("the file nests more than %d levels deep, which a file of definitions may", r.limits.Depth)
	case n.Kind == yaml.AliasNode:
		return nil, fail("YAML aliases (*) aren't supported: write the value out")
	case n.Anchor != "":
		return nil, fail("YAML anchors (&) aren't supported")
	case n.Style&yaml.TaggedStyle != 0:
		return nil, fail("YAML tags, such as %s, aren't supported: quote a value to make it text", Quote(n.Tag))
	case r.lines.at(n.Line, n.Column) == '!':
		// security: go-yaml keeps no trace of the non-specific tag !, which
		// makes PyYAML resolve a quoted scalar as if it were plain: ! "123"
		// is text to go-yaml, and 123 to NetBox's import. A node starts at
		// its tag, and nothing else starts with !, so this finds every tag.
		return nil, fail("YAML tags, such as !, aren't supported: quote a value to make it text")
	}
	out := &Node{Line: n.Line, Column: n.Column}
	switch n.Kind {
	case yaml.ScalarNode:
		out.Kind = Scalar
		if n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			out.Tag, out.Value = TagStr, n.Value
			return out, nil
		}
		tag, value, err := resolvePlain(n.Value)
		if err != nil {
			return nil, fail("%s", err)
		}
		out.Tag, out.Value = tag, value
	case yaml.SequenceNode:
		out.Kind = Sequence
		out.Items = make([]*Node, 0, len(n.Content))
		for i, item := range n.Content {
			child, err := r.node(item, field+"["+strconv.Itoa(i)+"]", depth+1)
			if err != nil {
				return nil, err
			}
			out.Items = append(out.Items, child)
		}
	case yaml.MappingNode:
		out.Kind = Mapping
		if len(n.Content)%2 != 0 {
			return nil, fail("a mapping has a key without a value")
		}
		out.Pairs = make([]Pair, 0, len(n.Content)/2)
		seen := make(map[string]*Node, len(n.Content)/2)
		for i := 0; i < len(n.Content); i += 2 {
			key, err := r.key(n.Content[i], field, depth+1)
			if err != nil {
				return nil, err
			}
			if first, ok := seen[key.Value]; ok {
				return nil, &ParseError{
					Line: key.Line, Column: key.Column, Field: JoinField(field, key.Value),
					Reason: fmt.Sprintf("the key is given twice, on lines %d and %d", first.Line, key.Line),
				}
			}
			seen[key.Value] = key
			value, err := r.node(n.Content[i+1], JoinField(field, key.Value), depth+1)
			if err != nil {
				return nil, err
			}
			out.Pairs = append(out.Pairs, Pair{Key: key, Value: value})
		}
	default:
		return nil, fail("the file holds a YAML node of a kind TrueUp doesn't read")
	}
	return out, nil
}

// key reads a mapping's key, which must be text.
func (r *reader) key(n *yaml.Node, field string, depth int) (*Node, error) {
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		return nil, &ParseError{Line: n.Line, Column: n.Column, Field: field, Reason: "a key is a mapping or a list, not text"}
	}
	key, err := r.node(n, field, depth)
	if err != nil {
		return nil, err
	}
	if key.Tag != TagStr {
		return nil, &ParseError{
			Line: n.Line, Column: n.Column, Field: field,
			Reason: fmt.Sprintf("the key %s reads as %s, not text: quote it", Quote(n.Value), key.Tag),
		}
	}
	return key, nil
}

// Describe says what a node is, for a problem's reason: a mapping, a list,
// text, a whole number, and so on.
func Describe(n *Node) string {
	switch n.Kind {
	case Mapping:
		return "a mapping"
	case Sequence:
		return "a list"
	default:
		return n.Tag.String()
	}
}

// JoinField adds a key to a field path: interfaces[3] and name make
// interfaces[3].name. A key that isn't plain letters, digits, - and _ is
// quoted, cut short.
func JoinField(field, key string) string {
	if !plainKey(key) {
		key = "[" + Quote(key) + "]"
		return field + key
	}
	if field == "" {
		return key
	}
	return field + "." + key
}

func plainKey(key string) bool {
	if key == "" || len(key) > 64 {
		return false
	}
	for i := range len(key) {
		if !plainKeyByte(key[i]) {
			return false
		}
	}
	return true
}

func plainKeyByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_'
}

// lines finds the character at a line and column of a file, as go-yaml
// counts them: from 1, in characters, with lines broken by line feeds,
// carriage returns, either alone or together, and YAML 1.1's other line
// breaks, U+0085, U+2028 and U+2029.
type lines struct {
	data   []byte
	starts []int         // the offset each line starts at
	runes  map[int][]int // for a line with characters beyond ASCII, each one's offset
}

func newLines(data []byte) *lines {
	l := &lines{data: data, starts: []int{0}, runes: map[int][]int{}}
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		switch {
		case r == '\r' && i+1 < len(data) && data[i+1] == '\n':
			size = 2
			fallthrough
		case r == '\r', r == '\n', r == 0x85, r == 0x2028, r == 0x2029:
			l.starts = append(l.starts, i+size)
		}
		i += size
	}
	return l
}

// at returns the character at a line and column, from 1, or 0 if there is
// none.
func (l *lines) at(line, column int) rune {
	if line < 1 || line > len(l.starts) || column < 1 {
		return 0
	}
	start, end := l.starts[line-1], len(l.data)
	if line < len(l.starts) {
		end = l.starts[line]
	}
	text := l.data[start:end]
	offsets, ok := l.runes[line]
	if !ok {
		if isASCII(text) {
			offsets = nil
		} else {
			for i := range string(text) {
				offsets = append(offsets, i)
			}
		}
		l.runes[line] = offsets
	}
	i := column - 1
	if offsets != nil {
		if i >= len(offsets) {
			return 0
		}
		i = offsets[i]
	}
	if i >= len(text) {
		return 0
	}
	r, _ := utf8.DecodeRune(text[i:])
	return r
}

func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
