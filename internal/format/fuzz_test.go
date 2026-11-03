package format

import (
	"errors"
	"strings"
	"testing"
)

// FuzzParseYAML checks that no file makes the reader, or validation of what
// it reads, panic or break its promises: a mapping at the top, no key twice,
// values in their canonical forms, and errors that say where and stay short.
func FuzzParseYAML(f *testing.F) {
	for _, seed := range []string{
		"manufacturer: Huawei\nmodel: NE8000-X8\npart_number: 02351930\nu_height: 16\nis_full_depth: true\n",
		"name: abc\nsize: 1.5\nweight: 12.21\nkind: a\nparts: [{name: x, count: 10}]\n",
		"a: &x 1\nb: *x\n", "a: 1\n<<: {b: 2}\n", "a: !!str 1\n", "a: 1\na: 2\n", "a: [1, .inf, 1:30, 0x1F, 2024-01-01]\n",
		"---\na: 1\n---\nb: 2\n", "? [a]\n: 1\n", "a: |\n  text\n", "a: \"\\u202e\"\n", "a: 1e9999999\n", "a: 1.0e+999\n",
		"{a: {b: {c: {d: {e: {f: [[[[[[[[[[[[1]]]]]]]]]]]]}}}}}}\n",
	} {
		f.Add([]byte(seed))
	}
	s, err := CompileSchema(testSchemas, "schema/thing.json")
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		root, err := ParseYAML(data, DefinitionLimits)
		if err != nil {
			perr, ok := errors.AsType[*ParseError](err)
			switch {
			case !ok:
				t.Fatalf("ParseYAML = %T %v, want a *ParseError", err, err)
			case perr.Reason == "":
				t.Fatal("ParseYAML's error has no reason")
			case len(err.Error()) > 1000:
				t.Fatalf("ParseYAML's error is %d bytes long", len(err.Error()))
			}
			return
		}
		if root.Kind != Mapping {
			t.Fatalf("ParseYAML returned a %d, not a mapping", root.Kind)
		}
		check(t, root, 1)
		for _, p := range s.Validate("x.yaml", root) {
			if strings.Contains(p.Reason, "validator failed") || p.Reason == "" || len(p.String()) > 1000 {
				t.Fatalf("Validate gave %q", p)
			}
		}
	})
}

// check checks a node and those below it.
func check(t *testing.T, n *Node, depth int) {
	t.Helper()
	if depth > DefinitionLimits.Depth || n.Line < 1 || n.Column < 1 {
		t.Fatalf("node at %d:%d, depth %d", n.Line, n.Column, depth)
	}
	switch n.Kind {
	case Mapping:
		seen := map[string]bool{}
		for _, p := range n.Pairs {
			if p.Key.Kind != Scalar || p.Key.Tag != TagStr || seen[p.Key.Value] {
				t.Fatalf("key %q at %d:%d is a %s, or given twice", p.Key.Value, p.Key.Line, p.Key.Column, p.Key.Tag)
			}
			seen[p.Key.Value] = true
			check(t, p.Value, depth+1)
		}
	case Sequence:
		for _, item := range n.Items {
			check(t, item, depth+1)
		}
	case Scalar:
		if _, _, err := resolvePlain(n.Value); n.Tag != TagStr && err != nil {
			t.Fatalf("scalar %s %q doesn't read back: %v", n.Tag, n.Value, err)
		}
		switch n.Tag {
		case TagStr:
		case TagBool:
			if n.Value != "true" && n.Value != "false" {
				t.Fatalf("boolean %q", n.Value)
			}
		case TagNull:
			if n.Value != "" {
				t.Fatalf("null %q", n.Value)
			}
		case TagInt, TagFloat:
			if tag, v, err := resolvePlain(n.Value); err != nil || v != n.Value || n.Tag == TagInt && tag != TagInt {
				t.Fatalf("number %q isn't canonical: %s %q %v", n.Value, tag, v, err)
			}
		default:
			t.Fatalf("scalar of tag %d", n.Tag)
		}
	default:
		t.Fatalf("node of kind %d", n.Kind)
	}
}
