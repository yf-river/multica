-- User bindings are keyed and authorized by installation/open_id plus the
-- Multica member. The unused union_id placeholder was never supplied by a
-- current caller; bot mention routing uses lark_installation.bot_union_id.
ALTER TABLE lark_user_binding
DROP COLUMN IF EXISTS union_id;
