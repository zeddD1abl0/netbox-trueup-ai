-- name: GetSnapshotVersion :one
-- Returns the version of a source's snapshot. A write transaction holds
-- SQLite's write lock from its start, so nothing changes it meanwhile.
SELECT version FROM snapshots WHERE source = ?;

-- name: DeleteComparison :exec
-- Deletes the comparison of one of a source's definitions.
DELETE FROM comparisons WHERE source = ? AND definition_id = ?;

-- name: DeleteComparisons :exec
-- Deletes every comparison of a source's definitions.
DELETE FROM comparisons WHERE source = ?;

-- name: InsertComparison :exec
INSERT INTO comparisons (
    source, definition_id, source_version, compared_at,
    netbox_url, netbox_version, outcome, netbox_id, differences, result
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListComparisons :many
-- Returns the comparisons of a source's definitions, each with its
-- definition's key.
SELECT
    d.id, d.object_type, d.path, d.identity,
    c.source_version, c.compared_at, c.netbox_url, c.netbox_version,
    c.outcome, c.netbox_id, c.differences, c.result
FROM comparisons AS c
INNER JOIN definitions AS d ON d.source = c.source AND d.id = c.definition_id
WHERE c.source = ?
ORDER BY d.object_type, d.path, d.identity;
