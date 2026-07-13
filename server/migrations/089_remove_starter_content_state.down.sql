-- Rollback restores an empty compatibility column only; the retired workflow
-- and its historical state machine are intentionally not reconstructed.
ALTER TABLE "user"
ADD COLUMN IF NOT EXISTS starter_content_state text;
