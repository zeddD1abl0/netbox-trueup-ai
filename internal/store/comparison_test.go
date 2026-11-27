package store_test

import (
	"strings"
	"testing"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
)

// TestPrepareRefusesBranchesTheSchemaRefuses checks that Prepare itself
// refuses the branches each engine's schema refuses too, so that the error
// says what is wrong rather than which constraint failed. The shared storage
// tests can't tell the two apart.
func TestPrepareRefusesBranchesTheSchemaRefuses(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 27, 1, 2, 3, 0, time.UTC)
	run := func(b *store.Branch) store.ComparisonRun {
		return store.ComparisonRun{
			Source: "lib", Loaded: store.LoadKey{Version: "v1", Reader: "r/1", Settings: "s"}, LoadedAt: at, ComparedAt: at,
			NetBoxURL: "https://netbox.example.com/", NetBoxVersion: "4.7.2", Branch: b,
		}
	}
	valid := store.Branch{SchemaID: "abcd1234", Name: "lab", NetBoxID: 7}
	got, err := run(&valid).Prepare()
	if err != nil {
		t.Fatalf("Prepare with a valid branch: %v", err)
	}
	if got.Target() != "abcd1234" {
		t.Errorf("Target() = %q, want abcd1234", got.Target())
	}
	// Prepare keeps a copy, so that the caller changing its branch
	// afterwards changes nothing recorded.
	got.Branch.Name = "changed"
	if valid.Name != "lab" {
		t.Error("Prepare kept the caller's branch, not a copy")
	}
	if main, err := run(nil).Prepare(); err != nil || main.Target() != "" || main.Branch != nil {
		t.Errorf("Prepare in main = %+v, %v; want no branch and the target ''", main, err)
	}

	tests := []struct {
		name   string
		branch store.Branch
		want   string
	}{
		{"no schema ID", store.Branch{Name: "lab", NetBoxID: 7}, "schema ID"},
		{"a short schema ID", store.Branch{SchemaID: "abcd123", Name: "lab", NetBoxID: 7}, "schema ID"},
		{"a long schema ID", store.Branch{SchemaID: "abcd12345", Name: "lab", NetBoxID: 7}, "schema ID"},
		{"an upper-case schema ID", store.Branch{SchemaID: "ABCD1234", Name: "lab", NetBoxID: 7}, "schema ID"},
		{"a schema ID with a line break", store.Branch{SchemaID: "abcd123\n", Name: "lab", NetBoxID: 7}, "schema ID"},
		{"no name", store.Branch{SchemaID: "abcd1234", NetBoxID: 7}, "name must be text"},
		{"a name that isn't UTF-8", store.Branch{SchemaID: "abcd1234", Name: "l\xffb", NetBoxID: 7}, "name must be text"},
		{"a name with a NUL", store.Branch{SchemaID: "abcd1234", Name: "l\x00b", NetBoxID: 7}, "name must be text"},
		{"no NetBox ID", store.Branch{SchemaID: "abcd1234", Name: "lab"}, "NetBox ID must be positive"},
		{"a negative NetBox ID", store.Branch{SchemaID: "abcd1234", Name: "lab", NetBoxID: -1}, "NetBox ID must be positive"},
	}
	for _, tt := range tests {
		if _, err := run(&tt.branch).Prepare(); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Prepare with %s: %v, want an error about its %s", tt.name, err, tt.want)
		}
	}
}
