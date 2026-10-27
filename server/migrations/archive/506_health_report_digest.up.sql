CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS health_report_digest_idx ON health_report(workspace_id,user_id,sha256);
