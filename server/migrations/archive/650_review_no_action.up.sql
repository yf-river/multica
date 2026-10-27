ALTER TABLE issue_review DROP CONSTRAINT issue_review_state_check;
ALTER TABLE issue_review ADD CONSTRAINT issue_review_state_check CHECK (state IN ('idle','dispatched','rejected','passed','stopped','no_action'));
