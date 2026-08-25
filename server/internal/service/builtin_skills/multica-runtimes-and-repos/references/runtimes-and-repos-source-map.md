# Runtimes and repos source map

Current evidence for runtime lifecycle, task claim and repository checkout.

## CLI and server routes

- server/cmd/multica/cmd_runtime.go registers runtime list, usage, activity
  and delete. Cascade delete reads the active-agent conflict and calls the
  archive-agents-and-delete endpoint.
- server/cmd/multica/cmd_repo.go registers repo checkout. It requires the
  daemon port, sends workspace/workdir/ref/agent/task context to the local
  daemon and prints the checked-out path.
- server/cmd/server/router.go registers daemon runtime, workspace repo and
  task-claim APIs under /api/daemon.

## Daemon runtime boundary

| Responsibility | Current source |
|---|---|
| Daemon state and shared dependencies | server/internal/daemon/daemon.go |
| Startup, auth, registration, runtime recovery and repo/profile sync | server/internal/daemon/register.go |
| Workspace sync, heartbeat, polling and cancellation loops | server/internal/daemon/loops.go |
| Claim handling, execution-space preparation and result reporting | server/internal/daemon/task_exec.go |
| Provider run orchestration | server/internal/daemon/task_run.go |
| Streaming/tool activity and idle watchdog | server/internal/daemon/task_stream.go |
| Task/project/repo execution environment | server/internal/daemon/execenv/runtime_config.go |

Key symbol anchors:

    rg -n "func .*Run|func .*registerRuntimesForWorkspace" server/internal/daemon/register.go
    rg -n "func .*pollLoop|func .*heartbeatLoop" server/internal/daemon/loops.go
    rg -n "func .*handleTask|func .*prepareIssueExecutionSpace" server/internal/daemon/task_exec.go
    rg -n "func .*runTask" server/internal/daemon/task_run.go

## Verification

    go test ./internal/service -run "TestRuntimesAndReposSkill|TestBuiltinSkillsConformToTemplate"
    go test ./internal/daemon
