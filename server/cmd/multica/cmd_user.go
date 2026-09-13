// 文件职责：实现 `multica user` 命令组——查/改当前登录用户的个人资料。user 命名
// 空间的存在，是为了让 daemon 注入 agent 简报里的 `## Requesting User` 段落有一个
// 人能照着操作而不必手写 PATCH /api/me 的 CLI 表面。当前只接 profile description；
// 未来的用户级旋钮（如偏好语言）应作为更多子命令落在这里，而不是去别处扩动词。
package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// User namespace（见文件头说明）；userCmd 是 `multica user` 父命令。
var userCmd = &cobra.Command{
	Use:   "user",
	Short: "Work with your user account",
}

// userProfileCmd 是 `user profile` 分组命令（get/update）。
var userProfileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Get or update your personal profile",
	Long: "Manage the personal profile that agents see when they pick up a task " +
		"on your behalf. The description is injected into the agent brief under " +
		"`## Requesting User`, so use it to share role, stack, and collaboration " +
		"preferences.",
}

var userProfileGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show your current user profile",
	RunE:  runUserProfileGet,
}

var userProfileUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update your user profile (currently: profile description)",
	Long: "Set the personal profile description that gets injected into agent " +
		"briefs as `## Requesting User`. Pass an empty value to clear it.\n\n" +
		"Pick the input mode that preserves your content:\n" +
		"  --description \"...\"          inline (decodes \\n / \\t escapes)\n" +
		"  --description-stdin           pipe a HEREDOC (preserves verbatim)\n" +
		"  --description-file <path>     read a UTF-8 file (Windows-safe)\n",
	RunE: runUserProfileUpdate,
}

func init() {
	userCmd.AddCommand(userProfileCmd)
	userProfileCmd.AddCommand(userProfileGetCmd)
	userProfileCmd.AddCommand(userProfileUpdateCmd)

	userProfileGetCmd.Flags().String("output", "table", "Output format: table or json")

	userProfileUpdateCmd.Flags().String("description", "", "New profile description (decodes \\n, \\r, \\t, \\\\; pipe via --description-stdin to preserve literal backslashes)")
	userProfileUpdateCmd.Flags().Bool("description-stdin", false, "Read description from stdin (preserves multi-line content verbatim)")
	userProfileUpdateCmd.Flags().String("description-file", "", "Read description from a UTF-8 file (preserves multi-line content verbatim; use this on Windows when stdin piping mangles non-ASCII bytes). The path must be inside the current working directory unless --allow-external-file is set.")
	userProfileUpdateCmd.Flags().Bool("allow-external-file", false, "Allow --description-file to read a path outside the current working directory. Off by default so a stale temp file from another run/environment can't be picked up (MUL-4252).")
	userProfileUpdateCmd.Flags().Bool("clear", false, "Clear the profile description (equivalent to --description \"\")")
	userProfileUpdateCmd.Flags().String("output", "table", "Output format: table or json")
}

// runUserProfileGet 执行 `user profile get`：GET /api/me 并展示。
func runUserProfileGet(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var me map[string]any
	if err := client.GetJSON(ctx, "/api/me", &me); err != nil {
		return fmt.Errorf("get user profile: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, me)
	}

	printUserProfileTable(os.Stdout, me)
	return nil
}

// runUserProfileUpdate 执行 `user profile update`：写 profile description。
// 正文来源沿用 resolveTextFlag 三种输入；另设 --clear 一键清空。
func runUserProfileUpdate(cmd *cobra.Command, _ []string) error {
	// `--clear` 是独立 flag（不是「传空串」），因为 cobra 的 Changed("") 判断会让
	// flag 默认值与「用户显式敲了 --description ""」混在一起。两种写法都支持——
	// 空串行内传法正是写 bash 脚本的人会伸手去用的形式。
	clearFlag, _ := cmd.Flags().GetBool("clear")
	desc, hasDesc, err := resolveTextFlag(cmd, "description")
	if err != nil {
		return err
	}

	if clearFlag && hasDesc {
		return fmt.Errorf("--clear cannot be combined with --description / --description-stdin / --description-file")
	}
	if !clearFlag && !hasDesc && !cmd.Flags().Changed("description") {
		return fmt.Errorf("nothing to update; pass --description, --description-stdin, --description-file, or --clear")
	}

	if clearFlag {
		desc = ""
	}

	body := map[string]any{"profile_description": desc}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var me map[string]any
	if err := client.PatchJSON(ctx, "/api/me", body, &me); err != nil {
		return fmt.Errorf("update user profile: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, me)
	}

	printUserProfileTable(os.Stdout, me)
	return nil
}

// printUserProfileTable 以键值表渲染 /api/me 的个人资料（profile description 空则
// 显示 "(not set)"）。
func printUserProfileTable(out *os.File, me map[string]any) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	defer w.Flush()

	fmt.Fprintf(w, "ID\t%s\n", strVal(me, "id"))
	fmt.Fprintf(w, "NAME\t%s\n", strVal(me, "name"))
	fmt.Fprintf(w, "EMAIL\t%s\n", strVal(me, "email"))
	desc := strVal(me, "profile_description")
	if desc == "" {
		desc = "(not set)"
	}
	fmt.Fprintf(w, "PROFILE DESCRIPTION\t%s\n", desc)
}
