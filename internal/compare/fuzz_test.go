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
	// Interface names, which NetBox might hold untrimmed or twice, though
	// only another tool could.
	fuzzIfaces     = []string{"eth0", "eth0 ", "ETH0", "eth1"}
	fuzzIfaceTypes = []string{"1000base-t", "10gbase-t"}
	fuzzPorts      = []string{"PSU1", "PSU2"}
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
		dt := id
		id++
		for range in.next(4) {
			observed = append(observed, nbChild(id, dt, "interface", fuzzIfaces[in.next(len(fuzzIfaces))],
				map[string]model.Value{"type": model.Choice(fuzzIfaceTypes[in.next(len(fuzzIfaceTypes))])}))
			id++
		}
		for range in.next(3) {
			observed = append(observed, nbChild(id, dt, "powerport", fuzzPorts[in.next(len(fuzzPorts))], nil))
			id++
		}
		if in.next(2) == 1 {
			// Its power port, by ID: none, or any power port observed so
			// far, of this device type or another.
			var ports []int64
			for _, o := range observed {
				if o.Object.Type == "dcim.powerporttemplate" {
					ports = append(ports, o.ID)
				}
			}
			var port model.Value = model.Null{}
			if k := in.next(len(ports) + 1); k < len(ports) {
				port = model.Reference{Type: "dcim.powerporttemplate", ID: ports[k]}
			}
			observed = append(observed, nbChild(id, dt, "poweroutlet", "O1", map[string]model.Value{"power_port": port}))
			id++
		}
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
		var ifaces model.Children
		named := map[string]bool{}
		for range in.next(4) {
			// A definition's names are as NetBox would store them, and
			// different.
			name := strings.TrimSpace(fuzzIfaces[in.next(len(fuzzIfaces))])
			if named[name] {
				continue
			}
			named[name] = true
			ifaces = append(ifaces, child("interface", name, map[string]model.Value{"type": model.Choice(fuzzIfaceTypes[in.next(len(fuzzIfaceTypes))])}))
		}
		if ifaces != nil {
			fields["interfaces"] = ifaces
		}
		if in.next(2) == 1 {
			port := fuzzPorts[in.next(len(fuzzPorts))]
			fields["power_ports"] = model.Children{child("powerport", port, nil)}
			if in.next(2) == 1 {
				fields["power_outlets"] = model.Children{child("poweroutlet", "O1", map[string]model.Value{"power_port": powerPort(port)})}
			}
		}
		// The format refuses a manufacturer's name with white space at an
		// end, which NetBox's import wouldn't find, and Compare does too.
		maker := strings.TrimSpace(fuzzNames[in.next(len(fuzzNames))])
		d := deviceType(maker, fuzzModels[in.next(len(fuzzModels))], fuzzSlugs[in.next(len(fuzzSlugs))], fields)
		d.Unmapped = nil
		defs = append(defs, d)
	}
	return defs, observed
}

// FuzzCompare checks Compare against a plain reading of NetBox's rules, and
// that its results don't depend on the order of NetBox's objects.
func FuzzCompare(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{2, 0, 0, 1, 1, 3, 0, 0, 0, 0, 0, 1, 0, 1, 0, 2, 2, 1, 0, 0, 2, 0, 0, 0})
	f.Add([]byte{4, 3, 1, 4, 1, 0, 0, 6, 0, 0, 1, 0, 0, 1, 0, 1, 1, 1, 2, 0, 2, 3, 1, 1, 3, 1, 4, 0, 1, 0, 1, 3, 2, 1, 0, 0})
	// Dell's R670 in NetBox with eth0 twice, eth1, PSU1 and PSU2, and O1
	// on PSU2; and a definition of it with eth0, ETH0, PSU1, and O1 on
	// PSU1.
	f.Add([]byte{1, 0, 0, 1, 0, 0, 0, 0, 0, 3, 0, 0, 0, 1, 3, 0, 2, 0, 1, 1, 1, 0, 1, 0, 0, 2, 0, 0, 2, 1, 1, 0, 1, 0, 0, 0})
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
			kids := childOracle(defs[i], observed, want.match)
			if len(kids.twice) > 0 {
				want.outcome = compare.Conflict
			}
			if r.Outcome != want.outcome || r.NetBoxID != want.match {
				t.Fatalf("definition %d (%v): Compare says %s matching #%d, NetBox's rules say %s matching #%d",
					i, defs[i].Object.Fields, r.Outcome, r.NetBoxID, want.outcome, want.match)
			}
			var got, gotTwice []int64
			for _, c := range r.Collisions {
				if len(c.Path) > 0 {
					gotTwice = append(gotTwice, c.ID)
					continue
				}
				if c.ID == r.NetBoxID {
					t.Fatalf("definition %d collides with its own match #%d", i, c.ID)
				}
				got = append(got, c.ID)
			}
			slices.Sort(got)
			if !slices.Equal(slices.Compact(got), want.collisions) {
				t.Fatalf("definition %d collides with %v, want %v", i, got, want.collisions)
			}
			slices.Sort(gotTwice)
			if !slices.Equal(gotTwice, kids.twice) {
				t.Fatalf("definition %d: NetBox's children %v share keys, want %v", i, gotTwice, kids.twice)
			}
			var differ, onlyNetBox []string
			for _, row := range r.Rows {
				switch {
				case len(row.Path) < 2 || !row.Differs:
				case row.Managed:
					differ = append(differ, row.Path.String())
				default:
					onlyNetBox = append(onlyNetBox, row.Path.String())
				}
			}
			slices.Sort(differ)
			slices.Sort(onlyNetBox)
			if !slices.Equal(differ, kids.differ) || !slices.Equal(onlyNetBox, kids.onlyNetBox) {
				t.Fatalf("definition %d's children differ in %q and NetBox alone has %q; want %q and %q",
					i, differ, onlyNetBox, kids.differ, kids.onlyNetBox)
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

// expectedChildren is what a definition's children make of its comparison.
type expectedChildren struct {
	// differ and onlyNetBox are the paths of the children, or children's
	// fields, that differ and are managed, and of the children only NetBox
	// has, sorted.
	differ, onlyNetBox []string
	// twice are the NetBox children whose name another child of their
	// device type has, sorted.
	twice []int64
}

// childOracle works out the plain way how a definition's children compare
// with those of NetBox's device type #match: by name, exactly, and a power
// outlet's power port by the name of the power port of the same device type
// NetBox's ID refers to.
func childOracle(d compare.Definition, observed []compare.Observed, match int64) expectedChildren {
	var e expectedChildren
	if match == 0 {
		return e
	}
	parent := func(o compare.Observed) int64 {
		ref, _ := o.Object.Fields["device_type"].(model.Reference)
		return ref.ID
	}
	nameOf := func(fields map[string]model.Value) string {
		name, _ := fields["name"].(model.String)
		return string(name)
	}
	byID := map[int64]compare.Observed{}
	for _, o := range observed {
		byID[o.ID] = o
	}
	for _, col := range []struct{ name, typ string }{
		{"power_ports", "dcim.powerporttemplate"},
		{"power_outlets", "dcim.poweroutlettemplate"},
		{"interfaces", "dcim.interfacetemplate"},
	} {
		netbox := map[string][]compare.Observed{}
		for _, o := range observed {
			if o.Object.Type == col.typ && parent(o) == match {
				netbox[nameOf(o.Object.Fields)] = append(netbox[nameOf(o.Object.Fields)], o)
			}
		}
		where := func(name string) string { return col.name + "[" + model.String(name).String() + "]" }
		defined := map[string]bool{}
		children, _ := d.Object.Fields[col.name].(model.Children)
		for _, c := range children {
			name := nameOf(c.Fields)
			defined[name] = true
			theirs := netbox[name]
			if len(theirs) == 0 {
				e.differ = append(e.differ, where(name))
			}
			if len(theirs) != 1 {
				continue
			}
			if mine, ok := c.Fields["type"]; ok && !model.Equal(mine, theirs[0].Object.Fields["type"]) {
				e.differ = append(e.differ, where(name)+".type")
			}
			if mine, ok := c.Fields["power_port"].(model.Reference); ok {
				ref, _ := theirs[0].Object.Fields["power_port"].(model.Reference)
				port, found := byID[ref.ID]
				if !found || parent(port) != match || nameOf(port.Object.Fields) != nameOf(mine.Key) {
					e.differ = append(e.differ, where(name)+".power_port")
				}
			}
		}
		for name, theirs := range netbox {
			if !defined[name] {
				e.onlyNetBox = append(e.onlyNetBox, where(name))
			}
			if len(theirs) > 1 {
				for _, o := range theirs {
					e.twice = append(e.twice, o.ID)
				}
			}
		}
	}
	slices.Sort(e.differ)
	slices.Sort(e.onlyNetBox)
	slices.Sort(e.twice)
	return e
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
		if len(makers) > 1 {
			// NetBox can't say which manufacturer it is: a conflict, with
			// whatever it would collide with under any of them.
			for _, o := range observed {
				if o.Object.Type == "dcim.devicetype" && slices.Contains(makers, makerOf(o)) &&
					(str(o, "model") == trim(d.Object.Fields["model"]) || str(o, "slug") == trim(d.Object.Fields["slug"])) {
					collide[o.ID] = true
				}
			}
			for id := range collide {
				e.collisions = append(e.collisions, id)
			}
			slices.Sort(e.collisions)
			e.outcome = compare.Conflict
			return e
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

		// NetBox's children, and the definition's of them.
		kids := []compare.Observed{
			nbChild(3, 2, "interface", "eth0", map[string]model.Value{"type": model.Choice("1000base-t"), "mgmt_only": model.Boolean(true)}),
			nbChild(4, 2, "interface", "eth1", map[string]model.Value{"type": model.Choice("10gbase-t")}),
			nbChild(5, 2, "powerport", "PSU1", map[string]model.Value{"type": model.Choice("iec-60320-c14"), "maximum_draw": model.Integer(500)}),
			nbChild(6, 2, "poweroutlet", "O1", map[string]model.Value{"power_port": model.Reference{Type: "dcim.powerporttemplate", ID: 5}}),
		}
		mine := map[string]model.Children{
			"interfaces": {
				child("interface", "eth0", map[string]model.Value{"type": model.Choice("1000base-t"), "mgmt_only": model.Boolean(true)}),
				child("interface", "eth1", map[string]model.Value{"type": model.Choice("10gbase-t")}),
			},
			"power_ports":   {child("powerport", "PSU1", map[string]model.Value{"type": model.Choice("iec-60320-c14"), "maximum_draw": model.Integer(500)})},
			"power_outlets": {child("poweroutlet", "O1", map[string]model.Value{"power_port": powerPort("PSU1")})},
		}
		for name, c := range mine {
			def.Object.Fields[name] = c
		}
		observed := append([]compare.Observed{maker, nb}, kids...)
		r = run(t, []compare.Definition{def}, observed)[0]
		if !r.InStep() {
			t.Fatalf("a definition of what NetBox holds, with its children, compares as %s with %d differences", r.Outcome, r.Differences)
		}
		// Leaving a child out manages it no more.
		without := cloneDefinition(def)
		without.Object.Fields["interfaces"] = mine["interfaces"][:1]
		if r = run(t, []compare.Definition{without}, observed)[0]; !r.InStep() {
			t.Fatalf("leaving eth1 out: %s with %d differences, want in step", r.Outcome, r.Differences)
		}

		withChild := func(collection string, i int, field string, v model.Value) func(compare.Definition) {
			return func(d compare.Definition) {
				children, _ := d.Object.Fields[collection].(model.Children)
				c := slices.Clone(children)
				c[i] = model.Object{Type: c[i].Type, Fields: maps.Clone(c[i].Fields)}
				c[i].Fields[field] = v
				d.Object.Fields[collection] = c
			}
		}
		setField := func(field string, v model.Value) func(compare.Definition) {
			return func(d compare.Definition) { d.Object.Fields[field] = v }
		}
		changes := []struct {
			path   string
			change func(compare.Definition)
		}{
			{"part_number", setField("part_number", model.String("X-1"))},
			{"u_height", setField("u_height", model.NewDecimal(25, 1))},
			{"is_full_depth", setField("is_full_depth", model.Boolean(false))},
			{"airflow", setField("airflow", model.Choice("rear-to-front"))},
			{"weight", setField("weight", model.NewDecimal(1250, 2))},
			{"description", setField("description", model.String("changed"))},
			{"comments", setField("comments", model.String("changed"))},
			{`interfaces["eth1"].type`, withChild("interfaces", 1, "type", model.Choice("1000base-t"))},
			{`interfaces["eth0"].mgmt_only`, withChild("interfaces", 0, "mgmt_only", model.Boolean(false))},
			// A field NetBox has at its default, set to something else.
			{`interfaces["eth1"].enabled`, withChild("interfaces", 1, "enabled", model.Boolean(false))},
			{`power_ports["PSU1"].maximum_draw`, withChild("power_ports", 0, "maximum_draw", model.Null{})},
			{`power_outlets["O1"].power_port`, withChild("power_outlets", 0, "power_port", model.Null{})},
			{`device_bays["Bay 1"]`, setField("device_bays", model.Children{child("devicebay", "Bay 1", nil)})},
		}
		c := changes[int(change)%len(changes)]
		changed := cloneDefinition(def)
		c.change(changed)
		r = run(t, []compare.Definition{changed}, observed)[0]
		if r.Outcome != compare.Candidate || r.Differences != 1 {
			t.Fatalf("changing %s: %s with %d differences, want a candidate with 1", c.path, r.Outcome, r.Differences)
		}
		for _, row := range r.Rows {
			if row.Differs != (row.Path.String() == c.path) {
				t.Fatalf("changing %s: row %s differs = %t", c.path, row.Path, row.Differs)
			}
		}
	})
}

// cloneDefinition copies a definition's fields, so that changing them
// leaves the original as it is.
func cloneDefinition(d compare.Definition) compare.Definition {
	d.Object.Fields = maps.Clone(d.Object.Fields)
	return d
}
