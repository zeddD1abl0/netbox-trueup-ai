package devicetypelib

import (
	"fmt"
	"strconv"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// portMappingsKey is devicetype-library's list of front port mappings, each
// a front port's position mapped to a rear port's, both ports named. NetBox's
// import reads it with PortTemplateMappingImportForm (dcim/forms/
// object_import.py), after the device type's rear and front ports
// (DeviceTypeImportView.related_object_forms in dcim/views.py), and finds
// each port by name among the device type's. NetBox's API carries a front
// port's mappings in its rear_ports, keyed by the front port's position, so
// TrueUp folds each mapping into its front port
// (docs/adr/0027-nest-port-mappings-in-their-front-port.md).
const portMappingsKey = "port-mappings"

// The keys of a port mapping, as NetBox's import form and the library's
// schema (schema/components.json) name them.
const (
	mappingFrontPort         = "front_port"
	mappingFrontPortPosition = "front_port_position"
	mappingRearPort          = "rear_port"
	mappingRearPortPosition  = "rear_port_position"
)

// inventoryItemsKey is devicetype-library's list of inventory item
// templates, which TrueUp doesn't support: NetBox has deprecated them in
// favour of modules (docs/adr/0028-no-inventory-item-templates.md).
const inventoryItemsKey = "inventory-items"

// portMapping is one of a file's port mappings, read.
type portMapping struct {
	where string // such as port-mappings[3]
	item  *format.Node
	// front is the front port's name, and mapping the mapping as one of the
	// front port's rear_ports.
	front   model.String
	mapping model.Object
	// nodes holds the node of each key the item gives.
	nodes map[string]*format.Node
}

// portMappings reads a file's list of port mappings, reporting each problem
// with at, and adding each mapping's references to its ports to refs, which
// are checked once the device type's lists are read. It returns the
// mappings that have no errors of their own.
func (r *reader) portMappings(n *format.Node, at childAt, refs *[]pendingRef) []portMapping {
	if n.Kind != format.Sequence {
		at(format.Error, n, portMappingsKey, "isn't a list, which devicetype-library's "+portMappingsKey+" is")
		return nil
	}
	spec := r.specs["dcim.porttemplatemapping"]
	var out []portMapping
	for i, item := range n.Items {
		where := portMappingsKey + "[" + strconv.Itoa(i) + "]"
		if item.Kind != format.Mapping {
			at(format.Error, item, where, "is "+format.Describe(item)+", but each of devicetype-library's "+portMappingsKey+" is a mapping")
			continue
		}
		m := portMapping{
			where: where, item: item, nodes: map[string]*format.Node{},
			mapping: model.Object{Type: spec.Type, Fields: map[string]model.Value{}},
		}
		ok := true
		fail := func(n *format.Node, field, reason string) {
			at(format.Error, n, field, reason)
			ok = false
		}
		for _, pair := range item.Pairs {
			key, v := pair.Key.Value, pair.Value
			field := format.JoinField(where, key)
			switch key {
			case mappingFrontPort, mappingRearPort:
				m.nodes[key] = v
				typ := "dcim.frontporttemplate"
				if key == mappingRearPort {
					typ = "dcim.rearporttemplate"
				}
				name, why := r.portName(typ, v)
				if why != "" {
					fail(v, field, why)
					continue
				}
				ref := model.Reference{Type: typ, Key: map[string]model.Value{"name": name}}
				*refs = append(*refs, pendingRef{node: v, field: field, ref: ref})
				if key == mappingFrontPort {
					m.front = name
				} else {
					m.mapping.Fields["rear_port"] = ref
				}
			case mappingFrontPortPosition, mappingRearPortPosition:
				m.nodes[key] = v
				name := "position"
				if key == mappingRearPortPosition {
					name = "rear_port_position"
				}
				f := spec.Field(name)
				pos, err := format.Value(f, v)
				if err == nil {
					pos = f.Normalize(pos)
					err = f.Validate(pos)
				}
				if err != nil {
					fail(v, field, reason(err))
					continue
				}
				m.mapping.Fields[name] = pos
			default:
				at(format.Warning, pair.Key, field, "isn't a key TrueUp reads from devicetype-library's "+portMappingsKey+" yet, so it is ignored")
			}
		}
		// NetBox's import requires the ports, and fills a position left
		// out with its form's initial value, the model's default, though
		// the library's schema requires both. A front port position left
		// out is the key, so it takes that value; a rear port position
		// left out is left unset, as any field a file leaves out is.
		for _, key := range []string{mappingFrontPort, mappingRearPort} {
			if _, given := m.nodes[key]; !given {
				fail(item, where, "has no "+key+", which NetBox needs to map a front port")
			}
		}
		if _, given := m.nodes[mappingFrontPortPosition]; !given {
			m.mapping.Fields["position"] = spec.Field("position").ImportedDefault()
		}
		if ok {
			out = append(out, m)
		}
	}
	return out
}

// portName reads a mapping's port, by name, as NetBox's import finds it: a
// ModelChoiceField with to_field_name name, among the device type's ports of
// type typ, which looks for exactly that name, untrimmed, and refuses none.
func (r *reader) portName(typ string, n *format.Node) (model.String, string) {
	switch {
	case n.Kind != format.Scalar || n.Tag != format.TagStr:
		return "", "is " + format.Describe(n) + ", but it names one of the device type's " + collectionNoun(r.deviceType, typ) + ", as text"
	case n.Value == "":
		return "", "is blank, but NetBox's import needs one of the device type's " + collectionNoun(r.deviceType, typ)
	}
	return lookupName(r.specs[typ].Field("name"), n.Value, "template")
}

// foldMappings nests each port mapping in its front port, among the
// definition's front ports, once it has checked the mapping as NetBox's
// import would: a rear port position must be one of the rear port's
// (PortMappingBase.clean in dcim/models/base.py), and no two mappings may
// share a front port's position or a rear port's (PortMappingBase.Meta). A
// front port position beyond the front port's positions is no error to
// NetBox. A mapping whose port the file hasn't got is reported by
// references; one whose port has an error of its own isn't checked against
// it.
func (r *reader) foldMappings(def *format.Definition, mappings []portMapping, given *names, at childAt) {
	fronts, _ := def.Object.Fields["front_ports"].(model.Children)
	byName := map[model.Value]model.Object{}
	for _, fp := range fronts {
		byName[fp.Fields["name"]] = fp
	}
	// Each rear port's positions, as NetBox's import gives them where the
	// file leaves them out.
	defaultPositions, _ := r.specs["dcim.rearporttemplate"].Field("positions").ImportedDefault().(model.Integer)
	rears, _ := def.Object.Fields["rear_ports"].(model.Children)
	positions := map[model.Value]model.Integer{}
	for _, rp := range rears {
		n, set := rp.Fields["positions"].(model.Integer)
		if !set {
			n = defaultPositions
		}
		positions[rp.Fields["name"]] = n
	}
	defaultRearPosition := r.specs["dcim.porttemplatemapping"].Field("rear_port_position").ImportedDefault()

	type slot struct {
		port     model.Value
		position model.Value
	}
	frontSlots, rearSlots := map[slot]string{}, map[slot]string{}
	for _, m := range mappings {
		position, rearPosition := m.mapping.Fields["position"], m.mapping.Fields["rear_port_position"]
		if rearPosition == nil {
			rearPosition = defaultRearPosition
		}
		rear, _ := m.mapping.Fields["rear_port"].(model.Reference)
		rearName := rear.Key["name"]
		// A mapping that names a port the file hasn't got has that error
		// of its own, which references reports.
		if !given.has("dcim.frontporttemplate", m.front) || !given.has("dcim.rearporttemplate", rearName) {
			continue
		}
		frontSlot, rearSlot := slot{m.front, position}, slot{rearName, rearPosition}
		if n, known := positions[rearName]; known {
			if p, _ := rearPosition.(model.Integer); p > n {
				at(format.Error, m.node(mappingRearPortPosition), format.JoinField(m.where, mappingRearPortPosition),
					fmt.Sprintf("is %s, but rear-ports %s has only %d positions, so NetBox's import would refuse it", rearPosition, rearName, n))
				continue
			}
		}
		if first, taken := frontSlots[frontSlot]; taken {
			at(format.Error, m.node(mappingFrontPortPosition), format.JoinField(m.where, mappingFrontPortPosition),
				fmt.Sprintf("maps position %s of front-ports %s, as %s does, but NetBox maps each position of a front port once", position, m.front, first))
			continue
		}
		if first, taken := rearSlots[rearSlot]; taken {
			at(format.Error, m.node(mappingRearPortPosition), format.JoinField(m.where, mappingRearPortPosition),
				fmt.Sprintf("maps to position %s of rear-ports %s, as %s does, but NetBox maps each position of a rear port once", rearPosition, rearName, first))
			continue
		}
		// Only a mapping without an error of its own takes its positions,
		// as only a template without one takes its name: NetBox's import
		// stops at the first mapping it refuses, so it never blames a
		// later one for a position that one took. A port with an error of
		// its own is no error of the mapping's.
		frontSlots[frontSlot], rearSlots[rearSlot] = m.where, m.where
		if fp, found := byName[m.front]; found {
			nested, _ := fp.Fields["rear_ports"].(model.Children)
			fp.Fields["rear_ports"] = append(nested, m.mapping)
		}
	}
}

// node returns the node of one of the mapping's keys, or the mapping's own
// where the file leaves the key out.
func (m *portMapping) node(key string) *format.Node {
	if n := m.nodes[key]; n != nil {
		return n
	}
	return m.item
}
