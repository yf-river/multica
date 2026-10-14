package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) ListWorkspaceMcpChecks(w http.ResponseWriter, r *http.Request) {
	ws, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	if !h.requireWorkspaceMcpWriter(w, r, uuidToString(ws)) {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "serverId"), "server id")
	if !ok {
		return
	}
	if err := h.Queries.ExpireWorkspaceMcpChecks(r.Context(), ws); err != nil {
		writeError(w, 500, "failed to expire checks")
		return
	}
	rows, err := h.Queries.ListWorkspaceMcpChecks(r.Context(), db.ListWorkspaceMcpChecksParams{WorkspaceID: ws, ServerID: id})
	if err != nil {
		writeError(w, 500, "failed to list checks")
		return
	}
	if rows == nil {
		rows = []db.ListWorkspaceMcpChecksRow{}
	}
	writeJSON(w, 200, rows)
}
func (h *Handler) CreateWorkspaceMcpCheck(w http.ResponseWriter, r *http.Request) {
	ws, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	if !h.requireWorkspaceMcpWriter(w, r, uuidToString(ws)) {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "serverId"), "server id")
	if !ok {
		return
	}
	var req struct {
		RuntimeID string `json:"runtime_id"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	rt, ok := h.requireDaemonRuntimeAccess(w, r, req.RuntimeID)
	if !ok {
		return
	}
	member, allowed := h.requireWorkspaceMember(w, r, uuidToString(ws), "workspace not found")
	if !allowed {
		return
	}
	if !canUseRuntimeForAgent(member, rt) {
		writeError(w, 403, "该运行时属于其他成员且未共享。")
		return
	}
	if rt.WorkspaceID != ws {
		writeError(w, 404, "runtime not found")
		return
	}
	if !rt.LastSeenAt.Valid || time.Since(rt.LastSeenAt.Time) > 2*time.Minute {
		writeError(w, 409, "运行时离线，请先启动运行时。")
		return
	}
	var metadata struct {
		Capabilities []string `json:"capabilities"`
	}
	_ = json.Unmarshal(rt.Metadata, &metadata)
	supported := false
	for _, cap := range metadata.Capabilities {
		if cap == "workspace-mcp-check-v1" {
			supported = true
		}
	}
	if !supported {
		writeError(w, 409, "运行时版本不支持连接测试，请更新运行时。")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to begin check")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if _, err = qtx.LockWorkspaceForChatSessionCreate(r.Context(), ws); err != nil {
		writeError(w, 404, "workspace not found")
		return
	}
	if _, err = qtx.LockWorkspaceMcpServerForShare(r.Context(), db.LockWorkspaceMcpServerForShareParams{ID: id, WorkspaceID: ws}); err != nil {
		writeError(w, 404, "MCP server not found")
		return
	}
	server, err := qtx.GetWorkspaceMcpServer(r.Context(), db.GetWorkspaceMcpServerParams{ID: id, WorkspaceID: ws})
	if err != nil {
		writeError(w, 404, "MCP server not found")
		return
	}
	if err = h.Queries.ExpireWorkspaceMcpChecks(r.Context(), ws); err != nil {
		writeError(w, 500, "failed to expire checks")
		return
	}
	_, err = qtx.UpsertWorkspaceMcpCheck(r.Context(), db.UpsertWorkspaceMcpCheckParams{WorkspaceID: ws, ServerID: id, RuntimeID: rt.ID, ConfigVersion: server.ConfigVersion})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 500, "failed to create check")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to commit check")
		return
	}
	h.ListWorkspaceMcpChecks(w, r)
}
func (h *Handler) pendingWorkspaceMcpCheck(ctx context.Context, rt db.AgentRuntime) *protocol.DaemonHeartbeatPendingMcpCheck {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, err := h.Queries.ClaimWorkspaceMcpCheck(ctx, rt.ID)
	if err != nil {
		return nil
	}
	s, err := h.Queries.GetWorkspaceMcpServer(ctx, db.GetWorkspaceMcpServerParams{ID: c.ServerID, WorkspaceID: rt.WorkspaceID})
	if err != nil || s.ConfigVersion != c.ConfigVersion {
		return nil
	}
	return &protocol.DaemonHeartbeatPendingMcpCheck{ID: uuidToString(c.ID), Config: s.Config}
}
func (h *Handler) ReportWorkspaceMcpCheck(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.requireDaemonRuntimeAccess(w, r, chi.URLParam(r, "runtimeId"))
	if !ok {
		return
	}
	if !canReceiveMcpCheck(rt, middleware.DaemonIDFromContext(r.Context()), requestUserID(r)) {
		writeError(w, 403, "runtime identity required")
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "requestId"), "request id")
	if !ok {
		return
	}
	var body struct {
		ToolCount int32  `json:"tool_count"`
		ErrorCode string `json:"error_code"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.ToolCount < 0 {
		writeError(w, 400, "invalid result")
		return
	}
	status := "completed"
	if body.ErrorCode != "" {
		status = "failed"
		switch body.ErrorCode {
		case "timeout", "start_failed", "connection_failed", "authentication_failed", "protocol_error", "unsupported_transport":
		default:
			body.ErrorCode = "connection_failed"
		}
	}
	if body.ErrorCode == "timeout" {
		status = "timeout"
	}
	_, err := h.Queries.CompleteWorkspaceMcpCheck(r.Context(), db.CompleteWorkspaceMcpCheckParams{ID: id, RuntimeID: rt.ID, WorkspaceID: rt.WorkspaceID, Status: status, ToolCount: body.ToolCount, ErrorCode: body.ErrorCode})
	if err != nil {
		writeError(w, 500, "failed to save result")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func canReceiveMcpCheck(rt db.AgentRuntime, daemonID, userID string) bool {
	if daemonID != "" {
		return rt.DaemonID.Valid && rt.DaemonID.String == daemonID
	}
	return userID != "" && rt.OwnerID.Valid && uuidToString(rt.OwnerID) == userID
}
