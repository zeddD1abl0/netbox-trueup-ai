-- name: UpsertLoadFailed :exec
-- Records a load of a source that failed, keeping what the latest load
-- that succeeded recorded.
INSERT INTO definition_loads (
    source, started_at, version, reader, settings, error, files, invalid_files
) VALUES ($1, $2, $3, $4, $5, $6, 0, 0)
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
) VALUES ($1, $2, $3, $4, $5, NULL, $2, $3, $4, $5, $6, $7)
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
SELECT id, object_type, path, identity FROM definitions WHERE source = $1;

-- name: DeleteDefinitionsByID :exec
-- Deletes definitions by their IDs.
DELETE FROM definitions WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: DeleteDefinitions :exec
-- Deletes every definition of a source.
DELETE FROM definitions WHERE source = $1;

-- name: UpsertDefinitions :exec
-- Records definitions, the nth of each list making the nth definition, as
-- unnest's in one select list go in step, keeping the ID of each the
-- source defined before, and leaving its row as it is if nothing in it
-- changed.
INSERT INTO definitions (
    source, object_type, path, identity, seq,
    manufacturer, model, slug, part_number, search, digest, definition,
    unique_values
)
SELECT
    sqlc.arg(source)::text,
    unnest(sqlc.arg(object_types)::text[]),
    unnest(sqlc.arg(paths)::text[]),
    unnest(sqlc.arg(identities)::text[]),
    unnest(sqlc.arg(seqs)::bigint[]),
    unnest(sqlc.arg(manufacturers)::text[]),
    unnest(sqlc.arg(models)::text[]),
    unnest(sqlc.arg(slugs)::text[]),
    unnest(sqlc.arg(part_numbers)::text[]),
    unnest(sqlc.arg(searches)::text[]),
    unnest(sqlc.arg(digests)::text[]),
    unnest(sqlc.arg(definitions)::text[]),
    unnest(sqlc.arg(unique_values)::text[])
ON CONFLICT (source, object_type, path, identity) DO UPDATE SET
    seq = excluded.seq,
    manufacturer = excluded.manufacturer,
    model = excluded.model,
    slug = excluded.slug,
    part_number = excluded.part_number,
    search = excluded.search,
    digest = excluded.digest,
    definition = excluded.definition,
    unique_values = excluded.unique_values
WHERE (
    definitions.seq, definitions.manufacturer, definitions.model, definitions.slug,
    definitions.part_number, definitions.search, definitions.digest, definitions.definition,
    definitions.unique_values
) IS DISTINCT FROM (
    excluded.seq, excluded.manufacturer, excluded.model, excluded.slug,
    excluded.part_number, excluded.search, excluded.digest, excluded.definition,
    excluded.unique_values
);

-- name: DeleteDefinitionProblems :exec
DELETE FROM definition_problems WHERE source = $1;

-- name: InsertDefinitionProblems :copyfrom
-- Adds a source's problems, all in one COPY.
INSERT INTO definition_problems (
    source, seq, severity, path, line_number, column_number, field, reason
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: DeleteDefinitionLoad :exec
-- Deletes the record of a source's loads, once its definitions and
-- problems are deleted.
DELETE FROM definition_loads WHERE source = $1;

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
WHERE snapshots.source = $1;

-- name: ListDefinitions :many
SELECT
    id, object_type, path, identity,
    manufacturer, model, slug, part_number, digest, definition, unique_values
FROM definitions
WHERE source = $1
ORDER BY seq;

-- name: SearchDefinitions :many
-- Returns a source's definitions whose search text holds the search, which
-- is in lower case, exactly: strpos, unlike LIKE, gives % and _ no meaning.
SELECT
    id, object_type, path, identity,
    manufacturer, model, slug, part_number, digest, definition, unique_values
FROM definitions
WHERE source = sqlc.arg(source) AND strpos(search, sqlc.arg(search)) > 0
ORDER BY seq;

-- name: ListUniqueValues :many
-- Returns a source's definitions, each with its unique values, but not the
-- whole of it.
SELECT
    id, object_type, path, identity,
    manufacturer, model, slug, part_number, digest, unique_values
FROM definitions
WHERE source = $1
ORDER BY seq;

-- name: ListDefinitionsByID :many
-- Returns those of a source's definitions with the IDs given.
SELECT
    id, object_type, path, identity,
    manufacturer, model, slug, part_number, digest, definition, unique_values
FROM definitions
WHERE source = sqlc.arg(source) AND id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY seq;

-- name: ListDefinitionProblems :many
SELECT severity, path, line_number, column_number, field, reason
FROM definition_problems
WHERE source = $1
ORDER BY seq;
