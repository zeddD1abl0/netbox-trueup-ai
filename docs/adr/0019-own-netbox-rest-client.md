---
status: accepted
date: 2026-10-24
decision-makers: "@zeddD1abl0"
---

# Read and write NetBox through our own REST client on net/http

## Context and problem

TrueUp reads manufacturers, device types and their component templates from NetBox in milestone 1, and writes them in milestone 2. It talks to one NetBox release line at a time, NetBox 4.7 for now ([ADR 0003](0003-initial-netbox-target.md)), and relies on features of recent releases: v2 tokens, cursor pagination, `ETag` and `If-Match`, per-object bulk errors, and the branch header of netbox-branching. Everything NetBox sends crosses a trust boundary (B2 in [architecture.md](../architecture.md#trust-boundaries)).

[tooling.md §3.1](../tooling.md#31-netbox-access-and-comparison) listed our own client on `net/http` as a spike, to be confirmed with the milestone 1 client. This records the result.

## Decision drivers

- Support exactly the NetBox releases TrueUp targets, on our own schedule.
- Use what NetBox 4.7 offers: cursor pagination, filters across many parents, v2 tokens, and later `If-Match` and bulk errors.
- Treat answers as untrusted: limit their size, check their shape, and never turn TLS verification off.
- Keep the read path free of any way to write.
- Prefer the standard library.

## Considered options

1. Our own client on `net/http`
2. go-netbox, the generated SDK
3. A client we generate ourselves from NetBox 4.7's OpenAPI schema, such as with oapi-codegen or ogen
4. Our own NetBox code on a general HTTP client library, such as resty or go-retryablehttp

## Decision

Chosen option: "Our own client on `net/http`", in `internal/netbox`. The prototype covers milestone 1's reads and `netbox-trueup-ai netbox check`, in about 1,100 lines with their comments, not counting tests, and no new dependency.

- **Read-only in milestone 1.** All requests go through one function, which only sends GET, and the client has no method that writes. Tests seed NetBox through `internal/netboxtest` instead. Milestone 2 adds writes through a separate path that is never retried automatically ([ADR 0010](0010-write-through-reviewed-plans.md)).
- **Version check.** The client reads `/api/status/` when it connects, and refuses any release but 4.7.x, of any patch release, with a clear error. Every later answer must carry the same `API-Version` header, `4.7`, so a NetBox upgraded while TrueUp runs is noticed. Then `/api/authentication-check/` confirms the token. NetBox refuses a bad token on every endpoint, but a proxy that drops the `Authorization` header leaves a request without one, which `/api/status/` answers when `LOGIN_REQUIRED` is off.
- **Tokens.** v2 tokens are sent as `Bearer`. v1 tokens are sent as `Token` and work, with a warning, until NetBox 5.0 removes them. The token is never logged, request logs give the `Authorization` header as its scheme and `[redacted]`, and text from NetBox has the token removed before it reaches an error.
- **Listings** use cursor pagination with pages of 1000, NetBox's default `MAX_PAGE_SIZE`. The client follows the presence of a next link rather than page sizes, since NetBox quietly caps them at its own `MAX_PAGE_SIZE`. It refuses a page whose IDs don't keep rising, and a full page without a next link, which NetBox never sends and would end a listing early.
- **Filtered listings** put many parent IDs in one request, such as `device_type_id=1&device_type_id=2`, in batches that keep the URL well under common web servers' limits. NetBox ignores filters it doesn't know, which would list every object, so the client checks that every child belongs to a parent it asked for.
- **Permissions.** NetBox answers 403 for a list whose view permission the token's user lacks, so `netbox check` can report which object types a token can read. A 403 counts as NetBox's own, about the token or a permission, only if it carries the `API-Version` header NetBox puts on every API answer; one from a proxy or firewall in front of NetBox is an error.
- **Resilience.** Each request has a timeout (`netbox.timeout`, 30s), requests in flight are capped (`netbox.max_requests`, 4), and answers are limited in size (`netbox.max_response_size`, 32 MiB) and must be JSON.
- **Retries.** Reads are retried (`netbox.retries`, 3) after connection failures, timeouts, and 429, 500, 502, 503 and 504 answers. Waits start at 1 second and double up to 30, with jitter. `Retry-After` on 429 and 503 replaces the wait, up to a minute; a longer one, or one past the command's deadline, ends the read at once. Failures that would only repeat aren't retried: certificates that don't verify, TLS handshakes the server refuses, unknown hosts, answers over the limit, and other 4xx answers.
- **TLS** verifies against the system's trust store, plus an optional CA bundle (`netbox.ca_bundle`), with TLS 1.2 or later. No setting turns verification off. Redirects are never followed, so the token goes only to `netbox.url`. Proxies come from `HTTPS_PROXY` and `NO_PROXY`, and only for `https`: a proxy would see a plain `http` request whole, token included.
- **JSON, not types.** The client returns objects as raw JSON, and the object model's specs decode them ([ADR 0008](0008-describe-object-types-with-specs.md)).

### Consequences

- Good, because which NetBox releases TrueUp supports is our decision alone, and supporting another is a reviewed change to one check.
- Good, because it adds no dependency, and the code that handles untrusted answers is short enough to review in full.
- Good, because `netbox check` tells an operator before a sync whether NetBox is supported and what the token can read.
- Bad, because pagination, retries and limits are ours to maintain and test. Unit tests run them against a fake NetBox, and integration tests against NetBox 4.7.2.
- Bad, because there are no generated types for NetBox's objects. Specs describe the fields TrueUp uses, and the contract test against the OpenAPI schema keeps them honest (roadmap step 8).
- Until the specs exist, the client holds the list of object types milestone 1 reads, for `netbox check`.

## Pros and cons of the options

### go-netbox

- Good, because it has typed models and methods for every endpoint.
- Bad, because its latest release targets NetBox 4.3, and TrueUp's version support would follow its release cadence ([tooling.md §9](../tooling.md#9-considered-and-not-chosen)).
- Bad, because a generated SDK of that size is a large dependency to review for the handful of endpoints TrueUp uses.

### A client generated from NetBox 4.7's OpenAPI schema

- Good, because the types follow the exact release, and can be regenerated for the next.
- Bad, because the schema lacks what TrueUp needs most, such as uniqueness rules and write shapes, so specs are needed anyway (ADR 0008).
- Bad, because generated code doesn't give retries, limits, cursor pagination or the parent checks, and a generator is one more tool in the chain.

### A general HTTP client library

- Good, because retries and backoff come ready-made.
- Bad, because it is a dependency for what `net/http` and about a hundred lines already do.
- Bad, because such libraries choose their own defaults, such as following redirects or retrying more than reads, which would need overriding to meet B2.

## More information

The NetBox 4.7.2 behaviour the client follows, with sources:

- Cursor pagination: `start` is a non-negative ID, results are ordered by ID from it, `start` with `offset` or `ordering` is refused with 400, and `count` and `previous` are null. A next link appears only for a full page, after any `MAX_PAGE_SIZE` cap (`NetBoxPagination` in `netbox/netbox/api/pagination.py`). `MAX_PAGE_SIZE` defaults to 1000 and `PAGINATE_COUNT` to 50 (`netbox/netbox/config/parameters.py`).
- Tokens: a value starting with `nbt_` is v2, `nbt_<key>.<token>`, and any other is v1; `Bearer` and `Token` are both accepted for either. A token that fails gets 403, not 401, on every endpoint, since REST framework authenticates before it checks permissions (`TokenAuthentication` in `netbox/netbox/api/authentication.py`).
- Permissions: a list endpoint answers 403 to a user without the model's view permission, because REST framework's `DjangoModelPermissions` checks it before the view runs. A permission with constraints passes that check and filters the list (`TokenPermissions` in the same file, and `ObjectPermissionBackend.has_perm` in `netbox/netbox/authentication/__init__.py`).
- Status: `/api/status/` uses `IsAuthenticatedOrLoginNotRequired`, and `LOGIN_REQUIRED` defaults to on in 4.7.2, though deprecated; `/api/authentication-check/` always needs an authenticated user (`StatusView` and `AuthenticationCheckView` in `netbox/netbox/api/views.py`).
- `API-Version` is the release's major and minor version, set on every API answer, errors included (`CoreMiddleware` in `netbox/netbox/middleware.py`, `REST_FRAMEWORK_VERSION` in `netbox/netbox/settings.py`).
- Filters: component templates filter on `device_type_id` with a `ModelMultipleChoiceFilter`, so an ID that refers to nothing fails the whole request with 400 (`DeviceTypeComponentFilterSet` in `netbox/dcim/filtersets.py`). NetBox doesn't reject unknown filters.
