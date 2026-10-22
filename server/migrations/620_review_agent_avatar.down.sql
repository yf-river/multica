-- 1. Revert the backfilled default avatar; values set by anything else stay.
UPDATE agent SET avatar_url = NULL, updated_at = now()
WHERE system_key = 'issue_review'
  AND avatar_url = 'emoji:🧐';
