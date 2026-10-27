package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var ErrInteractionConflict = errors.New("native interaction no longer matches the task")

// SuspendTaskForInteraction 在进程退出后同时保存问题、原生恢复指针并撤销任务权限。
// 等待期间不结算任务义务，也不触发审查或自动重试；人工回复负责下一轮入队。
func (s *TaskService) SuspendTaskForInteraction(ctx context.Context, taskID pgtype.UUID, pending protocol.SuspendedInteraction, result []byte, sessionID, workDir string) (*db.AgentTaskQueue, error) {
	if err := pending.Validate(); err != nil {
		return nil, err
	}
	if s.TxStarter == nil {
		return nil, errors.New("suspending interactions requires a database transaction")
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(workDir) == "" {
		return nil, errors.New("native interaction requires session and work directory")
	}
	var task db.AgentTaskQueue
	var event events.Event
	changed := false
	err := s.runInTx(ctx, func(q *db.Queries) error {
		if err := lockChatSessionForTaskWrite(ctx, q, taskID); err != nil {
			return err
		}
		current, err := q.GetAgentTaskForInteractionUpdate(ctx, taskID)
		if err != nil {
			return err
		}
		if current.Status == "waiting_plan" || current.Status == "waiting_input" || current.Status == "completed" || current.Status == "cancelled" {
			row, err := q.GetSuspendedTaskInteraction(ctx, taskID)
			if err != nil {
				return err
			}
			var previous protocol.SuspendedInteraction
			if json.Unmarshal(row.Request, &previous) != nil || !reflect.DeepEqual(previous, pending) || current.SessionID.String != sessionID || current.WorkDir.String != workDir {
				return ErrInteractionConflict
			}
			task = current
			return nil
		}
		if current.Status != "running" {
			return ErrInteractionConflict
		}
		runtime, err := q.GetAgentRuntime(ctx, current.RuntimeID)
		if err != nil {
			return err
		}
		if runtime.Provider != "dsh" {
			return errors.New("suspended interaction requires a DSH runtime")
		}
		if _, governed := lifeTaskEnvelopeForQueue(current); governed {
			return errors.New("background cognition cannot request human interaction")
		}
		workspace, err := resolveTaskWorkspaceWithQueries(ctx, q, current)
		if err != nil {
			return err
		}
		var workspaceID pgtype.UUID
		if err := workspaceID.Scan(workspace); err != nil {
			return err
		}
		body, err := json.Marshal(pending)
		if err != nil {
			return err
		}
		_, err = q.CreateSuspendedTaskInteraction(ctx, db.CreateSuspendedTaskInteractionParams{
			TaskID: taskID, ID: dbid.NewV7(), WorkspaceID: workspaceID, IssueID: current.IssueID, Kind: pending.Kind, Request: body,
		})
		if err != nil {
			return err
		}
		status := "waiting_input"
		if pending.Kind == "plan" {
			status = "waiting_plan"
		}
		task, err = q.SuspendAgentTaskForInteraction(ctx, db.SuspendAgentTaskForInteractionParams{
			ID: taskID, Status: status, Result: result,
			SessionID: pgtype.Text{String: sessionID, Valid: true}, WorkDir: pgtype.Text{String: workDir, Valid: true},
		})
		if err != nil {
			return err
		}
		if err := q.DeleteTaskTokensByTask(ctx, taskID); err != nil {
			return fmt.Errorf("revoke suspended task permissions: %w", err)
		}
		if current.ChatSessionID.Valid {
			if err := q.UpdateChatSessionSession(ctx, db.UpdateChatSessionSessionParams{
				ID: current.ChatSessionID, SessionID: task.SessionID, WorkDir: task.WorkDir, RuntimeID: current.RuntimeID,
			}); err != nil {
				return err
			}
			if _, err := s.writeChatCompletionOutcome(ctx, q, task, result); err != nil {
				return err
			}
		}
		event, err = recordTaskLifecycleEventTx(ctx, q, protocol.EventTaskWaitingHuman, task)
		changed = err == nil
		return err
	})
	if err != nil {
		return nil, err
	}
	if changed {
		publishCommittedEvent(s.Bus, event)
		s.ReconcileAgentStatus(ctx, task.AgentID)
		s.NotifyTaskFinished(task)
	}
	return &task, nil
}

// RespondSuspendedInteraction 以一次事务保存人工决定并创建唯一续跑任务。
// 续跑只引用原生会话；计划正文留在原生历史，避免每次确认重发整段上下文。
func (s *TaskService) RespondSuspendedInteraction(ctx context.Context, taskID, interactionID, author pgtype.UUID, answer protocol.InteractionAnswer) (db.TaskInteraction, error) {
	if s.TxStarter == nil {
		return db.TaskInteraction{}, errors.New("interaction response requires a database transaction")
	}
	source, err := s.Queries.GetAgentTask(ctx, taskID)
	if err != nil {
		return db.TaskInteraction{}, err
	}
	existing, err := s.Queries.GetTaskInteraction(ctx, db.GetTaskInteractionParams{TaskID: taskID, ID: interactionID})
	if err != nil {
		return db.TaskInteraction{}, err
	}
	if existing.Provider != "dsh" || existing.DeliveryMode != "suspended" {
		return db.TaskInteraction{}, ErrInteractionConflict
	}
	var prompt protocol.InteractionPrompt
	if err := json.Unmarshal(existing.Request, &prompt); err != nil {
		return db.TaskInteraction{}, err
	}
	if err := prompt.ValidateContinuation(answer); err != nil {
		return db.TaskInteraction{}, err
	}
	if existing.Status == "responded" {
		var previous protocol.InteractionAnswer
		if json.Unmarshal(existing.Response, &previous) != nil || !reflect.DeepEqual(previous, answer) {
			return db.TaskInteraction{}, ErrInteractionConflict
		}
		return existing, nil
	}
	var prepared PreparedChatTaskEnqueue
	if answer.Decision != "cancelled" {
		prepared, err = s.PrepareChatTaskEnqueue(ctx, source.AgentID, author)
		if err != nil {
			return db.TaskInteraction{}, err
		}
	}
	var response db.TaskInteraction
	var followup db.AgentTaskQueue
	var followupEvent events.Event
	var resolvedEvent events.Event
	changed := false
	err = s.runInTx(ctx, func(q *db.Queries) error {
		if err := lockChatSessionForTaskWrite(ctx, q, taskID); err != nil {
			return err
		}
		carrier, err := q.GetAgentForClaimUpdate(ctx, source.AgentID)
		if err != nil {
			return err
		}
		parent, err := q.GetAgentTaskForInteractionUpdate(ctx, taskID)
		if err != nil {
			return err
		}
		row, err := q.GetTaskInteraction(ctx, db.GetTaskInteractionParams{TaskID: taskID, ID: interactionID})
		if err != nil {
			return err
		}
		if row.Provider != "dsh" || row.DeliveryMode != "suspended" {
			return ErrInteractionConflict
		}
		if row.Status == "responded" {
			var previous protocol.InteractionAnswer
			if json.Unmarshal(row.Response, &previous) != nil || !reflect.DeepEqual(previous, answer) {
				return ErrInteractionConflict
			}
			response = row
			return nil
		}
		if row.Status != "pending" || !row.ExpiresAt.Time.After(time.Now()) ||
			(parent.Status != "waiting_plan" && parent.Status != "waiting_input") {
			return ErrInteractionConflict
		}
		var pending protocol.SuspendedInteraction
		if err := json.Unmarshal(row.Request, &pending); err != nil {
			return err
		}
		if err := pending.ValidateContinuation(answer); err != nil {
			return err
		}
		if !parent.SessionID.Valid || !parent.WorkDir.Valid {
			return ErrInteractionConflict
		}
		nextStatus := "cancelled"
		if answer.Decision != "cancelled" {
			if carrier.ArchivedAt.Valid || carrier.RuntimeID != parent.RuntimeID {
				return ErrInteractionConflict
			}
			allowed, err := memberMayInvokeAgent(ctx, q, carrier, author, row.WorkspaceID)
			if err != nil {
				return err
			}
			if !allowed {
				return ErrInteractionNotAllowed
			}
			newer, err := q.HasNewerInteractionTask(ctx, taskID)
			if err != nil {
				return err
			}
			if newer {
				return ErrInteractionConflict
			}
			content := interactionContinuationText(pending.InteractionPrompt, answer)
			if parent.ChatSessionID.Valid {
				session, err := q.GetChatSession(ctx, parent.ChatSessionID)
				if err != nil {
					return err
				}
				if session.Status != "active" || session.SessionID.String != parent.SessionID.String || session.RuntimeID != parent.RuntimeID {
					return ErrInteractionConflict
				}
				followup, err = q.CreateChatTask(ctx, db.CreateChatTaskParams{
					ID: dbid.NewV7(), AgentID: carrier.ID, RuntimeID: carrier.RuntimeID, ChatSessionID: parent.ChatSessionID,
					Priority: parent.Priority, InitiatorUserID: author, OriginatorUserID: author, AccountableUserID: prepared.accountableUser,
					RuntimeMcpOverlay: prepared.runtimeOverlay.Overlay, RuntimeConnectedApps: prepared.runtimeOverlay.ConnectedApps,
					OriginatorSource: prepared.attrSource, TriggerEvidenceKind: prepared.attrEvidenceKind, TriggerEvidenceRefID: session.ID,
				})
				if err != nil {
					return err
				}
				followup, err = q.SetChatTaskInputOwnerSelf(ctx, followup.ID)
				if err != nil {
					return err
				}
				_, err = q.CreateChatMessage(ctx, db.CreateChatMessageParams{ID: dbid.NewV7(), ChatSessionID: session.ID,
					Role: "user", Content: content, TaskID: followup.ID, MessageKind: pgtype.Text{String: protocol.ChatMessageKindMessage, Valid: true},
				})
				if err != nil {
					return err
				}
				if err := q.TouchChatSession(ctx, session.ID); err != nil {
					return err
				}
			} else if parent.IssueID.Valid {
				comment, err := q.CreateComment(ctx, db.CreateCommentParams{ID: dbid.NewV7(), IssueID: parent.IssueID, WorkspaceID: row.WorkspaceID,
					AuthorType: "member", AuthorID: author, Content: content, Type: "comment",
				})
				if err != nil {
					return err
				}
				followup, err = q.CreateAgentTask(ctx, db.CreateAgentTaskParams{
					ID: dbid.NewV7(), AgentID: carrier.ID, RuntimeID: carrier.RuntimeID, IssueID: parent.IssueID, Priority: parent.Priority,
					TriggerCommentID: comment.ID, TriggerSummary: pgtype.Text{String: content, Valid: true},
					OriginatorUserID: author, AccountableUserID: prepared.accountableUser,
					OriginatorSource: prepared.attrSource, TriggerEvidenceKind: pgtype.Text{String: "comment", Valid: true}, TriggerEvidenceRefID: comment.ID,
					RuntimeMcpOverlay: prepared.runtimeOverlay.Overlay, RuntimeConnectedApps: prepared.runtimeOverlay.ConnectedApps,
					SquadID: parent.SquadID, IsLeaderTask: pgtype.Bool{Bool: parent.IsLeaderTask, Valid: true},
				})
				if err != nil {
					return err
				}
			} else {
				return ErrInteractionConflict
			}
			followup, err = q.MarkTaskInteractionContinuation(ctx, followup.ID)
			if err != nil {
				return err
			}
			followupEvent, err = RecordTaskQueuedEventTx(ctx, q, util.UUIDToString(row.WorkspaceID), followup)
			if err != nil {
				return err
			}
			nextStatus = "completed"
		}
		encoded, err := json.Marshal(answer)
		if err != nil {
			return err
		}
		response, err = q.ResolveSuspendedTaskInteraction(ctx, db.ResolveSuspendedTaskInteractionParams{
			TaskID: taskID, ID: interactionID, Response: encoded, AuthorID: author, FollowupTaskID: followup.ID,
		})
		if err != nil {
			return err
		}
		parent, err = q.ResolveSuspendedAgentTask(ctx, db.ResolveSuspendedAgentTaskParams{ID: taskID, Status: nextStatus})
		if err != nil {
			return err
		}
		if err := SettleTerminalTaskState(ctx, q, parent); err != nil {
			return err
		}
		resolvedEvent, err = recordTaskLifecycleEventTx(ctx, q, protocol.EventTaskInteractionResolved, parent)
		changed = err == nil
		return err
	})
	if err != nil {
		return db.TaskInteraction{}, err
	}
	if changed {
		publishCommittedEvent(s.Bus, resolvedEvent)
		if followup.ID.Valid {
			publishCommittedEvent(s.Bus, followupEvent)
			s.NotifyTaskEnqueued(ctx, followup)
		}
		s.ReconcileAgentStatus(ctx, source.AgentID)
	}
	return response, nil
}

var ErrInteractionNotAllowed = errors.New("agent invocation is not allowed")

func interactionContinuationText(prompt protocol.InteractionPrompt, answer protocol.InteractionAnswer) string {
	if prompt.Kind == "plan" {
		if answer.Decision == "approved" {
			return "已批准本轮计划，请按计划执行。" + feedbackSuffix(answer.Feedback)
		}
		return "继续规划，暂不执行。" + feedbackSuffix(answer.Feedback)
	}
	var text strings.Builder
	text.WriteString("对上一轮问题的回答：")
	for _, question := range prompt.Questions {
		text.WriteString("\n" + question.ID + ": " + strings.Join(answer.Answers[question.ID], "; "))
	}
	return text.String()
}

func feedbackSuffix(feedback string) string {
	if strings.TrimSpace(feedback) == "" {
		return ""
	}
	return "\n" + feedback
}

// 每轮只处理有索引覆盖的 32 个过期交互，经既有取消事务收敛状态和通知。
func (s *TaskService) ExpireSuspendedInteractions(ctx context.Context) error {
	tasks, err := s.Queries.ListExpiredSuspendedTaskInteractions(ctx)
	if err != nil {
		return err
	}
	for _, taskID := range tasks {
		if _, err := s.CancelTaskWithResult(ctx, taskID, CancelTaskOptions{ErrorMessage: "人工确认已过期", FailureReason: "interaction_expired"}); err != nil {
			return err
		}
	}
	return nil
}

// 人工决定只提交一次新回合；基础设施重试不能再次投递同一条批准输入。
func isDSHInteractionContinuation(task db.AgentTaskQueue) bool {
	var context map[string]json.RawMessage
	if json.Unmarshal(task.Context, &context) != nil {
		return false
	}
	return len(context["dsh_interaction_continuation"]) != 0
}

var ErrInteractionContinuationRerun = errors.New("计划或问答的续跑不能从头重跑，请恢复原生会话或重新规划")
