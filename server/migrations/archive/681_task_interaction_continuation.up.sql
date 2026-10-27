ALTER TABLE task_interaction
    ADD COLUMN IF NOT EXISTS delivery_mode TEXT NOT NULL DEFAULT 'live' CHECK (delivery_mode IN ('live', 'suspended')),
    ADD COLUMN IF NOT EXISTS followup_task_id UUID;
