-- Neither timestamp has a current reader: Pull Request links use their actor
-- identity and close intent, while Lark card recovery uses task identity and
-- status. Remove write-only state that falsely implies lifecycle semantics.
ALTER TABLE issue_pull_request
DROP COLUMN IF EXISTS linked_at;

ALTER TABLE lark_outbound_card_message
DROP COLUMN IF EXISTS last_patched_at;
