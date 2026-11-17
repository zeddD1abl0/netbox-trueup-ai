package devicetypelib

import (
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// componentList is one of devicetype-library's lists of component templates
// that TrueUp reads, and the device type's collection it fills.
type componentList struct {
	// Key is the list's key in a device type file, and Collection the
	// device type's collection (dcim's deviceTypeComponents).
	Key, Collection string
	// Fields are the keys of a list item that TrueUp reads, each as the
	// template's field of the same name: those NetBox's import form takes
	// (dcim/forms/object_import.py) and devicetype-library's schema allows
	// (schema/components.json), as for the device type's own fields.
	Fields []string
	// Dropped are keys the library's schema allows that TrueUp reads and
	// checks, but neither keeps nor compares, since NetBox's import ignores
	// them too, each with what it must be.
	Dropped map[string]format.Tag
}

// componentLists are the lists of component templates TrueUp reads, in the
// order NetBox's import creates them (DeviceTypeImportView in dcim/views.py),
// whatever the order of the file's keys. TrueUp reads them in the file's
// order, and checks each reference to another template once it has read
// them all (references).
var componentLists = []componentList{
	{
		Key: "console-ports", Collection: "console_ports",
		Fields: []string{"name", "label", "type", "description"},
		// A hint for the library's own tests, like is_powered.
		Dropped: map[string]format.Tag{"_is_power_source": format.TagBool},
	},
	{
		Key: "console-server-ports", Collection: "console_server_ports",
		Fields: []string{"name", "label", "type", "description"},
	},
	{
		Key: "power-ports", Collection: "power_ports",
		Fields: []string{"name", "label", "type", "maximum_draw", "allocated_draw", "description"},
	},
	{
		Key: "power-outlets", Collection: "power_outlets",
		Fields: []string{"name", "label", "type", "power_port", "feed_leg", "description"},
	},
	{
		Key: "interfaces", Collection: "interfaces",
		Fields: []string{"name", "label", "type", "enabled", "mgmt_only", "description", "poe_mode", "poe_type", "rf_role"},
		// NetBox's InterfaceTemplateImportForm has no bridge, so its own
		// import ignores the library's.
		Dropped: map[string]format.Tag{"bridge": format.TagStr},
	},
	{
		Key: "module-bays", Collection: "module_bays",
		Fields: []string{"name", "label", "position", "description"},
	},
	{
		Key: "device-bays", Collection: "device_bays",
		Fields: []string{"name", "label", "description"},
	},
}

// unmappedKeys are the lists of component templates that roadmap step 12
// reads, which a definition counts in Unmapped meanwhile.
var unmappedKeys = []string{"front-ports", "rear-ports", "port-mappings", "inventory-items"}

// componentListOf returns the list TrueUp reads under key, or nil.
func componentListOf(key string) *componentList {
	for i := range componentLists {
		if componentLists[i].Key == key {
			return &componentLists[i]
		}
	}
	return nil
}

// childAt reports a problem at a list item, or a key of one.
type childAt func(severity format.Severity, n *format.Node, field, reason string)

// pendingRef is a reference to another template of the device type, which
// is checked once all the device type's lists are read.
type pendingRef struct {
	node  *format.Node
	field string
	ref   model.Reference
}

// names holds, by template type, the names a file gives its templates, as
// NetBox would store them, whether or not the template has an error.
type names struct {
	given map[string]map[model.Value]bool
	// unread holds the types of the lists some of whose names can't be
	// read: a list that isn't one, or an item that isn't a mapping or has
	// no name TrueUp can read. Each of those has its own error.
	unread map[string]bool
}

func newNames() *names {
	return &names{given: map[string]map[model.Value]bool{}, unread: map[string]bool{}}
}

func (ns *names) add(typ string, name model.Value) {
	if ns.given[typ] == nil {
		ns.given[typ] = map[model.Value]bool{}
	}
	ns.given[typ][name] = true
}

// components reads one of a device type's lists of component templates
// into the children of the device type's collection, as NetBox's import
// would create them, reporting each problem with at, and adding each name
// to given and each reference to another template to refs. It returns
// false, and no children, if the value isn't a list.
func (r *reader) components(list *componentList, n *format.Node, at childAt, refs *[]pendingRef, given *names) (model.Children, bool) {
	col := r.deviceType.Collection(list.Collection)
	spec := r.specs[col.Type]
	if n.Kind != format.Sequence {
		at(format.Error, n, list.Key, "isn't a list, which devicetype-library's "+list.Key+" is")
		given.unread[spec.Type] = true
		return nil, false
	}
	children := make(model.Children, 0, len(n.Items))
	// Where each name is first given, to find a name given twice, which
	// NetBox keeps unique among a device type's templates of a kind.
	first := map[model.Value]int{}
	for i, item := range n.Items {
		where := list.Key + "[" + strconv.Itoa(i) + "]"
		if item.Kind != format.Mapping {
			at(format.Error, item, where, "is "+format.Describe(item)+", but each of devicetype-library's "+list.Key+" is a mapping")
			given.unread[spec.Type] = true
			continue
		}
		child, ok := r.component(list, spec, col, item, where, at, refs)
		if name, named := child.Fields["name"]; named {
			given.add(spec.Type, name)
		} else {
			given.unread[spec.Type] = true
		}
		if !ok {
			continue
		}
		key := child.Fields["name"]
		if j, seen := first[key]; seen {
			at(format.Error, item, format.JoinField(where, "name"),
				fmt.Sprintf("is %s, as %s[%d]'s is, but NetBox keeps a name unique among a device type's %s", key, list.Key, j, list.Key))
			continue
		}
		first[key] = i
		children = append(children, child)
	}
	return children, true
}

// component reads one list item as a child of spec's type. It returns false
// if the item has an error, which at reports.
func (r *reader) component(list *componentList, spec *model.Spec, col *model.Collection, item *format.Node, where string, at childAt, refs *[]pendingRef) (model.Object, bool) {
	child := model.Object{Type: spec.Type, Fields: map[string]model.Value{}}
	ok := true
	fail := func(n *format.Node, field, reason string) {
		at(format.Error, n, field, reason)
		ok = false
	}
	for _, pair := range item.Pairs {
		key, n := pair.Key.Value, pair.Value
		field := format.JoinField(where, key)
		if tag, dropped := list.Dropped[key]; dropped {
			if n.Kind != format.Scalar || n.Tag != tag {
				fail(n, field, "is "+format.Describe(n)+", but devicetype-library's "+key+" is "+tag.String())
			}
			continue
		}
		if !slices.Contains(list.Fields, key) {
			at(format.Warning, pair.Key, field, "isn't a key TrueUp reads from devicetype-library's "+list.Key+" yet, so it is ignored")
			continue
		}
		f := spec.Field(key)
		if f.Kind == model.KindReference {
			v, reason := r.within(f, n)
			if reason != "" {
				fail(n, field, reason)
				continue
			}
			if ref, isRef := v.(model.Reference); isRef {
				*refs = append(*refs, pendingRef{node: n, field: field, ref: ref})
			}
			child.Fields[key] = v
			continue
		}
		v, err := format.Value(f, n)
		if err != nil {
			fail(n, field, err.Error())
			continue
		}
		v = f.Normalize(v)
		if err := f.Validate(v); err != nil {
			if !errors.Is(err, model.ErrConfigurableChoice) {
				fail(n, field, reason(err))
				continue
			}
			// The NetBox TrueUp talks to may take it.
			at(format.Warning, n, field, reason(err))
		}
		child.Fields[key] = v
	}
	for _, f := range spec.Fields {
		if f.Name == col.Parent {
			continue
		}
		if _, set := child.Fields[f.Name]; !set && (f.Required || f.ImportRequired) && ok {
			fail(item, where, "has no "+f.Name+", which NetBox needs to create one of a device type's "+list.Key)
		}
	}
	return child, ok
}

// within reads a reference to another template of the same device type,
// such as a power outlet's power port, by name, as NetBox's import finds it:
// a ModelChoiceField with to_field_name name, among the device type's
// templates, looks for exactly that name, untrimmed, and takes a blank or
// null as none. Whether the device type has it is checked once its lists
// are read (references). It returns why it can't, if it can't.
func (r *reader) within(f *model.Field, n *format.Node) (model.Value, string) {
	if n.Kind == format.Scalar && n.Tag == format.TagNull {
		return model.Null{}, ""
	}
	if n.Kind != format.Scalar || n.Tag != format.TagStr {
		return nil, "is " + format.Describe(n) + ", but it names another of the device type's templates, as text"
	}
	if n.Value == "" {
		return model.Null{}, ""
	}
	target := r.specs[f.Ref.Types[0]]
	v, why := lookupName(target.Field("name"), n.Value, "template")
	if why != "" {
		return nil, why
	}
	return model.Reference{Type: target.Type, Key: map[string]model.Value{"name": v}}, ""
}

// references checks that each reference to another template of the device
// type names one the file gives, as NetBox's import does: it refuses a name
// it can't find among the templates it has created, which it creates list
// by list, in the order of componentLists, and item by item. A power
// outlet's power port is in an earlier list, so any of the file's power
// ports will do; a reference within one list, such as an interface's parent,
// which TrueUp doesn't read, would have to name an earlier item. A template
// the file gives with an error of its own counts, so that what refers to it
// isn't blamed too; so does any name in a list some of whose names can't be
// read, as the reference may name one of those.
func (r *reader) references(refs []pendingRef, given *names, at childAt) {
	for _, p := range refs {
		if given.unread[p.ref.Type] {
			continue
		}
		if !given.given[p.ref.Type][p.ref.Key["name"]] {
			at(format.Error, p.node, p.field,
				fmt.Sprintf("names %s, which isn't one of the device type's %s, so NetBox's import wouldn't find it", p.ref.Key["name"], collectionNoun(r.deviceType, p.ref.Type)))
		}
	}
}

// collectionNoun names the library's list that holds templates of type typ,
// such as power-ports.
func collectionNoun(dt *model.Spec, typ string) string {
	for _, list := range componentLists {
		if col := dt.Collection(list.Collection); col != nil && col.Type == typ {
			return list.Key
		}
	}
	return typ
}
