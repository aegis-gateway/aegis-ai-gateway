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
-- A NULL row is REVOKED, not backfilled to '[]'.
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
DO $$
DECLARE
    affected INTEGER;
BEGIN
    -- Only an ACTIVE key is revoked here. A key that is already revoked or
    -- expired carries lifecycle evidence, a compromise response or a departure,
    -- in revoked_at and revoked_reason. Overwriting those with this migration's
    -- timestamp and generic message would destroy security-relevant history
    -- irreversibly, and it would do so merely to prepare a column for NOT NULL.
    -- Such rows get the allowed_models fill and nothing else; they already deny.
    UPDATE api_keys
       SET allowed_models = '[]'::jsonb
     WHERE allowed_models IS NULL
       AND status <> 'active';

    UPDATE api_keys
       SET status         = 'revoked',
           revoked_at     = NOW(),
           revoked_reason = 'allowed_models was NULL at migration 015; restrictions unknown, revoked to fail closed',
           allowed_models = '[]'::jsonb
     WHERE allowed_models IS NULL
       AND status = 'active';

    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected > 0 THEN
        RAISE WARNING 'migration 015 revoked % API key(s) whose allowed_models was NULL. Their restrictions could not be determined, so they fail closed. Set an explicit allowed_models and reissue.', affected;
    END IF;
END $$;

ALTER TABLE api_keys ALTER COLUMN allowed_models SET NOT NULL;

-- jsonb_typeof pins the shape as well as the presence. An object or a bare
-- string decodes to an empty Go slice just as NULL did, with the same effect.
ALTER TABLE api_keys
    ADD CONSTRAINT allowed_models_is_array
    CHECK (jsonb_typeof(allowed_models) = 'array');

COMMENT ON COLUMN api_keys.allowed_models IS
    'JSON array of permitted model aliases. An empty array means every model is permitted. Never NULL: an unreadable value would grant every model.';
