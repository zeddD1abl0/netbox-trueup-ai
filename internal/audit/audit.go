// Package audit describes TrueUp's audit log: who acted, what they did, when,
// through what, about what, and with what result
// (docs/adr/0014-audit-every-action.md, docs/adr/0034-audit-what-changes-what-trueup-manages.md).
//
// It holds the log's vocabulary, finds the operating system user who runs
// a command, and reads the times the audit command takes. The store records
// events, each in the same transaction as the change it describes, and never
// changes or deletes one.
package audit

import (
	"encoding/json"
	"strings"
	"time"
)

// ActorKind says how an actor was identified. Its values are part of the
// CLI's JSON output.
type ActorKind string

const (
	// OSUser is a person at the command line, identified as the operating
	// system user running it. The web UI's signed-in users (M3) and the
	// system actors of schedules (M6) and webhooks (M7) join it later.
	OSUser ActorKind = "os_user"
)

// Actor is who acted.
type Actor struct {
	Kind ActorKind
	// Name is the user's name, such as jordan or CORP\jordan, or "" where
	// the system's user database has no entry for the user's ID.
	Name string
	// ID is the user's ID: a Unix user ID, such as 1000, or a Windows
	// security identifier.
	ID string
	// Host is the name of the host the actor acted on, or "" where it
	// couldn't be read.
	Host string
	// SudoUser is, for a command run through sudo, the user who ran sudo,
	// as sudo's environment reports it, or "" otherwise.
	SudoUser string
}

// Through says how an action reached TrueUp. Its values are part of the
// CLI's JSON output.
type Through string

const (
	// CommandLine is a command run on the host. The web UI, schedules and
	// webhooks join it later.
	CommandLine Through = "cli"
)

// Action is what was done, as the object acted on and a verb, such as
// source.added. Its values are part of the CLI's JSON output.
type Action string

// The actions audited so far. Later milestones add their own.
const (
	// SourceAdded is a source TrueUp records for the first time, when it
	// is first fetched.
	SourceAdded Action = "source.added"
	// SourceReconfigured is a source fetched with another kind, URL or ref
	// than its last fetch.
	SourceReconfigured Action = "source.reconfigured"
	// SourceNewVersion is a source whose fetch found another version than
	// the snapshot TrueUp held, or a first one.
	SourceNewVersion Action = "source.new_version"
	// SourceRemoved is a source no longer configured, whose records TrueUp
	// removed.
	SourceRemoved Action = "source.removed"
)

// Result is how an action ended. Its values are part of the CLI's JSON
// output.
type Result string

const (
	Succeeded Result = "succeeded"
	Failed    Result = "failed"
	// Started is an action, such as a write to NetBox, recorded before it
	// is done, since its result can't share a transaction with it. Another
	// event completes it.
	Started Result = "started"
)

// By is where an action came from: who acted, and how the action reached
// TrueUp.
type By struct {
	Actor   Actor
	Through Through
	// ClientAddress is the address a request came from, for actions
	// through the web UI or a webhook, or "".
	ClientAddress string
}

// CommandLineBy returns where an action run at the command line comes from:
// the operating system user running this process (CommandLineActor).
func CommandLineBy(getenv func(string) string) By {
	return By{Actor: CommandLineActor(getenv), Through: CommandLine}
}

// Event is one action, as recorded.
type Event struct {
	By
	Action Action
	Result Result
	// Completes is, for the result of an action recorded as started, the
	// started event's ID, or 0.
	Completes int64
	// Error says why the action failed, or is "".
	Error string

	// What the event is about, for filtering: a source's name; an object's
	// type and identity, such as dcim.devicetype and Dell / PowerEdge R640;
	// and the NetBox it is in, its branch's schema ID or "" for main, and
	// its ID there, or 0. Each is "" or 0 where it doesn't apply.
	Source       string
	ObjectType   string
	Object       string
	NetBoxURL    string
	NetBoxBranch string
	NetBoxID     int64

	// Details holds the rest, as a JSON object: before and after values,
	// versions, counts. nil means none.
	Details json.RawMessage
}

// Recorded is an event as the store holds it.
type Recorded struct {
	// ID orders events: each is greater than those recorded before it, and
	// never reused.
	ID int64
	// OccurredAt is when it was recorded, in UTC, to the microsecond.
	OccurredAt time.Time
	Event
}

// Filter chooses the events to list.
type Filter struct {
	// Since and Until bound when the events were recorded: at Since or
	// later, and before Until. A zero time bounds nothing.
	Since, Until time.Time
	// Actor is an actor's name, Actions the actions, and Source a
	// source's name, each matched exactly; empty matches any.
	Actor   string
	Actions []Action
	Source  string
	// Limit is the most events to list, the latest that match; 0 lists
	// them all.
	Limit int
}

// Page is the events a filter chose, oldest first.
type Page struct {
	Events []Recorded
	// More says that more events match than Limit allowed: those before
	// the first listed.
	More bool
}

// Known reports whether k is a kind of actor this release records.
func (k ActorKind) Known() bool { return k == OSUser }

// Known reports whether t is a channel this release records.
func (t Through) Known() bool { return t == CommandLine }

// Known reports whether r is a result.
func (r Result) Known() bool { return r == Succeeded || r == Failed || r == Started }

// Actions returns the actions this release records, sorted.
func Actions() []Action {
	return []Action{SourceAdded, SourceNewVersion, SourceReconfigured, SourceRemoved}
}

// WellFormed reports whether a is an object and a verb, each of lowercase
// letters and underscores, joined by a dot, such as source.new_version.
func (a Action) WellFormed() bool {
	object, verb, ok := strings.Cut(string(a), ".")
	return ok && word(object) && word(verb)
}

func word(s string) bool {
	return s != "" && s[0] != '_' && !strings.ContainsFunc(s, func(r rune) bool { return (r < 'a' || r > 'z') && r != '_' })
}
