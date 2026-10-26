# Configuration

This page lists every setting, and how TrueUp reads them. Why it works this way is recorded in [ADR 0012](adr/0012-layered-configuration.md).

To see the settings TrueUp would use, and where each came from, run:

```sh
netbox-trueup-ai config show
```

## Where settings come from

Each setting comes from the first of these layers that sets it:

1. command-line flags;
2. environment variables starting with `TRUEUP_`;
3. the config file;
4. defaults.

Once the web UI arrives, settings it stores in the database will sit between the config file and the defaults. A setting declared in one of the first three layers stays read-only in the UI.

An empty value counts as unset, so the next layer down is used. That covers an empty environment variable, an empty string in the file and a YAML `null`. A list, such as `sources`, comes whole from one layer: layers never merge lists.

Settings are checked whenever a command that reads them runs, which is every command but `version`, `help` and `completion`. Every problem is reported at once, with where its value came from. A configuration problem exits with code 3 ([ADR 0018](adr/0018-cli-conventions.md)). Settings that only some commands use, such as `netbox.url`, are checked for form on every run, but required only by the commands that use them.

## The config file

TrueUp reads one YAML file, the first of:

1. the file named by `--config`;
2. the file named by the `TRUEUP_CONFIG` environment variable;
3. `/etc/netbox-trueup-ai/config.yaml` on Linux and macOS, or `%ProgramData%\netbox-trueup-ai\config.yaml` on Windows, if it exists.

A file named by `--config` or `TRUEUP_CONFIG` must exist. TrueUp never looks for a config file in the working directory.

- Keys are snake_case, nested by section, as in the example below. A dotted key such as `netbox.url:` is an error: nest it under `netbox:` instead.
- Unknown keys and duplicate keys are errors, so a misspelt key never goes unnoticed.
- The file holds a single YAML document, without tags (such as `!!str`), anchors or aliases. Settings don't need them, and the YAML library's errors about them can quote a value, which could be a secret.
- Relative paths in the file are relative to the file's own directory.
- A setting that takes a number, such as `netbox.retries`, can be a YAML number or a string.
- The file can be at most 1 MiB.

```yaml
log:
  level: info
  format: json
data_dir: /var/lib/netbox-trueup-ai
netbox:
  url: https://netbox.example.com
  token_file: /run/secrets/netbox-token
sources:
  - name: devicetype-library
    kind: git
    url: https://github.com/netbox-community/devicetype-library.git
    ref: master
    format: devicetype-library
    include: ["device-types/Juniper/**"]
```

## Environment variables

A setting's environment variable is `TRUEUP_` followed by its key in upper case, with dots as underscores: `database.url` is `TRUEUP_DATABASE_URL`, and `netbox.token_file` is `TRUEUP_NETBOX_TOKEN_FILE`.

- Names are upper case. An unknown variable starting with `TRUEUP_`, in any case, is an error, which catches misspelt names. On Windows, which ignores the case of variable names, any case works.
- `TRUEUP_SOURCES` takes the sources list as JSON, such as `[{"name":"lib","kind":"git","url":"https://example.com/lib.git","format":"devicetype-library"}]`. The config file suits lists better.
- Relative paths are relative to the working directory.

## Secrets

`netbox.token` and `database.url` are secrets. Each has a companion setting ending in `_file` that names a file to read the secret from, which suits Docker and Kubernetes secrets: `netbox.token_file`, or `TRUEUP_NETBOX_TOKEN_FILE` in the environment.

- Setting both a secret and its `_file` companion in the same layer is an error. Across layers, the higher layer wins, whichever form it uses.
- A secret is never accepted as a flag's value, because other users of a host can see command lines. The flags `--netbox-token-file` and `--database-url-file` take a file instead.
- A trailing line break in a secret file is dropped. The file can be at most 64 KiB.
- TrueUp warns when a secret file, or a config file holding a secret's value, can be read by every user on the host. Restrict it with `chmod o-r`. For a Kubernetes secret volume, set `defaultMode: 0440` along with the pod's `fsGroup`; for a Docker secret, set its `mode`. Windows permissions aren't checked.
- Secrets are never logged, and `config show` displays them as `[redacted]`.

## Settings

| Key | Environment variable | Flag | Default | Description |
| --- | --- | --- | --- | --- |
| `log.level` | `TRUEUP_LOG_LEVEL` | `--log-level` | `info` | The lowest level to log: `debug`, `info`, `warn` or `error`. |
| `log.format` | `TRUEUP_LOG_FORMAT` | `--log-format` | `text` | `text` for terminals, or `json` for log pipelines. Logs go to stderr. |
| `data_dir` | `TRUEUP_DATA_DIR` | `--data-dir` | `/var/lib/netbox-trueup-ai`; `%ProgramData%\netbox-trueup-ai` on Windows | The directory for the database and source caches. |
| `database.url` | `TRUEUP_DATABASE_URL` | none | not set | Secret. A `sqlite`, `postgres` or `postgresql` URL. When it isn't set, TrueUp uses the SQLite database `netbox-trueup-ai.db` in the data directory. A PostgreSQL URL takes libpq's parameters, `sslmode` among them. See [The database](#the-database). Percent-encode special characters in a password. |
| `database.url_file` | `TRUEUP_DATABASE_URL_FILE` | `--database-url-file` | not set | A file to read `database.url` from. |
| `netbox.url` | `TRUEUP_NETBOX_URL` | `--netbox-url` | not set | NetBox's URL, such as `https://netbox.example.com`, with no user name, password, query or fragment. `http` is accepted, but TrueUp warns unless the host is this machine (`localhost`, `127.0.0.1` or `::1`), because the token would cross the network unencrypted. |
| `netbox.token` | `TRUEUP_NETBOX_TOKEN` | none | not set | Secret. The NetBox API token: printable ASCII, with no spaces. That catches what a token file can carry unseen, such as a byte-order mark. |
| `netbox.token_file` | `TRUEUP_NETBOX_TOKEN_FILE` | `--netbox-token-file` | not set | A file to read `netbox.token` from. |
| `netbox.ca_bundle` | `TRUEUP_NETBOX_CA_BUNDLE` | `--netbox-ca-bundle` | not set | A PEM file of CA certificates to trust for NetBox's certificate, as well as the system's, such as a private CA's. It holds only certificates, and is read whenever settings are. |
| `netbox.timeout` | `TRUEUP_NETBOX_TIMEOUT` | `--netbox-timeout` | `30s` | The limit on each request to NetBox, reading the answer included: a number and a unit, such as `30s` or `2m`, from `1s` to `10m`. |
| `netbox.max_requests` | `TRUEUP_NETBOX_MAX_REQUESTS` | `--netbox-max-requests` | `4` | How many requests to NetBox may be in flight at once, from 1 to 32. |
| `netbox.retries` | `TRUEUP_NETBOX_RETRIES` | `--netbox-retries` | `3` | How many more times a read from NetBox that failed is tried, from 0 to 10. See [Connecting to NetBox](#connecting-to-netbox). |
| `netbox.max_response_size` | `TRUEUP_NETBOX_MAX_RESPONSE_SIZE` | `--netbox-max-response-size` | `32MiB` | The most TrueUp reads of one answer from NetBox: a number of bytes, or one with `KiB`, `MiB` or `GiB`, from `1MiB` to `1GiB`. A larger answer is an error. |
| `sources` | `TRUEUP_SOURCES`, as JSON | none | none | The sources to read definitions from. See below. |

### Sources

Each source in the `sources` list has these fields:

| Field | Required | Description |
| --- | --- | --- |
| `name` | Yes | A name unique among the sources: up to 63 lowercase letters, digits, dots, hyphens and underscores, starting and ending with a letter or digit. Names Windows reserves for devices, such as `con` and `nul`, aren't allowed. |
| `kind` | Yes | How the source is fetched. Only `git` for now. |
| `url` | Yes | For Git, an `https` or `ssh` URL, such as `https://github.com/netbox-community/devicetype-library.git` or `git@github.com:netbox-community/devicetype-library.git`. An `https` URL can't hold a user name or password, since a token can pass as a user name. An `ssh` URL can name a user, but not a password. Neither can have a query or fragment. |
| `ref` | No | The Git branch or tag to follow. When it isn't set, TrueUp follows the repository's default branch. |
| `format` | Yes | How the source's files are laid out. Only `devicetype-library` for now. |
| `include` | No | [Patterns](#patterns) of paths to read, relative to the top of the source, such as `device-types/Juniper/**`. When it isn't set, TrueUp reads everything. |
| `exclude` | No | Patterns of paths to skip, even ones `include` chooses. |
| `max_file_size` | No | The largest file to read: a number of bytes, or one with `KiB`, `MiB` or `GiB`, from `1KiB` to `64MiB`. Default `1MiB`. Larger files are skipped and reported, and a Git fetch doesn't download them. |
| `max_files` | No | The most files the source may hold once its patterns apply, from 1 to 1000000. Default `50000`. A source with more fails to fetch. |
| `fetch_timeout` | No | The limit on one fetch of the source: a number and a unit, such as `15m`, from `10s` to `2h`. Default `15m`. |
| `max_fetch_size` | No | The most one fetch of the source may download, and write to disk, as a size from `16MiB` to `64GiB`. Default `2GiB`. |

A limit can be a YAML number or a string, like other settings that take numbers. `config show` shows each source's limits, with `default` as where a limit came from when the source doesn't set it.

### Patterns

`include` and `exclude` hold patterns, matched against each file's path from the top of the source, with `/` between directories:

- `*` matches any characters within one directory or file name, `?` any one character, and `[...]` one character of a class, such as `[A-C]`, or `[^...]` one character outside it. A backslash makes the character after it match itself.
- `**`, as a whole name between slashes, matches any number of directories, none included: `device-types/**/*.yaml` matches `device-types/x.yaml` and `device-types/Juniper/x.yaml`.
- A pattern that matches a directory matches everything in it, so `device-types/Juniper` chooses the same files as `device-types/Juniper/**`.
- Matching is case-sensitive.

A file is read when an `include` pattern matches it, or `include` isn't set, and no `exclude` pattern does. A pattern can't start or end with `/`, or contain `.` or `..` as a name.

## Connecting to NetBox

Commands that read NetBox need `netbox.url` and `netbox.token`. To check them, and what the token can read, run:

```sh
netbox-trueup-ai netbox check
```

It connects, refuses a NetBox release other than 4.7.x ([ADR 0003](adr/0003-initial-netbox-target.md)), and confirms the token. Then it reads at most one object of each type that TrueUp reads, and writes nothing:

```text
NetBox:  https://netbox.example.com, release 4.7.2 (API 4.7)
Token:   v2, for user trueup

OBJECT TYPE                     ENDPOINT                                  READABLE
dcim.manufacturer               /api/dcim/manufacturers/                  yes
dcim.devicetype                 /api/dcim/device-types/                   yes
dcim.consoleporttemplate        /api/dcim/console-port-templates/         no
...
```

- **The token.** Use a v2 token, `nbt_<key>.<token>`, for a user with the view permission on each object type listed. TrueUp only reads for now, so the token needn't be write-enabled. A permission with constraints counts as readable, but TrueUp then sees only the objects it allows. A v1 token still works with NetBox 4.7, with a warning, until NetBox 5.0 removes them.
- **Exit codes.** 0 when the token can read every type, and 4 when it can't read some, with the result printed as usual. 1 when the check couldn't be made, such as when NetBox can't be reached, runs another release or refuses the token, and 3 for a configuration problem ([ADR 0018](adr/0018-cli-conventions.md)).
- **JSON.** With `--output json`, the result is one document whose fields scripts can rely on: `url`, `netbox_version`, `api_version`, `user`, `token_version`, `ok` (whether the token can read every type), `object_types` with `object_type`, `endpoint` and `readable` for each, and `warnings`, each with `message` and, where it is about one setting, `key`.
- **TLS.** TrueUp verifies NetBox's certificate against the system's trust store, and the certificates in `netbox.ca_bundle` if it is set. Nothing turns verification off. Plain `http` works, with a warning unless NetBox is on this machine.
- **Proxies and redirects.** For an `https` NetBox, TrueUp uses the proxy that `HTTPS_PROXY` names, except for hosts in `NO_PROXY` and this machine. It never sends plain `http` through a proxy, which would see the token, so an `http` NetBox is reached directly. It never follows a redirect, so that the token can't be led elsewhere: point `netbox.url` at the address NetBox's API answers on. A reverse proxy in front of NetBox must pass the `Authorization` header on; Apache, for one, needs `WSGIPassAuthorization On`. An answer that a proxy or firewall gives instead of NetBox, without NetBox's `API-Version` header, is reported as such, never as a missing permission.
- **Retries.** A read that fails because NetBox couldn't be reached, didn't answer within `netbox.timeout`, or answered 429, 500, 502, 503 or 504 is tried again, up to `netbox.retries` more times. The waits start at 1 second and double each time up to 30 seconds, with some randomness. When NetBox answers 429 or 503 with `Retry-After`, TrueUp waits as long as it asks, but gives up at once if that is more than a minute. Other failures, such as a certificate that doesn't verify or a TLS handshake the server refuses, aren't retried. Each retry is logged as a warning.

## Fetching sources

To fetch the latest version of every configured source, or of those named, run:

```sh
netbox-trueup-ai source fetch [source...]
```

It reports each source's version and the files it holds once its patterns apply, and what it skipped:

```text
SOURCE              REF     VERSION                                   FILES  SKIPPED
devicetype-library  master  19470a551058fc9207f036371beefc41b197202e  17413  174

Skipped files (--output json lists them all):
  devicetype-library: 174 larger than max_file_size, such as elevation-images/Arista/arista-awe-5510.front.png
```

- **What a fetch does.** TrueUp asks the repository which commit its branch or tag is at: `ref`, or the default branch when `ref` isn't set. A name that is both a branch and a tag means the branch, as with `git clone --branch`. If the cache already holds that commit, nothing is downloaded. Otherwise TrueUp clones that one commit, without its history and without files larger than `max_file_size`, and reads the commit's files from Git's own storage without checking them out, so Git hooks, filters and submodules never run. TrueUp needs no `git` executable ([ADR 0020](adr/0020-git-sources-with-go-git.md)).
- **The cache.** Each source has its own cache in `<data_dir>/cache/sources/<name>/`: a clone, in a directory named for its commit and a number, and `latest.json`, which names it. A new clone replaces the old one only once it is complete and its files have passed every check, so a fetch that fails leaves the previous version in place. The cache is disposable: delete it, and the next fetch downloads the source again. If a clone in the cache is damaged, the error says which directory to delete. `source fetch` removes the caches of sources that are no longer configured. Changing `url` or `ref`, or raising `max_file_size`, means a new clone at the next fetch; changing patterns or lowering a limit doesn't.
- **Files.** Only plain files are read. These are skipped and reported, if the patterns choose them: files larger than `max_file_size`, symbolic links, which are never followed, Git submodules, and entries whose names are unsafe: empty, `.` or `..`, holding a slash, a backslash, a control character or invalid UTF-8, named `.git`, given twice in a directory, or more than 128 directories deep or 4096 bytes long. Nothing inside an unsafe directory is read.
- **Limits.** A fetch stops after `fetch_timeout`; when the server sends more than `max_fetch_size`, or the fetch would write more than that to disk; when the repository's list of branches and tags is larger than 32 MiB; or when the server sends more than 1,100,000 objects. A source whose patterns choose more than `max_files` files fails, as does one holding more than a million entries in all, or a directory whose Git tree is larger than 32 MiB. Each fails the source, and leaves its previous version in the cache.
- **Cost.** Each file is read once, after each fetch, to give its size and SHA-256 digest. Fetched whole at the defaults, devicetype-library downloads about 490 MB in about 35 seconds, and every fetch spends about 13 seconds reading its 17,000 files, mostly images. With `include: ["device-types", "schema"]`, the same reading takes about a second. Patterns don't shrink the download, since Git servers can't leave files out by path, but a smaller `max_file_size` does: at `128KiB`, the library downloads 64 MB in 6 seconds, with every YAML file still in.
- **Private repositories.** Not yet. TrueUp fetches over `https` only, without credentials. An `ssh` URL, which the configuration accepts, fails to fetch until credentials are supported.
- **TLS, proxies and redirects.** TrueUp verifies the server's certificate against the system's trust store, and nothing turns verification off. It uses the proxy that `HTTPS_PROXY` names, except for hosts in `NO_PROXY` and this machine. It never follows a redirect: use the repository's own URL. It reads nothing from Git's configuration, credential helpers, `~/.ssh` or `~/.netrc`.
- **Records.** Each fetch is recorded in [the database](#the-database), which `source fetch` creates and migrates first if needed: when the fetch started, and the version, files and skipped entries it found, or why it failed. A source that fails keeps the records of the version it had, as its cache does. Fetches of one source take turns, even in different TrueUp processes: a fetch waits up to the source's `fetch_timeout` for another to finish and be recorded, under a lock in `<data_dir>/cache/sources/.locks/`, and otherwise fails, recording nothing. `source fetch` leaves the records and cache of a source no longer configured while another process is fetching it. The records of sources no longer configured are removed with their caches.
- **Exit codes.** 0 when every source was fetched. 4 when some couldn't be, with the result printed as usual and each one's error. 2 when a name given isn't a configured source, 3 for a configuration problem, no sources at all, or a `database.url` this release can't use, and 1 when the command couldn't run, such as when the data directory can't be written to or the database can't be opened or written ([ADR 0018](adr/0018-cli-conventions.md)).
- **JSON.** With `--output json`, the result is one document whose fields scripts can rely on: `ok` (whether every source was fetched), and `sources`, with for each one `name`, `kind`, `url`, `ref` (the branch or tag followed, the default branch's name when `ref` isn't set), `ok`, `version` (the commit), `files` (how many it holds), `skipped`, each with `path` and `reason` (`too_large`, `symlink`, `submodule`, `unsafe_path` or `not_a_file`), and `error`. `ref`, `version`, `files` and `error` are `null` where they don't apply.

## The database

TrueUp records what it learns, such as each source's latest fetch, in a database: SQLite on a single host, which needs no setup, or PostgreSQL, which processes on several hosts can share. `database.url` chooses: unset, or a `sqlite:` URL, for SQLite, and a `postgres:` or `postgresql:` URL for PostgreSQL ([ADR 0011](adr/0011-store-state-with-sqlc-and-goose.md)). Either way, TrueUp creates what it needs, migrates it as [Migrations](#migrations) says, and refuses a database that holds another application's tables in the place of TrueUp's, before it changes anything.

### SQLite

- **Where it is.** Unless `database.url` is set, the database is the file `netbox-trueup-ai.db` in the data directory. To put it elsewhere, set `database.url` to a `sqlite:` URL that gives the file's absolute path, such as `sqlite:///srv/trueup/netbox-trueup-ai.db`, or `sqlite:///C:/ProgramData/trueup/netbox-trueup-ai.db` on Windows, with special characters percent-encoded. It can't have a host, user name, query or fragment: TrueUp sets SQLite's options itself.
- **Files.** TrueUp creates the file, and its directory, readable only by the user it runs as, and warns when every user can read an existing one. It refuses a file that holds another application's SQLite database, and changes nothing in it. Windows doesn't use those permissions, so there the file takes its directory's: keep the data directory where only TrueUp's account can read it. While the database is open, SQLite keeps `-wal` and `-shm` files beside it, with the same permissions, and TrueUp keeps `-lock` and `-gate` files there, which put processes and migrations in order. Keep the database on a local disk: SQLite's WAL mode, which TrueUp uses, doesn't work on a network file system.
- **Several processes** on one host can use the database at once, such as a scheduled `source fetch` and a command someone runs. A write waits up to 30 seconds for another to finish. Processes that share a database must share the data directory too, since the database records what each source's cache holds.
- **Backups.** Copy the database while no TrueUp command runs, or use SQLite's own backup, such as the `sqlite3` tool's `.backup` command, which is safe at any time. A copy of the main file alone, made while TrueUp runs, can miss what is still in the `-wal` file.

### PostgreSQL

TrueUp supports PostgreSQL 15 and later, as NetBox 4.7 does, and warns about older releases. Its tests run on PostgreSQL 16 ([ADR 0022](adr/0022-postgresql-schema-and-advisory-locks.md)).

- **The URL.** `database.url` is a libpq connection URL, such as `postgres://trueup@db.example.com/trueup?sslmode=verify-full`, with special characters percent-encoded. libpq's parameters work as [its documentation](https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-PARAMKEYWORDS) says, and so do its environment variables, such as `PGPASSWORD` and `PGSSLMODE`, and its password file, `~/.pgpass`. Several hosts can be given, as `db1.example.com,db2.example.com`. Unless the URL or the environment sets them, TrueUp waits 10 seconds to connect (`connect_timeout`), and names its sessions `netbox-trueup-ai` (`application_name`), which is how to find them in `pg_stat_activity`. TrueUp never prints the URL: the user name and password appear nowhere in its output.
- **TLS** is as `sslmode` says. libpq's default, `prefer`, encrypts the connection if the server offers TLS, but verifies nothing, and falls back to an unencrypted one if the server doesn't: anyone on the network path could read or change what crosses it, the password included. To verify the server, set `sslmode=verify-full`, which checks its certificate against the system's trusted certificates, or against those in the file that `sslrootcert` names, such as a private CA's. `sslcert` and `sslkey` give a client certificate.
- **The database and its schema.** Give TrueUp a database of its own, or share one: TrueUp keeps all its tables in the schema `netbox_trueup_ai`, and never reads or changes another. Every connection's `search_path` names that schema alone, whatever the URL, the role or a pooler sets. The database must hold text as UTF-8. TrueUp creates the schema if it doesn't exist, which needs the `CREATE` privilege on the database; without it, have a database administrator create the schema for TrueUp's role:

  ```sql
  CREATE SCHEMA netbox_trueup_ai AUTHORIZATION trueup;
  ```

  TrueUp marks the schema as its own with a comment, `netbox-trueup-ai`, and refuses a schema of that name that holds tables without the mark.
- **Several hosts** can share the database, but each keeps its own source caches, and fetches of one source take turns on one host only, under the lock in its data directory. Across hosts, the database keeps the fetch recorded last, which needn't match what another host's cache holds. Until high availability arrives (milestone 6), fetch sources from one host.
- **Connections.** Each process keeps a connection of its own open while it uses the database, and a pool of others: by default up to 4, or as many as the host has CPUs if that is more. pgx's URL parameters change the pool, such as `pool_max_conns=10` ([pgxpool's documentation](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool#ParseConfig)).
- **Poolers.** A pooler such as PgBouncer works in session mode only. TrueUp puts processes and migrations in order with advisory locks, which belong to a server connection, and transaction or statement pooling would pass its locks to other clients. TrueUp can't detect that, so don't configure it.
- **Locks.** TrueUp's advisory locks have the keys (1414681936, 1), (1414681936, 2) and (1414681936, 3), as `pg_locks` shows them, with `classid` 1414681936. A process that ends releases its locks with its sessions. If the host running a TrueUp process vanishes without closing its connections, PostgreSQL keeps its locks until it notices the connection is gone, which with the usual TCP keepalive settings can take hours, so a migration meanwhile waits its 2 minutes and fails: end the session with `pg_terminate_backend`.
- **Backups.** Use PostgreSQL's own tools, such as `pg_dump --schema=netbox_trueup_ai`, which are safe while TrueUp runs.

### Migrations

A command that uses the database, such as `source fetch`, creates it if it doesn't exist and brings its schema up to date first, applying the migrations it lacks. Every TrueUp process holds a shared lock while it has the database open, and a migration needs the lock to itself, so it never changes the schema under a running process: it waits up to 2 minutes for other processes to close the database, and then fails, saying so. While it waits, processes that start wait for it, so that they can't keep it waiting. So install a new release while no long command, such as a large `source fetch`, is running. Processes that open the database at once take turns, so a migration is never applied twice. On SQLite the locks are on the `-lock` and `-gate` files; on PostgreSQL they are advisory locks. Migrations only go forward, and a release refuses a database whose schema is newer than it knows, so back up the database before installing a new release, to be able to go back ([ADR 0021](adr/0021-forward-only-migrations-when-opening.md)).

To create the database, or migrate it as a step of its own, such as after installing a new release, run:

```sh
netbox-trueup-ai migrate
```

```text
Database:  SQLite, /var/lib/netbox-trueup-ai/netbox-trueup-ai.db
Schema:    version 1, was 0
Applied:   00001_sources.sql
```

- **Exit codes.** 0 when the schema is up to date, whether migrations were applied or none were needed. 1 when the database can't be opened or migrated, or its schema is newer than this release knows, and 3 for a configuration problem, such as a `database.url` this release can't use ([ADR 0018](adr/0018-cli-conventions.md)).
- **JSON.** With `--output json`, the result is one document whose fields scripts can rely on: `engine` (`sqlite` or `postgres`), `location` (the SQLite database's file, or for PostgreSQL a URL with only the hosts, ports and database name, such as `postgres://db.example.com:5432/trueup`), `version_before` and `version` (the schema's version before and after, with 0 for a database that didn't exist), and `applied`, the migrations applied, each with `version` and `name`.

## `config show`

`config show` prints each setting, the layer it came from and its value:

```text
Config file: /etc/netbox-trueup-ai/config.yaml (default location)

SETTING                   FROM                        VALUE
log.level                 env TRUEUP_LOG_LEVEL        debug
log.format                default                     text
data_dir                  default                     /var/lib/netbox-trueup-ai
database.url              default                     (not set)
netbox.url                file                        https://netbox.example.com
netbox.token              file, as netbox.token_file  [redacted] (read from /run/secrets/netbox-token)
netbox.ca_bundle          default                     (not set)
netbox.timeout            default                     30s
...
```

With `--output json` it prints the same as one JSON document, whose fields scripts can rely on ([ADR 0018](adr/0018-cli-conventions.md)):

- `config_file`: the file read, as `path` and `origin`, or `null` if none was read;
- `settings`: one object per setting, with `key`, `value`, `origin` (`default`, `file`, `env` or `flag`), and, where they apply, `name` (the flag, variable or key that set it), `secret` and `file` (the file a secret was read from). In `sources`, a limit the source doesn't set is `""`, and its default applies;
- `warnings`: one object per warning, with `message` and, where it is about one setting, `key`.

Warnings, such as a secret file that every user can read, are also logged. `config show` prints them after the settings whatever the log level.
