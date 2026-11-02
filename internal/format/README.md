# Formats

A format turns the files of a source's snapshot into definitions: objects of the object model as one source describes them ([ADR 0009](../../docs/adr/0009-source-kinds-and-format-adapters.md), [architecture.md](../../docs/architecture.md#formats-and-validation)). Everything a source holds is untrusted input, and each problem a format reports quotes what it found cut short; escape a problem before showing it.

| Package | What it holds |
| --- | --- |
| `format` | `Format`, `Definition` and `Problem`; `ParseYAML`, which reads a file as PyYAML's `SafeLoader` does, within limits; `CompileSchema` and `Schema.Validate`, for a source's own JSON schemas, with every reference outside them refused; and `Value`, which reads a scalar as a spec's field. |
| `format/devicetypelib` | devicetype-library's format: device types, and a manufacturer derived from each name they give. Component templates follow in roadmap steps 11 and 12. |

## Reading YAML as NetBox does

NetBox's own YAML import and devicetype-library's tests read files with PyYAML, which follows YAML 1.1, so `ParseYAML` resolves every unquoted value the same way, though go-yaml parses the file ([ADR 0024](../../docs/adr/0024-read-source-files-as-pyyaml-does.md)):

| Written | PyYAML, and TrueUp | YAML 1.2, and go-yaml |
| --- | --- | --- |
| `yes`, `On` | true | text |
| `010` | 8 | 10 |
| `02351930`, `08` | text | a number |
| `1:30` | 90 | text |
| `1e3` | text | 1000 |
| `1.0e+3` | 1000 | 1000 |
| `2024-01-01` | a date, which TrueUp refuses | text |

`testdata/pyyaml.json` holds PyYAML's answers for 1,954 unquoted values, and `TestResolveAsPyYAML` holds the reader to them. To add cases, edit `testdata/pyyaml.py` and run it with PyYAML 6:

```sh
python3 -I internal/format/testdata/pyyaml.py > internal/format/testdata/pyyaml.json
```

The limits are in `DefinitionLimits`, and a file that breaks one, or uses anchors, aliases, merge keys, tags or a key twice, fails with a `*ParseError` that says where.

## devicetype-library

`devicetypelib/testdata/library` holds unchanged copies of 20 device types and all six schemas from devicetype-library, with its CC0 licence; its README says where they came from and why each is there. `library.golden.json` is what `Read` makes of them, every definition and problem: after a change to the adapter, review the diff that `go test ./internal/format/devicetypelib -update` makes to it.

To read a whole checkout of the library, and fail on any file found invalid, as when the library changes:

```sh
go test ./internal/format/devicetypelib -run WholeLibrary -v -library "$PWD/.cache/devicetype-library"
```

`library_test.go` says how to make the checkout.

## Fuzzing

`FuzzParseYAML` and `FuzzReadDeviceType` run their seeds in `make test`. To fuzz:

```sh
go test ./internal/format -run '^$' -fuzz FuzzParseYAML -fuzztime 5m
go test ./internal/format/devicetypelib -run '^$' -fuzz FuzzReadDeviceType -fuzztime 5m
```
