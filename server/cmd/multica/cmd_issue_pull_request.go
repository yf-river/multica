// 文件职责：实现 `multica issue mr` 子命令组——create（在 gongfeng 上创建合并
// 请求并链到 issue）、link（把已有的 gongfeng/github MR/PR 链到 issue，例如 agent
// 完成编码后回填链接）、list（复用 cmd_issue.go 的 runIssuePullRequests）。另外
// 提供请求体构造辅助 flagNameToJSONKey 与结果打印辅助。
package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// issueMRCmd 是 `issue mr` 分组命令：create / link / list。
var issueMRCmd = &cobra.Command{
	Use:   "mr",
	Short: "Work with issue merge requests",
}

// issueMRCreateCmd 在 gongfeng 上新建合并请求，并把它的引用链回 issue。
var issueMRCreateCmd = &cobra.Command{
	Use:   "create <issue-id>",
	Short: "Create a Gongfeng merge request and link it to an issue",
	Args:  exactArgs(1),
	RunE:  runIssueMRCreate,
}

// issueMRLinkCmd 把一个已存在的 gongfeng/github PR 记录链到 issue 上。
var issueMRLinkCmd = &cobra.Command{
	Use:   "link <issue-id>",
	Short: "Link a Gongfeng/GitHub merge request to an issue",
	Args:  exactArgs(1),
	RunE:  runIssuePullRequestLink,
}

// issueMRListCmd 复用 runIssuePullRequests，列出一个 issue 关联的全部 PR/MR。
var issueMRListCmd = &cobra.Command{
	Use:   "list <issue-id>",
	Short: "List merge requests linked to an issue",
	Args:  exactArgs(1),
	RunE:  runIssuePullRequests,
}

func init() {
	issueCmd.AddCommand(issueMRCmd)
	issueMRCmd.AddCommand(issueMRCreateCmd, issueMRLinkCmd, issueMRListCmd)

	addIssuePullRequestLinkFlags(issueMRLinkCmd)
	addIssueMRCreateFlags(issueMRCreateCmd)
	issueMRListCmd.Flags().String("output", "table", "Output format: table or json")
}

// addIssuePullRequestLinkFlags 给 `issue mr link` 挂上 PR/MR 元数据 flag。
func addIssuePullRequestLinkFlags(cmd *cobra.Command) {
	cmd.Flags().String("provider", "gongfeng", "MR provider: gongfeng or github")
	cmd.Flags().String("project-path", "", "Repository project path, e.g. ChainWeaver/ida/user-center")
	cmd.Flags().String("repo-url", "", "Repository URL")
	cmd.Flags().Int32("number", 0, "Pull request or merge request number")
	cmd.Flags().Int32("iid", 0, "Gongfeng merge request IID")
	cmd.Flags().String("title", "", "Merge request title")
	cmd.Flags().String("state", "open", "Merge request state: open, draft, closed, or merged")
	cmd.Flags().String("html-url", "", "Merge request URL")
	cmd.Flags().String("source-branch", "", "Source branch name")
	cmd.Flags().String("target-branch", "", "Target branch name")
	cmd.Flags().String("author-login", "", "Author login")
	cmd.Flags().String("head-sha", "", "Head commit SHA")
	cmd.Flags().String("mergeable-state", "", "Mergeable state")
	cmd.Flags().Int32("additions", 0, "Lines added")
	cmd.Flags().Int32("deletions", 0, "Lines deleted")
	cmd.Flags().Int32("changed-files", 0, "Changed file count")
	cmd.Flags().Bool("close-intent", false, "Whether this MR is intended to close the issue")
	cmd.Flags().String("output", "table", "Output format: table or json")
}

// addIssueMRCreateFlags 给 `issue mr create` 挂上创建 MR 所需 flag。
func addIssueMRCreateFlags(cmd *cobra.Command) {
	cmd.Flags().String("provider", "gongfeng", "MR provider: gongfeng")
	cmd.Flags().String("project-path", "", "Repository project path, e.g. ChainWeaver/ida/user-center")
	cmd.Flags().String("source-branch", "", "Source branch name")
	cmd.Flags().String("target-branch", "", "Target branch name")
	cmd.Flags().String("title", "", "Merge request title")
	cmd.Flags().String("description", "", "Merge request description")
	cmd.Flags().String("description-file", "", "Read merge request description from a UTF-8 file")
	cmd.Flags().Bool("close-intent", false, "Whether this MR is intended to close the issue")
	cmd.Flags().Bool("remove-source-branch", false, "Remove source branch after merge")
	cmd.Flags().Bool("squash", false, "Squash commits when merging")
	cmd.Flags().String("output", "table", "Output format: table or json")
}

// runIssuePullRequestLink 执行 `issue mr link <issue-id>`：把调用方提供的 PR/MR
// 元数据（provider/project/number/分支等）POST 到 issue 的 pull-requests 关联上，
// 从而在平台侧留下「这个 PR 是为解决该 issue」的痕迹。只有非零/非空的 flag 才
// 进请求体，避免把默认零值当真实值上报。
func runIssuePullRequestLink(cmd *cobra.Command, args []string) error {
	client, ctx, cancel, issueRef, err := newResolvedAPIClientContext(cmd, args[0], "issue", resolveIssueRef)
	if err != nil {
		return err
	}
	defer cancel()

	body := map[string]any{}
	for _, name := range []string{
		"provider", "project-path", "repo-url", "title", "state", "html-url",
		"source-branch", "target-branch", "author-login", "head-sha",
	} {
		value, _ := cmd.Flags().GetString(name)
		if value != "" {
			body[flagNameToJSONKey(name)] = value
		}
	}
	if mergeableState, _ := cmd.Flags().GetString("mergeable-state"); mergeableState != "" {
		body["mergeable_state"] = mergeableState
	}
	for _, name := range []string{"number", "iid", "additions", "deletions", "changed-files"} {
		value, _ := cmd.Flags().GetInt32(name)
		if value != 0 {
			body[flagNameToJSONKey(name)] = value
		}
	}
	if closeIntent, _ := cmd.Flags().GetBool("close-intent"); closeIntent {
		body["close_intent"] = true
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+url.PathEscape(issueRef.ID)+"/pull-requests", body, &result); err != nil {
		return fmt.Errorf("link issue pull request: %w", err)
	}

	return printIssuePullRequestMutationResult(cmd, result)
}

// runIssueMRCreate 执行 `issue mr create <issue-id>`：经 /merge-requests/create
// 端点直接在 gongfeng 上创建 MR 并链到 issue。--description 优先用
// --description-file 的内容覆盖（适用于脚本/agent 从文件读长描述）。
func runIssueMRCreate(cmd *cobra.Command, args []string) error {
	client, ctx, cancel, issueRef, err := newResolvedAPIClientContext(cmd, args[0], "issue", resolveIssueRef)
	if err != nil {
		return err
	}
	defer cancel()

	body := map[string]any{}
	for _, name := range []string{"provider", "project-path", "source-branch", "target-branch", "title", "description"} {
		value, _ := cmd.Flags().GetString(name)
		if value != "" {
			body[flagNameToJSONKey(name)] = value
		}
	}
	if descriptionFile, _ := cmd.Flags().GetString("description-file"); descriptionFile != "" {
		data, err := os.ReadFile(descriptionFile)
		if err != nil {
			return fmt.Errorf("read description file: %w", err)
		}
		body["description"] = string(data)
	}
	for _, name := range []string{"close-intent", "remove-source-branch", "squash"} {
		value, _ := cmd.Flags().GetBool(name)
		if value {
			body[flagNameToJSONKey(name)] = true
		}
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+url.PathEscape(issueRef.ID)+"/merge-requests/create", body, &result); err != nil {
		return fmt.Errorf("create issue merge request: %w", err)
	}

	return printIssuePullRequestMutationResult(cmd, result)
}

// printIssuePullRequestMutationResult 输出 link/create 这类「变更型」命令的结果：
// --output json 直接透传响应；表格则从结果里抽出 pull_request 打表。
func printIssuePullRequestMutationResult(cmd *cobra.Command, result map[string]any) error {
	if wantsJSONOutput(cmd) {
		return cli.PrintJSON(os.Stdout, result)
	}
	pr, _ := result["pull_request"].(map[string]any)
	printIssuePullRequestsTable(normalizePullRequestList([]any{pr}))
	return nil
}

// flagNameToJSONKey 把 kebab-case 的 flag 名（如 project-path）转成 snake_case
// 的 JSON 字段名（project_path），供请求体 key 使用。
func flagNameToJSONKey(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}
