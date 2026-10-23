# A disposable NetBox for tests

This directory runs a throwaway NetBox 4.7.2 on your machine, for integration tests and for trying TrueUp by hand. It holds:

- `compose.yaml`, the only definition of the stack: PostgreSQL, Valkey and NetBox from [netbox-docker](https://github.com/netbox-community/netbox-docker)'s image;
- the `netboxtest` Go package, which starts that stack for tests with [testcontainers-go](https://golang.testcontainers.org) and provisions an API token;
- `cmd/netbox-token`, which provisions a token for the stack `make netbox-up` starts.

You need Docker with Compose, running on this machine.

**Only ever use this NetBox.** Its credentials are dummy values published in this repository, so it listens on 127.0.0.1 only. Never point netboxtest, its tests or `netbox-token` at any other NetBox: they refuse a URL that isn't on this machine.

## By hand

```sh
make netbox-up     # start NetBox on http://127.0.0.1:8000 and provision a token
make netbox-down   # stop it and delete its data
```

The first start takes about 4 minutes while NetBox runs its database migrations. `make netbox-up` then prints how to point TrueUp at it:

```sh
export TRUEUP_NETBOX_URL=http://127.0.0.1:8000
export TRUEUP_NETBOX_TOKEN_FILE=$PWD/.cache/netbox/token
```

- Sign in to the web UI as `admin` with the password `insecure-test-only`.
- The token file holds a write-enabled v2 token for that user, readable only by you. Each `make netbox-up` provisions a new one.
- For another port, run `make netbox-up NETBOX_PORT=8001`, and pass the same `NETBOX_PORT` to `make netbox-down`. The variable isn't named `TRUEUP_...`, because TrueUp rejects `TRUEUP_` variables it doesn't know.
- The containers and their data stay until `make netbox-down`. If Docker or the machine restarts, run `make netbox-up` again to start them.

## In tests

Integration tests carry the `integration` build tag, so `make test` skips them. `make test-integration` runs them, with the unit tests, and CI runs it in the "Integration tests" job. For your editor to check them, give gopls the tag: set `"buildFlags": ["-tags=integration"]` in its settings.

Start one NetBox per test binary in `TestMain` and share it, since each start takes minutes:

```go
//go:build integration

package mypackage

var nb *netboxtest.NetBox

func TestMain(m *testing.M) {
    os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
    defer cancel()
    var err error
    if nb, err = netboxtest.Start(ctx); err != nil {
        fmt.Fprintln(os.Stderr, "start NetBox:", err)
        return 1
    }
    code := m.Run()
    if err := nb.Stop(context.Background()); err != nil {
        fmt.Fprintln(os.Stderr, "stop NetBox:", err)
        return 1
    }
    return code
}
```

`nb.URL` is NetBox's base URL, on a random port of 127.0.0.1. `nb.Token` is a write-enabled v2 token for the superuser, as `nbt_<key>.<token>`, in a `config.Secret` so that it can't be logged by mistake. Call `nb.Token.Reveal()` to use it. Every test binary gets an empty NetBox of its own, and tests within one binary share it, so make test data that can't collide, or clean it up.

`Start` returns once NetBox passes its health check and has provisioned the token, so NetBox answers API requests by then. If anything fails, the error ends with the last lines of the failing container's log. If a test binary dies without calling `Stop`, testcontainers-go's reaper removes the containers.

`netboxtest_integration_test.go` holds the smoke test: `/api/status/` reports 4.7.2 to the token, and refuses a request without it or with a wrong secret. It also checks that a service that dies is reported with the end of its log, and cleaned up.

## How the token is made

NetBox 4.7 makes v2 tokens (`Bearer nbt_<key>.<token>`) only when `API_TOKEN_PEPPERS` is set, with each pepper at least 50 characters long. `compose.yaml` sets one through netbox-docker's `API_TOKEN_PEPPER_1`, and creates the superuser through `SUPERUSER_NAME` and `SUPERUSER_PASSWORD`.

The token comes from NetBox's own provisioning endpoint, `POST /api/users/tokens/provision/`, which exchanges a user name and password for a new token. That makes a v2 token, because v2 is the default version, and NetBox generates its value. See `TokenProvisionView` in `netbox/users/api/views.py` and `TokenProvisionSerializer` in `netbox/users/api/serializers_/tokens.py`. netbox-docker can also create a token at start-up, but only from a fixed key and value written into the stack, which would put a working token in this repository.

## What's in the stack

| Service | Image | Tag | Notes |
| --- | --- | --- | --- |
| `postgres` | `postgres` | `18.4-alpine3.24` | NetBox 4.7 needs PostgreSQL 15 or later. fsync is off, since the data is thrown away. |
| `valkey` | `valkey/valkey` | `9.1.2-alpine3.24` | The Redis-compatible server netbox-docker uses. One instance holds the task queue (database 0) and the cache (database 1), with no snapshots. |
| `netbox` | `netboxcommunity/netbox` | `v4.7.2-5.1.1` | NetBox 4.7.2, netbox-docker 5.1.1. Runs as the `netbox` user, as netbox-docker's own Compose file does. |

It differs from netbox-docker's own Compose file in a few ways:

- **No background worker.** Nothing in milestone 1 needs one, and without a worker NetBox 4.7 indexes search synchronously instead of queueing it. NetBox branching (milestone 2) will need one.
- **Nothing leaves this machine.** Census reporting, the release check and Copilot are off.
- **No passwords on PostgreSQL's and Valkey's ports,** which are reachable only from the stack's own network. Tests also publish them on random ports of 127.0.0.1.

Each image is pinned by tag and digest. Dependabot proposes updates after a 7-day cooldown. A NetBox update is a change of target ([ADR 0003](../../docs/adr/0003-initial-netbox-target.md)), so it fails `TestComposeFile` until `netboxtest.Version` and `NETBOX_VERSION` in the Makefile move with it.

## Changing the stack

`compose.yaml` is shared by Docker Compose and by `netboxtest`, which reads only the Compose keys used today: `name`, and for each service `image`, `user`, `command`, `environment`, `depends_on`, `expose`, `ports` and `healthcheck`. Any other key fails every test, so a feature the tests can't reproduce can't sneak in. To use a new key, teach `compose.go` to run it the way Compose does, and test it in `compose_test.go`.

The file also has to follow these rules, which `make test` checks:

- Images name a tag and pin a digest.
- Every service has a health check with `start_period`, `start_interval`, `interval`, `timeout` and `retries`. Tests wait for it as `docker compose up --wait` does, and a service starts only once those it `depends_on` are healthy.
- Every service lists its ports under `expose` or `ports`. Otherwise testcontainers-go would publish every port the image declares on all interfaces.
- `ports` publish on 127.0.0.1 only.
- Commands, environment values and health checks don't use `$`. Compose would fill it in from your shell, and the tests wouldn't. Only the host port in `ports` may use a variable, because tests choose their own.
- Environment values aren't empty, since Compose would take an empty one from your shell.
