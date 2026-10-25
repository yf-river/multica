package protocol

// 本文件职责：定义 server / daemon / web 客户端之间 WebSocket 消息的负载
// （payload）结构——任务派发与进度、聊天回合、心跳与挂起动作、daemon↔server
// 的 RPC 信封等。协议消息外层统一是 Message{Type, Payload}，Type 的取值与语义
// 见 events.go。

import (
	"encoding/json"
	"time"
)

// daemon 与 app 客户端用来宣告「我支持哪些能力」的字符串常量。server / daemon
// 靠它们做协议协商：daemon 报 capability、server 报 server_capabilities，
// 双方都只启用对方声明支持的特性。
const (
	DaemonCapabilitySkillAssetsV1       = "skill-assets-v1"
	DaemonCapabilitySkillBundlesV1      = "skill-bundles-v1"
	DaemonCapabilityCoalescedCommentsV1 = "coalesced-comments-v1"
	DaemonCapabilityExecutionManifestV1 = "execution-manifest-v1"
	DaemonCapabilityAgentSkillV1        = "agent-skill-v1"
	DaemonCapabilityRemoteMCPV1         = "remote-mcp-v1"
	// DaemonCapabilityLocalWorktreeV1 宣告 daemon 实现了 local_directory 资源的
	// worktree 模式（execution_mode=worktree）。
	//
	// 刻意做成 CAPABILITY 而非版本号检查。判错它的失败形态不是缺字段——
	// 一个没实现该功能的 daemon 会在 json 解析时跳过 execution_mode，
	// 然后在用户要求隔离的工作副本「就地」跑任务。版本字符串回答不了这个：
	// git-describe 的开发构建（"v0.4.21-24-g…"）被刻意豁免于版本下限，
	// 让 `make daemon` 不被卡住——结果恰恰放进来过这样的 daemon（MUL-5707）。
	// 实现了该模式的 daemon 自己说「我会」；没实现的，说不出来。
	DaemonCapabilityLocalWorktreeV1 = "local-worktree-v1"
	// DaemonCapabilitySourceContextQuickCreateV1 宣告支持「两段式 quick-create
	// 提示」，把新指令与不可变的历史源上下文分开携带。
	DaemonCapabilitySourceContextQuickCreateV1 = "source_context_quick_create_v1"

	// DaemonCapabilityRPCV1 宣告 daemon 能在 WebSocket 控制连接上承载
	// request/response RPC（MUL-4257）。开关式能力：只有双方都支持时，
	// 认领任务才走 WS；其余情况继续走 HTTP claim 端点。
	DaemonCapabilityRPCV1 = "rpc-v1"
	// 新会话仅在 checkout 保留已有工作时复用父执行目录。
	DaemonCapabilityCheckoutKeepsWorkV1 = "checkout-keeps-work-v1"

	// AppCapabilityChatDraftRestoreV1 由理解「持久化草稿恢复」路径的 app
	// 客户端用 X-Client-Capabilities 宣告：chat:cancel_finalized 作为失效提示，
	// 加草稿恢复端点。取消一个「已开始但空」的聊天任务时，空/非空判断被推迟
	// （#5219），所以它的取消响应不带同步恢复——没有这个能力的客户端会静默
	// 丢掉用户的提示，因此继续用旧的同步恢复路径。
	AppCapabilityChatDraftRestoreV1 = "chat-draft-restore-v1"
)

// ChatQuickAction 是一条服务端校验过的「后续动作」，挂到某条 assistant 回复上。
// Label 是简洁的胶囊文案；Prompt 是完整的下一轮用户发言。
type ChatQuickAction struct {
	Label   string `json:"label"`
	Prompt  string `json:"prompt"`
	Primary bool   `json:"primary,omitempty"`
}

// RPCRequestPayload 是 daemon→server 的通用请求信封，装进
// EventDaemonRPCRequest 类型消息的 payload。RequestID 用于把响应和请求配对；
// Method 选择 server 侧处理器（例如 "tasks.claim"）；Body 是该方法专属的
// 请求 JSON。
type RPCRequestPayload struct {
	RequestID string          `json:"request_id"`
	Method    string          `json:"method"`
	Body      json.RawMessage `json:"body,omitempty"`
	// TimeoutMs 是 server 侧的执行预算（毫秒）。server 用它约束 handler 的
	// context，让慢 RPC 被取消（其工作回滚），而不是在 daemon 已等超时并
	// 回落到 HTTP 之后才提交（MUL-4257）。0 表示不设 server 侧上限
	// （只受连接生命周期约束）。
	TimeoutMs int64 `json:"timeout_ms,omitempty"`
}

// RPCResponsePayload 是 server→daemon 的应答，装进 EventDaemonRPCResponse
// 类型消息。RequestID 回显请求。Status 镜像 HTTP 状态码，让 daemon 能统一
// 处理 WS 与 HTTP 两种结果。Body / Error 二选一有意义：成功（2xx）时是 Body，
// 失败时是 Error。
type RPCResponsePayload struct {
	RequestID string          `json:"request_id"`
	Status    int             `json:"status"`
	Body      json.RawMessage `json:"body,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// Message 是所有 WebSocket 消息的外层信封。
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// TaskDispatchPayload 是任务被指派时 server 发给 daemon 的消息。
type TaskDispatchPayload struct {
	TaskID      string `json:"task_id"`
	IssueID     string `json:"issue_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// TaskAvailablePayload 是 server 发给 daemon 的唤醒提示。daemon 仍然通过既有
// HTTP claim 端点认领工作。
type TaskAvailablePayload struct {
	RuntimeID string `json:"runtime_id"`
	TaskID    string `json:"task_id,omitempty"`
}

// RuntimeProfilesChangedPayload 是 workspace 的自定义 runtime profile 被创建、
// 编辑、禁用或删除时，server 发给 daemon 的唤醒提示。daemon 仍然通过既有
// HTTP 端点拉取 profile、注册 runtime。
type RuntimeProfilesChangedPayload struct {
	WorkspaceID      string `json:"workspace_id"`
	RuntimeProfileID string `json:"runtime_profile_id,omitempty"`
}

// WorkspacesChangedPayload 是一个账号维度的提示，请 daemon 校准自己的
// workspace 成员集合。server 始终是权威方；事件里不嵌任何 workspace 数据。
type WorkspacesChangedPayload struct{}

// PendingWorkPayload.Kind 携带的取值。kind 仅供参考——daemon 对每种 kind 的
// 反应都一样（一次立即心跳，把队列里排着的活认走）——所以较新 server 发出的
// 未知值在老 daemon 上也安全。
const (
	PendingWorkKindModelList        = "model_list"
	PendingWorkKindLocalSkills      = "local_skills"
	PendingWorkKindLocalSkillImport = "local_skill_import"
)

// PendingWorkPayload 是某 runtime 有「由心跳携带的请求」入队时，server 发给
// daemon 的唤醒提示。daemon 的响应方式是立刻为 RuntimeID 发一次心跳，而不是
// 等下一个调度 tick；请求本身仍走常规心跳路径被认领，所以这个事件不携带工作
// 内容，丢了、重复、被忽略都无害（MUL-5444）。
type PendingWorkPayload struct {
	RuntimeID string `json:"runtime_id"`
	Kind      string `json:"kind,omitempty"`
}

// TaskProgressPayload 是任务执行过程中 daemon 发给 server 的进度消息。
type TaskProgressPayload struct {
	TaskID  string `json:"task_id"`
	Summary string `json:"summary"`
	Step    int    `json:"step,omitempty"`
	Total   int    `json:"total,omitempty"`
}

// TaskCompletedPayload 是任务结束时 daemon 发给 server 的完成消息。
type TaskCompletedPayload struct {
	TaskID string `json:"task_id"`
	PRURL  string `json:"pr_url,omitempty"`
	Output string `json:"output,omitempty"`
}

// ChatQuickActionsPayload 用 daemon 建议扫描产出的「净化后」后续动作，补全一轮
// 已完成的聊天回合。空的 QuickActions 列表是一个有意义的终态——它把挂起的骨架
// 解析为「本回合无建议」。
type ChatQuickActionsPayload struct {
	ChatSessionID string            `json:"chat_session_id"`
	TaskID        string            `json:"task_id"`
	MessageID     string            `json:"message_id"`
	QuickActions  []ChatQuickAction `json:"quick_actions"`
	// Failed 标记：这次补全是为了结束客户端的刷新转圈，因为重新生成失败
	// （provider 阶段或投递阶段），而不是生成了新建议。此时 QuickActions 装的是
	// 本回合未变的药丸；客户端展示「刷新失败」提示，而不是把未变的内容当作
	// 静默成功（MUL-5149）。正常成功路径与自动 best-effort 扫描时为 false（省略）。
	Failed bool `json:"failed,omitempty"`
}

// TaskMessagePayload 表示单条 agent 执行消息（工具调用、文本等）。
type TaskMessagePayload struct {
	ID              string         `json:"id,omitempty"`
	OutputChars     int            `json:"output_chars,omitempty"`
	OutputTruncated bool           `json:"output_truncated,omitempty"`
	TaskID          string         `json:"task_id"`
	IssueID         string         `json:"issue_id,omitempty"`
	Seq             int            `json:"seq"`
	Type            string         `json:"type"`              // "text"、"tool_use"、"tool_result"、"error"
	Tool            string         `json:"tool,omitempty"`    // tool_use/tool_result 时的工具名
	Content         string         `json:"content,omitempty"` // 文本内容
	Input           map[string]any `json:"input,omitempty"`   // 工具入参（仅 tool_use）
	Output          string         `json:"output,omitempty"`  // 工具输出（仅 tool_result）
	CreatedAt       string         `json:"created_at,omitempty"`
	CallID          string         `json:"call_id,omitempty"` // 单次执行内的工具调用标识
}

// DaemonRegisterPayload 是 daemon 连接建立时发给 server 的注册消息。
type DaemonRegisterPayload struct {
	DaemonID string        `json:"daemon_id"`
	AgentID  string        `json:"agent_id"`
	Runtimes []RuntimeInfo `json:"runtimes"`
}

// RuntimeInfo 描述 daemon 机器上某个可用的 agent runtime。
type RuntimeInfo struct {
	Type    string `json:"type"`
	Version string `json:"version"`
	Status  string `json:"status"`
}

// ChatMessagePayload 是新聊天消息创建时广播的负载。
type ChatMessagePayload struct {
	ChatSessionID string `json:"chat_session_id"`
	MessageID     string `json:"message_id"`
	Role          string `json:"role"`
	Content       string `json:"content"`
	TaskID        string `json:"task_id,omitempty"`
	CreatedAt     string `json:"created_at"`
}

// 聊天消息的种类（chat_message.message_kind）。只增不改：老读者遇到未知值会
// 退化当成 ChatMessageKindMessage。
const (
	// ChatMessageKindMessage 是普通的用户/assistant 消息。
	ChatMessageKindMessage = "message"
	// ChatMessageKindNoResponse 标记一次「agent 完成了直接聊天回合却没有任何
	// 文字回复」——这是可见、刻意的终态，而不是被静默丢掉的回合（MUL-4351）。
	ChatMessageKindNoResponse = "no_response"
	// ChatMessageKindOnboardingKickoff 是服务端撰写的、隐藏的第一轮发言，
	// 用来启动 Mika 的 onboarding 对话。它会被持久化，让 runtime 收到一份
	// 正常的不可变聊天输入批次。面向用户的 API 会滤掉它；客户端也会防御性地
	// 忽略该 kind。
	ChatMessageKindOnboardingKickoff = "onboarding_kickoff"
	// ChatMessageKindOnboardingOpening 标记 onboarding kickoff 产出的 assistant
	// 回复。kickoff 行本身永远不会到客户端，所以 opening 自我描述：聊天界面
	// 在这个 kind 下渲染「引导卡片」，而不是快捷动作胶囊（MUL-5765）。
	ChatMessageKindOnboardingOpening = "onboarding_opening"
)

// ChatDonePayload 在 agent 回复完某条聊天消息后广播。它带上刚持久化的
// assistant ChatMessage，客户端可以就地写进消息缓存——省掉一次重新拉取，
// 避免 live-timeline → AssistantMessage 交接时曾出现的可见闪烁（#2123）。
//
// MessageKind 只增不改（MUL-4351）：老客户端忽略它，回落到 server 总会发的
// 非空 Content，于是 no_response 回合仍渲染出一个真实气泡而非空气泡。因为
// 直接聊天的完成现在总是恰好写一条 assistant 行（message 或 no_response），
// MessageID/Content/CreatedAt/ElapsedMs 在直接聊天里总是有值——omitempty
// 只会在「没有行的旧路径」上省掉字段。
type ChatDonePayload struct {
	ChatSessionID string            `json:"chat_session_id"`
	TaskID        string            `json:"task_id"`
	MessageID     string            `json:"message_id,omitempty"`
	Content       string            `json:"content,omitempty"`
	ElapsedMs     int64             `json:"elapsed_ms,omitempty"`
	CreatedAt     string            `json:"created_at,omitempty"`
	MessageKind   string            `json:"message_kind,omitempty"`
	QuickActions  []ChatQuickAction `json:"quick_actions,omitempty"`
	// QuickActionsPending 告诉客户端本回合稍后会来一条 chat:quick_actions 补全
	// （先渲染占位）。当 QuickActions 已填好时绝不会为 true。
	QuickActionsPending bool `json:"quick_actions_pending,omitempty"`
}

// ChatCancelFinalizedPayload.Outcome 的取值。
const (
	// ChatCancelOutcomeStopped：transcript 结果非空，于是持久化了一条
	// "Stopped." assistant 消息。
	ChatCancelOutcomeStopped = "stopped"
	// ChatCancelOutcomeRestored：transcript 始终为空，于是触发它的用户消息被
	// 删除，其内容应作为草稿恢复到输入框。
	ChatCancelOutcomeRestored = "restored"
)

// ChatCancelFinalizedPayload 在一个被取消聊天任务的「推迟收尾」落定时广播
// （#5219）。取消的 HTTP 响应带不了这个结果——要等 daemon 冲掉 transcript
// 才知道——所以客户端靠这个事件反应：outcome "stopped" 插入那条 assistant
// 消息（MessageID/Content/... 描述新行，形状同 ChatDonePayload）；outcome
// "restored" 把被删的用户消息从缓存移除，并提示发起方客户端去「仅创建者有权」
// 的端点取持久化草稿恢复。恢复出来的提示内容与附件刻意永不搭这条
// workspace 级广播走。
type ChatCancelFinalizedPayload struct {
	Outcome       string `json:"outcome"`
	ChatSessionID string `json:"chat_session_id"`
	TaskID        string `json:"task_id"`
	// InitiatorUserID 是触发被取消任务的人类。只有这个用户的客户端需要去取
	// 草稿恢复（那个端点本就只给创建者授权）；客户端把缺值当成「不是我」。
	InitiatorUserID string `json:"initiator_user_id,omitempty"`
	MessageID       string `json:"message_id,omitempty"`
	// Content/MessageKind/CreatedAt/ElapsedMs 描述持久化的 "Stopped."
	// assistant 行，只在 outcome 为 "stopped" 时设置——暴露面与 chat:done 相同。
	Content     string `json:"content,omitempty"`
	MessageKind string `json:"message_kind,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	ElapsedMs   int64  `json:"elapsed_ms,omitempty"`
}

// ChatSessionReadPayload 在创建者把某会话标记为已读时广播。
// 发给其它设备，让它们的未读数保持同步。
type ChatSessionReadPayload struct {
	ChatSessionID string `json:"chat_session_id"`
}

// ChatSessionCreatedPayload 在新聊天会话创建时广播（包括来源渠道与路由信息）。
type ChatSessionCreatedPayload struct {
	WorkspaceID           string                   `json:"workspace_id"`
	ChatSessionID         string                   `json:"chat_session_id"`
	AgentID               string                   `json:"agent_id"`
	CreatorID             string                   `json:"creator_id"`
	Title                 string                   `json:"title"`
	ChannelSource         ChatSessionChannelSource `json:"channel_source"`
	IsCurrentChannelRoute bool                     `json:"is_current_channel_route"`
}

// ChatSessionChannelSource 描述该聊天会话经由哪条渠道路由而来（用于渠道级会话
// 路由判断与展示）。
type ChatSessionChannelSource struct {
	ChannelType    string `json:"channel_type"`
	InstallationID string `json:"installation_id"`
	RouteRevision  int64  `json:"route_revision"`
}

// ChatSessionDeletedPayload 在聊天会话被硬删除时广播，让其它标签页/设备把它
// 从会话列表移除；若活跃指针正指向被删会话，则重置它。
type ChatSessionDeletedPayload struct {
	ChatSessionID string `json:"chat_session_id"`
}

// ChatSessionUpdatedPayload 在聊天会话的某个用户可编辑字段变化时广播
// （目前：标题的行内重命名）。其它标签页/设备在自己缓存里的会话行上打补丁，
// 让下拉列表同步，而无需整表重拉。
type ChatSessionUpdatedPayload struct {
	ChatSessionID string `json:"chat_session_id"`
	Title         string `json:"title"`
	// ProjectID 只由「项目上下文更新」路径设置。双重指针区分「字段省略」与
	// 「显式 JSON null」。
	ProjectID **string `json:"project_id,omitempty"`
	// Pinned 只由钉/解钉路径设置；普通重命名时为 nil，让接收方保持现有钉状态。
	Pinned *bool `json:"pinned,omitempty"`
	// Status 只由归档/取消归档路径设置（"active"/"archived"）；重命名/钉时
	// 为 nil，让接收方保持现有状态。
	Status    *string `json:"status,omitempty"`
	UpdatedAt string  `json:"updated_at"`
}

// DaemonHeartbeatRequestPayload 是 daemon 经 WebSocket 发给 server 的心跳，
// 用于更新 last_seen_at 并为单个 runtime 拉取挂起动作。镜像 POST
// /api/daemon/heartbeat 的请求体，让两种传输共享相同语义。
type DaemonHeartbeatRequestPayload struct {
	RuntimeID           string `json:"runtime_id"`
	SupportsBatchImport bool   `json:"supports_batch_import,omitempty"`
}

// DaemonHeartbeatAckPayload 是 server 对 DaemonHeartbeatRequestPayload 的应答。
// JSON 形状镜像 HTTP 心跳响应，daemon 代码两种都能解。ServerCapabilities 是
// 显式的 server→daemon 协议协商——daemon 绝不能从自己宣告的客户端能力反推
// server 支持什么。
//
// RuntimeGone 是 HTTP 404「runtime not found」在 WebSocket 上的替代。当 server
// 发现 runtime 行已被删除（UI 删除、7 天离线 GC）时，它回一个
// Status=HeartbeatStatusRuntimeGone、RuntimeGone=true 的 ack，而不是带着错误
// 拆掉连接。daemon 读到该信号后，从本地状态清除过期 runtime 并重新注册；
// 没有它，死 UUID 会一直心跳到 daemon 进程重启为止。
type DaemonHeartbeatPendingMcpCheck struct {
	ID     string          `json:"id"`
	Config json.RawMessage `json:"config"`
}

type DaemonHeartbeatAckPayload struct {
	// 1. A workspace-scoped request names one historical run; it grants no path or shell access.
	PendingExecutionEvidence *DaemonExecutionEvidenceRequest         `json:"pending_execution_evidence,omitempty"`
	PendingMcpCheck          *DaemonHeartbeatPendingMcpCheck         `json:"pending_mcp_check,omitempty"`
	RuntimeID                string                                  `json:"runtime_id"`
	Status                   string                                  `json:"status"`
	ServerCapabilities       []string                                `json:"server_capabilities,omitempty"`
	RuntimeGone              bool                                    `json:"runtime_gone,omitempty"`
	PendingUpdate            *DaemonHeartbeatPendingUpdate           `json:"pending_update,omitempty"`
	PendingModelList         *DaemonHeartbeatPendingModelList        `json:"pending_model_list,omitempty"`
	PendingLocalSkills       *DaemonHeartbeatPendingLocalSkills      `json:"pending_local_skills,omitempty"`
	PendingLocalSkillImport  *DaemonHeartbeatPendingLocalSkillImport `json:"pending_local_skill_import,omitempty"`
	// PendingLocalSkillImports 在同一次心跳里携带多条导入请求，让 daemon 可以
	// 并发处理。不认识该字段的老 daemon 会按标准 JSON 行为静默忽略它，
	// 回落到上面单数的 PendingLocalSkillImport。
	PendingLocalSkillImports []DaemonHeartbeatPendingLocalSkillImport `json:"pending_local_skill_imports,omitempty"`
}

type DaemonExecutionEvidenceRequest struct {
	MaterialID  string    `json:"material_id"`
	NextSeq     int32     `json:"next_seq"`
	TaskID      string    `json:"task_id"`
	WorkspaceID string    `json:"workspace_id"`
	Provider    string    `json:"provider"`
	SessionID   string    `json:"session_id"`
	CID         string    `json:"provider_trace_id"`
	Cwd         string    `json:"cwd"`
	Started     time.Time `json:"started_at"`
	Ended       time.Time `json:"ended_at"`
}

// HeartbeatStatusRuntimeGone 是 ack 在 runtime 行已从 server 侧消失时使用的
// Status 值。与 DaemonHeartbeatAckPayload.RuntimeGone 配套。
const HeartbeatStatusRuntimeGone = "runtime_gone"

// DaemonHeartbeatPendingUpdate 描述 daemon 应为该 runtime 执行的一次 CLI 更新。
type DaemonHeartbeatPendingUpdate struct {
	ID            string `json:"id"`
	TargetVersion string `json:"target_version"`
}

// DaemonHeartbeatPendingModelList 描述一个让 daemon 枚举 runtime 支持模型的请求。
type DaemonHeartbeatPendingModelList struct {
	ID string `json:"id"`
}

// DaemonHeartbeatPendingLocalSkills 描述一个请求 runtime 本地 skill 清单的请求。
type DaemonHeartbeatPendingLocalSkills struct {
	ID string `json:"id"`
}

// DaemonHeartbeatPendingLocalSkillImport 描述一个导入某条具体 runtime 本地
// skill 的请求。
type DaemonHeartbeatPendingLocalSkillImport struct {
	ID       string `json:"id"`
	SkillKey string `json:"skill_key"`
}
