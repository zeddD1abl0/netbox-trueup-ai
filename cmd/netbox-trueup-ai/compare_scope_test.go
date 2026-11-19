package main

import (
	"database/sql"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/dcim"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/engine"
)

// recordedKeys returns the keys of the comparisons recorded, sorted: a
// file's path, or a derived manufacturer's identity.
func recordedKeys(t *testing.T, dataDir string) []string {
	t.Helper()
	var keys []string
	for k := range recordedComparisons(t, dataDir) {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// TestCompareScopes checks what each scope compares and records: by
// default what NetBox has, a selection what it names, absent included, and
// --all everything.
func TestCompareScopes(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, dataDir, _, configure := compareEnv(t, nb)
	compareJSONOf := func(t *testing.T, wantCode int, args ...string) compareResult {
		t.Helper()
		r := runCLIApp(t, nil, configure, env, append([]string{"compare", "-o", "json"}, args...)...)
		if r.code != wantCode {
			t.Fatalf("compare %v: exit code %d, want %d\nstderr: %s", args, r.code, wantCode, r.stderr)
		}
		return decodeCompare(t, r.stdout)
	}
	everything := []string{
		"device-types/Huawei/NE8000-X8.yaml", "device-types/Netgear/GS105.yaml", "device-types/Netgear/GS510TLP.yml",
		`{"name":"Huawei"}`, `{"name":"NETGEAR"}`, `{"name":"Netgear"}`,
	}
	whatNetBoxHas := slices.DeleteFunc(slices.Clone(everything), func(k string) bool { return k == "device-types/Netgear/GS510TLP.yml" })

	// --all compares and records every definition.
	got := compareJSONOf(t, exitOK, "--all")
	if got.Scope != "all" || len(got.Objects) != 6 {
		t.Errorf("compare --all: scope %q, %d objects", got.Scope, len(got.Objects))
	}
	if diff := cmp.Diff(everything, recordedKeys(t, dataDir)); diff != "" {
		t.Errorf("recorded after --all (-want +got):\n%s", diff)
	}

	// By default, the definition NetBox hasn't got is counted, not
	// compared, and its comparison is removed.
	got = compareJSONOf(t, exitOK)
	src := got.Sources[0]
	if got.Scope != "netbox" || src.Definitions != 6 || src.Compared != 5 || src.Absent != 0 {
		t.Errorf("compare: scope %q, source %+v", got.Scope, src)
	}
	if diff := cmp.Diff([]string{"candidate Huawei 0", "conflict NETGEAR 0", "candidate Netgear 0", "candidate NE8000-X8 2", "candidate GS105 0"}, outcomes(got)); diff != "" {
		t.Errorf("compare's objects (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(whatNetBoxHas, recordedKeys(t, dataDir)); diff != "" {
		t.Errorf("recorded by default (-want +got):\n%s", diff)
	}
	before := recordedComparisons(t, dataDir)

	// A model names device types, absent included, which are compared with
	// their manufacturers; the others' comparisons are kept.
	got = compareJSONOf(t, exitOK, "--model", "GS510TLP")
	if diff := cmp.Diff([]string{"conflict NETGEAR 0", "absent GS510TLP 0"}, outcomes(got)); diff != "" || got.Scope != "selection" {
		t.Errorf("compare --model GS510TLP: scope %q, objects (-want +got):\n%s", got.Scope, diff)
	}
	if u := got.Objects[1].Unresolved; len(u) != 1 || u[0].Outcome == nil || *u[0].Outcome != "conflict" {
		t.Errorf("GS510TLP's unresolved references = %+v, want its manufacturer's conflict", u)
	}
	if diff := cmp.Diff(everything, recordedKeys(t, dataDir)); diff != "" {
		t.Errorf("recorded after --model (-want +got):\n%s", diff)
	}
	after := recordedComparisons(t, dataDir)
	for _, k := range []string{`{"name":"Huawei"}`, "device-types/Netgear/GS105.yaml"} {
		if !after[k].ComparedAt.Equal(before[k].ComparedAt) {
			t.Errorf("the comparison of %s, which --model didn't name, was recorded again", k)
		}
	}
	if after[`{"name":"NETGEAR"}`].ComparedAt.Equal(before[`{"name":"NETGEAR"}`].ComparedAt) {
		t.Error("the comparison of GS510TLP's manufacturer wasn't recorded again")
	}

	// Names count as NetBox would store them, case included; a manufacturer
	// narrows the models.
	for _, tt := range []struct {
		name string
		args []string
		want []string
	}{
		{"white space at the ends", []string{"--model", " GS105\t"}, []string{"candidate Netgear 0", "candidate GS105 0"}},
		{"a manufacturer and its model", []string{"--manufacturer", "Netgear", "--model", "GS105"}, []string{"candidate Netgear 0", "candidate GS105 0"}},
		{"a manufacturer alone", []string{"--manufacturer", "Huawei"}, []string{"candidate Huawei 0", "candidate NE8000-X8 2"}},
		{"two models", []string{"--model", "GS105", "--model", "NE8000-X8"}, []string{"candidate Huawei 0", "candidate Netgear 0", "candidate NE8000-X8 2", "candidate GS105 0"}},
	} {
		got := compareJSONOf(t, exitOK, tt.args...)
		if diff := cmp.Diff(tt.want, outcomes(got)); diff != "" {
			t.Errorf("%s: compare %v (-want +got):\n%s", tt.name, tt.args, diff)
		}
	}

	// A name nothing defines exits with 4, saying which, having compared
	// the rest.
	for _, tt := range []struct {
		name string
		args []string
		want []string
		warn string
	}{
		{"a model nothing defines", []string{"--model", "gs105"}, nil, `no source read defines a device type with the model "gs105"`},
		{
			"a model of another manufacturer",
			[]string{"--manufacturer", "Netgear", "--model", "GS510TLP"},
			[]string{"candidate Netgear 0"},
			`no source read defines a device type with the model "GS510TLP" of the manufacturer "Netgear"`,
		},
		{
			"a model of none of the manufacturers",
			[]string{"--manufacturer", "Netgear", "--manufacturer", "Huawei", "--model", "GS510TLP"},
			[]string{"candidate Huawei 0", "candidate Netgear 0"},
			`no source read defines a device type with the model "GS510TLP" of the manufacturer "Huawei" or "Netgear"`,
		},
		{"a manufacturer nothing defines", []string{"--manufacturer", "netgear"}, nil, `no source read defines the manufacturer "netgear"`},
	} {
		got := compareJSONOf(t, exitCompareProblems, tt.args...)
		if diff := cmp.Diff(tt.want, outcomes(got)); diff != "" || got.OK || len(got.Warnings) != 1 || got.Warnings[0].Message != tt.warn {
			t.Errorf("%s: compare %v: ok %v, warnings %+v, want %q; objects (-want +got):\n%s", tt.name, tt.args, got.OK, got.Warnings, tt.warn, diff)
		}
	}

	// Only the default scope counts what NetBox hasn't got.
	for _, args := range [][]string{{"--all"}, {"--model", "GS105"}} {
		r := runCLIApp(t, nil, configure, env, append([]string{"compare"}, args...)...)
		if r.code != exitOK || strings.Contains(r.stdout, "NetBox hasn't got") {
			t.Errorf("compare %v: exit code %d, and says what NetBox hasn't got:\n%s", args, r.code, r.stdout)
		}
	}

	// A name is quoted whole, however long.
	long := strings.Repeat("Hewlett Packard Enterprise ", 3)
	got = compareJSONOf(t, exitCompareProblems, "--manufacturer", long, "--model", "X")
	if want := strconv.Quote(strings.TrimSpace(long)); len(got.Warnings) != 2 || !strings.HasSuffix(got.Warnings[1].Message, want) {
		t.Errorf("compare --manufacturer %q --model X warns %+v", long, got.Warnings)
	}

	// A name nothing defines is logged with its flag's key.
	r := runCLIApp(t, nil, configure, env, "compare", "--model", "gs105", "--manufacturer", "Netgear")
	if !strings.Contains(r.stderr, `msg="no source read defines a device type with the model" model=gs105 manufacturers=[Netgear]`) {
		t.Errorf("compare --model gs105 logged:\n%s", r.stderr)
	}

	// --all with a selection is a mistake on the command line.
	for _, args := range [][]string{{"--all", "--model", "GS105"}, {"--all", "--manufacturer", "Netgear"}} {
		r := runCLIApp(t, nil, configure, env, append([]string{"compare"}, args...)...)
		if r.code != exitUsage || !strings.Contains(r.stderr, "--all compares every definition") {
			t.Errorf("compare %v: exit code %d, stderr %q", args, r.code, r.stderr)
		}
	}

	// So is a name NetBox would store as nothing.
	for _, args := range [][]string{{"--model", " "}, {"--manufacturer", "\t", "--model", "GS105"}} {
		r := runCLIApp(t, nil, configure, env, append([]string{"compare"}, args...)...)
		if r.code != exitUsage || !strings.Contains(r.stderr, "needs a name") {
			t.Errorf("compare %q: exit code %d, stderr %q", args, r.code, r.stderr)
		}
	}
}

// TestCompareAModelInTwoManufacturers checks that --model compares every
// device type with the model, whatever its manufacturer.
func TestCompareAModelInTwoManufacturers(t *testing.T) {
	t.Parallel()
	gs105 := libraryFiles(t, nil)["device-types/Netgear/GS105.yaml"]
	acme := strings.Replace(strings.Replace(gs105, "manufacturer: Netgear", "manufacturer: Acme", 1), "slug: netgear-gs105", "slug: acme-gs105", 1)
	if acme == gs105 || !strings.Contains(acme, "acme-gs105") {
		t.Fatalf("GS105's file changed: %s", gs105)
	}
	nb := libraryNetBox(t)
	repo, pool, _ := libraryRepo(t, map[string]string{"device-types/Acme/GS105.yaml": acme})
	env, _ := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repo+`","format":"devicetype-library"}]`)
	fetched(t, pool, env)
	env = append(env, checkEnv(t, nb.srv, checkToken)...)
	r := runCLIApp(t, nil, trust(pool), env, "compare", "-o", "json", "--model", "GS105")
	if r.code != exitOK {
		t.Fatalf("exit code %d\nstderr: %s", r.code, r.stderr)
	}
	want := []string{"absent Acme 0", "candidate Netgear 0", "absent GS105 0", "candidate GS105 0"}
	if diff := cmp.Diff(want, outcomes(decodeCompare(t, r.stdout))); diff != "" {
		t.Errorf("compare --model GS105 (-want +got):\n%s", diff)
	}
}

// TestCompareAnEmptyNetBox checks that, by default, nothing is compared
// with a NetBox that has nothing, and that --exit-code then passes.
func TestCompareAnEmptyNetBox(t *testing.T) {
	t.Parallel()
	nb := newCompareNetBox(t, nil, nil)
	env, dataDir, commit, configure := compareEnv(t, nb)
	r := runCLIApp(t, nil, configure, env, "compare", "--exit-code")
	if r.code != exitOK || r.stderr != "" {
		t.Fatalf("exit code %d\nstderr: %s", r.code, r.stderr)
	}
	want := `NetBox:  ` + nb.srv.URL + `, release 4.7.2

SOURCE  REF     VERSION                                   COMPARED  IN STEP  DIFFERING  ABSENT  CONFLICTS  INVALID  WARNINGS
lib     master  ` + commit + `  0         0        0          0       0          0        1

6 definitions NetBox hasn't got weren't compared: --all compares every definition, and --manufacturer or --model those named.

Warnings:
  lib: manufacturers "NETGEAR" and "Netgear" would both get the slug "netgear", which NetBox gives to one manufacturer only: create one of them in NetBox with another slug, or spell them alike in the source
`
	if diff := cmp.Diff(want, r.stdout); diff != "" {
		t.Errorf("compare (-want +got):\n%s", diff)
	}
	if keys := recordedKeys(t, dataDir); len(keys) != 0 {
		t.Errorf("recorded %v", keys)
	}
	// Only the manufacturers were read: no device type, no template.
	if got := nb.listed("/api/dcim/device-types/"); len(got) != 0 {
		t.Errorf("device types listed: %v", got)
	}
}

// TestCompareSourceLoadedAgainMeanwhile checks that a source loaded again
// between reading its unique values and reading its definitions whole is
// reported, and nothing of it recorded.
func TestCompareSourceLoadedAgainMeanwhile(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, dataDir, _, configure := compareEnv(t, nb)
	loaded := storedDefinitions(t, dataDir)
	var once sync.Once
	nb.onManufacturers = func() {
		once.Do(func() {
			recordLoadIn(t, dataDir, store.Load{
				Source: "lib", Version: loaded.Loaded.Version, Reader: loaded.Loaded.Reader, Settings: loaded.Loaded.Settings, StartedAt: time.Now(),
				Files: loaded.Files, InvalidFiles: loaded.InvalidFiles, Definitions: loaded.Definitions,
			})
		})
	}
	r := runCLIApp(t, nil, configure, env, "compare", "-o", "json")
	if r.code != exitCompareProblems {
		t.Fatalf("exit code %d, want %d\nstderr: %s", r.code, exitCompareProblems, r.stderr)
	}
	got := decodeCompare(t, r.stdout)
	// As a source that couldn't be read at all shows: no version, and no
	// counts of what it read.
	if src := got.Sources[0]; src.OK || src.Error == nil || !strings.Contains(*src.Error, "loaded again while it was compared") || len(got.Objects) != 0 ||
		src.Version != nil || src.Definitions != 0 || src.Compared != 0 {
		t.Errorf("the source = %+v, with %d objects", src, len(got.Objects))
	}
	if keys := recordedKeys(t, dataDir); len(keys) != 0 {
		t.Errorf("recorded %v", keys)
	}
}

// TestCompareLoadsUniqueValuesThatDontReadBack checks that a database
// holding unique values this release can't read back, as one that a
// release before them migrated does, is loaded again first.
func TestCompareLoadsUniqueValuesThatDontReadBack(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, dataDir, _, configure := compareEnv(t, nb)
	loaded := storedDefinitions(t, dataDir)
	l := store.Load{
		Source: "lib", Version: loaded.Loaded.Version, Reader: loaded.Loaded.Reader, Settings: loaded.Loaded.Settings, StartedAt: time.Now(),
		Files: loaded.Files, InvalidFiles: loaded.InvalidFiles, Definitions: loaded.Definitions,
	}
	for i := range l.Definitions {
		l.Definitions[i].UniqueValues = []byte(`{}`)
	}
	recordLoadIn(t, dataDir, l)
	r := runCLIApp(t, nil, configure, env, "compare", "-o", "json")
	if r.code != exitOK || !strings.Contains(r.stderr, "can't read back") {
		t.Fatalf("exit code %d\nstderr: %s", r.code, r.stderr)
	}
	if got := decodeCompare(t, r.stdout); len(got.Objects) != 5 {
		t.Errorf("compare compared %d definitions, want 5", len(got.Objects))
	}
	for _, d := range storedDefinitions(t, dataDir).Definitions {
		if string(d.UniqueValues) == `{}` {
			t.Errorf("%s's unique values weren't loaded again", d.DefinitionKey)
		}
	}
}

// TestCompareLoadsWhatAMigrationLeftWithoutUniqueValues checks that
// definitions without unique values, as migration 00004 leaves them, are
// loaded again first, with a note rather than a warning.
func TestCompareLoadsWhatAMigrationLeftWithoutUniqueValues(t *testing.T) {
	t.Parallel()
	nb := libraryNetBox(t)
	env, dataDir, _, configure := compareEnv(t, nb)
	db, err := sql.Open("sqlite", filepath.Join(dataDir, engine.DefaultFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "UPDATE definitions SET unique_values = ''"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	r := runCLIApp(t, nil, configure, env, "compare", "-o", "json")
	note := `msg="the database holds no unique values of the source's definitions, as a release before them left it, so it loads the source again"`
	if r.code != exitOK || !strings.Contains(r.stderr, note) || strings.Contains(r.stderr, "level=WARN") {
		t.Fatalf("exit code %d\nstderr: %s", r.code, r.stderr)
	}
	if got := decodeCompare(t, r.stdout); len(got.Objects) != 5 {
		t.Errorf("compare compared %d definitions, want 5", len(got.Objects))
	}
	for _, d := range storedDefinitions(t, dataDir).Definitions {
		if len(d.UniqueValues) == 0 {
			t.Errorf("%s's unique values weren't loaded again", d.DefinitionKey)
		}
	}
}

// TestCompareSelectionAcrossSources checks that --model compares the
// manufacturers of the device types it names from every source that
// defines them, and that a source a selection compares nothing of records
// nothing, so that its being fetched meanwhile isn't a failure.
func TestCompareSelectionAcrossSources(t *testing.T) {
	t.Parallel()
	gs105 := libraryFiles(t, nil)["device-types/Netgear/GS105.yaml"]
	gs999 := strings.Replace(strings.Replace(gs105, "model: GS105", "model: GS999", 1), "slug: netgear-gs105", "slug: netgear-gs999", 1)
	if !strings.Contains(gs999, "model: GS999") || !strings.Contains(gs999, "slug: netgear-gs999") {
		t.Fatalf("GS105's file changed: %s", gs105)
	}
	nb := libraryNetBox(t)
	// Two sources from one repository: the library, and GS999 alone.
	repo, pool, _ := libraryRepo(t, map[string]string{"device-types/Netgear/GS999.yaml": gs999})
	env, dataDir := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repo+`","format":"devicetype-library","exclude":["device-types/Netgear/GS999.yaml"]},`+
		`{"name":"extra","kind":"git","url":"`+repo+`","format":"devicetype-library","include":["device-types/Netgear/GS999.yaml"]}]`)
	fetched(t, pool, env)
	env = append(env, checkEnv(t, nb.srv, checkToken)...)
	configure := trust(pool)

	r := runCLIApp(t, nil, configure, env, "compare", "-o", "json", "--model", "GS999")
	if r.code != exitOK {
		t.Fatalf("compare --model GS999: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	if diff := cmp.Diff([]string{"candidate Netgear 0", "candidate Netgear 0", "absent GS999 0"}, outcomes(decodeCompare(t, r.stdout))); diff != "" {
		t.Errorf("compare --model GS999 (-want +got):\n%s", diff)
	}

	var once sync.Once
	nb.onManufacturers = func() {
		once.Do(func() {
			if err := recordFetchErr(t, dataDir, "extra", time.Now()); err != nil {
				t.Error(err)
			}
		})
	}
	r = runCLIApp(t, nil, configure, env, "compare", "-o", "json", "--model", "NE8000-X8")
	if r.code != exitOK {
		t.Fatalf("compare --model NE8000-X8, with the other source fetched meanwhile: exit code %d\nstderr: %s\nstdout: %s", r.code, r.stderr, r.stdout)
	}
	got := decodeCompare(t, r.stdout)
	if src := got.Sources[1]; src.Name != "extra" || !src.OK || src.Compared != 0 {
		t.Errorf("the source compared nothing of = %+v", src)
	}
}

// TestNormalizer checks that names are normalized as the specs say, and
// that a spec without the field fails rather than match otherwise.
func TestNormalizer(t *testing.T) {
	t.Parallel()
	name, err := normalizer(dcim.Specs(), "dcim.manufacturer", "name")
	if err != nil {
		t.Fatal(err)
	}
	if got := name(" Dell\t"); got != "Dell" {
		t.Errorf("normalized %q, want %q", got, "Dell")
	}
	for _, tt := range []struct{ typ, field string }{
		{"dcim.platform", "name"}, {"dcim.devicetype", "colour"}, {"dcim.devicetype", "u_height"},
	} {
		if _, err := normalizer(dcim.Specs(), tt.typ, tt.field); err == nil {
			t.Errorf("normalizer of %s's %s succeeded", tt.typ, tt.field)
		}
	}
}
