# Development tooling

This document lists the tools, libraries, and AI skills used to build NetBox TrueUp AI, and why each was chosen. It doubles as a reference for setting up a complete development environment, including AI-assisted development with your own Claude account.

**Audience:** human contributors and AI coding agents.
**Last reviewed:** 2026-10-28. Versions mentioned are current as of that date; `mise.toml`, `go.mod`, and the workflow files are the source of truth once they exist.

## Status legend

Each entry carries one of these statuses:

| Status | Meaning |
| --- | --- |
| **Use** | Decided. Introduced when the first feature needs it. |
| **Spike** | Preferred option. Confirmed (or replaced) by an Architecture Decision Record (ADR) after a short prototype. |
| **Later** | Wanted, but not needed for the first milestones. |
| **Optional** | Helpful for some contributors. Never required to build, test, or contribute. |

Any pull request that adds, removes, or replaces a tool updates this file in the same PR. Significant choices also get an ADR in `docs/adr/`.

## At a glance

| Area | Choice |
| --- | --- |
| Language | Go (latest stable; previous release also supported) |
| AI agent | Claude Code with project-level `CLAUDE.md` / `AGENTS.md`, shared plugins, and project skills |
| NetBox access | Own thin REST client, driven by the target NetBox's OpenAPI schema |
| Storage | SQLite (single node) and PostgreSQL (HA) via sqlc + goose |
| Web UI | Server-rendered: templ + htmx, CodeMirror 6 for YAML editing |
| Config | cobra (CLI) + koanf (flags, env, file), plus settings edited in the UI |
| Quality | golangci-lint v2, `go test -race`, fuzzing, testcontainers-go |
| Supply chain | govulncheck, CodeQL, gitleaks, Dependabot, SBOMs, cosign, build provenance |
| Release | release-please (versions, changelog) + GoReleaser (binaries, packages, images) |
| Deploy targets | systemd, Windows Service, container image, Helm chart |
| Docs | Markdown, ADRs (MADR), Mermaid diagrams |

---

## 1. AI-assisted development

### 1.1 Agent

| Tool | Status | Why |
| --- | --- | --- |
| [Claude Code](https://github.com/anthropics/claude-code) (CLI or VS Code extension) | Use | The primary AI agent for this project. All project AI configuration (`CLAUDE.md`, `.claude/`) targets it. |
| Model: Claude Opus (current: Opus 5.5) | Use | Best results for design, the comparison engine, and reviews. Sonnet is fine for routine edits. |

Any AI agent can contribute. `AGENTS.md` (the cross-tool convention, see [agents.md](https://agents.md)) is the canonical agent guide. `CLAUDE.md` imports it and adds only Claude-specific notes, so other agents and Claude read the same rules.

### 1.2 Plugins used in development

These plugins were already installed in the original development environment. They are listed in the project's `.claude/settings.json` (`extraKnownMarketplaces` and `enabledPlugins`), so Claude Code offers to install them when you trust the repository folder.

| Plugin | Source | Used for |
| --- | --- | --- |
| `netbox-labs` | [netboxlabs/skills](https://github.com/netboxlabs/skills) | NetBox domain knowledge. See the skill breakdown below. |
| `gopls-lsp` | [claude-plugins-official](https://github.com/anthropics/claude-plugins-official) | Lets Claude use the Go language server: definitions, references, diagnostics. Needs `gopls` on `PATH`. |
| `pr-review-toolkit` | claude-plugins-official | Pre-PR review agents. `silent-failure-hunter` matters most here, because a sync engine that swallows errors silently corrupts NetBox data. `type-design-analyzer` reviews the comparison model types. `pr-test-analyzer` checks coverage. |
| `claude-security` | claude-plugins-official | Security scans of the codebase and of PR diffs, with verified patch suggestions. |
| `context7` | claude-plugins-official ([upstash/context7](https://github.com/upstash/context7)) | Current library documentation, so the agent doesn't rely on stale training data for fast-moving dependencies. |
| `playwright` | claude-plugins-official ([microsoft/playwright-mcp](https://github.com/microsoft/playwright-mcp)) | Lets Claude drive the web UI in a real browser to check its own UI changes. |
| `hookify` | claude-plugins-official | Writes and maintains the project's guardrail hooks (section 1.5). |
| `frontend-design` | claude-plugins-official | Design guidance for the web UI. |
| `claude-md-management` | claude-plugins-official | Keeps `CLAUDE.md` / `AGENTS.md` accurate as the project evolves. |
| `session-report` | claude-plugins-official | Optional. Usage and token reports for your own sessions; not used by the project. |

Built-in Claude Code skills we rely on: `/code-review` and `/security-review` before opening a PR, `/simplify` after a feature lands, and `/init` once at bootstrap.

**NetBox skills (from `netbox-labs`):**

| Skill | Used for |
| --- | --- |
| `netbox-api-integration` | REST client design: pagination (including cursor pagination in 4.6+), ETag/`If-Match` concurrency control, v2 tokens, bulk operations. |
| `netbox-data-modeling` | Dependency ordering for sync (for example, manufacturers before device types) and choosing natural keys. |
| `netbox-administration` | Object permissions and API tokens. This is the basis for the feature that generates an importable permission set for the token in use. |
| `netbox-migration` | Import patterns and dependency order for bulk loads. |
| `netbox-ndx` | Structure of the community device-type library, a likely first sync source. |
| `netbox-review-integration` | Reviews our NetBox client code for pagination, auth, and performance mistakes. |
| `netbox-branching`, `netbox-changes` | Writing plans into NetBox branches and merging them, and opening change requests for those branches (milestone 2, ADR 0017). Covers the branch lifecycle, merge jobs, conflicts and merge gating. |
| `netbox-diode` | Reference only. We evaluated Diode as the write path and did not choose it (section 9). |
| `setup-mcp` | Optional. Connects a NetBox MCP server to a local development NetBox (section 1.6). |

The remaining `netbox-labs` skills (plugin development, custom scripts, assurance, discovery, and so on) cover NetBox features this project doesn't touch.

### 1.3 Third-party Go skills

From [samber/cc-skills-golang](https://github.com/samber/cc-skills-golang) (MIT). Install **only** this subset, at user scope (`~/.claude/skills/`), from a commit you have reviewed. The full plugin adds about 48 skills. Several of them push libraries this project doesn't use (testify, viper, the samber libraries, DI frameworks), which would steer agents the wrong way.

| Skill | Why |
| --- | --- |
| `golang-security` | Secure-coding guidance for injection, crypto, secrets, SSRF, and cookies. Already installed in the original environment. |
| `golang-safety` | Defensive coding against nil misuse, slice aliasing, and integer overflow. |
| `golang-error-handling` | Consistent error wrapping. The sync engine must never lose an error. |
| `golang-concurrency`, `golang-context` | Workers, cancellation, and graceful shutdown in a long-running HA service. |
| `golang-database` | Transactions, pooling, and migrations across two database engines. |
| `golang-testing` | Table-driven tests, fuzzing, goleak, integration tests. |
| `golang-lint` | golangci-lint v2 configuration. |
| `golang-continuous-integration` | GitHub Actions workflows for Go. |
| `golang-dependency-management` | `go.mod` hygiene and govulncheck. |
| `golang-observability` | slog, Prometheus metrics, OpenTelemetry. |
| `golang-spf13-cobra` | CLI command tree. |
| `golang-project-layout` | `cmd/` and `internal/` layout at bootstrap. |

Third-party skills are instructions that run with your agent's permissions. Read them before installing, and pin to a commit.

### 1.4 Project skills (we will write these)

These live in `.claude/skills/` and are versioned with the code. Each one is written when the code it describes first exists, so it documents a real procedure rather than a guess.

| Skill | Purpose |
| --- | --- |
| `add-netbox-object` | Checklist for teaching the comparison and sync engine a new NetBox object type by writing its spec (ADR 0008): uniqueness rules, dependency order, field semantics checked against NetBox's OpenAPI schema, override handling, fixtures, and a parity test against a real NetBox. |
| `netbox-parity` | Before writing comparison rules for a field, look up how NetBox itself validates and serializes it in the pinned NetBox source (section 3.1). The aim is to match NetBox's behaviour, not guess at it. Written in roadmap step 8, from writing the manufacturer and device type specs. |
| `db-change` | Add a migration and queries for **both** SQLite and PostgreSQL, regenerate sqlc, extend the shared storage tests, and run them on both engines. Written in roadmap step 6; its PostgreSQL steps are required since step 7. |
| `write-adr` | Create a numbered ADR from the template, add it to the index, and link any ADR it supersedes. |

### 1.5 Checked-in Claude configuration

| File | Purpose |
| --- | --- |
| `AGENTS.md` | Canonical guide for AI agents: build and test commands, architecture map, rules (no secrets, generated files, commit format, ADRs). |
| `CLAUDE.md` | Imports `AGENTS.md` and adds Claude-specific notes only. |
| `.claude/settings.json` | Shared plugin list, a permission allowlist for safe commands (`make` targets, `go test`, read-only `git`), deny rules for secret files, and the hooks below. |
| `.claude/hooks/` | Scripts the hooks run. POSIX `sh`, with no dependency beyond the pinned toolchain. |
| `.claude/skills/` | Project skills (section 1.4). |

Hooks are guardrails for agents, not a security boundary; CI is the real enforcement.

| Hook | Status |
| --- | --- |
| Format each Go file Claude edits or writes with gofumpt (`.claude/hooks/format-go.sh`, a `PostToolUse` hook). Not goimports: run on a half-finished edit, it deletes an import that is briefly unused, or adds whichever package of that name is in the module cache, which once picked a stray YAML fork. `make fmt`, the pre-commit hook and lint run goimports on finished code. | In place |
| Block edits to generated files: sqlc's packages (the `out:` lines of `sqlc.yaml`), `CHANGELOG.md`, and any file that starts with Go's `// Code generated ... DO NOT EDIT.` marker, as templ's output will (`.claude/hooks/block-generated.sh`, a `PreToolUse` hook). It names the source to change instead. Bundled UI assets join when they exist. | In place |
| Block `git commit` on `main`, force-pushes, and `--no-verify`. | Planned, backlog |
| Block reads of `.env*`, key files, and anything under `secrets/`. | Planned, backlog |

Personal overrides go in `.claude/settings.local.json`, which is gitignored.

### 1.6 Optional: NetBox MCP server

[netboxlabs/netbox-mcp-server](https://github.com/netboxlabs/netbox-mcp-server), pointed at your **local development** NetBox with a read-only token, lets the agent check real object shapes and field values while working. Configure it at user or local scope. Never commit its token, and never point it at production.

### 1.7 AI tooling we chose not to use

| Tool | Reason |
| --- | --- |
| [Claude Code GitHub Action](https://github.com/anthropics/claude-code-action) (automated PR review) | Not initially. On a public repository it needs an API key secret and would process untrusted PR content (a prompt-injection risk). Maintainers review PRs. Revisit with a read-only, no-secrets design. |
| Claude Docs, Google Docs | Project documentation lives in the repository so it is versioned and reviewed with the code. |
| `claude-api` skill | The product doesn't call an LLM. |

---

## 2. Core toolchain

| Tool | Status | Why |
| --- | --- | --- |
| [Go](https://go.dev) | Use | Required by the project brief. Single static binary for every target. `go.mod` declares the previous supported release so contributors on either supported Go version can build. CI tests both; releases use the latest. |
| [gopls](https://pkg.go.dev/golang.org/x/tools/gopls) | Use | Go language server for editors and for Claude (via `gopls-lsp`). |
| [mise](https://mise.jdx.dev) | Use | One `mise.toml` pins every tool version, Go included. Local machines, the dev container, and CI all read it, so everyone runs the same versions. Uses checksum-verified backends where available. Go and Node take the latest patch release promptly, because those releases usually carry security fixes. Other tools wait until a release has been public for 7 days. |
| GNU Make | Use | Thin task runner (`make test`, `make lint`, `make generate`). Preinstalled on Linux and macOS, and well understood by humans and agents. Targets only wrap `go` commands, so Windows contributors can run those directly or use the dev container. |
| [Dev container](https://containers.dev) | Use | `.devcontainer/` gives a ready environment in VS Code or GitHub Codespaces: Go, mise tools, Docker-in-Docker, and the [Claude Code dev container feature](https://github.com/anthropics/devcontainer-features). |
| Git | Use | Version control. |
| [GitHub CLI](https://cli.github.com) (`gh`) | Use | Creating PRs and issues and checking CI from the terminal. Agents use it to open PRs. |
| Docker with Compose | Use | Runs the disposable NetBox for integration tests and for `make netbox-up` ([internal/netboxtest](../internal/netboxtest/README.md)), PostgreSQL for the storage tests (`internal/store/postgres/pgtest`), and later container builds. |
| [netbox-docker](https://github.com/netbox-community/netbox-docker) | Use | Standard way to run a disposable NetBox locally, at any version. We use its image (`netboxcommunity/netbox`, pinned by digest) and the environment variables its configuration reads, with a Compose file of our own in `internal/netboxtest`: smaller than netbox-docker's, with one Valkey and no worker, and only on 127.0.0.1. |
| [Node.js](https://nodejs.org) (current LTS) | Use | Runs markdownlint-cli2 and, later, rebuilds the web UI's JavaScript bundle. Bundled output is committed, so a plain `go build` needs only Go. Node 20 is end-of-life. |

---

## 3. Application libraries

### 3.1 NetBox access and comparison

| Tool | Status | Why |
| --- | --- | --- |
| Own REST client (`net/http`) | Use | `internal/netbox`, a thin client that handles exactly what we need: the version check, v2 tokens, cursor pagination, filtered listings, timeouts, retries with backoff, size limits and TLS, and in milestone 2 `ETag`/`If-Match` for safe concurrent updates and bulk operations. We keep control of NetBox version support instead of depending on a generated SDK release (see go-netbox in section 9), and add no dependency. Confirmed by [ADR 0019](adr/0019-own-netbox-rest-client.md) with the milestone 1 client, which only reads. |
| NetBox's OpenAPI schema (`/api/schema/`), read with `encoding/json` | Use | Each NetBox object type is described by a hand-written spec (ADR 0008). A contract test checks every spec against the schema the target NetBox publishes: fields, writability, types, choices, nullability, lengths and bounds. `internal/model/openapi` reads it with the standard library alone, which in the step 8 spike was about ten times faster than kin-openapi and used a sixth of the memory, with no new dependency (see kin-openapi in section 9). Each spec package commits a trimmed snapshot in `testdata/openapi.json`, which `make test` checks specs against; integration tests check the snapshot against the disposable NetBox, and `make schema` refreshes it. Choice values are generated from the snapshot by `make generate` (`netbox-choices`), and checked by `make generate-check`. [ADR 0023](adr/0023-check-specs-against-the-schema-with-plain-json.md) records the measurements. |
| NetBox source at pinned tags (`.cache/reference/`, gitignored) | Use | `make reference` shallow-clones [netbox-community/netbox](https://github.com/netbox-community/netbox) at the supported version (4.7.2, see ADR 0003). Humans and agents read NetBox's own import and validation code there so our comparison logic matches it. |
| [devicetype-library](https://github.com/netbox-community/devicetype-library) | Use | Real-world YAML for fixtures and tests. Likely the first supported sync source. |

### 3.2 YAML, schemas, and Git sources

| Tool | Status | Why |
| --- | --- | --- |
| [go.yaml.in/yaml/v3](https://github.com/yaml/go-yaml) | Use | Maintained path of the de-facto Go YAML library. Its `yaml.Node` API keeps comments and key order, which we need when users edit YAML in the UI and we write it back. `gopkg.in/yaml.v3` is frozen. Move to v4 once it leaves release candidate (v4.0.0-rc.6 as of June 2026). Already used by the config loader to check that the config file is a mapping, and by koanf's YAML parser. |
| [santhosh-tekuri/jsonschema](https://github.com/santhosh-tekuri/jsonschema) (v6) | Use | Validates source YAML, such as devicetype-library files against that library's JSON schemas, before anything reaches NetBox. |
| [go-git](https://github.com/go-git/go-git) (v6, beta) | Use | Clones source repositories in pure Go, so the container image and Windows builds don't need a `git` binary, and reads a commit's files from Git's object store without checking them out. v6 rather than the stable v5, because only v6 can ask a server to leave out large files (a partial-clone filter), which cuts devicetype-library's download from 871 MB to 489 MB at the default limit, and lets TrueUp set its HTTP client, redirect policy and allowed schemes for each fetch rather than for the whole process. Pinned at 6.0.0-beta.1 until 6.0.0, with betas taken through Dependabot. Measurements and the alternatives are in [ADR 0020](adr/0020-git-sources-with-go-git.md). Its go-billy v6 filesystem confines clones to their directory through `os.Root`. Tests serve repositories with go-git's own server, and with `git http-backend` where `git` is installed, since go-git's server can't filter. |

### 3.3 Storage

| Tool | Status | Why |
| --- | --- | --- |
| [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) | Use | Pure-Go SQLite. No CGO, so cross-compiling for Windows, macOS, and arm64 stays trivial. At v1.60.1 (2026-09-29), which goose v3.29.0 also requires. It is SQLite's C code translated to Go, which the race detector slows about 25 times, so the storage tests take longer under `make test` than they would in production. |
| [pgx](https://github.com/jackc/pgx) (v5) | Use | The standard PostgreSQL driver for Go, pure Go, for databases that several hosts share. At v5.11.0 (2026-09-07), the version goose v3.29.0 requires, so it raised nothing in the module graph. sqlc generates code for its own interface (`sql_package: pgx/v5`), with `COPY` for file lists, and goose migrates through its `database/sql` adapter. `database.url` is read as pgx reads it, with libpq's parameters and environment variables ([ADR 0022](adr/0022-postgresql-schema-and-advisory-locks.md)). It brings pgpassfile, pgservicefile and puddle, all by its author, and `golang.org/x/text`. |
| [sqlc](https://sqlc.dev) | Use | Generates type-safe Go from plain SQL. One query set per database engine sits behind a shared Go interface, and a single test suite runs against both. SQL stays explicit and reviewable, with no reflection-based ORM. Pinned in `mise.toml` at 1.31.1 (2026-04-22), which mise installs from sqlc's GitHub releases through its aqua backend. Those releases publish no checksums or signatures, so mise can't verify the download beyond TLS. Only developers and CI run sqlc, and what it generates is committed, reviewed, and checked by `make generate-check`. |
| [goose](https://github.com/pressly/goose) | Use | Versioned migrations for both engines, embedded in the binary and applied, forward only, when a command opens the database ([ADR 0021](adr/0021-forward-only-migrations-when-opening.md)). At v3.29.0 (2026-10-11), through its `Provider` API, with no global state. Its own lockers cover PostgreSQL and MySQL, so SQLite takes a file lock instead. goose's `go.mod` lists many database drivers; only the packages TrueUp imports are built, but its requirements raise some test-only modules, such as moby's client, through Go's version selection. |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | Use | File locks (`internal/filelock`): `flock` on Unix, `LockFileEx` on Windows, which the operating system releases when a process dies. They keep a SQLite database's schema from changing under a process that has it open, and make fetches of one source take turns. From the Go team, and in the module graph already through other modules. |

### 3.4 Jobs and high availability

| Tool | Status | Why |
| --- | --- | --- |
| [River](https://github.com/riverqueue/river) | Spike | Durable job queue with built-in leader election, so only one node runs scheduled syncs in an HA cluster. Supports PostgreSQL and SQLite, matching our two backends. The spike confirms the SQLite driver works with our setup. Fallback: PostgreSQL advisory locks plus an in-process scheduler. |

### 3.5 Web UI

| Tool | Status | Why |
| --- | --- | --- |
| `net/http` (standard library) | Use | Go's built-in router handles method and path patterns, and its cross-origin protection (Go 1.25+) covers CSRF. No web framework dependency. |
| [templ](https://templ.guide) | Use | Type-checked HTML templates compiled to Go. Template mistakes fail the build instead of failing at runtime. |
| [htmx](https://htmx.org) | Use | Interactive pages without a single-page-app framework. The UI stays server-rendered in Go, the JavaScript surface stays small, and contributors stay in one language. |
| [CodeMirror 6](https://codemirror.net) (YAML language + merge view) | Spike | Editing YAML before sync and showing side-by-side diffs (source vs NetBox, original vs override). Much lighter than Monaco. |
| [esbuild](https://esbuild.github.io) | Use | Bundles the small amount of JavaScript (CodeMirror plus glue). The output is embedded in the binary with `embed`. |
| [scs](https://github.com/alexedwards/scs) | Spike | Server-side sessions stored in our own database, so sessions work across HA nodes. |
| [go-oidc](https://github.com/coreos/go-oidc) + `golang.org/x/oauth2` | Later | Single sign-on (OIDC) for the web UI, with roles mapped from the identity provider's groups. Planned for milestone 3 (ADR 0015). |
| SAML library (to be chosen) | Later | SP-initiated SAML sign-in, planned for milestone 4 (ADR 0015). XML signature handling is a common source of vulnerabilities, so the library must be maintained and well reviewed; candidates are compared in an ADR. |
| SCIM 2.0 endpoints (own code or a library, to be chosen) | Later | Provisioning users and groups from the identity provider, planned for milestone 4 (ADR 0015). |
| `golang.org/x/crypto/argon2` | Use | argon2id password hashing for local UI accounts. |

### 3.6 Configuration, CLI, and service integration

| Tool | Status | Why |
| --- | --- | --- |
| [cobra](https://github.com/spf13/cobra) | Use | Command tree (`serve`, `migrate`, `permissions export`, `version`), help text, and shell completion. Its flag library, [pflag](https://github.com/spf13/pflag), is also used directly, to define a flag for each setting. Conventions for commands are in ADR 0018. |
| [koanf](https://github.com/knadh/koanf) (v2) | Use | Layers configuration with clear precedence: flags, then environment, then file, then database, then defaults (ADR 0012). Precedence is simply load order, so settings stored in the database fit in as one more layer, and loading layers separately shows where each value came from, which `config show` and the UI's read-only marking need. It also keeps key case, and reads every prefixed environment variable rather than only keys it already knows. viper was considered (section 9). We use the core module, the `env/v2` and `posflag` providers, and the YAML parser. We don't use the `file` provider: it exists mainly to watch files, pulling in fsnotify, and settings only change on restart. TrueUp reads the file itself, with a size limit and a permission check. If live reloading is ever wanted, the `file` provider is the way to do it. |
| `log/slog` (standard library) | Use | Structured logging. JSON output for containers, text for terminals. Logs go to stderr, so stdout carries only a command's result. |
| [golang.org/x/sys/windows/svc](https://pkg.go.dev/golang.org/x/sys/windows/svc) | Use | Native Windows Service support from the official Go module. |
| [go-systemd](https://github.com/coreos/go-systemd) (`daemon`) | Use | systemd readiness and watchdog notifications (`Type=notify`). |
| [prometheus/client_golang](https://github.com/prometheus/client_golang) | Use | `/metrics` endpoint: sync durations, object counts, errors. |
| [OpenTelemetry Go](https://opentelemetry.io/docs/languages/go/) | Later | Traces across fetches, comparisons, plans and NetBox calls, and metrics over OTLP alongside Prometheus. Planned for milestone 6. |

---

## 4. Code quality and testing

| Tool | Status | Why |
| --- | --- | --- |
| [golangci-lint](https://golangci-lint.run) (v2) | Use | One linter runner: staticcheck, gosec, errcheck, revive, and others. It also runs the formatters (gofumpt, goimports). Run locally and in CI with the same config, which sets the `integration` build tag so that integration tests are linted too. |
| [gofumpt](https://github.com/mvdan/gofumpt) | Use | Stricter `gofmt`, which ends formatting debates. Runs through golangci-lint. |
| `go test -race` | Use | Every CI test run uses the race detector. This is a concurrent server. |
| Go native fuzzing | Use | Fuzz the YAML parser, comparison engine, and override merge, where unexpected input does the most damage. |
| [go-cmp](https://github.com/google/go-cmp) | Use | Readable diffs in test failures. Test-only; never used in production code. |
| [testcontainers-go](https://golang.testcontainers.org) | Use | Integration tests start real containers: a disposable NetBox, and PostgreSQL for the storage tests. Test-only, core module only, so no PostgreSQL module: `pgtest` starts the image that `internal/store/postgres/pgtest/compose.yaml` pins, PostgreSQL 16, on 127.0.0.1, and gives each test a database of its own. The NetBox stack is defined once, in `internal/netboxtest/compose.yaml`, which `make netbox-up` runs with Docker Compose. `netboxtest` reads the same file and runs each service with the core module, and rejects any Compose key it can't run the same way, so the two can't drift. It also imports moby's API types, which the core module's container options take. Integration tests carry the `integration` build tag, run with `make test-integration`, and have their own CI job. CI runs them against every supported NetBox version, which is 4.7.2 for now. Its reaper container, which removes what a crashed test leaves behind, publishes a control port on every interface while tests run; [the README](../internal/netboxtest/README.md#the-reaper) says how to keep it off a network you don't trust. |
| [goleak](https://github.com/uber-go/goleak) | Use | Catches goroutine leaks in a long-running service. |
| [Playwright](https://playwright.dev) | Later | End-to-end UI tests in CI. Claude also uses the Playwright MCP plugin interactively. |
| [lefthook](https://github.com/evilmartians/lefthook) | Use | Fast Git hooks from a single binary: format, lint changed files, gitleaks, commit-message check. Hooks are a convenience; CI enforces the same checks. |

Generated code (sqlc, templ, bundled JS) is committed. CI regenerates it and fails if the result differs from what is committed. For sqlc, `make generate` regenerates its packages, and `make generate-check` generates them afresh into a temporary copy and fails on any difference, a missing or left-over file included. It is part of `make check`, and a step in CI's "Lint and scan" job.

---

## 5. Security and supply chain

| Tool | Status | Why |
| --- | --- | --- |
| [govulncheck](https://go.dev/doc/security/vuln/) | Use | Reports only the Go vulnerabilities our code actually calls. Runs in CI and on a schedule. |
| [CodeQL](https://codeql.github.com) | Use | GitHub code scanning for Go, JavaScript, and Actions workflows. Free for public repositories. |
| [gitleaks](https://github.com/gitleaks/gitleaks) | Use | Secret detection in the pre-commit hook and in CI, alongside GitHub's secret-scanning push protection. |
| [Dependabot](https://docs.github.com/en/code-security/dependabot) | Use | Update PRs for Go modules, GitHub Actions, Docker base images, the test NetBox's images in `internal/netboxtest/compose.yaml`, and npm, with a 7-day cooldown on new releases (security updates aren't delayed). Native to GitHub, with no third-party app. It doesn't cover `mise.toml`, so tool pins there are bumped by hand. |
| Signed commits | Use | Every commit must carry a signature GitHub verifies (GPG or SSH). A ruleset enforces this on `main`, and a PR check enforces it for each commit in a pull request (ADR 0004). |
| [OpenSSF Scorecard](https://github.com/ossf/scorecard) | Use | Ongoing public score of the repository's security practices. |
| [actionlint](https://github.com/rhysd/actionlint) + [zizmor](https://docs.zizmor.sh) | Use | Lint GitHub Actions workflows for correctness and for security problems such as template injection and over-broad permissions. |
| Pinned actions | Use | Every third-party action is pinned to a full commit SHA. Workflow tokens default to read-only. |
| [Syft](https://github.com/anchore/syft) + [Grype](https://github.com/anchore/grype) | Use | Syft generates SBOMs for every release artifact and image (through GoReleaser). Grype scans images for known vulnerabilities. One vendor's toolchain for both. |
| [cosign](https://github.com/sigstore/cosign) | Use | Keyless signing of release checksums and container images. No signing keys to store or leak. |
| [GitHub artifact attestations](https://github.com/actions/attest-build-provenance) | Use | Build provenance (SLSA) for binaries and images, verifiable with `gh attestation verify`. |

---

## 6. Build, packaging, and release

| Tool | Status | Why |
| --- | --- | --- |
| [Conventional Commits](https://www.conventionalcommits.org) | Use | Structured commit messages (`feat:`, `fix:`, `docs:` and so on) that drive versioning and the changelog. |
| [action-semantic-pull-request](https://github.com/amannn/action-semantic-pull-request) | Use | PRs are squash-merged, so the PR title becomes the commit message. This check enforces the convention on the title. |
| [release-please](https://github.com/googleapis/release-please) | Use | Maintains a release PR with the next version and changelog. Merging it tags the release. Releases happen when a maintainer decides, not on every commit. |
| [GoReleaser](https://goreleaser.com) | Use | One config builds binaries for Linux, Windows, and macOS (amd64 and arm64), plus checksums, SBOMs, signatures, and multi-arch container images. |
| [nFPM](https://nfpm.goreleaser.com) (via GoReleaser) | Use | `.deb` and `.rpm` packages that install the binary, a hardened systemd unit, and a service user. |
| [distroless](https://github.com/GoogleContainerTools/distroless) static, `nonroot` | Use | Minimal runtime image with no shell or package manager, running as non-root. Possible because the binary is static (no CGO). |
| [GHCR](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry) | Use | Hosts container images and the Helm chart (OCI) next to the code. |
| [Helm](https://helm.sh) (v4) | Use | Kubernetes chart in `/helm`, for both single-replica SQLite and multi-replica PostgreSQL deployments. |
| [chart-testing](https://github.com/helm/chart-testing) + [kind](https://kind.sigs.k8s.io) | Use | Lint and install-test the chart on a throwaway Kubernetes cluster in CI. |
| [kubeconform](https://github.com/yannh/kubeconform) | Use | Validates rendered manifests against Kubernetes schemas. |
| [helm-docs](https://github.com/norwoodj/helm-docs) | Use | Generates the chart's values table in `helm/README.md` from `values.yaml` comments, so the docs can't drift. |

---

## 7. Documentation

| Tool | Status | Why |
| --- | --- | --- |
| Markdown in the repository | Use | Docs are versioned and reviewed with the code. Every directory a human would browse (`/helm`, `/docker`, `/deploy`, `/docs`) gets a `README.md`. |
| ADRs in [MADR](https://adr.github.io/madr/) format | Use | `docs/adr/NNNN-title.md` records each significant decision: context, options considered, outcome. This file says *what* we use; ADRs say *why* in depth. |
| [Mermaid](https://mermaid.js.org) | Use | Diagrams as text that GitHub renders inline and PRs can diff. |
| [markdownlint-cli2](https://github.com/DavidAnson/markdownlint-cli2) | Use | Consistent Markdown style. |
| [lychee](https://github.com/lycheeverse/lychee) | Use | Checks for broken links in the docs, in CI and on a schedule. |
| Docs website | Later | Decided when the docs outgrow browsing on GitHub. |

---

## 8. GitHub repository configuration

These settings are applied by a repository admin. They are listed here so the setup is reproducible for forks.

- **Ruleset on `main`:** require a pull request (no approval count, see ADR 0004), require status checks, require signed commits, linear history (squash merge only), and block force-pushes and deletion.
- **Security:** private vulnerability reporting, secret scanning with push protection, Dependabot alerts and security updates, CodeQL.
- **Actions:** default `GITHUB_TOKEN` permissions set to read-only; workflows request write access per job.
- **Community files:** `README.md`, `LICENSE`, `CONTRIBUTING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md` ([Contributor Covenant](https://www.contributor-covenant.org)), `CODEOWNERS`, issue forms, and a PR template with a checklist for both human and AI-assisted contributions.

---

## 9. Considered and not chosen

| Option | Why not |
| --- | --- |
| [go-netbox](https://github.com/netbox-community/go-netbox) | Generated NetBox SDK, but its latest release targets NetBox 4.3 while NetBox is at 4.7. Depending on it would tie our NetBox version support to its release cadence. |
| [Diode Go SDK](https://github.com/netboxlabs/diode-sdk-go) | Write-only gRPC ingestion. It needs a Diode server, and the reconciler that decides what changes is not open source. Our product needs its own read, compare, and write loop. Could become an optional output later. |
| viper | The most familiar choice, and from the same organization as cobra. But its precedence order is fixed, with no place for settings stored in the database between the config file and defaults, and its merged view can't say which layer a value came from. It also lowercases every key, and `Unmarshal` ignores environment variables for keys it hasn't otherwise seen. Version 1.20 cut its dependencies considerably, so size is no longer the main reason. |
| GORM, ent | Reflection or heavy code generation that hides the SQL. sqlc keeps queries explicit and reviewable. |
| mattn/go-sqlite3 | Needs CGO, which complicates cross-compiling for Windows and arm64. |
| gopkg.in/yaml.v3 | Frozen. The same API is maintained at `go.yaml.in/yaml/v3`. |
| React, Vue, or another SPA | A second toolchain and language, a larger client-side attack surface, and an API layer we don't otherwise need. |
| Monaco editor | Much heavier than CodeMirror 6 for the same YAML editing. |
| testify | The standard library plus go-cmp covers our needs with one fewer dependency. |
| testcontainers-go's compose module | It runs a Compose file directly, but brings in docker/compose, docker/cli, buildkit and containerd: 139 modules against about 50 for the core module, all for tests. Reading our one Compose file with the core module keeps a single definition of the test stack without them. |
| pre-commit (Python framework) | Adds a Python runtime to a Go project. lefthook is a single binary. |
| Task (go-task) | A good tool, but Make is already installed nearly everywhere. Revisit if Windows-native development becomes common. |
| Renovate | More capable than Dependabot (it can update `mise.toml`), but requires installing a third-party app with write access. Revisit if keeping tool pins current by hand becomes a burden. |
| Trivy | Capable scanner. Grype was chosen to stay on one SBOM toolchain with Syft. |
| [kin-openapi](https://github.com/getkin/kin-openapi) | Reads OpenAPI documents into a typed model. For checking specs against NetBox 4.7.2's schema, v0.151.0 added six modules, took about 1.1 s and 166 MB to load the 14.7 MB schema against 0.1 s and 27 MB for plain decoding, and its own validation rejects NetBox's schema, whose `servers` list has an empty URL. Plain JSON decoding needs about 330 lines of our own ([ADR 0023](adr/0023-check-specs-against-the-schema-with-plain-json.md)). |

---

## 10. Setting up a development environment

Once the bootstrap PRs land, most of this is automated by `mise.toml`, `.devcontainer/`, and `.claude/settings.json`.

**Fastest path:** open the repository in a GitHub Codespace, or in VS Code with "Reopen in Container". Then skip to step 4.

**Manual setup:**

1. Install Git, Docker with Compose, and [mise](https://mise.jdx.dev/getting-started.html).
2. In the repository, run `mise install` to install the pinned toolchain, including Go. Then run `make hooks` to install the Git hooks. You don't need to activate mise in your shell: `make` and the Git hooks find the pinned tools themselves, including when Git runs from an IDE or an AI agent.
3. Set up [commit signing](../CONTRIBUTING.md#signing-commits), and authenticate the GitHub CLI with `gh auth login`.
4. Install Claude Code ([instructions](https://github.com/anthropics/claude-code)) and sign in with your own Claude account. Optionally install the VS Code extension.
5. Open Claude Code in the repository and trust the folder. When prompted, install the plugins listed in `.claude/settings.json`. To install them by hand instead:

   ```text
   /plugin marketplace add anthropics/claude-plugins-official
   /plugin marketplace add netboxlabs/skills
   /plugin install netbox-labs@netbox-labs
   /plugin install gopls-lsp@claude-plugins-official
   /plugin install pr-review-toolkit@claude-plugins-official
   /plugin install claude-security@claude-plugins-official
   /plugin install context7@claude-plugins-official
   /plugin install playwright@claude-plugins-official
   /plugin install hookify@claude-plugins-official
   /plugin install frontend-design@claude-plugins-official
   /plugin install claude-md-management@claude-plugins-official
   ```

6. Optional: copy the Go skills listed in section 1.3 from a reviewed commit of [samber/cc-skills-golang](https://github.com/samber/cc-skills-golang) into `~/.claude/skills/`.
7. Optional: run a local NetBox with `make netbox-up` and set up the NetBox MCP server against it (section 1.6).
8. Run `make check` to confirm the environment works.
