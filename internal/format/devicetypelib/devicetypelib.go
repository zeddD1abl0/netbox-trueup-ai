// Package devicetypelib reads devicetype-library, the library of device
// types that NetBox's community keeps
// (https://github.com/netbox-community/devicetype-library), into
// definitions (docs/architecture.md, "Formats and validation"). It reads
// device types, and derives a manufacturer from each name they give.
// Component templates follow in roadmap steps 11 and 12, and module and rack
// types in milestone 8.
//
// Each file is read as NetBox's own YAML import reads it, validated against
// the library's JSON schemas from the same snapshot, mapped to the device
// type's spec, and validated against the spec
// (docs/adr/0024-read-source-files-as-pyyaml-does.md). A file that fails is
// reported and skipped.
package devicetypelib

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/dcim"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
)

const (
	// deviceTypesDir holds the device types, a directory for each
	// manufacturer, as the library's tests find them
	// (tests/definitions_test.py: device-types/*/*, named .yaml or .yml).
	deviceTypesDir = "device-types"
	// schemaRoot is the schema the library's tests validate device types
	// against (tests/test_configuration.py, SCHEMAS).
	schemaRoot = "schema/devicetype.json"
)

// The keys of a device type file that TrueUp reads, beyond the device
// type's fields.
var (
	// fieldKeys are the device type's fields, each as the field of the
	// same name: every field of NetBox's DeviceTypeImportForm
	// (dcim/forms/bulk_import.py) that the library's schema allows. The
	// manufacturer is read as a reference by name.
	fieldKeys = []string{
		"manufacturer", "model", "slug", "part_number", "u_height", "is_full_depth",
		"airflow", "weight", "weight_unit", "subdevice_role", "description", "comments",
	}
	// metadataKeys aren't NetBox fields: is_powered is a hint for the
	// library's own tests, and front_image and rear_image say that the
	// library has an elevation image. NetBox's import ignores them too.
	metadataKeys = []string{"is_powered", "front_image", "rear_image"}
	// componentKeys are the lists of component templates, which roadmap
	// steps 11 and 12 read.
	componentKeys = []string{
		"console-ports", "console-server-ports", "power-ports", "power-outlets", "interfaces",
		"front-ports", "rear-ports", "port-mappings", "module-bays", "device-bays", "inventory-items",
	}
)

// Format is devicetype-library's format.
type Format struct{}

// Name returns devicetype-library.
func (Format) Name() string { return config.FormatDeviceTypeLibrary }

// Needs returns the library's JSON schemas, which validate its files.
func (Format) Needs() []string { return []string{"schema/*.json"} }

// Reads returns the files the format reads: the device types, and anything
// else below their directory, which it reports, and the JSON schemas.
func (Format) Reads() []string { return []string{deviceTypesDir + "/**", "schema/*.json"} }

// Read reads the snapshot's device types, and derives their manufacturers.
// It fails, reading nothing, when the snapshot lacks the library's schemas
// or they don't compile, or a file can't be read from the snapshot.
func (Format) Read(ctx context.Context, snap *source.Snapshot) (*format.Result, error) {
	schema, err := compileSchema(ctx, snap)
	if err != nil {
		return nil, err
	}
	r := newReader(schema)
	for _, f := range snap.Files {
		if !strings.HasPrefix(f.Path, deviceTypesDir+"/") {
			continue
		}
		if !isDeviceTypePath(f.Path) {
			r.add(format.Problem{
				Severity: format.Warning, Path: f.Path,
				Reason: "isn't read: devicetype-library keeps each device type in device-types/<manufacturer>/, in a file named .yaml or .yml",
			})
			continue
		}
		data, err := snap.ReadFile(ctx, f.Path)
		if err != nil {
			return nil, err
		}
		r.file(f.Path, data)
	}
	for _, s := range snap.Skipped {
		if strings.HasPrefix(s.Path, deviceTypesDir+"/") {
			r.files++
			r.invalid++
			r.add(format.Problem{Severity: format.Error, Path: s.Path, Reason: skipReasons[s.Reason]})
		}
	}
	return r.result(), nil
}

// skipReasons say why the source layer left out a file, for each reason.
var skipReasons = map[source.Reason]string{
	source.TooLarge:   "wasn't read: it is larger than the source's max_file_size",
	source.Symlink:    "wasn't read: it is a symbolic link, which TrueUp never follows",
	source.Submodule:  "wasn't read: it is a Git submodule, which TrueUp never fetches",
	source.UnsafePath: "wasn't read: its name is unsafe, or its path too deep or too long",
	source.NotAFile:   "wasn't read: it isn't a file",
}

// isDeviceTypePath reports whether a path is where the library keeps a
// device type: device-types/<manufacturer>/<name>.yaml or .yml, not hidden,
// as the library's tests glob them.
func isDeviceTypePath(p string) bool {
	parts := strings.Split(p, "/")
	if len(parts) != 3 || parts[0] != deviceTypesDir || strings.HasPrefix(parts[2], ".") {
		return false
	}
	ext := path.Ext(parts[2])
	return (ext == ".yaml" || ext == ".yml") && len(parts[2]) > len(ext)
}

// compileSchema compiles the library's device type schema from the
// snapshot's schema/*.json.
func compileSchema(ctx context.Context, snap *source.Snapshot) (*format.Schema, error) {
	var files []format.SchemaFile
	for _, f := range snap.Files {
		if dir, name := path.Split(f.Path); dir != "schema/" || path.Ext(name) != ".json" {
			continue
		}
		data, err := snap.ReadFile(ctx, f.Path)
		if err != nil {
			return nil, err
		}
		files = append(files, format.SchemaFile{Path: f.Path, Data: data})
	}
	if !slices.ContainsFunc(files, func(f format.SchemaFile) bool { return f.Path == schemaRoot }) {
		for _, s := range snap.Skipped {
			if s.Path == schemaRoot {
				return nil, fmt.Errorf("%s, devicetype-library's schema for device types, %s", schemaRoot, strings.TrimPrefix(skipReasons[s.Reason], "wasn't read: it "))
			}
		}
		return nil, fmt.Errorf("the source has no %s, devicetype-library's schema for device types, which TrueUp validates its files against: "+
			"check that the source is devicetype-library, and that its exclude patterns leave schema/ in", schemaRoot)
	}
	schema, err := format.CompileSchema(files, schemaRoot)
	if err != nil {
		// A schema it refers to may be one the snapshot left out.
		var skipped []string
		for _, s := range snap.Skipped {
			if dir, name := path.Split(s.Path); dir == "schema/" && path.Ext(name) == ".json" && s.Reason == source.TooLarge {
				skipped = append(skipped, s.Path)
			}
		}
		switch len(skipped) {
		case 0:
		case 1:
			err = fmt.Errorf("%w (%s wasn't read: it is larger than the source's max_file_size)", err, skipped[0])
		default:
			err = fmt.Errorf("%w (%s and %s weren't read: they are larger than the source's max_file_size)", err, strings.Join(skipped[:len(skipped)-1], ", "), skipped[len(skipped)-1])
		}
		return nil, fmt.Errorf("devicetype-library's schemas: %w", err)
	}
	return schema, nil
}

// reader reads the device types of one snapshot.
type reader struct {
	schema                   *format.Schema
	deviceType, manufacturer *model.Spec
	deviceTypes              []format.Definition
	problems                 []format.Problem
	files, invalid           int
}

func newReader(schema *format.Schema) *reader {
	r := &reader{schema: schema}
	for _, s := range dcim.Specs() {
		switch s.Type {
		case "dcim.devicetype":
			r.deviceType = s
		case "dcim.manufacturer":
			r.manufacturer = s
		}
	}
	return r
}

func (r *reader) add(p ...format.Problem) {
	r.problems = append(r.problems, p...)
}

// file reads one device type file, keeping its definition if it has no
// errors.
func (r *reader) file(p string, data []byte) {
	r.files++
	def, problems := r.read(p, data)
	r.add(problems...)
	if slices.ContainsFunc(problems, func(p format.Problem) bool { return p.Severity == format.Error }) {
		r.invalid++
		return
	}
	r.deviceTypes = append(r.deviceTypes, def)
}

// read reads one device type file: parses it, validates it against the
// schema, and maps it to the device type's spec.
func (r *reader) read(p string, data []byte) (format.Definition, []format.Problem) {
	root, err := format.ParseYAML(data, format.DefinitionLimits)
	if err != nil {
		var perr *format.ParseError
		if errors.As(err, &perr) {
			return format.Definition{}, []format.Problem{perr.Problem(p)}
		}
		return format.Definition{}, []format.Problem{{Severity: format.Error, Path: p, Reason: err.Error()}}
	}
	if problems := r.schema.Validate(p, root); len(problems) > 0 {
		return format.Definition{}, problems
	}
	return r.deviceTypeOf(p, root)
}

// deviceTypeOf maps a file's root to a device type definition.
func (r *reader) deviceTypeOf(p string, root *format.Node) (format.Definition, []format.Problem) {
	def := format.Definition{
		Object: model.Object{Type: r.deviceType.Type, Fields: map[string]model.Value{}},
		Path:   p,
	}
	var problems []format.Problem
	at := func(severity format.Severity, n *format.Node, field, reason string) {
		problems = append(problems, format.Problem{Severity: severity, Path: p, Line: n.Line, Column: n.Column, Field: field, Reason: reason})
	}
	for _, pair := range root.Pairs {
		key, n := pair.Key.Value, pair.Value
		switch {
		case key == "manufacturer":
			ref, reason := r.manufacturerOf(n)
			if reason != "" {
				at(format.Error, n, key, reason)
				continue
			}
			def.Object.Fields[key] = ref
		case slices.Contains(fieldKeys, key):
			f := r.deviceType.Field(key)
			v, err := format.Value(f, n)
			if err != nil {
				at(format.Error, n, key, err.Error())
				continue
			}
			v = f.Normalize(v)
			if err := f.Validate(v); err != nil {
				severity := format.Error
				if errors.Is(err, model.ErrConfigurableChoice) {
					// The NetBox TrueUp talks to may take it.
					severity = format.Warning
				}
				at(severity, n, key, reason(err))
				if severity == format.Error {
					continue
				}
			}
			def.Object.Fields[key] = v
		case slices.Contains(metadataKeys, key):
			if n.Kind != format.Scalar || n.Tag != format.TagBool {
				at(format.Error, n, key, "isn't true or false, which devicetype-library's "+key+" is")
				continue
			}
			if def.Metadata == nil {
				def.Metadata = map[string]model.Value{}
			}
			def.Metadata[key] = model.Boolean(n.Value == "true")
		case slices.Contains(componentKeys, key):
			if n.Kind != format.Sequence {
				at(format.Error, n, key, "isn't a list, which devicetype-library's "+key+" is")
				continue
			}
			if def.Unmapped == nil {
				def.Unmapped = map[string]int{}
			}
			def.Unmapped[key] = len(n.Items)
		default:
			at(format.Warning, pair.Key, format.JoinField("", key), "isn't a key TrueUp reads from devicetype-library yet, so it is ignored")
		}
	}
	// The schema requires these, but it comes from the source, so check
	// that the file gives what NetBox needs to create a device type, with
	// its own import.
	for _, f := range r.deviceType.Fields {
		if _, ok := def.Object.Fields[f.Name]; !ok && (f.Required || f.ImportRequired) && !slices.ContainsFunc(problems, func(p format.Problem) bool { return p.Field == f.Name }) {
			at(format.Error, root, "", "has no "+f.Name+", which NetBox needs to create a device type")
		}
	}
	def.Identity = map[string]model.Value{}
	for _, name := range r.deviceType.Identity {
		def.Identity[name] = def.Object.Fields[name]
	}
	return def, problems
}

// manufacturerOf reads a device type's manufacturer as a reference by name,
// as NetBox's import finds it: CSVModelChoiceField with to_field_name name,
// in DeviceTypeImportForm, looks for a manufacturer of exactly that name,
// without trimming it. It returns why it can't, if it can't.
func (r *reader) manufacturerOf(n *format.Node) (model.Reference, string) {
	name := r.manufacturer.Field("name")
	if n.Kind != format.Scalar || n.Tag != format.TagStr {
		return model.Reference{}, "is " + format.Describe(n) + ", but a manufacturer is given by its name, as text"
	}
	v := model.String(n.Value)
	if err := name.Validate(v); err != nil {
		return model.Reference{}, reason(err)
	}
	if name.Normalize(v) != v {
		return model.Reference{}, format.Quote(n.Value) + " has white space at an end: NetBox trims a manufacturer's name, but its import looks for the name as the file gives it, and wouldn't find it"
	}
	ref := model.Reference{Type: r.manufacturer.Type, Key: map[string]model.Value{"name": v}}
	if err := r.deviceType.Field("manufacturer").Validate(ref); err != nil {
		return model.Reference{}, reason(err)
	}
	return ref, ""
}

// reason is why a value fails Field.Validate, without the field's name in
// front, which a problem gives as its field.
func reason(err error) string {
	if inner := errors.Unwrap(err); inner != nil {
		return inner.Error()
	}
	return err.Error()
}

// result derives the manufacturers, reports what NetBox couldn't hold, and
// returns the definitions and problems in order.
func (r *reader) result() *format.Result {
	manufacturers := r.deriveManufacturers()
	r.duplicates()
	slices.SortStableFunc(r.problems, func(a, b format.Problem) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
	})
	slices.SortFunc(r.deviceTypes, func(a, b format.Definition) int { return strings.Compare(a.Path, b.Path) })
	return &format.Result{
		Definitions: slices.Concat(manufacturers, r.deviceTypes),
		Problems:    r.problems,
		Files:       r.files,
		Invalid:     r.invalid,
	}
}

// deriveManufacturers returns a manufacturer for each name the valid device
// types give, sorted by name, with a slug made from the name as NetBox's UI
// makes one, and reports names that would get the same slug, which NetBox
// keeps unique.
func (r *reader) deriveManufacturers() []format.Definition {
	paths := map[string][]string{}
	for _, def := range r.deviceTypes {
		name := manufacturerName(def)
		paths[name] = append(paths[name], def.Path)
	}
	slug := r.manufacturer.Field("slug")
	var out []format.Definition
	bySlug := map[string][]string{}
	for _, name := range slices.Sorted(maps.Keys(paths)) {
		def := format.Definition{
			Object:      model.Object{Type: r.manufacturer.Type, Fields: map[string]model.Value{"name": model.String(name)}},
			Identity:    map[string]model.Value{"name": model.String(name)},
			DerivedFrom: slices.Sorted(slices.Values(paths[name])),
		}
		v := model.String(model.Slugify(name, model.SlugLength))
		if err := slug.Validate(v); err != nil {
			r.add(format.Problem{
				Severity: format.Warning,
				Reason:   fmt.Sprintf("NetBox's UI can't make a slug from the manufacturer name %s (%s), so TrueUp can't create the manufacturer: it can only match one NetBox has", format.Quote(name), reason(err)),
			})
		} else {
			def.Object.Fields["slug"] = v
			def.Derived = []string{"slug"}
			bySlug[string(v)] = append(bySlug[string(v)], name)
		}
		out = append(out, def)
	}
	for _, s := range slices.Sorted(maps.Keys(bySlug)) {
		names := bySlug[s]
		if len(names) < 2 {
			continue
		}
		quoted := make([]string, len(names))
		for i, name := range names {
			quoted[i] = format.Quote(name)
		}
		both, others := "both", "one of them"
		if len(names) > 2 {
			both, others = "all", "all but one of them"
		}
		r.add(format.Problem{
			Severity: format.Warning,
			Reason: fmt.Sprintf("manufacturers %s and %s would %s get the slug %s, which NetBox gives to one manufacturer only: create %s in NetBox with another slug, or spell them alike in the source",
				strings.Join(quoted[:len(quoted)-1], ", "), quoted[len(quoted)-1], both, format.Quote(s), others),
		})
	}
	return out
}

func manufacturerName(def format.Definition) string {
	ref, _ := def.Object.Fields["manufacturer"].(model.Reference)
	name, _ := ref.Key["name"].(model.String)
	return string(name)
}

// duplicates reports device types that NetBox couldn't hold together: two
// files that give the same values for one of the device type's uniqueness
// rules, such as the same manufacturer and model. Both are kept; grouping
// definitions across sources (roadmap step 13) decides what they mean.
func (r *reader) duplicates() {
	for _, u := range r.deviceType.Unique {
		if u.Gap != "" {
			continue
		}
		first := map[string]string{}
		for _, def := range r.deviceTypes {
			var key strings.Builder
			for _, name := range u.Fields {
				fmt.Fprintf(&key, "%s=%s;", name, def.Object.Fields[name])
			}
			other, seen := first[key.String()]
			if !seen {
				first[key.String()] = def.Path
				continue
			}
			r.add(format.Problem{
				Severity: format.Warning, Path: def.Path, Field: u.Fields[len(u.Fields)-1],
				Reason: fmt.Sprintf("%s has the same %s as this one, which NetBox keeps unique", other, strings.Join(u.Fields, " and ")),
			})
		}
	}
}
