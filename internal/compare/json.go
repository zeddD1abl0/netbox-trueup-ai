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
	Rule     []string               `json:"rule"`
	NetBoxID int64                  `json:"netbox_id"`
	Identity map[string]model.Value `json:"identity"`
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
}

// cellJSON is {"value": ...} for a version that sets the field, null
// included, and {} for one that doesn't.
type cellJSON struct {
	Value model.Value `json:"value,omitempty"`
}

// MarshalJSON writes the result as `compare --output json` gives it.
func (r Result) MarshalJSON() ([]byte, error) {
	out := resultJSON{
		Type:        r.Type,
		Identity:    r.Identity,
		Outcome:     r.Outcome,
		InStep:      r.InStep(),
		Differences: r.Differences,
		Collisions:  make([]collisionJSON, 0, len(r.Collisions)),
		Unresolved:  make([]unresolvedJSON, 0, len(r.Unresolved)),
		Columns:     r.Columns,
		Rows:        make([]rowJSON, 0, len(r.Rows)),
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
	for _, c := range r.Collisions {
		out.Collisions = append(out.Collisions, collisionJSON{Rule: c.Rule, NetBoxID: c.ID, Identity: c.Identity})
	}
	for _, u := range r.Unresolved {
		j := unresolvedJSON{Field: u.Field, Type: u.Ref.Type, Key: u.Ref.Key, NetBoxIDs: u.Candidates}
		if j.NetBoxIDs == nil {
			j.NetBoxIDs = []int64{}
		}
		if u.Outcome != "" {
			j.Outcome = &u.Outcome
		}
		out.Unresolved = append(out.Unresolved, j)
	}
	for _, row := range r.Rows {
		j := rowJSON{Path: row.Path, Differs: row.Differs, Managed: row.Managed, Derived: row.Derived}
		for _, v := range row.Cells {
			j.Cells = append(j.Cells, cellJSON{Value: v})
		}
		out.Rows = append(out.Rows, j)
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

// MarshalJSON writes a path as a list of its steps: a field's name, for
// now. Steps into child collections will be objects, a child's key.
func (p Path) MarshalJSON() ([]byte, error) {
	out := make([]string, len(p))
	for i, s := range p {
		out[i] = s.Field
	}
	return json.Marshal(out)
}
