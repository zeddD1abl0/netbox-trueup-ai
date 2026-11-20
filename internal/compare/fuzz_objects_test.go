package compare_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/compare"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/dcim"
)

// fuzzSources are the sources fuzzing gives definitions to: the first is the
// one the tests' helpers give, so that inputs that end before choosing are
// one source's.
var fuzzSources = []string{source, "other", "third"}

// checkObjects checks Objects against a plain reading of what an object is:
// its definitions grouped by type and identity as NetBox would store them,
// a conflict where a source defines it twice or it clashes, and its rows
// each definition's own, side by side. It checks first that each source's
// definitions, compared alone, get what they get compared with the others.
func checkObjects(t *testing.T, defs []compare.Definition, observed []compare.Observed, results []compare.Result) {
	t.Helper()
	specs := dcim.Specs()
	for _, src := range fuzzSources {
		var mine []compare.Definition
		var at []int
		for i, d := range defs {
			if d.Source == src {
				mine, at = append(mine, d), append(at, i)
			}
		}
		if len(mine) == 0 {
			continue
		}
		for k, r := range run(t, mine, observed) {
			a, _ := json.Marshal(r)
			b, _ := json.Marshal(results[at[k]])
			if string(a) != string(b) {
				t.Fatalf("definition %d, of %s, compared with its source's alone:\n%s\nwith the others':\n%s", at[k], src, a, b)
			}
		}
	}

	objects, err := compare.Objects(specs, results)
	if err != nil {
		t.Fatalf("Objects: %v", err)
	}
	// The objects, the plain way: by type, and names trimmed, as the specs
	// trim them.
	keyOf := func(d compare.Definition) string {
		text := func(v model.Value) string { s, _ := v.(model.String); return strings.TrimSpace(string(s)) }
		if d.Object.Type == "dcim.manufacturer" {
			return "m\x00" + text(d.Identity["name"])
		}
		ref, _ := d.Identity["manufacturer"].(model.Reference)
		return "d\x00" + text(ref.Key["name"]) + "\x00" + text(d.Identity["model"])
	}
	var groups [][]int
	byKey := map[string]int{}
	objectOf := map[string]int{}
	for i, d := range defs {
		k := keyOf(d)
		g, ok := byKey[k]
		if !ok {
			g = len(groups)
			byKey[k] = g
			groups = append(groups, nil)
		}
		groups[g] = append(groups[g], i)
		objectOf[k] = g
	}
	if len(objects) != len(groups) {
		t.Fatalf("%d objects, want %d", len(objects), len(groups))
	}
	grouped, err := compare.Group(specs, defs)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.EqualFunc(grouped, groups, slices.Equal) {
		t.Fatalf("Group gives %v, want %v", grouped, groups)
	}

	clashes := clashOracle(defs, observed, results, groups)
	for g, o := range objects {
		of := groups[g]
		if len(o.Definitions) != len(of) || !slices.Equal(o.Results, of) {
			t.Fatalf("object %d has %d definitions, want %d", g, len(o.Definitions), len(of))
		}
		sources := map[string]int{}
		var duplicated []string
		conflict := false
		for c, i := range of {
			mine, want := results[i], o.Definitions[c]
			mine.Rows = nil
			a, _ := json.Marshal(mine)
			b, _ := json.Marshal(want)
			if string(a) != string(b) {
				t.Fatalf("object %d's definition %d:\n%s\nwant result %d's:\n%s", g, c, b, i, a)
			}
			if results[i].NetBoxID != o.NetBoxID {
				t.Fatalf("object %d is NetBox's #%d, but its definition %d's #%d", g, o.NetBoxID, c, results[i].NetBoxID)
			}
			sources[defs[i].Source]++
			conflict = conflict || results[i].Outcome == compare.Conflict
		}
		for _, i := range of {
			if src := defs[i].Source; sources[src] > 1 && !slices.Contains(duplicated, src) {
				duplicated = append(duplicated, src)
			}
		}
		if !slices.Equal(o.DuplicatedIn, duplicated) {
			t.Fatalf("object %d is duplicated in %q, want %q", g, o.DuplicatedIn, duplicated)
		}
		if got, want := collisionsText(o.Collisions), unionText(results, of); !slices.Equal(got, want) {
			t.Fatalf("object %d collides with\n%q\nwant its definitions' each once:\n%q", g, got, want)
		}
		if got := clashText(o); !slices.Equal(got, clashes[g]) {
			t.Fatalf("object %d clashes:\n%q\nwant:\n%q", g, got, clashes[g])
		}
		want := results[of[0]].Outcome
		if conflict || len(duplicated) > 0 || len(clashes[g]) > 0 {
			want = compare.Conflict
		}
		if o.Outcome != want {
			t.Fatalf("object %d is %s, want %s", g, o.Outcome, want)
		}
		for _, u := range o.Unresolved {
			want := compare.Outcome("")
			name, _ := u.Ref.Key["name"].(model.String)
			if h, ok := objectOf["m\x00"+strings.TrimSpace(string(name))]; ok {
				want = objects[h].Outcome
			}
			if u.Outcome != want {
				t.Fatalf("object %d's reference to %s is %q, want %q", g, u.Ref, u.Outcome, want)
			}
		}
		checkMerged(t, o, results, of)
		if _, err := json.Marshal(o); err != nil {
			t.Fatalf("object %d: %v", g, err)
		}
	}
}

// clashOracle works out each object's clashes the plain way, from the
// definitions and NetBox's objects: what an import of each definition would
// leave NetBox holding of the slug, and for a device type its
// manufacturer's name, against what NetBox's object of each holds now. A
// manufacturer's slug is derived, so it counts only where NetBox hasn't got
// the manufacturer; a device type's is the definition's.
func clashOracle(defs []compare.Definition, observed []compare.Observed, results []compare.Result, groups [][]int) [][]string {
	text := func(v model.Value) string { s, _ := v.(model.String); return strings.TrimSpace(string(s)) }
	byID := map[int64]compare.Observed{}
	for _, o := range observed {
		byID[o.ID] = o
	}
	// What a definition, or NetBox's object, would hold, or "".
	holds := func(i int) string {
		d := defs[i]
		match := results[i].NetBoxID
		if d.Object.Type == "dcim.manufacturer" {
			slug, set := d.Object.Fields["slug"]
			switch {
			case set && match == 0:
				return "m\x00" + text(slug)
			case match != 0:
				return "m\x00" + text(byID[match].Object.Fields["slug"])
			}
			return ""
		}
		ref, _ := d.Object.Fields["manufacturer"].(model.Reference)
		return "d\x00" + text(ref.Key["name"]) + "\x00" + text(d.Object.Fields["slug"])
	}
	heldNow := func(g int) string {
		match := results[groups[g][0]].NetBoxID
		if match == 0 {
			return ""
		}
		o := byID[match].Object
		if o.Type == "dcim.manufacturer" {
			return "m\x00" + text(o.Fields["slug"])
		}
		ref, _ := o.Fields["manufacturer"].(model.Reference)
		return "d\x00" + text(ref.Key["name"]) + "\x00" + text(o.Fields["slug"])
	}
	type holder struct{ object, column int }
	byKey := map[string][]holder{}
	var order []string
	for g, of := range groups {
		for c, i := range of {
			k := holds(i)
			if k == "" || strings.HasSuffix(k, "\x00") {
				continue
			}
			if byKey[k] == nil {
				order = append(order, k)
			}
			byKey[k] = append(byKey[k], holder{g, c})
		}
	}
	out := make([][]string, len(groups))
	for _, k := range order {
		hs := byKey[k]
		if !slices.ContainsFunc(hs, func(h holder) bool { return h.object != hs[0].object }) ||
			slices.ContainsFunc(hs, func(h holder) bool { return heldNow(h.object) == k }) {
			continue
		}
		for _, h := range hs {
			if n := len(out[h.object]); n > 0 && strings.HasPrefix(out[h.object][n-1], k+" ") {
				// The same object's next column, which the line already
				// lists.
				continue
			}
			var columns, with []string
			for _, other := range hs {
				i := groups[other.object][other.column]
				if other.object == h.object {
					columns = append(columns, itoa(int64(other.column)))
					continue
				}
				with = append(with, defs[i].Source+":"+keyText(defs[i]))
			}
			out[h.object] = append(out[h.object], k+" "+strings.Join(columns, ",")+" with "+strings.Join(with, ","))
		}
	}
	return out
}

// keyText names a definition's object by its trimmed identity.
func keyText(d compare.Definition) string {
	text := func(v model.Value) string { s, _ := v.(model.String); return strings.TrimSpace(string(s)) }
	if d.Object.Type == "dcim.manufacturer" {
		return text(d.Identity["name"])
	}
	ref, _ := d.Identity["manufacturer"].(model.Reference)
	return text(ref.Key["name"]) + "/" + text(d.Identity["model"])
}

// clashText writes an object's clashes as clashOracle does.
func clashText(o compare.Object) []string {
	text := func(v model.Value) string { s, _ := v.(model.String); return strings.TrimSpace(string(s)) }
	var out []string
	for _, c := range o.Clashes {
		k := "m\x00" + text(c.Values["slug"])
		if ref, ok := c.Values["manufacturer"].(model.Reference); ok {
			k = "d\x00" + text(ref.Key["name"]) + "\x00" + text(c.Values["slug"])
		}
		var columns, with []string
		for _, col := range c.Columns {
			columns = append(columns, itoa(int64(col)))
		}
		for _, w := range c.With {
			name := text(w.Identity["name"])
			if ref, ok := w.Identity["manufacturer"].(model.Reference); ok {
				name = text(ref.Key["name"]) + "/" + text(w.Identity["model"])
			}
			with = append(with, w.Column.Source+":"+name)
		}
		out = append(out, k+" "+strings.Join(columns, ",")+" with "+strings.Join(with, ","))
	}
	return out
}

// checkMerged checks an object's rows against its definitions' own
// comparisons: a row for each path any of them has; each definition's cell
// its own, or where it hasn't the row, false on a child's row whose parent
// it has, and unset otherwise; NetBox's cell what each of them gives; a row
// differing where two set cells do, and managed where some definition
// manages it. And the order: the object's fields in its spec's order, then
// its collections in their order; each child's row followed by its fields'
// and nested children's rows; the children some definition has in the
// order of the first that has each, then those only NetBox has.
func checkMerged(t *testing.T, o compare.Object, results []compare.Result, of []int) {
	t.Helper()
	if len(of) == 1 {
		merged, err := compare.MergeRows(dcim.Specs(), results, of)
		if err != nil {
			t.Fatal(err)
		}
		if a, b := rows(results[of[0]]), rows(compare.Result{Rows: merged}); !slices.Equal(a, b) {
			t.Fatalf("merging one comparison gives\n%q\nnot its rows\n%q", b, a)
		}
		if a := objectRows(o); !slices.Equal(a, rows(results[of[0]])) {
			t.Fatalf("an object of one definition has rows\n%q\nnot its definition's", a)
		}
	}
	pathOf := func(p compare.Path) string { data, _ := json.Marshal(p); return string(data) }
	type at struct {
		row   compare.Row
		index int
	}
	mine := make([]map[string]at, len(of))
	union := map[string]bool{}
	for c, i := range of {
		mine[c] = map[string]at{}
		for k, row := range results[i].Rows {
			mine[c][pathOf(row.Path)] = at{row, k}
			union[pathOf(row.Path)] = true
		}
	}
	if len(o.Rows) != len(union) {
		t.Fatalf("the object has %d rows; its definitions' comparisons %d paths", len(o.Rows), len(union))
	}
	nb := o.NetBoxID != 0
	differences := 0
	isTrue := func(v model.Value) bool { b, ok := v.(model.Boolean); return ok && bool(b) }
	for _, row := range o.Rows {
		p := pathOf(row.Path)
		if !union[p] {
			t.Fatalf("the object has a row %s no definition's comparison has", row.Path)
		}
		width := len(of)
		if nb {
			width++
		}
		if len(row.Cells) != width {
			t.Fatalf("row %s has %d cells, want %d", row.Path, len(row.Cells), width)
		}
		managed, derived := false, false
		var netbox model.Value
		var netboxID int64
		for c := range of {
			theirs, ok := mine[c][p]
			var want model.Value
			switch {
			case ok:
				want = theirs.row.Cells[0]
				managed = managed || theirs.row.Managed
				derived = derived || theirs.row.Derived
				if nb {
					netbox, netboxID = theirs.row.Cells[1], theirs.row.NetBoxID
				}
			case row.Path[len(row.Path)-1].Key != nil:
				// A child's row: false where the definition has the parent.
				parent := -1
				for j := len(row.Path) - 2; j >= 0; j-- {
					if row.Path[j].Key != nil {
						parent = j
						break
					}
				}
				if parent < 0 {
					want = model.Boolean(false)
				} else if up, ok := mine[c][pathOf(row.Path[:parent+1])]; ok && isTrue(up.row.Cells[0]) {
					want = model.Boolean(false)
				}
			}
			if (want == nil) != (row.Cells[c] == nil) || want != nil && !model.Equal(want, row.Cells[c]) {
				t.Fatalf("row %s's cell %d is %v, want %v", row.Path, c, row.Cells[c], want)
			}
		}
		if nb {
			got := row.Cells[len(of)]
			if (netbox == nil) != (got == nil) || got != nil && !model.Equal(netbox, got) || row.NetBoxID != netboxID {
				t.Fatalf("row %s's NetBox cell is %v (#%d), want %v (#%d)", row.Path, got, row.NetBoxID, netbox, netboxID)
			}
		}
		differs := false
		for a := range row.Cells {
			for b := range a {
				if row.Cells[a] != nil && row.Cells[b] != nil && !model.Equal(row.Cells[a], row.Cells[b]) {
					differs = true
				}
			}
		}
		if row.Differs != differs || row.Managed != managed || row.Derived != derived {
			t.Fatalf("row %s differs %t, managed %t, derived %t; want %t, %t, %t", row.Path, row.Differs, row.Managed, row.Derived, differs, managed, derived)
		}
		if differs && managed {
			differences++
		}
	}
	if o.Differences != differences {
		t.Fatalf("the object counts %d differences; %d rows differ and are managed", o.Differences, differences)
	}

	// The order. Each row's place among its siblings: a field by its place
	// in the spec, a child by whether a definition has it, the first
	// definition that has it, or has its row, and the row's index there.
	specs := dcim.Specs()
	place := func(row compare.Row) [][3]int {
		var out [][3]int
		cur := model.SpecOf(specs, o.Type)
		for i, step := range row.Path {
			if step.Key != nil {
				p := pathOf(row.Path[:i+1])
				first, defined := -1, false
				for c := range of {
					if theirs, ok := mine[c][p]; ok && isTrue(theirs.row.Cells[0]) {
						out, defined = append(out, [3]int{0, c, theirs.index}), true
						break
					}
				}
				if !defined {
					for c := range of {
						if theirs, ok := mine[c][p]; ok {
							first = c
							out = append(out, [3]int{1, c, theirs.index})
							break
						}
					}
					if first < 0 {
						t.Fatalf("row %s is below a child no definition's comparison has", row.Path)
					}
				}
				continue
			}
			if j := slices.IndexFunc(cur.Fields, func(f model.Field) bool { return f.Name == step.Field }); j >= 0 {
				out = append(out, [3]int{0, j, 0})
				continue
			}
			j := slices.IndexFunc(cur.Children, func(col model.Collection) bool { return col.Name == step.Field })
			out = append(out, [3]int{1, j, 0})
			cur = model.SpecOf(specs, cur.Children[j].Type)
		}
		return out
	}
	for k := 1; k < len(o.Rows); k++ {
		a, b := place(o.Rows[k-1]), place(o.Rows[k])
		if slices.CompareFunc(a, b, func(x, y [3]int) int { return slices.Compare(x[:], y[:]) }) >= 0 {
			t.Fatalf("row %s comes before row %s", o.Rows[k-1].Path, o.Rows[k].Path)
		}
	}
}

// collisionsText writes collisions sorted, one a line.
func collisionsText(collisions []compare.Collision) []string {
	var out []string
	for _, c := range collisions {
		out = append(out, c.Path.String()+" "+strings.Join(c.Rule, ",")+" #"+itoa(c.ID))
	}
	slices.Sort(out)
	return out
}

// unionText writes the collisions of an object's definitions' results, of,
// each once: an object's own by rule and NetBox ID, and a child's, which
// are NetBox's children sharing a key, as the first definition that has any
// at its path gives them, since nested children have no IDs to tell them
// apart.
func unionText(results []compare.Result, of []int) []string {
	var union []compare.Collision
	own := map[string]bool{}
	from := map[string]int{}
	for _, i := range of {
		for _, c := range results[i].Collisions {
			if len(c.Path) == 0 {
				k := strings.Join(c.Rule, ",") + " #" + itoa(c.ID)
				if !own[k] {
					own[k] = true
					union = append(union, c)
				}
				continue
			}
			p := c.Path.String()
			if first, ok := from[p]; ok && first != i {
				continue
			}
			from[p] = i
			union = append(union, c)
		}
	}
	return collisionsText(union)
}
