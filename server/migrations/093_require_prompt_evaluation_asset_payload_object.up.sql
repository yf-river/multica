-- Asset payload is the current extensible profile object. Preserve all unknown
-- keys, normalize historical non-object shapes once, and prevent response-side
-- decode fallbacks from hiding persistence corruption.
UPDATE prompt_evaluation_asset
SET payload = '{}'::jsonb
WHERE jsonb_typeof(payload) IS DISTINCT FROM 'object';

ALTER TABLE prompt_evaluation_asset
DROP CONSTRAINT IF EXISTS prompt_evaluation_asset_payload_is_object;

ALTER TABLE prompt_evaluation_asset
ADD CONSTRAINT prompt_evaluation_asset_payload_is_object
CHECK (jsonb_typeof(payload) = 'object');
