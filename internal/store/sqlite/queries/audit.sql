-- name: GetSourceConfig :one
-- Reads a source's configuration as its latest fetch recorded it. A write
-- transaction holds SQLite's write lock from its start, so it stays so
-- until the transaction ends.
SELECT kind, url, ref FROM sources WHERE name = ?;

-- name: InsertAuditEvent :exec
INSERT INTO audit_events (
    occurred_at, actor_kind, actor, actor_id, host, sudo_user, through,
    client_address, action, result, completes, error, source, object_type,
    object, netbox_url, netbox_branch, netbox_id, details
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: ListAuditEvents :many
-- Lists the latest events that match, newest first, up to max of them, or
-- all for -1. A null filter matches any event; actor matches an actor's
-- name or ID, or the user who ran sudo. actions is a JSON array of
-- them: sqlc.slice would number its parameters after max's, binding max to
-- an action.
SELECT
    id, occurred_at, actor_kind, actor, actor_id, host, sudo_user, through,
    client_address, action, result, completes, error, source, object_type,
    object, netbox_url, netbox_branch, netbox_id, details
FROM audit_events
WHERE
    (sqlc.narg(since) IS NULL OR occurred_at >= sqlc.narg(since))
    AND (sqlc.narg(until) IS NULL OR occurred_at < sqlc.narg(until))
    AND (
        sqlc.narg(actor) IS NULL
        OR actor = sqlc.narg(actor)
        OR actor_id = sqlc.narg(actor)
        OR sudo_user = sqlc.narg(actor)
    )
    AND (sqlc.narg(source) IS NULL OR source = sqlc.narg(source))
    AND (
        sqlc.narg(actions) IS NULL
        OR action IN (SELECT value FROM json_each(sqlc.narg(actions)))
    )
ORDER BY id DESC
LIMIT sqlc.arg(max);

-- name: GetStartedEvent :one
-- Reads the action and result of an event that a result is to complete.
SELECT action, result FROM audit_events WHERE id = ?;
