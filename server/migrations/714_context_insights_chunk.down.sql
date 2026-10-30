DO $$ BEGIN RAISE EXCEPTION 'Pending upload chunks are retained when rolling back the application'; END $$;
