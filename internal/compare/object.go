package compare

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// Several sources can define one object, and one source can define it more
// than once (docs/adr/0032-compare-definitions-across-sources-as-objects.md).
// Definitions are grouped into objects by their type and identity as NetBox
// would store them, and an object's comparison is built by merging the
// comparisons of each of its definitions with NetBox, which Compare makes
// of each alone: so what is recorded of each definition is enough to build
// the object again, whatever other sources were compared with it.

// Object is an object that some of the definitions compared define, with
// every one of its definitions and NetBox's object side by side.
type Object struct {
	// Type and Identity are the object's: its type, and its identity as
	// NetBox would store it, normalized as the type's spec says.
	Type     string
	Identity map[string]model.Value
	// Outcome is a conflict if one of its definitions is, if a source
	// defines it more than once, or if it clashes with another object; and
	// otherwise its definitions' outcome, which all of them share.
	Outcome Outcome
	// NetBoxID is the NetBox object its identity matches, or 0.
	NetBoxID int64
	// Definitions are the comparisons of each of its definitions with
	// NetBox, in the order of their columns, without their rows; and
	// Results their indexes among the results Objects was given.
	Definitions []Result
	Results     []int
	// Collisions are its definitions' collisions, each once, sorted as a
	// result's are.
	Collisions []Collision
	// Unresolved are its definitions' references to objects that NetBox
	// has none, or several, of, each once, with the outcome of the object
	// it refers to among those compared, or "" if none is.
	Unresolved []Unresolved
	// DuplicatedIn names the sources that define it more than once, in the
	// order of their columns.
	DuplicatedIn []string
	// Clashes are the uniqueness rules under which its definitions and
	// another object's would leave NetBox holding the same values.
	Clashes []Clash
	// Columns are a definition's for each of Definitions, in the same
	// order, then NetBox's object's if the identity matches one.
	Columns []Column
	// Rows hold every field any version sets, in the order of the type's
	// spec; then, collection by collection, the children any definition
	// has, in the order of the first definition that has them, column by
	// column, then those only NetBox has, each with the rows of its fields
	// and nested children. A definition's cell is unset where it doesn't
	// set the field; on a child's row, it is false where the definition has
	// the child's parent but not the child, and unset where it hasn't the
	// parent either. A row differs where two cells that are set differ, and
	// is managed where some definition manages it.
	Rows []Row
	// Differences counts the rows that differ and are managed: between the
	// definitions as well as with NetBox.
	Differences int
}

// InStep reports whether NetBox holds the object as each of its definitions
// defines it: a candidate without differences.
func (o Object) InStep() bool {
	return o.Outcome == Candidate && o.Differences == 0
}

// Clash is a uniqueness rule under which some of an object's definitions
// and another object's would leave NetBox holding the same values, which
// NetBox's rules forbid: an import of each, as its object's definition,
// would hold them. A definition would hold the value it sets of a field it
// manages, and otherwise what NetBox's object holds; a rule doesn't hold
// where a field would be unset or null. Values that a NetBox object either
// object matches holds already aren't a clash: NetBox's comparison already
// reports the collision on the definitions that would change it.
type Clash struct {
	// Rule is the uniqueness rule's fields, such as manufacturer and slug.
	Rule []string
	// Values are what the definitions would hold, by field.
	Values map[string]model.Value
	// Columns are the object's definitions that would hold them, by their
	// index in Definitions.
	Columns []int
	// With are the other objects' definitions that would hold them.
	With []ClashWith
}

// ClashWith is another object's definition that a clash is with.
type ClashWith struct {
	// Type and Identity are the other object's, as Object's are.
	Type     string
	Identity map[string]model.Value
	// Column is the definition's column: its source and file.
	Column Column
}

// Group groups definitions into the objects they define, by their type and
// identity as NetBox would store them, and returns each object's
// definitions by their index in defs: objects in the order of their first
// definition, and each object's definitions in their order in defs. It
// reads only a definition's type and identity, so the definitions can be
// cut down to their unique values (UniqueValues). It fails for a
// definition of a type without a spec, or whose identity isn't the spec's.
func Group(specs []*model.Spec, defs []Definition) ([][]int, error) {
	groups, _, err := group(len(defs), func(i int) (string, map[string]model.Value, error) {
		d := &defs[i]
		k, identity, err := objectKey(specs, d.Object.Type, d.Identity)
		if err != nil {
			return "", nil, fmt.Errorf("%s's definition %s: %w", d.Source, describeDefinition(d), err)
		}
		return k, identity, nil
	})
	return groups, err
}

// keyed is an object's key, and its identity as NetBox would store it.
type keyed struct {
	key      string
	identity map[string]model.Value
}

// group groups n things by the key keyOf gives each, and returns each
// group's things, by index, groups in the order of their first thing, and
// each group's key.
func group(n int, keyOf func(i int) (string, map[string]model.Value, error)) ([][]int, []keyed, error) {
	byKey := map[string]int{}
	var groups [][]int
	var keys []keyed
	for i := range n {
		k, identity, err := keyOf(i)
		if err != nil {
			return nil, nil, err
		}
		g, ok := byKey[k]
		if !ok {
			g = len(groups)
			byKey[k] = g
			groups = append(groups, nil)
			keys = append(keys, keyed{k, identity})
		}
		groups[g] = append(groups[g], i)
	}
	return groups, keys, nil
}

// objectKey returns the key that tells an object of type typ from the
// others, from a definition's identity: each field normalized as NetBox
// would store it, and a reference by the key of what it refers to,
// normalized as that type's spec says. It returns the identity normalized
// too.
func objectKey(specs []*model.Spec, typ string, identity map[string]model.Value) (string, map[string]model.Value, error) {
	s := model.SpecOf(specs, typ)
	if s == nil {
		return "", nil, fmt.Errorf("is a %s, which has no spec", strconv.Quote(typ))
	}
	if len(identity) != len(s.Identity) {
		return "", nil, fmt.Errorf("has an identity of %d fields, but a %s's has %d", len(identity), typ, len(s.Identity))
	}
	var b strings.Builder
	b.WriteString(strconv.Quote(typ))
	normalized := make(map[string]model.Value, len(identity))
	for _, name := range s.Identity {
		v, f := identity[name], s.Field(name)
		if v == nil || f == nil {
			return "", nil, fmt.Errorf("has no %s in its identity", name)
		}
		v, err := normalizeKeyValue(specs, f, v)
		if err != nil {
			return "", nil, fmt.Errorf("its identity's %s %w", name, err)
		}
		if v.Kind() == model.KindNull {
			return "", nil, fmt.Errorf("has no %s in its identity", name)
		}
		normalized[name] = v
		b.WriteByte(0)
		b.WriteString(strconv.Quote(name))
		b.WriteByte('=')
		b.WriteString(valueKey(v))
	}
	return b.String(), normalized, nil
}

// normalizeKeyValue returns v as NetBox would store it in field f, and a
// reference, to a type f can refer to, without its ID, by its target's
// identity, normalized as objectKey normalizes it.
func normalizeKeyValue(specs []*model.Spec, f *model.Field, v model.Value) (model.Value, error) {
	v = f.Normalize(v)
	ref, ok := v.(model.Reference)
	if !ok {
		return v, nil
	}
	if f.Ref == nil || !slices.Contains(f.Ref.Types, ref.Type) {
		return nil, fmt.Errorf("refers to a %s, which %s can't", strconv.Quote(ref.Type), f.Name)
	}
	if len(ref.Key) == 0 {
		return nil, fmt.Errorf("refers to a %s by its ID alone, not by its key", ref.Type)
	}
	_, key, err := objectKey(specs, ref.Type, ref.Key)
	if err != nil {
		return nil, fmt.Errorf("refers to a %s whose key %w", ref.Type, err)
	}
	return model.Reference{Type: ref.Type, Key: key}, nil
}

// valueKey writes a value as a string that tells it from other values,
// as NetBox's constraints tell them apart: exactly, a reference by its key
// where it has one, and a set in any order.
func valueKey(v model.Value) string {
	switch v := v.(type) {
	case nil:
		return "unset"
	case model.Reference:
		if len(v.Key) == 0 {
			return strconv.Quote(v.Type) + "#" + strconv.FormatInt(v.ID, 10)
		}
		var b strings.Builder
		b.WriteString(strconv.Quote(v.Type))
		b.WriteByte('{')
		for _, name := range slices.Sorted(maps.Keys(v.Key)) {
			b.WriteString(strconv.Quote(name))
			b.WriteByte('=')
			b.WriteString(valueKey(v.Key[name]))
			b.WriteByte(';')
		}
		b.WriteByte('}')
		return b.String()
	case model.Set:
		elems := make([]string, len(v))
		for i, e := range v {
			elems[i] = valueKey(e)
		}
		slices.Sort(elems)
		return fmt.Sprintf("%d:%q", v.Kind(), elems)
	}
	// String quotes text and choices, so values can't run into each other.
	return strconv.Itoa(int(v.Kind())) + ":" + v.String()
}

// Objects groups results, each Compare's comparison of one definition with
// NetBox, into the objects their definitions define, as Group does, and
// merges each object's into one comparison, with a column for each
// definition, in the order of results, then NetBox's object. It finds the
// sources that define an object more than once, and the objects that
// clash, among those given: so the results are those of every definition
// compared. Objects come in the order of their first definition.
//
// It fails, rather than guess, on a result that isn't one Compare could
// make, and on an object whose results don't agree on NetBox's side, such as
// results made against different states of NetBox: on its NetBox object,
// the fields NetBox sets, the children NetBox has, the values of either, the
// children NetBox holds twice, and the objects a reference could be. A
// field of a child that NetBox holds at its default, which a comparison
// leaves out unless its definition sets it, can't be told apart that way:
// a caller merging comparisons recorded at different times checks when they
// were made too.
func Objects(specs []*model.Spec, results []Result) ([]Object, error) {
	members, keys, err := group(len(results), func(i int) (string, map[string]model.Value, error) {
		r := &results[i]
		if err := checkResult(specs, r); err != nil {
			return "", nil, fmt.Errorf("result %d: %w", i, err)
		}
		k, identity, err := objectKey(specs, r.Type, r.Identity)
		if err != nil {
			return "", nil, fmt.Errorf("%s's definition %s: %w", r.Columns[0].Source, describeColumn(r.Type, r.Identity, r.Columns[0]), err)
		}
		return k, identity, nil
	})
	if err != nil {
		return nil, err
	}
	objects := make([]Object, len(members))
	for g, of := range members {
		objects[g] = Object{Type: results[of[0]].Type, Identity: keys[g].identity}
	}
	holds := make([][]map[string]model.Value, len(objects))
	fields := map[string]map[string]bool{}
	for g := range objects {
		var err error
		if holds[g], err = buildObject(specs, fields, &objects[g], results, members[g]); err != nil {
			return nil, err
		}
	}
	if err := findClashes(specs, objects, holds); err != nil {
		return nil, err
	}
	// The outcomes, now that the clashes are known, and then those of the
	// objects that unresolved references name.
	outcomes := map[string]Outcome{}
	for g := range objects {
		o := &objects[g]
		if len(o.DuplicatedIn) > 0 || len(o.Clashes) > 0 {
			o.Outcome = Conflict
		}
		outcomes[keys[g].key] = o.Outcome
	}
	for g := range objects {
		for j := range objects[g].Unresolved {
			u := &objects[g].Unresolved[j]
			// Compare made sure that a reference's key is its target's
			// identity, so that it names one object.
			k, _, err := objectKey(specs, u.Ref.Type, u.Ref.Key)
			if err != nil {
				return nil, fmt.Errorf("%s's %s %w", describeObject(&objects[g]), u.Field, err)
			}
			u.Outcome = outcomes[k]
		}
	}
	return objects, nil
}

// checkResult checks that a result has the shape Compare gives one: a
// definition's column, then NetBox's object's where its identity matches
// one; an outcome that its NetBox match, collisions and references bear
// out; rows whose paths go through its type's spec, the object's fields
// before its children's, with a cell for each
// column, a child's row's cells true, false or, where that version hasn't
// the child's parent, unset; each row differing where two of its cells do,
// managed only where the definition sets it, and as many differences as
// rows that differ and are managed. It doesn't look for a row given twice,
// which merging two results refuses.
func checkResult(specs []*model.Spec, r *Result) error {
	ambiguous := slices.ContainsFunc(r.Unresolved, func(u Unresolved) bool { return len(u.Candidates) > 0 })
	switch {
	case len(r.Columns) == 0 || r.Columns[0].Kind != DefinitionColumn:
		return fmt.Errorf("the comparison of a %s doesn't start with its definition's column", r.Type)
	case r.NetBoxID == 0 && len(r.Columns) != 1,
		r.NetBoxID != 0 && (len(r.Columns) != 2 || r.Columns[1].Kind != NetBoxColumn || r.Columns[1].ID != r.NetBoxID):
		return fmt.Errorf("the comparison of a %s has columns that don't match its NetBox object #%d", r.Type, r.NetBoxID)
	case r.Outcome != Candidate && r.Outcome != Absent && r.Outcome != Conflict:
		return fmt.Errorf("the comparison of a %s has the outcome %q", r.Type, r.Outcome)
	case r.Outcome == Candidate && r.NetBoxID == 0, r.Outcome == Absent && r.NetBoxID != 0:
		return fmt.Errorf("the comparison of a %s is %s, though its NetBox object is #%d", r.Type, r.Outcome, r.NetBoxID)
	case (r.Outcome == Conflict) != (len(r.Collisions) > 0 || ambiguous):
		return fmt.Errorf("the comparison of a %s is %s, with %d collisions and %d references NetBox has several of", r.Type, r.Outcome, len(r.Collisions), len(r.Unresolved))
	}
	s := model.SpecOf(specs, r.Type)
	if s == nil {
		return fmt.Errorf("the comparison is of a %s, which has no spec", strconv.Quote(r.Type))
	}
	differences, children := 0, false
	for _, row := range r.Rows {
		if len(row.Path) == 1 && children {
			return fmt.Errorf("the comparison of a %s has the field %s after its children's rows", r.Type, row.Path)
		}
		children = children || len(row.Path) > 1
		if len(row.Cells) != len(r.Columns) {
			return fmt.Errorf("the comparison of a %s has a row %s with %d cells for %d columns", r.Type, row.Path, len(row.Cells), len(r.Columns))
		}
		if err := checkPath(specs, s, row.Path); err != nil {
			return fmt.Errorf("the comparison of a %s has a row %s that %w", r.Type, row.Path, err)
		}
		if isChildRow(row.Path) {
			for c, v := range row.Cells {
				if _, ok := v.(model.Boolean); !ok && (v != nil || c == 0) {
					return fmt.Errorf("the comparison of a %s has a child's row %s whose cell %d is %s, not whether it has the child", r.Type, row.Path, c, describe(v))
				}
			}
		}
		if row.Differs != differ(row.Cells) || row.Managed && row.Cells[0] == nil {
			return fmt.Errorf("the comparison of a %s has a row %s whose flags don't fit its cells", r.Type, row.Path)
		}
		if row.Differs && row.Managed {
			differences++
		}
	}
	if differences != r.Differences {
		return fmt.Errorf("the comparison of a %s counts %d differences, but %d rows differ and are managed", r.Type, r.Differences, differences)
	}
	return nil
}

// checkPath checks that a row's path goes through spec s: fields and
// collections it has, each collection followed by a child's key, of its key's
// fields, and the path ending at a field or a child's key.
func checkPath(specs []*model.Spec, s *model.Spec, p Path) error {
	if len(p) == 0 {
		return errors.New("has no path")
	}
	cur := s
	var col *model.Collection
	for i, step := range p {
		switch {
		case step.Key != nil:
			if col == nil || len(step.Key) != len(col.Key) || slices.ContainsFunc(col.Key, func(name string) bool { _, ok := step.Key[name]; return !ok }) {
				return fmt.Errorf("has a key at step %d that isn't a child's of a collection", i+1)
			}
			cur, col = model.SpecOf(specs, col.Type), nil
			if cur == nil {
				return fmt.Errorf("goes into children without a spec at step %d", i+1)
			}
		case col != nil || cur == nil:
			return fmt.Errorf("names %s where a child's key or nothing should be", step.Field)
		case cur.Field(step.Field) != nil:
			if i != len(p)-1 {
				return fmt.Errorf("goes on past the field %s", step.Field)
			}
			cur = nil
		case cur.Collection(step.Field) != nil:
			col = cur.Collection(step.Field)
		default:
			return fmt.Errorf("names %s, which a %s hasn't got", step.Field, cur.Type)
		}
	}
	if col != nil {
		return errors.New("ends at a collection")
	}
	return nil
}

// buildObject fills in an object from the results of its definitions, by
// their index in results, and returns what each definition would hold of
// the fields of its type's uniqueness rules (see Clash), in the order of
// its columns.
func buildObject(specs []*model.Spec, fields map[string]map[string]bool, o *Object, results []Result, of []int) ([]map[string]model.Value, error) {
	first := &results[of[0]]
	o.NetBoxID = first.NetBoxID
	// With one NetBox object, or none, a definition that isn't a conflict
	// is a candidate, or absent, as checkResult makes sure, as all of them
	// are.
	o.Outcome = Absent
	if o.NetBoxID != 0 {
		o.Outcome = Candidate
	}
	sources := map[string]int{}
	for _, i := range of {
		r := &results[i]
		if r.NetBoxID != o.NetBoxID {
			return nil, fmt.Errorf("the definitions of %s were compared with different NetBox objects, #%d and #%d", describeObject(o), o.NetBoxID, r.NetBoxID)
		}
		if r.Outcome == Conflict {
			o.Outcome = Conflict
		}
		def := *r
		def.Rows = nil
		o.Definitions = append(o.Definitions, def)
		o.Results = append(o.Results, i)
		o.Columns = append(o.Columns, r.Columns[0])
		sources[r.Columns[0].Source]++
	}
	for _, c := range o.Columns {
		if sources[c.Source] > 1 && !slices.Contains(o.DuplicatedIn, c.Source) {
			o.DuplicatedIn = append(o.DuplicatedIn, c.Source)
		}
	}
	if o.NetBoxID != 0 {
		o.Columns = append(o.Columns, Column{Kind: NetBoxColumn, ID: o.NetBoxID})
	}
	var err error
	if o.Collisions, o.Unresolved, err = unionOf(specs, results, of); err != nil {
		return nil, fmt.Errorf("%s: %w", describeObject(o), err)
	}

	s := model.SpecOf(specs, o.Type)
	if fields[o.Type] == nil {
		fields[o.Type] = ruleFields(s)
	}
	holds := make([]map[string]model.Value, len(of))
	for c, i := range of {
		holds[c] = wouldHold(fields[o.Type], &results[i])
	}
	if len(of) == 1 {
		// Merging one comparison gives its own rows (TestMergeOfOne).
		o.Rows, o.Differences = first.Rows, first.Differences
		return holds, nil
	}
	rows, err := mergeRows(specs, s, results, of)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", describeObject(o), err)
	}
	o.Rows = rows
	for _, row := range rows {
		if row.Differs && row.Managed {
			o.Differences++
		}
	}
	return holds, nil
}

// unionOf returns the collisions and unresolved references of the results
// of an object's definitions, each once. A child's collisions, which are
// NetBox's children sharing a key, are taken from the first definition that
// has any at that child's path, so that nested children, which have no
// IDs, are each counted once.
func unionOf(specs []*model.Spec, results []Result, of []int) ([]Collision, []Unresolved, error) {
	var collisions []Collision
	type own struct {
		rule string
		id   int64
	}
	seen := map[own]bool{}
	// A child's collisions, NetBox's children sharing its key, by path and
	// then by result, as a list: each result that finds them finds the same,
	// and every result finds those of the object's own children.
	var paths []string
	byPath := map[string]map[int]string{}
	firstFrom := map[string]int{}
	first := map[string][]Collision{}
	for _, i := range of {
		r := &results[i]
		for _, c := range r.Collisions {
			if len(c.Path) == 0 {
				k := own{strings.Join(c.Rule, "\x00"), c.ID}
				if !seen[k] {
					seen[k] = true
					collisions = append(collisions, c)
				}
				continue
			}
			p := pathKey(c.Path)
			if byPath[p] == nil {
				byPath[p] = map[int]string{}
				paths = append(paths, p)
				firstFrom[p] = i
			}
			byPath[p][i] += strings.Join(c.Rule, "\x00") + "#" + strconv.FormatInt(c.ID, 10) + ";"
			if firstFrom[p] == i {
				first[p] = append(first[p], c)
			}
		}
	}
	for _, p := range paths {
		found := byPath[p]
		want := found[firstFrom[p]]
		for _, i := range of {
			got, ok := found[i]
			if ok && got != want || !ok && keysIn(first[p][0].Path) == 1 {
				return nil, nil, fmt.Errorf("its definitions' comparisons disagree on what NetBox holds at %s", first[p][0].Path)
			}
		}
		collisions = append(collisions, first[p]...)
	}
	sortCollisions(collisions)

	var unresolved []Unresolved
	refs := map[string]string{}
	for _, i := range of {
		for _, u := range results[i].Unresolved {
			// As grouping keys what it refers to.
			key, _, err := objectKey(specs, u.Ref.Type, u.Ref.Key)
			if err != nil {
				return nil, nil, fmt.Errorf("its %s %w", u.Field, err)
			}
			k := u.Field + "\x00" + key
			candidates := fmt.Sprint(u.Candidates)
			if was, ok := refs[k]; ok {
				if was != candidates {
					return nil, nil, fmt.Errorf("its definitions' comparisons disagree on which NetBox objects its %s could be", u.Field)
				}
				continue
			}
			refs[k] = candidates
			unresolved = append(unresolved, u)
		}
	}
	return collisions, unresolved, nil
}

// sortCollisions sorts collisions as a result's: the object's own first,
// then its children's, by where they are, then by rule and ID.
func sortCollisions(collisions []Collision) {
	slices.SortStableFunc(collisions, func(a, b Collision) int {
		return cmp.Or(cmp.Compare(len(a.Path), len(b.Path)), cmp.Compare(a.Path.String(), b.Path.String()),
			slices.Compare(a.Rule, b.Rule), cmp.Compare(a.ID, b.ID))
	})
}

// ruleFields returns the fields that a type's uniqueness rules name.
func ruleFields(s *model.Spec) map[string]bool {
	fields := map[string]bool{}
	for _, u := range s.Unique {
		for _, name := range u.Fields {
			fields[name] = true
		}
	}
	return fields
}

// wouldHold returns what an import of a definition would leave NetBox
// holding in fields, those of its type's uniqueness rules, from its
// comparison's rows: the definition's value where it manages the field,
// NetBox's where it doesn't, or nothing where neither sets it.
func wouldHold(fields map[string]bool, r *Result) map[string]model.Value {
	held := map[string]model.Value{}
	for _, row := range r.Rows {
		if len(row.Path) != 1 {
			// The object's own fields come first, then its children.
			break
		}
		if row.Path[0].Key != nil || !fields[row.Path[0].Field] {
			continue
		}
		switch {
		case row.Managed:
			held[row.Path[0].Field] = row.Cells[0]
		case len(row.Cells) > 1:
			held[row.Path[0].Field] = row.Cells[1]
		}
	}
	return held
}

// findClashes finds the objects that clash (see Clash), from what each of
// their definitions would hold, holds[object][column].
func findClashes(specs []*model.Spec, objects []Object, holds [][]map[string]model.Value) error {
	type holder struct{ object, column int }
	type entry struct {
		rule    []string
		values  map[string]model.Value
		holders []holder
	}
	// What NetBox's object of each holds now, read once.
	nb := make([]map[string]model.Value, len(objects))
	for g := range objects {
		if objects[g].NetBoxID != 0 {
			nb[g] = netboxHolds(&objects[g])
		}
	}
	for _, typ := range typesOf(objects) {
		s := model.SpecOf(specs, typ)
		if s == nil {
			return fmt.Errorf("an object is a %s, which has no spec", strconv.Quote(typ))
		}
		iu := s.IdentityRule()
		for ui, u := range s.Unique {
			if ui == iu {
				continue
			}
			byKey := map[string]*entry{}
			var order []string
			// The values each object's NetBox object holds now.
			heldNow := map[int]string{}
			for g := range objects {
				o := &objects[g]
				if o.Type != typ {
					continue
				}
				if nb[g] != nil {
					if k, _, ok := holdKey(u, nb[g]); ok {
						heldNow[g] = k
					}
				}
				for c := range o.Definitions {
					k, values, ok := holdKey(u, holds[g][c])
					if !ok {
						continue
					}
					e := byKey[k]
					if e == nil {
						e = &entry{rule: u.Fields, values: values}
						byKey[k] = e
						order = append(order, k)
					}
					e.holders = append(e.holders, holder{g, c})
				}
			}
			for _, k := range order {
				e := byKey[k]
				// Values that the NetBox object one of them matches holds
				// already are a collision NetBox's comparison reports.
				held := slices.ContainsFunc(e.holders, func(h holder) bool { now, ok := heldNow[h.object]; return ok && now == k })
				if held || !slices.ContainsFunc(e.holders, func(h holder) bool { return h.object != e.holders[0].object }) {
					continue
				}
				for _, h := range e.holders {
					o := &objects[h.object]
					if n := len(o.Clashes); n > 0 && slices.Equal(o.Clashes[n-1].Rule, e.rule) && valuesKey(o.Clashes[n-1].Values) == k {
						o.Clashes[n-1].Columns = append(o.Clashes[n-1].Columns, h.column)
						continue
					}
					clash := Clash{Rule: slices.Clone(e.rule), Values: holds[h.object][h.column], Columns: []int{h.column}}
					clash.Values = pick(clash.Values, e.rule)
					for _, other := range e.holders {
						if other.object == h.object {
							continue
						}
						w := &objects[other.object]
						clash.With = append(clash.With, ClashWith{Type: w.Type, Identity: w.Identity, Column: w.Columns[other.column]})
					}
					o.Clashes = append(o.Clashes, clash)
				}
			}
		}
	}
	return nil
}

// typesOf returns the types of the objects, in the order each first comes.
func typesOf(objects []Object) []string {
	var types []string
	for _, o := range objects {
		if !slices.Contains(types, o.Type) {
			types = append(types, o.Type)
		}
	}
	return types
}

// netboxHolds returns what NetBox's object holds now in an object's
// fields, from its merged rows' NetBox cells.
func netboxHolds(o *Object) map[string]model.Value {
	held := map[string]model.Value{}
	nb := len(o.Columns) - 1
	for _, row := range o.Rows {
		if len(row.Path) != 1 {
			// The object's own fields come first, then its children.
			break
		}
		if row.Path[0].Key == nil {
			held[row.Path[0].Field] = row.Cells[nb]
		}
	}
	return held
}

// holdKey returns the key of what a rule's fields hold, and those values,
// or false if one of them is unset or null, where the rule doesn't hold.
func holdKey(u model.Unique, held map[string]model.Value) (string, map[string]model.Value, bool) {
	values := make(map[string]model.Value, len(u.Fields))
	for _, name := range u.Fields {
		v := held[name]
		if v == nil || v.Kind() == model.KindNull {
			return "", nil, false
		}
		values[name] = v
	}
	return valuesKey(values), values, true
}

// valuesKey writes values by field as one key.
func valuesKey(values map[string]model.Value) string {
	var b strings.Builder
	for _, name := range slices.Sorted(maps.Keys(values)) {
		b.WriteString(strconv.Quote(name))
		b.WriteByte('=')
		b.WriteString(valueKey(values[name]))
		b.WriteByte(0)
	}
	return b.String()
}

// pick returns the values of the fields named.
func pick(values map[string]model.Value, names []string) map[string]model.Value {
	out := make(map[string]model.Value, len(names))
	for _, name := range names {
		out[name] = values[name]
	}
	return out
}

// pathKey writes a path as a string that tells it from other paths: each
// field's name, and each child's key, field by field.
func pathKey(p Path) string {
	return string(appendPathKey(nil, p))
}

// appendPathKey appends pathKey's string to b.
func appendPathKey(b []byte, p Path) []byte {
	for _, s := range p {
		if s.Key == nil {
			b = strconv.AppendQuote(append(b, 'f'), s.Field)
			continue
		}
		b = append(b, "k{"...)
		if len(s.Key) == 1 {
			for name, v := range s.Key {
				b = appendValueKey(append(strconv.AppendQuote(b, name), '='), v)
			}
			b = append(b, ";}"...)
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(s.Key)) {
			b = appendValueKey(append(strconv.AppendQuote(b, name), '='), s.Key[name])
			b = append(b, ';')
		}
		b = append(b, '}')
	}
	return b
}

// appendValueKey appends valueKey's string to b, without the string for the
// common values.
func appendValueKey(b []byte, v model.Value) []byte {
	switch v := v.(type) {
	case model.String:
		return strconv.AppendQuote(append(strconv.AppendInt(b, int64(v.Kind()), 10), ':'), string(v))
	case model.Integer:
		return strconv.AppendInt(append(strconv.AppendInt(b, int64(v.Kind()), 10), ':'), int64(v), 10)
	}
	return append(b, valueKey(v)...)
}

// merged is a row of an object being merged from its definitions'
// comparisons.
type merged struct {
	row Row
	// has says which definitions' comparisons have the row.
	has []bool
	// netbox says NetBox's cell has been taken from one of them.
	netbox bool
	// at is where the row first comes, among those that have it: on a
	// child's row, among those whose definitions have the child if any do
	// (defined), by the definition's column and the row's index in its
	// comparison.
	defined   bool
	column    int
	index     int
	rankCache []rank
	// above are the rows of the children the row's path goes through, one
	// for each of its keys, the row's own last if it is a child's.
	above []*merged
}

// mergeRows merges the rows of the results of an object's definitions, by
// their index in results, into the object's rows (see Object.Rows).
func mergeRows(specs []*model.Spec, s *model.Spec, results []Result, of []int) ([]Row, error) {
	n := len(of)
	netbox := results[of[0]].NetBoxID != 0
	width := n
	if netbox {
		width++
	}
	byPath := map[string]*merged{}
	var all []*merged
	var key []byte
	for c, i := range of {
		// The rows of the children the rows below are of: a child's row
		// comes before those of its fields and nested children.
		var above []*merged
		for at, row := range results[i].Rows {
			key = appendPathKey(key[:0], row.Path)
			m := byPath[string(key)]
			depth := keysIn(row.Path)
			child := isChildRow(row.Path)
			if !child && depth > len(above) || child && depth-1 > len(above) {
				return nil, fmt.Errorf("the comparison of its definition in %s has the row %s before its child's", results[i].Columns[0].Source, row.Path)
			}
			for j, a := range above[:depth-boolInt(child)] {
				if !below(row.Path, a.row.Path, j) {
					return nil, fmt.Errorf("the comparison of its definition in %s has the row %s among those of %s", results[i].Columns[0].Source, row.Path, a.row.Path)
				}
			}
			if m == nil {
				m = &merged{row: Row{Path: row.Path, Cells: make([]model.Value, width)}, has: make([]bool, n), column: c, index: at}
				byPath[string(key)] = m
				all = append(all, m)
				m.above = append(make([]*merged, 0, depth), above[:depth-boolInt(child)]...)
				if child {
					m.above = append(m.above, m)
				}
			}
			if child {
				above = append(above[:depth-1], m)
			}
			if m.has[c] {
				return nil, fmt.Errorf("the comparison of its definition in %s has the row %s twice", results[i].Columns[0].Source, row.Path)
			}
			m.has[c] = true
			m.row.Cells[c] = row.Cells[0]
			m.row.Managed = m.row.Managed || row.Managed
			m.row.Derived = m.row.Derived || row.Derived
			if child && model.Equal(row.Cells[0], model.Boolean(true)) && !m.defined {
				m.defined, m.column, m.index = true, c, at
			}
			if !netbox {
				continue
			}
			if !m.netbox {
				m.netbox = true
				m.row.Cells[n], m.row.NetBoxID = row.Cells[1], row.NetBoxID
				continue
			}
			if !sameValue(m.row.Cells[n], row.Cells[1]) || m.row.NetBoxID != row.NetBoxID {
				return nil, fmt.Errorf("its definitions' comparisons give NetBox's %s differently", row.Path)
			}
		}
	}
	// Each definition's comparison with one NetBox has every field NetBox's
	// object sets, and every child NetBox has, of the object or of a child
	// the definition has that NetBox has once: comparisons that don't were
	// made against different states of NetBox.
	if netbox {
		for _, m := range all {
			nb := m.row.Cells[n]
			child := isChildRow(m.row.Path)
			var parent *merged
			if child && len(m.above) > 1 {
				parent = m.above[len(m.above)-2]
			}
			for c := range n {
				switch {
				case m.has[c]:
				case len(m.row.Path) == 1 && nb != nil,
					child && model.Equal(nb, model.Boolean(true)) &&
						(parent == nil || parent.has[c] && model.Equal(parent.row.Cells[c], model.Boolean(true)) && parent.row.NetBoxID != 0):
					return nil, fmt.Errorf("its definitions' comparisons disagree on what NetBox holds at %s", m.row.Path)
				}
			}
		}
	}
	// A definition's cell on a child's row it hasn't got: false where it has
	// the child's parent, unset where it hasn't.
	for _, m := range all {
		if !isChildRow(m.row.Path) {
			continue
		}
		var parent *merged
		if len(m.above) > 1 {
			parent = m.above[len(m.above)-2]
		}
		for c := range n {
			switch {
			case m.has[c]:
			case parent == nil, parent.has[c] && model.Equal(parent.row.Cells[c], model.Boolean(true)):
				m.row.Cells[c] = model.Boolean(false)
			}
		}
	}
	for _, m := range all {
		r, err := ranksOf(specs, s, m)
		if err != nil {
			return nil, err
		}
		m.rankCache = r
	}
	slices.SortStableFunc(all, func(a, b *merged) int { return compareRanks(a.rankCache, b.rankCache) })
	rows := make([]Row, len(all))
	for i, m := range all {
		m.row.Differs = differ(m.row.Cells)
		rows[i] = m.row
	}
	return rows, nil
}

// sameValue reports whether two cells hold the same value, or are both
// unset.
func sameValue(a, b model.Value) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return model.Equal(a, b)
}

// isChildRow reports whether a row's path ends at a child's key.
func isChildRow(p Path) bool {
	return len(p) > 0 && p[len(p)-1].Key != nil
}

// keysIn counts the children's keys in a path.
func keysIn(p Path) int {
	n := 0
	for _, step := range p {
		if step.Key != nil {
			n++
		}
	}
	return n
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// below reports whether path p goes through the child whose row's path is
// child, its keys' j-th, so that p starts with child.
func below(p, child Path, j int) bool {
	if len(child) > len(p) || keysIn(child) != j+1 {
		return false
	}
	for i, step := range child {
		other := p[i]
		if step.Field != other.Field || (step.Key == nil) != (other.Key == nil) || len(step.Key) != len(other.Key) {
			return false
		}
		for name, v := range step.Key {
			if w, ok := other.Key[name]; !ok || !sameValue(v, w) {
				return false
			}
		}
	}
	return true
}

// rank is where one step of a row's path sorts among its siblings.
type rank struct {
	group, a, b int
}

func compareRanks(x, y []rank) int {
	for i := range min(len(x), len(y)) {
		if c := cmp.Or(cmp.Compare(x[i].group, y[i].group), cmp.Compare(x[i].a, y[i].a), cmp.Compare(x[i].b, y[i].b)); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(x), len(y))
}

// ranksOf returns where each step of a merged row's path sorts: a field by
// its place in its spec's fields, before collections, which sort by their
// place in its spec's collections; a child that some definition has by the
// first such definition's column and the child's row's index in its
// comparison, before children only NetBox has, which sort by where they
// first come.
func ranksOf(specs []*model.Spec, s *model.Spec, m *merged) ([]rank, error) {
	p := m.row.Path
	ranks := make([]rank, len(p))
	cur := s
	keys := 0
	for i, step := range p {
		if step.Key != nil {
			if keys >= len(m.above) {
				return nil, fmt.Errorf("the row %s is below a child that has no row", p)
			}
			child := m.above[keys]
			keys++
			if child.defined {
				ranks[i] = rank{0, child.column, child.index}
			} else {
				ranks[i] = rank{1, child.column, child.index}
			}
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("the row %s goes below a field", p)
		}
		if j := slices.IndexFunc(cur.Fields, func(f model.Field) bool { return f.Name == step.Field }); j >= 0 {
			ranks[i] = rank{0, j, 0}
			cur = nil
			continue
		}
		j := slices.IndexFunc(cur.Children, func(col model.Collection) bool { return col.Name == step.Field })
		if j < 0 || i+1 >= len(p) || p[i+1].Key == nil {
			return nil, fmt.Errorf("the row %s names %s, which a %s hasn't got", p, step.Field, cur.Type)
		}
		ranks[i] = rank{1, j, 0}
		cur = model.SpecOf(specs, cur.Children[j].Type)
	}
	return ranks, nil
}

// describeObject names an object in an error, by its type and identity.
func describeObject(o *Object) string {
	return describeColumn(o.Type, o.Identity, Column{})
}

// describeColumn names a definition in an error: by its file, or by its
// type and identity.
func describeColumn(typ string, identity map[string]model.Value, c Column) string {
	d := Definition{Source: c.Source}
	d.Object.Type, d.Identity, d.Path = typ, identity, c.Path
	return describeDefinition(&d)
}
