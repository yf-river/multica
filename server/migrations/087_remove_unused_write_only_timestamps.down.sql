-- Rollback restores empty/default columns only; historical timestamp values
-- cannot be reconstructed and are not part of the current product contract.
ALTER TABLE issue_pull_request
ADD COLUMN IF NOT EXISTS linked_at timestamptz DEFAULT now() NOT NULL;

ALTER TABLE lark_outbound_card_message
ADD COLUMN IF NOT EXISTS last_patched_at timestamptz;
