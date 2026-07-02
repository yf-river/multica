# working-on-issues source map

Evidence layer for `SKILL.md`. Every contract the skill states is traced to a
current `file:line` here. Lines were re-derived against `feat/builtin-skills`
after the latest `main` merge; the prior skill cited pre-merge lines that have
since moved (see the "drifted" column). Re-confirm with the verification command
at the bottom before relying on an exact line.

## `multica issue pull-requests` — read MR links from Multica

| Behavior | File:line | Drifted from |
|---|---|---|
| CLI command `pull-requests <id>` (alias `prs`) | `server/cmd/multica/cmd_issue.go:105` | `:104` |
| `runIssuePullRequests` handler | `server/cmd/multica/cmd_issue.go:507` | new citation |
| Calls `GET /api/issues/<id>/pull-requests` | `server/cmd/multica/cmd_issue.go:522` | `:522` (unchanged) |
| API route registration | `server/cmd/server/router.go:480` | `:480` (unchanged) |
| Handler `ListPullRequestsForIssue` → `Queries.ListPullRequestsByIssue` | `server/internal/handler/github.go:466,471` | `:466` (unchanged) |
| Row → response mapper `issuePullRequestRowToResponse` | `server/internal/handler/github.go:149` | new citation |

The CLI resolves the issue ref, GETs the endpoint, and (for `--output json`)
prints the raw `{"pull_requests": [...]}` body. Only `--output` is accepted; the
default `table` shows `NUMBER STATE TITLE URL`.

## MR response shape

Compatibility response struct: `server/internal/handler/github.go:51`. JSON
fields the agent can read off each element of `pull_requests`:

- `number` (`json:"number"`, line 56)
- `html_url` (`json:"html_url"`, line 59)
- `title` (`json:"title"`, line 57)
- `state` (`json:"state"`, line 58) — the folded lifecycle enum (see below)
- `merged_at` (`json:"merged_at"`, line 63), `closed_at` (line 64)
- `mergeable_state` (`json:"mergeable_state"`, line 70) — mirrors the repository provider; UI only
  surfaces `clean`/`dirty`, other values round-trip as unknown
- `checks_conclusion` (`json:"checks_conclusion"`, line 74) — aggregated
  `"passed"`/`"failed"`/`"pending"` or `null` (no observed suite)
- `checks_passed` / `checks_failed` / `checks_pending` (lines 78-80) — per-suite
  counts; `aggregateChecksConclusion` (line 183) folds them into
  `checks_conclusion`

There is **no** standalone `draft` or `merged` boolean in the response. The
MR lifecycle is encoded in the single `state` string by the compatibility state helper
(`server/internal/handler/github.go:994`):

```
merged   → if PullRequest.Merged
closed   → else if PullRequest.State == "closed"
draft    → else if PullRequest.Draft
open     → otherwise
```

`derivePRState` is called when the webhook upserts the row
(`server/internal/handler/github.go:682`), so `state` is what the list endpoint
returns. "Is it merged?" = `state == "merged"` (or `merged_at != null`); "is it a
draft?" = `state == "draft"`. Combine with `checks_conclusion` for CI status.

## Explicit platform MR record

- API route: `server/cmd/server/router.go`
  `POST /api/issues/{id}/merge-requests/create`
- Handler: `server/internal/handler/github.go`
  `CreateMergeRequestForIssue`
- CLI: `server/cmd/multica/cmd_issue_pull_request.go`
  `multica issue mr create`, `mr list`, and `mr link`
- Completion gate: `server/internal/service/task.go`
  `closeIssueAfterCompletedSOPRun`

Current product contract: provider webhooks mirror MR/PR state, but issue
association is a synchronous platform action (`multica issue mr create` or
explicit `multica issue mr link`). Agents must not depend on title/body/branch
identifier scanning to create the issue link.

Drifted from the prior skill's handler citation.

Net: a readable issue key in an MR remains useful for humans, but the reliable
platform record is the explicit linked MR row.

## Status side effects (enqueue contracts)

| Behavior | File:line | Drifted from |
|---|---|---|
| Create-time: agent-assigned, non-backlog issue enqueues immediately | `server/internal/handler/issue.go:2263-2264` | new citation |
| `shouldEnqueueAgentTask` returns false for `backlog` (parking lot) | `server/internal/handler/issue.go:2644-2648` | new citation |
| Backlog → non-backlog (not done/cancelled) enqueues on update | `server/internal/handler/issue.go:2537-2540` | `:2523` |
| Same contract in batch update | `server/internal/handler/issue.go:3021-3024` | new citation |
| Child → `done` posts a system comment on the parent | `server/internal/handler/issue_child_done.go:51` (`notifyParentOfChildDone`; doc comment at `:15`) | func def `:51` |

Creation with `--status todo` (or any non-backlog status) on an agent-assigned
issue fires the agent immediately; `--status backlog` parks it with the assignee
set but no trigger. Promoting `backlog → todo` later fires it then (update path,
line 2537).

## Metadata CLI

| Behavior | File:line |
|---|---|
| `multica issue metadata set <issue-id> --key --value [--type]` | `server/cmd/multica/cmd_issue_metadata.go:80,109-111` |
| `multica issue metadata delete <issue-id> --key` | `server/cmd/multica/cmd_issue_metadata.go:93,113` |
| API routes (PUT/DELETE `/metadata/{key}`) | `server/cmd/server/router.go:478-479` |

`--value` is JSON-parsed by default (bool/number sniff); `--type` forces
`string`/`number`/`bool`.

## Verification command

Re-derive any line above before depending on it:

```bash
cd server
grep -n 'pull-requests <id>'                 cmd/multica/cmd_issue.go
grep -n 'ListPullRequestsForIssue'           cmd/server/router.go internal/handler/github.go
grep -n 'func issuePullRequestRowToResponse\|type .*PullRequestResponse struct\|func derive.*State\|func extractIdentifiers\|func extractClosingIdentifiers\|closingIdentifierRe' internal/handler/github.go
grep -n 'extractIdentifiers(\|extractClosingIdentifiers(\|derive.*State(' internal/handler/github.go
grep -n 'prevIssue.Status == "backlog"\|func (h \*Handler) shouldEnqueueAgentTask' internal/handler/issue.go
grep -n 'func notifyParentOfChildDone'       internal/handler/issue_child_done.go
```
