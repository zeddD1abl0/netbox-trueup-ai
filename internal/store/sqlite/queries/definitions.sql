-- name: UpsertLoadFailed :exec
-- Records a load of a source that failed, keeping what the latest load
-- that succeeded recorded.
INSERT INTO definition_loads (
    source, started_at, version, reader, settings, error, files, invalid_files
) VALUES (?, ?, ?, ?, ?, ?, 0, 0)
ON CONFLICT (source) DO UPDATE SET
    started_at = excluded.started_at,
    version = excluded.version,
    reader = excluded.reader,
    settings = excluded.settings,
    error = excluded.error;

-- name: UpsertLoadReady :exec
-- Records a load of a source that succeeded.
INSERT INTO definition_loads (
    source, started_at, version, reader, settings, error,
    loaded_at, loaded_version, loaded_reader, loaded_settings, files, invalid_files
) VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?)
ON CONFLICT (source) DO UPDATE SET
    started_at = excluded.started_at,
    version = excluded.version,
    reader = excluded.reader,
    settings = excluded.settings,
    error = NULL,
    loaded_at = excluded.loaded_at,
    loaded_version = excluded.loaded_version,
    loaded_reader = excluded.loaded_reader,
    loaded_settings = excluded.loaded_settings,
    files = excluded.files,
    invalid_files = excluded.invalid_files;

-- name: ListDefinitionKeys :many
SELECT id, object_type, path, identity FROM definitions WHERE source = ?;

-- name: DeleteDefinition :exec
DELETE FROM definitions WHERE id = ?;

-- name: DeleteDefinitions :exec
-- Deletes every definition of a source.
DELETE FROM definitions WHERE source = ?;

-- name: UpsertDefinition :exec
-- Records a definition, keeping its ID if the source defined it before,
-- and leaving its row as it is if nothing in it changed.
INSERT INTO definitions (
    source, object_type, path, identity, seq,
    manufacturer, model, slug, part_number, search, digest, definition
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (source, object_type, path, identity) DO UPDATE SET
    seq = excluded.seq,
    manufacturer = excluded.manufacturer,
    model = excluded.model,
    slug = excluded.slug,
    part_number = excluded.part_number,
    search = excluded.search,
    digest = excluded.digest,
    definition = excluded.definition
WHERE seq IS NOT excluded.seq
OR digest IS NOT excluded.digest
OR definition IS NOT excluded.definition
OR manufacturer IS NOT excluded.manufacturer
OR model IS NOT excluded.model
OR slug IS NOT excluded.slug
OR part_number IS NOT excluded.part_number
OR search IS NOT excluded.search;

-- name: DeleteDefinitionProblems :exec
DELETE FROM definition_problems WHERE source = ?;

-- name: InsertDefinitionProblem :exec
INSERT INTO definition_problems (
    source, seq, severity, path, line_number, column_number, field, reason
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: DeleteDefinitionLoad :exec
-- Deletes the record of a source's loads, once its definitions and
-- problems are deleted.
DELETE FROM definition_loads WHERE source = ?;

-- name: GetLoadStatus :one
-- Returns a source's snapshot, with the status of its loads, if any, and
-- how many definitions are recorded.
SELECT
    snapshots.version AS snapshot_version,
    snapshots.ref AS snapshot_ref,
    definition_loads.started_at,
    definition_loads.version,
    definition_loads.reader,
    definition_loads.settings,
    definition_loads.error,
    definition_loads.loaded_at,
    definition_loads.loaded_version,
    definition_loads.loaded_reader,
    definition_loads.loaded_settings,
    definition_loads.files,
    definition_loads.invalid_files,
    (SELECT count(*) FROM definitions WHERE definitions.source = snapshots.source) AS definitions
FROM snapshots
LEFT JOIN definition_loads ON definition_loads.source = snapshots.source
WHERE snapshots.source = ?;

-- name: ListDefinitions :many
SELECT
    id, object_type, path, identity,
    manufacturer, model, slug, part_number, digest, definition
FROM definitions
WHERE source = ?
ORDER BY seq;

-- name: SearchDefinitions :many
-- Returns a source's definitions whose search text holds the search, which
-- is in lower case, exactly: instr, unlike LIKE, gives % and _ no meaning.
SELECT
    id, object_type, path, identity,
    manufacturer, model, slug, part_number, digest, definition
FROM definitions
WHERE source = sqlc.arg(source) AND instr(search, sqlc.arg(search)) > 0
ORDER BY seq;

-- name: ListDefinitionProblems :many
SELECT severity, path, line_number, column_number, field, reason
FROM definition_problems
WHERE source = ?
ORDER BY seq;
