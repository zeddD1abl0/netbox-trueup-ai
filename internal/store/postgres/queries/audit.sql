-- name: GetSourceConfig :one
-- Reads a source's configuration as its latest fetch recorded it, and locks
-- its row until the transaction ends, so that another fetch's record waits.
SELECT kind, url, ref FROM sources WHERE name = $1 FOR UPDATE;

-- name: InsertSourceIfAbsent :execrows
-- Records a source's first fetch, unless another transaction recorded the
-- source meanwhile, as RecordFetch's lock can't stop: it returns 0 then.
INSERT INTO sources (name, kind, url, ref, fetched_at, error)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (name) DO NOTHING;

-- name: InsertAuditEvent :exec
INSERT INTO audit_events (
    occurred_at, actor_kind, actor, actor_id, host, sudo_user, sudo_user_id,
    through, client_address, action, result, completes, error, source,
    object_type, object, netbox_url, netbox_branch, netbox_id, details
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
    $17, $18, $19, $20
);

-- name: ListAuditEvents :many
-- Lists the latest events that match, newest first, up to max of them, or
-- all for null. A null filter, or no actions, matches any event; actor
-- matches an actor's name or ID, or the name or ID of the user who ran sudo.
SELECT
    id, occurred_at, actor_kind, actor, actor_id, host, sudo_user,
    sudo_user_id, through, client_address, action, result, completes, error,
    source, object_type, object, netbox_url, netbox_branch, netbox_id, details
FROM audit_events
WHERE
    (sqlc.narg(since)::timestamptz IS NULL OR occurred_at >= sqlc.narg(since))
    AND (sqlc.narg(until)::timestamptz IS NULL OR occurred_at < sqlc.narg(until))
    AND (
        sqlc.narg(actor)::text IS NULL
        OR actor = sqlc.narg(actor)
        OR actor_id = sqlc.narg(actor)
        OR sudo_user = sqlc.narg(actor)
        OR sudo_user_id = sqlc.narg(actor)
    )
    AND (sqlc.narg(source)::text IS NULL OR source = sqlc.narg(source))
    AND (
        cardinality(sqlc.arg(actions)::text []) = 0
        OR action = any(sqlc.arg(actions)::text [])
    )
ORDER BY id DESC
LIMIT sqlc.narg(max)::bigint;

-- name: GetStartedEvent :one
-- Reads the action and result of an event that a result is to complete.
SELECT action, result FROM audit_events WHERE id = $1;
