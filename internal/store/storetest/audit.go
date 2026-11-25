package storetest

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/audit"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
)

// asText compares details, and any other JSON, as text.
var asText = cmp.Transformer("text", func(b json.RawMessage) string { return string(b) })

// listEvents returns the events a filter chooses.
func listEvents(t *testing.T, s store.Store, f audit.Filter) audit.Page {
	t.Helper()
	page, err := s.Events(t.Context(), f)
	if err != nil {
		t.Fatalf("Events(%+v): %v", f, err)
	}
	return page
}

// checkEvents checks that got holds the events want, in order, recorded
// between from and to, with IDs that rise. Times don't order events, so
// they aren't checked against each other.
func checkEvents(t *testing.T, got []audit.Recorded, from, to time.Time, want ...audit.Event) {
	t.Helper()
	events := make([]audit.Event, len(got))
	for i, r := range got {
		events[i] = r.Event
		if i > 0 && r.ID <= got[i-1].ID {
			t.Errorf("event %d has ID %d, after %d", i, r.ID, got[i-1].ID)
		}
		if r.OccurredAt.Location() != time.UTC {
			t.Errorf("event %d occurred at %v, not in UTC", i, r.OccurredAt)
		}
		if r.OccurredAt.Before(from.Truncate(time.Microsecond)) || r.OccurredAt.After(to) {
			t.Errorf("event %d occurred at %v, not between %v and %v", i, r.OccurredAt, from, to)
		}
	}
	if diff := cmp.Diff(want, events, asText, equateEmpty); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}
}

// sourceEvent is an event about a source, as By's action.
func sourceEvent(action audit.Action, source, details string) audit.Event {
	return audit.Event{By: By, Action: action, Result: audit.Succeeded, Source: source, Details: json.RawMessage(details)}
}

func testFetchEvents(t *testing.T, s store.Store) {
	from := time.Now()
	record(t, s, success("lib", "v1", t0, "a.yaml"))
	record(t, s, success("lib", "v1", t0.Add(time.Minute), "a.yaml"))
	record(t, s, success("lib", "v2", t0.Add(2*time.Minute), "a.yaml"))
	record(t, s, failure("lib", t0.Add(3*time.Minute), "down"))
	moved := success("lib", "v3", t0.Add(4*time.Minute), "a.yaml")
	moved.Source.URL, moved.Source.Ref, moved.Ref = "https://git.example.com/moved.git", "", "master"
	record(t, s, moved)
	failed := failure("lib", t0.Add(5*time.Minute), "down")
	failed.Source = moved.Source
	failed.Source.Kind = "http"
	record(t, s, failed)
	record(t, s, failure("down", t0, "unreachable"))
	record(t, s, success("down", "v1", t0.Add(time.Minute), "a.yaml"))

	checkEvents(t, listEvents(t, s, audit.Filter{}).Events, from, time.Now(),
		// Fetching a version already held, or failing, changes nothing.
		sourceEvent(audit.SourceAdded, "lib", `{"kind":"git","url":"https://git.example.com/lib.git","ref":"main","version":"v1"}`),
		sourceEvent(audit.SourceNewVersion, "lib", `{"before":"v1","after":"v2","ref":"main"}`),
		sourceEvent(audit.SourceReconfigured, "lib",
			`{"before":{"kind":"git","url":"https://git.example.com/lib.git","ref":"main"},"after":{"kind":"git","url":"https://git.example.com/moved.git","ref":""}}`),
		sourceEvent(audit.SourceNewVersion, "lib", `{"before":"v2","after":"v3","ref":"master"}`),
		// A failed fetch records its configuration, and so its change.
		sourceEvent(audit.SourceReconfigured, "lib",
			`{"before":{"kind":"git","url":"https://git.example.com/moved.git","ref":""},"after":{"kind":"http","url":"https://git.example.com/moved.git","ref":""}}`),
		// A source whose first fetch failed is added without a version,
		// and its first snapshot is a new version.
		sourceEvent(audit.SourceAdded, "down", `{"kind":"git","url":"https://git.example.com/down.git","ref":"main"}`),
		sourceEvent(audit.SourceNewVersion, "down", `{"before":null,"after":"v1","ref":"main"}`),
	)
}

func testRemoveEvents(t *testing.T, s store.Store) {
	record(t, s, success("a", "v1", t0, "a.yaml"))
	record(t, s, failure("b", t0, "down"))
	record(t, s, success("c", "v1", t0, "c.yaml"))
	other := By
	other.Actor.Name, other.Actor.ID = "remover", "1001"
	from := time.Now()
	removed, err := s.RemoveSources(t.Context(), other, []string{"c"})
	if err != nil || !cmp.Equal(removed, []string{"a", "b"}) {
		t.Fatalf("RemoveSources = %v, %v; want a and b removed", removed, err)
	}
	removal := func(source, details string) audit.Event {
		e := sourceEvent(audit.SourceRemoved, source, details)
		e.By = other
		return e
	}
	checkEvents(t, listEvents(t, s, audit.Filter{Actions: []audit.Action{audit.SourceRemoved}}).Events, from, time.Now(),
		removal("a", `{"kind":"git","url":"https://git.example.com/a.git","ref":"main","snapshot":{"url":"https://git.example.com/a.git","ref":"main","version":"v1"}}`),
		removal("b", `{"kind":"git","url":"https://git.example.com/b.git","ref":"main"}`),
	)
	// A source's events outlive it.
	if got := listEvents(t, s, audit.Filter{Source: "a"}).Events; len(got) != 2 {
		t.Errorf("source a has %d events after its removal, want 2: added and removed", len(got))
	}
	// Removing nothing records nothing.
	if _, err := s.RemoveSources(t.Context(), By, []string{"c"}); err != nil {
		t.Fatal(err)
	}
	if got := listEvents(t, s, audit.Filter{}).Events; len(got) != 5 {
		t.Errorf("%d events after removing nothing, want 5", len(got))
	}

	// A source whose last fetch, from another URL, failed keeps the
	// snapshot of the URL it was fetched from.
	moved := failure("c", t0.Add(time.Hour), "down")
	moved.Source.URL = "https://git.example.com/moved.git"
	record(t, s, moved)
	from = time.Now()
	if _, err := s.RemoveSources(t.Context(), other, nil); err != nil {
		t.Fatal(err)
	}
	checkEvents(t, listEvents(t, s, audit.Filter{Actions: []audit.Action{audit.SourceRemoved}, Source: "c"}).Events, from, time.Now(),
		removal("c", `{"kind":"git","url":"https://git.example.com/moved.git","ref":"main","snapshot":{"url":"https://git.example.com/c.git","ref":"main","version":"v1"}}`),
	)
}

// fullEvent is an event with every field set.
func fullEvent() audit.Event {
	return audit.Event{
		By: audit.By{
			Actor:         audit.Actor{Kind: audit.OSUser, Name: "root", ID: "0", Host: "host.example.com", SudoUser: "jordan", SudoUserID: "1000"},
			Through:       audit.CommandLine,
			ClientAddress: "192.0.2.10",
		},
		Action:       "netbox.write",
		Result:       audit.Failed,
		Error:        "412 Precondition Failed",
		Source:       "lib",
		ObjectType:   "dcim.devicetype",
		Object:       "Dell / PowerEdge R640",
		NetBoxURL:    "https://netbox.example.com/",
		NetBoxBranch: "a1b2c3d4",
		NetBoxID:     42,
		Details:      json.RawMessage(`{"before": {"u_height": 1}, "after": {"u_height": 2}}`),
	}
}

func testRecordEvents(t *testing.T, s store.Store) {
	if err := s.RecordEvents(t.Context(), nil); err != nil {
		t.Fatalf("RecordEvents with none: %v", err)
	}
	full := fullEvent()
	bare := audit.Event{By: By, Action: audit.SourceAdded, Result: audit.Started}
	odd := fullEvent()
	odd.Actor.Host, odd.Error, odd.Object = "host\x00name", "bad \xff byte", "Dell / \u202eR640"
	odd.Details = json.RawMessage(`{"path":"a\u0000b"}`)
	from := time.Now()
	if err := s.RecordEvents(t.Context(), []audit.Event{full, bare, odd}); err != nil {
		t.Fatalf("RecordEvents: %v", err)
	}
	wantFull, wantBare, wantOdd := full, bare, odd
	wantFull.Details = json.RawMessage(`{"before":{"u_height":1},"after":{"u_height":2}}`)
	wantBare.Details = json.RawMessage(`{}`)
	wantOdd.Actor.Host, wantOdd.Error = "host\uFFFDname", "bad \uFFFD byte"
	checkEvents(t, listEvents(t, s, audit.Filter{}).Events, from, time.Now(), wantFull, wantBare, wantOdd)
}

func testCompletions(t *testing.T, s store.Store) {
	started := audit.Event{By: By, Action: "netbox.write", Result: audit.Started, Object: "Dell / R640"}
	done := audit.Event{By: By, Action: audit.SourceAdded, Result: audit.Succeeded}
	if err := s.RecordEvents(t.Context(), []audit.Event{started, done}); err != nil {
		t.Fatal(err)
	}
	all := listEvents(t, s, audit.Filter{}).Events
	startedID, doneID := all[0].ID, all[1].ID
	completion := func(id int64, action audit.Action) audit.Event {
		return audit.Event{By: By, Action: action, Result: audit.Failed, Error: "412", Completes: id}
	}
	for _, tt := range []struct {
		name string
		e    audit.Event
	}{
		{"an event that isn't recorded", completion(doneID+1000, "netbox.write")},
		{"an event that wasn't started", completion(doneID, audit.SourceAdded)},
		{"an event of another action", completion(startedID, "netbox.read")},
	} {
		if err := s.RecordEvents(t.Context(), []audit.Event{tt.e}); err == nil {
			t.Errorf("RecordEvents completing %s succeeded", tt.name)
		}
	}
	if err := s.RecordEvents(t.Context(), []audit.Event{completion(startedID, "netbox.write")}); err != nil {
		t.Fatalf("RecordEvents completing a started event: %v", err)
	}
	if err := s.RecordEvents(t.Context(), []audit.Event{completion(startedID, "netbox.write")}); err == nil {
		t.Error("RecordEvents completed a started event twice")
	}
	got := listEvents(t, s, audit.Filter{}).Events
	if len(got) != 3 || got[2].Completes != startedID || got[2].Result != audit.Failed {
		t.Errorf("events = %+v, want the started event's one completion last", got)
	}
}

func testInvalidEvents(t *testing.T, s store.Store) {
	tests := []struct {
		name   string
		change func(*audit.Event)
	}{
		{"no actor kind", func(e *audit.Event) { e.Actor.Kind = "" }},
		{"an unknown actor kind", func(e *audit.Event) { e.Actor.Kind = "robot" }},
		{"an actor with neither name nor ID", func(e *audit.Event) { e.Actor.Name, e.Actor.ID = "", "" }},
		{"no channel", func(e *audit.Event) { e.Through = "" }},
		{"no action", func(e *audit.Event) { e.Action = "" }},
		{"an action without a verb", func(e *audit.Event) { e.Action = "source" }},
		{"an action in capitals", func(e *audit.Event) { e.Action = "Source.Added" }},
		{"no result", func(e *audit.Event) { e.Result = "" }},
		{"a failure without an error", func(e *audit.Event) { e.Error = "" }},
		{"an error on a success", func(e *audit.Event) { e.Result = audit.Succeeded }},
		{"a started event completing another", func(e *audit.Event) { e.Result, e.Error, e.Completes = audit.Started, "", 7 }},
		{"a negative completion", func(e *audit.Event) { e.Completes = -1 }},
		{"a negative NetBox ID", func(e *audit.Event) { e.NetBoxID = -1 }},
		{"details that aren't JSON", func(e *audit.Event) { e.Details = json.RawMessage(`{"a":`) }},
		{"details that aren't an object", func(e *audit.Event) { e.Details = json.RawMessage(`[1]`) }},
		{"empty details", func(e *audit.Event) { e.Details = json.RawMessage(` `) }},
		{"details that aren't UTF-8", func(e *audit.Event) { e.Details = json.RawMessage("{\"a\":\"\xff\"}") }},
	}
	for _, tt := range tests {
		e := fullEvent()
		tt.change(&e)
		// With a valid event before it, so that nothing of the batch is
		// recorded.
		if err := s.RecordEvents(t.Context(), []audit.Event{fullEvent(), e}); err == nil {
			t.Errorf("RecordEvents with %s succeeded", tt.name)
		}
	}
	for _, f := range []audit.Filter{{Limit: -1}, {Actor: "a\xffb"}, {Source: "a\x00b"}} {
		if _, err := s.Events(t.Context(), f); err == nil {
			t.Errorf("Events with %+v succeeded", f)
		}
	}

	// A fetch or a removal without an actor records nothing, though it
	// would record no event.
	record(t, s, success("lib", "v1", t0, "a.yaml"))
	if err := s.RecordFetch(t.Context(), audit.By{}, success("lib", "v1", t0.Add(time.Minute), "a.yaml")); err == nil {
		t.Error("RecordFetch without an actor succeeded")
	}
	if _, err := s.RemoveSources(t.Context(), audit.By{}, []string{"lib"}); err == nil {
		t.Error("RemoveSources without an actor, removing nothing, succeeded")
	}
	if _, err := s.RemoveSources(t.Context(), audit.By{}, nil); err == nil {
		t.Error("RemoveSources without an actor succeeded")
	}
	if got := sources(t, s); len(got) != 1 || !got[0].FetchedAt.Equal(t0) {
		t.Errorf("Sources = %+v, want lib as first fetched", got)
	}
	if got := listEvents(t, s, audit.Filter{}).Events; len(got) != 1 {
		t.Errorf("%d events, want only lib's addition", len(got))
	}
}

func testEventFilters(t *testing.T, s store.Store) {
	event := func(actor string, action audit.Action, source string) audit.Event {
		e := audit.Event{By: By, Action: action, Result: audit.Succeeded, Source: source}
		e.Actor.Name, e.Actor.ID = actor, fmt.Sprintf("%d", len(actor)*100+int(actor[0]))
		return e
	}
	first := []audit.Event{
		event("ann", audit.SourceAdded, "a"),
		event("bob", audit.SourceAdded, "b"),
		event("Ann", audit.SourceNewVersion, "a"),
	}
	sudo := event("svc", audit.SourceNewVersion, "")
	sudo.Actor.SudoUser, sudo.Actor.SudoUserID = "bob", "398"
	second := []audit.Event{
		event("ann", audit.SourceRemoved, "b"),
		sudo,
	}
	if err := s.RecordEvents(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	// Events are recorded to the microsecond: wait, so that the second
	// batch is recorded later.
	time.Sleep(2 * time.Millisecond)
	if err := s.RecordEvents(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	all := listEvents(t, s, audit.Filter{})
	if len(all.Events) != 5 || all.More {
		t.Fatalf("Events = %d events, more %v; want 5", len(all.Events), all.More)
	}
	split := all.Events[3].OccurredAt
	ids := func(p audit.Page) string {
		var b strings.Builder
		for _, e := range p.Events {
			for i, r := range all.Events {
				if r.ID == e.ID {
					fmt.Fprintf(&b, "%d", i)
				}
			}
		}
		if p.More {
			b.WriteString("+")
		}
		return b.String()
	}
	tests := []struct {
		name   string
		filter audit.Filter
		want   string
	}{
		{"everything", audit.Filter{}, "01234"},
		{"an actor, exactly", audit.Filter{Actor: "ann"}, "03"},
		{"an actor no event has", audit.Filter{Actor: "carol"}, ""},
		{"an actor, through sudo too", audit.Filter{Actor: "bob"}, "14"},
		{"an actor by ID, through sudo too", audit.Filter{Actor: "398"}, "14"},
		{"the account sudo ran as", audit.Filter{Actor: "svc"}, "4"},
		{"an action", audit.Filter{Actions: []audit.Action{audit.SourceNewVersion}}, "24"},
		{"actions", audit.Filter{Actions: []audit.Action{audit.SourceAdded, audit.SourceRemoved}}, "013"},
		{"a source", audit.Filter{Source: "a"}, "02"},
		{"since", audit.Filter{Since: split}, "34"},
		{"until", audit.Filter{Until: split}, "012"},
		{"since a time between microseconds", audit.Filter{Since: split.Add(-time.Nanosecond)}, "34"},
		{"until a time between microseconds", audit.Filter{Until: split.Add(time.Nanosecond)}, "01234"},
		{"since and until, the wrong way round", audit.Filter{Since: split, Until: split}, ""},
		{"everything together", audit.Filter{Actor: "bob", Actions: []audit.Action{audit.SourceAdded}, Source: "b", Until: split}, "1"},
		{"the latest two", audit.Filter{Limit: 2}, "34+"},
		{"the latest of an action", audit.Filter{Actions: []audit.Action{audit.SourceAdded}, Limit: 1}, "1+"},
		{"a limit of all of them", audit.Filter{Limit: 5}, "01234"},
		{"the greatest limit", audit.Filter{Limit: math.MaxInt}, "01234"},
	}
	for _, tt := range tests {
		if got := ids(listEvents(t, s, tt.filter)); got != tt.want {
			t.Errorf("Events with %s = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func testConcurrentEvents(t *testing.T, s store.Store) {
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, 3*n)
	for i := range n {
		wg.Go(func() {
			// Every goroutine records the first fetch of one source, and
			// of a source of its own, at once.
			errs <- s.RecordFetch(t.Context(), By, success("shared", "v1", t0, "a.yaml"))
			errs <- s.RecordFetch(t.Context(), By, success(fmt.Sprintf("own%d", i), "v1", t0, "a.yaml"))
			errs <- s.RecordEvents(t.Context(), []audit.Event{{By: By, Action: "test.done", Result: audit.Succeeded}})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	all := listEvents(t, s, audit.Filter{}).Events
	count := map[string]int{}
	seen := map[int64]bool{}
	for _, e := range all {
		count[string(e.Action)+" "+e.Source]++
		if seen[e.ID] {
			t.Errorf("two events have ID %d", e.ID)
		}
		seen[e.ID] = true
	}
	if count["source.added shared"] != 1 || count["source.new_version shared"] != 0 {
		t.Errorf("the shared source was added %d times, with %d new versions; want once, none",
			count["source.added shared"], count["source.new_version shared"])
	}
	if count["test.done "] != n || len(all) != 2*n+1 {
		t.Errorf("%d events, %d of them test.done; want %d, %d", len(all), count["test.done "], 2*n+1, n)
	}

	// Fetches of one recorded source, each of a new version, recorded at
	// once: each records the version before it as the fetch recorded
	// before it left it, so that the versions chain, in the order the
	// events were recorded, to the snapshot's.
	var versions sync.WaitGroup
	errs = make(chan error, n)
	for i := range n {
		versions.Go(func() {
			errs <- s.RecordFetch(t.Context(), By, success("shared", fmt.Sprintf("v%d", i+2), t0.Add(time.Duration(i+1)*time.Minute), "a.yaml"))
		})
	}
	versions.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	chain := listEvents(t, s, audit.Filter{Source: "shared", Actions: []audit.Action{audit.SourceNewVersion}}).Events
	if len(chain) != n {
		t.Fatalf("%d new versions of the shared source, want %d", len(chain), n)
	}
	was := "v1"
	for _, e := range chain {
		var d struct{ Before, After string }
		if err := json.Unmarshal(e.Details, &d); err != nil {
			t.Fatal(err)
		}
		if d.Before != was {
			t.Errorf("event %d: before %q, want %q, the version the event before it left", e.ID, d.Before, was)
		}
		was = d.After
	}
	if snap := snapshot(t, s, "shared"); snap.Version != was {
		t.Errorf("the snapshot is at %s, but the last new version recorded is %s", snap.Version, was)
	}
}
