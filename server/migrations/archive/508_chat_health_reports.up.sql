ALTER TABLE chat_message ADD COLUMN health_report_ids uuid[] NOT NULL DEFAULT '{}';
