// 文件职责：实现 `multica workspace` 命令组——工作区的列出/创建/查看/更新与
// 「默认工作区」切换（switch）、成员管理（member list/invite），以及工作区级 MCP
// server 库的管理（mcp list/add/update/remove）。工作区引用统一收口在
// resolveWorkspaceRef（支持 UUID/slug/短前缀）。MCP 库是写后不可读的（配置只写
// 不回显），见各命令 Long。
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// workspaceCmd 是 `multica workspace` 父命令。
var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Work with workspaces",
}

// workspaceListCmd 列出当前登录用户所属的全部工作区。
var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all workspaces you belong to",
	RunE:  runWorkspaceList,
}

var workspaceCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a workspace",
	Long: "Creates a new workspace and adds you as its owner. Both --name and " +
		"--slug are required; the slug is permanent (lowercase letters, digits, " +
		"and hyphens) and cannot be changed after creation.\n\n" +
		"Creating a workspace does NOT change the current default workspace for " +
		"this profile — run 'multica workspace switch <slug>' afterward if you " +
		"want subsequent commands to target the new workspace.",
	Example: "  multica workspace create --name \"Support Team\" --slug support-team --issue-prefix SUP",
	Args:    cobra.NoArgs,
	RunE:    runWorkspaceCreate,
}

var workspaceGetCmd = &cobra.Command{
	Use:   "get [workspace-id|slug|prefix]",
	Short: "Get workspace details",
	Long: "Prints the full details of a workspace. The argument accepts a full " +
		"UUID, a slug, or a short UUID prefix (≥4 hex chars) as shown in " +
		"'workspace list'. If omitted, the current default workspace is used.",
	Args: cobra.MaximumNArgs(1),
	RunE: runWorkspaceGet,
}

var workspaceMemberCmd = &cobra.Command{
	Use:   "member",
	Short: "Manage workspace members",
}

var workspaceMemberListCmd = &cobra.Command{
	Use:   "list [workspace-id|slug|prefix]",
	Short: "List workspace members",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runWorkspaceMembers,
}

var workspaceMemberInviteCmd = &cobra.Command{
	Use:   "invite <email> [workspace-id|slug|prefix]",
	Short: "Invite a member to a workspace by email",
	Long: "Sends a workspace invitation to an email address. The invitee gets a " +
		"pending invitation they must accept before they join — this does not " +
		"add them instantly. The optional workspace argument accepts a full " +
		"UUID, a slug, or a short UUID prefix (≥4 hex chars) as shown in " +
		"'workspace list'; if omitted the current default workspace is used " +
		"(--workspace-id / MULTICA_WORKSPACE_ID / profile default).\n\n" +
		"Role defaults to 'member'; pass '--role admin' to invite an admin. " +
		"Owners cannot be invited.",
	Args: cobra.RangeArgs(1, 2),
	RunE: runWorkspaceMemberInvite,
}

var workspaceUpdateCmd = &cobra.Command{
	Use:   "update [workspace-id|slug|prefix]",
	Short: "Update workspace metadata (admin/owner only)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runWorkspaceUpdate,
}

var workspaceSwitchCmd = &cobra.Command{
	Use:   "switch <workspace-id|slug|prefix>",
	Short: "Set the default workspace for this profile",
	Long: "Sets the default workspace for the current profile after verifying you " +
		"have access to it. Accepts a full UUID, a slug, or a short UUID " +
		"prefix (≥4 hex chars) as shown in 'workspace list'. Subsequent " +
		"commands without --workspace-id or MULTICA_WORKSPACE_ID will target " +
		"this workspace.\n\n" +
		"Resolution priority (highest to lowest): --workspace-id flag, " +
		"MULTICA_WORKSPACE_ID env, profile default (set by this command).\n\n" +
		"For low-level use, 'multica config set workspace_id <id>' writes the " +
		"same setting without verification.",
	Args: exactArgs(1),
	RunE: runWorkspaceSwitch,
}

var workspaceMcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage the workspace's MCP server library",
	Long: "Manages the workspace's library of MCP servers. A server added here " +
		"is given to NO agent: it reaches an agent only when someone assigns it " +
		"to that agent ('multica agent mcp add'), which also carries a per-agent " +
		"on/off toggle. Same shape as workspace skills.\n\n" +
		"The stored configuration is write-only: reads return the server names " +
		"and transports, never the urls, commands, headers, or env.",
}

var workspaceMcpListCmd = &cobra.Command{
	Use:   "list [workspace-id|slug|prefix]",
	Short: "List the workspace's MCP servers",
	Long: "Lists the workspace MCP library by name and transport. The stored " +
		"configuration itself is write-only and is never returned — to anyone, " +
		"including owners — because MCP entries routinely embed API tokens and a " +
		"session URL is a credential on its own.",
	Args: cobra.MaximumNArgs(1),
	RunE: runWorkspaceMcpList,
}

var workspaceMcpAddCmd = &cobra.Command{
	Use:   "add <server-name> [workspace-id|slug|prefix]",
	Short: "Add an MCP server to the workspace library (admin/owner only)",
	Long: "Adds an MCP server to the workspace library. It is assigned to no " +
		"agent — use 'multica agent mcp add <agent-id> <server-id>' to give it to " +
		"one.\n\n" +
		"The payload is a single server entry, the same object shape that sits " +
		"under a name in \"mcpServers\". Prefer --server-config-file or " +
		"--server-config-stdin: MCP entries routinely carry API tokens, and an " +
		"inline value ends up in shell history and 'ps'.",
	Example: "  multica workspace mcp add linear --server-config-file ./linear.json\n" +
		"  echo '{\"url\":\"https://mcp.example\"}' | multica workspace mcp add example --server-config-stdin",
	Args: cobra.RangeArgs(1, 2),
	RunE: runWorkspaceMcpAdd,
}

var workspaceMcpUpdateCmd = &cobra.Command{
	Use:   "update <server-id> [workspace-id|slug|prefix]",
	Short: "Rename or replace a workspace MCP server (admin/owner only)",
	Long: "Renames a server, replaces its configuration, or both. Agents keep " +
		"their assignment across a rename because assignments key off the server " +
		"id, not its name.",
	Args: cobra.RangeArgs(1, 2),
	RunE: runWorkspaceMcpUpdate,
}

var workspaceMcpRemoveCmd = &cobra.Command{
	Use:   "remove <server-id> [workspace-id|slug|prefix]",
	Short: "Remove an MCP server from the workspace library (admin/owner only)",
	Long: "Removes the server from the library and from every agent it was " +
		"assigned to.",
	Args: cobra.RangeArgs(1, 2),
	RunE: runWorkspaceMcpRemove,
}

func init() {
	workspaceCmd.AddCommand(workspaceListCmd)
	workspaceCmd.AddCommand(workspaceCreateCmd)
	workspaceCmd.AddCommand(workspaceGetCmd)
	workspaceCmd.AddCommand(workspaceMemberCmd)
	workspaceMemberCmd.AddCommand(workspaceMemberListCmd)
	workspaceMemberCmd.AddCommand(workspaceMemberInviteCmd)
	workspaceCmd.AddCommand(workspaceUpdateCmd)
	workspaceCmd.AddCommand(workspaceSwitchCmd)
	workspaceCmd.AddCommand(workspaceMcpCmd)
	workspaceMcpCmd.AddCommand(workspaceMcpListCmd)
	workspaceMcpCmd.AddCommand(workspaceMcpAddCmd)
	workspaceMcpCmd.AddCommand(workspaceMcpUpdateCmd)
	workspaceMcpCmd.AddCommand(workspaceMcpRemoveCmd)

	workspaceListCmd.Flags().String("output", "table", "Output format: table or json")
	workspaceListCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")
	workspaceCreateCmd.Flags().String("name", "", "Workspace name")
	workspaceCreateCmd.Flags().String("slug", "", "Workspace slug")
	workspaceCreateCmd.Flags().String("description", "", "Workspace description (decodes \\n, \\r, \\t, \\\\; pipe via --description-stdin to preserve literal backslashes)")
	workspaceCreateCmd.Flags().Bool("description-stdin", false, "Read description from stdin (preserves multi-line content verbatim)")
	workspaceCreateCmd.Flags().String("context", "", "Workspace context (decodes \\n, \\r, \\t, \\\\; pipe via --context-stdin to preserve literal backslashes)")
	workspaceCreateCmd.Flags().Bool("context-stdin", false, "Read context from stdin (preserves multi-line content verbatim)")
	workspaceCreateCmd.Flags().String("issue-prefix", "", "Issue prefix (uppercased server-side)")
	workspaceCreateCmd.Flags().String("output", "json", "Output format: table or json")
	workspaceGetCmd.Flags().String("output", "json", "Output format: table or json")
	workspaceMemberListCmd.Flags().String("output", "table", "Output format: table or json")
	workspaceMemberInviteCmd.Flags().String("role", "member", "Member role to grant: member or admin (owner is not allowed)")
	workspaceMemberInviteCmd.Flags().String("output", "table", "Output format: table or json")

	workspaceUpdateCmd.Flags().String("name", "", "New workspace name")
	workspaceUpdateCmd.Flags().String("description", "", "New description (decodes \\n, \\r, \\t, \\\\; pipe via --description-stdin to preserve literal backslashes)")
	workspaceUpdateCmd.Flags().Bool("description-stdin", false, "Read description from stdin (preserves multi-line content verbatim)")
	workspaceUpdateCmd.Flags().String("context", "", "New workspace context (decodes \\n, \\r, \\t, \\\\; pipe via --context-stdin to preserve literal backslashes)")
	workspaceUpdateCmd.Flags().Bool("context-stdin", false, "Read context from stdin (preserves multi-line content verbatim)")
	workspaceUpdateCmd.Flags().String("issue-prefix", "", "New issue prefix (uppercased server-side)")
	workspaceUpdateCmd.Flags().String("output", "json", "Output format: table or json")

	workspaceMcpListCmd.Flags().String("output", "json", "Output format: table or json")
	// 与 `agent update` 相同的三条互斥 secret-safe 通道，都由共用的
	// resolveMcpJSONObject 解析——所有表面统一对「什么是合法 payload」的口径。
	workspaceMcpAddCmd.Flags().String("server-config", "", "Server entry as JSON (avoid: lands in shell history)")
	workspaceMcpAddCmd.Flags().Bool("server-config-stdin", false, "Read the server entry JSON from stdin")
	workspaceMcpAddCmd.Flags().String("server-config-file", "", "Read the server entry JSON from a file")
	workspaceMcpAddCmd.Flags().String("output", "json", "Output format: table or json")
	workspaceMcpUpdateCmd.Flags().String("name", "", "New server name")
	workspaceMcpUpdateCmd.Flags().String("server-config", "", "Replacement server entry as JSON (avoid: lands in shell history)")
	workspaceMcpUpdateCmd.Flags().Bool("server-config-stdin", false, "Read the replacement server entry JSON from stdin")
	workspaceMcpUpdateCmd.Flags().String("server-config-file", "", "Read the replacement server entry JSON from a file")
	workspaceMcpUpdateCmd.Flags().String("output", "json", "Output format: table or json")
	workspaceMcpRemoveCmd.Flags().String("output", "json", "Output format: table or json")
}

// workspaceSummary 是 CLI 从 /api/workspaces 驱动 list/switch 所需的字段子集。
// 放这里（而不是用完整的 WorkspaceResponse）避免对 handler 包的依赖。
type workspaceSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// fetchWorkspaces 列出认证用户所属的全部工作区。`list` 与 `switch` 共用，保证两边
// 看到同一份受访问控制的工作区视图。
func fetchWorkspaces(ctx context.Context, cmd *cobra.Command) ([]workspaceSummary, error) {
	serverURL := resolveServerURL(cmd)
	token := resolveToken(cmd)
	if token == "" {
		return nil, fmt.Errorf("not authenticated: run 'multica login' first%s", daemonPortOnlyContextHint())
	}

	client := cli.NewAPIClient(serverURL, "", token)
	var workspaces []workspaceSummary
	if err := client.GetJSON(ctx, "/api/workspaces", &workspaces); err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	return workspaces, nil
}

// runWorkspaceList 执行 `workspace list`：拉取所属工作区并输出（支持 --full-id）。
func runWorkspaceList(cmd *cobra.Command, _ []string) error {
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	workspaces, err := fetchWorkspaces(ctx, cmd)
	if err != nil {
		return err
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, workspaces)
	}

	if len(workspaces) == 0 {
		fmt.Fprintln(os.Stderr, "No workspaces found.")
		return nil
	}

	currentID := resolveWorkspaceID(cmd)
	fullID, _ := cmd.Flags().GetBool("full-id")
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "\tID\tNAME\tSLUG")
	for _, ws := range workspaces {
		marker := " "
		if ws.ID == currentID {
			marker = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", marker, displayID(ws.ID, fullID), ws.Name, ws.Slug)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if currentID != "" {
		fmt.Fprintln(os.Stderr, "\n* = current default workspace (use 'multica workspace switch <id|slug|prefix>' to change)")
	} else {
		fmt.Fprintln(os.Stderr, "\nNo default workspace set. Use 'multica workspace switch <id|slug|prefix>' to pick one.")
	}
	fmt.Fprintln(os.Stderr, "Tip: pass the ID column, SLUG, or full UUID (--full-id) to 'workspace get/update/switch'.")
	return nil
}

// buildWorkspaceCreateBody 从 flag 收集 create 请求体（name/slug 必填，其余可选；
// description 支持 stdin/行内解析）。
func buildWorkspaceCreateBody(cmd *cobra.Command) (map[string]any, error) {
	name, _ := cmd.Flags().GetString("name")
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("--name is required")
	}

	// server 要求 name 与 slug 都提供（缺了 POST /api/workspaces 会返回 400），
	// 且 slug 建后不可变，所以必须在这里显式挑选，而不是静默省略。
	slug, _ := cmd.Flags().GetString("slug")
	if strings.TrimSpace(slug) == "" {
		return nil, fmt.Errorf("--slug is required")
	}

	// 一条 stdin 流喂不了两个字段：谁先读谁抽干它，另一个只能拿到 EOF。与其
	// 抛出一个误导性的 "content is empty"，不如在前面就把这种歧义组合拒掉。
	descStdin, _ := cmd.Flags().GetBool("description-stdin")
	ctxStdin, _ := cmd.Flags().GetBool("context-stdin")
	if descStdin && ctxStdin {
		return nil, fmt.Errorf("--description-stdin and --context-stdin cannot be combined; a single stdin cannot feed both fields — pass one of them inline")
	}

	body := map[string]any{"name": name, "slug": slug}
	if cmd.Flags().Changed("description") || cmd.Flags().Changed("description-stdin") {
		desc, _, err := resolveTextFlag(cmd, "description")
		if err != nil {
			return nil, err
		}
		body["description"] = desc
	}
	if cmd.Flags().Changed("context") || cmd.Flags().Changed("context-stdin") {
		ctxText, _, err := resolveTextFlag(cmd, "context")
		if err != nil {
			return nil, err
		}
		body["context"] = ctxText
	}
	if cmd.Flags().Changed("issue-prefix") {
		v, _ := cmd.Flags().GetString("issue-prefix")
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("--issue-prefix cannot be empty; omit it to use the server-generated prefix")
		}
		body["issue_prefix"] = v
	}
	return body, nil
}

// runWorkspaceCreate 执行 `workspace create`：建新工作区并把自己设为 owner；
// 不改当前默认工作区。
func runWorkspaceCreate(cmd *cobra.Command, _ []string) error {
	body, err := buildWorkspaceCreateBody(cmd)
	if err != nil {
		return err
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	client.WorkspaceID = ""

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var ws map[string]any
	if err := client.PostJSON(ctx, "/api/workspaces", body, &ws); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	return printWorkspace(cmd, ws)
}

// resolveWorkspaceByIDOrSlug 在调用方可访问的列表里，按完整 UUID、slug（忽略
// 大小写）或短 UUID 前缀（≥4 位十六进制字符）查找工作区。匹配顺序是「精确
// UUID → 精确 slug → 前缀」，所以一个恰好长得像十六进制字符串的 slug 绝不会被
// 撞车的 UUID 前缀盖过。找不到匹配就报错，这同时充当「无权访问 / 不存在」检查
// ——server 只返回用户是其成员的工作区，能匹配上即说明有权访问。
func resolveWorkspaceByIDOrSlug(workspaces []workspaceSummary, target string) (workspaceSummary, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return workspaceSummary{}, fmt.Errorf("workspace id, slug, or id prefix is required")
	}
	// slug 比较忽略大小写（server 端存的是小写，但容忍用户输入大写）；规范
	// 形式下 UUID 也忽略大小写，所以统一转小写对两者都安全。
	lowered := strings.ToLower(target)
	for _, ws := range workspaces {
		if strings.ToLower(ws.ID) == lowered {
			return ws, nil
		}
	}
	for _, ws := range workspaces {
		if ws.Slug != "" && strings.ToLower(ws.Slug) == lowered {
			return ws, nil
		}
	}

	// 回退到短 UUID 前缀匹配：从 `workspace list` 默认（截断）ID 列复制的值，
	// 能原样作为 get/update/switch 的入参再解析回来。normalizeUUIDPrefix 强制
	// 至少 4 位十六进制字符，避免任意子串带来意外命中。
	if prefix, err := normalizeUUIDPrefix(target); err == nil {
		matches := make([]workspaceSummary, 0, 1)
		for _, ws := range workspaces {
			if strings.HasPrefix(compactUUID(ws.ID), prefix) {
				matches = append(matches, ws)
			}
		}
		switch len(matches) {
		case 0:
			// 落到下面的「未找到」报错
		case 1:
			return matches[0], nil
		default:
			return workspaceSummary{}, ambiguousWorkspacePrefixError(target, matches)
		}
	}

	return workspaceSummary{}, fmt.Errorf("workspace %q not found or you do not have access; run 'multica workspace list' to see options", target)
}

// ambiguousWorkspacePrefixError 把多个短前缀命中排成可读的歧义报错。
func ambiguousWorkspacePrefixError(input string, matches []workspaceSummary) error {
	parts := make([]string, 0, len(matches))
	for _, m := range matches {
		label := m.Name
		if m.Slug != "" {
			label = fmt.Sprintf("%s (%s)", m.Name, m.Slug)
		}
		parts = append(parts, fmt.Sprintf("  %s  %s", m.ID, label))
	}
	return fmt.Errorf("ambiguous workspace id prefix %q; matches:\n%s\nUse more characters, the slug, or the full UUID", input, strings.Join(parts, "\n"))
}

// resolveWorkspaceRef 拉取调用方的工作区列表，把输入（UUID、slug 或短 UUID
// 前缀）解析成 workspaceSummary。`workspace get`/`workspace update`/
// `workspace member list`/`workspace switch` 共用它，四条命令因此接受用户在
// `workspace list` 里看到的同一批标识符。
func resolveWorkspaceRef(ctx context.Context, cmd *cobra.Command, input string) (workspaceSummary, error) {
	target := strings.TrimSpace(input)
	if target == "" {
		return workspaceSummary{}, fmt.Errorf("workspace id, slug, or id prefix is required")
	}
	workspaces, err := fetchWorkspaces(ctx, cmd)
	if err != nil {
		return workspaceSummary{}, err
	}
	return resolveWorkspaceByIDOrSlug(workspaces, target)
}

// runWorkspaceSwitch 执行 `workspace switch <ref>`：校验有权访问后把该工作区设成
// 当前 profile 的默认（写 workspace_id 到配置）。
func runWorkspaceSwitch(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("workspace switch"); err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	ws, err := resolveWorkspaceRef(ctx, cmd, args[0])
	if err != nil {
		return err
	}

	profile := resolveProfile(cmd)
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err != nil {
		return err
	}
	cfg.WorkspaceID = ws.ID
	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "Switched to workspace: %s (%s)\n", ws.Name, ws.ID)
	return nil
}

// resolveWorkspaceArg 为带可选 `[workspace-id]` 参数的 workspace 命令返回规范
// UUID。给了参数就按调用方工作区列表解析（UUID、slug 或短前缀）；没给则回退到
// 标准 --workspace-id / env / profile 解析链——调用方负责兜住空串的情况。完整
// UUID 原样透传以省掉一次 /api/workspaces 往返；访问控制由下游端点负责。
func resolveWorkspaceArg(cmd *cobra.Command, args []string) (string, error) {
	if len(args) > 0 {
		trimmed := strings.TrimSpace(args[0])
		if uuidRegexp.MatchString(trimmed) {
			return trimmed, nil
		}
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		ws, err := resolveWorkspaceRef(ctx, cmd, trimmed)
		if err != nil {
			return "", err
		}
		return ws.ID, nil
	}
	return resolveWorkspaceID(cmd), nil
}

// runWorkspaceGet 执行 `workspace get [ref]`：打印工作区详情。
func runWorkspaceGet(cmd *cobra.Command, args []string) error {
	wsID, err := resolveWorkspaceArg(cmd, args)
	if err != nil {
		return err
	}
	if wsID == "" {
		return fmt.Errorf("workspace ID is required: pass an id/slug/prefix as argument or set MULTICA_WORKSPACE_ID")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var ws map[string]any
	if err := client.GetJSON(ctx, "/api/workspaces/"+wsID, &ws); err != nil {
		return fmt.Errorf("get workspace: %w", err)
	}

	return printWorkspace(cmd, ws)
}

// printWorkspace 按 --output 渲染单个工作区。
func printWorkspace(cmd *cobra.Command, ws map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		desc := strVal(ws, "description")
		if utf8.RuneCountInString(desc) > 60 {
			runes := []rune(desc)
			desc = string(runes[:57]) + "..."
		}
		wsContext := strVal(ws, "context")
		if utf8.RuneCountInString(wsContext) > 60 {
			runes := []rune(wsContext)
			wsContext = string(runes[:57]) + "..."
		}
		headers := []string{"ID", "NAME", "SLUG", "DESCRIPTION", "CONTEXT"}
		rows := [][]string{{
			strVal(ws, "id"),
			strVal(ws, "name"),
			strVal(ws, "slug"),
			desc,
			wsContext,
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}

	return cli.PrintJSON(os.Stdout, ws)
}

// buildWorkspaceUpdateBody 从调用方实际设置的 flag 拼出 PATCH 载荷，镜像
// server/internal/handler/workspace.go 的 UpdateWorkspaceRequest。只有 Changed()
// 过的字段才会进载荷，调用方因此不会意外清掉自己没传的字段。
func buildWorkspaceUpdateBody(cmd *cobra.Command) (map[string]any, error) {
	body := map[string]any{}
	if cmd.Flags().Changed("name") {
		v, _ := cmd.Flags().GetString("name")
		body["name"] = v
	}
	if cmd.Flags().Changed("description") || cmd.Flags().Changed("description-stdin") {
		desc, _, err := resolveTextFlag(cmd, "description")
		if err != nil {
			return nil, err
		}
		body["description"] = desc
	}
	if cmd.Flags().Changed("context") || cmd.Flags().Changed("context-stdin") {
		ctxText, _, err := resolveTextFlag(cmd, "context")
		if err != nil {
			return nil, err
		}
		body["context"] = ctxText
	}
	if cmd.Flags().Changed("issue-prefix") {
		v, _ := cmd.Flags().GetString("issue-prefix")
		// handler 会静默跳过空 prefix（workspace.go:274），不然 `--issue-prefix ""`
		// 会返回 200 却什么都没改。在这里拦住，让失败显性可见。
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("--issue-prefix cannot be empty; clearing the prefix is not supported")
		}
		body["issue_prefix"] = v
	}
	return body, nil
}

// runWorkspaceUpdate 执行 `workspace update [ref]`：更新工作区元数据（仅
// admin/owner）。
func runWorkspaceUpdate(cmd *cobra.Command, args []string) error {
	wsID, err := resolveWorkspaceArg(cmd, args)
	if err != nil {
		return err
	}
	if wsID == "" {
		return fmt.Errorf("workspace ID is required: pass an id/slug/prefix as argument or set MULTICA_WORKSPACE_ID")
	}

	body, err := buildWorkspaceUpdateBody(cmd)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return fmt.Errorf("no fields to update; use --name, --description, --context, or --issue-prefix")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var ws map[string]any
	if err := client.PatchJSON(ctx, "/api/workspaces/"+wsID, body, &ws); err != nil {
		return fmt.Errorf("update workspace: %w", err)
	}

	return printWorkspace(cmd, ws)
}

// runWorkspaceMcpList 执行 `workspace mcp list [ref]`：列出工作区 MCP 库的
// 名字与传输方式（配置本身只写不回）。
func runWorkspaceMcpList(cmd *cobra.Command, args []string) error {
	wsID, err := resolveWorkspaceArg(cmd, args)
	if err != nil {
		return err
	}
	if wsID == "" {
		return fmt.Errorf("workspace ID is required: pass an id/slug/prefix as argument or set MULTICA_WORKSPACE_ID")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var servers []workspaceMcpServer
	if err := client.GetJSON(ctx, "/api/workspaces/"+wsID+"/mcp-servers", &servers); err != nil {
		return fmt.Errorf("list workspace mcp servers: %w", err)
	}

	return printWorkspaceMcpServers(cmd, servers)
}

// runWorkspaceMcpAdd 执行 `workspace mcp add <server-name> [ref]`：把一条 MCP
// server 配置加进库（不会自动派给任何 agent）。
func runWorkspaceMcpAdd(cmd *cobra.Command, args []string) error {
	serverName := strings.TrimSpace(args[0])
	if serverName == "" {
		return fmt.Errorf("server name must not be empty")
	}
	wsID, err := resolveWorkspaceArg(cmd, args[1:])
	if err != nil {
		return err
	}
	if wsID == "" {
		return fmt.Errorf("workspace ID is required: pass an id/slug/prefix as argument or set MULTICA_WORKSPACE_ID")
	}

	entry, ok, err := resolveMcpJSONObject(cmd, "server-config", false)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("one of --server-config, --server-config-stdin, or --server-config-file is required")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var server workspaceMcpServer
	body := map[string]any{"name": serverName, "config": entry}
	if err := client.PostJSON(ctx, "/api/workspaces/"+wsID+"/mcp-servers", body, &server); err != nil {
		return fmt.Errorf("add workspace mcp server: %w", err)
	}

	return printWorkspaceMcpServers(cmd, []workspaceMcpServer{server})
}

// runWorkspaceMcpUpdate 执行 `workspace mcp update <server-id> [ref]`：改名/替换
// 配置（改名不影响已派给 agent 的引用，派发按 server id）。
func runWorkspaceMcpUpdate(cmd *cobra.Command, args []string) error {
	serverID := strings.TrimSpace(args[0])
	if serverID == "" {
		return fmt.Errorf("server ID must not be empty")
	}
	wsID, err := resolveWorkspaceArg(cmd, args[1:])
	if err != nil {
		return err
	}
	if wsID == "" {
		return fmt.Errorf("workspace ID is required: pass an id/slug/prefix as argument or set MULTICA_WORKSPACE_ID")
	}

	body := map[string]any{}
	if cmd.Flags().Changed("name") {
		name, _ := cmd.Flags().GetString("name")
		body["name"] = strings.TrimSpace(name)
	}
	entry, ok, err := resolveMcpJSONObject(cmd, "server-config", false)
	if err != nil {
		return err
	}
	if ok {
		body["config"] = entry
	}
	if len(body) == 0 {
		return fmt.Errorf("nothing to update; pass --name and/or one of --server-config, --server-config-stdin, --server-config-file")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var server workspaceMcpServer
	path := "/api/workspaces/" + wsID + "/mcp-servers/" + url.PathEscape(serverID)
	if err := client.PutJSON(ctx, path, body, &server); err != nil {
		return fmt.Errorf("update workspace mcp server: %w", err)
	}

	return printWorkspaceMcpServers(cmd, []workspaceMcpServer{server})
}

// runWorkspaceMcpRemove 执行 `workspace mcp remove <server-id> [ref]`：从库与所有
// 已派 agent 上移除该 server。
func runWorkspaceMcpRemove(cmd *cobra.Command, args []string) error {
	serverID := strings.TrimSpace(args[0])
	if serverID == "" {
		return fmt.Errorf("server ID must not be empty")
	}
	wsID, err := resolveWorkspaceArg(cmd, args[1:])
	if err != nil {
		return err
	}
	if wsID == "" {
		return fmt.Errorf("workspace ID is required: pass an id/slug/prefix as argument or set MULTICA_WORKSPACE_ID")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	path := "/api/workspaces/" + wsID + "/mcp-servers/" + url.PathEscape(serverID)
	if err := client.DeleteJSON(ctx, path); err != nil {
		return fmt.Errorf("remove workspace mcp server: %w", err)
	}

	fmt.Fprintf(os.Stdout, "removed MCP server %s\n", serverID)
	return nil
}

// workspaceMcpServer 是 CLI 侧「只写边界」的一半：解码成具名字段——而不是一个
// 会被原样重编码的 map[string]any——意味着即便 server 某天退化到回传已存的
// 条目、或条目里带 `url`/`headers`，它们也无法经由任何输出格式到达 stdout：
// 这里没声明的字段会被 encoding/json 直接丢弃。MCP server 列表里能安全展示的
// 形态是名字与传输方式：存储配置只写不回，类型上就保证这条命令印不出任何配置。
type workspaceMcpServer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Transport string `json:"transport"`
	// 只在 agent 的指派列表上出现；工作区库里为 nil。
	Enabled *bool `json:"enabled,omitempty"`
}

// printWorkspaceMcpServers 渲染 MCP server 列表。两种输出里都没有「配置条目」可
// 打：API 从不返回已存配置，上面的类型定义则从结构上保证本命令打不出配置。
func printWorkspaceMcpServers(cmd *cobra.Command, servers []workspaceMcpServer) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "table" {
		return cli.PrintJSON(os.Stdout, servers)
	}

	if len(servers) == 0 {
		fmt.Fprintln(os.Stdout, "no MCP servers")
		return nil
	}
	rows := make([][]string, 0, len(servers))
	for _, server := range servers {
		status := ""
		if server.Enabled != nil {
			status = "enabled"
			if !*server.Enabled {
				status = "disabled"
			}
		}
		rows = append(rows, []string{server.ID, server.Name, server.Transport, status})
	}
	cli.PrintTable(os.Stdout, []string{"ID", "NAME", "TRANSPORT", "STATUS"}, rows)
	return nil
}

// runWorkspaceMembers 执行 `workspace member list [ref]`：列出工作区成员。
func runWorkspaceMembers(cmd *cobra.Command, args []string) error {
	wsID, err := resolveWorkspaceArg(cmd, args)
	if err != nil {
		return err
	}
	if wsID == "" {
		return fmt.Errorf("workspace ID is required: pass an id/slug/prefix as argument or set MULTICA_WORKSPACE_ID")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var members []map[string]any
	if err := client.GetJSON(ctx, "/api/workspaces/"+wsID+"/members", &members); err != nil {
		return fmt.Errorf("list members: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, members)
	}

	headers := []string{"USER ID", "NAME", "EMAIL", "ROLE"}
	rows := make([][]string, 0, len(members))
	for _, m := range members {
		rows = append(rows, []string{
			strVal(m, "user_id"),
			strVal(m, "name"),
			strVal(m, "email"),
			strVal(m, "role"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// runWorkspaceMemberInvite 执行 `workspace member invite <email> [ref]`：按邮箱发
// 邀请（pending，待对方接受）；owner 不可邀请。
func runWorkspaceMemberInvite(cmd *cobra.Command, args []string) error {
	email := strings.ToLower(strings.TrimSpace(args[0]))
	if email == "" {
		return fmt.Errorf("email is required")
	}

	// 客户端先校验 role，用清晰的报错快速失败；server 端强制同一条规则
	// （normalizeMemberRole 拒绝未知角色、CreateInvitation 拒绝 owner）。两边的
	// 可接受集合要保持同步。
	role, _ := cmd.Flags().GetString("role")
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		role = "member"
	}
	switch role {
	case "member", "admin":
	case "owner":
		return fmt.Errorf("cannot invite as owner; use --role member or --role admin")
	default:
		return fmt.Errorf("invalid --role %q; expected member or admin", role)
	}

	wsID, err := resolveWorkspaceArg(cmd, args[1:])
	if err != nil {
		return err
	}
	if wsID == "" {
		return fmt.Errorf("workspace ID is required: pass an id/slug/prefix as argument or set MULTICA_WORKSPACE_ID")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	body := map[string]any{"email": email, "role": role}
	var inv map[string]any
	if err := client.PostJSON(ctx, "/api/workspaces/"+wsID+"/members", body, &inv); err != nil {
		return fmt.Errorf("invite member: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, inv)
	}

	fmt.Fprintf(os.Stdout, "Invitation sent to %s (role: %s, status: %s)\n",
		strVal(inv, "invitee_email"), strVal(inv, "role"), strVal(inv, "status"))
	return nil
}
