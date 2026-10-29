-- @compat: 仅扩展状态分类约束，既有分类和写入继续有效；旧程序恢复前须确认没有待发布任务。
ALTER TABLE issue_status
    DROP CONSTRAINT issue_status_category_check,
    ADD CONSTRAINT issue_status_category_check CHECK (category IN ('backlog','todo','in_progress','in_review','pending_release','done','blocked','cancelled'));
