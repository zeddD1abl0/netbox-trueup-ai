package dcim

import "github.com/zeddD1abl0/netbox-trueup-ai/internal/model"

// manufacturer is dcim.manufacturer: an OrganizationalModel, so its name and
// slug are each unique on their own, and the name identifies it, as NetBox's
// import matches a device type's manufacturer by name.
var manufacturer = &model.Spec{
	Type:     "dcim.manufacturer",
	Endpoint: "/api/dcim/manufacturers/",
	Identity: []string{"name"},
	Unique: []model.Unique{
		{Fields: []string{"name"}, Source: model.Source{File: "netbox/models/__init__.py", Symbol: "OrganizationalModel"}},
		{Fields: []string{"slug"}, Source: model.Source{File: "netbox/models/__init__.py", Symbol: "OrganizationalModel"}},
	},
	Fields: []model.Field{
		{Name: "name", Kind: model.KindString, Required: true, MaxLength: 100, Trim: true},
		{Name: "slug", Kind: model.KindString, Required: true, MaxLength: 100, Pattern: slugPattern, Trim: true},
		{Name: "description", Kind: model.KindString, Blank: true, Default: model.String(""), MaxLength: 200, Trim: true},
		{Name: "comments", Kind: model.KindString, Blank: true, Default: model.String(""), Trim: true},
	},
	Ignored: ignored(ignoredBookkeeping, ignoredNetBoxModel, []model.Ignored{
		{Name: "devicetype_count", Reason: "read-only: how many device types NetBox has from the manufacturer"},
		{Name: "moduletype_count", Reason: "read-only: how many module types NetBox has from the manufacturer"},
		{Name: "inventoryitem_count", Reason: "read-only: how many inventory items NetBox has from the manufacturer"},
		{Name: "platform_count", Reason: "read-only: how many platforms NetBox has from the manufacturer"},
	}),
	Sources: []model.Source{
		{File: "dcim/models/devices.py", Symbol: "Manufacturer"},
		{File: "netbox/models/__init__.py", Symbol: "OrganizationalModel"},
		{File: "dcim/api/serializers_/manufacturers.py", Symbol: "ManufacturerSerializer"},
		{File: "netbox/api/serializers/models.py", Symbol: "OrganizationalModelSerializer"},
		{File: "dcim/forms/bulk_import.py", Symbol: "ManufacturerImportForm"},
	},
}
