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
-- Backfill first, then constrain: adding NOT NULL to a column holding NULLs
-- fails, and this must not break a deployment that has one.
UPDATE api_keys SET allowed_models = '[]'::jsonb WHERE allowed_models IS NULL;

ALTER TABLE api_keys ALTER COLUMN allowed_models SET NOT NULL;

-- jsonb_typeof pins the shape as well as the presence. An object or a bare
-- string decodes to an empty Go slice just as NULL did, with the same effect.
ALTER TABLE api_keys
    ADD CONSTRAINT allowed_models_is_array
    CHECK (jsonb_typeof(allowed_models) = 'array');

COMMENT ON COLUMN api_keys.allowed_models IS
    'JSON array of permitted model aliases. An empty array means every model is permitted. Never NULL: an unreadable value would grant every model.';
