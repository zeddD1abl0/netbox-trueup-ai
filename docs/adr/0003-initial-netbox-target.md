---
status: accepted
date: 2026-10-22
decision-makers: "@zeddD1abl0"
---

# Target NetBox 4.7 first

## Context and problem

The comparison engine has to treat every field the way NetBox does, and NetBox's API and models change between minor releases. For example, NetBox 4.7 returns selection custom fields as `{value, label}` objects, replaces a service's `protocol` and `ports` with `port_mappings`, and adds cooling models and an `end_of_life` field on device types. Each additional supported version multiplies the comparison rules and the test matrix.

NetBox 4.7.0 was released on 2026-09-02; 4.7.2 is the current patch release.

## Decision drivers

- Get comparison semantics right before making them broad.
- Use the newest API features where they make syncing safer.
- Keep the integration test matrix small while the core is built.

## Considered options

1. NetBox 4.7 only
2. NetBox 4.6 and 4.7
3. NetBox 4.5 and later

## Decision

Chosen option: "NetBox 4.7 only" for the initial releases. The reference source for comparison semantics is pinned to NetBox 4.7.2 (`make reference`). Support for 4.6 may be added in a later release through a new ADR.

### Consequences

- Good, because we can rely on features from 4.6 and later: v2 API tokens, `ETag`/`If-Match` for safe concurrent updates, and cursor pagination.
- Good, because there is one set of comparison rules to get right first.
- Bad, because users still on NetBox 4.6 or earlier can't use the first releases.
- The server should refuse to sync against an unsupported NetBox version, with a clear error. That will be implemented with the NetBox client.

## Pros and cons of the options

### NetBox 4.6 and 4.7

- Good, because it covers more existing installations.
- Bad, because it means per-version comparison rules before the engine design has settled.

### NetBox 4.5 and later

- Bad, because it rules out 4.6 features that make syncing safer (`ETag`/`If-Match`), or makes them conditional.
