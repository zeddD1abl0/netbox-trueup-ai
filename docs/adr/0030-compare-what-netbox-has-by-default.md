---
status: accepted
date: 2026-11-19
decision-makers: "@zeddD1abl0"
---

# Compare what NetBox has by default, matching definitions by their unique values first

## Context and problem

Until now `compare` compared every definition of every source. Against devicetype-library that is 13,781 definitions, of which, on a NetBox that holds a few device types, all but a handful are absent: `--exit-code` always fails, the JSON runs to 220 MB, and the database records 111 MB of comparisons that say "NetBox hasn't got this". A NetBox that TrueUp is meant for may hold 15 manufacturers and 33 device types. In an operational setting the question is whether what NetBox has matches its definitions; a device NetBox hasn't got yet is found by searching the definitions (`list --search`), and then compared by name.

Comparing only what NetBox has means knowing, for every definition, whether it matches or collides with a NetBox object, without reading every definition whole: decoding them costs a second and hundreds of megabytes for the library, and the web UI (milestone 3) must answer in under a second.

What should `compare` compare by default, how does it find that without reading every definition, and what happens to the comparisons it records?

## Decision drivers

- An operational run compares what NetBox has, and `--exit-code` fails only for that.
- Whether a definition is in scope is decided by the same matching that compares it ([ADR 0025](0025-match-by-netbox-uniqueness-rules.md)), not by a second set of rules that could disagree.
- Each object type's rules come from its spec ([ADR 0008](0008-describe-object-types-with-specs.md)), so a new type or rule needs no code of its own here.
- A run records its comparisons only against the load of the source whose definitions it read, never against one that replaced it meanwhile ([ADR 0029](0029-keep-a-lookup-table-of-definitions.md)).
- `--all` costs no more than comparing everything did.

## Considered options

1. Compare every definition, as before, and filter the output
2. Match from the definitions table's existing columns (identity, manufacturer, model, slug)
3. Keep each definition cut down to what matching reads, its unique values, and match from those
4. Ask NetBox for only the objects the definitions name

## Decision

Chosen option: 3, keep each definition's unique values and match from those, with NetBox read as before.

- **Scopes.** By default `compare` compares the definitions that match a NetBox object, or would collide with one: candidates and conflicts. Those NetBox hasn't got are counted, not compared, listed or recorded. `--manufacturer` and `--model` compare what they name, absent included: a manufacturer and its device types; the device types with a model, in every manufacturer, or in those `--manufacturer` names; and the manufacturers of the device types compared, which an import of them needs, and whose comparisons theirs refer to. Names match as NetBox would store them (white space trimmed, case kept), and a name nothing defines exits 4. `--all` compares every definition, and is a usage error beside a selection. JSON output says which in a top-level `scope`: `netbox`, `selection` or `all`. Linked objects join the default scope in milestone 2.
- **Unique values.** A load stores beside each definition its unique values (`compare.UniqueValues`): its type, identity and path, the fields its type's identity and uniqueness rules name, its references to objects that stand on their own, and which of those the format derived, as JSON of the definition's own form. Matched with the same NetBox objects, they get the whole definition's match, collisions and unresolved references, and so its outcome, but for one case: a candidate whose NetBox object holds two templates with one name is a conflict, which only the whole definition, with its templates, finds. Absent is absent either way. `FuzzCompare` holds the engine to that.
- **Two reads.** `compare` reads every definition's unique values with the status of the source's load, reads NetBox's manufacturers and the device types of those the definitions name, as before, matches, and then reads whole, by ID, only the definitions it compares, checking that the load is still the one it read. The templates of what they match are read last, as before.
- **Records.** Comparisons are kept by their definition's ID, and a load that removes a definition removes its comparison. A run of the default scope, or of `--all`, looked at every definition, so its comparisons replace all of the source's; a selection replaces only those of what it compared. Recording checks, in the source's transaction, that the source's snapshot is the version compared and that its latest load is the one whose definitions were read: a load of the same version by another release, or other settings of the source, would otherwise slip past the snapshot's check.

### Consequences

- Good, because `--exit-code` and the output say what matters to a running NetBox, and a NetBox with nothing compares nothing.
- Good, because scope and comparison can't disagree: one matching, through the specs.
- Good, because the unique values are generic: a new object type or uniqueness rule needs no code here.
- Good, because comparisons keyed by ID drop the row-valued `NOT IN` that PostgreSQL couldn't always hash.
- Bad, because a definition NetBox hasn't got is seen only with `--all` or a selection; scripts that read absent definitions from the default output must add `--all`.
- Bad, because the migration leaves existing definitions without unique values, so each source is loaded again on first use, a few seconds for the library; comparisons of definitions not loaded since go, and the next compare makes them again.
- Bad, because each definition takes about 100 more bytes in the database.

## Pros and cons of the options

### Compare everything, and filter the output

- Good, because nothing changes underneath.
- Bad, because every definition is still read whole and compared, for nothing: the cost the web UI can't afford.

### Match from the existing columns

- Good, because no migration of definitions is needed.
- Bad, because it needs code for each object type to turn columns back into a definition, which a new rule would silently miss.
- Bad, because the columns can't tell a slug the format couldn't make from an empty one, and don't say which values were derived.

### Ask NetBox for only what the definitions name

- Good, because a large NetBox would send less.
- Bad, because the library names 407 manufacturers and 13,374 models, each with a slug: about 10 requests instead of one for the manufacturers, and hundreds instead of a few for the device types.
- Bad, because NetBox's filters compare names as its collations do, and TrueUp would have to prove them exact; matching in memory already is.

## More information

- Step 14 of the [roadmap](../roadmap.md), and its pull request.
- Measurements, against the whole library and a local NetBox, are in the pull request.
