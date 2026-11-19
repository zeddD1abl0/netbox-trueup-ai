-- Each definition's unique values, so that compare can tell which
-- definitions NetBox has without reading them whole, and each comparison
-- recorded by its definition's ID (docs/architecture.md, "Storage";
-- docs/adr/0030-compare-what-netbox-has-by-default.md).

-- +goose Up
-- The definition cut down to what matching it with NetBox reads, as JSON of
-- the same form as definition. A definition recorded before holds '' until
-- its source is loaded again, which reading it does, since TrueUp can't
-- read it back.
ALTER TABLE definitions ADD COLUMN unique_values TEXT NOT NULL DEFAULT '';

-- What a comparison refers to its definition by.
CREATE UNIQUE INDEX definitions_by_source_and_id ON definitions (source, id);

-- The latest comparison of each definition with NetBox. Each run of compare
-- replaces the comparisons of the definitions it compared, and a run of
-- every definition, or of what NetBox has, removes the source's others. A
-- load removes the comparisons of the definitions it removes.
--
-- A comparison is of the version of its source that the source's latest
-- load read, which source_version keeps: a later fetch leaves it, so that
-- it shows what it was based on.
CREATE TABLE comparisons_by_definition (
    source TEXT NOT NULL REFERENCES snapshots (source),
    definition_id INTEGER NOT NULL,
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
    outcome TEXT NOT NULL CHECK (outcome IN ('candidate', 'absent', 'conflict')),
    netbox_id INTEGER CHECK (netbox_id > 0),
    differences INTEGER NOT NULL CHECK (differences >= 0),
    result TEXT NOT NULL,
    PRIMARY KEY (source, definition_id),
    FOREIGN KEY (source, definition_id) REFERENCES definitions (source, id)
) STRICT;

-- A comparison recorded before keeps its definition's, if the definition is
-- recorded. The others are of sources not loaded since, or of definitions
-- gone, and the next compare makes them again.
INSERT INTO comparisons_by_definition (
    source, definition_id, source_version, compared_at, netbox_url,
    netbox_version, outcome, netbox_id, differences, result
)
SELECT
    c.source, d.id, c.source_version, c.compared_at, c.netbox_url,
    c.netbox_version, c.outcome, c.netbox_id, c.differences, c.result
FROM comparisons AS c
INNER JOIN definitions AS d
    ON
        d.source = c.source
        AND d.object_type = c.object_type
        AND d.path = c.path
        AND d.identity = c.identity
WHERE c.outcome IN ('candidate', 'absent', 'conflict');

DROP TABLE comparisons;
ALTER TABLE comparisons_by_definition RENAME TO comparisons;
