# Using TrueUp from the command line

This guide takes you from a configuration file to a comparison of [devicetype-library](https://github.com/netbox-community/devicetype-library) with your NetBox: which of the library's manufacturers and device types NetBox already has, which it lacks, and how those it has differ, field by field.

So far TrueUp only reads. It fetches the library, reads its device types as NetBox's own YAML import would, and compares them with what NetBox holds. Nothing writes to NetBox yet: importing and syncing come in milestone 2 ([roadmap](roadmap.md)). Component templates, such as interfaces and power ports, aren't compared yet either.

Every setting mentioned here is described in [configuration.md](configuration.md), and every command's `--help` lists its flags and exit codes.

## Before you start

You need:

- the `netbox-trueup-ai` binary. From a clone of this repository, `make build` puts it in `bin/`;
- a NetBox 4.7, and a v2 API token for a user with the view permission on manufacturers and device types. The token needn't be write-enabled;
- a directory TrueUp can keep its data in: a cache of each source, and a SQLite database, unless `database.url` names a PostgreSQL one.

Write a config file, such as `/etc/netbox-trueup-ai/config.yaml`, or anywhere you name with `--config`:

```yaml
data_dir: /var/lib/netbox-trueup-ai
netbox:
  url: https://netbox.example.com
  token_file: /etc/netbox-trueup-ai/netbox-token
sources:
  - name: devicetype-library
    kind: git
    url: https://github.com/netbox-community/devicetype-library.git
    ref: master
    format: devicetype-library
    # Leaves out the library's elevation images: a 64 MB download, not 490 MB.
    max_file_size: 128KiB
```

The token file holds the token alone, and should be readable by you only. To read only some manufacturers, give the source `include` patterns, such as `["device-types/Dell/**"]`; the library's schemas are added for you ([Patterns](configuration.md#patterns)).

## Check NetBox and fetch the library

```sh
netbox-trueup-ai netbox check
netbox-trueup-ai source fetch
```

`netbox check` connects, checks NetBox's release and the token, and lists what the token can read ([Connecting to NetBox](configuration.md#connecting-to-netbox)). `source fetch` downloads the library's latest commit into the cache, and records the fetch in the database ([Fetching sources](configuration.md#fetching-sources)). Fetch again whenever you want the library's latest version: `list` and `compare` read the version last fetched, unless you give them `--fetch`.

## List what the library defines

```sh
netbox-trueup-ai list
```

`list` prints each source's summary, then each manufacturer followed by its device types, then warnings and invalid files. It needs neither NetBox nor the database. A manufacturer is derived from each name the library's device types give, since the library has no files for manufacturers, with the slug NetBox's web UI would make from the name, marked `(derived)`. TrueUp uses a derived slug only to create a manufacturer, never to change one NetBox has.

A file that fails is reported with its path, line, column, field and reason, and skipped, such as a value NetBox would refuse. [Listing definitions](configuration.md#listing-definitions) describes what `list` reads, how it validates each file, and its JSON.

## Compare with NetBox

```sh
netbox-trueup-ai compare
```

```text
NetBox:  https://netbox.example.com, release 4.7.2

SOURCE              REF     VERSION                                   COMPARED  IN STEP  DIFFERING  ABSENT  CONFLICTS  INVALID  WARNINGS
devicetype-library  master  193473cbce8d8ce8171779dc9d76b9fd22ea8e7c  13781     5        1          13774   1          0        4

SOURCE              OUTCOME    TYPE          MANUFACTURER  MODEL           NETBOX  DIFFERENCES  FROM
devicetype-library  absent     manufacturer  2N            -               -       -            2 files
devicetype-library  conflict   manufacturer  NETGEAR       -               -       -            1 file
devicetype-library  candidate  device type   Dell          PowerEdge R640  #3      1            device-types/Dell/PowerEdge-R640.yaml
...

Conflicts:
  devicetype-library: manufacturer "NETGEAR" (1 file): its slug matches NetBox's manufacturer #3, "Netgear"

Differences:
  devicetype-library: device type Dell "PowerEdge R640" (device-types/Dell/PowerEdge-R640.yaml), NetBox #3:
    FIELD     DEFINITION  NETBOX
    u_height  1           2

Warnings:
  devicetype-library: manufacturers "NETGEAR" and "Netgear" would both get the slug "netgear", ...
```

The summary counts each source's definitions by outcome. The table below it lists every definition that isn't in step with NetBox; those in step are only counted. Then come the conflicts, the differences of each definition NetBox has, and the warnings and invalid files that `list` would show.

### Outcomes

Each definition is matched with NetBox's objects by NetBox's own uniqueness rules: a manufacturer's name, and its slug; a device type's manufacturer and model, and its manufacturer and slug ([ADR 0025](adr/0025-match-by-netbox-uniqueness-rules.md)). One of those rules is the definition's identity: a manufacturer's name, and a device type's manufacturer and model.

| Outcome | Means | Importing it would |
| --- | --- | --- |
| candidate | Its identity matches one NetBox object, and no other rule matches a different object. | link it to that object |
| absent | Nothing in NetBox matches it. | create it |
| conflict | Another rule matches an object that isn't its match, so NetBox couldn't hold both. | wait for a person to decide |

A candidate with no differences is **in step**. Some conflicts you will meet:

- A device type whose model matches one device type, and whose slug another: two device types of NetBox's are the library's one.
- A device type whose slug alone matches: often a device type renamed in the library, or in NetBox.
- A manufacturer whose name NetBox lacks, but whose derived slug another has: the library's `NETGEAR` beside NetBox's `Netgear`. Settle it in NetBox, by giving one of them another slug, or in the library.

Values are compared exactly, case and white space included, as NetBox's database compares them: `Dell` and `dell` are different manufacturers, to NetBox's import as to TrueUp. A device type whose manufacturer NetBox lacks is absent, and its JSON says what the manufacturer's own definition found. A NetBox that holds two objects with the same name or slug, which its own rules forbid, can't say which one a definition is: the definition is a conflict, and so are the device types of a manufacturer NetBox holds twice.

### Differences

A candidate's fields are compared as NetBox stores them, so that only real differences show: `u_height: 1` equals NetBox's `1.0`, text is compared after trimming white space at both ends, as NetBox trims it, and a blank choice equals NetBox's null.

Only the fields a definition sets are compared. devicetype-library leaves out fields such as `end_of_life` and `exclude_from_utilization`, and many of its files leave out `airflow` or `part_number`: whatever NetBox holds there isn't a difference, since TrueUp won't manage those fields ([ADR 0006](adr/0006-manage-only-linked-objects.md)). A derived value, such as a manufacturer's slug, isn't a difference either: TrueUp uses it only to create the manufacturer. JSON output shows every field side by side, these included.

### Choosing what to compare

- `compare devicetype-library` compares only the sources named.
- `--manufacturer NAME` compares only that manufacturer, by its exact name, and its device types. Repeat it for more. A name no source defines gets a warning, and the exit code 4, so that a mistyped name doesn't pass for a comparison in step. TrueUp then reads only those manufacturers' device types from NetBox.
- `--fetch` fetches each source first, as `source fetch` does.

### Exit codes

| Code | Means |
| --- | --- |
| 0 | Every source was read and its comparisons recorded, and every file is valid, whatever the outcomes. |
| 1 | The command couldn't run: NetBox can't be reached, runs another release, refuses the token, or the token can't read manufacturers or device types, or the database can't be opened or written. |
| 2 | A name given isn't a configured source. |
| 3 | The configuration is invalid, or `netbox.url` or the token isn't set. |
| 4 | Some sources couldn't be read, or their comparisons recorded, or some files are invalid, or no source defines a manufacturer `--manufacturer` names. The rest is compared and printed as usual. |
| 5 | Only with `--exit-code`: everything was read, and some definitions are absent, in conflict or differ. |

`--exit-code` suits a scheduled job that should fail when NetBox falls behind the library.

### Permissions

TrueUp sees what the token's permissions let it see. A token that can't read manufacturers or device types at all fails the command. A permission with constraints, such as one limited to some manufacturers, can't be detected: what it hides shows as absent.

### Recorded comparisons

Each run records the comparison of each definition it compared in the database, as the latest of that definition, replacing the one before: when it was made, against which version of the source and which NetBox, and the result, as `--output json` prints it. Comparisons of definitions the library no longer holds are removed. With `--manufacturer`, the other definitions keep the comparisons recorded before. A comparison is recorded only against the version of the source that the database holds: if the source was fetched again while it was compared, nothing is recorded for it, and `compare` says so and exits with 4. The web UI will read these records; for now they are in the `comparisons` table.

## Reading the JSON

With `--output json`, `compare` prints one document whose fields scripts can rely on ([ADR 0018](adr/0018-cli-conventions.md)):

- `ok`: whether every source was read and recorded, and every file is valid;
- `netbox`: its `url` and `version`; and `compared_at`, in UTC;
- `sources`: for each, `name`, `format`, `ref`, `version`, `ok`, `error`, `files`, `invalid_files`, `definitions` and `compared`, the counts `in_step`, `differing`, `absent` and `conflicts`, and `problems`, as `list` gives them;
- `objects`: the comparison of each definition, source by source;
- `warnings`, each with a `message`.

Each object has:

- `type` and `identity`, such as `{"manufacturer": {"name": "Dell"}, "model": "PowerEdge R640"}`;
- `outcome`, `in_step`, `netbox_id` (null unless the identity matches) and `differences`, the count;
- `collisions`, each with its `rule`, such as `["manufacturer", "slug"]`, and the NetBox object's `netbox_id` and `identity`;
- `unresolved`: references to objects NetBox lacks, each with its `field`, `type`, `key`, `netbox_ids`, the objects that all have the key when NetBox holds more than one, and the `outcome` of that object's own definition, or null;
- `columns`: the versions compared, the definition first, with its `source`, `path` (null for a derived manufacturer), `derived_from` and `unmapped`, the component lists not compared yet, then NetBox's object, with its `netbox_id`, when there is one;
- `rows`: each field any version sets, with its `path`, a list of steps, one `cell` per column, `{"value": ...}` or `{}` where that version doesn't set the field, and `differs`, `managed` and `derived`.

A path is a list so that later releases can add steps into a device type's components, by their names.

Some questions it answers, with [jq](https://jqlang.org):

```sh
# The device types NetBox lacks, as manufacturer and model.
netbox-trueup-ai compare -o json | jq -r '.objects[]
  | select(.type == "dcim.devicetype" and .outcome == "absent")
  | "\(.identity.manufacturer.name)\t\(.identity.model)"'

# Every field that differs, with both values.
netbox-trueup-ai compare -o json | jq -c '.objects[] | select(.differences > 0)
  | {model: .identity.model, netbox_id, fields: [.rows[] | select(.differs and .managed)
  | {field: .path[0], library: .cells[0].value, netbox: .cells[1].value}]}'

# The conflicts, with what each collides with.
netbox-trueup-ai compare -o json | jq -c '.objects[] | select(.outcome == "conflict")
  | {identity, collisions}'
```

## Try it against a disposable NetBox

To see a comparison without a NetBox of your own, start the disposable one this repository uses for its tests (Docker needed, [internal/netboxtest](../internal/netboxtest/README.md)):

```sh
make netbox-up
export TRUEUP_NETBOX_URL=http://127.0.0.1:8000
export TRUEUP_NETBOX_TOKEN_FILE=$PWD/.cache/netbox/token
```

Never point these commands, or the token in that file, at another NetBox.

Then import a few of the library's device types through NetBox's own YAML import, in its web UI, signed in as `admin` with the password `insecure-test-only`:

1. Under Devices, Manufacturers, Import, paste the manufacturers, with the slugs `list` shows:

   ```yaml
   - name: Dell
     slug: dell
   ```

2. Under Devices, Device Types, Import, paste a file from the library, such as `device-types/Dell/PowerEdge-R640.yaml`, as it is.

Run `netbox-trueup-ai compare --manufacturer Dell`: the manufacturer and the device type you imported are in step, and Dell's other device types are absent. Edit the device type in NetBox, such as its height, and compare again to see the difference. `make netbox-down` removes it all.
