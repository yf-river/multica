// 文件职责：实现 `multica runtime profile ...`——自定义 runtime profile 管理
// （MUL-3284）。命令对象挂在 cmd_runtime.go 的 `runtime` 命令下。
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/agent"
)

// ---------------------------------------------------------------------------
// 背景：runtime profile 让工作区声明一套「自定义 agent runtime」——底层路由到某
// 个受支持的协议族（routing backend），但用站点自定义的 command_name 启动（例如
// 一个注入凭据的包装脚本）。profile 存服务端、按工作区隔离；daemon 在本机 PATH
// 上解析 command_name。
//
// `set-path` / `unset-path` 是本机专属的逃生门：把 profile_id → 可执行文件绝对
// 路径的映射记进本机 CLI 配置，使 daemon 能启动不在 PATH 上的 profile（或在多个
// 安装中挑特定那个）。该映射永不离开本机——不会发给 server。
// ---------------------------------------------------------------------------

// runtimeProfileCmd 是 `runtime profile` 分组命令（挂在 runtime 命令下）。
var runtimeProfileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage custom runtime profiles",
}

var runtimeProfileListCmd = &cobra.Command{
	Use:   "list",
	Short: "List custom runtime profiles in the workspace",
	RunE:  runRuntimeProfileList,
}

// runtimeProfileCreateCmd 创建工作区级自定义 profile。
var runtimeProfileCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a custom runtime profile",
	RunE:  runRuntimeProfileCreate,
}

// runtimeProfileUpdateCmd 更新 profile 的可变字段（protocol family 不可改）。
var runtimeProfileUpdateCmd = &cobra.Command{
	Use:   "update <profile-id>",
	Short: "Update a custom runtime profile (protocol family is immutable)",
	Args:  exactArgs(1),
	RunE:  runRuntimeProfileUpdate,
}

// runtimeProfileDeleteCmd 删除 profile。
var runtimeProfileDeleteCmd = &cobra.Command{
	Use:   "delete <profile-id>",
	Short: "Delete a custom runtime profile",
	Args:  exactArgs(1),
	RunE:  runRuntimeProfileDelete,
}

// runtimeProfileSetPathCmd 为本机钉住某个 profile 的可执行文件绝对路径（本地配置）。
var runtimeProfileSetPathCmd = &cobra.Command{
	Use:   "set-path <profile-id>",
	Short: "Pin a per-machine executable path for a runtime profile (local only)",
	Args:  exactArgs(1),
	RunE:  runRuntimeProfileSetPath,
}

// runtimeProfileUnsetPathCmd 移除本机对该 profile 的路径覆盖。
var runtimeProfileUnsetPathCmd = &cobra.Command{
	Use:   "unset-path <profile-id>",
	Short: "Remove a per-machine executable path override for a runtime profile",
	Args:  exactArgs(1),
	RunE:  runRuntimeProfileUnsetPath,
}

func init() {
	runtimeCmd.AddCommand(runtimeProfileCmd)
	runtimeProfileCmd.AddCommand(runtimeProfileListCmd)
	runtimeProfileCmd.AddCommand(runtimeProfileCreateCmd)
	runtimeProfileCmd.AddCommand(runtimeProfileUpdateCmd)
	runtimeProfileCmd.AddCommand(runtimeProfileDeleteCmd)
	runtimeProfileCmd.AddCommand(runtimeProfileSetPathCmd)
	runtimeProfileCmd.AddCommand(runtimeProfileUnsetPathCmd)

	// list
	runtimeProfileListCmd.Flags().String("output", "table", "Output format: table or json")

	// create
	runtimeProfileCreateCmd.Flags().String("protocol-family", "", "Supported backend the profile routes to (required)")
	runtimeProfileCreateCmd.Flags().String("command-name", "", "Executable the daemon resolves on PATH (required)")
	runtimeProfileCreateCmd.Flags().String("display-name", "", "Human-readable profile name (required)")
	runtimeProfileCreateCmd.Flags().String("description", "", "Optional description")
	runtimeProfileCreateCmd.Flags().String("output", "json", "Output format: table or json")

	// update
	runtimeProfileUpdateCmd.Flags().String("display-name", "", "New display name")
	runtimeProfileUpdateCmd.Flags().String("command-name", "", "New command name")
	runtimeProfileUpdateCmd.Flags().String("description", "", "New description")
	// NOTE: --fixed-arg 暂不进入 CLI 的 create/update 表面：产品路径在 UI 里解析
	// 命令+参数并以 command_name + fixed_args 存储。在我们给这里也加上能感知 argv
	// 的命令行解析器之前，保持 CLI 形态狭窄。
	runtimeProfileUpdateCmd.Flags().Bool("enabled", true, "Enable or disable the profile")
	runtimeProfileUpdateCmd.Flags().String("output", "json", "Output format: table or json")

	// set-path
	runtimeProfileSetPathCmd.Flags().String("path", "", "Absolute path to the executable on this machine (required)")
}

// runtimeProfilesPath 拼出工作区级 runtime profile 集合的端点路径。
func runtimeProfilesPath(workspaceID string) string {
	return fmt.Sprintf("/api/workspaces/%s/runtime-profiles", workspaceID)
}

// validateProtocolFamily 在客户端先用 agent 侧权威白名单校验协议族，明显的拼写
// 错误会带一份可选项清单立刻失败，而不是撞上不可读的 server 400。
func validateProtocolFamily(family string) error {
	if !agent.IsSupportedType(family) {
		return fmt.Errorf("invalid --protocol-family %q: must be one of %s",
			family, strings.Join(agent.SupportedTypes, ", "))
	}
	return nil
}

// NOTE: v1 故意不暴露 --visibility flag。server 强制每个 profile 都是 'workspace'，
// 因为读路径还没有落实 'private'（暴露它会让「私有」profile 泄漏）。等
// creator-visibility 过滤就位再重新加上。跟进：MUL-3308。

// runRuntimeProfileList 执行 `runtime profile list`：列出工作区内全部自定义 profile。
func runRuntimeProfileList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var resp struct {
		RuntimeProfiles []map[string]any `json:"runtime_profiles"`
	}
	if err := client.GetJSON(ctx, runtimeProfilesPath(workspaceID), &resp); err != nil {
		return fmt.Errorf("list runtime profiles: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, resp.RuntimeProfiles)
	}
	printRuntimeProfileTable(resp.RuntimeProfiles)
	return nil
}

// runRuntimeProfileCreate 执行 `runtime profile create`：必填 --protocol-family /
// --command-name / --display-name，本地先校验 protocol family 合法再 POST 创建。
func runRuntimeProfileCreate(cmd *cobra.Command, _ []string) error {
	family, _ := cmd.Flags().GetString("protocol-family")
	commandName, _ := cmd.Flags().GetString("command-name")
	displayName, _ := cmd.Flags().GetString("display-name")
	description, _ := cmd.Flags().GetString("description")

	if strings.TrimSpace(family) == "" {
		return fmt.Errorf("--protocol-family is required")
	}
	if strings.TrimSpace(commandName) == "" {
		return fmt.Errorf("--command-name is required")
	}
	if strings.TrimSpace(displayName) == "" {
		return fmt.Errorf("--display-name is required")
	}
	if err := validateProtocolFamily(family); err != nil {
		return err
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return err
	}

	body := map[string]any{
		"display_name":    displayName,
		"protocol_family": family,
		"command_name":    commandName,
	}
	if description != "" {
		body["description"] = description
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var profile map[string]any
	if err := client.PostJSON(ctx, runtimeProfilesPath(workspaceID), body, &profile); err != nil {
		return fmt.Errorf("create runtime profile: %w", err)
	}
	return outputRuntimeProfile(cmd, profile)
}

// runRuntimeProfileUpdate 执行 `runtime profile update <profile-id>`：只把显式
// Changed 的字段（display-name/command-name/description/enabled）PATCH 上去。
func runRuntimeProfileUpdate(cmd *cobra.Command, args []string) error {
	profileID := args[0]

	body := map[string]any{}
	if cmd.Flags().Changed("display-name") {
		v, _ := cmd.Flags().GetString("display-name")
		body["display_name"] = v
	}
	if cmd.Flags().Changed("command-name") {
		v, _ := cmd.Flags().GetString("command-name")
		body["command_name"] = v
	}
	if cmd.Flags().Changed("description") {
		v, _ := cmd.Flags().GetString("description")
		body["description"] = v
	}
	if cmd.Flags().Changed("enabled") {
		v, _ := cmd.Flags().GetBool("enabled")
		body["enabled"] = v
	}

	if len(body) == 0 {
		return fmt.Errorf("no fields to update: pass at least one of --display-name, --command-name, --description, --enabled")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	path := runtimeProfilesPath(workspaceID) + "/" + profileID
	var profile map[string]any
	if err := client.PatchJSON(ctx, path, body, &profile); err != nil {
		return fmt.Errorf("update runtime profile: %w", err)
	}
	return outputRuntimeProfile(cmd, profile)
}

// runRuntimeProfileDelete 执行 `runtime profile delete <profile-id>`：删除 profile。
// 若 server 因仍绑定着活跃 agent 而拒绝（409），原样透出它的说明，让用户知道该
// 解绑什么——而不是给一句通用的 HTTP 包装错误。
func runRuntimeProfileDelete(cmd *cobra.Command, args []string) error {
	profileID := args[0]

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	path := runtimeProfilesPath(workspaceID) + "/" + profileID
	if err := client.DeleteJSON(ctx, path); err != nil {
		// 409 means the server refused — usually because active agents are
		// still bound to this profile, on machines it names. Surface that
		// sentence rather than the raw response body: it already reads as
		// guidance, and it is what tells the user the blockers may be sitting
		// on a machine other than the one they were cleaning up.
		if _, msg, isConflict := serverConflictMessage(err); isConflict {
			return errors.New(msg)
		}
		var httpErr *cli.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusConflict {
			return fmt.Errorf("cannot delete runtime profile %s: profile still has active agents bound to it", profileID)
		}
		return fmt.Errorf("delete runtime profile: %w", err)
	}
	fmt.Printf("Deleted runtime profile %s\n", profileID)
	return nil
}

// runRuntimeProfileSetPath 执行 `runtime profile set-path <profile-id> --path <abs>`：
// 把本机 profile_id → 可执行文件绝对路径的覆盖写进本地 CLI 配置（仅本机，不上传
// server）。要求 --path 是绝对路径。
func runRuntimeProfileSetPath(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("runtime profile set-path"); err != nil {
		return err
	}
	profileID := args[0]
	path, _ := cmd.Flags().GetString("path")
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("--path is required")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("--path must be an absolute path, got %q", path)
	}

	profile := resolveProfile(cmd)
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err != nil {
		return fmt.Errorf("load CLI config: %w", err)
	}
	if cfg.ProfileCommandOverrides == nil {
		cfg.ProfileCommandOverrides = map[string]string{}
	}
	cfg.ProfileCommandOverrides[profileID] = path
	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return fmt.Errorf("save CLI config: %w", err)
	}
	fmt.Printf("Pinned runtime profile %s to %s on this machine.\n", profileID, path)
	fmt.Println("Restart the daemon for the change to take effect.")
	return nil
}

// runRuntimeProfileUnsetPath 执行 `runtime profile unset-path <profile-id>`：删掉
// 本机对该 profile 的路径覆盖；没设置过就提示并直接返回。
func runRuntimeProfileUnsetPath(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("runtime profile unset-path"); err != nil {
		return err
	}
	profileID := args[0]

	profile := resolveProfile(cmd)
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err != nil {
		return fmt.Errorf("load CLI config: %w", err)
	}
	if _, ok := cfg.ProfileCommandOverrides[profileID]; !ok {
		fmt.Printf("No per-machine path override set for runtime profile %s.\n", profileID)
		return nil
	}
	delete(cfg.ProfileCommandOverrides, profileID)
	if len(cfg.ProfileCommandOverrides) == 0 {
		// 归一化回 nil，让该 key 从保存的 JSON 里消失。
		cfg.ProfileCommandOverrides = nil
	}
	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return fmt.Errorf("save CLI config: %w", err)
	}
	fmt.Printf("Removed per-machine path override for runtime profile %s.\n", profileID)
	fmt.Println("Restart the daemon for the change to take effect.")
	return nil
}

// outputRuntimeProfile 按 --output 渲染单个 profile 结果。
func outputRuntimeProfile(cmd *cobra.Command, profile map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, profile)
	}
	printRuntimeProfileTable([]map[string]any{profile})
	return nil
}

// printRuntimeProfileTable 以稳定排序的表格渲染 profile 列表（按 display name 排）。
func printRuntimeProfileTable(profiles []map[string]any) {
	headers := []string{"ID", "DISPLAY_NAME", "PROTOCOL_FAMILY", "COMMAND_NAME", "ENABLED"}
	rows := make([][]string, 0, len(profiles))
	for _, p := range profiles {
		rows = append(rows, []string{
			strVal(p, "id"),
			strVal(p, "display_name"),
			strVal(p, "protocol_family"),
			strVal(p, "command_name"),
			strVal(p, "enabled"),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][1] < rows[j][1] })
	cli.PrintTable(os.Stdout, headers, rows)
}
