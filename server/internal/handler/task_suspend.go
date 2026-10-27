package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// SuspendTask 接收已退出的原生进程结果；人工交互与恢复指针在同一事务提交。
func (h *Handler) SuspendTask(w http.ResponseWriter, r *http.Request) {
	task, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	var request struct {
		TaskCompleteRequest
		Interaction protocol.SuspendedInteraction `json:"interaction"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024)).Decode(&request) != nil {
		writeError(w, 400, "invalid suspended task result")
		return
	}
	if err := request.Interaction.Validate(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	sanitizeTaskCompleteRequest(&request.TaskCompleteRequest)
	if request.SessionID == "" || request.WorkDir == "" || request.SessionRolloutMissing || request.RetiredSessionID != "" {
		writeError(w, 400, "suspended tasks require a resumable native session")
		return
	}
	result, err := json.Marshal(request.TaskCompleteRequest)
	if err != nil {
		writeError(w, 400, "invalid suspended task result")
		return
	}
	suspended, err := h.TaskService.SuspendTaskForInteraction(r.Context(), task.ID, request.Interaction, result, request.SessionID, request.WorkDir)
	if err != nil {
		if errors.Is(err, service.ErrInteractionConflict) {
			writeError(w, 409, err.Error())
			return
		}
		writeError(w, 500, "failed to persist suspended task")
		return
	}
	writeJSON(w, 200, taskToResponse(*suspended, workspaceID))
}
