package compare_test

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/compare"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// The values fuzzing picks from: few, so that definitions and NetBox's
// objects often share them, and differing only in case or white space.
var (
	fuzzNames  = []string{"Dell", "dell", "Dell ", "NETGEAR", "Netgear", "HPE"}
	fuzzSlugs  = []string{"dell", "netgear", "hpe", "x"}
	fuzzModels = []string{"R670", "r670", "R670 ", " R670", "GS105"}
)

// fuzzInput reads a fuzzer's bytes as definitions and NetBox objects.
type fuzzInput struct {
	data []byte
}

func (in *fuzzInput) next(n int) int {
	if len(in.data) == 0 {
		return 0
	}
	b := in.data[0]
	in.data = in.data[1:]
	return int(b) % n
}

// build makes up to 4 NetBox manufacturers, 6 device types, 3 derived
// manufacturer definitions and 4 device type definitions. NetBox's objects
// need not keep NetBox's rules: an answer can't be trusted to.
func (in *fuzzInput) build() ([]compare.Definition, []compare.Observed) {
	var observed []compare.Observed
	var makers []compare.Observed
	id := int64(1)
	for range in.next(5) {
		m := nbManufacturer(id, fuzzNames[in.next(len(fuzzNames))], fuzzSlugs[in.next(len(fuzzSlugs))])
		makers = append(makers, m)
		observed = append(observed, m)
		id++
	}
	for range in.next(7) {
		if len(makers) == 0 {
			break
		}
		m := makers[in.next(len(makers))]
		name, _ := m.Object.Fields["name"].(model.String)
		fields := map[string]model.Value{"u_height": model.NewDecimal(int64(1+in.next(2)), 0)}
		if in.next(2) == 1 {
			fields["airflow"] = model.Choice("front-to-rear")
		}
		observed = append(observed, nbDeviceType(id, m.ID, string(name), fuzzModels[in.next(len(fuzzModels))], fuzzSlugs[in.next(len(fuzzSlugs))], fields))
		id++
	}
	var defs []compare.Definition
	for range in.next(4) {
		slug := ""
		if in.next(4) > 0 {
			slug = fuzzSlugs[in.next(len(fuzzSlugs))]
		}
		defs = append(defs, manufacturer(fuzzNames[in.next(len(fuzzNames))], slug))
	}
	for range in.next(5) {
		fields := map[string]model.Value{"u_height": model.NewDecimal(int64(1+in.next(2)), 0)}
		switch in.next(3) {
		case 1:
			fields["airflow"] = model.Choice("")
		case 2:
			fields["airflow"] = model.Choice("front-to-rear")
		}
		defs = append(defs, deviceType(fuzzNames[in.next(len(fuzzNames))], fuzzModels[in.next(len(fuzzModels))], fuzzSlugs[in.next(len(fuzzSlugs))], fields))
	}
	return defs, observed
}

// FuzzCompare checks Compare against a plain reading of NetBox's rules, and
// that its results don't depend on the order of NetBox's objects.
func FuzzCompare(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{2, 0, 0, 1, 1, 3, 0, 0, 0, 0, 0, 1, 0, 1, 0, 2, 2, 1, 0, 0, 2, 0, 0, 0})
	f.Add([]byte{4, 3, 1, 4, 1, 0, 0, 6, 0, 0, 1, 0, 0, 1, 0, 1, 1, 1, 2, 0, 2, 3, 1, 1, 3, 1, 4, 0, 1, 0, 1, 3, 2, 1, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		in := &fuzzInput{data: data}
		defs, observed := in.build()
		results := run(t, defs, observed)

		reversed := slices.Clone(observed)
		slices.Reverse(reversed)
		again := run(t, defs, reversed)
		for i := range results {
			a, _ := json.Marshal(results[i])
			b, _ := json.Marshal(again[i])
			if string(a) != string(b) {
				t.Fatalf("the order of NetBox's objects changed result %d:\n%s\n%s", i, a, b)
			}
		}

		for i, r := range results {
			want := oracle(defs[i], observed)
			if r.Outcome != want.outcome || r.NetBoxID != want.match {
				t.Fatalf("definition %d (%v): Compare says %s matching #%d, NetBox's rules say %s matching #%d",
					i, defs[i].Object.Fields, r.Outcome, r.NetBoxID, want.outcome, want.match)
			}
			var got []int64
			for _, c := range r.Collisions {
				if c.ID == r.NetBoxID {
					t.Fatalf("definition %d collides with its own match #%d", i, c.ID)
				}
				got = append(got, c.ID)
			}
			slices.Sort(got)
			if !slices.Equal(slices.Compact(got), want.collisions) {
				t.Fatalf("definition %d collides with %v, want %v", i, got, want.collisions)
			}
			checkRows(t, r)
		}
	})
}

// checkRows checks a result's rows against its columns and outcome.
func checkRows(t *testing.T, r compare.Result) {
	t.Helper()
	differences := 0
	for _, row := range r.Rows {
		if len(row.Cells) != len(r.Columns) {
			t.Fatalf("row %s has %d cells for %d columns", row.Path, len(row.Cells), len(r.Columns))
		}
		if row.Managed && row.Cells[0] == nil {
			t.Fatalf("row %s is managed, but the definition doesn't set it", row.Path)
		}
		if row.Managed && row.Derived && r.NetBoxID != 0 {
			t.Fatalf("row %s is derived and managed, though NetBox has the object", row.Path)
		}
		if row.Differs && row.Managed {
			differences++
		}
	}
	if differences != r.Differences {
		t.Fatalf("%d rows differ and are managed, but Differences is %d", differences, r.Differences)
	}
}

type expected struct {
	outcome    compare.Outcome
	match      int64
	collisions []int64
}

// oracle works out a definition's outcome the plain way: by looking through
// every NetBox object for each of the type's rules.
func oracle(d compare.Definition, observed []compare.Observed) expected {
	text := func(v model.Value) string { s, _ := v.(model.String); return string(s) }
	trim := func(v model.Value) string { return strings.TrimSpace(text(v)) }
	str := func(o compare.Observed, name string) string { return text(o.Object.Fields[name]) }
	makerOf := func(o compare.Observed) int64 {
		ref, _ := o.Object.Fields["manufacturer"].(model.Reference)
		return ref.ID
	}
	var e expected
	collide := map[int64]bool{}
	if d.Object.Type == "dcim.manufacturer" {
		name := trim(d.Object.Fields["name"])
		slug, hasSlug := d.Object.Fields["slug"]
		var named []int64
		for _, o := range observed {
			if o.Object.Type == "dcim.manufacturer" && str(o, "name") == name {
				named = append(named, o.ID)
			}
		}
		if len(named) == 1 {
			e.match = named[0]
		} else {
			for _, id := range named {
				collide[id] = true
			}
		}
		// The slug is derived, so it counts only if NetBox lacks the name.
		if hasSlug && e.match == 0 {
			for _, o := range observed {
				if o.Object.Type == "dcim.manufacturer" && str(o, "slug") == trim(slug) {
					collide[o.ID] = true
				}
			}
		}
	} else {
		ref, _ := d.Object.Fields["manufacturer"].(model.Reference)
		var maker int64
		var makers []int64
		for _, o := range observed {
			if o.Object.Type == "dcim.manufacturer" && str(o, "name") == text(ref.Key["name"]) {
				makers = append(makers, o.ID)
			}
		}
		if len(makers) == 1 {
			maker = makers[0]
		}
		var same []int64
		for _, o := range observed {
			if o.Object.Type != "dcim.devicetype" || maker == 0 || makerOf(o) != maker {
				continue
			}
			if str(o, "model") == trim(d.Object.Fields["model"]) {
				same = append(same, o.ID)
			}
		}
		if len(same) == 1 {
			e.match = same[0]
		} else {
			for _, id := range same {
				collide[id] = true
			}
		}
		for _, o := range observed {
			if o.Object.Type == "dcim.devicetype" && maker != 0 && makerOf(o) == maker &&
				str(o, "slug") == trim(d.Object.Fields["slug"]) && o.ID != e.match {
				collide[o.ID] = true
			}
		}
	}
	for id := range collide {
		e.collisions = append(e.collisions, id)
	}
	slices.Sort(e.collisions)
	switch {
	case len(e.collisions) > 0:
		e.outcome = compare.Conflict
	case e.match != 0:
		e.outcome = compare.Candidate
	default:
		e.outcome = compare.Absent
	}
	return e
}

// FuzzCompareWithItself checks that a definition made from what NetBox
// holds compares as in step with it, and that changing one field makes
// exactly that difference.
func FuzzCompareWithItself(f *testing.F) {
	f.Add(0, 0, 0, uint8(0))
	f.Add(1, 3, 2, uint8(5))
	f.Fuzz(func(t *testing.T, name, modelIdx, slug int, change uint8) {
		pick := func(list []string, i int) string { return list[((i%len(list))+len(list))%len(list)] }
		makerName := strings.TrimSpace(pick(fuzzNames, name))
		modelName := strings.TrimSpace(pick(fuzzModels, modelIdx))
		maker := nbManufacturer(1, makerName, pick(fuzzSlugs, slug))
		nb := nbDeviceType(2, 1, makerName, modelName, pick(fuzzSlugs, slug), nil)

		def := deviceType(makerName, modelName, pick(fuzzSlugs, slug), nil)
		maps.Copy(def.Object.Fields, nb.Object.Fields)
		def.Object.Fields["manufacturer"] = manufacturerRef(makerName)
		r := run(t, []compare.Definition{def}, []compare.Observed{maker, nb})[0]
		if !r.InStep() {
			t.Fatalf("a definition of what NetBox holds compares as %s with %d differences", r.Outcome, r.Differences)
		}

		changes := []struct {
			field string
			value model.Value
		}{
			{"part_number", model.String("X-1")},
			{"u_height", model.NewDecimal(25, 1)},
			{"is_full_depth", model.Boolean(false)},
			{"airflow", model.Choice("rear-to-front")},
			{"weight", model.NewDecimal(1250, 2)},
			{"description", model.String("changed")},
			{"comments", model.String("changed")},
		}
		c := changes[int(change)%len(changes)]
		def.Object.Fields[c.field] = c.value
		r = run(t, []compare.Definition{def}, []compare.Observed{maker, nb})[0]
		if r.Outcome != compare.Candidate || r.Differences != 1 {
			t.Fatalf("changing %s: %s with %d differences, want a candidate with 1", c.field, r.Outcome, r.Differences)
		}
		for _, row := range r.Rows {
			if row.Differs != (row.Path.String() == c.field) {
				t.Fatalf("changing %s: row %s differs = %t", c.field, row.Path, row.Differs)
			}
		}
	})
}
