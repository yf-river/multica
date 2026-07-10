package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/util"
)

// resolveTextFlag picks between a `--<name>` inline value, a `--<name>-stdin`
// flag, and a `--<name>-file <path>` flag, mirroring the existing text field
// input pattern. It returns the resolved string and an error
// when more than one source is set, or when stdin/file is requested but
// produces no body. Inline flag values are passed through
// util.UnescapeBackslashEscapes so bash-double-quoted `\n` becomes a real
// newline; stdin and file bodies are returned verbatim so literal backslashes
// survive intact.
//
// The `-file` source exists for Windows agents: piping HEREDOC content to
// `--<name>-stdin` from Windows PowerShell silently drops non-ASCII bytes
// (PowerShell 5.1's `$OutputEncoding` defaults to ASCIIEncoding when piping
// to a native command), so Chinese / Cyrillic / any non-ASCII content
// arrives as `?`. Reading a UTF-8 file directly bypasses the shell's pipe
// re-encoding entirely. See issues #2198 / #2236 / #2376.
func resolveTextFlag(cmd *cobra.Command, flagName string) (string, bool, error) {
	stdinFlag := flagName + "-stdin"
	fileFlag := flagName + "-file"
	useStdin, _ := cmd.Flags().GetBool(stdinFlag)
	inline, _ := cmd.Flags().GetString(flagName)
	filePath, _ := cmd.Flags().GetString(fileFlag)

	sources := 0
	if useStdin {
		sources++
	}
	if inline != "" {
		sources++
	}
	if filePath != "" {
		sources++
	}
	if sources > 1 {
		return "", false, fmt.Errorf("--%s, --%s, and --%s are mutually exclusive", flagName, stdinFlag, fileFlag)
	}

	if useStdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", false, fmt.Errorf("read stdin for --%s: %w", stdinFlag, err)
		}
		body := strings.TrimSuffix(string(data), "\n")
		if body == "" {
			return "", false, fmt.Errorf("stdin content for --%s is empty", stdinFlag)
		}
		return body, true, nil
	}
	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", false, fmt.Errorf("read file for --%s: %w", fileFlag, err)
		}
		body := strings.TrimSuffix(string(data), "\n")
		if body == "" {
			return "", false, fmt.Errorf("file content for --%s is empty", fileFlag)
		}
		return body, true, nil
	}
	if inline == "" {
		return "", false, nil
	}
	return util.UnescapeBackslashEscapes(inline), true, nil
}

func resolveFileOrStdinTextFlag(cmd *cobra.Command, flagName string) (string, bool, error) {
	stdinFlag := flagName + "-stdin"
	fileFlag := flagName + "-file"
	useStdin, _ := cmd.Flags().GetBool(stdinFlag)
	filePath, _ := cmd.Flags().GetString(fileFlag)

	if useStdin && filePath != "" {
		return "", false, fmt.Errorf("--%s and --%s are mutually exclusive", stdinFlag, fileFlag)
	}
	if useStdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", false, fmt.Errorf("read stdin for --%s: %w", stdinFlag, err)
		}
		body := strings.TrimSuffix(string(data), "\n")
		if body == "" {
			return "", false, fmt.Errorf("stdin content for --%s is empty", stdinFlag)
		}
		return body, true, nil
	}
	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", false, fmt.Errorf("read file for --%s: %w", fileFlag, err)
		}
		body := strings.TrimSuffix(string(data), "\n")
		if body == "" {
			return "", false, fmt.Errorf("file content for --%s is empty", fileFlag)
		}
		return body, true, nil
	}
	return "", false, nil
}

var issueCmd = &cobra.Command{
	Use:   "issue",
	Short: "Work with issues",
}

var issueListCmd = &cobra.Command{
	Use:   "list",
	Short: "List issues in the workspace",
	RunE:  runIssueList,
}

var issueGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get issue details",
	Args:  exactArgs(1),
	RunE:  runIssueGet,
}

var issueChildrenCmd = &cobra.Command{
	Use:   "children <id>",
	Short: "List child issues for an issue",
	Args:  exactArgs(1),
	RunE:  runIssueChildren,
}

var issuePullRequestsCmd = &cobra.Command{
	Use:     "pull-requests <id>",
	Aliases: []string{"prs"},
	Short:   "List pull requests linked to an issue",
	Args:    exactArgs(1),
	RunE:    runIssuePullRequests,
}

var issueSourceFetchCmd = &cobra.Command{
	Use:   "source-fetch <id>",
	Short: "Record external source fetch evidence on an issue",
	Args:  exactArgs(1),
	RunE:  runIssueSourceFetch,
}

var issueCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new issue",
	RunE:  runIssueCreate,
}

var issueUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update an issue",
	Args:  exactArgs(1),
	RunE:  runIssueUpdate,
}

var issueAssignCmd = &cobra.Command{
	Use:   "assign <id>",
	Short: "Assign an issue to a member, agent, or squad",
	Args:  exactArgs(1),
	RunE:  runIssueAssign,
}

var issueStatusCmd = &cobra.Command{
	Use:   "status <id> <status>",
	Short: "Change issue status",
	Long: "Change an issue's status. Valid statuses: " +
		"backlog, todo, in_progress, in_review, done, blocked, cancelled.",
	Args: exactArgs(2),
	RunE: runIssueStatus,
}

// Comment subcommands.

var issueCommentCmd = &cobra.Command{
	Use:   "comment",
	Short: "Work with issue comments",
}

var issueCommentListCmd = &cobra.Command{
	Use:   "list <issue-id>",
	Short: "List comments on an issue",
	Args:  exactArgs(1),
	RunE:  runIssueCommentList,
}

var issueCommentAddCmd = &cobra.Command{
	Use:   "add <issue-id>",
	Short: "Add a comment to an issue",
	Args:  exactArgs(1),
	RunE:  runIssueCommentAdd,
}

var issueCommentDeleteCmd = &cobra.Command{
	Use:   "delete <comment-id>",
	Short: "Delete a comment",
	Args:  exactArgs(1),
	RunE:  runIssueCommentDelete,
}

// Subscriber subcommands.

var issueSubscriberCmd = &cobra.Command{
	Use:   "subscriber",
	Short: "Work with issue subscribers",
}

var issueSubscriberListCmd = &cobra.Command{
	Use:   "list <issue-id>",
	Short: "List subscribers of an issue",
	Args:  exactArgs(1),
	RunE:  runIssueSubscriberList,
}

var issueSubscriberAddCmd = &cobra.Command{
	Use:   "add <issue-id>",
	Short: "Subscribe a user or agent to an issue (defaults to the caller)",
	Args:  exactArgs(1),
	RunE:  runIssueSubscriberAdd,
}

var issueSubscriberRemoveCmd = &cobra.Command{
	Use:   "remove <issue-id>",
	Short: "Unsubscribe a user or agent from an issue (defaults to the caller)",
	Args:  exactArgs(1),
	RunE:  runIssueSubscriberRemove,
}

// Execution history subcommands.

var issueRunsCmd = &cobra.Command{
	Use:   "runs <issue-id>",
	Short: "List execution history for an issue",
	Args:  exactArgs(1),
	RunE:  runIssueRuns,
}

var issueRunMessagesCmd = &cobra.Command{
	Use:   "run-messages <task-id>",
	Short: "List messages for an execution",
	Args:  exactArgs(1),
	RunE:  runIssueRunMessages,
}

var issueRerunCmd = &cobra.Command{
	Use:   "rerun <id>",
	Short: "Re-enqueue an issue's current agent assignment as a fresh task",
	Args:  exactArgs(1),
	RunE:  runIssueRerun,
}

var issueCancelTaskCmd = &cobra.Command{
	Use:   "cancel-task <task-id>",
	Short: "Cancel a running or queued task (interrupts in-flight agent)",
	Long: "Cancel a single task by its ID. Accepts the short ID prefix shown by `issue runs`. " +
		"Use --issue to scope short-ID resolution to a specific issue when ambiguous. " +
		"Triggers daemon-side interrupt of any in-flight agent so it stops emitting tool calls promptly.",
	Args: exactArgs(1),
	RunE: runIssueCancelTask,
}

var issueSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search issues by title or description",
	Args:  cobra.ExactArgs(1),
	RunE:  runIssueSearch,
}

var validIssueStatuses = []string{
	"backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled",
}

var validIssuePriorities = []string{
	"urgent", "high", "medium", "low", "none",
}

func validateIssueStatus(status string) error {
	return validateIssueEnum("status", status, validIssueStatuses)
}

func validateIssuePriority(priority string) error {
	return validateIssueEnum("priority", priority, validIssuePriorities)
}

func validateIssueEnum(field, value string, allowed []string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("invalid %s %q; valid values: %s", field, value, strings.Join(allowed, ", "))
}

func init() {
	issueCmd.AddCommand(issueListCmd)
	issueCmd.AddCommand(issueGetCmd)
	issueCmd.AddCommand(issueChildrenCmd)
	issueCmd.AddCommand(issuePullRequestsCmd)
	issueCmd.AddCommand(issueSourceFetchCmd)
	issueCmd.AddCommand(issueCreateCmd)
	issueCmd.AddCommand(issueUpdateCmd)
	issueCmd.AddCommand(issueAssignCmd)
	issueCmd.AddCommand(issueStatusCmd)
	issueCmd.AddCommand(issueCommentCmd)
	issueCmd.AddCommand(issueSubscriberCmd)
	issueCmd.AddCommand(issueRunsCmd)
	issueCmd.AddCommand(issueRunMessagesCmd)
	issueCmd.AddCommand(issueRerunCmd)
	issueCmd.AddCommand(issueCancelTaskCmd)
	issueCmd.AddCommand(issueSearchCmd)

	issueCommentCmd.AddCommand(issueCommentListCmd)
	issueCommentCmd.AddCommand(issueCommentAddCmd)
	issueCommentCmd.AddCommand(issueCommentDeleteCmd)

	issueSubscriberCmd.AddCommand(issueSubscriberListCmd)
	issueSubscriberCmd.AddCommand(issueSubscriberAddCmd)
	issueSubscriberCmd.AddCommand(issueSubscriberRemoveCmd)

	// issue list
	issueListCmd.Flags().String("output", "table", "Output format: table or json")
	issueListCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")
	issueListCmd.Flags().String("status", "", "Filter by status")
	issueListCmd.Flags().String("priority", "", "Filter by priority")
	issueListCmd.Flags().String("assignee", "", "Filter by assignee name (member, agent, or squad; fuzzy match)")
	issueListCmd.Flags().String("assignee-id", "", "Filter by assignee UUID — member, agent, or squad (mutually exclusive with --assignee)")
	issueListCmd.Flags().String("project", "", "Filter by project ID")
	issueListCmd.Flags().StringSlice("metadata", nil, "Filter by metadata key=value (repeatable; combined with AND). Value is JSON-parsed: 'true'/'false' → bool, numbers → number, otherwise string. Wrap as '\"42\"' to force a string when the value would otherwise sniff as a number.")
	issueListCmd.Flags().Int("limit", 50, "Maximum number of issues to return")
	issueListCmd.Flags().Int("offset", 0, "Number of issues to skip (for pagination)")

	// issue get
	issueGetCmd.Flags().String("output", "json", "Output format: table or json")

	// issue children
	issueChildrenCmd.Flags().String("output", "table", "Output format: table or json")

	// issue pull-requests
	issuePullRequestsCmd.Flags().String("output", "table", "Output format: table or json")

	// issue source-fetch
	issueSourceFetchCmd.Flags().String("provider", "tapd", "Source provider: tapd or gongfeng")
	issueSourceFetchCmd.Flags().String("fetch-provider", "", "Fetch mechanism, defaults to <provider>_mcp")
	issueSourceFetchCmd.Flags().String("status", "", "Fetch status: fetched or fetch_failed (required)")
	issueSourceFetchCmd.Flags().String("url", "", "Fetched source URL")
	issueSourceFetchCmd.Flags().String("source-workspace-id", "", "External source workspace ID")
	issueSourceFetchCmd.Flags().String("resource-type", "", "External source resource type")
	issueSourceFetchCmd.Flags().String("resource-id", "", "External source resource ID")
	issueSourceFetchCmd.Flags().String("title", "", "Fetched source title")
	issueSourceFetchCmd.Flags().String("summary", "", "Short fetched source summary")
	issueSourceFetchCmd.Flags().String("body-excerpt", "", "Short excerpt from the fetched source body or markdown")
	issueSourceFetchCmd.Flags().String("version", "", "External source version, revision, or modified timestamp")
	issueSourceFetchCmd.Flags().String("error", "", "Fetch failure reason, required for fetch_failed")
	issueSourceFetchCmd.Flags().Int64("duration-ms", 0, "Fetch duration in milliseconds")
	issueSourceFetchCmd.Flags().Bool("auto-fetch", false, "Fetch the source through the server-side account credential profile before recording evidence")
	issueSourceFetchCmd.Flags().String("output", "json", "Output format: table or json")

	// issue create
	issueCreateCmd.Flags().String("title", "", "Issue title (required)")
	issueCreateCmd.Flags().String("description", "", "Issue description (decodes \\n, \\r, \\t, \\\\; pipe via --description-stdin to preserve literal backslashes)")
	issueCreateCmd.Flags().Bool("description-stdin", false, "Read issue description from stdin (preserves multi-line content verbatim)")
	issueCreateCmd.Flags().String("description-file", "", "Read issue description from a UTF-8 file (preserves multi-line content verbatim; use this on Windows when stdin piping mangles non-ASCII bytes)")
	issueCreateCmd.Flags().String("status", "", "Issue status")
	issueCreateCmd.Flags().String("priority", "", "Issue priority")
	issueCreateCmd.Flags().String("assignee", "", "Assignee name (member, agent, or squad; fuzzy match)")
	issueCreateCmd.Flags().String("assignee-id", "", "Assignee UUID — member, agent, or squad (mutually exclusive with --assignee)")
	issueCreateCmd.Flags().String("parent", "", "Parent issue ID")
	issueCreateCmd.Flags().String("project", "", "Project ID")
	issueCreateCmd.Flags().String("start-date", "", "Start date (calendar day, YYYY-MM-DD)")
	issueCreateCmd.Flags().String("due-date", "", "Due date (calendar day, YYYY-MM-DD)")
	issueCreateCmd.Flags().String("output", "json", "Output format: table or json")
	issueCreateCmd.Flags().StringSlice("attachment", nil, "File path(s) to attach (can be specified multiple times)")
	issueCreateCmd.Flags().StringSlice("attachment-id", nil, "Existing attachment UUID(s) to bind to the created issue (can be specified multiple times)")

	// issue update
	issueUpdateCmd.Flags().String("title", "", "New title")
	issueUpdateCmd.Flags().String("description", "", "New description (decodes \\n, \\r, \\t, \\\\; pipe via --description-stdin to preserve literal backslashes)")
	issueUpdateCmd.Flags().Bool("description-stdin", false, "Read new description from stdin (preserves multi-line content verbatim)")
	issueUpdateCmd.Flags().String("description-file", "", "Read new description from a UTF-8 file (preserves multi-line content verbatim; use this on Windows when stdin piping mangles non-ASCII bytes)")
	issueUpdateCmd.Flags().String("status", "", "New status")
	issueUpdateCmd.Flags().String("priority", "", "New priority")
	issueUpdateCmd.Flags().String("assignee", "", "New assignee name (member, agent, or squad; fuzzy match)")
	issueUpdateCmd.Flags().String("assignee-id", "", "New assignee UUID — member, agent, or squad (mutually exclusive with --assignee)")
	issueUpdateCmd.Flags().String("project", "", "Project ID")
	issueUpdateCmd.Flags().String("start-date", "", "New start date (calendar day, YYYY-MM-DD; pass empty string to clear)")
	issueUpdateCmd.Flags().String("due-date", "", "New due date (calendar day, YYYY-MM-DD)")
	issueUpdateCmd.Flags().String("parent", "", "Parent issue ID (use --parent \"\" to clear)")
	issueUpdateCmd.Flags().String("output", "json", "Output format: table or json")

	// issue status
	issueStatusCmd.Flags().String("output", "table", "Output format: table or json")

	// issue assign
	issueAssignCmd.Flags().String("to", "", "Assignee name (member, agent, or squad; fuzzy match)")
	issueAssignCmd.Flags().String("to-id", "", "Assignee UUID — member, agent, or squad (mutually exclusive with --to)")
	issueAssignCmd.Flags().Bool("unassign", false, "Remove current assignee")
	issueAssignCmd.Flags().String("output", "json", "Output format: table or json")

	// issue comment list
	issueCommentListCmd.Flags().String("output", "table", "Output format: table or json")
	issueCommentListCmd.Flags().String("since", "", "Only return comments created after this timestamp (RFC3339)")
	issueCommentListCmd.Flags().String("thread", "", "Comment UUID — return the thread containing this comment (root + every descendant). May be a root or a reply id.")
	issueCommentListCmd.Flags().Int("tail", 0, "Only valid with --thread. Cap reply count to the N most recent replies; the thread root is always included (even with --tail 0). Use --before/--before-id to scroll to older replies.")
	issueCommentListCmd.Flags().Int("recent", 0, "Return the N most recently active threads (root + descendants per thread). Use --before/--before-id from the previous response to scroll to older threads.")
	issueCommentListCmd.Flags().Bool("roots-only", false, "Only return top-level comments (parent_id is null). Each root also carries reply_count + last_activity_at so you can triage which thread to open.")
	issueCommentListCmd.Flags().Bool("summary", false, "Clip each comment's content to a short preview (sets content_truncated) so you can scan a list without pulling full bodies. Composes with any mode.")
	issueCommentListCmd.Flags().String("before", "", "Cursor (RFC3339Nano timestamp). With --recent: thread cursor (last_activity_at). With --thread + --tail: reply cursor (reply created_at). Read from the X-Multica-Next-Before response header; must be paired with --before-id.")
	issueCommentListCmd.Flags().String("before-id", "", "Cursor UUID. With --recent: thread root UUID. With --thread + --tail: oldest reply UUID. Read from the X-Multica-Next-Before-Id response header; must be paired with --before.")

	// issue runs
	issueRunsCmd.Flags().String("output", "table", "Output format: table or json")
	issueRunsCmd.Flags().Bool("full-id", false, "Show full task UUIDs in table output")

	// issue rerun
	issueRerunCmd.Flags().String("output", "json", "Output format: table or json")
	// issue cancel-task
	issueCancelTaskCmd.Flags().String("output", "json", "Output format: table or json")
	issueCancelTaskCmd.Flags().String("issue", "", "Issue ID/key to scope short task ID prefix resolution")
	// issue run-messages
	issueRunMessagesCmd.Flags().String("output", "json", "Output format: table or json")
	issueRunMessagesCmd.Flags().Int("since", 0, "Only return messages after this sequence number")
	issueRunMessagesCmd.Flags().String("issue", "", "Issue ID/key to scope short task ID prefix resolution")

	// issue comment add
	issueCommentAddCmd.Flags().Bool("content-stdin", false, "Read comment content from stdin (preserves multi-line content verbatim)")
	issueCommentAddCmd.Flags().String("content-file", "", "Read comment content from a UTF-8 file (preserves multi-line content verbatim; use this on Windows when stdin piping mangles non-ASCII bytes)")
	issueCommentAddCmd.Flags().String("parent", "", "Parent comment ID (reply to a specific comment)")
	issueCommentAddCmd.Flags().StringSlice("attachment", nil, "File path(s) to attach (can be specified multiple times)")
	issueCommentAddCmd.Flags().String("output", "json", "Output format: table or json")

	// issue search
	issueSearchCmd.Flags().Int("limit", 20, "Maximum number of results to return")
	issueSearchCmd.Flags().Bool("include-closed", false, "Include done and cancelled issues")
	issueSearchCmd.Flags().String("output", "table", "Output format: table or json")

	// issue subscriber list
	issueSubscriberListCmd.Flags().String("output", "table", "Output format: table or json")

	// issue subscriber add
	issueSubscriberAddCmd.Flags().String("user", "", "Member or agent name to subscribe (fuzzy match; defaults to the caller)")
	issueSubscriberAddCmd.Flags().String("user-id", "", "Member or agent UUID to subscribe (mutually exclusive with --user)")
	issueSubscriberAddCmd.Flags().String("output", "json", "Output format: table or json")

	// issue subscriber remove
	issueSubscriberRemoveCmd.Flags().String("user", "", "Member or agent name to unsubscribe (fuzzy match; defaults to the caller)")
	issueSubscriberRemoveCmd.Flags().String("user-id", "", "Member or agent UUID to unsubscribe (mutually exclusive with --user)")
	issueSubscriberRemoveCmd.Flags().String("output", "json", "Output format: table or json")
}

// ---------------------------------------------------------------------------
// Issue commands
// ---------------------------------------------------------------------------

func runIssueList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	if client.WorkspaceID == "" {
		if _, err := requireWorkspaceID(cmd); err != nil {
			return err
		}
	}

	params := url.Values{}
	params.Set("workspace_id", client.WorkspaceID)
	if v, _ := cmd.Flags().GetString("status"); v != "" {
		params.Set("status", v)
	}
	if v, _ := cmd.Flags().GetString("priority"); v != "" {
		params.Set("priority", v)
	}
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		params.Set("limit", fmt.Sprintf("%d", v))
	}
	_, aID, hasAssignee, resolveErr := pickAssigneeFromFlags(ctx, client, cmd, "assignee", "assignee-id", issueAssigneeKinds)
	if resolveErr != nil {
		return fmt.Errorf("resolve assignee: %w", resolveErr)
	}
	if hasAssignee {
		params.Set("assignee_id", aID)
	}
	if v, _ := cmd.Flags().GetInt("offset"); v > 0 {
		params.Set("offset", fmt.Sprintf("%d", v))
	}
	if v, _ := cmd.Flags().GetString("project"); v != "" {
		project, err := resolveProjectID(ctx, client, v)
		if err != nil {
			return err
		}
		params.Set("project_id", project.ID)
	}
	if mdFlags, _ := cmd.Flags().GetStringSlice("metadata"); len(mdFlags) > 0 {
		filter, err := buildMetadataFilterQueryParam(mdFlags)
		if err != nil {
			return err
		}
		params.Set("metadata", filter)
	}

	path := "/api/issues"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var result map[string]any
	if err := client.GetJSON(ctx, path, &result); err != nil {
		return fmt.Errorf("list issues: %w", err)
	}

	issuesRaw, _ := result["issues"].([]any)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		total, _ := result["total"].(float64)
		limit, _ := cmd.Flags().GetInt("limit")
		offset, _ := cmd.Flags().GetInt("offset")
		hasMore := offset+len(issuesRaw) < int(total)
		wrapped := map[string]any{
			"issues":   issuesRaw,
			"total":    int(total),
			"limit":    limit,
			"offset":   offset,
			"has_more": hasMore,
		}
		return cli.PrintJSON(os.Stdout, wrapped)
	}

	fullID, _ := cmd.Flags().GetBool("full-id")
	headers := []string{"KEY", "TITLE", "STATUS", "PRIORITY", "ASSIGNEE", "START DATE", "DUE DATE"}
	if fullID {
		headers = []string{"KEY", "ID", "TITLE", "STATUS", "PRIORITY", "ASSIGNEE", "START DATE", "DUE DATE"}
	}
	actors := loadActorDisplayLookup(ctx, client)
	rows := make([][]string, 0, len(issuesRaw))
	for _, raw := range issuesRaw {
		issue, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		assignee := formatAssignee(issue, actors)
		startDate := strVal(issue, "start_date")
		if startDate != "" && len(startDate) >= 10 {
			startDate = startDate[:10]
		}
		dueDate := strVal(issue, "due_date")
		if dueDate != "" && len(dueDate) >= 10 {
			dueDate = dueDate[:10]
		}
		row := []string{
			issueDisplayKey(issue),
			strVal(issue, "title"),
			strVal(issue, "status"),
			strVal(issue, "priority"),
			assignee,
			startDate,
			dueDate,
		}
		if fullID {
			row = []string{
				issueDisplayKey(issue),
				strVal(issue, "id"),
				strVal(issue, "title"),
				strVal(issue, "status"),
				strVal(issue, "priority"),
				assignee,
				startDate,
				dueDate,
			}
		}
		rows = append(rows, row)
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runIssuePullRequests(cmd *cobra.Command, args []string) error {
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
	if err := client.GetJSON(ctx, "/api/issues/"+url.PathEscape(issueRef.ID)+"/pull-requests", &result); err != nil {
		return fmt.Errorf("list issue pull requests: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	prs, _ := result["pull_requests"].([]any)
	printIssuePullRequestsTable(normalizePullRequestList(prs))
	return nil
}

func normalizePullRequestList(raw []any) []map[string]any {
	prs := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		pr, ok := item.(map[string]any)
		if !ok {
			continue
		}
		prs = append(prs, pr)
	}
	return prs
}

func printIssuePullRequestsTable(prs []map[string]any) {
	headers := []string{"NUMBER", "STATE", "TITLE", "URL"}
	rows := make([][]string, 0, len(prs))
	for _, pr := range prs {
		rows = append(rows, []string{
			strVal(pr, "number"),
			strVal(pr, "state"),
			strVal(pr, "title"),
			pullRequestURL(pr),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
}

func pullRequestURL(pr map[string]any) string {
	if url := strVal(pr, "url"); url != "" {
		return url
	}
	return strVal(pr, "html_url")
}

func printIssueMutationResult(cmd *cobra.Command, result map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		headers := []string{"KEY", "TITLE", "STATUS", "PRIORITY"}
		rows := [][]string{{
			issueDisplayKey(result),
			strVal(result, "title"),
			strVal(result, "status"),
			strVal(result, "priority"),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}

	return cli.PrintJSON(os.Stdout, result)
}

func newIssueClientAndRef(cmd *cobra.Command, issueArg string) (*cli.APIClient, context.Context, context.CancelFunc, resolvedID, error) {
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

func runIssueGet(cmd *cobra.Command, args []string) error {
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

	var issue map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+issueRef.ID, &issue); err != nil {
		return fmt.Errorf("get issue: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		actors := loadActorDisplayLookup(ctx, client)
		assignee := formatAssignee(issue, actors)
		startDate := strVal(issue, "start_date")
		if startDate != "" && len(startDate) >= 10 {
			startDate = startDate[:10]
		}
		dueDate := strVal(issue, "due_date")
		if dueDate != "" && len(dueDate) >= 10 {
			dueDate = dueDate[:10]
		}
		headers := []string{"KEY", "TITLE", "STATUS", "PRIORITY", "ASSIGNEE", "START DATE", "DUE DATE", "DESCRIPTION"}
		rows := [][]string{{
			issueDisplayKey(issue),
			strVal(issue, "title"),
			strVal(issue, "status"),
			strVal(issue, "priority"),
			assignee,
			startDate,
			dueDate,
			strVal(issue, "description"),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}

	return cli.PrintJSON(os.Stdout, issue)
}

func runIssueChildren(cmd *cobra.Command, args []string) error {
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

	var resp struct {
		Issues []map[string]any `json:"issues"`
	}
	if err := client.GetJSON(ctx, "/api/issues/"+url.PathEscape(issueRef.ID)+"/children", &resp); err != nil {
		return fmt.Errorf("list child issues: %w", err)
	}
	children := resp.Issues

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{
			"parent_issue_id": issueRef.ID,
			"children":        children,
			"total":           len(children),
		})
	}

	actors := loadActorDisplayLookup(ctx, client)
	headers := []string{"KEY", "TITLE", "STATUS", "PROJECT", "ASSIGNEE"}
	rows := make([][]string, 0, len(children))
	for _, child := range children {
		rows = append(rows, []string{
			issueDisplayKey(child),
			strVal(child, "title"),
			strVal(child, "status"),
			strVal(child, "project_title"),
			formatAssignee(child, actors),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// isHTTPURL reports whether path is an http:// or https:// URL.
// Used to skip URL-shaped values passed to --attachment, which only
// accepts local file paths. Trims surrounding whitespace because
// agent-generated commands sometimes copy URLs with stray spaces.
func isHTTPURL(path string) bool {
	p := strings.TrimSpace(path)
	return strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://")
}

func appendUniqueStrings(dst []string, values ...string) []string {
	seen := make(map[string]struct{}, len(dst)+len(values))
	out := make([]string, 0, len(dst)+len(values))
	for _, v := range append(dst, values...) {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func quickCreateAttachmentIDsFromEnv() ([]string, error) {
	raw := strings.TrimSpace(os.Getenv("MULTICA_QUICK_CREATE_ATTACHMENT_IDS"))
	if raw == "" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("parse MULTICA_QUICK_CREATE_ATTACHMENT_IDS: %w", err)
	}
	return appendUniqueStrings(nil, ids...), nil
}

func runIssueCreate(cmd *cobra.Command, _ []string) error {
	title, _ := cmd.Flags().GetString("title")
	if title == "" {
		return fmt.Errorf("--title is required")
	}
	statusFlag, _ := cmd.Flags().GetString("status")
	if statusFlag != "" {
		if err := validateIssueStatus(statusFlag); err != nil {
			return err
		}
	}
	priorityFlag, _ := cmd.Flags().GetString("priority")
	if priorityFlag != "" {
		if err := validateIssuePriority(priorityFlag); err != nil {
			return err
		}
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	// Use a longer timeout when attachments are present (file uploads can be slow).
	timeout := cli.APITimeout()
	attachments, _ := cmd.Flags().GetStringSlice("attachment")
	if len(attachments) > 0 {
		timeout = cli.AtLeastAPITimeout(60 * time.Second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	body := map[string]any{"title": title}
	desc, hasDesc, err := resolveTextFlag(cmd, "description")
	if err != nil {
		return err
	}
	if hasDesc {
		body["description"] = desc
	}
	if statusFlag != "" {
		body["status"] = statusFlag
	}
	if priorityFlag != "" {
		body["priority"] = priorityFlag
	}
	if v, _ := cmd.Flags().GetString("parent"); v != "" {
		parent, err := resolveIssueRef(ctx, client, v)
		if err != nil {
			return fmt.Errorf("resolve parent issue: %w", err)
		}
		body["parent_issue_id"] = parent.ID
	}
	if v, _ := cmd.Flags().GetString("project"); v != "" {
		project, err := resolveProjectID(ctx, client, v)
		if err != nil {
			return fmt.Errorf("resolve project: %w", err)
		}
		body["project_id"] = project.ID
	}
	if v, _ := cmd.Flags().GetString("start-date"); v != "" {
		body["start_date"] = v
	}
	if v, _ := cmd.Flags().GetString("due-date"); v != "" {
		body["due_date"] = v
	}
	aType, aID, hasAssignee, resolveErr := pickAssigneeFromFlags(ctx, client, cmd, "assignee", "assignee-id", issueAssigneeKinds)
	if resolveErr != nil {
		return fmt.Errorf("resolve assignee: %w", resolveErr)
	}
	if hasAssignee {
		body["assignee_type"] = aType
		body["assignee_id"] = aID
	}

	// Quick-create stamp: when the daemon sets MULTICA_QUICK_CREATE_TASK_ID
	// before invoking the agent, the agent's `multica issue create` call
	// inherits the env var and tags the new issue with origin_type=
	// quick_create + origin_id=<task_id>. The completion handler then
	// locates the issue deterministically by origin instead of "most
	// recent issue by this agent", which is racy when max_concurrent_tasks
	// > 1 and the agent is creating other issues in parallel.
	if taskID := os.Getenv("MULTICA_QUICK_CREATE_TASK_ID"); taskID != "" {
		body["origin_type"] = "quick_create"
		body["origin_id"] = taskID
	}
	attachmentIDs, _ := cmd.Flags().GetStringSlice("attachment-id")
	envAttachmentIDs, err := quickCreateAttachmentIDsFromEnv()
	if err != nil {
		return err
	}
	attachmentIDs = appendUniqueStrings(attachmentIDs, envAttachmentIDs...)
	if len(attachmentIDs) > 0 {
		body["attachment_ids"] = attachmentIDs
	}

	// Pre-validate attachments BEFORE creating the issue so a bad path
	// can never produce a half-created issue (which would otherwise
	// trigger callers — especially the agent doing quick-create — to
	// retry the whole `issue create` and end up with duplicates).
	//
	//   - http(s) URLs are not local files; the API only accepts local
	//     paths here. Warn and skip rather than fail — a markdown image
	//     URL embedded in the prompt should never be re-attached, and
	//     skipping is the safest outcome for that case.
	//   - Anything else is treated as a local path and read upfront.
	//     A read failure here is a real user/agent mistake (typo,
	//     missing file) and we surface it pre-create so the issue
	//     never lands.
	type pendingAttachment struct {
		path string
		data []byte
	}
	pending := make([]pendingAttachment, 0, len(attachments))
	for _, filePath := range attachments {
		if isHTTPURL(filePath) {
			fmt.Fprintf(os.Stderr, "Skipping --attachment %q: URLs are not supported here, only local file paths.\n", filePath)
			continue
		}
		data, readErr := os.ReadFile(filePath)
		if readErr != nil {
			return fmt.Errorf("read attachment %s: %w", filePath, readErr)
		}
		pending = append(pending, pendingAttachment{path: filePath, data: data})
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues", body, &result); err != nil {
		return fmt.Errorf("create issue: %w", err)
	}

	// Upload attachments and link them to the newly created issue.
	// Failures here are partial-success: the issue exists already, so
	// turning a non-zero exit on the caller would encourage a retry that
	// duplicates the issue. Warn on stderr and continue.
	issueID := strVal(result, "id")
	for _, att := range pending {
		if _, uploadErr := client.UploadFile(ctx, att.data, att.path, issueID); uploadErr != nil {
			fmt.Fprintf(os.Stderr, "warning: upload attachment %s failed (issue already created, %s): %v\n",
				att.path, strVal(result, "identifier"), uploadErr)
			continue
		}
		fmt.Fprintf(os.Stderr, "Uploaded %s\n", att.path)
	}

	return printIssueMutationResult(cmd, result)
}

func runIssueUpdate(cmd *cobra.Command, args []string) error {
	statusChanged := cmd.Flags().Changed("status")
	statusFlag, _ := cmd.Flags().GetString("status")
	if statusChanged {
		if err := validateIssueStatus(statusFlag); err != nil {
			return err
		}
	}
	priorityChanged := cmd.Flags().Changed("priority")
	priorityFlag, _ := cmd.Flags().GetString("priority")
	if priorityChanged {
		if err := validateIssuePriority(priorityFlag); err != nil {
			return err
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

	body := map[string]any{}
	if cmd.Flags().Changed("title") {
		v, _ := cmd.Flags().GetString("title")
		body["title"] = v
	}
	if cmd.Flags().Changed("description") || cmd.Flags().Changed("description-stdin") || cmd.Flags().Changed("description-file") {
		desc, _, err := resolveTextFlag(cmd, "description")
		if err != nil {
			return err
		}
		body["description"] = desc
	}
	if statusChanged {
		body["status"] = statusFlag
	}
	if priorityChanged {
		body["priority"] = priorityFlag
	}
	if cmd.Flags().Changed("project") {
		v, _ := cmd.Flags().GetString("project")
		if v == "" {
			body["project_id"] = nil
		} else {
			project, err := resolveProjectID(ctx, client, v)
			if err != nil {
				return fmt.Errorf("resolve project: %w", err)
			}
			body["project_id"] = project.ID
		}
	}
	if cmd.Flags().Changed("start-date") {
		v, _ := cmd.Flags().GetString("start-date")
		body["start_date"] = v
	}
	if cmd.Flags().Changed("due-date") {
		v, _ := cmd.Flags().GetString("due-date")
		body["due_date"] = v
	}
	if cmd.Flags().Changed("assignee") || cmd.Flags().Changed("assignee-id") {
		aType, aID, hasAssignee, resolveErr := pickAssigneeFromFlags(ctx, client, cmd, "assignee", "assignee-id", issueAssigneeKinds)
		if resolveErr != nil {
			return fmt.Errorf("resolve assignee: %w", resolveErr)
		}
		if hasAssignee {
			body["assignee_type"] = aType
			body["assignee_id"] = aID
		}
	}
	if cmd.Flags().Changed("parent") {
		v, _ := cmd.Flags().GetString("parent")
		if v == "" {
			body["parent_issue_id"] = nil
		} else {
			parent, err := resolveIssueRef(ctx, client, v)
			if err != nil {
				return fmt.Errorf("resolve parent issue: %w", err)
			}
			body["parent_issue_id"] = parent.ID
		}
	}

	if len(body) == 0 {
		return fmt.Errorf("no fields to update; use flags like --title, --status, --priority, --assignee, etc.")
	}

	var result map[string]any
	if err := client.PutJSON(ctx, "/api/issues/"+issueRef.ID, body, &result); err != nil {
		return fmt.Errorf("update issue: %w", err)
	}

	return printIssueMutationResult(cmd, result)
}

func runIssueAssign(cmd *cobra.Command, args []string) error {
	toName, _ := cmd.Flags().GetString("to")
	unassign, _ := cmd.Flags().GetBool("unassign")
	toNameSet := cmd.Flags().Changed("to")
	toIDSet := cmd.Flags().Changed("to-id")

	if !toNameSet && !toIDSet && !unassign {
		return fmt.Errorf("provide --to <name>, --to-id <uuid>, or --unassign")
	}
	if (toNameSet || toIDSet) && unassign {
		return fmt.Errorf("--to/--to-id and --unassign are mutually exclusive")
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

	body := map[string]any{}
	displayTarget := toName
	if unassign {
		body["assignee_type"] = nil
		body["assignee_id"] = nil
	} else {
		aType, aID, _, resolveErr := pickAssigneeFromFlags(ctx, client, cmd, "to", "to-id", issueAssigneeKinds)
		if resolveErr != nil {
			return fmt.Errorf("resolve assignee: %w", resolveErr)
		}
		body["assignee_type"] = aType
		body["assignee_id"] = aID
		if displayTarget == "" {
			displayTarget = loadActorDisplayLookup(ctx, client).actor(aType, aID)
		}
	}

	var result map[string]any
	if err := client.PutJSON(ctx, "/api/issues/"+issueRef.ID, body, &result); err != nil {
		return fmt.Errorf("assign issue: %w", err)
	}

	if unassign {
		fmt.Fprintf(os.Stderr, "Issue %s unassigned.\n", issueDisplayKey(result))
	} else {
		fmt.Fprintf(os.Stderr, "Issue %s assigned to %s.\n", issueDisplayKey(result), displayTarget)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

func runIssueStatus(cmd *cobra.Command, args []string) error {
	id := args[0]
	status := args[1]

	if err := validateIssueStatus(status); err != nil {
		return err
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, id)
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	body := map[string]any{"status": status}
	var result map[string]any
	if err := client.PutJSON(ctx, "/api/issues/"+issueRef.ID, body, &result); err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Issue %s status changed to %s.\n", issueDisplayKey(result), status)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Comment commands
// ---------------------------------------------------------------------------

