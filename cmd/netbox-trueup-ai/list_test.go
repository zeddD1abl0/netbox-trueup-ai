package main

import (
	"crypto/x509"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
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
	deviceType := func(manufacturer, name string) definitionJSON {
		return definitionJSON{Type: "dcim.devicetype", Identity: map[string]model.Value{
			"manufacturer": model.Reference{Type: "dcim.manufacturer", Key: map[string]model.Value{"name": model.String(manufacturer)}},
			"model":        model.String(name),
		}}
	}
	got := definitionLines(listSourceJSON{Name: "lib", Definitions: []definitionJSON{
		{Type: "dcim.manufacturer", Identity: map[string]model.Value{"name": model.String("Acme")}},
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
            "front_image": true,`, `"unmapped": {
            "module-bays": 18
          }`, `"unmapped": {}`, `"part_number": "02351930"`, `"u_height": 16,`, `"weight": 94,`,
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
		if s := got.Sources[0]; s.Error == nil || !strings.Contains(*s.Error, "hasn't been fetched: run 'netbox-trueup-ai source fetch', or list with --fetch") || s.Version != nil {
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
