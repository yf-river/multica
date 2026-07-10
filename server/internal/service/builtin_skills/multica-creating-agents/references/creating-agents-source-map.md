# Creating agents source map

Evidence for the current multica-creating-agents contract. Re-derive line
numbers after moves; symbol names and tests are the stable anchors.

## Verification

    rg -n "func runAgentCreate|func runAgentUpdate" server/cmd/multica/cmd_agent.go
    rg -n "func .*CreateAgent|func .*UpdateAgent" server/internal/handler/agent.go
    rg -n "LoadAgentSkills|TaskAgentData" server/internal/handler/daemon_tasks.go
    go test ./internal/service -run "TestCreatingAgentsSkill|TestBuiltinSkillsConformToTemplate"

## CLI boundary

| Contract | Current source |
|---|---|
| Create flags: identity/runtime/model/thinking/args/scope/concurrency | server/cmd/multica/cmd_agent.go:159-175 |
| Secret-safe env and MCP stdin/file channels | server/cmd/multica/cmd_agent.go:167-172 |
| Update MCP and thinking flags | server/cmd/multica/cmd_agent.go:178-200 |
| Create request assembly and POST | server/cmd/multica/cmd_agent.go:425 |
| Update request assembly and PUT | server/cmd/multica/cmd_agent.go:459 |
| MCP object/null parser and channel resolver | server/cmd/multica/cmd_agent.go:1047,1075 |
| Skill list/set/add | server/cmd/multica/cmd_agent.go:726,749,774 |
| Env get/set | server/cmd/multica/cmd_agent.go:855,890 |

The supported CLI creation path is agent create. Agent template handlers remain
a separate server surface and are not taught by this skill.

## Create and update handler

| Contract | Current source |
|---|---|
| Response redacts custom_env and MCP secrets | server/internal/handler/agent.go:79-135 |
| CreateAgentRequest current fields | server/internal/handler/agent.go:729 |
| CreateAgent boundary | server/internal/handler/agent.go:766 |
| Required name/runtime, description cap, runtime access | server/internal/handler/agent.go:781-832 |
| Provider thinking-level validation | server/internal/handler/agent.go:834-841 |
| Current defaults and database insert | server/internal/handler/agent.go:851-894 |
| UpdateAgent boundary | server/internal/handler/agent.go:1061 |
| custom_env rejected in generic update; use env endpoint | server/internal/handler/agent.go:1078-1088 |
| MCP tri-state update/clear | server/internal/handler/agent.go:1122-1126,1282-1292 |

## Env authorization

| Contract | Current source |
|---|---|
| Shared authorizeAgentEnv gate | server/internal/handler/agent_env.go:66 |
| Agent actors denied; owner/admin required | server/internal/handler/agent_env.go:80-91 |
| GET and PUT handlers | server/internal/handler/agent_env.go:106,154 |
| Routes | server/cmd/server/router.go:1005-1006 |

## Claim-time runtime payload

| Contract | Current source |
|---|---|
| Claim handler | server/internal/handler/daemon_tasks.go:21 |
| Fresh agent read, workspace skills and filtered built-ins | server/internal/handler/daemon_tasks.go:71-80 |
| TaskAgentData carries instructions, env, args, model, thinking and MCP | server/internal/handler/daemon_tasks.go:105-116 |
| Workspace skill loading | server/internal/service/task_fail.go:541-562 |
| Embedded built-in skill loader | server/internal/service/builtin_skills.go:10-71 |

## Persistence

| Contract | Current source |
|---|---|
| Agent create/update SQL and generated params | server/pkg/db/queries/agent.sql; server/pkg/db/generated/agent.sql.go |
| Env-only write path | server/pkg/db/queries/agent.sql, UpdateAgentCustomEnv |

Description, scope and max_concurrent_tasks are management/scheduling fields;
the claim payload only carries fields consumed by the runtime.
