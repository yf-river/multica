package handler

// Runtime context events for one task run.
//
// The DSH adapter forwards the runtime's per-request context frames as
// task_message rows with type "runtime". They are not model-visible messages;
// the run view consumes them to draw context pressure, composition and
// compaction history without parsing provider session logs.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// GetTaskContext returns the runtime telemetry events recorded for one task.
// GET /api/issues/{id}/tasks/{taskId}/context
func (h *Handler) GetTaskContext(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	taskID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "taskId"), "task_id")
	if !ok {
		return
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
		ID:          taskID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || uuidToString(task.IssueID) != uuidToString(issue.ID) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	afterSeq, afterID, limit, err := parseTaskContextPage(r.URL.Query().Get("after"), r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	messages, err := h.Queries.ListTaskRuntimeMessagesPage(r.Context(), db.ListTaskRuntimeMessagesPageParams{
		TaskID: task.ID, AfterSeq: afterSeq, AfterID: afterID, PageLimit: limit + 1,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read task context")
		return
	}
	hasMore := len(messages) > int(limit)
	if hasMore {
		messages = messages[:limit]
	}
	events := make([]json.RawMessage, 0, len(messages))
	for _, message := range messages {
		if !message.Content.Valid || message.Content.String == "" {
			continue
		}
		// Only forward rows that are valid JSON; a partial write must not break
		// the whole panel.
		var event struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal([]byte(message.Content.String), &event) != nil || event.Kind == "" {
			continue
		}
		events = append(events, json.RawMessage(message.Content.String))
	}
	nextCursor := ""
	if hasMore && len(messages) > 0 {
		last := messages[len(messages)-1]
		nextCursor = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%s", last.Seq, uuidToString(last.ID))))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "next_cursor": nextCursor, "has_more": hasMore})
}

// 分页游标包含稳定排序键，重复 seq 的历史记录也能完整翻页。
func parseTaskContextPage(after, rawLimit string) (int32, pgtype.UUID, int32, error) {
	id := parseUUID("00000000-0000-0000-0000-000000000000")
	seq := int64(-1)
	limit := int64(500)
	if rawLimit != "" {
		value, err := strconv.ParseInt(rawLimit, 10, 32)
		if err != nil || value < 1 || value > 1000 {
			return 0, id, 0, fmt.Errorf("limit must be between 1 and 1000")
		}
		limit = value
	}
	if after != "" {
		if len(after) > 128 {
			return 0, id, 0, fmt.Errorf("invalid context cursor")
		}
		raw, err := base64.RawURLEncoding.DecodeString(after)
		parts := strings.SplitN(string(raw), ":", 2)
		if err != nil || len(parts) != 2 {
			return 0, id, 0, fmt.Errorf("invalid context cursor")
		}
		seq, err = strconv.ParseInt(parts[0], 10, 32)
		if err != nil || seq < 0 || id.Scan(parts[1]) != nil {
			return 0, id, 0, fmt.Errorf("invalid context cursor")
		}
	}
	return int32(seq), id, int32(limit), nil
}
