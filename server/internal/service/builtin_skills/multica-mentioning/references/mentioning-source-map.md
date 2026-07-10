# Mentioning source map

Evidence for the current mention grammar and comment-trigger behavior.

## Verification

    rg -n "MentionRe|func ParseMentions|func HasMentionAll" server/internal/util/mention.go
    rg -n "func .*Comment.*Trigger|func .*compute.*CommentTrigger" server/internal/handler/comment_*.go
    go test ./internal/service -run "TestMentioningSkill|TestBuiltinSkillsConformToTemplate"
    go test ./internal/handler -run "Test.*CommentTrigger|Test.*Mention"

## Grammar

| Contract | Current source |
|---|---|
| Single mention recognizer and member/agent/squad/issue/all grammar | server/internal/util/mention.go:16 |
| Parse and deduplicate type/id pairs | server/internal/util/mention.go:24-37 |
| Detect broadcast all | server/internal/util/mention.go:40-47 |
| Parser examples and invalid-name behavior | server/internal/util/mention_test.go |

The target id is a UUID-shaped hex/dash value, except all/all. A display name
in the id position is not a valid mention.

## Request and preview boundary

| Contract | Current source |
|---|---|
| Create/preview request and preview response types | server/internal/handler/comment_list.go:608-633 |
| Preview handler and editing-comment exclusion | server/internal/handler/comment_list.go:685-729 |
| Create and update handlers | server/internal/handler/comment.go:175,344 |
| Create/edit trigger invocation | server/internal/handler/comment.go:300-315,476-493 |
| suppress_agent_ids filtering | server/internal/handler/comment_triggers.go:36-46,90-111 |

## Trigger computation

| Contract | Current source |
|---|---|
| Shared enqueue entry and service-comment bridge | server/internal/handler/comment_triggers.go:36,48 |
| Enqueue helper | server/internal/handler/comment_triggers.go:113 |
| Full trigger computation | server/internal/handler/comment_triggers.go:373 |
| Assigned squad leader branch | server/internal/handler/comment_triggers.go:411 |
| Pending-task dedup with edit exclusion | server/internal/handler/comment_triggers.go:452 |
| Broadcast suppresses ordinary assignee wake-up | server/internal/handler/comment_triggers.go:472 |
| Explicit squad/agent mentions | server/internal/handler/comment_triggers.go:601-686 |
| member, issue and all do not enqueue an agent | server/internal/handler/comment_triggers.go:601-686 |
| Agent and squad enqueue service methods | server/internal/service/task_enqueue.go:81-101 |

Squad mentions target the current squad leader only. Direct agent mentions
target that agent. Archived/no-runtime/inaccessible/pending targets are skipped
by the guards in comment_triggers.go:622-680.

## Agent-authored comments

| Contract | Current source |
|---|---|
| TaskService comment callback contract | server/internal/service/task.go:38-41 |
| Post-commit agent comment projection invokes the callback | server/internal/service/task.go:485-498 |
| Handler wires callback to the shared trigger path | server/internal/handler/handler.go:210; comment_triggers.go:48 |

## CLI id sources

| Mention type | Current id source |
|---|---|
| member | workspace member list user_id, server/cmd/multica/cmd_workspace.go |
| agent | agent list id, server/cmd/multica/cmd_agent.go |
| squad | squad list id, server/cmd/multica/cmd_squad.go |
| formatted roster mention | server/internal/handler/squad_briefing.go:189-218 |

There is no member-notification delivery claim in this skill. The verified Go
path only turns agent and squad mentions into runs.
