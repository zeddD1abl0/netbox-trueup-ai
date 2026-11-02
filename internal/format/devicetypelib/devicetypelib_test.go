package devicetypelib

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source/sourcetest"
)

var update = flag.Bool("update", false, "rewrite testdata/library.golden.json from what Read makes of testdata/library")

// TestReadLibrary reads the files copied from devicetype-library and checks
// the result against testdata/library.golden.json: every definition, field
// by field, and every problem.
func TestReadLibrary(t *testing.T) {
	t.Parallel()
	snap := sourcetest.Snapshot(t, "lib", os.DirFS("testdata/library"))
	res, err := Format{}.Read(t.Context(), snap)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const golden = "testdata/library.golden.json"
	if *update {
		if err := os.WriteFile(golden, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("Read (-%s +got); run go test -update to accept:\n%s", golden, diff)
	}
	if res.Files != 20 || res.Invalid != 0 {
		t.Errorf("read %d files, %d invalid; want 20 and none", res.Files, res.Invalid)
	}
}

// TestReadLibraryAsNetBoxReadsIt checks what matters most in the copied
// files, beyond the golden file: values read as PyYAML reads them, metadata
// kept apart, components counted, and manufacturers derived.
func TestReadLibraryAsNetBoxReadsIt(t *testing.T) {
	t.Parallel()
	snap := sourcetest.Snapshot(t, "lib", os.DirFS("testdata/library"))
	res, err := Format{}.Read(t.Context(), snap)
	if err != nil {
		t.Fatal(err)
	}
	defs := map[string]format.Definition{}
	for _, d := range res.Definitions {
		if d.Path != "" {
			defs[d.Path] = d
		} else {
			name, _ := d.Identity["name"].(model.String)
			defs[d.Object.Type+":"+string(name)] = d
		}
	}
	huawei := defs["device-types/Huawei/NE8000-X8.yaml"]
	for field, want := range map[string]model.Value{
		// Not octal, so text, as PyYAML reads it.
		"part_number":   model.String("02351930"),
		"u_height":      model.NewDecimal(16, 0),
		"is_full_depth": model.Boolean(true),
		"airflow":       model.Choice("front-to-rear"),
		"weight":        model.NewDecimal(94, 0),
		"weight_unit":   model.Choice("kg"),
		"manufacturer":  model.Reference{Type: "dcim.manufacturer", Key: map[string]model.Value{"name": model.String("Huawei")}},
	} {
		if got := huawei.Object.Fields[field]; !model.Equal(got, want) {
			t.Errorf("NE8000-X8's %s = %v, want %v", field, got, want)
		}
	}
	if huawei.Unmapped["module-bays"] != 18 || huawei.Metadata != nil {
		t.Errorf("NE8000-X8 has %v unmapped and metadata %v, want 18 module bays and none", huawei.Unmapped, huawei.Metadata)
	}
	wallBox := defs["device-types/Generic/wall-box-1-utp-plug.yaml"]
	if !model.Equal(wallBox.Object.Fields["u_height"], model.NewDecimal(0, 0)) ||
		!model.Equal(wallBox.Metadata["is_powered"], model.Boolean(false)) ||
		wallBox.Unmapped["port-mappings"] != 1 {
		t.Errorf("wall box = %+v, want u_height 0, is_powered false as metadata and one port mapping", wallBox)
	}
	if _, ok := wallBox.Object.Fields["is_powered"]; ok {
		t.Error("is_powered is a field, not metadata")
	}
	// Three spellings, three manufacturers; the slugs NetBox's UI makes.
	for name, slug := range map[string]string{
		"Netgear": "netgear", "NETGEAR": "netgear", "GL.iNet": "gl-inet", "Rohde & Schwarz": "rohde-schwarz",
	} {
		m, ok := defs["dcim.manufacturer:"+name]
		if !ok {
			t.Errorf("no manufacturer %q", name)
			continue
		}
		if got := m.Object.Fields["slug"]; !model.Equal(got, model.String(slug)) || !cmp.Equal(m.Derived, []string{"slug"}) {
			t.Errorf("manufacturer %q has slug %v, derived %v; want %q, derived", name, got, m.Derived, slug)
		}
	}
	// Testing/Test-1000.yaml names Generic, whatever its directory.
	if got := defs["dcim.manufacturer:Generic"].DerivedFrom; !cmp.Equal(got, []string{
		"device-types/Generic/shelf-2he.yaml", "device-types/Generic/wall-box-1-utp-plug.yaml", "device-types/Testing/Test-1000.yaml",
	}) {
		t.Errorf("Generic is derived from %v", got)
	}
}

// librarySchemas returns the library's schemas, copied from it.
func librarySchemas(t testing.TB) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	entries, err := os.ReadDir("testdata/library/schema")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(path.Join("testdata/library/schema", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		fsys["schema/"+e.Name()] = &fstest.MapFile{Data: data}
	}
	return fsys
}

// withFiles returns the library's schemas and the device types given, by
// path.
func withFiles(t *testing.T, files map[string]string) fstest.MapFS {
	t.Helper()
	fsys := librarySchemas(t)
	for p, content := range files {
		fsys[p] = &fstest.MapFile{Data: []byte(content)}
	}
	return fsys
}

const valid = "manufacturer: Acme\nmodel: Box 1\nslug: acme-box-1\nu_height: 1\nis_full_depth: true\n"

// TestReadProblems checks what is wrong with files written by hand to break
// one rule each.
func TestReadProblems(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		file string
		want []format.Problem // without severity and path when they're an error in the file
	}{
		{"valid", valid, nil},
		{"not YAML", "manufacturer: [\n", []format.Problem{{Line: 1, Reason: "isn't valid YAML: did not find expected node content"}}},
		{"a duplicate key", valid + "model: Box 2\n", []format.Problem{{Line: 6, Column: 1, Field: "model", Reason: "the key is given twice, on lines 2 and 6"}}},
		{"an alias", "manufacturer: &m Acme\nmodel: *m\n", []format.Problem{{Line: 1, Column: 15, Field: "manufacturer", Reason: "YAML anchors (&) aren't supported"}}},
		{"YAML 1.1's booleans", strings.Replace(valid, "Box 1", "yes", 1), []format.Problem{
			{Line: 2, Column: 8, Field: "model", Reason: "is true or false, but the schema wants text"},
		}},
		{"YAML 1.1's octal", strings.Replace(valid, "Box 1", "010", 1), []format.Problem{
			{Line: 2, Column: 8, Field: "model", Reason: "is a whole number, but the schema wants text"},
		}},
		{"the schema's rules", valid + "airflow: sideways\nweight: 1.001\nextra: 1\n", []format.Problem{
			// The schema requires a weight_unit with a weight, of the
			// whole file.
			{Line: 1, Column: 1, Reason: `needs "weight_unit", which the schema requires`},
			{Line: 6, Column: 10, Field: "airflow", Reason: `"sideways" isn't one of the 10 values the schema allows`},
			{Line: 7, Column: 9, Field: "weight", Reason: "1.001 isn't a multiple of 0.01, as the schema requires"},
			{Line: 8, Column: 1, Field: "extra", Reason: "isn't a key the schema allows here"},
		}},
		{"what NetBox refuses but the schema doesn't", strings.Replace(valid, "u_height: 1", "u_height: 1000", 1), []format.Problem{
			{Line: 4, Column: 11, Field: "u_height", Reason: "1000 has more than 3 digits before the decimal point"},
		}},
		{"a manufacturer with white space", strings.Replace(valid, "Acme", `" Acme"`, 1), []format.Problem{
			{Line: 1, Column: 15, Field: "manufacturer", Reason: `" Acme" has white space at an end: NetBox trims a manufacturer's name, but its import looks for the name as the file gives it, and wouldn't find it`},
		}},
		{"a manufacturer with a NUL", strings.Replace(valid, "Acme", `"Ac\0me"`, 1), []format.Problem{
			{Line: 1, Column: 15, Field: "manufacturer", Reason: `"Ac\x00me" holds a NUL character, which NetBox refuses`},
		}},
		{"a date", strings.Replace(valid, "Box 1", "2024-01-01", 1), []format.Problem{
			{Line: 2, Column: 8, Field: "model", Reason: `"2024-01-01" reads as a date or time, which the format doesn't take: quote it if it is text`},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			const p = "device-types/Acme/box.yaml"
			snap := sourcetest.Snapshot(t, "lib", withFiles(t, map[string]string{p: tt.file}))
			res, err := Format{}.Read(t.Context(), snap)
			if err != nil {
				t.Fatal(err)
			}
			for i := range tt.want {
				tt.want[i].Severity, tt.want[i].Path = format.Error, p
			}
			if diff := cmp.Diff(tt.want, res.Problems); diff != "" {
				t.Errorf("problems (-want +got):\n%s", diff)
			}
			wantDefs := 2 // the device type and its manufacturer
			if tt.want != nil {
				wantDefs = 0
			}
			if len(res.Definitions) != wantDefs || res.Files != 1 || res.Invalid != 1-wantDefs/2 {
				t.Errorf("%d definitions, %d files, %d invalid; want %d, 1 and %d", len(res.Definitions), res.Files, res.Invalid, wantDefs, 1-wantDefs/2)
			}
		})
	}
}

// TestReadWithAMoreLenientSchema checks what the adapter does itself, when
// the schema, which comes from the source, lets through what it shouldn't.
func TestReadWithAMoreLenientSchema(t *testing.T) {
	t.Parallel()
	lenient := `{"$id": "urn:devicetype-library:device-type", "type": "object"}`
	tests := []struct {
		name string
		file string
		want []format.Problem
	}{
		{"a choice NetBox doesn't ship with, which an administrator can add", valid + "airflow: sideways\n", []format.Problem{
			{Severity: format.Warning, Line: 6, Column: 10, Field: "airflow", Reason: `"sideways" is not one of the choices NetBox ships with, though an administrator can add choices through FIELD_CHOICES["dcim.Device.airflow"]`},
		}},
		{"a choice NetBox doesn't have", valid + "subdevice_role: middle\n", []format.Problem{
			{Severity: format.Error, Line: 6, Column: 17, Field: "subdevice_role", Reason: `"middle" isn't one of its choices`},
		}},
		{"a key the adapter doesn't know", valid + "cooling_method: air\n", []format.Problem{
			{Severity: format.Warning, Line: 6, Column: 1, Field: "cooling_method", Reason: "isn't a key TrueUp reads from devicetype-library yet, so it is ignored"},
		}},
		{"a missing key NetBox needs", "manufacturer: Acme\nmodel: Box\nis_full_depth: true\n", []format.Problem{
			{Severity: format.Error, Line: 1, Column: 1, Reason: "has no slug, which NetBox needs to create a device type"},
			{Severity: format.Error, Line: 1, Column: 1, Reason: "has no u_height, which NetBox needs to create a device type"},
		}},
		{"a number for text", strings.Replace(valid, "Box 1", "12", 1), []format.Problem{
			{Severity: format.Error, Line: 2, Column: 8, Field: "model", Reason: "is a whole number, but NetBox's model is text"},
		}},
		{"a list for a manufacturer", strings.Replace(valid, "Acme", "[Acme]", 1), []format.Problem{
			{Severity: format.Error, Line: 1, Column: 15, Field: "manufacturer", Reason: "is a list, but a manufacturer is given by its name, as text"},
		}},
		{"a number for a manufacturer", strings.Replace(valid, "Acme", "3", 1), []format.Problem{
			{Severity: format.Error, Line: 1, Column: 15, Field: "manufacturer", Reason: "is a whole number, but a manufacturer is given by its name, as text"},
		}},
		{"components that aren't lists", valid + "interfaces: {a: 1}\nis_powered: 1\n", []format.Problem{
			{Severity: format.Error, Line: 6, Column: 13, Field: "interfaces", Reason: "isn't a list, which devicetype-library's interfaces is"},
			{Severity: format.Error, Line: 7, Column: 13, Field: "is_powered", Reason: "isn't true or false, which devicetype-library's is_powered is"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			const p = "device-types/Acme/box.yaml"
			fsys := withFiles(t, map[string]string{p: tt.file})
			fsys["schema/devicetype.json"] = &fstest.MapFile{Data: []byte(lenient)}
			res, err := Format{}.Read(t.Context(), sourcetest.Snapshot(t, "lib", fsys))
			if err != nil {
				t.Fatal(err)
			}
			for i := range tt.want {
				tt.want[i].Path = p
			}
			if diff := cmp.Diff(tt.want, res.Problems); diff != "" {
				t.Errorf("problems (-want +got):\n%s", diff)
			}
		})
	}
}

// TestReadAcrossFiles checks what the adapter reports of files together:
// manufacturers whose slugs would be the same, one it can't make a slug
// for, device types NetBox couldn't hold together, and files where the
// library keeps no device types.
func TestReadAcrossFiles(t *testing.T) {
	t.Parallel()
	file := func(manufacturer, model, slug string) string {
		return "manufacturer: " + manufacturer + "\nmodel: " + model + "\nslug: " + slug + "\nu_height: 1\nis_full_depth: true\n"
	}
	fsys := withFiles(t, map[string]string{
		"device-types/Acme/a.yaml":        file("Acme", "A", "acme-a"),
		"device-types/Acme/b.yml":         file("ACME", "B", "acme-b"),
		"device-types/Acme/c.yaml":        file("Acme", "A", "acme-c"),
		"device-types/Acme/d.yaml":        file("Acme", "D", "acme-a"),
		"device-types/Acme/e.yaml":        file("Acme!", "E", "acme-e"),
		"device-types/Nihon/f.yaml":       file("'\xe6\x97\xa5\xe6\x9c\xac'", "F", "nihon-f"),
		"device-types/Acme/README.md":     "# Acme\n",
		"device-types/Acme/.hidden.yaml":  file("Acme", "H", "acme-h"),
		"device-types/Acme/deep/g.yaml":   file("Acme", "G", "acme-g"),
		"device-types/top.yaml":           file("Acme", "T", "acme-t"),
		"device-types/Acme/old.yaml.bak":  file("Acme", "O", "acme-o"),
		"module-types/Acme/m.yaml":        "not read: module types come later\n",
		"elevation-images/Acme/a.png":     "not read\n",
		"device-types/Acme/f.yaml.yaml.x": "x",
	})
	fsys["device-types/Acme/link.yaml"] = &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("a.yaml")}
	res, err := Format{}.Read(t.Context(), sourcetest.Snapshot(t, "lib", fsys))
	if err != nil {
		t.Fatal(err)
	}
	notRead := "isn't read: devicetype-library keeps each device type in device-types/<manufacturer>/, in a file named .yaml or .yml"
	want := []format.Problem{
		{Severity: format.Warning, Reason: `NetBox's UI can't make a slug from the manufacturer name "日本" (can't be blank), so TrueUp can't create the manufacturer: it can only match one NetBox has`},
		{Severity: format.Warning, Reason: `manufacturers "ACME", "Acme" and "Acme!" would all get the slug "acme", which NetBox gives to one manufacturer only: create all but one of them in NetBox with another slug, or spell them alike in the source`},
		{Severity: format.Warning, Path: "device-types/Acme/.hidden.yaml", Reason: notRead},
		{Severity: format.Warning, Path: "device-types/Acme/README.md", Reason: notRead},
		{Severity: format.Warning, Path: "device-types/Acme/c.yaml", Field: "model", Reason: "device-types/Acme/a.yaml has the same manufacturer and model as this one, which NetBox keeps unique"},
		{Severity: format.Warning, Path: "device-types/Acme/d.yaml", Field: "slug", Reason: "device-types/Acme/a.yaml has the same manufacturer and slug as this one, which NetBox keeps unique"},
		{Severity: format.Warning, Path: "device-types/Acme/deep/g.yaml", Reason: notRead},
		{Severity: format.Warning, Path: "device-types/Acme/f.yaml.yaml.x", Reason: notRead},
		{Severity: format.Error, Path: "device-types/Acme/link.yaml", Reason: "wasn't read: it is a symbolic link, which TrueUp never follows"},
		{Severity: format.Warning, Path: "device-types/Acme/old.yaml.bak", Reason: notRead},
		{Severity: format.Warning, Path: "device-types/top.yaml", Reason: notRead},
	}
	if diff := cmp.Diff(want, res.Problems); diff != "" {
		t.Errorf("problems (-want +got):\n%s", diff)
	}
	var names []string
	for _, d := range res.Definitions {
		if d.Object.Type == "dcim.manufacturer" {
			name, _ := d.Identity["name"].(model.String)
			names = append(names, string(name))
			if _, ok := d.Object.Fields["slug"]; !ok && d.Derived != nil {
				t.Errorf("manufacturer %v has no slug, but derived %v", d.Identity, d.Derived)
			}
		}
	}
	if diff := cmp.Diff([]string{"ACME", "Acme", "Acme!", "\xe6\x97\xa5\xe6\x9c\xac"}, names); diff != "" {
		t.Errorf("manufacturers (-want +got):\n%s", diff)
	}
	// Both duplicates are kept, for grouping to decide.
	if len(res.Definitions) != 4+6 || res.Files != 7 || res.Invalid != 1 {
		t.Errorf("%d definitions, %d files, %d invalid; want 10, 7 and 1", len(res.Definitions), res.Files, res.Invalid)
	}
}

// TestReadNeedsSchemas checks that a source without the library's schemas,
// or with schemas that don't compile, reads nothing.
func TestReadNeedsSchemas(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(fstest.MapFS)
		limits config.SourceLimits
		want   string
	}{
		{"no schemas", func(fsys fstest.MapFS) {
			for p := range fsys {
				if strings.HasPrefix(p, "schema/") {
					delete(fsys, p)
				}
			}
		}, sourcetest.Limits, "the source has no schema/devicetype.json, devicetype-library's schema for device types"},
		{"no device type schema", func(fsys fstest.MapFS) { delete(fsys, "schema/devicetype.json") }, sourcetest.Limits, "has no schema/devicetype.json"},
		{"a schema that refers to one that is gone", func(fsys fstest.MapFS) { delete(fsys, "schema/components.json") }, sourcetest.Limits, "schema/devicetype.json doesn't compile"},
		{"a schema that isn't JSON", func(fsys fstest.MapFS) { fsys["schema/racktype.json"] = &fstest.MapFile{Data: []byte("{")} }, sourcetest.Limits, "schema/racktype.json isn't JSON"},
		{"a schema that refers outside", func(fsys fstest.MapFS) {
			fsys["schema/devicetype.json"] = &fstest.MapFile{Data: []byte(`{"$ref": "https://example.com/devicetype.json"}`)}
		}, sourcetest.Limits, "loads no schema from outside the source"},
		{"a schema too large to read", func(fstest.MapFS) {}, config.SourceLimits{MaxFileSize: 4096, MaxFiles: 100, FetchTimeout: 1, MaxFetchSize: 1}, "schema/devicetype.json, devicetype-library's schema for device types, is larger than the source's max_file_size"},
		{"a schema it refers to too large to read", func(fstest.MapFS) {}, config.SourceLimits{MaxFileSize: 5000, MaxFiles: 100, FetchTimeout: 1, MaxFetchSize: 1}, "(schema/components.json, schema/generated_schema.json and schema/moduletype.json weren't read: they are larger than the source's max_file_size)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fsys := withFiles(t, map[string]string{"device-types/Acme/box.yaml": valid})
			tt.change(fsys)
			snap := sourcetest.SnapshotOf(t, sourcetest.Kind{FS: fsys, Version: "v1"}, config.Source{Name: "lib", Kind: "test", Limits: tt.limits})
			res, err := Format{}.Read(t.Context(), snap)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Read = %+v, %v; want an error holding %q", res, err, tt.want)
			}
		})
	}
}

// TestReadStopsWhenCancelled checks that Read gives up, reading nothing,
// when its context ends.
func TestReadStopsWhenCancelled(t *testing.T) {
	t.Parallel()
	snap := sourcetest.Snapshot(t, "lib", os.DirFS("testdata/library"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if res, err := (Format{}).Read(ctx, snap); !errors.Is(err, context.Canceled) {
		t.Errorf("Read with a cancelled context = %+v, %v; want context.Canceled", res, err)
	}
}

// TestReadsAndNeeds checks the patterns the format gives commands, against
// the copied library.
func TestReadsAndNeeds(t *testing.T) {
	t.Parallel()
	f := Format{}
	spec, err := source.NewSpec(config.Source{Name: "lib", Kind: "test", Include: append([]string{"device-types/Huawei/**"}, f.Needs()...), Limits: sourcetest.Limits})
	if err != nil {
		t.Fatal(err)
	}
	spec.Only = sourcetest.Patterns(t, f.Reads()...)
	snap, err := source.Open(t.Context(), sourcetest.Kind{FS: os.DirFS("testdata/library"), Version: "v1"}, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Close() }()
	var paths []string
	for _, file := range snap.Files {
		paths = append(paths, file.Path)
	}
	want := []string{
		"device-types/Huawei/NE8000-X8.yaml", "schema/components.json", "schema/devicetype.json",
		"schema/generated_schema.json", "schema/moduletype.json", "schema/racktype.json", "schema/reusable.json",
	}
	if diff := cmp.Diff(want, paths); diff != "" {
		t.Errorf("files (-want +got):\n%s", diff)
	}
	res, err := f.Read(t.Context(), snap)
	if err != nil || res.Files != 1 || len(res.Definitions) != 2 {
		t.Errorf("Read = %+v, %v; want one device type and its manufacturer", res, err)
	}
	if f.Name() != config.FormatDeviceTypeLibrary {
		t.Errorf("Name() = %q", f.Name())
	}
	// The golden file's decimals are exact, never floats.
	golden, err := os.ReadFile("testdata/library.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(golden, []byte(`"weight": 19.18,`)) {
		t.Error("the golden file doesn't give HPE ClearPass-C2000's weight as 19.18")
	}
}
