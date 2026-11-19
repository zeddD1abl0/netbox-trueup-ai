---
status: accepted
date: 2026-11-18
decision-makers: "@zeddD1abl0"
---

# Keep each source's definitions in the database, and read them from there

## Context and problem

Until now `list` and `compare` read every file of every source each time they ran: about 2.8 seconds and 350 MB for devicetype-library's 13,187 files, and as much again for each further source. The web UI (milestone 3) needs a search across every source's definitions, and a page comparing one object's definitions in two or more sources with NetBox's, each in under a second on a local deployment, whatever the number of sources. Loading a source may take longer, as a job in the background, and so may a call to NetBox, if the page says so.

A file's name doesn't say what it defines: only 3,887 of the library's 13,374 device type files are named exactly as their model, and 1,515 not at all like it (`device-types/A10/TH4440.yaml` defines "Thunder 4440 ADC"). Finding what a source defines means reading its files, so a page can't afford to.

Where should definitions be read from, and when are a source's files read?

## Decision drivers

- A search and an object's definitions come from a query, in milliseconds, not from reading files.
- Each version of a source is read once, and the database says what read it, so that a new release of TrueUp that reads files differently reads them again.
- What the database keeps of a definition is what the format read, exactly, not a lossy copy.
- Both engines, one transaction per source, as fetches and comparisons have ([ADR 0021](0021-forward-only-migrations-when-opening.md), [ADR 0025](0025-match-by-netbox-uniqueness-rules.md)).
- A definition keeps an ID while its source keeps defining it, for pages to link to and, later, for comparisons and links to refer to.
- No new dependency.

## Considered options

1. Keep reading files on every run, and have the server (milestone 3) keep what it read in memory
2. Keep an index in the database, identity and search fields alone, and read the few files an object page needs from the source's cache
3. Keep each definition whole in the database, loaded once per version of the source, and read definitions only from there

## Decision

Chosen option: "keep each definition whole in the database", because every page and command then reads the same rows, the source's cache isn't needed to read them, and the cost of reading files is paid once per version, by the fetch.

- **Tables.** `definitions` has a row per definition of each source's latest loaded version: an internal ID, the source, the object type, the file's path, or `''` for a definition the format derives, the identity as JSON, which groups an object's definitions across sources, and its place in the format's order. It also has the manufacturer, model, slug and part number, which a definition is searched and shown by, the file's SHA-256, and the definition as `list --output json` gives it. `definition_problems` keeps the problems with the files, and `definition_loads` each source's loads: what the latest read, when, and why it failed, and the same of the latest that succeeded, with its counts. A load in progress isn't recorded: the process running it knows, and telling other processes, as the web UI's notice that a source is still loading will, needs the leases that milestone 6 brings, without which a process that dies leaves a load that never ends.
- **Loading.** `source fetch` reads each version it fetches with the source's format and records what it read, under the source's fetch lock, so that fetches and loads of a source take turns. `list` and `compare` read definitions from the database alone, and load a source first, under the same lock, when the database holds nothing that this release read from the source's latest version. A load replaces a source's definitions and problems in one transaction. Rows are written in place, so a definition the source still defines, of the same object in the same file, keeps its ID, and a row whose definition and place in the format's order are unchanged isn't rewritten; a file added early in the order moves the rest, which are rewritten, a fraction of a load's time. IDs are never reused: SQLite's `AUTOINCREMENT`, PostgreSQL's identity columns. A load that fails for what the source holds, such as schemas missing, is recorded, and leaves the definitions as they were; `list` and `compare` then report what it found, without reading the files again, until `source fetch`, or a change to the source's settings, tries again. A load that fails for this host's cache, gone, of another version than the database records, or unreadable, isn't recorded, since another host sharing the database, or this one a moment later, may read it. A definition a load can't keep exactly as it was read, which would be TrueUp's fault, is skipped as a problem with its file, and the rest are kept.
- **What read it.** Each format has a version (`format.Format.Version`), which the database keeps with what it loaded as the reader, with TrueUp's release, such as `devicetype-library/1 (TrueUp 1.2.0)`, so that each release loads its sources again once, whatever it changed in how they are read, and with a SHA-256 of the source's settings that choose the files read and say where they come from: its kind, url, ref, format, include and exclude patterns, `max_file_size` and `max_files`. A load by another reader, or with other settings, is read again, so that changing a source's patterns takes effect at once, as it did when every command read the files. A test holds the format's version to a SHA-256 of what the format makes of its test files, as stored, so that changing what it reads, or how definitions are encoded, fails until the version is raised, which matters between builds of one release. Definitions the database holds but this release can't read back, which a version left unraised would leave, are loaded again, with a warning.
- **Exactly what was read.** A definition is kept as the JSON `list` prints (`format.MarshalDefinition`). It is read back through its type's spec (`model.Spec.UnmarshalFields`), and a load checks that each definition reads back as the one the format gave before it records it (`format.EncodeDefinition`). Fuzzing the format checks the same of every file it takes.
- **Search.** A search looks in the manufacturer, model and part number, ignoring case: TrueUp lowers both the text kept and the search in Go, and the engines match them exactly, with SQLite's `instr` and PostgreSQL's `strpos`, so `%` and `_` mean themselves, and both engines find the same. A search with a control character in it is refused, so it never matches across two of the fields. There is no full-text index: a scan finds a model in 6 ms in one copy of the library, and in 60 ms in ten.
- **An object's definitions** are those with the same type and identity, by an index, not an array of IDs kept per object. SQLite has no arrays, a foreign key can't check an array's elements, so a definition removed upstream would stay in them, and loading one source would rewrite rows other sources share, taking the transaction of its own each source has. When an object is linked, in milestone 2, the link names its primary definition in a column of its own.
- **Pages.** Pages never read a source's files, or NetBox beyond the objects they show. Comparisons run as jobs, after a fetch, on a schedule or on a webhook, and pages read what they recorded.
- **Fetching.** `source fetch` reports how many definitions it loaded and how many files are invalid. If a version's files can't be read at all, it reports that too, but its exit code still says only whether the fetch worked: `list` and `compare` report the source as unreadable.

### Consequences

- Good, because `list` reads the library from the database in 1 second, instead of reading its files in 3, and a search takes 0.04 seconds, or 0.07 across two copies. `compare` takes 3.8 seconds, instead of 6.7 to 12.8, and 7.5 for two copies, instead of 13 to 14. Its output is the same as before.
- Good, because an object's definitions in every source take a query of a fraction of a millisecond, which milestone 3's pages and step 15's grouping read.
- Good, because what a page shows, what `list` prints and what `compare` compares are the same rows.
- Bad, because the database grows by about 32 MB for each copy of the library: 143 MB instead of 111 for one source.
- Bad, because the first `list` or `compare` after an upgrade, or after a fetch by another process into another cache, loads the source first: about 5 seconds for the library.
- Bad, because the version test sees only what the format makes of its test files: between builds of one release, a change that leaves them as they were still needs its author to raise the version, though definitions that then fail to read back are loaded again.
- Bad, because loading a source takes as much memory as reading it did, about 580 MB for the library, and sources are loaded one after another rather than at once, to keep that peak to one source's.
- Bad, because two releases sharing one database, as in a rolling upgrade, each load what the other loaded whenever they read it. Until high availability (milestone 6) runs loads as jobs on one node, run one release at a time.

## Pros and cons of the options

### Read files on every run, and keep them in memory in the server

- Good, because nothing new is stored.
- Bad, because every replica of the server reads every source when it starts, and holds all of it in memory: about 350 MB a copy of the library.
- Bad, because commands still take seconds to read every source, and the CLI and the server read definitions differently.

### Keep an index, and read an object's files from the cache

- Good, because the database grows less.
- Bad, because a page needs the source's cache, its JSON schemas compiled, and the file parsed and validated, on the node that serves it.
- Bad, because the index still needs a version of its reader, and comparisons still read every file.

## More information

- Measurements against the whole library, 2026-11-18: [roadmap step 13](../roadmap.md).
- Which definitions `compare` compares by default ([roadmap](../roadmap.md) step 14), and how definitions are grouped across sources (step 15), follow this decision in ADRs of their own.
- [Storage](../architecture.md#storage) and [Web UI](../architecture.md#web-ui-m3) in the architecture.
