-- Tracks what this gateway has submitted to a control plane.
--
-- The gateway keeps its own cursor rather than asking the control plane where
-- to resume. A cursor that falls behind is safe: a control plane implementing
-- api/controlplane/v1 accepts an identical resubmission as a duplicate and
-- rejects a gap, so replaying from an earlier point is idempotent and
-- replaying from a later one is refused rather than silently leaving a hole.
--
-- One row. A gateway reports to one control plane; pointing it at a second
-- would mean a second cursor, and the identity it registered under is not
-- transferable between them.
CREATE TABLE IF NOT EXISTS control_plane_state (
    singleton                 BOOLEAN PRIMARY KEY DEFAULT TRUE,

    -- Base URL of the control plane this gateway reports to. Recorded so that
    -- a cursor is never replayed against a different deployment, which would
    -- submit checkpoints under an identity that deployment did not issue.
    endpoint                  TEXT NOT NULL,

    -- Identity assigned by the control plane at registration.
    gateway_id                UUID NOT NULL,
    gateway_name              TEXT NOT NULL,

    -- Highest audit_checkpoints.id accepted by the control plane. Zero means
    -- nothing has been submitted yet.
    last_submitted_checkpoint BIGINT NOT NULL DEFAULT 0,
    last_submitted_at         TIMESTAMPTZ,

    registered_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT control_plane_state_singleton CHECK (singleton)
);

-- No bearer token is stored here. The credential is supplied through the
-- environment at run time and never written to the database, so a dump of this
-- schema cannot be replayed against the control plane.
