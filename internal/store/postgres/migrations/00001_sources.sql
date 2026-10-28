-- Sources, and the latest snapshot of each (docs/architecture.md, "Storage").
-- Only the latest snapshot is kept: each successful fetch replaces it, and
-- its file list, in one transaction.
--
-- The tables are created in the schema netbox_trueup_ai, which TrueUp creates
-- before migrating, since every connection's search_path names that schema
-- alone.
--
-- Columns that are sorted, or that a sorted column refers to, use the "C"
-- collation, so that they sort by bytes, as in SQLite, whatever the
-- database's locale.
--
-- Foreign keys don't cascade: removing a source deletes the rows that refer
-- to it first (see the db-change skill).

-- +goose Up
-- Marks the schema as TrueUp's, so that TrueUp never takes another
-- application's schema of the same name for its own.
COMMENT ON SCHEMA netbox_trueup_ai IS 'netbox-trueup-ai';

CREATE TABLE sources (
    -- The source's configured name, which identifies it.
    name text COLLATE "C" PRIMARY KEY,
    -- The source as its latest fetch found it configured.
    kind text NOT NULL,
    url text NOT NULL,
    ref text NOT NULL, -- '' for the default branch
    -- When the latest fetch started, and why it failed, or NULL if it
    -- succeeded. A failed fetch leaves the snapshot as it was.
    fetched_at timestamptz NOT NULL,
    error text
);

CREATE TABLE snapshots (
    source text COLLATE "C" PRIMARY KEY REFERENCES sources (name),
    -- The URL fetched, the version found, such as a Git commit's hash, and
    -- what the version was resolved from, such as the branch master.
    url text NOT NULL,
    version text NOT NULL,
    ref text NOT NULL,
    -- When the fetch that took the snapshot started.
    fetched_at timestamptz NOT NULL
);

-- The files a snapshot holds, once its patterns and limits apply.
CREATE TABLE snapshot_files (
    source text COLLATE "C" NOT NULL REFERENCES snapshots (source),
    path text COLLATE "C" NOT NULL,
    size bigint NOT NULL CHECK (size >= 0),
    digest text NOT NULL CHECK (length(digest) = 71 AND digest LIKE 'sha256:%'),
    PRIMARY KEY (source, path)
);

-- The entries a snapshot's patterns choose but the snapshot leaves out, in
-- the snapshot's order. A path is kept as the bytes read, since an unsafe
-- name need not be UTF-8, and two unsafe entries can share a path.
CREATE TABLE snapshot_skipped (
    source text COLLATE "C" NOT NULL REFERENCES snapshots (source),
    seq bigint NOT NULL,
    path bytea NOT NULL,
    reason text NOT NULL,
    PRIMARY KEY (source, seq)
);
