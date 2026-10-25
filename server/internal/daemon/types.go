package daemon

import (
	"encoding/json"

	"github.com/multica-ai/multica/server/internal/codediff"
	"github.com/multica-ai/multica/server/internal/runtimeapps"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

// AgentEntry 描述一个本机可用 agent CLI 的条目。
type AgentEntry struct {
	Path string // 启动时解析出的稳定 CLI 入口路径；真正拉起时可能沿平台链接追到具体可执行文件
	// Command 是启动时 Path 所解析自的命令名或 MULTICA_*_PATH 环境变量值。
	// 保留它的目的是：当被固定的可执行文件后来消失时（比如版本管理器
	// Homebrew Cask、nvm/fnm 原地升级会删掉 Path 指向的旧版本目录），
	// daemon 还能据此重新解析 Path。对于直接携带绝对路径的合成条目
	// （自定义 runtime profile），此字段为空。参见 Daemon.resolveAgentEntry
	// 与 MUL-4486。
	Command string
	Model   string // 可选的模型覆盖
}

// Runtime 表示一个已注册的 daemon runtime。
type Runtime struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
	// ProfileID 当该 runtime 是从 workspace 自定义 runtime profile 注册而来时非空
	// （MUL-3284）。它把 runtime 记录与 profile 关联起来，使 daemon 能把 profile
	// 里的 command_name 解析成要拉起的可执行文件。内置的（按 provider 探测到的）
	// runtime 此字段为空。
	ProfileID string `json:"profile_id,omitempty"`
}

// RepoData 存放来自 workspace 的仓库信息。
type RepoData struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	Ref         string `json:"ref,omitempty"`
}

// ProjectResourceData 镜像 handler.ProjectResourceData——单个传给 daemon 的项目
// 资源。resource_ref 是随类型而异的 JSON。
// WorkspaceRepoData 是 workspace 里登记的本地目录，供运行结束时的 diff 采集
// 在任务目录之外寻找 agent 新建的 worktree。
type WorkspaceRepoData struct {
	Path  string `json:"path"`
	Label string `json:"label,omitempty"`
}

type ProjectResourceData struct {
	ID           string          `json:"id"`
	ResourceType string          `json:"resource_type"`
	ResourceRef  json.RawMessage `json:"resource_ref"`
	Label        string          `json:"label,omitempty"`
}

// ConnectedAppData 把 claim 响应当中的字段留在 daemon 侧类型里，同时与
// runtime 应用元数据包共用同一份权威 JSON 形状。
type ConnectedAppData = runtimeapps.ConnectedApp

// ActiveSiblingRunData 镜像服务器在 claim 时返回的告警上下文：同一个 agent 名下
// 另一个正在执行中的 issue 任务。排队中的任务被有意排除在这个上下文之外。
type ActiveSiblingRunData struct {
	TaskID          string `json:"task_id"`
	IssueID         string `json:"issue_id"`
	IssueIdentifier string `json:"issue_identifier"`
	IssueTitle      string `json:"issue_title"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
	StartedAt       string `json:"started_at,omitempty"`
}

// DeliveryContextData 镜像 claim 载荷里的交付上下文：阶段、候选、分配到的验收环境、
// 当前种子和所在批次。镜像字段：internal/handler/agent.go 的同名结构。
type DeliveryContextData struct {
	ID               string `json:"id"`
	Phase            string `json:"phase"`
	CandidateID      string `json:"candidate_id,omitempty"`
	CandidateCommit  string `json:"candidate_commit,omitempty"`
	Environment      string `json:"environment,omitempty"`
	EnvironmentState string `json:"environment_state,omitempty"`
	SeedID           string `json:"seed_id,omitempty"`
	SeedSHA256       string `json:"seed_sha256,omitempty"`
	BatchID          string `json:"batch_id,omitempty"`
	BatchStatus      string `json:"batch_status,omitempty"`
	BatchMembers     int    `json:"batch_members,omitempty"`
}

// IssueStatusData 镜像 claim 载荷里的一个活动中的自定义 workspace 状态
// （MUL-6460）。镜像字段：internal/handler/agent.go 里的
// TaskIssueStatusData，JSON 字段名一致。
type IssueStatusData struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description,omitempty"`
}

// Task 表示一条从服务器认领（claim）到的任务。
// Agent 数据（name、skills）由 claim 端点填充。
type Task struct {
	ReleaseResumeTaskID string `json:"release_resume_task_id,omitempty"`
	ReleaseResumeRoot   string `json:"release_resume_root,omitempty"`
	// StartClaimSupported gates retries when talking to older servers.
	StartClaimSupported  bool                   `json:"start_claim_supported,omitempty"`
	DispatchedAt         string                 `json:"dispatched_at,omitempty"`
	ID                   string                 `json:"id"`
	AgentID              string                 `json:"agent_id"`
	RuntimeID            string                 `json:"runtime_id"`
	IssueID              string                 `json:"issue_id"`
	WorkspaceID          string                 `json:"workspace_id"`
	WorkspaceSlug        string                 `json:"workspace_slug,omitempty"`
	IssueIdentifier      string                 `json:"issue_identifier,omitempty"`
	RemoteMCPConnections []remotemcp.Connection `json:"remote_mcp_connections,omitempty"`
	// RemoteMCPDaemonToken 只留在 daemon 内部，用于给本地 broker 的凭证解析调用
	// 做鉴权。它绝不能进入 agent 的环境变量或配置。
	RemoteMCPDaemonToken string `json:"remote_mcp_daemon_token,omitempty"`
	// PluginHookTools 是该 workspace 的 agent 触发型插件钩子，本地 MCP server 会以
	// 工具的形式把它们呈现给 agent。由服务器在 claim 时解析好；daemon 自己从不读
	// 插件状态。
	PluginHookTools []PluginHookTool `json:"plugin_hook_tools,omitempty"`
	// WorkspaceContext 镜像 workspace.context（Settings → General 里设置的
	// 每个 workspace 的系统提示词）。服务器无论任务类型每次 claim 都会填充它，
	// 好让 daemon 把 `## Workspace Context` 注入 brief。所属者没设置时为空。
	WorkspaceContext string                      `json:"workspace_context,omitempty"`
	SourceContext    *protocol.TaskSourceContext `json:"source_context,omitempty"`
	// IssueStatuses 镜像 claim 载荷里活动中的 CUSTOM 状态目录（MUL-6460）：
	// 每个状态的 key/name/category/description，已按目录顺序排好。会被渲染进
	// brief 的状态命令那几行；为空时（包括从没发过该字段的老服务器）brief 仍与
	// 只用内置状态的形态逐字节一致。IssueStatusesOmitted 是超出容量被丢弃的个数。
	IssueStatuses        []IssueStatusData      `json:"issue_statuses,omitempty"`
	IssueStatusesOmitted int                    `json:"issue_statuses_omitted,omitempty"`
	ActiveSiblingRuns    []ActiveSiblingRunData `json:"active_sibling_runs,omitempty"`
	DeliveryContext      *DeliveryContextData   `json:"delivery_context,omitempty"`
	ThreadName           string                 `json:"thread_name,omitempty"` // 给 provider 原生会话/线程历史用的语义化标题
	Agent                *AgentData             `json:"agent,omitempty"`
	ConnectedApps        []ConnectedAppData     `json:"connected_apps,omitempty"` // 本次运行通过 runtime MCP overlays 挂载的每运行应用能力
	Repos                []RepoData             `json:"repos,omitempty"`
	ProjectID            string                 `json:"project_id,omitempty"`          // 本任务所在的活动项目（若有）
	ProjectTitle         string                 `json:"project_title,omitempty"`       // 人类可读的项目标题，用于注入上下文
	ProjectDescription   string                 `json:"project_description,omitempty"` // 持久化的项目级上下文，注入 brief
	ProjectResources     []ProjectResourceData  `json:"project_resources,omitempty"`   // 需要暴露给 agent 的项目级资源
	// WorkspaceRepos 是 workspace 级本地目录（与项目无关），用于在运行结束后发现
	// agent 在任务目录之外新建的 worktree。镜像 handler.WorkspaceRepoData。
	WorkspaceRepos []WorkspaceRepoData `json:"workspace_repos,omitempty"`
	IsLeaderTask   bool                `json:"is_leader_task,omitempty"` // 为 true 表示本次在“小队队长”协调角色下执行
	// LeaderRoleResolved 是服务器能力位：IsLeaderTask/SquadID 权威地回答了“这次是不是队长运行”。
	// 早于该能力的服务器不保证这一点——#4951 之前的服务器从不发 is_leader_task，之后的虽然会发
	// 但没有这个保证——因此 taskIsSquadLeader 对这两种情况都回退到 brief 里的标记（MUL-5811）。
	LeaderRoleResolved bool   `json:"leader_role_resolved,omitempty"`
	PriorSessionID     string `json:"prior_session_id,omitempty"` // 本 issue 上一次任务留下的 Claude 会话 ID
	PriorWorkDir       string `json:"prior_work_dir,omitempty"`   // 本 issue 上一次任务使用的 work_dir
	// PriorSessionResumeUnavailable: MUL-5305 中服务器提示“更新的 Codex 会话被扣下
	// （rollout 缺失），如果带 PriorSessionID 那也只是较旧的回退”，因此即使旧会话能干净
	// 续接，本次运行也必须向用户披露这段连续性缺口。老服务器上该字段缺失/为 false。
	PriorSessionResumeUnavailable bool                   `json:"prior_session_resume_unavailable,omitempty"`
	TriggerCommentID              string                 `json:"trigger_comment_id,omitempty"`      // 触发本任务的评论
	CoalescedCommentIDs           []string               `json:"coalesced_comment_ids,omitempty"`   // MUL-4195: 任务还在排队时被并入本次运行的更早评论；agent 除了（最新的）触发评论外还必须处理它们。老服务器/未合并运行时为 nil
	CoalescedComments             []CoalescedCommentData `json:"coalesced_comments,omitempty"`      // MUL-4195: 被并入评论的完整细节（thread_id/author/created_at/content），prompt 可以逐条处理而无需假设它们共享同一线程。老服务器/未合并运行为 nil
	TriggerThreadID               string                 `json:"trigger_thread_id,omitempty"`       // 触发线程的根评论 ID；老服务器上回退到 trigger_comment_id
	TriggerCommentContent         string                 `json:"trigger_comment_content,omitempty"` // 触发评论的内容
	TriggerAuthorType             string                 `json:"trigger_author_type,omitempty"`     // "agent" 或 "member"——触发评论的作者类型
	TriggerAuthorName             string                 `json:"trigger_author_name,omitempty"`     // 触发评论作者的显示名
	// NewCommentCount 是本 agent 上次运行以来 issue 全局新增的评论数（不含自己的
	// 与注入的触发评论）；老 daemon 或冷启动时为 0/缺省。
	NewCommentCount  int    `json:"new_comment_count,omitempty"`
	NewCommentsSince string `json:"new_comments_since,omitempty"` // RFC3339 锚点（上次运行 started_at），计数从此算起；冷启动为空
	ChatSessionID    string `json:"chat_session_id,omitempty"`    // chat 任务时非空
	// ChatChannelType: 当 chat 会话由 IM 频道承载时为 "slack"；纯网页 chat 为空。
	// 驱动 prompt 里的“频道感知”那一块。
	ChatChannelType          string               `json:"chat_channel_type,omitempty"`
	ChatChannelDeliversFiles bool                 `json:"chat_channel_delivers_files,omitempty"` // 服务器能力位：本次部署能把 agent 产出的文件走最后一跳投进该会话。早于该能力的服务器读出来是 false——此时会告诉 agent 用文字描述文件，最坏情况只是“本来能投而没投”。绝不能从 chat_channel_type 反推：是否有这一跳取决于服务器端的存储与 adapter 接线，daemon 根本看不到（MUL-4899）
	ChatType                 string               `json:"chat_type,omitempty"`                   // 频道会话是共享房间时为 "group"，与机器人 1:1 为 "p2p"。网页 chat 或老服务器为空；此时每轮 prompt 报 unknown 而不是猜成 1:1
	ChatInThread             bool                 `json:"chat_in_thread,omitempty"`              // 最新一次 @ 提及是楼层回复时为 true；决定 prompt 让 agent 先用哪条读取命令
	ChatMessage              string               `json:"chat_message,omitempty"`                // chat 任务的用户消息内容
	ChatMessageIDs           []string             `json:"chat_message_ids,omitempty"`            // 封进本回合的用户消息 id
	ChatMessageAttachments   []ChatAttachmentMeta `json:"chat_message_attachments,omitempty"`    // 关联到 chat 消息的附件；agent 用它们执行 `multica attachment download <id>`
	ChatIntro                bool                 `json:"chat_intro,omitempty"`                  // 对历史 is_agent_intro 会话的兼容遗留；新建 agent 不再产生这类 chat
	// IsCompanion 标记一种必须使用“受管辖的 Life 上下文”而非 provider 自有历史的
	// chat 或后台认知任务。
	IsCompanion  bool            `json:"is_companion,omitempty"`
	LifeContext  string          `json:"life_context,omitempty"`
	LifeJobID    string          `json:"life_job_id,omitempty"`
	LifeJobType  string          `json:"life_job_type,omitempty"`
	LifeJobInput json.RawMessage `json:"life_job_input,omitempty"`
	// RegenerateQuickActionsFor 只有早于“服务端生成快捷动作”的服务器才会设置
	// （MUL-5573）。读出来要当作“拒绝”标记，绝不能执行：见 runTask 里的守卫。
	RegenerateQuickActionsFor string          `json:"regenerate_quick_actions_for,omitempty"`
	AutopilotRunID            string          `json:"autopilot_run_id,omitempty"`          // autopilot 的 run_only 任务时非空
	AutopilotID               string          `json:"autopilot_id,omitempty"`              // 产生本次运行的 autopilot
	AutopilotTitle            string          `json:"autopilot_title,omitempty"`           // 作为任务上下文的 autopilot 标题
	AutopilotDescription      string          `json:"autopilot_description,omitempty"`     // 作为任务 prompt 的 autopilot 描述
	AutopilotSource           string          `json:"autopilot_source,omitempty"`          // manual、schedule、webhook 或 api
	AutopilotTriggerPayload   json.RawMessage `json:"autopilot_trigger_payload,omitempty"` // webhook/api 运行的可选触发载荷
	SupervisionPrompt         string          `json:"supervision_prompt,omitempty"`
	SupervisionInvestigation  json.RawMessage `json:"supervision_investigation,omitempty"`
	QuickCreatePrompt         string          `json:"quick_create_prompt,omitempty"`         // 快速创建任务时用户输入的自然语言
	QuickCreatePriority       string          `json:"quick_create_priority,omitempty"`       // 快速创建时显式选的优先级
	QuickCreateDueDate        string          `json:"quick_create_due_date,omitempty"`       // 快速创建时显式选的日历截止日
	QuickCreateAttachmentIDs  []string        `json:"quick_create_attachment_ids,omitempty"` // 快速创建 prompt 里上传、由 issue 创建绑定的附件
	QuickCreateSourceContext  json.RawMessage `json:"quick_create_source_context,omitempty"` // 不可变的过往上下文，与新的指令分开存放
	HandoffNote               string          `json:"handoff_note,omitempty"`                // 分配交接指令；渲染进开场 prompt 与 issue_context.md

	SquadID               string `json:"squad_id,omitempty"`                // 选择器用的是小队时，小队 UUID；Agent 仍是解析出的队长
	SquadName             string `json:"squad_name,omitempty"`              // 选择器小队的显示名，用于 prompt 文案
	ParentIssueID         string `json:"parent_issue_id,omitempty"`         // 从“添加子 issue”打开的快速创建任务——新 issue 应挂到的父 issue UUID
	ParentIssueIdentifier string `json:"parent_issue_identifier,omitempty"` // 快速创建父 issue 的人类可读标识（如 MUL-123），用于 prompt 上下文
	// RequestingUserName 与 RequestingUserProfileDescription 描述 agent 为之干活的
	// 人类用户。v1 从 runtime 属主（注册 daemon 的用户）那里取。runtime 无属主
	// （云/系统 runtime）或用户没填描述时为空。注入 brief 的 `## Requesting User`
	// 块；description 为空时整块省略，避免 agent 看到一个没用的标题。
	RequestingUserName               string `json:"requesting_user_name,omitempty"`
	RequestingUserProfileDescription string `json:"requesting_user_profile_description,omitempty"`
	// Initiator* 标识“触发本次任务”的行为人（当前评论/@提及或 chat 消息背后真正的
	// 请求者），与 agent 运行所用的凭据属主（runtime owner）区分开。评论触发的任务
	// 解析成触发评论作者；chat 任务解析成会话创建者。对没有可归属人类发起者的任务
	// 类型（on-assign、autopilot、quick-create）为空。InitiatorEmail 只有发起者是
	// member 时才填。daemon 把它们放进 brief 的 `## Task Initiator`，让可见于
	// workspace 的 agent 能按人归属请求。agent 的有效凭据仍是 owner 范围——
	// 这是一份经证实的身份声明，不是凭据。见 MUL-2645。
	InitiatorType  string `json:"initiator_type,omitempty"`
	InitiatorID    string `json:"initiator_id,omitempty"`
	InitiatorName  string `json:"initiator_name,omitempty"`
	InitiatorEmail string `json:"initiator_email,omitempty"`
	// AuthToken 是服务器在 claim 时铸造的任务级凭据。daemon 把它作为 MULTICA_TOKEN
	// 注入被拉起的 agent，使 agent 永远看不到 daemon 自己的（通常是 workspace 属主
	// 的）凭据。为空或非任务级的值对可写 agent 任务是致命的；daemon 绝不能回退到
	// 自己的 token。见 MUL-3292。
	AuthToken string `json:"auth_token,omitempty"`
}

// ChatAttachmentMeta 是 daemon 递给 chat 任务 agent 的结构化附件元数据。传
// id + filename + content_type，是为了让 chat prompt 能显式列出附件并指导 agent
// 执行 `multica attachment download <id>`，而不是去猜一个会过期的签名 CDN URL。
type ChatAttachmentMeta struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
}

// CoalescedCommentData 镜像服务端结构（handler.CoalescedCommentData）：任务还在
// 排队时被并入本次运行的评论的完整细节（MUL-4195）。prompt 会逐条内嵌它们，让
// agent 能覆盖每条被并入的评论，而无需假设它们都挂在触发线程下。
type CoalescedCommentData struct {
	ID         string `json:"id"`
	ThreadID   string `json:"thread_id,omitempty"`
	AuthorType string `json:"author_type,omitempty"`
	AuthorName string `json:"author_name,omitempty"`
	Content    string `json:"content"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// AgentData 存放 claim 端点返回的 agent 详情。
type AgentData struct {
	ID                    string                     `json:"id"`
	Name                  string                     `json:"name"`
	Instructions          string                     `json:"instructions"`
	Skills                []SkillData                `json:"skills,omitempty"`
	SkillRefs             []SkillRefData             `json:"skill_refs,omitempty"`
	CustomEnv             map[string]string          `json:"custom_env,omitempty"`
	CustomArgs            []string                   `json:"custom_args,omitempty"`
	McpConfig             json.RawMessage            `json:"mcp_config,omitempty"`
	Model                 string                     `json:"model,omitempty"`
	ThinkingLevel         string                     `json:"thinking_level,omitempty"`
	ServiceTier           string                     `json:"service_tier,omitempty"`
	DisabledRuntimeSkills []DisabledRuntimeSkillData `json:"disabled_runtime_skills,omitempty"`
	// RuntimeConfig 是 agent 记录上按 provider 存放的 runtime_config JSON，claim
	// 端点原样转发。各 provider 可自行解码自己的字段；其余后端忽略它。
	RuntimeConfig json.RawMessage `json:"runtime_config,omitempty"`
}

// DisabledRuntimeSkillData 是某个“必须对该 agent 的 provider 进程隐藏”的
// runtime 本地技能在任务链路上的身份标识。
type DisabledRuntimeSkillData struct {
	RuntimeID string `json:"runtime_id"`
	Provider  string `json:"provider"`
	Root      string `json:"root"`
	Key       string `json:"key"`
	Name      string `json:"name,omitempty"`
	Plugin    string `json:"plugin,omitempty"`
}

// SkillData 表示一个用于任务执行的结构化技能。
type SkillData struct {
	ID          string          `json:"id"`
	Source      string          `json:"source,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Hash        string          `json:"hash,omitempty"`
	SizeBytes   int64           `json:"size_bytes,omitempty"`
	Content     string          `json:"content"`
	Files       []SkillFileData `json:"files,omitempty"`
}

// SkillFileData 表示技能内的一个支撑文件。
type SkillFileData struct {
	Kind      string `json:"kind,omitempty"`
	FileID    string `json:"file_id,omitempty"`
	Mode      int32  `json:"mode,omitempty"`
	Data      []byte `json:"data,omitempty"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	SHA256    string `json:"sha256,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// SkillRefData 是技能引用：只描述技能的身份与内容指纹，不含文件内容本体。
type SkillRefData struct {
	ID          string             `json:"id"`
	Source      string             `json:"source"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Hash        string             `json:"hash"`
	SizeBytes   int64              `json:"size_bytes"`
	FileCount   int                `json:"file_count"`
	Files       []SkillFileRefData `json:"files,omitempty"`
}

// SkillFileRefData 是技能引用的单个文件指纹条目。
type SkillFileRefData struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

// TaskUsageEntry 表示一次任务执行中单个模型的 token 用量。
type TaskUsageEntry struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	// CostUSDTicks 是 provider 对该用量给出的价格，单位 1e-10 美元。agent 没上报
	// 花费时省略——这是常见情况——此时服务端把列留 NULL，改由客户端用定价表估算。
	// 参见 agent.TokenUsage.CostUSDTicks。
	CostUSDTicks int64 `json:"cost_usd_ticks,omitempty"`
}

// TaskTraceEntry 是本次执行在外部链路观测系统里的一条根请求记录。
//
// Provider 是 backend 名（codebuddy / claude …），SessionID 是子进程自己声明的会话，
// ProviderTraceID 是子进程自己声明的根请求 id。它与外部观测系统（AgentLens）的
// trace/span 是否对应**未经实测**，不要把它当成观测系统 id 使用。三者都取自子进程
// 输出，绝不从父进程环境变量推断。Correlation 说明这条关联是"准确记录"还是"事后
// 按会话+时间窗推断"，为空的按准确记录处理。
// ProviderTrace 是一条"某个子进程自报的根请求 id"及其所属 session。SessionID 可能为
// 空——子进程没有在事件里声明 session 时，未知就保持未知，不能用别的进程的 session 补。
type ProviderTrace struct {
	SessionID string
	TraceID   string
	// SDK 侧溯源：与 CID（TraceID）不是同一个命名空间，且不是外部观测系统的 id。
	// 归属唯一时才有值；空表示未知，不是"没有"。
	SDKTraceID           string
	SDKTraceSourceSHA256 string
	SDKTraceSourcePath   string
}

type TaskTraceEntry struct {
	Provider             string `json:"provider"`
	SessionID            string `json:"session_id,omitempty"`
	ProviderTraceID      string `json:"provider_trace_id"`
	Correlation          string `json:"correlation,omitempty"`
	Source               string `json:"source,omitempty"`
	SDKTraceID           string `json:"sdk_trace_id,omitempty"`
	SDKTraceSourceSHA256 string `json:"sdk_trace_source_sha256,omitempty"`
	SDKTraceSourcePath   string `json:"sdk_trace_source_path,omitempty"`
}

// TaskResult 是一次任务执行的结果。
type TaskResult struct {
	CodeDiff   *codediff.Diff `json:"code_diff,omitempty"`
	Status     string         `json:"status"`
	Comment    string         `json:"comment"`
	BranchName string         `json:"branch_name,omitempty"`
	EnvType    string         `json:"env_type,omitempty"`
	SessionID  string         `json:"session_id,omitempty"` // 供将来续接用的 Claude 会话 ID
	WorkDir    string         `json:"work_dir,omitempty"`   // 执行期间使用的工作目录
	// DurableWorkDir 仅在“一次性本地 worktree 被定稿并确认已删除”后取代 WorkDir。
	// 为空时 WorkDir 依然权威。
	DurableWorkDir string `json:"durable_work_dir,omitempty"`
	EnvRoot        string `json:"-"` // 写 GC 元数据用的环境根目录（不发服务器）
	FailureReason  string `json:"-"` // 在 blocked 路径上转发给 FailTask 的分类器；为空则回退 'agent_error'
	// SessionRolloutMissing: daemon 因该任务的 Codex 会话不在 store 里而扣下未用它
	// 时置位（MUL-5305）。转发给最终报告，让服务器清掉续接指针并给下一次 claim
	// 标记出连续性缺口。不属于线路上的结果字段本身。
	SessionRolloutMissing bool `json:"-"`
	// RetiredSessionID 记录本次运行被告知续接、后又因不可续接而放弃的会话
	// （GH #6066）。在所有终结路径上都会转发，包括成功那条：恰恰是“换新会话重试
	// 且成功”时，被放弃的 id 不转发就还会留在候选里。
	RetiredSessionID string           `json:"-"`
	Usage            []TaskUsageEntry `json:"usage,omitempty"` // 各模型的 token 用量
	// ProviderTraceIDs 是本次执行中每个子进程自报的根请求 id（0..N 条），每条都带着
	// 产生它的那个子进程的 session。它与用量独立：模型请求可能一个都没发出去，反过来
	// 没有用量也能有链路。由 daemon 单独上报，不塞进 complete/fail 的载荷里，避免
	// 终态重试把关联丢在半路。
	//
	// 之所以成对而不是只有一个 id 列表：换新会话重试时首进程是 S1/T1、重试进程是
	// S2/T2，只留 id 再统一填最终 result.SessionID 会把 T1 记到 S2 上，还标成精确
	// 关联。子进程没自报 session 时该字段留空，而不是拿别的进程的 session 顶替。
	ProviderTraceIDs []ProviderTrace `json:"-"`
}

// PluginHookTool 是一个 agent 触发型插件钩子，agent 视角下即一个工具。
//
// 线上镜像 service.PluginHookTool。在这里声明而非 import，是为了让 daemon 不依赖
// 服务端 service 包——Task 本身放在 daemon 侧类型也是同一个原因。
type PluginHookTool struct {
	InstallationID string          `json:"installation_id"`
	HookKey        string          `json:"hook_key"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	InputSchema    json.RawMessage `json:"input_schema,omitempty"`
}
