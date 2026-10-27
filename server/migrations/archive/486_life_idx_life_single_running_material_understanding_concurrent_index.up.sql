CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_life_single_running_material_understanding
    ON public.life_cognition_job (workspace_id, user_id) WHERE job_type = 'understand_materials' AND status = 'running';
