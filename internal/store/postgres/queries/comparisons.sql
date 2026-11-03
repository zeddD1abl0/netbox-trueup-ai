-- name: LockSnapshotVersion :one
-- Returns the version of a source's snapshot, and locks the source's row
-- until the transaction ends, so that a fetch or a removal of the source, or
-- another recording of its comparisons, either finishes first or waits.
SELECT snapshots.version
FROM sources
JOIN snapshots ON snapshots.source = sources.name
WHERE sources.name = $1
FOR UPDATE OF sources;

-- name: DeleteComparisonsExcept :exec
-- Deletes a source's comparisons, apart from those of the definitions
-- given.
DELETE FROM comparisons
WHERE source = sqlc.arg(source)
AND (object_type, path, identity) NOT IN (
    SELECT
        unnest(sqlc.arg(object_types)::text[]),
        unnest(sqlc.arg(paths)::text[]),
        unnest(sqlc.arg(identities)::text[])
);

-- name: DeleteComparisons :exec
-- Deletes every comparison of a source's definitions.
DELETE FROM comparisons WHERE source = $1;

-- name: InsertComparisons :copyfrom
-- Adds comparisons, all in one COPY.
INSERT INTO comparisons (
    source, object_type, path, identity, source_version, compared_at,
    netbox_url, netbox_version, outcome, netbox_id, differences, result
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: ListComparisons :many
SELECT
    object_type, path, identity, source_version, compared_at,
    netbox_url, netbox_version, outcome, netbox_id, differences, result
FROM comparisons
WHERE source = $1
ORDER BY object_type, path, identity;
