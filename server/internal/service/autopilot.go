package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issueposition"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TxStarter abstracts transaction creation (satisfied by pgxpool.Pool).
type TxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type AutopilotService struct {
	Queries   *db.Queries
	TxStarter TxStarter
	Bus       *events.Bus
	TaskSvc   *TaskService
}

// DefaultAutopilotTriggerTimezone is the timezone used to render Autopilot
// trigger output when a trigger has no configured timezone or the configured
// timezone fails to load. Exported so the scheduler can use the same default
// when computing next run times.
const DefaultAutopilotTriggerTimezone = "UTC"

func NewAutopilotService(q *db.Queries, tx TxStarter, bus *events.Bus, taskSvc *TaskService) *AutopilotService {
	return &AutopilotService{Queries: q, TxStarter: tx, Bus: bus, TaskSvc: taskSvc}
}

// DispatchAutopilot is the core execution entry point.
// It creates a run and either creates an issue or enqueues a direct agent task
// depending on execution_mode.
//
// Before any work is queued we run an admission check against the assignee
// agent's runtime: if it is not online, we record a `skipped` run with a
// failure_reason and return without enqueueing. This is the "触发时准入" gate
// from MUL-1899 — without it a paused laptop / offline daemon causes scheduled
// autopilots to pile thousands of doomed tasks onto agent_task_queue.
//
// When assignee_type='squad' the gate runs against the squad leader (Path A
// from MUL-2429: Autopilot-on-squad ≈ Autopilot-on-leader), so an offline or
// archived leader produces the same skip behaviour as an offline solo agent.
func (s *AutopilotService) DispatchAutopilot(
	ctx context.Context,
	autopilot db.Autopilot,
	triggerID pgtype.UUID,
	source string,
	payload []byte,
) (*db.AutopilotRun, error) {
	return s.dispatchAutopilot(ctx, autopilot, triggerID, source, payload, pgtype.UUID{})
}

// DispatchAutopilotOnce binds a caller-owned request key to one run. It is
// used by interactive/API triggers where a lost HTTP response must be safe to
// retry. Scheduled and webhook dispatch already have trigger/delivery
// identities and continue through DispatchAutopilot.
func (s *AutopilotService) DispatchAutopilotOnce(
	ctx context.Context,
	autopilot db.Autopilot,
	triggerID pgtype.UUID,
	source string,
	payload []byte,
	requestKey pgtype.UUID,
) (*db.AutopilotRun, error) {
	if !requestKey.Valid {
		return nil, errors.New("autopilot dispatch request key is required")
	}
	return s.dispatchAutopilot(ctx, autopilot, triggerID, source, payload, requestKey)
}

func (s *AutopilotService) dispatchAutopilot(
	ctx context.Context,
	autopilot db.Autopilot,
	triggerID pgtype.UUID,
	source string,
	payload []byte,
	requestKey pgtype.UUID,
) (*db.AutopilotRun, error) {
	if requestKey.Valid {
		if replay, err := s.awaitAutopilotRunReplay(ctx, autopilot, source, requestKey); err == nil {
			return &replay, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("load autopilot dispatch replay: %w", err)
		}
	}
	if reason, skip := s.shouldSkipDispatch(ctx, autopilot); skip {
		return s.recordSkippedRun(ctx, autopilot, triggerID, source, payload, requestKey, reason)
	}

	// Determine initial status based on execution mode.
	initialStatus := "issue_created"
	if autopilot.ExecutionMode == "run_only" {
		initialStatus = "running"
	}

	run, err := s.Queries.CreateAutopilotRun(ctx, db.CreateAutopilotRunParams{
		AutopilotID:    autopilot.ID,
		TriggerID:      triggerID,
		Source:         source,
		Status:         initialStatus,
		TriggerPayload: payload,
		SquadID:        autopilotSquadAttribution(autopilot),
		RequestKey:     requestKey,
	})
	if err != nil {
		if requestKey.Valid && isAutopilotRequestKeyConflict(err) {
			replay, replayErr := s.awaitAutopilotRunReplay(ctx, autopilot, source, requestKey)
			if replayErr != nil {
				return nil, fmt.Errorf("load concurrent autopilot dispatch replay: %w", replayErr)
			}
			return &replay, nil
		}
		return nil, fmt.Errorf("create run: %w", err)
	}
	s.captureAutopilotRunStarted(autopilot, run, source)

	switch autopilot.ExecutionMode {
	case "create_issue":
		triggerTimezone := s.resolveAutopilotTriggerTimezone(ctx, triggerID)
		if err := s.dispatchCreateIssue(ctx, autopilot, &run, triggerTimezone); err != nil {
			skipped, skipErr := s.handleDispatchSkip(ctx, autopilot, &run, err)
			if skipped {
				return &run, skipErr
			}
			dispatchErr := fmt.Errorf("dispatch create_issue: %w", err)
			return &run, s.failDispatchRun(ctx, autopilot, &run, err.Error(), dispatchErr)
		}
	case "run_only":
		if err := s.dispatchRunOnly(ctx, autopilot, &run); err != nil {
			skipped, skipErr := s.handleDispatchSkip(ctx, autopilot, &run, err)
			if skipped {
				return &run, skipErr
			}
			dispatchErr := fmt.Errorf("dispatch run_only: %w", err)
			return &run, s.failDispatchRun(ctx, autopilot, &run, err.Error(), dispatchErr)
		}
	default:
		dispatchErr := fmt.Errorf("unknown execution_mode: %s", autopilot.ExecutionMode)
		return &run, s.failDispatchRun(ctx, autopilot, &run, dispatchErr.Error(), dispatchErr)
	}

	// Publish run start event.
	s.Bus.Publish(events.Event{
		Type:        protocol.EventAutopilotRunStart,
		WorkspaceID: util.UUIDToString(autopilot.WorkspaceID),
		ActorType:   "system",
		Payload: map[string]any{
			"run_id":       util.UUIDToString(run.ID),
			"autopilot_id": util.UUIDToString(autopilot.ID),
			"source":       source,
			"status":       run.Status,
		},
	})

	return &run, nil
}

// dispatchCreateIssue creates an issue and enqueues a task for the agent.
//
// When the autopilot is assigned to a squad (Path A from MUL-2429), the
// created issue inherits assignee_type='squad' + assignee_id=squad while the
// task is bound to the resolved leader. Issue, task, run link, subscribers,
// inbox rows, SOP state, and the durable Issue event commit together.
//
// Creator on the issue is always the agent that will actually do the work
// (the resolved leader for a squad autopilot, otherwise the assignee agent
// itself), so activity / mentions render with the right author identity.
func (s *AutopilotService) dispatchCreateIssue(ctx context.Context, ap db.Autopilot, run *db.AutopilotRun, triggerTimezone string) error {
	leader, _, err := s.resolveAutopilotLeader(ctx, ap)
	if err != nil {
		return fmt.Errorf("resolve leader: %w", err)
	}
	ready, reason, err := AgentReadiness(ctx, s.Queries, leader)
	if err != nil {
		return fmt.Errorf("check agent readiness: %w", err)
	}
	if !ready {
		return &errDispatchSkipped{reason: formatAdmissionReason(ap, reason)}
	}
	// Re-check the private squad leader after the admission gate and before
	// opening the write transaction. A leader rotation can happen between the
	// two reads; rejecting here prevents an inaccessible Issue from committing
	// before the dispatch is classified as skipped.
	if ap.AssigneeType == "squad" && leader.Scope == "personal" && !s.canCreatorAccessPrivateLeader(ctx, ap, leader) {
		return &errDispatchSkipped{reason: formatAdmissionReason(ap, "creator cannot access private squad leader")}
	}

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.Queries.WithTx(tx)

	title := s.interpolateTemplate(ap, *run, triggerTimezone)
	description := s.buildIssueDescription(ap, *run, triggerTimezone)

	issueNumber, err := qtx.IncrementIssueCounter(ctx, ap.WorkspaceID)
	if err != nil {
		return fmt.Errorf("increment issue counter: %w", err)
	}

	newPosition, err := issueposition.NextTopPosition(ctx, tx, ap.WorkspaceID, "todo")
	if err != nil {
		return fmt.Errorf("get next issue position: %w", err)
	}

	issue, err := qtx.CreateIssueWithOrigin(ctx, db.CreateIssueWithOriginParams{
		WorkspaceID:  ap.WorkspaceID,
		Title:        title,
		Description:  description,
		Status:       "todo",
		Priority:     "none",
		AssigneeType: pgtype.Text{String: ap.AssigneeType, Valid: true},
		AssigneeID:   ap.AssigneeID,
		// The agent that the autopilot dispatches to is the issue's creator,
		// not the human who originally configured the autopilot. The latter
		// is captured separately via origin_type=autopilot + origin_id. For
		// squad-assigned autopilots, the creator is the resolved leader —
		// the same agent the issue listener will end up enqueueing.
		CreatorType:   "agent",
		CreatorID:     leader.ID,
		ParentIssueID: pgtype.UUID{},
		Position:      newPosition,
		StartDate:     pgtype.Date{},
		DueDate:       pgtype.Date{},
		Number:        issueNumber,
		ProjectID:     ap.ProjectID,
		OriginType:    pgtype.Text{String: "autopilot", Valid: true},
		OriginID:      ap.ID,
	})
	if err != nil {
		return fmt.Errorf("create issue: %w", err)
	}
	isSquad := ap.AssigneeType == "squad"
	task, err := qtx.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		AgentID:           leader.ID,
		RuntimeID:         leader.RuntimeID,
		IssueID:           issue.ID,
		Priority:          priorityToInt(issue.Priority),
		ForceFreshSession: pgtype.Bool{Bool: isSquad, Valid: isSquad},
		IsLeaderTask:      pgtype.Bool{Bool: isSquad, Valid: isSquad},
	})
	if err != nil {
		return fmt.Errorf("create autopilot issue task: %w", err)
	}
	if isSquad {
		if err := s.TaskSvc.createSquadSOPRunForLeaderTask(ctx, qtx, issue, task); err != nil {
			return fmt.Errorf("create autopilot squad SOP state: %w", err)
		}
	}

	// Fan out the default subscriber template inside the same tx as the
	// issue insert, before EventIssueCreated fires — so notification
	// listeners see the full subscriber set on the first event instead of
	// racing the listener that would otherwise hydrate the template.
	templateSubs, err := qtx.ListAutopilotSubscribers(ctx, ap.ID)
	if err != nil {
		return fmt.Errorf("list autopilot subscribers: %w", err)
	}
	for _, sub := range templateSubs {
		if err := qtx.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
			IssueID:  issue.ID,
			UserType: sub.UserType,
			UserID:   sub.UserID,
			Reason:   "autopilot",
		}); err != nil {
			return fmt.Errorf("add autopilot subscriber to issue: %w", err)
		}
	}

	updatedRun, err := qtx.UpdateAutopilotRunIssueCreated(ctx, db.UpdateAutopilotRunIssueCreatedParams{
		ID:      run.ID,
		IssueID: issue.ID,
	})
	if err != nil {
		return fmt.Errorf("link run to issue: %w", err)
	}

	prefix := s.getIssuePrefix(ap.WorkspaceID)
	createdEvent := events.Event{
		Type:        protocol.EventIssueCreated,
		StreamKey:   "issue:" + util.UUIDToString(issue.ID),
		WorkspaceID: util.UUIDToString(ap.WorkspaceID),
		ActorType:   "agent",
		ActorID:     util.UUIDToString(leader.ID),
		Payload: map[string]any{
			"issue": issueToMap(issue, prefix),
		},
	}
	createdEvent, err = eventoutbox.Enqueue(ctx, qtx, createdEvent)
	if err != nil {
		return fmt.Errorf("enqueue autopilot issue-created event: %w", err)
	}
	inboxEvents, err := s.createAutopilotSubscriberInbox(ctx, qtx, ap, issue, leader.ID, templateSubs)
	if err != nil {
		return err
	}
	if err := qtx.UpdateAutopilotLastRunAt(ctx, ap.ID); err != nil {
		return fmt.Errorf("update autopilot last run: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	*run = updatedRun

	// Publish the committed events for realtime invalidation; durable audience,
	// activity, and notification projections consume the outbox copy.
	s.Bus.Publish(createdEvent)
	for _, event := range inboxEvents {
		s.Bus.Publish(event)
	}
	s.captureIssueCreatedFromAutopilot(ap, run, issue, leader.ID)

	s.TaskSvc.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.TaskSvc.NotifyTaskEnqueued(ctx, task)

	slog.Info("autopilot dispatched (create_issue)",
		"autopilot_id", util.UUIDToString(ap.ID),
		"assignee_type", ap.AssigneeType,
		"issue_id", util.UUIDToString(issue.ID),
		"task_id", util.UUIDToString(task.ID),
		"leader_id", util.UUIDToString(leader.ID),
		"run_id", util.UUIDToString(run.ID),
	)
	return nil
}

// notifyAutopilotSubscribersOnCreate writes an inbox_item for each template
// subscriber of an autopilot-created issue and broadcasts an inbox:new event
// so the recipient's inbox updates in real time. Mirrors the inbox payload
// shape from notification_listeners.go so the WS consumer sees the same fields
// the listener-driven path produces. These rows are part of the same
// transaction as the issue, run link, subscriber fan-out, and durable event;
// a partial Autopilot dispatch is never exposed as successful.
func (s *AutopilotService) createAutopilotSubscriberInbox(
	ctx context.Context,
	queries *db.Queries,
	ap db.Autopilot,
	issue db.Issue,
	leaderID pgtype.UUID,
	subscribers []db.AutopilotSubscriber,
) ([]events.Event, error) {
	if len(subscribers) == 0 {
		return nil, nil
	}
	details, _ := json.Marshal(map[string]string{
		"autopilot_id": util.UUIDToString(ap.ID),
		"reason":       "autopilot",
	})
	emitted := make([]events.Event, 0, len(subscribers))
	for _, sub := range subscribers {
		// Autopilot subscribers are restricted to user_type='member' at the
		// handler boundary; defend in case that constraint is ever relaxed
		// (agents don't have inbox).
		if sub.UserType != "member" {
			continue
		}
		item, err := queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
			WorkspaceID:   ap.WorkspaceID,
			RecipientType: "member",
			RecipientID:   sub.UserID,
			Type:          "issue_subscribed",
			Severity:      "info",
			IssueID:       issue.ID,
			Title:         issue.Title,
			Body:          pgtype.Text{},
			ActorType:     pgtype.Text{String: "agent", Valid: true},
			ActorID:       leaderID,
			Details:       details,
		})
		if err != nil {
			return nil, fmt.Errorf("create autopilot subscriber inbox item %s: %w", util.UUIDToString(sub.UserID), err)
		}
		emitted = append(emitted, events.Event{
			Type:        protocol.EventInboxNew,
			WorkspaceID: util.UUIDToString(ap.WorkspaceID),
			ActorType:   "agent",
			ActorID:     util.UUIDToString(leaderID),
			Payload: map[string]any{
				"item": map[string]any{
					"id":             util.UUIDToString(item.ID),
					"workspace_id":   util.UUIDToString(item.WorkspaceID),
					"recipient_type": item.RecipientType,
					"recipient_id":   util.UUIDToString(item.RecipientID),
					"type":           item.Type,
					"severity":       item.Severity,
					"issue_id":       util.UUIDToPtr(item.IssueID),
					"issue_status":   issue.Status,
					"title":          item.Title,
					"body":           util.TextToPtr(item.Body),
					"read":           item.Read,
					"archived":       item.Archived,
					"created_at":     util.TimestampToString(item.CreatedAt),
					"actor_type":     util.TextToPtr(item.ActorType),
					"actor_id":       util.UUIDToPtr(item.ActorID),
					"details":        json.RawMessage(item.Details),
				},
			},
		})
	}
	return emitted, nil
}

// errDispatchSkipped wraps a readiness failure encountered after the
// admission gate has already passed. dispatchRunOnly returns this when a
// resolved leader has gone offline / been archived between admission and
// task creation; DispatchAutopilot recognises it and records a `skipped`
// run (with the wrapped reason) instead of a `failed` run.
//
// Without the sentinel, the existing failRun path would mark these races as
// failures and bubble a 500 out of the manual-trigger handler — both wrong
// (the work was never attempted, no one is at fault) and noisy (the failure
// monitor would auto-pause autopilots whose only crime was a flaky runtime).
type errDispatchSkipped struct {
	reason string
}

func (e *errDispatchSkipped) Error() string { return e.reason }

// dispatchRunOnly enqueues a direct agent task without creating an issue.
//
// For squad autopilots, the executing agent is the squad leader resolved at
// trigger time (Path A from MUL-2429). The same archived / runtime-bound /
// runtime-online gates that the upstream admission check (shouldSkipDispatch)
// applies also run here as belt-and-braces: if the leader changed between
// admission and dispatch, or the runtime went offline in the gap, we still
// fail closed instead of enqueueing a doomed task.
func (s *AutopilotService) dispatchRunOnly(ctx context.Context, ap db.Autopilot, run *db.AutopilotRun) error {
	agent, _, err := s.resolveAutopilotLeader(ctx, ap)
	if err != nil {
		// Same admission-vs-failure classification as shouldSkipDispatch:
		// if the row disappeared or the squad was archived between
		// admission and dispatch, that is a skip, not a failure.
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, errSquadArchived) {
			return &errDispatchSkipped{reason: formatAdmissionReason(ap, "assignee no longer resolvable")}
		}
		return fmt.Errorf("resolve leader: %w", err)
	}
	ready, reason, err := AgentReadiness(ctx, s.Queries, agent)
	if err != nil {
		return fmt.Errorf("check agent readiness: %w", err)
	}
	if !ready {
		return &errDispatchSkipped{reason: formatAdmissionReason(ap, reason)}
	}

	// Fail-closed personal-leader gate for squad autopilots.
	if ap.AssigneeType == "squad" && agent.Scope == "personal" && !s.canCreatorAccessPrivateLeader(ctx, ap, agent) {
		return &errDispatchSkipped{reason: formatAdmissionReason(ap, "creator cannot access private squad leader")}
	}

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin run-only dispatch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := s.Queries.WithTx(tx)
	task, err := queries.CreateAutopilotTask(ctx, db.CreateAutopilotTaskParams{
		AgentID:        agent.ID,
		RuntimeID:      agent.RuntimeID,
		Priority:       0,
		AutopilotRunID: run.ID,
		// Snapshot the autopilot title so task rows self-describe later
		// without joining back to autopilot. Truncated for the same
		// transmission-cost reason as comment-driven summaries.
		TriggerSummary: pgtype.Text{
			String: truncateForSummary(ap.Title, triggerSummaryMaxLen),
			Valid:  ap.Title != "",
		},
	})
	if err != nil {
		return fmt.Errorf("create autopilot task: %w", err)
	}

	// Update run with task reference.
	updatedRun, err := queries.UpdateAutopilotRunRunning(ctx, db.UpdateAutopilotRunRunningParams{
		ID:     run.ID,
		TaskID: task.ID,
	})
	if err != nil {
		return fmt.Errorf("link run-only task: %w", err)
	}
	if err := queries.UpdateAutopilotLastRunAt(ctx, ap.ID); err != nil {
		return fmt.Errorf("update autopilot last run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit run-only dispatch: %w", err)
	}
	*run = updatedRun

	// Drop the empty-claim cache and wake the daemon. dispatchRunOnly
	// inserts the task row directly via Queries.CreateAutopilotTask
	// (bypassing TaskService.Enqueue*), so without this the runtime
	// would not get a wakeup and any cached "empty" verdict would
	// stall the task until the TTL expired.
	s.TaskSvc.NotifyTaskEnqueued(ctx, task)

	slog.Info("autopilot dispatched (run_only)",
		"autopilot_id", util.UUIDToString(ap.ID),
		"task_id", util.UUIDToString(task.ID),
		"run_id", util.UUIDToString(run.ID),
	)
	return nil
}

// handleDispatchSkip recognises an errDispatchSkipped returned from a
// dispatch function and rewrites the in-flight run to `skipped` (instead of
// `failed`). The bool distinguishes "not a skip" from a failed skip
// transaction, so callers never misclassify a persistence error as a normal
// dispatch failure.
//
// Lives here, not inside dispatchRunOnly, because the run row was created by
// DispatchAutopilot up the stack and the failure-vs-skip distinction is
// owned by the dispatcher entry point. Keeps dispatchRunOnly free of
// state-mutation helpers.
func (s *AutopilotService) handleDispatchSkip(ctx context.Context, ap db.Autopilot, run *db.AutopilotRun, err error) (bool, error) {
	var skipErr *errDispatchSkipped
	if !errors.As(err, &skipErr) {
		return false, nil
	}
	tx, txErr := s.TxStarter.Begin(ctx)
	if txErr != nil {
		return true, fmt.Errorf("begin skipped dispatch: %w", txErr)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := s.Queries.WithTx(tx)
	updated, txErr := queries.UpdateAutopilotRunSkipped(ctx, db.UpdateAutopilotRunSkippedParams{
		ID:            run.ID,
		FailureReason: pgtype.Text{String: skipErr.reason, Valid: true},
	})
	if txErr != nil {
		return true, fmt.Errorf("mark dispatch skipped: %w", txErr)
	}
	if txErr := queries.UpdateAutopilotLastRunAt(ctx, ap.ID); txErr != nil {
		return true, fmt.Errorf("update autopilot last run: %w", txErr)
	}
	if txErr := tx.Commit(ctx); txErr != nil {
		return true, fmt.Errorf("commit skipped dispatch: %w", txErr)
	}
	*run = updated
	slog.Info("autopilot dispatch skipped post-admission",
		"autopilot_id", util.UUIDToString(ap.ID),
		"run_id", util.UUIDToString(run.ID),
		"reason", skipErr.reason,
	)
	s.publishRunDone(util.UUIDToString(ap.WorkspaceID), updated, "skipped")
	return true, nil
}

func (s *AutopilotService) failDispatchRun(ctx context.Context, ap db.Autopilot, run *db.AutopilotRun, reason string, cause error) error {
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("begin failed dispatch: %w", err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := s.Queries.WithTx(tx)
	updated, err := queries.UpdateAutopilotRunFailed(ctx, db.UpdateAutopilotRunFailedParams{
		ID:            run.ID,
		FailureReason: pgtype.Text{String: reason, Valid: true},
	})
	if err != nil {
		return errors.Join(cause, fmt.Errorf("mark dispatch failed: %w", err))
	}
	if err := queries.UpdateAutopilotLastRunAt(ctx, ap.ID); err != nil {
		return errors.Join(cause, fmt.Errorf("update autopilot last run: %w", err))
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.Join(cause, fmt.Errorf("commit failed dispatch: %w", err))
	}
	*run = updated
	s.publishRunDone(util.UUIDToString(ap.WorkspaceID), updated, "failed")
	return cause
}

// shouldSkipDispatch is the pre-flight admission check from MUL-1899.
// Returns (reason, true) when dispatching now would only enqueue a doomed
// task — i.e. the assignee (or, for squad autopilots, the squad leader) is
// gone, archived, has no runtime bound, or its runtime is not currently
// online. Returns ("", false) on the happy path.
//
// Errors are split into two classes:
//   - pgx.ErrNoRows / errSquadArchived (the row truly doesn't exist or is
//     archived) → hard skip. Retrying won't change anything; piling failed
//     runs would pollute the failure-rate auto-pause monitor.
//   - Anything else (connection drop, statement timeout, etc.) → fail-open:
//     log + do not skip, so a transient DB hiccup never silently swallows a
//     scheduled run. Migration 096 removed the agent FK on autopilot, so an
//     agent assignee being missing is now a real condition the gate must
//     handle (previously cascade-deleted).
func (s *AutopilotService) shouldSkipDispatch(ctx context.Context, ap db.Autopilot) (string, bool) {
	if !ap.AssigneeID.Valid {
		return "autopilot has no assignee", true
	}
	agent, squadResolved, err := s.resolveAutopilotLeader(ctx, ap)
	if err != nil {
		// Hard-skip the cases where another retry will produce the same
		// outcome. Logging is unconditional so ops can still spot a run of
		// dangling rows pointing at a deleted agent / archived squad.
		missing := errors.Is(err, pgx.ErrNoRows)
		archived := errors.Is(err, errSquadArchived)
		slog.Warn("autopilot admission: failed to resolve leader",
			"autopilot_id", util.UUIDToString(ap.ID),
			"assignee_type", ap.AssigneeType,
			"assignee_id", util.UUIDToString(ap.AssigneeID),
			"missing", missing,
			"archived", archived,
			"error", err,
		)
		switch {
		case archived:
			// Squad row exists but is archived — DeleteSquad's transfer
			// should have rewritten this autopilot's assignee to the leader
			// already; surfacing the case explicitly keeps the failure
			// reason useful when something slipped past the transfer.
			return "assignee squad is archived", true
		case missing && squadResolved:
			return "assignee squad cannot be resolved", true
		case missing && !squadResolved:
			// Agent row gone. With migration 096 the FK is gone too, so
			// this is the new "agent was hard-deleted under us" case. Skip
			// rather than fail-open: we know retrying will not help.
			return "assignee agent no longer exists", true
		}
		// Transient DB error — fail-open so the next scheduler tick gets a
		// chance to succeed.
		return "", false
	}
	ready, reason, err := AgentReadiness(ctx, s.Queries, agent)
	if err != nil {
		slog.Warn("autopilot admission: failed to load runtime",
			"autopilot_id", util.UUIDToString(ap.ID),
			"runtime_id", util.UUIDToString(agent.RuntimeID),
			"error", err,
		)
		return "", false
	}
	if !ready {
		return formatAdmissionReason(ap, reason), true
	}
	// Private-agent gate at the autopilot layer. Caller identity = the
	// autopilot's creator: if the creator no longer has access to the
	// (now-private) target agent, the dispatch is recorded as `skipped`.
	// Agent-created autopilots bypass the gate to preserve A2A
	// collaboration. Errors loading the workspace member fail closed —
	// without an authoritative role the gate cannot grant access.
	//
	// For squad autopilots the gate runs against the resolved leader.
	// Leader visibility is the right thing to check — if the human creator
	// can no longer reach the leader, the autopilot would silently fail
	// even though the squad itself looks intact.
	if agent.Scope == "personal" && ap.CreatedByType == "member" {
		creatorID := util.UUIDToString(ap.CreatedByID)
		if util.UUIDToString(agent.OwnerID) != creatorID {
			member, err := s.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
				UserID:      ap.CreatedByID,
				WorkspaceID: ap.WorkspaceID,
			})
			if err != nil {
				return "autopilot creator no longer in workspace", true
			}
			if member.Role != "owner" && member.Role != "admin" {
				return "autopilot creator lacks access to private assignee agent", true
			}
		}
	}
	return "", false
}

// formatAdmissionReason rewrites the generic AgentReadiness reason into the
// admission-gate phrasing the failure monitor and existing alerting are tuned
// for. Keeping the prefix stable matters: dashboards group skip reasons by
// substring ("offline at dispatch time" is how the MUL-1899 alert fires).
//
// For squad autopilots the message names the squad so an operator looking at
// the failure_reason field knows which squad's leader is down without
// joining back to autopilot_run.squad_id.
func formatAdmissionReason(ap db.Autopilot, raw string) string {
	prefix := "assignee "
	if ap.AssigneeType == "squad" {
		prefix = "squad leader "
	}
	switch raw {
	case "agent is archived":
		return prefix + "agent is archived"
	case "agent has no runtime bound":
		return prefix + "agent has no runtime bound"
	default:
		// raw is "agent runtime is X" — surface the runtime status while
		// preserving the legacy "at dispatch time" suffix from MUL-1899
		// so alert queries do not need to change.
		return raw + " at dispatch time"
	}
}

// errSquadArchived signals that an autopilot's squad assignee has been
// archived. Distinct from a missing/loadable-but-failed squad so the
// admission gate can phrase the skip reason precisely and the failure
// monitor does not see "cannot be resolved" wear noise for what is a
// known, expected post-archive condition.
var errSquadArchived = errors.New("squad is archived")

// resolveAutopilotLeader returns the agent that will actually execute the
// autopilot's work. For assignee_type='agent' the agent is the assignee
// itself; for assignee_type='squad' it is the squad's leader_id. The second
// return is true when the resolver took the squad branch — callers use this
// to distinguish "failed loading an agent" from "failed loading a squad", so
// the admission gate can choose between fail-open (transient DB error on a
// known-good agent) and fail-closed (squad row gone, no point retrying).
//
// Archived squads are rejected here too: TransferSquadAutopilotsToLeader
// flips surviving autopilots to assignee_type='agent' on DeleteSquad, but
// the gate still has to fail closed for any row that slips through that
// transfer (e.g. squad archived through a code path that bypasses the
// handler) so an archived squad never produces work.
//
// Unknown assignee_type values return an error. assignee_type is gated by a
// CHECK constraint at the DB layer, so this only fires if a future code path
// inserts a row that bypasses the check.
func (s *AutopilotService) resolveAutopilotLeader(ctx context.Context, ap db.Autopilot) (agent db.Agent, squadResolved bool, err error) {
	switch ap.AssigneeType {
	case "", "agent":
		agent, err = s.Queries.GetAgent(ctx, ap.AssigneeID)
		return agent, false, err
	case "squad":
		squad, err := s.Queries.GetSquad(ctx, ap.AssigneeID)
		if err != nil {
			return db.Agent{}, true, fmt.Errorf("load squad: %w", err)
		}
		if squad.ArchivedAt.Valid {
			return db.Agent{}, true, errSquadArchived
		}
		agent, err = s.Queries.GetAgent(ctx, squad.LeaderID)
		if err != nil {
			return db.Agent{}, true, fmt.Errorf("load squad leader: %w", err)
		}
		return agent, true, nil
	default:
		return db.Agent{}, false, fmt.Errorf("unknown assignee_type %q", ap.AssigneeType)
	}
}

// autopilotSquadAttribution returns the squad_id attribution hook for an
// autopilot_run row. Only populated when assignee_type='squad'. First-version
// reports do not consume this; it exists so a future squad-cost view does not
// need to backfill — see RFC §4.e (MUL-2429).
func autopilotSquadAttribution(ap db.Autopilot) pgtype.UUID {
	if ap.AssigneeType == "squad" && ap.AssigneeID.Valid {
		return ap.AssigneeID
	}
	return pgtype.UUID{}
}

// recordSkippedRun persists a `skipped` autopilot_run with the given reason
// and emits the same WS / analytics signals that a normal terminal transition
// would. Returns the run + nil error so callers (scheduler tick, manual
// trigger handler) treat this as a successful — but no-op — dispatch.
func (s *AutopilotService) recordSkippedRun(
	ctx context.Context,
	autopilot db.Autopilot,
	triggerID pgtype.UUID,
	source string,
	payload []byte,
	requestKey pgtype.UUID,
	reason string,
) (*db.AutopilotRun, error) {
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin skipped run: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := s.Queries.WithTx(tx)
	run, err := queries.CreateAutopilotRun(ctx, db.CreateAutopilotRunParams{
		AutopilotID:    autopilot.ID,
		TriggerID:      triggerID,
		Source:         source,
		Status:         "skipped",
		TriggerPayload: payload,
		SquadID:        autopilotSquadAttribution(autopilot),
		RequestKey:     requestKey,
	})
	if err != nil {
		if requestKey.Valid && isAutopilotRequestKeyConflict(err) {
			_ = tx.Rollback(ctx)
			replay, replayErr := s.awaitAutopilotRunReplay(ctx, autopilot, source, requestKey)
			if replayErr != nil {
				return nil, fmt.Errorf("load concurrent skipped dispatch replay: %w", replayErr)
			}
			return &replay, nil
		}
		return nil, fmt.Errorf("create skipped run: %w", err)
	}

	updated, err := queries.UpdateAutopilotRunSkipped(ctx, db.UpdateAutopilotRunSkippedParams{
		ID:            run.ID,
		FailureReason: pgtype.Text{String: reason, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("set skipped run reason: %w", err)
	}
	if err := queries.UpdateAutopilotLastRunAt(ctx, autopilot.ID); err != nil {
		return nil, fmt.Errorf("update autopilot last run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit skipped run: %w", err)
	}
	run = updated

	slog.Info("autopilot dispatch skipped",
		"autopilot_id", util.UUIDToString(autopilot.ID),
		"run_id", util.UUIDToString(run.ID),
		"source", source,
		"reason", reason,
	)

	s.publishRunDone(util.UUIDToString(autopilot.WorkspaceID), run, "skipped")
	return &run, nil
}

func (s *AutopilotService) loadAutopilotRunReplay(
	ctx context.Context,
	autopilotID pgtype.UUID,
	source string,
	requestKey pgtype.UUID,
) (db.AutopilotRun, error) {
	return s.Queries.GetAutopilotRunByRequestKey(ctx, db.GetAutopilotRunByRequestKeyParams{
		AutopilotID: autopilotID,
		Source:      source,
		RequestKey:  requestKey,
	})
}

func (s *AutopilotService) awaitAutopilotRunReplay(
	ctx context.Context,
	autopilot db.Autopilot,
	source string,
	requestKey pgtype.UUID,
) (db.AutopilotRun, error) {
	run, err := s.loadAutopilotRunReplay(ctx, autopilot.ID, source, requestKey)
	if err != nil || autopilotRunMaterialized(autopilot, run) {
		return run, err
	}

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return db.AutopilotRun{}, ctx.Err()
		case <-timeout.C:
			// The unique run remains the authoritative outcome even if the
			// original process died between reservation and dispatch. Returning
			// it is safer than creating a second Issue or task.
			return run, nil
		case <-ticker.C:
			run, err = s.loadAutopilotRunReplay(ctx, autopilot.ID, source, requestKey)
			if err != nil {
				return db.AutopilotRun{}, err
			}
			if autopilotRunMaterialized(autopilot, run) {
				return run, nil
			}
		}
	}
}

func autopilotRunMaterialized(autopilot db.Autopilot, run db.AutopilotRun) bool {
	switch run.Status {
	case "completed", "failed", "skipped":
		return true
	}
	if autopilot.ExecutionMode == "create_issue" {
		return run.IssueID.Valid
	}
	if autopilot.ExecutionMode == "run_only" {
		return run.TaskID.Valid
	}
	return true
}

func isAutopilotRequestKeyConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		pgErr.ConstraintName == "autopilot_run_request_key_unique"
}

func (s *AutopilotService) publishRunDone(workspaceID string, run db.AutopilotRun, status string) {
	s.Bus.Publish(events.Event{
		Type:        protocol.EventAutopilotRunDone,
		WorkspaceID: workspaceID,
		ActorType:   "system",
		Payload: map[string]any{
			"run_id":       util.UUIDToString(run.ID),
			"autopilot_id": util.UUIDToString(run.AutopilotID),
			"status":       status,
		},
	})
}

func (s *AutopilotService) captureIssueCreatedFromAutopilot(ap db.Autopilot, run *db.AutopilotRun, issue db.Issue, leaderID pgtype.UUID) {
	if s.TaskSvc == nil || s.TaskSvc.Analytics == nil {
		return
	}
	// For PostHog the agent_id should be the agent that will actually run
	// the work (the resolved leader for squad autopilots) so per-agent task
	// counts line up with what daemons report.
	obsmetrics.RecordEvent(s.TaskSvc.Analytics, s.TaskSvc.Metrics, analytics.IssueCreated(
		autopilotActorID(ap),
		util.UUIDToString(ap.WorkspaceID),
		util.UUIDToString(issue.ID),
		util.UUIDToString(leaderID),
		"",
		util.UUIDToString(run.ID),
		analytics.SourceAutopilot,
		analytics.PlatformServer,
	))
}

func (s *AutopilotService) captureAutopilotRunStarted(ap db.Autopilot, run db.AutopilotRun, triggerSource string) {
	if s.TaskSvc == nil || s.TaskSvc.Analytics == nil {
		return
	}
	obsmetrics.RecordEvent(s.TaskSvc.Analytics, s.TaskSvc.Metrics, analytics.AutopilotRunStarted(
		autopilotActorID(ap),
		util.UUIDToString(ap.WorkspaceID),
		util.UUIDToString(ap.ID),
		util.UUIDToString(run.ID),
		triggerSource, // cadence proxy: see autopilot cadence note in metrics/labels_pr3.go
		s.autopilotAssigneeAnalytics(ap),
		triggerSource,
	))
}

func (s *AutopilotService) captureAutopilotRunCompleted(ap db.Autopilot, run db.AutopilotRun) {
	if s.TaskSvc == nil || s.TaskSvc.Analytics == nil {
		return
	}
	obsmetrics.RecordEvent(s.TaskSvc.Analytics, s.TaskSvc.Metrics, analytics.AutopilotRunCompleted(
		autopilotActorID(ap),
		util.UUIDToString(ap.WorkspaceID),
		util.UUIDToString(ap.ID),
		util.UUIDToString(run.ID),
		run.Source,
		s.autopilotAssigneeAnalytics(ap),
		run.Source,
		autopilotRunDurationMS(run),
	))
}

func (s *AutopilotService) captureAutopilotRunFailed(ap db.Autopilot, run db.AutopilotRun, triggerSource, reason string) {
	if s.TaskSvc == nil || s.TaskSvc.Analytics == nil {
		return
	}
	if reason == "" {
		reason = "unknown"
	}
	obsmetrics.RecordEvent(s.TaskSvc.Analytics, s.TaskSvc.Metrics, analytics.AutopilotRunFailed(
		autopilotActorID(ap),
		util.UUIDToString(ap.WorkspaceID),
		util.UUIDToString(ap.ID),
		util.UUIDToString(run.ID),
		triggerSource,
		s.autopilotAssigneeAnalytics(ap),
		triggerSource,
		reason,
		autopilotErrorType(reason),
		false,
		autopilotRunDurationMS(run),
	))
}

// autopilotAssigneeAnalytics builds the PostHog assignee descriptor for an
// autopilot. For squad autopilots agent_id is best-effort the resolved
// leader (so per-agent funnels stay consistent); a resolve error degrades
// to the raw assignee_id rather than dropping the event — incomplete data
// in the dashboard is preferable to silent attribution gaps.
func (s *AutopilotService) autopilotAssigneeAnalytics(ap db.Autopilot) analytics.AutopilotAssignee {
	assignee := analytics.AutopilotAssignee{
		AssigneeType: ap.AssigneeType,
	}
	if ap.AssigneeType == "squad" {
		assignee.SquadID = util.UUIDToString(ap.AssigneeID)
		if leader, _, err := s.resolveAutopilotLeader(context.Background(), ap); err == nil {
			assignee.AgentID = util.UUIDToString(leader.ID)
		} else {
			assignee.AgentID = util.UUIDToString(ap.AssigneeID)
		}
	} else {
		assignee.AgentID = util.UUIDToString(ap.AssigneeID)
	}
	return assignee
}

func autopilotErrorType(reason string) string {
	switch {
	case strings.Contains(reason, "unknown execution_mode"):
		return "configuration"
	case strings.HasPrefix(reason, "issue "):
		return "issue_terminal"
	case strings.Contains(reason, "create issue"), strings.Contains(reason, "enqueue task"), strings.Contains(reason, "dispatch"):
		return "dispatch_error"
	case strings.HasPrefix(reason, "task "):
		return "task_error"
	default:
		return "autopilot_error"
	}
}

func autopilotActorID(ap db.Autopilot) string {
	id := util.UUIDToString(ap.CreatedByID)
	if ap.CreatedByType == "agent" && id != "" {
		return "agent:" + id
	}
	if id != "" {
		return id
	}
	return "system"
}

func autopilotRunDurationMS(run db.AutopilotRun) int64 {
	if !run.CompletedAt.Valid {
		return 0
	}
	start := run.TriggeredAt
	if !start.Valid {
		start = run.CreatedAt
	}
	if !start.Valid {
		return 0
	}
	ms := run.CompletedAt.Time.Sub(start.Time).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}

func (s *AutopilotService) resolveAutopilotTriggerTimezone(ctx context.Context, triggerID pgtype.UUID) string {
	if !triggerID.Valid || s == nil || s.Queries == nil {
		return DefaultAutopilotTriggerTimezone
	}

	trigger, err := s.Queries.GetAutopilotTrigger(ctx, triggerID)
	if err != nil {
		slog.Warn("failed to load autopilot trigger timezone; falling back to UTC",
			"trigger_id", util.UUIDToString(triggerID),
			"error", err,
		)
		return DefaultAutopilotTriggerTimezone
	}

	timezone := strings.TrimSpace(trigger.Timezone.String)
	if !trigger.Timezone.Valid || timezone == "" {
		return DefaultAutopilotTriggerTimezone
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		slog.Warn("invalid autopilot trigger timezone; falling back to UTC",
			"trigger_id", util.UUIDToString(triggerID),
			"timezone", timezone,
			"error", err,
		)
		return DefaultAutopilotTriggerTimezone
	}
	return timezone
}

func formatAutopilotRunTimestamp(run db.AutopilotRun, timezone string) string {
	triggeredAt := autopilotRunTriggeredAt(run)
	loc, label := autopilotTriggerLocation(timezone)
	return triggeredAt.In(loc).Format("2006-01-02 15:04") + " " + label
}

func formatAutopilotRunDate(run db.AutopilotRun, timezone string) string {
	triggeredAt := autopilotRunTriggeredAt(run)
	loc, _ := autopilotTriggerLocation(timezone)
	return triggeredAt.In(loc).Format("2006-01-02")
}

func autopilotRunTriggeredAt(run db.AutopilotRun) time.Time {
	if run.TriggeredAt.Valid {
		return run.TriggeredAt.Time
	}
	if run.CreatedAt.Valid {
		return run.CreatedAt.Time
	}
	return time.Now().UTC()
}

func autopilotTriggerLocation(timezone string) (*time.Location, string) {
	label := strings.TrimSpace(timezone)
	if label == "" {
		label = DefaultAutopilotTriggerTimezone
	}
	loc, err := time.LoadLocation(label)
	if err != nil {
		return time.UTC, DefaultAutopilotTriggerTimezone
	}
	return loc, label
}

// buildIssueDescription appends an autopilot system instruction to the
// user-provided description, asking the agent to rename the issue after
// it understands the actual work. For webhook-sourced runs, also appends
// a payload section so the agent has the event context inline (otherwise
// the agent only sees the issue body, never the run's trigger_payload).
func (s *AutopilotService) buildIssueDescription(ap db.Autopilot, run db.AutopilotRun, triggerTimezone string) pgtype.Text {
	triggeredAt := formatAutopilotRunTimestamp(run, triggerTimezone)
	var b strings.Builder
	b.WriteString(ap.Description.String)
	b.WriteString("\n\n---\n*Autopilot run triggered at ")
	b.WriteString(triggeredAt)
	b.WriteString(". After starting work, rename this issue to accurately reflect what you are doing.*")

	if run.Source == "webhook" && len(run.TriggerPayload) > 0 {
		event := "webhook.received"
		var payloadJSON []byte
		var env struct {
			Event        string          `json:"event"`
			EventPayload json.RawMessage `json:"eventPayload"`
		}
		if err := json.Unmarshal(run.TriggerPayload, &env); err == nil {
			if env.Event != "" {
				event = env.Event
			}
			if len(env.EventPayload) > 0 {
				if pretty, err := prettifyJSON(env.EventPayload); err == nil {
					payloadJSON = pretty
				}
			}
		}
		if len(payloadJSON) == 0 {
			if pretty, err := prettifyJSON(run.TriggerPayload); err == nil {
				payloadJSON = pretty
			} else {
				payloadJSON = run.TriggerPayload
			}
		}
		b.WriteString("\n\nWebhook event: ")
		b.WriteString(event)
		b.WriteString("\n\nWebhook payload:\n```json\n")
		b.Write(payloadJSON)
		b.WriteString("\n```")
	}

	return pgtype.Text{String: b.String(), Valid: true}
}

func prettifyJSON(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.MarshalIndent(v, "", "  ")
}

// issueTitleTemplateTokenRE matches any {{...}} token in an issue-title
// template. We deliberately permit whitespace inside the braces ({{ date }})
// so users can format templates either way; the canonical token is still
// {{date}}.
var issueTitleTemplateTokenRE = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)

// interpolateTemplate substitutes supported {{name}} placeholders in the
// issue title template. Whitespace inside the braces ({{ date }}) is
// tolerated so the render layer accepts every form that
// ValidateIssueTitleTemplate accepts — otherwise users would save templates
// that pass validation but still emit a literal token at trigger time.
func (s *AutopilotService) interpolateTemplate(ap db.Autopilot, run db.AutopilotRun, triggerTimezone string) string {
	tmpl := ap.Title
	if ap.IssueTitleTemplate.Valid && ap.IssueTitleTemplate.String != "" {
		tmpl = ap.IssueTitleTemplate.String
	}
	triggerDate := formatAutopilotRunDate(run, triggerTimezone)
	return issueTitleTemplateTokenRE.ReplaceAllStringFunc(tmpl, func(match string) string {
		name := strings.TrimSpace(match[2 : len(match)-2])
		switch name {
		case "date":
			return triggerDate
		default:
			return match
		}
	})
}

// SupportedIssueTitleTemplateVariables enumerates the placeholders that
// interpolateTemplate will substitute. Keep this in sync with the
// substitution logic above and with the docs in autopilots.mdx /
// autopilots.zh.mdx.
var SupportedIssueTitleTemplateVariables = []string{"date"}

// ValidateIssueTitleTemplate rejects templates that contain any {{...}} token
// other than the supported set. An empty template is valid (the autopilot
// falls back to its own Title). The error message names the first offending
// token to keep CLI feedback actionable.
func ValidateIssueTitleTemplate(tmpl string) error {
	if tmpl == "" {
		return nil
	}
	for _, m := range issueTitleTemplateTokenRE.FindAllStringSubmatch(tmpl, -1) {
		name := m[1]
		if !isSupportedIssueTitleVariable(name) {
			return fmt.Errorf(
				"unknown template variable %q; supported: {{%s}}",
				name,
				strings.Join(SupportedIssueTitleTemplateVariables, "}}, {{"),
			)
		}
	}
	return nil
}

func isSupportedIssueTitleVariable(name string) bool {
	for _, v := range SupportedIssueTitleTemplateVariables {
		if name == v {
			return true
		}
	}
	return false
}

func (s *AutopilotService) getIssuePrefix(workspaceID pgtype.UUID) string {
	ws, err := s.Queries.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return ""
	}
	return ws.IssuePrefix
}

// canCreatorAccessPrivateLeader checks whether the autopilot's creator still
// has access to a personal leader agent. Mirrors handler.canAccessPersonalAgent
// logic: agent creators always pass; member creators must be the agent owner
// or a workspace owner/admin. Returns false (fail-closed) on any lookup error.
func (s *AutopilotService) canCreatorAccessPrivateLeader(ctx context.Context, ap db.Autopilot, leader db.Agent) bool {
	if ap.CreatedByType == "agent" {
		return true
	}
	creatorID := util.UUIDToString(ap.CreatedByID)
	if util.UUIDToString(leader.OwnerID) == creatorID {
		return true
	}
	member, err := s.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      ap.CreatedByID,
		WorkspaceID: ap.WorkspaceID,
	})
	if err != nil {
		return false
	}
	return member.Role == "owner" || member.Role == "admin"
}
