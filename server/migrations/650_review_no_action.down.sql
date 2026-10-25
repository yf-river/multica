-- 回退程序可保留扩展状态；撤销约束前须先处置 no_action 记录。
ALTER TABLE issue_review DROP CONSTRAINT issue_review_state_check;
ALTER TABLE issue_review ADD CONSTRAINT issue_review_state_check CHECK (state IN ('idle','dispatched','rejected','passed','stopped'));
