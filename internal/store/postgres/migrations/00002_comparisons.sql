-- The latest comparison of each definition with NetBox (docs/architecture.md,
-- "Storage"). Each run of compare replaces the comparisons of the definitions
-- it compared, and removes those of definitions the source no longer holds.
--
-- A comparison is of the version of its source that the source's snapshot
-- holds when it is recorded, which source_version keeps: a later fetch leaves
-- it, so that it shows what it was based on.

-- +goose Up
CREATE TABLE comparisons (
    source text COLLATE "C" NOT NULL REFERENCES snapshots (source),
    -- The definition: its object type, its file, or '' for one the format
    -- derives, and its identity as JSON.
    object_type text COLLATE "C" NOT NULL,
    path text COLLATE "C" NOT NULL,
    identity text COLLATE "C" NOT NULL,
    -- The run that compared it: the source's version, when, and the NetBox
    -- it was compared with, and NetBox's release.
    source_version text NOT NULL,
    compared_at timestamptz NOT NULL,
    netbox_url text NOT NULL,
    netbox_version text NOT NULL,
    -- What it found: the outcome, the NetBox object matched, or NULL, how
    -- many fields differ, and the whole result, as compare --output json
    -- gives it. The result is text, not jsonb, so that it is kept byte for
    -- byte.
    outcome text NOT NULL,
    netbox_id bigint CHECK (netbox_id > 0),
    differences bigint NOT NULL CHECK (differences >= 0),
    result text NOT NULL,
    PRIMARY KEY (source, object_type, path, identity)
);
