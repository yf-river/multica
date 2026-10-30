DO $$ BEGIN RAISE EXCEPTION '附件属于已采集的洞察原件，程序回滚保留数据'; END $$;
