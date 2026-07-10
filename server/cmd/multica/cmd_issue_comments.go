package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

func runIssueCommentList(cmd *cobra.Command, args []string) error {
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

	since, _ := cmd.Flags().GetString("since")
	thread, _ := cmd.Flags().GetString("thread")
	recent, _ := cmd.Flags().GetInt("recent")
	tail, _ := cmd.Flags().GetInt("tail")
	rootsOnly, _ := cmd.Flags().GetBool("roots-only")
	summary, _ := cmd.Flags().GetBool("summary")
	// Flags().Changed distinguishes "user did not pass --recent" from
	// "user explicitly passed --recent 0" (or a negative value). The
	// GetInt zero-value collapses both cases, which would otherwise
	// cause us to silently drop an invalid value and fall back to the
	// default unparameterized list — exactly the drift Elon flagged in
	// the PR #2787 second review. --tail follows the same pattern, and
	// also keeps "--tail 0" (root-only) distinguishable from "no --tail".
	recentSet := cmd.Flags().Changed("recent")
	tailSet := cmd.Flags().Changed("tail")
	before, _ := cmd.Flags().GetString("before")
	beforeID, _ := cmd.Flags().GetString("before-id")

	// Mirror the server-side combination rules client-side so the user gets
	// a clear local error instead of a 400 round-trip. These match the
	// validation in handler.ListComments (server/internal/handler/comment.go).
	if recentSet && recent <= 0 {
		return fmt.Errorf("--recent must be a positive integer")
	}
	if tailSet && tail < 0 {
		return fmt.Errorf("--tail must be a non-negative integer (0 returns just the thread root)")
	}
	if thread != "" && recentSet {
		return fmt.Errorf("--thread and --recent are mutually exclusive")
	}
	if rootsOnly && thread != "" {
		return fmt.Errorf("--roots-only and --thread are mutually exclusive")
	}
	if rootsOnly && recentSet {
		return fmt.Errorf("--roots-only and --recent are mutually exclusive")
	}
	if rootsOnly && tailSet {
		return fmt.Errorf("--roots-only and --tail are mutually exclusive")
	}
	if rootsOnly && before != "" {
		return fmt.Errorf("--roots-only does not support --before / --before-id")
	}
	if tailSet && thread == "" {
		return fmt.Errorf("--tail requires --thread (it is a thread-scoped limit)")
	}
	if (before == "") != (beforeID == "") {
		return fmt.Errorf("--before and --before-id must be set together (composite cursor for stable pagination)")
	}
	if before != "" && !recentSet && !(thread != "" && tailSet) {
		return fmt.Errorf("--before / --before-id require --recent (thread cursor) or --thread + --tail (reply cursor)")
	}

	params := url.Values{}
	if since != "" {
		params.Set("since", since)
	}
	if rootsOnly {
		params.Set("roots_only", "true")
	}
	if summary {
		params.Set("summary", "true")
	}
	if thread != "" {
		params.Set("thread", thread)
	}
	if tailSet {
		params.Set("tail", fmt.Sprintf("%d", tail))
	}
	if recentSet {
		params.Set("recent", fmt.Sprintf("%d", recent))
	}
	if before != "" {
		params.Set("before", before)
		params.Set("before_id", beforeID)
	}

	path := "/api/issues/" + issueRef.ID + "/comments"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var comments []map[string]any
	respHeaders, err := client.GetJSONWithHeaders(ctx, path, &comments)
	if err != nil {
		return fmt.Errorf("list comments: %w", err)
	}
	// The server emits the next-page cursor in headers when there is likely
	// an older page. Surface it on stderr so an operator (and the agent
	// prompt update that follows this PR) can scroll deeper without having
	// to dig into the raw HTTP response. Label depends on which paging mode
	// the caller is in — under --recent the cursor is a thread cursor;
	// under --thread + --tail it is a reply cursor inside that thread.
	if nb := respHeaders.Get("X-Multica-Next-Before"); nb != "" {
		if nbid := respHeaders.Get("X-Multica-Next-Before-Id"); nbid != "" {
			label := "Next thread cursor"
			if thread != "" && tailSet {
				label = "Next reply cursor"
			}
			fmt.Fprintf(os.Stderr, "%s: --before %s --before-id %s\n", label, nb, nbid)
		}
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, comments)
	}

	actors := loadActorDisplayLookup(ctx, client)
	headers := []string{"ID", "PARENT", "AUTHOR", "TYPE", "CONTENT", "CREATED"}
	rows := make([][]string, 0, len(comments))
	for _, c := range comments {
		content := strVal(c, "content")
		if utf8.RuneCountInString(content) > 80 {
			runes := []rune(content)
			content = string(runes[:77]) + "..."
		}
		created := strVal(c, "created_at")
		if len(created) >= 16 {
			created = created[:16]
		}
		parentID := strVal(c, "parent_id")
		if parentID == "" {
			parentID = "—"
		}
		rows = append(rows, []string{
			strVal(c, "id"),
			parentID,
			actors.actor(strVal(c, "author_type"), strVal(c, "author_id")),
			strVal(c, "type"),
			content,
			created,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runIssueCommentAdd(cmd *cobra.Command, args []string) error {
	content, hasContent, err := resolveFileOrStdinTextFlag(cmd, "content")
	if err != nil {
		return err
	}
	if !hasContent {
		return fmt.Errorf("--content-stdin or --content-file is required")
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

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}
	issueID := issueRef.ID

	// Upload attachments and collect their IDs. URLs are skipped with a
	// warning — `--attachment` only accepts local file paths, and a
	// markdown image URL embedded in agent-supplied content should never
	// be re-uploaded as if it were a file. Unlike `issue create`, this
	// path uploads BEFORE posting the comment, so a hard failure on a
	// real (local) attachment correctly aborts the whole call.
	var attachmentIDs []string
	for _, filePath := range attachments {
		if isHTTPURL(filePath) {
			fmt.Fprintf(os.Stderr, "Skipping --attachment %q: URLs are not supported here, only local file paths.\n", filePath)
			continue
		}
		data, readErr := os.ReadFile(filePath)
		if readErr != nil {
			return fmt.Errorf("read attachment %s: %w", filePath, readErr)
		}
		id, uploadErr := client.UploadFile(ctx, data, filePath, issueID)
		if uploadErr != nil {
			return fmt.Errorf("upload attachment %s: %w", filePath, uploadErr)
		}
		attachmentIDs = append(attachmentIDs, id)
		fmt.Fprintf(os.Stderr, "Uploaded %s\n", filePath)
	}

	body := map[string]any{"content": content}
	if parentID, _ := cmd.Flags().GetString("parent"); parentID != "" {
		body["parent_id"] = parentID
	}
	if len(attachmentIDs) > 0 {
		body["attachment_ids"] = attachmentIDs
	}
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+issueID+"/comments", body, &result); err != nil {
		return fmt.Errorf("add comment: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Comment added to issue %s.\n", issueRef.Display)

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

func runIssueCommentDelete(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	if err := client.DeleteJSON(ctx, "/api/comments/"+args[0]); err != nil {
		return fmt.Errorf("delete comment: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Comment %s deleted.\n", args[0])
	return nil
}

// ---------------------------------------------------------------------------
// Execution history commands
// ---------------------------------------------------------------------------

func runIssueRuns(cmd *cobra.Command, args []string) error {
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

	var runs []map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+issueRef.ID+"/task-runs", &runs); err != nil {
		return fmt.Errorf("list runs: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, runs)
	}

	actors := loadActorDisplayLookup(ctx, client)
	fullID, _ := cmd.Flags().GetBool("full-id")
	headers := []string{"ID", "AGENT", "STATUS", "STARTED", "COMPLETED", "ERROR"}
	rows := make([][]string, 0, len(runs))
	for _, r := range runs {
		started := strVal(r, "started_at")
		if len(started) >= 16 {
			started = started[:16]
		}
		completed := strVal(r, "completed_at")
		if len(completed) >= 16 {
			completed = completed[:16]
		}
		errMsg := strVal(r, "error")
		if utf8.RuneCountInString(errMsg) > 50 {
			runes := []rune(errMsg)
			errMsg = string(runes[:47]) + "..."
		}
		rows = append(rows, []string{
			displayID(strVal(r, "id"), fullID),
			actors.agent(strVal(r, "agent_id")),
			strVal(r, "status"),
			started,
			completed,
			errMsg,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runIssueRunMessages(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueID := ""
	if issueInput, _ := cmd.Flags().GetString("issue"); issueInput != "" {
		issueRef, err := resolveIssueRef(ctx, client, issueInput)
		if err != nil {
			return fmt.Errorf("resolve issue: %w", err)
		}
		issueID = issueRef.ID
	}
	taskRef, err := resolveTaskRunID(ctx, client, issueID, args[0])
	if err != nil {
		return fmt.Errorf("resolve task run: %w", err)
	}

	path := "/api/tasks/" + url.PathEscape(taskRef.ID) + "/messages"
	if since, _ := cmd.Flags().GetInt("since"); since > 0 {
		path += fmt.Sprintf("?since=%d", since)
	}

	var messages []map[string]any
	if err := client.GetJSON(ctx, path, &messages); err != nil {
		return fmt.Errorf("list run messages: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, messages)
	}

	headers := []string{"SEQ", "TYPE", "TOOL", "CONTENT"}
	rows := make([][]string, 0, len(messages))
	for _, m := range messages {
		content := strVal(m, "content")
		if content == "" {
			content = strVal(m, "output")
		}
		if utf8.RuneCountInString(content) > 80 {
			runes := []rune(content)
			content = string(runes[:77]) + "..."
		}
		seq := ""
		if v, ok := m["seq"]; ok {
			seq = fmt.Sprintf("%v", v)
		}
		rows = append(rows, []string{
			seq,
			strVal(m, "type"),
			strVal(m, "tool"),
			content,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// ---------------------------------------------------------------------------
// Search command
// ---------------------------------------------------------------------------

func runIssueRerun(cmd *cobra.Command, args []string) error {
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

	var task map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+issueRef.ID+"/rerun", map[string]any{}, &task); err != nil {
		return fmt.Errorf("rerun issue: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, task)
	}
	agent := loadActorDisplayLookup(ctx, client).agent(strVal(task, "agent_id"))
	fmt.Fprintf(os.Stdout, "Re-enqueued task %s on agent %s\n", strVal(task, "id"), agent)
	return nil
}

// runIssueCancelTask cancels a single task by ID. It accepts the short ID
// prefix shown by `issue runs` (resolved through resolveTaskRunID), and uses
// /api/tasks/{taskId}/cancel which both updates the DB row to status=cancelled
// and triggers the daemon-side interrupt path (#2107) so an in-flight agent
// stops emitting tool calls promptly instead of running until its own timeout.
func runIssueCancelTask(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueScope := ""
	if issueInput, _ := cmd.Flags().GetString("issue"); issueInput != "" {
		issueRef, err := resolveIssueRef(ctx, client, issueInput)
		if err != nil {
			return fmt.Errorf("resolve issue: %w", err)
		}
		issueScope = issueRef.ID
	}
	taskRef, err := resolveTaskRunID(ctx, client, issueScope, args[0])
	if err != nil {
		return fmt.Errorf("resolve task run: %w", err)
	}

	var result map[string]any
	path := "/api/tasks/" + url.PathEscape(taskRef.ID) + "/cancel"
	if err := client.PostJSON(ctx, path, map[string]any{}, &result); err != nil {
		return fmt.Errorf("cancel task: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	status := strVal(result, "status")
	if status == "" {
		status = "cancelled"
	}
	fmt.Fprintf(os.Stdout, "Task %s -> status=%s\n", taskRef.ID, status)
	return nil
}

func runIssueSearch(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	params := url.Values{}
	params.Set("q", args[0])
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		params.Set("limit", fmt.Sprintf("%d", v))
	}
	if v, _ := cmd.Flags().GetBool("include-closed"); v {
		params.Set("include_closed", "true")
	}

	path := "/api/issues/search?" + params.Encode()

	var result map[string]any
	if err := client.GetJSON(ctx, path, &result); err != nil {
		return fmt.Errorf("search issues: %w", err)
	}

	issuesRaw, _ := result["issues"].([]any)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	headers := []string{"KEY", "TITLE", "STATUS", "MATCH"}
	rows := make([][]string, 0, len(issuesRaw))
	for _, raw := range issuesRaw {
		issue, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		matchInfo := strVal(issue, "match_source")
		if snippet := strVal(issue, "matched_snippet"); snippet != "" {
			if utf8.RuneCountInString(snippet) > 50 {
				runes := []rune(snippet)
				snippet = string(runes[:47]) + "..."
			}
			matchInfo += ": " + snippet
		}
		rows = append(rows, []string{
			strVal(issue, "identifier"),
			strVal(issue, "title"),
			strVal(issue, "status"),
			matchInfo,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// ---------------------------------------------------------------------------
// Subscriber commands
// ---------------------------------------------------------------------------

func runIssueSubscriberList(cmd *cobra.Command, args []string) error {
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

	var subscribers []map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+issueRef.ID+"/subscribers", &subscribers); err != nil {
		return fmt.Errorf("list subscribers: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, subscribers)
	}

	actors := loadActorDisplayLookup(ctx, client)
	headers := []string{"USER", "REASON", "CREATED"}
	rows := make([][]string, 0, len(subscribers))
	for _, s := range subscribers {
		created := strVal(s, "created_at")
		if len(created) >= 16 {
			created = created[:16]
		}
		rows = append(rows, []string{
			actors.actor(strVal(s, "user_type"), strVal(s, "user_id")),
			strVal(s, "reason"),
			created,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runIssueSubscriberAdd(cmd *cobra.Command, args []string) error {
	return runIssueSubscriberMutation(cmd, args[0], "subscribe")
}

func runIssueSubscriberRemove(cmd *cobra.Command, args []string) error {
	return runIssueSubscriberMutation(cmd, args[0], "unsubscribe")
}

// runIssueSubscriberMutation shares subscribe/unsubscribe logic — both endpoints
// take the same request body and only differ in the path.
func runIssueSubscriberMutation(cmd *cobra.Command, issueID, action string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, issueID)
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	body := map[string]any{}
	userName, _ := cmd.Flags().GetString("user")
	uType, uID, hasUser, resolveErr := pickAssigneeFromFlags(ctx, client, cmd, "user", "user-id", memberOrAgentKinds)
	if resolveErr != nil {
		return fmt.Errorf("resolve user: %w", resolveErr)
	}
	if hasUser {
		body["user_type"] = uType
		body["user_id"] = uID
	}

	var result map[string]any
	path := "/api/issues/" + issueRef.ID + "/" + action
	if err := client.PostJSON(ctx, path, body, &result); err != nil {
		return fmt.Errorf("%s issue: %w", action, err)
	}

	target := "caller"
	if userName != "" {
		target = userName
	} else if hasUser {
		target = loadActorDisplayLookup(ctx, client).actor(uType, uID)
	}
	if action == "subscribe" {
		fmt.Fprintf(os.Stderr, "Subscribed %s to issue %s.\n", target, issueRef.Display)
	} else {
		fmt.Fprintf(os.Stderr, "Unsubscribed %s from issue %s.\n", target, issueRef.Display)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type assigneeMatch struct {
	Type string // "member", "agent", or "squad"
	ID   string // user_id for members, agent id for agents, squad id for squads
	Name string
}

// assigneeKinds is the set of entity types a given flag is allowed to resolve
// to. Issue assignees accept all three (`issueAssigneeKinds`), while
// project lead and issue subscribers are member-or-agent only
// (`memberOrAgentKinds`) — the DB CHECK on `project.lead_type` and the
// `isWorkspaceEntity` switch in the subscriber handler both reject `squad`,
// so resolving to (squad, ...) for those callers would surface as a 500 /
// 403 instead of a clean CLI-side resolution error (MUL-2165 follow-up).
type assigneeKinds struct {
	member, agent, squad bool
}

var (
	issueAssigneeKinds = assigneeKinds{member: true, agent: true, squad: true}
	memberOrAgentKinds = assigneeKinds{member: true, agent: true}
)

func (k assigneeKinds) describe() string {
	parts := make([]string, 0, 3)
	if k.member {
		parts = append(parts, "member")
	}
	if k.agent {
		parts = append(parts, "agent")
	}
	if k.squad {
		parts = append(parts, "squad")
	}
	switch len(parts) {
	case 0:
		return "<none>"
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " or " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + ", or " + parts[len(parts)-1]
	}
}

