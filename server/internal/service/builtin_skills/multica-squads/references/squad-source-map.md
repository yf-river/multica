# Squads source map

Current evidence for multica-squads.

## Verification

    rg -n "func .*Squad|func .*squad" server/internal/handler/squad*.go
    rg -n "SquadLeader|squad" server/internal/handler/comment_triggers.go server/internal/service/task_enqueue.go
    go test ./internal/service -run "TestSquadsSkill|TestBuiltinSkillsConformToTemplate"
    go test ./internal/handler -run "Test.*Squad|Test.*ChildDone.*Squad"

## Data and CLI

| Contract | Current source |
|---|---|
| Squad and member schema | server/migrations/001_current_schema.up.sql; server/pkg/db/queries/squad.sql |
| Core squad type | packages/core/types/squad.ts |
| CLI commands and flags | server/cmd/multica/cmd_squad.go |

Squad members are member or agent rows. Issue assignee_type supports squad and
execution is routed through the current leader_id.

## Create, update and membership

| Contract | Current source |
|---|---|
| CreateSquad and UpdateSquad | server/internal/handler/squad.go:731,859 |
| Leader workspace validation and automatic leader membership | server/internal/handler/squad.go:760-815,913-945 |
| List/add/remove/role member endpoints | server/internal/handler/squad_members.go |
| Internal template/import surfaces | server/internal/handler/squad_internal.go |

Create/update validate that leader_id resolves to a workspace agent. Archived
leader readiness is enforced on assignment and dispatch rather than by silently
changing the leader field.

## Briefing and task claim

| Contract | Current source |
|---|---|
| Build leader briefing and roster | server/internal/handler/squad_briefing.go:70-168 |
| Claim-time briefing injection and fail-closed response build | server/internal/handler/daemon_tasks.go:251-280,658-696 |
| Archived roster agents skipped; roster read failures propagated | server/internal/handler/squad_briefing.go:108-166 |

Only the leader task receives the squad operating briefing.

## Issue assignment and dispatch

| Contract | Current source |
|---|---|
| Create/update assignee validation | server/internal/handler/issue.go:629,1023; server/internal/handler/issue_validation.go:62 |
| Backlog promotion and assign hooks | server/internal/handler/issue.go:1159-1161; server/internal/handler/issue_update_projection.go:18-32 |
| Readiness decision | server/internal/handler/squad_members.go:560 |
| Leader enqueue and private-agent gate | server/internal/handler/squad_members.go:600; agent_access.go:142 |
| Leader-specific task service method | server/internal/service/task_enqueue.go:94 |

Squad assignment targets one leader task, never all members. Backlog parks the
assignment; promotion to an executable state may enqueue after readiness,
access and pending-task checks.

## Comments and mentions

| Contract | Current source |
|---|---|
| Assigned-squad comment trigger | server/internal/handler/comment_triggers.go:411 |
| Explicit squad mention | server/internal/handler/comment_triggers.go:601-660 |
| Shared leader enqueue | server/internal/handler/comment_triggers.go:113-145 |
| Loop and prior-leader guards | server/internal/handler/squad_members.go:529; comment_triggers.go:411-449 |

## Autopilot and child completion

| Contract | Current source |
|---|---|
| Save-time autopilot assignee validation | server/internal/handler/autopilot_triggers.go:342 |
| Runtime leader resolution and dispatch | server/internal/service/autopilot.go |
| Parent squad trigger on child done | server/internal/handler/issue_child_done.go:246-387 |

Archived squads/leaders fail readiness. Child-done routing has same-squad,
same-leader and shared-leader loop guards.

## Access boundary

| Contract | Current source |
|---|---|
| Personal-agent access | server/internal/handler/agent_access.go:84-140 |
| System/agent leader enqueue access | server/internal/handler/agent_access.go:142-155 |

Workspace leaders pass. Personal leaders require the current owner/admin or
agent/system path allowed by the explicit access helper.
