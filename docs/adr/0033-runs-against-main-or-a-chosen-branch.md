---
status: accepted
date: 2026-11-23
decision-makers: "@zeddD1abl0"
---

# Sync against main or a chosen NetBox branch, with branches turned on by settings, and leave merging to people

## Context and problem

[ADR 0017](0017-netbox-branches.md) planned branches as an optional target for each plan: TrueUp would create a branch for a plan, write the plan into it, and merge it once approved; where the netbox-changes plugin was installed, it would open a change request for each branch and follow NetBox's decision, and where that plugin's `protect_main` setting was on, write every plan into a branch. Planning milestone 2 turned up four things that plan didn't account for:

- netbox-changes, NetBox Labs' change management plugin, isn't publicly available: it is on neither PyPI nor GitHub, and seems to ship only with NetBox Labs' commercial editions. TrueUp can't be built or tested against it.
- Many NetBox deployments don't run netbox-branching at all, and some that tried it went back to main alone. TrueUp mustn't make them deal with branches.
- Organizations with change management, through NetBox Changes or their own controls, protect main and want every change in a branch that a person merges once a change request is approved.
- TrueUp is used against NetBox Cloud and NetBox Enterprise as well as open-source NetBox.

How does TrueUp choose between main and a branch, how much does it do with branches, and what does it do about features it can't test against?

## Decision drivers

- With no branches, TrueUp works as if the feature didn't exist.
- Where main is protected, TrueUp works entirely through branches, including under change management it doesn't integrate with.
- A person confirms what reaches main through a branch.
- TrueUp holds no more rights in NetBox than an organization chooses to give it.
- Designed and tested against open-source NetBox, through its standard REST API, and usable against any NetBox that offers that API.

## Considered options

1. Runs against main or a branch chosen first, branches turned on by settings, and people merging in NetBox
2. ADR 0017 as written: a branch created for each plan, merged by TrueUp once approved, with change requests opened where netbox-changes is installed
3. Main only, with branches later

## Decision

Chosen option: 1, runs against main or a chosen branch, with people merging in NetBox.

- **Open-source NetBox first.** TrueUp is designed and tested against open-source NetBox, through its standard REST API, and integrates only with what an open-source deployment can have: netbox-branching (public, under NetBox Labs' Limited Use License) yes; netbox-changes, and other features of NetBox Labs' commercial editions, no. TrueUp still runs against NetBox Cloud and Enterprise, and never refuses a NetBox for its edition. Where a feature it doesn't integrate with stands in front of its writes, TrueUp reports NetBox's refusal as NetBox's, and never works around it.
- **Settings, for the NetBox connection.**
  - `netbox.branching`, off by default: every run reads and writes main, and nothing asks about branches.
  - `netbox.protect_main`, on by default: with branching on, a run targets a branch, and main isn't offered. With it off, a run may target main or a branch.
  - `netbox.manage_branches`, off by default: TrueUp may create a branch for a run, and delete the branches it created. It never deletes another.
- **Detection.** TrueUp reads NetBox's installed plugins when it connects (`/api/status/`). Branching on with netbox-branching missing, or not of the release line for the NetBox targeted, is an error; netbox-branching installed with branching off is a warning that branching is available but not turned on. Whether main is protected isn't visible through the API, so TrueUp doesn't warn about it. A write to main that NetBox refuses gets an error saying that main may be protected, and naming the settings.
- **Runs.** A run is a person's work to true-up one target of the NetBox: main, or a branch chosen from NetBox's list of ready branches. The target is chosen first, and every comparison, plan and write of the run uses it, through the `X-NetBox-Branch` header for a branch. A run holds its plans, and ends when the person finishes it, or abandons it at any stage. Abandoning never touches main, and discards what TrueUp holds only for the run's branch, such as links to objects created there.
- **People merge.** TrueUp never merges a branch. A person merges it in NetBox, after a change request where one is required, and so confirms what reaches main. TrueUp follows the branch's status and records the merge; objects TrueUp created in the branch stay managed in main, with the same IDs. A branch archived or deleted without being merged abandons its run. Where TrueUp may manage branches, it offers to delete the branches it created once their run is finished or abandoned.
- **No change requests.** TrueUp doesn't open, read or follow NetBox Changes' change requests. A change request works on TrueUp's branch as on any other, as a diff against main, and the merge waits for its approval in NetBox.
- **The bypass right.** The permission set TrueUp generates never includes NetBox Changes' bypass. An operator who gives TrueUp a superuser token, which bypasses a protected main, and turns `netbox.protect_main` off, chooses to have TrueUp write to main anyway.
- **Approval.** With approval policies off, applying a plan, to main or into a branch, is approving it; for a branch, the person merging confirms it again in NetBox. Approval policies ([ADR 0016](0016-permissions-and-approval-policies.md), milestone 4) gate applying; NetBox decides merges.

This supersedes ADR 0017, and in ADR 0016 the bullet "NetBox decides last", where a NetBox change request was the final arbiter of a merge TrueUp made.

### Consequences

- Good, because a NetBox without branches, and one where branches are turned off, see no difference.
- Good, because with branching on and main protected, no run writes to main, whether or not TrueUp could tell main was protected.
- Good, because TrueUp works under NetBox Changes without integrating with it, and needs no right to merge, nor, unless it manages branches, to create or delete them.
- Bad, because syncing through a branch takes a step in NetBox, the merge, that TrueUp doesn't take for the person. That is the point: the person confirms what is merged.
- Bad, because TrueUp can't tell in advance whether main is protected: a TrueUp set to write to main finds out with its first write, which NetBox refuses.
- Bad, because TrueUp can't be tested against NetBox Changes or the commercial editions. The test NetBox with netbox-branching stands in: branching's own `merge_validators` refuse a merge as a change request awaiting approval does, and a test-only rule refuses writes to main as a protected main does.

## Pros and cons of the options

### A branch for each plan, merged by TrueUp

- Good, because a plan goes from TrueUp to main without leaving TrueUp.
- Bad, because the change request integration can't be built or tested from public sources.
- Bad, because TrueUp would need the right to merge, and a merge could land what no person looked at in NetBox.
- Bad, because a person couldn't prepare a branch first, such as one a change request already covers.

### Main only, with branches later

- Good, because the first writes are simpler.
- Bad, because TrueUp couldn't write at all to a NetBox that accepts changes only through branches, and runs, links and plans would have to learn about branches after they were built.

## More information

- Roadmap, milestone 2, steps 17, 18, 22 and 24.
- netbox-branching 1.2.4: a branch's tables take their IDs from main's sequences, "so all branches continue to share a global id namespace" (`netbox_branching/provisioning.py`), and a merge applies each create with its original primary key (`ObjectChange.apply` in `netbox_branching/models/changes.py`), so an object created in a branch keeps its ID in main. Branch names are unique (`Branch.name` in `netbox_branching/models/branches.py`).
- NetBox 4.7.2: `/api/status/` lists installed plugins and their versions (`StatusView` in `netbox/netbox/api/views.py`, `get_installed_plugins` in `netbox/netbox/plugins/utils.py`).
