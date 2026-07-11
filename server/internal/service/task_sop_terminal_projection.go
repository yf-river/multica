package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	squadSOPEventStarted   = "步骤开始"
	squadSOPEventCompleted = "步骤完成"
	squadSOPEventFailed    = "步骤失败"

	squadSOPStatusCompleted = "已完成"
	squadSOPStatusFailed    = "已失败"
	leaderProjectionKey     = "leader_continuation"
	missingMRGateComment    = `05 验证已完成，但平台还没有关联 MR。请通过平台创建并关联 MR 后再进入人工 CodeReview：

multica issue mr create <issue-id> --provider gongfeng --project-path <project-path> --source-branch <branch> --target-branch <target-branch> --title "<title>" --output json

创建成功后，平台会把 MR 写入任务的关联 MR 区域。`
)

type squadSOPTerminalOutcome struct {
	eventType   string
	eventStatus string
	result      []byte
}

func completedSquadSOPOutcome(result []byte) squadSOPTerminalOutcome {
	return squadSOPTerminalOutcome{
		eventType:   squadSOPEventCompleted,
		eventStatus: squadSOPStatusCompleted,
		result:      result,
	}
}

func failedSquadSOPOutcome() squadSOPTerminalOutcome {
	return squadSOPTerminalOutcome{
		eventType:   squadSOPEventFailed,
		eventStatus: squadSOPStatusFailed,
	}
}

type squadSOPTerminalProjection struct {
	issueStatus  *taskIssueStatusProjection
	commentEvent *events.Event
	leaderTask   *db.AgentTaskQueue
}

func lockIssueForTaskTerminalProjection(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
) error {
	if !task.IssueID.Valid {
		return nil
	}
	if _, err := queries.LockIssueForTaskTerminalProjection(ctx, task.IssueID); err != nil {
		return fmt.Errorf("lock issue for task terminal projection: %w", err)
	}
	return nil
}

func isTerminalTaskStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// reconcileExistingSquadSOPTerminal repairs rows finalized by an older build
// whose automatic event, run or Issue projection was only partially written.
// It intentionally emits no second task-terminal event; only missing dependent
// state is reconciled through the same idempotent transaction as new writes.
func (s *TaskService) reconcileExistingSquadSOPTerminal(
	ctx context.Context,
	task db.AgentTaskQueue,
) (*squadSOPTerminalProjection, error) {
	if _, isSourceSummary := ParseIssueSourceSummaryContext(task); isSourceSummary {
		return nil, nil
	}
	var projection *squadSOPTerminalProjection
	err := s.runInTx(ctx, func(queries *db.Queries) error {
		if err := lockIssueForTaskTerminalProjection(ctx, queries, task); err != nil {
			return err
		}
		switch task.Status {
		case "completed":
			if err := s.linkGongfengMRsFromTaskComments(ctx, queries, task); err != nil {
				return fmt.Errorf("repair task merge-request links: %w", err)
			}
			var err error
			projection, err = s.repairSquadSOPTerminal(
				ctx,
				queries,
				task,
				completedSquadSOPOutcome(task.Result),
			)
			return err
		case "failed":
			delivered, err := squadSOPTaskHasDeliveryComment(ctx, queries, task)
			if err != nil {
				return fmt.Errorf("repair task delivery classification: %w", err)
			}
			outcome := failedSquadSOPOutcome()
			if delivered {
				if err := s.linkGongfengMRsFromTaskComments(ctx, queries, task); err != nil {
					return fmt.Errorf("repair delivered task merge-request links: %w", err)
				}
				outcome = completedSquadSOPOutcome(nil)
			}
			projection, err = s.repairSquadSOPTerminal(ctx, queries, task, outcome)
			return err
		default:
			return nil
		}
	})
	return projection, err
}

// projectSquadSOPTerminal applies the complete automatic terminal projection
// through the caller's transaction. The task row is already locked by its
// terminal UPDATE; this function then locks issue -> SOP run in that order.
// Database failures are returned so the task transition rolls back with every
// dependent event, run, Issue and leader-continuation write.
func (s *TaskService) projectSquadSOPTerminal(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
	outcome squadSOPTerminalOutcome,
) (*squadSOPTerminalProjection, error) {
	return s.projectSquadSOPTerminalWithPolicy(ctx, queries, task, outcome, false)
}

// repairSquadSOPTerminal replays only the dependent state of a task that is
// already terminal. If its durable event belongs to a former Squad assignment,
// that old run is no longer the current Issue projection and must not make the
// idempotent terminal API fail after a legitimate reassignment.
func (s *TaskService) repairSquadSOPTerminal(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
	outcome squadSOPTerminalOutcome,
) (*squadSOPTerminalProjection, error) {
	return s.projectSquadSOPTerminalWithPolicy(ctx, queries, task, outcome, true)
}

func (s *TaskService) projectSquadSOPTerminalWithPolicy(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
	outcome squadSOPTerminalOutcome,
	ignoreFormerAssignment bool,
) (*squadSOPTerminalProjection, error) {
	if !task.IssueID.Valid {
		return nil, nil
	}
	if outcome.eventType == squadSOPEventFailed {
		hasRetry, err := queries.HasRetryTaskForParent(ctx, task.ID)
		if err != nil {
			return nil, fmt.Errorf("check retry before Squad SOP failure projection: %w", err)
		}
		if hasRetry {
			return nil, nil
		}
		hasActive, err := queries.HasActiveTaskForIssue(ctx, task.IssueID)
		if err != nil {
			return nil, fmt.Errorf("check active tasks before Squad SOP failure projection: %w", err)
		}
		if hasActive {
			return nil, nil
		}
	}

	issue, err := queries.LockIssueForTaskTerminalProjection(ctx, task.IssueID)
	if err != nil {
		return nil, fmt.Errorf("lock Squad SOP issue: %w", err)
	}
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		return nil, nil
	}

	run, ok, err := lockSquadSOPRunForTerminal(
		ctx,
		queries,
		task,
		outcome.eventType,
		ignoreFormerAssignment,
	)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	if run.IssueID != issue.ID || run.WorkspaceID != issue.WorkspaceID || run.SquadID != issue.AssigneeID {
		if ignoreFormerAssignment {
			return nil, nil
		}
		return nil, errors.New("squad SOP run does not match the locked issue assignment")
	}

	agent, err := queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          task.AgentID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return nil, fmt.Errorf("load Squad SOP task agent: %w", err)
	}
	steps := parseSquadSOPProfileSteps(run.Profile)
	step, stepIndex, matches := matchSquadSOPStepForAgentRecord(steps, agent)
	if !matches {
		return nil, nil
	}

	duration := pgtype.Int8{}
	if task.StartedAt.Valid && task.CompletedAt.Valid {
		duration = pgtype.Int8{
			Int64: task.CompletedAt.Time.Sub(task.StartedAt.Time).Milliseconds(),
			Valid: true,
		}
	}
	terminalEvent, err := queries.UpsertAutomaticSquadSOPTerminalEvent(ctx, db.UpsertAutomaticSquadSOPTerminalEventParams{
		RunID:       run.ID,
		WorkspaceID: run.WorkspaceID,
		IssueID:     run.IssueID,
		SquadID:     run.SquadID,
		StepKey:     step.Key,
		StepName:    step.Name,
		RoleKey:     step.RoleKey,
		EventType:   outcome.eventType,
		Status:      outcome.eventStatus,
		Reason:      squadSOPTerminalEventReason(outcome.eventType),
		TaskID:      task.ID,
		DurationMs:  duration,
	})
	if err != nil {
		return nil, fmt.Errorf("persist automatic Squad SOP terminal event: %w", err)
	}

	projectedRun := run
	if run.Status != squadSOPStatusCompleted && run.Status != squadSOPStatusFailed {
		nextStatus, nextStepKey, shouldUpdate := nextSquadSOPStateForTaskEvent(
			issue,
			steps,
			stepIndex,
			step.Key,
			outcome.eventType,
		)
		if shouldUpdate {
			if outcome.eventType == squadSOPEventCompleted &&
				nextStatus == squadSOPStatusCompleted &&
				squadSOPFinalOutputBlocked(outcome.result) {
				nextStatus = "已阻塞"
			}
			projectedRun, err = queries.UpdateSquadSOPRunStatus(ctx, db.UpdateSquadSOPRunStatusParams{
				ID:             run.ID,
				WorkspaceID:    run.WorkspaceID,
				Status:         nextStatus,
				CurrentStepKey: pgtype.Text{String: nextStepKey, Valid: nextStepKey != ""},
			})
			if err != nil {
				return nil, fmt.Errorf("project automatic Squad SOP run state: %w", err)
			}
		}
	}

	projection := &squadSOPTerminalProjection{}
	projection.issueStatus, projection.commentEvent, err = s.projectSquadSOPIssueState(
		ctx,
		queries,
		task,
		issue,
		projectedRun.Status,
	)
	if err != nil {
		return nil, err
	}
	projectedIssue := issue
	if projection.issueStatus != nil {
		projectedIssue = projection.issueStatus.updated
	}
	if outcome.eventType == squadSOPEventCompleted && !leaderProjectionRecorded(terminalEvent.Evidence) {
		decision := "not_required"
		if projectedRun.Status != squadSOPStatusFailed {
			projection.leaderTask, decision, err = s.projectSquadLeaderContinuation(
				ctx,
				queries,
				task,
				projectedIssue,
				projectedRun,
			)
			if err != nil {
				return nil, err
			}
		}
		if err := recordLeaderProjectionDecision(ctx, queries, terminalEvent, decision, projection.leaderTask); err != nil {
			return nil, err
		}
	}
	return projection, nil
}

func leaderProjectionRecorded(evidence []byte) bool {
	if len(evidence) == 0 {
		return false
	}
	var fields map[string]any
	if json.Unmarshal(evidence, &fields) != nil {
		return false
	}
	decision, ok := fields[leaderProjectionKey].(string)
	return ok && decision != ""
}

func recordLeaderProjectionDecision(
	ctx context.Context,
	queries *db.Queries,
	event db.SquadSopStepEvent,
	decision string,
	leaderTask *db.AgentTaskQueue,
) error {
	fields := map[string]any{}
	if len(event.Evidence) > 0 {
		if err := json.Unmarshal(event.Evidence, &fields); err != nil {
			return fmt.Errorf("decode automatic Squad SOP event evidence: %w", err)
		}
	}
	fields[leaderProjectionKey] = decision
	if leaderTask != nil {
		fields["leader_task_id"] = util.UUIDToString(leaderTask.ID)
	}
	evidence, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encode automatic Squad SOP event evidence: %w", err)
	}
	if _, err := queries.UpdateAutomaticSquadSOPTerminalEventEvidence(ctx, db.UpdateAutomaticSquadSOPTerminalEventEvidenceParams{
		ID:       event.ID,
		Evidence: evidence,
	}); err != nil {
		return fmt.Errorf("record Squad leader projection decision: %w", err)
	}
	return nil
}

func lockSquadSOPRunForTerminal(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
	eventType string,
	repairExisting bool,
) (db.SquadSopRun, bool, error) {
	run, err := queries.LockSquadSOPRunForAutomaticTaskEvent(ctx, db.LockSquadSOPRunForAutomaticTaskEventParams{
		TaskID:    task.ID,
		EventType: eventType,
	})
	if err == nil {
		return run, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.SquadSopRun{}, false, fmt.Errorf("lock Squad SOP run for existing terminal event: %w", err)
	}
	if repairExisting {
		// A legacy task can be terminal without its terminal event because the
		// old projection committed each write independently. Its start event is
		// the remaining durable task -> run provenance. Without either event,
		// guessing the Issue's latest open run could corrupt a run created after
		// the Issue was reassigned, so an unprovable replay is a safe no-op.
		run, err = queries.LockSquadSOPRunForAutomaticTaskEvent(ctx, db.LockSquadSOPRunForAutomaticTaskEventParams{
			TaskID:    task.ID,
			EventType: squadSOPEventStarted,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return db.SquadSopRun{}, false, nil
		}
		if err != nil {
			return db.SquadSopRun{}, false, fmt.Errorf("lock Squad SOP run for task start event: %w", err)
		}
		return run, true, nil
	}
	run, err = queries.LockOpenSquadSOPRunByIssue(ctx, task.IssueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.SquadSopRun{}, false, nil
	}
	if err != nil {
		return db.SquadSopRun{}, false, fmt.Errorf("lock open Squad SOP run: %w", err)
	}
	return run, true, nil
}

func squadSOPTerminalEventReason(eventType string) string {
	switch eventType {
	case squadSOPEventCompleted:
		return "Agent task 已完成，自动记录 SOP 阶段完成。"
	case squadSOPEventFailed:
		return "Agent task 已失败，自动记录 SOP 阶段失败。"
	default:
		return "Agent task 状态自动同步到 SOP 阶段。"
	}
}

func (s *TaskService) projectSquadSOPIssueState(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
	issue db.Issue,
	runStatus string,
) (*taskIssueStatusProjection, *events.Event, error) {
	switch runStatus {
	case squadSOPStatusFailed, "已阻塞":
		projection, err := s.projectSquadSOPIssueStatus(ctx, queries, issue, "blocked", "squad_sop_blocked")
		return projection, nil, err
	case squadSOPStatusCompleted:
		return s.projectCompletedSquadSOPIssue(ctx, queries, task, issue)
	default:
		return nil, nil, nil
	}
}

func (s *TaskService) projectCompletedSquadSOPIssue(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
	issue db.Issue,
) (*taskIssueStatusProjection, *events.Event, error) {
	if issue.Status == "done" || issue.Status == "cancelled" {
		return nil, nil, nil
	}
	pullRequests, err := queries.ListPullRequestsByIssue(ctx, issue.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("load pull requests for completed Squad SOP: %w", err)
	}
	requiresMR, err := issueRequiresGongfengMRWithQueries(ctx, queries, issue)
	if err != nil {
		return nil, nil, err
	}
	if issue.Status == "blocked" {
		if requiresMR && len(pullRequests) == 0 {
			commentEvent, err := projectMissingMRGateComment(ctx, queries, task, issue)
			return nil, commentEvent, err
		}
		return nil, nil, nil
	}
	if requiresMR && len(pullRequests) == 0 {
		statusProjection, err := s.projectSquadSOPIssueStatus(
			ctx,
			queries,
			issue,
			"blocked",
			"squad_sop_missing_merge_request",
		)
		if err != nil || statusProjection == nil {
			return statusProjection, nil, err
		}
		commentEvent, err := projectMissingMRGateComment(
			ctx,
			queries,
			task,
			statusProjection.updated,
		)
		return statusProjection, commentEvent, err
	}
	if !requiresMR && len(pullRequests) > 0 {
		return nil, nil, nil
	}
	statusProjection, err := s.projectSquadSOPIssueStatus(
		ctx,
		queries,
		issue,
		"done",
		"squad_sop_completed",
	)
	return statusProjection, nil, err
}

func (s *TaskService) projectSquadSOPIssueStatus(
	ctx context.Context,
	queries *db.Queries,
	issue db.Issue,
	status string,
	reason string,
) (*taskIssueStatusProjection, error) {
	if issue.Status == status || issue.Status == "done" || issue.Status == "cancelled" || issue.Status == "blocked" {
		return nil, nil
	}
	if status == "done" {
		children, err := queries.ListChildIssues(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("load child issues before completed Squad SOP close: %w", err)
		}
		for _, child := range children {
			if child.Status != "done" {
				// An incomplete child is a current product constraint, not an I/O
				// failure. Keep the parent open while still committing the task and
				// run terminal state atomically.
				return nil, nil
			}
		}
	}
	return s.projectTaskIssueStatus(ctx, queries, issue, status, reason)
}

func issueRequiresGongfengMRWithQueries(
	ctx context.Context,
	queries *db.Queries,
	issue db.Issue,
) (bool, error) {
	if !issue.ProjectID.Valid {
		return false, nil
	}
	resources, err := queries.ListProjectResources(ctx, issue.ProjectID)
	if err != nil {
		return false, fmt.Errorf("load project resources for completed Squad SOP: %w", err)
	}
	for _, resource := range resources {
		if resource.ResourceType == "gongfeng_repo" {
			return true, nil
		}
	}
	return false, nil
}

func projectMissingMRGateComment(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
	issue db.Issue,
) (*events.Event, error) {
	_, err := queries.GetSystemCommentByIssueAndContent(ctx, db.GetSystemCommentByIssueAndContentParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Content:     missingMRGateComment,
	})
	if err == nil {
		return nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("find existing missing-MR gate comment: %w", err)
	}
	comment, err := queries.CreateComment(ctx, db.CreateCommentParams{
		IssueID:      issue.ID,
		WorkspaceID:  issue.WorkspaceID,
		AuthorType:   "system",
		AuthorID:     util.MustParseUUID("00000000-0000-0000-0000-000000000000"),
		Content:      missingMRGateComment,
		Type:         "comment",
		SourceTaskID: task.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("create missing-MR gate comment: %w", err)
	}
	event, err := eventoutbox.Enqueue(ctx, queries, taskCommentCreatedEvent(issue, comment, "system", ""))
	if err != nil {
		return nil, fmt.Errorf("enqueue missing-MR gate comment event: %w", err)
	}
	return &event, nil
}

func (s *TaskService) projectSquadLeaderContinuation(
	ctx context.Context,
	queries *db.Queries,
	workerTask db.AgentTaskQueue,
	issue db.Issue,
	run db.SquadSopRun,
) (*db.AgentTaskQueue, string, error) {
	if workerTask.IsLeaderTask || issue.Status == "done" || issue.Status == "cancelled" {
		return nil, "not_required", nil
	}
	squad, err := queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          run.SquadID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return nil, "", fmt.Errorf("load Squad for leader continuation: %w", err)
	}
	leader, err := queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          squad.LeaderID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return nil, "", fmt.Errorf("load Squad leader for continuation: %w", err)
	}
	if leader.ArchivedAt.Valid {
		return nil, "", errors.New("cannot enqueue Squad continuation for an archived leader")
	}
	if !leader.RuntimeID.Valid {
		return nil, "", errors.New("cannot enqueue Squad continuation for a leader without a runtime")
	}
	hasPending, err := queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: issue.ID,
		AgentID: squad.LeaderID,
	})
	if err != nil {
		return nil, "", fmt.Errorf("check pending Squad leader continuation: %w", err)
	}
	if hasPending {
		return nil, "coalesced", nil
	}
	continuationContext, err := json.Marshal(map[string]string{
		"type":           "squad_sop_leader_continuation",
		"source_task_id": util.UUIDToString(workerTask.ID),
	})
	if err != nil {
		return nil, "", fmt.Errorf("encode Squad leader continuation provenance: %w", err)
	}
	nextTask, err := queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		AgentID:        squad.LeaderID,
		RuntimeID:      leader.RuntimeID,
		IssueID:        issue.ID,
		Priority:       priorityToInt(issue.Priority),
		TriggerSummary: pgtype.Text{String: "SOP 阶段任务已完成，继续协调下一阶段。", Valid: true},
		IsLeaderTask:   pgtype.Bool{Bool: true, Valid: true},
		Context:        continuationContext,
	})
	if err != nil {
		return nil, "", fmt.Errorf("create Squad leader continuation: %w", err)
	}
	return &nextTask, "created", nil
}

func (s *TaskService) publishSquadSOPTerminalProjection(
	ctx context.Context,
	projection *squadSOPTerminalProjection,
) {
	if projection == nil {
		return
	}
	s.publishTaskIssueStatusProjection(ctx, projection.issueStatus)
	if projection.commentEvent != nil && s.Bus != nil {
		s.Bus.Publish(*projection.commentEvent)
	}
	if projection.leaderTask == nil {
		return
	}
	slog.Info("squad leader enqueued after worker stage completion",
		"task_id", util.UUIDToString(projection.leaderTask.ID),
		"issue_id", util.UUIDToString(projection.leaderTask.IssueID),
		"leader_id", util.UUIDToString(projection.leaderTask.AgentID),
	)
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, *projection.leaderTask)
	s.NotifyTaskEnqueued(ctx, *projection.leaderTask)
}
