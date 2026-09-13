// 职责定位：issue 元数据（每 issue 一个小的 JSONB KV map）的读写端点。agent 用它记录流水线
// 状态（PR 号、pipeline_status、waiting_on 等）。
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 每个 issue 的 metadata 是 agent 记录流水线状态的小型 JSONB KV map（PR 号、pipeline_status、
// waiting_on 等）。V1 表面由三条规则管束——handler 与 DB 两侧都强制：
//
//   - key 匹配 `^[a-zA-Z_][a-zA-Z0-9_.-]{0,63}$`（handler 侧）
//   - 每 issue 至多 50 个 key（handler 侧）
//   - value 只能是原始类型：string / number / bool（handler 侧）
//   - JSONB 列是对象且 ≤ 8KB（DB CHECK；纵深防御）
//
// 所有变更都是单 key 原子操作。UpdateIssue 不碰 metadata——任何整块覆盖都会与并发 agent 写
// 竞态（见 MUL-2017 的设计讨论）。
const (
	maxIssueMetadataKeys = 50
)

var issueMetadataKeyRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]{0,63}$`)

// SetIssueMetadataKeyRequest 携带要写到 URL 里 key 名下的 JSON 值。Value 用 RawMessage 保留
// 数字与字符串的区分直通 PostgreSQL——一旦解进 `any`，JSON 数字都会塌缩成 float64，整数精度
// 就丢了。
type SetIssueMetadataKeyRequest struct {
	Value json.RawMessage `json:"value"`
}

// validateIssueMetadataKey 校验 metadata key 非空且符合命名规则。
func validateIssueMetadataKey(key string) error {
	if key == "" {
		return errors.New("key is required")
	}
	if !issueMetadataKeyRE.MatchString(key) {
		return errors.New("key must match ^[a-zA-Z_][a-zA-Z0-9_.-]{0,63}$")
	}
	return nil
}

// validateIssueMetadataValue 拒绝原始 JSON 标量以外的值。null、数组与对象都不允许——V1 表面
// 是扁平 KV。删 key 用 DELETE，不用 null 值。
func validateIssueMetadataValue(raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("value is required")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("value must be valid JSON: %w", err)
	}
	switch v.(type) {
	case string, bool, float64:
		return nil
	case nil:
		return errors.New("value cannot be null (use DELETE to remove a key)")
	default:
		return errors.New("value must be a primitive: string, number, or bool")
	}
}

// parseIssueMetadata 把 db.Issue.Metadata 的 JSONB 字节解成适合序列化的 Go map。空或不可解析
// 的 blob 降级为空 map——DB CHECK 保证对象形态，这条路径只在某些早于迁移的行上命中。与
// service 层广播渲染（service.IssueToMap）共享，两种描述 issue 的方式对「未设置的包」在线上的
// 样子保持一致。
func parseIssueMetadata(raw []byte) map[string]any {
	return util.JSONObjectOrEmpty(raw)
}

// parseMetadataFilterParam 读取 `metadata` 查询参数（一个 JSON 对象），把它作为传给 ListIssues /
// CountIssues / ListOpenIssues 的 JSONB 过滤 blob。空输入 = 「不过滤」，返回 nil []byte，
// SQL 层把它当「跳过 @> 检查」。
//
// 校验过滤器本身是原始值的扁平对象，镜像写入时施加的约束——查 `{key: {nested}}` 永远匹配
// 不到，因为写入值按构造就是原始类型。
func parseMetadataFilterParam(w http.ResponseWriter, raw string) ([]byte, bool) {
	if raw == "" {
		return nil, true
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		writeError(w, http.StatusBadRequest, "metadata filter must be a JSON object")
		return nil, false
	}
	for k, v := range parsed {
		if err := validateIssueMetadataKey(k); err != nil {
			writeError(w, http.StatusBadRequest, "metadata filter "+err.Error())
			return nil, false
		}
		switch v.(type) {
		case string, bool, float64:
			// ok
		default:
			writeError(w, http.StatusBadRequest, "metadata filter values must be primitives (string, number, bool)")
			return nil, false
		}
	}
	// 重新序列化，向 PG 发送规范 JSON（而不是用户输入的、可能带多余空白的原文）。
	buf, err := json.Marshal(parsed)
	if err != nil {
		writeError(w, http.StatusBadRequest, "metadata filter is invalid")
		return nil, false
	}
	return buf, true
}

// ListIssueMetadata 处理 GET /api/issues/:id/metadata：返回某 issue 的整份元数据 map。
func (h *Handler) ListIssueMetadata(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"metadata": parseIssueMetadata(issue.Metadata)})
}

// SetIssueMetadataKey 处理 PUT /api/issues/:id/metadata/:key：单 key 原子地写入一个元数据值。
// 校验 key 与 value 类型、限制每 issue key 数量；写入、修订事件在事务里完成。超过 8KB 的
// CHECK 违约以 400 提示。
func (h *Handler) SetIssueMetadataKey(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	key := chi.URLParam(r, "key")
	if err := validateIssueMetadataKey(key); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req SetIssueMetadataKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateIssueMetadataValue(req.Value); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// 在 handler 里强制 key 数量上限。DB 只守大小；对「key 太多」给清晰的 4xx，好过等 key
	// 攒到上限时恰好撞上大小上限的 CHECK 违约。
	existing := parseIssueMetadata(issue.Metadata)
	if _, present := existing[key]; !present && len(existing) >= maxIssueMetadataKeys {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("metadata cannot exceed %d keys", maxIssueMetadataKeys))
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start metadata transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	updated, err := qtx.SetIssueMetadataKey(r.Context(), db.SetIssueMetadataKeyParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Key:         key,
		Value:       []byte(req.Value),
	})
	if err != nil {
		if isCheckViolation(err) {
			writeError(w, http.StatusBadRequest, "metadata exceeds the 8KB size limit")
			return
		}
		slog.Warn("SetIssueMetadataKey failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID, "key", key)...)
		writeError(w, http.StatusInternalServerError, "failed to set metadata key")
		return
	}

	workspaceID := uuidToString(updated.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	metadata := parseIssueMetadata(updated.Metadata)
	event, err := service.RecordDurableEventTx(r.Context(), qtx, events.Event{
		Type:           protocol.EventIssueMetadataChanged,
		IdempotencyKey: "issue_metadata:changed:" + util.UUIDToString(updated.ID) + ":" + fmt.Sprint(updated.Revision),
		StreamKey:      "issue:" + util.UUIDToString(updated.ID),
		WorkspaceID:    workspaceID,
		ActorType:      actorType,
		ActorID:        actorID,
		Payload: map[string]any{
			"issue_id":       uuidToString(updated.ID),
			"metadata":       metadata,
			"issue_revision": updated.Revision,
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record metadata event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit metadata change")
		return
	}
	h.publishEvent(event)
	writeJSON(w, http.StatusOK, map[string]any{"metadata": metadata, "issue_revision": updated.Revision})
}

// DeleteIssueMetadataKey 处理 DELETE /api/issues/:id/metadata/:key：删除某 key（幂等；不存在也
// 正常返回）。同样在事务内记录 metadata 变更事件。
func (h *Handler) DeleteIssueMetadataKey(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	key := chi.URLParam(r, "key")
	if err := validateIssueMetadataKey(key); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start metadata transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	updated, err := qtx.DeleteIssueMetadataKey(r.Context(), db.DeleteIssueMetadataKeyParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Key:         key,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue not found")
			return
		}
		slog.Warn("DeleteIssueMetadataKey failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID, "key", key)...)
		writeError(w, http.StatusInternalServerError, "failed to delete metadata key")
		return
	}

	workspaceID := uuidToString(updated.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	metadata := parseIssueMetadata(updated.Metadata)
	event, err := service.RecordDurableEventTx(r.Context(), qtx, events.Event{
		Type:           protocol.EventIssueMetadataChanged,
		IdempotencyKey: "issue_metadata:changed:" + util.UUIDToString(updated.ID) + ":" + fmt.Sprint(updated.Revision),
		StreamKey:      "issue:" + util.UUIDToString(updated.ID),
		WorkspaceID:    workspaceID,
		ActorType:      actorType,
		ActorID:        actorID,
		Payload: map[string]any{
			"issue_id":       uuidToString(updated.ID),
			"metadata":       metadata,
			"issue_revision": updated.Revision,
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record metadata event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit metadata change")
		return
	}
	h.publishEvent(event)
	writeJSON(w, http.StatusOK, map[string]any{"metadata": metadata, "issue_revision": updated.Revision})
}
