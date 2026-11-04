-- The latest comparison of each definition with NetBox (docs/architecture.md,
-- "Storage"). Each run of compare replaces the comparisons of the definitions
-- it compared, and removes those of definitions the source no longer holds.
--
-- A comparison is of the version of its source that the source's snapshot
-- holds when it is recorded, which source_version keeps: a later fetch leaves
-- it, so that it shows what it was based on.

-- +goose Up
CREATE TABLE comparisons (
    source TEXT NOT NULL REFERENCES snapshots (source),
    -- The definition: its object type, its file, or '' for one the format
    -- derives, and its identity as JSON.
    object_type TEXT NOT NULL,
    path TEXT NOT NULL,
    identity TEXT NOT NULL,
    -- The run that compared it: the source's version, when, and the NetBox
    -- it was compared with, and NetBox's release.
    source_version TEXT NOT NULL,
    compared_at TEXT NOT NULL CHECK (
        compared_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'
    ),
    netbox_url TEXT NOT NULL,
    netbox_version TEXT NOT NULL,
    -- What it found: the outcome, the NetBox object matched, or NULL, how
    -- many fields differ, and the whole result, as compare --output json
    -- gives it.
    outcome TEXT NOT NULL,
    netbox_id INTEGER CHECK (netbox_id > 0),
    differences INTEGER NOT NULL CHECK (differences >= 0),
    result TEXT NOT NULL,
    PRIMARY KEY (source, object_type, path, identity)
) STRICT;
