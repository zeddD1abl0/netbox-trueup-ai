---
status: accepted
date: 2026-11-17
decision-makers: "@zeddD1abl0"
---

# Don't support inventory item templates

## Context and problem

The roadmap planned step 12 to compare a device type's inventory item templates, as a tree with references to other templates, and milestone 1's scope named them. NetBox 4.3 deprecated inventory items, and their templates, in favour of modules, and may remove them in a later release (`docs/release-notes/version-4.3.md`, netbox-community/netbox#19004; `docs/models/dcim/inventoryitemtemplate.md`). devicetype-library's schema still allows an `inventory-items` list, which NetBox 4.7.2's import still reads, but no file in the library uses one, as of the 2026-11-01 snapshot.

Should TrueUp compare them, and later sync them?

## Decision drivers

- TrueUp follows NetBox, and shouldn't build on a model NetBox is retiring.
- What a file holds that TrueUp doesn't compare should be said, not dropped quietly.
- The object model should keep what other object types will need.

## Considered options

1. Support inventory item templates, as planned
2. Don't support them, now or later

## Decision

Chosen option: "don't support them", because NetBox has deprecated them in favour of modules, which milestone 8 brings.

- TrueUp has no spec of `dcim.inventoryitemtemplate`, reads no endpoint of them, and doesn't ask `netbox check` about them.
- devicetype-library's format ignores a file's `inventory-items`, with a warning that NetBox has deprecated them and TrueUp neither checks nor compares them. The file stays valid: NetBox's import still takes inventory items, though it may refuse one TrueUp hasn't checked, such as an item without a name.
- The object model keeps what only the step 8 prototype of inventory item templates used: generic references (`ShapeTypeAndID`), fields children share with their parent (`Shared`), and trees, with their tests. Later object types need them: an IP address's assigned object is a generic reference, and locations are a tree.

### Consequences

- Good, because nothing TrueUp writes depends on a model NetBox may remove.
- Good, because a library file with inventory items says so, rather than its items going missing without a word.
- Bad, because a NetBox that still uses inventory item templates sees them neither compared nor synced: they stay as people leave them.

## Pros and cons of the options

### Support them, as planned

- Good, because NetBox 4.7 still has them, and its import still reads them.
- Bad, because the work, a tree with a generic reference to other templates and a gap in its uniqueness rule, would go to a model on its way out, with no data in the library to test against.

## More information

- Roadmap step 12 ([roadmap.md](../roadmap.md)) and milestone 1's scope changed with this decision. [ADR 0026](0026-compare-children-by-their-key.md) expected step 12 to add inventory items, as a tree, to the device type's collections; its decision stands, without them.
- [ADR 0027](0027-nest-port-mappings-in-their-front-port.md) settles the rest of step 12.
