DO $$ BEGIN RAISE EXCEPTION 'Deployment history is retained when rolling back the application'; END $$;
