-- The prior implementation cannot resume durable events. Remove only events
-- that have not begun processing; operation rows remain queued for an
-- operator-visible retry instead of fabricating completion.
DELETE FROM domain_event_outbox
WHERE event_type = 'prompt_evaluation_case_operation:requested'
  AND processed_at IS NULL
  AND dead_lettered_at IS NULL;
