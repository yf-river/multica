// 本文件承载「任务执行轨迹（task trace）事件」的对外 API 模型与读取 handler：把 DB 表
// task_trace_event 的行换算成给前端用的 JSON 形态。事件由服务器在任务执行的关键节点
// best-effort 落库——主力写入方是 internal/service/task_trace.go（TaskService 记录用户
// 输入已接收、入队、完成/失败等阶段），issue_source_fetch.go 的 source fetch handler 也
// 会为「拉取外部来源」这类任务步骤补记 source.fetch 事件。
//
// 虽然文件名带着 daemon（这些事件记录的本质就是 daemon 侧一次任务执行的过程/结果），
// 当前挂载点却是用户侧 issues 路由下的 GET /api/issues/{id}/trace（需登录 + workspace
// 成员身份），返回某个 issue 名下按时间正序排列的全部轨迹事件，供执行历史/运行结果类
// 面板展示。issue_execution_tree.go 也复用了本文件的 TaskTraceEventResponse 类型，所以
// 这里是轨迹事件 API 形态的唯一权威出处。
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TaskTraceEventResponse 是一条任务执行轨迹事件对外形态。一次 task 的执行会被记成多行
// 事件（用户输入已接收、入队、开始、进度、完成/失败……），同一任务的行共享 task_id，
// Attempt 标出它属于第几次尝试；同一尝试内靠 EventType（机器可读的层级键，如
// "user_input.received"、"source.fetch"）与 EventName（人类可读的名字）区分阶段。
//
// 可空约定：IssueID/RuntimeID/SquadID/ProjectID 等 *string 与时长类 *int64 对应 DB 可空列，
// 本行不适用时在 JSON 里是 null 或省略；Provider/Model/token 计数只在相关阶段（执行中或
// 结束后）才有意义，缺省为空串/0。CreatedAt 已格式化成时间字符串。
type TaskTraceEventResponse struct {
	ID               string         `json:"id"`
	WorkspaceID      string         `json:"workspace_id"`
	TaskID           string         `json:"task_id"`
	IssueID          *string        `json:"issue_id"`
	AgentID          string         `json:"agent_id"`
	RuntimeID        *string        `json:"runtime_id"`
	SquadID          *string        `json:"squad_id"`
	ProjectID        *string        `json:"project_id"`
	Source           string         `json:"source"`
	EventType        string         `json:"event_type"`
	EventName        string         `json:"event_name"`
	Status           string         `json:"status"`
	Attempt          int32          `json:"attempt"`
	DurationMs       *int64         `json:"duration_ms,omitempty"`
	QueueWaitMs      *int64         `json:"queue_wait_ms,omitempty"`
	RunMs            *int64         `json:"run_ms,omitempty"`
	TotalMs          *int64         `json:"total_ms,omitempty"`
	Provider         string         `json:"provider"`
	Model            string         `json:"model"`
	InputTokens      int64          `json:"input_tokens"`
	OutputTokens     int64          `json:"output_tokens"`
	CacheReadTokens  int64          `json:"cache_read_tokens"`
	CacheWriteTokens int64          `json:"cache_write_tokens"`
	FailureReason    string         `json:"failure_reason"`
	ErrorType        string         `json:"error_type"`
	TriggerCommentID *string        `json:"trigger_comment_id"`
	AutopilotRunID   *string        `json:"autopilot_run_id"`
	ChatSessionID    *string        `json:"chat_session_id"`
	Metadata         map[string]any `json:"metadata"`
	CreatedAt        string         `json:"created_at"`
}

// taskTraceEventToResponse 把一条 DB 轨迹行换算成对外形态：UUID 列转字符串、可空列转
// 指针、时间戳转字符串。唯一可能失败的步骤是 Metadata——DB 侧是 jsonb 原始字节，这里
// 必须能被解码成一个 JSON 对象（map[string]any）；内容是 null/数组/标量或字节损坏都会
// 返回错误，保证对外契约「metadata 永远是对象」不被破坏。错误信息为英文并保留 DB
// 语义，供排障直接定位。
func taskTraceEventToResponse(ev db.TaskTraceEvent) (TaskTraceEventResponse, error) {
	var metadata map[string]any
	if err := json.Unmarshal(ev.Metadata, &metadata); err != nil || metadata == nil {
		return TaskTraceEventResponse{}, fmt.Errorf("decode task trace metadata: expected JSON object")
	}
	return TaskTraceEventResponse{
		ID:               uuidToString(ev.ID),
		WorkspaceID:      uuidToString(ev.WorkspaceID),
		TaskID:           uuidToString(ev.TaskID),
		IssueID:          uuidToPtr(ev.IssueID),
		AgentID:          uuidToString(ev.AgentID),
		RuntimeID:        uuidToPtr(ev.RuntimeID),
		SquadID:          uuidToPtr(ev.SquadID),
		ProjectID:        uuidToPtr(ev.ProjectID),
		Source:           ev.Source,
		EventType:        ev.EventType,
		EventName:        ev.EventName,
		Status:           ev.Status,
		Attempt:          ev.Attempt,
		DurationMs:       int8ToPtr(ev.DurationMs),
		QueueWaitMs:      int8ToPtr(ev.QueueWaitMs),
		RunMs:            int8ToPtr(ev.RunMs),
		TotalMs:          int8ToPtr(ev.TotalMs),
		Provider:         ev.Provider,
		Model:            ev.Model,
		InputTokens:      ev.InputTokens,
		OutputTokens:     ev.OutputTokens,
		CacheReadTokens:  ev.CacheReadTokens,
		CacheWriteTokens: ev.CacheWriteTokens,
		FailureReason:    ev.FailureReason,
		ErrorType:        ev.ErrorType,
		TriggerCommentID: uuidToPtr(ev.TriggerCommentID),
		AutopilotRunID:   uuidToPtr(ev.AutopilotRunID),
		ChatSessionID:    uuidToPtr(ev.ChatSessionID),
		Metadata:         metadata,
		CreatedAt:        timestampToString(ev.CreatedAt),
	}, nil
}

// taskTraceEventsToResponse 批量转换整批轨迹事件。任一条的 metadata 解码失败都会让整个
// 批次失败（返回 nil 与错误）——宁可整批不返回，也不给前端一份悄悄缺行的半截数据。
func taskTraceEventsToResponse(events []db.TaskTraceEvent) ([]TaskTraceEventResponse, error) {
	response := make([]TaskTraceEventResponse, len(events))
	for i, event := range events {
		converted, err := taskTraceEventToResponse(event)
		if err != nil {
			return nil, err
		}
		response[i] = converted
	}
	return response, nil
}

// ListIssueTaskTraceEvents 处理 GET /api/issues/{id}/trace：返回某 issue 名下持久化的
// 全部任务执行轨迹事件（DB 按 created_at 正序），响应包成 {"events":[...]}。
//
// 归属与权限：chi URL 参数 {id} 交给 loadIssueForUser 解析——它要求请求携带 X-User-ID
// 与 workspace_id，支持按标识符（如 MUL-123）或 UUID 两种写法，并确保 issue 属于该
// workspace（查不到或跨 workspace 一律 404）。路由本身已套 workspace 成员中间件，这里
// 的复查是纵深防御。
//
// 返回：200 + {"events":[...]}（空时是 []）；issue 不存在/无权限回 404；DB 查询失败回
// 500；任一条轨迹 metadata 解码失败也回 500（见 taskTraceEventsToResponse 的整批失败
// 策略）。
func (h *Handler) ListIssueTaskTraceEvents(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	events, err := h.Queries.ListIssueTaskTraceEvents(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issue trace events")
		return
	}
	resp, err := taskTraceEventsToResponse(events)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decode issue trace events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": resp})
}
