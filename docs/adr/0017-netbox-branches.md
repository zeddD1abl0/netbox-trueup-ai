---
status: accepted
date: 2026-10-23
decision-makers: "@zeddD1abl0"
---

# Write plans through NetBox branches where teams want them

## Context and problem

Organizations that manage change carefully often run the netbox-branching plugin. A branch isolates changes in NetBox until they are merged into main. Some also run the netbox-changes plugin, which refuses to merge a branch until a change request is approved. Its `protect_main` setting goes further, and accepts changes to main only through branches. TrueUp has to work in those NetBox instances from its first write, and reviewers want to see a change in NetBox itself before it lands.

## Decision drivers

- Work in NetBox instances that accept changes only through branches.
- Let reviewers see a change in NetBox before it reaches main.
- Never let a merge silently override someone else's change.
- Keep writing straight to main simple where branches aren't used.

## Considered options

1. Branches as an optional target for plans, supported from the first write
2. Main only, with branches added later
3. Always write through branches

## Decision

Chosen option: "Branches as an optional target for plans, supported from the first write".

- **Choosing the target.** A plan targets main or a new branch, chosen for each plan or required by a policy ([ADR 0016](0016-permissions-and-approval-policies.md)). The branch option needs netbox-branching 1.2.x, the line for NetBox 4.7, and TrueUp checks for it when it connects.
- **Writing.** TrueUp creates a branch named after the plan and its change reference, waits until it is ready, and writes the plan into it with the `X-NetBox-Branch` header and the same `If-Match` checks as on main. It never uses `?background=true`, which NetBox runs without the branch header, so those writes would land in main.
- **Approval gates the merge.** Writing into a branch changes nothing in main, so approval is required before merging, not before writing ([ADR 0010](0010-write-through-reviewed-plans.md)). Reviewers can see the change in the branch in NetBox as well as in TrueUp.
- **Merging.** TrueUp merges an approved plan's branch and follows the merge job until it finishes. The checks for applying run again first: a newer source version, or a stale branch, makes the plan stale.
- **Conflicts.** TrueUp never acknowledges merge conflicts on anyone's behalf, because acknowledging makes the branch's values win. It reports them, and a person decides.
- **NetBox change management is the final arbiter.** Where netbox-changes is installed, TrueUp opens a change request for every plan it writes into a branch, carrying the plan and its ticket, and only NetBox's approval allows the merge. TrueUp's own approval policy, if one applies, is checked before the change request is opened: it can add requirements, but can never approve a merge NetBox hasn't. A rejected change request closes the plan as rejected. TrueUp never holds the plugin's bypass permission.
- **Protected main.** Where that plugin's `protect_main` setting is on, NetBox accepts changes only through branches, and TrueUp writes every plan into one.

### Consequences

- Good, because TrueUp works in NetBox instances that only accept changes through branches.
- Good, because reviewers can see each change in NetBox before it reaches main.
- Bad, because branches add states to follow: provisioning, merge jobs, conflicts, and branches that go stale.
- Bad, because integration tests need a NetBox with the branching plugin installed, and with the change request plugin for that part.

## Pros and cons of the options

### Main only, with branches added later

- Good, because the first writes are simpler.
- Bad, because TrueUp couldn't write at all to NetBox instances that protect main, and branch handling would have to be fitted into a write path built without it.

### Always write through branches

- Good, because every change is reviewable in NetBox.
- Bad, because it requires the plugin everywhere, and adds steps a single user doesn't want.
