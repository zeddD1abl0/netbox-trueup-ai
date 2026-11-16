// Package compare is TrueUp's comparison engine (docs/architecture.md,
// "Comparison engine"). It matches definitions to the objects NetBox holds,
// by NetBox's own uniqueness rules, and compares each definition with the
// object it matches, field by field, side by side
// (docs/adr/0025-match-by-netbox-uniqueness-rules.md). It does no I/O, and
// reads no clock.
//
// Definitions come from sources, and objects from NetBox, so both are
// untrusted: Compare checks what it relies on, and fails rather than guess.
package compare

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// Outcome is what importing a definition would do (docs/architecture.md,
// "Matching NetBox"). Its values are part of the CLI's JSON output.
type Outcome string

const (
	// Candidate means the definition's identity matches one NetBox object,
	// and no other uniqueness rule collides with another: importing links
	// the definition to it.
	Candidate Outcome = "candidate"
	// Absent means nothing in NetBox matches: importing creates the object.
	Absent Outcome = "absent"
	// Conflict means the definition collides with a NetBox object that
	// isn't its match, under one of NetBox's uniqueness rules, so that
	// NetBox couldn't hold both: a person decides.
	Conflict Outcome = "conflict"
)

// Definition is one source's definition of an object.
type Definition struct {
	// Source is the name of the source that defines it.
	Source string
	format.Definition
}

// Observed is an object NetBox holds, as model's Spec.DecodeAPI read it from
// NetBox's API.
type Observed struct {
	ID     int64
	Object model.Object
}

// Result is the comparison of one definition with NetBox.
type Result struct {
	// Type and Identity are the definition's: its object type, and the
	// values of its identity fields, as the source gives them.
	Type     string
	Identity map[string]model.Value
	Outcome  Outcome
	// NetBoxID is the NetBox object the definition's identity matches, or 0
	// if it matches none, or more than one.
	NetBoxID int64
	// Collisions are the NetBox objects, other than the match, that the
	// definition collides with, sorted by rule and ID. A conflict has at
	// least one, or a reference with Candidates.
	Collisions []Collision
	// Unresolved lists the definition's references to objects that NetBox
	// doesn't have, such as a device type's manufacturer.
	Unresolved []Unresolved
	// Columns are the versions of the object compared: the definition, and
	// NetBox's object if the identity matches one.
	Columns []Column
	// Rows hold every field that any version sets, in the order of the
	// type's spec, with a cell for each column.
	Rows []Row
	// Differences counts the rows that differ and are managed.
	Differences int
}

// InStep reports whether the definition matches a NetBox object that holds
// what it defines: a candidate without differences.
func (r Result) InStep() bool {
	return r.Outcome == Candidate && r.Differences == 0
}

// Collision is a NetBox object that a definition collides with under one of
// NetBox's uniqueness rules, or a child of the object the definition
// matches that shares its key with another, which NetBox's rules forbid.
type Collision struct {
	// Path is where the child is, such as interfaces["eth0"], or empty for
	// an object that stands on its own.
	Path Path
	// Rule is the uniqueness rule's fields, such as manufacturer and slug,
	// or a child's parent and key fields.
	Rule []string
	// ID and Identity are the NetBox object's: for a child, its key.
	ID       int64
	Identity map[string]model.Value
}

// Unresolved is a reference to an object that NetBox doesn't have, or has
// more than one of.
type Unresolved struct {
	// Field is the reference's field, such as manufacturer.
	Field string
	// Ref is the reference, by its natural key.
	Ref model.Reference
	// Candidates are the NetBox objects that all have the reference's key,
	// sorted, when NetBox has more than one, which its uniqueness rules
	// forbid. The definition is then a conflict.
	Candidates []int64
	// Outcome is that of the same source's definition of the object
	// referred to, compared at the same time, or "" if it doesn't have
	// exactly one.
	Outcome Outcome
}

// ColumnKind says what version of an object a column holds. Its values are
// part of the CLI's JSON output.
type ColumnKind string

// The kinds of column. The live edit and the desired object join them in
// later milestones.
const (
	DefinitionColumn ColumnKind = "definition"
	NetBoxColumn     ColumnKind = "netbox"
)

// Column is one version of an object.
type Column struct {
	Kind ColumnKind
	// Source, Path, DerivedFrom and Unmapped are a definition's: its
	// source, its file or the files it is derived from, and the lists of
	// children it holds that TrueUp doesn't compare yet.
	Source      string
	Path        string
	DerivedFrom []string
	Unmapped    map[string]int
	// ID is a NetBox object's.
	ID int64
}

// Step is one step of a row's path: a field, or a collection, or a child
// in the collection the step before names, by its key.
type Step struct {
	// Field is a field's or a collection's name, or "" for a child.
	Field string
	// Key is a child's key, such as {"name": "eth0"}, or nil for a field.
	Key map[string]model.Value
}

// Path says which field a row holds, or which child: a field of the
// object, such as model; a child, such as interfaces["eth0"]; or a field
// of a child, such as interfaces["eth0"].type.
type Path []Step

// String writes the path as its fields joined by dots, with each child's
// key in brackets: its value, or for a key of several fields, each as
// name=value.
func (p Path) String() string {
	var b strings.Builder
	for i, s := range p {
		if s.Key == nil {
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString(s.Field)
			continue
		}
		b.WriteByte('[')
		names := slices.Sorted(maps.Keys(s.Key))
		for j, name := range names {
			if j > 0 {
				b.WriteString(", ")
			}
			if len(names) > 1 {
				b.WriteString(name)
				b.WriteByte('=')
			}
			b.WriteString(describe(s.Key[name]))
		}
		b.WriteByte(']')
	}
	return b.String()
}

// childPath is the path of a child in a collection.
func childPath(collection string, key map[string]model.Value) Path {
	return Path{{Field: collection}, {Key: key}}
}

// Row is one field of an object, side by side in each version, or whether
// each version has one of its children.
type Row struct {
	Path Path
	// Cells hold the field's value in each column, normalized as the
	// type's spec says, or nil where that version doesn't set it. A child's
	// row, whose path ends at its key, holds true or false in each: whether
	// that version has the child.
	Cells []model.Value
	// NetBoxID is, on a child's row, the ID of NetBox's child, or 0 if
	// NetBox has none, or more than one.
	NetBoxID int64
	// Differs says that two of the versions that set the field hold
	// different values.
	Differs bool
	// Managed says the definition manages the field: it sets it, and the
	// field isn't one the format derived, which is used only to create the
	// object (docs/adr/0009-source-kinds-and-format-adapters.md). A child's
	// row is managed when the definition has the child: TrueUp never
	// removes a child that only NetBox has.
	Managed bool
	// Derived says the format derived the definition's value rather than
	// read it, such as a manufacturer's slug.
	Derived bool
}

// Compare compares each definition with the NetBox objects observed, and
// returns a result for each, in the same order. specs must describe every
// type of definition and observed object, and every type they refer to.
// observed must hold every object NetBox has that a definition could match
// or collide with: every manufacturer, and the device types of each
// manufacturer that Referenced finds the definitions refer to; and every
// child of each object that Matched finds the definitions match, such as a
// device type's interface templates.
//
// It fails, rather than guess, if a definition or an object is of a type
// without a spec, or doesn't hold its fields as its spec describes them (see
// checkObserved and checkDefinition), if NetBox listed an object twice, or
// if a child observed belongs to no object observed.
func Compare(specs []*model.Spec, defs []Definition, observed []Observed) ([]Result, error) {
	ix, err := newIndex(specs, observed)
	if err != nil {
		return nil, err
	}
	results := make([]Result, len(defs))
	for i := range defs {
		r, err := ix.compare(&defs[i])
		if err != nil {
			return nil, err
		}
		results[i] = r
	}
	fillOutcomes(results)
	return results, nil
}

// Referenced returns, sorted, the NetBox objects of type typ, among
// observed, that the definitions' references to typ resolve to, as Compare
// resolves them, each candidate included where NetBox has several: so that
// a caller reads the objects that refer to exactly those, such as the device
// types of the manufacturers that device type definitions name.
func Referenced(specs []*model.Spec, defs []Definition, observed []Observed, typ string) ([]int64, error) {
	ix, err := newIndex(specs, observed)
	if err != nil {
		return nil, err
	}
	found := map[int64]bool{}
	for i := range defs {
		d := &defs[i]
		s := ix.specs[d.Object.Type]
		if s == nil {
			return nil, fmt.Errorf("a definition in %s is a %s, which has no spec", d.Source, strconv.Quote(d.Object.Type))
		}
		for _, f := range s.Fields {
			ref, ok := d.Object.Fields[f.Name].(model.Reference)
			if !ok || ref.Type != typ || f.Ref == nil || f.Ref.Within != "" {
				continue
			}
			ids, err := ix.find(ref)
			if err != nil {
				return nil, fmt.Errorf("%s's definition %s: %s %w", d.Source, describeDefinition(d), f.Name, err)
			}
			for _, id := range ids {
				found[id] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(found)), nil
}

// newIndex indexes the objects observed, which it checks.
func newIndex(specs []*model.Spec, observed []Observed) (*index, error) {
	ix := &index{
		specs:    map[string]*model.Spec{},
		objects:  map[string]map[int64]model.Object{},
		rules:    map[string][]map[string][]int64{},
		holders:  map[string][]holder{},
		children: map[string]map[int64]map[string][]int64{},
	}
	for _, s := range specs {
		ix.specs[s.Type] = s
		ix.objects[s.Type] = map[int64]model.Object{}
		ix.rules[s.Type] = make([]map[string][]int64, len(s.Unique))
		for i := range s.Unique {
			ix.rules[s.Type][i] = map[string][]int64{}
		}
		for i := range s.Children {
			col := &s.Children[i]
			ix.holders[col.Type] = append(ix.holders[col.Type], holder{parent: s, col: col})
		}
	}
	for _, o := range observed {
		if err := ix.add(o); err != nil {
			return nil, err
		}
	}
	// A child whose parent wasn't listed couldn't be compared with it.
	for _, typ := range slices.Sorted(maps.Keys(ix.children)) {
		for _, id := range slices.Sorted(maps.Keys(ix.children[typ])) {
			if _, ok := ix.objects[typ][id]; !ok {
				return nil, fmt.Errorf("NetBox listed children of %s #%d, but not the %s itself", typ, id, typ)
			}
		}
	}
	return ix, nil
}

// index holds the objects NetBox has, by type and ID, and by the values of
// each uniqueness rule, and each object's children.
type index struct {
	specs   map[string]*model.Spec
	objects map[string]map[int64]model.Object
	// rules holds, by type and then by the index of the rule in the spec's
	// Unique, the IDs of the objects with each rule's values.
	rules map[string][]map[string][]int64
	// holders lists, by a child's type, the collections that hold it.
	holders map[string][]holder
	// children holds, by a parent's type, its ID, and the name of its
	// collection, the IDs of its children, in the order NetBox listed them.
	children map[string]map[int64]map[string][]int64
}

// holder is a collection of a parent type.
type holder struct {
	parent *model.Spec
	col    *model.Collection
}

func (ix *index) add(o Observed) error {
	s := ix.specs[o.Object.Type]
	switch {
	case s == nil:
		return fmt.Errorf("NetBox's object #%d is a %s, which has no spec", o.ID, strconv.Quote(o.Object.Type))
	case o.ID <= 0:
		return fmt.Errorf("NetBox's %s has the ID %d, which isn't one", s.Type, o.ID)
	}
	if _, ok := ix.objects[s.Type][o.ID]; ok {
		return fmt.Errorf("NetBox listed %s #%d twice", s.Type, o.ID)
	}
	if err := checkObserved(s, o.Object.Fields); err != nil {
		return fmt.Errorf("NetBox's %s #%d %w", s.Type, o.ID, err)
	}
	if err := ix.addChild(s, o); err != nil {
		return err
	}
	ix.objects[s.Type][o.ID] = o.Object
	// NetBox's constraints compare what it stores, so the object's values
	// count as NetBox returns them, not normalized.
	for i, u := range s.Unique {
		if key, ok := ruleKey(u, o.Object.Fields); ok {
			ix.rules[s.Type][i][key] = append(ix.rules[s.Type][i][key], o.ID)
		}
	}
	return nil
}

// addChild files an object that some type's collection holds under its
// parent, by the field that refers to it. It fails for one whose parent
// field refers to no object, since only a parent's children are listed.
func (ix *index) addChild(s *model.Spec, o Observed) error {
	hs := ix.holders[s.Type]
	if len(hs) == 0 {
		return nil
	}
	for _, h := range hs {
		if h.col.Nested {
			return fmt.Errorf("NetBox's %s #%d is held nested in its parent, which TrueUp doesn't compare yet", s.Type, o.ID)
		}
		ref, ok := o.Object.Fields[h.col.Parent].(model.Reference)
		if !ok || ref.ID <= 0 || ref.Type != h.parent.Type {
			continue
		}
		byParent := ix.children[h.parent.Type]
		if byParent == nil {
			byParent = map[int64]map[string][]int64{}
			ix.children[h.parent.Type] = byParent
		}
		if byParent[ref.ID] == nil {
			byParent[ref.ID] = map[string][]int64{}
		}
		byParent[ref.ID][h.col.Name] = append(byParent[ref.ID][h.col.Name], o.ID)
		return nil
	}
	return fmt.Errorf("NetBox's %s #%d belongs to none of the objects that hold its type", s.Type, o.ID)
}

// described checks that every field in fields is one the spec describes.
func described(s *model.Spec, fields map[string]model.Value) error {
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if s.Field(name) == nil {
			return fmt.Errorf("sets %s, which the spec of %s doesn't describe", strconv.Quote(name), s.Type)
		}
	}
	return nil
}

// ruleKey returns the values of a uniqueness rule's fields as a key, or false
// if a field is unset, null, or a reference that isn't resolved to an ID. A
// rule doesn't hold where a field is null, since PostgreSQL treats nulls as
// distinct; model.Check makes sure a spec records that gap. Values count as
// they are, exactly, as PostgreSQL's deterministic collations compare text:
// case and white space included.
func ruleKey(u model.Unique, fields map[string]model.Value) (string, bool) {
	var b strings.Builder
	for _, name := range u.Fields {
		switch v := fields[name].(type) {
		case nil, model.Null:
			return "", false
		case model.Reference:
			if v.ID == 0 {
				return "", false
			}
			fmt.Fprintf(&b, "%s#%d", v.Type, v.ID)
		case model.Set:
			// In any order, as NetBox and model.Equal take a set.
			elems := make([]string, len(v))
			for i, e := range v {
				elems[i] = describe(e)
			}
			slices.Sort(elems)
			fmt.Fprintf(&b, "%d:%q", v.Kind(), elems)
		default:
			// String quotes text and choices, so values can't run into
			// each other.
			fmt.Fprintf(&b, "%d:%s", v.Kind(), v.String())
		}
		b.WriteByte(0)
	}
	return b.String(), true
}

func (ix *index) compare(d *Definition) (Result, error) {
	s, iu, err := ix.checked(d)
	if err != nil {
		return Result{}, err
	}
	m, err := ix.matchDefinition(s, iu, d)
	if err != nil {
		return Result{}, err
	}
	r := Result{Type: s.Type, Identity: d.Identity, Unresolved: m.unresolved, NetBoxID: m.match, Collisions: m.collisions}
	r.Columns = []Column{{
		Kind: DefinitionColumn, Source: d.Source, Path: d.Path,
		DerivedFrom: d.DerivedFrom, Unmapped: d.Unmapped,
	}}
	var netbox map[string]model.Value
	if r.NetBoxID != 0 {
		r.Columns = append(r.Columns, Column{Kind: NetBoxColumn, ID: r.NetBoxID})
		netbox = ix.objects[s.Type][r.NetBoxID].Fields
	}
	for i := range s.Fields {
		f := &s.Fields[i]
		row := Row{Path: Path{{Field: f.Name}}, Cells: []model.Value{m.fields[f.Name]}, Derived: m.derived[f.Name]}
		if netbox != nil {
			row.Cells = append(row.Cells, f.Normalize(netbox[f.Name]))
		}
		if !slices.ContainsFunc(row.Cells, func(v model.Value) bool { return v != nil }) {
			continue
		}
		row.Differs = differ(row.Cells)
		row.Managed = row.Cells[0] != nil && (!row.Derived || r.NetBoxID == 0)
		r.addRow(row)
	}
	for i := range s.Children {
		if err := ix.compareChildren(&r, s, &s.Children[i], m.fields); err != nil {
			return Result{}, fmt.Errorf("%s's definition %s: %w", d.Source, describeDefinition(d), err)
		}
	}
	switch {
	case len(r.Collisions) > 0 || m.ambiguous:
		r.Outcome = Conflict
	case r.NetBoxID != 0:
		r.Outcome = Candidate
	default:
		r.Outcome = Absent
	}
	return r, nil
}

// addRow adds a row to the result, counting it if it is a difference.
func (r *Result) addRow(row Row) {
	if row.Differs && row.Managed {
		r.Differences++
	}
	r.Rows = append(r.Rows, row)
}

// checked returns the spec of a definition's type, and the index of its
// identity among its rules, once it has checked the definition against it.
func (ix *index) checked(d *Definition) (*model.Spec, int, error) {
	s := ix.specs[d.Object.Type]
	if s == nil {
		return nil, 0, fmt.Errorf("a definition in %s is a %s, which has no spec", d.Source, strconv.Quote(d.Object.Type))
	}
	if err := ix.checkDefinition(s, d); err != nil {
		return nil, 0, fmt.Errorf("%s's definition %s %w", d.Source, describeDefinition(d), err)
	}
	iu := s.IdentityRule()
	if iu < 0 {
		return nil, 0, fmt.Errorf("the spec of %s has no identity among its uniqueness rules", s.Type)
	}
	return s, iu, nil
}

// matched is what matching a definition found.
type matched struct {
	// fields are the definition's, normalized, with references resolved.
	fields     map[string]model.Value
	derived    map[string]bool
	unresolved []Unresolved
	// match is the NetBox object the identity matches, or 0.
	match      int64
	collisions []Collision
	// ambiguous says that a reference NetBox has several objects for left
	// the definition unmatched.
	ambiguous bool
}

// matchDefinition matches a definition to NetBox's objects of its type, by
// each of the type's uniqueness rules, iu being its identity.
func (ix *index) matchDefinition(s *model.Spec, iu int, d *Definition) (matched, error) {
	fields, unresolved, err := ix.resolve(s, d.Object.Fields)
	if err != nil {
		return matched{}, fmt.Errorf("%s's definition %s: %w", d.Source, describeDefinition(d), err)
	}
	m := matched{fields: fields, derived: map[string]bool{}, unresolved: unresolved}
	for _, name := range d.Derived {
		m.derived[name] = true
	}
	// A reference that NetBox has more than one object for can't be
	// resolved, since NetBox's answer can't be trusted to say which it is.
	// The definition is then a conflict, colliding with what it would
	// collide with under each of them.
	assignments := []map[string]model.Value{fields}
	for _, u := range unresolved {
		if len(u.Candidates) == 0 {
			continue
		}
		var next []map[string]model.Value
		for _, a := range assignments {
			for _, id := range u.Candidates {
				b := maps.Clone(a)
				ref := u.Ref
				ref.ID = id
				b[u.Field] = ref
				next = append(next, b)
			}
		}
		assignments = next
	}
	m.ambiguous = len(assignments) > 1
	if !m.ambiguous {
		m.match, m.collisions = ix.match(s, iu, fields, m.derived)
		return m, nil
	}
	// Every object it would match or collide with, under each.
	type seen struct {
		rule int
		id   int64
	}
	done := map[seen]bool{}
	for _, a := range assignments {
		match, collisions := ix.match(s, iu, a, m.derived)
		if match != 0 {
			collisions = append(collisions, ix.collision(s, iu, match))
		}
		for _, c := range collisions {
			k := seen{slices.IndexFunc(s.Unique, func(u model.Unique) bool { return slices.Equal(u.Fields, c.Rule) }), c.ID}
			if !done[k] {
				done[k] = true
				m.collisions = append(m.collisions, c)
			}
		}
	}
	return m, nil
}

// Matched returns, sorted, the NetBox objects of type typ, among observed,
// that the definitions of that type match, as Compare matches them: so that
// a caller reads the children of exactly those, such as the interface
// templates of the device types that definitions match.
func Matched(specs []*model.Spec, defs []Definition, observed []Observed, typ string) ([]int64, error) {
	ix, err := newIndex(specs, observed)
	if err != nil {
		return nil, err
	}
	found := map[int64]bool{}
	for i := range defs {
		d := &defs[i]
		if d.Object.Type != typ {
			continue
		}
		// Matching needs the definition's own fields alone; Compare
		// checks its children.
		own := *d
		own.Object.Fields = maps.Clone(d.Object.Fields)
		if s := ix.specs[d.Object.Type]; s != nil {
			for _, col := range s.Children {
				delete(own.Object.Fields, col.Name)
			}
		}
		s, iu, err := ix.checked(&own)
		if err != nil {
			return nil, err
		}
		m, err := ix.matchDefinition(s, iu, &own)
		if err != nil {
			return nil, err
		}
		if m.match != 0 {
			found[m.match] = true
		}
	}
	return slices.Sorted(maps.Keys(found)), nil
}

// match returns the NetBox object the definition's identity matches, if it
// matches exactly one, and the objects the definition collides with under
// the type's other rules, or, if its identity matches several, which
// NetBox's constraint forbids, those.
func (ix *index) match(s *model.Spec, iu int, fields map[string]model.Value, derived map[string]bool) (int64, []Collision) {
	var match int64
	var collisions []Collision
	var matches []int64
	if key, ok := ruleKey(s.Unique[iu], fields); ok {
		matches = ix.rules[s.Type][iu][key]
	}
	if len(matches) == 1 {
		match = matches[0]
	} else {
		// NetBox's answer can't be trusted to say which one this is.
		for _, id := range matches {
			collisions = append(collisions, ix.collision(s, iu, id))
		}
	}
	for i, u := range s.Unique {
		// A derived value is used only to create the object, so once the
		// identity matches, it can't collide with anything.
		if i == iu || match != 0 && slices.ContainsFunc(u.Fields, func(f string) bool { return derived[f] }) {
			continue
		}
		key, ok := ruleKey(u, fields)
		if !ok {
			continue
		}
		for _, id := range ix.rules[s.Type][i][key] {
			if id != match {
				collisions = append(collisions, ix.collision(s, i, id))
			}
		}
	}
	return match, collisions
}

// resolve returns the definition's fields normalized as the spec says, with
// each reference to an object that stands on its own resolved to its NetBox
// ID, and lists the references NetBox has nothing, or more than one object,
// for. A reference is resolved by its natural key, exactly, as NetBox's own
// import finds a device type's manufacturer by name.
func (ix *index) resolve(s *model.Spec, in map[string]model.Value) (map[string]model.Value, []Unresolved, error) {
	fields := make(map[string]model.Value, len(in))
	var unresolved []Unresolved
	for i := range s.Fields {
		f := &s.Fields[i]
		v, ok := in[f.Name]
		if !ok {
			continue
		}
		v = f.Normalize(v)
		// checkDefinition makes sure such a reference has a key, and no ID.
		if ref, isRef := v.(model.Reference); isRef && f.Ref.Within == "" {
			ids, err := ix.find(ref)
			switch {
			case err != nil:
				return nil, nil, fmt.Errorf("%s %w", f.Name, err)
			case len(ids) == 1:
				ref.ID = ids[0]
				v = ref
			default:
				unresolved = append(unresolved, Unresolved{Field: f.Name, Ref: ref, Candidates: slices.Sorted(slices.Values(ids))})
			}
		}
		fields[f.Name] = v
	}
	// Children, as NetBox would store them; checkDefinition makes sure
	// they are children, of types with specs, setting their fields.
	for i := range s.Children {
		col := &s.Children[i]
		children, ok := in[col.Name].(model.Children)
		if !ok {
			continue
		}
		cs := ix.specs[col.Type]
		out := make(model.Children, len(children))
		for j, c := range children {
			out[j] = model.Object{Type: c.Type, Fields: make(map[string]model.Value, len(c.Fields))}
			for name, v := range c.Fields {
				out[j].Fields[name] = cs.Field(name).Normalize(v)
			}
		}
		fields[col.Name] = out
	}
	return fields, unresolved, nil
}

// find returns the NetBox objects whose identity is ref's key: none, one,
// or, from a NetBox that breaks its own uniqueness rules, several. It fails
// if ref's key isn't the identity of the type it refers to.
func (ix *index) find(ref model.Reference) ([]int64, error) {
	target := ix.specs[ref.Type]
	if target == nil {
		return nil, fmt.Errorf("refers to a %s, which has no spec", strconv.Quote(ref.Type))
	}
	iu := target.IdentityRule()
	if iu < 0 {
		return nil, fmt.Errorf("refers to a %s, whose spec has no identity among its uniqueness rules", ref.Type)
	}
	if len(ref.Key) != len(target.Identity) {
		return nil, fmt.Errorf("refers to a %s by a key of %d fields, but its identity has %d", ref.Type, len(ref.Key), len(target.Identity))
	}
	for _, name := range target.Identity {
		v, f := ref.Key[name], target.Field(name)
		if v == nil || f == nil || v.Kind() == model.KindNull {
			return nil, fmt.Errorf("refers to a %s without its %s", ref.Type, name)
		}
		if err := checkKind(f, v); err != nil {
			return nil, fmt.Errorf("refers to a %s whose %s %w", ref.Type, name, err)
		}
		// NetBox's import finds what a reference names as it is written,
		// but stores names normalized, so a key it would store otherwise,
		// such as a name with white space at an end, names nothing NetBox
		// could hold, though the definition of what it names matches.
		if !model.Equal(f.Normalize(v), v) {
			return nil, fmt.Errorf("refers to a %s by a %s, %s, that NetBox would store otherwise", ref.Type, name, v)
		}
	}
	key, ok := ruleKey(target.Unique[iu], ref.Key)
	if !ok {
		// Such as a key that refers to another object in turn, which
		// would need resolving first.
		return nil, fmt.Errorf("refers to a %s by a key TrueUp can't match with NetBox's", ref.Type)
	}
	return ix.rules[target.Type][iu][key], nil
}

func (ix *index) collision(s *model.Spec, rule int, id int64) Collision {
	obj := ix.objects[s.Type][id]
	identity := make(map[string]model.Value, len(s.Identity))
	for _, name := range s.Identity {
		identity[name] = obj.Fields[name]
	}
	return Collision{Rule: s.Unique[rule].Fields, ID: id, Identity: identity}
}

// differ reports whether two of the cells that are set hold different
// values.
func differ(cells []model.Value) bool {
	var first model.Value
	for _, v := range cells {
		switch {
		case v == nil:
		case first == nil:
			first = v
		case !model.Equal(first, v):
			return true
		}
	}
	return false
}

// fillOutcomes gives each unresolved reference the outcome of the same
// source's definition of the object it refers to, where exactly one of the
// results is, and sorts collisions.
func fillOutcomes(results []Result) {
	// By source, so that what a result records doesn't depend on which
	// other sources were compared with it.
	outcomes := map[string][]Outcome{}
	for _, r := range results {
		k := r.Columns[0].Source + "\x00" + identityKey(r.Type, r.Identity)
		outcomes[k] = append(outcomes[k], r.Outcome)
	}
	for i := range results {
		r := &results[i]
		for j := range r.Unresolved {
			u := &r.Unresolved[j]
			if o := outcomes[r.Columns[0].Source+"\x00"+identityKey(u.Ref.Type, u.Ref.Key)]; len(o) == 1 {
				u.Outcome = o[0]
			}
		}
		// The object's own first, then its children's, by where they are.
		slices.SortStableFunc(r.Collisions, func(a, b Collision) int {
			return cmp.Or(cmp.Compare(len(a.Path), len(b.Path)), cmp.Compare(a.Path.String(), b.Path.String()),
				slices.Compare(a.Rule, b.Rule), cmp.Compare(a.ID, b.ID))
		})
	}
}

// identityKey writes an object's type and natural key as one string.
func identityKey(typ string, key map[string]model.Value) string {
	var b strings.Builder
	b.WriteString(typ)
	for _, name := range slices.Sorted(maps.Keys(key)) {
		b.WriteByte(0)
		b.WriteString(name)
		b.WriteByte('=')
		if v := key[name]; v != nil {
			b.WriteString(v.String())
		}
	}
	return b.String()
}

// describeDefinition names a definition in an error: by its file, or by its
// type and identity for a derived one, such as dcim.manufacturer name
// "Dell".
func describeDefinition(d *Definition) string {
	if d.Path != "" {
		return format.Quote(d.Path)
	}
	parts := make([]string, 0, len(d.Identity))
	for _, name := range slices.Sorted(maps.Keys(d.Identity)) {
		v := "unset"
		if x := d.Identity[name]; x != nil {
			v = x.String()
		}
		parts = append(parts, name+" "+v)
	}
	return d.Object.Type + " " + strings.Join(parts, ", ")
}
