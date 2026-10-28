package dcim

import "github.com/zeddD1abl0/netbox-trueup-ai/internal/model"

// Prototype specs of the component templates that roadmap steps 11 and 12
// bring, to show that the object model already holds what they need, checked
// against NetBox's schema like the real specs:
//
//   - child collections of a device type, each with its key;
//   - front port mappings, which have no endpoint of their own: a front port
//     template carries them in rear_ports, NetBox replaces them all whenever
//     that field is written, and each is told apart by its position on the
//     front port (reconcile_port_mappings in dcim/utils.py);
//   - references to another template of the same device type, such as a
//     mapping's rear port, by ID;
//   - inventory item templates as a tree, with the gap in the uniqueness of
//     top-level items, and a generic reference to a component.
//
// Steps 11 and 12 move these into the package and complete them.

// withPrototypes returns the package's specs with the prototypes, and the
// device type with the collections that hold them.
func withPrototypes() []*model.Spec {
	dt := *deviceType
	dt.Children = []model.Collection{
		{Name: "front_ports", Type: "dcim.frontporttemplate", Parent: "device_type", Key: []string{"name"}},
		{Name: "rear_ports", Type: "dcim.rearporttemplate", Parent: "device_type", Key: []string{"name"}},
		{
			Name: "inventory_items", Type: "dcim.inventoryitemtemplate", Parent: "device_type", Key: []string{"name"},
			Gap: "a top-level item's parent is null, and PostgreSQL treats nulls as distinct, so NetBox accepts two top-level items of the same name",
		},
	}
	return []*model.Spec{manufacturer, &dt, rearPortTemplate, frontPortTemplate, portTemplateMapping, inventoryItemTemplate}
}

var ignoredTemplate = []model.Ignored{
	{Name: "id", Reason: "NetBox's ID for the object"},
	{Name: "url", Reason: "read-only"},
	{Name: "display", Reason: "read-only"},
	{Name: "created", Reason: "read-only"},
	{Name: "last_updated", Reason: "read-only"},
	{Name: "module_type", Reason: "module types are outside milestone 1; a device type's templates leave it null"},
}

func portFields(choicesOf string) []model.Field {
	return []model.Field{
		{
			Name: "device_type", Kind: model.KindReference, Nullable: true, CreateOnly: true, Default: model.Null{},
			Ref: &model.Ref{Types: []string{"dcim.devicetype"}},
		},
		{Name: "name", Kind: model.KindString, Required: true, MaxLength: 64, Trim: true},
		{Name: "label", Kind: model.KindString, Blank: true, Default: model.String(""), MaxLength: 64, Trim: true},
		{Name: "type", Kind: model.KindChoice, Required: true, Choices: &model.Choices{Values: choices(choicesOf, "type")}},
		{Name: "color", Kind: model.KindString, Blank: true, Default: model.String(""), MaxLength: 6, Pattern: "[0-9a-f]{6}", Trim: true},
		{Name: "positions", Kind: model.KindInteger, Min: model.Integer(1), Max: model.Integer(1024), Default: model.Integer(1)},
		{Name: "description", Kind: model.KindString, Blank: true, Default: model.String(""), MaxLength: 200, Trim: true},
	}
}

var templateUnique = []model.Unique{{
	Fields: []string{"device_type", "name"},
	Gap:    "a module type's templates have a null device_type, and a rule of their own",
	Source: model.Source{File: "dcim/models/device_component_templates.py", Symbol: "ModularComponentTemplateModel.Meta"},
}}

var rearPortTemplate = &model.Spec{
	Type:      "dcim.rearporttemplate",
	Endpoint:  "/api/dcim/rear-port-templates/",
	DependsOn: []string{"dcim.devicetype"},
	Unique:    templateUnique,
	Fields:    portFields("RearPortTemplate"),
	Ignored: ignored(ignoredTemplate, []model.Ignored{
		{Name: "front_ports", Reason: "the mappings again, which the front port templates carry"},
	}),
	Sources: []model.Source{
		{File: "dcim/models/device_component_templates.py", Symbol: "RearPortTemplate"},
		{File: "dcim/api/serializers_/devicetype_components.py", Symbol: "RearPortTemplateSerializer"},
	},
}

var frontPortTemplate = &model.Spec{
	Type:      "dcim.frontporttemplate",
	Endpoint:  "/api/dcim/front-port-templates/",
	DependsOn: []string{"dcim.devicetype"},
	Unique:    templateUnique,
	Fields:    portFields("FrontPortTemplate"),
	Children: []model.Collection{{
		Name: "rear_ports", Type: "dcim.porttemplatemapping", Key: []string{"position"}, Nested: true,
		Source: model.Source{File: "dcim/utils.py", Symbol: "reconcile_port_mappings"},
	}},
	Ignored: ignoredTemplate,
	Sources: []model.Source{
		{File: "dcim/models/device_component_templates.py", Symbol: "FrontPortTemplate"},
		{File: "dcim/api/serializers_/devicetype_components.py", Symbol: "FrontPortTemplateSerializer"},
		{File: "dcim/api/serializers_/base.py", Symbol: "PortSerializer.update"},
	},
}

var portTemplateMapping = &model.Spec{
	Type: "dcim.porttemplatemapping",
	Unique: []model.Unique{{
		Fields: []string{"rear_port", "rear_port_position"},
		Source: model.Source{File: "dcim/models/base.py", Symbol: "PortMappingBase.Meta"},
	}},
	Fields: []model.Field{
		{Name: "position", Kind: model.KindInteger, Required: true},
		{
			Name: "rear_port", Kind: model.KindReference, Required: true,
			Ref: &model.Ref{Types: []string{"dcim.rearporttemplate"}, Within: "dcim.devicetype", Shape: model.ShapeID},
		},
		{Name: "rear_port_position", Kind: model.KindInteger, Min: model.Integer(1), Max: model.Integer(1024), Default: model.Integer(1)},
	},
	Sources: []model.Source{
		{File: "dcim/models/device_component_templates.py", Symbol: "PortTemplateMapping"},
		{File: "dcim/api/serializers_/devicetype_components.py", Symbol: "FrontPortTemplateMappingSerializer"},
	},
}

var inventoryItemTemplate = &model.Spec{
	Type:      "dcim.inventoryitemtemplate",
	Endpoint:  "/api/dcim/inventory-item-templates/",
	DependsOn: []string{"dcim.devicetype", "dcim.manufacturer"},
	Unique: []model.Unique{{
		Fields: []string{"device_type", "parent", "name"},
		Gap:    "a top-level item's parent is null, and the constraint doesn't set nulls_distinct=False, so NetBox accepts two top-level items of the same name",
		Source: model.Source{File: "dcim/models/device_component_templates.py", Symbol: "InventoryItemTemplate.Meta"},
	}},
	Fields: []model.Field{
		{Name: "device_type", Kind: model.KindReference, Required: true, CreateOnly: true, Ref: &model.Ref{Types: []string{"dcim.devicetype"}}},
		{
			Name: "parent", Kind: model.KindReference, Nullable: true, Default: model.Null{},
			Ref: &model.Ref{Types: []string{"dcim.inventoryitemtemplate"}, Within: "dcim.devicetype", Shape: model.ShapeID},
		},
		{Name: "name", Kind: model.KindString, Required: true, MaxLength: 64, Trim: true},
		{Name: "label", Kind: model.KindString, Blank: true, Default: model.String(""), MaxLength: 64, Trim: true},
		{
			Name: "manufacturer", Kind: model.KindReference, Nullable: true, Default: model.Null{},
			Ref: &model.Ref{Types: []string{"dcim.manufacturer"}},
		},
		{Name: "part_id", Kind: model.KindString, Blank: true, Default: model.String(""), MaxLength: 50, Trim: true},
		{Name: "description", Kind: model.KindString, Blank: true, Default: model.String(""), MaxLength: 200, Trim: true},
		{
			Name: "component", Kind: model.KindReference, Nullable: true, Default: model.Null{},
			Ref: &model.Ref{
				Types:  []string{"dcim.frontporttemplate", "dcim.rearporttemplate"},
				Within: "dcim.devicetype", Shape: model.ShapeTypeAndID, TypeField: "component_type", IDField: "component_id",
			},
		},
	},
	Children: []model.Collection{{
		Name: "child_items", Type: "dcim.inventoryitemtemplate", Parent: "parent", Key: []string{"name"}, Shared: []string{"device_type"},
	}},
	Ignored: ignored(ignoredTemplate[:5], []model.Ignored{
		{Name: "role", Reason: "a reference to a dcim.inventoryitemrole, which has no spec yet"},
		{Name: "_depth", Reason: "read-only: how deep in the tree the item is"},
	}),
	Sources: []model.Source{
		{File: "dcim/models/device_component_templates.py", Symbol: "InventoryItemTemplate"},
		{File: "dcim/api/serializers_/devicetype_components.py", Symbol: "InventoryItemTemplateSerializer"},
	},
}
