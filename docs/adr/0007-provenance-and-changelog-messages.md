---
status: accepted
date: 2026-10-23
decision-makers: "@zeddD1abl0"
---

# Keep provenance in TrueUp, and mark NetBox writes with changelog messages only

## Context and problem

TrueUp promises to record which source and file produced each NetBox object, and when it last synced. That record could live in TrueUp's database, in NetBox, or both. People looking at an object in NetBox may also want to know that TrueUp changed it.

NetBox 4.7 offers three places for such a mark:

- a `changelog_message` on any API write. NetBox records it with the change on create, update and delete, for every object type TrueUp writes, component templates included;
- tags;
- custom fields.

Component templates support neither tags nor custom fields.

## Decision drivers

- A complete record, including component templates.
- No changes to the NetBox schema, and no extra permissions for TrueUp's token.
- No extra changes to NetBox objects just to keep a mark up to date.

## Considered options

1. TrueUp's database, plus a changelog message on every write
2. As option 1, plus a tag on managed objects
3. As option 1, plus custom fields holding the source and file

## Decision

Chosen option: "TrueUp's database, plus a changelog message on every write".

- Links, sync history and the audit log in TrueUp's database are the provenance record ([ADR 0006](0006-manage-only-linked-objects.md), [ADR 0014](0014-audit-every-action.md)).
- Every write to NetBox carries a `changelog_message` that identifies TrueUp, the source, the file, the source version (the commit, for Git), the plan, its change reference if it has one, and the person who approved it. NetBox records TrueUp's service account as the user for every write, so this is how NetBox's own history shows the person behind a change. NetBox stores at most 200 characters (`ObjectChange.message` in `core/models/change_logging.py`), so long paths are shortened.
- TrueUp creates no tags or custom fields in NetBox.

### Consequences

- Good, because the NetBox changelog of every object TrueUp touched shows where each change came from, component templates included.
- Good, because TrueUp needs no permissions on tags or custom fields, and doesn't change the NetBox schema.
- Bad, because NetBox users can't filter NetBox's lists by "managed by TrueUp". TrueUp's own UI answers that question. An opt-in tag could be added later through a new ADR.

## Pros and cons of the options

### Plus a tag

- Good, because NetBox users can filter managed objects.
- Bad, because it covers only manufacturers and device types, not component templates.
- Bad, because the token needs permission to manage the tag.

### Plus custom fields

- Good, because the source is visible on the object itself in NetBox.
- Bad, because it changes the NetBox schema, which needs administrator rights, and doesn't cover component templates.
- Bad, because a field holding the source version would change every managed object on every upstream change, flooding the changelog.
