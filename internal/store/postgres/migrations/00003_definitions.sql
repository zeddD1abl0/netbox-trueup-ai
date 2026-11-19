-- The definitions read from each source's snapshot, and the problems with
-- its files, so that searches and pages read them from here, never from the
-- source's files (docs/architecture.md, "Storage";
-- docs/adr/0029-keep-a-lookup-table-of-definitions.md). Each load of a
-- source replaces them in one transaction, keeping the ID of a definition
-- the source still defines.

-- +goose Up
CREATE TABLE definition_loads (
    source text COLLATE "C" PRIMARY KEY REFERENCES snapshots (source),
    -- The latest load: when it started, the version of the snapshot it
    -- read, the format that read it and its version, a digest of the
    -- source's settings that chose the files it read, and why it failed,
    -- or NULL if it succeeded. A load in progress isn't recorded.
    started_at timestamptz NOT NULL,
    version text NOT NULL,
    reader text NOT NULL,
    settings text NOT NULL,
    error text,
    -- The latest load that succeeded, whose definitions are recorded, the
    -- same, or NULL if none has: when it started, what it read, and how
    -- many files it took as definitions, and skipped as invalid.
    loaded_at timestamptz,
    loaded_version text,
    loaded_reader text,
    loaded_settings text,
    files bigint NOT NULL CHECK (files >= 0),
    invalid_files bigint NOT NULL CHECK (invalid_files >= 0 AND invalid_files <= files),
    CHECK (
        (loaded_at IS NULL) = (loaded_version IS NULL)
        AND (loaded_at IS NULL) = (loaded_reader IS NULL)
        AND (loaded_at IS NULL) = (loaded_settings IS NULL)
    ),
    -- A load that succeeded is the latest that did.
    CHECK (error IS NOT NULL OR loaded_at IS NOT NULL)
);

CREATE TABLE definitions (
    -- The definition's internal ID: never reused, and kept by later loads
    -- for as long as the source defines the same object in the same file.
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source text COLLATE "C" NOT NULL REFERENCES definition_loads (source),
    -- The definition: its object type, its file, or '' for one the format
    -- derives, and its identity as JSON.
    object_type text COLLATE "C" NOT NULL,
    path text COLLATE "C" NOT NULL,
    identity text COLLATE "C" NOT NULL,
    -- Its place in the format's order.
    seq bigint NOT NULL CHECK (seq >= 0),
    -- What it is found and shown by, or '': a device type's manufacturer,
    -- model, slug and part number, or a manufacturer's name and slug; and
    -- what a search looks in, the manufacturer, model and part number in
    -- lower case, one per line.
    manufacturer text NOT NULL,
    model text NOT NULL,
    slug text NOT NULL,
    part_number text NOT NULL,
    search text NOT NULL,
    -- The SHA-256 of its file, or '' for a definition the format derives.
    digest text NOT NULL CHECK (digest = '' OR (length(digest) = 71 AND digest LIKE 'sha256:%')),
    -- The definition, as list --output json gives it: text, not jsonb, so
    -- that it is kept byte for byte.
    definition text NOT NULL,
    UNIQUE (source, object_type, path, identity)
);

-- A source's definitions in its format's order.
CREATE INDEX definitions_by_seq ON definitions (source, seq);
-- An object's definitions in every source, by its type and identity.
CREATE INDEX definitions_by_object ON definitions (object_type, identity);

-- The problems with a source's files, in its format's order.
CREATE TABLE definition_problems (
    source text COLLATE "C" NOT NULL REFERENCES definition_loads (source),
    seq bigint NOT NULL,
    severity text NOT NULL CHECK (severity IN ('error', 'warning')),
    -- The file, or '' for a problem with what was derived from several.
    path text NOT NULL,
    -- Where in the file, from 1, or 0; and where in its structure, or ''.
    line_number bigint NOT NULL CHECK (line_number >= 0),
    column_number bigint NOT NULL CHECK (column_number >= 0),
    field text NOT NULL,
    reason text NOT NULL,
    PRIMARY KEY (source, seq)
);
