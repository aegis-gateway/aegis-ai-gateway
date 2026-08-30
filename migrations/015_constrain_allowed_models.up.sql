-- api_keys.allowed_models is an access control whose EMPTY value grants every
-- model, so its representation must be unambiguous.
--
-- The column was created as JSONB DEFAULT '[]' with no NOT NULL, so a SQL NULL
-- was possible on any row an import or a manual UPDATE touched. That value read
-- back as an empty allowlist, which is indistinguishable from "no restriction"
-- and therefore granted every model on a key whose restrictions could not be
-- determined.
--
-- The normal no-allowlist value is the default '[]', not NULL. Backfilling and
-- constraining makes the anomaly impossible rather than leaving the code to
-- refuse it at every read.
--
-- Every row the reader would refuse is repaired, not just the NULLs.
--
-- "An array" is not enough. ["aegis-fast", null] and [1] are arrays, so an
-- element check is the only way for the schema to enforce what the reader
-- accepts: without it such a row survives the cleanup, satisfies the
-- constraint, and then fails to unmarshal into []string, so an active
-- credential starts returning authentication errors having never been revoked,
-- warned about, or repaired. A key that silently stops working is a worse
-- outcome than one that is explicitly revoked with a reason.
--
-- The CHECK below is validated against existing rows, so a single key holding
-- {} or "aegis-fast" would abort the migration and block the whole gateway
-- rollout. Those are the same malformed states the reader now refuses, so they
-- must be handled here rather than left to stop an upgrade.
--
-- A NULL or malformed row is REVOKED, not silently backfilled to '[]'.
--
-- '[]' means unrestricted. Quietly writing it into a row whose restrictions are
-- unknown would grant every model to exactly the keys this migration exists to
-- make safe, and it would do so on the deployments that actually have the
-- anomaly. A migration must not make that decision on an operator's behalf.
--
-- Revoking fails closed without blocking the upgrade. The key stops working,
-- which is the correct outcome for a credential whose restrictions cannot be
-- determined, and revoked_reason says why so an operator can set an explicit
-- allowlist and reissue. allowed_models is set to '[]' on the same rows only so
-- the NOT NULL below can be applied; the key is already revoked by then, so
-- that value grants nothing.
--
-- keygen has always written this column, so on a deployment whose keys were all
-- issued normally this affects no rows at all.
--
-- WHERE THE WARNINGS BELOW APPEAR. They are RAISE WARNING, so PostgreSQL emits
-- them to the server log and to psql. cmd/migrate does not surface server
-- notices, so running the migration through it will NOT print them. On a
-- deployment where these rows might exist, run this file through psql or check
-- the PostgreSQL log, and afterwards query for the revoked_reason this
-- migration writes.
DO $$
DECLARE
    affected INTEGER;
    filled   INTEGER;
BEGIN
    -- Only a key that is active AND UNEXPIRED is revoked here.
    --
    -- A revoked key carries lifecycle evidence in revoked_at and revoked_reason,
    -- a compromise response or a departure, and overwriting that with this
    -- migration's timestamp and generic message would destroy security-relevant
    -- history irreversibly, merely to prepare a column for NOT NULL.
    --
    -- An expired key needs the same protection for a less obvious reason.
    -- Expiry is expressed by expires_at, not by status: lookupDB filters on
    -- expires_at > NOW() and nothing ever transitions a row to an expired
    -- status, so an expired credential normally still reads status = 'active'.
    -- Revoking it would relabel an expiry as a revocation in the permanent
    -- record, and would foreclose the ordinary remedy of extending expires_at
    -- once the allowlist is repaired.
    --
    -- Neither kind can authenticate, so neither needs revoking to fail closed.
    -- Both get the allowed_models fill and nothing else.
    UPDATE api_keys
       SET allowed_models = '[]'::jsonb
     WHERE (allowed_models IS NULL
              OR jsonb_typeof(allowed_models) <> 'array'
              OR jsonb_path_exists(allowed_models, '$[*] ? (@.type() != "string")'))
       AND (status <> 'active' OR expires_at <= NOW());

    -- Reported, because the fill leaves these rows unrestricted on paper.
    -- Neither an expired nor a revoked key can authenticate, so nothing is open
    -- now; but '[]' means "every model permitted", so extending expires_at or
    -- reactivating one of these without first setting an explicit allowlist
    -- would silently produce an unrestricted key. The migration cannot know the
    -- intended allowlist, and revoking an expired key would relabel its
    -- lifecycle, so the operator is told instead.
    GET DIAGNOSTICS filled = ROW_COUNT;
    IF filled > 0 THEN
        RAISE WARNING 'migration 015 filled allowed_models on % revoked or expired API key(s) whose value was malformed. They cannot authenticate as they stand, but the fill reads as unrestricted: set an explicit allowed_models before extending expiry or reactivating any of them.', filled;
    END IF;

    UPDATE api_keys
       SET status         = 'revoked',
           revoked_at     = NOW(),
           revoked_reason = 'allowed_models was not a JSON array of strings at migration 015; restrictions unknown, revoked to fail closed',
           allowed_models = '[]'::jsonb
     WHERE (allowed_models IS NULL
              OR jsonb_typeof(allowed_models) <> 'array'
              OR jsonb_path_exists(allowed_models, '$[*] ? (@.type() != "string")'))
       AND status = 'active'
       AND expires_at > NOW();

    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected > 0 THEN
        RAISE WARNING 'migration 015 revoked % API key(s) whose allowed_models was not a JSON array of strings. Their restrictions could not be determined, so they fail closed. Set an explicit allowed_models and reissue.', affected;
    END IF;
END $$;

ALTER TABLE api_keys ALTER COLUMN allowed_models SET NOT NULL;

-- The constraint pins the shape AND the element type, so the column can hold
-- only what the reader accepts. An object or a bare string decodes to an empty
-- Go slice just as NULL did, with the same fail-open effect; an array with a
-- non-string element decodes to an error, which fails closed but leaves an
-- active key that can never authenticate.
ALTER TABLE api_keys
    ADD CONSTRAINT allowed_models_is_string_array
    CHECK (
        jsonb_typeof(allowed_models) = 'array'
        AND NOT jsonb_path_exists(allowed_models, '$[*] ? (@.type() != "string")')
    );

COMMENT ON COLUMN api_keys.allowed_models IS
    'JSON array of permitted model alias strings. An empty array means every model is permitted. Never NULL: an unreadable value would grant every model.';
