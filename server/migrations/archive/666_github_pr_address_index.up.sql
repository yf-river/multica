-- PR 快照按编号、仓库所有者与仓库名定位。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_github_pull_request_pr_owner_repo
    ON github_pull_request (pr_number, repo_owner, repo_name);
