package handler

import (
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/openviking"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

func (h *Handler) ListOpenVikingEvents(w http.ResponseWriter, r *http.Request) {
	if !openviking.Enabled() {
		writeError(w, 503, "OpenViking integration is disabled")
		return
	}
	offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if r.URL.Query().Get("offset") == "" {
		offset = 0
		err = nil
	}
	if err != nil || offset < 0 {
		writeError(w, 400, "invalid offset")
		return
	}
	rows, err := h.DB.Query(r.Context(), `SELECT id::text,COALESCE(workspace_id::text,''),COALESCE(agent_id::text,''),COALESCE(chat_session_id::text,''),COALESCE(issue_id::text,''),target_user,kind,payload FROM openviking_event WHERE delivered_at IS NULL ORDER BY created_at,id LIMIT 50 OFFSET $1`, offset)
	if err != nil {
		writeError(w, 500, "could not read archive events")
		return
	}
	defer rows.Close()
	events := []openviking.PlatformEvent{}
	for rows.Next() {
		var e openviking.PlatformEvent
		var raw []byte
		if err = rows.Scan(&e.ID, &e.WorkspaceID, &e.AgentID, &e.ChatID, &e.IssueID, &e.User, &e.Kind, &raw); err != nil {
			writeError(w, 500, "could not decode archive event")
			return
		}
		e.Payload = raw
		events = append(events, e)
	}
	if err = rows.Err(); err != nil {
		writeError(w, 500, "could not read archive events")
		return
	}
	writeJSON(w, 200, map[string]any{"events": events, "has_more": len(events) == 50})
}

func (h *Handler) AckOpenVikingEvent(w http.ResponseWriter, r *http.Request) {
	if !openviking.Enabled() {
		writeError(w, 503, "OpenViking integration is disabled")
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "eventId"), "event_id")
	if !ok {
		return
	}
	result, err := h.DB.Exec(r.Context(), `UPDATE openviking_event SET delivered_at=COALESCE(delivered_at,clock_timestamp()) WHERE id=$1`, id)
	if err != nil {
		writeError(w, 500, "could not acknowledge archive event")
		return
	}
	if result.RowsAffected() != 1 {
		writeError(w, 404, "archive event not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (h *Handler) GetOpenVikingObject(w http.ResponseWriter, r *http.Request) {
	if !openviking.Enabled() {
		writeError(w, 503, "OpenViking integration is disabled")
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "objectId"), "object_id")
	if !ok {
		return
	}
	var source []byte
	err := h.DB.QueryRow(r.Context(), `SELECT payload->'row' FROM openviking_event WHERE kind='attachment' AND payload->'row'->>'id'=$1 ORDER BY created_at DESC LIMIT 1`, uuidToString(id)).Scan(&source)
	if err != nil {
		writeError(w, 404, "original attachment not found")
		return
	}
	if r.URL.Query().Get("metadata") == "true" {
		writeJSON(w, 200, json.RawMessage(source))
		return
	}
	var metadata struct {
		URL         string `json:"url"`
		ContentType string `json:"content_type"`
	}
	if json.Unmarshal(source, &metadata) != nil {
		writeError(w, 500, "invalid original attachment metadata")
		return
	}
	link, contentType := metadata.URL, metadata.ContentType
	path := openviking.ObjectPath(os.Getenv("MULTICA_OPENVIKING_ARCHIVE_ROOT"), uuidToString(id))
	file, err := os.Open(path)
	if err == nil {
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			writeError(w, 500, "could not read original attachment")
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
		return
	}
	if !os.IsNotExist(err) || h.Storage == nil {
		writeError(w, 503, "original attachment is unavailable")
		return
	}
	// 历史文件尚未进入本地 outbox 时，从原对象存储读取；找不到就保留失败事件。
	reader, err := h.Storage.GetReader(r.Context(), h.Storage.KeyFromURL(link))
	if err != nil {
		writeError(w, 503, "historical attachment is unavailable")
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", contentType)
	_, _ = io.Copy(w, reader)
}

// OpenVikingEventReady 等待对应任务结束，使原生记忆提取包含完整的一轮交互。
func (h *Handler) OpenVikingEventReady(w http.ResponseWriter, r *http.Request) {
	if !openviking.Enabled() {
		writeError(w, 503, "OpenViking integration is disabled")
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "eventId"), "event_id")
	if !ok {
		return
	}
	var ready bool
	err := h.DB.QueryRow(r.Context(), `SELECT NOT EXISTS (
 SELECT 1 FROM agent_task_queue q WHERE q.agent_id=e.agent_id
 AND q.status IN ('queued','dispatched','running','waiting_local_directory','deferred','waiting_plan','waiting_input')
 AND ((e.chat_session_id IS NOT NULL AND q.chat_session_id=e.chat_session_id)
 OR (e.chat_session_id IS NULL AND e.issue_id IS NOT NULL AND q.issue_id=e.issue_id)))
 FROM openviking_event e WHERE e.id=$1`, id).Scan(&ready)
	if err != nil {
		writeError(w, 500, "could not determine conversation state")
		return
	}
	writeJSON(w, 200, map[string]bool{"ready": ready})
}
