CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS user_account_key ON public."user" (account) WHERE account <> '';
