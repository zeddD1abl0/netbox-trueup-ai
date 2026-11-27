# A disposable NetBox for tests

This directory runs a throwaway NetBox 4.7.2 on your machine, for integration tests and for trying TrueUp by hand. It holds:

- `compose.yaml`, the only definition of the stack: PostgreSQL, Valkey and NetBox from [netbox-docker](https://github.com/netbox-community/netbox-docker)'s image, in two profiles (below);
- `branching/`, the image of the `branching` profile: netbox-docker's image with [netbox-branching](https://github.com/netboxlabs/netbox-branching) added;
- the `netboxtest` Go package, which starts that stack for tests with [testcontainers-go](https://golang.testcontainers.org), provisions an API token, seeds NetBox with test data, and creates branches;
- `cmd/netbox-token`, which provisions a token for the stack `make netbox-up` starts.

## Two profiles

The stack runs one of two NetBoxes on the same PostgreSQL and Valkey, chosen by a [Compose profile](https://docs.docker.com/compose/how-tos/profiles/):

- **`stock`**, the default: netbox-docker's image as it is, as in milestone 1.
- **`branching`**: the same NetBox, configuration and data, with netbox-branching 1.2.4 installed and turned on, and the background worker that provisions its branches.

TrueUp has to work with both ([ADR 0033](../../docs/adr/0033-runs-against-main-or-a-chosen-branch.md)), so CI runs every integration test against each ([ADR 0035](../../docs/adr/0035-test-against-netbox-with-and-without-branching.md)).

You need Docker with Compose, running on this machine: through a local socket, or TCP on loopback. In a dev container, Docker-in-Docker works. A Docker socket mounted from the host doesn't, because NetBox's port is then on the host's 127.0.0.1, which the container can't reach.

**Only ever use this NetBox.** Its credentials are dummy values published in this repository, so it listens on 127.0.0.1 only, and answers only to the host names `localhost`, `127.0.0.1` and `[::1]` (`ALLOWED_HOSTS`), so that a web page can't reach it by rebinding its own name to 127.0.0.1. Never point netboxtest, its tests or `netbox-token` at any other NetBox: they refuse a URL that isn't on this machine, and refuse to connect anywhere else.

## By hand

```sh
make netbox-up                          # start NetBox on http://127.0.0.1:8000 and provision a token
make netbox-up NETBOX_PROFILE=branching # the same, with netbox-branching and a worker
make netbox-down                        # stop either and delete its data
```

The first start takes about 4 minutes while NetBox runs its database migrations, and the first `branching` start builds its image first, which takes a few seconds once netbox-docker's image is pulled. Run `make netbox-down` before switching profiles: both NetBoxes would use the same database and port. `make netbox-up` then prints how to point TrueUp at it:

```sh
export TRUEUP_NETBOX_URL=http://127.0.0.1:8000
export TRUEUP_NETBOX_TOKEN_FILE=$PWD/.cache/netbox/token
```

- Sign in to the web UI as `admin` with the password `insecure-test-only`.
- The token file holds a write-enabled v2 token for that user, readable only by you. Each `make netbox-up` provisions a new one.
- For another port, run `make netbox-up NETBOX_PORT=8001`, and pass the same `NETBOX_PORT` to `make netbox-down`. Neither `NETBOX_PORT` nor `NETBOX_PROFILE` is named `TRUEUP_...`, because TrueUp rejects `TRUEUP_` variables it doesn't know.
- In the `branching` profile, the web UI's Branches menu, and its API under `/api/plugins/branching/`, create and use branches. The containers are named `trueup-netbox-netbox-branching-1` and `trueup-netbox-netbox-worker-1`.
- The containers and their data stay until `make netbox-down`. If Docker or the machine restarts, run `make netbox-up` again to start them.

## In tests

Integration tests carry the `integration` build tag, so `make test` skips them. `make test-integration` runs them, with the unit tests, against the `stock` profile, and `make test-integration NETBOX_PROFILE=branching` against the other. It refuses a `NETBOXTEST_PROFILE` that disagrees with `NETBOX_PROFILE`, and, in CI, a `NETBOX_PROFILE` left to its default, so that a misspelled variable can't run the branching job's tests against the stock NetBox. CI runs both, side by side, in the "Integration tests (stock)" and "Integration tests (branching)" jobs, and its required "Integration tests" check passes only when both do. For your editor to check them, give gopls the tag: set `"buildFlags": ["-tags=integration"]` in its settings.

Start one NetBox per test binary in `TestMain` and share it, since each start takes minutes:

```go
//go:build integration

package mypackage

var nb *netboxtest.NetBox

func TestMain(m *testing.M) {
    os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
    ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
    defer cancel()
    var err error
    if nb, err = netboxtest.Start(ctx); err != nil {
        fmt.Fprintln(os.Stderr, "start NetBox:", err)
        return 1
    }
    code := m.Run()
    stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Minute)
    defer stopCancel()
    if err := nb.Stop(stopCtx); err != nil {
        fmt.Fprintln(os.Stderr, "stop NetBox:", err)
        return 1
    }
    return code
}
```

`Start` runs the profile the environment variable `NETBOXTEST_PROFILE` names, `stock` if it is unset or empty, and fails for any other name; `make test-integration` sets it from `NETBOX_PROFILE`. `nb.Profile` says which is running. A test that needs netbox-branching skips itself on `stock`, as `netboxtest`'s own tests do:

```go
if nb.Profile != netboxtest.Branching {
    t.Skip("needs netbox-branching: run with NETBOXTEST_PROFILE=branching")
}
```

Give `Start` at least 25 minutes. `compose.yaml`'s health checks allow up to 15m45s for the `stock` profile's three services to become ready; the `branching` profile's worker takes up to 1m45s more, and NetBox up to 2 minutes more to count it, 19m30s in all; and images may need pulling or building first. `make test-integration` runs `go test` with `-timeout 30m`, which applies to each package.

Each package with integration tests starts a NetBox of its own, which takes about 4 minutes and 1 GB of memory, so `make test-integration` runs packages one at a time (`go test -p 1`). Four packages start one, and the whole run takes about 20 minutes in CI.

`nb.URL` is NetBox's base URL, on a random port of 127.0.0.1. `nb.Token` is a write-enabled v2 token for the superuser, as `nbt_<key>.<token>`, in a `config.Secret` so that it can't be logged by mistake. Call `nb.Token.Reveal()` to use it. Every test binary gets an empty NetBox of its own, and tests within one binary share it, so make test data that can't collide, or clean it up.

`Start` returns once NetBox passes its health check and has provisioned the token, so NetBox answers API requests by then; in the `branching` profile, also once NetBox counts its worker running, so branches can be provisioned. As each container starts, it checks that Docker published its ports on 127.0.0.1 only and put it on the stack's network. If anything fails, the error ends with the failing container's last health check results and the end of its log, or the end of an image's build log, each line indented and without control characters, since NetBox's own output isn't trusted. If a test binary dies without calling `Stop`, for example on Ctrl-C, testcontainers-go's reaper removes the containers.

### Seeding NetBox

TrueUp's NetBox client never writes, so tests create what they need through `netboxtest`:

- `nb.Post(ctx, path, body, out)` sends `body` as JSON to an API path, such as `/api/dcim/manufacturers/`, with the superuser's token, and decodes the answer into `out`. A list creates many objects at once.
- `nb.Patch(ctx, path, body, out)` changes the fields in `body` of the object at an API path, such as `/api/dcim/device-types/7/`, and `nb.Delete(ctx, path)` deletes it.
- `nb.Get(ctx, path, query, out)` reads an API path, such as `/api/dcim/interface-templates/` with `device_type_id=7`, for a test that checks what NetBox itself made, such as through its import. TrueUp's own code reads NetBox through `internal/netbox`.
- `nb.Import(ctx, path, data)` creates objects through NetBox's own bulk import, as a person does in its web UI: it signs in as the superuser and submits YAML to an import page, such as `/dcim/device-types/import/`, which for device types also creates the components a file lists. It returns NetBox's own errors when NetBox refuses the import. `cmd/netbox-trueup-ai`'s parity test imports devicetype-library's files this way.
- `nb.UserToken(ctx, username, password)` provisions a read-only v2 token for another user, such as one a test made with `Post` and gave a single permission, to see what a token without the others can do.

`internal/netbox`'s integration tests show both: they seed device types with interface templates, and a user who may view only manufacturers.

### Branches

In the `branching` profile, tests can work in netbox-branching's branches:

- `nb.CreateBranch(ctx, name, description)` creates a branch, as the superuser, and returns once it is ready. NetBox's worker provisions it in a background job, a copy of main that takes a second or two here. It checks the worker first, so that a stopped one makes no branch, and fails with the job's error if provisioning fails, with the end of the worker's log if the worker stops, and with the job's state if the branch isn't ready within 2 minutes or when `ctx` ends; such a branch stays in NetBox, named in the error. It refuses at once in the `stock` profile.
- `nb.InBranch(branch)` returns a client whose `Post`, `Patch`, `Delete` and `Get` work as `nb`'s own do, but in the branch, with the `X-NetBox-Branch` header. What they change stays in the branch until someone merges it. netbox-branching branches only some models: the client refuses the paths it always serves from main, such as `/api/status/`, `/api/users/`, `/api/core/`, its own `/api/plugins/branching/`, and extras' custom fields and webhooks (`mainOnlyPaths` in `branch.go` lists them; `/api/plugins/branching/branchable-models/` lists every model a branch holds).

TrueUp's own client finds branches and reads through them in `internal/netbox` (writes come later); these are for seeding and checking. Nothing here merges a branch: TrueUp never merges, a person does, in NetBox ([ADR 0033](../../docs/adr/0033-runs-against-main-or-a-chosen-branch.md)).

### The reaper

The reaper is a container, Ryuk, that testcontainers-go starts for each test run. It publishes its control port on every interface, and testcontainers-go has no setting to change that. Anyone who can connect to it while tests run can ask it to remove containers, networks and volumes on your Docker host, matched by label, when the run ends. That doesn't matter in CI. On a network you don't trust, do one of these:

- Set `"ip": "127.0.0.1"` in Docker's `daemon.json` and restart Docker. Docker then publishes every port that names no host address on 127.0.0.1, Ryuk's included.
- Run with `TESTCONTAINERS_RYUK_DISABLED=true`. `Stop` still removes the stack, but a test binary that dies leaves it running. Find what's left with `docker ps --filter label=org.testcontainers=true`.

`netboxtest_integration_test.go` holds the smoke test: `/api/status/` reports 4.7.2 to the token, and refuses a request without it or with a wrong secret, and reports netbox-branching 1.2.4 and a running worker in the `branching` profile, and neither in `stock`. It also checks that a service that dies is reported with the end of its log, and cleaned up; that a branch becomes ready, and what is written in it stays out of main; and that a worker that has stopped is reported with the end of its log.

## How the token is made

NetBox 4.7 makes v2 tokens (`Bearer nbt_<key>.<token>`) only when `API_TOKEN_PEPPERS` is set, with each pepper at least 50 characters long. `compose.yaml` sets one through netbox-docker's `API_TOKEN_PEPPER_1`, and creates the superuser through `SUPERUSER_NAME` and `SUPERUSER_PASSWORD`.

The token comes from NetBox's own provisioning endpoint, `POST /api/users/tokens/provision/`, which exchanges a user name and password for a new token. That makes a v2 token, because v2 is the default version, and NetBox generates its value. See `TokenProvisionView` in `netbox/users/api/views.py` and `TokenProvisionSerializer` in `netbox/users/api/serializers_/tokens.py`. netbox-docker can also create a token at start-up, but only from a fixed key and value written into the stack, which would put a working token in this repository.

## What's in the stack

| Service | Image | Tag | Notes |
| --- | --- | --- | --- |
| `postgres` | `postgres` | `18.4-alpine3.24` | NetBox 4.7 needs PostgreSQL 15 or later. fsync is off, since the data is thrown away. |
| `valkey` | `valkey/valkey` | `9.1.2-alpine3.24` | The Redis-compatible server netbox-docker uses. One instance holds the task queue (database 0) and the cache (database 1), with no snapshots. |
| `netbox` (`stock`) | `netboxcommunity/netbox` | `v4.7.2-5.1.1` | NetBox 4.7.2, netbox-docker 5.1.1. Runs as the `netbox` user, as netbox-docker's own Compose file does. |
| `netbox-branching` (`branching`) | built from `branching/` | | The same image, with netbox-branching 1.2.4. The same environment, port and health check as `netbox`. |
| `netbox-worker` (`branching`) | built from `branching/` | | NetBox's background worker (`manage.py rqworker`), which provisions branches. Starts once `netbox-branching` has migrated the database. |

It differs from netbox-docker's own Compose file in a few ways:

- **A background worker in the `branching` profile only.** Nothing in `stock` needs one, and without a worker NetBox 4.7 indexes search synchronously instead of queueing it; netbox-branching needs one to provision branches. Its health check is netbox-docker's, that its process runs, and `netboxtest` also waits until NetBox counts it running.
- **Nothing leaves this machine.** Census reporting, the release check and Copilot are off.
- **No passwords on PostgreSQL's and Valkey's ports,** which are reachable only from the stack's own network. Tests also publish them on random ports of 127.0.0.1.

Each image is pinned by tag and digest. Dependabot proposes updates after a 7-day cooldown. PostgreSQL's and Valkey's patch and minor updates merge themselves once every required check passes, the integration tests included ([ADR 0031](../../docs/adr/0031-merge-routine-dependabot-updates-automatically.md)). A NetBox update is a change of target ([ADR 0003](../../docs/adr/0003-initial-netbox-target.md)), so it fails `TestComposeFile` until `branching/Dockerfile`'s base, `netboxtest.Version` and `NETBOX_VERSION` in the Makefile move with it.

### The branching image

`branching/` holds what Compose and `netboxtest` both build from:

- `Dockerfile`: netbox-docker's image, the stock `netbox` service's exactly, with netbox-branching installed by `uv pip install --require-hashes --only-binary :all: --no-deps`, so only the wheel whose hash is pinned installs, and no build code runs.
- `requirements.txt`: netbox-branching, pinned by version and by the hash of its wheel. It has no dependencies. Its version is `netboxtest.BranchingVersion`, which `TestBranchingRequirements` checks. Dependabot proposes its patch releases, and a maintainer reviews them, as the NetBox image; 1.2.x is the line for NetBox 4.7, so a new minor release moves with the target.
- `plugins.py`, which replaces netbox-docker's empty `/etc/netbox/config/plugins.py`: it turns on netbox-branching as the plugin requires, wrapping netbox-docker's `DATABASES` in its `DynamicSchemaDict` and adding its database router.

The image is built on your machine and never pushed to a registry; TrueUp never ships netbox-branching. `make netbox-up` builds it with Compose, as `trueup-netbox-netbox-branching` and `trueup-netbox-netbox-worker`. `netboxtest` builds it through testcontainers-go, from the files it embeds, as `trueup-netbox-branching:<tag>`, the tag naming the files' content, and keeps it, so later test binaries reuse it; remove old ones with `docker image rm`. Each build after the first comes from Docker's cache.

### What it doesn't stand in for

- **Merges.** TrueUp never merges a branch; a person does, in NetBox, so the stack doesn't refuse or check merges.
- **A protected main.** Some deployments refuse writes to main made outside a branch. How they do it, and what NetBox answers, isn't known from public sources, so the stack doesn't imitate it yet; the roadmap's backlog holds it.

## Changing the stack

`compose.yaml` is shared by Docker Compose and by `netboxtest`, which reads only the Compose keys used today: `name`, and for each service `profiles`, `image`, `build` (with `context` only), `user`, `command`, `environment`, `depends_on`, `expose`, `ports` and `healthcheck`. Any other key fails every test, so a feature the tests can't reproduce can't sneak in. To use a new key, teach `compose.go` to run it the way Compose does, and test it in `compose_test.go`.

The file also has to follow these rules, which `make test` checks:

- Each service is in one profile, `stock` or `branching`, or in none, to run in both. Each profile has exactly one service that publishes a port, its NetBox, and a service depends only on services that run whenever it does.
- Images name a tag and pin a digest. A service gives `image` or `build`, not both.
- A `build` context is a directory beside `compose.yaml`, which `netboxtest` embeds whole, as Compose sends it; `TestBuildContexts` checks the two match. Its Dockerfile has one `FROM`, first, naming a tag and pinning a digest, then only `RUN` and `COPY`, without flags, and no parser directives: Compose builds with BuildKit and `netboxtest` with Docker's classic builder, which build those the same way, and nothing else reaches the build, such as arguments or URLs.
- Every service has a health check with `start_period`, `start_interval`, `interval`, `timeout` and `retries`. Tests wait for it as `docker compose up --wait` does, and a service starts only once those it `depends_on` are healthy.
- Every service lists its ports under `expose` or `ports`. Otherwise testcontainers-go would publish every port the image declares on all interfaces. The worker listens on none, so it lists NetBox's web port, 8080, which nothing in it serves, and which `netboxtest` publishes on 127.0.0.1 only.
- `ports` publish on 127.0.0.1 exactly, with a host port that is a number, empty (Docker chooses), or a variable with a default, such as `${NETBOX_PORT:-8000}`.
- Commands, environment values and health checks don't use `$`. Compose would fill it in from your shell, and the tests wouldn't. Only the host port in `ports` may use a variable, because tests choose their own.
- Environment values aren't empty, since Compose would take an empty one from your shell.
- Values are YAML strings: quote anything YAML would read as a number or boolean, such as `"0"` or `"false"`. Compose turns those back into text its own way (`01` becomes `1`), while the tests would keep them as written. Only `retries` is a number. Anchors and aliases aren't allowed.
- NetBox's `ALLOWED_HOSTS` is set, and isn't `*`.
