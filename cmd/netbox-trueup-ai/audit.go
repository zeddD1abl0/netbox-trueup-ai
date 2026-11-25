package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/audit"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

// defaultAuditLimit is how many events audit lists unless --limit says.
const defaultAuditLimit = 100

func newAuditCommand(a *app) *cobra.Command {
	var (
		out               output
		since, until      string
		actor, sourceName string
		actions           []string
		limit             int
	)
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "List the audit log: who changed what, and when",
		Long: `List the audit log, the record of every action that changes what TrueUp
manages, what NetBox holds, or what sources TrueUp reads: who acted, what they
did, when, through what, about what, and with what result. At the command
line, who acted is the operating system user running the command, found by
its user ID, never from $USER; a command run through sudo records the user
who ran sudo too, as sudo reports it.

Actions recorded so far:
  source.added         a source fetched for the first time
  source.reconfigured  a source fetched with another kind, URL or ref
  source.new_version   a source whose fetch found another version
  source.removed       a source no longer configured, whose records went

These are recorded by source fetch, and by list and compare with --fetch,
which fetch the same way. Fetches that find the version TrueUp holds, loads,
comparisons and lists change nothing that is audited. A command that can't
tell who runs it fetches nothing. Events are never changed or deleted.

The latest events that match are listed, oldest first: by default the latest
100, or as many as --limit says, or all with --limit 0. --since and --until
take an RFC 3339 time, such as 2026-11-23T09:30:00Z, or a duration back from
now, such as 90m, 24h or 7d: events recorded at --since or later, and before
--until. --actor matches a user's name or user ID, such as 1000, or those of
the user who ran sudo, exactly, case included: a user whose name the system's
user database lacks, as in some containers, is recorded by ID alone.

The database is created and migrated first if needed. Reading the log isn't
audited.

Exit codes:
  0  The events were listed, if there were any.
  1  The command couldn't run, such as when the database can't be opened.
  2  A flag is invalid, such as a time that isn't one, an action that isn't
     recorded, or a negative limit.
  3  The configuration is invalid, or database.url names a database this
     release can't use.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			for _, flag := range []string{"since", "until"} {
				if cmd.Flags().Changed(flag) && cmd.Flags().Lookup(flag).Value.String() == "" {
					return usageError{fmt.Errorf("--%s needs a time: RFC 3339, or a duration back from now, such as 24h", flag)}
				}
			}
			filter, err := auditFilter(time.Now(), since, until, actor, sourceName, actions, limit)
			if err != nil {
				return usageError{err}
			}
			cfg, err := a.loadConfig(cmd)
			if err != nil {
				return err
			}
			page, err := a.auditEvents(cmd.Context(), cfg, filter)
			if err != nil {
				return err
			}
			if out == outputJSON {
				return writeJSON(cmd.OutOrStdout(), auditResult(page))
			}
			return writeAuditText(cmd.OutOrStdout(), page, filter.Limit)
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "list events recorded at this `time` or later: RFC 3339, or a duration back from now")
	cmd.Flags().StringVar(&until, "until", "", "list events recorded before this `time`: RFC 3339, or a duration back from now")
	cmd.Flags().StringVar(&actor, "actor", "", "list events by the user with this `name` or user ID, or run through sudo by them, exactly")
	cmd.Flags().StringArrayVar(&actions, "action", nil, "list events of this `action`, such as source.added; repeat for more")
	cmd.Flags().StringVar(&sourceName, "source", "", "list events about the source with this `name`")
	cmd.Flags().IntVar(&limit, "limit", defaultAuditLimit, "list at most this `many` of the latest events; 0 for all")
	addOutputFlag(cmd, &out)
	return cmd
}

// auditFilter reads audit's flags into a filter, with now for times given
// as durations.
func auditFilter(now time.Time, since, until, actor, sourceName string, actions []string, limit int) (audit.Filter, error) {
	f := audit.Filter{Actor: actor, Source: sourceName, Limit: limit}
	if limit < 0 {
		return audit.Filter{}, fmt.Errorf("--limit must be 0, for all, or more, not %d", limit)
	}
	for _, flag := range []struct{ name, value string }{{"--actor", actor}, {"--source", sourceName}} {
		// Events hold UTF-8 text alone, which PostgreSQL insists on.
		if !utf8.ValidString(flag.value) || strings.ContainsRune(flag.value, 0) {
			return audit.Filter{}, fmt.Errorf("%s %s isn't UTF-8 text", flag.name, strconv.Quote(flag.value))
		}
	}
	for _, flag := range []struct {
		name, value string
		into        *time.Time
	}{{"--since", since, &f.Since}, {"--until", until, &f.Until}} {
		if flag.value == "" {
			continue
		}
		t, err := audit.ParseTime(flag.value, now)
		if err != nil {
			return audit.Filter{}, fmt.Errorf("%s %w", flag.name, err)
		}
		*flag.into = t
	}
	if !f.Since.IsZero() && !f.Until.IsZero() && !f.Since.Before(f.Until) {
		return audit.Filter{}, fmt.Errorf("--since %s isn't before --until %s, so no event could match", strconv.Quote(since), strconv.Quote(until))
	}
	known := audit.Actions()
	for _, name := range actions {
		action := audit.Action(name)
		if !slices.Contains(known, action) {
			names := make([]string, len(known))
			for i, k := range known {
				names[i] = string(k)
			}
			return audit.Filter{}, fmt.Errorf("--action %s isn't an action TrueUp records; it records %s", cell(name), strings.Join(names, ", "))
		}
		if !slices.Contains(f.Actions, action) {
			f.Actions = append(f.Actions, action)
		}
	}
	return f, nil
}

// auditEvents reads the events a filter chooses from the database.
func (a *app) auditEvents(ctx context.Context, cfg *config.Config, f audit.Filter) (_ audit.Page, err error) {
	db, _, err := a.openStore(ctx, cfg)
	if err != nil {
		return audit.Page{}, err
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	page, err := db.Events(ctx, f)
	if err != nil {
		if ctx.Err() != nil {
			return audit.Page{}, fmt.Errorf("interrupted while reading the audit log: %w", ctx.Err())
		}
		return audit.Page{}, err
	}
	return page, nil
}

// auditJSON is audit's result as JSON. Its fields are part of the CLI's
// public interface (ADR 0018).
type auditJSON struct {
	Events []eventJSON `json:"events"`
	// More says that more events match than --limit allowed: those before
	// the first listed.
	More bool `json:"more"`
}

// eventJSON is an audit event. A field that doesn't apply is null.
type eventJSON struct {
	ID            int64           `json:"id"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Actor         actorJSON       `json:"actor"`
	Through       string          `json:"through"`
	ClientAddress *string         `json:"client_address"`
	Action        string          `json:"action"`
	Result        string          `json:"result"`
	Completes     *int64          `json:"completes"`
	Error         *string         `json:"error"`
	Source        *string         `json:"source"`
	ObjectType    *string         `json:"object_type"`
	Object        *string         `json:"object"`
	NetBoxURL     *string         `json:"netbox_url"`
	NetBoxBranch  *string         `json:"netbox_branch"`
	NetBoxID      *int64          `json:"netbox_id"`
	Details       json.RawMessage `json:"details"`
}

type actorJSON struct {
	Kind       string  `json:"kind"`
	Name       *string `json:"name"`
	ID         *string `json:"id"`
	Host       *string `json:"host"`
	SudoUser   *string `json:"sudo_user"`
	SudoUserID *string `json:"sudo_user_id"`
}

func auditResult(page audit.Page) auditJSON {
	text := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	number := func(n int64) *int64 {
		if n == 0 {
			return nil
		}
		return &n
	}
	r := auditJSON{Events: make([]eventJSON, len(page.Events)), More: page.More}
	for i, e := range page.Events {
		r.Events[i] = eventJSON{
			ID:         e.ID,
			OccurredAt: e.OccurredAt,
			Actor: actorJSON{
				Kind: string(e.Actor.Kind), Name: text(e.Actor.Name), ID: text(e.Actor.ID),
				Host: text(e.Actor.Host), SudoUser: text(e.Actor.SudoUser), SudoUserID: text(e.Actor.SudoUserID),
			},
			Through:       string(e.Through),
			ClientAddress: text(e.ClientAddress),
			Action:        string(e.Action),
			Result:        string(e.Result),
			Completes:     number(e.Completes),
			Error:         text(e.Error),
			Source:        text(e.Source),
			ObjectType:    text(e.ObjectType),
			Object:        text(e.Object),
			NetBoxURL:     text(e.NetBoxURL),
			NetBoxBranch:  text(e.NetBoxBranch),
			NetBoxID:      number(e.NetBoxID),
			Details:       e.Details,
		}
	}
	return r
}

// writeAuditText writes each event as a few lines: its number, time,
// action and result; who acted, and through what; what it is about; and
// its details, one per line. Every value is escaped where it holds
// characters that would mislead the terminal, since a host's name or an
// error can come from outside TrueUp.
func writeAuditText(w io.Writer, page audit.Page, limit int) error {
	var b strings.Builder
	if len(page.Events) == 0 {
		b.WriteString("No audit events match.\n")
	}
	if page.More {
		fmt.Fprintf(&b, "The latest %d events that match; --limit 0 lists them all.\n\n", limit)
	}
	for i, e := range page.Events {
		if i > 0 {
			b.WriteByte('\n')
		}
		// The action and result too: the store checks them only as TrueUp
		// records them, and its schema doesn't for the action.
		fmt.Fprintf(&b, "#%d  %s  %s  %s", e.ID, e.OccurredAt.Format(time.RFC3339), cell(string(e.Action)), cell(string(e.Result)))
		if e.Completes != 0 {
			fmt.Fprintf(&b, ", completing #%d", e.Completes)
		}
		b.WriteByte('\n')
		fmt.Fprintf(&b, "    by %s\n", describeBy(e.By))
		if about := describeSubject(e.Event); about != "" {
			fmt.Fprintf(&b, "    about %s\n", about)
		}
		if e.Error != "" {
			fmt.Fprintf(&b, "    error: %s\n", cell(e.Error))
		}
		lines, err := detailLines(e.Details)
		if err != nil {
			return fmt.Errorf("event %d: %w", e.ID, err)
		}
		for _, line := range lines {
			fmt.Fprintf(&b, "    %s\n", line)
		}
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// describeBy says who acted, and how the action reached TrueUp.
func describeBy(by audit.By) string {
	var b strings.Builder
	b.WriteString(describeUser(by.Actor.Name, by.Actor.ID))
	if by.Actor.SudoUser != "" || by.Actor.SudoUserID != "" {
		fmt.Fprintf(&b, ", through sudo from %s", describeUser(by.Actor.SudoUser, by.Actor.SudoUserID))
	}
	if by.Actor.Host != "" {
		fmt.Fprintf(&b, ", on %s", cell(by.Actor.Host))
	}
	switch by.Through {
	case audit.CommandLine:
		b.WriteString(", at the command line")
	default:
		fmt.Fprintf(&b, ", through %s", cell(string(by.Through)))
	}
	if by.ClientAddress != "" {
		fmt.Fprintf(&b, " from %s", cell(by.ClientAddress))
	}
	return b.String()
}

// describeUser writes a user's name with its ID, or either alone where the
// other isn't known.
func describeUser(name, id string) string {
	switch {
	case name != "" && id != "":
		return fmt.Sprintf("%s (%s)", cell(name), describeID(id))
	case name != "":
		return cell(name)
	default:
		return describeID(id)
	}
}

// describeID writes a Unix user ID as one, and any other ID, such as a
// Windows security identifier, as it is.
func describeID(id string) string {
	if _, err := strconv.ParseUint(id, 10, 32); err == nil {
		return "uid " + id
	}
	return cell(id)
}

// describeSubject says what an event is about, or "" if nothing in
// particular.
func describeSubject(e audit.Event) string {
	var parts []string
	if e.Source != "" {
		parts = append(parts, "source "+cell(e.Source))
	}
	if e.ObjectType != "" || e.Object != "" {
		parts = append(parts, strings.TrimSpace(cell(e.ObjectType)+" "+cell(e.Object)))
	}
	if e.NetBoxURL != "" {
		where := "NetBox " + cell(e.NetBoxURL)
		if e.NetBoxBranch != "" {
			where += " branch " + cell(e.NetBoxBranch)
		}
		if e.NetBoxID != 0 {
			where += fmt.Sprintf(" ID %d", e.NetBoxID)
		}
		parts = append(parts, where)
	}
	return strings.Join(parts, ", ")
}

// detailLines writes an event's details a line per field, in the order the
// event holds them: a string as it is, escaped, and anything else as
// compact JSON.
func detailLines(details json.RawMessage) ([]string, error) {
	if len(details) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(details)))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("its details aren't a JSON object")
	}
	var lines []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("its details: %w", err)
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("its details aren't a JSON object")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("its details: %w", err)
		}
		var s string
		// null unmarshals into a string as "", which it isn't.
		if err := json.Unmarshal(value, &s); err == nil && !bytes.Equal(value, []byte("null")) {
			lines = append(lines, cell(key)+": "+cell(s))
			continue
		}
		lines = append(lines, cell(key)+": "+cell(string(value)))
	}
	return lines, nil
}
