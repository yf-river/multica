CREATE TABLE supervision_frozen_material (
 workspace_id uuid NOT NULL,
 issue_id uuid NOT NULL,
 round_id uuid NOT NULL,
 material_cutoff timestamptz NOT NULL,
 kind text NOT NULL,
 source_id uuid NOT NULL,
 run_id uuid,
 generation bigint NOT NULL,
 reviewed_generation bigint NOT NULL DEFAULT 0,
 reviewed_offset integer NOT NULL DEFAULT 0,
 changed_at timestamptz NOT NULL,
 body text NOT NULL
);
