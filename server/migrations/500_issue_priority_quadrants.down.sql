DO $$
BEGIN
  ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_priority_check;
  UPDATE issue
  SET priority = 'none'
  WHERE priority IN (
    'important_urgent',
    'important_not_urgent',
    'not_important_urgent',
    'not_important_not_urgent'
  );

  ALTER TABLE issue
    ALTER COLUMN priority SET DEFAULT 'none';

  ALTER TABLE issue
    ADD CONSTRAINT issue_priority_check CHECK (
      priority IN ('urgent', 'high', 'medium', 'low', 'none')
    );
END $$;
