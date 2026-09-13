// 本文件是 webhook 投递记录（webhook_delivery）面向「已认证 API」的只读视图：
// 列出某 autopilot 最近的投递、看单条完整投递（含原始 body 与头子集）、以及重放
// （replay）一条历史投递。
//
// 投递行的实际派发不在这里——投递记录由 autopilot 收到 webhook 后排队产生，真正的
// 派发与重试由持久化 worker（WebhookDeliveryWorker / webhook_delivery_worker.go）
// 拥有。本文件只负责新增一行「排队的重放投递」和查询。
//
// 数据安全约定：经此表面永不回显签名密钥/token；详情接口才会带上 raw body 与
// 请求头子集（调试用）。重放受幂等键约束（ReplayIdempotencyKey），重复重放请求会
// 返回既有的那次投递。
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// ── 响应类型 ────────────────────────────────────────────────────────────────

// WebhookDeliveryResponse 是 webhook_delivery 在已认证 API 上的视图。列表端点返回
// 这些行时不填充 RawBody / SelectedHeaders；详情端点两者都带上供调试。签名密钥或
// token 绝不经过这个表面回显。
type WebhookDeliveryResponse struct {
	ID                     string  `json:"id"`
	WorkspaceID            string  `json:"workspace_id"`
	AutopilotID            string  `json:"autopilot_id"`
	TriggerID              string  `json:"trigger_id"`
	Provider               string  `json:"provider"`
	Event                  string  `json:"event"`
	DedupeKey              *string `json:"dedupe_key"`
	DedupeSource           *string `json:"dedupe_source"`
	SignatureStatus        string  `json:"signature_status"`
	Status                 string  `json:"status"`
	AttemptCount           int32   `json:"attempt_count"`
	DispatchAttempts       int32   `json:"dispatch_attempts"`
	AvailableAt            string  `json:"available_at"`
	ContentType            *string `json:"content_type"`
	ResponseStatus         *int32  `json:"response_status"`
	AutopilotRunID         *string `json:"autopilot_run_id"`
	ReplayedFromDeliveryID *string `json:"replayed_from_delivery_id"`
	Error                  *string `json:"error"`
	ReasonCode             *string `json:"reason_code"`
	ReplayIdempotencyKey   *string `json:"replay_idempotency_key"`
	ReceivedAt             string  `json:"received_at"`
	LastAttemptAt          string  `json:"last_attempt_at"`
	CreatedAt              string  `json:"created_at"`

	// 仅详情端点的字段。列表响应把它们留空，一页 N 条投递就不会序列化出约
	// N × 256 KiB 的原始 body。需要详情的客户端走 GET /deliveries/{deliveryId}。
	SelectedHeaders json.RawMessage `json:"selected_headers,omitempty"`
	RawBody         *string         `json:"raw_body,omitempty"`
	ResponseBody    *string         `json:"response_body,omitempty"`
}

// slimDeliveryToResponse 把列表用的投影行（不含 raw_body / selected_headers /
// response_body）映射成对外响应形状。
func slimDeliveryToResponse(d db.ListWebhookDeliveriesByAutopilotRow) WebhookDeliveryResponse {
	resp := WebhookDeliveryResponse{
		ID:                   uuidToString(d.ID),
		WorkspaceID:          uuidToString(d.WorkspaceID),
		AutopilotID:          uuidToString(d.AutopilotID),
		TriggerID:            uuidToString(d.TriggerID),
		Provider:             d.Provider,
		Event:                d.Event,
		DedupeKey:            textToPtr(d.DedupeKey),
		DedupeSource:         textToPtr(d.DedupeSource),
		SignatureStatus:      d.SignatureStatus,
		Status:               d.Status,
		AttemptCount:         d.AttemptCount,
		DispatchAttempts:     d.DispatchAttempts,
		AvailableAt:          timestampToString(d.AvailableAt),
		ContentType:          textToPtr(d.ContentType),
		ReceivedAt:           timestampToString(d.ReceivedAt),
		LastAttemptAt:        timestampToString(d.LastAttemptAt),
		CreatedAt:            timestampToString(d.CreatedAt),
		ReasonCode:           textToPtr(d.ReasonCode),
		ReplayIdempotencyKey: textToPtr(d.ReplayIdempotencyKey),
	}
	if d.ResponseStatus.Valid {
		v := d.ResponseStatus.Int32
		resp.ResponseStatus = &v
	}
	if d.AutopilotRunID.Valid {
		v := uuidToString(d.AutopilotRunID)
		resp.AutopilotRunID = &v
	}
	if d.ReplayedFromDeliveryID.Valid {
		v := uuidToString(d.ReplayedFromDeliveryID)
		resp.ReplayedFromDeliveryID = &v
	}
	if d.Error.Valid {
		v := d.Error.String
		resp.Error = &v
	}
	return resp
}

// deliveryToResponse 把完整投递行映射成对外响应；detail=true 时才带上大字段
// （SelectedHeaders / RawBody / ResponseBody）。
func deliveryToResponse(d db.WebhookDelivery, detail bool) WebhookDeliveryResponse {
	resp := WebhookDeliveryResponse{
		ID:                   uuidToString(d.ID),
		WorkspaceID:          uuidToString(d.WorkspaceID),
		AutopilotID:          uuidToString(d.AutopilotID),
		TriggerID:            uuidToString(d.TriggerID),
		Provider:             d.Provider,
		Event:                d.Event,
		DedupeKey:            textToPtr(d.DedupeKey),
		DedupeSource:         textToPtr(d.DedupeSource),
		SignatureStatus:      d.SignatureStatus,
		Status:               d.Status,
		AttemptCount:         d.AttemptCount,
		DispatchAttempts:     d.DispatchAttempts,
		AvailableAt:          timestampToString(d.AvailableAt),
		ContentType:          textToPtr(d.ContentType),
		ReceivedAt:           timestampToString(d.ReceivedAt),
		LastAttemptAt:        timestampToString(d.LastAttemptAt),
		CreatedAt:            timestampToString(d.CreatedAt),
		ReasonCode:           textToPtr(d.ReasonCode),
		ReplayIdempotencyKey: textToPtr(d.ReplayIdempotencyKey),
	}
	if d.ResponseStatus.Valid {
		v := d.ResponseStatus.Int32
		resp.ResponseStatus = &v
	}
	if d.AutopilotRunID.Valid {
		v := uuidToString(d.AutopilotRunID)
		resp.AutopilotRunID = &v
	}
	if d.ReplayedFromDeliveryID.Valid {
		v := uuidToString(d.ReplayedFromDeliveryID)
		resp.ReplayedFromDeliveryID = &v
	}
	if d.Error.Valid {
		v := d.Error.String
		resp.Error = &v
	}
	if detail {
		if len(d.SelectedHeaders) > 0 {
			resp.SelectedHeaders = json.RawMessage(d.SelectedHeaders)
		}
		if len(d.RawBody) > 0 {
			s := string(d.RawBody)
			resp.RawBody = &s
		}
		if d.ResponseBody.Valid {
			v := d.ResponseBody.String
			resp.ResponseBody = &v
		}
	}
	return resp
}

// ── 端点 ────────────────────────────────────────────────────────────────────

// ListAutopilotDeliveries 返回某 autopilot 最近的投递（GET）。瘦投影——省略
// selected_headers / raw_body / response_body，保证列表响应足够小。要完整载荷请用
// GetAutopilotDelivery。
func (h *Handler) ListAutopilotDeliveries(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)

	autopilot, ok := h.loadAutopilotInWorkspace(w, r, autopilotID, workspaceID)
	if !ok {
		return
	}

	limit := int32(20)
	offset := int32(0)
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = int32(v)
		}
	}
	if limit > 100 {
		limit = 100
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = int32(v)
		}
	}

	rows, err := h.Queries.ListWebhookDeliveriesByAutopilot(r.Context(), db.ListWebhookDeliveriesByAutopilotParams{
		AutopilotID: autopilot.ID,
		WorkspaceID: autopilot.WorkspaceID,
		Limit:       limit,
		Offset:      offset,
	})
	if err != nil {
		slog.Error("list deliveries failed", "error", err, "autopilot_id", autopilotID)
		writeError(w, http.StatusInternalServerError, "failed to list deliveries")
		return
	}

	resp := make([]WebhookDeliveryResponse, len(rows))
	for i, row := range rows {
		resp[i] = slimDeliveryToResponse(row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": resp, "total": len(resp)})
}

// GetAutopilotDelivery 返回单条完整投递，包括原始 body 与请求头子集。经 autopilot
// 查询先限定在 workspace 内；随后再核对投递确实属于该 autopilot，防止从别的
// workspace 猜 delivery id 把数据带出来。
func (h *Handler) GetAutopilotDelivery(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	deliveryID := chi.URLParam(r, "deliveryId")
	workspaceID := h.resolveWorkspaceID(r)

	autopilot, ok := h.loadAutopilotInWorkspace(w, r, autopilotID, workspaceID)
	if !ok {
		return
	}
	delivery, ok := h.loadDeliveryForAutopilot(w, r, autopilot, deliveryID)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, deliveryToResponse(delivery, true))
}

// ReplayAutopilotDelivery 从一条历史投递新建一条「排队中」的投递行；派发仍归持久化
// worker 所有。新行带着 `replayed_from_delivery_id`，方便运维关联。签名校验本就没过
// 的投递拒绝重放——把攻击载荷对 autopilot 再跑一遍，等于绕过当初的拒绝。
//
// 重放通过插入 NULL dedupe_key 绕过提供方的去重：沿用原 key 会把重放静默合并到旧的
// 那次投递上（部分唯一索引会触发）。这是有意行为——重放就是明确要求「再跑一次」。
// API 层的重试则由 (原投递, Idempotency-Key) 独立去重。
func (h *Handler) ReplayAutopilotDelivery(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	deliveryID := chi.URLParam(r, "deliveryId")
	workspaceID := h.resolveWorkspaceID(r)

	autopilot, ok := h.loadAutopilotInWorkspace(w, r, autopilotID, workspaceID)
	if !ok {
		return
	}
	if !h.requireAutopilotWrite(w, r, autopilot, workspaceID) {
		return
	}
	original, ok := h.loadDeliveryForAutopilot(w, r, autopilot, deliveryID)
	if !ok {
		return
	}
	if original.Status == deliveryStatusRejected || original.SignatureStatus == sigStatusInvalid {
		writeError(w, http.StatusBadRequest, "cannot replay a delivery that failed signature verification")
		return
	}
	if len(original.RawBody) == 0 {
		writeError(w, http.StatusBadRequest, "original delivery has no raw body to replay")
		return
	}

	if autopilot.Status != "active" {
		writeError(w, http.StatusBadRequest, "autopilot is not active")
		return
	}

	trigRow, err := h.Queries.GetAutopilotTrigger(r.Context(), original.TriggerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "trigger not found")
		return
	}
	if !trigRow.Enabled {
		writeError(w, http.StatusBadRequest, "trigger is disabled")
		return
	}

	// 用原始 body + 原始头子集重建信封，供事件推断使用。SelectedHeaders 很小且是
	// JSON 形状，把它解回 header map 以复用同一条 normalize 路径。
	headers := headersFromSelected(original.SelectedHeaders)
	envelope, err := normalizeWebhookPayload(original.RawBody, headers)
	if err != nil {
		writeError(w, http.StatusBadRequest, "stored body no longer parses: "+err.Error())
		return
	}
	_ = envelope // 持久化 worker 会再次规范化存储的载荷

	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) > 255 {
		writeError(w, http.StatusBadRequest, "Idempotency-Key is too long")
		return
	}
	if idempotencyKey == "" {
		idempotencyKey = service.NewRequestIdempotencyKey()
	}
	keyText := pgtype.Text{String: idempotencyKey, Valid: true}
	if existing, lookupErr := h.Queries.GetWebhookReplayByIdempotencyKey(r.Context(), db.GetWebhookReplayByIdempotencyKeyParams{
		ReplayedFromDeliveryID: original.ID, ReplayIdempotencyKey: keyText,
	}); lookupErr == nil {
		writeJSON(w, http.StatusAccepted, deliveryToResponse(existing, true))
		return
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to inspect replay request")
		return
	}

	contentType := ""
	if original.ContentType.Valid {
		contentType = original.ContentType.String
	}
	replay, err := h.Queries.CreateWebhookDelivery(r.Context(), db.CreateWebhookDeliveryParams{
		ID:                     dbid.NewV7(),
		WorkspaceID:            autopilot.WorkspaceID,
		AutopilotID:            autopilot.ID,
		TriggerID:              original.TriggerID,
		Provider:               original.Provider,
		Event:                  envelope.Event,
		SignatureStatus:        sigStatusNotRequired,
		Status:                 deliveryStatusQueued,
		SelectedHeaders:        original.SelectedHeaders,
		ContentType:            pgtype.Text{String: contentType, Valid: contentType != ""},
		RawBody:                original.RawBody,
		ReplayedFromDeliveryID: original.ID,
		ReplayIdempotencyKey:   keyText,
	})
	if err != nil {
		if isUniqueViolation(err) {
			existing, lookupErr := h.Queries.GetWebhookReplayByIdempotencyKey(r.Context(), db.GetWebhookReplayByIdempotencyKeyParams{
				ReplayedFromDeliveryID: original.ID, ReplayIdempotencyKey: keyText,
			})
			if lookupErr == nil {
				writeJSON(w, http.StatusAccepted, deliveryToResponse(existing, true))
				return
			}
		}
		slog.Error("replay: insert delivery failed",
			"error", err,
			"original_delivery_id", uuidToString(original.ID),
		)
		writeError(w, http.StatusInternalServerError, "failed to create replay delivery")
		return
	}

	if h.WebhookDeliveryWorker != nil {
		h.WebhookDeliveryWorker.Notify()
	}
	writeJSON(w, http.StatusAccepted, deliveryToResponse(replay, true))
}

// loadDeliveryForAutopilot 当投递行存在、且同时满足「同 workspace 且属于给定
// autopilot」时返回该行。跨 autopilot / 跨 workspace 的 id 一律按 404 处理——
// 针对猜 id 的纵深防御。
func (h *Handler) loadDeliveryForAutopilot(w http.ResponseWriter, r *http.Request, autopilot db.Autopilot, deliveryID string) (db.WebhookDelivery, bool) {
	deliveryUUID, ok := parseUUIDOrBadRequest(w, deliveryID, "delivery id")
	if !ok {
		return db.WebhookDelivery{}, false
	}
	delivery, err := h.Queries.GetWebhookDeliveryInWorkspace(r.Context(), db.GetWebhookDeliveryInWorkspaceParams{
		ID:          deliveryUUID,
		WorkspaceID: autopilot.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "delivery not found")
			return db.WebhookDelivery{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load delivery")
		return db.WebhookDelivery{}, false
	}
	if uuidToString(delivery.AutopilotID) != uuidToString(autopilot.ID) {
		writeError(w, http.StatusNotFound, "delivery not found")
		return db.WebhookDelivery{}, false
	}
	return delivery, true
}

// headersFromSelected 把入库时保存的「头子集」小 blob 解回 http.Header。只在重放
// 路径用到——入口处没捕获的字段在这里自然缺失，与原始请求本来就没发这些头的效果
// 一致。
func headersFromSelected(raw []byte) http.Header {
	out := http.Header{}
	if len(raw) == 0 {
		return out
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return out
	}
	canonical := map[string]string{
		"user-agent":        "User-Agent",
		"x-github-event":    "X-GitHub-Event",
		"x-github-delivery": "X-GitHub-Delivery",
		"x-gitlab-event":    "X-Gitlab-Event",
		"x-event-type":      "X-Event-Type",
		"idempotency-key":   "Idempotency-Key",
	}
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			continue
		}
		header := canonical[k]
		if header == "" {
			continue
		}
		out.Set(header, s)
	}
	return out
}
