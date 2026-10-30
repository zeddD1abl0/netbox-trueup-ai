package model

// Spec describes one NetBox object type: where NetBox's REST API keeps it,
// what it depends on, how its objects are told apart, its fields and NetBox's
// rules for them, its child collections, and the NetBox source it follows
// (docs/architecture.md, "Object model and specs"). Specs are declared in Go,
// one package per NetBox app, such as internal/model/dcim. Check tests that
// a set of specs is consistent, and internal/model/spectest checks them
// against NetBox's OpenAPI schema.
type Spec struct {
	// Type is the object type as NetBox names it, app label and model, such
	// as dcim.devicetype.
	Type string
	// Endpoint is the REST API path that lists objects of the type, such as
	// /api/dcim/device-types/. It is empty for a type with no endpoint of its
	// own, whose objects a parent carries (Collection.Nested).
	Endpoint string
	// DependsOn lists the object types that must exist before an object of
	// this type can be created, such as a device type's manufacturer: every
	// type a reference field refers to, apart from references within a
	// parent (Ref.Within).
	DependsOn []string
	// Identity lists the fields that make two definitions, or a definition
	// and a NetBox object, the same object: a device type's manufacturer and
	// model. They are the fields of one of the Unique rules. A type whose
	// objects are only ever children has none, since the key of the
	// collection that holds them tells them apart.
	Identity []string
	// Unique lists the uniqueness rules NetBox enforces, from the model's
	// unique fields and Meta.constraints.
	Unique []Unique
	// Fields lists the fields the spec describes, in the order NetBox's API
	// gives them.
	Fields []Field
	// Children lists the type's child collections.
	Children []Collection
	// Ignored lists the fields of NetBox's API that the spec leaves out on
	// purpose, each with the reason, so that a field NetBox adds in a new
	// release is noticed rather than missed.
	Ignored []Ignored
	// Sources cite the NetBox source the spec follows.
	Sources []Source
}

// Unique is a uniqueness rule NetBox enforces: no two objects of a type have
// the same values in all of Fields.
type Unique struct {
	Fields []string
	// Gap says where NetBox doesn't enforce the rule, or is empty when it
	// always does. PostgreSQL treats nulls as distinct, for example, so a rule
	// over a nullable field doesn't hold where that field is null.
	Gap    string
	Source Source
}

// Field describes one field of an object type: its kind, how NetBox's API
// takes it, NetBox's rules for its values, and how its values compare.
type Field struct {
	// Name is the field's name in NetBox's REST API.
	Name string
	Kind Kind

	// ReadOnly fields are set by NetBox and never written.
	ReadOnly bool
	// CreateOnly fields are written when an object is created and can't be
	// changed afterwards, such as the device type a component template
	// belongs to.
	CreateOnly bool
	// Required fields must be given when an object is created through the
	// REST API.
	Required bool
	// Nullable fields can be null.
	Nullable bool
	// Blank fields take the empty string, or for a choice field the blank
	// choice.
	Blank bool

	// Default is what NetBox gives the field when an object is created
	// through the REST API without it. Every writable field that isn't
	// required has one.
	Default Value
	// ImportDefault is what NetBox's own CSV and YAML import gives the field
	// when a record leaves it out, where that differs from Default. The
	// import fills a form, and a form takes a missing checkbox as false.
	ImportDefault Value
	// ImportRequired says NetBox's import refuses a record that leaves the
	// field out, though the REST API gives it a default.
	ImportRequired bool

	// MaxLength is the most characters a string has; zero means no limit.
	MaxLength int
	// Pattern is a regular expression, in Go's syntax and without anchors,
	// that a whole string matches, such as a slug's [-a-zA-Z0-9_]+.
	Pattern string
	// Digits and Places are the most digits a decimal has in all and after
	// the decimal point: its DecimalField's max_digits and decimal_places.
	Digits, Places int
	// Min and Max bound an integer or decimal, inclusively; nil means it is
	// bounded only by Digits. They are Integers or Decimals, as the field is.
	Min, Max Value
	// Step, for a decimal, is what it must be a whole multiple of, such as a
	// device type's height in units of 0.5; nil means any.
	Step Value
	// TighterBounds cites where NetBox enforces Min or Max beyond the bounds
	// its API states, such as a weight's minimum of 0, which NetBox's model
	// enforces when it saves one. A bound tighter than the API's needs it.
	TighterBounds Source

	// Choices are a choice field's values.
	Choices *Choices
	// Ref describes what a reference field refers to, and how NetBox's API
	// carries it.
	Ref *Ref
	// Elem describes a set's elements. Its Name is unused.
	Elem *Field

	// Trim says NetBox removes white space from both ends of the value when
	// it is written, as Django REST Framework's and Django's CharFields do,
	// so values compare trimmed.
	Trim bool
	// BlankIsNull says NetBox's API reads a blank value back as null, as
	// NetBox's ChoiceField does, so the two compare equal.
	BlankIsNull bool

	// Source cites the NetBox source for the field's rules where the spec's
	// Sources don't cover them, such as a model's clean method.
	Source Source
}

// Choices are the values a choice field takes.
type Choices struct {
	// Values are the choices NetBox ships with, in its order, without the
	// blank choice. They are generated from NetBox's OpenAPI schema.
	Values []string
	// Configurable is the key under which a NetBox administrator can
	// replace or extend Values through the FIELD_CHOICES setting, such as
	// dcim.Device.airflow, or empty if they can't. Values are then only
	// NetBox's defaults: the NetBox TrueUp talks to may take others.
	Configurable string
}

// Ref describes a reference field.
type Ref struct {
	// Types lists the object types the field can refer to: one, or several
	// for a generic reference such as an inventory item template's
	// component.
	Types []string
	// Within is set for a reference to another child of the same parent,
	// such as a power outlet template's power port: it names the parent's
	// object type, and the reference names the object by its key in the
	// parent's collection. It is empty for a reference to an object that
	// stands on its own, which names the object by its identity.
	Within string
	// Shape is how NetBox's API carries the reference.
	Shape Shape
	// TypeField and IDField are the API fields that carry a generic
	// reference (ShapeTypeAndID): the object type, such as
	// dcim.interfacetemplate, and the ID.
	TypeField, IDField string
}

// Shape is how NetBox's API carries a reference.
type Shape uint8

const (
	// ShapeNested reads as a brief object, with the ID and the natural key,
	// and writes as the ID. Most references are like this.
	ShapeNested Shape = iota
	// ShapeID reads and writes as the ID alone, such as an inventory item
	// template's parent.
	ShapeID
	// ShapeTypeAndID reads and writes as two fields, Ref.TypeField and
	// Ref.IDField, such as an inventory item template's component_type and
	// component_id.
	ShapeTypeAndID
)

// Collection describes a child collection: objects of another type that
// belong to one object of this type, such as a device type's interface
// templates.
type Collection struct {
	// Name is the collection's field in its parent Object. For a nested
	// collection it is also the parent's field in NetBox's API that carries
	// the children.
	Name string
	// Type is the children's object type.
	Type string
	// Parent is the children's field that refers to their parent. A nested
	// collection has none, since the parent carries its children.
	Parent string
	// Shared lists fields children have the same value in as their parent,
	// such as the device type of an inventory item template nested in
	// another.
	Shared []string
	// Key lists the children's fields that tell one parent's children apart.
	Key []string
	// Gap says where NetBox doesn't keep Key unique among one parent's
	// children, or is empty when it does. Children whose keys are the same
	// can't be told apart.
	Gap string
	// Nested says the children have no endpoint of their own. NetBox's API
	// carries them in the parent's field Name, and replaces the whole
	// collection whenever that field is written, leaving it alone when the
	// field is left out.
	Nested bool
	Source Source
}

// Ignored is a field of NetBox's API that a spec leaves out on purpose.
type Ignored struct {
	Name   string
	Reason string
}

// Source cites NetBox's source at the release TrueUp targets
// (docs/adr/0003-initial-netbox-target.md), which `make reference` fetches.
type Source struct {
	// File is a path under the netbox directory of NetBox's repository,
	// such as dcim/models/devices.py.
	File string
	// Symbol is a class, function or method in File, such as DeviceType or
	// DeviceType.clean.
	Symbol string
}

// Field returns the field called name, or nil if the spec has none.
func (s *Spec) Field(name string) *Field {
	for i := range s.Fields {
		if s.Fields[i].Name == name {
			return &s.Fields[i]
		}
	}
	return nil
}

// Collection returns the child collection called name, or nil if the spec
// has none.
func (s *Spec) Collection(name string) *Collection {
	for i := range s.Children {
		if s.Children[i].Name == name {
			return &s.Children[i]
		}
	}
	return nil
}
