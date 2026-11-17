package compare

import (
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

// compareChildren adds the rows of collection col of a definition of type
// s, whose fields are normalized, to r, and as collisions the NetBox
// children whose keys are another's, which NetBox's rules forbid. It fails
// if the collection's type has no spec, since its children, the
// definition's or NetBox's, couldn't be compared.
func (ix *index) compareChildren(r *Result, s *model.Spec, col *model.Collection, fields map[string]model.Value) error {
	cs := ix.specs[col.Type]
	if cs == nil {
		return fmt.Errorf("the spec of %s has %s, whose type %s has no spec", s.Type, col.Name, col.Type)
	}
	defs, _ := fields[col.Name].(model.Children)
	hasNetBox := r.NetBoxID != 0

	// NetBox's children of the match, by key.
	netbox := map[string][]int64{}
	netboxKeys := map[string]map[string]model.Value{}
	if hasNetBox {
		for _, id := range ix.children[s.Type][r.NetBoxID][col.Name] {
			k, key, ok := keyOf(col, ix.objects[col.Type][id].Fields)
			if !ok {
				// checkObserved makes sure a key field holds a value of its
				// kind, which is null only where the field takes null, and
				// then the child can be told from no other.
				k = "#" + strconv.FormatInt(id, 10)
			}
			if _, seen := netbox[k]; !seen {
				netboxKeys[k] = key
			}
			netbox[k] = append(netbox[k], id)
		}
		for _, k := range slices.Sorted(maps.Keys(netbox)) {
			ids := slices.Sorted(slices.Values(netbox[k]))
			if len(ids) < 2 {
				continue
			}
			for _, id := range ids {
				r.Collisions = append(r.Collisions, Collision{
					Path: childPath(col.Name, netboxKeys[k]), Rule: append([]string{col.Parent}, col.Key...),
					ID: id, Identity: netboxKeys[k],
				})
			}
		}
	}

	defined := map[string]bool{}
	for _, c := range defs {
		// checkDefinition makes sure each child has its key.
		k, key, _ := keyOf(col, c.Fields)
		defined[k] = true
		ids := netbox[k]
		row := Row{Path: childPath(col.Name, key), Cells: []model.Value{model.Boolean(true)}, Managed: true}
		var theirs map[string]model.Value
		if hasNetBox {
			row.Cells = append(row.Cells, model.Boolean(len(ids) > 0))
			if len(ids) == 1 {
				row.NetBoxID = ids[0]
				theirs = ix.objects[col.Type][ids[0]].Fields
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
			if theirs != nil {
				nb = ix.netboxValue(s, r.NetBoxID, f, theirs[f.Name])
			}
			if mine == nil && (nb == nil || isDefault(f, nb)) {
				continue
			}
			field := Row{Path: append(childPath(col.Name, key), Step{Field: f.Name}), Cells: []model.Value{mine}}
			if hasNetBox {
				field.Cells = append(field.Cells, nb)
			}
			field.Differs = differ(field.Cells)
			field.Managed = mine != nil
			r.addRow(field)
		}
	}

	// The children only NetBox has, by key, so that NetBox's order doesn't
	// matter.
	for _, k := range slices.Sorted(maps.Keys(netbox)) {
		if defined[k] {
			continue
		}
		row := Row{
			Path:    childPath(col.Name, netboxKeys[k]),
			Cells:   []model.Value{model.Boolean(false), model.Boolean(true)},
			Differs: true,
		}
		if ids := netbox[k]; len(ids) == 1 {
			row.NetBoxID = ids[0]
		}
		r.addRow(row)
	}
	return nil
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
