-- Rollback can only assign a new timestamp; removed historical values were not
-- part of the current binding contract and cannot be reconstructed.
ALTER TABLE lark_user_binding
ADD COLUMN IF NOT EXISTS bound_at timestamptz DEFAULT now() NOT NULL;
