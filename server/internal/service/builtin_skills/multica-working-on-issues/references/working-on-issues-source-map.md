# Working on issues source map

Current evidence for issue execution, MR linkage and status-trigger behavior.

## Verification

    rg -n "runIssuePullRequests|pull-requests" server/cmd/multica/cmd_issue.go server/cmd/server/router.go
    rg -n "ListPullRequestsForIssue|CreateMergeRequestForIssue|derivePRState" server/internal/handler/github*.go
    rg -n "prevIssue.Status == \"backlog\"|shouldEnqueueAgentTask" server/internal/handler/issue*.go
    go test ./internal/service -run "TestWorkingOnIssuesSkill|TestBuiltinSkillsConformToTemplate"

## Pull and merge request records

| Contract | Current source |
|---|---|
| CLI pull-requests command | server/cmd/multica/cmd_issue.go:105,594 |
| GET issue pull requests route | server/cmd/server/router.go:717 |
| List handler and response mapper | server/internal/handler/github.go:188,594 |
| Explicit merge-request create route/handler | server/cmd/server/router.go:719; server/internal/handler/github.go:674 |
| CLI mr create/list/link | server/cmd/multica/cmd_issue_pull_request.go |
| Provider lifecycle state fold | server/internal/handler/github_webhook.go:522 |
| SOP completion MR gate | server/internal/service/task_claim.go:492 |

The durable issue association is the explicit platform row created or linked
through the API/CLI. Human-readable issue keys in titles or branches are not a
replacement for that record.

The list response shape is defined by GitHubPullRequestResponse in
server/internal/handler/github.go. Lifecycle is the state string: merged,
closed, draft or open. checks_conclusion and check counts report CI state.

## Issue create/update triggers

| Contract | Current source |
|---|---|
| CreateIssue boundary | server/internal/handler/issue.go:591 |
| Current assignee pair validation | server/internal/handler/issue.go:1212 |
| Agent non-backlog enqueue decision | server/internal/handler/issue.go:1283 |
| UpdateIssue and backlog promotion | server/internal/handler/issue.go:893,1181 |
| Batch update equivalent | server/internal/handler/issue_batch.go:17-260 |
| Child done parent notification/dispatch | server/internal/handler/issue_child_done.go:15-387 |

An agent-assigned non-backlog issue may enqueue immediately. Backlog is the
parking state; promotion to a non-terminal executable state runs the readiness
and dedup path.

## Metadata CLI

| Contract | Current source |
|---|---|
| metadata set/delete commands | server/cmd/multica/cmd_issue_metadata.go:80-113 |
| metadata PUT/DELETE routes | server/cmd/server/router.go:714-715 |

Values are JSON by default; the type flag forces string, number or bool.
