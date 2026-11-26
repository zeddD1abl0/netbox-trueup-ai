---
status: accepted
date: 2026-11-26
decision-makers: "@zeddD1abl0"
---

# Test against NetBox with and without netbox-branching, from one Compose file, with the plugin pinned by hash into an image built locally

## Context and problem

[ADR 0033](0033-runs-against-main-or-a-chosen-branch.md) has TrueUp run against main or a chosen branch where NetBox has netbox-branching, and work as before where it hasn't. Milestone 2 is done only when the integration tests pass against both. The disposable NetBox in `internal/netboxtest` was netbox-docker's image alone, with no background worker, and netbox-branching needs both a change to NetBox's configuration and a worker to provision branches. netbox-docker publishes no image with plugins, so the plugin has to come from PyPI into an image of our own: the first package from a public index in anything the tests build.

How should the test NetBox offer both, how does the plugin get into it, which tests run against which, and what does the branching NetBox stand in for?

## Decision drivers

- One definition of the stack, as `compose.yaml` is now, which `make netbox-up` and the tests both run, and which can't drift.
- The NetBox with netbox-branching is the stock one with only the plugin and its worker added, so a difference between the two runs is the plugin's.
- Nothing installed into a test image that isn't pinned and checked, and nothing published.
- TrueUp's tests run against both without each test having to choose.
- Dependabot's routine updates still merge themselves ([ADR 0031](0031-merge-routine-dependabot-updates-automatically.md)), and the `main` ruleset needn't change with the test matrix.

## Considered options

1. One `compose.yaml` with two profiles, `stock` and `branching`; the branching image built locally from netbox-docker's, with the plugin pinned by hash; the whole suite run against each profile in CI
2. A second Compose file for the branching NetBox
3. A published image with the plugin, built by CI
4. The plugin in every test NetBox, turned on by an environment variable

## Decision

Chosen option: 1, because it keeps one definition of the stack, adds nothing to the stock NetBox, and tests TrueUp against both NetBoxes without asking each test which it needs.

- **Profiles.** `postgres` and `valkey` run in every profile. The `stock` profile runs `netbox`, netbox-docker's image as it is; the `branching` profile runs `netbox-branching`, built from `internal/netboxtest/branching/`, and `netbox-worker`, NetBox's background worker, which provisions branches. The stock `netbox` needs a profile of its own, because Compose always starts a service with none. `netboxtest` learns Compose's `profiles` and `build` (with `context` only), and its rules: each profile runs exactly one NetBox, the one service that publishes a port; a service depends only on services that run whenever it does; and a test keeps the three NetBoxes' environments, ports and health checks the same, since Compose files can't share them without anchors, which `netboxtest` doesn't read.
- **The image.** `branching/Dockerfile` builds on the stock `netbox` service's image, exactly, which a test checks, and installs netbox-branching from `branching/requirements.txt` with `uv pip install --require-hashes --only-binary :all: --no-deps`: one wheel, pinned by version and hash, with no dependencies, so no build code runs. `branching/plugins.py` replaces netbox-docker's empty `plugins.py`, wrapping its `DATABASES` in netbox-branching's `DynamicSchemaDict` and adding the plugin's database router, which the plugin refuses to start without. The plugin ships no static files, so nothing is collected. Compose builds the image with BuildKit for `make netbox-up`, and `netboxtest` with Docker's classic builder, through testcontainers-go, from the directory it embeds, which a test keeps the same as the one on disk. So the Dockerfile may hold only what both build the same way: one pinned `FROM`, then `RUN` and `COPY` without flags. The image is built on the machine running the tests, kept under a tag naming its files' content, and never published.
- **The worker.** In the `branching` profile only, with netbox-docker's command and health check, after NetBox has migrated the database. `netboxtest` also waits until NetBox counts it running. A stock NetBox without a worker indexes search synchronously, as it always has here.
- **Which tests.** `NETBOXTEST_PROFILE`, `stock` by default, chooses the profile every package's `netboxtest.Start` runs; `make test-integration` and `make netbox-up` take `NETBOX_PROFILE`. A test that needs branches skips itself on `stock`. `netboxtest` creates a branch and waits until it is ready, and reads and writes in one with the `X-NetBox-Branch` header, for tests to seed and check with; TrueUp's own client gets branches in the roadmap's step 18.
- **CI.** The integration job runs once for each profile, side by side, as "Integration tests (stock)" and "Integration tests (branching)", and a job named "Integration tests", the `main` ruleset's required check, passes only if both did.
- **Updates.** Dependabot proposes netbox-branching's patch releases, after its 7-day cooldown; its minor releases pair with other NetBox releases, so they move with the target ([ADR 0003](0003-initial-netbox-target.md)). The auto-merge workflow merges only Go modules and the compose files' images, so a maintainer reviews these, as NetBox's own image. The Dockerfile's base isn't a Dependabot entry: the NetBox image's update, which a maintainer reviews, fails the tests until the Dockerfile moves with it, as `netboxtest.Version` and the Makefile's `NETBOX_VERSION` do.
- **What it doesn't stand in for.** ADR 0033 expected the branching NetBox to refuse merges, as a change request awaiting approval does, and writes to main, as a protected main does. Neither is in it. Merging is a person's act in NetBox, which TrueUp never automates, so nothing tests a refused merge. How deployments protect main, and what NetBox answers when it refuses a write, isn't known from public sources: netbox-branching 1.2.4 has no such setting, and NetBox Changes isn't public. That is future work, in the roadmap's backlog; until then, TrueUp's error for a refused write to main has nothing in the test NetBox to refuse it.

This replaces ADR 0033's consequence that described those stand-ins.

### Consequences

- Good, because TrueUp is tested against NetBox with and without netbox-branching from one file, and a difference between the two runs comes from the plugin.
- Good, because the plugin is the only thing the image adds, pinned to one wheel's hash, and nothing is published.
- Good, because the existing tests run against both NetBoxes unchanged, and CI's wall time stays about the same.
- Bad, because CI spends twice the runner minutes on integration tests.
- Bad, because building the image needs PyPI at test time, once per machine or CI run; Docker's cache serves later builds.
- Bad, because Compose and `netboxtest` build with different builders, which limits the Dockerfile to the instructions both build the same way.
- Bad, because a protected main isn't tested until its mechanism is known.
- Neutral, because built images stay on the machine, one per version of `branching/`, until removed.

## Pros and cons of the options

### A second Compose file

- Good, because each file stays simple, with no profiles.
- Bad, because PostgreSQL and Valkey would be pinned twice, and Dependabot's update of one file would leave the other behind, failing the check that keeps them the same and holding back updates that now merge themselves.

### A published image

- Good, because tests wouldn't build anything.
- Bad, because it needs a registry, a publishing workflow with write access, and its own updates, for an image only these tests use.

### The plugin in every test NetBox, turned on by a variable

- Good, because there would be one NetBox service.
- Bad, because the stock NetBox would no longer be netbox-docker's image, and the configuration would read a variable that Compose fills in from the shell, which `netboxtest`'s rules refuse.

## More information

- netbox-branching 1.2.4: `AppConfig.ready` in `netbox_branching/__init__.py` (the `DynamicSchemaDict` and router checks, and `min_version` 4.7.0), `docs/netbox-docker.md`, `pyproject.toml` (`dependencies = []`), and `ProvisionBranchJob` in `netbox_branching/jobs.py`.
- netbox-docker 5.1.1: `/opt/netbox/netbox/netbox/configuration.py` in its image loads `/etc/netbox/config/configuration.py` first, then every other `*.py` there in name order.
- NetBox 4.7.2: `/api/status/` reports installed plugins and `rq-workers-running` (`StatusView` in `netbox/netbox/api/views.py`); search indexing runs in the worker when one is running (`netbox/search/deferred.py`).
- testcontainers-go 0.45: `WithDockerfile`, building from a tar archive with Docker's API.
- [internal/netboxtest/README.md](../../internal/netboxtest/README.md).
