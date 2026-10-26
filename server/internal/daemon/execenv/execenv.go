// Package execenv 为 daemon 管理「每个任务一个隔离执行环境」的创建与回收。
//
// 角色定位：每个任务会分到一个专属目录（env root），里面会注入描述任务背景的
// 上下文文件（issue_context.md、技能目录等）。代码仓库不在这里预先检出，而是由
// agent 在运行中按需执行 `multica repo checkout` 拉取，所以新环境的 workdir 初始
// 是空的。
//
// 职责边界：本包只负责「环境目录」本身——目录的命名/创建/抢占加锁/清理，以及
// 按 provider（codex、claude、hermes、qwenpaw、reasonix、cursor、omp 等）生成对
// 应的 per-task 配置目录（CODEX_HOME、HERMES_HOME overlay、Cursor 数据目录等）。
// 不负责真正拉起 agent 进程，也不负责执行用户代码。
//
// 核心不变量：同一 env root 同一时刻只能被一次执行持有——靠目录内 .task_owner
// 身份标记 + .task_lock 排他锁（OS 建议锁）双重保证；持有锁的进程无论正常退出
// 还是崩溃，内核都会自动释放锁，因此锁能回答「上次执行是否还活着」。环境的归属
// 判断一律依据写入目录的 identity 文件，而不是目录名。
package execenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/runtimeapps"
)

// RepoContextForEnv 描述一个可供本任务 checkout 的工作区代码仓库，渲染进上下文
// 文件后由 agent 决定何时通过 `multica repo checkout` 拉取。
type RepoContextForEnv struct {
	URL         string // 远端仓库地址
	Description string // 仓库用途说明（可选）
	Ref         string // 本任务默认要检出的分支/commit（可选）
}

// ProjectResourceForEnv 描述挂在 issue 项目上的一个外部资源（如 github 仓库）。
//
// resource_ref 里是随类型而异的原始 JSON，agent 直接去读磁盘上的 resources.json
// 就能拿到完整结构，这里只保留 meta-skill 模板渲染「人可读摘要」所需的字段：
// github_repo 展示 URL，其余类型展示通用 Label。
type ProjectResourceForEnv struct {
	ID           string          `json:"id"`              // 服务端分配的 UUID
	ResourceType string          `json:"resource_type"`   // 如 "github_repo"
	ResourceRef  json.RawMessage `json:"resource_ref"`    // API 返回的原始 JSONB 载荷
	Label        string          `json:"label,omitempty"` // 用户可选填的展示标签
}

// PrepareParams 汇集了搭建一个任务执行环境所需的全部输入。
//
// 既有稳定的身份字段（workspace/task UUID，用作路径后缀保证不撞名），也有人类
// 可读、仅供展示的路径前缀；还携带各 agent provider 准备 per-task 配置目录时
// 所需的开关与数据（如 EnvRootPreclaimed、Hermes*、CursorMcpAuthSource 等）。
type PrepareParams struct {
	WorkspacesRoot  string // 所有任务环境的根目录（如 ~/multica_workspaces）
	WorkspaceID     string // workspace UUID——稳定的身份标识，也是路径后缀
	WorkspaceSlug   string // 人类可读的 workspace 路径前缀（仅展示，不作身份）
	TaskID          string // task UUID——稳定的身份标识，也是路径后缀
	IssueIdentifier string // 人类可读的 issue 键（如 MUL-6063）；非 issue 任务为空
	AgentName       string // 仅供 git 分支命名使用
	// EnvRootPreclaimed 表示调用方已经持有并重置过该环境根目录的占用
	// （参见 ClaimEnvRoot），Prepare 就不再自行抢占。
	//
	// 之所以需要这个开关：生产环境的准备动作跑在短命的辅助进程里
	// （PrepareIsolated），在该进程内拿到的锁会在它一退出就被内核释放，而
	// *os.File 又无法穿过 JSON 边界带回 daemon 进程。所以锁必须由父进程
	// 持有——父进程的存活期才是任务真正运行的时长。
	EnvRootPreclaimed bool
	// Profile 是 daemon 的 profile 名（空 = 默认）。它把「每个 issue 的 Codex
	// 会话存储」按 profile 隔离命名空间，这样另一个共用同一个 ~/.codex 的
	// profile-daemon 看不到、也回收不掉本 daemon 的会话存储（MUL-4424）。
	Profile      string
	Provider     string // agent 提供商（决定走哪套运行时配置与技能注入路径）
	CodexVersion string // 探测到的 Codex CLI 版本（仅 Provider == "codex" 时使用）
	// McpConfig 是 agent 保存的 `mcp_config` JSON。当某个 provider 通过 per-task
	// 配置文件把 MCP 落地时，这份 JSON 会被转发给对应 provider 的配置准备逻辑。
	// Cursor、OMP 在这里消费它；其余 provider 的 MCP 由 agent 后端的
	// ExecOptions.McpConfig 接线，不经过这里。
	McpConfig json.RawMessage
	// CursorMcpAuthSource 是显式指向 Cursor mcp-auth.json 的路径（或包含该文件
	// 的 Cursor 项目数据目录）。只有 Cursor 的 managed MCP 路径消费它。
	CursorMcpAuthSource string
	// LocalWorkDir 非空时，把 agent 的工作目录从自动生成的 envRoot/workdir 换成
	// 用户指定的绝对路径。该路径不会被复制或挂载——agent 直接在用户目录上操作。
	// daemon 仍会创建 envRoot 放 output/、logs/ 与 .gc_meta.json，只是 workdir
	// 这一格被替换。用于 local_directory 型 project_resource 流程（MUL-2663）。
	// 设置后不再创建 envRoot/workdir 目录。
	LocalWorkDir string
	// LocalWorktree 非 nil 时走 worktree 模式，是 LocalWorkDir 的对照方案：
	// 任务不直接跑在用户目录里，而是在 envRoot 内为该仓库建一个专属 git
	// worktree，并把成果以分支交付。这样共享同一目录的多个任务可以并发跑。
	// 与 LocalWorkDir 互斥——daemon 按资源的 execution_mode 二选一。
	LocalWorktree *LocalWorktreeParams
	// HermesSourceHome 是共享的 Hermes home，per-task overlay 以它为模板播种。
	// daemon 通过 execenv.ResolveHermesProfile 解析它，从而尊重 agent custom_env
	// 里的 HERMES_HOME 以及 -p/--profile 或粘性选择。仅 hermes provider 使用；
	// 为空则退回平台默认值。
	HermesSourceHome string
	// HermesSourceMustExist 让 overlay 构建在 HermesSourceHome 缺失时「关闭即失败」。
	// 只有显式指定了具名 profile 时才置位：这样拼写错误不会静默地从一个空 home
	// 播种，进而丢掉用户的鉴权/配置。
	HermesSourceMustExist bool
	// HermesMemoryStore 是 agent 的持久化 Hermes 记忆存储（HermesMemoryStorePath），
	// overlay 把 memories/ 链接到它，使记忆能活过单次任务。为空则 memories/ 保持
	// 任务本地——因为没有可关联的 agent，或 Multica profile 目录解析不出来。
	HermesMemoryStore string
	// HermesSessionStore 是会话的持久化 Hermes 会话存储（HermesSessionStorePath），
	// overlay 把 state.db 链接到它，使对话记录能活过任务、后续轮次真正可以接着它
	// 继续。为空则 state.db 保持任务本地——没有可关联的 agent 或会话，或 Multica
	// profile 目录解析不出来。
	HermesSessionStore string
	// HermesEnv 是清洗后的有效环境变量（agent custom_env 减去 daemon 列入黑名单
	// 的键），用来展开 Hermes external_dirs 里的 ${VAR}，使其与 Hermes 子进程
	// 真实看到的环境一致。仅 hermes 使用。
	HermesEnv map[string]string
	// ReasonixEnv 是清洗后的 agent custom_env，叠加在 daemon 自身环境上，与构造
	// 子进程环境的方式完全一致。per-task 的 reasonix.toml 会把该环境最终解析到
	// 的用户配置权限重申一遍；这样即便 agent 重新指向（或清空）REASONIX_HOME，
	// daemon 的读取也跟着走。仅 reasonix 使用。
	ReasonixEnv map[string]string
	// CodexCustomArgs 是本任务实际启动 Codex 时携带的 CLI 参数（daemon 默认值 +
	// profile 固定项 + 每个 agent 的 custom_args）。只有 Windows 沙箱决策会读它，
	// 用来尊重一个永远不会写进 config.toml 的 `-c windows.sandbox=...` 覆盖
	// （MUL-4957）。
	CodexCustomArgs []string
	Task            TaskContextForEnv // 写上下文文件时所需的上下文数据
}

// TaskContextForEnv 是任务上下文中「写上下文文件所需」的那部分子集。任务哪些
// 信息会被写进 agent 工作目录里的简介/上下文文件，由这里的字段决定。
type TaskContextForEnv struct {
	IssueID          string
	TriggerCommentID string // 触发本任务的评论 ID（on_assign 型任务为空）
	TriggerThreadID  string // 触发线程的根评论 ID；为空时回退到 TriggerCommentID
	// CommentReplyTargets 在一次评论任务「合并了横跨多个根线程的评论」时才会被设置
	// （MUL-4348）。条目数 >=2 时，工作流的回复步骤会一对一线程地分发回复，而不是
	// 走单一的 `--parent=trigger` 方式——这是为了让持久化的 brief 与每一轮的 prompt
	// 保持一致：跨线程的任务不会出现一份材料说「回复一条」、另一份说「每个线程一
	// 条」的分裂。同一线程内的追问在上游已被合并成一组，所以这个字段保持为空，走
	// 单父线程路径（不会产生重复回复）。
	CommentReplyTargets []ThreadReplyTarget
	NewCommentCount     int    // 自该 agent 上次运行以来 issue 全站的新评论数（不含它自己的和被注入的触发评论）
	NewCommentsSince    string // RFC3339 锚点（上次运行的 started_at），计数从它算起；冷启动时为空
	PriorSessionResumed bool   // 为 true 表示 daemon 会恢复该任务已有的 provider 会话
	// PriorSessionResumeUnavailable 为 true 表示本任务带着一个 daemon 期望恢复的旧
	// 会话，但实际没能恢复（被复用的 workdir 已消失，或 Codex 的会话存档不在本任务
	// 的 CODEX_HOME 里）。brief 会把这点暴露出来，让 agent 主动告诉用户「之前的对话
	// 上下文已丢失、本次是全新开始」——把静默的上下文丢失变成用户可见的（MUL-4424）。
	// 这与普通冷启动不同：冷启动从来就没有旧会话可丢。
	PriorSessionResumeUnavailable bool
	AgentID                       string // 被派发 agent 的唯一 ID
	AgentName                     string
	AgentInstructions             string // agent 身份/人设指令，注入到 CLAUDE.md
	AgentSkills                   []SkillContextForEnv
	DisabledRuntimeSkills         []RuntimeSkillRefForEnv
	Repos                         []RepoContextForEnv     // 可供 checkout 的工作区仓库
	ProjectID                     string                  // 本任务所属的活跃项目（若有）
	ProjectTitle                  string                  // 人类可读的项目标题
	ProjectDescription            string                  // 持久的项目级上下文，渲染到 brief 的 Project Context 一节
	ProjectResources              []ProjectResourceForEnv // 挂在项目上的资源
	ChatSessionID                 string                  // 聊天任务时非空
	// ChatChannelType 是聊天会话背后的 IM 平台（"slack"、"feishu"、"wecom"）；
	// web/移动端聊天为空。它只在 brief 文案里指明「回复落到哪个表面」；该表面究竟
	// 能「交付什么」，则由下面另一个字段负责（MUL-4899）。读者范围与历史策略属于
	// 正交问题，放在每轮聊天的 prompt 里（daemon/prompt.go）——服务端没有读取其它
	// 任何频道的阅读器。
	ChatChannelType string
	// ChatChannelDeliversFiles 是服务端对「本 turn」的裁决：agent 产出的文件能否真
	// 正到达读者手里——adapter 能回去取回绑定的附件，且当前部署具备它依赖的对象存
	// 储。该值随 claim 到达并被原样使用。false 涵盖三种情况：从没发过该字段的旧服务
	// 端、没有对象存储的部署、以及 adapter 不做这步搬运的频道——三种情况需要的指令
	// 完全一致：让 agent 用文字描述它的文件。
	//
	// 该字段被带到这里，但刻意不渲染进 brief。它是每轮变化的值：服务端升级开始发它，
	// 或对象存储被打开/关闭，都会让一个跨变更续跑的聊天会话翻转它，而 brief 是
	// prompt-cache 前缀（MUL-5377）。面向 agent 的裁决由每轮聊天 prompt
	// （daemon.buildChatPrompt）另行输出，TestBriefByteIdenticalAcrossRunsForEveryKind
	// 正是用来确保此字段不进入 brief 的测试。
	ChatChannelDeliversFiles bool

	AutopilotRunID          string // autopilot run_only 型任务时非空
	AutopilotID             string
	AutopilotTitle          string
	AutopilotDescription    string
	AutopilotSource         string
	AutopilotTriggerPayload string
	QuickCreatePrompt       string // quick-create 型任务时非空
	HandoffNote             string // 交接班指令；渲染进 issue_context.md（MUL-3375）
	IsSquadLeader           bool   // 为 true 表示本任务让 agent 扮演 squad-leader 角色（无动作时可能静默退出）；由 claim 的 is_leader_task/squad_id 推导，绝不从指令文本里嗅探（MUL-5811）
	// WorkspaceContext 是 workspace 级的系统提示词（数据库里的 workspace.context）。
	// 非空时以 `## Workspace Context` 渲染进 brief，使 workspace 内每个 agent 都看到
	// 同一份共享上下文，无论它来自 issue / chat / autopilot / quick-create。
	WorkspaceContext string
	// IssueStatuses 是 claim 载荷里 workspace 当前启用的「自定义」状态目录（MUL-6460），
	// 按目录顺序排列。它会渲染进 brief 的状态命令行，让 agent 能看到并使用七个内置
	// 状态之外的自定义状态。与 WorkspaceContext 一样，它是持久的 workspace 配置而非
	// 每轮状态：管理员在续跑会话的两次运行之间改了目录，brief 字节因此变化是合法的，
	// 与编辑 Workspace Context 同理。为空——包括从未发送过该字段的旧服务端——渲染出
	// 的仍是与之前字节一致的纯内置状态行。
	IssueStatuses []IssueStatusForEnv
	// IssueStatusesOmitted 是服务端因数量上限而从 IssueStatuses 里裁掉的活跃自定义
	// 状态个数，让 brief 能披露截断，而不是把残缺目录当成完整目录展示。
	IssueStatusesOmitted int
	// ConnectedApps 列出本 run 通过 MCP overlay 挂载的外部应用能力。brief 里只轻量
	// 提及，让 agent 能把 Notion 之类的应用名对应到真实 MCP server 名（`composio`）。
	ConnectedApps []runtimeapps.ConnectedApp
	// RequestingUserName + RequestingUserProfileDescription 描述 agent 正在代其行事的
	// 人类。v1 从 runtime owner（注册 daemon 的用户）处获取。仅当 description 非空时
	// 才以 `## Requesting User` 一节渲染进 brief——为空表示该用户选择不注入个人资料
	// 上下文，agent 保持匿名用户模式。
	RequestingUserName               string
	RequestingUserProfileDescription string
	// Initiator* 标识触发「本任务」的角色（真正的请求者），与 runtime owner 区分。
	// 有名字时以 `## Task Initiator` 渲染进 brief；InitiatorEmail 仅对成员发起者展示。
	// on-assign / autopilot / quick-create 任务没有可归属的人类发起者，故为空。参见
	// MUL-2645。
	InitiatorType  string
	InitiatorID    string
	InitiatorName  string
	InitiatorEmail string
	// Life 字段标识受治理的后台认知任务及其边界上下文。
	LifeContext  string
	LifeJobID    string
	LifeJobType  string
	LifeJobInput string
}

// IssueStatusForEnv 是一个被渲染进 brief 的活跃「自定义」workspace 状态（MUL-6460）。
// Name 与 Description 是用户撰写的文本，渲染前必须经过 brief 的清洗器；Key 虽已被
// 存储层 CHECK 约束为 `^[a-z0-9][a-z0-9_]{0,31}$`，这里仍用 sanitizeBriefCodeToken
// 兜底一道，作为纵深防御。
type IssueStatusForEnv struct {
	Key         string
	Name        string
	Category    string
	Description string
}

// SkillContextForEnv 描述一个要写入执行环境的技能：技能名 + 一句话描述 + 技能正文
// + 一组附属文件。大致等同于一个按「目录 + SKILL.md」组织的技能的扁平化表示。
type SkillContextForEnv struct {
	Name        string
	Description string
	Content     string
	Files       []SkillFileContextForEnv
}

// SkillFileContextForEnv 描述技能目录里的一个附属文件（相对路径 + 内容）。
type SkillFileContextForEnv struct {
	Data    []byte
	Mode    int32
	Path    string
	Content string
}

// Environment 表示一个「已准备好、与其他环境隔离」的任务执行环境。它是 Prepare /
// Reuse 的产物，持有环境根目录、工作目录以及各 provider 特有的 per-task 目录，
// 并负责在生命周期结束时释放根目录上的执行锁。
type Environment struct {
	// RootDir 是环境顶层目录（{workspacesRoot}/{...}/{task 路径}/）。
	RootDir string
	// WorkDir 是要传给 agent 的 Cwd。通常为 {RootDir}/workdir/；当任务绑定到
	// local_directory 型 project_resource 时，改为用户的路径。参见 LocalDirectory。
	WorkDir string
	// LocalDirectory 为 true 表示 WorkDir 指向 RootDir 之外、由用户提供的路径
	// （local_directory 流程）。以「能否把 WorkDir 当临时目录删除」为行为依据的
	// 调用方必须检查该字段——例如 GC 循环永远不会删除用户的目录。
	//
	// worktree 模式下刻意保持 false：那里的 workdir 是 RootDir 内一次性的 git
	// worktree，环境根目录仍是普通的 daemon 自有临时目录，GC 应按正常节奏回收，
	// 也不需要专门保护用户目录的 sidecar 回滚。
	LocalDirectory bool
	// MulticaConfigRoot 是导出给子 CLI 进程的私有 per-task 配置目录。它让子进程
	// 无法隐式发现 daemon 所有者的 ~/.multica profile，同时又不必改动 provider 侧
	// 的 HOME。
	MulticaConfigRoot string
	// LocalWorktree 在任务针对 local_directory 资源走 worktree 模式时被设置。
	// agent 退出后，daemon 对它调用 Finalize：提交遗留改动、删除 worktree、并取回
	// 需要作为任务结果汇报的分支名。
	LocalWorktree *LocalWorktree
	// CodexHome 是 per-task CODEX_HOME 目录的路径（仅 codex provider 设置）。
	CodexHome string
	// ClaudeSettingsPath 是任务本地的一个 --settings JSON 文件，用来施加「禁用运行
	// 时技能」策略，且不改动用户自己的 Claude 配置。
	ClaudeSettingsPath    string
	CodebuddySettingsPath string
	// CursorDataDir 是 per-task 的 Cursor 数据目录（仅 cursor provider 且 agent 有
	// managed mcp_config 时设置）。daemon 把它导出为 CURSOR_DATA_DIR，使项目级的
	// MCP 审批与用户持久的 ~/.cursor/projects 状态隔离。
	CursorDataDir string
	// HermesHome 是 per-task HERMES_HOME overlay 的路径（仅 hermes provider 且 agent
	// 绑定了技能时设置——否则为空，用户真实 home 保持不变）。它通过 symlink 镜像
	// ~/.hermes/，派生出引用用户真实技能为外部根（external root）的 config.yaml，
	// 并把绑定的技能放在其 skills/ 子目录。daemon 将其导出为 HERMES_HOME，让 hermes
	// CLI 原生发现这些技能——Hermes 没有 workspace 相对发现机制，所以早先的
	// .agent_context/skills/ 回退方案从未被读取（issue #5242）。参见 hermes_home.go。
	HermesHome string
	// HermesSessionStore 是本任务 state.db 实际链接到的会话存储；若会话数据库仍停留
	// 在任务本地（没有可关联的存储，或宿主机无法建链）则为 ""。
	HermesSessionStore string
	// HermesSessionHistoryPresent 报告被挂载的存储里是否真的有一个会话数据库——即本
	// 任务可以续接的、上一轮的对话记录。仅仅挂载成功并不代表有历史：会话首轮、存储
	// 在两轮之间被 GC 回收、Hermes profile 被切换、或运维人员 `rm` 掉了文件，都会让
	// 挂载干净地落空。daemon 用「这个字段」而非存储路径回答「这里的旧会话 ID 还能
	// 解析吗？」——参见 gateResumeToReachableSession。
	HermesSessionHistoryPresent bool
	// QwenpawWorkspace 是 per-task QwenPaw workspace 目录的路径（仅 qwenpaw provider
	// 设置）。里面放好绑定的技能及其 enabled: true 的 skill.json 清单，技能即刻生效。
	// daemon 把 --workspace <dir> 传给 `qwenpaw acp`，让 QwenPaw agent 原生发现技能。
	// 参见 qwenpaw_workspace.go。
	QwenpawWorkspace string

	logger *slog.Logger // 用于清理阶段的日志
	// lockFile 在本 Environment 存活期间持有环境根目录的排他执行锁，
	// 由 Cleanup 释放。参见 claimEnvRoot。
	lockFile *os.File
}

// RootDirParams 是推导新任务环境根目录所需的「身份 + 展示」数据。ID 提供稳定且防
// 冲突的后缀；用户可控制的 label 只作为可读前缀，绝不充当身份。
type RootDirParams struct {
	WorkspacesRoot  string
	WorkspaceID     string
	WorkspaceSlug   string
	TaskID          string
	IssueIdentifier string
}

// 让每个可读路径段与 main 侧的 <workspace UUID>/<12 位 task key> 布局保持在同一
// 预算量级。环境根目录会同时作为 checkout 与 git ref 路径的前缀，在 Windows 上
// 每多一个字符都是代价，所以要限制长度。
const readablePathSegmentMax = 24

// PredictRootDir 不做任何 I/O，仅返回为任务「提议」的可读路径。需要拿到任务权威
// 物理根目录的调用方必须使用 ResolveRootDir——它会冻结住第一个提议。
func PredictRootDir(params RootDirParams) string {
	if params.WorkspacesRoot == "" || params.WorkspaceID == "" || params.TaskID == "" {
		return ""
	}
	return filepath.Join(
		params.WorkspacesRoot,
		readablePathSegment(params.WorkspaceSlug, "workspace", params.WorkspaceID),
		readablePathSegment(params.IssueIdentifier, "task", params.TaskID),
	)
}

// readablePathSegment 把用户控制的 label 转成一个有长度上限、小写 ASCII 的前缀，
// 再拼上稳定的 ID 后缀。后缀保证：即便两个 label 仅大小写不同、清洗后值相同、或
// 之后被改动，路径也互不混淆。
func readablePathSegment(label, fallback, id string) string {
	prefix := strings.ToLower(strings.TrimSpace(label))
	prefix = nonAlphanumeric.ReplaceAllString(prefix, "-")
	prefix = strings.Trim(prefix, "-")
	if prefix == "" {
		prefix = fallback
	}

	// UUIDv7 的高位是时间戳，突发创建的多个任务会共享它；taskKey 取的是随机尾段，
	// 同样适用于 workspace UUID。
	suffix := strings.ToLower(taskKey(id))
	maxPrefix := readablePathSegmentMax - len(suffix) - 1
	if len(prefix) > maxPrefix {
		prefix = strings.TrimRight(prefix[:maxPrefix], "-")
	}
	if prefix == "" {
		prefix = fallback
	}
	return prefix + "-" + suffix
}

// Prepare 为一个任务创建隔离的执行环境。
// workdir 初始为空（不预先检出任何仓库）；agent 在需要时通过
// `multica repo checkout <url>` 按需检出。
func Prepare(params PrepareParams, logger *slog.Logger) (*Environment, error) {
	if params.WorkspacesRoot == "" {
		return nil, fmt.Errorf("execenv: workspaces root is required")
	}
	if params.WorkspaceID == "" {
		return nil, fmt.Errorf("execenv: workspace ID is required")
	}
	if params.TaskID == "" {
		return nil, fmt.Errorf("execenv: task ID is required")
	}

	envRoot, err := ResolveRootDir(RootDirParams{
		WorkspacesRoot:  params.WorkspacesRoot,
		WorkspaceID:     params.WorkspaceID,
		WorkspaceSlug:   params.WorkspaceSlug,
		TaskID:          params.TaskID,
		IssueIdentifier: params.IssueIdentifier,
	})
	if err != nil {
		return nil, err
	}

	// 每次任务启动都自愈一次根级 daemon 标记：daemon 运行期间若该标记被删除，
	// 在 agent 启动前把它恢复。每个 workdir 内还会写一个标记，但那只管「cwd 落在
	// workdir 内」的子进程；根级标记让「丢失全部 MULTICA_* 环境变量又逃逸到
	// workdir 之外」的子进程依然处于 CLI fail-closed 防护之下。非致命：没有它，
	// workdir 标记仍能守住常见情形。
	if err := EnsureWorkspacesRootMarker(params.WorkspacesRoot); err != nil && logger != nil {
		logger.Warn("execenv: workspaces root marker not written; fail-closed guard limited to the task workdir", "error", err)
	}

	// 在碰目录里任何东西之前，先取得 env root 的独占所有权。因为接下来会清空该
	// 目录，而段名曾是 UUIDv7 前缀时，曾反复把仍在运行的兄弟任务的工作目录、
	// worktree 和 task-scoped 配置一并清掉（#7326）。taskKey 现在读取 id 的随机
	// 尾段，使路径撞车从「家常便饭」变成「几乎不可能」——但仍然不是不可能，所以
	// 要证明归属，而不是假设归属。一个拒绝启动的任务可以重来；一个删掉运行中
	// 兄弟任务未提交工作的任务则无法挽回。
	//
	// claimEnvRoot 是两个同 key 任务之间唯一的防线，所以必须全程原子：先读后删
	// 会让两者都通过检查、其中一个仍然删掉另一个。一旦抢到，该 claim 会持有到
	// Prepare 结束——下面的 reset 只清目录「内容」、保留标记，因此任何竞态任务
	// 都不会看到 env root 处于「无人认领」的瞬间。
	var lockFile *os.File
	lockClaimed := false
	if params.EnvRootPreclaimed {
		// 调用方已持有 claim 并已重置过根目录；这里只需在填充前确保目录存在。
		if err := os.MkdirAll(envRoot, 0o755); err != nil {
			return nil, fmt.Errorf("execenv: create env root %s: %w", envRoot, err)
		}
	} else {
		lock, reset, err := claimEnvRoot(envRoot, params.WorkspaceID, params.TaskID)
		if err != nil {
			return nil, fmt.Errorf("execenv: %w", err)
		}
		lockFile = lock
		// 下面任一失败路径都要释放锁；成功路径会把锁交给 Environment。
		lockClaimed = true
		defer func() {
			if lockClaimed {
				releaseLockFile(lockFile)
			}
		}()
		// reset 为 true 表示该任务本来就拥有这个目录，而留下它的那次执行已消失——
		// 这是一次重跑，理应从一个干净的树开始。复用一个「先前任务」的目录不会
		// 走到这里；那是 Reuse 的职责，它接受显式 WorkDir 且不删除任何东西。
		if reset {
			if err := resetEnvRootContents(envRoot); err != nil {
				return nil, fmt.Errorf("execenv: reset existing env: %w", err)
			}
		}
	}

	// 创建目录树。标准流程下 agent 的 workdir 是 envRoot/workdir；local_directory
	// 任务则用用户路径顶替它，这里只需在 envRoot 下建好临时目录。
	workDir := filepath.Join(envRoot, "workdir")
	scratchDirs := []string{filepath.Join(envRoot, "output"), filepath.Join(envRoot, "logs")}
	if params.LocalWorkDir == "" && params.LocalWorktree == nil {
		scratchDirs = append(scratchDirs, workDir)
	} else if params.LocalWorkDir != "" {
		workDir = params.LocalWorkDir
	}
	for _, dir := range scratchDirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("execenv: create directory %s: %w", dir, err)
		}
	}
	multicaConfigRoot := filepath.Join(envRoot, "multica-config")
	if err := os.MkdirAll(multicaConfigRoot, 0o700); err != nil {
		return nil, fmt.Errorf("execenv: create task-local Multica config directory: %w", err)
	}
	if err := os.Chmod(multicaConfigRoot, 0o700); err != nil {
		return nil, fmt.Errorf("execenv: restrict task-local Multica config directory: %w", err)
	}

	// worktree 模式：在 envRoot 内为用户的仓库建立本任务自己的检出副本，并把它
	// 当作 workdir。必须赶在任何上下文文件写入之前完成，这样 sidecar 落在一次性
	// 的 worktree 里，而不是用户的目录里。
	var localWorktree *LocalWorktree
	// 记录 Prepare 是否走到了成功返回。worktree 创建之后的步骤仍可能失败——
	// 上下文文件、provider home、MCP 配置——而这些失败路径上调用方拿不到
	// Environment，下游无人知道存在一个待清理的 worktree。没有下面的回滚，每次
	// 这类失败都会在用户仓库里留下一条登记和一个从未有任务跑过的分支。
	prepareSucceeded := false
	if params.LocalWorktree != nil {
		wtParams := *params.LocalWorktree
		wtParams.EnvRoot = envRoot
		wtParams.AgentName = params.AgentName
		wtParams.TaskID = params.TaskID
		wtParams.ConversationKey, wtParams.ConversationID = localWorktreeConversation(params)
		wtParams.WorkspaceID = params.WorkspaceID
		wtParams.AgentID = params.Task.AgentID
		var err error
		localWorktree, err = PrepareLocalWorktree(wtParams, logger)
		if err != nil {
			return nil, err
		}
		defer func() {
			if prepareSucceeded {
				return
			}
			// 无条件丢弃是安全的：agent 尚未运行，worktree 里只有 Prepare 自己
			// 放进去的东西。
			localWorktree.Discard(logger)
		}()
		workDir = localWorktree.WorkDir
		// 资源可能指向一个只含被忽略文件的子目录，git 不会把它物化到 worktree
		// 中，所以这里补建目录兜底。
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return nil, fmt.Errorf("execenv: create worktree workdir %s: %w", workDir, err)
		}
	}

	env := &Environment{
		RootDir:           envRoot,
		WorkDir:           workDir,
		LocalDirectory:    params.LocalWorkDir != "",
		LocalWorktree:     localWorktree,
		MulticaConfigRoot: multicaConfigRoot,
		logger:            logger,
		lockFile:          lockFile,
	}

	// 把上下文文件写进 workdir（技能写到 provider 原生的路径）。把每个创建的文件/
	// 目录记进一份 manifest，供 CleanupSidecars 把 local_directory 的 workdir 回滚
	// 到 Prepare 前的状态。云端任务并不需要 manifest（GC 循环会整棵清掉 envRoot），
	// 但这里总是写一份——它很廉价、让 Prepare/Reuse 保持对称，也避免一个「一旦
	// local_directory 判定逻辑漂移就会静默关闭清理」的条件分支。
	manifest := &sidecarManifest{}

	// 回滚要在第一次写入「之前」就位，而不是等 writeContextFiles 返回之后。
	// writeContextFiles 第一步就放下 daemon 任务标记，后续任何一步都可能失败——
	// .agent_context、技能文件、项目资源——若 defer 注册在它返回之后，恰好会漏掉
	// 「只有标记孤零零、其余全空」这类失败。回滚一份空 manifest 是空操作，这正是
	// 提前布防也安全的原因。
	//
	// 记录这些写入的 manifest 直到 Prepare 末尾才会持久化，且任何失败路径上调用方
	// 都收不到 Environment，因此通常负责撤销这棵树的 teardown defer 一个都不会注册。
	// 没有这份回滚，文件会留在磁盘上且没有任何「该删什么」的记录（MUL-6132）。
	//
	// 仅对 local_directory 生效。worktree 模式失败时在上方直接丢弃整个 worktree；
	// 云端 envRoot 会被 GC 整棵清掉——只有 local_directory 流程是写进一个活得比任务
	// 更久、且属于用户的目录，那里的残留标记会让整个目录树下所有 multica 命令失效，
	// 直到有人手工删除。
	if params.LocalWorkDir != "" {
		defer func() {
			if prepareSucceeded {
				return
			}
			if err := rollBackPreparedSidecars(*manifest); err != nil && logger != nil {
				logger.Warn("execenv: roll back sidecars after failed prepare", "work_dir", workDir, "error", err)
			}
		}()
	}

	if err := writeContextFiles(workDir, params.Provider, params.Task, manifest); err != nil {
		return nil, fmt.Errorf("execenv: write context files: %w", err)
	}
	if err := prepareOmpMcpConfig(workDir, params.Provider, params.McpConfig, manifest); err != nil {
		return nil, fmt.Errorf("execenv: prepare omp mcp config: %w", err)
	}

	// 对「非本地、可续用」的环境，在 Prepare 时就持久化 managed-env provenance
	//（而不是像 .gc_meta.json 那样等到任务完成才写）。同一 issue 的后续轮次可能在前
	// 一任务完成的瞬间就被认领——比前一任务 handler 写 .gc_meta.json 还早——所以
	// 「可否复用」必须能由「环境一创建就存在」的工件来证明。只有受管（非
	// local_directory）的 issue 与 chat 环境才打这个标记，那恰是拥有持久会话范围的
	// 集合。非致命：写入失败只是让下一次跟进失去会话复用（退回全新会话），绝不能
	// 阻塞本任务的派发。
	if params.LocalWorkDir == "" && (params.Task.IssueID != "" || params.Task.ChatSessionID != "") {
		if err := WriteManagedEnvProvenance(envRoot, ManagedEnvProvenance{
			WorkspaceID:   params.WorkspaceID,
			IssueID:       params.Task.IssueID,
			ChatSessionID: params.Task.ChatSessionID,
			AgentID:       params.Task.AgentID,
		}); err != nil && logger != nil {
			logger.Warn("execenv: write managed env provenance failed (non-fatal); a follow-up may start a fresh session", "error", err)
		}
	}

	// 对 Codex：从 ~/.codex/ 播种一个带技能的 per-task CODEX_HOME。
	if params.Provider == "codex" {
		codexHome := filepath.Join(envRoot, codexHomeDirName)
		if err := prepareCodexHomeWithOpts(codexHome, CodexHomeOptions{CodexVersion: params.CodexVersion, IsLocalDirectory: params.LocalWorkDir != "" || params.LocalWorktree != nil, SessionStoreKey: codexSessionStoreKey(params.Profile, params.Task), CodexCustomArgs: params.CodexCustomArgs}, logger); err != nil {
			return nil, fmt.Errorf("execenv: prepare codex-home: %w", err)
		}
		if err := hydrateCodexSkills(codexHome, params.Task.AgentSkills, params.Task.DisabledRuntimeSkills, logger); err != nil {
			return nil, fmt.Errorf("execenv: hydrate codex skills: %w", err)
		}
		env.CodexHome = codexHome
	}

	if params.Provider == "claude" {
		settingsPath, err := prepareClaudeSkillSettings(envRoot, params.Task.DisabledRuntimeSkills, params.Task.AgentSkills)
		if err != nil {
			return nil, fmt.Errorf("execenv: prepare claude skill settings: %w", err)
		}
		env.ClaudeSettingsPath = settingsPath
	}
	if params.Provider == "codebuddy" {
		settingsPath, err := prepareCodebuddySkillSettings(envRoot, params.Task.DisabledRuntimeSkills, params.Task.AgentSkills)
		if err != nil {
			return nil, fmt.Errorf("execenv: prepare codebuddy skill settings: %w", err)
		}
		env.CodebuddySettingsPath = settingsPath
	}

	// 对 Hermes：仅当 agent 绑定了技能时，才把 HERMES_HOME 重定向到 per-task 的
	// 兼容性 overlay。没有技能的 Hermes 任务保留用户真实 home，行为与原来完全一致。
	// overlay 的作用是让绑定技能可见——Hermes 只从自己的 home 发现技能，所以旧有的
	// .agent_context/skills/ 回退方案从未被读取（issue #5242）。参见 hermes_home.go。
	//
	// 注意这是本地约定，不是可观察的产品行为：服务端会给每个 agent 的技能集追加
	// 平台内置技能（service.LoadAgentSkillBundles），因此被认领任务的 AgentSkills
	// 永不为空，「无技能」分支在生产环境实际上不可达。把某个 agent 自己的技能列表
	// 清空并不能作为退出 overlay 的方式。
	if params.Provider == "hermes" && len(params.Task.AgentSkills) > 0 {
		hermesHome := filepath.Join(envRoot, "hermes-home")
		sessions, err := prepareHermesHome(hermesHome, params.HermesSourceHome, params.HermesSourceMustExist, params.Task.AgentSkills, params.HermesEnv, params.HermesMemoryStore, params.HermesSessionStore, logger)
		if err != nil {
			return nil, fmt.Errorf("execenv: prepare hermes-home: %w", err)
		}
		env.HermesHome = hermesHome
		if sessions.Mounted {
			env.HermesSessionStore = params.HermesSessionStore
			env.HermesSessionHistoryPresent = sessions.HistoryPresent
		}
	}
	if params.Provider == "qwenpaw" {
		qwenpawWorkspace := filepath.Join(envRoot, "qwenpaw-workspace")
		if err := prepareQwenpawWorkspace(qwenpawWorkspace, params.Task.AgentSkills, logger); err != nil {
			return nil, fmt.Errorf("execenv: prepare qwenpaw workspace: %w", err)
		}
		env.QwenpawWorkspace = qwenpawWorkspace
	}

	// 对 Reasonix：通过 project-scoped 的 reasonix.toml 禁用本任务的 `ask` 工具。
	// 降级而非致命：即使没写成功，任务仍会在后端 fail-closed 的问题处理下运行。
	if params.Provider == "reasonix" {
		if err := writeReasonixProjectConfig(workDir, params.ReasonixEnv, manifest, logger); err != nil {
			logger.Warn("execenv: write reasonix project config failed", "error", err)
		}
	}

	// 对 Cursor：把 managed MCP 物化到项目本地配置，并用隔离的 CURSOR_DATA_DIR
	// 承载 per-workdir 的审批 sidecar。Cursor 仍会读取 ~/.cursor/mcp.json，但只有
	// 在这个 per-task 数据目录里有审批记录的 server 才能加载，因此用户全局的 MCP
	// server 不会泄漏进 managed-MCP 的运行里。
	if params.Provider == "cursor" {
		cursorDataDir, err := prepareCursorMcpConfig(envRoot, workDir, params.McpConfig, params.CursorMcpAuthSource, manifest)
		if err != nil {
			return nil, fmt.Errorf("execenv: prepare cursor mcp config: %w", err)
		}
		env.CursorDataDir = cursorDataDir
	}

	if err := writeSidecarManifest(envRoot, manifest); err != nil {
		// local_directory 模式下这份 manifest 是我们写进用户自有目录的「唯一记录」，
		// 丢了它 sidecar 树就会永久滞留在那里——甚至不需要崩溃，一次磁盘或权限抖动
		// 就够（MUL-6132）。因此这里直接报错，让上面注册的回滚趁内存里还有 manifest
		// 立刻清掉这棵树。其余场景下 manifest 只是 GC 可有可无的便利品，警告即可。
		if params.LocalWorkDir != "" {
			return nil, fmt.Errorf("execenv: write sidecar manifest: %w", err)
		}
		logger.Warn("execenv: write sidecar manifest failed (non-fatal)", "error", err)
	}

	logger.Info("execenv: prepared env", "root", envRoot, "repos_available", len(params.Task.Repos))
	prepareSucceeded = true
	lockClaimed = false // ownership of any lock passes to the Environment
	return env, nil
}

// ReuseParams 描述 Reuse 的输入。它镜像 PrepareParams 中「各 provider 专属的旋钮」
// （如 CodexVersion），使调用方在首次运行与复用两条路径上都能传同一个已解析的
// 二进制路径。
type ReuseParams struct {
	RootDir string

	// WorkspacesRoot 是 daemon 自有、承载所有任务环境的总根目录。复用路径上也要
	// 传它，好在这里自愈根级 fail-closed 标记——daemon 运行期间被删的标记会在复用
	// 任务启动前恢复，而不只是全新 Prepare 时才恢复。
	WorkspacesRoot string
	WorkDir        string
	Provider       string
	CodexVersion   string // 仅 Provider == "codex" 时使用
	// ResumeSessionID 是本复用任务想要续接的旧 Codex thread/session ID（若有）。
	// 仅在 Provider == "codex" 时被查询，且只用于迁移「sessions/ 仍软链共享
	// ~/.codex/sessions」的遗留 per-task home——这个 ID 的唯一存档会被暴露进新的
	// task-local sessions 目录，让 thread/resume 仍能找到它。为空表示全新 thread。
	// 参见 prepareCodexSessionsDir（MUL-4424）。
	ResumeSessionID string
	// McpConfig 是 agent 保存的 `mcp_config` JSON。复用时重放，使新保存的 managed
	// 集合在任务启动前重新物化进 wrapper——否则上一轮的陈旧 wrapper 会让旧 MCP
	// 集合继续生效。
	McpConfig json.RawMessage
	// CursorMcpAuthSource 在复用时镜像 PrepareParams.CursorMcpAuthSource。
	CursorMcpAuthSource string
	// Profile 是 daemon 的 profile 名（空 = 默认），镜像 PrepareParams.Profile，
	// 让复用任务把 per-issue Codex 会话存储放进同一个 profile 命名空间（MUL-4424）。
	Profile string
	// LocalDirectory 为 true 表示被复用的 WorkDir 是用户提供的目录（local_directory
	// 流程）。该标记会传播进返回的 Environment，使下游调用方（尤其 GC 循环）在复用
	// 路径上仍守住「绝不删除用户目录」的不变量。
	LocalDirectory bool
	// HermesSourceHome、HermesEnv、HermesMemoryStore 与 HermesSessionStore 在复用时
	// 镜像 PrepareParams，使 Hermes overlay 依据 agent 当前的 source home / profile、
	// external_dirs 变量、记忆存储与会话存储重新推导。
	HermesSourceHome      string
	HermesSourceMustExist bool
	HermesEnv             map[string]string
	HermesMemoryStore     string
	HermesSessionStore    string
	// ReasonixEnv 在复用时镜像 PrepareParams.ReasonixEnv，让重写的 reasonix.toml
	// 继续重申 owner 当前的权限。
	ReasonixEnv map[string]string
	// CodexCustomArgs 在复用时镜像 PrepareParams.CodexCustomArgs，使 Windows 沙箱
	// 决策在这里也尊重 `-c windows.sandbox=...` 覆盖（MUL-4957）。
	CodexCustomArgs []string
	Task            TaskContextForEnv // 需要刷新的上下文文件 / 技能
}

// Reuse 把一个已存在的 workdir 包成 Environment，并刷新其中的上下文文件。
// 若 workdir 不存在则返回 nil（调用方应回退到 Prepare）。
func Reuse(params ReuseParams, logger *slog.Logger) *Environment {
	if _, err := os.Stat(params.WorkDir); err != nil {
		return nil
	}

	// 复用路径上同样自愈根级 daemon 标记：daemon 运行期间若标记被删，要在复用的
	// 任务启动前恢复——否则复用会在没有 fail-closed 防护的情况下运行，直到下一次
	// 全新 Prepare。非致命：per-workdir 标记仍守护常见情形；WorkspacesRoot 为空
	// （旧调用方）时直接跳过。
	if params.WorkspacesRoot != "" {
		if err := EnsureWorkspacesRootMarker(params.WorkspacesRoot); err != nil && logger != nil {
			logger.Warn("execenv: workspaces root marker not written on reuse; fail-closed guard limited to the task workdir", "error", err)
		}
	}

	rootDir := filepath.Dir(params.WorkDir)
	if params.LocalDirectory {
		// local_directory 任务里用户的 WorkDir 与 envRoot 无关（envRoot 仍位于
		// workspacesRoot/{wsID}/... 之下），因此从 filepath.Dir(WorkDir) 读出的只会
		// 是用户目录的父级。复用路径上需要真实 RootDir 的调用方应当自行显式传入；
		// v1 中 daemon 只会在同一次任务生命周期内、于全新 Prepare 之后复用
		// local_directory 的 workdir，因此复用时的空 RootDir 对现有调用方无害（GC 用
		// Prepare 的结果而非 Reuse 的结果来写 meta）。
		rootDir = ""
	}
	if params.RootDir != "" {
		rootDir = params.RootDir
	}
	env := &Environment{
		RootDir:        rootDir,
		WorkDir:        params.WorkDir,
		LocalDirectory: params.LocalDirectory,
		logger:         logger,
	}
	if env.RootDir != "" {
		env.MulticaConfigRoot = filepath.Join(env.RootDir, "multica-config")
		if err := os.MkdirAll(env.MulticaConfigRoot, 0o700); err != nil {
			logger.Warn("execenv: restore task-local Multica config directory failed; forcing fresh prepare", "error", err)
			return nil
		}
		if err := os.Chmod(env.MulticaConfigRoot, 0o700); err != nil {
			logger.Warn("execenv: restrict task-local Multica config directory failed; forcing fresh prepare", "error", err)
			return nil
		}
	}

	// 刷新上下文之前，先回滚上一次派发留下的 sidecar 写入。复用时 workdir 仍带着
	// 上一轮的 issue_context.md 和技能目录；若不先清掉，writeSkillFiles 会看到自己
	// 早先的输出正占着规范 slug，于是回退去生成无冲突的兄弟目录（issue-review、
	// issue-review-multica、issue-review-multica-2……），同一 issue 每次重新派发都
	// 堆积一个重复。allocateCollisionFreeSkillDir 的存在是为了绕开「用户自有」的
	// 技能目录（local_directory 流程），而不是我们自己先前的写入——所以先通过上一
	// 轮的 manifest 撤销自己的写入，让下面的刷新把每个技能放回它天然的 slug。这也
	// 让标准 provider 与 Codex 路径对齐——hydrateCodexSkills 在重新注入前本来就会
	// 清空自己的技能目录。
	//
	// 分两步、按顺序：
	//   1. removeReusedManagedSkillDirs 回收平台自己的技能目录，即使上一轮 agent
	//      在其中一个目录里留了文件也能收。CleanupSidecars 单独做不到这件事——它
	//      会保留任何被 agent 填充过的已记录目录（在 local_directory 的拆除路径上
	//      这是正确的），却也因此让规范 slug 一直被占、把刷新挤回 issue-review-multica。
	//   2. CleanupSidecars 回滚其余 sidecar 文件（issue_context.md、项目资源）以及
	//      manifest 本身。
	//
	// RootDir 为空时为空操作（旧版 local_directory 复用，daemon 本来就会跳过它）；
	// 没有旧 manifest 时同样为空操作（更早的构建）。
	if env.RootDir != "" {
		if err := removeReusedManagedSkillDirs(env.RootDir, skillsDirPath(params.WorkDir, params.Provider)); err != nil {
			logger.Warn("execenv: reclaim managed skill dirs on reuse failed", "error", err)
		}
		if err := CleanupSidecars(env.RootDir); err != nil {
			logger.Warn("execenv: roll back prior sidecars on reuse failed", "error", err)
		}
	}

	// 刷新上下文文件（issue_context.md、技能）。Reuse 在 env.RootDir 下跟踪一份全新
	// 的 manifest，好让之后的 CleanupSidecars 看到最新的写入清单（沿用上一轮旧
	// manifest 的话，会引用这次 Reuse 不再创建的文件）。local_directory 任务 daemon
	// 会完全跳过 Reuse（见 daemon.runTask），但这里无条件写 manifest，保证
	// Prepare/Reuse 对称，未来调用方可以放心：无论走哪条路径 manifest 都是最新的。
	// 旧版 local_directory Reuse 回退时 RootDir 为空——此时跳过持久化，避免在文件
	// 系统根目录凭空造出一个游离 manifest。
	manifest := &sidecarManifest{}
	if err := writeContextFiles(params.WorkDir, params.Provider, params.Task, manifest); err != nil {
		logger.Warn("execenv: refresh context files failed", "error", err)
	}
	if err := prepareOmpMcpConfig(params.WorkDir, params.Provider, params.McpConfig, manifest); err != nil {
		logger.Warn("execenv: refresh omp mcp config failed; forcing fresh prepare", "error", err)
		return nil
	}

	// 为 Codex provider 恢复 CodexHome——per-task 的 codex-home 目录与 workdir 同级。
	// 重跑 prepareCodexHomeWithOpts 以保证配置（尤其是沙箱/网络访问策略）是最新的。
	if params.Provider == "codex" {
		codexHome := filepath.Join(env.RootDir, codexHomeDirName)
		if err := prepareCodexHomeWithOpts(codexHome, CodexHomeOptions{CodexVersion: params.CodexVersion, ResumeSessionID: params.ResumeSessionID, IsLocalDirectory: params.LocalDirectory, SessionStoreKey: codexSessionStoreKey(params.Profile, params.Task), CodexCustomArgs: params.CodexCustomArgs}, logger); err != nil {
			// Leaving env.CodexHome empty does not launch Codex against an
			// ambient home: configureCodexTaskShellEnvironment rejects the empty
			// value ("task CODEX_HOME is missing") and the run fails before
			// launch. Decline the reuse instead, so the caller falls back to
			// Prepare and the task still gets a usable task-local home. The
			// prior session is not carried over, and a fresh Prepare that fails
			// the same way still stops the task.
			logger.Warn("execenv: refresh codex-home failed; forcing fresh prepare", "error", err)
			return nil
		} else {
			env.CodexHome = codexHome
			if err := hydrateCodexSkills(codexHome, params.Task.AgentSkills, params.Task.DisabledRuntimeSkills, logger); err != nil {
				logger.Warn("execenv: refresh codex skills failed", "error", err)
			}
		}
	}

	if params.Provider == "claude" && env.RootDir != "" {
		settingsPath, err := prepareClaudeSkillSettings(env.RootDir, params.Task.DisabledRuntimeSkills, params.Task.AgentSkills)
		if err != nil {
			logger.Warn("execenv: refresh claude skill settings failed", "error", err)
		} else {
			env.ClaudeSettingsPath = settingsPath
		}
	}
	if params.Provider == "codebuddy" && env.RootDir != "" {
		settingsPath, err := prepareCodebuddySkillSettings(env.RootDir, params.Task.DisabledRuntimeSkills, params.Task.AgentSkills)
		if err != nil {
			logger.Warn("execenv: refresh codebuddy skill settings failed; forcing fresh prepare", "error", err)
			return nil
		}
		env.CodebuddySettingsPath = settingsPath
	}

	// 复用路径上重新禁用 Reasonix 的 `ask` 工具：上面的 CleanupSidecars 已移除上一
	// 轮的 reasonix.toml，不重写的话下一轮就会重新开放该工具。
	if params.Provider == "reasonix" {
		if err := writeReasonixProjectConfig(params.WorkDir, params.ReasonixEnv, manifest, logger); err != nil {
			logger.Warn("execenv: refresh reasonix project config failed", "error", err)
		}
	}

	// 复用路径上刷新（或拆除）per-task 的 QwenPaw workspace：重建它，使增/删/改过的
	// 技能得到反映。
	if params.Provider == "qwenpaw" && env.RootDir != "" {
		qwenpawWorkspace := filepath.Join(env.RootDir, "qwenpaw-workspace")
		if err := prepareQwenpawWorkspace(qwenpawWorkspace, params.Task.AgentSkills, logger); err != nil {
			logger.Warn("execenv: refresh qwenpaw workspace failed; forcing fresh prepare", "error", err)
			return nil
		}
		env.QwenpawWorkspace = qwenpawWorkspace
	}

	// 复用路径上刷新（或拆除）per-task 的 HERMES_HOME。绑定技能时重建 overlay，让
	// 镜像的 home/config 在下一个 hermes 进程启动前追平用户当前的 ~/.hermes/；没有
	// 技能时彻底去掉重定向，使任务回到用户真实 home——与无技能 agent 的全新 Prepare
	// 行为一致。
	if params.Provider == "hermes" && env.RootDir != "" {
		hermesHome := filepath.Join(env.RootDir, "hermes-home")
		if len(params.Task.AgentSkills) > 0 {
			sessions, err := prepareHermesHome(hermesHome, params.HermesSourceHome, params.HermesSourceMustExist, params.Task.AgentSkills, params.HermesEnv, params.HermesMemoryStore, params.HermesSessionStore, logger)
			if err != nil {
				// 关闭即失败：半成品 overlay 绝不能运行。返回 nil 让 daemon 回退到全新
				// Prepare，其报错随后会阻断派发，而不是静默丢掉绑定的技能。
				logger.Warn("execenv: refresh hermes-home failed; forcing fresh prepare", "error", err)
				return nil
			}
			env.HermesHome = hermesHome
			env.HermesSessionStore = ""
			env.HermesSessionHistoryPresent = false
			if sessions.Mounted {
				env.HermesSessionStore = params.HermesSessionStore
				env.HermesSessionHistoryPresent = sessions.HistoryPresent
			}
		} else {
			env.HermesHome = ""
			env.HermesSessionStore = ""
			env.HermesSessionHistoryPresent = false
			if err := os.RemoveAll(hermesHome); err != nil {
				logger.Warn("execenv: remove stale hermes-home failed", "error", err)
			}
		}
	}

	// 复用路径上刷新 Cursor 的 managed MCP sidecar：新保存的 agent mcp_config 必须在
	// 下一个 cursor-agent 进程启动前替换掉上一轮的 .cursor/mcp.json 与隔离的审批。
	if params.Provider == "cursor" && env.RootDir != "" {
		cursorDataDir, err := prepareCursorMcpConfig(env.RootDir, params.WorkDir, params.McpConfig, params.CursorMcpAuthSource, manifest)
		if err != nil {
			logger.Warn("execenv: refresh cursor mcp config failed", "error", err)
			return nil
		}
		env.CursorDataDir = cursorDataDir
	}

	if env.RootDir != "" {
		if err := writeSidecarManifest(env.RootDir, manifest); err != nil {
			logger.Warn("execenv: refresh sidecar manifest failed", "error", err)
		}
	}

	logger.Info("execenv: reusing env", "workdir", params.WorkDir)
	return env
}

// hydrateCodexSkills 往 per-task 的 CODEX_HOME/skills 目录里同时注入两类技能：
// 用户安装的技能（来自共享的 ~/.codex/skills/）与 workspace 分配的技能。名字冲突
// 时 workspace 技能胜出——它们最后写入，且 seedUserCodexSkills 已预先过滤掉同名项。
//
// 技能目录会先被整个清空，这样 Reuse 路径本来会泄漏的两类陈旧状态就消失了：
//
//   - 一个「现在被 workspace 技能认领」的名字，若之前只装过用户播种的副本——用户
//     版本的支持文件会残留在 workspace 技能目录下。
//   - 一个「自上次运行起已从共享 ~/.codex/skills/ 删除」的用户技能——它的旧内容会
//     继续对 codex CLI 可见。
//
// Codex 是唯一需要这种两段式注入的运行时：因为 daemon 把 CODEX_HOME 设成 per-task
// 目录，把 CLI 与用户真实的 ~/.codex/ 隔离开。其它运行时保持 HOME 不动、天然发现
// 用户级技能（workspace 技能走的 workdir 本地路径见 context.go）。
func hydrateCodexSkills(codexHome string, workspaceSkills []SkillContextForEnv, disabledRuntimeSkills []RuntimeSkillRefForEnv, logger *slog.Logger) error {
	skillsDir := filepath.Join(codexHome, "skills")
	if err := os.RemoveAll(skillsDir); err != nil {
		return fmt.Errorf("clear codex skills dir: %w", err)
	}
	if err := seedUserCodexSkills(codexHome, workspaceSkills, logger); err != nil {
		return fmt.Errorf("seed user codex skills: %w", err)
	}
	if len(workspaceSkills) > 0 {
		if err := writeSkillFiles(skillsDir, workspaceSkills, nil); err != nil {
			return err
		}
	}
	if err := ensureCodexDisabledSkillsConfig(filepath.Join(codexHome, "config.toml"), codexHome, disabledRuntimeSkills, workspaceSkills); err != nil {
		return err
	}
	return writeCodexSkillManifest(codexHome)
}

// GCMetaKind 标识一个任务 workdir 归属于哪种父记录。GC 循环按这个值分发决策树，
// 使 chat / autopilot / quick-create 任务不再被强行塞进以 issue 为中心的老路径。
type GCMetaKind string

const (
	GCKindIssue        GCMetaKind = "issue"         // issue 型任务
	GCKindChat         GCMetaKind = "chat"          // 聊天型任务
	GCKindAutopilotRun GCMetaKind = "autopilot_run" // autopilot run 型任务
	GCKindQuickCreate  GCMetaKind = "quick_create"  // quick-create 型任务
)

// GCMeta 被持久化到 env root 里的 .gc_meta.json，供 GC 循环判断该目录是否可回收。
// 它是一个以 Kind 为判别键的联合结构：只有与 Kind 匹配的那个「父 ID」字段有意义。
// TaskID 也会为每个新任务持久化，这样本地报告无需再从目录名反推任务身份；
// quick-create 时它同时充当 GC 状态端点所用的父 ID。
//
// 更早的 meta 文件（pre-v2）没有 Kind 字段；读取方必须把空 Kind 默认成 GCKindIssue
// 以向后兼容——此前只写 IssueID，而且只有 issue 型任务会产生 meta 文件。
type GCMeta struct {
	Kind           GCMetaKind `json:"kind,omitempty"`
	IssueID        string     `json:"issue_id,omitempty"`
	ChatSessionID  string     `json:"chat_session_id,omitempty"`
	AutopilotRunID string     `json:"autopilot_run_id,omitempty"`
	TaskID         string     `json:"task_id,omitempty"`
	WorkspaceID    string     `json:"workspace_id"`
	CompletedAt    time.Time  `json:"completed_at"`
	// LocalDirectory 标记任务的 WorkDir 指向用户自有路径（而非合成的
	// envRoot/workdir）。GC 循环据此永远不进 gcActionClean 分支（那会 RemoveAll
	// envRoot——按结构虽安全，但仍希望让 envRoot 的 output/ 和 logs/ 多留一会儿，
	// 方便用户查看 agent 在自己的树里做了什么）。基于模式匹配的产物清理仍被允许。
	LocalDirectory bool `json:"local_directory,omitempty"`
}

const gcMetaFile = ".gc_meta.json"

// WriteGCMeta 把 GC 元数据写进给定目录。调用方负责选好 Kind 并填充对应的 ID 字段；
// CompletedAt 在这里盖章，调用方无需操心时钟。
func WriteGCMeta(envRoot string, meta GCMeta, logger *slog.Logger) error {
	if envRoot == "" {
		return nil
	}
	if meta.Kind == "" {
		// 防御：一个不属于任何已知 kind 的任务若写 meta，GC 循环将无法分发。
		// 静默跳过——目录会退回「按 mtime 判断孤儿」的路径。
		logger.Debug("execenv: skipping .gc_meta.json write: kind is empty", "envRoot", envRoot)
		return nil
	}
	meta.CompletedAt = time.Now().UTC()
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal gc meta: %w", err)
	}
	return os.WriteFile(filepath.Join(envRoot, gcMetaFile), data, 0o644)
}

// ReadGCMeta 从任务目录根读取 GC 元数据。pre-v2 的 meta 文件（无 kind 字段）会被
// 归一化成 GCKindIssue，让老 issue 路径无需迁移即可继续工作。
func ReadGCMeta(envRoot string) (*GCMeta, error) {
	data, err := os.ReadFile(filepath.Join(envRoot, gcMetaFile))
	if err != nil {
		return nil, err
	}
	var meta GCMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	if meta.Kind == "" {
		meta.Kind = GCKindIssue
	}
	return &meta, nil
}

const managedEnvProvenanceFile = ".managed_env.json"

// ManagedEnvProvenanceManagedBy 是一个判别值，用于区分 daemon 写的 managed-env
// provenance 文件与任何恰好占用同路径的类 JSON 文件。
const ManagedEnvProvenanceManagedBy = "multica-daemon-managed-env"

// ManagedEnvProvenance 在 Prepare 阶段（而不是像 .gc_meta.json 那样在完成阶段）
// 持久化到 env root 的 .managed_env.json。它记录：该 env root 是一个 daemon 受管的、
// 非 local_directory 的可续用环境，归属特定的 workspace、会话范围与 agent。
//
// 它存在的全部理由就是时序问题。同一 issue 的 squad-leader 后续轮次可能在前一任务
// 完成的瞬间就被认领——服务端的任务完成 handler 会先协调后续轮次并唤醒 runtime，
// 然后才是前一任务 daemon handler 写 .gc_meta.json。因此拿 .gc_meta.json 当复用资格
// 判据会竞态：继任者读到一份还没写出的文件，于是开了全新会话（MUL-4886）。这个
// 标记从环境被创建那一刻就在盘上，继任者得以在那段窗口内证明复用的安全性。它只为
// 非本地的受管 issue/chat 环境而写，所以它的存在本身就是「可安全复用、并非用户
// local_directory」的断言；参见 shouldReusePriorWorkdir。
type ManagedEnvProvenance struct {
	ManagedBy     string `json:"managed_by"`
	WorkspaceID   string `json:"workspace_id"`
	IssueID       string `json:"issue_id,omitempty"`
	ChatSessionID string `json:"chat_session_id,omitempty"`
	AgentID       string `json:"agent_id"`
}

// WriteManagedEnvProvenance 在 env root 持久化「可复用性」标记。调用方只允许为
// 非 local_directory 的可续用环境调用它——因为该文件的存在本身就是「非本地」的
// 断言。ManagedBy 在这里盖章，调用方不可能忘写判别值。
func WriteManagedEnvProvenance(envRoot string, p ManagedEnvProvenance) error {
	if envRoot == "" {
		return nil
	}
	p.ManagedBy = ManagedEnvProvenanceManagedBy
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal managed env provenance: %w", err)
	}
	return os.WriteFile(filepath.Join(envRoot, managedEnvProvenanceFile), data, 0o644)
}

// ReadManagedEnvProvenance 从 env root 读取 Prepare 阶段写下的「可复用性」标记。
// 文件缺失或损坏都会返回错误；调用方在任何错误下都应「关闭即失败」（不复用）。
func ReadManagedEnvProvenance(envRoot string) (*ManagedEnvProvenance, error) {
	data, err := os.ReadFile(filepath.Join(envRoot, managedEnvProvenanceFile))
	if err != nil {
		return nil, err
	}
	var p ManagedEnvProvenance
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Cleanup 拆除执行环境。
// removeAll 为 true 时删除整个 env root；否则只删 workdir，保留 output/ 与 logs/
// 供排障。
//
// 对 local_directory 任务（env.LocalDirectory==true），WorkDir 是用户自有路径——
// 无论 removeAll 取值如何，Cleanup 都绝不能删它。此模式下只删 envRoot 临时目录。
func (env *Environment) Cleanup(removeAll bool) error {
	if env == nil {
		return nil
	}
	// 先放下执行锁。在它释放前，env root 仍会显示「有活动执行占着」，会让合法的
	// 重跑「关闭即失败」。daemon 也会在整个任务运行期间 defer ReleaseLock；两条
	// 路径都是幂等的。
	env.ReleaseLock()

	if env.LocalDirectory {
		// 绝不碰用户的目录。RootDir 是 daemon 自己的临时空间；调用方要求整体拆除
		// 时删掉它是安全的。
		if removeAll && env.RootDir != "" {
			if err := os.RemoveAll(env.RootDir); err != nil {
				env.logger.Warn("execenv: cleanup local_directory envRoot failed", "error", err)
				return err
			}
		}
		return nil
	}

	if removeAll {
		if err := os.RemoveAll(env.RootDir); err != nil {
			env.logger.Warn("execenv: cleanup removeAll failed", "error", err)
			return err
		}
		return nil
	}

	// 部分清理：删 workdir，保留 output/ 与 logs/。
	if err := os.RemoveAll(env.WorkDir); err != nil {
		env.logger.Warn("execenv: cleanup workdir failed", "error", err)
		return err
	}
	return nil
}

// EnvRootClaim 是某个任务 env root 上「已持有的占用」。它存在的意义，是让占用能
// 活得比「准备目录的那段代码」更久。
//
// 生产环境的准备动作跑在短命的辅助进程里（PrepareIsolated / ReuseIsolated）。
// 在辅助进程内加的锁会在它一退出就被内核释放，而 *os.File 也无法穿过辅助进程的
// JSON 响应回来——所以在那里抢到的占用，等 agent 真正运行时已经毫无保护。由
// daemon 父进程抢下占用并持有整个任务运行期，才是有效的。
type EnvRootClaim struct {
	rootDir string
	lock    *os.File
}

// RootDir 返回该 claim 覆盖的 env root。
func (c *EnvRootClaim) RootDir() string {
	if c == nil {
		return ""
	}
	return c.rootDir
}

// Release 放弃占用。nil 安全，也允许调用两次。
func (c *EnvRootClaim) Release() {
	if c == nil || c.lock == nil {
		return
	}
	releaseLockFile(c.lock)
	c.lock = nil
}

// ClaimEnvRoot 为 taskID 的一次全新准备取得完整占用：证明归属、拒绝被活动执行或
// 其它任务持有的 root、并重置本任务拥有的陈旧 root，使调用方拿到干净的目录。
//
// 把 claim 传给 Prepare 的调用方还必须设置 PrepareParams.EnvRootPreclaimed，
// 否则 Prepare 会对这个已被本 claim 持有的锁再抢一次并失败。
func ClaimEnvRoot(params RootDirParams) (*EnvRootClaim, error) {
	envRoot, err := ResolveRootDir(params)
	if err != nil {
		return nil, fmt.Errorf("execenv: resolve env root: %w", err)
	}
	if envRoot == "" {
		return nil, fmt.Errorf("execenv: claim env root: workspaces root, workspace ID and task ID are all required")
	}
	lock, reset, err := claimEnvRoot(envRoot, params.WorkspaceID, params.TaskID)
	if err != nil {
		return nil, fmt.Errorf("execenv: %w", err)
	}
	if reset {
		if err := resetEnvRootContents(envRoot); err != nil {
			releaseLockFile(lock)
			return nil, fmt.Errorf("execenv: reset existing env: %w", err)
		}
	}
	return &EnvRootClaim{rootDir: envRoot, lock: lock}, nil
}

// ErrEnvRootBusy 表示某个 env root 已被活动执行持有。复用路径上它是「回退到全新
// Prepare」的理由，而不是让任务失败的理由。
var ErrEnvRootBusy = errors.New("env root is held by a running execution")

// LockEnvRootForReuse 为一个「延续到先前任务目录」的任务取走占用的「排他」半边，
// 不含身份半边。
//
// 复用是刻意收养另一个任务的 env root，因此 owner 标记写的是那个更早任务的名字，
// 不得重写或改义——但同一任务的两个延续仍不得同时刷新并运行同一个目录。
//
// wsRoot 必须是调用方在「做任何校验之前」就打开的那个 Root，rel 是 env root 在它
// 内部的相对路径。两者都很关键：
//
//   - 若在这里用一个「刚才已校验过的名字」重新打开 Root，等于重解析该名字。有人把
//     整个 workspaces root 移走、在原位置留一个指向相似树的 symlink，os.Root 就会
//     忠实地钉住那个替代品——Root 保证的是「无法逃出它打开的那棵树」，而不是
//     「它打开的就是你想的那棵」。
//   - 下面每个操作都经由钉在 env root 上的同一个子 Root，使「返回身份的那个目录」
//     与「创建锁文件的目录」不可能变成两个不同目录。若从 wsRoot 解析 rel 两次，
//     恰好就会这样：第一次解析到 A，第二次解析到 B。
//
// 函数返回该目录的 FileInfo，让调用方确认它就是校验批准的那个；Root 仍会跟随树内
// 的 symlink，所以「锁了 A 却复用 B」必须靠身份排除，而不能靠包含关系。
//
// env root 缺失时返回 nil claim 且无错误；被其它执行持有时返回 ErrEnvRootBusy——
// 两者都意味着「回退到全新 Prepare」。
func LockEnvRootForReuse(wsRoot *os.Root, rel, envRoot string) (*EnvRootClaim, os.FileInfo, error) {
	if wsRoot == nil || rel == "" || !filepath.IsLocal(rel) {
		return nil, nil, fmt.Errorf("execenv: env root %s is not inside the workspaces root", envRoot)
	}

	// 钉住 env root 本身。从这之后所有操作都不再按名字解析。
	envRootHandle, err := wsRoot.OpenRoot(rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("execenv: open env root %s: %w", envRoot, err)
	}
	defer envRootHandle.Close()

	info, err := envRootHandle.Stat(".")
	if err != nil {
		return nil, nil, fmt.Errorf("execenv: inspect env root %s: %w", envRoot, err)
	}
	if !info.IsDir() {
		return nil, nil, nil
	}

	lock, err := envRootHandle.OpenFile(envRootLockFile, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("execenv: open env root lock for %s: %w", envRoot, err)
	}
	locked, err := lockFileExclusiveNonBlocking(lock)
	if err != nil {
		lock.Close()
		return nil, nil, fmt.Errorf("execenv: lock env root %s: %w", envRoot, err)
	}
	if !locked {
		lock.Close()
		return nil, nil, fmt.Errorf("execenv: reuse %s: %w", envRoot, ErrEnvRootBusy)
	}
	return &EnvRootClaim{rootDir: envRoot, lock: lock}, info, nil
}

// envRootOwnerFile 记录某个 env root 归属于哪个 workspace 与 task：也就是「谁是
// 主人」。下面的执行锁则单独回答「这个主人是否还在运行」。
const envRootOwnerFile = ".task_owner"

const (
	envRootOwnerTempPrefix = ".task_owner-" // 写 owner 时临时文件的前缀
	envRootOwnerTempSuffix = ".tmp"         // 写 owner 时临时文件的后缀
)

// envRootLockFile 承载 env root 的排他执行锁：即「主人是否仍在运行」。两者回答的
// 问题不同且都必需——死任务的目录里仍留着它的工作；活任务的目录即便被同任务的
// 另一次执行也不能重置。
const envRootLockFile = ".task_lock"

// claimEnvRoot 为 taskID 取得 envRoot 的独占所有权。
//
// 返回持锁文件——调用方一直持有它直到 Cleanup——以及该目录使用前是否需要重置。
//
// 这把锁是 OS 建议锁，内核在持有进程无论因何退出都会释放它。这正是它成为「锁」
// 而非第三份标记文件的原因：它能跨进程回答「上一次执行是否还活着」，不需要心跳、
// 不需要 PID 表、也不需要清理陈旧状态。身份标记单独回答不了这个问题——旧设计正是
// 因此让同一 task id 的重新派发清掉了仍在运行执行的工作（#7326 follow-up）：
//
//   - 服务端用同一个 task id 重新投递一条 prepare 租约已过期的任务行
//     （ReclaimStaleDispatchedTaskForRuntime）。
//   - 失败的租约续期只会记日志，并不会取消仍在飞行中的 Prepare。
//   - 于是同一个 task id 的两个 Prepare 可能重叠，「owner == mine」被读成
//     「是我自己的重跑，重置安全」。
//
// 持有锁同时串行化其下的一切操作，这正是「修复被撕坏的标记」安全的原因：不可能有
// 别的执行卡在 claim 中途。
func claimEnvRoot(envRoot, workspaceID, taskID string) (lockFile *os.File, reset bool, err error) {
	if err := os.MkdirAll(envRoot, 0o755); err != nil {
		return nil, false, fmt.Errorf("create env root %s: %w", envRoot, err)
	}

	lockFile, err = openLockFile(filepath.Join(envRoot, envRootLockFile))
	if err != nil {
		return nil, false, fmt.Errorf("open env root lock for %s: %w", envRoot, err)
	}
	locked, err := lockFileExclusiveNonBlocking(lockFile)
	if err != nil {
		lockFile.Close()
		return nil, false, fmt.Errorf("lock env root %s: %w", envRoot, err)
	}
	if !locked {
		lockFile.Close()
		return nil, false, fmt.Errorf("env root %s is held by a running execution; refusing to reset it for task %s", envRoot, taskID)
	}
	// 从此刻起，本进程或其它任何进程都只有我们在碰这个 env root，下面的检查不会
	// 再被竞态。
	defer func() {
		if err != nil {
			releaseLockFile(lockFile)
			lockFile = nil
		}
	}()
	if err := removeStaleEnvRootOwnerTemps(envRoot); err != nil {
		return nil, false, fmt.Errorf("remove stale env root owner temp files for %s: %w", envRoot, err)
	}

	owner, err := ReadEnvRootOwner(envRoot)
	if err != nil {
		return nil, false, fmt.Errorf("read env root owner for %s: %w", envRoot, err)
	}
	switch {
	case owner.TaskID == taskID && (owner.WorkspaceID == "" || owner.WorkspaceID == workspaceID):
		// 趁锁在手、重写是独占的，把遗留的「仅任务」标记升级为带 workspace 的版本。
		// 之后磁盘占用统计就能按 workspace 归因活跃 root。
		if owner.WorkspaceID == "" {
			if err := writeEnvRootOwner(envRoot, workspaceID, taskID); err != nil {
				return nil, false, err
			}
		}
		// 是我们的，而且留下它的那次执行可证明已消失——我们正握着它本该继续持有的
		// 那把锁。
		return lockFile, true, nil
	case owner.TaskID != "":
		return nil, false, fmt.Errorf("env root %s belongs to task %s in workspace %s; refusing to reset it for task %s in workspace %s", envRoot, owner.TaskID, owner.WorkspaceID, taskID, workspaceID)
	}

	// 没有记录任何 owner。要么目录是全新的，要么一次崩溃在它指名任何人之前就撕坏了
	// 标记。无主的工作永远不是我们该删的；而一个空空如也的 env root 收养起来毫无
	// 代价。
	hasWork, err := envRootHoldsWork(envRoot)
	if err != nil {
		return nil, false, fmt.Errorf("inspect env root %s: %w", envRoot, err)
	}
	if hasWork {
		return nil, false, fmt.Errorf("env root %s already holds files but names no owning task; refusing to delete it", envRoot)
	}
	if err := writeEnvRootOwner(envRoot, workspaceID, taskID); err != nil {
		return nil, false, err
	}
	return lockFile, false, nil
}

// ReleaseLock 放下 env root 的执行锁。daemon 会在整个任务运行期 defer 它：锁的
// 生命周期属于「执行」，而不是这个值。Cleanup 单独不是合适的位置——生产环境从不
// 调用它（env root 由 GC 稍后回收），若只在 Cleanup 释放，锁会一直持有到 daemon
// 退出，把该任务的每一次合法重跑都挡在门外。nil 安全，允许调用两次。
func (env *Environment) ReleaseLock() {
	if env == nil || env.lockFile == nil {
		return
	}
	releaseLockFile(env.lockFile)
	env.lockFile = nil
}

// releaseLockFile 放下执行锁并关闭文件。nil 安全。
func releaseLockFile(f *os.File) {
	if f == nil {
		return
	}
	_ = unlockFile(f)
	_ = f.Close()
}

// writeEnvRootOwner 记录权威的 workspace/task 身份。调用方必须持有 env root 锁，
// 这把「覆写一个被撕坏或遗留的标记」在另一个执行卡在 claim 中途时变得安全。
// 同目录临时文件 + rename 让不持锁的读者永远不会读到一份截断的 JSON 标记。
func writeEnvRootOwner(envRoot, workspaceID, taskID string) error {
	path := filepath.Join(envRoot, envRootOwnerFile)
	data, err := json.Marshal(EnvRootOwner{WorkspaceID: workspaceID, TaskID: taskID})
	if err != nil {
		return fmt.Errorf("encode env root owner for %s: %w", envRoot, err)
	}

	tmp, err := os.CreateTemp(envRoot, envRootOwnerTempPrefix+"*"+envRootOwnerTempSuffix)
	if err != nil {
		return fmt.Errorf("create temp env root owner for %s: %w", envRoot, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp env root owner for %s: %w", envRoot, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp env root owner for %s: %w", envRoot, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp env root owner for %s: %w", envRoot, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp env root owner for %s: %w", envRoot, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace env root owner for %s: %w", envRoot, err)
	}
	return nil
}

// removeStaleEnvRootOwnerTemps 清理「在原子 rename 之前就退出」的进程留下的未发布
// 文件。调用方持有 env root 锁，因此不会有活动 owner 写操作正在使用这些文件。
func removeStaleEnvRootOwnerTemps(envRoot string) error {
	entries, err := os.ReadDir(envRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, envRootOwnerTempPrefix) || !strings.HasSuffix(name, envRootOwnerTempSuffix) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(envRoot, name)); err != nil {
			return err
		}
	}
	return nil
}

// readEnvRootOwner 返回拥有 envRoot 的任务 id；无标记或标记从未写完时返回 ""。
// 不可读的标记算错误而非「无主」：把它当无主会等于发给调用方一张删除许可证，去删
// 一个它根本没能识别身份的目录。
func readEnvRootOwner(envRoot string) (string, error) {
	owner, err := ReadEnvRootOwner(envRoot)
	if err != nil {
		return "", err
	}
	return owner.TaskID, nil
}

// EnvRootOwner 在任何任务内容写入之前就落盘，因此即使没有完成态元数据，活跃的或
// 部分准备好的根目录也保留着权威身份。
type EnvRootOwner struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	TaskID      string `json:"task_id"`
}

// ReadEnvRootOwner 同时读取当前的 JSON 标记与遗留的纯文本任务 ID 两种格式。
func ReadEnvRootOwner(envRoot string) (*EnvRootOwner, error) {
	b, err := os.ReadFile(filepath.Join(envRoot, envRootOwnerFile))
	if errors.Is(err, os.ErrNotExist) {
		return &EnvRootOwner{}, nil
	}
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(b))
	if !strings.HasPrefix(trimmed, "{") {
		return &EnvRootOwner{TaskID: trimmed}, nil
	}
	var owner EnvRootOwner
	if err := json.Unmarshal(b, &owner); err != nil {
		return nil, err
	}
	owner.TaskID = strings.TrimSpace(owner.TaskID)
	owner.WorkspaceID = strings.TrimSpace(owner.WorkspaceID)
	return &owner, nil
}

// resetEnvRootContents 清空一个「调用方已拥有并持锁」的 env root，同时保留簿记
// 文件。若改成删除再重建目录，则在重建完成前会把 claim 与锁一起丢掉——那恰好是
// claimEnvRoot 要封死的时间窗口。
func resetEnvRootContents(envRoot string) error {
	entries, err := os.ReadDir(envRoot)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if isEnvRootBookkeeping(e.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(envRoot, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// envRootHoldsWork 报告 envRoot 里是否装了 owner 标记与锁文件之外的任何东西——
// 也就是任何「任务可能丢失的内容」。
func envRootHoldsWork(envRoot string) (bool, error) {
	entries, err := os.ReadDir(envRoot)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !isEnvRootBookkeeping(e.Name()) {
			return true, nil
		}
	}
	return false, nil
}

func isEnvRootBookkeeping(name string) bool {
	if name == envRootOwnerFile || name == envRootLockFile {
		return true
	}
	// 未发布的 owner 临时文件是 writeEnvRootOwner 的崩溃残留，不是任务内容。
	// claimEnvRoot 会在自己检查前先清掉它们，但 findOwnedTaskRoot 是在「不持锁」
	// 的情况下检查候选 root 的——若把一个空临时文件当作「有工作」，收养逻辑就会
	// 拒绝一个根本不装任何任务内容的 root，把该任务永久卡死。
	return strings.HasPrefix(name, envRootOwnerTempPrefix) &&
		strings.HasSuffix(name, envRootOwnerTempSuffix)
}
