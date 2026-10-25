// 文件职责：实现 `multica issue review submit`——审查 Agent 提交一轮审查结论的入口。
// 结论、问题清单与评论 id 一起提交，服务端据此决定通过（绿勾）、打回（回到进行中并
// 唤醒开发）或停滞（停下交给人）。审查 Agent 不需要、也不应该自己改任务状态。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

func init() {
	group := &cobra.Command{Use: "review", Short: "Review outcomes for issues in review"}

	submit := &cobra.Command{
		Use:   "submit <issue-id>",
		Short: "Submit the verdict of one review round (pass, reject, stalled or no_action)",
		Args:  exactArgs(1),
		RunE:  runIssueReviewSubmit,
	}
	submit.Flags().String("verdict", "", "pass, reject, stalled or no_action")
	submit.Flags().String("findings-file", "", "JSON file with {summary, items[]}")
	submit.Flags().String("comment-id", "", "id of the review comment already posted on the issue")
	group.AddCommand(submit)
	issueCmd.AddCommand(group)
}

// runIssueReviewSubmit 读取问题清单文件并提交结论。文件可省略，此时只有结论与摘要。
func runIssueReviewSubmit(cmd *cobra.Command, args []string) error {
	verdict, _ := cmd.Flags().GetString("verdict")
	switch strings.ToLower(strings.TrimSpace(verdict)) {
	case "pass", "reject", "stalled", "no_action":
	default:
		return fmt.Errorf("--verdict must be pass, reject, stalled or no_action")
	}

	findings := map[string]any{}
	if path, _ := cmd.Flags().GetString("findings-file"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read findings file: %w", err)
		}
		if err := json.Unmarshal(raw, &findings); err != nil {
			return fmt.Errorf("findings file is not a JSON object: %w", err)
		}
	}

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

	commentID, _ := cmd.Flags().GetString("comment-id")
	body := map[string]any{
		"verdict":    strings.ToLower(strings.TrimSpace(verdict)),
		"findings":   findings,
		"comment_id": strings.TrimSpace(commentID),
	}
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+issueRef.ID+"/review-verdict", body, &result); err != nil {
		return fmt.Errorf("submit review verdict: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Printf("verdict %s recorded for round %v: state=%v\n", verdict, result["round"], result["state"])
	if reason, ok := result["reason"].(string); ok && reason != "" {
		fmt.Printf("automation stopped: %s\n", reason)
	}
	return nil
}
