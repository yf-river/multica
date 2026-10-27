INSERT INTO public.life_cognition_job (
    workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input, scheduled_at
)
SELECT experiment.workspace_id,
       experiment.user_id,
       profile.agent_id,
       'experiment_check',
       'round:' || round.id::text || ':review-repair-020',
       jsonb_build_object(
           'round_id', round.id,
           'status', round.status,
           'plan', round.plan,
           'starts_at', round.starts_at,
           'ends_at', round.ends_at,
           'stopped_at', round.stopped_at,
           'stop_reason', round.stop_reason
       ),
       now()
FROM public.life_experiment_round round
JOIN public.life_experiment experiment ON experiment.id = round.experiment_id
JOIN public.companion_profile profile
  ON profile.workspace_id = experiment.workspace_id
 AND profile.user_id = experiment.user_id
WHERE round.status = 'awaiting_review'
  AND round.review_draft IS NULL
ON CONFLICT (workspace_id, user_id, job_type, dedupe_key) DO NOTHING;
