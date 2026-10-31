package openapi

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ChoiceSet is the values of one choice field, as a response component of
// the schema gives them.
type ChoiceSet struct {
	// Key is the component and field, such as DeviceType.airflow.
	Key string
	// Values are the field's choices in the schema's order, without the
	// blank choice.
	Values []string
}

// Choices lists every choice field of the schema's response components, in
// order of key. NetBox's API gives a choice field's value as {value, label},
// and its schema lists the values as the enum of value (ChoiceFieldFix in
// netbox/core/api/schema.py), with the blank string and null where the field
// takes them, which Choices leaves out. Brief and nested components, pages
// and request components repeat what the response components say, so
// Choices skips them.
func (d *Document) Choices() ([]ChoiceSet, error) {
	var sets []ChoiceSet
	for _, name := range slices.Sorted(maps.Keys(d.Components.Schemas)) {
		if !isResponse(name) {
			continue
		}
		c := d.Components.Schemas[name]
		for _, field := range slices.Sorted(maps.Keys(c.Properties)) {
			p, err := d.Unwrap(c.Properties[field])
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", name, field, err)
			}
			value, ok := p.Properties["value"]
			if p.Type != "object" || !ok || len(value.Enum) == 0 {
				continue
			}
			values, _, _, err := EnumValues(value)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", name, field, err)
			}
			sets = append(sets, ChoiceSet{Key: name + "." + field, Values: values})
		}
	}
	return sets, nil
}

// isResponse reports whether a component describes objects as NetBox's API
// gives them in full.
func isResponse(name string) bool {
	for _, prefix := range []string{"Brief", "Nested", "Paginated", "Patched", "Writable"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return !strings.HasSuffix(name, "Request")
}
