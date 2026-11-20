package compare

import (
	"encoding/json"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// The JSON form of a result is part of the CLI's public interface, in
// `compare --output json`, and is what the database records
// (docs/adr/0018-cli-conventions.md): fields can be added, but renaming or
// removing one, or changing its type, is a breaking change. Lists are
// empty rather than null, so that scripts needn't check.

type resultJSON struct {
	Type     string                 `json:"type"`
	Identity map[string]model.Value `json:"identity"`
	Outcome  Outcome                `json:"outcome"`
	InStep   bool                   `json:"in_step"`
	// NetBoxID is null when the identity matches no NetBox object.
	NetBoxID    *int64           `json:"netbox_id"`
	Differences int              `json:"differences"`
	Collisions  []collisionJSON  `json:"collisions"`
	Unresolved  []unresolvedJSON `json:"unresolved"`
	Columns     []Column         `json:"columns"`
	Rows        []rowJSON        `json:"rows"`
}

type collisionJSON struct {
	Rule []string `json:"rule"`
	// NetBoxID is null for a nested child, such as a front port's mapping,
	// which NetBox gives no ID.
	NetBoxID *int64                 `json:"netbox_id"`
	Identity map[string]model.Value `json:"identity"`
	// Path is where a child is, or empty for the object itself.
	Path Path `json:"path"`
}

type unresolvedJSON struct {
	Field string                 `json:"field"`
	Type  string                 `json:"type"`
	Key   map[string]model.Value `json:"key"`
	// NetBoxIDs are the NetBox objects that all have the key, when there
	// are several, which NetBox's uniqueness rules forbid; empty otherwise.
	NetBoxIDs []int64 `json:"netbox_ids"`
	// Outcome is null when no one definition of the object was compared.
	Outcome *Outcome `json:"outcome"`
}

type rowJSON struct {
	Path    Path       `json:"path"`
	Cells   []cellJSON `json:"cells"`
	Differs bool       `json:"differs"`
	Managed bool       `json:"managed"`
	Derived bool       `json:"derived"`
	// NetBoxID is, on a child's row, NetBox's child's ID, and null on a
	// field's row, or where NetBox hasn't got the child, or has two.
	NetBoxID *int64 `json:"netbox_id"`
}

// cellJSON is {"value": ...} for a version that sets the field, null
// included, and {} for one that doesn't.
type cellJSON struct {
	Value model.Value `json:"value,omitempty"`
}

// MarshalJSON writes the result as the database records it, and as
// `compare --output json` gives an object with one definition, but for what
// only objects have.
func (r Result) MarshalJSON() ([]byte, error) {
	out := resultJSON{
		Type:        r.Type,
		Identity:    r.Identity,
		Outcome:     r.Outcome,
		InStep:      r.InStep(),
		Differences: r.Differences,
		Collisions:  collisionsJSON(r.Collisions),
		Unresolved:  unresolvedsJSON(r.Unresolved),
		Columns:     r.Columns,
		Rows:        rowsJSON(r.Rows),
	}
	if out.Identity == nil {
		out.Identity = map[string]model.Value{}
	}
	if r.NetBoxID != 0 {
		out.NetBoxID = &r.NetBoxID
	}
	if out.Columns == nil {
		out.Columns = []Column{}
	}
	return json.Marshal(out)
}

func collisionsJSON(collisions []Collision) []collisionJSON {
	out := make([]collisionJSON, 0, len(collisions))
	for _, c := range collisions {
		j := collisionJSON{Rule: c.Rule, Identity: c.Identity, Path: c.Path}
		if c.ID != 0 {
			j.NetBoxID = &c.ID
		}
		if j.Identity == nil {
			j.Identity = map[string]model.Value{}
		}
		out = append(out, j)
	}
	return out
}

func unresolvedsJSON(unresolved []Unresolved) []unresolvedJSON {
	out := make([]unresolvedJSON, 0, len(unresolved))
	for _, u := range unresolved {
		j := unresolvedJSON{Field: u.Field, Type: u.Ref.Type, Key: u.Ref.Key, NetBoxIDs: u.Candidates}
		if j.NetBoxIDs == nil {
			j.NetBoxIDs = []int64{}
		}
		if u.Outcome != "" {
			j.Outcome = &u.Outcome
		}
		out = append(out, j)
	}
	return out
}

func rowsJSON(rows []Row) []rowJSON {
	out := make([]rowJSON, 0, len(rows))
	for _, row := range rows {
		j := rowJSON{Path: row.Path, Differs: row.Differs, Managed: row.Managed, Derived: row.Derived}
		if row.NetBoxID != 0 {
			j.NetBoxID = &row.NetBoxID
		}
		for _, v := range row.Cells {
			j.Cells = append(j.Cells, cellJSON{Value: v})
		}
		out = append(out, j)
	}
	return out
}

// objectJSON is an object as `compare --output json` gives it: a result's
// fields, with the object's meaning, then what only objects have, and a
// column for each definition that holds its own comparison with NetBox.
type objectJSON struct {
	Type     string                 `json:"type"`
	Identity map[string]model.Value `json:"identity"`
	Outcome  Outcome                `json:"outcome"`
	InStep   bool                   `json:"in_step"`
	// NetBoxID is null when the identity matches no NetBox object.
	NetBoxID    *int64           `json:"netbox_id"`
	Differences int              `json:"differences"`
	Collisions  []collisionJSON  `json:"collisions"`
	Unresolved  []unresolvedJSON `json:"unresolved"`
	// DuplicatedIn names the sources that define the object more than once.
	DuplicatedIn []string    `json:"duplicated_in"`
	Clashes      []clashJSON `json:"clashes"`
	Columns      []any       `json:"columns"`
	Rows         []rowJSON   `json:"rows"`
}

// definitionColumnJSON is a definition's column in an object: the column
// a result gives it, and the outcome, differences and collisions of its own
// comparison with NetBox.
type definitionColumnJSON struct {
	Kind        ColumnKind      `json:"kind"`
	Source      string          `json:"source"`
	Path        *string         `json:"path"`
	DerivedFrom []string        `json:"derived_from"`
	Unmapped    map[string]int  `json:"unmapped"`
	Outcome     Outcome         `json:"outcome"`
	InStep      bool            `json:"in_step"`
	Differences int             `json:"differences"`
	Collisions  []collisionJSON `json:"collisions"`
}

type clashJSON struct {
	Rule   []string               `json:"rule"`
	Values map[string]model.Value `json:"values"`
	// Columns are the object's definitions that would hold the values, by
	// their index among its columns.
	Columns []int           `json:"columns"`
	With    []clashWithJSON `json:"with"`
}

// clashWithJSON is another object's definition that a clash is with: the
// object's type and identity, and the definition's source and file, or the
// files it is derived from.
type clashWithJSON struct {
	Type        string                 `json:"type"`
	Identity    map[string]model.Value `json:"identity"`
	Source      string                 `json:"source"`
	Path        *string                `json:"path"`
	DerivedFrom []string               `json:"derived_from"`
}

// MarshalJSON writes the object as `compare --output json` gives it.
func (o Object) MarshalJSON() ([]byte, error) {
	out := objectJSON{
		Type:         o.Type,
		Identity:     o.Identity,
		Outcome:      o.Outcome,
		InStep:       o.InStep(),
		Differences:  o.Differences,
		Collisions:   collisionsJSON(o.Collisions),
		Unresolved:   unresolvedsJSON(o.Unresolved),
		DuplicatedIn: o.DuplicatedIn,
		Clashes:      make([]clashJSON, 0, len(o.Clashes)),
		Columns:      make([]any, 0, len(o.Columns)),
		Rows:         rowsJSON(o.Rows),
	}
	if out.Identity == nil {
		out.Identity = map[string]model.Value{}
	}
	if o.NetBoxID != 0 {
		out.NetBoxID = &o.NetBoxID
	}
	if out.DuplicatedIn == nil {
		out.DuplicatedIn = []string{}
	}
	for _, c := range o.Clashes {
		j := clashJSON{Rule: c.Rule, Values: c.Values, Columns: c.Columns, With: make([]clashWithJSON, 0, len(c.With))}
		for _, w := range c.With {
			wj := clashWithJSON{Type: w.Type, Identity: w.Identity, Source: w.Column.Source, DerivedFrom: w.Column.DerivedFrom}
			if w.Column.Path != "" {
				wj.Path = &w.Column.Path
			}
			if wj.DerivedFrom == nil {
				wj.DerivedFrom = []string{}
			}
			j.With = append(j.With, wj)
		}
		out.Clashes = append(out.Clashes, j)
	}
	for i, c := range o.Columns {
		if c.Kind != DefinitionColumn || i >= len(o.Definitions) {
			out.Columns = append(out.Columns, c)
			continue
		}
		d := o.Definitions[i]
		j := definitionColumnJSON{
			Kind: c.Kind, Source: c.Source, DerivedFrom: c.DerivedFrom, Unmapped: c.Unmapped,
			Outcome: d.Outcome, InStep: d.InStep(), Differences: d.Differences, Collisions: collisionsJSON(d.Collisions),
		}
		if c.Path != "" {
			j.Path = &c.Path
		}
		if j.DerivedFrom == nil {
			j.DerivedFrom = []string{}
		}
		if j.Unmapped == nil {
			j.Unmapped = map[string]int{}
		}
		out.Columns = append(out.Columns, j)
	}
	return json.Marshal(out)
}

// MarshalJSON writes a column: a definition's with its source, its file or
// null, the files it is derived from and its unmapped children, and NetBox's
// with the object's ID.
func (c Column) MarshalJSON() ([]byte, error) {
	if c.Kind == NetBoxColumn {
		return json.Marshal(struct {
			Kind     ColumnKind `json:"kind"`
			NetBoxID int64      `json:"netbox_id"`
		}{c.Kind, c.ID})
	}
	out := struct {
		Kind        ColumnKind     `json:"kind"`
		Source      string         `json:"source"`
		Path        *string        `json:"path"`
		DerivedFrom []string       `json:"derived_from"`
		Unmapped    map[string]int `json:"unmapped"`
	}{Kind: c.Kind, Source: c.Source, DerivedFrom: c.DerivedFrom, Unmapped: c.Unmapped}
	if c.Path != "" {
		out.Path = &c.Path
	}
	if out.DerivedFrom == nil {
		out.DerivedFrom = []string{}
	}
	if out.Unmapped == nil {
		out.Unmapped = map[string]int{}
	}
	return json.Marshal(out)
}

// MarshalJSON writes a path as a list of its steps: a field's or a
// collection's name as a string, and a child's key as an object, such as
// ["interfaces", {"name": "eth0"}, "type"]. It writes the bytes encoding/json
// writes for the list of them, without going through it for the names and
// keys most paths have, since a whole library's comparisons hold millions.
func (p Path) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, 16*len(p)+2)
	b = append(b, '[')
	for i, s := range p {
		if i > 0 {
			b = append(b, ',')
		}
		var err error
		if s.Key == nil {
			b, err = appendJSONName(b, s.Field)
		} else {
			b, err = appendJSONKey(b, s.Key)
		}
		if err != nil {
			return nil, err
		}
	}
	return append(b, ']'), nil
}

// appendJSONName appends a name as a JSON string: between quotes, as it is,
// where it is made of the ASCII letters, digits and underscores a spec's
// names are made of, which JSON never escapes; through encoding/json
// otherwise.
func appendJSONName(b []byte, name string) ([]byte, error) {
	plain := name != ""
	for _, c := range []byte(name) {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' {
			plain = false
			break
		}
	}
	if plain {
		return append(append(append(b, '"'), name...), '"'), nil
	}
	out, err := json.Marshal(name)
	return append(b, out...), err
}

// appendJSONKey appends a child's key as a JSON object, as encoding/json
// writes the map: by hand for a key of one field, and through encoding/json
// for others, which it sorts by name.
func appendJSONKey(b []byte, key map[string]model.Value) ([]byte, error) {
	if len(key) != 1 {
		out, err := json.Marshal(key)
		return append(b, out...), err
	}
	for name, v := range key {
		b = append(b, '{')
		b, err := appendJSONName(b, name)
		if err != nil {
			return nil, err
		}
		out, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		b = append(append(append(b, ':'), out...), '}')
		return b, nil
	}
	return b, nil
}
