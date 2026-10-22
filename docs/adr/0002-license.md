---
status: accepted
date: 2026-10-22
decision-makers: "@zeddD1abl0"
---

# License under Apache 2.0

## Context and problem

The project is public from its first commit and needs a license before anyone can use or contribute to it.

## Decision drivers

- Easy for organizations to adopt alongside NetBox.
- Clear terms for contributions, without a separate contributor agreement.
- Protection for users against patent claims from contributors.

## Considered options

1. Apache License 2.0
2. MIT
3. GPL-3.0 or AGPL-3.0

## Decision

Chosen option: "Apache License 2.0". It is the license NetBox itself uses, it includes an explicit patent grant, and its section 5 makes contributions licensed under the same terms by default, so no contributor license agreement is needed.

### Consequences

- Good, because organizations that already run NetBox are used to its license.
- Good, because the patent grant and inbound licensing come built in.
- Bad, because derivative works may be closed-source. We accept that for wider adoption.

## Pros and cons of the options

### MIT

- Good, because it is short and familiar.
- Bad, because it has no explicit patent grant.

### GPL-3.0 or AGPL-3.0

- Good, because improvements must be shared.
- Bad, because many organizations restrict copyleft software, which would limit adoption.
