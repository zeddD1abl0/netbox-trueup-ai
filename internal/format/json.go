package format

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// The JSON form of a definition is what `list --output json` gives for each
// definition, and what TrueUp's database keeps of it, so that the database
// can give definitions back without reading the source's files again
// (docs/adr/0029-keep-a-lookup-table-of-definitions.md). Its fields are part
// of the CLI's public interface (docs/adr/0018-cli-conventions.md): fields
// can be added, but renaming or removing one, or changing its type, is a
// breaking change. Lists and objects are empty rather than null, so that
// scripts needn't check, apart from fields, which is never null for a
// definition a format reads.
type definitionJSON struct {
	// Type is the object type, such as dcim.devicetype.
	Type string `json:"type"`
	// Identity holds the fields that tell the object from others of its
	// type, such as a device type's manufacturer and model.
	Identity map[string]model.Value `json:"identity"`
	// Path is the file that defines the object, or null for one derived
	// from other files, which DerivedFrom lists.
	Path        *string  `json:"path"`
	DerivedFrom []string `json:"derived_from"`
	// Fields are the fields the definition sets, as NetBox's API writes
	// them: a reference as the natural key of what it refers to.
	Fields map[string]model.Value `json:"fields"`
	// Derived lists fields made rather than read, which TrueUp uses only
	// to create the object.
	Derived []string `json:"derived"`
	// Metadata holds what the file says that isn't a NetBox field.
	Metadata map[string]model.Value `json:"metadata"`
	// Unmapped counts the lists of children the file holds that TrueUp is
	// to read but doesn't yet (Definition.Unmapped).
	Unmapped map[string]int `json:"unmapped"`
}

// definitionKeys are the keys of a definition's JSON, which
// UnmarshalDefinition needs all of.
var definitionKeys = []string{"type", "identity", "path", "derived_from", "fields", "derived", "metadata", "unmapped"}

// MarshalDefinition writes d as `list --output json` has always given it:
// <, > and & as they are in its own strings, and escaped, as \u003c, in
// those of a reference or children, which encode themselves.
func MarshalDefinition(d Definition) ([]byte, error) {
	out := definitionJSON{
		Type:        d.Object.Type,
		Identity:    d.Identity,
		DerivedFrom: d.DerivedFrom,
		Fields:      d.Object.Fields,
		Derived:     d.Derived,
		Metadata:    d.Metadata,
		Unmapped:    d.Unmapped,
	}
	if d.Path != "" {
		out.Path = &d.Path
	}
	if out.Identity == nil {
		out.Identity = map[string]model.Value{}
	}
	if out.DerivedFrom == nil {
		out.DerivedFrom = []string{}
	}
	if out.Derived == nil {
		out.Derived = []string{}
	}
	if out.Metadata == nil {
		out.Metadata = map[string]model.Value{}
	}
	if out.Unmapped == nil {
		out.Unmapped = map[string]int{}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, fmt.Errorf("write a %s definition as JSON: %w", d.Object.Type, err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// UnmarshalDefinition reads a definition that MarshalDefinition wrote, its
// fields and identity through the spec of its type among specs
// (model.Spec.UnmarshalFields). Metadata, which no spec describes, is read
// as true or false, text, null, or a number: a whole one as an integer, any
// other as a decimal. Anything else, or a key MarshalDefinition doesn't
// write, or one it does left out, is an error.
func UnmarshalDefinition(specs []*model.Spec, data []byte) (Definition, error) {
	if !json.Valid(data) {
		return Definition{}, errors.New("a definition isn't valid JSON")
	}
	var in map[string]json.RawMessage
	if json.Unmarshal(data, &in) != nil {
		return Definition{}, errors.New("a definition isn't a JSON object")
	}
	for _, key := range slices.Sorted(maps.Keys(in)) {
		if !slices.Contains(definitionKeys, key) {
			return Definition{}, fmt.Errorf("a definition has the key %s, which TrueUp doesn't write", strconv.Quote(key))
		}
	}
	for _, key := range definitionKeys {
		if _, ok := in[key]; !ok {
			return Definition{}, fmt.Errorf("a definition has no %s", key)
		}
	}
	var d Definition
	if err := json.Unmarshal(in["type"], &d.Object.Type); err != nil || d.Object.Type == "" {
		return Definition{}, errors.New("a definition's type isn't text")
	}
	s := model.SpecOf(specs, d.Object.Type)
	if s == nil {
		return Definition{}, fmt.Errorf("a definition is of type %s, which has no spec", strconv.Quote(d.Object.Type))
	}
	fail := func(err error) (Definition, error) {
		return Definition{}, fmt.Errorf("a %s definition: %w", d.Object.Type, err)
	}
	var err error
	if !bytes.Equal(bytes.TrimSpace(in["fields"]), []byte("null")) {
		if d.Object.Fields, err = s.UnmarshalFields(specs, in["fields"]); err != nil {
			return fail(fmt.Errorf("fields: %w", err))
		}
	}
	if d.Identity, err = s.UnmarshalFields(specs, in["identity"]); err != nil {
		return fail(fmt.Errorf("identity: %w", err))
	}
	if !sameNames(slices.Collect(maps.Keys(d.Identity)), s.Identity) {
		return fail(fmt.Errorf("its identity has %v, not %v", slices.Sorted(maps.Keys(d.Identity)), s.Identity))
	}
	var path *string
	if err := json.Unmarshal(in["path"], &path); err != nil || path != nil && *path == "" {
		return fail(errors.New("its path is neither a file's nor null"))
	}
	if path != nil {
		d.Path = *path
	}
	for _, list := range []struct {
		key string
		to  *[]string
	}{{"derived_from", &d.DerivedFrom}, {"derived", &d.Derived}} {
		if err := json.Unmarshal(in[list.key], list.to); err != nil || *list.to == nil {
			return fail(fmt.Errorf("its %s isn't a list of text", list.key))
		}
		if len(*list.to) == 0 {
			*list.to = nil
		}
	}
	if d.Metadata, err = unmarshalMetadata(in["metadata"]); err != nil {
		return fail(err)
	}
	if err := json.Unmarshal(in["unmapped"], &d.Unmapped); err != nil || d.Unmapped == nil {
		return fail(errors.New("its unmapped isn't an object of counts"))
	}
	if len(d.Unmapped) == 0 {
		d.Unmapped = nil
	}
	return d, nil
}

// unmarshalMetadata reads a definition's metadata, whose values no spec
// describes.
func unmarshalMetadata(raw json.RawMessage) (map[string]model.Value, error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(raw, &in); err != nil || in == nil {
		return nil, errors.New("its metadata isn't an object")
	}
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]model.Value, len(in))
	for _, key := range slices.Sorted(maps.Keys(in)) {
		v, err := metadataValue(in[key])
		if err != nil {
			return nil, fmt.Errorf("metadata: %s: %w", key, err)
		}
		out[key] = v
	}
	return out, nil
}

func metadataValue(raw json.RawMessage) (model.Value, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	switch v := v.(type) {
	case nil:
		return model.Null{}, nil
	case bool:
		return model.Boolean(v), nil
	case string:
		return model.String(v), nil
	case json.Number:
		if i, err := strconv.ParseInt(v.String(), 10, 64); err == nil {
			return model.Integer(i), nil
		}
		return model.ParseDecimal(v.String())
	}
	return nil, errors.New("is a list or an object, which TrueUp doesn't keep as metadata")
}

// EncodeDefinition writes d as MarshalDefinition does, and checks that
// UnmarshalDefinition reads it back as the same definition, so that what
// TrueUp keeps of a definition is what the format read, never a lossy copy
// of it.
func EncodeDefinition(specs []*model.Spec, d Definition) ([]byte, error) {
	data, err := MarshalDefinition(d)
	if err != nil {
		return nil, err
	}
	back, err := UnmarshalDefinition(specs, data)
	if err != nil {
		return nil, fmt.Errorf("read back the %s definition %s: %w", d.Object.Type, describeDefinition(d), err)
	}
	if !SameDefinition(d, back) {
		return nil, fmt.Errorf("the %s definition %s doesn't read back as it was written", d.Object.Type, describeDefinition(d))
	}
	return data, nil
}

// SameDefinition reports whether a and b are the same definition: the same
// object, compared as model.Object.Equal compares objects, and the same
// identity, path, derived fields, metadata and unmapped lists. An empty
// list or map is the same as none.
func SameDefinition(a, b Definition) bool {
	return a.Object.Equal(b.Object) &&
		sameValues(a.Identity, b.Identity) &&
		a.Path == b.Path &&
		slices.Equal(a.DerivedFrom, b.DerivedFrom) &&
		slices.Equal(a.Derived, b.Derived) &&
		sameValues(a.Metadata, b.Metadata) &&
		maps.Equal(a.Unmapped, b.Unmapped)
}

func sameValues(a, b map[string]model.Value) bool {
	return maps.EqualFunc(a, b, model.Equal)
}

func sameNames(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

// describeDefinition names a definition for an error: by its file, or for
// a derived one, by its identity.
func describeDefinition(d Definition) string {
	if d.Path != "" {
		return strconv.Quote(d.Path)
	}
	id, err := json.Marshal(d.Identity)
	if err != nil {
		return "derived from other files"
	}
	return string(id)
}
