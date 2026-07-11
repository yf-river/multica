# Gongfeng repository flow

## Ownership and credentials

`workspace.repos` is the workspace-level repository inventory. Each entry owns
the current Gongfeng project path, selected default branch and last resolved
commit metadata. A Project may attach a `gongfeng_repo` resource, but that
resource is a scoped reference to an inventory entry, not a second repository
configuration.

The Settings repository view reads and updates the inventory through the
workspace API. The backend normalizes repository URLs and rejects removal of
the last inventory entry for a project path while a Project resource still
uses it. This preserves a single repository identity across workspace,
project, Issue and daemon execution context.

Gongfeng credentials are account-scoped external credential profiles. Tokens
are resolved at the HTTP boundary and are never persisted in workspace or
project resource JSON. Probe, branch-list and resolve operations require a
usable profile and return explicit authorization, not-found and transport
errors.

## Repository resolution and execution

`POST /api/workspaces/{id}/repos/probe` validates access and lists branches
without changing the inventory. `POST /api/workspaces/{id}/repos/resolve`
resolves the selected branch head and returns normalized inventory metadata;
the caller persists that result with the workspace update endpoint.

Project resource create/update verifies that its Gongfeng project path exists
in the workspace inventory. The daemon receives the resulting Project resource
as execution context. Local checkout paths remain daemon/runtime state and are
not inferred from remote repository metadata.

Issues backed by a Gongfeng Project resource cannot transition to `done`
without a linked merge request. Agent terminal projection scans only comments
written by that task, parses canonical Gongfeng MR URLs and records the PR
mirror plus Issue link inside the surrounding terminal transaction.

## Merge-request create and link boundary

`multica issue mr create` calls the authenticated Issue endpoint. The backend:

1. validates Issue access, repository path, branches and credential profile;
2. resolves the Gongfeng project API identity;
3. looks for the current open MR with the exact source/target branch pair;
4. creates the remote MR only when none exists;
5. after an ambiguous provider error, queries the same branch pair again to
   recover a remote success whose response was lost;
6. commits the local PR mirror and Issue link in one database transaction.

After the local transaction commits, the handler emits
`pull_request:linked`; React Query invalidates the Issue-scoped PR list for
other open clients. The event is refresh signaling only, never the source of
record.

This is the single create path. Gongfeng does not expose a caller idempotency
key on this API, so provider state is the recovery authority. A retry after a
local database failure reuses the already-open MR and repairs the local link
instead of creating another one. If both create and reconciliation lookup are
unavailable, the endpoint returns a visible provider error; it does not claim
success. The operator can retry after Gongfeng recovers, and the pre-create
lookup performs the same reconciliation.

`multica issue mr link` is the explicit path for an MR that already exists.
It performs no remote write and atomically upserts the local PR mirror and
Issue link. Listing always reads the local mirror used by Issue completion and
review UI.

## Verification anchors

- Workspace inventory and remote resolution:
  `server/internal/handler/workspace.go`
- Project resource integrity and Gongfeng transport:
  `server/internal/handler/project_resource.go`
- MR create, reconciliation and local link transaction:
  `server/internal/handler/github.go`
- Agent-reported MR terminal projection:
  `server/internal/service/task_complete.go`
- Issue completion gate: `server/internal/handler/issue.go`
- Settings inventory UI:
  `packages/views/settings/components/project-gongfeng-repositories.tsx`
- CLI entry point: `server/cmd/multica/cmd_issue_pull_request.go`
