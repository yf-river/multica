package handler

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"unicode/utf8"

	"net/http"

	db "github.com/multica-ai/multica/server/pkg/db/generated"

	"github.com/multica-ai/multica/server/internal/codediff"
	"github.com/multica-ai/multica/server/internal/util"
)

// runTextTarget names the platform object a task edited. Prompt text lives in
// the database, so a code diff can never show it; these rows are what make a
// prompt change reviewable next to the files it produced.
type runTextTarget struct {
	Kind string
	ID   string
	Name string
}

type runTextChangePayload struct {
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
	TargetName string `json:"target_name,omitempty"`
	Field      string `json:"field"`
	Before     string `json:"before,omitempty"`
	After      string `json:"after,omitempty"`
	Added      int    `json:"added"`
	Deleted    int    `json:"deleted"`
	Redacted   bool   `json:"redacted,omitempty"`
}

// recordRunTextChange stores one field edit made by the agent that is running
// this task. It is best effort: a failure here must never fail the edit the
// user asked for, only cost the record.
func (h *Handler) recordRunTextChange(ctx context.Context, q *db.Queries, r *http.Request, workspaceID string, target runTextTarget, field, before, after string) error {
	if before == after {
		return nil
	}
	task, ok := h.taskFromRequestHeader(r)
	if !ok {
		return nil
	}
	// Attribution needs a live run: a member editing in the UI, or an agent
	// writing after its run finished, is not "this task changed it".
	if task.Status != "running" {
		return nil
	}
	agent, err := h.Queries.GetAgent(ctx, task.AgentID)
	if err != nil || uuidToString(agent.WorkspaceID) != workspaceID {
		return nil
	}
	added, deleted := codediff.CountLineChanges(before, after)
	redacted := codediff.ContainsSecret(before) || codediff.ContainsSecret(after)
	if redacted {
		// The credential itself is what we must not persist, but the shape of
		// the edit (how much moved) is still useful.
		before, after = "", ""
	}
	// A body budget keeps one pathological prompt from bloating the row; the
	// counts above already describe the whole edit.
	before, after = clipText(before), clipText(after)
	if _, err := q.InsertRunTextChange(ctx, db.InsertRunTextChangeParams{
		WorkspaceID: agent.WorkspaceID,
		TaskID:      task.ID,
		TargetKind:  target.Kind,
		TargetID:    target.ID,
		TargetName:  target.Name,
		Field:       field,
		BeforeText:  before,
		AfterText:   after,
		Added:       int32(added),
		Deleted:     int32(deleted),
		Redacted:    redacted,
	}); err != nil {
		slog.Warn("record run text change failed", "task_id", util.UUIDToString(task.ID), "field", field, "error", err)
		return err
	}

	return nil
}

// attachRunTextChanges folds the text edits of this task into the terminal
// result so the card can render them beside the file diff.
func (h *Handler) attachRunTextChanges(ctx context.Context, taskID string, result map[string]json.RawMessage) map[string]json.RawMessage {
	taskUUID, err := util.ParseUUID(taskID)
	if err != nil {
		return result
	}
	rows, err := h.Queries.ListRunTextChangesByTask(ctx, taskUUID)
	if err != nil {
		slog.Warn("load run text changes failed", "task_id", taskID, "error", err)
		return result
	}
	if len(rows) == 0 {
		return result
	}
	changes := make([]runTextChangePayload, 0, len(rows))
	for _, row := range rows {
		changes = append(changes, runTextChangePayload{
			TargetKind: row.TargetKind,
			TargetID:   row.TargetID,
			TargetName: row.TargetName,
			Field:      row.Field,
			Before:     row.BeforeText,
			After:      row.AfterText,
			Added:      int(row.Added),
			Deleted:    int(row.Deleted),
			Redacted:   row.Redacted,
		})
	}
	encoded, err := json.Marshal(changes)
	if err != nil {
		return result
	}
	if result == nil {
		result = map[string]json.RawMessage{}
	}
	result["text_changes"] = encoded
	return result
}

const maxRunTextBytes = 64 << 10

func clipText(text string) string {
	if len(text) <= maxRunTextBytes {
		return text
	}
	end := maxRunTextBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "\n…（已截断）"
}

// parseResultEnvelope reads a marshalled terminal payload back into its fields
// so an extra key can be attached without redefining the whole request type.
func parseResultEnvelope(raw []byte) map[string]json.RawMessage {
	envelope := map[string]json.RawMessage{}
	if len(raw) == 0 {
		return envelope
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return map[string]json.RawMessage{}
	}
	return envelope
}

func marshalResultEnvelope(envelope map[string]json.RawMessage) []byte {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// 审计失败只回滚嵌套事务，不污染 Agent/Skill 主修改。
func (h *Handler) recordRunTextChangeTx(ctx context.Context, tx pgx.Tx, r *http.Request, workspaceID string, target runTextTarget, field, before, after string) {
	if before == after || r.Header.Get("X-Task-ID") == "" {
		return
	}
	nested, err := tx.Begin(ctx)
	if err != nil {
		slog.Warn("begin text audit savepoint", "error", err)
		return
	}
	defer nested.Rollback(ctx)
	if err = h.recordRunTextChange(ctx, db.New(nested), r, workspaceID, target, field, before, after); err != nil {
		if rollbackErr := nested.Rollback(ctx); rollbackErr != nil {
			slog.Warn("text audit rollback failed", "error", rollbackErr)
		}
		return
	}
	if err = nested.Commit(ctx); err != nil {
		slog.Warn("text audit rolled back", "error", err)
	}
}
