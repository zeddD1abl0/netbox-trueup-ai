-- name: UpsertSource :exec
-- Records a fetch of a source as its latest.
INSERT INTO sources (name, kind, url, ref, fetched_at, error)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (name) DO UPDATE SET
    kind = excluded.kind,
    url = excluded.url,
    ref = excluded.ref,
    fetched_at = excluded.fetched_at,
    error = excluded.error;

-- name: UpsertSnapshot :exec
-- Records a source's latest snapshot, without its lists.
INSERT INTO snapshots (source, url, version, ref, fetched_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (source) DO UPDATE SET
    url = excluded.url,
    version = excluded.version,
    ref = excluded.ref,
    fetched_at = excluded.fetched_at;

-- name: DeleteSnapshotFiles :exec
DELETE FROM snapshot_files WHERE source = ?;

-- name: InsertSnapshotFile :exec
INSERT INTO snapshot_files (source, path, size, digest) VALUES (?, ?, ?, ?);

-- name: DeleteSnapshotSkipped :exec
DELETE FROM snapshot_skipped WHERE source = ?;

-- name: InsertSnapshotSkipped :exec
INSERT INTO snapshot_skipped (source, seq, path, reason) VALUES (?, ?, ?, ?);

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
SELECT source, url, version, ref, fetched_at FROM snapshots WHERE source = ?;

-- name: ListSnapshotFiles :many
SELECT path, size, digest FROM snapshot_files WHERE source = ? ORDER BY path;

-- name: ListSnapshotSkipped :many
SELECT path, reason FROM snapshot_skipped WHERE source = ? ORDER BY seq;

-- name: ListSourceNames :many
SELECT name FROM sources ORDER BY name;

-- name: DeleteSnapshot :exec
-- Deletes a source's snapshot, once its lists are deleted.
DELETE FROM snapshots WHERE source = ?;

-- name: DeleteSource :exec
-- Deletes a source, once its snapshot is deleted.
DELETE FROM sources WHERE name = ?;
