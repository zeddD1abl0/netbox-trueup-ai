# Security policy

## Reporting a vulnerability

Please **don't** open a public issue for a security problem.

Report it privately through GitHub instead: open the repository's **Security** tab and choose **Report a vulnerability**, or go directly to [the advisory form](https://github.com/zeddD1abl0/netbox-trueup-ai/security/advisories/new).

Include as much of the following as you can:

- what the problem is and what an attacker could do with it;
- the version or commit affected;
- steps to reproduce, or a proof of concept;
- any suggested fix.

This project is maintained by volunteers. You'll get an acknowledgement as soon as possible, and updates as the report is investigated and fixed. Unless you'd rather stay anonymous, you'll be credited in the published advisory.

## Supported versions

There are no releases yet. Once releases begin, security fixes go into the latest release.

## Scope

In scope: the code in this repository, its build and release pipeline, and the artifacts it publishes (binaries, packages, container images, Helm chart).

Out of scope:

- vulnerabilities in NetBox itself, which go to the [NetBox project](https://github.com/netbox-community/netbox/security);
- vulnerabilities in third-party dependencies, which go to the dependency's maintainers. Do tell us if NetBox TrueUp AI is affected.
