ALTER TABLE supervision_config
    DROP CONSTRAINT IF EXISTS supervision_config_pkey,
    ADD CONSTRAINT supervision_config_pkey PRIMARY KEY USING INDEX supervision_config_pkey_idx;

ALTER TABLE supervision_subject
    DROP CONSTRAINT IF EXISTS supervision_subject_pkey,
    ADD CONSTRAINT supervision_subject_pkey PRIMARY KEY USING INDEX supervision_subject_pkey_idx;

ALTER TABLE supervision_material
    DROP CONSTRAINT IF EXISTS supervision_material_pkey,
    ADD CONSTRAINT supervision_material_pkey PRIMARY KEY USING INDEX supervision_material_pkey_idx;

ALTER TABLE supervision_batch
    DROP CONSTRAINT IF EXISTS supervision_batch_pkey,
    ADD CONSTRAINT supervision_batch_pkey PRIMARY KEY USING INDEX supervision_batch_pkey_idx;

ALTER TABLE supervision_finding
    DROP CONSTRAINT IF EXISTS supervision_finding_pkey,
    ADD CONSTRAINT supervision_finding_pkey PRIMARY KEY USING INDEX supervision_finding_pkey_idx;

ALTER TABLE supervision_action
    DROP CONSTRAINT IF EXISTS supervision_action_pkey,
    ADD CONSTRAINT supervision_action_pkey PRIMARY KEY USING INDEX supervision_action_pkey_idx;
