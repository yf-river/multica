// 文件职责：实现 `multica agent` 整棵 CLI 命令树——agent 的增删改查、归档/恢复、
// 任务列表、头像上传、技能指派（skills）与自定义环境变量（env）等子命令；其中
// env 走审计端点、mcp_config 的读取改道到 `agent env`，flag 注释里均有说明。文件
// 前半部集中了 CLI 的「任务/人类上下文」判定：通过 mat_ token、MULTICA_* 环境变量
// 与工作目录里残留的 daemon marker 三层信号判断当前是否跑在 daemon 托管的 agent
// 任务里，从而决定 server URL、workspace、配置文件根该从哪一层解析，并拦截「人
// 本地的登录/配置类命令在任务上下文里被误用」的路径。文末 Helpers 提供
// --custom-env / --mcp-config 三条 secret-safe 输入通道（行内 / stdin / 文件）的
// 解析与互斥校验，值一律当机密材料处理。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// agentCmd 是 `multica agent` 父命令，其下挂 list/get/create/update/archive/
// restore/tasks/avatar/skills/env 等子命令。
var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Work with agents",
}

var agentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List agents in the workspace",
	RunE:  runAgentList,
}

var agentGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get agent details",
	Args:  exactArgs(1),
	RunE:  runAgentGet,
}

var agentCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new agent",
	RunE:  runAgentCreate,
}

var agentUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update an agent",
	Args:  exactArgs(1),
	RunE:  runAgentUpdate,
}

var agentArchiveCmd = &cobra.Command{
	Use:   "archive <id>",
	Short: "Archive an agent",
	Args:  exactArgs(1),
	RunE:  runAgentArchive,
}

var agentRestoreCmd = &cobra.Command{
	Use:   "restore <id>",
	Short: "Restore an archived agent",
	Args:  exactArgs(1),
	RunE:  runAgentRestore,
}

var agentTasksCmd = &cobra.Command{
	Use:   "tasks <id>",
	Short: "List tasks for an agent",
	Args:  exactArgs(1),
	RunE:  runAgentTasks,
}

var agentAvatarCmd = &cobra.Command{
	Use:   "avatar <id>",
	Short: "Upload an avatar image for an agent",
	Args:  exactArgs(1),
	RunE:  runAgentAvatar,
}

// agentSkillsCmd 是 `agent skills` 分组命令，负责给 agent 指派工作区技能
// （list/set/add）。
var agentSkillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Manage agent skill assignments",
}

// agentEnvCmd 是 `agent env` 分组命令。custom_env 只在创建后通过这里读写，
// 因为 `agent list / get / update` 已不在线上暴露 env——每次调用都打到带审计
// 记录的 `/api/agents/{id}/env` 端点。参见 MUL-2600。
var agentEnvCmd = &cobra.Command{
	Use:   "env",
	Short: "Read and update an agent's custom environment variables (audited)",
}

var agentEnvGetCmd = &cobra.Command{
	Use:   "get <agent-id>",
	Short: "Print an agent's custom_env as a JSON map (agent owner or workspace owner/admin; every call is recorded)",
	Args:  exactArgs(1),
	RunE:  runAgentEnvGet,
}

var agentEnvSetCmd = &cobra.Command{
	Use:   "set <agent-id>",
	Short: "Replace an agent's custom_env (agent owner or workspace owner/admin; values equal to **** preserve the existing entry)",
	Args:  exactArgs(1),
	RunE:  runAgentEnvSet,
}

var agentSkillsListCmd = &cobra.Command{
	Use:   "list <agent-id>",
	Short: "List skills assigned to an agent",
	Args:  exactArgs(1),
	RunE:  runAgentSkillsList,
}

var agentSkillsSetCmd = &cobra.Command{
	Use:   "set <agent-id>",
	Short: "Set skills for an agent (replaces all current assignments)",
	Args:  exactArgs(1),
	RunE:  runAgentSkillsSet,
}

var agentSkillsAddCmd = &cobra.Command{
	Use:   "add <agent-id>",
	Short: "Add skills to an agent without replacing existing assignments",
	Args:  exactArgs(1),
	RunE:  runAgentSkillsAdd,
}

func init() {
	agentCmd.AddCommand(agentListCmd)
	agentCmd.AddCommand(agentGetCmd)
	agentCmd.AddCommand(agentCreateCmd)
	agentCmd.AddCommand(agentUpdateCmd)
	agentCmd.AddCommand(agentArchiveCmd)
	agentCmd.AddCommand(agentRestoreCmd)
	agentCmd.AddCommand(agentTasksCmd)
	agentCmd.AddCommand(agentAvatarCmd)
	agentCmd.AddCommand(agentSkillsCmd)
	agentCmd.AddCommand(agentEnvCmd)

	agentSkillsCmd.AddCommand(agentSkillsListCmd)
	agentSkillsCmd.AddCommand(agentSkillsSetCmd)
	agentSkillsCmd.AddCommand(agentSkillsAddCmd)

	agentEnvCmd.AddCommand(agentEnvGetCmd)
	agentEnvCmd.AddCommand(agentEnvSetCmd)

	// agent list
	agentListCmd.Flags().String("output", "table", "Output format: table or json")
	agentListCmd.Flags().Bool("include-archived", false, "Include archived agents")

	// agent get
	agentGetCmd.Flags().String("output", "json", "Output format: table or json")

	// agent create
	agentCreateCmd.Flags().String("name", "", "Agent name (required)")
	agentCreateCmd.Flags().String("description", "", "Agent description")
	agentCreateCmd.Flags().String("instructions", "", "Agent instructions")
	agentCreateCmd.Flags().String("runtime-id", "", "Runtime ID (required)")
	agentCreateCmd.Flags().String("runtime-config", "", "Runtime config as JSON string")
	agentCreateCmd.Flags().String("model", "", "Model identifier (e.g. claude-sonnet-4-6, openai/gpt-4o). Prefer this over passing --model in --custom-args.")
	agentCreateCmd.Flags().String("thinking-level", "", "Reasoning/effort level for the agent's runtime (e.g. Claude: low|medium|high|xhigh|max; Codex values come from the runtime model catalog). The set is runtime/model-specific; malformed values are rejected server-side and the daemon validates the exact model/level pair. Some runtimes (e.g. hermes) expose no reasoning control and reject every value. Empty = runtime default.")
	agentCreateCmd.Flags().String("service-tier", "", "Codex execution speed: empty = inherit local Codex configuration; default = explicit Standard when supported by the daemon's installed Codex CLI; a catalog tier such as priority = explicit Fast.")
	agentCreateCmd.Flags().String("custom-args", "", "Custom CLI arguments as JSON array. For model selection prefer --model; some providers reject --model in custom_args.")
	agentCreateCmd.Flags().String("custom-env", "", "Custom environment variables as JSON object, e.g. '{\"KEY\":\"value\"}'. Treated as secret material — never logged by the CLI, but values passed on the command line are visible to shell history and 'ps'; prefer --custom-env-stdin or --custom-env-file for real secrets. Pass '{}' to set an empty map.")
	agentCreateCmd.Flags().Bool("custom-env-stdin", false, "Read the --custom-env JSON object from stdin. Keeps secrets out of shell history and 'ps'. Mutually exclusive with --custom-env and --custom-env-file.")
	agentCreateCmd.Flags().String("custom-env-file", "", "Read the --custom-env JSON object from a file path (suggested mode: 0600). Mutually exclusive with --custom-env and --custom-env-stdin.")
	agentCreateCmd.Flags().String("mcp-config", "", "MCP server configuration as a JSON object, e.g. '{\"mcpServers\":{\"shortcut\":{...}}}'. Treated as secret material (MCP entries often carry API tokens) — never logged by the CLI, but values passed on the command line are visible to shell history and 'ps'; prefer --mcp-config-stdin or --mcp-config-file for real secrets.")
	agentCreateCmd.Flags().Bool("mcp-config-stdin", false, "Read the --mcp-config JSON object from stdin. Keeps secrets out of shell history and 'ps'. Mutually exclusive with --mcp-config and --mcp-config-file.")
	agentCreateCmd.Flags().String("mcp-config-file", "", "Read the --mcp-config JSON object from a file path (suggested mode: 0600). Mutually exclusive with --mcp-config and --mcp-config-stdin.")
	agentCreateCmd.Flags().String("visibility", "private", "Visibility: private or workspace (legacy; mapped to --permission-mode. private->private, workspace->public_to+workspace target)")
	agentCreateCmd.Flags().String("permission-mode", "", "Invocation permission mode: private (owner only) or public_to (allow-list via --public-to-*). Authoritative over --visibility when set.")
	agentCreateCmd.Flags().Bool("public-to-workspace", false, "public_to: allow every workspace member to invoke this agent.")
	agentCreateCmd.Flags().StringSlice("public-to-member", nil, "public_to: allow the given member user id(s) to invoke this agent. Repeatable.")
	agentCreateCmd.Flags().Int32("max-concurrent-tasks", 6, "Maximum concurrent tasks (1-50)")
	agentCreateCmd.Flags().String("output", "json", "Output format: table or json")

	// agent update
	agentUpdateCmd.Flags().String("name", "", "New name")
	agentUpdateCmd.Flags().String("description", "", "New description")
	agentUpdateCmd.Flags().String("instructions", "", "New instructions")
	agentUpdateCmd.Flags().String("runtime-id", "", "New runtime ID")
	agentUpdateCmd.Flags().String("runtime-config", "", "New runtime config as JSON string")
	agentUpdateCmd.Flags().String("model", "", "New model identifier. Pass an empty string to clear and fall back to the runtime default.")
	agentUpdateCmd.Flags().String("thinking-level", "", "New reasoning/effort level for the agent's runtime (e.g. Claude: low|medium|high|xhigh|max; Codex values come from the runtime model catalog). The set is runtime/model-specific; malformed values are rejected server-side and the daemon validates the exact model/level pair. Some runtimes (e.g. hermes) expose no reasoning control and reject every value. Pass an empty string to clear and fall back to the runtime default.")
	agentUpdateCmd.Flags().String("service-tier", "", "New Codex execution speed: default = explicit Standard when supported by the daemon's installed Codex CLI; a catalog tier such as priority = explicit Fast. Pass an empty string to clear and inherit local Codex configuration.")
	agentUpdateCmd.Flags().String("custom-args", "", "New custom CLI arguments as JSON array. For model selection prefer --model; some providers reject --model in custom_args.")
	// custom_env 有意不放进 `agent update`：要用请走 `multica agent env set
	// <id>`——那条路径允许 agent 属主或 workspace 属主/管理员操作、拒绝 agent
	// 角色，并留下持久化审计记录。
	//
	// mcp_config 与 custom_env 不同，确实可以在这里更新：它走通用的
	// UpdateAgent 端点持久化（没有专属审计端点）。提供与 `agent create`
	// 相同的三条 secret-safe 输入通道；传 `--mcp-config null` 表示清空。
	agentUpdateCmd.Flags().String("mcp-config", "", "New MCP server configuration as a JSON object, e.g. '{\"mcpServers\":{...}}'. Pass 'null' to clear. Treated as secret material — never logged by the CLI, but values passed on the command line are visible to shell history and 'ps'; prefer --mcp-config-stdin or --mcp-config-file for real secrets.")
	agentUpdateCmd.Flags().Bool("mcp-config-stdin", false, "Read the --mcp-config JSON from stdin. Keeps secrets out of shell history and 'ps'. Mutually exclusive with --mcp-config and --mcp-config-file.")
	agentUpdateCmd.Flags().String("mcp-config-file", "", "Read the --mcp-config JSON from a file path (suggested mode: 0600). Mutually exclusive with --mcp-config and --mcp-config-stdin.")
	agentUpdateCmd.Flags().String("visibility", "", "New visibility: private or workspace (legacy; mapped to --permission-mode)")
	agentUpdateCmd.Flags().String("permission-mode", "", "New invocation permission mode: private or public_to. Authoritative over --visibility. Owner-only.")
	agentUpdateCmd.Flags().Bool("public-to-workspace", false, "public_to: allow every workspace member to invoke this agent.")
	agentUpdateCmd.Flags().StringSlice("public-to-member", nil, "public_to: allow the given member user id(s) to invoke this agent. Repeatable.")
	agentUpdateCmd.Flags().String("status", "", "New status")
	agentUpdateCmd.Flags().Int32("max-concurrent-tasks", 0, "New max concurrent tasks (1-50)")
	agentUpdateCmd.Flags().String("output", "json", "Output format: table or json")

	// agent archive
	agentArchiveCmd.Flags().String("output", "json", "Output format: table or json")

	// agent restore
	agentRestoreCmd.Flags().String("output", "json", "Output format: table or json")

	// agent tasks
	agentTasksCmd.Flags().String("output", "table", "Output format: table or json")
	agentTasksCmd.Flags().Int("limit", 200, "Maximum tasks per page (1-200)")
	agentTasksCmd.Flags().String("before", "", "Continuation cursor from the previous response")

	// agent avatar
	agentAvatarCmd.Flags().String("file", "", "Path to the avatar image file (required)")
	agentAvatarCmd.Flags().String("output", "json", "Output format: table or json")

	// agent skills list
	agentSkillsListCmd.Flags().String("output", "table", "Output format: table or json")

	// agent skills set
	agentSkillsSetCmd.Flags().StringSlice("skill-ids", nil, "Skill IDs to assign (comma-separated)")
	agentSkillsSetCmd.Flags().String("output", "json", "Output format: table or json")

	// agent skills add
	agentSkillsAddCmd.Flags().StringSlice("skill-ids", nil, "Skill IDs to add (comma-separated)")
	agentSkillsAddCmd.Flags().String("output", "json", "Output format: table or json")

	// agent env get
	agentEnvGetCmd.Flags().String("output", "json", "Output format: json or table")

	// agent env set。与 `agent create` 相同的三条 secret-safe 输入通道，
	// 脚本可借此把机密挡在 shell history 之外。互斥与空输入处理由
	// resolveCustomEnv 统一把关。
	agentEnvSetCmd.Flags().String("custom-env", "", "Replacement custom_env as a JSON object, e.g. '{\"KEY\":\"value\"}'. Values equal to '****' preserve the existing entry. Treated as secret material — values passed on the command line are visible to shell history and 'ps'; prefer --custom-env-stdin or --custom-env-file for real secrets. Pass '{}' to clear all keys.")
	agentEnvSetCmd.Flags().Bool("custom-env-stdin", false, "Read the replacement custom_env JSON object from stdin. Keeps secrets out of shell history and 'ps'. Mutually exclusive with --custom-env and --custom-env-file.")
	agentEnvSetCmd.Flags().String("custom-env-file", "", "Read the replacement custom_env JSON object from a file path (suggested mode: 0600). Mutually exclusive with --custom-env and --custom-env-stdin.")
	agentEnvSetCmd.Flags().String("output", "json", "Output format: json or table")
}

// resolveProfile 返回 --profile flag 的取值（空串表示默认 profile）。
func resolveProfile(cmd *cobra.Command) string {
	val, _ := cmd.Flags().GetString("profile")
	return val
}

func newAPIClient(cmd *cobra.Command) (*cli.APIClient, error) {
	taskContext := inDaemonManagedExecutionContext()
	token := resolveToken(cmd)
	if taskContext && !strings.HasPrefix(token, "mat_") {
		// 当唯一的 daemon 信号是工作目录里的 marker（没有 MULTICA_AGENT_ID /
		// MULTICA_TASK_ID / MULTICA_DAEMON_PORT）时，任务外最可能的原因就是某个
		// 在 local_directory 里跑崩的 daemon 任务留下的残留 marker。报错里点名
		// 具体文件，普通用户照着手工删除即可恢复，而不是撞上一个晦涩的
		// "requires mat_ token"。措辞与 requireHumanLocalCommand 保持一致：
		// 同因同解。
		if markerPath := leftoverDaemonTaskMarkerPath(); markerPath != "" {
			return nil, fmt.Errorf("agent execution context requires MULTICA_TOKEN to be a task-scoped mat_ token%s", leftoverMarkerSuffix(markerPath))
		}
		return nil, fmt.Errorf("agent execution context requires MULTICA_TOKEN to be a task-scoped mat_ token%s", daemonPortOnlyContextHint())
	}

	serverURL := resolveServerURL(cmd)
	workspaceID := resolveWorkspaceID(cmd)
	if serverURL == "" {
		return nil, fmt.Errorf("server URL not set: use --server-url flag, MULTICA_SERVER_URL env, or 'multica config set server_url <url>'")
	}

	client := cli.NewAPIClient(serverURL, workspaceID, token)
	// 跑在 daemon 任务里时，把操作归因到对应 agent 上。
	if agentID := os.Getenv("MULTICA_AGENT_ID"); agentID != "" {
		client.AgentID = agentID
	}
	if taskID := os.Getenv("MULTICA_TASK_ID"); taskID != "" {
		client.TaskID = taskID
	}
	return client, nil
}

const (
	defaultCloudServerURL = "https://api.multica.ai"
	defaultCloudAppURL    = "https://multica.ai"
)

func tryResolveServerURL(cmd *cobra.Command) string {
	if val := tryResolveExplicitServerURL(cmd); val != "" {
		return val
	}
	if inDaemonManagedExecutionContext() && strings.TrimSpace(os.Getenv(cli.TaskConfigRootEnv)) == "" {
		return ""
	}
	return tryResolveProfileServerURL(cmd)
}

// tryResolveHumanServerURL 只给通过了 requireHumanLocalCommand 的「人类/本地」
// 命令用。与通用解析器不同：宿主机/容器环境里过期的 MULTICA_DAEMON_PORT 不能
// 掩盖 login 明确要更新的人类 profile。
func tryResolveHumanServerURL(cmd *cobra.Command) string {
	if val := tryResolveExplicitServerURL(cmd); val != "" {
		return val
	}
	return tryResolveProfileServerURL(cmd)
}

func tryResolveExplicitServerURL(cmd *cobra.Command) string {
	val := cli.FlagOrEnv(cmd, "server-url", "MULTICA_SERVER_URL", "")
	if val == "" {
		return ""
	}
	return normalizeAPIBaseURL(val)
}

func tryResolveProfileServerURL(cmd *cobra.Command) string {
	profile := resolveProfile(cmd)
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err == nil && cfg.ServerURL != "" {
		return normalizeAPIBaseURL(cfg.ServerURL)
	}
	return ""
}

func resolveServerURL(cmd *cobra.Command) string {
	if val := tryResolveServerURL(cmd); val != "" {
		return val
	}
	fmt.Fprintln(os.Stderr, missingServerConfigMessage())
	os.Exit(1)
	return "" // 不可达（上面已 os.Exit）
}

func missingServerConfigMessage() string {
	return fmt.Sprintf("No server configured. Run 'multica setup' first%s.", daemonPortOnlyContextHint())
}

func resolveHumanServerURL(cmd *cobra.Command) string {
	if val := tryResolveHumanServerURL(cmd); val != "" {
		return val
	}
	fmt.Fprintln(os.Stderr, "No server configured. Run 'multica setup' first.")
	os.Exit(1)
	return "" // 不可达（上面已 os.Exit）
}

func resolveLoginTokenServerURL(cmd *cobra.Command) string {
	if val := tryResolveHumanServerURL(cmd); val != "" {
		return val
	}
	return defaultCloudServerURL
}

func normalizeAPIBaseURL(raw string) string {
	normalized, err := daemon.NormalizeServerBaseURL(raw)
	if err == nil {
		return normalized
	}
	return raw
}

// inAgentExecutionContext 报告 CLI 是否带着 daemon 托管 agent 任务注入的显式
// 任务身份标记（MULTICA_AGENT_ID / MULTICA_TASK_ID）。
func inAgentExecutionContext() bool {
	return os.Getenv("MULTICA_AGENT_ID") != "" || os.Getenv("MULTICA_TASK_ID") != ""
}

// inDaemonManagedExecutionContext 报告 CLI 是否正跑在 daemon 托管的 agent 任务
// 里。MULTICA_DAEMON_PORT 被当作纵深防御的额外信号：某些子进程会丢掉
// MULTICA_AGENT_ID / MULTICA_TASK_ID，但仍处于 daemon 环境。此上下文里
// workspace 与 token 必须取自 daemon 注入的 env；回退到用户全局
// ~/.multica/config.json 可能让 agent 的写入落成普通成员身份。
func inDaemonManagedExecutionContext() bool {
	return inAgentExecutionContext() || os.Getenv("MULTICA_DAEMON_PORT") != "" || hasDaemonTaskContextMarker()
}

// inDaemonTaskIdentityContext 报告当前进程是否确实属于 daemon 托管任务的强
// 证据。MULTICA_DAEMON_PORT 单独不足以作数：较老的宿主机/容器环境可能在登录
// 或 daemon 启动前就把这个本无作用的任务提示导出了。
func inDaemonTaskIdentityContext() bool {
	return inAgentExecutionContext() ||
		strings.TrimSpace(os.Getenv(cli.TaskConfigRootEnv)) != "" ||
		hasDaemonTaskContextMarker()
}

func daemonPortOnlyContextHint() string {
	if strings.TrimSpace(os.Getenv("MULTICA_DAEMON_PORT")) == "" || inDaemonTaskIdentityContext() {
		return ""
	}
	return "; MULTICA_DAEMON_PORT is set without task identity — if this is a host or container startup shell, remove that variable and retry"
}

// requireTaskLocalConfigRoot 防止丢了部分注入环境变量的 daemon 托管子进程
// 静默地把 Multica 状态解析到 daemon 属主 HOME 之下的错误位置。明确支持
// task-local 配置的命令（目前是 config show/set 与 auth status）在任何加载
// 之前都会先调用它。
func requireTaskLocalConfigRoot() error {
	if !inDaemonManagedExecutionContext() {
		return nil
	}
	if strings.TrimSpace(os.Getenv(cli.TaskConfigRootEnv)) == "" {
		// 残留 marker 还能触发第三条拒绝路径（另外两条在 newAPIClient 与
		// requireHumanLocalCommand）。同样必须点名文件：用户通过 `config show`
		// 或 `auth status` 撞上它时和其它路径一样无助，而「你先跑了哪条命令」
		// 不该决定错误是否可操作。
		if markerPath := leftoverDaemonTaskMarkerPath(); markerPath != "" {
			return fmt.Errorf("daemon-managed task requires a task-local Multica config root in %s%s", cli.TaskConfigRootEnv, leftoverMarkerSuffix(markerPath))
		}
		return fmt.Errorf("daemon-managed task requires a task-local Multica config root in %s%s", cli.TaskConfigRootEnv, daemonPortOnlyContextHint())
	}
	return nil
}

// requireHumanLocalCommand 拒绝以认证、初始化或操作人类自有本地 daemon/profile
// 为目的的命令。任务 API 命令靠注入的 mat_ token 仍可用，这些本地命令不可用。
func requireHumanLocalCommand(command string) error {
	if !inDaemonTaskIdentityContext() {
		return nil
	}
	// 带任务作用域的工作目录 marker 且环境里没有任务身份，是唯一能活得比
	// 写出它的任务更久的信号：一次没做清理的 local_directory 运行会把 marker
	// 留在用户自己的仓库里，在有人手工删掉这个文件（MUL-6132）之前，它会禁用
	// 该目录树下本函数之后的所有命令。点名这个文件，用户才知道要删的是哪个；
	// 只有一句干巴巴的报错会把用户引向源码。与 newAPIClient 的残留 marker 处理
	// 互为镜像。
	if markerPath := leftoverDaemonTaskMarkerPath(); markerPath != "" {
		return fmt.Errorf("%s is not available inside a daemon-managed task%s", command, leftoverMarkerSuffix(markerPath))
	}
	return fmt.Errorf("%s is not available inside a daemon-managed task", command)
}

func hasDaemonTaskContextMarker() bool {
	return daemonTaskContextMarkerPath() != ""
}

// daemonTaskContextMarkerPath 从当前工作目录逐级向上，返回第一个可读、且其
// managed_by 匹配的 daemon 任务 marker 的路径；找不到时返回空串。
func daemonTaskContextMarkerPath() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		markerPath := filepath.Join(dir, execenv.TaskContextMarkerRelPath)
		// 只有「能读到」且「managed_by 匹配」的 marker 才算 daemon 任务信号；
		// 其它任何结果——文件缺失、路径不可读、或同名文件来自他人——一律视为
		// 「此处无信号」，继续向上走。不能在无关的读取错误上 fail closed（例如
		// 普通用户机器上某个祖先目录不可搜索），那会无缘无故拒绝用户的 PAT；
		// daemon 把 marker 写成 agent 自己工作目录里人人可读的文件，合法 agent
		// 总能读到它。
		if data, err := os.ReadFile(markerPath); err == nil {
			var marker struct {
				ManagedBy string `json:"managed_by"`
			}
			if json.Unmarshal(data, &marker) == nil && marker.ManagedBy == execenv.TaskContextMarkerManagedBy {
				return markerPath
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func resolveWorkspaceID(cmd *cobra.Command) string {
	val := cli.FlagOrEnv(cmd, "workspace-id", "MULTICA_WORKSPACE_ID", "")
	if val != "" {
		return val
	}
	// agent 任务内部，workspace 身份的唯一权威是 daemon；绝不回读用户全局 CLI
	// 配置。
	if inDaemonManagedExecutionContext() {
		return ""
	}
	profile := resolveProfile(cmd)
	cfg, _ := cli.LoadCLIConfigForProfile(profile)
	return cfg.WorkspaceID
}

// requireWorkspaceID 解析 workspace ID，若为空（例如用户有多个 workspace 却没
// 配置默认值）则返回带可操作指引的错误。
func requireWorkspaceID(cmd *cobra.Command) (string, error) {
	id := resolveWorkspaceID(cmd)
	if id == "" {
		if inDaemonManagedExecutionContext() {
			return "", fmt.Errorf("workspace_id is required: MULTICA_WORKSPACE_ID must be set by the daemon in agent execution context (no fallback to user config)")
		}
		return "", fmt.Errorf("workspace_id is required: use --workspace-id flag, set MULTICA_WORKSPACE_ID env, or run 'multica config set workspace_id <id>'")
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// agent 主命令（run* 实现）
// ---------------------------------------------------------------------------

func runAgentList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if client.WorkspaceID == "" {
		if _, err := requireWorkspaceID(cmd); err != nil {
			return err
		}
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var agents []map[string]any
	params := url.Values{}
	params.Set("workspace_id", client.WorkspaceID)
	if v, _ := cmd.Flags().GetBool("include-archived"); v {
		params.Set("include_archived", "true")
	}
	path := "/api/agents"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	if err := client.GetJSON(ctx, path, &agents); err != nil {
		return fmt.Errorf("list agents: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, agents)
	}

	headers := []string{"ID", "NAME", "STATUS", "RUNTIME", "ARCHIVED"}
	rows := make([][]string, 0, len(agents))
	for _, a := range agents {
		archived := ""
		if v := strVal(a, "archived_at"); v != "" {
			archived = "yes"
		}
		rows = append(rows, []string{
			strVal(a, "id"),
			strVal(a, "name"),
			strVal(a, "status"),
			strVal(a, "runtime_mode"),
			archived,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runAgentGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var agent map[string]any
	if err := client.GetJSON(ctx, "/api/agents/"+args[0], &agent); err != nil {
		return fmt.Errorf("get agent: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, agent)
	}

	headers := []string{"ID", "NAME", "STATUS", "RUNTIME", "VISIBILITY", "AVATAR_URL", "DESCRIPTION"}
	rows := [][]string{{
		strVal(agent, "id"),
		strVal(agent, "name"),
		strVal(agent, "status"),
		strVal(agent, "runtime_mode"),
		strVal(agent, "visibility"),
		strVal(agent, "avatar_url"),
		strVal(agent, "description"),
	}}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// applyAgentPermissionFlags 把调用权限相关 flag（--permission-mode /
// --public-to-workspace / --public-to-member）翻译成 permission_mode +
// invocation_targets 请求字段（MUL-3963）。这些 flag 一个都没设置时是 no-op，
// 继续由旧的 --visibility 处理逻辑驱动请求；只给了 public-to-* 却没显式
// --permission-mode 时，mode 默认取 public_to。
func applyAgentPermissionFlags(cmd *cobra.Command, body map[string]any) {
	hasMode := cmd.Flags().Changed("permission-mode")
	hasWorkspace := cmd.Flags().Changed("public-to-workspace")
	hasMembers := cmd.Flags().Changed("public-to-member")
	if !hasMode && !hasWorkspace && !hasMembers {
		return
	}

	mode := "public_to"
	if hasMode {
		mode, _ = cmd.Flags().GetString("permission-mode")
	}
	body["permission_mode"] = mode

	targets := []map[string]any{}
	if on, _ := cmd.Flags().GetBool("public-to-workspace"); on {
		targets = append(targets, map[string]any{"target_type": "workspace"})
	}
	if members, _ := cmd.Flags().GetStringSlice("public-to-member"); len(members) > 0 {
		for _, m := range members {
			targets = append(targets, map[string]any{"target_type": "member", "target_id": m})
		}
	}
	body["invocation_targets"] = targets
}

func runAgentCreate(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	runtimeID, _ := cmd.Flags().GetString("runtime-id")
	if runtimeID == "" {
		return fmt.Errorf("--runtime-id is required")
	}

	body := map[string]any{
		"name":       name,
		"runtime_id": runtimeID,
	}
	if v, _ := cmd.Flags().GetString("description"); v != "" {
		body["description"] = v
	}
	if v, _ := cmd.Flags().GetString("instructions"); v != "" {
		body["instructions"] = v
	}
	if cmd.Flags().Changed("runtime-config") {
		v, _ := cmd.Flags().GetString("runtime-config")
		var rc any
		if err := json.Unmarshal([]byte(v), &rc); err != nil {
			return fmt.Errorf("--runtime-config must be valid JSON: %w", err)
		}
		body["runtime_config"] = rc
	}
	if cmd.Flags().Changed("custom-args") {
		v, _ := cmd.Flags().GetString("custom-args")
		ca, err := parseCustomArgs(v)
		if err != nil {
			return err
		}
		body["custom_args"] = ca
	}
	if ce, ok, err := resolveCustomEnv(cmd); err != nil {
		return err
	} else if ok {
		body["custom_env"] = ce
	}
	if mc, ok, err := resolveMcpConfig(cmd); err != nil {
		return err
	} else if ok {
		body["mcp_config"] = mc
	}
	if cmd.Flags().Changed("model") {
		v, _ := cmd.Flags().GetString("model")
		body["model"] = v
	}
	// thinking_level 与 model 一样只是薄透传：直接落到 server 已接受并校验的
	// agent 顶层字段（IsKnownThinkingValue）。CLI 有意不枚举合法取值——它们是
	// runtime/model 相关的，目录由 server 维护（MUL-2339）。
	if cmd.Flags().Changed("thinking-level") {
		v, _ := cmd.Flags().GetString("thinking-level")
		body["thinking_level"] = v
	}
	if cmd.Flags().Changed("service-tier") {
		v, _ := cmd.Flags().GetString("service-tier")
		body["service_tier"] = v
	}
	if cmd.Flags().Changed("visibility") {
		v, _ := cmd.Flags().GetString("visibility")
		body["visibility"] = v
	}
	applyAgentPermissionFlags(cmd, body)
	if cmd.Flags().Changed("max-concurrent-tasks") {
		v, _ := cmd.Flags().GetInt32("max-concurrent-tasks")
		if err := validateAgentMaxConcurrentTasksFlag(v); err != nil {
			return err
		}
		body["max_concurrent_tasks"] = v
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/agents", body, &result); err != nil {
		return fmt.Errorf("create agent: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	fmt.Printf("Agent created: %s (%s)\n", strVal(result, "name"), strVal(result, "id"))
	return nil
}

func runAgentUpdate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	body := map[string]any{}
	if cmd.Flags().Changed("name") {
		v, _ := cmd.Flags().GetString("name")
		body["name"] = v
	}
	if cmd.Flags().Changed("description") {
		v, _ := cmd.Flags().GetString("description")
		body["description"] = v
	}
	if cmd.Flags().Changed("instructions") {
		v, _ := cmd.Flags().GetString("instructions")
		body["instructions"] = v
	}
	if cmd.Flags().Changed("runtime-id") {
		v, _ := cmd.Flags().GetString("runtime-id")
		body["runtime_id"] = v
	}
	if cmd.Flags().Changed("runtime-config") {
		v, _ := cmd.Flags().GetString("runtime-config")
		var rc any
		if err := json.Unmarshal([]byte(v), &rc); err != nil {
			return fmt.Errorf("--runtime-config must be valid JSON: %w", err)
		}
		body["runtime_config"] = rc
	}
	if cmd.Flags().Changed("custom-args") {
		v, _ := cmd.Flags().GetString("custom-args")
		ca, err := parseCustomArgs(v)
		if err != nil {
			return err
		}
		body["custom_args"] = ca
	}
	if cmd.Flags().Changed("model") {
		v, _ := cmd.Flags().GetString("model")
		body["model"] = v
	}
	// thinking_level 在 server 端是三态（缺省 = 不改，"" = 清回 runtime 默认，
	// 有值 = 设置）。只在用户提供了对应 flag 时才发送该 key，正好还原这种三态
	// 语义，与 --model 的行为一致。
	if cmd.Flags().Changed("thinking-level") {
		v, _ := cmd.Flags().GetString("thinking-level")
		body["thinking_level"] = v
	}
	if cmd.Flags().Changed("service-tier") {
		v, _ := cmd.Flags().GetString("service-tier")
		body["service_tier"] = v
	}
	if cmd.Flags().Changed("visibility") {
		v, _ := cmd.Flags().GetString("visibility")
		body["visibility"] = v
	}
	applyAgentPermissionFlags(cmd, body)
	if cmd.Flags().Changed("status") {
		v, _ := cmd.Flags().GetString("status")
		body["status"] = v
	}
	if cmd.Flags().Changed("max-concurrent-tasks") {
		v, _ := cmd.Flags().GetInt32("max-concurrent-tasks")
		if err := validateAgentMaxConcurrentTasksFlag(v); err != nil {
			return err
		}
		body["max_concurrent_tasks"] = v
	}
	if mc, ok, err := resolveMcpConfig(cmd); err != nil {
		return err
	} else if ok {
		body["mcp_config"] = mc
	}

	if len(body) == 0 {
		return fmt.Errorf("no fields to update; use --name, --description, --instructions, --runtime-id, --runtime-config, --model, --thinking-level, --service-tier, --custom-args, --mcp-config, --visibility, --status, or --max-concurrent-tasks (env vars now live behind `multica agent env set <id>`)")
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var result map[string]any
	if err := client.PutJSON(ctx, "/api/agents/"+args[0], body, &result); err != nil {
		return fmt.Errorf("update agent: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	fmt.Printf("Agent updated: %s (%s)\n", strVal(result, "name"), strVal(result, "id"))
	return nil
}

func runAgentArchive(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/agents/"+args[0]+"/archive", nil, &result); err != nil {
		return fmt.Errorf("archive agent: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	fmt.Printf("Agent archived: %s (%s)\n", strVal(result, "name"), strVal(result, "id"))
	return nil
}

func runAgentRestore(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/agents/"+args[0]+"/restore", nil, &result); err != nil {
		return fmt.Errorf("restore agent: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	fmt.Printf("Agent restored: %s (%s)\n", strVal(result, "name"), strVal(result, "id"))
	return nil
}

func runAgentTasks(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	output, _ := cmd.Flags().GetString("output")
	params := url.Values{}
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 1 {
		return fmt.Errorf("limit must be a positive integer")
	}
	params.Set("limit", fmt.Sprint(limit))
	before, _ := cmd.Flags().GetString("before")
	if before != "" {
		params.Set("before", before)
	}
	if output == "json" {
		params.Set("include_usage", "true")
	}
	path := "/api/agents/" + args[0] + "/tasks?" + params.Encode()

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var tasks []map[string]any
	responseHeaders, err := client.GetJSONWithHeaders(ctx, path, &tasks)
	if err != nil {
		return fmt.Errorf("list agent tasks: %w", err)
	}
	if cursor := responseHeaders.Get("X-Agent-Tasks-Next-Cursor"); cursor != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "More runs available; use --before %q to fetch the next page.\n", cursor)
	}

	if output == "json" {
		return cli.PrintJSON(os.Stdout, tasks)
	}

	headers := []string{"ID", "ISSUE_ID", "STATUS", "CREATED_AT"}
	rows := make([][]string, 0, len(tasks))
	for _, t := range tasks {
		rows = append(rows, []string{
			strVal(t, "id"),
			strVal(t, "issue_id"),
			strVal(t, "status"),
			strVal(t, "created_at"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runAgentAvatar(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	filePath, _ := cmd.Flags().GetString("file")
	if filePath == "" {
		return fmt.Errorf("--file is required")
	}

	// 先校验文件存在。
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("file not found: %w", err)
	}

	// 校验扩展名。
	ext := strings.ToLower(filepath.Ext(filePath))
	validExts := map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}
	if !validExts[ext] {
		return fmt.Errorf("unsupported file format %q: must be .png, .jpg, .jpeg, .gif, or .webp", ext)
	}

	// 客户端侧大小闸门：拒绝超过 5MB 的文件。
	const maxSize = 5 << 20 // 5 MB
	if info.Size() > maxSize {
		return fmt.Errorf("file too large: %d bytes (max 5MB)", info.Size())
	}

	fileData, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	// 防御性复查：防止 TOCTOU 竞态——stat 与 read 之间文件被调包。
	if len(fileData) > maxSize {
		return fmt.Errorf("file too large: %d bytes (max 5MB)", len(fileData))
	}

	ctx, cancel := context.WithTimeout(context.Background(), cli.AtLeastAPITimeout(60*time.Second))
	defer cancel()

	// 上传前先确认 agent 存在。
	var agent map[string]any
	if err := client.GetJSON(ctx, "/api/agents/"+args[0], &agent); err != nil {
		return fmt.Errorf("get agent: %w", err)
	}

	id, url, err := client.UploadFileWithURL(ctx, fileData, filePath)
	if err != nil {
		return fmt.Errorf("upload avatar: %w", err)
	}

	body := map[string]any{"avatar_url": url}
	var result map[string]any
	if err := client.PutJSON(ctx, "/api/agents/"+args[0], body, &result); err != nil {
		return fmt.Errorf("update agent avatar: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{
			"id":         id,
			"agent_id":   args[0],
			"avatar_url": url,
		})
	}

	headers := []string{"ID", "AGENT_ID", "AVATAR_URL"}
	rows := [][]string{{
		id,
		args[0],
		url,
	}}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// ---------------------------------------------------------------------------
// agent skills 子命令
// ---------------------------------------------------------------------------

func runAgentSkillsList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var skills []map[string]any
	if err := client.GetJSON(ctx, "/api/agents/"+args[0]+"/skills", &skills); err != nil {
		return fmt.Errorf("list agent skills: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, skills)
	}

	headers := []string{"ID", "NAME", "DESCRIPTION"}
	rows := make([][]string, 0, len(skills))
	for _, s := range skills {
		rows = append(rows, []string{
			strVal(s, "id"),
			strVal(s, "name"),
			strVal(s, "description"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runAgentSkillsSet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	if !cmd.Flags().Changed("skill-ids") {
		return fmt.Errorf("--skill-ids is required (comma-separated skill IDs; use --skill-ids '' to clear all)")
	}
	cleanIDs := cleanSkillIDsFlag(cmd)
	body := map[string]any{
		"skill_ids": cleanIDs,
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var result json.RawMessage
	if err := client.PutJSON(ctx, "/api/agents/"+args[0]+"/skills", body, &result); err != nil {
		return fmt.Errorf("set agent skills: %w", err)
	}

	return printAgentSkillsMutationResult(cmd, args[0], result)
}

func runAgentSkillsAdd(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	if !cmd.Flags().Changed("skill-ids") {
		return fmt.Errorf("--skill-ids is required (comma-separated skill IDs)")
	}
	cleanIDs := cleanSkillIDsFlag(cmd)
	if len(cleanIDs) == 0 {
		return fmt.Errorf("--skill-ids must include at least one skill ID")
	}
	body := map[string]any{
		"skill_ids": cleanIDs,
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var result json.RawMessage
	if err := client.PostJSON(ctx, "/api/agents/"+args[0]+"/skills/add", body, &result); err != nil {
		return fmt.Errorf("add agent skills: %w", err)
	}

	return printAgentSkillsMutationResult(cmd, args[0], result)
}

func cleanSkillIDsFlag(cmd *cobra.Command) []string {
	skillIDs, _ := cmd.Flags().GetStringSlice("skill-ids")
	cleanIDs := make([]string, 0, len(skillIDs))
	for _, id := range skillIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			cleanIDs = append(cleanIDs, id)
		}
	}
	return cleanIDs
}

func printAgentSkillsMutationResult(cmd *cobra.Command, agentID string, result json.RawMessage) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		var pretty any
		json.Unmarshal(result, &pretty)
		return cli.PrintJSON(os.Stdout, pretty)
	}

	var skills []map[string]any
	if err := json.Unmarshal(result, &skills); err != nil {
		return fmt.Errorf("decode agent skills response: %w", err)
	}
	if len(skills) == 0 {
		fmt.Printf("No skills assigned to agent %s\n", agentID)
		return nil
	}
	headers := []string{"ID", "NAME", "DESCRIPTION"}
	rows := make([][]string, 0, len(skills))
	for _, s := range skills {
		rows = append(rows, []string{
			strVal(s, "id"),
			strVal(s, "name"),
			strVal(s, "description"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// ---------------------------------------------------------------------------
// agent env 子命令
// ---------------------------------------------------------------------------

// runAgentEnvGet 经带审计的 `/env` 端点取回单个 agent 的明文 custom_env。
// JSON 模式下原样打印原始 JSON，否则打 key/value 表；这里从不截断或打码——
// 安全闸门在 server 端，不在打印端。
func runAgentEnvGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var resp map[string]any
	if err := client.GetJSON(ctx, "/api/agents/"+args[0]+"/env", &resp); err != nil {
		return fmt.Errorf("get agent env: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, resp)
	}

	headers := []string{"KEY", "VALUE"}
	env, _ := resp["custom_env"].(map[string]any)
	rows := make([][]string, 0, len(env))
	for k, v := range env {
		rows = append(rows, []string{k, fmt.Sprintf("%v", v)})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// runAgentEnvSet 经带审计的 `/env` 端点整体替换 agent 的 custom_env。三条
// secret-safe 输入通道（--custom-env / --custom-env-stdin /
// --custom-env-file）至少要给一个；server 把值为 "****" 的条目视为「保留
// 原值」（见 handler 里的 **** 防护）。
func runAgentEnvSet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ce, ok, err := resolveCustomEnv(cmd)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("specify the new env via --custom-env, --custom-env-stdin, or --custom-env-file (pass '{}' to clear)")
	}

	body := map[string]any{"custom_env": ce}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var result map[string]any
	if err := client.PutJSON(ctx, "/api/agents/"+args[0]+"/env", body, &result); err != nil {
		return fmt.Errorf("update agent env: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	env, _ := result["custom_env"].(map[string]any)
	fmt.Printf("Env updated for agent %s (%d keys)\n", args[0], len(env))
	return nil
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// parseCustomEnv 把 --custom-env flag 的值（一个 JSON 对象字面量）解析成适合放
// 进请求体的 string map。「全部清空」的信号是显式的 JSON 对象 "{}"；空串或纯
// 空白输入会被拒绝，因为对 stdin/文件通道而言，那几乎总意味着上游故障（文件
// 缺失、管道未接、set -o pipefail 未关）而不是有意清空——把这种情况当成「清空」
// 会在无声中抹掉机密。
//
// 载荷按机密材料对待：解析错误从不包装底层 json 错误，因为某些畸形输入会让
// json.SyntaxError / UnmarshalTypeError 泄出输入的一小段内容。
func parseCustomEnv(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("--custom-env: empty input; pass '{}' to clear")
	}
	var ce map[string]string
	if err := json.Unmarshal([]byte(raw), &ce); err != nil {
		return nil, fmt.Errorf("--custom-env must be a valid JSON object of string keys and string values")
	}
	if ce == nil {
		ce = map[string]string{}
	}
	return ce, nil
}

// parseCustomArgs 解析 --custom-args flag 的值（CLI 参数字符串组成的 JSON
// 数组）。错误信息不带内容，理由与 parseCustomEnv 相同：custom_args 目前虽然
// 不是专属机密通道，但它经常携带如 "--api-key=…" 之类的 runtime provider
// 取值，json.Unmarshal 错误可能回显畸形输入的一小段内容。
func parseCustomArgs(raw string) ([]string, error) {
	var ca []string
	if err := json.Unmarshal([]byte(raw), &ca); err != nil {
		return nil, fmt.Errorf("--custom-args must be a valid JSON array of strings")
	}
	return ca, nil
}

// resolveCustomEnv 汇总 --custom-env / --custom-env-stdin / --custom-env-file
// 三个 flag，返回解析好的 map、一个表示「调用方是否确实提供了其中任一通道」的
// bool，以及可能的错误。三条通道互斥，避免调用方不小心把同一个机密给两次；
// stdin 与文件输入存在的意义是把机密材料挡在 shell history 和
// 'ps' / /proc/<pid>/cmdline 之外。
func resolveCustomEnv(cmd *cobra.Command) (map[string]string, bool, error) {
	inline := cmd.Flags().Changed("custom-env")
	fromStdin, _ := cmd.Flags().GetBool("custom-env-stdin")
	filePath, _ := cmd.Flags().GetString("custom-env-file")
	// 注意：显式传 `--custom-env-file ""` 会被当作「用户明确选了这条通道但给了
	// 空路径」对待，在下方抛出一个真实错误，而不是被静默吞掉。
	fromFile := cmd.Flags().Changed("custom-env-file")

	count := 0
	if inline {
		count++
	}
	if fromStdin {
		count++
	}
	if fromFile {
		count++
	}
	switch {
	case count == 0:
		return nil, false, nil
	case count > 1:
		return nil, false, fmt.Errorf("--custom-env, --custom-env-stdin, and --custom-env-file are mutually exclusive; pick one")
	}

	var raw string
	switch {
	case inline:
		raw, _ = cmd.Flags().GetString("custom-env")
	case fromStdin:
		buf, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return nil, false, fmt.Errorf("read --custom-env-stdin: %w", err)
		}
		raw = string(buf)
		if strings.TrimSpace(raw) == "" {
			return nil, false, fmt.Errorf("--custom-env-stdin: empty input; pass '{}' to clear")
		}
	case fromFile:
		if filePath == "" {
			return nil, false, fmt.Errorf("--custom-env-file: path must not be empty")
		}
		buf, err := os.ReadFile(filePath)
		if err != nil {
			// 文件系统错误可能带路径但不会带内容——用 %w 透传是安全的。
			return nil, false, fmt.Errorf("read --custom-env-file: %w", err)
		}
		raw = string(buf)
		if strings.TrimSpace(raw) == "" {
			return nil, false, fmt.Errorf("--custom-env-file %q: empty contents; pass '{}' to clear", filePath)
		}
	}

	ce, err := parseCustomEnv(raw)
	if err != nil {
		return nil, false, err
	}
	return ce, true, nil
}

// parseMcpConfig 校验 --mcp-config 的值，返回要发送的原始 JSON。它接受一个
// JSON 对象（MCP 配置，如 {"mcpServers": {…}}），或表示清空该 agent 配置的
// 字面量 `null`。
func parseMcpConfig(raw string) (json.RawMessage, error) {
	return parseMcpJSONObject("--mcp-config", raw, true)
}

// parseMcpJSONObject 校验经某条 secret-safe 输入通道送来的 JSON 对象载荷。顶层
// 数组或基本类型会被拒绝，因为它们永远不可能是合法的 MCP 载荷——这与 agent
// 设置 UI（mcp-config-tab.tsx）一致。空串/纯空白输入同样被拒绝而非当作清空：
// 对 stdin/文件通道它几乎总意味着上游故障（文件缺失、管道未接），而不是有意
// 清空，而静默抹掉一个含机密的字段是错误的默认行为。
//
// allowNull 控制字面量 `null` 是否被接受为「清空」哨兵。单个 server 条目不适用
// ——移除某条有自己的命令，此时 `null` 更可能是手误。
//
// 载荷按机密材料对待（MCP 条目通常携带 API token），解析错误从不包装底层
// json 错误，以免回显畸形输入的一小段内容。
func parseMcpJSONObject(flag, raw string, allowNull bool) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if allowNull {
			return nil, fmt.Errorf("%s: empty input; pass 'null' to clear or a JSON object to set", flag)
		}
		return nil, fmt.Errorf("%s: empty input; pass a JSON object", flag)
	}
	var probe any
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		if allowNull {
			return nil, fmt.Errorf("%s must be a valid JSON object, or 'null' to clear", flag)
		}
		return nil, fmt.Errorf("%s must be a valid JSON object", flag)
	}
	if probe == nil {
		if allowNull {
			// null → 清空（server 端对应 NULL 列；create 时是 no-op）。
			return json.RawMessage("null"), nil
		}
		return nil, fmt.Errorf("%s must be a JSON object, not null", flag)
	}
	if _, ok := probe.(map[string]any); !ok {
		if allowNull {
			return nil, fmt.Errorf("%s must be a JSON object, or 'null' to clear", flag)
		}
		return nil, fmt.Errorf("%s must be a JSON object", flag)
	}
	return json.RawMessage(trimmed), nil
}

// resolveMcpConfig 汇总 --mcp-config / --mcp-config-stdin / --mcp-config-file
// 三个 flag，返回要发送的原始 JSON 值、一个表示「调用方是否确实提供了任一
// 通道」的 bool，以及可能的错误。与 resolveCustomEnv 互为镜像，唯一行为差异是
// 清空哨兵（这里是 `null`，custom_env 是 `{}`）——因为 server 端区分「显式空
// 对象」与「未提供配置」。
func resolveMcpConfig(cmd *cobra.Command) (json.RawMessage, bool, error) {
	return resolveMcpJSONObject(cmd, "mcp-config", true)
}

// resolveMcpJSONObject 汇总 `<prefix>` / `<prefix>-stdin` / `<prefix>-file` 三个
// flag。三条通道互斥，避免调用方不小心把同一个机密给两次；stdin 与文件输入的
// 存在意义是把载荷——它通常内嵌 API token——挡在 shell history 和 'ps' 之外。
func resolveMcpJSONObject(cmd *cobra.Command, prefix string, allowNull bool) (json.RawMessage, bool, error) {
	inline := cmd.Flags().Changed(prefix)
	fromStdin, _ := cmd.Flags().GetBool(prefix + "-stdin")
	filePath, _ := cmd.Flags().GetString(prefix + "-file")
	fromFile := cmd.Flags().Changed(prefix + "-file")

	count := 0
	if inline {
		count++
	}
	if fromStdin {
		count++
	}
	if fromFile {
		count++
	}
	switch {
	case count == 0:
		return nil, false, nil
	case count > 1:
		return nil, false, fmt.Errorf("--%s, --%s-stdin, and --%s-file are mutually exclusive; pick one", prefix, prefix, prefix)
	}

	clearHint := ""
	if allowNull {
		clearHint = "; pass 'null' to clear"
	}
	var raw string
	switch {
	case inline:
		raw, _ = cmd.Flags().GetString(prefix)
	case fromStdin:
		buf, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return nil, false, fmt.Errorf("read --%s-stdin: %w", prefix, err)
		}
		raw = string(buf)
		if strings.TrimSpace(raw) == "" {
			return nil, false, fmt.Errorf("--%s-stdin: empty input%s", prefix, clearHint)
		}
	case fromFile:
		if filePath == "" {
			return nil, false, fmt.Errorf("--%s-file: path must not be empty", prefix)
		}
		buf, err := os.ReadFile(filePath)
		if err != nil {
			// 文件系统错误可能带路径但不会带内容——用 %w 透传是安全的。
			return nil, false, fmt.Errorf("read --%s-file: %w", prefix, err)
		}
		raw = string(buf)
		if strings.TrimSpace(raw) == "" {
			return nil, false, fmt.Errorf("--%s-file %q: empty contents%s", prefix, filePath, clearHint)
		}
	}

	mc, err := parseMcpJSONObject("--"+prefix, raw, allowNull)
	if err != nil {
		return nil, false, err
	}
	return mc, true, nil
}

func strVal(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}
