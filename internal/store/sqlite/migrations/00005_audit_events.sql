-- The audit log: every action that changes what TrueUp manages, what NetBox
-- holds, or what sources TrueUp reads (docs/architecture.md, "Identity,
-- access and audit"; docs/adr/0034-audit-what-changes-what-trueup-manages.md).

-- +goose Up
-- One row per event, never changed or deleted. Nothing refers to an event
-- by a foreign key, and an event refers to nothing so, since events outlive
-- what they describe.
CREATE TABLE audit_events (
    -- Orders events: each is greater than those recorded before it, and
    -- AUTOINCREMENT never reuses one.
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    -- When it was recorded.
    occurred_at TEXT NOT NULL CHECK (
        occurred_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'
    ),
    -- Who acted: how they were identified, their name ('' where it isn't
    -- known), their ID, the host, and the user who ran sudo, if any.
    actor_kind TEXT NOT NULL,
    actor TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    host TEXT NOT NULL,
    sudo_user TEXT,
    -- How the action reached TrueUp, and from what address, if any.
    through TEXT NOT NULL,
    client_address TEXT,
    action TEXT NOT NULL,
    result TEXT NOT NULL CHECK (result IN ('succeeded', 'failed', 'started')),
    -- For a result, the event that recorded its action as started.
    completes INTEGER CHECK (completes > 0),
    error TEXT,
    -- What it is about, where each applies: a source, an object, and the
    -- NetBox, branch and ID it is in.
    source TEXT,
    object_type TEXT,
    object TEXT,
    netbox_url TEXT,
    netbox_branch TEXT,
    netbox_id INTEGER CHECK (netbox_id > 0),
    -- The rest, as a JSON object.
    details TEXT NOT NULL,
    CHECK ((result = 'failed') = (error IS NOT NULL)),
    CHECK (completes IS NULL OR result != 'started')
) STRICT;

CREATE INDEX audit_events_by_time ON audit_events (occurred_at);
CREATE INDEX audit_events_by_actor ON audit_events (actor, id);
CREATE INDEX audit_events_by_action ON audit_events (action, id);
CREATE INDEX audit_events_by_source ON audit_events (source, id);
CREATE INDEX audit_events_by_object ON audit_events (object_type, netbox_id, id);

-- Events are never changed or deleted. The triggers guard against a bug in
-- TrueUp, not against someone who can change the database.
-- +goose StatementBegin
CREATE TRIGGER audit_events_never_change BEFORE UPDATE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit events are never changed');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER audit_events_never_go BEFORE DELETE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit events are never deleted');
END;
-- +goose StatementEnd
