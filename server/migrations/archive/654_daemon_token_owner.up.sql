-- Daemon 凭据绑定签发时的运行时所有者；旧凭据不推断签发人。
ALTER TABLE daemon_token ADD COLUMN IF NOT EXISTS owner_id UUID;
