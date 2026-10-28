package dcim

import "github.com/zeddD1abl0/netbox-trueup-ai/internal/model"

// deviceType is dcim.devicetype: a make and model of device. Its
// manufacturer and model identify it, and its manufacturer and slug are
// unique too, so two device types of one manufacturer can't share a slug.
//
// Its component templates are child collections, which roadmap steps 11 and
// 12 add. Some of its rules span fields and aren't in the spec yet: a weight
// needs a unit (WeightMixin.clean), and a child device type is 0U
// (DeviceType.clean).
var deviceType = &model.Spec{
	Type:      "dcim.devicetype",
	Endpoint:  "/api/dcim/device-types/",
	DependsOn: []string{"dcim.manufacturer"},
	Identity:  []string{"manufacturer", "model"},
	Unique: []model.Unique{
		{Fields: []string{"manufacturer", "model"}, Source: model.Source{File: "dcim/models/devices.py", Symbol: "DeviceType.Meta"}},
		{Fields: []string{"manufacturer", "slug"}, Source: model.Source{File: "dcim/models/devices.py", Symbol: "DeviceType.Meta"}},
	},
	Fields: []model.Field{
		{
			Name: "manufacturer", Kind: model.KindReference, Required: true,
			Ref: &model.Ref{Types: []string{"dcim.manufacturer"}},
		},
		{Name: "model", Kind: model.KindString, Required: true, MaxLength: 100, Trim: true},
		{Name: "slug", Kind: model.KindString, Required: true, MaxLength: 100, Pattern: slugPattern, Trim: true},
		{Name: "part_number", Kind: model.KindString, Blank: true, Default: model.String(""), MaxLength: 50, Trim: true},
		{
			// A height in rack units, in steps of half a unit. The REST API
			// defaults it to 1, but NetBox's import requires it, since the
			// model field isn't blank.
			Name: "u_height", Kind: model.KindDecimal, Digits: 4, Places: 1,
			Min: model.NewDecimal(0, 0), Step: model.NewDecimal(5, 1),
			Default: model.NewDecimal(1, 0), ImportRequired: true,
			Source: model.Source{File: "dcim/models/devices.py", Symbol: "DeviceType.clean"},
		},
		{Name: "exclude_from_utilization", Kind: model.KindBoolean, Default: model.Boolean(false)},
		{
			// The REST API defaults it to true, the model's default, but
			// NetBox's import fills a form, which takes a missing checkbox
			// as false.
			Name: "is_full_depth", Kind: model.KindBoolean,
			Default: model.Boolean(true), ImportDefault: model.Boolean(false),
			Source: model.Source{File: "netbox/views/generic/bulk_views.py", Symbol: "BulkImportView._process_import_records"},
		},
		{
			Name: "subdevice_role", Kind: model.KindChoice,
			Nullable: true, Blank: true, BlankIsNull: true, Default: model.Null{},
			Choices: &model.Choices{Values: choices("DeviceType", "subdevice_role")},
			Source:  model.Source{File: "dcim/choices.py", Symbol: "SubdeviceRoleChoices"},
		},
		{
			// An administrator can change the choices through
			// FIELD_CHOICES["dcim.Device.airflow"], which device types share
			// with devices.
			Name: "airflow", Kind: model.KindChoice,
			Nullable: true, Blank: true, BlankIsNull: true, Default: model.Null{},
			Choices: &model.Choices{Values: choices("DeviceType", "airflow"), Configurable: "dcim.Device.airflow"},
			Source:  model.Source{File: "dcim/choices.py", Symbol: "DeviceAirflowChoices"},
		},
		{
			Name: "cooling_method", Kind: model.KindChoice,
			Nullable: true, Blank: true, BlankIsNull: true, Default: model.Null{},
			Choices: &model.Choices{Values: choices("DeviceType", "cooling_method")},
			Source:  model.Source{File: "dcim/choices.py", Symbol: "CoolingMethodChoices"},
		},
		{
			Name: "weight", Kind: model.KindDecimal, Digits: 8, Places: 2,
			Nullable: true, Default: model.Null{},
			Source: model.Source{File: "netbox/models/mixins.py", Symbol: "WeightMixin"},
		},
		{
			Name: "weight_unit", Kind: model.KindChoice,
			Nullable: true, Blank: true, BlankIsNull: true, Default: model.Null{},
			Choices: &model.Choices{Values: choices("DeviceType", "weight_unit")},
			Source:  model.Source{File: "netbox/choices.py", Symbol: "WeightUnitChoices"},
		},
		{Name: "end_of_life", Kind: model.KindDate, Nullable: true, Default: model.Null{}},
		{Name: "description", Kind: model.KindString, Blank: true, Default: model.String(""), MaxLength: 200, Trim: true},
		{Name: "comments", Kind: model.KindString, Blank: true, Default: model.String(""), Trim: true},
	},
	Ignored: ignored(ignoredBookkeeping, ignoredNetBoxModel, []model.Ignored{
		{Name: "default_platform", Reason: "a reference to a dcim.platform, which has no spec yet; devicetype-library doesn't set it, and TrueUp leaves it as it is"},
		{Name: "front_image", Reason: "an image file, which needs an upload; devicetype-library's front_image is a boolean saying it has one, kept as metadata"},
		{Name: "rear_image", Reason: "an image file, which needs an upload; devicetype-library's rear_image is a boolean saying it has one, kept as metadata"},
		{Name: "device_count", Reason: "read-only: how many devices of the type NetBox has"},
		{Name: "console_port_template_count", Reason: "read-only: a count of the type's templates"},
		{Name: "console_server_port_template_count", Reason: "read-only: a count of the type's templates"},
		{Name: "power_port_template_count", Reason: "read-only: a count of the type's templates"},
		{Name: "power_outlet_template_count", Reason: "read-only: a count of the type's templates"},
		{Name: "cooling_intake_template_count", Reason: "read-only: a count of the type's templates"},
		{Name: "cooling_outflow_template_count", Reason: "read-only: a count of the type's templates"},
		{Name: "interface_template_count", Reason: "read-only: a count of the type's templates"},
		{Name: "front_port_template_count", Reason: "read-only: a count of the type's templates"},
		{Name: "rear_port_template_count", Reason: "read-only: a count of the type's templates"},
		{Name: "device_bay_template_count", Reason: "read-only: a count of the type's templates"},
		{Name: "module_bay_template_count", Reason: "read-only: a count of the type's templates"},
		{Name: "inventory_item_template_count", Reason: "read-only: a count of the type's templates"},
	}),
	Sources: []model.Source{
		{File: "dcim/models/devices.py", Symbol: "DeviceType"},
		{File: "netbox/models/__init__.py", Symbol: "PrimaryModel"},
		{File: "dcim/api/serializers_/devicetypes.py", Symbol: "DeviceTypeSerializer"},
		{File: "netbox/api/serializers/models.py", Symbol: "PrimaryModelSerializer"},
		{File: "netbox/api/fields.py", Symbol: "ChoiceField.to_representation"},
		{File: "dcim/forms/bulk_import.py", Symbol: "DeviceTypeImportForm"},
	},
}
