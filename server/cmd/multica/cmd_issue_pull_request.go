package main

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var issuePullRequestCmd = &cobra.Command{
	Use:   "pull-request",
	Short: "Work with a single issue pull request or merge request",
}

var issuePullRequestLinkCmd = &cobra.Command{
	Use:   "link <issue-id>",
	Short: "Link a Gongfeng/GitHub merge request to an issue",
	Args:  exactArgs(1),
	RunE:  runIssuePullRequestLink,
}

func init() {
	issueCmd.AddCommand(issuePullRequestCmd)
	issuePullRequestCmd.AddCommand(issuePullRequestLinkCmd)

	issuePullRequestLinkCmd.Flags().String("provider", "gongfeng", "MR provider: gongfeng or github")
	issuePullRequestLinkCmd.Flags().String("project-path", "", "Repository project path, e.g. ChainWeaver/ida/user-center")
	issuePullRequestLinkCmd.Flags().String("repo-url", "", "Repository URL")
	issuePullRequestLinkCmd.Flags().Int32("number", 0, "Pull request or merge request number")
	issuePullRequestLinkCmd.Flags().Int32("iid", 0, "Gongfeng merge request IID")
	issuePullRequestLinkCmd.Flags().String("title", "", "Merge request title")
	issuePullRequestLinkCmd.Flags().String("state", "open", "Merge request state: open, draft, closed, or merged")
	issuePullRequestLinkCmd.Flags().String("html-url", "", "Merge request URL")
	issuePullRequestLinkCmd.Flags().String("source-branch", "", "Source branch name")
	issuePullRequestLinkCmd.Flags().String("target-branch", "", "Target branch name")
	issuePullRequestLinkCmd.Flags().String("author-login", "", "Author login")
	issuePullRequestLinkCmd.Flags().String("head-sha", "", "Head commit SHA")
	issuePullRequestLinkCmd.Flags().String("mergeable-state", "", "Mergeable state")
	issuePullRequestLinkCmd.Flags().Int32("additions", 0, "Lines added")
	issuePullRequestLinkCmd.Flags().Int32("deletions", 0, "Lines deleted")
	issuePullRequestLinkCmd.Flags().Int32("changed-files", 0, "Changed file count")
	issuePullRequestLinkCmd.Flags().Bool("close-intent", false, "Whether this MR is intended to close the issue")
	issuePullRequestLinkCmd.Flags().String("output", "table", "Output format: table or json")
}

func runIssuePullRequestLink(cmd *cobra.Command, args []string) error {
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

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	pr, _ := result["pull_request"].(map[string]any)
	printIssuePullRequestsTable(normalizePullRequestList([]any{pr}))
	return nil
}

func flagNameToJSONKey(name string) string {
	switch name {
	case "project-path":
		return "project_path"
	case "repo-url":
		return "repo_url"
	case "html-url":
		return "html_url"
	case "source-branch":
		return "source_branch"
	case "target-branch":
		return "target_branch"
	case "author-login":
		return "author_login"
	case "head-sha":
		return "head_sha"
	case "changed-files":
		return "changed_files"
	default:
		return name
	}
}
