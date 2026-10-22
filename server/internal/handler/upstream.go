package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/upstream"
)

// upstreamScope 是人类侧入口：成员可读，只有 owner/admin 能改配置与标记。
// Agent 携带 task_token 时一律拒绝，避免同步 Agent 自己给自己盖章。
func (h *Handler) upstreamScope(w http.ResponseWriter, r *http.Request, write bool) (string, string, bool) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		writeError(w, 403, "上游同步配置与标记需要成员身份")
		return "", "", false
	}
	ws := middleware.WorkspaceIDFromContext(r.Context())
	user := requestUserID(r)
	member, ok := h.requireWorkspaceMember(w, r, ws, "工作区不存在")
	if !ok {
		return "", "", false
	}
	if write && member.Role != "owner" && member.Role != "admin" {
		writeError(w, 403, "需要工作区管理员权限")
		return "", "", false
	}
	return ws, user, true
}

// upstreamTaskScope 校验 Agent 身份：必须是正在运行的任务，且该任务属于
// 本工作区绑定的同步 Agent。批次 id 不从这里取，避免 Agent 写错别人的批次。
func (h *Handler) upstreamTaskScope(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, 403, "需要有效的同步运行身份")
		return "", "", false
	}
	taskID := strings.TrimSpace(r.Header.Get("X-Task-ID"))
	agentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	if taskID == "" || agentID == "" {
		writeError(w, 403, "缺少任务或 Agent 标识")
		return "", "", false
	}
	ws := middleware.WorkspaceIDFromContext(r.Context())
	var bound string
	err := h.DB.QueryRow(r.Context(), `SELECT c.agent_id::text
 FROM upstream_sync_config c
 JOIN agent_task_queue t ON t.id=$1 AND t.workspace_id=c.workspace_id AND t.agent_id=c.agent_id AND t.status='running'
 WHERE c.workspace_id=$2 AND c.agent_id=$3`, taskID, ws, agentID).Scan(&bound)
	if err != nil || bound != agentID {
		writeError(w, 403, "同步 Agent 未绑定或任务已结束")
		return "", "", false
	}
	return ws, taskID, true
}

func writeUpstreamError(w http.ResponseWriter, err error) {
	var verr upstream.ValidationError
	switch {
	case errors.As(err, &verr):
		writeError(w, 400, verr.Error())
	case errors.Is(err, upstream.ErrBusy):
		writeError(w, 409, "另一批次正在运行")
	case errors.Is(err, upstream.ErrNeedsManual):
		writeError(w, 409, "存量同步只能由人工在页面上触发")
	case errors.Is(err, upstream.ErrRunGone):
		writeError(w, 409, "批次不存在或已结束")
	default:
		writeError(w, 500, "上游同步操作失败")
	}
}

func (h *Handler) GetUpstream(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.upstreamScope(w, r, false)
	if !ok {
		return
	}
	snap, err := h.Upstream.Snapshot(r.Context(), ws)
	if err != nil {
		writeError(w, 500, "读取上游同步状态失败")
		return
	}
	writeJSON(w, 200, snap)
}

func (h *Handler) UpdateUpstreamConfig(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.upstreamScope(w, r, true)
	if !ok {
		return
	}
	var body struct {
		UpstreamURL    string  `json:"upstream_url"`
		UpstreamBranch string  `json:"upstream_branch"`
		LocalRef       string  `json:"local_ref"`
		BatchSize      int     `json:"batch_size"`
		AgentID        *string `json:"agent_id"`
		AutopilotID    *string `json:"autopilot_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "请求体格式错误")
		return
	}
	if body.UpstreamURL == "" {
		body.UpstreamURL = "https://github.com/multica-ai/multica.git"
	}
	if body.UpstreamBranch == "" {
		body.UpstreamBranch = "main"
	}
	if body.LocalRef == "" {
		body.LocalRef = "main"
	}
	if body.BatchSize == 0 {
		body.BatchSize = 200
	}
	err := h.Upstream.UpsertConfig(r.Context(), ws, upstream.Config{
		UpstreamURL: body.UpstreamURL, UpstreamBranch: body.UpstreamBranch,
		LocalRef: body.LocalRef, BatchSize: body.BatchSize,
	}, body.AgentID, body.AutopilotID)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (h *Handler) ListUpstreamItems(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.upstreamScope(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, err := h.Upstream.ListItems(r.Context(), ws, upstream.ItemFilter{
		Status: q.Get("status"), Verdict: q.Get("verdict"),
		Presence: q.Get("presence"), Query: q.Get("q"),
		Limit: limit, Offset: offset,
	})
	if err != nil {
		writeError(w, 500, "读取待吸收清单失败")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (h *Handler) MarkUpstreamItems(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.upstreamScope(w, r, true)
	if !ok {
		return
	}
	var body struct {
		IDs    []string `json:"ids"`
		Status string   `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "请求体格式错误")
		return
	}
	n, err := h.Upstream.MarkItems(r.Context(), ws, body.IDs, body.Status, user)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]int{"updated": n})
}

func (h *Handler) ListUpstreamRuns(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.upstreamScope(w, r, false)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := h.Upstream.ListRuns(r.Context(), ws, limit)
	if err != nil {
		writeError(w, 500, "读取同步记录失败")
		return
	}
	writeJSON(w, 200, map[string]any{"runs": runs})
}

func (h *Handler) StartUpstreamRun(w http.ResponseWriter, r *http.Request) {
	ws, taskID, ok := h.upstreamTaskScope(w, r)
	if !ok {
		return
	}
	trigger := "manual"
	var ai string
	// 有 autopilot_run_id 说明这一轮是定时任务派出来的：存量阶段必须拒绝，
	// 否则第一次定时跑会悄悄把全量历史扫掉。
	err := h.DB.QueryRow(r.Context(), `SELECT COALESCE(autopilot_run_id::text,'') FROM agent_task_queue WHERE id=$1`, taskID).Scan(&ai)
	if err == nil && ai != "" {
		trigger = "schedule"
	}
	res, err := h.Upstream.Start(r.Context(), ws, taskID, r.Header.Get("X-Agent-ID"), trigger)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (h *Handler) AppendUpstreamItems(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.upstreamTaskScope(w, r)
	if !ok {
		return
	}
	runID := chi.URLParam(r, "runId")
	var rep upstream.Report
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeError(w, 400, "请求体格式错误")
		return
	}
	if err := upstream.ValidateReport(rep); err != nil {
		writeUpstreamError(w, err)
		return
	}
	n, err := h.Upstream.AppendReport(r.Context(), ws, runID, rep)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]int{"items": n})
}

func (h *Handler) FinishUpstreamRun(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.upstreamTaskScope(w, r)
	if !ok {
		return
	}
	var body struct {
		CursorSHA string `json:"cursor_sha"`
		HasMore   bool   `json:"has_more"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "请求体格式错误")
		return
	}
	if err := h.Upstream.Finish(r.Context(), ws, chi.URLParam(r, "runId"), body.CursorSHA, body.HasMore); err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (h *Handler) FailUpstreamRun(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.upstreamTaskScope(w, r)
	if !ok {
		return
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "请求体格式错误")
		return
	}
	if err := h.Upstream.Fail(r.Context(), ws, chi.URLParam(r, "runId"), body.Error); err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
