package service

// Squad SOP (Standard Operating Procedure) runtime + helpers.
// Split out of task.go — pure code move, no logic change.
//
// Covers three sub-themes:
//   - squadSOPProfile parsing + step matching (parseSquadSOPProfileSteps,
//     matchSquadSOPStepForAgent*, nextSquadSOPStateForTaskEvent),
//   - the sync/close/block side of a SOP run driven by task lifecycle events
//     (syncSquadSOPTaskStep*, closeIssueAfterCompletedSOPRun,
//     blockIssueAfterBlockedSOPRun, ...),
//   - the gongfeng MR gate that decides whether an issue is allowed to
//     transition to review (issueRequiresGongfengMR + recordMissingMRGateComment).
//
// Cross-references: sibling files task.go / task_analytics.go call into
// helpers here (taskFailureSummary, squadSOPFinalOutputBlocked, etc.) — same
// package, so visibility is unchanged.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/prompteval"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

type squadSOPProfileStep struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	RoleKey string `json:"role_key"`
}

type squadSOPProfile struct {
	Steps []squadSOPProfileStep `json:"steps"`
}

func (s *TaskService) syncSquadSOPTaskStep(ctx context.Context, task db.AgentTaskQueue, eventType, eventStatus string) {
	s.syncSquadSOPTaskStepWithResult(ctx, task, eventType, eventStatus, nil)
}

func (s *TaskService) syncSquadSOPTaskStepWithResult(ctx context.Context, task db.AgentTaskQueue, eventType, eventStatus string, result []byte) {
	if !task.IssueID.Valid {
		return
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil || !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		return
	}
	run, err := s.Queries.GetOpenSquadSOPRunByIssue(ctx, task.IssueID)
	if err != nil {
		return
	}
	agent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          task.AgentID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("sync squad SOP task step skipped: agent not found",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"agent_id", util.UUIDToString(task.AgentID),
			"error", err,
		)
		return
	}
	steps := parseSquadSOPProfileSteps(run.Profile)
	step, index, ok := matchSquadSOPStepForAgentRecord(steps, agent)
	if !ok {
		return
	}
	events, err := s.Queries.ListSquadSOPStepEventsByRun(ctx, run.ID)
	if err != nil {
		slog.Warn("sync squad SOP task step skipped: list existing events failed",
			"run_id", util.UUIDToString(run.ID),
			"task_id", util.UUIDToString(task.ID),
			"error", err,
		)
		return
	}
	for _, existing := range events {
		if existing.TaskID.Valid && existing.TaskID == task.ID && existing.EventType == eventType {
			return
		}
	}

	var duration pgtype.Int8
	if eventType != "步骤开始" && task.StartedAt.Valid && task.CompletedAt.Valid {
		duration = pgtype.Int8{Int64: task.CompletedAt.Time.Sub(task.StartedAt.Time).Milliseconds(), Valid: true}
	}
	reason := "Agent task 状态自动同步到 SOP 阶段。"
	switch eventType {
	case "步骤开始":
		reason = "Agent task 已开始，自动记录 SOP 阶段开始。"
	case "步骤完成":
		reason = "Agent task 已完成，自动记录 SOP 阶段完成。"
	case "步骤失败":
		reason = "Agent task 已失败，自动记录 SOP 阶段失败。"
	}
	if _, err := s.Queries.CreateSquadSOPStepEvent(ctx, db.CreateSquadSOPStepEventParams{
		RunID:         run.ID,
		WorkspaceID:   run.WorkspaceID,
		IssueID:       run.IssueID,
		SquadID:       run.SquadID,
		StepKey:       step.Key,
		StepName:      step.Name,
		RoleKey:       step.RoleKey,
		EventType:     eventType,
		Status:        eventStatus,
		Reason:        reason,
		DurationMs:    duration,
		CreatedByType: "system",
		TaskID:        task.ID,
	}); err != nil {
		slog.Warn("sync squad SOP task step event failed",
			"run_id", util.UUIDToString(run.ID),
			"task_id", util.UUIDToString(task.ID),
			"step_key", step.Key,
			"event_type", eventType,
			"error", err,
		)
		return
	}

	nextStatus, nextStepKey, shouldUpdate := nextSquadSOPStateForTaskEvent(issue, steps, index, step.Key, eventType)
	if !shouldUpdate {
		return
	}
	finalStepBlocked := eventType == "步骤完成" && nextStatus == "已完成" && squadSOPFinalOutputBlocked(result)
	if finalStepBlocked {
		nextStatus = "已阻塞"
	}
	updatedRun, err := s.Queries.UpdateSquadSOPRunStatus(ctx, db.UpdateSquadSOPRunStatusParams{
		ID:             run.ID,
		WorkspaceID:    run.WorkspaceID,
		Status:         nextStatus,
		CurrentStepKey: pgtype.Text{String: nextStepKey, Valid: nextStepKey != ""},
	})
	if err != nil {
		slog.Warn("sync squad SOP run status failed",
			"run_id", util.UUIDToString(run.ID),
			"task_id", util.UUIDToString(task.ID),
			"step_key", step.Key,
			"event_type", eventType,
			"error", err,
		)
		return
	}
	if eventType == "步骤完成" && updatedRun.Status == "已完成" {
		s.closeIssueAfterCompletedSOPRun(ctx, issue)
	}
	if eventType == "步骤完成" && updatedRun.Status == "已阻塞" {
		s.blockIssueAfterBlockedSOPRun(ctx, issue)
	}
	if eventType == "步骤失败" && updatedRun.Status == "已失败" {
		s.blockIssueAfterBlockedSOPRun(ctx, issue)
	}
}

func (s *TaskService) squadSOPFailureComment(ctx context.Context, task db.AgentTaskQueue, errMsg, failureReason string) (string, bool) {
	if !task.IssueID.Valid {
		return "", false
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil || !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" {
		return "", false
	}
	run, err := s.Queries.GetOpenSquadSOPRunByIssue(ctx, task.IssueID)
	if err != nil {
		return "", false
	}
	agent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          task.AgentID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return "", false
	}
	steps := parseSquadSOPProfileSteps(run.Profile)
	step, _, ok := matchSquadSOPStepForAgentRecord(steps, agent)
	if !ok {
		return "", false
	}
	reason := strings.TrimSpace(failureReason)
	if reason == "" {
		reason = taskfailure.Classify(errMsg).String()
	}
	var b strings.Builder
	b.WriteString("## 阶段执行失败\n\n")
	fmt.Fprintf(&b, "- 阶段：%s\n", step.Name)
	fmt.Fprintf(&b, "- Agent：%s\n", agent.Name)
	fmt.Fprintf(&b, "- Task：%s\n", util.UUIDToString(task.ID))
	fmt.Fprintf(&b, "- 失败类型：%s\n", reason)
	b.WriteString("- 处理结果：SOP 运行已标记为失败，当前 issue 已阻塞，等待 PM 或人工确认后重试。\n")
	if summary := taskFailureSummary(errMsg); summary != "" {
		fmt.Fprintf(&b, "- 错误摘要：%s\n", summary)
	}
	return b.String(), true
}

func (s *TaskService) squadSOPTaskHasDeliveryComment(ctx context.Context, task db.AgentTaskQueue) bool {
	if !task.IssueID.Valid {
		return false
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil || !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" {
		return false
	}
	run, ok := s.squadSOPRunForWorkerTask(ctx, task, issue)
	if !ok {
		return false
	}
	agent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          task.AgentID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return false
	}
	if _, _, ok := matchSquadSOPStepForAgentRecord(parseSquadSOPProfileSteps(run.Profile), agent); !ok {
		return false
	}
	comments, err := s.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       2000,
	})
	if err != nil {
		slog.Warn("squad SOP delivery comment lookup failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"error", err,
		)
		return false
	}
	for _, comment := range comments {
		if comment.AuthorType == "agent" &&
			comment.SourceTaskID.Valid &&
			comment.SourceTaskID == task.ID &&
			comment.Type != "system" &&
			strings.TrimSpace(comment.Content) != "" {
			return true
		}
	}
	return false
}

func taskFailureSummary(errMsg string) string {
	errMsg = strings.TrimSpace(redact.Text(errMsg))
	if errMsg == "" {
		return ""
	}
	errMsg = strings.Join(strings.Fields(errMsg), " ")
	if len([]rune(errMsg)) > 240 {
		return "原始错误较长，已保留在任务运行记录中。"
	}
	return errMsg
}

func (s *TaskService) closeIssueAfterCompletedSOPRun(ctx context.Context, issue db.Issue) {
	switch issue.Status {
	case "done", "cancelled", "blocked":
		return
	}
	pullRequests, err := s.Queries.ListPullRequestsByIssue(ctx, issue.ID)
	if err != nil {
		slog.Warn("auto-close issue after completed SOP skipped: pull request lookup failed",
			"issue_id", util.UUIDToString(issue.ID),
			"error", err,
		)
		return
	}
	if s.issueRequiresGongfengMR(ctx, issue) {
		if len(pullRequests) > 0 {
			if _, err := s.updateIssueStatusAfterCompletedSOPRun(ctx, issue, "done"); err != nil {
				slog.Warn("auto-close issue after completed SOP with linked MR failed",
					"issue_id", util.UUIDToString(issue.ID),
					"error", err,
				)
				return
			}
			slog.Info("issue auto-closed after completed SOP run with linked MR", "issue_id", util.UUIDToString(issue.ID))
			return
		}
		if _, err := s.updateIssueStatusAfterCompletedSOPRun(ctx, issue, "blocked"); err != nil {
			slog.Warn("block issue after completed SOP without MR failed",
				"issue_id", util.UUIDToString(issue.ID),
				"error", err,
			)
			return
		}
		s.recordMissingMRGateComment(ctx, issue)
		slog.Info("issue blocked after completed SOP run without linked MR", "issue_id", util.UUIDToString(issue.ID))
		return
	}
	if len(pullRequests) > 0 {
		return
	}
	if _, err := s.updateIssueStatusAfterCompletedSOPRun(ctx, issue, "done"); err != nil {
		slog.Warn("auto-close issue after completed SOP failed",
			"issue_id", util.UUIDToString(issue.ID),
			"error", err,
		)
		return
	}
	slog.Info("issue auto-closed after completed SOP run", "issue_id", util.UUIDToString(issue.ID))
}

func (s *TaskService) blockIssueAfterBlockedSOPRun(ctx context.Context, issue db.Issue) {
	switch issue.Status {
	case "done", "cancelled", "blocked":
		return
	}
	if _, err := s.updateIssueStatusAfterCompletedSOPRun(ctx, issue, "blocked"); err != nil {
		slog.Warn("block issue after blocked SOP run failed",
			"issue_id", util.UUIDToString(issue.ID),
			"error", err,
		)
		return
	}
	slog.Info("issue blocked after blocked SOP run", "issue_id", util.UUIDToString(issue.ID))
}

func (s *TaskService) updateIssueStatusAfterCompletedSOPRun(ctx context.Context, issue db.Issue, status string) (db.Issue, error) {
	if status == "done" {
		incomplete, err := s.countIncompleteChildIssues(ctx, issue)
		if err != nil {
			return db.Issue{}, err
		}
		if incomplete > 0 {
			return db.Issue{}, fmt.Errorf("%w: %d incomplete child issue(s)", errIssueDoneBlockedByChildren, incomplete)
		}
	}
	updated, err := s.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID:          issue.ID,
		Status:      status,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return db.Issue{}, err
	}
	s.broadcastIssueUpdated(updated)
	if s.IssueStatusChanged != nil {
		s.IssueStatusChanged(ctx, issue, updated, "system", "")
	}
	return updated, nil
}

func (s *TaskService) countIncompleteChildIssues(ctx context.Context, issue db.Issue) (int, error) {
	children, err := s.Queries.ListChildIssues(ctx, issue.ID)
	if err != nil {
		return 0, err
	}
	incomplete := 0
	for _, child := range children {
		if child.Status != "done" {
			incomplete++
		}
	}
	return incomplete, nil
}

func (s *TaskService) autoStartIssueForTask(ctx context.Context, task db.AgentTaskQueue) {
	if !shouldAutoStartIssueForTask(task) {
		return
	}
	s.autoTransitionIssueStatus(ctx, task, "todo", "in_progress", "task_started")
}

func (s *TaskService) autoReviewIssueForTask(ctx context.Context, task db.AgentTaskQueue) {
	if !shouldConsiderAutoReviewIssueForTask(task) {
		return
	}
	issue, ok := s.issueForTaskStatusAutomation(ctx, task)
	if !ok || !shouldAutoReviewIssueForTask(task, issue) || issue.Status != "in_progress" {
		return
	}
	s.updateIssueStatusForTaskAutomation(ctx, task, issue, "in_review", "task_completed")
}

func (s *TaskService) autoBlockIssueForTaskFailure(ctx context.Context, task db.AgentTaskQueue) {
	if !shouldAutoBlockIssueForTaskFailure(task) {
		return
	}
	hasActive, err := s.Queries.HasActiveTaskForIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("task failure issue status automation skipped: active task check failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"error", err,
		)
		return
	}
	if hasActive {
		return
	}
	s.autoTransitionIssueStatus(ctx, task, "in_progress", "blocked", "task_failed")
}

func (s *TaskService) autoTransitionIssueStatus(ctx context.Context, task db.AgentTaskQueue, fromStatus, toStatus, reason string) {
	issue, ok := s.issueForTaskStatusAutomation(ctx, task)
	if !ok || issue.Status != fromStatus {
		return
	}
	s.updateIssueStatusForTaskAutomation(ctx, task, issue, toStatus, reason)
}

func (s *TaskService) issueForTaskStatusAutomation(ctx context.Context, task db.AgentTaskQueue) (db.Issue, bool) {
	if !task.IssueID.Valid {
		return db.Issue{}, false
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("task issue status automation skipped: issue lookup failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"error", err,
		)
		return db.Issue{}, false
	}
	return issue, true
}

func (s *TaskService) updateIssueStatusForTaskAutomation(ctx context.Context, task db.AgentTaskQueue, issue db.Issue, status string, reason string) {
	updated, err := s.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID:          issue.ID,
		Status:      status,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("task issue status automation failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(issue.ID),
			"from_status", issue.Status,
			"to_status", status,
			"reason", reason,
			"error", err,
		)
		return
	}
	slog.Info("task issue status automated",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(issue.ID),
		"from_status", issue.Status,
		"to_status", status,
		"reason", reason,
	)
	s.broadcastIssueUpdated(updated)
	if s.IssueStatusChanged != nil {
		s.IssueStatusChanged(ctx, issue, updated, "system", "")
	}
}

func shouldAutoStartIssueForTask(task db.AgentTaskQueue) bool {
	return isAssignmentIssueTaskForStatusAutomation(task)
}

func shouldConsiderAutoReviewIssueForTask(task db.AgentTaskQueue) bool {
	return isAssignmentIssueTaskForStatusAutomation(task) && !task.IsLeaderTask
}

func shouldAutoReviewIssueForTask(task db.AgentTaskQueue, issue db.Issue) bool {
	if !shouldConsiderAutoReviewIssueForTask(task) {
		return false
	}
	return issue.AssigneeType.Valid &&
		issue.AssigneeType.String == "agent" &&
		issue.AssigneeID.Valid &&
		issue.AssigneeID == task.AgentID
}

func shouldAutoBlockIssueForTaskFailure(task db.AgentTaskQueue) bool {
	return isAssignmentIssueTaskForStatusAutomation(task)
}

func isAssignmentIssueTaskForStatusAutomation(task db.AgentTaskQueue) bool {
	if !task.IssueID.Valid ||
		task.TriggerCommentID.Valid ||
		task.ChatSessionID.Valid ||
		task.AutopilotRunID.Valid {
		return false
	}
	if _, ok := ParseIssueSourceSummaryContext(task); ok {
		return false
	}
	return true
}

func (s *TaskService) issueRequiresGongfengMR(ctx context.Context, issue db.Issue) bool {
	if !issue.ProjectID.Valid {
		return false
	}
	resources, err := s.Queries.ListProjectResources(ctx, issue.ProjectID)
	if err != nil {
		slog.Warn("detect issue gongfeng MR requirement failed",
			"issue_id", util.UUIDToString(issue.ID),
			"project_id", util.UUIDToString(issue.ProjectID),
			"error", err,
		)
		return false
	}
	for _, resource := range resources {
		if resource.ResourceType == "gongfeng_repo" {
			return true
		}
	}
	return false
}

func (s *TaskService) recordMissingMRGateComment(ctx context.Context, issue db.Issue) {
	content := strings.TrimSpace(`05 验证已完成，但平台还没有关联 MR。请通过平台创建并关联 MR 后再进入人工 CodeReview：

multica issue mr create <issue-id> --provider gongfeng --project-path <project-path> --source-branch <branch> --target-branch <target-branch> --title "<title>" --output json

创建成功后，平台会把 MR 写入任务的关联 MR 区域。`)
	comment, err := s.Queries.CreateComment(ctx, db.CreateCommentParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    util.MustParseUUID("00000000-0000-0000-0000-000000000000"),
		Content:     content,
		Type:        "comment",
	})
	if err != nil {
		slog.Warn("create missing MR gate comment failed",
			"issue_id", util.UUIDToString(issue.ID),
			"error", err,
		)
		return
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventCommentCreated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload: map[string]any{
			"comment": map[string]any{
				"id":          util.UUIDToString(comment.ID),
				"issue_id":    util.UUIDToString(comment.IssueID),
				"author_type": comment.AuthorType,
				"author_id":   util.UUIDToString(comment.AuthorID),
				"content":     comment.Content,
				"type":        comment.Type,
				"created_at":  comment.CreatedAt.Time.Format("2006-01-02T15:04:05Z"),
			},
			"issue_title":  issue.Title,
			"issue_status": "blocked",
		},
	})
}

func parseSquadSOPProfileSteps(raw []byte) []squadSOPProfileStep {
	var profile squadSOPProfile
	if len(raw) == 0 || json.Unmarshal(raw, &profile) != nil {
		return nil
	}
	return profile.Steps
}

func matchSquadSOPStepForAgentRecord(steps []squadSOPProfileStep, agent db.Agent) (squadSOPProfileStep, int, bool) {
	if roleKey := roleKeyFromAgentRuntimeConfig(agent.RuntimeConfig); roleKey != "" {
		if step, index, ok := matchSquadSOPStepForAgent(steps, roleKey); ok {
			return step, index, true
		}
	}
	return matchSquadSOPStepForAgent(steps, agent.Name)
}

func roleKeyFromAgentRuntimeConfig(raw []byte) string {
	var runtimeConfig map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &runtimeConfig) != nil {
		return ""
	}
	if scope, ok := runtimeConfig["internal_squad"].(map[string]any); ok {
		return prompteval.StringFromAny(scope["role_key"])
	}
	return ""
}

func matchSquadSOPStepForAgent(steps []squadSOPProfileStep, agentNameOrRoleKey string) (squadSOPProfileStep, int, bool) {
	agentKey := normalizeSOPMatchKey(agentNameOrRoleKey)
	if agentKey == "" {
		return squadSOPProfileStep{}, -1, false
	}
	for i, step := range steps {
		if agentKey == normalizeSOPMatchKey(step.RoleKey) ||
			agentKey == normalizeSOPMatchKey(step.Key) ||
			agentKey == normalizeSOPMatchKey(step.Name) {
			return step, i, true
		}
	}
	return squadSOPProfileStep{}, -1, false
}

func normalizeSOPMatchKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	return strings.Trim(value, "-")
}

func nextSquadSOPStateForTaskEvent(issue db.Issue, steps []squadSOPProfileStep, stepIndex int, stepKey string, eventType string) (status, currentStepKey string, ok bool) {
	switch eventType {
	case "步骤开始":
		return "进行中", stepKey, true
	case "步骤失败":
		return "已失败", stepKey, true
	case "步骤完成":
		if issue.Status == "done" {
			return "已完成", stepKey, true
		}
		if len(steps) > 0 && stepIndex >= 0 && stepIndex < len(steps)-1 {
			return "进行中", steps[stepIndex+1].Key, true
		}
		if len(steps) == 0 || stepIndex == len(steps)-1 {
			return "已完成", stepKey, true
		}
	}
	return "", "", false
}

var squadSOPBlockedOutputPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?im)(?:最终判定|总体结论|验证结论|V[123][^\n|]*)[^\n|]*(?:BLOCKED|FAILED|FAIL|未通过|阻塞)`),
	regexp.MustCompile(`(?im)(?:结论|结果|判定)\s*[：:]\s*(?:❌\s*)?(?:BLOCKED|FAILED|FAIL|未通过|阻塞)`),
	regexp.MustCompile(`(?im)(?:\|\s*V[123][^|\n]*\|\s*[^|\n]*\|\s*)?(?:❌\s*)?(?:BLOCKED|FAILED|FAIL|未通过)\s*\|`),
}

func squadSOPFinalOutputBlocked(result []byte) bool {
	body := taskResultOutputText(result)
	if body == "" {
		return false
	}
	for _, pattern := range squadSOPBlockedOutputPatterns {
		if pattern.MatchString(body) {
			return true
		}
	}
	return false
}

func taskResultOutputText(result []byte) string {
	var payload protocol.TaskCompletedPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(util.UnescapeBackslashEscapes(payload.Output))
}
