// 本文件是「Agent（智能体）」资源的 HTTP 处理器集合，负责 agent 的
// 增删改查、归档/恢复、任务取消与运行历史查询，以及工作区维度的
// 聚合统计（活动/运行次数/任务快照等）。核心关注点：
//   - 响应组装与脱敏：把 DB 行转成对外 AgentResponse，凡是含密钥的字段
//     （mcp_config、gateway.token、composio 白名单、custom_env）都必须按
//     调用者身份（成员/Agent actor/所有者/管理员）脱敏，绝不原样上送。
//   - 权限模型：私有 agent 仅所有者/工作区 owner-admin 可见，public_to
//     agent 需在 invocation_targets 白名单内；修改/归档必须 canManageAgent。
//   - 调用参数（MUL-3963）与 composio 叠加层（MUL-3869）为「仅所有者可写」。
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/runtimeapps"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

// 与 packages/core/agents/constants.ts 中的 AGENT_DESCRIPTION_MAX_LENGTH
// 以及 060 号迁移里的 agent_description_length CHECK 约束保持一致。计数单位
// 是 Unicode 码点（utf8.RuneCountInString），对应 Postgres 的 char_length
// 和前端 String.prototype.length 的用户体验。
const maxAgentDescriptionLength = 255

const (
	// 开场白最多 3 条；标签 ≤80 字符；提示正文 ≤4000 字符。
	maxAgentConversationStarters      = 3
	maxAgentConversationStarterLabel  = 80
	maxAgentConversationStarterLength = 4000
)

// AgentConversationStarter 是 agent 可选的「开场白」建议：用户在对话
// 第一轮可一键点选的预设提问。
type AgentConversationStarter struct {
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
}

// AgentResponse 是对外返回的 agent 资源结构（列表/详情/变更后响应通用）。
// 多个字段遵循「脱敏」约定：含密钥的配置绝不出现在该结构里，只给粗粒度元数据。
type AgentResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	// RuntimeID 在 agent「未绑定 runtime」时为空串——当它的 runtime 被删除后，
	// agent 的配置与历史得以保留，但需绑定新 runtime 才能再次运行（MUL-5559）。
	// 线格式仍保持 string 以兼容已安装客户端；真正的信号是 RuntimeBound。
	RuntimeID string `json:"runtime_id"`
	// RuntimeBound 恰好只在 agent 没有 runtime 时为 false。UI 应以此字段而非
	// RuntimeID 是否为空来判断，且不能与「已绑定但离线」（另一回事：重连机器
	// vs 换一台新机器）混淆。
	RuntimeBound bool   `json:"runtime_bound"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	// Instructions 是 Agent 的完整可编辑提示词。
	Instructions string `json:"instructions"`
	// ConversationStarters 是可选的、agent 专属的第一轮建议。空列表表示
	// 让客户端渲染本地化的兜底开场白。
	ConversationStarters []AgentConversationStarter `json:"conversation_starters"`
	// SystemKey 标识产品内置 agent（如 "mika"）。用户或模板创建的 agent 为空。
	// UI 用该键而不是显示名（所有者可以改名）来判断「这个 agent 由 Multica 维护」。
	SystemKey     string          `json:"system_key,omitempty"`
	AvatarURL     *string         `json:"avatar_url"`
	RuntimeMode   string          `json:"runtime_mode"`
	RuntimeConfig any             `json:"runtime_config"`
	CustomArgs    []string        `json:"custom_args"`
	McpConfig     json.RawMessage `json:"mcp_config"`
	// custom_env 有意不序列化进 agent 资源。agent 的 list/get/create/update/
	// archive/restore 响应及 WS 事件只暴露粗粒度元数据（has_custom_env、
	// custom_env_key_count），让 UI 能展示「已配置 N 个变量」而无需把密钥
	// 拖过整个 API 面。读取取值须走专门的、带审计的 `GET /api/agents/{id}/env`
	// 接口；写入须 `PUT` 同一路径。agent-actor 令牌在该处被拒绝。见 MUL-2600。
	HasCustomEnv      bool   `json:"has_custom_env"`
	CustomEnvKeyCount int    `json:"custom_env_key_count"`
	McpConfigRedacted bool   `json:"mcp_config_redacted"`
	Visibility        string `json:"visibility"`
	// PermissionMode 是调用权限模式（MUL-3963）："private"（仅所有者）或
	// "public_to"（白名单见 InvocationTargets）。它取代 Visibility 成为
	// 鉴权依据；Visibility 仅作为派生出来的遗留字段保留，让旧客户端永远
	// 看不到权限被悄悄放宽。
	PermissionMode string `json:"permission_mode"`
	// InvocationTargets 是 public_to agent 的白名单。private agent 为空。
	// 仅在会加载它的 detail/list/create/update 响应中填充；广播负载留空。
	InvocationTargets  []AgentInvocationTargetDTO `json:"invocation_targets"`
	Status             string                     `json:"status"`
	MaxConcurrentTasks int32                      `json:"max_concurrent_tasks"`
	Model              string                     `json:"model"`
	// ThinkingLevel 是该 agent 持久化的、runtime 原生的推理档位 token
	// （空 = 用 runtime 默认）。选择器按 runtime×model 提供；API 不做
	// 跨厂商归一化。见 MUL-2339。
	ThinkingLevel string `json:"thinking_level"`
	// ServiceTier 是该 agent 持久化的、runtime 原生的 Codex 执行档位
	// （空 = 继承本地 Codex 配置）。
	ServiceTier string `json:"service_tier"`
	// ComposioToolkitAllowlist 是该 agent 在任务派发时被允许挂载为 MCP 的
	// Composio toolkit 子集——适用于任何通过该 agent 调用权限的运行，使用
	// agent 所有者的 Composio 连接（MUL-3963；不再要求发起人==所有者）。
	// NULL 或空 = 不叠加。和 mcp_config 一样属于「所有者专属」数据：slug
	// 本身不保密，但「{agent 所有者} 愿意暴露什么」这一视图跨账号展示会泄漏
	// 其他成员对集成的使用面，隐私上易生困惑。对非所有者一律脱敏为 `nil` +
	// composio_toolkit_allowlist_redacted=true，与既有 mcp_config 脱敏契约一致。
	ComposioToolkitAllowlist         []string               `json:"composio_toolkit_allowlist,omitempty"`
	ComposioToolkitAllowlistRedacted bool                   `json:"composio_toolkit_allowlist_redacted,omitempty"`
	OwnerID                          *string                `json:"owner_id"`
	Skills                           []AgentSkillSummary    `json:"skills"`
	DisabledRuntimeSkills            []DisabledRuntimeSkill `json:"disabled_runtime_skills"`
	CreatedAt                        string                 `json:"created_at"`
	UpdatedAt                        string                 `json:"updated_at"`
	ArchivedAt                       *string                `json:"archived_at"`
	ArchivedBy                       *string                `json:"archived_by"`
}

// runtimeConfigGatewayTokenMask 是 API 对任何非空 `runtime_config.gateway.token`
// 的占位替换值。该 token 是 bearer 凭证，直接经 GET 响应透出会让任何对
// agent 有读权限的人都拿到网关密钥。该掩码是哨兵——当 UI 之后 PATCH agent
// 并在该字段原样提交同一个掩码时，更新处理器会用持久化的真实 token 还原，
// 而不是把掩码当真值覆盖进去。
const runtimeConfigGatewayTokenMask = "***"

// agentToResponse 把 DB 行转成对外 AgentResponse：反序列化各 JSON 列、
// 统计 custom_env 键数（不暴露值）、对 gateway.token 做掩码等。
func (h *Handler) agentToResponse(a db.Agent) AgentResponse {
	var rc any
	if a.RuntimeConfig != nil {
		json.Unmarshal(a.RuntimeConfig, &rc)
	}
	if rc == nil {
		rc = map[string]any{}
	}
	maskGatewayToken(rc)

	// 只计算 env 元数据，绝不暴露取值。这里解出 map 仅为了数键数，map 不会
	// 进入响应。UI 拿到的是粗粒度 has_custom_env / key_count——要看值必须走
	// GET /api/agents/{id}/env（仅 agent 所有者或工作区 owner/admin，且审计）。
	envKeyCount := 0
	if a.CustomEnv != nil {
		var customEnv map[string]string
		if err := json.Unmarshal(a.CustomEnv, &customEnv); err != nil {
			slog.Warn("failed to unmarshal agent custom_env", "agent_id", uuidToString(a.ID), "error", err)
		}
		envKeyCount = len(customEnv)
	}

	var customArgs []string
	if a.CustomArgs != nil {
		if err := json.Unmarshal(a.CustomArgs, &customArgs); err != nil {
			slog.Warn("failed to unmarshal agent custom_args", "agent_id", uuidToString(a.ID), "error", err)
		}
	}
	if customArgs == nil {
		customArgs = []string{}
	}

	var mcpConfig json.RawMessage
	if a.McpConfig != nil {
		mcpConfig = json.RawMessage(a.McpConfig)
	}

	conversationStarters := []AgentConversationStarter{}
	if len(a.ConversationStarters) > 0 {
		if err := json.Unmarshal(a.ConversationStarters, &conversationStarters); err != nil {
			slog.Warn("failed to unmarshal agent conversation_starters", "agent_id", uuidToString(a.ID), "error", err)
			conversationStarters = []AgentConversationStarter{}
		}
	}

	// composio_toolkit_allowlist 列以 TEXT[] 存储，sqlc 拿到的是 []string。
	// 通过 postgres 驱动时 NULL 与 `{}` 都会序列化为 nil——两者都正确表示
	// "没有 toolkit"，但 API 面要区分「owner 还没开通集成」只能依赖三元组
	// （nil / 空切片 / 非空切片）。这里原样传递切片，由下方脱敏+仅所有者
	// 门控来决定。
	composioAllowlist := a.ComposioToolkitAllowlist

	return AgentResponse{
		ID:                       uuidToString(a.ID),
		WorkspaceID:              uuidToString(a.WorkspaceID),
		RuntimeID:                uuidToString(a.RuntimeID),
		RuntimeBound:             a.RuntimeID.Valid,
		Name:                     a.Name,
		Description:              a.Description,
		Instructions:             a.Instructions,
		ConversationStarters:     conversationStarters,
		SystemKey:                a.SystemKey.String,
		AvatarURL:                h.resolveAvatarURLPtr(textToPtr(a.AvatarUrl)),
		RuntimeMode:              a.RuntimeMode,
		RuntimeConfig:            rc,
		CustomArgs:               customArgs,
		McpConfig:                mcpConfig,
		HasCustomEnv:             envKeyCount > 0,
		CustomEnvKeyCount:        envKeyCount,
		Visibility:               a.Visibility,
		PermissionMode:           a.PermissionMode,
		InvocationTargets:        []AgentInvocationTargetDTO{},
		Status:                   a.Status,
		MaxConcurrentTasks:       a.MaxConcurrentTasks,
		Model:                    a.Model.String,
		ThinkingLevel:            a.ThinkingLevel.String,
		ServiceTier:              a.ServiceTier.String,
		ComposioToolkitAllowlist: composioAllowlist,
		OwnerID:                  uuidToPtr(a.OwnerID),
		Skills:                   []AgentSkillSummary{},
		DisabledRuntimeSkills:    decodeDisabledRuntimeSkills(a.DisabledRuntimeSkills),
		CreatedAt:                timestampToString(a.CreatedAt),
		UpdatedAt:                timestampToString(a.UpdatedAt),
		ArchivedAt:               timestampToPtr(a.ArchivedAt),
		ArchivedBy:               uuidToPtr(a.ArchivedBy),
	}
}

// maskGatewayToken 在 runtime_config.gateway.token 非空时用公开掩码哨兵
// 替换它；其他任何结构都直接跳过（no-op）。
func maskGatewayToken(rc any) {
	root, ok := rc.(map[string]any)
	if !ok {
		return
	}
	gw, ok := root["gateway"].(map[string]any)
	if !ok {
		return
	}
	tok, _ := gw["token"].(string)
	if tok == "" {
		return
	}
	gw["token"] = runtimeConfigGatewayTokenMask
}

// preserveMaskedGatewayToken 在请求把公开掩码哨兵提交到 `gateway.token` 时，
// 把此前持久化的真实 gateway token 还原回来。否则一次 GET 之后原样 PATCH
// 同一个请求体，就会把 "***" 掩码写回数据库，悄悄销毁真实密钥。旧值取自
// 处理器刚为所有权/作用域检查加载的 agent 行。
func preserveMaskedGatewayToken(incoming any, persistedRuntimeConfig []byte) {
	root, ok := incoming.(map[string]any)
	if !ok {
		return
	}
	gw, ok := root["gateway"].(map[string]any)
	if !ok {
		return
	}
	tok, _ := gw["token"].(string)
	if tok != runtimeConfigGatewayTokenMask {
		return
	}
	// 进来的 token 是掩码——从 agent 行里把真实值捞出来。
	var prev struct {
		Gateway struct {
			Token string `json:"token"`
		} `json:"gateway"`
	}
	if len(persistedRuntimeConfig) == 0 {
		// 没有可保留的旧 token，字段视为被清空。
		delete(gw, "token")
		return
	}
	if err := json.Unmarshal(persistedRuntimeConfig, &prev); err != nil || prev.Gateway.Token == "" {
		delete(gw, "token")
		return
	}
	gw["token"] = prev.Gateway.Token
}

// RepoData 携带 claim 响应中的仓库信息，daemon 据此为每个工作区仓库
// 建立 worktree。
type RepoData struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	Ref         string `json:"ref,omitempty"`
}

// ProjectResourceData 是 claim 响应里 project 资源的线格式。daemon 读取该
// 列表并写入 agent 工作目录，让 skills/agent 能发现 project 级上下文。
//
// resource_ref 是类型相关的 JSON；除 url 这类 github_repo 的已知字段外，
// daemon 不做进一步解释。新增类型无需改动本结构。
type ProjectResourceData struct {
	ID           string          `json:"id"`
	ResourceType string          `json:"resource_type"`
	ResourceRef  json.RawMessage `json:"resource_ref"`
	Label        string          `json:"label,omitempty"`
}

// ConnectedAppData 把 daemon-claim 的线字段类型限定在 handler 包内，
// 同时与 runtime app 元数据包共享同一份规范 JSON 形状。
type ConnectedAppData = runtimeapps.ConnectedApp

// ActiveSiblingRunData 是 claim 时刻携带的、关于同一 agent 另一个进行中
// issue 任务的受限上下文。排队的任务有意不包含——它们尚无法协调。它让
// daemon 在新 claim 到的运行重复做别处正在做的代码/PR 工作之前给出警告。
type ActiveSiblingRunData struct {
	TaskID          string `json:"task_id"`
	IssueID         string `json:"issue_id"`
	IssueIdentifier string `json:"issue_identifier"`
	IssueTitle      string `json:"issue_title"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
	StartedAt       string `json:"started_at,omitempty"`
}

// taskIssueStatusCap 限制 claim 负载里自定义状态的上限。这是防御性封顶，
// 不是产品上限：真实目录只有寥寥数条，而脚本化建了数百条状态的工作区，
// brief 不能被无限撑大。溢出通过 IssueStatusesOmitted 上报，brief 能明示截断。
const taskIssueStatusCap = 30

// TaskIssueStatusData 是 claim 线上单个「活跃的自定义工作区状态」（MUL-6460）。
// 只传 agent 选择/写入状态所需的字段：key 是 CLI 参数，name 是用户在下发
// 指令里称呼它的名字，category 锚定继承的平台行为，description 是管理员写
// 的「何时用它」指引——当某个 category 下有多个状态时它是消歧器。颜色/位置/
// id 不上线：对 agent 无行为意义，且服务端已按目录顺序（category 排序、
// 位置、key）输出。
type TaskIssueStatusData struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description,omitempty"`
}

// AgentTaskResponse 是 agent 任务（运行）的对外线格式：既是任务列表/详情的
// 返回体，也是 daemon claim 运行的负载。任务种类（issue 评论触发 / 聊天 /
// autopilot / quick_create / 直接指派）由 Kind 判别，各自的来源上下文
// （TriggerCommentID / ChatSessionID / AutopilotRunID 等）按需携带。
type AgentTaskResponse struct {
	ID                   string                 `json:"id"`
	AgentID              string                 `json:"agent_id"`
	RuntimeID            string                 `json:"runtime_id"`
	IssueID              string                 `json:"issue_id"`
	WorkspaceID          string                 `json:"workspace_id"`
	WorkspaceSlug        string                 `json:"workspace_slug,omitempty"`
	IssueIdentifier      string                 `json:"issue_identifier,omitempty"`
	RemoteMCPConnections []remotemcp.Connection `json:"remote_mcp_connections,omitempty"`
	// PluginHookTools 是该工作区的 agent 触发型插件钩子，daemon 在本任务中
	// 把它们渲染成 MCP 工具。在 claim 时刻解析，这样停用/卸载插件对下一个
	// 任务生效，而不是等 daemon 碰巧重启。
	PluginHookTools []service.PluginHookTool `json:"plugin_hook_tools,omitempty"`
	// RemoteMCPDaemonToken 是短时、仅作用于工作区与 daemon 的凭证，只被本地
	// daemon 的只写 Remote MCP 代理使用。绝不被注入 agent 进程。
	RemoteMCPDaemonToken string `json:"remote_mcp_daemon_token,omitempty"`
	// WorkspaceContext 是工作区设置里配置的工作区级系统提示词（DB 列
	// `workspace.context`）。注入 brief 的 `## Workspace Context` 段，让工作区
	// 内所有 agent（无论 issue/chat/autopilot/quick-create）看到同一份共享
	// 上下文。工作区所有者未设置时为空。
	WorkspaceContext string                      `json:"workspace_context,omitempty"`
	SourceContext    *protocol.TaskSourceContext `json:"source_context,omitempty"`
	// IssueStatuses 是工作区「活跃的自定义状态」目录（MUL-6460），注入 brief
	// 让 agent 能看到并使用七个内置之外的状态。内置状态不传：它们的 key/name/
	// 语义是锁定的（is_system 行不可改名/归档），daemon 本就知道。没有自定义
	// 状态的工作区为空——此时 daemon 按原样渲染 brief，保持既有部署字节一致。
	// 最多 taskIssueStatusCap 条，超出数量由 IssueStatusesOmitted 携带。
	IssueStatuses []TaskIssueStatusData `json:"issue_statuses,omitempty"`
	// IssueStatusesOmitted 记录被上限丢弃了多少条活跃自定义状态，让 brief
	// 能说明列表不完整，而不是把截断后的目录当成全部。
	IssueStatusesOmitted int                    `json:"issue_statuses_omitted,omitempty"`
	ActiveSiblingRuns    []ActiveSiblingRunData `json:"active_sibling_runs,omitempty"`
	ThreadName           string                 `json:"thread_name,omitempty"` // 供厂商原生会话/线程历史使用的语义化标题
	Status               string                 `json:"status"`
	Priority             int32                  `json:"priority"`
	DispatchedAt         *string                `json:"dispatched_at"`
	StartedAt            *string                `json:"started_at"`
	CompletedAt          *string                `json:"completed_at"`
	Result               any                    `json:"result"`
	Error                *string                `json:"error"`
	FailureReason        string                 `json:"failure_reason,omitempty"` // 见 TaskService.MaybeRetryFailedTask
	Attempt              int32                  `json:"attempt"`
	MaxAttempts          int32                  `json:"max_attempts"`
	ParentTaskID         *string                `json:"parent_task_id,omitempty"`
	IsLeaderTask         bool                   `json:"is_leader_task,omitempty"`
	// LeaderRoleResolved 是仅 claim 具备的能力，此处恒为 true：IsLeaderTask/
	// SquadID 已权威地回答「是否 leader 运行」，daemon 不得再从 brief 文本反推
	// 角色。早于 #4951 的服务器不发 is_leader_task，之后发送也不保证 brief——
	// 见不到该能力的 daemon 保留旧式推断。绝不渲染进提示词；见
	// daemon.taskIsSquadLeader（MUL-5811）。镜像字段：internal/daemon/types.go，
	// JSON 名相同。
	LeaderRoleResolved bool                  `json:"leader_role_resolved,omitempty"`
	Agent              *TaskAgentData        `json:"agent,omitempty"`
	ConnectedApps      []ConnectedAppData    `json:"connected_apps,omitempty"` // 仅 daemon-claim：经 runtime MCP 叠加挂载的每次运行的应用能力
	Repos              []RepoData            `json:"repos,omitempty"`
	ProjectID          string                `json:"project_id,omitempty"`          // issue 所属项目（若有）
	ProjectTitle       string                `json:"project_title,omitempty"`       // 用于在 agent 上下文中展示
	ProjectDescription string                `json:"project_description,omitempty"` // 注入 brief 的持久项目级上下文
	ProjectResources   []ProjectResourceData `json:"project_resources,omitempty"`   // 项目上挂载的资源
	CreatedAt          string                `json:"created_at"`
	PriorSessionID     string                `json:"prior_session_id,omitempty"` // 同一 issue 上一任务留下的会话 ID
	PriorWorkDir       string                `json:"prior_work_dir,omitempty"`   // 同一 issue 上一任务留下的工作目录
	// PriorSessionResumeUnavailable 在较新的 Codex 会话因缺失回滚被扣下时置位
	// （MUL-5305）；PriorSessionID（如有）为较旧的兜底。即使旧会话正常恢复，
	// daemon 也会在 brief 中点明这段连续性缺口。omitempty 让常见（无缺口）场景
	// 与旧 daemon 都不受影响。
	PriorSessionResumeUnavailable bool   `json:"prior_session_resume_unavailable,omitempty"`
	WorkDir                       string `json:"work_dir,omitempty"` // 本任务固定的本地工作目录；daemon 上报后才填充
	// RelativeWorkDir 是 WorkDir 的隐私安全展示形式，供 UI 使用。标准任务去掉
	// daemon 的 workspaces 根目录，同时保留旧式或可读的 workspace/task 段；
	// local_directory 任务的绝对路径在 envRoot 布局之外，这里剥离常见的 home
	// 目录前缀（`/Users/<name>/`、`/home/<name>/`、`<drive>:/Users/<name>/`），
	// 否则退回 basename，保证该字段永不携带用户的 home 目录或账户名。WorkDir
	// 为空或剥离后无内容则为空。完整规则见 relativeWorkDir()。旧客户端仍可直读
	// WorkDir；新 UI 应优先用 RelativeWorkDir。
	RelativeWorkDir string `json:"relative_work_dir,omitempty"`
	// DurableWorkDir 是 daemon 确认的、在一次性的任务 worktree 收尾删除后依然
	// 可用的目录。它是任务时点快照，不随之后的资源编辑变化。
	DurableWorkDir string `json:"durable_work_dir,omitempty"`
	// RelativeDurableWorkDir 是隐私安全展示形式。绝对路径仅保留给显式复制操作。
	RelativeDurableWorkDir string `json:"relative_durable_work_dir,omitempty"`
	// BranchName 是本运行交付成果所在的 git 分支，仅由 worktree 模式的
	// local_directory 任务设置。与 WorkDir 不同它可原样展示：是用户自己仓库内
	// 的 ref，而非文件系统路径。两条终态路径都会填充——失败运行也可能已提交
	// 部分成果，这正是该指针最要紧的时刻。
	BranchName               string                 `json:"branch_name,omitempty"`
	TriggerCommentID         *string                `json:"trigger_comment_id,omitempty"`      // 触发本任务的评论
	CoalescedCommentIDs      []string               `json:"coalesced_comment_ids,omitempty"`   // MUL-4195：本运行尚未开始时被并入的更早评论，保证单次运行仍覆盖每条有意的评论；trigger_comment_id 是最新那条。暴露给 UI 展示覆盖了哪些评论。omitempty 让旧客户端忽略
	CoalescedComments        []CoalescedCommentData `json:"coalesced_comments,omitempty"`      // MUL-4195：被并入评论的完整详情（thread_id/author/created_at/content），让 daemon 提示词能逐条回应，而无需假设它们共用触发线程。omitempty 让旧客户端忽略
	DeliveredCommentIDs      []string               `json:"delivered_comment_ids"`             // 恒存在：[] 是权威的「已送达为空」回执；字段缺失则标识来自旧版服务器的响应
	TriggerThreadID          string                 `json:"trigger_thread_id,omitempty"`       // 触发线程的根评论 ID
	TriggerCommentContent    string                 `json:"trigger_comment_content,omitempty"` // 触发评论的内容
	TriggerCommentCreatedAt  string                 `json:"trigger_comment_created_at,omitempty"`
	TriggerSummary           *string                `json:"trigger_summary,omitempty"`             // 规范的短描述快照（评论文本 / autopilot 标题），任务创建时截取；源被编辑/删除后依然有效
	TriggerAuthorType        string                 `json:"trigger_author_type,omitempty"`         // "agent" 或 "member"——触发评论的作者类型
	TriggerAuthorName        string                 `json:"trigger_author_name,omitempty"`         // 触发评论作者的显示名
	NewCommentCount          int                    `json:"new_comment_count,omitempty"`           // 上次运行以来触发线程的新评论数；不含注入的触发评论与自己的评论；omitempty 让旧 daemon 忽略
	NewCommentsSince         string                 `json:"new_comments_since,omitempty"`          // 计数起点的 RFC3339 锚点（上次运行的 started_at）；omitempty 让旧 daemon 忽略
	ChatSessionID            string                 `json:"chat_session_id,omitempty"`             // chat 任务非空
	ChatChannelType          string                 `json:"chat_channel_type,omitempty"`           // 聊天会话由 IM 频道承载时为 "slack"；纯 Web 聊天为空。让 agent 感知频道（从频道读历史，而非 Multica）
	ChatChannelDeliversFiles bool                   `json:"chat_channel_delivers_files,omitempty"` // 服务端能力：本部署能否把 agent 产出的文件放进本会话——适配器能回查绑定的附件且对象存储可用。旧服务器缺失/false 是安全读法：agent 被告知用文字描述文件。绝不由 daemon 依据 chat_channel_type 推断；见 handler.Handler.channelDeliversFiles
	ChatType                 string                 `json:"chat_type,omitempty"`                   // channel_chat_session_binding.chat_type——共享房间为 "group"，与机器人 1:1 为 "p2p"。让每轮提示告诉 agent 还有谁能读它的回复；纯 Web 聊天为空
	ChatInThread             bool                   `json:"chat_in_thread,omitempty"`              // 最近一次 @提及是否为线程内回复；告诉 agent 用 `multica chat thread` 还是 `multica chat history`
	ChatMessage              string                 `json:"chat_message,omitempty"`                // chat 任务的用户消息
	ChatMessageIDs           []string               `json:"chat_message_ids,omitempty"`            // 封入本轮 chat 的用户消息 id
	ChatMessageAttachments   []ChatAttachmentMeta   `json:"chat_message_attachments,omitempty"`    // 用户消息上的附件——agent 逐个执行 `multica attachment download <id>`
	ChatIntro                bool                   `json:"chat_intro,omitempty"`                  // 历史 is_agent_intro 会话的兼容字段；新建 agent 不再创建这类聊天
	// IsCompanion 与 LifeContext 标识受管控的 Life 聊天/后台运行。daemon 每次
	// 运行都从 Multica 数据重建该上下文。
	IsCompanion              bool            `json:"is_companion,omitempty"`
	LifeContext              string          `json:"life_context,omitempty"`
	LifeJobID                string          `json:"life_job_id,omitempty"`
	LifeJobType              string          `json:"life_job_type,omitempty"`
	LifeJobInput             json.RawMessage `json:"life_job_input,omitempty"`
	AutopilotRunID           string          `json:"autopilot_run_id,omitempty"`          // autopilot 派生的任务非空
	AutopilotID              string          `json:"autopilot_id,omitempty"`              // 派生本任务的 autopilot
	AutopilotTitle           string          `json:"autopilot_title,omitempty"`           // 用作任务上下文的 autopilot 标题
	AutopilotDescription     string          `json:"autopilot_description,omitempty"`     // 用作任务提示词的 autopilot 描述
	AutopilotSource          string          `json:"autopilot_source,omitempty"`          // manual、schedule、webhook 或 api
	AutopilotTriggerPayload  json.RawMessage `json:"autopilot_trigger_payload,omitempty"` // webhook/api 运行的可选触发负载
	SupervisionPrompt        string          `json:"supervision_prompt,omitempty"`
	SupervisionInvestigation json.RawMessage `json:"supervision_investigation,omitempty"`
	QuickCreatePrompt        string          `json:"quick_create_prompt,omitempty"`         // quick-create 任务的用户自然语言输入
	QuickCreatePriority      string          `json:"quick_create_priority,omitempty"`       // quick-create 显式选择的优先级
	QuickCreateDueDate       string          `json:"quick_create_due_date,omitempty"`       // quick-create 显式选择的日历截止日期
	QuickCreateAttachmentIDs []string        `json:"quick_create_attachment_ids,omitempty"` // quick-create 提示词里上传、建 issue 时绑定的附件 id
	QuickCreateSourceContext json.RawMessage `json:"quick_create_source_context,omitempty"` // source-context quick-create 的不可变历史上下文
	HandoffNote              string          `json:"handoff_note,omitempty"`                // 指派交接指令；渲染进运行的开场提示词与 issue_context.md（omitempty 让旧 daemon 忽略）
	SquadID                  string          `json:"squad_id,omitempty"`                    // quick-create 选择器是 squad 时使用；Agent 仍是被解析出的 leader
	SquadName                string          `json:"squad_name,omitempty"`                  // 被选 squad 的显示名
	ParentIssueID            string          `json:"parent_issue_id,omitempty"`             // 「添加子 issue」发起时——新 issue 应挂的父 issue（UUID）
	ParentIssueIdentifier    string          `json:"parent_issue_identifier,omitempty"`     // quick-create 父 issue 的可读标识（如 MUL-123），claim 时为提示词上下文解析
	// RequestingUserName + RequestingUserProfileDescription 镜像 agent 代其行动的
	// 用户（见 daemon/types.go）。v1 从 runtime 所有者取值，因此 daemon runtime
	// 下有值，其余为空。daemon 将两者写入 brief 的 `## Requesting User` 段；
	// 描述为空时整个标题段都省略。
	RequestingUserName               string `json:"requesting_user_name,omitempty"`
	RequestingUserProfileDescription string `json:"requesting_user_profile_description,omitempty"`
	// Initiator* 标识触发本任务的行动者——当前评论/提及/聊天消息背后真正的
	// 请求者——区别于以其凭据运行的 runtime 所有者。claim 时刻解析：评论触发
	// 的任务取触发评论作者；chat 任务取会话创建者。没有可归属的人类发起者的
	// 任务类型（on-assign、autopilot、quick-create）为空。InitiatorEmail 仅
	// 成员发起者（"member"）有值；agent 发起者（"agent"）只有名字无邮箱。
	// daemon 将其写入 brief 的 `## Task Initiator`，让工作区可见的多用户 agent
	// 能归属请求并按人套用隐私/访问规则，而不是把每个请求者都当成所有者。
	// agent 的有效 Multica 凭据仍以所有者作用域为准——这是证言式身份，不是
	// 凭据。见 MUL-2645。
	InitiatorType  string `json:"initiator_type,omitempty"`  // "member" 或 "agent"
	InitiatorID    string `json:"initiator_id,omitempty"`    // 用户 UUID（member）或 agent UUID
	InitiatorName  string `json:"initiator_name,omitempty"`  // 发起者的显示名
	InitiatorEmail string `json:"initiator_email,omitempty"` // 成员邮箱；agent 发起者为空
	Kind           string `json:"kind"`                      // 判别字段："comment" | "autopilot" | "chat" | "quick_create" | "direct"——activity 行用它给无关联 issue 的任务打标
	// Attribution 是本运行已解析的「可追责人类来源」（MUL-4302 §9）：来源标签
	// 与精确标志、发起者（可追责方）与授权方引用、证据指针与血缘。恒存在（纯
	// taskToResponse 构建标签与裸 id）；发起者/授权者的名字仅在面向用户的面上
	// 从全局用户表补全。
	Attribution *TaskAttribution `json:"attribution,omitempty"`
	// Usage 是本运行自身的 token 消耗，每个 (provider, model) 一条——与
	// task_usage 存储粒度一致，客户端也按此计价。仅在 issue 面的执行日志端点
	// （ListTasksByIssue）补全；daemon claim 路径保持 nil，claim 负载不携带
	// agent 用不上的记账数据。
	//
	// nil 与 [] 都表示「无用量记录」，UI 对两者渲染破折号——早于用量上报的
	// 运行，或死在任何模型调用之前的运行，确实没有数字；显示 0 反而断言它
	// 免费。omitempty 让两者都不上线。
	Usage []TaskUsageData `json:"usage,omitempty"`
	// AuthToken 是本任务作用域的 `mat_` token，daemon 必须以 MULTICA_TOKEN
	// 注入 agent 进程环境。服务端在 claim 时把它绑定到 (agent_id, task_id) 对，
	// 凡用该 token 鉴权的请求一律视为 actor=agent，与请求头无关——这样 agent
	// 进程无法借它经 env 管理端点读取别的 agent 的密钥。runtime 无属主用户时
	// claim 失败关闭；daemon 不得回退用自己的凭据。见 MUL-3292。
	AuthToken string `json:"auth_token,omitempty"`
}

// TaskAttribution 是一次运行的「可追责人类来源」线格式（MUL-4302 §9）。
// Source/Precise/Evidence/血缘直接来自行（纯数据）；Initiator/Originator
// 恒带裸用户 id，显示名/邮箱/头像只在面向用户的面上补全后才填充。
type TaskAttribution struct {
	// Source 是解析出可追责人类的水位级别：direct_human | delegation |
	// comment_source | rule_owner | owner_fallback | backfill | unattributed。
	// 永不为空（迁移前的 NULL 渲染为 "unattributed"）。
	Source string `json:"source"`
	// Precise 对降级来源（owner_fallback / backfill / unattributed）为 false；
	// UI 会显著区分它们，并计入覆盖率指标。
	Precise bool `json:"precise"`
	// Initiator 是可追责人类（accountable_user_id）。未归属时为 nil。
	Initiator *AttributionUser `json:"initiator,omitempty"`
	// Originator 是授权人类（originator_user_id）；autopilot（rule_owner /
	// owner_fallback）没有人类授权本次运行时为 nil。
	Originator *AttributionUser `json:"originator,omitempty"`
	// Evidence 指向运行的直接原因，UI 可据此跳转。
	Evidence            *TaskEvidence `json:"evidence,omitempty"`
	RuleVersionID       string        `json:"rule_version_id,omitempty"`
	DelegatedFromTaskID string        `json:"delegated_from_task_id,omitempty"`
	RetryOfTaskID       string        `json:"retry_of_task_id,omitempty"`
	RerunOfTaskID       string        `json:"rerun_of_task_id,omitempty"`
}

// AttributionUser 是「成员已离开也安全」的用户引用，从全局用户表解析。
// Name/Email/AvatarURL 补全前为空。
type AttributionUser struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Email     string `json:"email,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

// TaskEvidence 是运行直接原因（评论、autopilot 运行、规则版本、源任务……）
// 的带类型句柄。
type TaskEvidence struct {
	Kind  string `json:"kind"`
	RefID string `json:"ref_id"`
}

// taskAttributionBase 从行构建一次运行归因中「纯」的部分（不查库）：来源
// 标签与精确标志、证据、血缘，以及 initiator/originator 的裸用户 id。
// 名字稍后由 hydrateTaskAttributions 补全。
func taskAttributionBase(t db.AgentTaskQueue) *TaskAttribution {
	src := attribution.Source(t.OriginatorSource.String)
	attr := &TaskAttribution{
		Source:              src.String(), // 空（迁移前）→ "unattributed"
		Precise:             src.Precise(),
		RuleVersionID:       uuidToString(t.RuleVersionID),
		DelegatedFromTaskID: uuidToString(t.DelegatedFromTaskID),
		RetryOfTaskID:       uuidToString(t.RetryOfTaskID),
		RerunOfTaskID:       uuidToString(t.RerunOfTaskID),
	}
	if t.AccountableUserID.Valid {
		attr.Initiator = &AttributionUser{ID: uuidToString(t.AccountableUserID)}
	}
	if t.OriginatorUserID.Valid {
		attr.Originator = &AttributionUser{ID: uuidToString(t.OriginatorUserID)}
	}
	if t.TriggerEvidenceKind.Valid && t.TriggerEvidenceKind.String != "" {
		attr.Evidence = &TaskEvidence{Kind: t.TriggerEvidenceKind.String, RefID: uuidToString(t.TriggerEvidenceRefID)}
	}
	return attr
}

// hydrateTaskAttributions 用「全局」用户表批量补全各归因里 initiator/originator
// 的显示名/邮箱/头像（成员已离开也安全，且无 N+1）。尽力而为：查询失败时保留
// 已存在的裸用户 id——名字只是展示信息，不是门控条件。
func (h *Handler) hydrateTaskAttributions(ctx context.Context, attrs []*TaskAttribution) {
	seen := make(map[string]struct{})
	var ids []pgtype.UUID
	add := func(ref *AttributionUser) {
		if ref == nil || ref.ID == "" {
			return
		}
		if _, ok := seen[ref.ID]; ok {
			return
		}
		if u, err := util.ParseUUID(ref.ID); err == nil {
			seen[ref.ID] = struct{}{}
			ids = append(ids, u)
		}
	}
	for _, a := range attrs {
		if a == nil {
			continue
		}
		add(a.Initiator)
		add(a.Originator)
	}
	if len(ids) == 0 {
		return
	}
	users, err := h.Queries.GetUsersByIDs(ctx, ids)
	if err != nil {
		return
	}
	byID := make(map[string]db.GetUsersByIDsRow, len(users))
	for _, u := range users {
		byID[uuidToString(u.ID)] = u
	}
	fill := func(ref *AttributionUser) {
		if ref == nil {
			return
		}
		if u, ok := byID[ref.ID]; ok {
			ref.Name = u.Name
			ref.Email = u.Email
			if u.AvatarUrl.Valid {
				ref.AvatarURL = h.resolveAvatarURL(u.AvatarUrl.String)
			}
		}
	}
	for _, a := range attrs {
		if a == nil {
			continue
		}
		fill(a.Initiator)
		fill(a.Originator)
	}
}

// attributionsOf 从一个任务响应切片里收集（非 nil 的）TaskAttribution 指针，
// 让列表端点能一次性批量补全。
func attributionsOf(resps []AgentTaskResponse) []*TaskAttribution {
	out := make([]*TaskAttribution, 0, len(resps))
	for i := range resps {
		if resps[i].Attribution != nil {
			out = append(out, resps[i].Attribution)
		}
	}
	return out
}

// ChatAttachmentMeta 是嵌入在 chat 任务 claim 响应里的结构化附件元数据。
// agent 据此执行 `multica attachment download <id>`，而不是从 markdown URL 猜
// （私有 CDN 的 URL 带签名且 30 分钟过期）。daemon 侧镜像结构在
// internal/daemon/types.go，JSON 字段名一致。
type ChatAttachmentMeta struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
}

// CoalescedCommentData 携带被并入「尚未开始运行」的评论完整详情（MUL-4195），
// daemon 可直接嵌入提示词。早先的合并路径只下发评论 id 外加「它们在触发线程
// 里」的提示——当被并入的评论横跨多个线程时这是错的（issue 的指派人可能被
// 不同线程触发）。下发 thread_id/author/created_at/content 让提示词能逐条回应
// 每条被并入的评论，无需假设单一线程，也无需依赖未必覆盖到它们的 `--recent N`
// 窗口。daemon 侧镜像结构在 internal/daemon/types.go，JSON 字段名一致。
type CoalescedCommentData struct {
	ID         string `json:"id"`
	ThreadID   string `json:"thread_id,omitempty"`
	AuthorType string `json:"author_type,omitempty"`
	AuthorName string `json:"author_name,omitempty"`
	Content    string `json:"content"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// TaskUsageData 是一次运行中某个 (provider, model) 分片的 token 用量。字段名与
// runtime/dashboard 的用量行完全一致，客户端无需适配器即可喂给同一个
// `estimateCost` / `estimateCacheSavings` 帮助函数。
//
// CostUsdTicks 是厂商对这批 token 的报价（1e-10 美元）；厂商未上报时为 nil，
// 此时客户端按自己的费率表估算该分片。用指针而非零值：0 ticks 是真实答案
// （「厂商说这段免费」），必须与「厂商没说话」可区分。
type TaskUsageData struct {
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	CostUsdTicks     *int64 `json:"cost_usd_ticks,omitempty"`
}

// TaskAgentData 携带 claim 响应里的 agent 信息，daemon 据此搭好执行环境
// （分支命名、skill 文件、指令）。
type TaskAgentData struct {
	ID                    string                      `json:"id"`
	Name                  string                      `json:"name"`
	Instructions          string                      `json:"instructions"`
	Skills                []service.AgentSkillData    `json:"skills,omitempty"`
	SkillRefs             []service.AgentSkillRefData `json:"skill_refs,omitempty"`
	CustomEnv             map[string]string           `json:"custom_env,omitempty"`
	CustomArgs            []string                    `json:"custom_args,omitempty"`
	McpConfig             json.RawMessage             `json:"mcp_config,omitempty"`
	Model                 string                      `json:"model,omitempty"`
	ThinkingLevel         string                      `json:"thinking_level,omitempty"`
	ServiceTier           string                      `json:"service_tier,omitempty"`
	DisabledRuntimeSkills []DisabledRuntimeSkill      `json:"disabled_runtime_skills,omitempty"`
	// RuntimeConfig 是 agent 保存的原始 runtime_config JSON。daemon 解码其中
	// 各厂商专有字段，同时保留服务端所有的 schema。
	RuntimeConfig json.RawMessage `json:"runtime_config,omitempty"`
}

// taskToResponse 把队列行映射为线格式。workspaceID 由外部传入，因为行本身
// 不携带（workspace 在 agent/issue/chat 会话上）——要求调用方解析一次后传下来。
// 它填充 WorkspaceID 并为隐私安全的 RelativeWorkDir 推导提供输入；仅在确实
// 没有它的 daemon 面路径传 ""，此时 RelativeWorkDir 退回既有 WorkDir。
func taskToResponse(t db.AgentTaskQueue, workspaceID string) AgentTaskResponse {
	var result any
	if t.Result != nil {
		json.Unmarshal(t.Result, &result)
	}
	failureReason := ""
	if t.FailureReason.Valid {
		failureReason = t.FailureReason.String
	}
	workDir := ""
	if t.WorkDir.Valid {
		workDir = t.WorkDir.String
	}
	durableWorkDir := ""
	if t.DurableWorkDir.Valid {
		durableWorkDir = t.DurableWorkDir.String
	}
	handoffNote := ""
	if t.HandoffNote.Valid {
		handoffNote = t.HandoffNote.String
	}
	branchName := ""
	if t.BranchName.Valid {
		branchName = t.BranchName.String
	}
	return AgentTaskResponse{
		ID:                     uuidToString(t.ID),
		AgentID:                uuidToString(t.AgentID),
		RuntimeID:              uuidToString(t.RuntimeID),
		IssueID:                uuidToString(t.IssueID),
		WorkspaceID:            workspaceID,
		Status:                 t.Status,
		Priority:               t.Priority,
		DispatchedAt:           timestampToPtr(t.DispatchedAt),
		StartedAt:              timestampToPtr(t.StartedAt),
		CompletedAt:            timestampToPtr(t.CompletedAt),
		Result:                 result,
		Error:                  textToPtr(t.Error),
		FailureReason:          failureReason,
		BranchName:             branchName,
		Attempt:                t.Attempt,
		MaxAttempts:            t.MaxAttempts,
		ParentTaskID:           uuidToPtr(t.ParentTaskID),
		IsLeaderTask:           t.IsLeaderTask,
		CreatedAt:              timestampToString(t.CreatedAt),
		TriggerCommentID:       uuidToPtr(t.TriggerCommentID),
		CoalescedCommentIDs:    uuidsToStrings(t.CoalescedCommentIds),
		DeliveredCommentIDs:    uuidStringsOrEmpty(t.DeliveredCommentIds),
		TriggerSummary:         textToPtr(t.TriggerSummary),
		HandoffNote:            handoffNote,
		WorkDir:                workDir,
		RelativeWorkDir:        relativeWorkDir(workDir, workspaceID, uuidToString(t.ID)),
		DurableWorkDir:         durableWorkDir,
		RelativeDurableWorkDir: relativeWorkDir(durableWorkDir, "", ""),
		// 暴露任务来源，让 UI 区分 issue 关联任务与 chat/autopilot 派生任务；
		// 一旦任务没有关联 issue，三者都可能带着 issue_id = ""。
		ChatSessionID:  uuidToString(t.ChatSessionID),
		AutopilotRunID: uuidToString(t.AutopilotRunID),
		Kind:           computeTaskKind(t),
		// 归因的标签+证据+血缘+裸用户 id（纯数据）。名字只在面向用户的面上
		// 另行补全（MUL-4302 §9）。
		Attribution: taskAttributionBase(t),
	}
}

// relativeWorkDir 生成 daemon 上报的绝对 work_dir 的隐私安全展示形式。契约：
// 返回值绝不包含用户的 home 目录前缀或账户名。该「小标签」常出现在屏幕共享、
// 截图、录制里，因此本函数是唯一防线。
//
//   - 标准任务：按稳定 ID 后缀校验相邻的 workspace/task 段，然后剥离其之前
//     的一切。既兼容旧式 `<wsUUID>/<taskShort>` 根，也兼容可读的
//     `<workspaceSlug>-<wsShort>/<issueKey>-<taskShort>` 根，且不把标签当身份。
//   - local_directory 任务：绝对路径在 envRoot 布局之外。尝试识别常见 home
//     目录前缀（`/Users/<name>/`、`/home/<name>/`、`<drive>:/Users/<name>/`）
//     并剥掉，返回其余部分（如 `repos/foo`）。前缀无法识别时——不常见的 home
//     布局、网络挂载、`/opt`、`/srv` 下的路径等——退回 basename，避免碰巧把
//     某段用户名路径组件渲染出来。
//
// work_dir 为空、或剥离后什么都不剩时返回空（即 work_dir 恰为 home 目录——
// 渲染「什么都没有」好过渲染出一个写着 `<name>` 的标签）。taskDirSegment()
// 必须与 server/internal/daemon/execenv/git.go:taskKey 保持同步——两者消费同一
// 个任务 UUID。legacyTaskDirSegment 让隐私安全展示对 #7347 之前的根以及本 PR
// 早期构建创建的根继续有效。
func relativeWorkDir(workDir, workspaceID, taskID string) string {
	if workDir == "" {
		return ""
	}
	// 归一化 Windows 分隔符，使后面只按正斜杠推理。
	normalized := strings.ReplaceAll(workDir, "\\", "/")

	if workspaceID != "" && taskID != "" {
		parts := strings.Split(normalized, "/")
		for i := 0; i+1 < len(parts); i++ {
			if matchesWorkspacePathSegment(parts[i], workspaceID) &&
				matchesTaskPathSegment(parts[i+1], taskID) {
				return strings.Join(parts[i:], "/")
			}
		}
	}

	if stripped, ok := stripHomePrefix(normalized); ok {
		return stripped
	}

	return basename(normalized)
}

func matchesWorkspacePathSegment(segment, workspaceID string) bool {
	lower := strings.ToLower(segment)
	legacy := legacyTaskDirSegment(workspaceID)
	current := strings.ToLower(taskDirSegment(workspaceID))
	return strings.EqualFold(segment, workspaceID) ||
		strings.HasSuffix(lower, "-"+legacy) || strings.HasSuffix(lower, "-"+current)
}

func matchesTaskPathSegment(segment, taskID string) bool {
	lower := strings.ToLower(segment)
	legacy := legacyTaskDirSegment(taskID)
	current := strings.ToLower(taskDirSegment(taskID))
	return strings.EqualFold(segment, legacy) || strings.EqualFold(segment, current) ||
		strings.HasSuffix(lower, "-"+legacy) || strings.HasSuffix(lower, "-"+current)
}

// taskDirSegmentLen 与 taskDirSegment 镜像 execenv.taskKeyLen / execenv.taskKey
// ——任务 id 的最后 12 个十六进制字符。就地内联是为了让 agent handler 对 daemon
// 包零依赖（否则 handler 与 daemon 之间会形成不想要的循环引用）。
//
// 必须继续取「尾部」。UUIDv7 前导十六进制是时间戳位，约 65.5 秒内创建的任务
// 会共享它（#7326）；daemon 已不再从那一端读取，此处重建只有在两端一致时才
// 能匹配。
const taskDirSegmentLen = 12

func taskDirSegment(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	if len(s) > taskDirSegmentLen {
		return s[len(s)-taskDirSegmentLen:]
	}
	return s
}

// legacyTaskDirSegment 镜像历史 shortID 布局：去掉破折号后的前八个字符。
// 仅为展示兼容，绝不作为新身份。
func legacyTaskDirSegment(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	if len(s) > 8 {
		return strings.ToLower(s[:8])
	}
	return strings.ToLower(s)
}

// homeDirPattern 匹配 macOS/Linux/Windows（反斜杠归一化后）常见的按用户划分的
// home 目录布局：
//
//	/Users/<name>[/<rest>]
//	/home/<name>[/<rest>]
//	<drive>:/Users/<name>[/<rest>]
//
// 大小写不敏感：macOS/Windows 在文件系统层就不区分大小写；`/users/...` 与
// `/Users/...` 同等待遇可让剥离在共享盘上不常见的写法下依然健壮。
// 捕获组 1 是用户名段之后的可选剩余部分。
var homeDirPattern = regexp.MustCompile(`(?i)^(?:[A-Za-z]:)?/(?:Users|home)/[^/]+(?:/(.*))?$`)

// stripHomePrefix 识别常见 home 目录布局，返回用户名段之后的路径剩余部分。
// 命中已知 home 前缀时返回 (剩余, true)。剩余可能为空串（work_dir 恰好就是
// home 目录）——调用方将其视为「无可安全展示内容」。
func stripHomePrefix(p string) (string, bool) {
	m := homeDirPattern.FindStringSubmatch(p)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// basename 返回正斜杠路径最后一个非空段。作为无法识别路径时的最终隐私安全
// 兜底：单段绝不暴露 home 前缀，而叶名几乎总是最有用的上下文（local_directory
// 任务通常就是仓库目录名）。
func basename(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

// computeTaskKind 挑选 activity UI 用来决定任务行渲染方式的来源判别字符串。
// 由既有外键形状计算，无需额外查库：chat / autopilot / issue 上被触发（既有
// issue_id 又有 trigger_comment_id 的任意触发任务）/ quick_create（无关联源——
// agent 自己在建 issue）/ direct（既有 issue 上由指派人驱动的任务）。
func computeTaskKind(t db.AgentTaskQueue) string {
	if _, ok := lifeCognitionContextForTask(t); ok {
		return "life_cognition"
	}
	if uuidToString(t.ChatSessionID) != "" {
		return "chat"
	}
	if uuidToString(t.AutopilotRunID) != "" {
		return "autopilot"
	}
	if uuidToString(t.IssueID) == "" {
		return "quick_create"
	}
	if uuidToString(t.TriggerCommentID) != "" {
		return "comment"
	}
	return "direct"
}

// ListAgents 处理 GET /api/agents（默认不含已归档）。任何工作区成员都可调用，
// 但 private agent 仅所有者或工作区 owner/admin 可见，public_to agent 仅白名单
// 成员可见；agent actor 走 A2A 协作放行。响应会按角色对 mcp_config 与 composio
// 白名单脱敏。?include_archived=true 时含已归档。
func (h *Handler) ListAgents(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	userID := requestUserID(r)

	var agents []db.Agent
	var err error
	if r.URL.Query().Get("include_archived") == "true" {
		agents, err = h.Queries.ListAllAgents(r.Context(), parseUUID(workspaceID))
	} else {
		agents, err = h.Queries.ListAgents(r.Context(), parseUUID(workspaceID))
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agents")
		return
	}

	// 批量加载全部 agent 的 skills，避免 N+1。
	skillRows, err := h.Queries.ListAgentSkillsByWorkspace(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent skills")
		return
	}
	skillMap := map[string][]AgentSkillSummary{}
	for _, row := range skillRows {
		agentID := uuidToString(row.AgentID)
		skillMap[agentID] = append(skillMap[agentID], AgentSkillSummary{
			ID:          uuidToString(row.ID),
			Name:        row.Name,
			Description: row.Description,
			Enabled:     row.Enabled,
		})
	}

	// mcp_config 仍采用工作区级的 always-redact 设置加逐行 owner/admin 门控——
	// MCP server 配置里的密钥遵循 custom_env 过去的暴露规则。custom_env 本身已
	// 不再序列化进 agent 资源（MUL-2600）；见 AgentResponse 注释。
	ws, err := h.Queries.GetWorkspace(r.Context(), parseUUID(workspaceID))
	if err != nil {
		slog.Warn("GetWorkspace failed for redact check", "workspace_id", workspaceID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	alwaysRedact := workspaceAlwaysRedactSecrets(ws.Settings)

	// 只解析一次请求 actor。Agent actor 绕过可见性门控以保留 A2A 协作；成员
	// 只有拥有该 private agent 或是工作区 owner/admin 才可见，public_to agent
	// 只有在调用白名单上才可见。targets 批量加载避免 N+1，并复用来丰富每个
	// 响应的 invocation_targets。
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	targetsByAgent, ok := h.loadInvocationTargetsByAgent(r.Context(), agents)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to load agent invocation targets")
		return
	}
	visible := make([]AgentResponse, 0, len(agents))
	for _, a := range agents {
		// 质量监督的工作区托管 Agent 由监督设置维护，不属于可指派或可编辑的
		// Agent；它是可见 Agent 列表之外的执行身份。
		if a.SystemKey.Valid && a.SystemKey.String == service.SupervisionSystemKey {
			continue
		}
		targets := targetsByAgent[uuidToString(a.ID)]
		if actorType == "member" {
			if !memberAllowedToViewAgent(a, targets, actorID, member.Role) {
				continue
			}
		}
		resp := h.agentToResponse(a)
		applyInvocationTargetsToResponse(&resp, targets)
		if skills, ok := skillMap[resp.ID]; ok {
			resp.Skills = skills
		}
		// agent actor 永远看不到 mcp_config 密钥，即使其宿主的 PAT 本可满足
		// owner/admin 角色门控。否则跑在所有者 daemon 下的 agent 就能读到别的
		// agent 的 MCP 配置（常内嵌第三方 API token）——这正是 MUL-2600 为
		// custom_env 封堵的横向移动路径。
		if actorType == "agent" || alwaysRedact || !canViewAgentSecrets(a, userID, member.Role) {
			redactMcpConfig(&resp)
		}
		// composio_toolkit_allowlist 仅所有者可见——不是 slug 保密，而是把
		// 「{所有者} 开通了什么」展示给整个工作区会泄漏所有者的集成使用面，
		// 也困惑那些实际上无法编辑它的非所有者。工作区 owner/admin 不绕过此
		// 门控（区别于 mcp_config）：叠加层用的是所有者的连接并遵循调用权限
		// （MUL-3963），把 slug 展示给管理员毫无用处。agent actor 同样脱敏
		// （与 mcp_config 相同的 A2A 横向移动考量）。
		if !h.composioMCPAppsEnabled(r.Context()) {
			suppressComposioToolkitAllowlist(&resp)
		} else if actorType == "agent" || uuidToString(a.OwnerID) != userID {
			redactComposioToolkitAllowlist(&resp)
		}
		visible = append(visible, resp)
	}

	writeJSON(w, http.StatusOK, visible)
}

// GetAgent 处理 GET /api/agents/{id}。先按可见性加载 agent，再走 private 门控
// （403 而非 404，便于前端渲染「无权限」占位）；无权限返回 403。响应按调用者
// 角色脱敏 mcp_config 与 composio 白名单，并附带 skills 与 invocation_targets。
func (h *Handler) GetAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	// Private-agent 门控：成员必须在 allowed_principals 里才能查看（从而跳转到）
	// private agent。403 让前端渲染显式「无权访问」占位而不是 404——见
	// agent-detail-page.tsx。
	workspaceID := uuidToString(agent.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return
	}
	resp := h.agentToResponse(agent)
	if !h.enrichAgentResponseWithTargetsHTTP(w, r, &resp, agent.ID) {
		return
	}
	// 用摘要查询（无 `content` 列）——内嵌的 AgentSkillSummary 只需要
	// id/name/description，读大段 SKILL.md 正文又丢掉正是 #2174 修掉的回归。
	if err := h.attachAgentSkills(r.Context(), &resp, agent.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent skills")
		return
	}

	// mcp_config 脱敏（MUL-2600 已把 custom_env 移出该响应形状；密钥如今经
	// GET /api/agents/{id}/env 拉取）。
	userID := requestUserID(r)
	ws, err := h.Queries.GetWorkspace(r.Context(), agent.WorkspaceID)
	if err != nil {
		slog.Warn("GetWorkspace failed for redact check", "workspace_id", uuidToString(agent.WorkspaceID), "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	alwaysRedact := workspaceAlwaysRedactSecrets(ws.Settings)
	// agent actor 永远看不到 mcp_config（理由同 ListAgents）。
	if actorType == "agent" || alwaysRedact {
		redactMcpConfig(&resp)
	} else if member, ok := ctxMember(r.Context()); ok {
		if !canViewAgentSecrets(agent, userID, member.Role) {
			redactMcpConfig(&resp)
		}
	}
	// composio_toolkit_allowlist 严格仅所有者可见（理由同 ListAgents）。
	// 工作区 owner/admin 不绕过。
	if !h.composioMCPAppsEnabled(r.Context()) {
		suppressComposioToolkitAllowlist(&resp)
	} else if actorType == "agent" || uuidToString(agent.OwnerID) != userID {
		redactComposioToolkitAllowlist(&resp)
	}

	writeJSON(w, http.StatusOK, resp)
}

// CreateAgentRequest 是创建 agent 的请求体。
type CreateAgentRequest struct {
	Name                 string                     `json:"name"`
	Description          string                     `json:"description"`
	Instructions         string                     `json:"instructions"`
	ConversationStarters []AgentConversationStarter `json:"conversation_starters"`
	AvatarURL            *string                    `json:"avatar_url"`
	RuntimeID            string                     `json:"runtime_id"`
	RuntimeConfig        any                        `json:"runtime_config"`
	CustomEnv            map[string]string          `json:"custom_env"`
	CustomArgs           []string                   `json:"custom_args"`
	McpConfig            json.RawMessage            `json:"mcp_config"`
	Visibility           string                     `json:"visibility"`
	// PermissionMode + InvocationTargets 是新的调用权限输入（MUL-3963）。
	// permission_mode 出现时权威，忽略 Visibility；缺席时映射旧 Visibility
	// （private→private，workspace→public_to+workspace 目标）。创建时调用者
	// 只能是所有者，因此 targets 无条件接受。
	PermissionMode     *string                    `json:"permission_mode"`
	InvocationTargets  []AgentInvocationTargetDTO `json:"invocation_targets"`
	MaxConcurrentTasks int32                      `json:"max_concurrent_tasks"`
	Model              string                     `json:"model"`
	ThinkingLevel      string                     `json:"thinking_level"`
	ServiceTier        string                     `json:"service_tier"`
	// ComposioToolkitAllowlist 为逐任务叠加层门控播种（MUL-3869）。创建时只有
	// 调用用户可能是所有者，故此处无条件接受该字段；跨所有者的权限门控在 PUT。
	// nil = 列留 NULL（无叠加）。空切片 = 显式 `{}`（也无叠加，但列读作「已配置」，
	// 区别于「所有者从未开通集成」）。
	ComposioToolkitAllowlist []string `json:"composio_toolkit_allowlist"`
	// Template 记录 `agent_created` 分析事件使用的创建来源（例如
	// "agent_builder"）。空表示手工编写的 agent。
	Template string `json:"template"`
	// SkillIDs 在与 agent 行相同的事务里挂接，创建永远不呈现半配置状态。
	SkillIDs []string `json:"skill_ids"`
}

// decodeJSONBodyWithRawFields 一次性读 body 并做两次解组：目标结构体 + 原始
// map。后者用于区分「字段缺席」与「显式 null/空值」（Go 指针解组两者都变 nil），
// 是 thinking_level / composio 白名单等三态字段必需的信息。
func decodeJSONBodyWithRawFields(body io.Reader, dst any) (map[string]json.RawMessage, error) {
	payload, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(payload, dst); err != nil {
		return nil, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}

	return raw, nil
}

// normaliseAgentConversationStarters 校验并规范化开场白列表：条数/各字段的
// label、prompt 必填与长度上限，并去掉首尾空白。出错返回对用户友好的 400 文案。
func normaliseAgentConversationStarters(starters []AgentConversationStarter) ([]AgentConversationStarter, error) {
	if len(starters) > maxAgentConversationStarters {
		return nil, fmt.Errorf("conversation_starters must contain at most %d items", maxAgentConversationStarters)
	}

	normalised := make([]AgentConversationStarter, 0, len(starters))
	for i, item := range starters {
		item.Label = strings.TrimSpace(item.Label)
		item.Prompt = strings.TrimSpace(item.Prompt)
		if item.Label == "" {
			return nil, fmt.Errorf("conversation_starters[%d].label is required", i)
		}
		if item.Prompt == "" {
			return nil, fmt.Errorf("conversation_starters[%d].prompt is required", i)
		}
		if utf8.RuneCountInString(item.Label) > maxAgentConversationStarterLabel {
			return nil, fmt.Errorf("conversation_starters[%d].label must be %d characters or fewer", i, maxAgentConversationStarterLabel)
		}
		if utf8.RuneCountInString(item.Prompt) > maxAgentConversationStarterLength {
			return nil, fmt.Errorf("conversation_starters[%d].prompt must be %d characters or fewer", i, maxAgentConversationStarterLength)
		}
		normalised = append(normalised, item)
	}
	return normalised, nil
}

// CreateAgent 处理 POST /api/agents。权限与校验要点：请求者须是工作区成员且
// runtime 允许其使用（private runtime 仅其所有者可用）；对 thinking_level /
// service_tier 按目标 runtime 的能力做校验；custom_env 值不进 agent 行。创建与
// skill 挂接、invocation targets 在同一事务内完成，成功后发 agent:created 事件，
// 并按 actor 类型对响应脱敏。名字冲突（含已归档的）返回 409。
func (h *Handler) CreateAgent(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)

	var req CreateAgentRequest
	rawFields, err := decodeJSONBodyWithRawFields(r.Body, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ownerID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if utf8.RuneCountInString(req.Description) > maxAgentDescriptionLength {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("description must be %d characters or fewer", maxAgentDescriptionLength))
		return
	}
	if req.RuntimeID == "" {
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return
	}
	conversationStarters, err := normaliseAgentConversationStarters(req.ConversationStarters)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Visibility == "" {
		req.Visibility = "private"
	}
	if err := defaultAndValidateAgentMaxConcurrentTasks(rawFields, &req.MaxConcurrentTasks); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	runtimeUUID, ok := parseUUIDOrBadRequest(w, req.RuntimeID, "runtime_id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	// 解析调用权限（MUL-3963）。permission_mode 出现时权威；否则映射旧
	// visibility。创建时调用者恒为所有者，targets 无条件接受。
	_, hasTargets := rawFields["invocation_targets"]
	legacyVis := req.Visibility
	perm, _, permErr := parsePermissionInput(wsUUID, req.PermissionMode, req.InvocationTargets, req.PermissionMode != nil, hasTargets, &legacyVis)
	if permErr != nil {
		writeError(w, http.StatusBadRequest, permErr.Error())
		return
	}
	runtime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
		ID:          runtimeUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid runtime_id")
		return
	}

	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	if !canUseRuntimeForAgent(member, runtime) {
		writeError(w, http.StatusForbidden, "this runtime is private; only its owner can create agents on it")
		return
	}

	// thinking_level 校验：固定枚举厂商拒绝未知字面量；动态目录厂商
	// （Codex/OpenCode）在此拒绝畸形 token。Pi 有固定 token 宇宙加 daemon 发现
	// 的逐模型子集。逐模型组合缺口由 daemon 在运行时执行（MUL-2339）：
	// 组合无效的值记日志并从调用中剔除。
	if !agent.IsKnownThinkingValue(runtime.Provider, req.ThinkingLevel) {
		writeError(w, http.StatusBadRequest, thinkingLevelRejection(runtime.Provider, req.ThinkingLevel))
		return
	}
	// 对 ACP 目录厂商，厂商名不是能力答案——该 runtime 自己发现的目录才是。
	// 这样 Hermes Agent 用户收到明确的 400，而不是收下一个 daemon 之后会丢弃
	// 的档位。
	if req.ThinkingLevel != "" {
		switch h.acpThinkingDecision(r.Context(), runtime.Provider, runtime.ID) {
		case acpEffortAbsent:
			writeError(w, http.StatusBadRequest, thinkingCapabilityRejection(runtime.Provider))
			return
		case acpEffortUnknown:
			writeError(w, http.StatusBadRequest, thinkingCapabilityUnknownRejection(runtime.Provider))
			return
		}
	}
	if !agent.IsKnownServiceTier(runtime.Provider, req.ServiceTier) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("service_tier %q is not a recognised value for runtime %q", req.ServiceTier, runtime.Provider))
		return
	}

	// 在 INSERT 前探测工作区 agent 数量，让漏斗得到干净的「工作区首个 agent」
	// 信号——onboarding 的第 4 步总是落在这个分支。非致命读取：列表失败就以
	// isFirstAgent=false 继续，主 DB 操作是插入本身。
	isFirstAgent := false
	if existing, listErr := h.Queries.ListAgents(r.Context(), wsUUID); listErr == nil {
		isFirstAgent = len(existing) == 0
	}

	// 创建没有旧 token 可还原，因此若调用者把公开掩码哨兵提交为 gateway.token
	// （比如回放了被掩码的 GET 请求体），直接丢弃而非把字面 "***" 当真实 bearer
	// token 持久化。
	preserveMaskedGatewayToken(req.RuntimeConfig, nil)
	rc, _ := json.Marshal(req.RuntimeConfig)
	if req.RuntimeConfig == nil {
		rc = []byte("{}")
	}

	ce, _ := json.Marshal(req.CustomEnv)
	if req.CustomEnv == nil {
		ce = []byte("{}")
	}

	ca, _ := json.Marshal(req.CustomArgs)
	if req.CustomArgs == nil {
		ca = []byte("[]")
	}

	sp, _ := json.Marshal(conversationStarters)

	var mc []byte
	if rawMcpConfig, ok := rawFields["mcp_config"]; ok && !bytes.Equal(bytes.TrimSpace(rawMcpConfig), []byte("null")) {
		mc = append([]byte(nil), rawMcpConfig...)
	}

	// composio_toolkit_allowlist：JSON 字段是 slug 列表，存为 TEXT[]。这里做
	// 规范化（小写+去空白+去重），派发路径就能与逐用户的连接行做直接的相等比较。
	// nil 切片（或 JSON 键缺席）插入时映射到 NULL 列；显式空列表（`[]`）保留为
	// 空 TEXT[]（派发路径对 NULL 与 `{}` 一视同仁）。
	allowlist := normaliseComposioToolkitAllowlist(req.ComposioToolkitAllowlist)
	if !h.composioMCPAppsEnabled(r.Context()) {
		allowlist = nil
	}

	skillUUIDs, ok := parseUUIDSliceOrBadRequest(w, req.SkillIDs, "skill_ids")
	if !ok {
		return
	}
	for _, skillID := range skillUUIDs {
		if _, err := h.Queries.GetSkillInWorkspace(r.Context(), db.GetSkillInWorkspaceParams{
			ID:          skillID,
			WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "skill does not belong to this workspace")
			return
		}
	}

	avatarURL, ok := h.newAgentAvatar(w, r, req.AvatarURL)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start agent create transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	created, err := qtx.CreateAgent(r.Context(), db.CreateAgentParams{
		WorkspaceID:              wsUUID,
		Name:                     req.Name,
		Description:              req.Description,
		Instructions:             req.Instructions,
		AvatarUrl:                avatarURL,
		RuntimeMode:              runtime.RuntimeMode,
		RuntimeConfig:            rc,
		RuntimeID:                runtime.ID,
		Visibility:               perm.legacyVisibility(),
		PermissionMode:           perm.mode,
		MaxConcurrentTasks:       req.MaxConcurrentTasks,
		OwnerID:                  parseUUID(ownerID),
		CustomEnv:                ce,
		CustomArgs:               ca,
		McpConfig:                mc,
		Model:                    pgtype.Text{String: req.Model, Valid: req.Model != ""},
		ThinkingLevel:            pgtype.Text{String: req.ThinkingLevel, Valid: req.ThinkingLevel != ""},
		ServiceTier:              pgtype.Text{String: req.ServiceTier, Valid: req.ServiceTier != ""},
		ConversationStarters:     sp,
		ComposioToolkitAllowlist: allowlist,
	})
	if err != nil {
		// (workspace_id, name) 唯一约束——返回清晰的冲突错误，让 UI 显示正确
		// 信息而非通用 500。
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "agent_workspace_name_unique" {
			writeError(w, http.StatusConflict, fmt.Sprintf("an agent named %q already exists in this workspace", req.Name))
			return
		}
		slog.Warn("create agent failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to create agent: "+err.Error())
		return
	}
	if err := replaceInvocationTargetsWithQueries(r.Context(), qtx, created.ID, parseUUID(ownerID), perm.targets); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save agent access")
		return
	}
	for _, skillID := range skillUUIDs {
		if err := qtx.AddAgentSkill(r.Context(), db.AddAgentSkillParams{
			AgentID: created.ID,
			SkillID: skillID,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to attach agent skill")
			return
		}
	}
	actorType, actorID := h.resolveActor(r, ownerID, workspaceID)
	createdEvent, err := service.RecordDurableEventTx(r.Context(), qtx, h.buildAgentDomainEvent(protocol.EventAgentCreated, created, actorType, actorID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record agent event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent create")
		return
	}
	slog.Info("agent created", append(logger.RequestAttrs(r), "agent_id", uuidToString(created.ID), "name", created.Name, "workspace_id", workspaceID)...)

	if runtime.Status == "online" {
		// runtime 在线则触发一次状态协调，让新 agent 立即反映真实可达性。
		h.TaskService.ReconcileAgentStatus(r.Context(), created.ID)
		created, _ = h.Queries.GetAgent(r.Context(), created.ID)
	}

	resp := h.agentToResponse(created)
	if err := h.attachAgentSkills(r.Context(), &resp, created.ID); err != nil {
		slog.Warn("create agent: load skills for response failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(created.ID))...)
	}
	if err := h.enrichAgentResponseWithTargets(r.Context(), &resp, created.ID); err != nil {
		slog.Warn("create agent: load invocation targets for response failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(created.ID))...)
	}
	h.publishEvent(createdEvent)

	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.AgentCreated(
		ownerID,
		workspaceID,
		uuidToString(created.ID),
		runtime.Provider,
		runtime.RuntimeMode,
		req.Template,
		isFirstAgent,
	))

	redactAgentResponseForActor(&resp, actorType)
	if !h.composioMCPAppsEnabled(r.Context()) {
		suppressComposioToolkitAllowlist(&resp)
	}
	writeJSON(w, http.StatusCreated, resp)
}

// UpdateAgentRequest 是更新 agent（PUT /api/agents/{id}）的请求体。多数字段
// 为指针以支持三态（缺席/清空/设置），配合解码期捕获的 rawFields 判定键是否
// 提交。
type UpdateAgentRequest struct {
	Name                 *string                     `json:"name"`
	Description          *string                     `json:"description"`
	Instructions         *string                     `json:"instructions"`
	ConversationStarters *[]AgentConversationStarter `json:"conversation_starters"`
	AvatarURL            *string                     `json:"avatar_url"`
	RuntimeID            *string                     `json:"runtime_id"`
	RuntimeConfig        any                         `json:"runtime_config"`
	// custom_env 有意不能经本端点修改。env 变更请走 `PUT /api/agents/{id}/env`
	// ——该路径只允许 agent 所有者或工作区 owner/admin，拒绝 agent actor，且会
	// 写持久化审计日志。若 `PUT /api/agents/{id}` 的请求体携带 `custom_env`，
	// 下方 handler 返回 400 拒绝——避免调用者误以为轮换了密钥而实际没变，也避免
	// 回传之前拿到过的掩码 map 的客户端把真实密钥悄悄覆盖成字面 `****`。见 MUL-2600。
	CustomArgs *[]string        `json:"custom_args"`
	McpConfig  *json.RawMessage `json:"mcp_config"`
	Visibility *string          `json:"visibility"`
	// PermissionMode + InvocationTargets 是调用权限输入（MUL-3963）。仅所有者
	// 可写（同 composio_toolkit_allowlist）：非所有者的 admin 传它们会被静默忽略，
	// 因为调用门控基于 owner/白名单，admin 写的白名单会让所有者困惑于谁能运行
	// 自己的 agent。permission_mode 出现时权威；否则映射旧 visibility。
	PermissionMode     *string                     `json:"permission_mode"`
	InvocationTargets  *[]AgentInvocationTargetDTO `json:"invocation_targets"`
	Status             *string                     `json:"status"`
	MaxConcurrentTasks *int32                      `json:"max_concurrent_tasks"`
	Model              *string                     `json:"model"`
	// ThinkingLevel 按 MUL-2339 的三态处理：
	//   - 字段缺席 → 不变（保留现值）
	//   - 字段为 "" → 显式清除（用 runtime 默认）
	//   - 字段非空 → 设置（服务端校验）
	// 之所以用指针，正是为了区分这些模式；解码期捕获的 rawFields 告诉我们键是否
	// 被发送。
	ThinkingLevel *string `json:"thinking_level"`
	// ServiceTier 与 ThinkingLevel 同一三态契约：缺席保留、空串清除、非空设置
	// 一个 Codex 目录 ID。
	ServiceTier *string `json:"service_tier"`
	// ComposioToolkitAllowlist 是三态，模式同 thinking_level、mcp_config：
	//   - 字段缺席 → 不变（列原样保留）
	//   - 字段为 null → 显式清除（ClearAgent... 查询）
	//   - 字段为 [] → 存空 TEXT[]（已配置，无 toolkit）
	//   - 字段非空 → 存去重小写 slug
	// 解码期的 rawFields map 区分「缺席」与「显式 null」（*[]string 做不到：
	// nil 指针与两者线表示相同）。MUL-3869。
	ComposioToolkitAllowlist *[]string `json:"composio_toolkit_allowlist"`
}

// workspaceAlwaysRedactSecrets 返回工作区是否已选择对读响应的含密钥字段
// （目前是 `mcp_config`）无条件脱敏，无论调用者角色。
//
// 兼容旧工作区保留遗留 JSON 键 `always_redact_env`（在 MUL-2600 之前开启该
// 设置的）。该设置不再影响 `custom_env`——该字段已永不出现在 agent 资源上，
// 其密钥只经带审计的 `GET /api/agents/{id}/env` 拉取——因此现在只约束
// `mcp_config` 的暴露。
func workspaceAlwaysRedactSecrets(settings []byte) bool {
	if len(settings) == 0 {
		return false
	}
	var s struct {
		AlwaysRedactEnv bool `json:"always_redact_env"`
	}
	if err := json.Unmarshal(settings, &s); err != nil {
		return false
	}
	return s.AlwaysRedactEnv
}

// canViewAgentSecrets 判断请求用户能否查看 agent 的含密钥字段（目前是
// `mcp_config`）。只有 agent 所有者或工作区 owner/admin 够格，其余人拿到的
// 响应都被脱敏。`custom_env` 已不属于 agent 资源响应（见 MUL-2600），故此
// 谓词仅被剩余的 mcp_config 脱敏路径共用。
func canViewAgentSecrets(agent db.Agent, userID string, memberRole string) bool {
	if roleAllowed(memberRole, "owner", "admin") {
		return true
	}
	return uuidToString(agent.OwnerID) == userID
}

// broadcastAgentResponse 在上 WebSocket 总线前剥掉 AgentResponse 里的含密钥
// 字段。变更处理器在扇出 create/update/archive/restore 事件时调用：订阅方（含
// 用自己任务 token 鉴权的 agent 进程）不得通过绕过了 ListAgents/GetAgent 读路
// 脱敏的 WS 推送，得知别的 agent 的 mcp_config。调用者从 HTTP 响应仍拿到规范
// 形式；只有广播副本被脱敏。
//
// composio_toolkit_allowlist 遵循同一扇出规则：每个工作区成员都订阅
// agent:created/updated/archived，不脱敏的广播会把 agent 所有者逐 toolkit 的
// 白名单泄漏给每个成员，无论他们经 GET 是否有权读它。广播副本上无条件脱敏。
func broadcastAgentResponse(resp AgentResponse) AgentResponse {
	out := resp
	redactMcpConfig(&out)
	redactComposioToolkitAllowlist(&out)
	// 双保险：agentToResponse 已在每次读时掩码 gateway.token，因此响应到达本
	// 广播助手时字段已是 "***"。这里再掩一次，防止未来某次绕过 agentToResponse
	// 的重构（如新 handler 直接由裸 db.Agent 构造 AgentResponse）把 token 悄悄
	// 泄漏给工作区上每个 WS 订阅方，包括 agent 进程。
	maskGatewayToken(out.RuntimeConfig)
	return out
}

// redactMcpConfig 在调用者无权查看时移除响应里的 mcp_config：字段置 null，
// McpConfigRedacted 置 true，让调用者知道存在配置但看不到内容（可能含密钥）。
func redactMcpConfig(resp *AgentResponse) {
	if resp.McpConfig != nil {
		resp.McpConfig = nil
		resp.McpConfigRedacted = true
	}
}

// redactComposioToolkitAllowlist 在调用者非 agent 所有者时移除响应里的
// composio_toolkit_allowlist。slug 列表本身不保密，但「{所有者} 开通了什么」
// 视图会向整个工作区泄漏所有者的集成使用面——与把 mcp_config 可见性门控在
// 仅所有者的 canViewAgentSecrets 是同一个隐私顾虑。暴露粗粒度 `_redacted` 标志
// 让前端可渲染「已配置」而不展示内容（与 mcp_config_redacted 对齐）。置 nil
// 而非 `[]string{}` 很重要：JSON 的 `omitempty` 只丢弃 nil 切片。
func redactComposioToolkitAllowlist(resp *AgentResponse) {
	if resp.ComposioToolkitAllowlist != nil {
		resp.ComposioToolkitAllowlist = nil
		resp.ComposioToolkitAllowlistRedacted = true
	}
}

// suppressComposioToolkitAllowlist 在功能开关关闭时整体隐藏该字段（不置
// redacted 标志——不是「已配置但隐藏」，而是功能不存在）。
func suppressComposioToolkitAllowlist(resp *AgentResponse) {
	resp.ComposioToolkitAllowlist = nil
	resp.ComposioToolkitAllowlistRedacted = false
}

// normaliseComposioToolkitAllowlist 在持久化前规范化收到的白名单负载。每个
// slug 去空白+小写——派发路径（与 user_composio_connection.toolkit_slug 比较，
// Composio 服务存的就是小写）就能做平铺字符串匹配，无需 CITEXT 或每次查询
// LOWER()。丢弃空/纯空白串并折叠重复项，草率的 UI 负载不会浪费 DB 行长或在
// 响应里出现两次。
//
// 契约：
//   - nil 进 → nil 出：「字段缺席 / 显式 null」被保留。配合 UpdateAgent 里
//     sqlc.narg('composio_toolkit_allowlist')::text[]，「省略字段」即「不动列」。
//   - 空切片进 → 空切片出：「所有者清掉了所有 toolkit」。只在列 NULL 层面与
//     nil 有区别；派发路径把两者都当「无叠加」。
//   - 非空进 → 去空白、小写、去重、稳定顺序。
func normaliseComposioToolkitAllowlist(in []string) []string {
	if in == nil {
		return nil
	}
	if len(in) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		s := strings.ToLower(strings.TrimSpace(raw))
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// redactAgentResponseForActor 在请求 actor 是 agent 时，从 agent 资源 HTTP
// 响应里剥离含密钥字段。读 handler 已按 actorType 门控——变更 handler
// （create/update/archive/restore）必须应用同一规则，否则带着宿主 owner/admin
// token 的 agent 可以对目标 agent 做无关变更（如翻转 max_concurrent_tasks）并
// 从变更响应里捞取目标 agent 的 mcp_config。MUL-2600。
//
// composio_toolkit_allowlist 在同样逻辑下脱敏：agent 用宿主所有者的 PAT 运行，
// 对兄弟 agent 做变更时，响应里可能带出兄弟所有者的白名单。
func redactAgentResponseForActor(resp *AgentResponse, actorType string) {
	if actorType == "agent" {
		redactMcpConfig(resp)
		redactComposioToolkitAllowlist(resp)
	}
}

// canManageAgent 判断当前用户能否更新/归档 agent。只有 agent 所有者或工作区
// owner/admin 能管理任何 agent，与其 public/private 无关。失败时已写好错误响应。
func (h *Handler) canManageAgent(w http.ResponseWriter, r *http.Request, agent db.Agent) bool {
	wsID := uuidToString(agent.WorkspaceID)
	member, ok := h.requireWorkspaceRole(w, r, wsID, "agent not found", "owner", "admin", "member")
	if !ok {
		return false
	}
	isAdmin := roleAllowed(member.Role, "owner", "admin")
	isAgentOwner := uuidToString(agent.OwnerID) == requestUserID(r)
	if !isAdmin && !isAgentOwner {
		writeError(w, http.StatusForbidden, "only the agent owner can manage this agent")
		return false
	}
	return true
}

// UpdateAgent 处理 PUT /api/agents/{id}。门控与校验要点：
//   - canManageAgent（所有者或工作区 owner/admin）；换 runtime 时复用 CreateAgent
//     的 private-runtime 门控；
//   - 拒绝 custom_env 写入（400，须走 /env 端点）；
//   - thinking_level / service_tier / composio_toolkit_allowlist 均为三态；
//     composio 白名单与调用权限字段仅所有者可写（非所有者显式改权限→403，
//     原样回传→静默忽略）；
//   - 需显式清空的字段在更新后走 Clear* 专用查询，与行更新同一事务；
//   - 成功后发 agent:status 事件，响应按 actor 脱敏并重挂 skills/targets。
func (h *Handler) UpdateAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if existing.SystemKey.Valid && existing.SystemKey.String == service.SupervisionSystemKey {
		writeError(w, http.StatusForbidden, "该 Agent 由质量监督托管，请到质量监督设置中修改")
		return
	}
	if !h.canManageAgent(w, r, existing) {
		return
	}

	var req UpdateAgentRequest
	rawFields, err := decodeJSONBodyWithRawFields(r.Body, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// 硬性拒绝经通用更新端点写 custom_env。静默丢弃（`omitempty` 字段的行为）
	// 是本次改动前的旧行为，会让用户以为轮换了密钥而实际值没变。env 值只能经
	// `PUT /api/agents/{id}/env`——该端点只放行 agent 所有者或工作区 owner/admin、
	// 拒绝 agent actor、且写可查的审计行。
	if _, ok := rawFields["custom_env"]; ok {
		writeError(w, http.StatusBadRequest, "custom_env is no longer accepted on this endpoint; use PUT /api/agents/{id}/env (or `multica agent env set`)")
		return
	}

	params := db.UpdateAgentParams{
		ID: existing.ID,
	}
	if req.Name != nil {
		params.Name = pgtype.Text{String: *req.Name, Valid: true}
	}
	if req.Description != nil {
		if utf8.RuneCountInString(*req.Description) > maxAgentDescriptionLength {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("description must be %d characters or fewer", maxAgentDescriptionLength))
			return
		}
		params.Description = pgtype.Text{String: *req.Description, Valid: true}
	}
	if req.Instructions != nil {
		params.Instructions = pgtype.Text{String: *req.Instructions, Valid: true}
	}
	if req.ConversationStarters != nil {
		conversationStarters, err := normaliseAgentConversationStarters(*req.ConversationStarters)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		encoded, _ := json.Marshal(conversationStarters)
		params.ConversationStarters = encoded
	}
	if req.AvatarURL != nil {
		avatarURL, ok := h.acceptAvatarURL(w, r, *req.AvatarURL, existing.AvatarUrl.String)
		if !ok {
			return
		}
		params.AvatarUrl = pgtype.Text{String: avatarURL, Valid: true}
	}
	if req.RuntimeConfig != nil {
		// 请求把公开掩码哨兵提交到 gateway.token 时，还原持久化的网关 token。
		// 否则 UI 一旦 GET agent 再把同一负载 PATCH 回去，就会把 "***" 回写
		// 数据库，悄悄毁掉真实密钥（issue #3260）。
		preserveMaskedGatewayToken(req.RuntimeConfig, existing.RuntimeConfig)
		rc, _ := json.Marshal(req.RuntimeConfig)
		params.RuntimeConfig = rc
	}
	if req.CustomArgs != nil {
		ca, _ := json.Marshal(*req.CustomArgs)
		params.CustomArgs = ca
	}
	rawMcpConfig, hasMcpConfig := rawFields["mcp_config"]
	shouldClearMcpConfig := hasMcpConfig && bytes.Equal(bytes.TrimSpace(rawMcpConfig), []byte("null"))
	if hasMcpConfig && !shouldClearMcpConfig {
		params.McpConfig = append([]byte(nil), rawMcpConfig...)
	}

	// 解析本更新生效后的目标 runtime，让 thinking_level 校验命中正确的厂商
	// 枚举。请求不改动 runtime 时，校验 thinking_level 变更仍需要加载「当前」
	// runtime。解析一次复用。
	targetRuntimeID := existing.RuntimeID
	targetProvider := ""
	if existing.RuntimeID.Valid {
		if runtime, runtimeErr := h.getAgentRuntime(r.Context(), obsmetrics.RuntimeLookupSourceRuntimeAPI, existing.RuntimeID); runtimeErr == nil {
			targetProvider = runtime.Provider
		}
	}
	if req.RuntimeID != nil {
		runtimeUUID, ok := parseUUIDOrBadRequest(w, *req.RuntimeID, "runtime_id")
		if !ok {
			return
		}
		runtime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
			ID:          runtimeUUID,
			WorkspaceID: existing.WorkspaceID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid runtime_id")
			return
		}
		// 与 CreateAgent 相同的门控——防止用 UpdateAgent 把 agent 改绑到别人
		// 的 private runtime 上，否则就成了绕过 CreateAgent 检查的静默后门。
		member, ok := h.workspaceMember(w, r, uuidToString(existing.WorkspaceID))
		if !ok {
			return
		}
		if !canUseRuntimeForAgent(member, runtime) {
			writeError(w, http.StatusForbidden, "this runtime is private; only its owner can move agents onto it")
			return
		}
		params.RuntimeID = runtime.ID
		params.RuntimeMode = pgtype.Text{String: runtime.RuntimeMode, Valid: true}
		targetRuntimeID = runtime.ID
		targetProvider = runtime.Provider
	}
	// 调用权限（MUL-3963）。仅所有者可写：access 是工作区 admin 也不能改的
	// agent 属性（只有所有者能决定谁运行自己的 agent——叠加层用所有者自己的
	// Composio 连接，admin 改的 access 会令人困惑且不安全）。
	//
	// 非所有者行为：真正的变更以 403 拒绝，让契约显式且与仅所有者的 UI 一致
	// （非所有者的选择器只读）。no-op 重提——admin 用 PATCH-as-PUT 客户端改
	// 别的字段时把不变的权限原样回传——被容忍（丢弃），避免破坏合法 admin 编辑。
	_, hasPermissionMode := rawFields["permission_mode"]
	_, hasTargets := rawFields["invocation_targets"]
	permissionTouched := hasPermissionMode || hasTargets || req.Visibility != nil
	replacePermissionTargets := false
	var resolvedPerm resolvedPermission
	if permissionTouched {
		isAgentOwner := uuidToString(existing.OwnerID) == requestUserID(r)
		if !isAgentOwner {
			changed, permErr := h.permissionInputChangesAgent(r.Context(), existing, req, hasPermissionMode, hasTargets)
			if permErr != nil {
				writeError(w, http.StatusInternalServerError, "failed to evaluate invocation permission change")
				return
			}
			if changed {
				writeError(w, http.StatusForbidden, "only the agent owner can change access (permission_mode / invocation_targets)")
				return
			}
			slog.Debug("update agent: non-owner permission fields matched current state; ignored",
				append(logger.RequestAttrs(r), "agent_id", id)...)
		} else {
			var targetsDTO []AgentInvocationTargetDTO
			if req.InvocationTargets != nil {
				targetsDTO = *req.InvocationTargets
			}
			perm, _, permErr := parsePermissionInput(existing.WorkspaceID, req.PermissionMode, targetsDTO, hasPermissionMode, hasTargets, req.Visibility)
			if permErr != nil {
				writeError(w, http.StatusBadRequest, permErr.Error())
				return
			}
			resolvedPerm = perm
			replacePermissionTargets = true
			params.PermissionMode = pgtype.Text{String: perm.mode, Valid: true}
			params.Visibility = pgtype.Text{String: perm.legacyVisibility(), Valid: true}
		}
	}
	if req.Status != nil {
		params.Status = pgtype.Text{String: *req.Status, Valid: true}
	}
	if req.MaxConcurrentTasks != nil {
		if err := validateAgentMaxConcurrentTasks(*req.MaxConcurrentTasks); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.MaxConcurrentTasks = pgtype.Int4{Int32: *req.MaxConcurrentTasks, Valid: true}
	}
	if req.Model != nil {
		params.Model = pgtype.Text{String: *req.Model, Valid: true}
	} else if req.RuntimeID != nil && existing.Model.Valid && agent.ModelKnownIncompatibleWithProvider(targetProvider, existing.Model.String) {
		// Model 是 runtime 原生的。当 agent 跨已知厂商族迁移且调用者未另选模型时，
		// 清空旧值，让新 runtime 回退到自己的默认模型，而不是收到明显外来的模型
		// ID（如 Claude Code → Codex）。未知/自定义模型串由该助手保留。
		params.Model = pgtype.Text{String: "", Valid: true}
	}

	// Life agent 使用一对受管控的 runtime/model。普通 agent 保留常规的逐 agent
	// model 规则；本检查只适用于已绑定为 companion 或独立 observer 的 agent。
	var lifeAgent bool
	if lifeAgent, err = h.Queries.IsLifeAgent(r.Context(), db.IsLifeAgentParams{
		WorkspaceID: existing.WorkspaceID, AgentID: existing.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate Life agent")
		return
	}
	if lifeAgent {
		lifeAgentCandidate := existing
		if params.RuntimeID.Valid {
			lifeAgentCandidate.RuntimeID = params.RuntimeID
		}
		if params.Model.Valid {
			lifeAgentCandidate.Model = params.Model
		}
		selection, selectionErr := h.lifeAgentSelection(r.Context(), lifeAgentCandidate, existing.WorkspaceID)
		if selectionErr != nil {
			writeError(w, http.StatusConflict, selectionErr.Error())
			return
		}
		if selection.SetModel {
			params.Model = pgtype.Text{String: selection.Model, Valid: true}
		}
	}

	// thinking_level 处理（MUL-2339）。三态语义：
	//   - 字段缺席 → 不动列（COALESCE narg）；但如果同一请求换了 runtime，会让
	//     「既有」值对新厂商固定枚举/token 语法非法，则 400 拒绝。精确的动态目录
	//     兼容由 daemon 负责。
	//   - 字段为 "" → 显式清除（更新后执行 ClearAgentThinkingLevel）
	//   - 字段为值 → 按目标 runtime 的固定枚举或动态 token 语法校验，字面非法
	//     直接 400。逐模型组合检查在 daemon 运行时执行，不在此处。
	shouldClearThinkingLevel := false
	if req.ThinkingLevel != nil {
		value := *req.ThinkingLevel
		if value == "" {
			shouldClearThinkingLevel = true
		} else {
			// 需要目标 runtime 的 provider 来校验。仅当上面还没加载过（即请求没
			// 改 runtime_id）时才重取，让无变更路径保持一次 DB 往返。
			provider := targetProvider
			if provider == "" {
				var ok bool
				provider, ok = h.resolveAgentProvider(r, existing.WorkspaceID, targetRuntimeID)
				if !ok {
					writeError(w, http.StatusInternalServerError, "failed to resolve runtime for thinking_level validation")
					return
				}
			}
			if !agent.IsKnownThinkingValue(provider, value) {
				writeError(w, http.StatusBadRequest, thinkingLevelRejection(provider, value))
				return
			}
			switch h.acpThinkingDecision(r.Context(), provider, targetRuntimeID) {
			case acpEffortAbsent:
				writeError(w, http.StatusBadRequest, thinkingCapabilityRejection(provider))
				return
			case acpEffortUnknown:
				writeError(w, http.StatusBadRequest, thinkingCapabilityUnknownRejection(provider))
				return
			}
			params.ThinkingLevel = pgtype.Text{String: value, Valid: true}
		}
	} else if req.RuntimeID != nil && existing.ThinkingLevel.Valid && existing.ThinkingLevel.String != "" {
		// runtime 在变但调用者没碰 thinking_level。若既有值不在新厂商的枚举里，
		// 保留它等于把一个字面非法的 token 偷运给 daemon。与显式设置路径同一
		// 条线：字面非法一律 400，绝不静默强改。调用者可传 `thinking_level: ""`
		// 清除，或挑一个对新 runtime 有效的值。
		provider := targetProvider
		if provider == "" {
			var ok bool
			provider, ok = h.resolveAgentProvider(r, existing.WorkspaceID, targetRuntimeID)
			if !ok {
				writeError(w, http.StatusInternalServerError, "failed to resolve runtime for thinking_level validation")
				return
			}
		}
		if !agent.IsKnownThinkingValue(provider, existing.ThinkingLevel.String) {
			writeError(w, http.StatusBadRequest, existingThinkingLevelRejection(provider, existing.ThinkingLevel.String))
			return
		}
		switch h.acpThinkingDecision(r.Context(), provider, targetRuntimeID) {
		case acpEffortAbsent:
			writeError(w, http.StatusBadRequest, existingThinkingCapabilityRejection(provider, existing.ThinkingLevel.String))
			return
		case acpEffortUnknown:
			writeError(w, http.StatusBadRequest, existingThinkingCapabilityUnknownRejection(provider, existing.ThinkingLevel.String))
			return
		}
	}

	shouldClearServiceTier := false
	if req.ServiceTier != nil {
		value := *req.ServiceTier
		if value == "" {
			shouldClearServiceTier = true
		} else {
			provider := targetProvider
			if provider == "" {
				var ok bool
				provider, ok = h.resolveAgentProvider(r, existing.WorkspaceID, targetRuntimeID)
				if !ok {
					writeError(w, http.StatusInternalServerError, "failed to resolve runtime for service_tier validation")
					return
				}
			}
			if !agent.IsKnownServiceTier(provider, value) {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("service_tier %q is not a recognised value for runtime %q", value, provider))
				return
			}
			params.ServiceTier = pgtype.Text{String: value, Valid: true}
		}
	} else if req.RuntimeID != nil && existing.ServiceTier.Valid && existing.ServiceTier.String != "" {
		provider := targetProvider
		if provider == "" {
			var ok bool
			provider, ok = h.resolveAgentProvider(r, existing.WorkspaceID, targetRuntimeID)
			if !ok {
				writeError(w, http.StatusInternalServerError, "failed to resolve runtime for service_tier validation")
				return
			}
		}
		if !agent.IsKnownServiceTier(provider, existing.ServiceTier.String) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"existing service_tier %q is not valid for runtime %q; pass service_tier=\"\" to clear or set a value valid for the new runtime",
				existing.ServiceTier.String, provider,
			))
			return
		}
	}

	// composio_toolkit_allowlist 处理（MUL-3869）。三态语义镜像 thinking_level
	// （见上）：缺席 → 不变，null → ClearAgentComposioToolkitAllowlist，切片 →
	// 整体替换。
	//
	// 仅所有者可写。调用者已过 canManageAgent（工作区 owner/admin 与 agent 所有者
	// 都能过），但 Composio 叠加层用的是 agent 所有者的连接（MUL-3963）——admin
	// 改别人的白名单，等于悄悄重塑「所有者通过自己的已连接应用暴露了什么」，
	// 让所有者困惑。保持仅所有者。
	// 用 debug 日志丢弃而不是报错：把整个 agent 负载每次保存都原样发回的过度
	// 积极的 UI（PATCH-as-PUT）能继续工作——即 issue 点名的「静默忽略」立场，
	// 也是 mcp_config 对更广的 admin 模式采取的同一立场。
	shouldClearComposioAllowlist := false
	if _, hasAllowlist := rawFields["composio_toolkit_allowlist"]; hasAllowlist {
		isAgentOwner := uuidToString(existing.OwnerID) == requestUserID(r)
		if !h.composioMCPAppsEnabled(r.Context()) {
			slog.Debug("update agent: composio_toolkit_allowlist write dropped because feature flag is disabled",
				append(logger.RequestAttrs(r), "agent_id", id)...)
		} else if !isAgentOwner {
			slog.Debug("update agent: composio_toolkit_allowlist write by non-owner silently dropped",
				append(logger.RequestAttrs(r), "agent_id", id)...)
		} else if req.ComposioToolkitAllowlist == nil {
			// JSON null → 走专用查询显式清除。
			shouldClearComposioAllowlist = true
		} else {
			// 规范化（去空白/小写/去重）。空切片保留为空 TEXT[]，使持久化值能
			// 区分「所有者清掉了所有 toolkit」与「所有者从未开通集成」（派发路径
			// 两者都当「无叠加」，但列能告诉 UX 展示已就绪还是空选择器）。
			params.ComposioToolkitAllowlist = normaliseComposioToolkitAllowlist(*req.ComposioToolkitAllowlist)
		}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start agent update transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	updated, err := qtx.UpdateAgent(r.Context(), params)
	if err != nil {
		// (workspace_id, name) 唯一约束——镜像 CreateAgent 返回清晰的冲突而不是
		// 泄漏裸约束名的 500。名字仍可能被「已归档」agent 占用（约束不排除归档
		// 行），所以这是调用者判断「改名撞了」而非服务端故障的唯一信号。
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "agent_workspace_name_unique" {
			name := ""
			if req.Name != nil {
				name = *req.Name
			}
			writeError(w, http.StatusConflict, fmt.Sprintf("an agent named %q already exists in this workspace", name))
			return
		}
		slog.Warn("update agent failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to update agent: "+err.Error())
		return
	}

	// 可空 runtime 覆盖项：请求里 null/空表示显式清空字段。UpdateAgent 的
	// COALESCE 无法把列设为 NULL，因此 mcp_config、thinking_level、service_tier
	// 用专用清空查询。
	if shouldClearMcpConfig {
		updated, err = qtx.ClearAgentMcpConfig(r.Context(), updated.ID)
		if err != nil {
			slog.Warn("clear agent mcp_config failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to clear mcp_config: "+err.Error())
			return
		}
	}
	if shouldClearThinkingLevel {
		updated, err = qtx.ClearAgentThinkingLevel(r.Context(), updated.ID)
		if err != nil {
			slog.Warn("clear agent thinking_level failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to clear thinking_level: "+err.Error())
			return
		}
	}
	if shouldClearServiceTier {
		updated, err = qtx.ClearAgentServiceTier(r.Context(), updated.ID)
		if err != nil {
			slog.Warn("clear agent service_tier failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to clear service_tier: "+err.Error())
			return
		}
	}
	if shouldClearComposioAllowlist {
		updated, err = qtx.ClearAgentComposioToolkitAllowlist(r.Context(), updated.ID)
		if err != nil {
			slog.Warn("clear agent composio_toolkit_allowlist failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to clear composio_toolkit_allowlist: "+err.Error())
			return
		}
	}

	// 调用 targets（MUL-3963）：所有者动权限时整体替换。放在行更新之后，让
	// permission_mode 翻转与 targets 一起落库。
	if replacePermissionTargets {
		if err := replaceInvocationTargetsWithQueries(r.Context(), qtx, updated.ID, parseUUID(requestUserID(r)), resolvedPerm.targets); err != nil {
			slog.Warn("update agent: persist invocation targets failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to update invocation targets: "+err.Error())
			return
		}
	}
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, uuidToString(updated.WorkspaceID))
	updatedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, h.buildAgentDomainEvent(protocol.EventAgentStatus, updated, actorType, actorID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record agent event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent update")
		return
	}

	resp := h.agentToResponse(updated)
	if err := h.enrichAgentResponseWithTargets(r.Context(), &resp, updated.ID); err != nil {
		slog.Warn("update agent: load invocation targets for response failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to load agent invocation targets")
		return
	}
	// agentToResponse 恒把 Skills 初始化为 []；SQL 更新不碰关联表行，所以这里
	// 重载一遍，让响应（以及镜像它的广播）与现实同步。否则每次纯元数据更新后
	// 调用者都会看到 "skills": []，误以为绑定被清掉了——见 #3459。
	if err := h.attachAgentSkills(r.Context(), &resp, updated.ID); err != nil {
		slog.Warn("load agent skills after update failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to load agent skills")
		return
	}
	slog.Info("agent updated", append(logger.RequestAttrs(r), "agent_id", id, "workspace_id", uuidToString(updated.WorkspaceID))...)
	h.publishEvent(updatedEvent)
	redactAgentResponseForActor(&resp, actorType)
	// 工作区 admin / 非所有者成员能过 canManageAgent 做合法的管理动作（如批量把
	// agent 从离开成员的 runtime 重指派出去），但他们不得从变更响应里获知 agent
	// 所有者的 composio 白名单。与 ListAgents/GetAgent 同一门控。
	if !h.composioMCPAppsEnabled(r.Context()) {
		suppressComposioToolkitAllowlist(&resp)
	} else if uuidToString(updated.OwnerID) != userID {
		redactComposioToolkitAllowlist(&resp)
	}
	writeJSON(w, http.StatusOK, resp)
}

// attachAgentSkills 从 agent_skill 关联表为给定 agent 填充 resp.Skills。
// agentToResponse 会把该字段清零；不刷新它的变更 handler 会在每次成功响应里
// 端出一个误导性的空数组（#3459）。
func (h *Handler) attachAgentSkills(ctx context.Context, resp *AgentResponse, agentID pgtype.UUID) error {
	skills, err := h.Queries.ListAgentSkillSummaries(ctx, agentID)
	if err != nil {
		return err
	}
	if len(skills) == 0 {
		return nil
	}
	out := make([]AgentSkillSummary, len(skills))
	for i, s := range skills {
		out[i] = AgentSkillSummary{
			ID:          uuidToString(s.ID),
			Name:        s.Name,
			Description: s.Description,
			Enabled:     s.Enabled,
		}
	}
	resp.Skills = out
	return nil
}

// resolveAgentProvider 返回在途更新生效后、将拥有该 agent 的 runtime 的厂商名。
// thinking_level 校验器用它，保证同一请求里 runtime/model 交换与档位交换都对着
// 同一个厂商。
func (h *Handler) resolveAgentProvider(r *http.Request, workspaceID pgtype.UUID, runtimeID pgtype.UUID) (string, bool) {
	rt, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
		ID:          runtimeID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return "", false
	}
	return rt.Provider, true
}

// thinkingLevelRejection 解释为何目标 runtime 不接受这个 thinking_level。
// 过去两种失败共用一句话：目录里没有的 token，以及根本没有推理控制的 runtime。
// 后者让 "high" 看着像拼写错误，用户会去找该 runtime 并不存在的值（MUL-5770），
// 所以现在把能力缺口单独说清楚。
func thinkingLevelRejection(provider, value string) string {
	if !agent.ThinkingControlSupported(provider) {
		return thinkingCapabilityRejection(provider)
	}
	return fmt.Sprintf("thinking_level %q is not a recognised value for runtime %q", value, provider)
}

// existingThinkingCapabilityRejection 是携旧值路径的能力提示句——与
// thinkingCapabilityRejection 同答案，但点出 agent 上已有的值和清除它的逃生口。
func existingThinkingCapabilityRejection(provider, value string) string {
	return fmt.Sprintf(
		"runtime %q does not support a per-agent reasoning effort; pass thinking_level=\"\" to clear the existing %q",
		provider, value,
	)
}

// thinkingCapabilityUnknownRejection 覆盖歧义情况：厂商名说明不了装了哪个
// 二进制，目录也尚未上报，因此既不能确认也不能否认该能力。
//
// 它有意不复用「不支持」那句话。对 jcode runtime 那样说会主动误导——让它的
// 所有者去找一个不存在的限制——而点出「缺失证据」则指向解决它的那个动作。
func thinkingCapabilityUnknownRejection(provider string) string {
	return fmt.Sprintf(
		"cannot confirm whether runtime %q supports a per-agent reasoning effort: it has not reported a model catalog yet. Open the model picker for this runtime to trigger discovery and retry, or leave thinking_level empty to use the runtime default",
		provider,
	)
}

// existingThinkingCapabilityUnknownRejection 是携旧值路径的同答案版本：点出
// agent 上已有的值和逃生口。
func existingThinkingCapabilityUnknownRejection(provider, value string) string {
	return fmt.Sprintf(
		"cannot confirm whether runtime %q supports a per-agent reasoning effort: it has not reported a model catalog yet. Pass thinking_level=\"\" to clear the existing %q, or retry once the runtime has reported its models",
		provider, value,
	)
}

// thinkingCapabilityRejection 是「该 runtime 没有推理旋钮」的提示句。单独抽出
// 是因为同一答案现在有两个来源：仅凭厂商名，或——对厂商名不具决定性的 ACP
// 目录厂商——凭一个宣称无 effort 的已发现目录。
func thinkingCapabilityRejection(provider string) string {
	return fmt.Sprintf(
		"runtime %q does not support a per-agent reasoning effort; leave thinking_level empty to use the runtime default",
		provider,
	)
}

// acpEffortEvidence 是已发现模型目录对 runtime 推理档位支持的说法。
type acpEffortEvidence int

const (
	// acpEffortUnknown —— 该 runtime 尚无已发现目录。
	//
	// 这不是瞬时冷启动态。目录只在 ReportModelListResult 之后才会写入，即只在
	// 客户端显式要模型列表后才有——从不打开模型选择器的调用者（纯 CLI 使用）
	// 可能一直停在这里。因此把 unknown 当「支持」就不是短暂窗口——对这类用户
	// 是永久漏洞。
	acpEffortUnknown acpEffortEvidence = iota
	// acpEffortAbsent —— 目录存在，但没有模型宣称支持 effort。
	acpEffortAbsent
	// acpEffortPresent —— 目录存在，且至少一个模型宣称支持。
	acpEffortPresent
)

// ambiguousACPEffortProviders 是其厂商名无法确定实际安装了哪个二进制的厂商，
// 因此「尚未发现」不能被读作「支持」。
//
// 只有 `hermes`：它涵盖 jcode（宣称支持 effort 且会应用）与 Hermes Agent
// （宣称不支持）。reasonix 有意缺席——该厂商只对应一个支持 effort 的二进制，
// 未发现的 reasonix runtime 应安全放行，而不是在其首次发现前就被拦。
var ambiguousACPEffortProviders = map[string]bool{
	"hermes": true,
}

// acpThinkingDecision 回答该 runtime 是否可携带 thinking level，依据其 daemon
// 上报的模型目录。
//
// acpEffortPresent 表示「允许」。ACP 目录集合之外的厂商，以及无目录但无歧义的
// ACP 厂商，也报告为 present——其能力已由厂商名落定。只有歧义厂商才会把「未
// 发现目录」变成拒绝，因为对这些厂商名真的回答不了问题，猜「支持」正是当初让
// Hermes Agent 用户持久化了一个 daemon 之后会丢弃的档位的原因。
func (h *Handler) acpThinkingDecision(ctx context.Context, provider string, runtimeID pgtype.UUID) acpEffortEvidence {
	if !agent.UsesACPCatalogThinking(provider) {
		return acpEffortPresent
	}
	snapshot := h.cachedModelCatalog(ctx, uuidToString(runtimeID))
	if snapshot == nil || len(snapshot.Models) == 0 {
		if ambiguousACPEffortProviders[provider] {
			return acpEffortUnknown
		}
		return acpEffortPresent
	}
	for _, m := range snapshot.Models {
		if m.Thinking != nil && len(m.Thinking.SupportedLevels) > 0 {
			return acpEffortPresent
		}
	}
	return acpEffortAbsent
}

// existingThinkingLevelRejection 是携旧值路径的 thinkingLevelRejection——调用者
// 换了 runtime 却未动新 runtime 收不下的档位。两个分支都指向同一逃生口，用户
// 不必猜测能否清除。
func existingThinkingLevelRejection(provider, value string) string {
	if !agent.ThinkingControlSupported(provider) {
		return existingThinkingCapabilityRejection(provider, value)
	}
	return fmt.Sprintf(
		"existing thinking_level %q is not valid for runtime %q; pass thinking_level=\"\" to clear or set a value valid for the new runtime",
		value, provider,
	)
}

// ArchiveAgent 处理 PUT/POST /api/agents/{id}/archive。需要 canManageAgent；
// 已归档→409；系统 agent 属于产品不可归档。归档行更新与事件记录在同一事务，
// 成功后取消该 agent 全部待办/运行中任务（带委托失败的结算一起提交）。
func (h *Handler) ArchiveAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "agent is already archived")
		return
	}

	// 系统 agent 属于产品而非工作区，整个工作区的入口都经过它。归档它会让它从
	// 所有列表消失而行仍在——还会卡住 bootstrap 端点：其查询跳过已归档行，但
	// 唯一索引不排除。
	if agent.SystemKey.Valid && agent.SystemKey.String != "" {
		writeError(w, http.StatusBadRequest, "this agent is built into Multica and cannot be archived")
		return
	}

	userID := requestUserID(r)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start agent archive transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	archived, err := qtx.ArchiveAgent(r.Context(), db.ArchiveAgentParams{
		ID:         agent.ID,
		ArchivedBy: parseUUID(userID),
	})
	if err != nil {
		slog.Warn("archive agent failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to archive agent")
		return
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(archived.WorkspaceID))
	archivedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, h.buildAgentDomainEvent(protocol.EventAgentArchived, archived, actorType, actorID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record agent event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent archive")
		return
	}

	// 取消该 agent 全部待办/运行中任务。取消与其委托失败的结算一起提交——结算
	// 若在取消提交之后发出，就永远无法修复了。逐任务的 task:cancelled 事件仍被
	// 跳过：下方的 agent:archived 事件已经会触发每个已连接客户端对活跃任务列表的
	// 全量失效。
	if cancelled, err := h.TaskService.CancelTasksForArchivedAgent(r.Context(), agent.ID); err != nil {
		slog.Warn("cancel agent tasks on archive failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
	} else {
		h.TaskService.CaptureCancelledTasks(r.Context(), cancelled)
	}

	wsID := uuidToString(archived.WorkspaceID)
	slog.Info("agent archived", append(logger.RequestAttrs(r), "agent_id", id, "workspace_id", wsID)...)
	resp := h.agentToResponse(archived)
	if err := h.attachAgentSkills(r.Context(), &resp, archived.ID); err != nil {
		slog.Warn("load agent skills after archive failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to load agent skills")
		return
	}
	h.publishEvent(archivedEvent)
	redactAgentResponseForActor(&resp, actorType)
	writeJSON(w, http.StatusOK, resp)
}

// RestoreAgent 处理 POST /api/agents/{id}/restore。需要 canManageAgent；未归档
// →409。行更新与事件记录同一事务；成功后发 agent:restored 事件。
func (h *Handler) RestoreAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	if !agent.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "agent is not archived")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start agent restore transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	restored, err := qtx.RestoreAgent(r.Context(), agent.ID)
	if err != nil {
		slog.Warn("restore agent failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to restore agent")
		return
	}

	wsID := uuidToString(restored.WorkspaceID)
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, wsID)
	restoredEvent, err := service.RecordDurableEventTx(r.Context(), qtx, h.buildAgentDomainEvent(protocol.EventAgentRestored, restored, actorType, actorID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record agent event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent restore")
		return
	}
	slog.Info("agent restored", append(logger.RequestAttrs(r), "agent_id", id, "workspace_id", wsID)...)
	resp := h.agentToResponse(restored)
	if err := h.attachAgentSkills(r.Context(), &resp, restored.ID); err != nil {
		slog.Warn("load agent skills after restore failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to load agent skills")
		return
	}
	h.publishEvent(restoredEvent)
	redactAgentResponseForActor(&resp, actorType)
	writeJSON(w, http.StatusOK, resp)
}

// CancelAgentTasks 批量取消属于某个 agent 的全部活跃任务（queued/dispatched/
// running）。支撑 agents 列表的「取消全部任务」行操作。权限门控与归档相同
// （canManageAgent——所有者或工作区 admin/owner）。每行取消都会触发 task:cancelled
// WS 事件，让已连接客户端立即清掉活动卡片。
//
// 注意：daemon 侧的 `running` 任务不会真正立刻停下，最多滞后约 5 秒（daemon 以
// 该间隔轮询 GetTaskStatus）。DB 行立即标为已取消，但子进程还会短暂继续；见
// daemon/daemon.go:919-942 的轮询循环。请在确认对话框文案里点明这一点，用户
// 才不会被后面拖尾的转录行吓到。
type cancelAgentTasksResponse struct {
	Cancelled int `json:"cancelled"`
}

// CancelAgentTasks 处理 POST /api/agents/{id}/cancel-tasks。门控见上方类型注释；
// 返回取消的任务条数。
func (h *Handler) CancelAgentTasks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}

	cancelled, err := h.TaskService.CancelTasksForAgent(r.Context(), parseUUID(id))
	if err != nil {
		slog.Warn("cancel agent tasks failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to cancel tasks")
		return
	}

	slog.Info("agent tasks cancelled",
		append(logger.RequestAttrs(r), "agent_id", id, "count", len(cancelled))...)
	writeJSON(w, http.StatusOK, cancelAgentTasksResponse{Cancelled: len(cancelled)})
}

// ListAgentTasks 处理 GET /api/agents/{id}/tasks，返回该 agent 的运行历史。
// 运行历史属于 private-agent 门控的一部分（「查看历史会话」），无权限返回 403。
func (h *Handler) ListAgentTasks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	// 运行历史是 private-agent 门控的一部分（「查看历史会话」）。403 语义同
	// GetAgent。
	workspaceID := uuidToString(agent.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return
	}

	tasks, err := h.Queries.ListAgentTasks(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent tasks")
		return
	}

	resp := make([]AgentTaskResponse, len(tasks))
	for i, t := range tasks {
		resp[i] = taskToResponse(t, workspaceID)
	}
	h.hydrateTaskAttributions(r.Context(), attributionsOf(resp))

	writeJSON(w, http.StatusOK, resp)
}

// AgentActivityBucket 是 Agents 列表 ACTIVITY 迷你趋势图里的一个按天分桶的吞吐
// 采样。bucket_at 是该日的 UTC 零点。
type AgentActivityBucket struct {
	AgentID     string `json:"agent_id"`
	BucketAt    string `json:"bucket_at"`
	TaskCount   int32  `json:"task_count"`
	FailedCount int32  `json:"failed_count"`
}

// AgentRunCount 是每个 agent 近 30 天任务运行总数，支撑 Agents 列表 RUNS 列。
type AgentRunCount struct {
	AgentID  string `json:"agent_id"`
	RunCount int32  `json:"run_count"`
}

// WorkspaceWorkingAgent 是工作区 working-agents 端点返回的隐私安全 agent 摘要。
// 只携带展示信息加当前运行任务数与引用的 issue id；完整 AgentResponse 里含
// runtime 与集成配置，这个卡片用不上。
type WorkspaceWorkingAgent struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	AvatarURL        *string  `json:"avatar_url"`
	RunningTaskCount int32    `json:"running_task_count"`
	IssueIDs         []string `json:"issue_ids"`
}

// ListWorkspaceWorkingAgents 返回工作区当前在工作的用户创建 agent，不依赖
// issue 筛选或表格分页。可选 type 查询选择 issue、autopilot 或 chat 工作；省略
// 则保持全来源投影。scope=mine 把 issue 工作收窄到认证成员选的 My Issues 关系。
// 访问过滤与其它工作区级 agent 聚合一致，private/非白名单 agent 绝不以名字、
// 头像、数量甚至存在性暴露。
func (h *Handler) ListWorkspaceWorkingAgents(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	workType := strings.TrimSpace(r.URL.Query().Get("type"))
	switch workType {
	case "", "issue", "autopilot", "chat":
	default:
		writeError(w, http.StatusBadRequest, "invalid type: must be issue, autopilot, or chat")
		return
	}

	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	mineRelation := strings.TrimSpace(r.URL.Query().Get("relation"))
	var memberID pgtype.UUID
	switch scope {
	case "":
		if mineRelation != "" {
			writeError(w, http.StatusBadRequest, "relation requires scope=mine")
			return
		}
	case "mine":
		if workType != "issue" {
			writeError(w, http.StatusBadRequest, "scope=mine requires type=issue")
			return
		}
		if mineRelation == "" {
			mineRelation = "any"
		}
		switch mineRelation {
		case "assigned", "created", "involved", "any":
		default:
			writeError(w, http.StatusBadRequest, "invalid relation: must be assigned, created, involved, or any")
			return
		}
		userID, ok := requireUserID(w, r)
		if !ok {
			return
		}
		var err error
		memberID, err = util.ParseUUID(userID)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "user not authenticated")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid scope: must be mine")
		return
	}

	// 把投影收窄到一个 issue 的直接子 issue，让 issue 详情页的子 issue 头部直接
	// 读本端点，而不是在客户端推算计数。缺席时留零，查询把它当 NULL 即「不收窄」
	// ——从不发它的旧客户端保持与原来完全一致的工作区级行为。
	var parentIssueID pgtype.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("parent")); raw != "" {
		if workType != "issue" {
			writeError(w, http.StatusBadRequest, "parent requires type=issue")
			return
		}
		if scope != "" {
			writeError(w, http.StatusBadRequest, "parent cannot be combined with scope")
			return
		}
		var ok bool
		parentIssueID, ok = parseUUIDOrBadRequest(w, raw, "parent")
		if !ok {
			return
		}
	}

	rows, err := h.Queries.ListWorkspaceWorkingAgents(
		r.Context(),
		db.ListWorkspaceWorkingAgentsParams{
			WorkspaceID:   parseUUID(workspaceID),
			WorkType:      workType,
			MineRelation:  mineRelation,
			MemberID:      memberID,
			ParentIssueID: parentIssueID,
		},
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workspace working agents")
		return
	}

	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	resp := make([]WorkspaceWorkingAgent, 0, len(rows))
	for _, row := range rows {
		agentID := uuidToString(row.ID)
		if _, ok := allowed[agentID]; !ok {
			continue
		}
		resp = append(resp, WorkspaceWorkingAgent{
			ID:               agentID,
			Name:             row.Name,
			AvatarURL:        h.resolveAvatarURLPtr(textToPtr(row.AvatarUrl)),
			RunningTaskCount: row.RunningTaskCount,
			IssueIDs:         uuidStringsOrEmpty(row.IssueIds),
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetWorkspaceAgentRunCounts 返回工作区每个 agent 的 30 天运行总数。与
// live-tasks/activity 相同的单次拉取模式，保证无论 agent 多少 Agents 列表都廉价。
func (h *Handler) GetWorkspaceAgentRunCounts(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	rows, err := h.Queries.GetWorkspaceAgentRunCounts(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get agent run counts")
		return
	}

	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	resp := make([]AgentRunCount, 0, len(rows))
	for _, row := range rows {
		agentID := uuidToString(row.AgentID)
		if _, ok := allowed[agentID]; !ok {
			continue
		}
		resp = append(resp, AgentRunCount{
			AgentID:  agentID,
			RunCount: row.RunCount,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetWorkspaceAgentActivity30d 返回每个 agent 最近 30 天、以 completed_at 锚定的
// 逐日任务数。一次工作区级读取同时支撑 Agents 列表的迷你趋势图（用最近 7 桶）
// 与 agent 详情的「最近 30 天」面板（用全部 30 桶）——一次拉取比两次便宜。
// 缺天的桶由前端填零；后端省略空桶保持响应小巧。
func (h *Handler) GetWorkspaceAgentActivity30d(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	rows, err := h.Queries.GetWorkspaceAgentActivity30d(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get agent activity")
		return
	}

	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	resp := make([]AgentActivityBucket, 0, len(rows))
	for _, row := range rows {
		agentID := uuidToString(row.AgentID)
		if _, ok := allowed[agentID]; !ok {
			continue
		}
		resp = append(resp, AgentActivityBucket{
			AgentID:     agentID,
			BucketAt:    timestampToString(row.Bucket),
			TaskCount:   row.TaskCount,
			FailedCount: row.FailedCount,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// ListWorkspaceAgentTaskSnapshot 返回前端读取的工作区级任务数据，用于两件事：
// 每个活跃任务（queued/dispatched/running/waiting_local_directory）——当前
// workload 存在性由此而来；以及每个 agent 最近一次「结果」任务（仅 completed/
// failed）——自 #1823 起不再是存在性的一部分，只喂 Squad 悬浮卡片的「最近活动」
// 行。结果半边按设计排除 cancelled——取消是程序性信号（「尝试中止」），不是结果，
// 不能盖住先前的失败。逐 agent 过滤在前端针对该快照进行。
//
// 结果半边有意仍在这里提供，让已发布的桌面版继续工作；MUL-5436 跟踪把它移到
// 专门的懒加载端点。
func (h *Handler) ListWorkspaceAgentTaskSnapshot(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	tasks, err := h.Queries.ListWorkspaceAgentTaskSnapshot(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent task snapshot")
		return
	}

	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	resp := make([]AgentTaskResponse, 0, len(tasks))
	for _, t := range tasks {
		if _, ok := allowed[uuidToString(t.AgentID)]; !ok {
			continue
		}
		resp = append(resp, taskToResponse(t, workspaceID))
	}
	h.hydrateTaskAttributions(r.Context(), attributionsOf(resp))

	writeJSON(w, http.StatusOK, resp)
}
