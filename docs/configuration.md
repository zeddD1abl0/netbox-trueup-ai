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
| `database.url` | `TRUEUP_DATABASE_URL` | none | not set | Secret. A `sqlite`, `postgres` or `postgresql` URL. When it isn't set, TrueUp uses a SQLite database in the data directory. Percent-encode special characters in a password. |
| `database.url_file` | `TRUEUP_DATABASE_URL_FILE` | `--database-url-file` | not set | A file to read `database.url` from. |
| `netbox.url` | `TRUEUP_NETBOX_URL` | `--netbox-url` | not set | NetBox's URL, such as `https://netbox.example.com`, with no user name, password, query or fragment. `http` is accepted, but TrueUp warns unless the host is this machine (`localhost`, `127.0.0.1` or `::1`), because the token would cross the network unencrypted. |
| `netbox.token` | `TRUEUP_NETBOX_TOKEN` | none | not set | Secret. The NetBox API token: printable ASCII, with no spaces. That catches what a token file can carry unseen, such as a byte-order mark. |
| `netbox.token_file` | `TRUEUP_NETBOX_TOKEN_FILE` | `--netbox-token-file` | not set | A file to read `netbox.token` from. |
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
| `include` | No | Patterns of paths to read, relative to the top of the source, such as `device-types/Juniper/**`. When it isn't set, TrueUp reads everything. |
| `exclude` | No | Patterns of paths to skip. |

## `config show`

`config show` prints each setting, the layer it came from and its value:

```text
Config file: /etc/netbox-trueup-ai/config.yaml (default location)

SETTING             FROM                        VALUE
log.level           env TRUEUP_LOG_LEVEL        debug
log.format          default                     text
data_dir            default                     /var/lib/netbox-trueup-ai
database.url        default                     (not set)
netbox.url          file                        https://netbox.example.com
netbox.token        file, as netbox.token_file  [redacted] (read from /run/secrets/netbox-token)
sources[0].name     file                        devicetype-library
...
```

With `--output json` it prints the same as one JSON document, whose fields scripts can rely on ([ADR 0018](adr/0018-cli-conventions.md)):

- `config_file`: the file read, as `path` and `origin`, or `null` if none was read;
- `settings`: one object per setting, with `key`, `value`, `origin` (`default`, `file`, `env` or `flag`), and, where they apply, `name` (the flag, variable or key that set it), `secret` and `file` (the file a secret was read from);
- `warnings`: one object per warning, with `message` and, where it is about one setting, `key`.

Warnings, such as a secret file that every user can read, are also logged. `config show` prints them after the settings whatever the log level.
