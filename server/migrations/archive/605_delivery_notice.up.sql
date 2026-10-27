CREATE TABLE IF NOT EXISTS delivery_notice (
 delivery_id uuid NOT NULL, stage text NOT NULL, comment_id uuid NOT NULL,
 delivered_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
