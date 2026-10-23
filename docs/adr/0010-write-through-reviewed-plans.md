---
status: accepted
date: 2026-10-23
decision-makers: "@zeddD1abl0"
---

# Write to NetBox only through reviewed plans

## Context and problem

Syncing writes to an organization's source of truth. Wrong writes can come from a bad upstream change, a bug, or an object edited in NetBox between TrueUp reading it and writing it. Several writers may act at once: people in NetBox, other tools, the TrueUp CLI and, with high availability, several TrueUp replicas.

## Decision drivers

- A person can see every change before it happens.
- No lost updates when someone else edits an object.
- A history of what changed, when and why.
- Safe recovery when a sync fails partway through.

## Considered options

1. Stored plans that a person reviews and then applies
2. Direct sync: compare and write in one step
3. Write into a NetBox branch (the netbox-branching plugin) and review there, instead of keeping plans in TrueUp

## Decision

Chosen option: "Stored plans that a person reviews and then applies".

- **Planning.** The planner turns the comparison results for linked definitions and import requests into a plan: creates, updates, and any deletes a person selected, in dependency order. Each plan is stored with the source versions, files and overrides it was built from, and the ETag of every NetBox object it read.
- **Approval.** Nothing reaches NetBox's main data until the plan is approved, in the CLI or the UI. With approval policies off, the default, the person who applies a plan is its approver. With them on, the plan needs the approvals its policy sets ([ADR 0016](0016-permissions-and-approval-policies.md)). A plan can also be written into a NetBox branch first, where approval gates the merge ([ADR 0017](0017-netbox-branches.md)). Applying plans automatically on a schedule may be added later, as an opt-in setting per source, and never for plans a policy covers.
- **Latest source only.** If any file the plan used has changed in its source by the time the plan is applied, the plan is stale: nothing is written, and a new plan must be built and approved. TrueUp never writes an older version of a definition ([ADR 0009](0009-source-kinds-and-format-adapters.md)). Changes to files the plan didn't use don't make it stale.
- **Concurrency.** Updates `PATCH` only the fields that differ, and send `If-Match`. NetBox (4.6 and later) answers 412 if the object changed after the plan read it. The executor then stops and marks the plan stale, and a new plan is built from fresh data.
- **Failures and unknown outcomes.** Writes are never retried automatically. When a write fails, or its outcome is unknown, the executor stops, re-reads NetBox and re-plans. Comparison is idempotent, so finished work shows as in sync. A create that did go through shows up as a matching object, which the new plan links ([ADR 0006](0006-manage-only-linked-objects.md)).
- **History.** Each write carries a changelog message ([ADR 0007](0007-provenance-and-changelog-messages.md)). Each operation's result is stored as sync history, and who approved and applied the plan is recorded in the audit log ([ADR 0014](0014-audit-every-action.md)).
- **Locking.** Only one plan applies at a time per NetBox instance, enforced by a lock in the database.

### Consequences

- Good, because every change can be reviewed before it happens and audited afterwards.
- Good, because concurrent edits in NetBox are detected, not overwritten.
- Bad, because syncing takes two steps.
- Bad, because on a busy NetBox plans can go stale and need rebuilding. Rebuilding is cheap.

## Pros and cons of the options

### Direct sync

- Good, because it takes one step.
- Bad, because nobody sees the changes before they happen, and there is nothing to inspect when something goes wrong.

### NetBox branches instead of plans

- Good, because review and merge happen in NetBox.
- Bad, because it needs the netbox-branching plugin, which many instances don't run.
- Branches are supported instead as an optional target for plans ([ADR 0017](0017-netbox-branches.md)).
