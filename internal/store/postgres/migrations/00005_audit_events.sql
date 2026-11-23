-- The audit log: every action that changes what TrueUp manages, what NetBox
-- holds, or what sources TrueUp reads (docs/architecture.md, "Identity,
-- access and audit"; docs/adr/0034-audit-what-changes-what-trueup-manages.md).

-- +goose Up
-- One row per event, never changed or deleted. Nothing refers to an event
-- by a foreign key, and an event refers to nothing so, since events outlive
-- what they describe.
CREATE TABLE audit_events (
    -- Orders events: each is greater than those recorded before it. A
    -- sequence never hands out a value twice; a transaction that rolls back
    -- leaves a gap.
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- When it was recorded.
    occurred_at timestamptz NOT NULL,
    -- Who acted: how they were identified, their name ('' where it isn't
    -- known), their ID, the host, and the user who ran sudo, if any.
    actor_kind text NOT NULL,
    actor text COLLATE "C" NOT NULL,
    actor_id text NOT NULL,
    host text NOT NULL,
    sudo_user text,
    -- How the action reached TrueUp, and from what address, if any.
    through text NOT NULL,
    client_address text,
    action text COLLATE "C" NOT NULL,
    result text NOT NULL CHECK (result IN ('succeeded', 'failed', 'started')),
    -- For a result, the event that recorded its action as started.
    completes bigint CHECK (completes > 0),
    error text,
    -- What it is about, where each applies: a source, an object, and the
    -- NetBox, branch and ID it is in.
    source text COLLATE "C",
    object_type text COLLATE "C",
    object text,
    netbox_url text,
    netbox_branch text,
    netbox_id bigint CHECK (netbox_id > 0),
    -- The rest, as a JSON object.
    details text NOT NULL,
    CHECK ((result = 'failed') = (error IS NOT NULL)),
    CHECK (completes IS NULL OR result != 'started')
);

CREATE INDEX audit_events_by_time ON audit_events (occurred_at);
CREATE INDEX audit_events_by_actor ON audit_events (actor, id);
CREATE INDEX audit_events_by_actor_id ON audit_events (actor_id, id);
CREATE INDEX audit_events_by_sudo_user ON audit_events (sudo_user, id);
CREATE INDEX audit_events_by_action ON audit_events (action, id);
CREATE INDEX audit_events_by_source ON audit_events (source, id);
CREATE INDEX audit_events_by_object ON audit_events (object_type, netbox_id, id);

-- A started action is completed once, by one result.
CREATE UNIQUE INDEX audit_events_by_completed ON audit_events (completes)
WHERE completes IS NOT NULL;

-- Events are never changed or deleted. The triggers guard against a bug in
-- TrueUp, not against someone who can change the database: its owner can
-- drop them.
-- +goose StatementBegin
CREATE FUNCTION audit_events_never_change() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    RAISE EXCEPTION 'audit events are never changed or deleted';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER audit_events_never_change
BEFORE UPDATE OR DELETE ON audit_events
FOR EACH ROW EXECUTE FUNCTION audit_events_never_change();

CREATE TRIGGER audit_events_never_truncated
BEFORE TRUNCATE ON audit_events
FOR EACH STATEMENT EXECUTE FUNCTION audit_events_never_change();
