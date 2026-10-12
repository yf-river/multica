DO $$
BEGIN
  ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_priority_check;

  UPDATE issue
  SET priority = 'not_important_not_urgent'
  WHERE priority IN ('urgent', 'high', 'medium', 'low', 'none');

  ALTER TABLE issue
    ALTER COLUMN priority SET DEFAULT 'not_important_not_urgent';

  ALTER TABLE issue
    ADD CONSTRAINT issue_priority_check CHECK (
      priority IN (
        'important_urgent',
        'important_not_urgent',
        'not_important_urgent',
        'not_important_not_urgent'
      )
    );
END $$;
