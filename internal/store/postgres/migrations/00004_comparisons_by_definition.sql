-- Each definition's unique values, so that compare can tell which
-- definitions NetBox has without reading them whole, and each comparison
-- recorded by its definition's ID (docs/architecture.md, "Storage";
-- docs/adr/0030-compare-what-netbox-has-by-default.md).

-- +goose Up
-- The definition cut down to what matching it with NetBox reads, as JSON of
-- the same form as definition. A definition recorded before holds '' until
-- its source is loaded again, which reading it does, since TrueUp can't
-- read it back.
ALTER TABLE definitions ADD COLUMN unique_values text NOT NULL DEFAULT '';

-- What a comparison refers to its definition by.
ALTER TABLE definitions
ADD CONSTRAINT definitions_by_source_and_id UNIQUE (source, id);

-- Each comparison is now of a definition, by its ID. Each run of compare
-- replaces the comparisons of the definitions it compared, and a run of
-- every definition, or of what NetBox has, removes the source's others. A
-- load removes the comparisons of the definitions it removes. A comparison
-- recorded before keeps its definition's, if the definition is recorded.
-- The others are of sources not loaded since, or of definitions gone, and
-- the next compare makes them again.
ALTER TABLE comparisons ADD COLUMN definition_id bigint;

UPDATE comparisons AS c SET definition_id = d.id
FROM definitions AS d
WHERE
    d.source = c.source
    AND d.object_type = c.object_type
    AND d.path = c.path
    AND d.identity = c.identity;

DELETE FROM comparisons
WHERE
    definition_id IS NULL
    OR outcome NOT IN ('candidate', 'absent', 'conflict');

-- Dropping the definition's key drops the primary key it was part of.
ALTER TABLE comparisons
DROP COLUMN object_type,
DROP COLUMN path,
DROP COLUMN identity,
ALTER COLUMN definition_id SET NOT NULL,
ADD PRIMARY KEY (source, definition_id),
ADD FOREIGN KEY (source, definition_id) REFERENCES definitions (source, id),
ADD CHECK (outcome IN ('candidate', 'absent', 'conflict'));
