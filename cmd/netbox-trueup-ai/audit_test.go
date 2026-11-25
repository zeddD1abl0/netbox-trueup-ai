package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/audit"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/engine"
)

// testBy is who commands act as in tests, unless a test sets whoami.
var testBy = audit.By{
	Actor:   audit.Actor{Kind: audit.OSUser, Name: "tester", ID: "1000", Host: "test-host"},
	Through: audit.CommandLine,
}

// auditLog runs audit with args, as JSON, and returns its result.
func auditLog(t *testing.T, env []string, args ...string) auditJSON {
	t.Helper()
	r := runCLI(t, nil, env, append([]string{"audit", "-o", "json"}, args...)...)
	if r.code != exitOK {
		t.Fatalf("audit: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	var got auditJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("audit's JSON: %v\n%s", err, r.stdout)
	}
	return got
}

// eventSummary is what the tests compare of an event.
type eventSummary struct {
	Action, Source, Actor, Details string
}

// summarize returns what the tests compare of events, with their details
// as compact JSON, as they are recorded: audit indents its output.
func summarize(t *testing.T, events []eventJSON) []eventSummary {
	t.Helper()
	out := make([]eventSummary, len(events))
	for i, e := range events {
		var details bytes.Buffer
		if err := json.Compact(&details, e.Details); err != nil {
			t.Fatal(err)
		}
		out[i] = eventSummary{Action: e.Action, Source: *e.Source, Actor: *e.Actor.Name, Details: details.String()}
	}
	return out
}

func TestAuditRecordsSourceChanges(t *testing.T) {
	t.Parallel()
	repoURL, pool, commit := gitRepo(t)
	lib := `{"name":"lib","kind":"git","url":"` + repoURL + `","format":"devicetype-library"}`
	env, dataDir := sourcesEnv(t, `[`+lib+`]`)
	fetch := func(env []string) {
		t.Helper()
		if r := runCLIApp(t, nil, trust(pool), env, "source", "fetch"); r.code != exitOK {
			t.Fatalf("source fetch: exit code %d\nstderr: %s", r.code, r.stderr)
		}
	}
	fetch(env)
	// The same version again changes nothing.
	fetch(env)
	// Another ref, which resolves to the same commit.
	reffed := `{"name":"lib","kind":"git","url":"` + repoURL + `","ref":"master","format":"devicetype-library"}`
	fetch([]string{"TRUEUP_DATA_DIR=" + dataDir, "TRUEUP_SOURCES=[" + reffed + "]"})
	// lib no longer configured, and other in its place.
	other := `{"name":"other","kind":"git","url":"` + repoURL + `","format":"devicetype-library"}`
	fetch([]string{"TRUEUP_DATA_DIR=" + dataDir, "TRUEUP_SOURCES=[" + other + "]"})

	got := auditLog(t, env)
	conf := func(ref string) string {
		return `{"kind":"git","url":"` + repoURL + `","ref":"` + ref + `"}`
	}
	want := []eventSummary{
		{"source.added", "lib", "tester", `{"kind":"git","url":"` + repoURL + `","ref":"","version":"` + commit + `","resolved_ref":"master"}`},
		{"source.reconfigured", "lib", "tester", `{"before":` + conf("") + `,"after":` + conf("master") + `}`},
		{"source.added", "other", "tester", `{"kind":"git","url":"` + repoURL + `","ref":"","version":"` + commit + `","resolved_ref":"master"}`},
		{"source.removed", "lib", "tester", `{"kind":"git","url":"` + repoURL + `","ref":"master","snapshot":{"url":"` + repoURL + `","ref":"master","version":"` + commit + `"}}`},
	}
	if diff := cmp.Diff(want, summarize(t, got.Events)); diff != "" {
		t.Errorf("audit (-want +got):\n%s", diff)
	}
	if got.More {
		t.Error("audit says more events match")
	}
	for _, e := range got.Events {
		if e.Actor.Kind != "os_user" || *e.Actor.ID != "1000" || *e.Actor.Host != "test-host" || e.Through != "cli" || e.Result != "succeeded" {
			t.Errorf("event %d: %+v, want tester's success at the command line", e.ID, e)
		}
	}

	// Filters.
	if got := summarize(t, auditLog(t, env, "--source", "lib", "--action", "source.removed", "--action", "source.reconfigured").Events); len(got) != 2 ||
		got[0].Action != "source.reconfigured" || got[1].Action != "source.removed" {
		t.Errorf("audit --source lib with two actions = %+v", got)
	}
	latest := auditLog(t, env, "--limit", "1")
	if len(latest.Events) != 1 || latest.Events[0].Action != "source.removed" || !latest.More {
		t.Errorf("audit --limit 1 = %+v, want the removal and more", latest)
	}
	if got := auditLog(t, env, "--since", "1h", "--until", "2999-01-01T00:00:00Z", "--actor", "tester"); len(got.Events) != 4 {
		t.Errorf("audit with times and actor = %d events, want 4", len(got.Events))
	}
	if got := auditLog(t, env, "--since", "2999-01-01T00:00:00Z"); len(got.Events) != 0 {
		t.Errorf("audit since the future = %d events, want none", len(got.Events))
	}
	if got := auditLog(t, env, "--actor", "Tester"); len(got.Events) != 0 {
		t.Errorf("audit --actor Tester = %d events, want none: names match exactly", len(got.Events))
	}
	if got := auditLog(t, env, "--actor", "1000"); len(got.Events) != 4 {
		t.Errorf("audit --actor 1000 = %d events, want 4: an ID matches too", len(got.Events))
	}
}

func TestAuditText(t *testing.T) {
	t.Parallel()
	env := []string{"TRUEUP_DATA_DIR=" + t.TempDir()}
	r := runCLI(t, nil, env, "audit")
	if r.code != exitOK || r.stdout != "No audit events match.\n" {
		t.Errorf("audit of an empty log: exit code %d, stdout %q\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if got := auditLog(t, env); got.Events == nil || len(got.Events) != 0 || got.More {
		t.Errorf("audit -o json of an empty log = %+v, want an empty list", got)
	}

	dataDir := strings.TrimPrefix(env[0], "TRUEUP_DATA_DIR=")
	s, _, err := engine.Open(t.Context(), engine.Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	odd := testBy
	odd.Actor.Name, odd.Actor.Host, odd.Actor.SudoUser, odd.Actor.SudoUserID = "root", "host\x1b[31m", "carol", "1000"
	odd.Actor.ID = "0"
	events := []audit.Event{
		{
			By: testBy, Action: audit.SourceAdded, Result: audit.Succeeded, Source: "lib",
			Details: json.RawMessage(`{"kind":"git","url":"https://git.example.com/lib.git","ref":"","version":"abc"}`),
		},
		{By: odd, Action: "netbox.write", Result: audit.Started, ObjectType: "dcim.devicetype", Object: "Dell / R640"},
		{By: audit.By{Actor: audit.Actor{Kind: audit.OSUser, ID: "1234"}, Through: audit.CommandLine}, Action: audit.SourceRemoved, Result: audit.Succeeded},
	}
	if err := s.RecordEvents(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	// A result completes the event recorded as started, by its ID.
	result := audit.Event{
		By: odd, Action: "netbox.write", Result: audit.Failed, Completes: 2, Error: "refused\u202e",
		ObjectType: "dcim.devicetype", Object: "Dell / R640", NetBoxURL: "https://netbox.example.com/", NetBoxBranch: "a1b2c3d4", NetBoxID: 7,
		Details: json.RawMessage(`{"before":{"u_height":1},"note":"line\nbreak"}`),
	}
	if err := s.RecordEvents(t.Context(), []audit.Event{result}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	r = runCLI(t, nil, env, "audit")
	if r.code != exitOK {
		t.Fatalf("audit: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	stamp := regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ`)
	got := stamp.ReplaceAllString(r.stdout, "TIME")
	want := `#1  TIME  source.added  succeeded
    by tester (uid 1000), on test-host, at the command line
    about source lib
    kind: git
    url: https://git.example.com/lib.git
    ref: ""
    version: abc

#2  TIME  netbox.write  started
    by root (uid 0), through sudo from carol (uid 1000), on "host\x1b[31m", at the command line
    about dcim.devicetype Dell / R640

#3  TIME  source.removed  succeeded
    by uid 1234, at the command line

#4  TIME  netbox.write  failed, completing #2
    by root (uid 0), through sudo from carol (uid 1000), on "host\x1b[31m", at the command line
    about dcim.devicetype Dell / R640, NetBox https://netbox.example.com/ branch a1b2c3d4 ID 7
    error: "refused\u202e"
    before: {"u_height":1}
    note: "line\nbreak"
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("audit (-want +got):\n%s", diff)
	}

	r = runCLI(t, nil, env, "audit", "--limit", "2")
	if !strings.HasPrefix(r.stdout, "The latest 2 events that match; --limit 0 lists them all.\n\n#3 ") {
		t.Errorf("audit --limit 2 doesn't say more match:\n%s", r.stdout)
	}
}

// TestAuditNobodyFetchesNothing checks that a command that can't tell who
// runs it changes nothing: an action nobody can be named for isn't taken.
func TestAuditNobodyFetchesNothing(t *testing.T) {
	t.Parallel()
	repoURL, pool, _ := gitRepo(t)
	env, dataDir := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library"}]`)
	nobody := func(a *app) {
		a.sourceRootCAs = pool
		a.whoami = func() (audit.By, error) { return audit.By{}, errors.New("no token") }
	}
	r := runCLIApp(t, nil, nobody, env, "source", "fetch")
	if r.code != exitFailure || !strings.Contains(r.stderr, "no token") {
		t.Errorf("source fetch by nobody: exit code %d, stderr %q; want %d and the reason", r.code, r.stderr, exitFailure)
	}
	s, _, err := engine.Open(t.Context(), engine.Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if sources, err := s.Sources(t.Context()); err != nil || len(sources) != 0 {
		t.Errorf("Sources = %v, %v; want none recorded", sources, err)
	}
}

func TestAuditFlags(t *testing.T) {
	t.Parallel()
	env := []string{"TRUEUP_DATA_DIR=" + t.TempDir()}
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"--since", "yesterday"}, `--since "yesterday" is neither an RFC 3339 time`},
		{[]string{"--until", "-5m"}, `--until "-5m" is a duration back from now that is negative`},
		{[]string{"--action", "source.deleted"}, "--action source.deleted isn't an action TrueUp records; it records source.added, source.new_version, source.reconfigured, source.removed"},
		{[]string{"--limit", "-1"}, "--limit must be 0, for all, or more, not -1"},
		{[]string{"extra"}, `takes no arguments, but got "extra"`},
		{[]string{"--since="}, "--since needs a time"},
		{[]string{"--until", ""}, "--until needs a time"},
		{[]string{"--actor", ""}, "--actor needs a user's name or ID"},
		{[]string{"--source="}, "--source needs a source's name"},
		{[]string{"--actor", "a\xffb"}, `--actor "a\xffb" isn't UTF-8 text`},
		{[]string{"--source", "a\x00b"}, `--source "a\x00b" isn't UTF-8 text`},
		{[]string{"--since", "1h", "--until", "7d"}, `--since "1h" isn't before --until "7d", so no event could match`},
		{[]string{"--since", "2026-11-23T09:30:00Z", "--until", "2026-11-23T19:30:00+10:00"}, `isn't before --until`},
		{[]string{"--until", "9999-12-31T23:59:59-10:00"}, `is later than the last time the audit log can be searched by, 9999-12-31T23:59:59.999999Z`},
	} {
		r := runCLI(t, nil, env, append([]string{"audit"}, tt.args...)...)
		if r.code != exitUsage || !strings.Contains(r.stderr, tt.want) {
			t.Errorf("audit %q: exit code %d, stderr %q; want %d and %q", tt.args, r.code, r.stderr, exitUsage, tt.want)
		}
	}
}

func TestAuditFilter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	got, err := auditFilter(now, "7d", "2026-11-23T19:30:00+10:00", "ann", "lib",
		[]string{"source.added", "source.removed", "source.added"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := audit.Filter{
		Since: now.Add(-7 * 24 * time.Hour), Until: time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC),
		Actor: "ann", Source: "lib", Actions: []audit.Action{audit.SourceAdded, audit.SourceRemoved},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("auditFilter (-want +got):\n%s", diff)
	}
}

// TestAuditActsAsTheProcessUser checks that a command acts as the
// operating system user running it, and records sudo's user from the
// command's environment.
// TestByInterrupted checks that a command interrupted while it finds who
// runs it says so, rather than that it can't tell.
func TestByInterrupted(t *testing.T) {
	t.Parallel()
	a := &app{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := a.by(ctx)
	if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "interrupted while finding who runs this command") {
		t.Errorf("by with ctx ended = %v, want it interrupted", err)
	}
}

func TestAuditActsAsTheProcessUser(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("users are found by their security identifier on Windows")
	}
	repoURL, pool, _ := gitRepo(t)
	env, _ := sourcesEnv(t, `[{"name":"lib","kind":"git","url":"`+repoURL+`","format":"devicetype-library"}]`)
	env = append(env, "SUDO_UID=99999", "SUDO_USER=alice", "SUDO_USER=someone-else", "USER=mallory")
	asProcess := func(a *app) {
		a.sourceRootCAs = pool
		a.whoami = nil
	}
	if r := runCLIApp(t, nil, asProcess, env, "source", "fetch"); r.code != exitOK {
		t.Fatalf("source fetch: exit code %d\nstderr: %s", r.code, r.stderr)
	}
	got := auditLog(t, env).Events
	if len(got) != 1 {
		t.Fatalf("%d events, want 1", len(got))
	}
	a := got[0].Actor
	if a.Kind != "os_user" || a.ID == nil || *a.ID != strconv.Itoa(os.Getuid()) {
		t.Errorf("actor %+v, want this process's user ID %d", a, os.Getuid())
	}
	if a.Name != nil && *a.Name == "mallory" {
		t.Error("the actor's name came from $USER")
	}
	if a.SudoUser == nil || *a.SudoUser != "alice" {
		t.Errorf("sudo user %v, want alice, the first SUDO_USER, as getenv reads it", a.SudoUser)
	}
	if a.SudoUserID == nil || *a.SudoUserID != "99999" {
		t.Errorf("sudo user ID %v, want 99999", a.SudoUserID)
	}
}

// FuzzAuditText checks that audit's text never passes a character that
// could drive or mislead a terminal, nor bytes that aren't UTF-8, whatever
// an event's text and details hold, and that each event keeps its lines.
func FuzzAuditText(f *testing.F) {
	f.Add("host", "error", "key", "value", "dcim.devicetype", "Dell / R640")
	f.Add("h\x1b[2J", "e\u202e", "k\x00", "v\xff", "\u2028", "\u200b")
	f.Fuzz(func(t *testing.T, host, errText, key, value, objectType, object string) {
		details, err := json.Marshal(map[string]any{key: value, "n": map[string]string{key: value}})
		if err != nil {
			return
		}
		// The action and result as well, which a row written to the
		// database directly, not through the store, can hold.
		e := audit.Recorded{ID: 1, OccurredAt: time.Unix(0, 0).UTC(), Event: audit.Event{
			By:     audit.By{Actor: audit.Actor{Kind: audit.OSUser, Name: host, ID: host, Host: host, SudoUser: host, SudoUserID: host}, Through: audit.CommandLine, ClientAddress: host},
			Action: audit.Action(objectType), Result: audit.Result(object), Error: errText, Source: host,
			ObjectType: objectType, Object: object, NetBoxURL: host, NetBoxBranch: host, NetBoxID: 3, Details: details,
		}}
		var out strings.Builder
		if err := writeAuditText(&out, audit.Page{Events: []audit.Recorded{e}}, 0); err != nil {
			t.Fatal(err)
		}
		s := out.String()
		if !utf8.ValidString(s) {
			t.Errorf("output isn't UTF-8: %q", s)
		}
		for _, r := range s {
			if r != '\n' && unsafeRune(r) {
				t.Errorf("output holds %U: %q", r, s)
			}
		}
		// Nothing a value holds starts a line of its own: after the
		// heading, every line is indented, under the event.
		lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		if len(lines) < 3 || !strings.HasPrefix(lines[0], "#1  ") {
			t.Fatalf("output doesn't start with the event's heading and who acted: %q", s)
		}
		for _, line := range lines[1:] {
			if !strings.HasPrefix(line, "    ") {
				t.Errorf("line %q isn't indented under the event: %q", line, s)
			}
		}
	})
}

func TestAuditTextHeading(t *testing.T) {
	t.Parallel()
	e := audit.Recorded{ID: 2, OccurredAt: time.Unix(0, 0).UTC(), Event: audit.Event{By: testBy, Action: audit.SourceAdded, Result: audit.Succeeded}}
	for _, tt := range []struct {
		limit int
		want  string
	}{
		{1, "The latest event that matches; --limit 0 lists them all.\n\n#2 "},
		{2, "The latest 2 events that match; --limit 0 lists them all.\n\n#2 "},
	} {
		var b strings.Builder
		if err := writeAuditText(&b, audit.Page{Events: []audit.Recorded{e}, More: true}, tt.limit); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(b.String(), tt.want) {
			t.Errorf("with --limit %d: %q, want it to start %q", tt.limit, b.String(), tt.want)
		}
	}
}

func TestDescribe(t *testing.T) {
	t.Parallel()
	by := func(name, id, sudoUser, sudoID string) audit.By {
		return audit.By{Actor: audit.Actor{Kind: audit.OSUser, Name: name, ID: id, Host: "ops1", SudoUser: sudoUser, SudoUserID: sudoID}, Through: audit.CommandLine}
	}
	for _, tt := range []struct {
		by   audit.By
		want string
	}{
		{by("carol", "1000", "", ""), "carol (uid 1000), on ops1, at the command line"},
		{by(`CORP\carol`, "S-1-5-21-1-2-3-1001", "", ""), `CORP\carol (S-1-5-21-1-2-3-1001), on ops1, at the command line`},
		// sudo's variables, set by hand to read as more of the line.
		{
			by("alice", "1000", "root (uid 0), on db-prod, at the command line;", "0"),
			`alice (uid 1000), through sudo from "root (uid 0), on db-prod, at the command line;" (uid 0), on ops1, at the command line`,
		},
		{by("alice", "1000", "", "0), on db-prod"), `alice (uid 1000), through sudo from "0), on db-prod", on ops1, at the command line`},
	} {
		if got := describeBy(tt.by); got != tt.want {
			t.Errorf("describeBy = %s, want %s", got, tt.want)
		}
	}
	for _, tt := range []struct {
		e    audit.Event
		want string
	}{
		{audit.Event{ObjectType: "dcim.devicetype", Object: "Dell / R640"}, "dcim.devicetype Dell / R640"},
		{audit.Event{Object: " Dell / R640 "}, `" Dell / R640 "`},
		{audit.Event{ObjectType: "dcim.devicetype"}, "dcim.devicetype"},
	} {
		if got := describeSubject(tt.e); got != tt.want {
			t.Errorf("describeSubject(%+v) = %s, want %s", tt.e, got, tt.want)
		}
	}
}

func TestDetailLines(t *testing.T) {
	t.Parallel()
	got, err := detailLines(json.RawMessage(`{"before":null,"after":"v1","n":1,"o":{"a":[1,2]},"s":"a\u202eb","":"","ref":"null","one":"1","q":"\"a\""}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"before: null", "after: v1", "n: 1", `o: {"a":[1,2]}`, `s: "a\u202eb"`, `: ""`,
		// Strings that read as other JSON values, quoted.
		`ref: "null"`, `one: "1"`, `q: "\"a\""`,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("detailLines (-want +got):\n%s", diff)
	}
	for _, bad := range []string{`[1]`, `"x"`, `{"a":`, `{1:2}`} {
		if _, err := detailLines(json.RawMessage(bad)); err == nil {
			t.Errorf("detailLines(%s) succeeded", bad)
		}
	}
}
