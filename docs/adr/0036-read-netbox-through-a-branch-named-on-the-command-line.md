---
status: accepted
date: 2026-11-27
decision-makers: "@zeddD1abl0"
---

# Read NetBox through a branch named on the command line, and keep a comparison for each target

## Context and problem

[ADR 0033](0033-runs-against-main-or-a-chosen-branch.md) has runs target main or a branch chosen from NetBox's ready branches, turned on by `netbox.branching`, with `netbox.protect_main` keeping runs out of main, and has TrueUp read NetBox's installed plugins when it connects. Before runs exist (roadmap step 22), `netbox check` and `compare` must read through a branch, and `compare` records what it finds. That leaves questions ADR 0033 doesn't answer:

- How does a person name a branch, and how does the client know it?
- What does TrueUp do when netbox-branching refuses a branch, which it can do at any request, since a person can start merging or synchronizing it?
- May `compare` read main while main is protected?
- A comparison is recorded for each definition. Is one made in a branch a replacement for main's, or kept beside it?

## Decision drivers

- People name branches in NetBox's web UI by their names. netbox-branching's header takes the branch's schema ID, which only the branch's page shows.
- netbox-branching's answers are untrusted, and its refusals aren't JSON.
- Reading never changes NetBox, and `netbox.protect_main` exists to keep writes out of main.
- What TrueUp knows of main stays true while a person works in a branch, so that, once the branch is merged, comparing main shows what it brought.
- With branching off, the default, nothing changes.

## Considered options

For naming a branch:

1. Its name, resolved through netbox-branching's list
2. Its schema ID, as the header takes it
3. Either

For recording comparisons:

1. One comparison of each definition for each target: main, and each branch
2. One comparison of each definition, whatever the target

## Decision

Chosen options: a branch's name, and one comparison for each target.

- **Settings.** `netbox.branching` and `netbox.protect_main` are TrueUp's first boolean settings: `true` or `false`, in any case, from a flag, an environment variable or the file, where a YAML boolean works too. Any other spelling, such as `yes` or `1`, is a configuration problem. Given alone, the flag `--netbox-branching` means true. `netbox.protect_main` set explicitly while branching is off gets a warning that it has no effect.
- **Detection.** The client reads `plugins` from the `/api/status/` answer it already reads for NetBox's version. netbox-branching of the 1.2 line, with any suffix, works with NetBox 4.7. With branching on, a NetBox without it, or with another release or none named, fails to connect; a status that doesn't list plugins at all fails too, while with branching off it is no fault, in case a hosted NetBox leaves them out. With branching off, NetBox having netbox-branching is a warning.
- **`--branch` takes a name**, exactly, case included, without white space at either end, as NetBox stores names. The client finds it through netbox-branching's list, filtered by name, and checks the name itself, since NetBox ignores a filter it doesn't know. A branch that doesn't exist or isn't ready ends the command before anything else is read. `--branch` with branching off is a usage error. `netbox branches` lists what a run can target: the ready branches, and main where the settings allow it, and with `--all` every branch with its status. Runs (step 22) will record schema IDs, which never change.
- **A client bound to a branch.** Connecting stays on main, since netbox-branching checks the header on the token check too. The bound client sends `X-NetBox-Branch` with the schema ID on every request, but never on the reads of branches themselves, which netbox-branching refuses for a branch that isn't ready, so that a command can still read a branch's status. A schema ID from NetBox must be eight lower case letters and digits before it goes in a header. NetBox's answers don't say which branch they come from, so before binding, the client sends one request naming a branch that can't exist: netbox-branching must refuse it, before NetBox checks the token, or something in front of NetBox, such as a proxy, drops the header, and every read would come from main unnoticed. That is an error.
- **Refusals.** netbox-branching's middleware answers 400 with plain text, before the token is checked, for a branch that no longer exists or isn't ready. A 400 that carries NetBox's `API-Version`, isn't JSON, and answers a request that named a branch is that refusal, reported as such with NetBox's text quoted, and never retried. The text isn't matched: one is translatable, and either could change.
- **Reading main while it is protected.** `compare` without `--branch` reads main, whatever `netbox.protect_main` says: it keeps runs and writes out of main, and reading is neither. `netbox branches` lists run targets, so it leaves main out.
- **A comparison for each target.** The comparisons table's key becomes source, definition and branch, by its schema ID, or `''` for main, with the branch's name when compared and its ID in NetBox. A run replaces, and a whole run removes, only the comparisons of its own target; a load removes the comparisons of the definitions it removes in every target. Comparisons made before are main's. Comparisons of a branch since merged or deleted stay until step 24 follows branches. Migration 00006, on both engines, makes the change.

### Consequences

- Good, because a person names a branch as NetBox shows it, and a script can still find its schema ID in `netbox branches --output json`.
- Good, because a branch that someone starts merging mid-command ends the command with an error that says so, rather than an unexplained 400 or a retry.
- Good, because comparing in a branch leaves what is known of main, and runs and the web UI can show main and a branch side by side.
- Bad, because a name can change between commands; runs will key on schema IDs instead.
- Bad, because comparisons of finished branches pile up until step 24 removes them.
- Bad, because a release with migration 00006 can't be followed by an older one on the same database ([ADR 0021](0021-forward-only-migrations-when-opening.md)).

## Pros and cons of the options

### A branch's schema ID

- Good, because the header takes it as it is, with no lookup, and a token needs no permission on branches: netbox-branching checks none for the header.
- Bad, because people don't know branches by it, and without a lookup TrueUp couldn't say a branch isn't ready until NetBox refused a request.

### Either

- Good, because both kinds of user are served.
- Bad, because a branch can be named like another's schema ID, and the two would be ambiguous.

### One comparison of each definition, whatever the target

- Good, because the table and its key stay as they are.
- Bad, because a comparison in a branch would wipe main's, and what TrueUp recorded would depend on which was compared last.

## More information

- netbox-branching 1.2.4: `get_active_branch` in `netbox_branching/utilities.py` (the header takes `schema_id`; a branch not `ready` raises `BranchNotReady`); `BranchMiddleware` in `middleware.py` (`HttpResponseBadRequest('Invalid branch identifier')` and `'Selected branch is not ready.'`, for every path but `EXEMPT_PATHS`, which holds `/api/status/` alone, in `constants.py`); `BranchViewSet` in `api/views.py` is REST framework's `ModelViewSet`, so `brief` and `fields` are ignored and permission constraints don't filter the list; `BranchFilterSet` in `filtersets.py` (exact `name`, no `schema_id` filter); `Branch` in `models/branches.py` (`name` unique and editable, `schema_id` unique and not editable); `min_version` and `max_version` in `__init__.py`, and `COMPATIBILITY.md`.
- NetBox 4.7.2: `get_installed_plugins` in `netbox/netbox/plugins/utils.py` (each plugin's name to its version, with `-<release_track>`, or `null`); `CoreMiddleware` in `netbox/netbox/middleware.py` sets `API-Version` around plugins' middleware, which `netbox/netbox/settings.py` appends to `MIDDLEWARE`.
- Probed against the disposable NetBox with netbox-branching 1.2.4 on 2026-11-27: the 400s are `text/html; charset=utf-8` with `API-Version: 4.7`, also for requests without a token; a token without `netbox_branching.view_branch` gets NetBox's 403 on the list, but can read through a branch whose schema ID it knows.
