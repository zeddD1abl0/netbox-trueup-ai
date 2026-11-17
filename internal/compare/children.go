package compare

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// A definition's children are compared with the children of the NetBox
// object its identity matches, in each of its type's collections, by their
// key within the parent, such as a device type's interface templates by
// name (docs/adr/0026-compare-children-by-their-key.md). Keys count exactly,
// as for uniqueness rules: the definition's as NetBox would store them, and
// NetBox's as it gives them.
//
// Each child has a row whose path ends at its key, saying whether each
// version has it. NetBox's children that share a key are collisions, and
// neither is compared: the definition's child's fields show NetBox's cells
// unset. A child only the definition has is one difference, not
// one for each of its fields; a child only NetBox has is reported, but
// isn't managed, since TrueUp never removes one, so it doesn't count. A
// child's fields have rows where the definition sets them, or where NetBox
// holds something other than what creating the child would give it,
// leaving out the fields that refer to the parent and make up the key,
// which the path holds.
//
// A nested collection, such as a front port's mappings, is compared the
// same way within its parent, after the parent's fields, and its rows' paths
// go on from the parent's, such as front_ports["FP1"].rear_ports[1]
// (docs/adr/0027-nest-port-mappings-in-their-front-port.md). Where NetBox
// hasn't got the parent, or has it twice, the nested children's rows leave
// NetBox's cells unset, as their parent's fields do, so a missing front port
// is one difference, not one for each of its mappings too. Nested children
// have no NetBox IDs. Their references to other children, such as a
// mapping's rear port, are within the object the paths start from, and
// compare by the key of what they refer to there.

// compareChildren adds the rows of collection col of a definition of type
// s, whose fields are normalized, to r, and as collisions the NetBox
// children whose keys are another's, which NetBox's rules forbid, those of
// nested collections included. It fails if a collection's type has no spec,
// since its children, the definition's or NetBox's, couldn't be compared.
func (ix *index) compareChildren(r *Result, s *model.Spec, col *model.Collection, fields map[string]model.Value) error {
	var theirs map[string]model.Value
	if r.NetBoxID != 0 {
		theirs = ix.objects[s.Type][r.NetBoxID].Fields
	}
	return ix.compareCollection(r, s, parentAt{spec: s, id: r.NetBoxID, has: r.NetBoxID != 0, theirs: theirs}, col, fields[col.Name])
}

// parentAt is the parent of a collection compareCollection compares: its
// spec, its path, and whether NetBox has exactly one, with its ID, which a
// nested child hasn't got, and its fields.
type parentAt struct {
	path   Path
	spec   *model.Spec
	has    bool
	id     int64
	theirs map[string]model.Value
}

// netboxChild is one of NetBox's children of a parent: its ID, or 0 for a
// nested child, and its fields.
type netboxChild struct {
	id     int64
	fields map[string]model.Value
}

// compareCollection adds the rows of the children mine of collection col of
// parent, within an object of type root, and those of their nested
// collections, to r.
func (ix *index) compareCollection(r *Result, root *model.Spec, parent parentAt, col *model.Collection, mine model.Value) error {
	cs := ix.specs[col.Type]
	if cs == nil {
		return fmt.Errorf("the spec of %s has %s, whose type %s has no spec", parent.spec.Type, col.Name, col.Type)
	}
	defs, _ := mine.(model.Children)
	netboxColumn := r.NetBoxID != 0

	// NetBox's children of the parent.
	var observed []netboxChild
	switch {
	case !parent.has:
	case col.Nested:
		// checkObserved makes sure NetBox's parent carries them, as
		// children of the collection's type.
		theirs, _ := parent.theirs[col.Name].(model.Children)
		for _, c := range theirs {
			observed = append(observed, netboxChild{fields: c.Fields})
		}
	default:
		for _, id := range ix.children[parent.spec.Type][parent.id][col.Name] {
			observed = append(observed, netboxChild{id: id, fields: ix.objects[col.Type][id].Fields})
		}
	}
	// By key, as indexes into observed.
	netbox := map[string][]int{}
	netboxKeys := map[string]map[string]model.Value{}
	for i, c := range observed {
		k, key, ok := keyOf(col, c.fields)
		if !ok {
			// checkObserved makes sure a key field holds a value of its
			// kind, which is null only where the field takes null, and
			// then the child can be told from no other.
			k = "#" + strconv.FormatInt(c.id, 10)
			if c.id == 0 {
				k = "#n" + strconv.Itoa(i)
			}
		}
		if _, seen := netbox[k]; !seen {
			netboxKeys[k] = key
		}
		netbox[k] = append(netbox[k], i)
	}
	for _, k := range slices.Sorted(maps.Keys(netbox)) {
		if len(netbox[k]) < 2 {
			continue
		}
		// By ID, which nested children haven't got, so they stay in
		// NetBox's order.
		same := slices.SortedStableFunc(slices.Values(netbox[k]), func(a, b int) int { return cmp.Compare(observed[a].id, observed[b].id) })
		for _, i := range same {
			r.Collisions = append(r.Collisions, Collision{
				Path: parent.childPath(col.Name, netboxKeys[k]), Rule: collisionRule(col), ID: observed[i].id, Identity: netboxKeys[k],
			})
		}
	}

	defined := map[string]bool{}
	for _, c := range defs {
		// checkDefinition makes sure each child has its key.
		k, key, _ := keyOf(col, c.Fields)
		defined[k] = true
		path := parent.childPath(col.Name, key)
		row := Row{Path: path, Cells: []model.Value{model.Boolean(true)}, Managed: true}
		child := parentAt{path: path, spec: cs}
		if netboxColumn {
			// Unset where NetBox hasn't got the parent, which is the
			// difference.
			var has model.Value
			if parent.has {
				has = model.Boolean(len(netbox[k]) > 0)
			}
			row.Cells = append(row.Cells, has)
			if is := netbox[k]; len(is) == 1 {
				row.NetBoxID = observed[is[0]].id
				child.has, child.id, child.theirs = true, observed[is[0]].id, observed[is[0]].fields
			}
		}
		row.Differs = differ(row.Cells)
		r.addRow(row)
		for i := range cs.Fields {
			f := &cs.Fields[i]
			if f.Name == col.Parent || slices.Contains(col.Key, f.Name) {
				continue
			}
			mine := c.Fields[f.Name]
			var nb model.Value
			if child.has {
				nb = ix.netboxValue(root, r.NetBoxID, f, child.theirs[f.Name])
			}
			if mine == nil && (nb == nil || isDefault(f, nb)) {
				continue
			}
			field := Row{Path: append(slices.Clone(path), Step{Field: f.Name}), Cells: []model.Value{mine}}
			if netboxColumn {
				field.Cells = append(field.Cells, nb)
			}
			field.Differs = differ(field.Cells)
			field.Managed = mine != nil
			r.addRow(field)
		}
		// The child's nested collections. Those with endpoints of their
		// own, which checkDefinition refuses below the object the paths
		// start from, TrueUp doesn't read.
		for i := range cs.Children {
			nested := &cs.Children[i]
			if !nested.Nested {
				continue
			}
			if err := ix.compareCollection(r, root, child, nested, c.Fields[nested.Name]); err != nil {
				return err
			}
		}
	}

	// The children only NetBox has, by key, so that NetBox's order doesn't
	// matter.
	for _, k := range slices.Sorted(maps.Keys(netbox)) {
		if defined[k] {
			continue
		}
		row := Row{
			Path:    parent.childPath(col.Name, netboxKeys[k]),
			Cells:   []model.Value{model.Boolean(false), model.Boolean(true)},
			Differs: true,
		}
		if is := netbox[k]; len(is) == 1 {
			row.NetBoxID = observed[is[0]].id
		}
		r.addRow(row)
	}
	return nil
}

// collisionRule is the rule two of a collection's children break by sharing
// a key: the parent field and the key, or for a nested collection, whose
// children have no parent field, the key alone.
func collisionRule(col *model.Collection) []string {
	if col.Nested {
		return slices.Clone(col.Key)
	}
	return append([]string{col.Parent}, col.Key...)
}

// childPath is the path of a child of the parent, in its collection.
func (p parentAt) childPath(collection string, key map[string]model.Value) Path {
	return append(slices.Clone(p.path), childPath(collection, key)...)
}

// keyOf returns a child's key in collection col, as a string that tells
// keys apart and as the key's fields, or false if a key field is unset or
// null.
func keyOf(col *model.Collection, fields map[string]model.Value) (string, map[string]model.Value, bool) {
	k, ok := ruleKey(model.Unique{Fields: col.Key}, fields)
	key := make(map[string]model.Value, len(col.Key))
	for _, name := range col.Key {
		key[name] = fields[name]
	}
	return k, key, ok
}

// netboxValue returns the value NetBox gives for field f of a child of the
// parent of type parent with ID parentID, normalized, with a reference to
// another child of the parent given its key, as a definition names it. A
// reference to an object that isn't one of the parent's children, which
// NetBox's rules forbid, keeps only its ID, and so never equals a
// definition's.
func (ix *index) netboxValue(parent *model.Spec, parentID int64, f *model.Field, v model.Value) model.Value {
	v = f.Normalize(v)
	ref, ok := v.(model.Reference)
	if !ok || f.Ref == nil || f.Ref.Within == "" {
		return v
	}
	ref.Key = nil
	target, found := ix.objects[ref.Type][ref.ID]
	if !found {
		return ref
	}
	for _, h := range ix.holders[ref.Type] {
		if h.parent.Type != parent.Type {
			continue
		}
		if p, _ := target.Fields[h.col.Parent].(model.Reference); p.ID != parentID {
			continue
		}
		if _, key, ok := keyOf(h.col, target.Fields); ok {
			ref.Key = key
		}
		break
	}
	return ref
}

// isDefault reports whether v, normalized, is what NetBox gives field f
// when an object is created without it, through its REST API or its import.
func isDefault(f *model.Field, v model.Value) bool {
	return f.Default != nil && model.Equal(v, f.Normalize(f.Default)) ||
		f.ImportDefault != nil && model.Equal(v, f.Normalize(f.ImportDefault))
}
