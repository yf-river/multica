package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

func (s *TaskService) CompleteTask(ctx context.Context, taskID pgtype.UUID, result []byte, sessionID, workDir string) (*db.AgentTaskQueue, error) {
	var task db.AgentTaskQueue
	var completedEvent events.Event
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		t, err := qtx.CompleteAgentTask(ctx, db.CompleteAgentTaskParams{
			ID:        taskID,
			Result:    result,
			SessionID: pgtype.Text{String: sessionID, Valid: sessionID != ""},
			WorkDir:   pgtype.Text{String: workDir, Valid: workDir != ""},
		})
		if err != nil {
			return err
		}
		task = t

		if t.ChatSessionID.Valid {
			// Pin the chat_session's runtime_id alongside the session_id so the
			// next claim can apply the runtime-guard. Both fields move together:
			// when there's no session_id to record, leave runtime_id untouched
			// (NULL → COALESCE keeps the existing value).
			var sessionRuntimeID pgtype.UUID
			if sessionID != "" {
				sessionRuntimeID = t.RuntimeID
			}
			// COALESCE in SQL guarantees empty inputs don't wipe the
			// existing resume pointer; we still surface DB errors.
			if err := qtx.UpdateChatSessionSession(ctx, db.UpdateChatSessionSessionParams{
				ID:        t.ChatSessionID,
				SessionID: pgtype.Text{String: sessionID, Valid: sessionID != ""},
				WorkDir:   pgtype.Text{String: workDir, Valid: workDir != ""},
				RuntimeID: sessionRuntimeID,
			}); err != nil {
				return fmt.Errorf("update chat session resume pointer: %w", err)
			}
		}
		completedEvent, err = s.enqueueTaskEvent(ctx, qtx, protocol.EventTaskCompleted, task)
		return err
	}); err != nil {
		// When parallel agents race, a task may already be completed,
		// cancelled, or failed by the time this call runs. The UPDATE
		// … WHERE status = 'running' returns no rows in that case.
		// Treat it as an idempotent success — same pattern as CancelTask.
		if existing, lookupErr := s.Queries.GetAgentTask(ctx, taskID); lookupErr == nil {
			if errors.Is(err, pgx.ErrNoRows) {
				slog.Info("complete task: already finalized",
					"task_id", util.UUIDToString(taskID),
					"current_status", existing.Status,
					"agent_id", util.UUIDToString(existing.AgentID),
				)
				return &existing, nil
			}
			slog.Warn("complete task failed",
				"task_id", util.UUIDToString(taskID),
				"current_status", existing.Status,
				"issue_id", util.UUIDToString(existing.IssueID),
				"chat_session_id", util.UUIDToString(existing.ChatSessionID),
				"agent_id", util.UUIDToString(existing.AgentID),
				"error", err,
			)
		} else {
			slog.Warn("complete task failed: task not found",
				"task_id", util.UUIDToString(taskID),
				"lookup_error", lookupErr,
			)
		}
		return nil, fmt.Errorf("complete task: %w", err)
	}

	slog.Info("task completed", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID))
	s.captureTaskCompleted(ctx, task)
	if sc, ok := ParseIssueSourceSummaryContext(task); ok {
		s.completeIssueSourceSummaryTask(ctx, task, sc, result)
		s.ReconcileAgentStatus(ctx, task.AgentID)
		s.Bus.Publish(completedEvent)
		return &task, nil
	}
	s.linkGongfengMRsFromTaskComments(ctx, task)
	s.syncSquadSOPTaskStepWithResult(ctx, task, "步骤完成", "已完成", result)
	s.enqueueSquadLeaderAfterWorkerStageCompletion(ctx, task)
	s.autoReviewIssueForTask(ctx, task)

	// Invariant: every completed issue task must have at least one agent
	// comment on the issue, so the user always sees something when a run
	// ends. If the agent posted a comment during execution (result, progress
	// ping, or CLI reply), HasAgentCommentedSince returns true and we skip.
	// Otherwise, synthesize one from the final output. For comment-triggered
	// tasks, TriggerCommentID threads the fallback under the original comment;
	// for assignment-triggered tasks it is NULL and the fallback is top-level.
	// Chat tasks have no IssueID and are handled separately below.
	if task.IssueID.Valid {
		suppressNoActionComment, err := HasSquadLeaderNoActionEvaluationForTask(ctx, s.Queries, task)
		if err != nil {
			slog.Warn("checking squad leader no_action evaluation failed",
				"task_id", util.UUIDToString(task.ID),
				"issue_id", util.UUIDToString(task.IssueID),
				"agent_id", util.UUIDToString(task.AgentID),
				"error", err,
			)
		}
		agentCommented, _ := s.Queries.HasAgentCommentedSince(ctx, db.HasAgentCommentedSinceParams{
			IssueID:  task.IssueID,
			AuthorID: task.AgentID,
			Since:    task.StartedAt,
		})
		if !suppressNoActionComment && !agentCommented {
			var payload protocol.TaskCompletedPayload
			if err := json.Unmarshal(result, &payload); err == nil {
				if payload.Output != "" {
					// Match the CLI's --content / --description behavior: agents that
					// emit literal `\n` 4-char sequences (Python/JSON-style) get them
					// decoded into real newlines before the comment hits the DB. See
					// util.UnescapeBackslashEscapes for the exact contract.
					body := util.UnescapeBackslashEscapes(payload.Output)
					if !containsAgentMention(body) {
						if dispatchBody := s.fallbackDispatchCommentFromMessages(ctx, task.ID); dispatchBody != "" {
							body = dispatchBody
						}
					}
					if task.TriggerCommentID.Valid && isTrivialDoneOutput(body) {
						slog.Warn("suppressing trivial comment-trigger fallback output",
							"task_id", util.UUIDToString(task.ID),
							"issue_id", util.UUIDToString(task.IssueID),
							"agent_id", util.UUIDToString(task.AgentID),
						)
					} else {
						s.createAgentComment(ctx, task.IssueID, task.AgentID, redact.Text(body), "comment", task.TriggerCommentID, task.ID)
					}
				}
			}
		}
	}

	// For chat tasks, save assistant reply and broadcast chat:done. The
	// resume pointer was already persisted inside the transaction above.
	if task.ChatSessionID.Valid {
		var assistantMsg *db.ChatMessage
		var payload protocol.TaskCompletedPayload
		if err := json.Unmarshal(result, &payload); err == nil && payload.Output != "" {
			// Same unescape as the issue-comment path above: literal `\n` from
			// agent stdout becomes a real newline so the chat panel renders
			// paragraph breaks instead of one wall of prose.
			body := util.UnescapeBackslashEscapes(payload.Output)
			row, err := s.Queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
				ChatSessionID: task.ChatSessionID,
				Role:          "assistant",
				Content:       redact.Text(body),
				TaskID:        task.ID,
				ElapsedMs:     computeChatElapsedMs(task),
			})
			if err != nil {
				slog.Error("failed to save assistant chat message", "task_id", util.UUIDToString(task.ID), "error", err)
			} else {
				assistantMsg = &row
				// Event-driven unread: stamp unread_since on the first unread
				// assistant message. No-op if the session already has unread.
				// If the user is actively viewing the session, the frontend's
				// auto-mark-read effect will clear this within a tick.
				if err := s.Queries.SetUnreadSinceIfNull(ctx, task.ChatSessionID); err != nil {
					slog.Warn("failed to set unread_since", "chat_session_id", util.UUIDToString(task.ChatSessionID), "error", err)
				}
			}
		}
		s.broadcastChatDone(ctx, task, assistantMsg)
	}

	// Reconcile agent status
	s.ReconcileAgentStatus(ctx, task.AgentID)

	// Broadcast
	s.Bus.Publish(completedEvent)

	return &task, nil
}

func (s *TaskService) completeIssueSourceSummaryTask(ctx context.Context, task db.AgentTaskQueue, sc IssueSourceSummaryContext, result []byte) {
	description, ok := issueSourceSummaryDescriptionFromResult(result)
	status := "completed"
	errorMessage := ""
	if !ok {
		status = "failed"
		errorMessage = "摘要任务输出无效，已使用来源内容生成兜底摘要。"
		description = s.fallbackIssueSourceSummaryDescription(ctx, task.IssueID, errorMessage)
	}
	s.applyIssueSourceSummary(ctx, task, sc, description, status, errorMessage)
}

func (s *TaskService) failIssueSourceSummaryTask(ctx context.Context, task db.AgentTaskQueue, sc IssueSourceSummaryContext, errMsg string) {
	errorMessage := strings.TrimSpace(errMsg)
	if errorMessage == "" {
		errorMessage = "摘要任务执行失败，已使用来源内容生成兜底摘要。"
	}
	description := s.fallbackIssueSourceSummaryDescription(ctx, task.IssueID, errorMessage)
	s.applyIssueSourceSummary(ctx, task, sc, description, "failed", errorMessage)
}

func (s *TaskService) applyIssueSourceSummary(ctx context.Context, task db.AgentTaskQueue, sc IssueSourceSummaryContext, description, status, errorMessage string) {
	if strings.TrimSpace(description) == "" {
		return
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("source summary completion: issue not found",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"error", err,
		)
		return
	}
	updated, err := s.persistIssueUpdate(ctx, issue, taskIssueUpdateChanges{Description: true}, func(queries *db.Queries) (db.Issue, error) {
		updated, err := queries.UpdateIssue(ctx, db.UpdateIssueParams{
			ID:            issue.ID,
			Description:   pgtype.Text{String: redact.Text(description), Valid: true},
			AssigneeType:  issue.AssigneeType,
			AssigneeID:    issue.AssigneeID,
			StartDate:     issue.StartDate,
			DueDate:       issue.DueDate,
			ParentIssueID: issue.ParentIssueID,
			ProjectID:     issue.ProjectID,
		})
		if err != nil {
			return db.Issue{}, err
		}
		type metadataField struct {
			key   string
			value string
		}
		metadata := []metadataField{
			{key: "source_summary_status", value: status},
			{key: "source_summary_task_id", value: util.UUIDToString(task.ID)},
		}
		if strings.TrimSpace(errorMessage) != "" {
			metadata = append(metadata, metadataField{key: "source_summary_error", value: errorMessage})
		}
		if sc.Provider != "" {
			metadata = append(metadata, metadataField{key: "source_summary_provider", value: sc.Provider})
		}
		for _, field := range metadata {
			updated, err = queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
				ID:          issue.ID,
				WorkspaceID: issue.WorkspaceID,
				Key:         field.key,
				Value:       mustJSONStringBytes(field.value),
			})
			if err != nil {
				return db.Issue{}, fmt.Errorf("set %s: %w", field.key, err)
			}
		}
		return updated, nil
	})
	if err != nil {
		slog.Warn("source summary completion: persist issue update failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(issue.ID),
			"error", err,
		)
		return
	}
	s.enqueueIssueAfterSourceSummary(ctx, updated)
}

func issueSourceSummaryDescriptionFromResult(result []byte) (string, bool) {
	var payload protocol.TaskCompletedPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		return "", false
	}
	body := strings.TrimSpace(util.UnescapeBackslashEscapes(payload.Output))
	body = unwrapMarkdownFence(body)
	if body == "" || isTrivialDoneOutput(body) {
		return "", false
	}
	runes := []rune(body)
	if len(runes) > 5000 {
		body = string(runes[:5000])
	}
	if !strings.Contains(body, "## 需求摘要") {
		body = "## 需求摘要\n" + body
	}
	return strings.TrimSpace(body), true
}

func unwrapMarkdownFence(body string) string {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 3 || !strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		return trimmed
	}
	return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
}

func (s *TaskService) fallbackIssueSourceSummaryDescription(ctx context.Context, issueID pgtype.UUID, reason string) string {
	issue, err := s.Queries.GetIssue(ctx, issueID)
	if err != nil {
		return "## 需求摘要\n摘要生成失败，请查看 TAPD 来源卡片或重新触发摘要。\n"
	}
	title := strings.TrimSpace(issueMetadataString(issue.Metadata, "source_fetch_title"))
	body := strings.TrimSpace(firstNonEmpty(
		issueMetadataString(issue.Metadata, "source_fetch_summary"),
		issueMetadataString(issue.Metadata, "source_fetch_body_excerpt"),
	))
	if body == "" {
		body = strings.TrimSpace(issue.Description.String)
	}
	body = truncateForSummary(body, 900)
	var b strings.Builder
	b.WriteString("## 需求摘要\n")
	if title != "" {
		b.WriteString(title)
		if body != "" && body != title {
			b.WriteString("\n\n")
			b.WriteString(body)
		}
	} else if body != "" {
		b.WriteString(body)
	} else {
		b.WriteString("摘要生成失败，请查看 TAPD 来源卡片或重新触发摘要。")
	}
	if strings.TrimSpace(reason) != "" {
		b.WriteString("\n\n## 摘要状态\n")
		b.WriteString(reason)
	}
	b.WriteString("\n")
	return b.String()
}

func (s *TaskService) enqueueIssueAfterSourceSummary(ctx context.Context, issue db.Issue) {
	if issue.Status == "backlog" || !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return
	}
	switch issue.AssigneeType.String {
	case "agent":
		if _, err := s.EnqueueTaskForIssue(ctx, issue); err != nil {
			slog.Warn("source summary completion: enqueue assigned agent failed",
				"issue_id", util.UUIDToString(issue.ID),
				"agent_id", util.UUIDToString(issue.AssigneeID),
				"error", err,
			)
		}
	case "squad":
		squad, err := s.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          issue.AssigneeID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			slog.Warn("source summary completion: load squad failed",
				"issue_id", util.UUIDToString(issue.ID),
				"squad_id", util.UUIDToString(issue.AssigneeID),
				"error", err,
			)
			return
		}
		hasPending, err := s.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
			IssueID: issue.ID,
			AgentID: squad.LeaderID,
		})
		if err != nil || hasPending {
			return
		}
		if _, err := s.EnqueueTaskForSquadLeader(ctx, issue, squad.LeaderID, pgtype.UUID{}); err != nil {
			slog.Warn("source summary completion: enqueue squad leader failed",
				"issue_id", util.UUIDToString(issue.ID),
				"squad_id", util.UUIDToString(squad.ID),
				"leader_id", util.UUIDToString(squad.LeaderID),
				"error", err,
			)
		}
	}
}

var (
	gongfengMRURLRe     = regexp.MustCompile(`https://git\.code\.tencent\.com/([A-Za-z0-9_.~%+/\-]+?)/(?:-/)?merge_requests/([0-9]+)`)
	gongfengMRBranchRe  = regexp.MustCompile(`(?im)(?:源分支|source\s+branch|source_branch)\s*(?:[：:]|\|)\s*` + "`?" + `([A-Za-z0-9._/\-]+)` + "`?")
	gongfengMRTitleLine = regexp.MustCompile(`(?m)(?:MR\s*(?:已创建|created)?|merge\s+request)\s*[：:]\s*(.+)$`)
)

type gongfengMRCommentRef struct {
	ProjectPath  string
	Number       int32
	HTMLURL      string
	SourceBranch string
	Title        string
}

func (s *TaskService) linkGongfengMRsFromTaskComments(ctx context.Context, task db.AgentTaskQueue) {
	if !task.IssueID.Valid {
		return
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("task comment MR collection skipped: issue lookup failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"error", err,
		)
		return
	}
	comments, err := s.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       2000,
	})
	if err != nil {
		slog.Warn("task comment MR collection skipped: comments lookup failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"error", err,
		)
		return
	}
	refsByURL := map[string]gongfengMRCommentRef{}
	for _, comment := range comments {
		if !comment.SourceTaskID.Valid || comment.SourceTaskID != task.ID {
			continue
		}
		for _, ref := range parseGongfengMRRefsFromComment(comment.Content) {
			refsByURL[ref.HTMLURL] = ref
		}
	}
	for _, ref := range refsByURL {
		if err := s.linkGongfengMRCommentRef(ctx, issue, task, ref); err != nil {
			slog.Warn("task comment MR collection failed to link MR",
				"task_id", util.UUIDToString(task.ID),
				"issue_id", util.UUIDToString(issue.ID),
				"html_url", ref.HTMLURL,
				"error", err,
			)
		}
	}
}

func (s *TaskService) linkGongfengMRCommentRef(ctx context.Context, issue db.Issue, task db.AgentTaskQueue, ref gongfengMRCommentRef) error {
	repoOwner, repoName := splitGongfengProjectPath(ref.ProjectPath)
	if repoOwner == "" || repoName == "" || ref.Number <= 0 || ref.HTMLURL == "" {
		return nil
	}
	now := time.Now().UTC()
	title := strings.TrimSpace(ref.Title)
	if title == "" {
		title = fmt.Sprintf("MR !%d", ref.Number)
	}
	pr, err := s.Queries.UpsertGitHubPullRequest(ctx, db.UpsertGitHubPullRequestParams{
		WorkspaceID:         issue.WorkspaceID,
		InstallationID:      0,
		RepoOwner:           repoOwner,
		RepoName:            repoName,
		PrNumber:            ref.Number,
		Title:               title,
		State:               "open",
		HtmlUrl:             ref.HTMLURL,
		Branch:              pgtype.Text{String: ref.SourceBranch, Valid: ref.SourceBranch != ""},
		AuthorLogin:         pgtype.Text{},
		AuthorAvatarUrl:     pgtype.Text{},
		MergedAt:            pgtype.Timestamptz{},
		ClosedAt:            pgtype.Timestamptz{},
		PrCreatedAt:         pgtype.Timestamptz{Time: now, Valid: true},
		PrUpdatedAt:         pgtype.Timestamptz{Time: now, Valid: true},
		HeadSha:             "",
		MergeableState:      pgtype.Text{},
		ClearMergeableState: pgtype.Bool{},
		Additions:           0,
		Deletions:           0,
		ChangedFiles:        0,
	})
	if err != nil {
		return fmt.Errorf("upsert pull request: %w", err)
	}
	if err := s.Queries.LinkIssueToPullRequest(ctx, db.LinkIssueToPullRequestParams{
		IssueID:             issue.ID,
		PullRequestID:       pr.ID,
		CloseIntent:         false,
		LinkedByType:        pgtype.Text{String: "agent", Valid: true},
		LinkedByID:          task.AgentID,
		PreserveCloseIntent: true,
	}); err != nil {
		return fmt.Errorf("link issue pull request: %w", err)
	}
	slog.Info("linked Gongfeng MR reported by task comment",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(issue.ID),
		"mr_url", ref.HTMLURL,
		"mr_number", ref.Number,
	)
	return nil
}

func parseGongfengMRRefsFromComment(content string) []gongfengMRCommentRef {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	matches := gongfengMRURLRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	globalBranch := ""
	if len(matches) == 1 {
		if branchMatch := gongfengMRBranchRe.FindStringSubmatch(content); len(branchMatch) == 2 {
			globalBranch = strings.TrimSpace(branchMatch[1])
		}
	}
	lines := strings.Split(content, "\n")
	refs := make([]gongfengMRCommentRef, 0, len(matches))
	seen := map[string]struct{}{}
	for lineIdx, line := range lines {
		lineMatches := gongfengMRURLRe.FindAllStringSubmatch(line, -1)
		for _, match := range lineMatches {
			if len(match) != 3 {
				continue
			}
			number64, err := strconv.ParseInt(match[2], 10, 32)
			if err != nil || number64 <= 0 {
				continue
			}
			projectPath := strings.Trim(match[1], "/")
			htmlURL := canonicalGongfengMRURL(projectPath, int32(number64))
			if _, ok := seen[htmlURL]; ok {
				continue
			}
			seen[htmlURL] = struct{}{}
			number := int32(number64)
			branch := gongfengBranchNearMR(lines, lineIdx)
			if branch == "" {
				branch = globalBranch
			}
			refs = append(refs, gongfengMRCommentRef{
				ProjectPath:  projectPath,
				Number:       number,
				HTMLURL:      htmlURL,
				SourceBranch: branch,
				Title:        gongfengTitleNearMR(lines, lineIdx, number),
			})
		}
	}
	return refs
}

func canonicalGongfengMRURL(projectPath string, number int32) string {
	projectPath = strings.Trim(projectPath, "/")
	if projectPath == "" || number <= 0 {
		return ""
	}
	return fmt.Sprintf("https://git.code.tencent.com/%s/merge_requests/%d", projectPath, number)
}

func gongfengBranchNearMR(lines []string, lineIdx int) string {
	if branch := gongfengBranchFromLine(lines[lineIdx]); branch != "" {
		return branch
	}
	for i := lineIdx + 1; i < len(lines) && i <= lineIdx+8; i++ {
		if i != lineIdx+1 && strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			break
		}
		if branch := gongfengBranchFromLine(lines[i]); branch != "" {
			return branch
		}
	}
	for i := lineIdx - 1; i >= 0 && i >= lineIdx-4; i-- {
		if branch := gongfengBranchFromLine(lines[i]); branch != "" {
			return branch
		}
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			break
		}
	}
	return ""
}

func gongfengBranchFromLine(line string) string {
	if branchMatch := gongfengMRBranchRe.FindStringSubmatch(line); len(branchMatch) == 2 {
		return strings.Trim(strings.TrimSpace(branchMatch[1]), "`")
	}
	return ""
}

func gongfengTitleNearMR(lines []string, lineIdx int, number int32) string {
	marker := fmt.Sprintf("!%d", number)
	for i := lineIdx; i >= 0 && i >= lineIdx-8; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.Contains(line, marker) && strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "# "))
		}
	}
	return ""
}

func splitGongfengProjectPath(projectPath string) (string, string) {
	parts := strings.Split(strings.Trim(projectPath, "/"), "/")
	if len(parts) < 2 {
		return "", ""
	}
	return strings.Join(parts[:len(parts)-1], "/"), parts[len(parts)-1]
}

func (s *TaskService) enqueueSquadLeaderAfterWorkerStageCompletion(ctx context.Context, task db.AgentTaskQueue) {
	if !task.IssueID.Valid || task.IsLeaderTask {
		return
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil || !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		return
	}
	if issue.Status == "done" || issue.Status == "cancelled" {
		return
	}
	run, ok := s.squadSOPRunForWorkerTask(ctx, task, issue)
	if !ok {
		return
	}
	agent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          task.AgentID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return
	}
	if _, _, ok := matchSquadSOPStepForAgentRecord(parseSquadSOPProfileSteps(run.Profile), agent); !ok {
		return
	}
	squad, err := s.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return
	}
	leader, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          squad.LeaderID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || !leader.RuntimeID.Valid || leader.ArchivedAt.Valid {
		return
	}
	hasPending, err := s.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: issue.ID,
		AgentID: squad.LeaderID,
	})
	if err != nil || hasPending {
		return
	}
	nextTask, err := s.Queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		AgentID:        squad.LeaderID,
		RuntimeID:      leader.RuntimeID,
		IssueID:        issue.ID,
		Priority:       priorityToInt(issue.Priority),
		TriggerSummary: pgtype.Text{String: "SOP 阶段任务已完成，继续协调下一阶段。", Valid: true},
		IsLeaderTask:   pgtype.Bool{Bool: true, Valid: true},
	})
	if err != nil {
		slog.Warn("enqueue squad leader after worker stage completion failed",
			"issue_id", util.UUIDToString(issue.ID),
			"worker_task_id", util.UUIDToString(task.ID),
			"leader_id", util.UUIDToString(squad.LeaderID),
			"error", err,
		)
		return
	}
	slog.Info("squad leader enqueued after worker stage completion",
		"task_id", util.UUIDToString(nextTask.ID),
		"issue_id", util.UUIDToString(issue.ID),
		"worker_task_id", util.UUIDToString(task.ID),
		"leader_id", util.UUIDToString(squad.LeaderID),
	)
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, nextTask)
	s.NotifyTaskEnqueued(ctx, nextTask)
}

func (s *TaskService) squadSOPRunForWorkerTask(ctx context.Context, task db.AgentTaskQueue, issue db.Issue) (db.SquadSopRun, bool) {
	run, err := s.Queries.GetOpenSquadSOPRunByIssue(ctx, task.IssueID)
	if err == nil {
		return run, true
	}
	runs, err := s.Queries.ListIssueSquadSOPRuns(ctx, db.ListIssueSquadSOPRunsParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || len(runs) == 0 {
		return db.SquadSopRun{}, false
	}
	return runs[0], true
}

// FailTask marks a task as failed.
// For assignment-triggered issue tasks without an automatic retry, the
// platform blocks an in-progress issue instead of moving it back to todo.
//
// sessionID/workDir are optional: when the agent established a real session
// before failing (e.g. crashed mid-conversation, was cancelled, or hit a
// tool error), the daemon should pass them so we can preserve the resume
// pointer on both the task row and the chat_session — otherwise the next
// chat turn would silently start a brand-new session and lose memory.
//
// failureReason is a coarse classifier consumed by the auto-retry path.
// Pass "" when unknown — the server runs the raw error text through
// taskfailure.Classify so the persisted failure_reason still lands in
// the canonical refined taxonomy rather than the legacy "agent_error"
// coarse bucket. Daemon callers that already produced a refined reason
// (via classifyPoisonedError, the timeout / runtime classifier, etc.)
// will have their value preserved untouched.
