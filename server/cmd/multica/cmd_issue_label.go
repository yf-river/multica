// 文件职责：定义 `multica issue label` 子命令组——列出某个 issue 上挂的标签、
// 给 issue 挂标签 / 摘标签。注意 label（标签库条目）本身的增删改查由
// cmd_label.go 的 `multica label ...` 负责，本文件只管理「issue ↔ label」的关联。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// issueLabelCmd 是 `issue label` 分组命令，管理挂在某个 issue 上的标签，
// 子命令：list / add / remove。用法示例：`multica issue label {list|add|remove}`。
// 标签条目本身归 cmd_label.go 的 `multica label ...` 管。
var issueLabelCmd = &cobra.Command{
	Use:   "label",
	Short: "Manage labels on an issue",
}

var issueLabelListCmd = &cobra.Command{
	Use:   "list <issue-id>",
	Short: "List labels on an issue",
	Args:  exactArgs(1),
	RunE:  runIssueLabelList,
}

var issueLabelAddCmd = &cobra.Command{
	Use:   "add <issue-id> <label-id>",
	Short: "Attach a label to an issue",
	Args:  exactArgs(2),
	RunE:  runIssueLabelAdd,
}

var issueLabelRemoveCmd = &cobra.Command{
	Use:   "remove <issue-id> <label-id>",
	Short: "Remove a label from an issue",
	Args:  exactArgs(2),
	RunE:  runIssueLabelRemove,
}

func init() {
	issueLabelCmd.AddCommand(issueLabelListCmd)
	issueLabelCmd.AddCommand(issueLabelAddCmd)
	issueLabelCmd.AddCommand(issueLabelRemoveCmd)

	issueLabelListCmd.Flags().String("output", "table", "Output format: table or json")
	issueLabelAddCmd.Flags().String("output", "table", "Output format: table or json")
	issueLabelRemoveCmd.Flags().String("output", "table", "Output format: table or json")
	issueLabelListCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")
	issueLabelAddCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")
	issueLabelRemoveCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")

	// 挂在顶层 `issue` 命令下，形成 `multica issue label ...` 的调用路径。
	issueCmd.AddCommand(issueLabelCmd)
}

// runIssueLabelList 执行 `issue label list <issue-id>`：列出该 issue 上已挂的标签。
func runIssueLabelList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	var result map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+issueRef.ID+"/labels", &result); err != nil {
		return fmt.Errorf("list issue labels: %w", err)
	}
	labelsRaw, _ := result["labels"].([]any)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, labelsRaw)
	}
	fullID, _ := cmd.Flags().GetBool("full-id")
	printLabelTable(labelsRaw, fullID)
	return nil
}

// runIssueLabelAdd 执行 `issue label add <issue-id> <label-id>`：把某个标签挂到
// issue 上，成功后照例把最新标签列表打给用户看。
func runIssueLabelAdd(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}
	labelRef, err := resolveLabelID(ctx, client, args[1])
	if err != nil {
		return fmt.Errorf("resolve label: %w", err)
	}

	body := map[string]any{"label_id": labelRef.ID}
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+issueRef.ID+"/labels", body, &result); err != nil {
		return fmt.Errorf("attach label: %w", err)
	}
	labelsRaw, _ := result["labels"].([]any)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, labelsRaw)
	}
	fullID, _ := cmd.Flags().GetBool("full-id")
	printLabelTable(labelsRaw, fullID)
	return nil
}

// runIssueLabelRemove 执行 `issue label remove <issue-id> <label-id>`：把标签从
// issue 上摘下。删除成功后顺手再拉一次最新列表展示结果；若这次刷新失败，仍打印
// 明确的成功消息——摘除动作本身已经成功了。
func runIssueLabelRemove(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}
	labelRef, err := resolveLabelID(ctx, client, args[1])
	if err != nil {
		return fmt.Errorf("resolve label: %w", err)
	}

	if err := client.DeleteJSON(ctx, "/api/issues/"+issueRef.ID+"/labels/"+labelRef.ID); err != nil {
		return fmt.Errorf("detach label: %w", err)
	}

	// 接着拉一次当前标签列表让用户看到结果。若刷新失败，仍打印清晰的
	// 成功消息——摘除本身已经成功了。
	var result map[string]any
	output, _ := cmd.Flags().GetString("output")
	if err := client.GetJSON(ctx, "/api/issues/"+issueRef.ID+"/labels", &result); err != nil {
		if output == "json" {
			return cli.PrintJSON(os.Stdout, map[string]any{"detached": true})
		}
		fmt.Fprintln(os.Stdout, "Label detached.")
		return nil
	}
	labelsRaw, _ := result["labels"].([]any)
	if output == "json" {
		return cli.PrintJSON(os.Stdout, labelsRaw)
	}
	fullID, _ := cmd.Flags().GetBool("full-id")
	printLabelTable(labelsRaw, fullID)
	return nil
}

// printLabelTable 以 ID/NAME/COLOR 三列表格渲染标签列表（labels 是响应里解出的
// []any，元素为标签 map）。
func printLabelTable(labels []any, fullID bool) {
	headers := []string{"ID", "NAME", "COLOR"}
	rows := make([][]string, 0, len(labels))
	for _, raw := range labels {
		l, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, []string{
			displayID(strVal(l, "id"), fullID),
			strVal(l, "name"),
			strVal(l, "color"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
}
