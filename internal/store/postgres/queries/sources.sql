-- name: UpsertSource :exec
-- Records a fetch of a source as its latest.
INSERT INTO sources (name, kind, url, ref, fetched_at, error)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (name) DO UPDATE SET
    kind = excluded.kind,
    url = excluded.url,
    ref = excluded.ref,
    fetched_at = excluded.fetched_at,
    error = excluded.error;

-- name: UpsertSnapshot :exec
-- Records a source's latest snapshot, without its lists.
INSERT INTO snapshots (source, url, version, ref, fetched_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (source) DO UPDATE SET
    url = excluded.url,
    version = excluded.version,
    ref = excluded.ref,
    fetched_at = excluded.fetched_at;

-- name: DeleteSnapshotFiles :exec
DELETE FROM snapshot_files WHERE source = $1;

-- name: InsertSnapshotFiles :copyfrom
-- Adds files to a snapshot, all in one COPY.
INSERT INTO snapshot_files (source, path, size, digest) VALUES ($1, $2, $3, $4);

-- name: DeleteSnapshotSkipped :exec
DELETE FROM snapshot_skipped WHERE source = $1;

-- name: InsertSnapshotSkipped :copyfrom
-- Adds skipped entries to a snapshot, all in one COPY.
INSERT INTO snapshot_skipped (source, seq, path, reason) VALUES ($1, $2, $3, $4);

-- name: ListSources :many
SELECT
    sources.name,
    sources.kind,
    sources.url,
    sources.ref,
    sources.fetched_at,
    sources.error,
    snapshots.url AS snapshot_url,
    snapshots.version AS snapshot_version,
    snapshots.ref AS snapshot_ref,
    snapshots.fetched_at AS snapshot_fetched_at
FROM sources
LEFT JOIN snapshots ON snapshots.source = sources.name
ORDER BY sources.name;

-- name: GetSnapshot :one
SELECT source, url, version, ref, fetched_at FROM snapshots WHERE source = $1;

-- name: ListSnapshotFiles :many
SELECT path, size, digest FROM snapshot_files WHERE source = $1 ORDER BY path;

-- name: ListSnapshotSkipped :many
SELECT path, reason FROM snapshot_skipped WHERE source = $1 ORDER BY seq;

-- name: ListSourceNames :many
-- Locks every source's row until the transaction ends, so that a fetch
-- being recorded meanwhile either finishes first, and is removed with the
-- rest, or waits, and records the source afresh.
SELECT name FROM sources ORDER BY name FOR UPDATE;

-- name: DeleteSnapshot :exec
-- Deletes a source's snapshot, once its lists are deleted.
DELETE FROM snapshots WHERE source = $1;

-- name: DeleteSource :exec
-- Deletes a source, once its snapshot is deleted.
DELETE FROM sources WHERE name = $1;
