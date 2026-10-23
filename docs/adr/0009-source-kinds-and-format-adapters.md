---
status: accepted
date: 2026-10-23
decision-makers: "@zeddD1abl0"
---

# Read sources through source kinds and format adapters

## Context and problem

Definitions will come from more than one kind of place: Git repositories first, then HTTP endpoints, local files, and possibly others. They will also come in more than one layout, and cover more object types over time: device types first, then module types, module type profiles, rack types and more. Only the latest content of a source matters: once a file changes, its old version is of no further use to TrueUp.

The first source is [devicetype-library](https://github.com/netbox-community/devicetype-library), read from Git. Its files don't match the shape of NetBox's API:

- component lists use hyphenated keys;
- port mappings are a separate list;
- the manufacturer is given by name;
- some keys aren't NetBox fields at all: `is_powered`, `front_image` and `rear_image`.

The library publishes JSON schemas for its files. All source content comes from places TrueUp doesn't control, so it is untrusted input.

## Decision drivers

- Add a source kind without changing formats or the engine, and add a format without changing source kinds.
- Use the library as it is, without asking users to convert it.
- Use only the latest content of each source, and record which version each comparison and sync used.
- Reject bad input before it reaches comparison or NetBox, with precise errors, and without one bad file blocking the rest.

## Considered options

1. Source kinds and format adapters as separate extension points, joined by snapshots
2. One adapter per combination of source kind and format
3. A single native TrueUp format, into which users convert the library
4. Passing library files straight to NetBox's own YAML import

## Decision

Chosen option: "Source kinds and format adapters as separate extension points, joined by snapshots".

**Source kinds** fetch content and produce snapshots:

- A snapshot is a source's latest content, read at a single version so that one run never mixes files from different versions. Its version is the latest commit on the configured branch or tag for Git, and a content digest for other kinds. It lists its files with their paths, sizes and digests. Nothing outside the source layer knows which kind produced a snapshot.
- Each fetch replaces the previous snapshot. Older versions are never read again. Comparisons, plans and links record the version they used, only to show what they were based on.
- Git is the first kind. HTTP and local files follow, and others can be added behind the same interface.
- Every kind applies the same rules: limits on file size and count, and paths confined to the snapshot, with symbolic links skipped.

**Format adapters** turn the files of a snapshot into definitions in the generic object model ([ADR 0008](0008-describe-object-types-with-specs.md)), whatever kind of source the files came from:

- Every file goes through these checks in order:
  1. a size limit;
  2. YAML parsing into nodes with `go.yaml.in/yaml/v3`, rejecting duplicate keys and limiting nesting depth and alias expansion;
  3. the format's JSON schema;
  4. mapping to definitions;
  5. validation against the specs.
- For devicetype-library, the JSON schemas come from the same snapshot as the files, so validation follows the library's rules as they change. They are compiled with remote references turned off.
- The adapter converts the format's layout to NetBox's, for example by folding `port-mappings` into the front port templates they belong to.
- Values the adapter derives instead of reading, such as the slug of a manufacturer that the library only names, are used only when TrueUp creates the object. For an existing object they aren't managed.
- Keys that aren't NetBox fields are kept as metadata and never sent to NetBox. A key that the format's schema allows but the adapter doesn't know yet is reported as a warning and ignored.
- An invalid file is reported with its path, line and column, and skipped. The rest of the snapshot is still used.
- A format grows with the object types it carries. The devicetype-library adapter starts with device types, and later reads the library's module and rack types.

### Consequences

- Good, because source kinds, formats and object types can each grow without changing the others.
- Good, because users can point TrueUp at the library as published.
- Bad, because the adapter has to keep up with changes to the library's format. Warnings show when it falls behind.
- Bad, because a schema change upstream can start rejecting files that used to pass. That is what the library intends, and the errors say why.
- Good, because no source kind has to serve old versions, and TrueUp never stores source content beyond a cache of the latest snapshot.
- Bad, because TrueUp can't show how a definition changed between versions. The source's own history, such as the Git log, does that.

## Pros and cons of the options

### One adapter per combination of source kind and format

- Good, because the first adapter is slightly simpler.
- Bad, because the number of adapters multiplies with every new kind or format, and each repeats the same validation.

### A single native format

- Good, because there is one format to maintain.
- Bad, because users would have to convert the library, and convert it again after every upstream change.

### Passing files to NetBox's YAML import

- Good, because NetBox interprets the files itself.
- Bad, because there would be no field-level comparison, overrides or provenance.
- Bad, because the YAML import is a UI view, not part of the REST API, and it creates components but can't update existing ones.
