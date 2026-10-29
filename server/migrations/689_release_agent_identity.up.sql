CREATE UNIQUE INDEX CONCURRENTLY agent_issue_release_unique ON agent(workspace_id) WHERE system_key='issue_release';
