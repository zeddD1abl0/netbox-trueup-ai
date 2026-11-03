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
	// least one.
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
// NetBox's uniqueness rules.
type Collision struct {
	// Rule is the uniqueness rule's fields, such as manufacturer and slug.
	Rule []string
	// ID and Identity are the NetBox object's.
	ID       int64
	Identity map[string]model.Value
}

// Unresolved is a reference to an object that NetBox doesn't have.
type Unresolved struct {
	// Field is the reference's field, such as manufacturer.
	Field string
	// Ref is the reference, by its natural key.
	Ref model.Reference
	// Outcome is that of the definition of the object referred to, among
	// those compared at the same time, or "" if there isn't exactly one.
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

// Step is one step of a row's path: for now, a field of the object. Steps 11
// and 12 of the roadmap add steps into child collections, by a child's key.
type Step struct {
	Field string
}

// Path says which field a row holds.
type Path []Step

// String writes the path as its fields joined by dots.
func (p Path) String() string {
	parts := make([]string, len(p))
	for i, s := range p {
		parts[i] = s.Field
	}
	return strings.Join(parts, ".")
}

// Row is one field of an object, side by side in each version.
type Row struct {
	Path Path
	// Cells hold the field's value in each column, normalized as the
	// type's spec says, or nil where that version doesn't set it.
	Cells []model.Value
	// Differs says that two of the versions that set the field hold
	// different values.
	Differs bool
	// Managed says the definition manages the field: it sets it, and the
	// field isn't one the format derived, which is used only to create the
	// object (docs/adr/0009-source-kinds-and-format-adapters.md).
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
// manufacturer a definition names.
//
// It fails if a definition or an object is of a type without a spec, sets a
// field its spec doesn't describe, or if NetBox listed an object twice.
func Compare(specs []*model.Spec, defs []Definition, observed []Observed) ([]Result, error) {
	ix := index{
		specs:   map[string]*model.Spec{},
		objects: map[string]map[int64]model.Object{},
		rules:   map[string][]map[string][]int64{},
	}
	for _, s := range specs {
		ix.specs[s.Type] = s
		ix.objects[s.Type] = map[int64]model.Object{}
		ix.rules[s.Type] = make([]map[string][]int64, len(s.Unique))
		for i := range s.Unique {
			ix.rules[s.Type][i] = map[string][]int64{}
		}
	}
	for _, o := range observed {
		if err := ix.add(o); err != nil {
			return nil, err
		}
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

// index holds the objects NetBox has, by type and ID, and by the values of
// each uniqueness rule.
type index struct {
	specs   map[string]*model.Spec
	objects map[string]map[int64]model.Object
	// rules holds, by type and then by the index of the rule in the spec's
	// Unique, the IDs of the objects with each rule's values.
	rules map[string][]map[string][]int64
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
	if err := described(s, o.Object.Fields); err != nil {
		return fmt.Errorf("NetBox's %s #%d %w", s.Type, o.ID, err)
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
		default:
			// String quotes text and choices, so values can't run into
			// each other.
			fmt.Fprintf(&b, "%d:%s", v.Kind(), v.String())
		}
		b.WriteByte(0)
	}
	return b.String(), true
}

// identityRule returns the index of the spec's identity among its Unique
// rules, which model.Check makes sure of, or -1.
func identityRule(s *model.Spec) int {
	return slices.IndexFunc(s.Unique, func(u model.Unique) bool {
		return len(u.Fields) == len(s.Identity) && !slices.ContainsFunc(u.Fields, func(f string) bool { return !slices.Contains(s.Identity, f) })
	})
}

func (ix *index) compare(d *Definition) (Result, error) {
	s := ix.specs[d.Object.Type]
	if s == nil {
		return Result{}, fmt.Errorf("a definition in %s is a %s, which has no spec", d.Source, strconv.Quote(d.Object.Type))
	}
	if err := described(s, d.Object.Fields); err != nil {
		return Result{}, fmt.Errorf("%s's definition %s %w", d.Source, describeDefinition(d), err)
	}
	iu := identityRule(s)
	if iu < 0 {
		return Result{}, fmt.Errorf("the spec of %s has no identity among its uniqueness rules", s.Type)
	}
	fields, unresolved := ix.resolve(s, d.Object.Fields)
	derived := map[string]bool{}
	for _, name := range d.Derived {
		derived[name] = true
	}

	r := Result{
		Type:       s.Type,
		Identity:   d.Identity,
		Unresolved: unresolved,
		Collisions: []Collision{},
	}
	var matches []int64
	if key, ok := ruleKey(s.Unique[iu], fields); ok {
		matches = ix.rules[s.Type][iu][key]
	}
	if len(matches) == 1 {
		r.NetBoxID = matches[0]
	} else {
		// NetBox's constraint forbids two objects with the same identity,
		// so NetBox's answer can't be trusted to say which one this is.
		for _, id := range matches {
			r.Collisions = append(r.Collisions, ix.collision(s, iu, id))
		}
	}
	for i, u := range s.Unique {
		// A derived value is used only to create the object, so once the
		// identity matches, it can't collide with anything.
		if i == iu || r.NetBoxID != 0 && slices.ContainsFunc(u.Fields, func(f string) bool { return derived[f] }) {
			continue
		}
		key, ok := ruleKey(u, fields)
		if !ok {
			continue
		}
		for _, id := range ix.rules[s.Type][i][key] {
			if id != r.NetBoxID {
				r.Collisions = append(r.Collisions, ix.collision(s, i, id))
			}
		}
	}
	switch {
	case len(r.Collisions) > 0:
		r.Outcome = Conflict
	case r.NetBoxID != 0:
		r.Outcome = Candidate
	default:
		r.Outcome = Absent
	}

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
		row := Row{Path: Path{{Field: f.Name}}, Cells: []model.Value{fields[f.Name]}, Derived: derived[f.Name]}
		if netbox != nil {
			row.Cells = append(row.Cells, f.Normalize(netbox[f.Name]))
		}
		if !slices.ContainsFunc(row.Cells, func(v model.Value) bool { return v != nil }) {
			continue
		}
		row.Differs = differ(row.Cells)
		row.Managed = row.Cells[0] != nil && (!row.Derived || r.NetBoxID == 0)
		if row.Differs && row.Managed {
			r.Differences++
		}
		r.Rows = append(r.Rows, row)
	}
	return r, nil
}

// resolve returns the definition's fields normalized as the spec says, with
// each reference to an object that stands on its own resolved to its NetBox
// ID, and lists the references NetBox has nothing, or more than one object,
// for. A reference is resolved by its natural key, exactly, as NetBox's own
// import finds a device type's manufacturer by name.
func (ix *index) resolve(s *model.Spec, in map[string]model.Value) (map[string]model.Value, []Unresolved) {
	fields := make(map[string]model.Value, len(in))
	unresolved := []Unresolved{}
	for i := range s.Fields {
		f := &s.Fields[i]
		v, ok := in[f.Name]
		if !ok {
			continue
		}
		v = f.Normalize(v)
		if ref, isRef := v.(model.Reference); isRef && ref.ID == 0 && f.Ref != nil && f.Ref.Within == "" {
			if id, found := ix.find(ref); found {
				ref.ID = id
				v = ref
			} else {
				unresolved = append(unresolved, Unresolved{Field: f.Name, Ref: ref})
			}
		}
		fields[f.Name] = v
	}
	return fields, unresolved
}

// find returns the ID of the one NetBox object that ref's key is the
// identity of.
func (ix *index) find(ref model.Reference) (int64, bool) {
	target := ix.specs[ref.Type]
	if target == nil {
		return 0, false
	}
	iu := identityRule(target)
	if iu < 0 {
		return 0, false
	}
	key, ok := ruleKey(target.Unique[iu], ref.Key)
	if !ok {
		return 0, false
	}
	ids := ix.rules[target.Type][iu][key]
	if len(ids) != 1 {
		return 0, false
	}
	return ids[0], true
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

// fillOutcomes gives each unresolved reference the outcome of the
// definition of the object it refers to, where exactly one of the results
// is of that object, and sorts collisions.
func fillOutcomes(results []Result) {
	outcomes := map[string][]Outcome{}
	for _, r := range results {
		k := identityKey(r.Type, r.Identity)
		outcomes[k] = append(outcomes[k], r.Outcome)
	}
	for i := range results {
		r := &results[i]
		for j := range r.Unresolved {
			u := &r.Unresolved[j]
			if o := outcomes[identityKey(u.Ref.Type, u.Ref.Key)]; len(o) == 1 {
				u.Outcome = o[0]
			}
		}
		slices.SortStableFunc(r.Collisions, func(a, b Collision) int {
			return cmp.Or(slices.Compare(a.Rule, b.Rule), cmp.Compare(a.ID, b.ID))
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
// identity for a derived one.
func describeDefinition(d *Definition) string {
	if d.Path != "" {
		return format.Quote(d.Path)
	}
	return d.Object.Type + " " + format.Quote(identityKey("", d.Identity))
}
