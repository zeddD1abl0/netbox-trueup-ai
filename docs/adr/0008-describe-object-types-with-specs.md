---
status: accepted
date: 2026-10-23
decision-makers: "@zeddD1abl0"
---

# Describe NetBox object types with declarative specs

## Context and problem

Matching, comparison, overrides, planning, the UI's diff views and the generated permission set all need the same knowledge about each NetBox object type TrueUp handles:

- its endpoint, fields and field types;
- which fields are writable or nullable, their defaults, and how NetBox normalizes them;
- its uniqueness constraints, and what it depends on;
- its child objects;
- where the write shape differs from what the API returns. For example, NetBox 4.7 takes a front port template's port mappings as a nested `rear_ports` list.

NetBox's OpenAPI schema describes types, writability, nullability and choices. It doesn't describe uniqueness constraints, dependency order or write shapes. The generated Go SDK, go-netbox, targets NetBox 4.3 (see [tooling.md §9](../tooling.md#9-considered-and-not-chosen)).

## Decision drivers

- One implementation of comparison, overrides and planning for every object type.
- Behaviour that matches NetBox exactly, and can be traced to NetBox's source.
- Adding an object type should be cheap and easy to review.
- Catch differences when NetBox changes.

## Considered options

1. Generic objects described by hand-written declarative specs, checked against the OpenAPI schema
2. A typed Go struct per object type, with hand-written comparison and planning code
3. Go types generated from the OpenAPI schema
4. Everything derived from the OpenAPI schema at runtime

## Decision

Chosen option: "Generic objects described by hand-written declarative specs, checked against the OpenAPI schema".

- An object is an object type plus field values. A value is a string, integer, decimal, boolean, date, choice, reference, set or child collection.
- Each object type has a spec, written in Go, that declares its endpoint, dependencies, uniqueness constraints, fields and their semantics, child collections and their keys, and write shapes. Each spec cites the NetBox source files it follows at the pinned version.
- A contract test checks every spec against the OpenAPI schema of the target NetBox: the fields exist, and writability, types and choice values agree. Tests also check that each spec is consistent with itself, for example that every key refers to a declared field.
- Parts of specs, such as choice values, may later be generated from the schema. Generated files are never edited by hand.

### Consequences

- Good, because comparison, overrides, planning, the UI and permission generation are written once.
- Good, because specs are short, reviewable and tied to NetBox's source.
- Bad, because field names are strings, not Go identifiers, so a typo isn't a compile error. The contract and consistency tests catch it instead.
- Bad, because specs are maintained by hand for each supported NetBox version.

## Pros and cons of the options

### A typed Go struct per object type

- Good, because the compiler checks field names.
- Bad, because every generic feature (diffs, overrides by field path, the UI) then needs per-type code or reflection, for what will become many object types.

### Go types generated from the OpenAPI schema

- Good, because types cover every object type.
- Bad, because the schema lacks the semantics TrueUp needs, so specs are needed anyway.
- Bad, because it means a large generated surface tied to the schema's quirks.

### Everything derived from the schema at runtime

- Good, because no code is needed per object type.
- Bad, because uniqueness constraints, dependencies and write shapes would be missing.
- Bad, because behaviour would change with whichever NetBox is connected, in ways tests can't pin down.

## More information

The `add-netbox-object` project skill ([tooling.md §1.4](../tooling.md#14-project-skills-we-will-write-these)) will hold the checklist for writing a spec. Choosing between kin-openapi and plain JSON decoding for the contract test is a spike in milestone 1 ([roadmap.md](../roadmap.md)).
