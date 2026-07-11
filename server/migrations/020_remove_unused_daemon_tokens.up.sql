-- Daemons authenticate with the current CLI user credential (mul_ PAT, cloud
-- PAT, or JWT). The mdt_ token design was never wired to a production minting
-- path, so this table cannot contain a credential the current product creates.
DROP TABLE IF EXISTS daemon_token;
