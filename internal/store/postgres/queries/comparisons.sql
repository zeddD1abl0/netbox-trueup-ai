-- name: LockSource :one
-- Locks a source's row until the transaction ends, so that a fetch or a
-- removal of the source, or another recording of its comparisons, either
-- finishes first or waits. Read the snapshot's version afterwards, in a
-- statement of its own: a join here would give the snapshot as it was
-- before a fetch that the lock waited for, since PostgreSQL re-reads only
-- the rows it locks.
SELECT name FROM sources WHERE name = $1 FOR UPDATE;

-- name: GetSnapshotVersion :one
-- Returns the version of a source's snapshot.
SELECT version FROM snapshots WHERE source = $1;

-- name: DeleteComparisonsOf :exec
-- Deletes the comparisons of those of a source's definitions with the IDs
-- given, in every target.
DELETE FROM comparisons
WHERE source = sqlc.arg(source) AND definition_id = ANY(sqlc.arg(ids)::bigint[]);

-- name: DeleteComparisons :exec
-- Deletes every comparison of a source's definitions, in every target.
DELETE FROM comparisons WHERE source = $1;

-- name: DeleteTargetComparisonsOf :exec
-- Deletes the comparisons of those of a source's definitions with the IDs
-- given, in one target: a branch, by its schema ID, or main, as the empty string.
DELETE FROM comparisons
WHERE
    source = sqlc.arg(source)
    AND branch = sqlc.arg(branch)
    AND definition_id = ANY(sqlc.arg(ids)::bigint[]);

-- name: DeleteTargetComparisons :exec
-- Deletes every comparison of a source's definitions in one target: a
-- branch, by its schema ID, or main, as the empty string.
DELETE FROM comparisons WHERE source = $1 AND branch = $2;

-- name: InsertComparisons :copyfrom
-- Adds comparisons, all in one COPY.
INSERT INTO comparisons (
    source, definition_id, branch, branch_name, branch_netbox_id,
    source_version, compared_at, netbox_url, netbox_version, outcome,
    netbox_id, differences, result
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

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
WHERE c.source = $1
ORDER BY c.branch, d.object_type, d.path, d.identity;
