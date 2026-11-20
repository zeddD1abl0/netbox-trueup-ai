package compare

import (
	"fmt"
	"maps"
	"strconv"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// UniqueValues returns d cut down to what matching it with NetBox's objects
// reads: its type, identity and path; the fields that its type's identity
// and uniqueness rules name; its references to objects that stand on their
// own, such as a device type's manufacturer, which matching resolves, and
// which make it a conflict where NetBox has several objects they could be;
// and which of those fields the format derived. TrueUp's database keeps it
// beside the whole definition, so that compare can find which definitions
// NetBox has without reading them whole
// (docs/adr/0030-compare-what-netbox-has-by-default.md).
//
// Compared with the same objects, it gets the whole's NetBox match, the
// collisions with objects that stand on their own, and the unresolved
// references, and so the whole's outcome, but for one case: a candidate
// whose NetBox object has two children with one key is a conflict, which
// only the whole, with its children, finds. FuzzCompare holds it to that.
func UniqueValues(specs []*model.Spec, d format.Definition) (format.Definition, error) {
	s := model.SpecOf(specs, d.Object.Type)
	if s == nil {
		return format.Definition{}, fmt.Errorf("a definition is a %s, which has no spec", strconv.Quote(d.Object.Type))
	}
	keep := map[string]bool{}
	for _, name := range s.Identity {
		keep[name] = true
	}
	for _, u := range s.Unique {
		for _, name := range u.Fields {
			keep[name] = true
		}
	}
	for _, f := range s.Fields {
		if f.Ref != nil && f.Ref.Within == "" {
			keep[f.Name] = true
		}
	}
	out := format.Definition{
		Object:   model.Object{Type: d.Object.Type, Fields: map[string]model.Value{}},
		Identity: maps.Clone(d.Identity),
		Path:     d.Path,
	}
	for name, v := range d.Object.Fields {
		if keep[name] {
			out.Object.Fields[name] = v
		}
	}
	for _, name := range d.Derived {
		if _, ok := out.Object.Fields[name]; ok {
			out.Derived = append(out.Derived, name)
		}
	}
	return out, nil
}
