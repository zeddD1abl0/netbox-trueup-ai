package store_test

import (
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/audit"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/storetest"
)

// TestPrepareEventRefusesWhatTheSchemaRefuses checks that PrepareEvent itself
// refuses events that each engine's schema refuses too, so that the error
// says what is wrong rather than which constraint failed. The shared storage
// tests can't tell the two apart.
func TestPrepareEventRefusesWhatTheSchemaRefuses(t *testing.T) {
	t.Parallel()
	valid := audit.Event{By: storetest.By, Action: audit.SourceAdded, Result: audit.Failed, Error: "403", NetBoxID: 42, Completes: 7}
	if _, err := store.PrepareEvent(valid); err != nil {
		t.Fatalf("PrepareEvent with a valid event: %v", err)
	}
	tests := []struct {
		name   string
		change func(*audit.Event)
	}{
		{"an unknown result", func(e *audit.Event) { e.Result = "done" }},
		{"a failure without an error", func(e *audit.Event) { e.Error = "" }},
		{"an error on a success", func(e *audit.Event) { e.Result = audit.Succeeded }},
		{"a started event completing another", func(e *audit.Event) { e.Result, e.Error = audit.Started, "" }},
		{"a negative completion", func(e *audit.Event) { e.Completes = -1 }},
		{"a negative NetBox ID", func(e *audit.Event) { e.NetBoxID = -1 }},
	}
	for _, tt := range tests {
		e := valid
		tt.change(&e)
		if _, err := store.PrepareEvent(e); err == nil {
			t.Errorf("PrepareEvent with %s succeeded", tt.name)
		}
	}
}
