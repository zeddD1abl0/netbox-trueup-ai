---
status: accepted
date: 2026-10-23
decision-makers: "@zeddD1abl0"
---

# Manage only objects a person has imported

## Context and problem

NetBox instances already hold objects that people created by hand, imported with other tools, or maintain through other automation. Many already contain device types from devicetype-library. A sync tool that assumes it owns every object of a type, or every field of an object, will overwrite or delete other people's work. TrueUp needs a rule for which NetBox objects, and which fields, it may change.

## Decision drivers

- Never surprise the people who use NetBox.
- Identify objects the same way NetBox does.
- Follow an object through upstream renames.
- Work alongside manual edits and other tools.

## Considered options

1. Explicit import: manage only what a person has imported
2. Adopt automatically: manage any NetBox object whose natural key matches a definition
3. Mirror the source: adopt matches, and delete whatever the source no longer has
4. Manage only objects TrueUp created

## Decision

Chosen option: "Explicit import".

- **Import creates the link.** A NetBox object is managed only after a person imports a definition. If NetBox has a matching object when the plan is built, TrueUp links the definition to it. If not, TrueUp creates the object and then links it. A link records the NetBox object type and ID, and what the object follows, its primary definition: the source and path of a definition, or NetBox itself, which means "leave it as it is" apart from overrides. When several sources define the same object, the person chooses which one is primary ([ADR 0013](0013-compose-objects-across-sources.md)). Importing a selection of definitions is how users choose what syncs.
- **Matching follows NetBox.** TrueUp matches definitions to existing objects using the uniqueness constraints NetBox enforces for that model at the target version. For a device type, those are `(manufacturer, model)` and `(manufacturer, slug)` (`DeviceType.Meta.constraints` in `dcim/models/devices.py`, NetBox 4.7.2). An existing object matches if creating the definition would collide with it on any of those constraints. If the constraints point to different objects, TrueUp reports a conflict and doesn't guess.
- **Linked objects are addressed by ID.** When planning, TrueUp adds the linked NetBox ID to the desired object, so a model or slug renamed upstream updates the same NetBox object.
- **Only fields the desired object sets are managed.** These are the fields the primary definition sets, plus any that overrides or injected values set. Other fields keep whatever value they have in NetBox. This matches NetBox's own import, which updates only the fields present in a record that carries an `id` (`_process_import_records` in `netbox/views/generic/bulk_views.py`).
- **Components follow the same rule.** Components a definition lists, such as interface templates on a device type, are managed and matched within their parent by NetBox's uniqueness rule, usually the name. Components that exist only in NetBox are reported, not removed.
- **TrueUp never deletes on its own.** When a primary definition disappears from its source, or a managed component is removed from it, TrueUp reports it as orphaned. It is deleted only if a person selects the deletion in a plan.
- **Unlinking** stops management and leaves the NetBox object as it is.
- **Prerequisites.** TrueUp looks up objects it needs as references, such as a device type's manufacturer, by natural key. An existing one is referred to, and left alone unless it was imported itself. A missing one is imported in the same plan from a source that defines it, where the reviewer sees it, and is linked like any other import. If no source defines it, the plan reports the objects that need it as blocked.

### Consequences

- Good, because TrueUp can share a NetBox with people and other tools without overwriting their work.
- Good, because links survive upstream renames.
- Good, because the link is also the provenance record ([ADR 0007](0007-provenance-and-changelog-messages.md)).
- Bad, because nothing syncs until someone imports it, and a first run against a populated NetBox needs that step. Importing by selection keeps it short.
- Bad, because drift in fields and components TrueUp doesn't manage is reported but not corrected.
- Removing a field from a definition stops TrueUp managing it, and NetBox keeps the last value.

## Pros and cons of the options

### Adopt automatically

- Good, because the first sync needs no extra step.
- Bad, because TrueUp would start changing objects nobody asked it to manage.

### Mirror the source

- Good, because NetBox ends up exactly like the source.
- Bad, because it deletes other people's objects and components, which is unacceptable in a shared NetBox.

### Manage only objects TrueUp created

- Good, because existing objects can never be touched.
- Bad, because device types already imported from the library could never come under management. Deleting and recreating them isn't possible while devices use them.

## More information

A later feature exports a NetBox object to a writable repository and links it to the new file, creating links from the NetBox side. See [architecture.md](../architecture.md#linking-planning-and-applying-m2).
