package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// ErrLifeStructuredOutputRequired is returned when a governed background Life
// task tries to use the ordinary daemon completion endpoint.  The task is
// failed atomically with its cognition job before this error is returned; the
// worker will schedule the job again instead of leaving two terminal states
// that disagree.
var ErrLifeStructuredOutputRequired = errors.New("life cognition tasks must submit structured output with life_job_complete")

type lifeTaskEnvelope struct {
	Type           string `json:"type"`
	JobID          string `json:"job_id"`
	ClaimToken     string `json:"claim_token"`
	ContextVersion int64  `json:"context_version_number"`
}

func lifeTaskEnvelopeForQueue(task db.AgentTaskQueue) (lifeTaskEnvelope, bool) {
	if len(task.Context) == 0 {
		return lifeTaskEnvelope{}, false
	}
	var envelope lifeTaskEnvelope
	if json.Unmarshal(task.Context, &envelope) != nil || envelope.Type != "life_cognition" {
		return lifeTaskEnvelope{}, false
	}
	return envelope, true
}

func lifeTaskActive(status string) bool {
	switch status {
	case "dispatched", "running", "waiting_local_directory":
		return true
	default:
		return false
	}
}

// failLifeCognitionTask is the sole ordinary task-terminal fallback for a
// governed Life background task.  It deliberately does not create platform
// retries or issue/chat side effects: the cognition job owns its retry budget
// and will be claimed again after its scheduled backoff.
func (s *TaskService) failLifeCognitionTask(ctx context.Context, taskID pgtype.UUID, errMsg, failureReason string) (*db.AgentTaskQueue, error) {
	errMsg = util.SanitizeTextForPostgres(strings.TrimSpace(errMsg))
	if errMsg == "" {
		errMsg = "life cognition task ended without governed structured output"
	}
	if failureReason == "" {
		failureReason = taskfailure.Classify(errMsg).String()
	}
	failureReason = taskfailure.NormalizeDaemonReason(failureReason, errMsg).String()

	var task db.AgentTaskQueue
	var terminalEvent events.Event
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		// Structured completion takes the job lock before the task transition.
		// Use the same order here so a late ordinary callback cannot deadlock or
		// partially win against a valid life_job_complete request.
		job, jobErr := qtx.GetLifeCognitionJobForTaskForUpdate(ctx, taskID)
		if jobErr != nil && !errors.Is(jobErr, pgx.ErrNoRows) {
			return fmt.Errorf("load Life cognition job for terminal fallback: %w", jobErr)
		}

		current, currentErr := qtx.GetAgentTask(ctx, taskID)
		if currentErr != nil {
			return currentErr
		}
		if !lifeTaskActive(current.Status) {
			// A structured completion or a recovery pass won the race. Treat the
			// ordinary callback as an idempotent no-op and never touch the job.
			task = current
			return nil
		}
		envelope, declared := lifeTaskEnvelopeForQueue(current)
		if !declared {
			return fmt.Errorf("task %s is not a governed Life task", util.UUIDToString(taskID))
		}

		failed, err := qtx.FailAgentTask(ctx, db.FailAgentTaskParams{
			ID:            taskID,
			Error:         pgtype.Text{String: errMsg, Valid: true},
			FailureReason: pgtype.Text{String: failureReason, Valid: true},
			SessionID:     pgtype.Text{}, WorkDir: pgtype.Text{},
			DurableWorkDir: pgtype.Text{}, BranchName: pgtype.Text{},
			RetiredSessionID: pgtype.Text{},
		})
		if err != nil {
			return err
		}
		task = failed

		if jobErr == nil && job.Status == "running" {
			var changed int64
			if job.ClaimToken.Valid && envelope.ClaimToken != "" {
				if envelope.ClaimToken != job.ClaimToken.String || envelope.ContextVersion != job.ContextVersion || envelope.JobID != util.UUIDToString(job.ID) {
					return fmt.Errorf("life cognition claim changed while reporting terminal fallback")
				}
				changed, err = qtx.FailLifeCognitionJobForTaskFenced(ctx, db.FailLifeCognitionJobForTaskFencedParams{
					ID: job.ID, TaskID: taskID, ClaimToken: job.ClaimToken,
					ContextVersion: job.ContextVersion, Error: errMsg,
				})
			} else {
				changed, err = qtx.FailLifeCognitionJobForTask(ctx, db.FailLifeCognitionJobForTaskParams{
					ID: job.ID, TaskID: taskID, Error: errMsg,
				})
			}
			if err != nil {
				return fmt.Errorf("fail Life cognition job: %w", err)
			}
			if changed != 1 {
				return fmt.Errorf("life cognition job claim is no longer current")
			}
		}

		if s.TxStarter != nil {
			terminalEvent, err = recordTaskTerminalEventTx(ctx, qtx, protocol.EventTaskFailed, task,
				taskFailedFields(errMsg, failureReason, false))
			if err != nil {
				return fmt.Errorf("record Life task failure event: %w", err)
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("fail Life cognition task: %w", err)
	}

	if task.ID.Valid && task.Status == "failed" {
		slog.Warn("Life cognition task failed through governed fallback",
			"task_id", util.UUIDToString(task.ID), "failure_reason", failureReason)
		s.captureTaskFailed(ctx, task)
		s.ReconcileAgentStatus(ctx, task.AgentID)
		s.NotifyTaskFinished(task)
		if terminalEvent.ID != "" {
			publishCommittedEvent(s.Bus, terminalEvent)
		} else {
			s.broadcastTaskFailedEvent(ctx, task, errMsg, failureReason, false)
		}
	}
	return &task, nil
}
