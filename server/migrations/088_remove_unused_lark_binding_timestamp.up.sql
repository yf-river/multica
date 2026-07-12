-- Binding validity is defined by installation/open_id identity, current
-- workspace membership and the bound user. The write-only bound_at value has
-- no authorization, recovery, ordering, response or observability consumer.
ALTER TABLE lark_user_binding
DROP COLUMN IF EXISTS bound_at;
