package handler

import (
	"encoding/json"
	"errors"
	"github.com/multica-ai/multica/server/internal/service"
	"net/http"
	"reflect"
	"time"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

func interactionResponse(row db.TaskInteraction, taskStatus string) protocol.TaskInteraction {
	var prompt protocol.InteractionPrompt
	_ = json.Unmarshal(row.Request, &prompt)
	status := row.Status
	if status == "pending" && (!interactionTaskAcceptsInput(row, taskStatus) || !row.ExpiresAt.Time.After(time.Now())) {
		status = "expired"
	}
	result := protocol.TaskInteraction{
		ID: uuidToString(row.ID), TaskID: uuidToString(row.TaskID), Provider: row.Provider,
		Prompt: prompt, Status: status, DeliveryMode: row.DeliveryMode, FollowupTaskID: uuidToString(row.FollowupTaskID), CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		ExpiresAt: row.ExpiresAt.Time.UTC().Format(time.RFC3339Nano),
	}
	if row.Status == "responded" {
		var response protocol.InteractionAnswer
		if json.Unmarshal(row.Response, &response) == nil {
			result.Response = &response
		}
	}
	return result
}

func (h *Handler) CreateTaskInteraction(w http.ResponseWriter, r *http.Request) {
	task, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	var request struct {
		ID       string                     `json:"id"`
		Provider string                     `json:"provider"`
		Prompt   protocol.InteractionPrompt `json:"prompt"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&request) != nil {
		writeError(w, 400, "invalid interaction request")
		return
	}
	id, ok := parseUUIDOrBadRequest(w, request.ID, "interaction_id")
	if !ok {
		return
	}
	if request.Provider != "codex" && request.Provider != "codebuddy" {
		writeError(w, 400, "unsupported interaction provider")
		return
	}
	if err := request.Prompt.Validate(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	clean := func(text string) string { return redact.Text(sanitizeNullBytes(text)) }
	request.Prompt.Title = clean(request.Prompt.Title)
	for i := range request.Prompt.Questions {
		q := &request.Prompt.Questions[i]
		q.Header, q.Question = clean(q.Header), clean(q.Question)
		for j := range q.Options {
			q.Options[j].Label, q.Options[j].Description = clean(q.Options[j].Label), clean(q.Options[j].Description)
		}
	}
	body, _ := json.Marshal(request.Prompt)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to create interaction")
		return
	}
	defer tx.Rollback(r.Context())
	var status, provider string
	if err := tx.QueryRow(r.Context(), `SELECT t.status, rt.provider FROM agent_task_queue t JOIN agent_runtime rt ON rt.id=t.runtime_id WHERE t.id=$1 FOR UPDATE OF t`, task.ID).Scan(&status, &provider); err != nil {
		writeError(w, 500, "failed to load interaction task")
		return
	}
	if status != "running" {
		writeError(w, 409, "task is no longer running")
		return
	}
	if provider != request.Provider {
		writeError(w, 400, "interaction provider does not match task runtime")
		return
	}
	row, err := h.Queries.WithTx(tx).CreateTaskInteraction(r.Context(), db.CreateTaskInteractionParams{
		TaskID: task.ID, ID: id, WorkspaceID: parseUUID(workspaceID), IssueID: task.IssueID,
		Provider: provider, Kind: request.Prompt.Kind, Request: body,
	})
	if err != nil {
		if isNotFound(err) {
			writeError(w, 409, "interaction identity conflicts with an existing request")
		} else {
			writeError(w, 500, "failed to save interaction")
		}
		return
	}
	if tx.Commit(r.Context()) != nil {
		writeError(w, 500, "failed to save interaction")
		return
	}
	writeJSON(w, 200, interactionResponse(row, status))
}

func (h *Handler) GetTaskInteractionByDaemon(w http.ResponseWriter, r *http.Request) {
	task, ok := h.requireDaemonTaskAccess(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "interactionId"), "interaction_id")
	if !ok {
		return
	}
	row, err := h.Queries.GetTaskInteraction(r.Context(), db.GetTaskInteractionParams{TaskID: task.ID, ID: id})
	if err != nil {
		if isNotFound(err) {
			writeError(w, 404, "interaction not found")
		} else {
			writeError(w, 500, "failed to read interaction")
		}
		return
	}
	writeJSON(w, 200, interactionResponse(row, task.Status))
}

// 活跃 daemon 续租等待请求；进程丢失后旧请求会在一分钟内失效。
func (h *Handler) RenewTaskInteractionLease(w http.ResponseWriter, r *http.Request) {
	task, ok := h.requireDaemonTaskAccess(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "interactionId"), "interaction_id")
	if !ok {
		return
	}
	row, err := h.Queries.RenewTaskInteractionLease(r.Context(), db.RenewTaskInteractionLeaseParams{TaskID: task.ID, ID: id})
	if isNotFound(err) {
		row, err = h.Queries.GetTaskInteraction(r.Context(), db.GetTaskInteractionParams{TaskID: task.ID, ID: id})
	}
	if err != nil {
		if isNotFound(err) {
			writeError(w, 404, "interaction not found")
		} else {
			writeError(w, 500, "failed to renew interaction lease")
		}
		return
	}
	writeJSON(w, 200, interactionResponse(row, task.Status))
}

func (h *Handler) ExpireTaskInteraction(w http.ResponseWriter, r *http.Request) {
	task, ok := h.requireDaemonTaskAccess(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "interactionId"), "interaction_id")
	if !ok {
		return
	}
	if err := h.Queries.ExpireTaskInteraction(r.Context(), db.ExpireTaskInteractionParams{TaskID: task.ID, ID: id}); err != nil {
		writeError(w, 500, "failed to expire interaction")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "expired"})
}

func (h *Handler) ListTaskInteractions(w http.ResponseWriter, r *http.Request) {
	task, ok := h.loadTaskForUser(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListTaskInteractions(r.Context(), task.ID)
	if err != nil {
		writeError(w, 500, "failed to read interactions")
		return
	}
	result := make([]protocol.TaskInteraction, 0, len(rows))
	for _, row := range rows {
		result = append(result, interactionResponse(row, task.Status))
	}
	writeJSON(w, 200, map[string]any{"interactions": result})
}

func (h *Handler) RespondTaskInteraction(w http.ResponseWriter, r *http.Request) {
	if isMachineCredentialActor(r) {
		writeError(w, http.StatusForbidden, "this endpoint is only available to human actors")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	task, ok := h.loadTaskForUser(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "interactionId"), "interaction_id")
	if !ok {
		return
	}
	author, ok := parseUUIDOrBadRequest(w, userID, "user_id")
	if !ok {
		return
	}
	var answer protocol.InteractionAnswer
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&answer) != nil {
		writeError(w, 400, "invalid interaction answer")
		return
	}
	current, err := h.Queries.GetTaskInteraction(r.Context(), db.GetTaskInteractionParams{TaskID: task.ID, ID: id})
	if err != nil {
		if isNotFound(err) {
			writeError(w, 404, "interaction not found")
		} else {
			writeError(w, 500, "failed to read interaction")
		}
		return
	}
	if current.DeliveryMode == "suspended" {
		agent, err := h.Queries.GetAgent(r.Context(), task.AgentID)
		if err != nil {
			writeError(w, 500, "failed to load interaction agent")
			return
		}
		if !h.canInvokeAgent(r.Context(), agent, "member", userID, userID, uuidToString(current.WorkspaceID)) {
			writeError(w, 403, "agent invocation is not allowed")
			return
		}
		var prompt protocol.InteractionPrompt
		if json.Unmarshal(current.Request, &prompt) != nil {
			writeError(w, 500, "invalid persisted interaction")
			return
		}
		if err := prompt.ValidateContinuation(answer); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		row, err := h.TaskService.RespondSuspendedInteraction(r.Context(), task.ID, id, author, answer)
		if err != nil {
			switch {
			case errors.Is(err, service.ErrInteractionConflict):
				writeError(w, 409, err.Error())
			case errors.Is(err, service.ErrInteractionNotAllowed):
				writeError(w, 403, err.Error())
			default:
				writeError(w, 500, "failed to create interaction continuation")
			}
			return
		}
		writeJSON(w, 200, interactionResponse(row, task.Status))
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to answer interaction")
		return
	}
	defer tx.Rollback(r.Context())
	var status string
	if tx.QueryRow(r.Context(), `SELECT status FROM agent_task_queue WHERE id=$1 FOR UPDATE`, task.ID).Scan(&status) != nil {
		writeError(w, 500, "failed to load interaction task")
		return
	}
	queries := h.Queries.WithTx(tx)
	row, err := queries.GetTaskInteraction(r.Context(), db.GetTaskInteractionParams{TaskID: task.ID, ID: id})
	if err != nil {
		if isNotFound(err) {
			writeError(w, 404, "interaction not found")
		} else {
			writeError(w, 500, "failed to read interaction")
		}
		return
	}
	if row.Status == "responded" {
		var previous protocol.InteractionAnswer
		_ = json.Unmarshal(row.Response, &previous)
		if reflect.DeepEqual(answer, previous) {
			writeJSON(w, 200, interactionResponse(row, status))
			return
		}
		writeError(w, 409, "interaction already answered")
		return
	}
	if interactionResponse(row, status).Status != "pending" {
		writeError(w, 409, "interaction is no longer pending")
		return
	}
	var prompt protocol.InteractionPrompt
	if json.Unmarshal(row.Request, &prompt) != nil {
		writeError(w, 500, "invalid persisted interaction")
		return
	}
	if err := prompt.ValidateAnswer(answer); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	body, _ := json.Marshal(answer)
	row, err = queries.RespondTaskInteraction(r.Context(), db.RespondTaskInteractionParams{TaskID: task.ID, ID: id, Response: body, AuthorID: author})
	if err != nil {
		writeError(w, 409, "interaction changed before the answer was saved")
		return
	}
	if tx.Commit(r.Context()) != nil {
		writeError(w, 500, "failed to save interaction answer")
		return
	}
	writeJSON(w, 200, interactionResponse(row, status))
}

func interactionTaskAcceptsInput(row db.TaskInteraction, status string) bool {
	if row.DeliveryMode == "suspended" {
		return row.Provider == "dsh" && ((row.Kind == "plan" && status == "waiting_plan") || (row.Kind == "question" && status == "waiting_input"))
	}
	return status == "running"
}
