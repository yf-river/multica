INSERT INTO upstream_sync_config(project_id,upstream_url,upstream_branch,local_ref,sync_state)
 VALUES('openviking','https://github.com/volcengine/OpenViking.git','main','main','paused')
 ON CONFLICT(project_id) DO NOTHING;
