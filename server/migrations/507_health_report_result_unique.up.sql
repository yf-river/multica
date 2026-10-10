CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS health_report_result_attempt_idx ON health_report_result(report_id,attempt);
