// 职责定位：把 issue 拖拽移动（MoveIssue）从「客户端算好的绝对 position」改成「相对邻居」：
// 服务端解析前后锚点、自己推导新 position，再转交 UpdateIssue 走同一条既有校验/实时/触发/
// 父级通知路径。
package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

// issueMoveFields 是 MoveIssue 接受的字段白名单（其余字段直接拒绝，防止夹带）。
var issueMoveFields = map[string]struct{}{
	"status":            {},
	"priority":          {},
	"assignee_type":     {},
	"assignee_id":       {},
	"parent_issue_id":   {},
	"project_id":        {},
	"before_id":         {},
	"after_id":          {},
	"expected_revision": {},
}

// MoveIssue 处理 POST /api/issues/:id/move：接受相对邻居（before_id/after_id）而不是客户端自造
// 的规范 position。它在 issue 所属工作区内解析两个锚点、在服务端推导 position，然后把真正的
// 写入委托给 UpdateIssue——所有既有校验、实时、任务触发与父级通知行为都收在一条路径上。
// 面向已发布客户端的旧 PUT 端点保持不变。
func (h *Handler) MoveIssue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	current, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	for field := range fields {
		if _, ok := issueMoveFields[field]; !ok {
			writeError(w, http.StatusBadRequest, "unsupported move field: "+field)
			return
		}
	}
	if _, ok := fields["before_id"]; !ok {
		writeError(w, http.StatusBadRequest, "before_id is required")
		return
	}
	if _, ok := fields["after_id"]; !ok {
		writeError(w, http.StatusBadRequest, "after_id is required")
		return
	}
	if rawProjectID, touched := fields["project_id"]; touched && !rawJSONNull(rawProjectID) {
		projectID, valid := decodeIssueMoveAnchor(w, rawProjectID, "project_id")
		if !valid {
			return
		}
		var exists bool
		err := h.DB.QueryRow(r.Context(), `
			SELECT EXISTS (
				SELECT 1
				FROM project
				WHERE workspace_id = $1 AND id = $2
			)
		`, current.WorkspaceID, *projectID).Scan(&exists)
		if err != nil {
			writeIssueTableQueryFailure(w, r, "failed to validate move project")
			return
		}
		if !exists {
			writeError(w, http.StatusBadRequest, "project not found in this workspace")
			return
		}
	}

	beforeID, ok := decodeIssueMoveAnchor(w, fields["before_id"], "before_id")
	if !ok {
		return
	}
	afterID, ok := decodeIssueMoveAnchor(w, fields["after_id"], "after_id")
	if !ok {
		return
	}
	if beforeID != nil && *beforeID == current.ID {
		writeError(w, http.StatusBadRequest, "before_id cannot be the moved issue")
		return
	}
	if afterID != nil && *afterID == current.ID {
		writeError(w, http.StatusBadRequest, "after_id cannot be the moved issue")
		return
	}
	if beforeID != nil && afterID != nil && *beforeID == *afterID {
		writeError(w, http.StatusBadRequest, "move anchors must be distinct")
		return
	}

	// V1 刻意把规范写留在 UpdateIssue 上，让已发布客户端与新 move 端点共享校验、实时与任务
	// 副作用。这意味着「锚点解析」与「写入」不是同一事务：可检测时过期/乱序锚点用 409 失败
	// 关闭；float 间距耗尽也返回 409 而不是悄悄给邻居重编号。未来的 rebalance 必须把所有
	// position 写入者（含 legacy PUT 路径）挪到同一个事务性排序边界后。
	beforePosition, ok := h.issueMoveAnchorPosition(w, r, current.WorkspaceID, beforeID)
	if !ok {
		return
	}
	afterPosition, ok := h.issueMoveAnchorPosition(w, r, current.WorkspaceID, afterID)
	if !ok {
		return
	}
	position, err := issueMovePosition(current.Position, beforePosition, afterPosition)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	delete(fields, "before_id")
	delete(fields, "after_id")
	fields["position"], _ = json.Marshal(position)
	updateBody, err := json.Marshal(fields)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode move")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(updateBody))
	r.ContentLength = int64(len(updateBody))
	h.UpdateIssue(w, r)
}

// decodeIssueMoveAnchor 把 before_id/after_id 的原始 JSON 解成 *pgtype.UUID（显式 null 为 nil）。
func decodeIssueMoveAnchor(w http.ResponseWriter, raw json.RawMessage, field string) (*pgtype.UUID, bool) {
	if rawJSONNull(raw) {
		return nil, true
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		writeError(w, http.StatusBadRequest, field+" must be a UUID or null")
		return nil, false
	}
	id, err := util.ParseUUID(value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid "+field)
		return nil, false
	}
	return &id, true
}

// rawJSONNull 判断一段原始 JSON 是不是字面量 null。
func rawJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// issueMoveAnchorPosition 读某锚点 issue 的 position（工作区范围内）；锚点为 nil（表示移到
// 列表头/尾）时直接返回 nil、无错误。
func (h *Handler) issueMoveAnchorPosition(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID pgtype.UUID,
	id *pgtype.UUID,
) (*float64, bool) {
	if id == nil {
		return nil, true
	}
	var position float64
	err := h.DB.QueryRow(r.Context(), `
		SELECT position
		FROM issue
		WHERE workspace_id = $1 AND id = $2
	`, workspaceID, *id).Scan(&position)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "move anchor not found in this workspace")
		} else {
			writeIssueTableQueryFailure(w, r, "failed to resolve move anchor")
		}
		return nil, false
	}
	return &position, true
}

// issueMovePosition 在前后锚点之间取中点作为新 position（float 排序用）；只给一边时 ±1。
// 间距耗尽、上下溢或出现 NaN/Inf 都报错，由调用方回 409 让用户刷新重试。
func issueMovePosition(current float64, before, after *float64) (float64, error) {
	switch {
	case before != nil && after != nil:
		if !(*before < *after) {
			return 0, errors.New("move anchors are stale or out of order")
		}
		position := *before + (*after-*before)/2
		if !(position > *before && position < *after) ||
			math.IsInf(position, 0) || math.IsNaN(position) {
			return 0, errors.New("move anchors are too close; refresh and retry")
		}
		return position, nil
	case before != nil:
		position := *before + 1
		if math.IsInf(position, 0) || math.IsNaN(position) {
			return 0, errors.New("move position is out of range")
		}
		return position, nil
	case after != nil:
		position := *after - 1
		if math.IsInf(position, 0) || math.IsNaN(position) {
			return 0, errors.New("move position is out of range")
		}
		return position, nil
	default:
		return current, nil
	}
}
