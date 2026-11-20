---
status: accepted
date: 2026-11-20
decision-makers: "@zeddD1abl0"
---

# Compare definitions across sources as objects, merged from each definition's own comparison

## Context and problem

Several sources can define one object, and one source can define it more than once: devicetype-library and an organization's own repository may both define Dell's PowerEdge R640, each right about some fields ([ADR 0013](0013-compose-objects-across-sources.md)). Until now `compare` compared each definition alone, so two sources' definitions of one device type showed as two unrelated results, and nothing noticed two device types that two sources would both create with one slug.

Milestone 1 ends when `compare` shows every source's definition of an object and NetBox's side by side. The web UI (milestone 3) shows the same on an object's page, and lists objects by outcome, from what the database records, in under a second ([ADR 0029](0029-keep-a-lookup-table-of-definitions.md)). How are definitions grouped into objects, what is an object's outcome, what is compared, and what is recorded?

## Decision drivers

- An object's comparison shows each definition and NetBox's object, field by field, and says where they differ.
- What is recorded of a definition doesn't depend on which other sources were compared with it, so that each source's records can be replaced on their own.
- A page can build an object again from what is recorded, without comparing again.
- Grouping and conflicts follow NetBox's own uniqueness rules, through the specs ([ADR 0025](0025-match-by-netbox-uniqueness-rules.md)), so that a new type needs no code here.
- One source costs no more than before; two cost at most a quarter more than comparing their definitions one by one did.

## Considered options

1. Compare each definition alone, and lay several sources' results side by side only in the output
2. Group definitions into objects, and build each object by merging its definitions' own comparisons
3. Compare all of an object's definitions at once, in the engine

## Decision

Chosen option: 2, objects merged from each definition's own comparison.

- **Grouping.** Definitions from the sources compared are grouped by their type and identity as NetBox would store them: each identity field normalized through its spec, such as a model trimmed, and a reference by the key of what it refers to. The key comes from each definition's unique values, before anything is read whole ([ADR 0030](0030-compare-what-netbox-has-by-default.md)). Objects come in the order of their first definition, sources in the configuration's order and each in its format's, so one source keeps the order it had; each object has a column for each definition, in the same order, then NetBox's object. A source that defines an object more than once gives it a column for each definition.
- **Scope.** By default an object is compared if any of its definitions' unique values match or collide with a NetBox object; then all of its definitions are read whole and compared, absent ones included. A selection, or `--all`, compares every definition of an object it names, or none.
- **Two outcomes.** Each definition keeps its own outcome against NetBox, as before; the sources' counts and the records count definitions. An object is a conflict if one of its definitions is, if a source defines it more than once, or if it clashes; otherwise it has its definitions' outcome, which they all share, since one identity matches one NetBox object or none. An object is in step when it is a candidate and NetBox and every definition agree on every managed row. `--exit-code` counts objects.
- **Clashes.** Two compared objects clash when a definition of each would leave NetBox holding the same values for one of their type's other uniqueness rules: an import of each, as its object's definition, would hold its own value where it manages the field, and NetBox's object's otherwise. A rule over a field that would be unset or null doesn't hold. Values a NetBox object either object matches holds already aren't a clash, since NetBox's comparison already reports a collision on the definition that would change them. Clashes are found among the objects compared, never adding to them, and from the definitions' comparisons alone.
- **Rows.** An object's rows are every field any version sets, then each collection's children: those any definition has, in the order of the first that has each, then those only NetBox has. A definition's cell is its own, or unset where it doesn't set the field; on a child's row, false where it has the child's parent but not the child, and unset where it hasn't the parent either. A row differs where two cells that are set differ, and is managed where some definition manages it, so an object's differences count those between definitions too.
- **Records.** Each definition's comparison is recorded alone, as before, with no change to the database. An object is built by merging its definitions' comparisons (`compare.Objects`), which refuses comparisons whose NetBox sides differ. Clashes and definitions repeated in a source are found again from the comparisons and the definitions; neither is recorded. The scopes replace records as before; a definition compared because of another definition of its object is recorded too, so whether a definition has a record can depend on the sources compared with it, though what it records doesn't.
- **Output.** `compare` prints objects: in text, a line for each object, a column for each definition in its differences, and the reasons for each conflict; in JSON, one entry in `objects` for each, with the fields a comparison has, given the object's meaning, plus `duplicated_in`, `clashes`, and each definition's own outcome, differences and collisions in its column, and a top-level `counts` of objects. A script that read NetBox's cell as the second must find NetBox's column by its kind.

### Consequences

- Good, because one view shows every source's definition and NetBox's object, with the differences between sources counted.
- Good, because each record is independent of other sources, and pages can build objects from records with the same function.
- Good, because grouping, clashes and merging are generic: they read the specs, and any number of sources.
- Bad, because an object built from records of different runs may have NetBox sides that disagree; the merge refuses those, and the web UI must compare again or say so.
- Bad, because a selection or `--all` over the library now shows four pairs of its manufacturers, whose derived slugs are the same, as conflicts, where they were absent with a warning.
- Bad, because the text and JSON outputs change shape: the table's columns move, and a second source moves NetBox's cell.

## Pros and cons of the options

### Lay results side by side in the output

- Good, because nothing changes underneath.
- Bad, because an object's outcome, its differences between sources, and clashes would be the output's to work out, and the web UI would have to repeat it.

### Merge each definition's comparison

- Good, because records stay one per definition, independent of other sources, and the merge is a pure function of them.
- Bad, because merging must reproduce what a direct comparison would give; `FuzzCompare` holds it to that, with up to three sources.

### Compare an object's definitions at once

- Good, because the table comes straight from the engine.
- Bad, because a comparison would depend on the other sources compared, so a source's records couldn't be replaced alone, and the web UI couldn't build an object from records.

## More information

- Step 15 of the [roadmap](../roadmap.md), and its pull request, with measurements against the whole library and a local NetBox.
- NetBox's rules followed: a manufacturer's `name` and `slug` are each `unique=True` (`OrganizationalModel` in `netbox/models/__init__.py`), and a device type's manufacturer with model, and with slug, are `UniqueConstraint`s (`DeviceType.Meta` in `dcim/models/devices.py`), none of them ignoring case.
