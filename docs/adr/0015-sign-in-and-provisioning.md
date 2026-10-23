---
status: accepted
date: 2026-10-23
decision-makers: "@zeddD1abl0"
---

# Sign in with OpenID Connect, SAML or local accounts, provision with SCIM, and never trust proxy headers

## Context and problem

TrueUp must know who did what ([ADR 0014](0014-audit-every-action.md)). Small installations have no identity provider. Organizations do have one, often more than one kind across teams. They expect accounts to appear and disappear with their directory, without anyone creating them by hand. Some web applications also accept an identity asserted in a header by a reverse proxy in front of them.

## Decision drivers

- Work with the identity providers organizations actually run.
- Accounts created and removed from the organization's directory.
- An administrator can still sign in when the identity provider is down.
- No way to sign in that is easy to misconfigure into a hole.

## Considered options

1. OpenID Connect and SAML for single sign-on, SCIM for provisioning, local accounts as well, and no proxy authentication
2. OpenID Connect only
3. Authentication delegated to a reverse proxy, which passes the identity in a header

## Decision

Chosen option: "OpenID Connect and SAML for single sign-on, SCIM for provisioning, local accounts as well, and no proxy authentication".

- **OpenID Connect** with the authorization code flow and PKCE. The ID token's signature, issuer, audience, expiry and nonce are checked.
- **SAML**, SP-initiated. Assertions must be signed. Audience, recipient, validity window and issuer are checked, and assertion IDs are remembered to reject replays. XML signature handling is a common source of vulnerabilities, so it relies on a maintained, well-reviewed library, chosen in its own ADR.
- **SCIM 2.0** endpoints let an identity provider create, update and deactivate users and groups. Each provider authenticates with its own bearer token, which only reaches the SCIM endpoints. Deactivating a user ends their sessions.
- **Local accounts**, with argon2id password hashes, for installations without an identity provider and for administrators to sign in when the provider is down.
- **Groups** from OpenID Connect claims, SAML attributes or SCIM map to TrueUp roles explicitly ([ADR 0016](0016-permissions-and-approval-policies.md)). Nothing is granted by default.
- **No proxy authentication.** TrueUp never takes a user's identity from a request header set by a reverse proxy.

### Consequences

- Good, because organizations can use their existing identity provider, whichever protocol it speaks.
- Good, because joiners and leavers are handled by the directory, and a leaver's sessions end at once.
- Bad, because supporting two single sign-on protocols, and SCIM, is more code and more tests to maintain.
- Bad, because deployments that already authenticate everything at a proxy must configure single sign-on in TrueUp as well.

## Pros and cons of the options

### OpenID Connect only

- Good, because there is one protocol to get right.
- Bad, because some organizations' identity providers, or their policies, only allow SAML.

### Proxy authentication

- Good, because it is simple to integrate with an existing proxy.
- Bad, because anyone who reaches TrueUp without going through the proxy, or through a misconfigured one, can claim to be anyone by setting the header.
