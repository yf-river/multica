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

var issueMRCmd = &cobra.Command{
	Use:   "mr",
	Short: "Work with issue merge requests",
}

var issuePullRequestLinkCmd = &cobra.Command{
	Use:   "link <issue-id>",
	Short: "Link a Gongfeng/GitHub merge request to an issue",
	Args:  exactArgs(1),
	RunE:  runIssuePullRequestLink,
}

var issueMRCreateCmd = &cobra.Command{
	Use:   "create <issue-id>",
	Short: "Create a Gongfeng merge request and link it to an issue",
	Args:  exactArgs(1),
	RunE:  runIssueMRCreate,
}

var issueMRLinkCmd = &cobra.Command{
	Use:   "link <issue-id>",
	Short: "Link a Gongfeng/GitHub merge request to an issue",
	Args:  exactArgs(1),
	RunE:  runIssuePullRequestLink,
}

var issueMRListCmd = &cobra.Command{
	Use:   "list <issue-id>",
	Short: "List merge requests linked to an issue",
	Args:  exactArgs(1),
	RunE:  runIssuePullRequests,
}

func init() {
	issueCmd.AddCommand(issuePullRequestCmd)
	issueCmd.AddCommand(issueMRCmd)
	issuePullRequestCmd.AddCommand(issuePullRequestLinkCmd)
	issueMRCmd.AddCommand(issueMRCreateCmd, issueMRLinkCmd, issueMRListCmd)

	addIssuePullRequestLinkFlags(issuePullRequestLinkCmd)
	addIssuePullRequestLinkFlags(issueMRLinkCmd)
	addIssueMRCreateFlags(issueMRCreateCmd)
	issueMRListCmd.Flags().String("output", "table", "Output format: table or json")
}

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

func runIssuePullRequestLink(cmd *cobra.Command, args []string) error {
	client, ctx, cancel, issueRef, err := newIssuePullRequestClientAndIssueRef(cmd, args[0])
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

func runIssueMRCreate(cmd *cobra.Command, args []string) error {
	client, ctx, cancel, issueRef, err := newIssuePullRequestClientAndIssueRef(cmd, args[0])
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

func newIssuePullRequestClientAndIssueRef(cmd *cobra.Command, issueArg string) (*cli.APIClient, context.Context, context.CancelFunc, resolvedID, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, nil, nil, resolvedID{}, err
	}
	ctx, cancel := cli.APIContext(context.Background())
	issueRef, err := resolveIssueRef(ctx, client, issueArg)
	if err != nil {
		cancel()
		return nil, nil, nil, resolvedID{}, fmt.Errorf("resolve issue: %w", err)
	}
	return client, ctx, cancel, issueRef, nil
}

func printIssuePullRequestMutationResult(cmd *cobra.Command, result map[string]any) error {
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
	case "close-intent":
		return "close_intent"
	case "remove-source-branch":
		return "remove_source_branch"
	default:
		return name
	}
}
