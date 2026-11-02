---
status: accepted
date: 2026-11-02
decision-makers: "@zeddD1abl0"
---

# Read source files as PyYAML does, within fixed limits, and validate them with santhosh-tekuri/jsonschema

## Context and problem

Roadmap step 9 reads devicetype-library's device types. [ADR 0009](0009-source-kinds-and-format-adapters.md) settled the steps for each file: a size limit, YAML parsed into nodes with `go.yaml.in/yaml/v3`, duplicate keys rejected and nesting and aliases limited, the library's own JSON schemas from the same snapshot, compiled with remote references off, then mapping to definitions and validation against specs. It left open how YAML is read, which limits apply, and which JSON Schema library to use.

Two facts shape the answer:

- NetBox's YAML import reads files with PyYAML's `SafeLoader` (`BulkImportForm._clean_yaml` in `utilities/forms/bulk_import.py`), and so do the library's own tests (`tests/yaml_loader.py`). PyYAML follows YAML 1.1, where `yes` is true, `010` is 8, `1:30` is 90 and `1e3` is text. go-yaml resolves unquoted values by YAML 1.2's rules instead. Read go-yaml's way, 4 of the library's 13,187 device types fail its schema, though the library's CI and NetBox's import take them: `part_number: 02351930` in two Huawei files, and outlet names `08` and `09` in a Vertiv and an Eaton PDU. go-yaml reads them as numbers.
- Source files and schemas are untrusted (boundary B1 in [architecture.md](../architecture.md#trust-boundaries)). go-yaml builds a file's whole node tree before anything can count it, so a hostile file costs memory in proportion to its size. jsonschema libraries load what a schema refers to, and santhosh-tekuri/jsonschema's default loader reads local files.

## Decision drivers

- A file is valid in TrueUp when NetBox's import and the library's CI take it, and its values mean what they mean there.
- Reading one file costs bounded memory and time, whatever it holds.
- A schema refers only to the source's own schemas: nothing is loaded from the network or the file system.
- Errors say where, in path, line, column and field, and why, without repeating large values.
- Few dependencies, released at least 7 days before we adopt them.

## Considered options

Reading YAML:

1. go-yaml v3's node tree, with every unquoted value resolved by PyYAML's rules, by TrueUp
2. go-yaml v3's own resolution, by YAML 1.2's rules
3. goccy/go-yaml, with its own syntax tree
4. go-yaml v4, still a release candidate (rc.8 on 2026-11-02)

YAML features the library never uses, such as anchors and aliases:

1. Refuse them
2. Accept them, counting expanded nodes against the limit, with PyYAML's merge keys

Validating against JSON schemas:

1. santhosh-tekuri/jsonschema v6.0.3, of 2026-06-28
2. google/jsonschema-go v0.4.3
3. kaptinlin/jsonschema v0.9.10
4. xeipuuv/gojsonschema
5. No JSON Schema: validate against specs only

## Decision

Chosen options: "go-yaml v3's node tree, resolved by PyYAML's rules", "refuse them" and "santhosh-tekuri/jsonschema v6.0.3". `internal/format` holds the reader and the schema validation, which every format can use.

**Reading YAML.**

- go-yaml v3 parses a file into its node tree. TrueUp then resolves each unquoted scalar with PyYAML 6's implicit resolvers, from `yaml/resolver.py`, in their order, and constructs it as `SafeConstructor` does: booleans, whole numbers in binary, octal, hexadecimal, base 60 and decimal, numbers, and null. A test holds the reader to PyYAML's answers for 1,954 scalars, which a script in `internal/format/testdata` wrote by running PyYAML.
- A number is read into a binary float and kept as its shortest decimal form, as Python writes a float, and as NetBox's `DecimalField` and the library's `DecimalSafeLoader` read PyYAML's float. `1.00000000000000001` is 1.0 there, and here. Numbers stay exact from there on: JSON Schema sees `json.Number`, and specs see `model.Decimal`.
- A value with no JSON form or no place in NetBox is refused, with a hint to quote it if it is text: a date or time, which PyYAML would make a `date` that the library's schema refuses anyway; infinity and not a number; a whole number beyond 64 bits; and PyYAML's merge key `<<` and value indicator `=` outside the places it takes them.
- Quoted and block scalars are text, as in PyYAML.

**Limits and refusals**, fixed in code like the source layer's walk limits:

| | Limit | devicetype-library's largest |
| --- | --- | --- |
| A file of definitions | 256 KiB, whatever the source's `max_file_size` | 18.7 KB |
| Depth, the top mapping and the scalars at the bottom included | 16 | 4 |
| Nodes: mappings, lists, keys and values | 50,000 | 2,229 |
| Documents | exactly one, a mapping | one |
| JSON schema files | 32, each at most 1 MiB | 6, 49 KB in all |

A file is refused, with its position and the reason, for anchors and aliases, merge keys, explicit tags, keys that aren't text, keys that a mapping gives twice, reported with both lines, and more than one document. PyYAML would take most of them, keeping the last of two duplicate keys, but the library uses none, and its yamllint configuration rejects duplicate keys. The config loader refuses tags, anchors and aliases already.

**JSON schemas.**

- Every `schema/*.json` of the snapshot is registered under its own `$id`, such as `urn:devicetype-library:device-type`, as the library's tests register them, or under a name made from its path when it has none. Two with the same `$id`, or one that isn't JSON, fail the source.
- The compiler's loader refuses every URL, so a reference to anything else, `file:///etc/passwd` or an `https` URL, and a `$schema` naming another meta-schema, fail to compile. jsonschema's default loader would read local files.
- Draft 2020-12 unless a schema names another; `format` and content keywords annotate only, jsonschema's defaults; and patterns run as Go's regular expressions, in linear time.
- A failed check becomes a problem at the YAML node it concerns, worded by TrueUp for each keyword rather than in jsonschema's words, which list every value an enum allows. At most 20 problems a file, and a count of the rest.
- jsonschema v6.0.3 panics on a number whose exponent `big.Rat` can't hold, such as `1e9999999`. Upstream fixed that on 2026-10-31, after the last release. Every number reaches it through a float64, whose exponent is at most 308, so none can; a panic from anything else fails only the file.

### Consequences

- Good, because a file means in TrueUp what it means in NetBox's import and the library's CI: every one of the library's 13,187 device types passes its schema, read in 2.1 s on one core.
- Good, because the cost of a file is bounded: the worst 256 KiB file measured, a flow list of 131,074 nodes, held 23 MB of memory and parsed in 48 ms, against 110 MB and 0.5 s for 1 MiB.
- Good, because a schema can refer only to the source's own schemas.
- Good, because jsonschema v6 passes the official test suite, and adds only `golang.org/x/text`, which TrueUp has already at a newer version.
- Bad, because TrueUp is stricter than NetBox's import where the library is too: an alias, a duplicate key or a tag fails a file that NetBox would take. The errors say why.
- Bad, because a whole number written with a decimal point, such as `positions: 2.0`, passes a schema's `type: integer`, as JSON Schema says it should, though the library's CI, reading it as a Python `Decimal`, refuses it. NetBox's import takes it.
- Bad, because go-yaml's parser isn't PyYAML's: exotic syntax can parse in one and not the other. Fuzzing finds no file that breaks the reader's promises, but not every difference in what parses.
- Bad, because a hostile schema can still make validation slow, with references that fan out at every level, and jsonschema takes no context to stop it. The depth limit bounds the work but doesn't remove it. Only administrators add sources; the risk is recorded in the trust boundaries, accepted for milestone 1.

## Pros and cons of the options

### go-yaml v3's own resolution

- Good, because it is no code of ours.
- Bad, because it fails 4 valid library files, and would read others differently from NetBox's import wherever YAML 1.1 and 1.2 disagree.

### goccy/go-yaml

- Good, because it keeps positions and comments well.
- Bad, because it is a second YAML library for the same job, and resolves by YAML 1.2's rules too.

### go-yaml v4

- Bad, because it is a release candidate, and rc.8 came out the day of this decision.

### Accept anchors and aliases within a budget

- Good, because NetBox's import takes them.
- Bad, because it means expanding aliases, and PyYAML's merge keys, which no library file uses, in code that hostile files would aim at first.

### google/jsonschema-go and kaptinlin/jsonschema

- Bad, because both are before 1.0, and neither is as proven against the official test suite.

### xeipuuv/gojsonschema

- Bad, because it stops at draft-07, and the library's schemas use draft 2020-12's `dependentRequired`. It has had no release since 2019.

### No JSON Schema

- Good, because there is no dependency.
- Bad, because specs check fields, not a file's structure, such as its component lists, and the library's schemas are its own definition of a valid file ([ADR 0009](0009-source-kinds-and-format-adapters.md)).

## More information

- Measurements, on 2026-11-02, with devicetype-library at commit `e160491`: a spike validated every device type with jsonschema v6, in 1.7 s with PyYAML's rules and 4 failures with go-yaml's own; compiling the schemas took 3 ms. go-yaml v3.0.6 built 524,290 nodes in 110 MB from a 1 MiB flow list.
- PyYAML's resolvers and constructors: `yaml/resolver.py` and `yaml/constructor.py` in PyYAML 6.0.
- The reader and its tests: `internal/format`, and its README.
