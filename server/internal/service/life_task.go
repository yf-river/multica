// life_task.go —— 受治理（governed）的 Life 后台认知任务的队列行助手与终态兜底：
// 这类任务须以 life_job_complete 提交结构化输出，队列只作执行载体。
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

// ErrLifeStructuredOutputRequired 在受治理的 Life 后台任务试图使用普通 daemon 完成端点时返回。返回该错误前任务
// 已与其认知作业原子地失败；worker 会重新调度作业，而不是留下两个互相矛盾的终态。
var ErrLifeStructuredOutputRequired = errors.New("life cognition tasks must submit structured output with life_job_complete")

// lifeTaskEnvelope 是 Life 认知任务 context 中携带的执行信封（类型标记 + 作业引用/认领）。
type lifeTaskEnvelope struct {
	Type           string `json:"type"`
	JobID          string `json:"job_id"`
	ClaimToken     string `json:"claim_token"`
	ContextVersion int64  `json:"context_version_number"`
}

// lifeTaskEnvelopeForQueue 判断排队行是否声明为受治理 Life 任务并取出信封。
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

// lifeTaskActive 判断状态是否属于“Life 任务仍在活跃执行”（dispatched/running/waiting_local_directory）。
func lifeTaskActive(status string) bool {
	switch status {
	case "dispatched", "running", "waiting_local_directory":
		return true
	default:
		return false
	}
}

// failLifeCognitionTask 是受治理 Life 后台任务唯一的普通任务终态兜底。它刻意不创建平台重试或 issue/chat
// 副作用：认知作业握有自己的重试预算，会在预定退避后被再次认领。
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
		// 结构化完成在任务转换前取作业锁。这里用相同顺序，迟到的普通回调才不至于与合法的 life_job_complete
		// 请求死锁或部分获胜。
		job, jobErr := qtx.GetLifeCognitionJobForTaskForUpdate(ctx, taskID)
		if jobErr != nil && !errors.Is(jobErr, pgx.ErrNoRows) {
			return fmt.Errorf("load Life cognition job for terminal fallback: %w", jobErr)
		}

		current, currentErr := qtx.GetAgentTask(ctx, taskID)
		if currentErr != nil {
			return currentErr
		}
		if !lifeTaskActive(current.Status) {
			// 结构化完成或恢复流程赢了竞态：把普通回调当作幂等 no-op，绝不触碰作业。
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
