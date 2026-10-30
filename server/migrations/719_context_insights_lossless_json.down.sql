DO $$ BEGIN RAISE EXCEPTION '原始事件可能包含 JSONB 无法表示的字符，程序回滚保留无损数据'; END $$;
