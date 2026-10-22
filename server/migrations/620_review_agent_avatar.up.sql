-- 1. The managed review agent used to be created without an avatar, so the
-- reviewer rendered blank in comments and review badges. Backfill empty values
-- only; a workspace that already carries an avatar keeps it.
UPDATE agent SET avatar_url = 'emoji:🧐', updated_at = now()
WHERE system_key = 'issue_review'
  AND (avatar_url IS NULL OR btrim(avatar_url) = '');
