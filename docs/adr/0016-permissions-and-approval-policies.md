---
status: accepted
date: 2026-10-23
decision-makers: "@zeddD1abl0"
---

# Scoped permissions, and approval policies and change references that can be turned on

## Context and problem

TrueUp serves two very different users. One person running it against their own NetBox wants to build a plan and apply it, and any approval step would only get in the way. A large organization with several teams needs change management: teams own different parts of the data, changes are approved by the right people before they land, and every change is tied to a change in the organization's own process.

## Decision drivers

- Nothing extra for a single user.
- Separation of duties where an organization needs it.
- Teams can own different sources, object types or selections.
- Every change can be traced to the external change it belongs to.

## Considered options

1. Scoped permissions, plus approval policies and change references that are off by default and turned on per scope
2. Always require a second person's approval
3. Global roles only, with approval left to NetBox (branches and change requests)

## Decision

Chosen option: "Scoped permissions, plus approval policies and change references that are off by default and turned on per scope".

- **Permissions.** A permission grants a role to a user or group on a scope. Roles bundle actions: view; import and choose primaries; override and edit; build plans; approve plans; apply plans and merge branches; administer. Built-in roles cover the usual cases: Viewer, Operator, Approver and Administrator. Scopes are everything, a source, an object type, or a selection such as a manufacturer. Groups come from the identity provider ([ADR 0015](0015-sign-in-and-provisioning.md)).
- **Approval policies** are off by default. Then the person who applies a plan is recorded as its approver. When an administrator turns a policy on for a scope, it sets how many approvals a plan needs and from which groups. A plan touching several scopes must satisfy every policy that covers it.
- **Separation of duties.** Under a policy, the person who built a plan can't approve it.
- **Approvals are bound to content.** An approval covers the plan's exact operations. A rebuilt plan needs fresh approvals.
- **Change references.** Any plan can carry a reference to the ticket for its external change, by URL or by ticket number. A policy can require one, matching a pattern. The reference is recorded with the plan, in audit events and outbound events, in each write's changelog message, and on the plan's NetBox branch.
- **NetBox decides last.** When a plan is written into a NetBox branch and the netbox-changes plugin is installed, NetBox's change request is the final arbiter. A TrueUp policy is checked first and can add requirements, but can never approve a merge NetBox hasn't ([ADR 0017](0017-netbox-branches.md)). Without that plugin, TrueUp's own policy gates the merge.
- **Automatic apply** never applies a plan that a policy covers.

### Consequences

- Good, because a single user never sees an approval step.
- Good, because an organization gets separation of duties and traceability to its change process, for exactly the scopes that need them.
- Bad, because scoped permissions and policies are more to configure and to understand. Built-in roles and "off by default" keep the simple case simple.
- Bad, because a change reference is only recorded, not checked against the organization's change management system. That can come later.

## Pros and cons of the options

### Always require a second person's approval

- Good, because there is one way of working.
- Bad, because a single user can never apply anything on their own.

### Global roles only, with approval left to NetBox

- Good, because TrueUp has less to build.
- Bad, because it only works where the branching and change request plugins are installed, and can't scope permissions to the teams that own each part of the data.
