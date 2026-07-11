package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func isNoteComment(content string) bool {
	trimmed := strings.TrimLeft(content, " \t\r\n")
	firstToken := trimmed
	if i := strings.IndexFunc(trimmed, unicode.IsSpace); i >= 0 {
		firstToken = trimmed[:i]
	}
	return strings.EqualFold(firstToken, noteCommentPrefix)
}

func isNoActionOnlyComment(content string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(content))
	if trimmed == "" || len(util.ParseMentions(content)) > 0 {
		return false
	}
	return strings.Contains(trimmed, "no action") ||
		strings.Contains(trimmed, "无需行动") ||
		strings.Contains(trimmed, "不需要行动") ||
		strings.Contains(trimmed, "静默退出")
}

func (h *Handler) triggerTasksForComment(ctx context.Context, issue db.Issue, comment db.Comment, parentComment *db.Comment, actorType, actorID string, suppressAgentIDs []pgtype.UUID) {
	if isNoteComment(comment.Content) {
		return
	}
	opts := commentTriggerComputeOptions{
		SuppressAssignedSquadLeader: h.isSquadSOPWorkerStageComment(ctx, issue, comment),
	}
	triggers := h.computeCommentAgentTriggers(ctx, issue, comment.Content, parentComment, actorType, actorID, opts)
	triggers = filterSuppressedCommentAgentTriggers(triggers, suppressAgentIDs)
	h.enqueueCommentAgentTriggers(ctx, issue, comment.ID, triggers)
}

func (h *Handler) triggerTasksForAgentServiceComment(ctx context.Context, issue db.Issue, comment db.Comment, parentComment *db.Comment) {
	h.triggerTasksForComment(ctx, issue, comment, parentComment, comment.AuthorType, uuidToString(comment.AuthorID), nil)
}

func (h *Handler) isSquadSOPWorkerStageComment(ctx context.Context, issue db.Issue, comment db.Comment) bool {
	if !comment.SourceTaskID.Valid || comment.AuthorType != "agent" {
		return false
	}
	task, err := h.Queries.GetAgentTask(ctx, comment.SourceTaskID)
	if err != nil || !task.IssueID.Valid || uuidToString(task.IssueID) != uuidToString(issue.ID) {
		return false
	}
	if task.IsLeaderTask {
		return false
	}
	if isActiveTaskStatus(task.Status) {
		return true
	}
	events, err := h.Queries.ListIssueSquadSOPStepEvents(ctx, db.ListIssueSquadSOPStepEventsParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return false
	}
	for _, event := range events {
		if event.TaskID.Valid && uuidToString(event.TaskID) == uuidToString(comment.SourceTaskID) {
			return true
		}
	}
	return false
}

func isActiveTaskStatus(status string) bool {
	switch status {
	case "queued", "dispatched", "running", "waiting_local_directory":
		return true
	default:
		return false
	}
}

func filterSuppressedCommentAgentTriggers(triggers []commentAgentTrigger, suppressAgentIDs []pgtype.UUID) []commentAgentTrigger {
	if len(triggers) == 0 || len(suppressAgentIDs) == 0 {
		return triggers
	}
	suppressed := make(map[string]struct{}, len(suppressAgentIDs))
	for _, id := range suppressAgentIDs {
		if id.Valid {
			suppressed[uuidToString(id)] = struct{}{}
		}
	}
	if len(suppressed) == 0 {
		return triggers
	}
	filtered := make([]commentAgentTrigger, 0, len(triggers))
	for _, trigger := range triggers {
		if _, ok := suppressed[uuidToString(trigger.Agent.ID)]; ok {
			continue
		}
		filtered = append(filtered, trigger)
	}
	return filtered
}

func (h *Handler) enqueueCommentAgentTriggers(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, triggers []commentAgentTrigger) {
	for _, trigger := range triggers {
		if h.shouldBlockParentSOPStageTriggerForCrossProjectChildren(ctx, issue, triggerCommentID, trigger) {
			h.recordBlockedParentSOPStageTriggerComment(ctx, issue, trigger.Agent.Name)
			continue
		}
		switch trigger.Source {
		case commentTriggerSourceIssueAssignee:
			if trigger.Squad != nil {
				if _, err := h.TaskService.EnqueueTaskForSquadLeader(ctx, issue, trigger.Agent.ID, triggerCommentID); err != nil {
					slog.Warn("enqueue squad leader task failed",
						"issue_id", uuidToString(issue.ID),
						"squad_id", uuidToString(trigger.Squad.ID),
						"leader_id", uuidToString(trigger.Agent.ID),
						"error", err)
				}
				continue
			}
			if _, err := h.TaskService.EnqueueTaskForIssue(ctx, issue, triggerCommentID); err != nil {
				slog.Warn("enqueue agent task on comment failed", "issue_id", uuidToString(issue.ID), "error", err)
			}
		case commentTriggerSourceMentionSquadLeader:
			if _, err := h.TaskService.EnqueueTaskForSquadLeader(ctx, issue, trigger.Agent.ID, triggerCommentID); err != nil {
				slog.Warn("enqueue squad leader mention task failed",
					"issue_id", uuidToString(issue.ID),
					"agent_id", uuidToString(trigger.Agent.ID),
					"error", err)
			}
		case commentTriggerSourceMentionAgent:
			if _, err := h.TaskService.EnqueueTaskForMention(ctx, issue, trigger.Agent.ID, triggerCommentID); err != nil {
				slog.Warn("enqueue mention agent task failed",
					"issue_id", uuidToString(issue.ID),
					"agent_id", uuidToString(trigger.Agent.ID),
					"error", err)
			}
		}
	}
}

func (h *Handler) shouldBlockParentSOPStageTriggerForCrossProjectChildren(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, trigger commentAgentTrigger) bool {
	roleKey := normalizeSOPRoleMentionKey(roleKeyFromAgentRuntimeConfig(trigger.Agent))
	if roleKey == "" {
		switch trigger.Agent.Name {
		case projectSOPAgent04:
			roleKey = "04-implement"
		case projectSOPAgent05:
			roleKey = "05-verify"
		}
	}
	if roleKey != "04-implement" && roleKey != "05-verify" {
		return false
	}
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		return false
	}
	squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || !isStageChainSOPProfile(squad.SopProfile) {
		return false
	}
	if !h.latestTaskSplitRequiresCrossProjectChildren(ctx, issue) {
		return false
	}
	children, err := h.Queries.ListChildIssues(ctx, issue.ID)
	if err != nil {
		slog.Warn("sop cross-project child gate skipped: list children failed",
			"issue_id", uuidToString(issue.ID),
			"error", err)
		return false
	}
	if len(children) == 0 {
		return true
	}
	for _, child := range children {
		if child.Status != "done" {
			return true
		}
	}
	return false
}

func isStageChainSOPProfile(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var profile commentSOPProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return false
	}
	if strings.EqualFold(profile.Mode, "stage_chain") {
		return true
	}
	required := map[string]bool{
		"pm": true, "01-clarify": true, "02-design": true,
		"03-task-split": true, "04-implement": true, "05-verify": true,
	}
	for _, step := range profile.Steps {
		key := normalizeSOPRoleMentionKey(step.RoleKey)
		delete(required, key)
	}
	return len(required) == 0
}

func (h *Handler) latestTaskSplitRequiresCrossProjectChildren(ctx context.Context, issue db.Issue) bool {
	comments, err := h.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       commentHardCap,
	})
	if err != nil {
		slog.Warn("sop cross-project child gate skipped: list task-split comments failed",
			"issue_id", uuidToString(issue.ID),
			"error", err)
		return false
	}
	for i := len(comments) - 1; i >= 0; i-- {
		if isTaskSplitCrossProjectEvidenceComment(comments[i].Content) {
			return containsRequiredCrossProjectDependency(comments[i].Content)
		}
	}
	return false
}

func isTaskSplitCrossProjectEvidenceComment(content string) bool {
	lower := strings.ToLower(strings.TrimSpace(content))
	if !strings.Contains(lower, "03-task-split") && !strings.Contains(lower, "03-任务拆分") {
		return false
	}
	evidenceMarkers := []string{
		"## 03",
		"阶段产物",
		"已输出",
		"已闭环",
		"required cross-project dependencies",
		"not required projects",
		"跨项目依赖",
		"无跨项目",
		"child issue",
		"子 issue",
		"子任务",
		"handoff-",
	}
	for _, marker := range evidenceMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func containsRequiredCrossProjectDependency(content string) bool {
	text := strings.ToLower(content)
	if taskSplitDeclaresNoRequiredCrossProjectChildren(text) {
		return false
	}
	requiredMarkers := []string{
		"待 pm 创建 child issue",
		"pm 下一步先创建/复用对应 child issue",
		"创建/复用对应目标项目 child issue",
		"必须创建 child issue",
		"需要创建 child issue",
		"需创建 child issue",
	}
	for _, marker := range requiredMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return requiredCrossProjectSectionHasEntries(text)
}

func taskSplitDeclaresNoRequiredCrossProjectChildren(text string) bool {
	noRequirementMarkers := []string{
		"无跨项目依赖",
		"无 required cross-project dependencies",
		"required cross-project dependencies: none",
		"required cross-project dependencies：none",
		"required cross-project dependencies: 无",
		"required cross-project dependencies：无",
		"无需创建 child issue",
		"不需要创建 child issue",
		"无跨项目 child issue",
		"无 child issue",
		"不创建 child issue",
		"无子任务",
	}
	for _, marker := range noRequirementMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func requiredCrossProjectSectionHasEntries(text string) bool {
	inSection := false
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		lower := strings.ToLower(line)
		if strings.Contains(lower, "required cross-project dependencies") {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(lower, "#") ||
			strings.Contains(lower, "not required projects") ||
			strings.Contains(lower, "v1/v2/v3") ||
			strings.Contains(lower, "sandbox_plan") {
			return false
		}
		if strings.Contains(lower, "none") ||
			strings.Contains(lower, "not required") ||
			strings.Contains(line, "无") ||
			strings.Contains(line, "不需要") {
			continue
		}
		if strings.Contains(lower, "待 pm") ||
			strings.Contains(lower, "child issue") ||
			strings.Contains(lower, "handoff-") ||
			strings.Contains(line, "子任务") ||
			strings.Contains(line, "分发") {
			return true
		}
	}
	return false
}

func (h *Handler) recordBlockedParentSOPStageTriggerComment(ctx context.Context, issue db.Issue, stageName string) {
	content := strings.TrimSpace("平台已阻止父任务阶段调度：03-任务拆分已识别 required 跨项目依赖，但父 issue 的 child issue 仍缺失或未全部完成，因此不能触发父 issue 的 " + stageName + "。请 PM 先创建/复用并回读 required child issue；所有 required child issue 完成后，再继续父 issue 阶段。")
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		slog.Warn("begin sop cross-project child gate comment transaction failed", "issue_id", uuidToString(issue.ID), "stage", stageName, "error", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := h.Queries.WithTx(tx)
	comment, err := queries.CreateComment(ctx, db.CreateCommentParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    util.MustParseUUID("00000000-0000-0000-0000-000000000000"),
		Content:     content,
		Type:        "comment",
	})
	if err != nil {
		slog.Warn("create sop cross-project child gate comment failed",
			"issue_id", uuidToString(issue.ID),
			"stage", stageName,
			"error", err)
		return
	}
	createdEvent := buildCommentCreatedEvent(issue, commentToResponse(comment, nil, nil), "system", "")
	createdEvent, err = eventoutbox.Enqueue(ctx, queries, createdEvent)
	if err != nil {
		slog.Warn("enqueue sop cross-project child gate comment event failed", "issue_id", uuidToString(issue.ID), "stage", stageName, "error", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("commit sop cross-project child gate comment failed", "issue_id", uuidToString(issue.ID), "stage", stageName, "error", err)
		return
	}
	h.publishEvent(createdEvent)
}

func (h *Handler) computeCommentAgentTriggers(ctx context.Context, issue db.Issue, content string, parentComment *db.Comment, actorType, actorID string, opts commentTriggerComputeOptions) []commentAgentTrigger {
	if isNoteComment(content) {
		return nil
	}

	seen := make(map[string]struct{})
	triggers := make([]commentAgentTrigger, 0, 2)
	add := func(trigger commentAgentTrigger) {
		id := uuidToString(trigger.Agent.ID)
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		triggers = append(triggers, trigger)
	}

	if actorType == "member" && h.shouldEnqueueOnComment(ctx, issue, actorType, actorID, opts) &&
		!h.commentMentionsOthersButNotAssignee(content, issue) &&
		!h.isReplyToMemberThread(ctx, parentComment, content, issue) {
		if agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          issue.AssigneeID,
			WorkspaceID: issue.WorkspaceID,
		}); err == nil {
			add(commentAgentTrigger{Agent: agent, Source: commentTriggerSourceIssueAssignee})
		}
	}

	if trigger, ok := h.computeAssignedSquadLeaderCommentTrigger(ctx, issue, content, actorType, actorID, opts); ok {
		add(trigger)
	}

	for _, trigger := range h.computeMentionedAgentCommentTriggers(ctx, issue, content, parentComment, actorType, actorID, opts) {
		add(trigger)
	}

	return triggers
}

func (h *Handler) computeAssignedSquadLeaderCommentTrigger(ctx context.Context, issue db.Issue, content, authorType, authorID string, opts commentTriggerComputeOptions) (commentAgentTrigger, bool) {
	if opts.SuppressAssignedSquadLeader {
		return commentAgentTrigger{}, false
	}
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		return commentAgentTrigger{}, false
	}
	squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return commentAgentTrigger{}, false
	}
	if !h.canUseSquad(ctx, squad, authorType, authorID, uuidToString(issue.WorkspaceID)) {
		return commentAgentTrigger{}, false
	}
	if authorType == "agent" && authorID == uuidToString(squad.LeaderID) &&
		h.lastTaskWasLeader(ctx, issue.ID, squad.LeaderID) {
		return commentAgentTrigger{}, false
	}
	if authorType == "member" && commentMentionsAnyone(content) {
		return commentAgentTrigger{}, false
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          squad.LeaderID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid {
		return commentAgentTrigger{}, false
	}
	if !h.canAccessPersonalAgent(ctx, agent, authorType, authorID, uuidToString(issue.WorkspaceID)) {
		return commentAgentTrigger{}, false
	}
	hasPending, err := h.hasPendingTaskForIssueAndAgent(ctx, issue.ID, squad.LeaderID, opts)
	if err != nil || hasPending {
		return commentAgentTrigger{}, false
	}
	return commentAgentTrigger{Agent: agent, Source: commentTriggerSourceIssueAssignee, Squad: &squad}, true
}

func (h *Handler) hasPendingTaskForIssueAndAgent(ctx context.Context, issueID, agentID pgtype.UUID, opts commentTriggerComputeOptions) (bool, error) {
	if opts.ExcludeTriggerCommentID.Valid {
		return h.Queries.HasPendingTaskForIssueAndAgentExcludingTriggerComment(ctx, db.HasPendingTaskForIssueAndAgentExcludingTriggerCommentParams{
			IssueID:                 issueID,
			AgentID:                 agentID,
			ExcludeTriggerCommentID: opts.ExcludeTriggerCommentID,
		})
	}
	return h.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: issueID,
		AgentID: agentID,
	})
}

// commentMentionsOthersButNotAssignee returns true if the comment @mentions
// anyone but does NOT @mention the issue's assignee agent. This is used to
// suppress the on_comment trigger when the user is directing their comment at
// someone else (e.g. sharing results with a colleague, asking another agent).
// @all is treated as a broadcast — it suppresses the trigger because the user
// is announcing to everyone, not specifically requesting work from the agent.
func (h *Handler) commentMentionsOthersButNotAssignee(content string, issue db.Issue) bool {
	mentions := util.ParseMentions(content)
	// Filter out issue mentions — they are cross-references, not @people.
	filtered := mentions[:0]
	for _, m := range mentions {
		if m.Type != "issue" {
			filtered = append(filtered, m)
		}
	}
	mentions = filtered
	if len(mentions) == 0 {
		return false // No mentions (or only issue refs) — normal on_comment behavior
	}
	// @all is a broadcast to all members — suppress agent trigger.
	if util.HasMentionAll(mentions) {
		return true
	}
	if !issue.AssigneeID.Valid {
		return true // No assignee — mentions target others
	}
	assigneeID := uuidToString(issue.AssigneeID)
	for _, m := range mentions {
		if m.ID == assigneeID {
			return false // Assignee is mentioned — allow trigger
		}
	}
	return true // Others mentioned but not assignee — suppress trigger
}

// isReplyToMemberThread returns true if the comment is a reply in a thread
// started by a member and does NOT @mention the issue's assignee agent.
// When a member replies in a member-started thread, they are most likely
// continuing a human conversation — not requesting work from the assigned agent.
// Replying to an agent-started thread, or explicitly @mentioning the assignee
// in the reply, still triggers on_comment as expected.
// If the parent (thread root) itself @mentions the assignee, the thread is
// considered a conversation with the agent, so replies are allowed to trigger.
// If the assigned agent has already replied in the thread, the member is
// conversing with the agent, so replies are allowed to trigger.
func (h *Handler) isReplyToMemberThread(ctx context.Context, parent *db.Comment, content string, issue db.Issue) bool {
	if parent == nil {
		return false // Not a reply — normal top-level comment
	}
	if parent.AuthorType != "member" {
		return false // Thread started by an agent — allow trigger
	}
	// Thread was started by a member. Suppress on_comment unless the reply
	// or the parent explicitly @mentions the assignee agent, or the agent
	// has already participated in this thread.
	if !issue.AssigneeID.Valid {
		return true // No assignee to mention
	}
	assigneeID := uuidToString(issue.AssigneeID)
	// Check current comment mentions.
	for _, m := range util.ParseMentions(content) {
		if m.ID == assigneeID {
			return false // Assignee explicitly mentioned in reply — allow trigger
		}
	}
	// Check parent (thread root) mentions — if the thread was started by
	// mentioning the assignee, replies continue that conversation.
	for _, m := range util.ParseMentions(parent.Content) {
		if m.ID == assigneeID {
			return false // Assignee mentioned in thread root — allow trigger
		}
	}
	// Check if the assigned agent has already replied in this thread —
	// if so, the member is continuing a conversation with the agent.
	if h.Queries != nil {
		hasReplied, err := h.Queries.HasAgentRepliedInThread(ctx, db.HasAgentRepliedInThreadParams{
			ParentID: parent.ID,
			AgentID:  issue.AssigneeID,
		})
		if err == nil && hasReplied {
			return false // Agent participated in thread — allow trigger
		}
	}
	return true // Reply to member thread without agent participation — suppress
}

// shouldInheritParentMentions decides whether a reply with no explicit
// mentions should inherit the parent (thread root) comment's mentions.
//
// Inheritance lets a member who started a thread by @mentioning an agent
// continue the conversation with that agent without re-typing the mention
// on every follow-up reply.
//
// It is intentionally narrow:
//
//   - Only when the reply contains zero mentions of its own. Any explicit
//     mention in the reply is a deliberate choice about who to involve.
//   - Only when the reply author is a member. Agent-authored replies must
//     never inherit, otherwise an agent posting in a thread whose root
//     mentioned another agent would re-trigger that agent and create a loop.
//   - Only when the parent author is a member. When an agent authors a
//     comment that @mentions another agent, it is typically a one-shot
//     delegation (e.g. an agent posting a PR completion that @mentions a
//     reviewer agent). Subsequent member follow-ups in the same thread are
//     directed at the assignee, not at the delegated agent — inheriting
//     would re-trigger the delegated agent on every plain reply.
func shouldInheritParentMentions(parentComment *db.Comment, replyMentions []util.Mention, replyAuthorType string) bool {
	if parentComment == nil {
		return false
	}
	if len(replyMentions) > 0 {
		return false
	}
	if replyAuthorType == "agent" {
		return false
	}
	return parentComment.AuthorType == "member"
}

// computeMentionedAgentCommentTriggers parses @agent mentions from comment
// content and returns a trigger for each mentioned agent. When parentComment
// is non-nil (i.e. the comment is a reply), mentions from the parent (thread
// root) are also included so that agents mentioned in the top-level comment
// are re-triggered by subsequent replies in the same thread — unless the reply
// explicitly @mentions only non-agent entities (members, issues), which
// signals the user is talking to other people and not the agent.
// Skips agents with on_mention trigger disabled, and personal agents mentioned
// by non-owner members (only the agent owner or workspace admin/owner can
// mention a personal agent). Self-mentions are intentionally allowed so an
// agent running in one issue can explicitly enqueue itself on another (e.g.
// a child-issue run notifying the parent issue whose assignee is the same
// agent); runaway loops are prevented by HasPendingTaskForIssueAndAgent
// dedupe and the natural queued/dispatched coalescing of the task queue.
// Note: no status gate here — @mention is an explicit action and should work
// even on done/cancelled issues (the agent can reopen the issue if needed).
func (h *Handler) computeMentionedAgentCommentTriggers(ctx context.Context, issue db.Issue, content string, parentComment *db.Comment, authorType, authorID string, opts commentTriggerComputeOptions) []commentAgentTrigger {
	wsID := uuidToString(issue.WorkspaceID)
	mentions := util.ParseMentions(content)
	if shouldInheritParentMentions(parentComment, mentions, authorType) {
		mentions = util.ParseMentions(parentComment.Content)
	}
	mentions = append(mentions, h.parseSquadSOPRoleKeyMentions(ctx, issue, content)...)
	triggers := make([]commentAgentTrigger, 0, len(mentions))
	seen := make(map[string]struct{}, len(mentions))
	add := func(trigger commentAgentTrigger) {
		id := uuidToString(trigger.Agent.ID)
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		triggers = append(triggers, trigger)
	}
	for _, m := range mentions {
		if m.Type == "squad" {
			// @squad mention → trigger the squad's leader agent.
			squadUUID := parseUUID(m.ID)
			squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
				ID:          squadUUID,
				WorkspaceID: issue.WorkspaceID,
			})
			if err != nil {
				continue
			}
			if !h.canUseSquad(ctx, squad, authorType, authorID, wsID) {
				continue
			}
			leaderID := squad.LeaderID
			// Prevent self-trigger only when the agent's last activity on this
			// issue was itself a leader task. An agent that holds both the
			// leader and a worker role in the squad must still wake its
			// leader role after posting a comment from its worker task.
			if authorType == "agent" && authorID == uuidToString(leaderID) &&
				h.lastTaskWasLeader(ctx, issue.ID, leaderID) {
				continue
			}
			// Verify leader agent is ready (has runtime, not archived).
			agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
				ID:          leaderID,
				WorkspaceID: issue.WorkspaceID,
			})
			if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid {
				continue
			}
			// Private-agent gate: prevent triggering a personal leader via squad mention.
			if !h.canAccessPersonalAgent(ctx, agent, authorType, authorID, wsID) {
				continue
			}
			// Dedup: skip if leader already has a pending task for this issue.
			hasPending, err := h.hasPendingTaskForIssueAndAgent(ctx, issue.ID, leaderID, opts)
			if err != nil || hasPending {
				continue
			}
			add(commentAgentTrigger{Agent: agent, Source: commentTriggerSourceMentionSquadLeader, Squad: &squad})
			continue
		}
		if m.Type != "agent" {
			continue
		}
		agentUUID := parseUUID(m.ID)
		// Load the agent scoped to the current issue's workspace. Using the
		// bare GetAgent here would let a mention resolve to an agent in a
		// different workspace, and the visibility check below would then be
		// applied against the wrong workspace's roles (a workspace owner in
		// THIS workspace would pass the gate for a personal agent that lives
		// in someone else's workspace).
		agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          agentUUID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid {
			continue
		}
		// Private-agent gate (member→private requires allowed_principals;
		// agent→agent always passes).
		if !h.canAccessPersonalAgent(ctx, agent, authorType, authorID, wsID) {
			continue
		}
		// Dedup: skip if this agent already has a pending task for this issue.
		hasPending, err := h.hasPendingTaskForIssueAndAgent(ctx, issue.ID, agentUUID, opts)
		if err != nil || hasPending {
			continue
		}
		if issue.AssigneeType.Valid && issue.AssigneeType.String == "squad" && issue.AssigneeID.Valid {
			squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
				ID:          issue.AssigneeID,
				WorkspaceID: issue.WorkspaceID,
			})
			if err == nil && uuidToString(squad.LeaderID) == uuidToString(agentUUID) {
				add(commentAgentTrigger{Agent: agent, Source: commentTriggerSourceMentionSquadLeader, Squad: &squad})
				continue
			}
		}
		add(commentAgentTrigger{Agent: agent, Source: commentTriggerSourceMentionAgent})
	}
	return triggers
}

func (h *Handler) parseSquadSOPRoleKeyMentions(ctx context.Context, issue db.Issue, content string) []util.Mention {
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		return nil
	}
	matches := sopRoleKeyMentionRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	wantedRoles := map[string]struct{}{}
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		if roleKey, ok := normalizeSOPRoleAlias(match[1]); ok {
			wantedRoles[roleKey] = struct{}{}
		}
	}
	if len(wantedRoles) == 0 {
		return nil
	}
	squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return nil
	}
	members, err := h.Queries.ListSquadMembers(ctx, squad.ID)
	if err != nil {
		return nil
	}
	memberIDs := map[string]struct{}{uuidToString(squad.LeaderID): {}}
	for _, member := range members {
		if member.MemberType == "agent" && member.MemberID.Valid {
			memberIDs[uuidToString(member.MemberID)] = struct{}{}
		}
	}
	agents, err := h.Queries.ListAgents(ctx, issue.WorkspaceID)
	if err != nil {
		return nil
	}
	out := make([]util.Mention, 0, len(wantedRoles))
	seen := map[string]struct{}{}
	for _, agent := range agents {
		id := uuidToString(agent.ID)
		if _, ok := memberIDs[id]; !ok {
			continue
		}
		roleKey := normalizeSOPRoleMentionKey(roleKeyFromAgentRuntimeConfig(agent))
		if roleKey == "" {
			roleKey = legacySOPRoleKeyFromAgentName(agent.Name)
		}
		if _, ok := wantedRoles[roleKey]; !ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, util.Mention{Type: "agent", ID: id})
	}
	return out
}

func normalizeSOPRoleAlias(value string) (string, bool) {
	switch normalizeSOPRoleMentionKey(value) {
	case "pm":
		return "pm", true
	case "01", "01-clarify":
		return "01-clarify", true
	case "02", "02-design":
		return "02-design", true
	case "03", "03-task-split":
		return "03-task-split", true
	case "04", "04-implement":
		return "04-implement", true
	case "05", "05-verify":
		return "05-verify", true
	default:
		return "", false
	}
}

func legacySOPRoleKeyFromAgentName(name string) string {
	switch name {
	case projectSOPAgentPM:
		return "pm"
	case projectSOPAgent01:
		return "01-clarify"
	case projectSOPAgent02:
		return "02-design"
	case projectSOPAgent03:
		return "03-task-split"
	case projectSOPAgent04:
		return "04-implement"
	case projectSOPAgent05:
		return "05-verify"
	default:
		return ""
	}
}

func normalizeSOPRoleMentionKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	return strings.Trim(value, "-")
}
