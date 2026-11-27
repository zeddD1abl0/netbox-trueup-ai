-- name: GetSnapshotVersion :one
-- Returns the version of a source's snapshot. A write transaction holds
-- SQLite's write lock from its start, so nothing changes it meanwhile.
SELECT version FROM snapshots WHERE source = ?;

-- name: DeleteComparison :exec
-- Deletes the comparisons of one of a source's definitions, in every target.
DELETE FROM comparisons WHERE source = ? AND definition_id = ?;

-- name: DeleteComparisons :exec
-- Deletes every comparison of a source's definitions, in every target.
DELETE FROM comparisons WHERE source = ?;

-- name: DeleteTargetComparison :exec
-- Deletes the comparison of one of a source's definitions in one target: a
-- branch, by its schema ID, or main, as ''.
DELETE FROM comparisons WHERE source = ? AND definition_id = ? AND branch = ?;

-- name: DeleteTargetComparisons :exec
-- Deletes every comparison of a source's definitions in one target: a
-- branch, by its schema ID, or main, as ''.
DELETE FROM comparisons WHERE source = ? AND branch = ?;

-- name: InsertComparison :exec
INSERT INTO comparisons (
    source, definition_id, branch, branch_name, branch_netbox_id,
    source_version, compared_at, netbox_url, netbox_version, outcome,
    netbox_id, differences, result
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListComparisons :many
-- Returns the comparisons of a source's definitions, in every target, each
-- with its definition's key: main's first, then each branch's.
SELECT
    d.id, d.object_type, d.path, d.identity,
    c.branch, c.branch_name, c.branch_netbox_id,
    c.source_version, c.compared_at, c.netbox_url, c.netbox_version,
    c.outcome, c.netbox_id, c.differences, c.result
FROM comparisons AS c
INNER JOIN definitions AS d ON d.source = c.source AND d.id = c.definition_id
WHERE c.source = ?
ORDER BY c.branch, d.object_type, d.path, d.identity;
