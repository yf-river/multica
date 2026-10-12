// 本文件是 agent 任务（task）「生命周期救援」入口，全部由 daemon 或用户在特定时机
// 主动调用，把卡住或失败的任务接回正轨：
//
//   - RecoverOrphanedTasks：daemon 启动时调用——把自己名下「服务端仍认为是 dispatched /
//     running」的孤儿任务（即上一个 daemon 进程死时正在跑的）原子地标记失败，并走统一
//     的失败后处理管线（事件、agent reconcile、issue 回滚、自动重试）；
//   - PinTaskSession：daemon 拿到 agent 的 session_id / work_dir 后立刻持久化，崩溃后
//     下次尝试仍能接着对话继续（配合 chat session 指针推进，修复 GH #6340 的一半）；
//   - RerunIssue / RetrySourceContextQuickCreate：用户手动重跑（force_fresh_session，
//     不复用旧会话）或重试失败的「来源上下文 quick-create」。
package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RecoverOrphanedTasks 由 daemon 在启动时为它拥有的每个 runtime 调用。它把服务端仍
// 认为属于该 runtime 的 dispatched/running 任务（即上个 daemon 进程死时正在跑的那些）
// 原子地标记为失败，并对每个任务触发 MaybeRetryFailedTask——让用户看到一次全新尝试，
// 而不是永远卡住的一行。
//
// 这是针对「daemon 在任务中途重启时 issue 卡在 in_progress」的定向修复：runtime 心跳
// 清扫器要最多 75 秒 + 进程内任务超时（2.5h）才能发现这类任务；daemon 自己一回来就
// 知道，所以让它上报孤儿恢复。
func (h *Handler) RecoverOrphanedTasks(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}

	rows, err := h.TaskService.RecoverOrphanedTasksForRuntime(r.Context(), parseUUID(runtimeID))
	if err != nil {
		slog.Warn("recover-orphans failed", "runtime_id", runtimeID, "error", err)
		writeError(w, http.StatusInternalServerError, "recover orphans failed")
		return
	}

	// 走共享的失败后处理管线，让 task:failed 事件、agent reconcile、issue 回滚与自动
	// 重试和 runtime 清扫器完全一致。以前这里是绕过这些副作用的快路径，当没有重试被
	// 创建时（max_attempts 耗尽、autopilot、不可重试原因）UI 会一直停留在旧状态。
	retried := h.TaskService.HandleFailedTasks(r.Context(), rows)

	if len(rows) > 0 {
		slog.Info("recover-orphans completed",
			"runtime_id", runtimeID,
			"orphaned", len(rows),
			"retried", retried,
		)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"orphaned": len(rows),
		"retried":  retried,
	})
}

// PinTaskSessionRequest 让 daemon 在知道 agent 的 session_id 与 work_dir 时立刻
// 持久化它们——通常在 agent 发出第一条系统消息之后——这样任务中途崩溃不会丢掉
// 下次尝试接续对话所需的恢复指针。
type PinTaskSessionRequest struct {
	SessionID string `json:"session_id,omitempty"`
	WorkDir   string `json:"work_dir,omitempty"`
}

// PinTaskSession 处理 daemon 的 session 落点请求（见 PinTaskSessionRequest 上的说明）。
func (h *Handler) PinTaskSession(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	if _, ok := h.requireDaemonTaskAccess(w, r, taskID); !ok {
		return
	}

	var req PinTaskSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SessionID == "" && req.WorkDir == "" {
		writeError(w, http.StatusBadRequest, "session_id or work_dir required")
		return
	}

	params := db.UpdateAgentTaskSessionParams{ID: parseUUID(taskID)}
	if req.SessionID != "" {
		params.SessionID = pgtype.Text{String: req.SessionID, Valid: true}
	}
	if req.WorkDir != "" {
		params.WorkDir = pgtype.Text{String: req.WorkDir, Valid: true}
	}
	// pin 可能在用户已经取消运行之后才到——它是异步的，对 Codex 而言还要等 rollout
	// 到达 store。取消事务当时没找到要发布的 session，于是聊天的恢复指针还停在上一
	// 轮，会盖掉这次 pin 正要记录的 session（claim handler 在 GetLastChatSession 兜底
	// 之前先读指针）。在这里推进指针，堵上 GH #6340 的另一半。
	//
	// 两个写入一起提交。先把 session 落在任务行、再推指针，会留下与取消路径同样的
	// 窗口：行已经指向新 session，指针还指向上一轮，其间认领的后续任务会恢复到旧的
	// 一轮。锁放在最前，理由与 CancelTaskWithResult 里相同——chat_session ->
	// agent_task_queue 是全局锁顺序，ErrNoRows 只是说明没有可锁可推的 session。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("pin-session failed to start tx", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "pin session failed")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	if _, err := qtx.LockChatSessionForTask(r.Context(), params.ID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("pin-session failed to lock chat session", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "pin session failed")
		return
	}
	if err := qtx.UpdateAgentTaskSession(r.Context(), params); err != nil {
		slog.Warn("pin-session failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "pin session failed")
		return
	}
	// 该语句重读行、忽略任何不是已取消 chat task 的情况，且当更新的轮次已经拥有
	// session 时拒绝移动指针——所以迟到的 pin 不可能把对话往回拖。
	if err := qtx.AdvanceCancelledChatSessionPointer(r.Context(), params.ID); err != nil {
		slog.Warn("advance cancelled chat session pointer failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "pin session failed")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("pin-session commit failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "pin session failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RerunIssueRequest 是 POST /api/issues/{id}/rerun 的可选 body。所有字段可选；空 body
// 保持 CLI 使用的「重跑 issue 当前指派人」旧行为。
type RerunIssueRequest struct {
	// TaskID 标识用户点击重试的那条执行日志行。设置后，重跑针对的是跑那条具体任务
	// 的 agent（并复用其 leader/worker 角色），而不是 issue 当前的指派人——这样在
	// 一条已被换掉的 agent 的行上点重试，重发的是那个同一 agent，而不是新指派人。
	TaskID string `json:"task_id,omitempty"`
}

// RerunIssue 手动为 issue 重新入队一次 agent 运行。默认目标是 issue 当前指派人
// （agent 或 squad 队长）；若请求带 task_id，则改跑跑过那条历史任务的 agent。新任务
// 标记 force_fresh_session=true：daemon 认领时会跳过 (agent_id, issue_id) 的会话续接
// 查找，让 agent 起一个干净会话。用户点重跑，就是刚判断旧输出不行——重放同一段对话
// 等于重放同一份被污染的状态。（自动重试则相反、刻意继承会话：那条路径处理的是
// 基础设施故障，不是糟糕的输出。）
func (h *Handler) RerunIssue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}

	// body 可选。零长度或 `{}` 保持 CLI 依赖的指派人驱动旧行为。
	var req RerunIssueRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	var sourceTaskID pgtype.UUID
	if req.TaskID != "" {
		parsed, ok := parseUUIDOrBadRequest(w, req.TaskID, "task_id")
		if !ok {
			return
		}
		sourceTaskID = parsed
	}

	// 手动重跑是直接的人类动作：把新运行归到重跑成员名下（MUL-4302 §5）。与
	// assign/promote 一样解析 actor；agent A2A actor 不是人，会在这里被当成非法
	// actor 拒掉。
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	actorUserID := memberActorUserID(actorType, actorID)

	// 在取消/创建任何东西之前，复核操作者对解析出的目标 agent 的触发权限
	// （MUL-4525）。能看到 issue 不等于有权触发私有 agent——task_id 重跑必须闸住的是
	// 历史那个 agent，而不是（可能已被重新指派的）当前指派人。
	originatorUserID := h.invokeOriginatorFromRequest(r, actorType, actorID)
	canInvoke := func(agent db.Agent) bool {
		return h.canInvokeAgent(r.Context(), agent, actorType, actorID, originatorUserID, workspaceID)
	}

	task, err := h.TaskService.RerunIssue(r.Context(), issue.ID, sourceTaskID, pgtype.UUID{}, actorUserID, canInvoke)
	if errors.Is(err, service.ErrRerunInvokeNotAllowed) {
		h.writeDispatchBlocked(w, http.StatusForbidden, ReasonInvocationNotAllowed)
		return
	}
	if err != nil {
		slog.Warn("issue rerun failed", "issue_id", id, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := taskToResponse(*task, uuidToString(issue.WorkspaceID))
	h.hydrateTaskAttributions(r.Context(), []*TaskAttribution{resp.Attribution})
	writeJSON(w, http.StatusAccepted, resp)
}

// RetrySourceContextQuickCreate 手动重入队一次失败的「无 issue 的 quick-create」，同时
// 原子地把它 pending 的不可变来源上下文搬到新任务。workspace 中间件提供租户边界；
// service 还要求原始请求者身份并走常规的私有 agent 触发闸门。
func (h *Handler) RetrySourceContextQuickCreate(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace id")
	if !ok {
		return
	}
	requesterID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	taskID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "taskId"), "task id")
	if !ok {
		return
	}
	canInvoke := func(agent db.Agent) bool {
		return h.canInvokeAgent(r.Context(), agent, "member", userID, userID, uuidToString(workspaceID))
	}
	task, err := h.TaskService.RetrySourceContextQuickCreate(r.Context(), workspaceID, requesterID, taskID, canInvoke)
	if errors.Is(err, service.ErrRerunInvokeNotAllowed) {
		h.writeDispatchBlocked(w, http.StatusForbidden, ReasonInvocationNotAllowed)
		return
	}
	if errors.Is(err, service.ErrSourceContextRetryUnavailable) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"code":  "source_context_retry_unavailable",
			"error": "This context can no longer be retried. Start again from the branch point.",
		})
		return
	}
	if err != nil {
		slog.Warn("source context quick-create retry failed", "task_id", uuidToString(taskID), "error", err)
		writeError(w, http.StatusInternalServerError, "retry source context quick create")
		return
	}
	resp := taskToResponse(*task, uuidToString(workspaceID))
	h.hydrateTaskAttributions(r.Context(), []*TaskAttribution{resp.Attribution})
	writeJSON(w, http.StatusAccepted, resp)
}
