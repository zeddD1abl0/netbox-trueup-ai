---
status: accepted
date: 2026-11-23
decision-makers: "@zeddD1abl0"
---

# Audit what changes what TrueUp manages, as the operating system user at the command line, in the change's own transaction

## Context and problem

[ADR 0014](0014-audit-every-action.md) records every action that changes TrueUp's state or NetBox in an append-only audit log, with who acted, what, when, through what and with what result. Milestone 2 builds the log, and its commands are the first to record actions: imports, plans, applies and writes to NetBox come in the steps after this one. Before they do, four things need deciding. Which actions count, when nearly every command records something? Who is the actor at the command line, where people and service accounts both run commands, often through sudo? How does an event stay with the change it describes? And how does the log stay append-only?

## Decision drivers

- The log answers who changed what TrueUp manages, what NetBox holds, or what TrueUp reads, and when. It isn't a copy of TrueUp's other records.
- An actor can't be named from anything the actor can set.
- An event is recorded exactly when its change is: never one without the other.
- A bug in TrueUp can't rewrite history.
- A schedule fetching every few minutes (milestone 6) doesn't bury the log.

## Considered options

What is audited:

1. Actions that change what TrueUp manages, what NetBox holds, or what sources it reads
2. Every command that writes to the database, fetches, loads and comparisons included

Who acts at the command line:

1. The user of the process's real user ID, from the system's user database, with sudo's user beside it
2. The user `user.Current` returns
3. `$USER`, or `$LOGNAME`

Recording:

1. In the change's own transaction
2. In a transaction after the change

## Decision

Chosen options: "actions that change what TrueUp manages, NetBox holds or TrueUp reads", "the user of the real user ID, with sudo's user beside it", and "in the change's own transaction".

- **What is audited.** A source added, its kind, URL or ref changed, a new version fetched, and its records removed (this step); imports, links, unlinks and changes of primary; runs and plans; applies, and each write to NetBox, recorded as started and as completed; branches created, merged and deleted ([ADR 0033](0033-runs-against-main-or-a-chosen-branch.md)). Fetches that find the version TrueUp holds, failed fetches that change nothing, loads, comparisons and lists aren't audited: they refresh TrueUp's copies of a source and of NetBox, their latest result is in their own tables, and a plan records the versions it used. Schema migrations aren't either: goose's table and `migrate` record them. An action refused before it changed anything isn't recorded; a write to NetBox that fails is, since NetBox may hold part of it.
- **Who.** At the command line, the actor is the operating system user of the process's real user ID, named from the system's user database, never from `$USER` or `$LOGNAME`. TrueUp's releases are built without cgo, where Go's `user.Current` falls back to `$USER` for a user ID the database lacks, as in a container run with an arbitrary user, so TrueUp looks the ID up itself, and records the ID alone when the database has no entry for it. On Windows, the account comes from the process's token, with its security identifier. The host's name is recorded too, since several hosts can share PostgreSQL. A command run through sudo, as another user than the one who ran sudo, records sudo's `SUDO_USER` beside the actor, as "through sudo from". sudo sets it; the process's own user could set it too, but it never replaces the actor, and root, or a service account people reach through sudo, can change the database anyway.
- **One table, append-only.** `audit_events` holds one row per event, ordered by an ID that is never reused, with when it was recorded, the actor, how the action reached TrueUp, the action, its result and error, what it is about (a source, an object, the NetBox, branch and ID it is in), and the rest as a JSON object. Triggers on both engines refuse to change or delete an event: they guard against a bug in TrueUp, not against someone who can change the database, which streaming the log out (milestone 7) answers. Nothing refers to an event by a foreign key, and an event refers to nothing so: events outlive what they describe. A write to NetBox can't share a transaction with TrueUp's records, so its start and its result are two events, the result naming the start; recovery finds starts without a result.
- **In the change's own transaction.** A store method that changes what is audited takes who acted, and records its events in its own transaction: a change and its event commit together, or neither does. A method whose change records no event still checks who acted, so a missing actor is found at once. Events with no change of TrueUp's own, such as a write to NetBox, are recorded on their own.
- **Reading the log.** `netbox-trueup-ai audit` lists the latest events that match, oldest first: by default the latest 100. It filters by time, given in RFC 3339 or as a duration back from now, by an actor's name, by actions, and by source, and prints text or JSON. Reading the log isn't audited.
- **Retention.** None yet: the log grows. Pruning after a retention period, itself audited, comes with schedules (milestone 6) or with streaming the log out (milestone 7).

### Consequences

- Good, because the log holds decisions and their effects, not every refresh, so it stays readable as schedules arrive.
- Good, because a user can't name someone else as the actor by setting an environment variable.
- Good, because no change is recorded without its event, and no event without its change.
- Good, because a bug can't change or delete an event.
- Bad, because a service account that people run commands as, without sudo, is the only actor the log can name. Team deployments should act through the web UI (milestone 3).
- Bad, because without cgo Go reads only `/etc/passwd`, so on a host whose users come from a directory, such as LDAP through SSSD, TrueUp records them by user ID alone, without a name. The ID still identifies them; looking names up through the host's name service would mean running `getent`, or building with cgo.
- Bad, because whoever can change the database can still drop the triggers and rewrite the log. Copies streamed out are what make it hard to tamper with.
- Bad, because the log grows without bound until retention arrives.

## Pros and cons of the options

### Every command that writes to the database

- Good, because nothing is left out.
- Bad, because fetches and comparisons, which a schedule runs every few minutes, would outnumber every decision in the log, and their latest results are recorded already.

### `user.Current`

- Good, because it is one call.
- Bad, because without cgo it reads `$USER` when the user database lacks the user ID, so a user in such a container could name anyone.

### `$USER` or `$LOGNAME`

- Good, because it works everywhere.
- Bad, because the user sets it.

### In a transaction after the change

- Good, because store methods needn't know about the audit log.
- Bad, because a crash between the two loses the event, and a failure after it records an event for a change that never happened.

## More information

- Roadmap, milestone 2, step 16.
- Go's `os/user` (1.26 and 1.27), `lookup_stubs.go`: without cgo, `current()` returns `$USER` as the name when the user ID isn't in `/etc/passwd`; `lookup_unix.go` reads `/etc/passwd` alone.
