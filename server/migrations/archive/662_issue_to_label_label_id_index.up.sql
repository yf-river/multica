-- 标签统计与清理按标签定位关联。
CREATE INDEX CONCURRENTLY IF NOT EXISTS issue_to_label_label_idx
    ON issue_to_label (label_id);
