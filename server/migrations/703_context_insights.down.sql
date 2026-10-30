DO $$ BEGIN RAISE EXCEPTION 'Collected Context Insights are retained when rolling back the application'; END $$;
