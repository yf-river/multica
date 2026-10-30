DO $$ BEGIN RAISE EXCEPTION '部署恢复记录需保留，程序回滚无需删除动作历史'; END $$;
