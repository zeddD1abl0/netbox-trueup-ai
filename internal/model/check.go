package model

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

var (
	typeName       = regexp.MustCompile(`^[a-z]+\.[a-z]+$`)
	endpointPath   = regexp.MustCompile(`^/api/[a-z]+/[a-z0-9-]+/$`)
	fieldName      = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	choicesKey     = regexp.MustCompile(`^[a-z]+\.[A-Za-z]+\.[a-z_]+$`)
	sourceFile     = regexp.MustCompile(`^[a-z_]+(/[a-z0-9_]+)*\.py$`)
	sourceSymbol   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	errNoSuchField = errors.New("isn't one of its fields")
)

// Check reports every way in which specs are inconsistent, within one spec
// or between them: names that refer to nothing, rules that contradict each
// other, defaults NetBox wouldn't take, dependencies in a cycle. specs must
// be every spec TrueUp has, since specs refer to each other. Check returns
// nil when it finds nothing.
func Check(specs []*Spec) error {
	c := checker{
		specs:   map[string]*Spec{},
		child:   map[string]bool{},
		nested:  map[string]bool{},
		parents: map[string][]string{},
	}
	endpoints := map[string]string{}
	for _, s := range specs {
		if !typeName.MatchString(s.Type) {
			c.fail(s, "its type %q isn't app_label.model", s.Type)
		}
		if _, ok := c.specs[s.Type]; ok {
			c.fail(s, "there are two specs for it")
		}
		c.specs[s.Type] = s
		if s.Endpoint != "" {
			if other, ok := endpoints[s.Endpoint]; ok {
				c.fail(s, "its endpoint %s is %s's too", s.Endpoint, other)
			}
			endpoints[s.Endpoint] = s.Type
		}
		for _, col := range s.Children {
			c.child[col.Type] = true
			if col.Nested {
				c.nested[col.Type] = true
			}
			c.parents[col.Type] = append(c.parents[col.Type], s.Type)
		}
	}
	for _, s := range specs {
		c.check(s)
	}
	c.checkCycles(specs)
	return errors.Join(c.problems...)
}

type checker struct {
	specs    map[string]*Spec
	child    map[string]bool     // types some collection holds
	nested   map[string]bool     // types some nested collection holds
	parents  map[string][]string // the types whose collections hold a type
	problems []error
}

// isAncestor reports whether objects of type typ are held, directly or
// further down, in a collection of an object of type ancestor.
func (c *checker) isAncestor(ancestor, typ string) bool {
	seen := map[string]bool{}
	pending := []string{typ}
	for len(pending) > 0 {
		t := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, p := range c.parents[t] {
			if p == ancestor {
				return true
			}
			if !seen[p] {
				seen[p] = true
				pending = append(pending, p)
			}
		}
	}
	return false
}

func (c *checker) fail(s *Spec, format string, args ...any) {
	c.problems = append(c.problems, fmt.Errorf("%s: %s", s.Type, fmt.Sprintf(format, args...)))
}

func (c *checker) check(s *Spec) {
	switch {
	case s.Endpoint == "" && !c.nested[s.Type]:
		c.fail(s, "it has no endpoint, and no collection nests it")
	case s.Endpoint != "" && !endpointPath.MatchString(s.Endpoint):
		c.fail(s, "its endpoint %q isn't /api/<app>/<objects>/", s.Endpoint)
	case s.Endpoint != "" && c.nested[s.Type]:
		c.fail(s, "it has an endpoint, but a collection nests it")
	}
	if len(s.Sources) == 0 {
		c.fail(s, "it cites no NetBox source")
	}
	for _, src := range s.Sources {
		c.checkSource(s, "", src)
	}
	c.checkNames(s)
	for i := range s.Fields {
		c.checkField(s, &s.Fields[i], s.Fields[i].Name)
	}
	c.checkIdentity(s)
	c.checkDependencies(s)
	for i := range s.Children {
		c.checkCollection(s, &s.Children[i])
	}
	for _, ig := range s.Ignored {
		if ig.Reason == "" {
			c.fail(s, "it ignores %s without saying why", ig.Name)
		}
	}
}

// checkNames checks that fields, the fields of generic references,
// collections and ignored fields have names, and different ones.
func (c *checker) checkNames(s *Spec) {
	seen := map[string]bool{}
	use := func(name string) {
		switch {
		case !fieldName.MatchString(name):
			c.fail(s, "%q isn't a field name", name)
		case seen[name]:
			c.fail(s, "it names %s twice", name)
		}
		seen[name] = true
	}
	for _, f := range s.Fields {
		use(f.Name)
		if f.Ref != nil && f.Ref.Shape == ShapeTypeAndID {
			use(f.Ref.TypeField)
			use(f.Ref.IDField)
		}
	}
	for _, col := range s.Children {
		use(col.Name)
	}
	for _, ig := range s.Ignored {
		use(ig.Name)
	}
}

func (c *checker) checkSource(s *Spec, where string, src Source) {
	if !sourceFile.MatchString(src.File) || !sourceSymbol.MatchString(src.Symbol) {
		c.fail(s, "%sit cites %q in %q, which isn't a Python file and a name defined in it", where, src.Symbol, src.File)
	}
}

func (c *checker) checkIdentity(s *Spec) {
	if len(s.Identity) == 0 {
		if !c.child[s.Type] {
			c.fail(s, "it has no identity, and isn't a child type")
		}
	} else if i := slices.IndexFunc(s.Unique, func(u Unique) bool { return sameNames(u.Fields, s.Identity) }); i < 0 {
		c.fail(s, "its identity %v isn't one of its uniqueness rules", s.Identity)
	} else if s.Unique[i].Gap != "" {
		c.fail(s, "its identity %v is a uniqueness rule with a gap, which can't tell objects apart", s.Identity)
	}
	for _, name := range s.Identity {
		if s.Field(name) == nil {
			c.fail(s, "its identity names %s, which %v", name, errNoSuchField)
		}
	}
	for i, u := range s.Unique {
		if len(u.Fields) == 0 {
			c.fail(s, "a uniqueness rule has no fields")
		}
		for _, name := range u.Fields {
			switch f := s.Field(name); {
			case f == nil:
				c.fail(s, "a uniqueness rule names %s, which %v", name, errNoSuchField)
			case f.Nullable && u.Gap == "":
				c.fail(s, "the uniqueness rule %v has no gap, though %s can be null, which PostgreSQL treats as distinct", u.Fields, name)
			}
		}
		if slices.ContainsFunc(s.Unique[:i], func(v Unique) bool { return sameNames(u.Fields, v.Fields) }) {
			c.fail(s, "it has the uniqueness rule %v twice", u.Fields)
		}
		if u.Source != (Source{}) {
			c.checkSource(s, fmt.Sprintf("the uniqueness rule %v: ", u.Fields), u.Source)
		}
	}
}

// checkDependencies checks that a spec depends on the types its references
// refer to, apart from references within a parent, and on nothing that
// isn't a spec.
func (c *checker) checkDependencies(s *Spec) {
	for i, d := range s.DependsOn {
		switch {
		case d == s.Type:
			c.fail(s, "it depends on itself")
		case c.specs[d] == nil:
			c.fail(s, "it depends on %s, which has no spec", d)
		case slices.Contains(s.DependsOn[:i], d):
			c.fail(s, "it depends on %s twice", d)
		}
	}
	for _, f := range s.Fields {
		ref := f.Ref
		if f.Kind == KindSet && f.Elem != nil {
			ref = f.Elem.Ref // a set of references, such as tags
		}
		if ref == nil || ref.Within != "" {
			continue
		}
		for _, t := range ref.Types {
			if t != s.Type && !slices.Contains(s.DependsOn, t) {
				c.fail(s, "%s refers to a %s, which it doesn't depend on", f.Name, t)
			}
		}
	}
}

// checkCycles reports dependencies in a cycle, which no order of creation
// could satisfy.
func (c *checker) checkCycles(specs []*Spec) {
	const (
		unseen = iota
		visiting
		done
	)
	state := map[string]int{}
	var visit func(s *Spec, path []string)
	visit = func(s *Spec, path []string) {
		switch state[s.Type] {
		case visiting:
			start := slices.Index(path, s.Type)
			c.fail(s, "its dependencies are in a cycle: %s", strings.Join(append(path[start:], s.Type), " -> "))
			return
		case done:
			return
		}
		state[s.Type] = visiting
		for _, d := range s.DependsOn {
			if dep := c.specs[d]; dep != nil {
				visit(dep, append(path, s.Type))
			}
		}
		state[s.Type] = done
	}
	for _, s := range specs {
		visit(s, nil)
	}
}

func (c *checker) checkField(s *Spec, f *Field, name string) {
	fail := func(format string, args ...any) {
		c.fail(s, "%s %s", name, fmt.Sprintf(format, args...))
	}
	if f.Kind < KindString || f.Kind > KindSet {
		fail("has kind %s, which a field can't have", f.Kind)
		return
	}
	writable := !f.ReadOnly
	switch {
	case f.ReadOnly && (f.Required || f.CreateOnly || f.Default != nil || f.ImportDefault != nil || f.ImportRequired):
		fail("is read-only, so it can't be required or have a default")
	case f.Required && (f.Default != nil || f.ImportDefault != nil || f.ImportRequired):
		fail("is required, so it can't have a default")
	case writable && !f.Required && f.Default == nil && name == f.Name:
		fail("isn't required, so it needs the default NetBox gives it")
	}
	for _, d := range []struct {
		what string
		v    Value
	}{{"default", f.Default}, {"import default", f.ImportDefault}} {
		if d.v == nil {
			continue
		}
		if err := f.validate(d.v); err != nil {
			fail("has the %s %s, which %v", d.what, d.v, err)
		}
	}
	if f.Kind != KindString && (f.MaxLength != 0 || f.Pattern != "" || f.Trim) {
		fail("isn't a string, so it can't have a length, pattern or trimming")
	}
	if f.Kind != KindDecimal && (f.Digits != 0 || f.Places != 0 || f.Step != nil) {
		fail("isn't a decimal, so it can't have digits, places or a step")
	}
	if f.Kind != KindInteger && f.Kind != KindDecimal && (f.Min != nil || f.Max != nil || f.TighterBounds != (Source{})) {
		fail("isn't a number, so it can't have bounds")
	}
	if f.TighterBounds != (Source{}) {
		if f.Min == nil && f.Max == nil {
			fail("cites TighterBounds, but has no bounds")
		}
		c.checkSource(s, name+"'s bounds: ", f.TighterBounds)
	}
	if f.Kind != KindString && f.Kind != KindChoice && (f.Blank || f.BlankIsNull) {
		fail("isn't a string or choice, so it can't be blank")
	}
	if f.BlankIsNull && (!f.Blank || !f.Nullable) {
		fail("reads blank as null, so it must be blank and nullable")
	}
	if (f.Choices != nil) != (f.Kind == KindChoice) {
		fail("must have choices if, and only if, it is a choice")
	}
	if (f.Ref != nil) != (f.Kind == KindReference) {
		fail("must have a Ref if, and only if, it is a reference")
	}
	if (f.Elem != nil) != (f.Kind == KindSet) {
		fail("must have an element type if, and only if, it is a set")
	}
	if f.Source != (Source{}) {
		c.checkSource(s, name+": ", f.Source)
	}
	switch f.Kind {
	case KindString:
		if f.MaxLength < 0 {
			fail("has a negative length")
		}
		if f.Pattern != "" {
			if _, err := compile(f.Pattern); err != nil {
				fail("has a pattern that doesn't compile: %v", err)
			}
		}
	case KindDecimal:
		if f.Digits < 1 || f.Digits > maxDecimalDigits || f.Places < 0 || f.Places > f.Digits {
			fail("has %d digits and %d places, which isn't 1 to %d digits with up to that many places", f.Digits, f.Places, maxDecimalDigits)
		}
		if step, ok := f.Step.(Decimal); f.Step != nil && (!ok || step.Cmp(Decimal{}) <= 0) {
			fail("has a step that isn't a positive decimal")
		}
		c.checkBounds(fail, f)
	case KindInteger:
		c.checkBounds(fail, f)
	case KindChoice:
		c.checkChoices(fail, f)
	case KindReference:
		c.checkRef(s, fail, f)
	case KindBoolean, KindDate, KindChildren, KindNull:
		// Nothing more to check. A field can't hold children or only nulls,
		// which the start of checkField reports.
	case KindSet:
		if f.Elem != nil {
			if f.Elem.Kind == KindSet {
				fail("is a set of sets")
			} else {
				c.checkField(s, f.Elem, name+"'s elements")
			}
		}
	}
}

func (c *checker) checkBounds(fail func(string, ...any), f *Field) {
	for _, b := range []Value{f.Min, f.Max} {
		if b != nil && b.Kind() != f.Kind {
			fail("has a bound %s that isn't %s", b, f.Kind.withArticle())
			return
		}
	}
	if f.Min != nil && f.Max != nil && compare(f.Min, f.Max) > 0 {
		fail("has a minimum %s above its maximum %s", f.Min, f.Max)
	}
}

func (c *checker) checkChoices(fail func(string, ...any), f *Field) {
	if f.Choices == nil {
		return
	}
	if f.Source == (Source{}) {
		fail("is a choice field, so it needs a Source citing its choice set")
	}
	if len(f.Choices.Values) == 0 {
		fail("has no choices: is it missing from the generated choices?")
	}
	for i, v := range f.Choices.Values {
		if v == "" {
			fail("lists the blank choice, which Blank stands for")
		}
		if slices.Contains(f.Choices.Values[:i], v) {
			fail("lists the choice %q twice", v)
		}
	}
	if k := f.Choices.Configurable; k != "" && !choicesKey.MatchString(k) {
		fail("names %q as its FIELD_CHOICES key, which isn't app.Model.field", k)
	}
}

func (c *checker) checkRef(s *Spec, fail func(string, ...any), f *Field) {
	r := f.Ref
	if r == nil {
		return
	}
	if len(r.Types) == 0 {
		fail("refers to no type")
	}
	for i, t := range r.Types {
		target := c.specs[t]
		switch {
		case target == nil:
			fail("refers to %s, which has no spec", t)
		case slices.Contains(r.Types[:i], t):
			fail("refers to %s twice", t)
		case r.Within == "" && len(target.Identity) == 0:
			fail("refers to %s, which has no identity to name it by", t)
		}
	}
	switch r.Shape {
	case ShapeNested, ShapeID:
		if r.TypeField != "" || r.IDField != "" {
			fail("has type and ID fields, which only a generic reference has")
		}
	case ShapeTypeAndID:
		if r.TypeField == "" || r.IDField == "" {
			fail("is a generic reference without its type and ID fields")
		}
		if f.Source == (Source{}) {
			fail("is a generic reference, whose types NetBox's schema doesn't list, so it needs a Source that does")
		}
	default:
		fail("has an unknown shape %d", r.Shape)
	}
	if r.Within == "" {
		return
	}
	parent := c.specs[r.Within]
	if parent == nil {
		fail("refers within %s, which has no spec", r.Within)
		return
	}
	if !c.isAncestor(r.Within, s.Type) {
		fail("refers within a %s, which doesn't hold a %s", r.Within, s.Type)
	}
	for _, t := range r.Types {
		if !slices.ContainsFunc(parent.Children, func(col Collection) bool { return col.Type == t }) {
			fail("refers to a %s within a %s, which has no collection of them", t, r.Within)
		}
	}
}

func (c *checker) checkCollection(s *Spec, col *Collection) {
	fail := func(format string, args ...any) {
		c.fail(s, "the collection %s %s", col.Name, fmt.Sprintf(format, args...))
	}
	child := c.specs[col.Type]
	if child == nil {
		fail("holds %s, which has no spec", col.Type)
		return
	}
	if col.Source != (Source{}) {
		c.checkSource(s, "the collection "+col.Name+": ", col.Source)
	}
	switch {
	case col.Nested && col.Parent != "":
		fail("is nested, so its children have no field for their parent")
	case !col.Nested && child.Endpoint == "":
		fail("holds %s, which has no endpoint, so the collection must be nested", col.Type)
	case !col.Nested:
		p := child.Field(col.Parent)
		switch {
		case p == nil:
			fail("names %s as its children's parent field, which %v", col.Parent, errNoSuchField)
		case p.Kind != KindReference || p.Ref == nil || !slices.Equal(p.Ref.Types, []string{s.Type}):
			fail("names %s as its children's parent field, which isn't a reference to a %s", col.Parent, s.Type)
		}
	}
	if len(col.Key) == 0 {
		fail("has no key")
	}
	// A key without a gap claims that NetBox keeps it unique among one
	// parent's children: a uniqueness rule of the children's, over the
	// parent, shared and key fields, must say so, or for a nested collection
	// a Source.
	switch {
	case col.Gap != "":
	case col.Nested:
		if col.Source == (Source{}) {
			fail("is nested and has no gap, so it needs a Source saying where NetBox keeps its key unique")
		}
	default:
		scope := append([]string{col.Parent}, col.Shared...)
		scope = append(scope, col.Key...)
		if !slices.ContainsFunc(child.Unique, func(u Unique) bool { return isSubset(u.Fields, scope) && holdsWithin(u, child, col) }) {
			fail("has no gap, but no uniqueness rule of %s's makes its key %v unique among one parent's children", col.Type, col.Key)
		}
	}
	for _, name := range col.Key {
		switch {
		case child.Field(name) == nil:
			fail("has the key field %s, which %v of %s", name, errNoSuchField, col.Type)
		case name == col.Parent:
			fail("has its parent field %s in its key", name)
		}
	}
	for _, name := range col.Shared {
		switch {
		case child.Field(name) == nil || s.Field(name) == nil:
			fail("shares %s, which isn't a field of both %s and %s", name, s.Type, col.Type)
		case name == col.Parent:
			fail("shares its parent field %s", name)
		}
	}
}

// holdsWithin reports whether a uniqueness rule of a collection's children
// holds among one parent's children: it has no gap, or its gap comes only
// from the children's parent field being nullable, which it never is in the
// collection. A Gap is free text, so this takes every gap to come from null
// values, as all of NetBox's rules that specs cite do. A rule that holds only
// under a condition, such as a UniqueConstraint with condition=, needs the
// collection to state the gap itself.
func holdsWithin(u Unique, child *Spec, col *Collection) bool {
	if u.Gap == "" {
		return true
	}
	for _, name := range u.Fields {
		if f := child.Field(name); f != nil && f.Nullable && name != col.Parent {
			return false
		}
	}
	return true
}

// isSubset reports whether every name in a is in b.
func isSubset(a, b []string) bool {
	return !slices.ContainsFunc(a, func(n string) bool { return !slices.Contains(b, n) })
}

// sameNames reports whether a and b hold the same names in any order.
func sameNames(a, b []string) bool {
	return maps.Equal(nameSet(a), nameSet(b))
}

func nameSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}
