-- One comparison of each definition for each target it was compared in:
-- NetBox's main, or one of its branches, with netbox-branching
-- (docs/architecture.md, "Storage";
-- docs/adr/0036-read-netbox-through-a-branch-named-on-the-command-line.md).

-- +goose Up
-- A comparison is of a definition in one target. A run that compares in a
-- branch replaces only that branch's comparisons, and a run of every
-- definition, or of what NetBox has, removes only that branch's others: main's
-- stay. A load removes the comparisons of the definitions it removes, in
-- every target.
--
-- branch is the branch's schema ID, which netbox-branching never changes, or
-- '' for main. branch_name and branch_netbox_id are its name when compared,
-- which can change, and its ID in NetBox, or NULL for main.
--
-- SQLite can't change a primary key in place, so the table is made again.
-- Nothing refers to comparisons, so no key breaks meanwhile.
CREATE TABLE comparisons_by_target (
    source TEXT NOT NULL REFERENCES snapshots (source),
    definition_id INTEGER NOT NULL,
    branch TEXT NOT NULL CHECK (
        branch = '' OR (length(branch) = 8 AND branch NOT GLOB '*[^a-z0-9]*')
    ),
    branch_name TEXT,
    branch_netbox_id INTEGER,
    source_version TEXT NOT NULL,
    compared_at TEXT NOT NULL CHECK (
        compared_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'
    ),
    netbox_url TEXT NOT NULL,
    netbox_version TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('candidate', 'absent', 'conflict')),
    netbox_id INTEGER CHECK (netbox_id > 0),
    differences INTEGER NOT NULL CHECK (differences >= 0),
    result TEXT NOT NULL,
    PRIMARY KEY (source, definition_id, branch),
    FOREIGN KEY (source, definition_id) REFERENCES definitions (source, id),
    CHECK (
        (branch = '' AND branch_name IS NULL AND branch_netbox_id IS NULL)
        OR (branch != '' AND branch_name != '' AND branch_netbox_id > 0)
    )
) STRICT;

-- Every comparison recorded before is main's: TrueUp read no branch until now.
INSERT INTO comparisons_by_target (
    source, definition_id, branch, branch_name, branch_netbox_id,
    source_version, compared_at, netbox_url, netbox_version, outcome,
    netbox_id, differences, result
)
SELECT
    source, definition_id, '', NULL, NULL,
    source_version, compared_at, netbox_url, netbox_version, outcome,
    netbox_id, differences, result
FROM comparisons;

DROP TABLE comparisons;
ALTER TABLE comparisons_by_target RENAME TO comparisons;
