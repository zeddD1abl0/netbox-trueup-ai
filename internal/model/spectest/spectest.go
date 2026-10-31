package spectest

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/openapi"
)

var update = flag.Bool("update", false, "rewrite the schema snapshot from the live NetBox's schema (integration tests only)")

// Endpoints returns the endpoints of specs, sorted: what a snapshot of the
// schema keeps.
func Endpoints(specs []*model.Spec) []string {
	var endpoints []string
	for _, s := range specs {
		if s.Endpoint != "" {
			endpoints = append(endpoints, s.Endpoint)
		}
	}
	slices.Sort(endpoints)
	return endpoints
}

// Release returns the NetBox release a schema is from, such as 4.7.2 for
// the version "4.7.2-Docker-5.1.1 (4.7)".
func Release(doc *openapi.Document) string {
	release, _, _ := strings.Cut(doc.Info.Version, "-")
	release, _, _ = strings.Cut(release, " ")
	return release
}

// Run runs the tests every spec package runs on its specs, which must be
// every spec TrueUp has: that they are consistent (model.Check), that they
// agree with the schema snapshot in the file snapshot, which holds their
// endpoints and nothing more, and, when `make reference` has fetched NetBox's
// source, that their citations are there.
func Run(t *testing.T, specs []*model.Spec, snapshot string) {
	t.Helper()
	t.Run("Consistent", func(t *testing.T) {
		if err := model.Check(specs); err != nil {
			t.Errorf("the specs aren't consistent:\n%v", err)
		}
	})
	data, err := os.ReadFile(snapshot) //nolint:gosec // G304: the test names its own snapshot.
	if err != nil {
		t.Fatalf("read the schema snapshot: %v", err)
	}
	doc, err := openapi.Load(data)
	if err != nil {
		t.Fatalf("schema snapshot %s: %v", snapshot, err)
	}
	t.Run("Snapshot", func(t *testing.T) {
		trimmed, err := openapi.Trim(data, Endpoints(specs))
		switch {
		case err != nil:
			t.Errorf("schema snapshot %s: %v; run make schema to refresh it from NetBox", snapshot, err)
		case !bytes.Equal(trimmed, data):
			t.Errorf("schema snapshot %s doesn't hold exactly the specs' endpoints:\n%s\nrun make schema to refresh it from NetBox", snapshot, firstDifference(data, trimmed))
		}
	})
	t.Run("Contract", func(t *testing.T) {
		if err := Contract(doc, specs); err != nil {
			t.Errorf("the specs don't agree with the schema snapshot %s, of NetBox %s:\n%v", snapshot, doc.Info.Version, err)
		}
	})
	t.Run("Sources", func(t *testing.T) {
		root, err := ReferenceSource(Release(doc))
		switch {
		case errors.Is(err, ErrNoReference):
			t.Skipf("can't check the specs' citations: %v", err)
		case err != nil:
			t.Fatal(err)
		}
		if err := Sources(root, specs); err != nil {
			t.Errorf("NetBox's source in %s doesn't bear out the specs' citations:\n%v", root, err)
		}
	})
}

// Live checks specs against schema, the live schema of the disposable NetBox
// (internal/netboxtest), and that the file snapshot is that schema trimmed to
// the specs' endpoints. With -update, it only rewrites the snapshot: the
// specs' choices, generated from the old snapshot, may not agree with the new
// one until `make generate` has run, which `make schema` does next, before
// it tests the specs.
func Live(t *testing.T, schema []byte, specs []*model.Spec, snapshot string) {
	t.Helper()
	doc, err := openapi.Load(schema)
	if err != nil {
		t.Fatal(err)
	}
	trimmed, err := openapi.Trim(schema, Endpoints(specs))
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(snapshot, trimmed, 0o644); err != nil { //nolint:gosec // G306: a committed file.
			t.Fatal(err)
		}
		t.Logf("wrote %s from NetBox %s's schema", snapshot, doc.Info.Version)
		return
	}
	if err := Contract(doc, specs); err != nil {
		t.Errorf("the specs don't agree with the schema of NetBox %s:\n%v", doc.Info.Version, err)
	}
	committed, err := os.ReadFile(snapshot) //nolint:gosec // G304: the test names its own snapshot.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(committed, trimmed) {
		t.Errorf("schema snapshot %s isn't NetBox %s's schema:\n%s\nrun make schema to refresh it, and review the difference",
			snapshot, doc.Info.Version, firstDifference(committed, trimmed))
	}
}

// firstDifference shows where two versions of a file first differ, a few
// lines either side.
func firstDifference(was, now []byte) string {
	a, b := strings.Split(string(was), "\n"), strings.Split(string(now), "\n")
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	var out strings.Builder
	fmt.Fprintf(&out, "first difference at line %d (- committed, + expected):\n", i+1)
	for _, side := range []struct {
		mark  string
		lines []string
	}{{"-", a}, {"+", b}} {
		for j := max(i-2, 0); j < min(i+4, len(side.lines)); j++ {
			fmt.Fprintf(&out, "%s %s\n", side.mark, side.lines[j])
		}
	}
	return out.String()
}
