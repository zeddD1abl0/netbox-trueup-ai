-- Sources, and the latest snapshot of each (docs/architecture.md, "Storage").
-- Only the latest snapshot is kept: each successful fetch replaces it, and
-- its file list, in one transaction.
--
-- Times are UTC, written as 2006-01-02T15:04:05.000000Z. The format has a
-- fixed width, so comparing two times as text compares the times.

-- +goose Up
CREATE TABLE sources (
    -- The source's configured name, which identifies it.
    name TEXT PRIMARY KEY,
    -- The source as its latest fetch found it configured.
    kind TEXT NOT NULL,
    url TEXT NOT NULL,
    ref TEXT NOT NULL, -- '' for the default branch
    -- When the latest fetch started, and why it failed, or NULL if it
    -- succeeded. A failed fetch leaves the snapshot as it was.
    fetched_at TEXT NOT NULL CHECK (
        fetched_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'
    ),
    error TEXT
) STRICT;

CREATE TABLE snapshots (
    source TEXT PRIMARY KEY REFERENCES sources (name) ON DELETE CASCADE,
    -- The URL fetched, the version found, such as a Git commit's hash, and
    -- what the version was resolved from, such as the branch master.
    url TEXT NOT NULL,
    version TEXT NOT NULL,
    ref TEXT NOT NULL,
    -- When the fetch that took the snapshot started.
    fetched_at TEXT NOT NULL CHECK (
        fetched_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'
    )
) STRICT;

-- The files a snapshot holds, once its patterns and limits apply.
CREATE TABLE snapshot_files (
    source TEXT NOT NULL REFERENCES snapshots (source) ON DELETE CASCADE,
    path TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size >= 0),
    digest TEXT NOT NULL CHECK (length(digest) = 71 AND digest GLOB 'sha256:*'),
    PRIMARY KEY (source, path)
) STRICT, WITHOUT ROWID;

-- The entries a snapshot's patterns choose but the snapshot leaves out, in
-- the snapshot's order. A path is kept as the bytes read, since an unsafe
-- name need not be UTF-8, and two unsafe entries can share a path.
CREATE TABLE snapshot_skipped (
    source TEXT NOT NULL REFERENCES snapshots (source) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    path BLOB NOT NULL,
    reason TEXT NOT NULL,
    PRIMARY KEY (source, seq)
) STRICT;
