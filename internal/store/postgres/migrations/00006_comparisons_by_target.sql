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
-- which can change, and its ID in NetBox, or NULL for main. Every comparison
-- recorded before is main's: TrueUp read no branch until now.
ALTER TABLE comparisons
ADD COLUMN branch text COLLATE "C" NOT NULL DEFAULT '' CHECK (branch = '' OR branch ~ '^[a-z0-9]{8}$'),
ADD COLUMN branch_name text,
ADD COLUMN branch_netbox_id bigint,
ADD CHECK (
    (branch = '' AND branch_name IS NULL AND branch_netbox_id IS NULL)
    OR (branch != '' AND branch_name != '' AND branch_netbox_id > 0)
),
DROP CONSTRAINT comparisons_pkey,
ADD PRIMARY KEY (source, definition_id, branch);

-- Every run names its target, as the SQLite table, which has no default, needs.
ALTER TABLE comparisons ALTER COLUMN branch DROP DEFAULT;
