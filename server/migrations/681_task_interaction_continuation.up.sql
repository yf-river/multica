ALTER TABLE task_interaction
    ADD COLUMN delivery_mode TEXT NOT NULL DEFAULT 'live' CHECK (delivery_mode IN ('live', 'suspended')),
    ADD COLUMN followup_task_id UUID;
