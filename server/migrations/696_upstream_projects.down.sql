DO $$ BEGIN RAISE EXCEPTION 'Project histories are retained; roll back the application without dropping project data'; END $$;
