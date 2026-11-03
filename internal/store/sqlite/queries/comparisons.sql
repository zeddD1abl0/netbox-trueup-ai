-- name: GetSnapshotVersion :one
-- Returns the version of a source's snapshot. A write transaction holds
-- SQLite's write lock from its start, so nothing changes it meanwhile.
SELECT version FROM snapshots WHERE source = ?;

-- name: ListComparisonKeys :many
SELECT object_type, path, identity FROM comparisons WHERE source = ?;

-- name: DeleteComparison :exec
DELETE FROM comparisons WHERE source = ? AND object_type = ? AND path = ? AND identity = ?;

-- name: DeleteComparisons :exec
-- Deletes every comparison of a source's definitions.
DELETE FROM comparisons WHERE source = ?;

-- name: InsertComparison :exec
INSERT INTO comparisons (
    source, object_type, path, identity, source_version, compared_at,
    netbox_url, netbox_version, outcome, netbox_id, differences, result
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListComparisons :many
SELECT
    object_type, path, identity, source_version, compared_at,
    netbox_url, netbox_version, outcome, netbox_id, differences, result
FROM comparisons
WHERE source = ?
ORDER BY object_type, path, identity;
