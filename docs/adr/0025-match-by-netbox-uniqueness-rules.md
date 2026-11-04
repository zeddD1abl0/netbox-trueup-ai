---
status: accepted
date: 2026-11-03
decision-makers: "@zeddD1abl0"
---

# Match definitions to NetBox by its uniqueness rules, compare them side by side, and record each comparison as compare prints it

## Context and problem

Roadmap step 10 compares devicetype-library's manufacturers and device types with what NetBox holds. [ADR 0006](0006-manage-only-linked-objects.md) settled that matching follows NetBox's uniqueness constraints, with one of them the object's identity, and [ADR 0013](0013-compose-objects-across-sources.md) that versions of an object are compared side by side. Neither settled:

- how values compare when matching, as NetBox's constraints compare them;
- what a value the format derived rather than read, such as a manufacturer's slug ([ADR 0009](0009-source-kinds-and-format-adapters.md)), counts for in matching;
- how much of NetBox to read;
- the shape of a comparison, which roadmap steps 11 to 13, the web UI and the database will all use;
- what "the latest comparison of each definition is recorded" stores.

## Decision drivers

- An outcome says what importing the definition would do in NetBox, and is never "absent" for an object TrueUp couldn't see.
- One structure from the first two-column comparison to many sources, children and the UI, without breaking the CLI's JSON (ADR 0018).
- The engine stays pure and fuzzed, so that it can be held to a plain reading of NetBox's rules.
- Read cost follows what is compared and what NetBox holds, never the library times NetBox.

## Considered options

Matching:

1. Every uniqueness rule, with values compared exactly, and derived values counting only where TrueUp would create the object
2. The identity alone, with collisions found when planning
3. Values compared after case folding, to catch near misses

Reading NetBox:

1. Every manufacturer, and the device types of the manufacturers the definitions name, with only the fields the specs describe
2. Everything of both types
3. Filtered by the definitions' names and slugs

Recording:

1. A row per definition, holding the comparison as compare's JSON prints it
2. A row per field compared, in tables of their own
3. Nothing beyond the outcome

## Decision

Chosen options: "every uniqueness rule, exactly", "the manufacturers, and the device types of those named" and "a row per definition, as compare prints it".

**Matching.** For each of the type's `Unique` rules, the engine (`internal/compare`) looks for NetBox objects with the definition's values:

- **Candidate:** the identity matches one object, and no other rule matches a different one.
- **Conflict:** another rule matches an object that isn't the identity's match, whether the identity matched or not: a device type whose model matches one device type and whose slug another, or only the slug. Each collision is reported with its rule and the object's ID and identity.
- **Absent:** nothing matches.

Two further rules settle the cases that step needed:

- **Exactly.** Values count as PostgreSQL's deterministic collations compare them: byte for byte, case and white space included. None of these constraints uses `Lower()`, none of their columns has a collation of its own, and none can be null. A definition's values count as NetBox would store them, normalized through the spec; NetBox's count as it returns them. A rule with a null value matches nothing, as PostgreSQL treats nulls as distinct.
- **Derived values** count only where TrueUp would create the object. Once the identity matches, a rule that includes a derived field is skipped, since importing links to the match and never writes the derived value. When the identity matches nothing, they count, since creating the object would write them: NETGEAR's derived slug `netgear` conflicts with a NetBox manufacturer Netgear that has it.

A reference, such as a device type's manufacturer, is resolved to a NetBox ID by the referred object's identity, exactly, as NetBox's own import finds a manufacturer by name. A device type whose manufacturer NetBox lacks is absent, and says what its manufacturer's own definition found. Two NetBox objects that share a rule's values, which NetBox's constraints forbid, are reported as a conflict, never trusted to say which is the match: a definition whose identity matches both, and a device type whose manufacturer's name both have, which collides with what it would collide with under either. The engine refuses, as an error, an object or a definition that doesn't hold its fields as its spec describes them, since a value of the wrong kind, or a field left out, would drop out of matching unseen.

**Reading NetBox.** Every manufacturer, since a manufacturer's name and slug are each unique among all of them, and the device types of those manufacturers that the definitions name and NetBox has, through the `manufacturer_id` filter, since both device type rules include the manufacturer. Each listing asks for `fields=id,<the spec's fields>`, and an object missing one of them, or holding a value NetBox wouldn't write, fails the command: leaving it out would make what it is show as absent. A 403 from NetBox fails the command too, naming the type.

**The comparison.** A result holds the outcome, the NetBox ID matched, the collisions, the references NetBox lacks, columns and rows:

- a column for each version: the definition, with its source and its file or the files it is derived from, and NetBox's object, with its ID;
- a row for each field any version sets, with a cell per column that is either unset or holds a value, null included, normalized through the spec;
- on each row, whether the versions that set it differ, whether the definition manages it, which it does when it sets the field and the field isn't derived on an object NetBox has, and whether it is derived.

A difference is a managed row that differs. The path of a row is a list of steps, a field's name for now; steps 11 and 12 add steps into child collections, by a child's key, and step 13 adds a definition column for each source that defines an object.

**Recording.** Table `comparisons`, migration 00002, keyed by source, object type, file and identity, the identity telling derived definitions apart. A row holds the version of the source compared, when, the NetBox URL and release, the outcome, the NetBox ID, the count of differences, and the result as `compare --output json` prints it, so the stored shape is the public one. A run records each source in one transaction, which first locks the source's row, as fetches and removals of it do, and checks that the source's recorded snapshot is the version compared; otherwise nothing is recorded, and the source is reported. It replaces the comparisons of the definitions compared and removes those of definitions the source no longer holds, except while the source has invalid files: a file that fails to read defines nothing, though it is still there, so its last comparison stays.

### Consequences

- Good, because an outcome is what NetBox would do: a conflict is a create or a rename that NetBox would refuse.
- Good, because a derived slug never makes a manufacturer NetBox already has look wrong, and still stops TrueUp creating one NetBox would refuse.
- Good, because the engine has no I/O, and fuzzing holds it to a plain reading of the rules, and to its results not depending on NetBox's order.
- Good, because the database, the CLI and later the UI share one shape, under ADR 0018's rules for changing it.
- Bad, because a name that differs from NetBox's only in case is absent, or a conflict where the slug collides, rather than flagged as a near miss. NetBox's own import treats it the same way.
- Bad, because a token whose permissions have constraints sees fewer objects, and nothing can tell: what it can't see is absent. The help and the guide say so.
- Bad, because each run rewrites every row it compared, about 30 MB for the whole library, and `--output json` builds its document in memory, about 0.5 GB at the library's size.

## Pros and cons of the options

### The identity alone

- Good, because it is simpler.
- Bad, because a device type whose slug another already has would look importable, and fail only when planned.

### Case folding

- Good, because it catches `Netgear` beside `NETGEAR`.
- Bad, because NetBox treats them as different objects, so matching them would link a definition to the wrong one. The slug rule already reports most such pairs as conflicts.

### Everything of both types

- Good, because it is one listing each.
- Bad, because a comparison of one manufacturer would read every device type NetBox holds.

### Filtered by names and slugs

- Good, because it reads least for a few definitions.
- Bad, because finding collisions needs a lookup by model and another by slug for each batch, and those filters' rules for case and exact matches would be one more thing to hold to NetBox.

### A row per field

- Good, because fields can be queried in SQL.
- Bad, because it is a second shape to keep in step with the JSON, for queries nothing needs yet.

### Only the outcome

- Good, because it is small.
- Bad, because the UI and later plans need the differences, which the source and NetBox may no longer hold when they are read.

## More information

- NetBox 4.7.2: `OrganizationalModel` in `netbox/models/__init__.py` (manufacturer `name` and `slug`, each `unique=True`), `DeviceType.Meta` in `dcim/models/devices.py` (two `UniqueConstraint`s), `DeviceTypeFilterSet.manufacturer_id` in `dcim/filtersets.py`, `BaseViewSet.field_kwargs` in `netbox/api/viewsets/__init__.py` (`fields`, which leaves out a name it doesn't know), `ChoiceField.to_representation` in `netbox/api/fields.py`.
- The parity test, `TestCompareParity` in `cmd/netbox-trueup-ai`, imports the test library through NetBox's own YAML import (`BulkImportView` in `netbox/views/generic/bulk_views.py`) and finds every definition in step, then changes one field at a time.
