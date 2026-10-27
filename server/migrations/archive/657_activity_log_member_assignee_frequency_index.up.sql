-- 成员分配频率统计按工作区与操作者读取历史。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_activity_log_member_assignee_frequency
    ON activity_log (
        workspace_id,
        actor_id,
        (details->>'to_type'),
        (details->>'to_id')
    )
    WHERE actor_type = 'member'
      AND action = 'assignee_changed'
      AND details->>'to_type' IS NOT NULL
      AND details->>'to_id' IS NOT NULL;
