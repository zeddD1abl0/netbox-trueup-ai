package model

import (
	"encoding/json"
	"errors"
)

// The values whose Go types don't already encode as NetBox's API writes
// them. String, Integer, Boolean and Choice encode as JSON strings, numbers
// and booleans, and a Set as an array of its elements.

// MarshalJSON writes d as a JSON number, exactly, such as 1.5: never through
// a float.
func (d Decimal) MarshalJSON() ([]byte, error) {
	return []byte(d.String()), nil
}

// MarshalJSON writes d as a JSON string, YYYY-MM-DD, as NetBox's API does.
func (d Date) MarshalJSON() ([]byte, error) {
	if d == (Date{}) {
		return nil, errors.New("the zero Date isn't a date")
	}
	return json.Marshal(d.String())
}

// MarshalJSON writes null.
func (Null) MarshalJSON() ([]byte, error) {
	return []byte("null"), nil
}

// MarshalJSON writes children as a JSON array of their fields, such as
// [{"name":"eth0","type":"1000base-t"}], in their order. The collection that
// holds them says their type.
func (c Children) MarshalJSON() ([]byte, error) {
	out := make([]map[string]Value, len(c))
	for i, o := range c {
		out[i] = o.Fields
		if out[i] == nil {
			out[i] = map[string]Value{}
		}
	}
	return json.Marshal(out)
}

// MarshalJSON writes r as a JSON object of its natural key, such as
// {"name":"Dell"} for a manufacturer, with its NetBox ID as "id" once it is
// resolved. The field that holds a reference says what type it refers to.
func (r Reference) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(r.Key)+1)
	for name, v := range r.Key {
		out[name] = v
	}
	if r.ID != 0 {
		out["id"] = r.ID
	}
	return json.Marshal(out)
}
