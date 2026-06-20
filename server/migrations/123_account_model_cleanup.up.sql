DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'user' AND column_name = 'email'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'user' AND column_name = 'account'
    ) THEN
        ALTER TABLE "user" RENAME COLUMN email TO account;
    END IF;
END $$;

ALTER TABLE "user" DROP COLUMN IF EXISTS language;
DROP TABLE IF EXISTS workspace_invitation;
