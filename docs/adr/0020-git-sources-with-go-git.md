---
status: accepted
date: 2026-10-26
decision-makers: "@zeddD1abl0"
---

# Fetch Git sources with go-git v6, leaving out files larger than the limit

## Context and problem

Milestone 1 reads devicetype-library from Git ([ADR 0009](0009-source-kinds-and-format-adapters.md)). [architecture.md](../architecture.md#git-sources-m1) settled on go-git, so that neither the binary nor the container image needs a `git` executable, and on reading a commit's tree from Git's object store rather than checking it out. It named one known risk: devicetype-library is large, mostly because of its elevation images, and a depth-1 clone transferred about 830 MB in October 2026. Roadmap step 5 measures the clone and picks a mitigation if one is needed.

go-git has two lines. v5 is stable; its latest release is 5.19.3, of 2026-10-04. v6 is in beta; 6.0.0-beta.1 came out the same day. The difference that matters here is partial clone: v6's `CloneOptions` take a `Filter`, such as `blob:limit=1m`, which asks the server to leave out large files. v5 can encode a filter in its protocol code, but its `Remote` never sends one.

Repository content is untrusted (boundary B1 in [architecture.md](../architecture.md#trust-boundaries)), so a fetch must be bounded in time, disk and memory, and must not let a server lead it elsewhere.

## Decision drivers

- No `git` executable, and nothing checked out.
- Fetching devicetype-library should cost far less than the whole repository.
- Bounded time, disk and memory against a hostile repository.
- Transport settings for TrueUp alone: our own HTTP client, no redirects, no ambient credentials or configuration.
- A dependency that is maintained, and safe to follow.

## Considered options

1. go-git v6.0.0-beta.1, with a `blob:limit` filter at the source's `max_file_size`
2. go-git v5.19.3, cloning everything
3. go-git v5.19.3, with our own fetch on its protocol code, sending the filter
4. An optional system `git`, used when it is installed

## Measurements

A depth-1, single-branch, bare clone of devicetype-library's `master` at commit `19470a5`, made on 2026-10-25 from a developer's machine into a RAM-backed directory, and then a walk that read and hashed every file fetched:

| go-git | Filter | Fetch | Disk | Peak memory | Files left out |
| --- | --- | --- | --- | --- | --- |
| v5.19.3 | none possible | 68 s | 871 MB | 298 MiB | 0 |
| v6.0.0-beta.1 | none | 57 s | 871 MB | 68 MiB | 0 |
| v6.0.0-beta.1 | `blob:limit=1m` | 34 s | 489 MB | 67 MiB | 174 |
| v6.0.0-beta.1 | `blob:limit=256k` | 9 s | 157 MB | 71 MiB | 1,028 |
| v6.0.0-beta.1 | `blob:limit=128k` | 6 s | 64 MB | 73 MiB | 1,607 |
| v6.0.0-beta.1 | `blob:limit=64k` | 4 s | 24 MB | 70 MiB | 2,138 |
| v6.0.0-beta.1 | `blob:none` | 1 s | 0.5 MB | 20 MiB | 17,587 |

The tree holds 17,587 files and 1.07 GB. Images make up 1.05 GB of that; the 12,400 device type files 14 MB, the largest 19 KB. Every YAML and schema file survives even the 64 KiB filter.

TrueUp's own `source fetch`, built from this decision with the default `max_file_size` of 1 MiB, downloaded 489 MB in 32 seconds, and took 45 seconds and 95 MiB in all, listing and hashing the 17,413 files it kept. Fetching again, with nothing changed upstream, downloaded nothing and took 14 seconds, nearly all of it hashing images; with `include: ["device-types", "schema"]`, 1.5 seconds.

## Decision

Chosen option: "go-git v6.0.0-beta.1, with a `blob:limit` filter at the source's `max_file_size`", because only it brings the download down without our own protocol code, and because v6 lets TrueUp set every transport option for each fetch, which v5 can do only for the whole process.

**Fetching.**

- A fetch lists the repository's refs, and resolves `ref` to the branch of that name, else the tag, or the branch `HEAD` names when `ref` isn't set. If the cache already holds what that ref points to, nothing is downloaded.
- Otherwise TrueUp makes a shallow (depth 1), single-branch, bare clone of the ref, without tags, asking the server to leave out every file larger than `max_file_size`. The filter is `blob:limit` at one byte more than the limit, since it leaves out files of at least the size it names.
- A server that can't filter makes go-git refuse the fetch. TrueUp then fetches again without the filter, still bounded by `max_fetch_size`, and logs a warning.
- go-git can't fetch a left-out file later, and TrueUp never needs to: a file larger than `max_file_size` is skipped whether it was fetched or not. Raising `max_file_size` means a new clone, made at the next fetch.
- Each upstream change means a new clone, not an incremental fetch into the old one. A clone made afresh never grows, never needs repacking, and doesn't depend on go-git's shallow updates, its least-tested path. At the defaults that costs the library about 490 MB per change; a smaller `max_file_size` cuts it, as the table shows.

**The cache.** Each source has a directory in `<data_dir>/cache/sources/`. A clone is made in a temporary directory there, renamed to the commit's hash once complete, and then named in `latest.json`, which is replaced in one step. Older clones are then removed, and so are temporary directories older than four hours, left by fetches that crashed. A failed fetch therefore leaves the previous clone in use. `source fetch` removes the caches of sources no longer configured. The cache is disposable.

**Reading.** The commit's tree is read from the clone's object store, never checked out. File sizes come from the pack index without reading the files. A file the filter left out reads as missing, and is reported as too large; in a clone made without a filter, a missing file is an error. The source layer then checks every name, applies patterns and limits, and reads each file it keeps once, to give its size and SHA-256 digest ([ADR 0009](0009-source-kinds-and-format-adapters.md)).

**Transport.**

- `https` only. go-git gets TrueUp's own `http.Client`: TLS 1.2 or later, verified against the system's trust store, with no way to turn verification off; a proxy from `HTTPS_PROXY` and `NO_PROXY` for `https` only; and no redirects, refused both by go-git's `NoFollowRedirects` policy and by the client's `CheckRedirect`. By default go-git follows the redirects of the first request, which could lead a fetch, and later its credentials, to another host.
- go-git's transports for `http`, `ssh`, `git` and `file` are replaced with ones that refuse to connect, so a URL that slipped past the configuration's checks still reaches nothing.
- go-git reads no global Git configuration while cloning, so `url.<base>.insteadOf` rules and credential helpers in `~/.gitconfig` play no part, and it never reads `~/.netrc`.
- Errors carry go-git's text cut to 500 bytes, since it can hold what the server sent, and URLs in logs and errors carry no user name or password.

**Limits on a hostile repository.**

- `fetch_timeout` bounds the whole fetch.
- `max_fetch_size` bounds what a fetch writes to disk: go-git writes through a filesystem that counts every byte.
- The pack's header announces how many objects it holds; more than 1,100,000 stops the fetch before go-git indexes them, since memory and the index grow with the number of objects, which a small pack of tiny objects can make huge. The source layer walks at most a million entries, so no legitimate source comes near.
- go-git's own checks limit delta chains and inflate objects only to their declared size; the source layer reads each file through a limit as well.
- The clone's files are confined to its directory through `os.Root`, which go-billy's `osfs` uses.

**Credentials** are out of scope for now. An `ssh` URL, which the configuration accepts, fails to fetch with a message saying so, and nothing is taken from an SSH agent, `~/.ssh/config` or `known_hosts`. Supporting private repositories needs its own decision.

**Following the beta.** TrueUp pins 6.0.0-beta.1 and takes later betas, and 6.0.0, through Dependabot's reviewed updates. Moving back to v5 would cost the filter and per-call transport options, and is the fallback if v6 stalls.

### Consequences

- Good, because the library downloads 489 MB at the defaults rather than 871 MB, and 64 MB with `max_file_size: 128KiB`, while every YAML file still arrives.
- Good, because v6 uses about a quarter of v5's memory for the same clone.
- Good, because every transport setting is passed with each fetch, so nothing in the process is changed for go-git, and no other scheme can be reached.
- Bad, because v6 is a beta: its API may change before 6.0.0, so an update can need code changes, and its code has had less use than v5's.
- Bad, because each upstream change downloads the source again, filtered, rather than only what changed.
- Bad, because patterns don't shrink a download: the servers TrueUp targets can't leave files out by path. A future option is a clone without any files (`blob:none`, 0.5 MB for the library), then a second fetch of just the files the patterns choose, as Git's own client fetches a file a partial clone left out; go-git can't do that today.
- Bad, because each fetch reads every file it keeps to hash it: 13 seconds for the whole library at the defaults, a second when `include` narrows it to device types. The database in roadmap step 6 records each snapshot's file list, which later reads can use instead.
- Bad, because testing the filter needs Git's own server: go-git's doesn't offer partial clone. Tests run `git http-backend` when `git` is installed, as it is in CI and the dev container.

## Pros and cons of the options

### go-git v5.19.3, cloning everything

- Good, because it is go-git's stable line.
- Bad, because every clone of the library downloads and stores 871 MB, and takes about 300 MiB of memory.
- Bad, because transport settings, such as the HTTP client and the redirect policy, can only be changed in go-git's process-wide protocol registry.

### go-git v5.19.3, with our own fetch

- Good, because the library stays on the stable line and the download shrinks as with v6.
- Bad, because TrueUp would own about 200 lines of protocol code (capabilities, shallow lines, side-band, storing the pack), the part of a Git client most easily got wrong, to save adopting a beta.

### An optional system `git`

- Good, because Git's own client supports every filter and can fetch left-out files on demand.
- Bad, because the binary and image are meant to work alone, and a second code path would need its own hardening: hooks, configuration, credential helpers and environment variables all affect `git`.

## More information

- The measurements came from small programs on each go-git version, kept out of the repository; TrueUp's own figures from `source fetch` with the default limits.
- go-git's partial clone support: "Fetching with a filter is supported and records the partial clone… go-git cannot fetch the withheld objects back on demand" (`COMPATIBILITY.md` in go-git v6.0.0-beta.1).
- Redirects: `RedirectPolicy` in `plumbing/transport/http` of both lines, whose default follows redirects of the first request.
