---
status: accepted
date: 2026-10-23
decision-makers: "@zeddD1abl0"
---

# Record every action in an append-only audit log, and stream it out

## Context and problem

People need to know who synced what, and when, including who approved each change. Many organizations also want that record in their own log or SIEM systems. Actions come from people in the web UI and at the CLI, and from schedules and webhooks. NetBox's own changelog doesn't answer the question: it shows TrueUp's service account for every write, and it never sees actions that stay inside TrueUp, such as imports, override choices or settings changes.

## Decision drivers

- Every change is attributed to a person, or to a named automated trigger.
- The record can be queried in TrueUp and sent to external systems.
- No events are lost when an external system is unavailable.
- One mechanism serves both the audit log and notifications to other systems.

## Considered options

1. An append-only audit log in TrueUp's database, with an outbox that streams events to configured sinks
2. Structured log lines only, leaving storage to an external pipeline
3. NetBox's changelog only

## Decision

Chosen option: "An append-only audit log in TrueUp's database, with an outbox that streams events to configured sinks".

- **What is recorded.** Every action that changes TrueUp's state or NetBox records an audit event. It holds who acted and how they were identified, what they did (the action, the objects and plan, and before and after values where they matter), when, through what (UI, CLI, schedule or webhook, with the client address where there is one), and the result. Sign-ins, failed sign-ins, approvals and rejections, and changes to users, permissions, policies, sources and settings are recorded too. Events about a plan carry its change reference.
- **Who.** People are identified by single sign-on or a local account in the UI, and by their operating system user at the CLI. Schedules and webhooks act as named system actors, recorded with the schedule or delivery that triggered them.
- **Append-only.** TrueUp never changes or deletes audit events, apart from pruning after a configured retention period, and pruning is itself audited.
- **In NetBox.** Each write's changelog message names the plan and the person who approved it ([ADR 0007](0007-provenance-and-changelog-messages.md)), so NetBox's own history shows who is behind a change.
- **Writes to NetBox** can't share a transaction with TrueUp's records, so the event for each one is recorded as started before the write and completed with the result after it. After a crash, recovery re-reads NetBox and completes any event left started.
- **Streaming out.** Each event is also written to an outbox in the same transaction as the TrueUp change it describes, or as its completion for a write to NetBox. Configured sinks read from the outbox with retries: for example a structured log stream, OTLP logs to an OpenTelemetry collector, or outbound webhooks. The same outbox carries change notifications for other systems.

### Consequences

- Good, because "who synced what, and when" can be answered in TrueUp, in NetBox's changelog, and in external systems.
- Good, because a sink that is down delays delivery but loses nothing.
- Good, because audit export and outbound notifications share one mechanism.
- Bad, because the audit log grows without limit unless a retention period is set.
- Bad, because a CLI user's identity is only as trustworthy as the host's accounts. Team deployments should act through the UI.
- Bad, because anyone with direct database access can alter TrueUp's copy. Copies streamed to an external system are what make the record hard to tamper with.

## Pros and cons of the options

### Structured log lines only

- Good, because it is simple, and log pipelines already exist.
- Bad, because log lines can be dropped, and TrueUp's UI couldn't show an object's history.

### NetBox's changelog only

- Good, because it needs no extra work.
- Bad, because it shows TrueUp's service account instead of people, and misses every action that stays inside TrueUp.
