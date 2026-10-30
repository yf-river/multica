package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/upstream"
)

type upstreamProjectContextKey struct{}

func (h *Handler) upstreamService(r *http.Request) *upstream.Service {
	id, _ := r.Context().Value(upstreamProjectContextKey{}).(string)
	if id == "" {
		id = r.URL.Query().Get("project_id")
	}
	if id == "" {
		id = upstream.DefaultProject
	}
	return h.Upstream.ForProject(id)
}

func (h *Handler) ListUpstreamProjects(w http.ResponseWriter, r *http.Request) {
	_, canWrite, ok := h.upstreamScope(w, r, false)
	if !ok {
		return
	}
	projects, err := h.Upstream.Projects(r.Context())
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	for i := range projects {
		projects[i].Snapshot.CanWrite = canWrite
	}
	writeJSON(w, 200, map[string]any{"projects": projects, "can_write": canWrite})
}

func (h *Handler) SaveUpstreamProject(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.upstreamScope(w, r, true); !ok {
		return
	}
	var p upstream.Project
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&p); err != nil {
		writeError(w, 400, "项目格式无效")
		return
	}
	if err := h.Upstream.SaveProject(r.Context(), p); err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (h *Handler) SyncUpstreamProject(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.upstreamScope(w, r, true); !ok {
		return
	}
	// 同时串行人工派发与运行检查，覆盖任务尚未领取分析批次的时间窗口。
	tx, lockErr := h.TxStarter.Begin(r.Context())
	if lockErr != nil {
		writeError(w, 500, "同步派发不可用")
		return
	}
	defer tx.Rollback(context.Background())
	var locked bool
	if tx.QueryRow(r.Context(), `SELECT pg_try_advisory_xact_lock(871306201)`).Scan(&locked) != nil || !locked {
		writeError(w, 409, "已有同步派发正在执行")
		return
	}
	var busy bool
	if tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_task_queue t JOIN upstream_sync_config c ON c.agent_id=t.agent_id WHERE t.status IN ('queued','pending','dispatched','running','waiting_local_directory','deferred'))`).Scan(&busy) != nil {
		writeError(w, 500, "读取同步运行状态失败")
		return
	}
	if busy {
		writeError(w, 409, "已有项目等待同步或正在同步")
		return
	}
	var enabled bool
	if err := h.DB.QueryRow(r.Context(), `SELECT enabled FROM upstream_project WHERE id=$1`, h.upstreamService(r).Project()).Scan(&enabled); err != nil || !enabled {
		writeError(w, 409, "项目已停用")
		return
	}
	snap, err := h.upstreamService(r).Snapshot(r.Context())
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if snap.ActiveRunID != "" {
		writeError(w, 409, "该项目正在同步")
		return
	}
	if snap.Config.BootstrapBaseSHA == "" {
		writeError(w, 409, "请先在设置中指定分析起点 SHA")
		return
	}
	if snap.Config.AutopilotID == "" || snap.ExecutorState != "bound" {
		writeError(w, 409, "请先配置同步运行时")
		return
	}
	if _, err = h.DB.Exec(r.Context(), `UPDATE upstream_sync_config SET sync_state=CASE WHEN cursor_sha='' THEN 'bootstrap' ELSE 'incremental' END WHERE project_id=$1 AND sync_state='paused'`, h.upstreamService(r).Project()); err != nil {
		writeUpstreamError(w, err)
		return
	}
	if _, err = h.DB.Exec(r.Context(), `UPDATE autopilot SET status='active',updated_at=now() WHERE id=$1 AND status='paused'`, snap.Config.AutopilotID); err != nil {
		writeUpstreamError(w, err)
		return
	}
	rc := chi.RouteContext(r.Context())
	rc.URLParams.Add("id", snap.Config.AutopilotID)
	h.TriggerAutopilot(w, r)
}
