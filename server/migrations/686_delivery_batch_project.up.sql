ALTER TABLE delivery_batch ADD COLUMN project_id uuid;
ALTER TABLE delivery_batch ADD COLUMN hold_kind text NOT NULL DEFAULT '';
