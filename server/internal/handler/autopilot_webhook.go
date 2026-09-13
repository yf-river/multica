// 本文件实现 autopilot 的 webhook 公开入口（HandleAutopilotWebhook）与其支撑
// 工具：body 规范化（任意 provider 的 JSON → 统一 WebhookEnvelope）、dedupe 键
// 提取、HMAC 签名校验、事件过滤器，以及 webhook_delivery 行的持久化/终态落库。
// 该入口跑在鉴权路由组之外——URL 路径里的 bearer token 本身就是凭据。流程为
// 「先持久化、同步准入、异步派发」：写入 delivery 行后由数据库租约的 worker
// 重新检查状态/过滤范围、幂等派发并推进 last_fired_at。
package handler

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// maxWebhookBodyBytes 是 webhook 入口的请求体上限。256 KiB 对常规 provider
// webhook 绰绰有余（最大号的 GitHub PR 负载也远低于此），又小到攻击者无法靠发
// 数 MB 任意 JSON 卡死 agent 的上下文窗口。
const maxWebhookBodyBytes = 256 * 1024

// webhookTokenPrefix 让泄漏的 token 在日志/审计轨迹里可辨识，又不暴露熵字节本身。
// 32 随机字节编码成 URL-safe base64（无填充）是 43 字符，因此完整 token 为
// "awt_" + 43 = 47 字符。URL-safe base64 让 token 无需转义即可放进 URL。
const webhookTokenPrefix = "awt_"

// generateWebhookToken 返回一把密码学随机的 bearer token，用作公开 webhook URL
// 的密钥。格式："awt_" + URL-safe base64(32 字节, 无填充)。刻意不用 UUID——它熵
// 更低（122 bit vs 256），视觉上又跟内部 id 重叠，早期原型里 token-vs-id 容易
// 混淆。
func generateWebhookToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return webhookTokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// signature_status 取值镜像 webhook_delivery 上的 CHECK 约束。
const (
	sigStatusNotRequired = "not_required"
	sigStatusValid       = "valid"
	sigStatusInvalid     = "invalid"
	sigStatusMissing     = "missing"
)

// delivery status 取值镜像 webhook_delivery 上的 CHECK 约束。
//
// "Duplicate" 是 *响应* 状态而非 delivery 状态——重复投递不建自己的行，它们在
// 既有 dedupe 目标上累加 attempt_count。同理 "skipped" 是 autopilot 服务跳过该
// 运行（如 runtime 离线）时上报的 *响应* 状态；delivery 行本身记 `dispatched`
// 并经 autopilot_run_id 链接被跳过的运行，因为从入口视角我们确实把负载交给了
// autopilot 机制。
const (
	deliveryStatusQueued     = "queued"
	deliveryStatusDispatched = "dispatched"
	deliveryStatusRejected   = "rejected"
	deliveryStatusIgnored    = "ignored"
	deliveryStatusFailed     = "failed"
)

// ── Payload 规范化 ─────────────────────────────────────────────────────────

// WebhookEnvelope 是存在 autopilot_run.trigger_payload、并展示给 agent 的规范
// 形状。handler 把任意 JSON body 规范化成该形状，让下游消费者（run_only 的
// daemon 提示词、create_issue 的描述附录）无论哪个 provider 发的 webhook 都
// 依赖同一个稳定 schema。
type WebhookEnvelope struct {
	Event        string          `json:"event"`
	EventPayload json.RawMessage `json:"eventPayload"`
	Request      WebhookRequest  `json:"request"`
}

// WebhookRequest 是信封里关于本次 HTTP 请求本身的元数据。
type WebhookRequest struct {
	ReceivedAt  string `json:"receivedAt"`
	ContentType string `json:"contentType,omitempty"`
}

// normalizeWebhookPayload 解析入站 webhook body 并返回 WebhookEnvelope。规则：
//
//  1. Body 必须是合法 JSON 对象或数组。标量/非法 JSON 返回错误，让 handler 回
//     400。
//  2. 若 body 是含字符串 `event` 且带 `eventPayload` 的对象，原样保留。
//  3. 否则 `event` 从头/body 字段推断，整个原始 body 成为 `eventPayload`。
//  4. 默认 event 是 `webhook.received`。
//
// 推断顺序：
//
//	X-GitHub-Event（存在时与 body.action 合并）、X-Gitlab-Event、X-Event-Type、
//	body.event、body.type、body.action。
func normalizeWebhookPayload(body []byte, headers http.Header) (WebhookEnvelope, error) {
	body = stripBOM(body)
	if len(body) == 0 {
		return WebhookEnvelope{}, errors.New("empty body")
	}

	// 先校验 JSON 形状（对象或数组）。尽早拒绝标量——`"hello"` 技术上合法，但作为
	// webhook 负载没有可用解释，会以裸字符串落进 agent 提示词。
	var asAny any
	if err := json.Unmarshal(body, &asAny); err != nil {
		return WebhookEnvelope{}, fmt.Errorf("invalid json: %w", err)
	}
	switch asAny.(type) {
	case map[string]any, []any:
		// 合法形状
	default:
		return WebhookEnvelope{}, errors.New("body must be a JSON object or array")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	contentType := headers.Get("Content-Type")
	if i := strings.Index(contentType, ";"); i >= 0 {
		contentType = strings.TrimSpace(contentType[:i])
	}

	env := WebhookEnvelope{
		Request: WebhookRequest{
			ReceivedAt:  now,
			ContentType: contentType,
		},
	}

	// 1. 调用方自带的信封。
	if obj, ok := asAny.(map[string]any); ok {
		if eventStr, ok := obj["event"].(string); ok && eventStr != "" {
			if rawPayload, ok := obj["eventPayload"]; ok {
				inner, err := json.Marshal(rawPayload)
				if err == nil {
					env.Event = eventStr
					env.EventPayload = inner
					return env, nil
				}
			}
			// `event` 在但无 eventPayload：仍保留 event 串，退回用整个 body 当负载。
			env.Event = eventStr
			env.EventPayload = json.RawMessage(body)
			return env, nil
		}
	}

	// 2. 推断 event。
	event := inferEvent(headers, asAny)
	env.Event = event
	env.EventPayload = json.RawMessage(body)
	return env, nil
}

// inferEvent 从头与 body 尽力推断事件标识。
func inferEvent(headers http.Header, body any) string {
	if gh := headers.Get("X-GitHub-Event"); gh != "" {
		if obj, ok := body.(map[string]any); ok {
			if action, ok := obj["action"].(string); ok && action != "" {
				return "github." + gh + "." + action
			}
		}
		return "github." + gh
	}
	if gl := headers.Get("X-Gitlab-Event"); gl != "" {
		return "gitlab." + gl
	}
	if xe := headers.Get("X-Event-Type"); xe != "" {
		return xe
	}
	if obj, ok := body.(map[string]any); ok {
		if e, ok := obj["event"].(string); ok && e != "" {
			return e
		}
		if t, ok := obj["type"].(string); ok && t != "" {
			return t
		}
		if a, ok := obj["action"].(string); ok && a != "" {
			return a
		}
	}
	return "webhook.received"
}

// stripBOM 去掉开头的 UTF-8 byte-order-mark——部分客户端（尤其 PowerShell 脚本）
// 会给 JSON body 前置 BOM。
func stripBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// ── Dedupe + 签名辅助 ───────────────────────────────────────────────────────

// extractDedupeKey 从请求头返回 provider 专属的幂等标识，外加标明来源头的短标签。
// 没有可识别的头时返回 ("", "")。
//
//	github  -> X-GitHub-Delivery
//	generic -> Idempotency-Key
//
// 其它 provider 回退到通用头，让 Postman / curl 的手动重放与触发器配置无关地
// 保持一致行为。
func extractDedupeKey(provider string, headers http.Header) (string, string) {
	if v := strings.TrimSpace(headers.Get("X-GitHub-Delivery")); v != "" && provider == "github" {
		return v, "x-github-delivery"
	}
	if v := strings.TrimSpace(headers.Get("Idempotency-Key")); v != "" {
		return v, "idempotency-key"
	}
	if v := strings.TrimSpace(headers.Get("X-GitHub-Delivery")); v != "" {
		return v, "x-github-delivery"
	}
	return "", ""
}

// verifyWebhookSignatureForProvider 返回 sigStatus* 之一，描述对已配置触发器做
// HMAC 校验的结果。
//
// 未配置签名密钥时结果为 `not_required`——该触发器选择了纯 bearer-token 鉴权。
// 配置了密钥时请求必须携带期望头；否则结果为 `missing`（调用方仍会记一条
// rejected 投递）。
//
//	github  -> X-Hub-Signature-256: sha256=<hex>
//	generic -> X-Hub-Signature-256（同形状；让 curl/Postman 也能选用）
func verifyWebhookSignatureForProvider(provider, secret string, headers http.Header, rawBody []byte) string {
	if secret == "" {
		return sigStatusNotRequired
	}
	sig := headers.Get("X-Hub-Signature-256")
	if sig == "" {
		return sigStatusMissing
	}
	if !verifyHubSignature(secret, sig, rawBody) {
		return sigStatusInvalid
	}
	_ = provider
	return sigStatusValid
}

// verifyHubSignature 实现 GitHub 兼容的 HMAC-SHA256 方案：
// `X-Hub-Signature-256: sha256=<hex(hmac(body, secret))>`。hmac.Equal 的比较是
// 常数时间的，部分前缀攻击无法靠计时泄漏。
func verifyHubSignature(secret, header string, body []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	want, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

// selectedHeadersJSON 返回一小撮我们持久化到 delivery 行上、便于排查的请求头。
// 签名头只记 present/absent——绝不记值，投递 dump 不会泄漏敏感 body 的 HMAC。
func selectedHeadersJSON(headers http.Header) []byte {
	out := map[string]any{}
	add := func(name string) {
		if v := headers.Get(name); v != "" {
			out[strings.ToLower(name)] = v
		}
	}
	add("User-Agent")
	add("X-GitHub-Event")
	add("X-GitHub-Delivery")
	add("X-Gitlab-Event")
	add("X-Event-Type")
	add("Idempotency-Key")
	if v := headers.Get("X-Hub-Signature-256"); v != "" {
		out["x-hub-signature-256-present"] = true
	}
	b, err := json.Marshal(out)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// ── 公开入口 ────────────────────────────────────────────────────────────────

// HandleAutopilotWebhook 是 webhook 触发 autopilot 的公开入口。它跑在鉴权路由组
// 之外：URL 路径里的 bearer token 本身就是凭据。
//
// 流程（先持久化、同步准入 + 持久异步派发）：
//
//  1. 高绝对值的按 IP 上限，加一个对既有坏凭据债的「非消费」检查（都在 DB I/O
//     之前）。
//  2. 查 token。ErrNoRows → 404；其它 DB 错误 → 500。
//  3. 读原始 body（封顶）。超限 → 413。
//  4. 规范化 JSON 信封。非法 → 400（不持久化——无法解析的 body 里没有可信的
//     dedupe 标识）。
//  5. 按 provider 从头提取 dedupe 键。
//  6. 校验签名（未配置密钥时为 `not_required`）。
//  7. INSERT webhook_delivery 行（status=queued）。dedupe 冲突（23505，撞
//     `(trigger_id, dedupe_key)`）按重复处理：在既有行上累加 attempt_count，并
//     以 200 返回它的 delivery_id + autopilot_run_id。
//  8. 签名非法/缺失：计坏凭据债，UPDATE delivery → rejected，返回 401。
//  9. 触发器禁用 / autopilot 暂停或归档：UPDATE delivery → ignored，返回 200。
//  10. 创建或复用该投递的幂等运行，持久化相应的 200 accepted/skipped 回执，并
//     唤醒数据库租约的 worker。worker 重新检查状态/过滤范围、幂等派发、推进
//     last_fired_at。
//
// 响应形状：
//   - 200 {"status":"accepted",  "delivery_id", "run_id", "autopilot_id", "trigger_id"}
//   - 200 {"status":"skipped",   "delivery_id", "run_id", "reason"}
//   - 200 {"status":"ignored",   "delivery_id", "reason"}
//   - 200 {"status":"duplicate", "delivery_id", "run_id?"}
//   - 400 {"error":"..."}                                          — 非法 JSON / 标量 / 空
//   - 401 {"status":"rejected",  "delivery_id", "reason":"..."}    — 签名失败
//   - 404 {"error":"webhook not found"}                            — 未知 token
//   - 413 {"error":"payload too large"}                            — body 超限
//   - 429 {"error":"rate limit exceeded"}                          — IP 安全/债门
//   - 500 {"error":"..."}                                          — 内部失败
func (h *Handler) HandleAutopilotWebhook(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		writeError(w, http.StatusNotFound, "webhook not found")
		return
	}

	// 1. 始终保留一个高绝对值上限。较低的共享 IP 限流器只跟踪已被归类为坏凭据的
	//    请求，因此 NAT 后面一窝合法 GitHub hook 不会耗尽它的预算。
	ip := h.clientIPForRateLimit(r)
	if ip != "" && h.WebhookAbsoluteIPRateLimiter != nil && !h.WebhookAbsoluteIPRateLimiter.Allow(r.Context(), ip) {
		writeWebhookRateLimit(w, r, h.WebhookAbsoluteIPRateLimiter, ip, "absolute_ip", h.Metrics)
		return
	}
	if ip != "" && h.WebhookIPRateLimiter != nil && !slidingWindowLimiterCheck(r.Context(), h.WebhookIPRateLimiter, ip) {
		writeWebhookRateLimit(w, r, h.WebhookIPRateLimiter, ip, "bad_credential_ip", h.Metrics)
		return
	}

	// 2. 查 token。区分「无行」与「DB 错误」：把两者都折成 404 的话，一次瞬时 DB
	//    抖动会悄悄丢掉真实投递（GitHub 这类 provider 对 404 不重试）。对无行我们
	//    仍返回通用文案，不泄漏哪些 token 存在过。
	trigRow, err := h.Queries.GetWebhookTriggerByToken(r.Context(), pgtype.Text{String: token, Valid: true})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if ip != "" && h.WebhookIPRateLimiter != nil {
				h.WebhookIPRateLimiter.Allow(r.Context(), ip)
			}
			writeError(w, http.StatusNotFound, "webhook not found")
			return
		}
		slog.Error("webhook: token lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	middleware.SetWebhookTriggerID(r, uuidToString(trigRow.ID))

	// 3. body 大小上限 + JSON 校验。http.MaxBytesReader 在超过上限后中途截断读取，
	//    超大负载在被完整缓冲前就被拒绝。
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
			return
		}
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	// 4. 在持久化投递之前交叉核对 autopilot/workspace 一致性——webhook_delivery.
	//    workspace_id 是 NOT NULL，过期 FK 行否则会在我们付过 body 读取的代价后才
	//    让 INSERT 失败。与 token 查询相同的 ErrNoRows-vs-DB-error 切分。
	autopilot, err := h.Queries.GetAutopilot(r.Context(), trigRow.AutopilotID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "webhook not found")
			return
		}
		slog.Error("webhook: autopilot lookup failed",
			"error", err,
			"trigger_id", uuidToString(trigRow.ID),
		)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if uuidToString(autopilot.WorkspaceID) != uuidToString(trigRow.AutopilotWorkspaceID) {
		slog.Warn("webhook: trigger workspace mismatch",
			"trigger_id", uuidToString(trigRow.ID),
			"autopilot_id", uuidToString(autopilot.ID),
		)
		writeError(w, http.StatusNotFound, "webhook not found")
		return
	}

	// 5. 规范化 body。非法 JSON → 400 且不持久化：body 里没有可用的 dedupe
	//    标识，重放一个无法解析的负载没有意义。
	envelope, err := normalizeWebhookPayload(body, r.Header)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	envelopeBytes, err := json.Marshal(envelope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode envelope")
		return
	}
	// 6. provider + dedupe + 签名。
	provider := trigRow.Provider
	if provider == "" {
		provider = "generic"
	}
	dedupeKey, dedupeSource := extractDedupeKey(provider, r.Header)
	sigStatus := verifyWebhookSignatureForProvider(provider, trigRow.SigningSecret.String, r.Header, body)

	// 7. 持久化（INSERT delivery）。dedupe 冲突 → 累加既有行。
	delivery, dup, err := h.persistInboundDelivery(r, persistDeliveryInput{
		WorkspaceID:     autopilot.WorkspaceID,
		AutopilotID:     autopilot.ID,
		TriggerID:       trigRow.ID,
		Provider:        provider,
		Event:           envelope.Event,
		DedupeKey:       dedupeKey,
		DedupeSource:    dedupeSource,
		SignatureStatus: sigStatus,
		ContentType:     envelope.Request.ContentType,
		RawBody:         body,
		SelectedHeaders: selectedHeadersJSON(r.Header),
	})
	if err != nil {
		slog.Error("webhook: persist delivery failed",
			"error", err,
			"trigger_id", uuidToString(trigRow.ID),
		)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if dup {
		// 之前的某条投递已覆盖这个 dedupe 键。以 200 返回原 delivery_id + 已准入
		// 的 run_id 供调用方关联。delivery 行在 worker 完成前不会回链 run，因此
		// 在此之前重试必须回退到该运行的持久 delivery 锚点。
		resp := map[string]any{
			"status":      "duplicate",
			"delivery_id": uuidToString(delivery.ID),
		}
		runID := delivery.AutopilotRunID
		if !runID.Valid {
			run, runErr := h.Queries.GetAutopilotRunByWebhookDelivery(r.Context(), delivery.ID)
			switch {
			case runErr == nil:
				runID = run.ID
			case !errors.Is(runErr, pgx.ErrNoRows):
				slog.Error("webhook: resolve duplicate run failed",
					"delivery_id", uuidToString(delivery.ID),
					"error", runErr,
				)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
		}
		if runID.Valid {
			resp["run_id"] = uuidToString(runID)
		}
		if delivery.Status == deliveryStatusQueued && h.WebhookDeliveryWorker != nil {
			h.WebhookDeliveryWorker.Notify()
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// 8. 签名失败 → rejected 投递 + 401。不派发、不重放。密钥配错时 provider 会
	//    找 4xx 反馈。
	if sigStatus == sigStatusInvalid || sigStatus == sigStatusMissing {
		reason := "invalid_signature"
		if sigStatus == sigStatusMissing {
			reason = "missing_signature"
		}
		respBody := map[string]any{
			"status":      "rejected",
			"delivery_id": uuidToString(delivery.ID),
			"reason":      reason,
		}
		if ip != "" && h.WebhookIPRateLimiter != nil {
			h.WebhookIPRateLimiter.Allow(r.Context(), ip)
		}
		h.finaliseDeliveryTerminal(r, delivery.ID, deliveryStatusRejected, http.StatusUnauthorized, respBody, reason)
		writeJSON(w, http.StatusUnauthorized, respBody)
		return
	}

	// 9. 触发器禁用 / autopilot 暂停或归档 → ignored。我们回 200，让发送方的
	//    webhook 重试机制别继续锤我们；"ignored" 状态 + delivery 行让运维查看
	//    delivery 日志时这个 no-op 可见。
	if !trigRow.Enabled {
		respBody := map[string]any{"status": "ignored", "delivery_id": uuidToString(delivery.ID), "reason": "trigger_disabled"}
		h.finaliseDeliveryTerminal(r, delivery.ID, deliveryStatusIgnored, http.StatusOK, respBody, "trigger_disabled")
		writeJSON(w, http.StatusOK, respBody)
		return
	}
	if autopilot.Status == "archived" {
		respBody := map[string]any{"status": "ignored", "delivery_id": uuidToString(delivery.ID), "reason": "autopilot_archived"}
		h.finaliseDeliveryTerminal(r, delivery.ID, deliveryStatusIgnored, http.StatusOK, respBody, "autopilot_archived")
		writeJSON(w, http.StatusOK, respBody)
		return
	}
	if autopilot.Status != "active" {
		respBody := map[string]any{"status": "ignored", "delivery_id": uuidToString(delivery.ID), "reason": "autopilot_paused"}
		h.finaliseDeliveryTerminal(r, delivery.ID, deliveryStatusIgnored, http.StatusOK, respBody, "autopilot_paused")
		writeJSON(w, http.StatusOK, respBody)
		return
	}

	// 10. 事件过滤范围 → ignored。若触发器声明了具体的 event_filters 列表而进来的
	//    事件在其范围外，记一条 ignored 投递，不创建昂贵的 run/task。
	if !webhookEventAllowedByTriggerScope(trigRow.EventFilters, envelope) {
		respBody := map[string]any{
			"status":      "ignored",
			"delivery_id": uuidToString(delivery.ID),
			"reason":      "event_filtered",
			"event":       envelope.Event,
		}
		h.finaliseDeliveryTerminal(r, delivery.ID, deliveryStatusIgnored, http.StatusOK, respBody, "event_filtered")
		writeJSON(w, http.StatusOK, respBody)
		return
	}

	// 11. 同步分配幂等运行，让既有 webhook 客户端保持 v0.4.0 的响应契约。queued
	//    delivery 仍是持久派发源：worker 在响应之后恢复这个 run，进程崩溃后也能
	//    恢复它。
	run, err := h.AutopilotService.AdmitAutopilotWebhookDelivery(
		r.Context(),
		autopilot,
		trigRow.ID,
		envelopeBytes,
		delivery.ID,
	)
	if err != nil {
		var quotaErr *service.AutopilotQuotaExceededError
		if errors.As(err, &quotaErr) {
			respBody := map[string]any{
				"status": "ignored", "delivery_id": uuidToString(delivery.ID),
				"reason_code": "quota_exceeded",
			}
			h.finaliseDeliveryTerminal(r, delivery.ID, deliveryStatusIgnored, http.StatusOK, respBody, quotaErr.Error(), "quota_exceeded")
			writeJSON(w, http.StatusOK, respBody)
			return
		}
		slog.Warn("webhook admission failed",
			"trigger_id", uuidToString(trigRow.ID),
			"autopilot_id", uuidToString(autopilot.ID),
			"delivery_id", uuidToString(delivery.ID),
			"error", err,
		)
		if h.WebhookDeliveryWorker != nil {
			h.WebhookDeliveryWorker.Notify()
		}
		writeError(w, http.StatusInternalServerError, "failed to admit autopilot")
		return
	}

	respBody := map[string]any{
		"status":       "accepted",
		"delivery_id":  uuidToString(delivery.ID),
		"run_id":       uuidToString(run.ID),
		"autopilot_id": uuidToString(autopilot.ID),
		"trigger_id":   uuidToString(trigRow.ID),
	}
	if run.Status == "skipped" {
		respBody = map[string]any{
			"status":      "skipped",
			"delivery_id": uuidToString(delivery.ID),
			"run_id":      uuidToString(run.ID),
		}
		if run.FailureReason.Valid {
			respBody["reason"] = run.FailureReason.String
		}
	}
	bodyJSON, _ := json.Marshal(respBody)
	if _, err := h.Queries.AcknowledgeWebhookDelivery(r.Context(), db.AcknowledgeWebhookDeliveryParams{
		ID:             delivery.ID,
		ResponseStatus: pgtype.Int4{Int32: http.StatusOK, Valid: true},
		ResponseBody:   pgtype.Text{String: string(bodyJSON), Valid: true},
	}); err != nil {
		slog.Warn("webhook: persist acknowledgement metadata failed",
			"delivery_id", uuidToString(delivery.ID),
			"error", err,
		)
	}
	if h.WebhookDeliveryWorker != nil {
		h.WebhookDeliveryWorker.Notify()
	}
	writeJSON(w, http.StatusOK, respBody)
}

// ── 事件过滤器辅助 ─────────────────────────────────────────────────────────

// WebhookEventFilter 声明一个事件与可选的 action 列表。Actions 为 nil/空表示
// 「该事件任意 action 都行」。
type WebhookEventFilter struct {
	Event   string   `json:"event"`
	Actions []string `json:"actions,omitempty"`
}

// validateWebhookEventFilters 在 HTTP 边界强制该契约，让畸形形状永远到不了
// 数据库。匹配器（读路径）信任已存内容——见 webhookEventAllowedByTriggerScope。
func validateWebhookEventFilters(filters []WebhookEventFilter) error {
	for i, f := range filters {
		if strings.TrimSpace(f.Event) == "" {
			return fmt.Errorf("event_filters[%d].event must not be empty", i)
		}
		for j, a := range f.Actions {
			if strings.TrimSpace(a) == "" {
				return fmt.Errorf("event_filters[%d].actions[%d] must not be empty", i, j)
			}
		}
	}
	return nil
}

// encodeWebhookEventFilters 返回 CREATE 时需持久化的 JSONB 字节。nil/空输入映射
// 为 nil 字节（列保持 NULL → 匹配器放行所有事件），因此创建时绝不写显式 `[]`。
func encodeWebhookEventFilters(filters []WebhookEventFilter) ([]byte, error) {
	if len(filters) == 0 {
		return nil, nil
	}
	return json.Marshal(filters)
}

// encodeWebhookEventFiltersAlways 总是返回非 nil 字节，空切片也返回
// (`[]byte("[]")`)。UPDATE handler 用它，让 PATCH body 里的显式空数组能经
// COALESCE 把既有行覆盖成清空态——传 nil 将与「字段省略、保持不变」无法区分。
func encodeWebhookEventFiltersAlways(filters []WebhookEventFilter) ([]byte, error) {
	if filters == nil {
		filters = []WebhookEventFilter{}
	}
	return json.Marshal(filters)
}

// webhookEventAllowedByTriggerScope 在触发器没有过滤器（NULL/空）时返回 true，
// 或入站信封至少命中一个声明过的过滤器时返回 true。
func webhookEventAllowedByTriggerScope(eventFilters []byte, envelope WebhookEnvelope) bool {
	if len(eventFilters) == 0 {
		return true
	}
	var filters []WebhookEventFilter
	if err := json.Unmarshal(eventFilters, &filters); err != nil {
		// 严格的写时校验应防止畸形字节到达这个分支。若某行确实损坏，就失败关闭
		// ——在「只允许 X」的策略上静默放宽白名单，比丢掉事件直到运维注意到更糟。
		slog.Warn("webhook: malformed event_filters, denying", "error", err)
		return false
	}
	if len(filters) == 0 {
		return true
	}
	_, eventName, eventAction := splitWebhookEvent(envelope.Event)
	actionCandidates := webhookActionCandidates(eventAction, envelope.EventPayload)
	for _, f := range filters {
		if f.Event != eventName {
			continue
		}
		if len(f.Actions) == 0 {
			return true
		}
		for _, action := range actionCandidates {
			for _, allowed := range f.Actions {
				if action == allowed {
					return true
				}
			}
		}
		// 刻意不在这里 return false：UI 允许若干共享同一事件名的过滤器（如两行
		// 覆盖不相交 action 的 workflow_run）。旧代码在首次命中事件名时就短路，
		// 会让一行按迭代顺序悄悄遮蔽其余行——见 PR #3231 评审。继续扫，让任何
		// 靠后的过滤器仍有出场机会。
	}
	return false
}

// splitWebhookEvent 把规范化事件（如 "github.workflow_run.completed"）拆成
// (provider, eventName, action)。对未限定的事件返回 ("", event, "")。
func splitWebhookEvent(event string) (provider, name, action string) {
	parts := strings.Split(event, ".")
	if isKnownProvider(parts[0]) {
		if len(parts) >= 3 {
			return parts[0], parts[1], strings.Join(parts[2:], ".")
		}
		if len(parts) == 2 {
			return parts[0], parts[1], ""
		}
		return parts[0], "", ""
	}
	if len(parts) >= 2 {
		return "", parts[0], strings.Join(parts[1:], ".")
	}
	return "", event, ""
}

func isKnownProvider(prefix string) bool {
	switch prefix {
	case "github", "gitlab", "bitbucket", "gitea":
		return true
	}
	return false
}

// webhookActionCandidates 从事件 action 后缀与众所周知的有效负载字段里提取
// 可能的 action 值。
func webhookActionCandidates(eventAction string, payload json.RawMessage) []string {
	seen := map[string]struct{}{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		seen[v] = struct{}{}
	}
	add(eventAction)
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err == nil {
		for _, key := range []string{"action", "state", "conclusion", "status"} {
			if v, ok := obj[key].(string); ok {
				add(v)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	return out
}

// ── 持久化辅助 ─────────────────────────────────────────────────────────────

type persistDeliveryInput struct {
	WorkspaceID     pgtype.UUID
	AutopilotID     pgtype.UUID
	TriggerID       pgtype.UUID
	Provider        string
	Event           string
	DedupeKey       string
	DedupeSource    string
	SignatureStatus string
	ContentType     string
	RawBody         []byte
	SelectedHeaders []byte
}

// persistInboundDelivery INSERT 一条全新 `queued` 投递，快乐路径返回 (row, false,
// nil)。dedupe 键唯一冲突时，在先前行上累加 attempt_count 后返回
// (existing-row, true, nil)。其它错误原样上抛，让 handler 干净地回 500。
func (h *Handler) persistInboundDelivery(r *http.Request, in persistDeliveryInput) (db.WebhookDelivery, bool, error) {
	params := db.CreateWebhookDeliveryParams{
		ID:              dbid.NewV7(),
		WorkspaceID:     in.WorkspaceID,
		AutopilotID:     in.AutopilotID,
		TriggerID:       in.TriggerID,
		Provider:        in.Provider,
		Event:           in.Event,
		SignatureStatus: in.SignatureStatus,
		Status:          deliveryStatusQueued,
		SelectedHeaders: in.SelectedHeaders,
		RawBody:         in.RawBody,
	}
	if in.DedupeKey != "" {
		params.DedupeKey = pgtype.Text{String: in.DedupeKey, Valid: true}
		params.DedupeSource = pgtype.Text{String: in.DedupeSource, Valid: true}
	}
	if in.ContentType != "" {
		params.ContentType = pgtype.Text{String: in.ContentType, Valid: true}
	}

	delivery, err := h.Queries.CreateWebhookDelivery(r.Context(), params)
	if err == nil {
		return delivery, false, nil
	}
	if !isUniqueViolation(err) || in.DedupeKey == "" {
		return db.WebhookDelivery{}, false, err
	}
	// Dedupe 冲突：取回原始行，累加 attempt 数。
	existing, lookupErr := h.Queries.GetWebhookDeliveryByTriggerAndDedupe(r.Context(), db.GetWebhookDeliveryByTriggerAndDedupeParams{
		TriggerID: in.TriggerID,
		DedupeKey: pgtype.Text{String: in.DedupeKey, Valid: true},
	})
	if lookupErr != nil {
		return db.WebhookDelivery{}, false, fmt.Errorf("lookup duplicate delivery: %w", lookupErr)
	}
	bumped, bumpErr := h.Queries.BumpWebhookDeliveryAttempt(r.Context(), existing.ID)
	if bumpErr != nil {
		// 仍按重复处理；只记下 bump 失败供运维排查，返回我们确实读到的行。
		slog.Warn("webhook: failed to bump attempt_count",
			"delivery_id", uuidToString(existing.ID),
			"error", bumpErr,
		)
		return existing, true, nil
	}
	return bumped, true, nil
}

// finaliseDeliveryTerminal 记录一条未派发的结果（rejected、ignored、failed）。
// HTTP 状态与完整响应体都会被捕获，未来某个 Deliveries UI 能精确展示我们回了
// 什么。
func (h *Handler) finaliseDeliveryTerminal(
	r *http.Request,
	id pgtype.UUID,
	status string,
	httpStatus int,
	responseBody any,
	errMsg string,
	reasonCode ...string,
) {
	bodyJSON, _ := json.Marshal(responseBody)
	params := db.UpdateWebhookDeliveryTerminalParams{
		ID:             id,
		Status:         status,
		ResponseStatus: pgtype.Int4{Int32: int32(httpStatus), Valid: true},
		ResponseBody:   pgtype.Text{String: string(bodyJSON), Valid: true},
	}
	if errMsg != "" {
		params.Error = pgtype.Text{String: errMsg, Valid: true}
	}
	if len(reasonCode) > 0 && reasonCode[0] != "" {
		params.ReasonCode = pgtype.Text{String: reasonCode[0], Valid: true}
	}
	if _, err := h.Queries.UpdateWebhookDeliveryTerminal(r.Context(), params); err != nil {
		slog.Warn("webhook: finalise terminal failed",
			"delivery_id", uuidToString(id),
			"status", status,
			"error", err,
		)
	}
	h.Metrics.RecordWebhookDelivery(h.deliveryProvider(r.Context(), id), status)
}

// finaliseDeliveryWithRun 记录一条产出了（或准入时跳转成）autopilot_run 的投递。
// 与终态路径相同的响应捕获。
func (h *Handler) finaliseDeliveryWithRun(
	r *http.Request,
	id pgtype.UUID,
	status string,
	runID pgtype.UUID,
	httpStatus int,
	responseBody any,
) {
	bodyJSON, _ := json.Marshal(responseBody)
	params := db.UpdateWebhookDeliveryDispatchedParams{
		ID:             id,
		Status:         status,
		AutopilotRunID: runID,
		ResponseStatus: pgtype.Int4{Int32: int32(httpStatus), Valid: true},
		ResponseBody:   pgtype.Text{String: string(bodyJSON), Valid: true},
	}
	if _, err := h.Queries.UpdateWebhookDeliveryDispatched(r.Context(), params); err != nil {
		slog.Warn("webhook: finalise with run failed",
			"delivery_id", uuidToString(id),
			"run_id", uuidToString(runID),
			"error", err,
		)
	}
	h.Metrics.RecordWebhookDelivery(h.deliveryProvider(r.Context(), id), status)
}

// deliveryProvider 尽力读取某 delivery id 的 provider，让 webhook delivery 指标
// 带上可用溯源。查询失败时回退 "generic"——每次 finalise 调用指标必须恰好 +1，
// 让 dashboard 计数与 autopilot_run 体量对齐。
func (h *Handler) deliveryProvider(ctx context.Context, id pgtype.UUID) string {
	if h.Queries == nil {
		return "generic"
	}
	row, err := h.Queries.GetWebhookDelivery(ctx, id)
	if err != nil || row.Provider == "" {
		return "generic"
	}
	return row.Provider
}

// ── 限流 / IP 管道 ─────────────────────────────────────────────────────────

func writeWebhookRateLimit(w http.ResponseWriter, r *http.Request, limiter WebhookRateLimiter, key, gate string, metrics *obsmetrics.BusinessMetrics) {
	retryAfter := time.Second
	if limiter != nil {
		if retry := slidingWindowLimiterRetryAfter(r.Context(), limiter, key); retry > 0 {
			retryAfter = retry
		}
	}
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	metrics.RecordWebhookRateLimited(gate)
	writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
}

// clientIPForRateLimit 返回用作限流桶键的 IP。
//
// 默认行为：用 r.RemoteAddr 的主机部分。转发头（X-Forwarded-For、X-Real-IP）被
// 忽略，除非运维经 MULTICA_TRUSTED_PROXIES 显式选择信任——且即使如此，也只在
// r.RemoteAddr 本身落在所列某个 CIDR 内时才用。
func (h *Handler) clientIPForRateLimit(r *http.Request) string {
	remoteIP := remoteAddrHost(r.RemoteAddr)
	if len(h.cfg.TrustedProxies) == 0 {
		return remoteIP
	}
	remoteAddr, ok := parseNetIPAddr(remoteIP)
	if !ok || !addrInPrefixes(remoteAddr, h.cfg.TrustedProxies) {
		// 来源不是可信代理——头部不可信。
		return remoteIP
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	return remoteIP
}

func remoteAddrHost(remote string) string {
	if remote == "" {
		return ""
	}
	if strings.HasPrefix(remote, "[") {
		if end := strings.IndexByte(remote, ']'); end > 0 {
			return remote[1:end]
		}
	}
	if i := strings.LastIndexByte(remote, ':'); i >= 0 && !strings.Contains(remote, "]") {
		if strings.Count(remote, ":") == 1 {
			return remote[:i]
		}
	}
	return remote
}

func parseNetIPAddr(s string) (netip.Addr, bool) {
	if s == "" {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func addrInPrefixes(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
