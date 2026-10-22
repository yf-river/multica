package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/upstream"
)

// upstreamScope 是人类侧入口：成员可读，owner/admin 可写。
// 台账是平台级的，这里的成员校验只用来回答"这个人能不能改全平台的配置"。
func (h *Handler) upstreamScope(w http.ResponseWriter, r *http.Request, write bool) (string, bool) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		writeError(w, 403, "上游同步配置与标记需要成员身份")
		return "", false
	}
	ws := middleware.WorkspaceIDFromContext(r.Context())
	member, ok := h.requireWorkspaceMember(w, r, ws, "工作区不存在")
	if !ok {
		return "", false
	}
	if write && member.Role != "owner" && member.Role != "admin" {
		writeError(w, 403, "需要工作区管理员权限")
		return "", false
	}
	return requestUserID(r), true
}

// upstreamTaskScope 校验 Agent 身份。批次 id 从任务反查，不接受客户端传入，
// 避免执行体写错别人的批次。
func (h *Handler) upstreamTaskScope(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, 403, "需要有效的同步运行身份")
		return "", false
	}
	taskID := strings.TrimSpace(r.Header.Get("X-Task-ID"))
	agentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	if taskID == "" || agentID == "" {
		writeError(w, 403, "缺少任务或 Agent 标识")
		return "", false
	}
	var bound string
	err := h.DB.QueryRow(r.Context(), `SELECT c.agent_id::text
 FROM upstream_sync_config c
 JOIN agent_task_queue t ON t.id=$1 AND t.agent_id=c.agent_id AND t.status='running'
 WHERE c.singleton=1 AND c.agent_id=$2`, taskID, agentID).Scan(&bound)
	if err != nil || bound != agentID {
		writeError(w, 403, "同步 Agent 未绑定或任务已结束")
		return "", false
	}
	if run := chi.URLParam(r, "runId"); run != "" {
		var owner string
		if err := h.DB.QueryRow(r.Context(), `SELECT task_id::text FROM upstream_sync_run WHERE id=$1`, run).Scan(&owner); err != nil || owner != taskID {
			writeError(w, 403, "不能操作其他任务的同步批次")
			return "", false
		}
	}
	return taskID, true
}

func writeUpstreamError(w http.ResponseWriter, err error) {
	var verr upstream.ValidationError
	switch {
	case errors.As(err, &verr):
		writeError(w, 400, verr.Error())
	case errors.Is(err, upstream.ErrBusy):
		writeError(w, 409, "另一批次正在运行")
	case errors.Is(err, upstream.ErrRunGone):
		writeError(w, 409, "批次不存在或已结束")
	default:
		writeError(w, 500, "上游同步操作失败")
	}
}

func (h *Handler) GetUpstream(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.upstreamScope(w, r, false); !ok {
		return
	}
	snap, err := h.Upstream.Snapshot(r.Context())
	if err != nil {
		writeError(w, 500, "读取上游同步状态失败")
		return
	}
	snap.Instructions = service.UpstreamSyncInstructions()
	writeJSON(w, 200, snap)
}

// UpdateUpstreamConfig 保存配置；带上 runtime_id 时顺带确保隐藏执行体与每日自动化，
// 用户不需要点"创建智能体"。
func (h *Handler) UpdateUpstreamConfig(w http.ResponseWriter, r *http.Request) {
	user, ok := h.upstreamScope(w, r, true)
	if !ok {
		return
	}
	var body struct {
		UpstreamURL    string `json:"upstream_url"`
		UpstreamBranch string `json:"upstream_branch"`
		LocalRef       string `json:"local_ref"`
		RuntimeID      string `json:"runtime_id"`
		Enabled        *bool  `json:"enabled"`
		BatchSize      int    `json:"batch_size"`
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
		body.LocalRef = "origin/personal-studio-upstream"
	}
	if body.BatchSize == 0 {
		body.BatchSize = 200
	}

	if body.RuntimeID != "" {
		var runtimeWorkspace, runtimeStatus string
		if err := h.DB.QueryRow(r.Context(), `SELECT workspace_id::text,status FROM agent_runtime WHERE id=$1 AND provider IN ('codex','codebuddy') AND (visibility='workspace' OR owner_id=$2 OR owner_id IS NULL)`, body.RuntimeID, user).Scan(&runtimeWorkspace, &runtimeStatus); err != nil || runtimeWorkspace != middleware.WorkspaceIDFromContext(r.Context()) {
			writeError(w, 403, "只能绑定当前有管理权限工作区的运行时")
			return
		}
		if (body.Enabled == nil || *body.Enabled) && runtimeStatus != "online" {
			writeError(w, 400, "运行时离线，可暂停同步或选择其他在线运行时")
			return
		}
	}
	agentID, autopilotID, err := h.Upstream.SetupExecutor(r.Context(), upstream.Config{UpstreamURL: body.UpstreamURL, UpstreamBranch: body.UpstreamBranch, LocalRef: body.LocalRef, RuntimeID: body.RuntimeID, BatchSize: body.BatchSize}, user, service.UpstreamSyncInstructions(), body.Enabled == nil || *body.Enabled)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	result := map[string]any{"ok": true, "agent_id": agentID, "autopilot_id": autopilotID}
	writeJSON(w, 200, result)
}

func (h *Handler) ListUpstreamItems(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.upstreamScope(w, r, false); !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, err := h.Upstream.ListItems(r.Context(), upstream.ItemFilter{
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
	user, ok := h.upstreamScope(w, r, true)
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
	n, err := h.Upstream.MarkItems(r.Context(), body.IDs, body.Status, user)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]int{"updated": n})
}

func (h *Handler) ListUpstreamRuns(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.upstreamScope(w, r, false); !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := h.Upstream.ListRuns(r.Context(), limit)
	if err != nil {
		writeError(w, 500, "读取同步记录失败")
		return
	}
	writeJSON(w, 200, map[string]any{"runs": runs})
}

func (h *Handler) StartUpstreamRun(w http.ResponseWriter, r *http.Request) {
	taskID, ok := h.upstreamTaskScope(w, r)
	if !ok {
		return
	}
	trigger := "manual"
	if err := h.DB.QueryRow(r.Context(), `SELECT COALESCE(a.source,'manual') FROM agent_task_queue t LEFT JOIN autopilot_run a ON a.id=t.autopilot_run_id WHERE t.id=$1`, taskID).Scan(&trigger); err != nil {
		writeError(w, 403, "任务来源不可用")
		return
	}
	res, err := h.Upstream.Start(r.Context(), taskID, r.Header.Get("X-Agent-ID"), trigger)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (h *Handler) AppendUpstreamItems(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.upstreamTaskScope(w, r); !ok {
		return
	}
	var rep upstream.Report
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeError(w, 400, "请求体格式错误")
		return
	}
	if err := upstream.ValidateReport(rep); err != nil {
		writeUpstreamError(w, err)
		return
	}
	n, err := h.Upstream.AppendReport(r.Context(), chi.URLParam(r, "runId"), rep)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]int{"items": n})
}

func (h *Handler) FinishUpstreamRun(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.upstreamTaskScope(w, r); !ok {
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
	if err := h.Upstream.Finish(r.Context(), chi.URLParam(r, "runId"), body.CursorSHA, body.HasMore); err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (h *Handler) FailUpstreamRun(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.upstreamTaskScope(w, r); !ok {
		return
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "请求体格式错误")
		return
	}
	if err := h.Upstream.Fail(r.Context(), chi.URLParam(r, "runId"), body.Error); err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (h *Handler) PrepareUpstreamRun(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.upstreamTaskScope(w, r); !ok {
		return
	}
	var m upstream.BatchManifest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&m); err != nil {
		writeError(w, 400, "提交清单格式错误")
		return
	}
	if err := h.Upstream.Prepare(r.Context(), chi.URLParam(r, "runId"), m); err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (h *Handler) HeartbeatUpstreamRun(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.upstreamTaskScope(w, r); !ok {
		return
	}
	if err := h.Upstream.Heartbeat(r.Context(), chi.URLParam(r, "runId")); err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
