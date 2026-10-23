---
status: accepted
date: 2026-10-23
decision-makers: "@zeddD1abl0"
---

# Compare objects across sources side by side, and compose them field by field

## Context and problem

Several sources can define the same object. A Git source and a file source might both define a Dell PowerEdge R710, and NetBox may already hold one. Each version can be right about some fields and wrong about others. People need to see every version of an object side by side, and to build the object NetBox should hold by taking each field from the version that has it right, or by editing it directly.

## Decision drivers

- Any number of sources, plus NetBox, compared in one view.
- Choices per field, not only per object.
- It is always visible where each field's value comes from.
- Each source is still read at its latest version only ([ADR 0009](0009-source-kinds-and-format-adapters.md)).
- TrueUp takes over no field that nobody chose ([ADR 0006](0006-manage-only-linked-objects.md)).

## Considered options

1. A primary definition per object, with per-field overrides from another source, NetBox or an edit
2. An automatic merge by source priority: each field comes from the highest-priority source that sets it
3. One source per object, with edits saved as a full copy of the definition

## Decision

Chosen option: "A primary definition per object, with per-field overrides from another source, NetBox or an edit".

- **Grouping.** Definitions from all sources are grouped into objects by the NetBox uniqueness rules that are also used to match NetBox objects. Two device type definitions with the same manufacturer and model, or the same manufacturer and slug, describe the same object, because NetBox couldn't hold both. A source that defines one object twice reports a conflict.
- **Side by side.** The comparison engine compares any number of versions of one object, field by field: each source's definition, NetBox's object, the live edit and the desired object. Every version is normalized through the same spec, so only real differences show.
- **Primary definition.** A managed object follows one primary definition. Importing from a source makes that source's definition primary, and a person can switch to another source's definition later. NetBox can be the primary too, which means "leave it": TrueUp manages none of the object's fields except those with overrides.
- **Overrides.** For any field, child, or child collection, a person can tick Override and take the value from:
  - another source's definition of the same object. The override tracks that source's latest version. If the source stops defining the object, or is removed, the override goes with it, and the field falls back to the primary;
  - NetBox, which means "leave it": TrueUp stops managing that field;
  - a local edit from the live edit area, which is an override stored directly in TrueUp's database.
- **Precedence.** The desired object is the primary definition, then injected values (rules across many objects), then overrides. A choice made for one object always beats a rule.
- **Live edit.** The web UI has a YAML editor, in the format of the primary definition, whose content is validated like a source file and compared with every other version as the person types. Saving stores the fields that differ from the primary definition as local-edit overrides. No source is changed.
- **Review of changes.** Each override records the primary definition's value when it was made. If the primary definition later changes that field, TrueUp flags the override for review instead of silently keeping or dropping it. Any change to the desired object, including an override removed with its source, reaches NetBox only through a reviewed plan ([ADR 0010](0010-write-through-reviewed-plans.md)).

### Consequences

- Good, because the origin of every field is explicit, and visible in the comparison view.
- Good, because a source can be trusted for some fields and not others.
- Good, because an override that takes a field from another source keeps following that source.
- Bad, because an object's desired state can depend on several sources, so a change in any of them changes it. Plans record the version of every source they used, and go stale when any of them moves on ([ADR 0010](0010-write-through-reviewed-plans.md)).
- Bad, because the comparison view gets wide with many sources. The UI can hide columns, and rows where every version agrees.

## Pros and cons of the options

### Automatic merge by source priority

- Good, because no choices are needed per object.
- Bad, because where each field comes from is implicit.
- Bad, because a field newly added to a higher-priority source silently replaces a value people relied on.

### One source per object, with edits saved as a full copy

- Good, because it is simple.
- Bad, because it can't take one field from one source and the rest from another.
- Bad, because a full copy stops following upstream changes.

## More information

How overrides and injection rules are stored, and the syntax of field paths, are decided with the milestone 5 work. See [architecture.md](../architecture.md#overrides-and-injected-values-m5).
