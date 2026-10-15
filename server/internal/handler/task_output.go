package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 调用方已执行与消息列表相同的工作区及任务可见性检查。
func (h *Handler) readTaskOutput(w http.ResponseWriter, r *http.Request, taskID pgtype.UUID) {
	id, ok := parseUUIDOrBadRequest(w, r.URL.Query().Get("output_id"), "output_id")
	if !ok {
		return
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			writeError(w, http.StatusBadRequest, "invalid output offset")
			return
		}
		offset = value
	}
	output, err := h.Queries.GetTaskMessageOutput(r.Context(), db.GetTaskMessageOutputParams{TaskID: taskID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "output not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read output")
		return
	}
	writeJSON(w, http.StatusOK, protocol.PageTaskOutput(output.String, offset, r.URL.Query().Get("q")))
}
