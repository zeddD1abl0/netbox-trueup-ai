package main

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/engine"
)

// libraryFiles returns files copied from devicetype-library, with the
// library's schemas, and the extra files given, by path.
func libraryFiles(t *testing.T, extra map[string]string) map[string]string {
	t.Helper()
	const lib = "../../internal/format/devicetypelib/testdata/library"
	files := map[string]string{}
	for _, name := range []string{
		"schema/components.json", "schema/devicetype.json", "schema/generated_schema.json",
		"schema/moduletype.json", "schema/racktype.json", "schema/reusable.json",
		"device-types/Huawei/NE8000-X8.yaml", "device-types/Netgear/GS105.yaml", "device-types/Netgear/GS510TLP.yml",
	} {
		data, err := os.ReadFile(filepath.Join(lib, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(data)
	}
	for name, content := range extra {
		files[name] = content
	}
	return files
}

// libraryRepo serves a repository of files from the library, and returns
// its URL, the certificates that trust it, and its commit.
func libraryRepo(t *testing.T, extra map[string]string) (repoURL string, pool *x509.CertPool, commit string) {
	t.Helper()
	return gitRepoWith(t, libraryFiles(t, extra))
}

// listResult decodes list's JSON output, with values as encoding/json
// reads them.
type listResult struct {
	OK      bool `json:"ok"`
	Sources []struct {
		Name         string  `json:"name"`
		Format       string  `json:"format"`
		Ref          *string `json:"ref"`
		Version      *string `json:"version"`
		OK           bool    `json:"ok"`
		Error        *string `json:"error"`
		Files        int     `json:"files"`
		InvalidFiles int     `json:"invalid_files"`
		Definitions  []struct {
			Type     string         `json:"type"`
			Identity map[string]any `json:"identity"`
			Path     *string        `json:"path"`
			Fields   map[string]any `json:"fields"`
		} `json:"definitions"`
		Problems []problemJSON `json:"problems"`
	} `json:"sources"`
}

func fetched(t *testing.T, pool *x509.CertPool, env []string) {
	t.Helper()
	if r := runCLIApp(t, nil, trust(pool), env, "source", "fetch"); r.code != exitOK {
		t.Fatalf("source fetch: exit code %d\nstderr: %s", r.code, r.stderr)
	}
}

func TestListText(t *testing.T) {
	t.Parallel()
	repoURL, pool, commit := libraryRepo(t, nil)
	env, _ := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library"}]`)
	fetched(t, pool, env)
	r := runCLIApp(t, nil, trust(pool), env, "list")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	want := `SOURCE  REF     VERSION                                   FILES  INVALID  WARNINGS  MANUFACTURERS  DEVICE TYPES
lib     master  ` + commit + `  3      0        1         3              3

SOURCE  TYPE          MANUFACTURER  MODEL      SLUG               FROM
lib     manufacturer  Huawei        -          huawei (derived)   1 file
lib     device type   Huawei        NE8000-X8  huawei-ne8000-x8   device-types/Huawei/NE8000-X8.yaml
lib     manufacturer  NETGEAR       -          netgear (derived)  1 file
lib     device type   NETGEAR       GS510TLP   netgear-gs510tlp   device-types/Netgear/GS510TLP.yml
lib     manufacturer  Netgear       -          netgear (derived)  1 file
lib     device type   Netgear       GS105      netgear-gs105      device-types/Netgear/GS105.yaml

Warnings:
  lib: manufacturers "NETGEAR" and "Netgear" would both get the slug "netgear", which NetBox gives to one manufacturer only: create one of them in NetBox with another slug, or spell them alike in the source
`
	if diff := cmp.Diff(want, r.stdout); diff != "" {
		t.Errorf("list (-want +got):\n%s", diff)
	}
}

// TestListTextKeepsWhatNoManufacturerHolds checks that a definition whose
// manufacturer the source doesn't define is still listed as text, after
// those whose manufacturer it does.
func TestListTextKeepsWhatNoManufacturerHolds(t *testing.T) {
	t.Parallel()
	deviceType := func(manufacturer, name string) format.Definition {
		return format.Definition{Object: model.Object{Type: "dcim.devicetype"}, Identity: map[string]model.Value{
			"manufacturer": model.Reference{Type: "dcim.manufacturer", Key: map[string]model.Value{"name": model.String(manufacturer)}},
			"model":        model.String(name),
		}}
	}
	got := definitionLines(listSourceJSON{Name: "lib", defs: []format.Definition{
		{Object: model.Object{Type: "dcim.manufacturer"}, Identity: map[string]model.Value{"name": model.String("Acme")}},
		deviceType("Acme", "Box 1"),
		deviceType("Zeta", "Box 2"),
	}})
	want := []string{
		"lib\tmanufacturer\tAcme\t-\t-\t-",
		"lib\tdevice type\tAcme\tBox 1\t-\t-",
		"lib\tdevice type\tZeta\tBox 2\t-\t-",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("definitionLines (-want +got):\n%s", diff)
	}
}

func TestListJSON(t *testing.T) {
	t.Parallel()
	repoURL, pool, commit := libraryRepo(t, map[string]string{
		"device-types/Acme/bad.yaml":   "manufacturer: Acme\nmodel: Box\nslug: acme-box\nu_height: 1\nis_full_depth: yes\nairflow: sideways\n",
		"device-types/Acme/README.md":  "# Acme\n",
		"elevation-images/Acme/x.png":  "image",
		"module-types/Acme/thing.yaml": "model: thing\n",
	})
	env, _ := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library"}]`)
	fetched(t, pool, env)
	r := runCLIApp(t, nil, trust(pool), env, "list", "-o", "json")
	if r.code != exitListProblems {
		t.Errorf("exit code = %d, want %d\nstderr: %s", r.code, exitListProblems, r.stderr)
	}
	if !strings.HasSuffix(r.stderr, "Error: 1 file is invalid\n") {
		t.Errorf("stderr = %q", r.stderr)
	}
	var got listResult
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("list -o json printed %q: %v", r.stdout, err)
	}
	if len(got.Sources) != 1 {
		t.Fatalf("list -o json gave %d sources", len(got.Sources))
	}
	s := got.Sources[0]
	if got.OK || s.OK || s.Error != nil || *s.Version != commit || *s.Ref != "master" || s.Files != 4 || s.InvalidFiles != 1 || len(s.Definitions) != 6 {
		t.Errorf("list -o json = %+v", got)
	}
	var problems []string
	for _, p := range s.Problems {
		problems = append(problems, p.Severity+" "+problemText(p))
	}
	wantProblems := []string{
		`warning manufacturers "NETGEAR" and "Netgear" would both get the slug "netgear", which NetBox gives to one manufacturer only: create one of them in NetBox with another slug, or spell them alike in the source`,
		"warning device-types/Acme/README.md: isn't read: devicetype-library keeps each device type in device-types/<manufacturer>/, in a file named .yaml or .yml",
		`error device-types/Acme/bad.yaml:6:10: airflow: "sideways" isn't one of the 10 values the schema allows`,
	}
	if diff := cmp.Diff(wantProblems, problems); diff != "" {
		t.Errorf("problems (-want +got):\n%s", diff)
	}
	// The field names are public interface, and so is how values are
	// written: decimals as numbers, references by name, and empty
	// collections rather than null.
	for _, field := range []string{
		`"ok": false`, `"name": "lib"`, `"format": "devicetype-library"`, `"files": 4`, `"invalid_files": 1`,
		`"type": "dcim.devicetype"`, `"identity": {`, `"manufacturer": {
              "name": "Huawei"
            },`, `"path": "device-types/Huawei/NE8000-X8.yaml"`, `"path": null`, `"derived_from": [
            "device-types/Huawei/NE8000-X8.yaml"
          ]`, `"derived": [
            "slug"
          ]`, `"derived": []`, `"metadata": {}`, `"metadata": {
            "front_image": true,`, `"module_bays": [
              {
                "name": "LPU 1",
                "position": "1"
              },`, `"unmapped": {}`, `"part_number": "02351930"`, `"u_height": 16,`, `"weight": 94,`,
		`"severity": "error"`, `"line": 6`, `"column": 10`, `"field": "airflow"`, `"line": null`, `"field": null`, `"reason": "`,
	} {
		if !strings.Contains(r.stdout, field) {
			t.Errorf("list -o json has no %s", field)
		}
	}
}

func TestListFailures(t *testing.T) {
	t.Parallel()
	repoURL, pool, _ := libraryRepo(t, nil)
	t.Run("not fetched", func(t *testing.T) {
		t.Parallel()
		env, _ := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library"}]`)
		r := runCLIApp(t, nil, trust(pool), env, "list", "-o", "json")
		if r.code != exitListProblems || !strings.Contains(r.stderr, "Error: 1 of the 1 sources couldn't be read: lib") {
			t.Errorf("exit code = %d\nstderr: %s", r.code, r.stderr)
		}
		var got listResult
		if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
			t.Fatal(err)
		}
		if s := got.Sources[0]; s.Error == nil || !strings.Contains(*s.Error, "hasn't been fetched: run 'netbox-trueup-ai source fetch', or use --fetch") || s.Version != nil {
			t.Errorf("list -o json = %+v", s)
		}
		if !strings.Contains(r.stdout, `"version": null`) || !strings.Contains(r.stdout, `"definitions": []`) {
			t.Errorf("list -o json of a source not fetched:\n%s", r.stdout)
		}
	})
	t.Run("schemas excluded", func(t *testing.T) {
		t.Parallel()
		env, _ := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library","exclude":["schema"]}]`)
		fetched(t, pool, env)
		r := runCLIApp(t, nil, trust(pool), env, "list")
		if r.code != exitListProblems || !strings.Contains(r.stdout, "lib: format devicetype-library: the source has no schema/devicetype.json") {
			t.Errorf("exit code = %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
		}
	})
	t.Run("unknown source", func(t *testing.T) {
		t.Parallel()
		env, _ := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library"}]`)
		if r := runCLIApp(t, nil, trust(pool), env, "list", "other"); r.code != exitUsage {
			t.Errorf("exit code = %d, want %d\nstderr: %s", r.code, exitUsage, r.stderr)
		}
	})
	t.Run("no sources", func(t *testing.T) {
		t.Parallel()
		if r := runCLI(t, nil, []string{"TRUEUP_DATA_DIR=" + t.TempDir()}, "list"); r.code != exitConfig {
			t.Errorf("exit code = %d, want %d\nstderr: %s", r.code, exitConfig, r.stderr)
		}
	})
}

// TestListFetch checks that list --fetch fetches each source first, and
// records the fetch, and that a source whose fetch fails isn't listed.
func TestListFetch(t *testing.T) {
	t.Parallel()
	repoURL, pool, commit := libraryRepo(t, nil)
	env, dataDir := sourcesEnv(t, `[
		{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library","include":["device-types/Huawei"]},
		{"name":"private","kind":"git","url":"git@git.example.com:defs.git","format":"devicetype-library"}
	]`)
	r := runCLIApp(t, nil, trust(pool), env, "list", "--fetch", "-o", "json")
	if r.code != exitListProblems {
		t.Errorf("exit code = %d, want %d\nstderr: %s", r.code, exitListProblems, r.stderr)
	}
	var got listResult
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatal(err)
	}
	lib, private := got.Sources[0], got.Sources[1]
	// Huawei alone, validated by the schemas the format added.
	if !lib.OK || *lib.Version != commit || lib.Files != 1 || len(lib.Definitions) != 2 {
		t.Errorf("lib = %+v", lib)
	}
	if private.OK || private.Error == nil || !strings.HasPrefix(*private.Error, "the fetch failed: an ssh repository needs credentials") {
		t.Errorf("private = %+v", private)
	}
	sources, snaps := records(t, dataDir)
	if len(sources) != 2 || snaps["lib"].Version != commit {
		t.Fatalf("the database records %+v", sources)
	}
	// The fetch keeps the schemas, which the format needs, though the
	// source includes only Huawei's device types.
	var paths []string
	for _, f := range snaps["lib"].Files {
		paths = append(paths, f.Path)
	}
	want := []string{
		"device-types/Huawei/NE8000-X8.yaml", "schema/components.json", "schema/devicetype.json",
		"schema/generated_schema.json", "schema/moduletype.json", "schema/racktype.json", "schema/reusable.json",
	}
	if diff := cmp.Diff(want, paths); diff != "" {
		t.Errorf("recorded files (-want +got):\n%s", diff)
	}
}

// TestListEscapes checks that what a file holds can't drive the terminal
// through list's text output.
func TestListEscapes(t *testing.T) {
	t.Parallel()
	repoURL, pool, _ := libraryRepo(t, map[string]string{
		"device-types/Acme/a.yaml": "manufacturer: \"Ac\\e[31mme\"\nmodel: \"Box\\u202e1\"\nslug: acme-box\nu_height: 1\nis_full_depth: true\n",
		"device-types/Acme/c.yaml": "manufacturer: Acme\nmodel: \"Box\\u20283\"\nslug: acme-box-3\nu_height: 1\nis_full_depth: true\n",
		"device-types/Acme/b.yaml": "manufacturer: Acme\nmodel: Box 2\nslug: acme-box-2\nu_height: 1\nis_full_depth: true\n\"extra\\a\": 1\n",
	})
	env, _ := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library"}]`)
	fetched(t, pool, env)
	r := runCLIApp(t, nil, trust(pool), env, "list")
	if strings.ContainsAny(r.stdout, "\x1b\u202e\a\u2028") {
		t.Errorf("list printed a control character:\n%q", r.stdout)
	}
	for _, escaped := range []string{`"Ac\x1b[31mme"`, `"Box\u202e1"`, `"Box\u20283"`, `["extra\a"]: isn't a key the schema allows here`} {
		if !strings.Contains(r.stdout, escaped) {
			t.Errorf("list doesn't show %s:\n%s", escaped, r.stdout)
		}
	}
}

func TestListSearch(t *testing.T) {
	t.Parallel()
	repoURL, pool, commit := libraryRepo(t, nil)
	env, _ := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library"}]`)
	fetched(t, pool, env)

	// The model, ignoring case, and the manufacturer, whose own definition
	// matches too; not the slug.
	r := runCLIApp(t, nil, trust(pool), env, "list", "--search", " gs1 ")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	want := `SOURCE  REF     VERSION                                   FILES  INVALID  WARNINGS  MANUFACTURERS  DEVICE TYPES
lib     master  ` + commit + `  3      0        1         0              1

SOURCE  TYPE         MANUFACTURER  MODEL  SLUG           FROM
lib     device type  Netgear       GS105  netgear-gs105  device-types/Netgear/GS105.yaml

Warnings:
  lib: manufacturers "NETGEAR" and "Netgear" would both get the slug "netgear", which NetBox gives to one manufacturer only: create one of them in NetBox with another slug, or spell them alike in the source
`
	if diff := cmp.Diff(want, r.stdout); diff != "" {
		t.Errorf("list --search gs1 (-want +got):\n%s", diff)
	}

	r = runCLIApp(t, nil, trust(pool), env, "list", "--search", "netgear", "-o", "json")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	var got listResult
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatal(err)
	}
	// In the format's order: manufacturers by name, then device types by
	// path.
	var found []string
	for _, d := range got.Sources[0].Definitions {
		where := "derived"
		if d.Path != nil {
			where = *d.Path
		}
		found = append(found, fmt.Sprintf("%s %v %s", d.Type, d.Identity, where))
	}
	wantFound := []string{
		"dcim.manufacturer map[name:NETGEAR] derived",
		"dcim.manufacturer map[name:Netgear] derived",
		"dcim.devicetype map[manufacturer:map[name:Netgear] model:GS105] device-types/Netgear/GS105.yaml",
		"dcim.devicetype map[manufacturer:map[name:NETGEAR] model:GS510TLP] device-types/Netgear/GS510TLP.yml",
	}
	if diff := cmp.Diff(wantFound, found); diff != "" {
		t.Errorf("list --search netgear -o json (-want +got):\n%s", diff)
	}
	if !strings.Contains(r.stdout, `"search": "netgear"`) || len(got.Sources[0].Problems) != 1 {
		t.Errorf("list --search netgear -o json doesn't give the search and every problem:\n%s", r.stdout)
	}

	// Nothing found is no problem; text that can't be searched is.
	if r := runCLIApp(t, nil, trust(pool), env, "list", "--search", "dell-poweredge"); r.code != exitOK || strings.Contains(r.stdout, "device type") {
		t.Errorf("list --search dell-poweredge: exit code %d\nstdout: %s", r.code, r.stdout)
	}
	for _, search := range []string{"", "  ", "a\nb"} {
		if r := runCLIApp(t, nil, trust(pool), env, "list", "--search", search); r.code != exitUsage {
			t.Errorf("list --search %q: exit code %d, want %d\nstderr: %s", search, r.code, exitUsage, r.stderr)
		}
	}
}

func TestListReadsTheDatabase(t *testing.T) {
	t.Parallel()
	repoURL, pool, _ := libraryRepo(t, nil)
	env, dataDir := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library"}]`)
	fetched(t, pool, env)
	want := runCLIApp(t, nil, trust(pool), env, "list", "-o", "json")
	if want.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", want.code, want.stderr)
	}

	// Once loaded, the definitions are read from the database alone.
	if err := os.RemoveAll(filepath.Join(dataDir, "cache")); err != nil {
		t.Fatal(err)
	}
	r := runCLIApp(t, nil, trust(pool), env, "list", "-o", "json")
	if r.code != exitOK || r.stdout != want.stdout {
		t.Errorf("list without the cache: exit code %d\nstderr: %s", r.code, r.stderr)
	}
}

func TestListLoadsWhatTheDatabaseLacks(t *testing.T) {
	t.Parallel()
	repoURL, pool, commit := libraryRepo(t, nil)
	env, dataDir := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library"}]`)
	fetched(t, pool, env)
	want := runCLIApp(t, nil, trust(pool), env, "list", "-o", "json")
	if want.code != exitOK || strings.Contains(want.stderr, "loaded the source's definitions") {
		t.Fatalf("list after a fetch: exit code %d\nstderr: %s", want.code, want.stderr)
	}
	loadedBy := storedDefinitions(t, dataDir).Loaded
	reader, settings := loadedBy.Reader, loadedBy.Settings
	broken := func(t *testing.T) store.Definition {
		t.Helper()
		key, err := definitionKey(format.Definition{Object: model.Object{Type: "dcim.manufacturer"}, Identity: map[string]model.Value{"name": model.String("Acme")}})
		if err != nil {
			t.Fatal(err)
		}
		return store.Definition{DefinitionKey: key, Manufacturer: "Acme", JSON: []byte(`{"type":"dcim.manufacturer"}`), UniqueValues: []byte(`{}`)}
	}
	for _, tt := range []struct {
		name, reader, settings, log string
		defs                        []store.Definition
	}{
		// What another release's format read is read again, and so is what
		// other settings of the source chose.
		{"another reader", "devicetype-library/0", settings, `msg="loaded the source's definitions into the database"`, nil},
		{"another release", "devicetype-library/1 (TrueUp 0.9.0)", settings, `msg="loaded the source's definitions into the database"`, nil},
		{"other settings", reader, "sha256:other", `msg="loaded the source's definitions into the database"`, nil},
		// So is what this release can't read back, which a format whose
		// version wasn't raised would leave.
		{
			"definitions that don't read back", reader, settings,
			`msg="the database holds definitions of the source that this release can't read back`,
			[]store.Definition{broken(t)},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recordLoadIn(t, dataDir, store.Load{
				Source: "lib", Version: commit, Reader: tt.reader, Settings: tt.settings, StartedAt: time.Now(), Definitions: tt.defs,
			})
			r := runCLIApp(t, nil, trust(pool), env, "list", "-o", "json")
			if r.code != exitOK || r.stdout != want.stdout {
				t.Errorf("exit code %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
			}
			if !strings.Contains(r.stderr, tt.log) {
				t.Errorf("stderr doesn't log %s:\n%s", tt.log, r.stderr)
			}
			if loaded := storedDefinitions(t, dataDir); loaded.Loaded.Reader != reader || len(loaded.Definitions) != 6 {
				t.Errorf("the database holds %d definitions read by %q, want 6 read by %s", len(loaded.Definitions), loaded.Loaded.Reader, reader)
			}
		})
	}
}

func TestListLoadsAgainWhenTheSettingsChange(t *testing.T) {
	t.Parallel()
	repoURL, pool, _ := libraryRepo(t, nil)
	sources := func(extra string) string {
		return `[{"name":"lib","kind":"git","url":"` + repoURL + `","format":"devicetype-library"` + extra + `}]`
	}
	env, dataDir := sourcesEnv(t, sources(""))
	fetched(t, pool, env)
	countLoaded := func(env []string) (definitions int, loaded bool) {
		t.Helper()
		r := runCLIApp(t, nil, trust(pool), env, "list", "-o", "json")
		if r.code != exitOK {
			t.Fatalf("exit code %d\nstderr: %s", r.code, r.stderr)
		}
		var got listResult
		if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
			t.Fatal(err)
		}
		return len(got.Sources[0].Definitions), strings.Contains(r.stderr, "loaded the source's definitions")
	}
	if n, loaded := countLoaded(env); n != 6 || loaded {
		t.Errorf("list after a fetch: %d definitions, loaded again: %t; want 6 and false", n, loaded)
	}
	// Excluding Huawei's device types, though the version is the same, is
	// another load, without a fetch; and fetching then loads nothing more.
	excluded := []string{"TRUEUP_DATA_DIR=" + dataDir, "TRUEUP_SOURCES=" + sources(`,"exclude":["device-types/Huawei"]`)}
	if n, loaded := countLoaded(excluded); n != 4 || !loaded {
		t.Errorf("list once Huawei is excluded: %d definitions, loaded again: %t; want 4 and true", n, loaded)
	}
	if r := runCLIApp(t, nil, trust(pool), excluded, "source", "fetch"); r.code != exitOK || strings.Contains(r.stderr, "loaded the source's definitions") {
		t.Errorf("fetching with the settings loaded: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	if n, loaded := countLoaded(excluded); n != 4 || loaded {
		t.Errorf("list again: %d definitions, loaded again: %t; want 4 and false", n, loaded)
	}
	if n, loaded := countLoaded(env); n != 6 || !loaded {
		t.Errorf("list with the settings as they were: %d definitions, loaded again: %t; want 6 and true", n, loaded)
	}
}

// recordLoadIn records a load in the database in dataDir.
func recordLoadIn(t *testing.T, dataDir string, l store.Load) {
	t.Helper()
	s, _, err := engine.Open(t.Context(), engine.Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.RecordLoad(t.Context(), l); err != nil {
		t.Fatal(err)
	}
}

func TestAFailedLoadIsTriedAgainByAFetch(t *testing.T) {
	t.Parallel()
	repoURL, pool, _ := libraryRepo(t, nil)
	sources := func(extra string) []string {
		return []string{"TRUEUP_SOURCES=" + `[{"name":"lib","kind":"git","url":"` + repoURL + `","format":"devicetype-library"` + extra + `}]`}
	}
	env, dataDir := sourcesEnv(t, "[]")
	noSchemas := append(slices.Clone(env[:1]), sources(`,"exclude":["schema"]`)...)
	fetched(t, pool, noSchemas)
	first := storedDefinitions(t, dataDir)
	if first.State() != store.LoadFailed || !strings.Contains(first.Error, "no schema/devicetype.json") {
		t.Fatalf("after the fetch, the load is %s: %q", first.State(), first.Error)
	}

	// list reports what the load found, without reading the files again.
	r := runCLIApp(t, nil, trust(pool), noSchemas, "list")
	if r.code != exitListProblems || !strings.Contains(r.stdout, "lib: format devicetype-library: the source has no schema/devicetype.json") {
		t.Errorf("list: exit code %d\nstdout: %s", r.code, r.stdout)
	}
	if again := storedDefinitions(t, dataDir); !again.StartedAt.Equal(first.StartedAt) {
		t.Errorf("list loaded the source again, at %v", again.StartedAt)
	}
	// A fetch tries again.
	fetched(t, pool, noSchemas)
	if again := storedDefinitions(t, dataDir); !again.StartedAt.After(first.StartedAt) || again.State() != store.LoadFailed {
		t.Errorf("after another fetch, the load is %s, started at %v, after %v", again.State(), again.StartedAt, first.StartedAt)
	}
	// So do other settings, as when the schemas are let in again.
	withSchemas := append(slices.Clone(env[:1]), sources("")...)
	if r := runCLIApp(t, nil, trust(pool), withSchemas, "list"); r.code != exitOK {
		t.Errorf("list with the schemas: exit code %d\nstderr: %s", r.code, r.stderr)
	}
}

// A source that isn't OK always gets a reason in the error after the
// result, even when no file is invalid.
func TestSummariesSayWhyASourceIsntOK(t *testing.T) {
	t.Parallel()
	path := "device-types/Acme/W1.yaml"
	for _, tt := range []struct {
		name     string
		problems []problemJSON
		invalid  int
		want     string
	}{
		{"an error with no file", []problemJSON{{Severity: "error", Reason: "x"}}, 0, "1 error concerns no one file"},
		{"two errors with no file", []problemJSON{{Severity: "error", Reason: "x"}, {Severity: "error", Reason: "y"}}, 0, "2 errors concern no one file"},
		{"an invalid file, and an error with no file", []problemJSON{{Severity: "error", Path: &path, Reason: "x"}, {Severity: "error", Reason: "y"}}, 1, "1 file is invalid; 1 error concerns no one file"},
		{"a warning with no file", []problemJSON{{Severity: "warning", Reason: "x"}}, 0, ""},
	} {
		list := listJSON{Sources: []listSourceJSON{{Name: "lib", InvalidFiles: tt.invalid, Problems: tt.problems}}}
		if got := list.summary(); got != tt.want {
			t.Errorf("%s: list's summary = %q, want %q", tt.name, got, tt.want)
		}
		compared := compareJSON{Sources: []compareSourceJSON{{Name: "lib", InvalidFiles: tt.invalid, Problems: tt.problems}}}
		if got := compared.summary(); got != tt.want {
			t.Errorf("%s: compare's summary = %q, want %q", tt.name, got, tt.want)
		}
	}
}
