// Package protocol 定义 server、web 客户端与 daemon 三方之间 WebSocket 通信的
// 协议常量与消息结构：事件类型字符串（events.go）、各事件携带的负载结构
// （messages.go）、以及任务源上下文（source_context.go）。负载经 JSON 序列化，
// 封装在 Message{Type, Payload} 里传输；Type 决定 Payload 怎么解。
package protocol

// 供 server、web 客户端与 daemon 之间 WebSocket 通信的事件类型。
const (
	// Issue 事件
	EventIssueCreated            = "issue:created"
	EventIssueUpdated            = "issue:updated"
	EventIssueDeleted            = "issue:deleted"
	EventIssueMetadataChanged    = "issue_metadata:changed"
	EventIssueAttachmentsChanged = "issue_attachments:changed"

	// Comment 事件
	EventCommentCreated       = "comment:created"
	EventCommentUpdated       = "comment:updated"
	EventCommentDeleted       = "comment:deleted"
	EventCommentResolved      = "comment:resolved"
	EventCommentUnresolved    = "comment:unresolved"
	EventReactionAdded        = "reaction:added"
	EventReactionRemoved      = "reaction:removed"
	EventIssueReactionAdded   = "issue_reaction:added"
	EventIssueReactionRemoved = "issue_reaction:removed"

	// Agent 事件
	EventAgentStatus   = "agent:status"
	EventAgentCreated  = "agent:created"
	EventAgentArchived = "agent:archived"
	EventAgentRestored = "agent:restored"

	// Task 事件（server <-> daemon）。
	// 每个事件对应 agent_task_queue 上的一次状态迁移。前端按 `task:` 前缀订阅，
	// 并让 workspace 任务快照失效重拉，所以这里的粒度是「用户想看什么变化」——
	// 而不是「每次内部状态翻转」。
	EventTaskQueued                = "task:queued"                  // ∅ → queued（入队 / 重试创建）
	EventTaskDeferred              = "task:deferred"                // ∅ → deferred（等密封渠道媒体解析）
	EventTaskDispatch              = "task:dispatch"                // queued → dispatched（daemon 认领）
	EventTaskRunning               = "task:running"                 // dispatched → running（daemon 已启动）
	EventTaskWaitingLocalDirectory = "task:waiting_local_directory" // dispatched → waiting_local_directory（daemon 因 local_directory 路径忙而停车等待）
	EventTaskProgress              = "task:progress"
	EventTaskCompleted             = "task:completed" // running → completed
	EventTaskFailed                = "task:failed"    // running → failed
	EventTaskMessage               = "task:message"
	EventTaskCancelled             = "task:cancelled" // * → cancelled

	// Inbox 事件
	EventInboxNew           = "inbox:new"
	EventInboxRead          = "inbox:read"
	EventInboxUnread        = "inbox:unread"
	EventInboxArchived      = "inbox:archived"
	EventInboxUnarchived    = "inbox:unarchived"
	EventInboxBatchRead     = "inbox:batch-read"
	EventInboxBatchArchived = "inbox:batch-archived"

	// Workspace 事件
	EventWorkspaceUpdated = "workspace:updated"
	EventWorkspaceDeleted = "workspace:deleted"

	// Member 事件
	EventMemberAdded   = "member:added"
	EventMemberUpdated = "member:updated"
	EventMemberRemoved = "member:removed"

	// Subscriber 事件
	EventSubscriberAdded   = "subscriber:added"
	EventSubscriberRemoved = "subscriber:removed"

	// Activity 事件
	EventActivityCreated = "activity:created"

	// Skill 事件
	EventSkillCreated = "skill:created"
	EventSkillUpdated = "skill:updated"
	EventSkillDeleted = "skill:deleted"

	// Chat 事件
	EventChatMessage = "chat:message"
	EventChatDone    = "chat:done"
	// EventChatQuickActions 用 daemon 回合后扫描产出的后续建议补全一个已完成的
	// 回合。在 chat:done 之后发送；不认识它的客户端只是永远不会去解析那个
	// （同样未知的）挂起状态而已。
	EventChatQuickActions = "chat:quick_actions"
	// EventChatCancelFinalized 携带被取消聊天任务的推迟结果，等 daemon 冲掉
	// transcript（或清扫器宽限期耗尽）后发出：要么是一条迟到的 "Stopped."
	// assistant 消息，要么是一次草稿恢复（#5219）。渠道外发（Slack/Lark）
	// 刻意不订阅它——取消在外部渠道上保持沉默。
	EventChatCancelFinalized = "chat:cancel_finalized"
	EventChatSessionCreated  = "chat:session_created"
	EventChatSessionRead     = "chat:session_read"
	EventChatSessionDeleted  = "chat:session_deleted"
	EventChatSessionUpdated  = "chat:session_updated"

	// Project 事件
	EventProjectCreated         = "project:created"
	EventProjectUpdated         = "project:updated"
	EventProjectDeleted         = "project:deleted"
	EventProjectResourceCreated = "project_resource:created"
	EventProjectResourceUpdated = "project_resource:updated"
	EventProjectResourceDeleted = "project_resource:deleted"

	// Label 事件
	EventLabelCreated       = "label:created"
	EventLabelUpdated       = "label:updated"
	EventLabelDeleted       = "label:deleted"
	EventIssueLabelsChanged = "issue_labels:changed"

	// 自定义属性事件。定义只会归档、从不删除，所以没有 property:deleted——
	// 归档以 property:updated 的形式到达。
	EventPropertyCreated        = "property:created"
	EventPropertyUpdated        = "property:updated"
	EventIssuePropertiesChanged = "issue_properties:changed"

	// Issue 状态目录事件（MUL-6243）。对全部四种写入——创建、编辑、归档、
	// 重排——只发这一个事件，而不是每种写入一个动词，因为该目录是整表读取、
	// 且所有客户端对四种写入的回应都一样：重新读一遍。拆成四个只会造出四个
	// 没有任何消费者能区分的事件，而且重排本来也没有单行可点名。payload 里的
	// `action` 仅供参考（让 devtools 里的帧能自描述）；没有任何逻辑依赖它。
	EventIssueStatusChanged = "issue_status:changed"

	// Pin 事件
	EventPinCreated   = "pin:created"
	EventPinDeleted   = "pin:deleted"
	EventPinReordered = "pin:reordered"

	// Invitation 事件
	EventInvitationCreated  = "invitation:created"
	EventInvitationAccepted = "invitation:accepted"
	EventInvitationDeclined = "invitation:declined"
	EventInvitationRevoked  = "invitation:revoked"

	// Autopilot 事件
	EventAutopilotCreated  = "autopilot:created"
	EventAutopilotUpdated  = "autopilot:updated"
	EventAutopilotDeleted  = "autopilot:deleted"
	EventAutopilotRunStart = "autopilot:run_start"
	EventAutopilotRunDone  = "autopilot:run_done"

	// Squad 事件
	EventSquadCreated = "squad:created"
	EventSquadUpdated = "squad:updated"
	EventSquadDeleted = "squad:deleted"

	// Daemon events
	EventDaemonHeartbeat               = "daemon:heartbeat"
	EventDaemonHeartbeatAck            = "daemon:heartbeat_ack"
	EventDaemonRegister                = "daemon:register"
	EventDaemonTaskAvailable           = "daemon:task_available"
	EventDaemonTaskSupplementAvailable = "daemon:task_supplement_available"
	EventDaemonRuntimeProfilesChanged  = "daemon:runtime_profiles_changed"
	EventDaemonWorkspacesChanged       = "daemon:workspaces_changed"
	// EventDaemonPendingWork is a runtime-scoped hint that a heartbeat-carried
	// request (model discovery, capability discovery, or local-skill import) is
	// queued for that runtime. Without it the daemon only learns about the
	// request on its next scheduled heartbeat, adding up to one HeartbeatInterval
	// (15s by default) of dead wait to an interactive UI flow (MUL-5444). The
	// hint carries no work
	// itself: the daemon still pulls the request through the normal heartbeat
	// claim, so a lost or duplicated hint is harmless.
	EventDaemonPendingWork = "daemon:pending_work"
	// 经 WebSocket 控制连接的通用 daemon→server request/response（MUL-4257）。
	// daemon 发 EventDaemonRPCRequest（带关联 id + method + body），server 用
	// EventDaemonRPCResponse（同 request id）应答。这是「WS 优先认领任务
	// （HTTP 兜底）」以及将来任何 daemon→server RPC 的传输层。
	EventDaemonRPCRequest  = "daemon:rpc_request"
	EventDaemonRPCResponse = "daemon:rpc_response"

	// GitHub 集成事件
	EventGitHubInstallationCreated = "github_installation:created"
	EventGitHubInstallationDeleted = "github_installation:deleted"
	EventPullRequestLinked         = "pull_request:linked"
	EventPullRequestUpdated        = "pull_request:updated"
	EventPullRequestUnlinked       = "pull_request:unlinked"

	// VCS 集成事件（Forgejo / Gitea / GitLab）
	EventVCSConnectionCreated = "vcs_connection:created"
	EventVCSConnectionDeleted = "vcs_connection:deleted"

	// Lark 集成事件。`created` 同时覆盖首次安装（(workspace_id, agent_id) 上的
	// UNIQUE 意味着每个 agent 至多一行）与经 UpsertLarkInstallation 的重新安装
	// ——前端把两者都当作同一条「安装出现/刷新了」的通知。`revoked` 把状态翻成
	// 'revoked' 而不删行；审计轨迹得以保留。
	EventLarkInstallationCreated = "lark_installation:created"
	EventLarkInstallationRevoked = "lark_installation:revoked"

	// Slack 安装生命周期（MUL-3666）。语义与 Lark 事件相同：`created` 同时覆盖
	// 首次安装与 OAuth 重装（(workspace_id, agent_id, channel_type) 上的 UNIQUE
	// 意味着每个 agent 至多一行），`revoked` 翻状态不删行。前端对两者都会让
	// Slack installations 查询失效。
	EventSlackInstallationCreated = "slack_installation:created"
	EventSlackInstallationRevoked = "slack_installation:revoked"

	// DingTalk 安装生命周期沿用与 Slack BYO 渠道安装相同的 create/revoke 语义。
	// BindingUpdated 共享 dingtalk_installation 前缀，因为它改变的是安装列表
	// 返回的、按成员作用域的账号标识。
	EventDingTalkInstallationCreated   = "dingtalk_installation:created"
	EventDingTalkInstallationRevoked   = "dingtalk_installation:revoked"
	EventDingTalkAccountBindingUpdated = "dingtalk_installation:binding_updated"

	// WeCom 智能机器人安装生命周期。语义同 Lark / Slack：`created` 同时覆盖
	// 首次安装与经 UpsertChannelInstallation 的重装（(workspace_id, agent_id,
	// channel_type) 上的 UNIQUE 意味着每个 agent 至多一行）；`revoked` 翻状态
	// 不删行。前端对两者都会让 wecom installations 查询失效。
	EventWecomInstallationCreated = "wecom_installation:created"
	EventWecomInstallationRevoked = "wecom_installation:revoked"

	// Telegram 安装生命周期。契约同 Slack 那对：前端对两者都会让 Telegram
	// installations 查询失效。
	EventTelegramInstallationCreated = "telegram_installation:created"
	EventTelegramInstallationRevoked = "telegram_installation:revoked"

	// Life 事件描述对个人助手的「受管辖状态」的一次已提交变更。payload 指明
	// 实体与动作；流键保持单个用户的 Life 时间线有序，而 outbox 回执在发起
	// 事务已经改完行之后关闭该事件。
	EventLifeChanged = "life:changed"
)
