DELETE FROM public.life_cognition_job
WHERE job_type = 'experiment_check'
  AND dedupe_key LIKE 'round:%:review-repair-020'
  AND status = 'queued'
  AND task_id IS NULL;
