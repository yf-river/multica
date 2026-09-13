// 职责定位：Cloud 计费（billing）的转发层处理器。本仓库自己不持有任何计费数据，而是把所有
// 计费端点原样代理给 multica-cloud HTTP 服务（Fleet 与 Billing 共享 `:8080`，见上游 README），
// 转发路径统一为 cloud 侧的 /api/v1/billing/* 与 /api/v1/subscriptions/*。详细约定如下方
// 本文件导读注释。
//
// 处理方式分三档：
//   - 余额类 owner-credit 端点（GET/POST /api/cloud-billing/*）走通用代理 proxyCloudRuntime，
//     只注入 X-User-ID；
//   - workspace 订阅端点（/api/cloud-subscriptions/*）走更严的包装 proxyCloudSubscription，
//     额外注入权威 workspace_id、对 Idempotency-Key 做白名单校验；
//   - Stripe webhook（/api/webhooks/stripe）是唯一按「原始字节」转发的出口（见
//     HandleCloudBillingStripeWebhook）。
package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/cloudruntime"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/util"
)

// Cloud 计费端点全部代理到同一个 multica-cloud HTTP 服务——cloud-runtime 背后的服务
// （Fleet 与 Billing 共享 `:8080`，按上游 README）。这里所有路径都原样转发到 cloud 侧的
// /api/v1/billing/*，镜像 cloud-runtime 处理器的形态：
//
//   - owner-credit（余额）端点挂在路由的 /api/cloud-billing/* 下。workspace 订阅端点挂在
//     /api/cloud-subscriptions/* 下，额外加了成员/角色检查，并注入权威的 workspace ID。
//     两者都要求常规 Auth，并把解析出的 user_id 作为 `X-User-ID` 注入。
//
//   - Stripe webhook 是唯一的例外：它挂在 /api/webhooks/stripe（Auth 组之外），接收原始
//     请求体（逐字节不加工），并把 `Stripe-Signature` 原样转发给 cloud 侧，由 cloud 用它的
//     STRIPE_WEBHOOK_SECRET 验签。上游契约说得明确：「webhook 使用原始请求体进行签名校验，
//     不要在反向代理里改写 body」。
//
// owner-credit 路径共用 `proxyCloudRuntime`。workspace 订阅用一个更小的严格包装来注入
// workspace_id 并对 Idempotency-Key 做白名单；webhook 仍是唯一的原始 body 转发者。

// maxStripeWebhookBodySize 限制我们愿意转发给上游的原始 body 大小。Stripe 文档里的事件
// payload 上限远低于此；设这个上限是为了防止恶意发送方在「上游拒签」之前先逼我们读走任意
// 内存。
const maxStripeWebhookBodySize = 1 << 20 // 1 MiB

// stripeSignatureHeader 是 Stripe 用来携带「对原始 body 的 HMAC」的头的标准名字。客户端发
// 什么我们原样转发什么；只有 cloud 侧知道共享密钥、负责在验签不符时拒绝。
const stripeSignatureHeader = "Stripe-Signature"

// 幂等键：可放 header（Idempotency-Key）也可放请求体，转发前要校验长度。
const idempotencyKeyHeader = "Idempotency-Key"
const maxCloudSubscriptionIdempotencyKeyLength = 255
const maxCloudSubscriptionSeatPurchaseIdempotencyKeyLength = 200

// cloudSubscriptionCheckoutRequest 是「创建订阅结账会话」的客户端请求体（我们只校验
// interval，其余字段不直接透传）。
type cloudSubscriptionCheckoutRequest struct {
	Interval       string `json:"interval"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// cloudSubscriptionCheckoutUpstreamRequest 是真正转发给 cloud 的请求体。这个白名单是刻意
// 收紧的：cloud 新增的字段不会自动透传，必须等主仓库显式评审并在这里加上才行。
type cloudSubscriptionCheckoutUpstreamRequest struct {
	WorkspaceID    string `json:"workspace_id"`
	Interval       string `json:"interval"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	CustomerEmail  string `json:"customer_email,omitempty"`
}

// cloudSubscriptionSeatPurchasePreviewRequest 是「加购席位预览」的请求体，只接受增量座位数。
type cloudSubscriptionSeatPurchasePreviewRequest struct {
	AdditionalSeats int `json:"additional_seats"`
}

// cloudSubscriptionSeatPurchaseRequest 是「确认加购席位」的请求体：客户端必须回传它当初看到
// 的「期望当前座位数 / 期望购买版本 / 已接受的 proration 金额 / 币种」，让 cloud 对报价做
// 二次确认（乐观并发）。
type cloudSubscriptionSeatPurchaseRequest struct {
	AdditionalSeats         int    `json:"additional_seats"`
	ExpectedCurrentSeats    int    `json:"expected_current_seats"`
	ExpectedPurchaseVersion int64  `json:"expected_purchase_version"`
	AcceptedProrationAmount int64  `json:"accepted_proration_amount"`
	Currency                string `json:"currency"`
	IdempotencyKey          string `json:"idempotency_key,omitempty"`
}

// requireCloudSubscriptionWorkspace 是路由层成员/角色中间件后面的「处理器级兜底」。
// 本地角色只是便宜的纵深防御；Cloud 在每次计费变更前仍会做权威授权。
func (h *Handler) requireCloudSubscriptionWorkspace(w http.ResponseWriter, r *http.Request, roles ...string) (string, string, bool) {
	if isMachineCredentialActor(r) {
		writeError(w, http.StatusForbidden, "this endpoint is only available to human actors")
		return "", "", false
	}
	if !featureflags.BillingWorkspaceSubscriptionsEnabled(r.Context(), h.FeatureFlags) {
		writeError(w, http.StatusServiceUnavailable, "workspace subscriptions are not enabled")
		return "", "", false
	}

	workspaceID := ctxWorkspaceID(r.Context())
	workspaceUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "workspace_id or workspace_slug is required")
		return "", "", false
	}
	member, ok := ctxMember(r.Context())
	if !ok {
		writeError(w, http.StatusForbidden, "workspace membership required")
		return "", "", false
	}
	if len(roles) > 0 && !roleAllowed(member.Role, roles...) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return "", "", false
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return "", "", false
	}
	return util.UUIDToString(workspaceUUID), userID, true
}

// proxyCloudSubscription 是 workspace 订阅类端点的统一转发入口：要求 Cloud 运行时已启用，
// 通过 h.CloudRuntime.Do 出站并把响应/错误转写回客户端。userID 用于 cloud 侧审计与鉴权。
func (h *Handler) proxyCloudSubscription(w http.ResponseWriter, r *http.Request, method, path, userID string, body []byte, headers http.Header) {
	if h.CloudRuntime == nil || !h.CloudRuntime.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "cloud runtime is not configured")
		return
	}
	resp, err := h.CloudRuntime.Do(r.Context(), cloudruntime.Request{
		Method:    method,
		Path:      path,
		Body:      body,
		UserID:    userID,
		RequestID: cloudRuntimeRequestID(r),
		Op:        "billing",
		Headers:   headers,
	})
	if err != nil {
		writeCloudRuntimeError(w, r, err)
		return
	}
	writeCloudRuntimeResponse(w, resp)
}

// cloudSubscriptionIdempotencyHeaders 从请求头里抽出 Idempotency-Key 打包成转发头；
// 请求里没带就返回 nil。
func cloudSubscriptionIdempotencyHeaders(r *http.Request) http.Header {
	key := strings.TrimSpace(r.Header.Get(idempotencyKeyHeader))
	if key == "" {
		return nil
	}
	return http.Header{idempotencyKeyHeader: []string{key}}
}

// requireCloudSubscriptionIdempotencyKey 取幂等键（请求体里的优先，缺了退回 header），
// 都缺返回 400，并校验长度上限。
func requireCloudSubscriptionIdempotencyKey(w http.ResponseWriter, r *http.Request, bodyKey string) (string, bool) {
	key := strings.TrimSpace(bodyKey)
	if key == "" {
		key = strings.TrimSpace(r.Header.Get(idempotencyKeyHeader))
	}
	if key == "" {
		writeError(w, http.StatusBadRequest, "Idempotency-Key or idempotency_key is required")
		return "", false
	}
	if len(key) > maxCloudSubscriptionIdempotencyKeyLength {
		writeError(w, http.StatusBadRequest, "idempotency key must be at most 255 bytes")
		return "", false
	}
	return key, true
}

// GetCloudWorkspaceSubscriptionSummary 转发 cloud 的 Billing 页读取：已解析的 entitlement
// 加本地订阅事实（座位数、周期、取消与宽限状态、Portal 是否可用）。与 entitlement 一样普通
// 成员可读；cloud 会对着自己的产品库复核成员身份。
//
// cloud 用自己库里（不查 Stripe）的数据回答，所以 Stripe 故障时这里仍可用。比该端点更老的
// cloud 会答 404，客户端把页面当「不可用」处理——绝不会误显示成 Free。
func (h *Handler) GetCloudWorkspaceSubscriptionSummary(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requireCloudSubscriptionWorkspace(w, r)
	if !ok {
		return
	}
	h.proxyCloudSubscription(w, r, http.MethodGet, "/api/v1/subscriptions/"+workspaceID+"/summary", userID, nil, nil)
}

// GetCloudWorkspaceSubscriptionPrices 转发 cloud 侧已校验过的按座位定价，避免客户端把
// Stripe 随时可能改的金额写死。
//
// 普通成员可读：看 Pro 多少钱不是特权动作，且 cloud 会把读取范围限定在调用者所属的
// workspace。cloud 只返回币种、最小货币单位金额与周期——Price/Product ID 留在服务端——
// 配置的 Price 无法校验时回 503，客户端把它渲染成「以 Checkout 为准」，而不是猜个数。
func (h *Handler) GetCloudWorkspaceSubscriptionPrices(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requireCloudSubscriptionWorkspace(w, r)
	if !ok {
		return
	}
	h.proxyCloudSubscription(w, r, http.MethodGet, "/api/v1/subscriptions/"+workspaceID+"/prices", userID, nil, nil)
}

// CreateCloudWorkspaceSubscriptionCheckout 把中间件解析出的 workspace 与已登录用户库里存的
// email 注入上游请求体。调用方没法用 JSON 偷塞一个别的 workspace 或别的 Stripe 付款人身份；
// 价格、数量与 Checkout 仍由 Cloud 说了算。
func (h *Handler) CreateCloudWorkspaceSubscriptionCheckout(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requireCloudSubscriptionWorkspace(w, r, "owner", "admin")
	if !ok {
		return
	}
	body, ok := readCloudRuntimeJSONBody(w, r)
	if !ok {
		return
	}
	var in cloudSubscriptionCheckoutRequest
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if in.Interval != "month" && in.Interval != "year" {
		writeError(w, http.StatusBadRequest, "interval must be month or year")
		return
	}
	idempotencyKey, ok := requireCloudSubscriptionIdempotencyKey(w, r, in.IdempotencyKey)
	if !ok {
		return
	}
	payerUserID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	user, err := h.Queries.GetUser(r.Context(), payerUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve checkout payer")
		return
	}
	customerEmail := strings.TrimSpace(user.Email)
	if customerEmail == "" {
		writeError(w, http.StatusInternalServerError, "checkout payer email is unavailable")
		return
	}
	upstreamBody, err := json.Marshal(cloudSubscriptionCheckoutUpstreamRequest{
		WorkspaceID:    workspaceID,
		Interval:       in.Interval,
		IdempotencyKey: idempotencyKey,
		CustomerEmail:  customerEmail,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build subscription request")
		return
	}
	h.proxyCloudSubscription(w, r, http.MethodPost, "/api/v1/subscriptions/checkout-sessions", userID, upstreamBody, cloudSubscriptionIdempotencyHeaders(r))
}

// ReconcileCloudWorkspaceSubscriptionSeats 只是个「提示」：本 handler 与它的调用方都不提供
// 座位数。Cloud 会用最小权限的「产品库」连接自己去重数人类成员。
func (h *Handler) ReconcileCloudWorkspaceSubscriptionSeats(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requireCloudSubscriptionWorkspace(w, r, "owner", "admin")
	if !ok {
		return
	}
	h.proxyCloudSubscription(w, r, http.MethodPost, "/api/v1/subscriptions/"+workspaceID+"/seats/reconcile", userID, nil, nil)
}

// PreviewCloudWorkspaceSubscriptionSeatPurchase 只接受增量座位数。Cloud 读取权威的当前数量，
// 返回 Stripe 估算的 proration 金额与下一张完整续费账单。
func (h *Handler) PreviewCloudWorkspaceSubscriptionSeatPurchase(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requireCloudSubscriptionWorkspace(w, r, "owner", "admin")
	if !ok {
		return
	}
	body, ok := readCloudRuntimeJSONBody(w, r)
	if !ok {
		return
	}
	var in cloudSubscriptionSeatPurchasePreviewRequest
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if in.AdditionalSeats < 1 {
		writeError(w, http.StatusBadRequest, "additional_seats must be positive")
		return
	}
	upstreamBody, err := json.Marshal(in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build seat purchase preview")
		return
	}
	h.proxyCloudSubscription(w, r, http.MethodPost,
		"/api/v1/subscriptions/"+workspaceID+"/seats/purchase-preview", userID, upstreamBody, nil)
}

// PurchaseCloudWorkspaceSubscriptionSeats 转发用户已确认的报价。白名单请求体不含任何客户端
// 指定的 workspace 或绝对目标座位数；Cloud 会复算报价，并全权负责并发、幂等与 Stripe 写入。
func (h *Handler) PurchaseCloudWorkspaceSubscriptionSeats(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requireCloudSubscriptionWorkspace(w, r, "owner", "admin")
	if !ok {
		return
	}
	body, ok := readCloudRuntimeJSONBody(w, r)
	if !ok {
		return
	}
	var in cloudSubscriptionSeatPurchaseRequest
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if in.AdditionalSeats < 1 || in.ExpectedCurrentSeats < 1 ||
		in.ExpectedPurchaseVersion < 1 || in.AcceptedProrationAmount < 0 || !isASCIICurrency(in.Currency) {
		writeError(w, http.StatusBadRequest, "invalid seat purchase confirmation")
		return
	}
	key, ok := requireCloudSubscriptionIdempotencyKey(w, r, in.IdempotencyKey)
	if !ok {
		return
	}
	if len(key) > maxCloudSubscriptionSeatPurchaseIdempotencyKeyLength {
		writeError(w, http.StatusBadRequest, "seat purchase idempotency key must be at most 200 bytes")
		return
	}
	in.Currency = strings.ToLower(in.Currency)
	in.IdempotencyKey = key
	upstreamBody, err := json.Marshal(in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build seat purchase request")
		return
	}
	h.proxyCloudSubscription(w, r, http.MethodPost,
		"/api/v1/subscriptions/"+workspaceID+"/seats/purchases", userID, upstreamBody,
		http.Header{idempotencyKeyHeader: []string{key}})
}

// isASCIICurrency 校验币种是恰好 3 个 ASCII 字母（ISO 4217 简写）。
func isASCIICurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') {
			return false
		}
	}
	return true
}

// CreateCloudWorkspaceSubscriptionPortal 创建进入 Stripe 客户门户的会话（owner/admin 可操作），
// 转发时需要幂等键。
func (h *Handler) CreateCloudWorkspaceSubscriptionPortal(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requireCloudSubscriptionWorkspace(w, r, "owner", "admin")
	if !ok {
		return
	}
	if _, ok := requireCloudSubscriptionIdempotencyKey(w, r, ""); !ok {
		return
	}
	h.proxyCloudSubscription(w, r, http.MethodPost, "/api/v1/subscriptions/"+workspaceID+"/portal-sessions", userID, nil, cloudSubscriptionIdempotencyHeaders(r))
}

// GetCloudBillingBalance 转发 GET /api/v1/billing/balance。
//
// 返回调用者的钱包余额。Cloud 读 `X-User-ID`；我们把它从已认证的上下文中盖上。
func (h *Handler) GetCloudBillingBalance(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodGet, "/api/v1/billing/balance", cloudRuntimeProxyOptions{
		withUserID: true,
	})
}

// ListCloudBillingTransactions 转发 GET /api/v1/billing/transactions。
//
// 上游支持 `page` / `page_size`；我们原样转发查询串。
func (h *Handler) ListCloudBillingTransactions(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodGet, "/api/v1/billing/transactions", cloudRuntimeProxyOptions{
		withUserID: true,
		withQuery:  true,
	})
}

// ListCloudBillingBatches 转发 GET /api/v1/billing/batches。
//
// 返回 owner 的充值/奖励批次（分页），查询形态与 transactions 相同。
func (h *Handler) ListCloudBillingBatches(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodGet, "/api/v1/billing/batches", cloudRuntimeProxyOptions{
		withUserID: true,
		withQuery:  true,
	})
}

// ListCloudBillingTopups 转发 GET /api/v1/billing/topups。
func (h *Handler) ListCloudBillingTopups(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodGet, "/api/v1/billing/topups", cloudRuntimeProxyOptions{
		withUserID: true,
		withQuery:  true,
	})
}

// ListCloudBillingPriceTiers 转发 GET /api/v1/billing/price-tiers。
//
// 按上游文档，这个端点要求 `X-User-ID`（它跟 /api/v1/billing/* 其余部分在同一道鉴权墙后），
// 即使今天的响应对所有 owner 都一样。我们盖上这个头，让 cloud 能审计是谁在列档位——也为将来
// 按客户差异化定价保持契约统一。
func (h *Handler) ListCloudBillingPriceTiers(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodGet, "/api/v1/billing/price-tiers", cloudRuntimeProxyOptions{
		withUserID: true,
	})
}

// CreateCloudBillingCheckoutSession 转发 POST /api/v1/billing/checkout-sessions。
//
// Body 形态（按上游）：`{tier_id, customer_email?}`。我们不在乎内容——proxyCloudRuntime
// 只校验它是合法 JSON 并原样转发字节。
func (h *Handler) CreateCloudBillingCheckoutSession(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodPost, "/api/v1/billing/checkout-sessions", cloudRuntimeProxyOptions{
		withUserID: true,
		withBody:   true,
	})
}

// GetCloudBillingCheckoutSession 转发 GET /api/v1/billing/checkout-sessions/{session_id}。
//
// 路径参数拼进上游 URL；缺失时在做任何网络请求前先回 400。
func (h *Handler) GetCloudBillingCheckoutSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	// Stripe Checkout 会话 id 本来就是 `cs_<base62>` —— 字母数字加下划线，没有别的。这个值
	// 会被直接拼进上游 URL 路径，所以超出该字母表的内容理论上可能带进路径/查询/片段段来改道
	// 请求。这里用白名单而不是黑名单：将来 Stripe 格式升级，新字符不会悄悄溜过我们，只会被
	// 拒掉，直到我们有意识地放宽规则。
	if !isValidStripeSessionID(sessionID) {
		writeError(w, http.StatusBadRequest, "invalid session_id")
		return
	}
	h.proxyCloudRuntime(w, r, http.MethodGet, "/api/v1/billing/checkout-sessions/"+sessionID, cloudRuntimeProxyOptions{
		withUserID: true,
	})
}

// isValidStripeSessionID 判断 s 是否「语法上像」Stripe Checkout 会话 id：非空的
// `[A-Za-z0-9_]` 串。Stripe 自己的格式是 `cs_<base62>`，但我们接受更宽的「字母数字+下划线」
// 集合，因为 (a) 它覆盖可能路由到这里的各种 Stripe ID 变体，(b) 它跟 Stripe 的 ID 文法同步，
// 又不用把 `cs_` 前缀写死。
func isValidStripeSessionID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '_':
		default:
			return false
		}
	}
	return true
}

// CreateCloudBillingPortalSession 转发 POST /api/v1/billing/portal-sessions。
//
// Body 形态由上游定义，可以为空。这里把 body 当「可选 JSON」：cloud_runtime 的助手在
// withBody=true 时会拒绝空 body，所以对 portal-sessions 我们刻意不开 withBody 也不向上游发
// body。将来上游若要求带 body，改成 withBody 让 cloud 校验即可。
func (h *Handler) CreateCloudBillingPortalSession(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodPost, "/api/v1/billing/portal-sessions", cloudRuntimeProxyOptions{
		withUserID: true,
	})
}

// HandleCloudBillingStripeWebhook 是 Stripe webhook 投递的公开入口。这里有三个关键点，且它们
// *不*与常规 proxyCloudRuntime 路径共享：
//
//  1. 不做认证。Stripe 是从自己的基础设施 POST 过来的；我们没有用户上下文，也不该捏造一个。
//     应用层的认证由 Stripe 的 HMAC 签名替代——由上游 cloud 服务验签。因此这条路由挂在
//     router.go 的 Auth 组之外。
//
//  2. body 逐字节原样转发。Stripe 的签名是对它发来的确切字节算的。我们绝不能 json.Unmarshal/
//     重新 marshal、裁空白或以任何方式碰 payload——哪怕差一个字节验签都会失败。
//
//  3. `Stripe-Signature` 头原样转发。这是从 Stripe 到上游的整个认证通道。
//
// 我们确实用 MaxBytesReader 限制读取量：恶意或配置错误的发送方不能在「上游凭签名拒绝」之前
// 逼我们缓冲任意大小的内存。
//
// 在读取 body 之前有两个预检：
//
//   - 按 IP 限流（镜像 HandleAutopilotWebhook）。该端点是公开的，不限流的话攻击者喷洒假
//     payload 会让每个请求都打到上游一次，烧掉 cloud 的 webhook 处理预算。伪造签名的喷洒
//     同样计入这个限流，所以快速 429 能止血。
//
//   - 必须有 `Stripe-Signature` 头。真正的 Stripe 投递总是带着它；缺失 ≡ 不是 Stripe 来的。
//     本地直接回 401，省掉最常见的一种越权试探（脚本小子的 curl）的一次上游往返。这是上游
//     行为的严格超集——Cloud 对缺失签名同样回 401——所以不影响 Stripe 自己投递仪表盘的视图。
func (h *Handler) HandleCloudBillingStripeWebhook(w http.ResponseWriter, r *http.Request) {
	if h.CloudRuntime == nil || !h.CloudRuntime.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "cloud runtime is not configured")
		return
	}

	// 在读取 body 或打上游之前先按 IP 限流。刻意复用 autopilot webhook 的同一个限流器：
	// 两者都是「公开、无认证」的入口且滥用画像相同，共用预算就只有一个旋钮可调。
	if h.WebhookIPRateLimiter != nil {
		if ip := h.clientIPForRateLimit(r); ip != "" {
			if !h.WebhookIPRateLimiter.Allow(r.Context(), ip) {
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
		}
	}

	// 真正的 Stripe 投递总是带 Stripe-Signature；缺失就是很有把握的「不是 Stripe」信号。
	// 本地拒绝以省一次上游往返。注意：用 Header.Values 判断「是否存在」而不是 Get——
	// 显式设成 "" 的头也算缺失（与上游的解释一致）。
	if len(r.Header.Values(stripeSignatureHeader)) == 0 {
		writeError(w, http.StatusUnauthorized, "missing Stripe-Signature header")
		return
	}

	// body 上限与 JSON 代理一致。Stripe 文档里的 payload 上限远小于此；这个限制是防御，
	// 不是契约。
	r.Body = http.MaxBytesReader(w, r.Body, maxStripeWebhookBodySize)
	// 这里用 io.ReadAll 是合适的：webhook body 在转发前要完整读入（Stripe 对字节签名，没法
	// 流式）。与 JSON 代理不同，这里刻意不裁空白、不做 JSON 校验——上游的验签是对我们实际
	// 收到的字节算的，任何一步变换都会悄悄弄坏验签。
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// 原样转发 Stripe-Signature，以及 Stripe 原始的 Content-Type（让上游看到与 Stripe 直接调
	// 它时完全一样的头集合）。cloudruntime 的默认 Content-Type 会把 `application/json;
	// charset=utf-8` 覆盖成纯 `application/json`；验签不在乎（HMAC 是对 body 算的），但保留
	// 确切的头很便宜，还能消掉调试时「这个头怎么不一样」的疑问。cloudruntime.Request 里调用
	// 方提供的 Headers 会覆盖默认值，所以把 Content-Type 放进来就够了。
	headers := http.Header{}
	if sigs := r.Header.Values(stripeSignatureHeader); len(sigs) > 0 {
		headers[stripeSignatureHeader] = sigs
	}
	if cts := r.Header.Values("Content-Type"); len(cts) > 0 {
		headers["Content-Type"] = cts
	}

	resp, err := h.CloudRuntime.Do(r.Context(), cloudruntime.Request{
		Method:    http.MethodPost,
		Path:      "/api/v1/webhooks/stripe",
		Body:      body,
		Headers:   headers,
		RequestID: cloudRuntimeRequestID(r),
		Op:        "billing",
		// 刻意不带 UserID——webhook 按设计无认证；即便注入一个空头也无害，但显式留白能让
		// 读者一眼看清这份契约。
	})
	if err != nil {
		writeCloudRuntimeError(w, r, err)
		return
	}
	writeCloudRuntimeResponse(w, resp)
}
