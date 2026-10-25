// 职责定位：Composio 集成的 HTTP 处理器（MUL-3720，第二阶段 MVP）。Composio 是一种
// 托管式第三方工具连接服务：前端跳转去 Composio 走完 OAuth 后，我们存下
// 「用户 ↔ 外部工具账号」的连接关系，供后续让 Agent 调用该工具。
//
// 路由归属：Composio 连接属于「用户」而非「工作区」，所以这批处理器放在 workspace
// 成员组之外。connect/init、toolkits、connections、delete 四个管理端点按用户鉴权
// （requireUserID），挂在 Auth 中间件下。ComposioCallback 是唯一例外：它是公开路由
// （在 Auth 组之外，见 router.go / MUL-3843）——浏览器常常没带会话 cookie 就跳到回调
// （会话过期、SameSite/ITP 剥 cookie、隐私窗口、自托管回调子域都可能导致），它的身份
// 完全来自 HMAC 签名的 state 查询参数，由 CompleteCallback 校验后才动作。
//
// 未配置约定：h.Composio 为 nil（即未设置 COMPOSIO_API_KEY）时，本组全部端点回 503，
// 与 Lark/GitHub 那套「integration not configured」的约定一致。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/featureflags"
	composio "github.com/multica-ai/multica/server/internal/integrations/composio"
)

// ComposioConnectInitRequest 是 POST /connect/init 的请求体。
type ComposioConnectInitRequest struct {
	ToolkitSlug string `json:"toolkit_slug"`
}

// ComposioConnectInitResponse 携带前端要跳转过去的 Composio 托管 Connect Link。
type ComposioConnectInitResponse struct {
	RedirectURL string `json:"redirect_url"`
}

// ComposioConnectionResponse 是一条连接记录对外返回的线格式。
type ComposioConnectionResponse struct {
	ID          string  `json:"id"`
	ToolkitSlug string  `json:"toolkit_slug"`
	Status      string  `json:"status"`
	ConnectedAt string  `json:"connected_at"`
	LastUsedAt  *string `json:"last_used_at"`
}

// ComposioToolkitResponse 是目录里一个 toolkit 对外返回的线格式。服务端只会返回
// 已启用 auth 配置的 toolkit，所以这里的每一项按构造保证可以连接。
type ComposioToolkitResponse struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Logo     string `json:"logo,omitempty"`
	Category string `json:"category,omitempty"`
}

// composioMCPAppsEnabled 通过特性开关判断当前是否启用 Composio MCP 应用能力。
func (h *Handler) composioMCPAppsEnabled(ctx context.Context) bool {
	return featureflags.ComposioMCPAppsEnabled(ctx, h.FeatureFlags)
}

// ComposioConnectInit (POST /api/integrations/composio/connect/init) 为请求的 toolkit
// 发起一次 Composio 托管授权流程，返回浏览器跳转 URL。不支持的 toolkit slug 是 400
// （MVP 只接了 Notion）。
func (h *Handler) ComposioConnectInit(w http.ResponseWriter, r *http.Request) {
	if h.Composio == nil || !h.composioMCPAppsEnabled(r.Context()) {
		writeFeatureDisabled(w, "composio_not_configured", "composio integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	var req ComposioConnectInitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.ToolkitSlug) == "" {
		writeError(w, http.StatusBadRequest, "toolkit_slug is required")
		return
	}

	redirectURL, err := h.Composio.BeginConnect(r.Context(), userUUID, req.ToolkitSlug)
	if err != nil {
		if errors.Is(err, composio.ErrToolkitNotSupported) {
			writeError(w, http.StatusBadRequest, "toolkit not supported")
			return
		}
		writeError(w, http.StatusBadGateway, "failed to start composio connect")
		return
	}
	writeJSON(w, http.StatusOK, ComposioConnectInitResponse{RedirectURL: redirectURL})
}

// ComposioCallback (GET /api/integrations/composio/callback) 是 Composio 在托管授权流程
// 结束后把用户浏览器重定向回来的地址。它是公开路由（Auth 中间件组之外，见 router.go /
// MUL-3843）：浏览器经常不带会话 cookie 就落到这里（会话过期、SameSite/ITP 剥 cookie、
// 隐私窗口、自托管回调子域等）。因此身份完全来自 HMAC 签名的 state 查询参数——
// CompleteCallback 在校验通过前不做任何动作。成功后把连接行 upsert 落库，并把浏览器
// 重定向到设置页；任何失败都重定向回同一页面并带上稳定的错误码，绝不让用户停在
// 一片空白的 API 响应上。
func (h *Handler) ComposioCallback(w http.ResponseWriter, r *http.Request) {
	if h.Composio == nil || !h.composioMCPAppsEnabled(r.Context()) {
		writeFeatureDisabled(w, "composio_not_configured", "composio integration not configured")
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	status := q.Get("status")
	connectedAccountID := q.Get("connected_account_id")

	slug, err := h.Composio.CompleteCallback(r.Context(), state, status, connectedAccountID)
	if err != nil {
		// 所有失败（state 被篡改/过期、status 非成功、落库出错）都收敛到同一个通用失败
		// 重定向——绝不告诉浏览器具体是哪一项检查没过，避免泄露可探测的信息。
		http.Redirect(w, r, h.Composio.CallbackRedirect(slug, false), http.StatusFound)
		return
	}
	http.Redirect(w, r, h.Composio.CallbackRedirect(slug, true), http.StatusFound)
}

// ListComposioConnections (GET /api/integrations/composio/connections) 返回调用者名下
// 的活动连接。
func (h *Handler) ListComposioConnections(w http.ResponseWriter, r *http.Request) {
	if h.Composio == nil || !h.composioMCPAppsEnabled(r.Context()) {
		writeFeatureDisabled(w, "composio_not_configured", "composio integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	conns, err := h.Composio.ListConnections(r.Context(), userUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list composio connections")
		return
	}
	out := make([]ComposioConnectionResponse, 0, len(conns))
	for _, c := range conns {
		out = append(out, ComposioConnectionResponse{
			ID:          c.ID,
			ToolkitSlug: c.ToolkitSlug,
			Status:      c.Status,
			ConnectedAt: c.ConnectedAt,
			LastUsedAt:  c.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ListComposioToolkits (GET /api/integrations/composio/toolkits) 返回当前可连接的
// Composio toolkit 目录，供设置页渲染。自 MUL-4009 起服务端会过滤掉项目里没启用 auth
// 配置的 toolkit，所以这里每一项都保证可连接。目录本身是项目级全局数据（不随用户变），
// 但路由和本组其它端点一样按用户鉴权（requireUserID）。上游解析失败时回 502，让前端
// 显示「加载失败」状态，而不是误显示一张空的目录。
func (h *Handler) ListComposioToolkits(w http.ResponseWriter, r *http.Request) {
	if h.Composio == nil || !h.composioMCPAppsEnabled(r.Context()) {
		writeFeatureDisabled(w, "composio_not_configured", "composio integration not configured")
		return
	}
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	toolkits, err := h.Composio.ListToolkits(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to list composio toolkits")
		return
	}
	out := make([]ComposioToolkitResponse, 0, len(toolkits))
	for _, tk := range toolkits {
		out = append(out, ComposioToolkitResponse{
			Slug:     tk.Slug,
			Name:     tk.Name,
			Logo:     tk.LogoURL,
			Category: tk.Category,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// DeleteComposioConnection (DELETE /api/integrations/composio/connections/{id})
// 断开一条属于当前调用者的连接。服务层幂等；连接不属于该用户时按 404 处理。
func (h *Handler) DeleteComposioConnection(w http.ResponseWriter, r *http.Request) {
	if h.Composio == nil || !h.composioMCPAppsEnabled(r.Context()) {
		writeFeatureDisabled(w, "composio_not_configured", "composio integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	connUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "connection id")
	if !ok {
		return
	}
	if err := h.Composio.Disconnect(r.Context(), userUUID, connUUID); err != nil {
		if errors.Is(err, composio.ErrConnectionNotFound) {
			writeError(w, http.StatusNotFound, "composio connection not found")
			return
		}
		writeError(w, http.StatusBadGateway, "failed to disconnect composio connection")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
