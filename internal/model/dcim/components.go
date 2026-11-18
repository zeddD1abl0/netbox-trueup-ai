package dcim

import (
	"slices"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// The component templates of a device type: what each device of the type is
// created with. Each is a child of its device type, in one of the device
// type's collections, and told apart from the type's other templates of its
// kind by its name.
//
// NetBox's own import creates them from the device type's lists, filling
// each field a list item leaves out from its import form's initial value
// (BulkImportView._save_object in netbox/views/generic/bulk_views.py). For
// these templates, that is the field's REST default, or a blank choice where
// the REST default is null, which NetBox's API reads back as null, so none
// needs an ImportDefault.
//
// Some of NetBox's rules span fields, and aren't in the specs: a power
// port's allocated draw is at most its maximum (PowerPortTemplate.clean),
// an interface's PoE type needs a PoE mode, only wireless interfaces have a
// wireless role, and channels need a channelized parent
// (InterfaceValidationMixin.clean), a device type with device bays is a
// parent (DeviceBayTemplate.clean), a mapping's rear port position is one
// of its rear port's (PortMappingBase.clean), and once a port is saved, its
// positions can't drop below the number of its mappings
// (FrontPortTemplate.clean, RearPortTemplate.clean).

// templateDeviceType is the device type a modular component template
// belongs to. A module type's templates have none, so it is nullable, but a
// device type's always have it, and can't move to another
// (ComponentTemplateModel.clean).
var templateDeviceType = model.Field{
	Name: "device_type", Kind: model.KindReference, Nullable: true, CreateOnly: true, Default: model.Null{},
	Ref:    &model.Ref{Types: []string{"dcim.devicetype"}},
	Source: model.Source{File: "dcim/models/device_component_templates.py", Symbol: "ComponentTemplateModel.clean"},
}

var (
	templateName  = model.Field{Name: "name", Kind: model.KindString, Required: true, MaxLength: 64, Trim: true}
	templateLabel = model.Field{Name: "label", Kind: model.KindString, Blank: true, Default: model.String(""), MaxLength: 64, Trim: true}
	templateDesc  = model.Field{Name: "description", Kind: model.KindString, Blank: true, Default: model.String(""), MaxLength: 200, Trim: true}
)

// templateUnique is the rule that a modular component template's name is
// unique among its device type's templates of its kind.
var templateUnique = model.Unique{
	Fields: []string{"device_type", "name"},
	Gap:    "a module type's templates have a null device_type, and a rule of their own",
	Source: model.Source{File: "dcim/models/device_component_templates.py", Symbol: "ModularComponentTemplateModel.Meta"},
}

// ignoredTemplate lists the fields of a component template that the specs
// leave out.
var ignoredTemplate = []model.Ignored{
	{Name: "id", Reason: "NetBox's ID for the object, which references and links use, not a field TrueUp manages"},
	{Name: "url", Reason: "read-only: the object's API URL"},
	{Name: "display", Reason: "read-only: the object's name as NetBox shows it"},
	{Name: "created", Reason: "read-only: when NetBox created the object"},
	{Name: "last_updated", Reason: "read-only: when NetBox last changed the object"},
}

// ignoredModuleType is a modular component template's module type.
var ignoredModuleType = model.Ignored{
	Name: "module_type", Reason: "module types are outside milestone 1; a device type's templates leave it null",
}

// optionalChoice is a choice field that may be left blank, which NetBox
// stores as null and reads back as null either way.
//
// A console port's type is one too, but its serializer's ChoiceField has no
// allow_null, so NetBox's API takes null as the blank choice, gives either
// back as null, and never holds a null of its own: blankChoice.
func optionalChoice(name, component string, source model.Source) model.Field {
	return model.Field{
		Name: name, Kind: model.KindChoice,
		Nullable: true, Blank: true, BlankIsNull: true, Default: model.Null{},
		Choices: &model.Choices{Values: choices(component, name)},
		Source:  source,
	}
}

// blankChoice is a choice field that may be blank but not null, whose blank
// choice NetBox's API gives as null (ChoiceField.to_representation in
// netbox/api/fields.py), and takes null for (validate_empty_values). Its
// column is nullable, and NetBox's own import leaves it null, which the API
// gives as null too, so TrueUp can't tell the two apart and needn't.
func blankChoice(name, component string, source model.Source) model.Field {
	return model.Field{
		Name: name, Kind: model.KindChoice, Blank: true, Default: model.Choice(""),
		Choices: &model.Choices{Values: choices(component, name)},
		Source:  source,
	}
}

// within refers to another template of the same device type, by its name.
func within(typ string) *model.Ref {
	return &model.Ref{Types: []string{typ}, Within: "dcim.devicetype"}
}

var consolePortTemplate = &model.Spec{
	Type:      "dcim.consoleporttemplate",
	Endpoint:  "/api/dcim/console-port-templates/",
	DependsOn: []string{"dcim.devicetype"},
	Unique:    []model.Unique{templateUnique},
	Fields: []model.Field{
		templateDeviceType, templateName, templateLabel,
		blankChoice("type", "ConsolePortTemplate", model.Source{File: "dcim/choices.py", Symbol: "ConsolePortTypeChoices"}),
		templateDesc,
	},
	Ignored: slices.Concat(ignoredTemplate, []model.Ignored{ignoredModuleType}),
	Sources: []model.Source{
		{File: "dcim/models/device_component_templates.py", Symbol: "ConsolePortTemplate"},
		{File: "dcim/api/serializers_/devicetype_components.py", Symbol: "ConsolePortTemplateSerializer"},
		{File: "dcim/forms/object_import.py", Symbol: "ConsolePortTemplateImportForm"},
	},
}

var consoleServerPortTemplate = &model.Spec{
	Type:      "dcim.consoleserverporttemplate",
	Endpoint:  "/api/dcim/console-server-port-templates/",
	DependsOn: []string{"dcim.devicetype"},
	Unique:    []model.Unique{templateUnique},
	Fields: []model.Field{
		templateDeviceType, templateName, templateLabel,
		blankChoice("type", "ConsoleServerPortTemplate", model.Source{File: "dcim/choices.py", Symbol: "ConsolePortTypeChoices"}),
		templateDesc,
	},
	Ignored: slices.Concat(ignoredTemplate, []model.Ignored{ignoredModuleType}),
	Sources: []model.Source{
		{File: "dcim/models/device_component_templates.py", Symbol: "ConsoleServerPortTemplate"},
		{File: "dcim/api/serializers_/devicetype_components.py", Symbol: "ConsoleServerPortTemplateSerializer"},
		{File: "dcim/forms/object_import.py", Symbol: "ConsoleServerPortTemplateImportForm"},
	},
}

// draw is a power port's maximum or allocated draw, in watts: a
// PositiveIntegerField, at least 1.
func draw(name string) model.Field {
	return model.Field{
		Name: name, Kind: model.KindInteger, Nullable: true, Default: model.Null{},
		Min: model.Integer(1), Max: model.Integer(2147483647),
	}
}

var powerPortTemplate = &model.Spec{
	Type:      "dcim.powerporttemplate",
	Endpoint:  "/api/dcim/power-port-templates/",
	DependsOn: []string{"dcim.devicetype"},
	Unique:    []model.Unique{templateUnique},
	Fields: []model.Field{
		templateDeviceType, templateName, templateLabel,
		optionalChoice("type", "PowerPortTemplate", model.Source{File: "dcim/choices.py", Symbol: "PowerPortTypeChoices"}),
		draw("maximum_draw"), draw("allocated_draw"),
		templateDesc,
	},
	Ignored: slices.Concat(ignoredTemplate, []model.Ignored{ignoredModuleType}),
	Sources: []model.Source{
		{File: "dcim/models/device_component_templates.py", Symbol: "PowerPortTemplate"},
		{File: "dcim/api/serializers_/devicetype_components.py", Symbol: "PowerPortTemplateSerializer"},
		{File: "dcim/forms/object_import.py", Symbol: "PowerPortTemplateImportForm"},
	},
}

var powerOutletTemplate = &model.Spec{
	Type:      "dcim.poweroutlettemplate",
	Endpoint:  "/api/dcim/power-outlet-templates/",
	DependsOn: []string{"dcim.devicetype"},
	Unique:    []model.Unique{templateUnique},
	Fields: []model.Field{
		templateDeviceType, templateName, templateLabel,
		optionalChoice("type", "PowerOutletTemplate", model.Source{File: "dcim/choices.py", Symbol: "PowerOutletTypeChoices"}),
		{Name: "color", Kind: model.KindString, Blank: true, Default: model.String(""), MaxLength: 6, Pattern: "[0-9a-f]{6}", Trim: true},
		{
			// The power port that feeds the outlet, which must be one of
			// the same device type's (PowerOutletTemplate.clean). NetBox's
			// import finds it by name among them.
			Name: "power_port", Kind: model.KindReference, Nullable: true, Default: model.Null{},
			Ref:    within("dcim.powerporttemplate"),
			Source: model.Source{File: "dcim/forms/object_import.py", Symbol: "PowerOutletTemplateImportForm.clean_device_type"},
		},
		optionalChoice("feed_leg", "PowerOutletTemplate", model.Source{File: "dcim/choices.py", Symbol: "PowerOutletFeedLegChoices"}),
		templateDesc,
	},
	Ignored: slices.Concat(ignoredTemplate, []model.Ignored{ignoredModuleType}),
	Sources: []model.Source{
		{File: "dcim/models/device_component_templates.py", Symbol: "PowerOutletTemplate"},
		{File: "dcim/api/serializers_/devicetype_components.py", Symbol: "PowerOutletTemplateSerializer"},
		{File: "dcim/forms/object_import.py", Symbol: "PowerOutletTemplateImportForm"},
	},
}

// channel is an interface's channel count or channel ID: a
// PositiveSmallIntegerField between INTERFACE_CHANNELS_MIN and
// INTERFACE_CHANNELS_MAX.
func channel(name string) model.Field {
	return model.Field{
		Name: name, Kind: model.KindInteger, Nullable: true, Default: model.Null{},
		Min: model.Integer(1), Max: model.Integer(1024),
		Source: model.Source{File: "dcim/constants.py", Symbol: "INTERFACE_CHANNELS_MAX"},
	}
}

var interfaceTemplate = &model.Spec{
	Type:      "dcim.interfacetemplate",
	Endpoint:  "/api/dcim/interface-templates/",
	DependsOn: []string{"dcim.devicetype"},
	Unique: []model.Unique{
		templateUnique,
		{
			Fields: []string{"parent", "channel_id"},
			Gap:    "an interface without a parent or a channel ID has null there, and PostgreSQL treats nulls as distinct",
			Source: model.Source{File: "dcim/models/device_component_templates.py", Symbol: "InterfaceTemplate.Meta"},
		},
	},
	Fields: []model.Field{
		templateDeviceType, templateName, templateLabel,
		{
			Name: "type", Kind: model.KindChoice, Required: true,
			Choices: &model.Choices{Values: choices("InterfaceTemplate", "type")},
			Source:  model.Source{File: "dcim/choices.py", Symbol: "InterfaceTypeChoices"},
		},
		channel("channels"), channel("channel_id"),
		{Name: "enabled", Kind: model.KindBoolean, Default: model.Boolean(true)},
		{Name: "mgmt_only", Kind: model.KindBoolean, Default: model.Boolean(false)},
		templateDesc,
		{
			// NetBox's import finds a parent by name among the device
			// type's interfaces; devicetype-library doesn't set it.
			Name: "parent", Kind: model.KindReference, Nullable: true, Default: model.Null{},
			Ref:    within("dcim.interfacetemplate"),
			Source: model.Source{File: "dcim/forms/object_import.py", Symbol: "InterfaceTemplateImportForm.clean_device_type"},
		},
		{
			// NetBox's import doesn't take a bridge, so TrueUp doesn't read
			// devicetype-library's, but its API does.
			Name: "bridge", Kind: model.KindReference, Nullable: true, Default: model.Null{},
			Ref:    within("dcim.interfacetemplate"),
			Source: model.Source{File: "dcim/models/device_component_templates.py", Symbol: "InterfaceTemplate.clean"},
		},
		optionalChoice("poe_mode", "InterfaceTemplate", model.Source{File: "dcim/choices.py", Symbol: "InterfacePoEModeChoices"}),
		optionalChoice("poe_type", "InterfaceTemplate", model.Source{File: "dcim/choices.py", Symbol: "InterfacePoETypeChoices"}),
		optionalChoice("rf_role", "InterfaceTemplate", model.Source{File: "wireless/choices.py", Symbol: "WirelessRoleChoices"}),
	},
	Ignored: slices.Concat(ignoredTemplate, []model.Ignored{ignoredModuleType}),
	Sources: []model.Source{
		{File: "dcim/models/device_component_templates.py", Symbol: "InterfaceTemplate"},
		{File: "dcim/models/mixins.py", Symbol: "InterfaceValidationMixin.clean"},
		{File: "dcim/api/serializers_/devicetype_components.py", Symbol: "InterfaceTemplateSerializer"},
		{File: "dcim/forms/object_import.py", Symbol: "InterfaceTemplateImportForm"},
	},
}

var moduleBayTemplate = &model.Spec{
	Type:      "dcim.modulebaytemplate",
	Endpoint:  "/api/dcim/module-bay-templates/",
	DependsOn: []string{"dcim.devicetype"},
	Unique:    []model.Unique{templateUnique},
	Fields: []model.Field{
		templateDeviceType, templateName, templateLabel,
		{Name: "position", Kind: model.KindString, Blank: true, Default: model.String(""), MaxLength: 30, Trim: true},
		{Name: "enabled", Kind: model.KindBoolean, Default: model.Boolean(true)},
		templateDesc,
	},
	Ignored: slices.Concat(ignoredTemplate, []model.Ignored{
		ignoredModuleType,
		{Name: "module_bay_types", Reason: "references to dcim.modulebaytype, which has no spec until module types (milestone 8); devicetype-library doesn't set them, and TrueUp leaves them as they are"},
	}),
	Sources: []model.Source{
		{File: "dcim/models/device_component_templates.py", Symbol: "ModuleBayTemplate"},
		{File: "dcim/api/serializers_/devicetype_components.py", Symbol: "ModuleBayTemplateSerializer"},
		{File: "dcim/forms/object_import.py", Symbol: "ModuleBayTemplateImportForm"},
	},
}

// portFields are the fields of a front or rear port template, whose type
// is from choices for component, such as FrontPortTemplate. Its positions
// are how many connections the port carries, such as the 12 fibres of an
// MPO rear port: a PositiveSmallIntegerField between PORT_POSITION_MIN and
// PORT_POSITION_MAX.
func portFields(component string) []model.Field {
	return []model.Field{
		templateDeviceType, templateName, templateLabel,
		{
			Name: "type", Kind: model.KindChoice, Required: true, Choices: &model.Choices{Values: choices(component, "type")},
			Source: model.Source{File: "dcim/choices.py", Symbol: "PortTypeChoices"},
		},
		{
			// A ColorField: six lowercase hexadecimal digits, or blank
			// (ColorValidator in utilities/validators.py).
			Name: "color", Kind: model.KindString, Blank: true, Default: model.String(""), MaxLength: 6, Pattern: "[0-9a-f]{6}", Trim: true,
			Source: model.Source{File: "utilities/fields.py", Symbol: "ColorField"},
		},
		{
			Name: "positions", Kind: model.KindInteger, Min: model.Integer(1), Max: model.Integer(1024), Default: model.Integer(1),
			Source: model.Source{File: "dcim/constants.py", Symbol: "PORT_POSITION_MAX"},
		},
		templateDesc,
	}
}

// rearPortTemplate is a port on the back of a device that passes through to
// front ports. Its front_ports are the mappings again, seen from the rear
// port, which the front port templates carry.
var rearPortTemplate = &model.Spec{
	Type:      "dcim.rearporttemplate",
	Endpoint:  "/api/dcim/rear-port-templates/",
	DependsOn: []string{"dcim.devicetype"},
	Unique:    []model.Unique{templateUnique},
	Fields:    portFields("RearPortTemplate"),
	Ignored: slices.Concat(ignoredTemplate, []model.Ignored{
		ignoredModuleType,
		{Name: "front_ports", Reason: "the mappings again, seen from the rear port, which the front port templates carry"},
	}),
	Sources: []model.Source{
		{File: "dcim/models/device_component_templates.py", Symbol: "RearPortTemplate"},
		{File: "dcim/api/serializers_/devicetype_components.py", Symbol: "RearPortTemplateSerializer"},
		{File: "dcim/forms/object_import.py", Symbol: "RearPortTemplateImportForm"},
	},
}

// frontPortTemplate is a port on the front of a device, with its mappings
// to rear ports nested in rear_ports: they have no endpoint of their own
// (PortMappingBase._netbox_private), and NetBox replaces the whole set
// whenever rear_ports is written, leaving it alone when it isn't
// (PortSerializer.update).
var frontPortTemplate = &model.Spec{
	Type:      "dcim.frontporttemplate",
	Endpoint:  "/api/dcim/front-port-templates/",
	DependsOn: []string{"dcim.devicetype"},
	Unique:    []model.Unique{templateUnique},
	Fields:    portFields("FrontPortTemplate"),
	Children: []model.Collection{{
		// A mapping's front port position tells it from the front port's
		// others: NetBox keeps it unique among them, and takes it as the
		// mapping's identity when it writes the set (reconcile_port_mappings
		// in dcim/utils.py).
		Name: "rear_ports", Type: "dcim.porttemplatemapping", Key: []string{"position"}, Nested: true,
		Source: model.Source{File: "dcim/models/base.py", Symbol: "PortMappingBase.Meta"},
	}},
	Ignored: slices.Concat(ignoredTemplate, []model.Ignored{ignoredModuleType}),
	Sources: []model.Source{
		{File: "dcim/models/device_component_templates.py", Symbol: "FrontPortTemplate"},
		{File: "dcim/api/serializers_/devicetype_components.py", Symbol: "FrontPortTemplateSerializer"},
		{File: "dcim/api/serializers_/base.py", Symbol: "PortSerializer.update"},
		{File: "dcim/forms/object_import.py", Symbol: "FrontPortTemplateImportForm"},
	},
}

// portTemplateMapping maps one position of a front port template to one
// position of a rear port template of the same device type
// (PortTemplateMapping.clean). NetBox's API gives it within its front port,
// with the front port's position as position and the rear port by its ID
// alone (FrontPortTemplateMappingSerializer).
//
// NetBox's model bounds both positions by PORT_POSITION_MIN and
// PORT_POSITION_MAX, and its import checks them, but its API's position is a
// plain IntegerField, and its API runs none of a mapping's model checks:
// ValidatedModelSerializer.validate (netbox/api/serializers/base.py) leaves
// out the nested mappings before it runs full_clean. So NetBox can hold a
// mapping its import would refuse, such as one at position 0, which TrueUp
// compares as it is. A rear port position must be one of the rear port's
// (PortMappingBase.clean); a front port position beyond the front port's
// positions is no error to NetBox, even to its import.
var portTemplateMapping = &model.Spec{
	Type: "dcim.porttemplatemapping",
	Unique: []model.Unique{{
		Fields: []string{"rear_port", "rear_port_position"},
		Source: model.Source{File: "dcim/models/base.py", Symbol: "PortMappingBase.Meta"},
	}},
	Fields: []model.Field{
		{
			// NetBox's API requires it, but its import fills it from the
			// model's default, 1, as the form's initial value.
			Name: "position", Kind: model.KindInteger, Required: true, ImportDefault: model.Integer(1),
			Min: model.Integer(1), Max: model.Integer(1024),
			TighterBounds: model.Source{File: "dcim/models/base.py", Symbol: "PortMappingBase"},
		},
		{
			// NetBox's import finds it by name among the device type's rear
			// ports (PortTemplateMappingImportForm.clean_device_type).
			Name: "rear_port", Kind: model.KindReference, Required: true,
			Ref:    &model.Ref{Types: []string{"dcim.rearporttemplate"}, Within: "dcim.devicetype", Shape: model.ShapeID},
			Source: model.Source{File: "dcim/models/device_component_templates.py", Symbol: "PortTemplateMapping.clean"},
		},
		{
			Name: "rear_port_position", Kind: model.KindInteger, Min: model.Integer(1), Max: model.Integer(1024), Default: model.Integer(1),
			Source: model.Source{File: "dcim/models/base.py", Symbol: "PortMappingBase.clean"},
		},
	},
	Sources: []model.Source{
		{File: "dcim/models/device_component_templates.py", Symbol: "PortTemplateMapping"},
		{File: "dcim/api/serializers_/devicetype_components.py", Symbol: "FrontPortTemplateMappingSerializer"},
		{File: "dcim/forms/object_import.py", Symbol: "PortTemplateMappingImportForm"},
		{File: "dcim/utils.py", Symbol: "reconcile_port_mappings"},
	},
}

// deviceBayTemplate isn't modular: only a device type has device bays, so
// its device type is never null.
var deviceBayTemplate = &model.Spec{
	Type:      "dcim.devicebaytemplate",
	Endpoint:  "/api/dcim/device-bay-templates/",
	DependsOn: []string{"dcim.devicetype"},
	Unique: []model.Unique{{
		Fields: []string{"device_type", "name"},
		Source: model.Source{File: "dcim/models/device_component_templates.py", Symbol: "ComponentTemplateModel.Meta"},
	}},
	Fields: []model.Field{
		{
			Name: "device_type", Kind: model.KindReference, Required: true, CreateOnly: true,
			Ref:    &model.Ref{Types: []string{"dcim.devicetype"}},
			Source: model.Source{File: "dcim/models/device_component_templates.py", Symbol: "ComponentTemplateModel.clean"},
		},
		templateName, templateLabel,
		{Name: "enabled", Kind: model.KindBoolean, Default: model.Boolean(true)},
		templateDesc,
	},
	Ignored: ignoredTemplate,
	Sources: []model.Source{
		{File: "dcim/models/device_component_templates.py", Symbol: "DeviceBayTemplate"},
		{File: "dcim/api/serializers_/devicetype_components.py", Symbol: "DeviceBayTemplateSerializer"},
		{File: "dcim/forms/object_import.py", Symbol: "DeviceBayTemplateImportForm"},
	},
}

// deviceTypeComponents are the device type's collections of component
// templates, in the order of NetBox's import, which creates rear ports
// before the front ports that map to them
// (DeviceTypeImportView.related_object_forms in dcim/views.py). TrueUp
// doesn't support inventory item templates, which NetBox has deprecated
// (docs/adr/0028-no-inventory-item-templates.md).
var deviceTypeComponents = []model.Collection{
	{Name: "console_ports", Type: "dcim.consoleporttemplate", Parent: "device_type", Key: []string{"name"}},
	{Name: "console_server_ports", Type: "dcim.consoleserverporttemplate", Parent: "device_type", Key: []string{"name"}},
	{Name: "power_ports", Type: "dcim.powerporttemplate", Parent: "device_type", Key: []string{"name"}},
	{Name: "power_outlets", Type: "dcim.poweroutlettemplate", Parent: "device_type", Key: []string{"name"}},
	{Name: "interfaces", Type: "dcim.interfacetemplate", Parent: "device_type", Key: []string{"name"}},
	{Name: "rear_ports", Type: "dcim.rearporttemplate", Parent: "device_type", Key: []string{"name"}},
	{Name: "front_ports", Type: "dcim.frontporttemplate", Parent: "device_type", Key: []string{"name"}},
	{Name: "module_bays", Type: "dcim.modulebaytemplate", Parent: "device_type", Key: []string{"name"}},
	{Name: "device_bays", Type: "dcim.devicebaytemplate", Parent: "device_type", Key: []string{"name"}},
}
