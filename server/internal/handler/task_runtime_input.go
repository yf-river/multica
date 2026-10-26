package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func runtimeInputResponse(row db.TaskRuntimeInput) protocol.TaskRuntimeInput {
	// 原生注入最多等待八秒，回执另有五秒预算。领取回执丢失也不能无限显示为正在投递。
	if row.Status == "delivering" && row.ClaimedAt.Valid && time.Since(row.ClaimedAt.Time) > 30*time.Second {
		row.Status, row.FailureReason = "failed", "delivery_unknown"
	}
	return protocol.TaskRuntimeInput{
		ID: uuidToString(row.ID), TaskID: uuidToString(row.TaskID), AuthorID: uuidToString(row.AuthorID),
		Kind: row.Kind, Content: row.Content, Status: row.Status, Output: row.Output, FailureReason: row.FailureReason,
		CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
	}
}

func parseRuntimeInputClaim(w http.ResponseWriter, claim protocol.RuntimeInputClaim) (pgtype.UUID, pgtype.Timestamptz, bool) {
	id, ok := parseUUIDOrBadRequest(w, claim.RuntimeID, "runtime_id")
	if !ok {
		return pgtype.UUID{}, pgtype.Timestamptz{}, false
	}
	generation, err := time.Parse(time.RFC3339Nano, claim.DispatchedAt)
	if err != nil || generation.Nanosecond()%1000 != 0 {
		writeError(w, 400, "dispatched_at must be a PostgreSQL claim timestamp")
		return pgtype.UUID{}, pgtype.Timestamptz{}, false
	}
	return id, pgtype.Timestamptz{Time: generation, Valid: true}, true
}

func (h *Handler) RegisterTaskRuntimeCapability(w http.ResponseWriter, r *http.Request) {
	task, ok := h.requireDaemonTaskAccess(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	var req struct {
		protocol.RuntimeInputClaim
		Capabilities []string `json:"capabilities"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil || len(req.Capabilities) == 0 || len(req.Capabilities) > 2 {
		writeError(w, 400, "invalid runtime capabilities")
		return
	}
	slices.Sort(req.Capabilities)
	for i, value := range req.Capabilities {
		if (value != "text" && value != "command") || (i > 0 && value == req.Capabilities[i-1]) {
			writeError(w, 400, "unsupported runtime capability")
			return
		}
	}
	runtimeID, generation, ok := parseRuntimeInputClaim(w, req.RuntimeInputClaim)
	if !ok {
		return
	}
	row, err := h.Queries.RegisterTaskRuntimeCapability(r.Context(), db.RegisterTaskRuntimeCapabilityParams{
		TaskID: task.ID, RuntimeID: runtimeID, DispatchedAt: generation, Capabilities: req.Capabilities,
	})
	if isNotFound(err) {
		writeError(w, 409, "task claim or capabilities changed")
		return
	}
	if err != nil {
		writeError(w, 500, "failed to register runtime capabilities")
		return
	}
	writeJSON(w, 200, map[string]any{"capabilities": row.Capabilities})
}

func (h *Handler) runtimeInputAuthorAllowed(r *http.Request, task db.AgentTaskQueue) bool {
	if isMachineCredentialActor(r) || requestUserID(r) == "" {
		return false
	}
	wsID, userID := middleware.WorkspaceIDFromContext(r.Context()), requestUserID(r)
	actorType, _ := h.resolveActor(r, userID, wsID)
	if actorType != "member" {
		return false
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: task.AgentID, WorkspaceID: parseUUID(wsID)})
	return err == nil && !agent.ArchivedAt.Valid && h.canInvokeAgent(r.Context(), agent, "member", userID, userID, wsID)
}

func (h *Handler) CreateTaskRuntimeInput(w http.ResponseWriter, r *http.Request) {
	if isMachineCredentialActor(r) {
		writeError(w, 403, "runtime input requires a human actor")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	authorID, ok := parseUUIDOrBadRequest(w, userID, "user_id")
	if !ok {
		return
	}
	task, ok := h.loadTaskForUser(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	if !h.runtimeInputAuthorAllowed(r, task) {
		writeError(w, 403, "you cannot run this agent")
		return
	}
	var req struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		Content string `json:"content"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 128000)).Decode(&req) != nil {
		writeError(w, 400, "invalid runtime input")
		return
	}
	if err := protocol.ValidateRuntimeInput(req.Kind, req.Content); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	id, ok := parseUUIDOrBadRequest(w, req.ID, "id")
	if !ok {
		return
	}
	lookup := db.GetTaskRuntimeInputParams{TaskID: task.ID, ID: id}
	existing, err := h.Queries.GetTaskRuntimeInput(r.Context(), lookup)
	if err == nil {
		if uuidToString(existing.AuthorID) != userID || existing.Kind != req.Kind || existing.Content != req.Content {
			writeError(w, 409, "runtime input identity conflicts with an existing input")
			return
		}
		writeJSON(w, 200, runtimeInputResponse(existing))
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 500, "failed to read runtime input")
		return
	}
	row, err := h.Queries.CreateTaskRuntimeInput(r.Context(), db.CreateTaskRuntimeInputParams{
		TaskID: task.ID, ID: id, WorkspaceID: parseUUID(middleware.WorkspaceIDFromContext(r.Context())),
		AuthorID: authorID, Kind: req.Kind, Content: req.Content,
	})
	if isUniqueViolation(err) {
		// 并发的同一逻辑输入只能保留原行，重走幂等校验而不产生第二次投递。
		h.writeExistingRuntimeInput(w, r, lookup, userID, req.Kind, req.Content)
		return
	}
	if isNotFound(err) {
		writeError(w, 409, "this execution cannot accept that input")
		return
	}
	if err != nil {
		writeError(w, 500, "failed to save runtime input")
		return
	}
	writeJSON(w, 201, runtimeInputResponse(row))
}

func (h *Handler) writeExistingRuntimeInput(w http.ResponseWriter, r *http.Request, lookup db.GetTaskRuntimeInputParams, authorID, kind, content string) {
	row, err := h.Queries.GetTaskRuntimeInput(r.Context(), lookup)
	if err != nil {
		writeError(w, 500, "failed to read runtime input")
		return
	}
	if uuidToString(row.AuthorID) != authorID || row.Kind != kind || row.Content != content {
		writeError(w, 409, "runtime input identity conflicts with an existing input")
		return
	}
	writeJSON(w, 200, runtimeInputResponse(row))
}

func (h *Handler) ListTaskRuntimeInputs(w http.ResponseWriter, r *http.Request) {
	task, ok := h.loadTaskForUser(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	beforeTime, beforeID := time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC), parseUUID("ffffffff-ffff-ffff-ffff-ffffffffffff")
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		stamp, value, found := strings.Cut(string(raw), "\n")
		var timeErr error
		beforeTime, timeErr = time.Parse(time.RFC3339Nano, stamp)
		if err != nil || !found || timeErr != nil || beforeID.Scan(value) != nil || !beforeID.Valid {
			writeError(w, 400, "invalid runtime input cursor")
			return
		}
	}
	rows, err := h.Queries.ListTaskRuntimeInputs(r.Context(), db.ListTaskRuntimeInputsParams{
		TaskID: task.ID, BeforeCreatedAt: pgtype.Timestamptz{Time: beforeTime, Valid: true}, BeforeID: beforeID, PageLimit: 101,
	})
	if err != nil {
		writeError(w, 500, "failed to list runtime inputs")
		return
	}
	next := ""
	if len(rows) > 100 {
		rows = rows[:100]
		last := rows[len(rows)-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt.Time.UTC().Format(time.RFC3339Nano) + "\n" + uuidToString(last.ID)))
	}
	inputs := make([]protocol.TaskRuntimeInput, 0, len(rows))
	for _, row := range rows {
		inputs = append(inputs, runtimeInputResponse(row))
	}
	writeJSON(w, 200, map[string]any{"inputs": inputs, "next_cursor": next})
}

func (h *Handler) GetTaskRuntimeCapabilities(w http.ResponseWriter, r *http.Request) {
	task, ok := h.loadTaskForUser(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	capabilities := []string{}
	if task.Status == "running" && (task.IssueID.Valid || task.ChatSessionID.Valid) && h.runtimeInputAuthorAllowed(r, task) {
		cap, err := h.Queries.GetTaskRuntimeCapability(r.Context(), task.ID)
		if err == nil && cap.RuntimeID == task.RuntimeID && cap.DispatchedAt.Time.Equal(task.DispatchedAt.Time) {
			capabilities = cap.Capabilities
		} else if err != nil && !isNotFound(err) {
			writeError(w, 500, "failed to load runtime capabilities")
			return
		}
	}
	writeJSON(w, 200, map[string]any{"capabilities": capabilities})
}

func (h *Handler) ClaimTaskRuntimeInput(w http.ResponseWriter, r *http.Request) {
	task, ok := h.requireDaemonTaskAccess(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	var req struct {
		protocol.RuntimeInputClaim
		ReadyKinds []string `json:"ready_kinds"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil || len(req.ReadyKinds) == 0 || len(req.ReadyKinds) > 2 {
		writeError(w, 400, "invalid ready kinds")
		return
	}
	for _, kind := range req.ReadyKinds {
		if kind != "text" && kind != "command" {
			writeError(w, 400, "invalid ready kind")
			return
		}
	}
	runtimeID, generation, ok := parseRuntimeInputClaim(w, req.RuntimeInputClaim)
	if !ok {
		return
	}
	row, err := h.Queries.ClaimTaskRuntimeInput(r.Context(), db.ClaimTaskRuntimeInputParams{
		TaskID: task.ID, RuntimeID: runtimeID, DispatchedAt: generation, ReadyKinds: req.ReadyKinds,
	})
	if isNotFound(err) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, 500, "failed to claim runtime input")
		return
	}
	writeJSON(w, 200, runtimeInputResponse(row))
}

func (h *Handler) AckTaskRuntimeInput(w http.ResponseWriter, r *http.Request) {
	task, ok := h.requireDaemonTaskAccess(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "inputId"), "input_id")
	if !ok {
		return
	}
	var req protocol.RuntimeInputAck
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 128000)).Decode(&req) != nil || len(req.Output) > 32000 || strings.ContainsRune(req.Output, '\x00') {
		writeError(w, 400, "invalid runtime input receipt")
		return
	}
	if (req.Status != "delivered" && req.Status != "failed") || (req.Status == "delivered" && req.FailureReason != "") ||
		(req.Status == "failed" && !slices.Contains([]string{"delivery_unknown", "provider_rejected", "turn_ended"}, req.FailureReason)) {
		writeError(w, 400, "invalid runtime input outcome")
		return
	}
	runtimeID, generation, ok := parseRuntimeInputClaim(w, req.RuntimeInputClaim)
	if !ok {
		return
	}
	cap, err := h.Queries.GetTaskRuntimeCapability(r.Context(), task.ID)
	if err != nil || cap.RuntimeID != runtimeID || !cap.DispatchedAt.Time.Equal(generation.Time) {
		writeError(w, 409, "runtime input claim changed")
		return
	}
	row, err := h.Queries.AckTaskRuntimeInput(r.Context(), db.AckTaskRuntimeInputParams{
		TaskID: task.ID, ID: id, Status: req.Status, Output: req.Output, FailureReason: req.FailureReason,
	})
	if isNotFound(err) {
		row, err = h.Queries.GetTaskRuntimeInput(r.Context(), db.GetTaskRuntimeInputParams{TaskID: task.ID, ID: id})
		if err == nil && (row.Status != req.Status || row.Output != req.Output || row.FailureReason != req.FailureReason) {
			writeError(w, 409, "runtime input already has another outcome or was not claimed")
			return
		}
	}
	if isNotFound(err) {
		writeError(w, 404, "runtime input not found")
		return
	}
	if err != nil {
		writeError(w, 500, "failed to save runtime input receipt")
		return
	}
	writeJSON(w, 200, runtimeInputResponse(row))
}
