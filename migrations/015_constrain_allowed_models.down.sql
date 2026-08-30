ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS allowed_models_is_string_array;
ALTER TABLE api_keys ALTER COLUMN allowed_models DROP NOT NULL;
