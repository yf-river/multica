-- Rollback restores an empty placeholder only. No current binding flow
-- produced this value, so there is no historical state to reconstruct.
ALTER TABLE lark_user_binding
ADD COLUMN IF NOT EXISTS union_id text;
