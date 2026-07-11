package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (s *TaskService) EnqueueTaskForIssue(ctx context.Context, issue db.Issue, triggerCommentID ...pgtype.UUID) (db.AgentTaskQueue, error) {
	var commentID pgtype.UUID
	if len(triggerCommentID) > 0 {
		commentID = triggerCommentID[0]
	}
	return s.enqueueIssueTask(ctx, issue, commentID, false)
}

// enqueueIssueTask is the shared implementation behind EnqueueTaskForIssue
// and the manual rerun path. forceFreshSession=true marks the task so the
// daemon claim handler skips the (agent_id, issue_id) resume lookup — the
// user already judged the prior output bad, a fresh agent session is the
// expected behavior.
func (s *TaskService) enqueueIssueTask(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, forceFreshSession bool) (db.AgentTaskQueue, error) {
	var task db.AgentTaskQueue
	err := s.runInTx(ctx, func(queries *db.Queries) error {
		var err error
		task, err = s.CreateIssueTaskInTx(ctx, queries, issue, triggerCommentID, forceFreshSession)
		return err
	})
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	s.PublishIssueTaskEnqueued(ctx, task)
	return task, nil
}

// CreateIssueTaskInTx validates the assigned agent and inserts the task using
// the caller's transaction. The caller must publish the task only after the
// transaction commits.
func (s *TaskService) CreateIssueTaskInTx(
	ctx context.Context,
	queries *db.Queries,
	issue db.Issue,
	triggerCommentID pgtype.UUID,
	forceFreshSession bool,
) (db.AgentTaskQueue, error) {
	if !issue.AssigneeID.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("issue has no assignee")
	}

	agent, err := queries.GetAgent(ctx, issue.AssigneeID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("agent is archived")
	}
	if !agent.RuntimeID.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("agent has no runtime")
	}

	task, err := queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		AgentID:           issue.AssigneeID,
		RuntimeID:         agent.RuntimeID,
		IssueID:           issue.ID,
		Priority:          priorityToInt(issue.Priority),
		TriggerCommentID:  triggerCommentID,
		TriggerSummary:    s.buildCommentTriggerSummaryWithQueries(ctx, queries, triggerCommentID),
		ForceFreshSession: pgtype.Bool{Bool: forceFreshSession, Valid: forceFreshSession},
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("create task: %w", err)
	}
	return task, nil
}

// PublishIssueTaskEnqueued emits only post-commit effects for an issue task.
func (s *TaskService) PublishIssueTaskEnqueued(ctx context.Context, task db.AgentTaskQueue) {
	slog.Info("task enqueued",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(task.IssueID),
		"agent_id", util.UUIDToString(task.AgentID),
		"force_fresh_session", task.ForceFreshSession,
	)
	// Order matters: broadcast first, notify daemon second. notifyTaskAvailable
	// kicks an in-process channel that the daemon picks up over HTTP and
	// claims; the claim path then emits its own task:dispatch. Doing the
	// queued broadcast afterwards risks the dispatch event reaching clients
	// before the queued one (rare but unsafe-by-construction). Publishing
	// in the desired observe-order makes correctness independent of timing.
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
}

// EnqueueTaskForMention creates a queued task for a mentioned agent on an issue.
// Unlike EnqueueTaskForIssue, this takes an explicit agent ID rather than
// deriving it from the issue assignee.
func (s *TaskService) EnqueueTaskForMention(ctx context.Context, issue db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueMentionTask(ctx, issue, agentID, triggerCommentID, false, false)
}

// EnqueueTaskForSquadLeader is the leader-role variant of EnqueueTaskForMention.
// The resulting task carries is_leader_task=true so that downstream
// self-trigger guards can distinguish a comment posted while the agent was
// acting as the squad's leader (skip) from one posted while it was acting
// as a worker (do not skip). This matters for agents that are simultaneously
// the leader and a worker of the same squad — see migration 090.
func (s *TaskService) EnqueueTaskForSquadLeader(ctx context.Context, issue db.Issue, leaderID pgtype.UUID, triggerCommentID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueMentionTask(ctx, issue, leaderID, triggerCommentID, true, true)
}

// CreateProjectOwnerApprovalTaskInTx inserts the review task using the
// caller's transaction. The caller publishes and wakes the daemon only after
// that transaction commits.
func (s *TaskService) CreateProjectOwnerApprovalTaskInTx(
	ctx context.Context,
	queries *db.Queries,
	issue db.Issue,
	project db.Project,
) (db.AgentTaskQueue, error) {
	if !project.LeadType.Valid || project.LeadType.String != "agent" || !project.LeadID.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("project lead is not an agent")
	}
	agent, err := queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          project.LeadID,
		WorkspaceID: project.WorkspaceID,
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("load project lead agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("project lead agent is archived")
	}
	if !agent.RuntimeID.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("project lead agent has no runtime")
	}
	task, err := queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		AgentID:           agent.ID,
		RuntimeID:         agent.RuntimeID,
		IssueID:           issue.ID,
		Priority:          priorityToInt(issue.Priority),
		TriggerSummary:    pgtype.Text{String: fmt.Sprintf("Project owner approval review for %s", project.Title), Valid: true},
		ForceFreshSession: pgtype.Bool{Bool: true, Valid: true},
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("create project owner approval task: %w", err)
	}
	return task, nil
}

// PublishProjectOwnerApprovalTaskEnqueued emits the non-durable effects for a
// review task after its transaction has committed.
func (s *TaskService) PublishProjectOwnerApprovalTaskEnqueued(ctx context.Context, task db.AgentTaskQueue, project db.Project) {
	slog.Info("project owner approval task enqueued",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(task.IssueID),
		"agent_id", util.UUIDToString(task.AgentID),
		"project_id", util.UUIDToString(project.ID),
	)
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
}

func (s *TaskService) enqueueMentionTask(ctx context.Context, issue db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID, isLeader bool, forceFreshSession bool) (db.AgentTaskQueue, error) {
	var task db.AgentTaskQueue
	err := s.runInTx(ctx, func(queries *db.Queries) error {
		var err error
		task, err = s.CreateMentionTaskInTx(ctx, queries, issue, agentID, triggerCommentID, isLeader, forceFreshSession)
		return err
	})
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	s.PublishMentionTaskEnqueued(ctx, task)
	return task, nil
}

// CreateMentionTaskInTx inserts a mention or squad-leader task and its SOP
// projection in the caller's transaction. The caller publishes after commit.
func (s *TaskService) CreateMentionTaskInTx(
	ctx context.Context,
	queries *db.Queries,
	issue db.Issue,
	agentID pgtype.UUID,
	triggerCommentID pgtype.UUID,
	isLeader bool,
	forceFreshSession bool,
) (db.AgentTaskQueue, error) {
	agent, err := queries.GetAgent(ctx, agentID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("agent is archived")
	}
	if !agent.RuntimeID.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("agent has no runtime")
	}

	task, err := queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		AgentID:           agentID,
		RuntimeID:         agent.RuntimeID,
		IssueID:           issue.ID,
		Priority:          priorityToInt(issue.Priority),
		TriggerCommentID:  triggerCommentID,
		TriggerSummary:    s.buildCommentTriggerSummaryWithQueries(ctx, queries, triggerCommentID),
		IsLeaderTask:      pgtype.Bool{Bool: isLeader, Valid: isLeader},
		ForceFreshSession: pgtype.Bool{Bool: forceFreshSession, Valid: forceFreshSession},
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("create task: %w", err)
	}
	if isLeader {
		if err := s.createSquadSOPRunForLeaderTask(ctx, queries, issue, task); err != nil {
			return db.AgentTaskQueue{}, fmt.Errorf("create squad SOP run: %w", err)
		}
	}
	return task, nil
}

// PublishMentionTaskEnqueued emits only post-commit effects for a mention task.
func (s *TaskService) PublishMentionTaskEnqueued(ctx context.Context, task db.AgentTaskQueue) {
	slog.Info("mention task enqueued", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID), "agent_id", util.UUIDToString(task.AgentID), "is_leader_task", task.IsLeaderTask)
	// See EnqueueTaskForIssue for ordering rationale.
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
}

func (s *TaskService) createSquadSOPRunForLeaderTask(ctx context.Context, queries *db.Queries, issue db.Issue, task db.AgentTaskQueue) error {
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		return nil
	}
	squad, err := queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("load squad for SOP run: %w", err)
	}

	profile := normalizeSquadSOPProfile(squad.SopProfile)
	profileKey, currentStepKey, currentStepName, roleKey := squadSOPProfileSummary(profile)
	run, err := queries.CreateSquadSOPRun(ctx, db.CreateSquadSOPRunParams{
		WorkspaceID:    issue.WorkspaceID,
		IssueID:        issue.ID,
		SquadID:        squad.ID,
		LeaderTaskID:   task.ID,
		ProfileKey:     profileKey,
		Profile:        profile,
		Status:         "进行中",
		CurrentStepKey: currentStepKey,
	})
	if err != nil {
		return fmt.Errorf("create squad SOP run: %w", err)
	}
	if currentStepKey == "" {
		return nil
	}
	if _, err := queries.CreateSquadSOPStepEvent(ctx, db.CreateSquadSOPStepEventParams{
		RunID:         run.ID,
		WorkspaceID:   issue.WorkspaceID,
		IssueID:       issue.ID,
		SquadID:       squad.ID,
		StepKey:       currentStepKey,
		StepName:      currentStepName,
		RoleKey:       roleKey,
		EventType:     "步骤开始",
		Status:        "进行中",
		Reason:        "队长任务已入队，自动进入小队 SOP 执行。",
		CreatedByType: "system",
		TaskID:        task.ID,
	}); err != nil {
		return fmt.Errorf("create squad SOP initial step event: %w", err)
	}
	return nil
}

func normalizeSquadSOPProfile(raw []byte) []byte {
	if len(raw) == 0 || string(raw) == "null" {
		return []byte(`{}`)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return []byte(`{}`)
	}
	normalized, err := json.Marshal(obj)
	if err != nil {
		return []byte(`{}`)
	}
	return normalized
}

func squadSOPProfileSummary(profile []byte) (profileKey, currentStepKey, currentStepName, roleKey string) {
	profileKey = "custom"
	var obj map[string]any
	if err := json.Unmarshal(profile, &obj); err != nil || obj == nil {
		return profileKey, "", "", ""
	}
	if v, ok := obj["profile_key"].(string); ok && strings.TrimSpace(v) != "" {
		profileKey = strings.TrimSpace(v)
	}
	steps, _ := obj["steps"].([]any)
	if len(steps) == 0 {
		return profileKey, "", "", ""
	}
	step, _ := steps[0].(map[string]any)
	if step == nil {
		return profileKey, "", "", ""
	}
	for _, key := range []string{"key", "step_key", "id"} {
		if v, ok := step[key].(string); ok && strings.TrimSpace(v) != "" {
			currentStepKey = strings.TrimSpace(v)
			break
		}
	}
	for _, key := range []string{"name", "title", "label"} {
		if v, ok := step[key].(string); ok && strings.TrimSpace(v) != "" {
			currentStepName = strings.TrimSpace(v)
			break
		}
	}
	for _, key := range []string{"role_key", "role"} {
		if v, ok := step[key].(string); ok && strings.TrimSpace(v) != "" {
			roleKey = strings.TrimSpace(v)
			break
		}
	}
	return profileKey, currentStepKey, currentStepName, roleKey
}

// QuickCreateContext is the JSON payload stored on a quick-create task's
// context column. The daemon detects this variant via Type == "quick_create"
// and switches to the quick-create prompt template; the durable terminal
// projection uses RequesterID + WorkspaceID for requester-facing results.
//
// ProjectID is the optional project the user picked in the modal. When
// non-empty the daemon claim handler resolves the project's title +
// resources, and the prompt template instructs the agent to pass
// `--project <uuid>` so the new issue lands in that project.
//
// SquadID is non-empty when the user picked a squad (rather than an agent)
// in the modal. The task is still enqueued against the squad's leader
// agent (Queries.CreateQuickCreateTask is agent-scoped); SquadID is the
// hint the daemon claim handler uses to layer the squad-leader briefing
// onto the agent's Instructions, matching the behavior of issue-bound
// tasks assigned to the squad.
type QuickCreateContext struct {
	Type          string   `json:"type"`
	Prompt        string   `json:"prompt"`
	RequesterID   string   `json:"requester_id"`
	WorkspaceID   string   `json:"workspace_id"`
	ProjectID     string   `json:"project_id,omitempty"`
	SquadID       string   `json:"squad_id,omitempty"`
	Status        string   `json:"status,omitempty"`
	Priority      string   `json:"priority,omitempty"`
	AssigneeType  string   `json:"assignee_type,omitempty"`
	AssigneeID    string   `json:"assignee_id,omitempty"`
	StartDate     string   `json:"start_date,omitempty"`
	DueDate       string   `json:"due_date,omitempty"`
	AttachmentIDs []string `json:"attachment_ids,omitempty"`
	// ParentIssueID is the optional UUID of the parent issue the new issue
	// should be filed under. Set when the user opens the modal from "Add
	// sub issue" on an existing issue; the daemon claim handler resolves the
	// parent's identifier and the prompt template instructs the agent to
	// pass `--parent <uuid>` so the sub-issue relationship is preserved
	// across the manual→agent mode flip.
	ParentIssueID string `json:"parent_issue_id,omitempty"`
}

// QuickCreateContextType marks a task as a quick-create job.
const QuickCreateContextType = "quick_create"

const IssueSourceSummaryContextType = "issue_source_summary"

type IssueSourceSummaryContext struct {
	Type         string `json:"type"`
	Provider     string `json:"provider,omitempty"`
	SourceURL    string `json:"source_url,omitempty"`
	ResourceType string `json:"resource_type,omitempty"`
	ResourceID   string `json:"resource_id,omitempty"`
}

type EnqueueQuickCreateTaskParams struct {
	WorkspaceID   pgtype.UUID
	RequesterID   pgtype.UUID
	AgentID       pgtype.UUID
	SquadID       pgtype.UUID
	Prompt        string
	ProjectID     pgtype.UUID
	ParentIssueID pgtype.UUID
	AttachmentIDs []pgtype.UUID
	Status        string
	Priority      string
	AssigneeType  string
	AssigneeID    pgtype.UUID
	StartDate     string
	DueDate       string
}

// EnqueueQuickCreateTask creates a queued task that has no issue / chat /
// autopilot link — the user's natural-language prompt is stored in the
// task's context JSONB and the agent is expected to translate it into a
// `multica issue create` call. Pre-validates that the agent is reachable
// (not archived, has a runtime) so the API can reject up-front rather than
// queue a task no one will ever claim.
//
// projectID is optional (zero-valued pgtype.UUID when the user didn't pick
// one). The handler is responsible for validating it belongs to the same
// workspace before passing it in.
//
// squadID is non-empty (Valid) when the user picked a squad as the actor.
// The handler has already resolved it to the squad's leader agent for
// agentID; the squadID hint is stamped into the task context so the daemon
// claim handler can inject the squad-leader briefing on dispatch.
//
// parentIssueID is optional (zero-valued pgtype.UUID when the user didn't
// open the modal from "Add sub issue"). The handler is responsible for
// validating it belongs to the same workspace before passing it in.
func (s *TaskService) EnqueueQuickCreateTask(ctx context.Context, p EnqueueQuickCreateTaskParams) (db.AgentTaskQueue, error) {
	agent, err := s.Queries.GetAgent(ctx, p.AgentID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("agent is archived")
	}
	if !agent.RuntimeID.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("agent has no runtime")
	}

	payload := QuickCreateContext{
		Type:        QuickCreateContextType,
		Prompt:      p.Prompt,
		RequesterID: util.UUIDToString(p.RequesterID),
		WorkspaceID: util.UUIDToString(p.WorkspaceID),
	}
	if p.ProjectID.Valid {
		payload.ProjectID = util.UUIDToString(p.ProjectID)
	}
	if p.SquadID.Valid {
		payload.SquadID = util.UUIDToString(p.SquadID)
	}
	if p.ParentIssueID.Valid {
		payload.ParentIssueID = util.UUIDToString(p.ParentIssueID)
	}
	if p.Status != "" {
		payload.Status = p.Status
	}
	if p.Priority != "" {
		payload.Priority = p.Priority
	}
	if p.AssigneeType != "" && p.AssigneeID.Valid {
		payload.AssigneeType = p.AssigneeType
		payload.AssigneeID = util.UUIDToString(p.AssigneeID)
	}
	if p.StartDate != "" {
		payload.StartDate = p.StartDate
	}
	if p.DueDate != "" {
		payload.DueDate = p.DueDate
	}
	if len(p.AttachmentIDs) > 0 {
		payload.AttachmentIDs = make([]string, 0, len(p.AttachmentIDs))
		for _, id := range p.AttachmentIDs {
			if id.Valid {
				payload.AttachmentIDs = append(payload.AttachmentIDs, util.UUIDToString(id))
			}
		}
	}
	contextJSON, err := json.Marshal(payload)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("marshal quick-create context: %w", err)
	}

	task, err := s.Queries.CreateQuickCreateTask(ctx, db.CreateQuickCreateTaskParams{
		AgentID:   p.AgentID,
		RuntimeID: agent.RuntimeID,
		Priority:  priorityToInt("high"),
		Context:   contextJSON,
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("create quick-create task: %w", err)
	}

	slog.Info("quick-create task enqueued",
		"task_id", util.UUIDToString(task.ID),
		"agent_id", util.UUIDToString(p.AgentID),
		"squad_id", payload.SquadID,
		"requester_id", util.UUIDToString(p.RequesterID),
		"workspace_id", util.UUIDToString(p.WorkspaceID),
		"project_id", payload.ProjectID,
		"parent_issue_id", payload.ParentIssueID,
	)
	// Match every other Enqueue* path: kick the daemon WS so the task
	// gets claimed promptly instead of waiting for the next 30 s poll
	// cycle. Without this the user perceives "quick create never
	// triggered" because the modal closes immediately and the task
	// sits in 'queued' until the next sleepWithContextOrWakeup tick.
	s.NotifyTaskEnqueued(ctx, task)
	return task, nil
}

// CreateIssueSourceSummaryTaskInTx inserts the TAPD source-summary task in the
// caller's transaction. The caller publishes only after commit.
func (s *TaskService) CreateIssueSourceSummaryTaskInTx(ctx context.Context, queries *db.Queries, issue db.Issue, agentID pgtype.UUID) (db.AgentTaskQueue, error) {
	if !agentID.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("source summary agent is required")
	}
	agent, err := queries.GetAgent(ctx, agentID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("load source summary agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("source summary agent is archived")
	}
	if !agent.RuntimeID.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("source summary agent has no runtime")
	}
	ctxPayload := IssueSourceSummaryContext{
		Type:         IssueSourceSummaryContextType,
		Provider:     issueMetadataString(issue.Metadata, "source_provider"),
		SourceURL:    issueMetadataString(issue.Metadata, "source_url"),
		ResourceType: issueMetadataString(issue.Metadata, "tapd_resource_type"),
		ResourceID:   issueMetadataString(issue.Metadata, "tapd_resource_id"),
	}
	contextJSON, err := json.Marshal(ctxPayload)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("marshal source summary context: %w", err)
	}
	task, err := queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		AgentID:           agent.ID,
		RuntimeID:         agent.RuntimeID,
		IssueID:           issue.ID,
		Priority:          priorityToInt("high"),
		TriggerSummary:    pgtype.Text{String: "为 TAPD 来源生成需求摘要", Valid: true},
		ForceFreshSession: pgtype.Bool{Bool: true, Valid: true},
		Context:           contextJSON,
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("create source summary task: %w", err)
	}
	return task, nil
}

// PublishIssueSourceSummaryTaskEnqueued emits the source-summary task's
// post-commit event and daemon wakeup.
func (s *TaskService) PublishIssueSourceSummaryTaskEnqueued(ctx context.Context, task db.AgentTaskQueue) {
	slog.Info("issue source summary task enqueued",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(task.IssueID),
		"agent_id", util.UUIDToString(task.AgentID),
	)
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
}

// ErrChatTaskAgentArchived signals that EnqueueChatTask refused to
// queue work because the destination agent has been archived. This
// is a productizable state — surface it to the user as "this agent
// has been archived" rather than retrying.
var ErrChatTaskAgentArchived = errors.New("chat task: agent archived")

// ErrChatTaskAgentNoRuntime signals that EnqueueChatTask refused to
// queue work because the agent has never been associated with a
// runtime (agent.runtime_id IS NULL). This is the "agent has no
// daemon configured" case — productizable as "agent offline".
//
// IMPORTANT: this is NOT the same as "the daemon is currently
// disconnected". When agent.runtime_id IS set, EnqueueChatTask
// enqueues the task and the daemon claims it on next online; that
// path returns a task row, not this error.
var ErrChatTaskAgentNoRuntime = errors.New("chat task: agent has no runtime")

// EnqueueChatTask creates a queued task for a chat session.
// Unlike issue tasks, chat tasks have no issue_id.
//
// Errors split into two layers:
//
//   - Productizable rejections (agent archived, no runtime) return
//     the sentinel errors above. Callers (e.g. the Lark dispatcher)
//     can errors.Is them to decide a user-visible outcome.
//
//   - Infrastructure failures (DB load / insert errors) are wrapped
//     as ordinary errors. The caller should treat them as retryable
//     or page-worthy, NOT as user-facing state.
//
// initiatorUserID is the user who actually sent the triggering message — the
// real requester behind this run. Callers pass it explicitly because
// chat_session.creator_id is not a reliable source: Lark group sessions set the
// creator to the installer, not the sender (see the lark dispatcher). Web chat
// passes the request user; the lark dispatcher passes the inbound sender of the
// latest message in the silence window. Stored on the task so the daemon brief
// can attribute the run to the right person. See MUL-2645.
func (s *TaskService) EnqueueChatTask(ctx context.Context, chatSession db.ChatSession, initiatorUserID pgtype.UUID) (db.AgentTaskQueue, error) {
	var task db.AgentTaskQueue
	err := s.runInTx(ctx, func(queries *db.Queries) error {
		agent, err := queries.LockAgentInWorkspaceForChat(ctx, db.LockAgentInWorkspaceForChatParams{
			ID:          chatSession.AgentID,
			WorkspaceID: chatSession.WorkspaceID,
		})
		if err != nil {
			return fmt.Errorf("lock chat agent: %w", err)
		}
		lockedSession, err := queries.LockChatSessionForSend(ctx, db.LockChatSessionForSendParams{
			ID:          chatSession.ID,
			WorkspaceID: chatSession.WorkspaceID,
			CreatorID:   chatSession.CreatorID,
		})
		if err != nil {
			return fmt.Errorf("lock chat session: %w", err)
		}
		if lockedSession.Status != "active" {
			return errors.New("chat task: session is not active")
		}
		task, err = s.CreateChatTaskInTx(ctx, queries, lockedSession, agent, initiatorUserID)
		return err
	})
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	s.PublishChatTaskEnqueued(ctx, task)
	return task, nil
}

// CreateChatTaskInTx performs only the database reads and task insert. Callers
// that already own a transaction can compose the task with its triggering
// message and idempotency record, then invoke PublishChatTaskEnqueued exactly
// once after commit.
func (s *TaskService) CreateChatTaskInTx(
	ctx context.Context,
	queries *db.Queries,
	chatSession db.ChatSession,
	agent db.Agent,
	initiatorUserID pgtype.UUID,
) (db.AgentTaskQueue, error) {
	if agent.ID != chatSession.AgentID || agent.WorkspaceID != chatSession.WorkspaceID {
		return db.AgentTaskQueue{}, errors.New("chat task: locked agent does not match session")
	}
	if agent.ArchivedAt.Valid {
		return db.AgentTaskQueue{}, ErrChatTaskAgentArchived
	}
	if !agent.RuntimeID.Valid {
		return db.AgentTaskQueue{}, ErrChatTaskAgentNoRuntime
	}

	task, err := queries.CreateChatTask(ctx, db.CreateChatTaskParams{
		AgentID:         chatSession.AgentID,
		RuntimeID:       agent.RuntimeID,
		Priority:        2, // medium priority for chat
		ChatSessionID:   chatSession.ID,
		InitiatorUserID: initiatorUserID,
	})
	if err != nil {
		slog.Error("chat task enqueue failed", "chat_session_id", util.UUIDToString(chatSession.ID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("create chat task: %w", err)
	}

	return task, nil
}

// PublishChatTaskEnqueued emits post-commit observability and wakeups for a
// task already durably inserted by CreateChatTaskInTx.
func (s *TaskService) PublishChatTaskEnqueued(ctx context.Context, task db.AgentTaskQueue) {
	slog.Info("chat task enqueued", "task_id", util.UUIDToString(task.ID), "chat_session_id", util.UUIDToString(task.ChatSessionID), "agent_id", util.UUIDToString(task.AgentID))
	// See EnqueueTaskForIssue for ordering rationale.
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
}

// WakeChatTaskIfQueued is the only replay side effect for a committed chat
// request. It repairs the narrow crash window between transaction commit and
// the original daemon wakeup without duplicating task/chat events, traces,
// metrics, or analytics.
func (s *TaskService) WakeChatTaskIfQueued(ctx context.Context, taskID string) {
	parsed, err := util.ParseUUID(taskID)
	if err != nil {
		return
	}
	task, err := s.Queries.GetAgentTask(ctx, parsed)
	if err != nil || task.Status != "queued" {
		return
	}
	s.notifyTaskAvailable(task)
}

// CancelTasksForIssue cancels every active task on the issue, reconciles each
// affected agent's status, and broadcasts task:cancelled events so frontends
// clear their live cards.
//
// Before #1587 this path was "cancel rows and return" — issue-status flips
// (e.g. user marks the issue `done` or `cancelled` while a task is still
// running) left the agent stuck at status="working" indefinitely, requiring a
// manual `multica agent update <id> --status idle` to unwedge. Matches the
// pattern already used by CancelTask and RerunIssue.
