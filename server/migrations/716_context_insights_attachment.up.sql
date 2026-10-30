CREATE TABLE context_insights_attachment (
 session_key text NOT NULL,
 attachment_id text NOT NULL,
 media_type text NOT NULL,
 filename text NOT NULL DEFAULT '',
 content bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
