package model_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/spectest"
)

// TestSlugify checks Slugify against what NetBox's own slugify makes of
// each name in testdata/slugify.json, which testdata/slugify.mjs wrote by
// running it.
func TestSlugify(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/slugify.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name  string `json:"name"`
		Chars int    `json:"chars"`
		Slug  string `json:"slug"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 50 {
		t.Fatalf("testdata/slugify.json holds %d cases, too few to trust", len(cases))
	}
	for _, c := range cases {
		length := model.SlugLength
		if c.Chars != 0 {
			length = c.Chars
		}
		if got := model.Slugify(c.Name, length); got != c.Slug {
			t.Errorf("Slugify(%q, %d) = %q, NetBox makes %q", c.Name, length, got, c.Slug)
		}
	}
}

// reslug is NetBox 4.7.2's slugify, as project-static/src/buttons/reslug.ts
// defines it, which Slugify follows and testdata/slugify.json comes from.
const reslug = `function slugify(slug: string, chars: number): string {
  return slug
    .replace(/[^\-.\w\s]/g, '') // Remove unneeded chars
    .replace(/^[\s.]+|[\s.]+$/g, '') // Trim leading/trailing spaces
    .replace(/[-.\s]+/g, '-') // Convert spaces and decimals to hyphens
    .toLowerCase() // Convert to lowercase
    .substring(0, chars); // Trim to first chars chars
}`

// TestSlugifyFollowsNetBox checks, when `make reference` has fetched NetBox's
// source, that its slugify is still the one Slugify follows, and that the
// slug field of NetBox's organizational models still sets no maxlength, so
// that the UI cuts a slug at 50 characters (model.SlugLength).
func TestSlugifyFollowsNetBox(t *testing.T) {
	t.Parallel()
	root, err := spectest.ReferenceSource("4.7.2")
	if errors.Is(err, spectest.ErrNoReference) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	ts, err := os.ReadFile(filepath.Join(root, "project-static", "src", "buttons", "reslug.ts")) //nolint:gosec // G304: NetBox's source, in the repository's own .cache.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ts), reslug) {
		t.Errorf("NetBox's slugify in reslug.ts isn't the one Slugify follows:\n%s", ts)
	}
	if !strings.Contains(string(ts), "let slugLength = 50;") {
		t.Errorf("NetBox's reslug.ts no longer cuts a slug at 50 characters when its field has no maxlength:\n%s", ts)
	}
	forms, err := os.ReadFile(filepath.Join(root, "netbox", "forms", "model_forms.py")) //nolint:gosec // G304: NetBox's source, in the repository's own .cache.
	if err != nil {
		t.Fatal(err)
	}
	const declared = "class OrganizationalModelForm(OwnerMixin, NetBoxModelForm):\n" +
		"    \"\"\"\n    Form for models which inherit from OrganizationalModel.\n    \"\"\"\n    slug = SlugField()\n"
	if !strings.Contains(string(forms), declared) {
		t.Error("OrganizationalModelForm no longer declares slug = SlugField() without a max_length: check the slug's maxlength in NetBox's UI")
	}
}
