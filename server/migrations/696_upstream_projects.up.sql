CREATE TABLE upstream_project (
 id text NOT NULL,
 name text NOT NULL,
 enabled boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO upstream_project(id,name) VALUES('multica','Multica'),('openviking','OpenViking');
ALTER TABLE upstream_sync_config DROP CONSTRAINT upstream_sync_config_singleton_check;
ALTER TABLE upstream_sync_settings DROP CONSTRAINT upstream_sync_settings_singleton_check;
ALTER TABLE upstream_sync_config ADD COLUMN project_id text NOT NULL DEFAULT 'multica';
ALTER TABLE upstream_sync_settings ADD COLUMN project_id text NOT NULL DEFAULT 'multica';
ALTER TABLE upstream_sync_run ADD COLUMN project_id text NOT NULL DEFAULT 'multica';
ALTER TABLE upstream_sync_item ADD COLUMN project_id text NOT NULL DEFAULT 'multica';
ALTER TABLE upstream_sync_commit ADD COLUMN project_id text NOT NULL DEFAULT 'multica';
DROP INDEX upstream_sync_config_singleton;
ALTER TABLE upstream_sync_settings DROP CONSTRAINT upstream_sync_settings_pkey;
DROP INDEX upstream_sync_item_cluster;
DROP INDEX upstream_sync_commit_unique;
UPDATE autopilot_trigger SET enabled=false,updated_at=now()
 WHERE autopilot_id IN (SELECT autopilot_id FROM upstream_sync_config) AND kind='schedule';
