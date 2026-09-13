// 文件职责：实现 `multica project` 命令组——工作区项目的 CRUD 与状态变更
// （list/get/create/update/delete/status），以及「项目附加资源」管理（resource
// list/add/update/remove，如 github_repo / local_directory）。通用资源用 JSON
// --ref 载荷走通，两种内建类型另有快捷 flag；ID 解析复用 cmd_id_resolver.go 的
// resolveProjectID（完整 UUID/短前缀）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// projectCmd 是 `multica project` 父命令。
var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "Work with projects",
}

// projectListCmd 列出工作区项目。
var projectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List projects in the workspace",
	RunE:  runProjectList,
}

var projectGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get project details",
	Args:  exactArgs(1),
	RunE:  runProjectGet,
}

var projectCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new project",
	RunE:  runProjectCreate,
}

var projectUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a project",
	Args:  exactArgs(1),
	RunE:  runProjectUpdate,
}

var projectDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a project",
	Args:  exactArgs(1),
	RunE:  runProjectDelete,
}

var projectStatusCmd = &cobra.Command{
	Use:   "status <id> <status>",
	Short: "Change project status",
	Args:  exactArgs(2),
	RunE:  runProjectStatus,
}

// projectResourceCmd 是 `project resource` 分组命令。
var projectResourceCmd = &cobra.Command{
	Use:   "resource",
	Short: "Manage resources attached to a project",
}

var projectResourceListCmd = &cobra.Command{
	Use:   "list <project-id>",
	Short: "List resources attached to a project",
	Args:  exactArgs(1),
	RunE:  runProjectResourceList,
}

var projectResourceAddCmd = &cobra.Command{
	Use:   "add <project-id>",
	Short: "Attach a resource to a project (e.g. --type github_repo --url <url>)",
	Args:  exactArgs(1),
	RunE:  runProjectResourceAdd,
}

var projectResourceUpdateCmd = &cobra.Command{
	Use:   "update <project-id> <resource-id>",
	Short: "Edit an attached resource (ref payload, label, or position)",
	Args:  exactArgs(2),
	RunE:  runProjectResourceUpdate,
}

var projectResourceRemoveCmd = &cobra.Command{
	Use:   "remove <project-id> <resource-id>",
	Short: "Detach a resource from a project",
	Args:  exactArgs(2),
	RunE:  runProjectResourceRemove,
}

// validProjectStatuses 是项目合法状态集合。
var validProjectStatuses = []string{
	"planned", "in_progress", "paused", "completed", "cancelled",
}

// validateProjectStatus 在客户端拒掉未知状态，让拼写错误带合法列表立刻失败，而
// 不是走一次 server 往返换 400。被 `project create` / `project update` /
// `project status` 共用。
func validateProjectStatus(status string) error {
	for _, s := range validProjectStatuses {
		if s == status {
			return nil
		}
	}
	return fmt.Errorf("invalid status %q; valid values: %s", status, strings.Join(validProjectStatuses, ", "))
}

func init() {
	projectCmd.AddCommand(projectListCmd)
	projectCmd.AddCommand(projectGetCmd)
	projectCmd.AddCommand(projectCreateCmd)
	projectCmd.AddCommand(projectUpdateCmd)
	projectCmd.AddCommand(projectDeleteCmd)
	projectCmd.AddCommand(projectStatusCmd)
	projectCmd.AddCommand(projectResourceCmd)

	projectResourceCmd.AddCommand(projectResourceListCmd)
	projectResourceCmd.AddCommand(projectResourceAddCmd)
	projectResourceCmd.AddCommand(projectResourceUpdateCmd)
	projectResourceCmd.AddCommand(projectResourceRemoveCmd)

	// project list
	projectListCmd.Flags().String("output", "table", "Output format: table or json")
	projectListCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")
	projectListCmd.Flags().String("status", "", "Filter by status")

	// project get
	projectGetCmd.Flags().String("output", "json", "Output format: table or json")

	// project create
	projectCreateCmd.Flags().String("title", "", "Project title (required)")
	projectCreateCmd.Flags().String("description", "", "Project description")
	projectCreateCmd.Flags().String("status", "", "Project status")
	projectCreateCmd.Flags().String("icon", "", "Project icon (emoji)")
	projectCreateCmd.Flags().String("lead", "", "Lead name (member or agent)")
	projectCreateCmd.Flags().String("start-date", "", "Start date (calendar day, YYYY-MM-DD)")
	projectCreateCmd.Flags().String("due-date", "", "Due date (calendar day, YYYY-MM-DD)")
	projectCreateCmd.Flags().StringArray("repo", nil, "Attach a github_repo resource by URL (may be repeated)")
	projectCreateCmd.Flags().String("output", "json", "Output format: table or json")

	// project resource list
	projectResourceListCmd.Flags().String("output", "table", "Output format: table or json")
	projectResourceListCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")

	// project resource add — 通用形态：任意 --type 配 JSON --ref 载荷即可，无需再改
	// CLI。github_repo 另有专门快捷 flag；对该类型，非 JSON 的 --ref 值当作默认检出
	// ref（checkout ref）。
	projectResourceAddCmd.Flags().String("type", "github_repo", "Resource type (e.g. github_repo, local_directory — see docs)")
	projectResourceAddCmd.Flags().String("url", "", "Shortcut: the repo URL (only used when --type github_repo)")
	projectResourceAddCmd.Flags().String("default-branch-hint", "", "Shortcut: optional default branch hint (only used when --type github_repo)")
	projectResourceAddCmd.Flags().String("local-path", "", "Shortcut: absolute path to the working directory (only used when --type local_directory)")
	projectResourceAddCmd.Flags().String("daemon-id", "", "Shortcut: id of the daemon that owns the local path (only used when --type local_directory)")
	projectResourceAddCmd.Flags().String("ref-label", "", "Shortcut: optional label embedded in resource_ref (only used when --type local_directory)")
	projectResourceAddCmd.Flags().String("execution-mode", "", "Shortcut: how tasks share the directory — in_place (default, one task at a time) or worktree (each task gets its own git worktree; requires a git repo) (only used when --type local_directory)")
	projectResourceAddCmd.Flags().String("ref", "", "Generic JSON resource_ref payload, or a github_repo checkout ref when used with --url")
	projectResourceAddCmd.Flags().String("label", "", "Optional human-readable label")
	projectResourceAddCmd.Flags().String("output", "json", "Output format: table or json")

	// project resource update — 镜像 `add` 的 flag，但每项都可选，调用方能一次只改
	// 一处。
	projectResourceUpdateCmd.Flags().String("url", "", "Shortcut: new repo URL (github_repo)")
	projectResourceUpdateCmd.Flags().String("default-branch-hint", "", "Shortcut: new default branch hint (github_repo)")
	projectResourceUpdateCmd.Flags().String("local-path", "", "Shortcut: new absolute local path (local_directory)")
	projectResourceUpdateCmd.Flags().String("daemon-id", "", "Shortcut: new daemon id (local_directory)")
	projectResourceUpdateCmd.Flags().String("ref-label", "", "Shortcut: new label embedded in resource_ref (local_directory)")
	projectResourceUpdateCmd.Flags().String("execution-mode", "", "Shortcut: new execution mode — in_place or worktree (local_directory)")
	projectResourceUpdateCmd.Flags().String("ref", "", "Generic JSON resource_ref payload, or a github_repo checkout ref")
	projectResourceUpdateCmd.Flags().String("label", "", "New human-readable label; pass an empty string to clear")
	projectResourceUpdateCmd.Flags().Bool("clear-label", false, "Clear the human-readable label")
	projectResourceUpdateCmd.Flags().Int32("position", 0, "New display position")
	projectResourceUpdateCmd.Flags().String("output", "json", "Output format: table or json")

	// project resource remove
	projectResourceRemoveCmd.Flags().String("output", "table", "Output format: table or json")

	// project update
	projectUpdateCmd.Flags().String("title", "", "New title")
	projectUpdateCmd.Flags().String("description", "", "New description")
	projectUpdateCmd.Flags().String("status", "", "New status")
	projectUpdateCmd.Flags().String("icon", "", "New icon (emoji)")
	projectUpdateCmd.Flags().String("lead", "", "New lead name (member or agent)")
	projectUpdateCmd.Flags().String("start-date", "", "New start date (calendar day, YYYY-MM-DD; pass empty string to clear)")
	projectUpdateCmd.Flags().String("due-date", "", "New due date (calendar day, YYYY-MM-DD; pass empty string to clear)")
	projectUpdateCmd.Flags().String("output", "json", "Output format: table or json")

	// project delete
	projectDeleteCmd.Flags().String("output", "json", "Output format: table or json")

	// project status
	projectStatusCmd.Flags().String("output", "table", "Output format: table or json")
}

// ---------------------------------------------------------------------------
// Project commands —— 各 project 子命令
// ---------------------------------------------------------------------------

// runProjectList 执行 `project list`：列出工作区项目（--status 过滤）。
func runProjectList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	params := url.Values{}
	if client.WorkspaceID != "" {
		params.Set("workspace_id", client.WorkspaceID)
	}
	if v, _ := cmd.Flags().GetString("status"); v != "" {
		params.Set("status", v)
	}

	path := "/api/projects"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var result map[string]any
	if err := client.GetJSON(ctx, path, &result); err != nil {
		return fmt.Errorf("list projects: %w", err)
	}

	projectsRaw, _ := result["projects"].([]any)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, projectsRaw)
	}

	fullID, _ := cmd.Flags().GetBool("full-id")
	actors := loadActorDisplayLookup(ctx, client)
	headers := []string{"ID", "TITLE", "STATUS", "LEAD", "CREATED"}
	rows := make([][]string, 0, len(projectsRaw))
	for _, raw := range projectsRaw {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		lead := formatLead(p, actors)
		created := strVal(p, "created_at")
		if len(created) >= 10 {
			created = created[:10]
		}
		rows = append(rows, []string{
			displayID(strVal(p, "id"), fullID),
			strVal(p, "title"),
			strVal(p, "status"),
			lead,
			created,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// runProjectGet 执行 `project get <id>`：取单个项目详情；带资源时给一条去
// resource list 的提示。
func runProjectGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	projectRef, err := resolveProjectID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}

	var project map[string]any
	if err := client.GetJSON(ctx, "/api/projects/"+projectRef.ID, &project); err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	// 给资源子集合指路。走 stderr，stdout 的 JSON 保持可解析；响应的
	// `resource_count` 字段是程序化等价物。JSON 数字解成 float64。
	if n, _ := project["resource_count"].(float64); n > 0 {
		fmt.Fprintf(os.Stderr, "%d resource(s) attached — run `multica project resource list %s` to view.\n",
			int64(n), strVal(project, "id"))
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		actors := loadActorDisplayLookup(ctx, client)
		lead := formatLead(project, actors)
		headers := []string{"ID", "TITLE", "STATUS", "LEAD", "DESCRIPTION"}
		rows := [][]string{{
			strVal(project, "id"),
			strVal(project, "title"),
			strVal(project, "status"),
			lead,
			strVal(project, "description"),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}

	return cli.PrintJSON(os.Stdout, project)
}

// runProjectCreate 执行 `project create`：--title 必填，可选 lead/日期/状态；多个
// --repo 打包成 github_repo 资源随创建一并挂（同一事务，失败不留半挂状态）。
func runProjectCreate(cmd *cobra.Command, _ []string) error {
	title, _ := cmd.Flags().GetString("title")
	if title == "" {
		return fmt.Errorf("--title is required")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	body := map[string]any{"title": title}
	if v, _ := cmd.Flags().GetString("description"); v != "" {
		body["description"] = v
	}
	if v, _ := cmd.Flags().GetString("status"); v != "" {
		if err := validateProjectStatus(v); err != nil {
			return err
		}
		body["status"] = v
	}
	if v, _ := cmd.Flags().GetString("icon"); v != "" {
		body["icon"] = v
	}
	if v, _ := cmd.Flags().GetString("lead"); v != "" {
		aType, aID, resolveErr := resolveAssignee(ctx, client, v, memberOrAgentKinds)
		if resolveErr != nil {
			return fmt.Errorf("resolve lead: %w", resolveErr)
		}
		body["lead_type"] = aType
		body["lead_id"] = aID
	}
	if v, _ := cmd.Flags().GetString("start-date"); v != "" {
		body["start_date"] = v
	}
	if v, _ := cmd.Flags().GetString("due-date"); v != "" {
		body["due_date"] = v
	}

	// 资源打包进 create 载荷，让 server 在同一事务里挂上；失败不会留下半挂起的
	// 项目。
	repos, _ := cmd.Flags().GetStringArray("repo")
	if len(repos) > 0 {
		resources := make([]map[string]any, 0, len(repos))
		for _, repoURL := range repos {
			repoURL = strings.TrimSpace(repoURL)
			if repoURL == "" {
				continue
			}
			resources = append(resources, map[string]any{
				"resource_type": "github_repo",
				"resource_ref":  map[string]any{"url": repoURL},
			})
		}
		if len(resources) > 0 {
			body["resources"] = resources
		}
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/projects", body, &result); err != nil {
		return fmt.Errorf("create project: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		headers := []string{"ID", "TITLE", "STATUS"}
		rows := [][]string{{
			strVal(result, "id"),
			strVal(result, "title"),
			strVal(result, "status"),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}

	return cli.PrintJSON(os.Stdout, result)
}

// runProjectUpdate 执行 `project update <id>`：只 PATCH Changed 字段（空串日期
// 也当作清除语义传给 server）。
func runProjectUpdate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	projectRef, err := resolveProjectID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}

	body := map[string]any{}
	if cmd.Flags().Changed("title") {
		v, _ := cmd.Flags().GetString("title")
		body["title"] = v
	}
	if cmd.Flags().Changed("description") {
		v, _ := cmd.Flags().GetString("description")
		body["description"] = v
	}
	if cmd.Flags().Changed("status") {
		v, _ := cmd.Flags().GetString("status")
		if err := validateProjectStatus(v); err != nil {
			return err
		}
		body["status"] = v
	}
	if cmd.Flags().Changed("icon") {
		v, _ := cmd.Flags().GetString("icon")
		body["icon"] = v
	}
	if cmd.Flags().Changed("lead") {
		v, _ := cmd.Flags().GetString("lead")
		aType, aID, resolveErr := resolveAssignee(ctx, client, v, memberOrAgentKinds)
		if resolveErr != nil {
			return fmt.Errorf("resolve lead: %w", resolveErr)
		}
		body["lead_type"] = aType
		body["lead_id"] = aID
	}
	// 用 Changed()（而不是 "" 判空），显式 `--start-date ""` 才能作为「清除」送到
	// server，镜像 issue update CLI 的语义。
	if cmd.Flags().Changed("start-date") {
		v, _ := cmd.Flags().GetString("start-date")
		body["start_date"] = v
	}
	if cmd.Flags().Changed("due-date") {
		v, _ := cmd.Flags().GetString("due-date")
		body["due_date"] = v
	}

	if len(body) == 0 {
		return fmt.Errorf("no fields to update; use flags like --title, --status, --description, --icon, --lead, --start-date, --due-date")
	}

	var result map[string]any
	if err := client.PutJSON(ctx, "/api/projects/"+projectRef.ID, body, &result); err != nil {
		return fmt.Errorf("update project: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		headers := []string{"ID", "TITLE", "STATUS"}
		rows := [][]string{{
			strVal(result, "id"),
			strVal(result, "title"),
			strVal(result, "status"),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}

	return cli.PrintJSON(os.Stdout, result)
}

// runProjectDelete 执行 `project delete <id>`：删除项目。
func runProjectDelete(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	projectRef, err := resolveProjectID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}

	if err := client.DeleteJSON(ctx, "/api/projects/"+projectRef.ID); err != nil {
		return fmt.Errorf("delete project: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Project %s deleted.\n", projectRef.Display)
	return nil
}

// runProjectStatus 执行 `project status <id> <status>`：改项目状态。
func runProjectStatus(cmd *cobra.Command, args []string) error {
	id := args[0]
	status := args[1]

	if err := validateProjectStatus(status); err != nil {
		return err
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	projectRef, err := resolveProjectID(ctx, client, id)
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}

	body := map[string]any{"status": status}
	var result map[string]any
	if err := client.PutJSON(ctx, "/api/projects/"+projectRef.ID, body, &result); err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Project %s status changed to %s.\n", strVal(result, "title"), status)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Project resource commands —— 项目附加资源管理
// ---------------------------------------------------------------------------

// runProjectResourceList 执行 `project resource list <project-id>`：列出项目资源。
func runProjectResourceList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	projectRef, err := resolveProjectID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}

	var result map[string]any
	if err := client.GetJSON(ctx, "/api/projects/"+projectRef.ID+"/resources", &result); err != nil {
		return fmt.Errorf("list project resources: %w", err)
	}
	resourcesRaw, _ := result["resources"].([]any)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, resourcesRaw)
	}

	fullID, _ := cmd.Flags().GetBool("full-id")
	headers := []string{"ID", "TYPE", "REF", "LABEL"}
	rows := make([][]string, 0, len(resourcesRaw))
	for _, raw := range resourcesRaw {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, []string{
			displayID(strVal(r, "id"), fullID),
			strVal(r, "resource_type"),
			summarizeResourceRef(r["resource_ref"]),
			strVal(r, "label"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// runProjectResourceAdd 执行 `project resource add <project-id>`：挂一个资源——
// 由 --type 决定用内建快捷（github_repo 需 --url；local_directory 需 --local-path
// + --daemon-id）还是通用 JSON --ref。
func runProjectResourceAdd(cmd *cobra.Command, args []string) error {
	resourceType, _ := cmd.Flags().GetString("type")
	resourceType = strings.TrimSpace(resourceType)
	if resourceType == "" {
		return fmt.Errorf("--type is required")
	}

	body := map[string]any{"resource_type": resourceType}

	// --ref 是 JSON 时优先：任意新资源类型都能走这条路，无需改 CLI。仅 github_repo
	// 下，非 JSON 的 --ref 是 checkout-ref 快捷值、与 --url 合并。
	if ref, ok, err := buildResourceRefFromRefFlag(cmd, resourceType, nil); err != nil {
		return err
	} else if ok {
		body["resource_ref"] = ref
	} else {
		switch resourceType {
		case "github_repo":
			ref, has, err := buildResourceRefFromFlags(cmd, resourceType, nil)
			if err != nil {
				return err
			}
			if !has {
				return fmt.Errorf("github_repo requires --url (or pass a JSON payload via --ref)")
			}
			body["resource_ref"] = ref
		case "local_directory":
			pathVal, _ := cmd.Flags().GetString("local-path")
			pathVal = strings.TrimSpace(pathVal)
			daemonVal, _ := cmd.Flags().GetString("daemon-id")
			daemonVal = strings.TrimSpace(daemonVal)
			if pathVal == "" || daemonVal == "" {
				return fmt.Errorf("local_directory requires --local-path and --daemon-id (or pass a JSON payload via --ref)")
			}
			ref := map[string]any{"local_path": pathVal, "daemon_id": daemonVal}
			if refLabel, _ := cmd.Flags().GetString("ref-label"); strings.TrimSpace(refLabel) != "" {
				ref["label"] = strings.TrimSpace(refLabel)
			}
			if mode, _ := cmd.Flags().GetString("execution-mode"); strings.TrimSpace(mode) != "" {
				ref["execution_mode"] = strings.TrimSpace(mode)
			}
			body["resource_ref"] = ref
		default:
			return fmt.Errorf("type %q has no built-in CLI shortcut; pass the payload via --ref '<json>'", resourceType)
		}
	}

	if label, _ := cmd.Flags().GetString("label"); label != "" {
		body["label"] = label
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	projectRef, err := resolveProjectID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/projects/"+projectRef.ID+"/resources", body, &result); err != nil {
		return fmt.Errorf("add project resource: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		headers := []string{"ID", "TYPE", "REF"}
		rows := [][]string{{
			strVal(result, "id"),
			strVal(result, "resource_type"),
			summarizeResourceRef(result["resource_ref"]),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

// runProjectResourceUpdate 执行 `project resource update <project-id> <resource-id>`：
// 局部编辑某条资源的 ref/label/position（先取现存行重建 payload，见注释）。
func runProjectResourceUpdate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	projectRef, err := resolveProjectID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}
	resourceRef, err := resolveProjectResourceID(ctx, client, projectRef.ID, args[1])
	if err != nil {
		return fmt.Errorf("resolve project resource: %w", err)
	}

	// 先拉出现有行，让各类型的快捷 flag 知道按哪个 schema 产出、该保留哪些字段。
	// server 把 resource_ref 当整体替换——像 `--default-branch-hint` 这种局部编辑
	// 必须在这里重建整个 payload，否则没提的 `url` 会消失、server 报 400。
	var existing map[string]any
	if err := client.GetJSON(ctx, "/api/projects/"+projectRef.ID+"/resources", &existing); err != nil {
		return fmt.Errorf("list project resources: %w", err)
	}
	var resourceType string
	var existingRef map[string]any
	if list, ok := existing["resources"].([]any); ok {
		for _, raw := range list {
			row, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if strVal(row, "id") == resourceRef.ID {
				resourceType = strVal(row, "resource_type")
				if ref, ok := row["resource_ref"].(map[string]any); ok {
					existingRef = ref
				}
				break
			}
		}
	}

	body := map[string]any{}

	if ref, ok, err := buildResourceRefFromRefFlag(cmd, resourceType, existingRef); err != nil {
		return err
	} else if ok {
		body["resource_ref"] = ref
	} else {
		ref, has, err := buildResourceRefFromFlags(cmd, resourceType, existingRef)
		if err != nil {
			return err
		}
		if has {
			body["resource_ref"] = ref
		}
	}

	clearLabel, _ := cmd.Flags().GetBool("clear-label")
	if clearLabel {
		body["label"] = nil
	} else if cmd.Flags().Changed("label") {
		label, _ := cmd.Flags().GetString("label")
		body["label"] = label
	}

	if cmd.Flags().Changed("position") {
		pos, _ := cmd.Flags().GetInt32("position")
		body["position"] = pos
	}

	if len(body) == 0 {
		return fmt.Errorf("nothing to update — pass --ref / --url / --local-path / --label / --position / --clear-label")
	}

	var result map[string]any
	if err := client.PutJSON(ctx, "/api/projects/"+projectRef.ID+"/resources/"+resourceRef.ID, body, &result); err != nil {
		return fmt.Errorf("update project resource: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		headers := []string{"ID", "TYPE", "REF", "LABEL"}
		rows := [][]string{{
			strVal(result, "id"),
			strVal(result, "resource_type"),
			summarizeResourceRef(result["resource_ref"]),
			strVal(result, "label"),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

// buildResourceRefFromRefFlag 处理通用 --ref：--ref 是 JSON 时的逃生门。对
// github_repo 身兼二职：JSON 对象/数组（"{...}" / "[...]"）仍是逃生门，其它任意
// 值——包括裸标量如数字 tag（"2024"）或全数字短 SHA（"1234567"）——是检出 ref
// 快捷值、与 --url 合并。只在值确实意在 JSON 时解析：否则 json.Unmarshal 会把
// "2024" 当数字吞掉，悄悄吃掉一个合法的检出 ref。
func buildResourceRefFromRefFlag(cmd *cobra.Command, resourceType string, existingRef map[string]any) (any, bool, error) {
	if !cmd.Flags().Changed("ref") {
		return nil, false, nil
	}
	rawRef, _ := cmd.Flags().GetString("ref")
	rawRef = strings.TrimSpace(rawRef)
	// --ref 是通用的 JSON resource_ref 逃生门。对 github_repo 身兼二职：JSON 对象/
	// 数组（"{...}" / "[...]"）仍是逃生门，但任何其它值——包括裸标量如数字 tag
	// （"2024"）或全数字短 SHA（"1234567"）——是检出-ref 快捷值，与 --url 合并。只在
	// 值确实意在 JSON 时解析；否则 json.Unmarshal 会把 "2024" 当数字接受、悄悄吞掉
	// 合法的检出 ref。
	if rawRef != "" && (resourceType != "github_repo" || looksLikeJSONPayload(rawRef)) {
		var ref any
		if err := json.Unmarshal([]byte(rawRef), &ref); err != nil {
			return nil, false, fmt.Errorf("--ref is not valid JSON: %w", err)
		}
		return ref, true, nil
	}
	if resourceType != "github_repo" {
		return nil, false, fmt.Errorf("--ref must be a JSON resource_ref payload for resource type %q", resourceType)
	}
	ref, has, err := buildResourceRefFromFlags(cmd, resourceType, existingRef)
	if err != nil {
		return nil, false, err
	}
	return ref, has, nil
}

// looksLikeJSONPayload 粗判字符串是否以 { 或 [ 开头（可能是 JSON 对象/数组）。
func looksLikeJSONPayload(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[")
}

// buildResourceRefFromFlags 把按类型分组的快捷 flag 收成 resource_ref payload；
// 用 existingRef 作底，局部编辑（只 --default-branch-hint、只 --ref-label）才能保留
// 没提的字段。只有调用方确实设了至少一个快捷 flag 才返回 (ref, true)——这让 update
// 命令能区分「没要求改」与「要把 ref 改成空对象」。add 路径 existingRef 可为 nil，
// 没有可合并的对象；此时漏掉必填字段的局部输入仍会被拒。
func buildResourceRefFromFlags(cmd *cobra.Command, resourceType string, existingRef map[string]any) (map[string]any, bool, error) {
	switch resourceType {
	case "github_repo":
		urlSet := cmd.Flags().Changed("url")
		hintSet := cmd.Flags().Changed("default-branch-hint")
		refSet := cmd.Flags().Changed("ref")
		if !urlSet && !hintSet && !refSet {
			return nil, false, nil
		}
		ref := map[string]any{}
		// 从现存行播种，`--default-branch-hint` 这类编辑才不会冲掉 `url`
		// （server 整体覆盖 resource_ref）。
		if existingRef != nil {
			if u, ok := existingRef["url"].(string); ok && strings.TrimSpace(u) != "" {
				ref["url"] = strings.TrimSpace(u)
			}
			if h, ok := existingRef["default_branch_hint"].(string); ok && strings.TrimSpace(h) != "" {
				ref["default_branch_hint"] = strings.TrimSpace(h)
			}
			if checkoutRef, ok := existingRef["ref"].(string); ok && strings.TrimSpace(checkoutRef) != "" {
				ref["ref"] = strings.TrimSpace(checkoutRef)
			}
		}
		if urlSet {
			urlVal, _ := cmd.Flags().GetString("url")
			urlVal = strings.TrimSpace(urlVal)
			if urlVal == "" {
				return nil, false, fmt.Errorf("--url cannot be empty")
			}
			ref["url"] = urlVal
		}
		if hintSet {
			hint := strings.TrimSpace(mustString(cmd, "default-branch-hint"))
			if hint == "" {
				delete(ref, "default_branch_hint")
			} else {
				ref["default_branch_hint"] = hint
			}
		}
		if refSet {
			checkoutRef := strings.TrimSpace(mustString(cmd, "ref"))
			if checkoutRef == "" {
				delete(ref, "ref")
			} else {
				ref["ref"] = checkoutRef
			}
		}
		if _, ok := ref["url"]; !ok {
			return nil, false, fmt.Errorf("github_repo: --url is required (no existing url to merge with)")
		}
		return ref, true, nil
	case "local_directory":
		pathSet := cmd.Flags().Changed("local-path")
		daemonSet := cmd.Flags().Changed("daemon-id")
		labelSet := cmd.Flags().Changed("ref-label")
		modeSet := cmd.Flags().Changed("execution-mode")
		if !pathSet && !daemonSet && !labelSet && !modeSet {
			return nil, false, nil
		}
		ref := map[string]any{}
		if existingRef != nil {
			if p, ok := existingRef["local_path"].(string); ok && strings.TrimSpace(p) != "" {
				ref["local_path"] = strings.TrimSpace(p)
			}
			if d, ok := existingRef["daemon_id"].(string); ok && strings.TrimSpace(d) != "" {
				ref["daemon_id"] = strings.TrimSpace(d)
			}
			if l, ok := existingRef["label"].(string); ok && strings.TrimSpace(l) != "" {
				ref["label"] = strings.TrimSpace(l)
			}
			if m, ok := existingRef["execution_mode"].(string); ok && strings.TrimSpace(m) != "" {
				ref["execution_mode"] = strings.TrimSpace(m)
			}
		}
		if pathSet {
			pathVal := strings.TrimSpace(mustString(cmd, "local-path"))
			if pathVal == "" {
				return nil, false, fmt.Errorf("--local-path cannot be empty")
			}
			ref["local_path"] = pathVal
		}
		if daemonSet {
			daemonVal := strings.TrimSpace(mustString(cmd, "daemon-id"))
			if daemonVal == "" {
				return nil, false, fmt.Errorf("--daemon-id cannot be empty")
			}
			ref["daemon_id"] = daemonVal
		}
		if labelSet {
			refLabel := strings.TrimSpace(mustString(cmd, "ref-label"))
			if refLabel == "" {
				delete(ref, "label")
			} else {
				ref["label"] = refLabel
			}
		}
		if modeSet {
			// 空值清空该字段回到 in_place 默认，镜像 --ref-label 清 label 的方式。
			mode := strings.TrimSpace(mustString(cmd, "execution-mode"))
			if mode == "" {
				delete(ref, "execution_mode")
			} else {
				ref["execution_mode"] = mode
			}
		}
		if v, ok := ref["local_path"].(string); !ok || v == "" {
			return nil, false, fmt.Errorf("local_directory: --local-path is required (no existing local_path to merge with)")
		}
		if v, ok := ref["daemon_id"].(string); !ok || v == "" {
			return nil, false, fmt.Errorf("local_directory: --daemon-id is required (no existing daemon_id to merge with)")
		}
		return ref, true, nil
	default:
		// 未知类型或类型为空（资源没找到）——调用方必须走 --ref。
		if cmd.Flags().Changed("url") || cmd.Flags().Changed("default-branch-hint") ||
			cmd.Flags().Changed("local-path") || cmd.Flags().Changed("daemon-id") ||
			cmd.Flags().Changed("ref-label") || cmd.Flags().Changed("execution-mode") {
			return nil, false, fmt.Errorf("no built-in shortcut for resource type %q; pass the full payload via --ref '<json>'", resourceType)
		}
		return nil, false, nil
	}
}

// mustString 取字符串 flag 值。
func mustString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

// runProjectResourceRemove 执行 `project resource remove <project-id> <resource-id>`：
// 把资源从项目上摘掉。
func runProjectResourceRemove(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	projectRef, err := resolveProjectID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}
	resourceRef, err := resolveProjectResourceID(ctx, client, projectRef.ID, args[1])
	if err != nil {
		return fmt.Errorf("resolve project resource: %w", err)
	}

	if err := client.DeleteJSON(ctx, "/api/projects/"+projectRef.ID+"/resources/"+resourceRef.ID); err != nil {
		return fmt.Errorf("remove project resource: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Resource %s removed from project %s.\n", resourceRef.Display, projectRef.Display)
	return nil
}

// summarizeResourceRef 从 resource_ref 对象里提取最有用的一段字面——github_repo 是
// URL（带检出 ref 时 "url @ ref"），local_directory 是本地路径。
func summarizeResourceRef(raw any) string {
	m, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	if u, ok := m["url"].(string); ok && u != "" {
		if ref, ok := m["ref"].(string); ok && strings.TrimSpace(ref) != "" {
			return u + " @ " + strings.TrimSpace(ref)
		}
		return u
	}
	if p, ok := m["local_path"].(string); ok && p != "" {
		return p
	}
	if data, err := json.Marshal(m); err == nil {
		return string(data)
	}
	return ""
}

// ---------------------------------------------------------------------------
// Helpers —— 公共辅助
// ---------------------------------------------------------------------------

// formatLead 把项目 lead 的 (type, id) 渲染成可读名字（未设 lead 返回空）。
func formatLead(project map[string]any, actors actorDisplayLookup) string {
	lType := strVal(project, "lead_type")
	lID := strVal(project, "lead_id")
	if lType == "" || lID == "" {
		return ""
	}
	return actors.actor(lType, lID)
}
