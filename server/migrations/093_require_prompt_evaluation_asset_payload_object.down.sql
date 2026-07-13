-- Data normalization is one-way; rollback only removes the shape constraint.
ALTER TABLE prompt_evaluation_asset
DROP CONSTRAINT IF EXISTS prompt_evaluation_asset_payload_is_object;
