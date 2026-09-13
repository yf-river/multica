// 职责定位：Lark（飞书/国际版 Lark）接入的 HTTP 处理器：安装清单（普通成员可读）、吊销
// 安装、bot 的「绑定账号」token 兑换，以及 device-flow 安装会话（begin/status 两步）。真正的
// Lark API 交互、AppSecret 加密与注册会话状态在 internal/integrations/lark，这里只做 HTTP
// 编排与授权。
//
// 授权口径：安装类操作按 agent 走 canManageAgent（绑定 agent 的 owner 或 workspace
// owner/admin）；对已孤儿化（agent 被硬删）的安装回退成仅 owner/admin 可清理（MUL-4213）。
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/integrations/lark"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// LarkInstallationResponse 是安装记录的接口形态。`app_secret_encrypted` 刻意不出现——加密块
// 是服务端内部机密，没有任何产品理由暴露它（唯一需要明文的消费方是 WS hub，它在服务端调
// InstallationService.DecryptAppSecret）。WS 租约列也省略——那是运行时状态，不是 API 表面。
type LarkInstallationResponse struct {
	ID              string  `json:"id"`
	WorkspaceID     string  `json:"workspace_id"`
	AgentID         string  `json:"agent_id"`
	AppID           string  `json:"app_id"`
	TenantKey       *string `json:"tenant_key,omitempty"`
	BotOpenID       string  `json:"bot_open_id"`
	InstallerUserID string  `json:"installer_user_id"`
	Status          string  `json:"status"`
	// Region 是这个安装所在的 Lark 云："feishu"（国内）或 "lark"（海外）。UI 用它渲染徽标，
	// 以及拼出正确的「在 Lark 后台管理」的 dev-console 域名。
	Region      string `json:"region"`
	InstalledAt string `json:"installed_at"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// larkInstallationToResponse 把 lark.Installation 转成接口结构（TenantKey 可选）。
func larkInstallationToResponse(row lark.Installation) LarkInstallationResponse {
	resp := LarkInstallationResponse{
		ID:              uuidToString(row.ID),
		WorkspaceID:     uuidToString(row.WorkspaceID),
		AgentID:         uuidToString(row.AgentID),
		AppID:           row.AppID,
		BotOpenID:       row.BotOpenID,
		InstallerUserID: uuidToString(row.InstallerUserID),
		Status:          row.Status,
		Region:          row.Region,
		InstalledAt:     row.InstalledAt.Time.UTC().Format(time.RFC3339),
		CreatedAt:       row.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt:       row.UpdatedAt.Time.UTC().Format(time.RFC3339),
	}
	if row.TenantKey.Valid {
		tk := row.TenantKey.String
		resp.TenantKey = &tk
	}
	return resp
}

// ListLarkInstallations (GET /api/workspaces/{id}/lark/installations)
// 普通成员可见——Integrations tab 不该对非管理员渲染成空白。与 GitHub 列表不同，这里不摘掉
// 任何字段：因为没有任何「API 表面列」同时充当管理句柄——吊销走 UUID id，而没有管理路由的
// 授权，这个 id 毫无意义，所以暴露它是无害的。
//
// 响应字段：
//   - configured：静态加密密钥已配置（`LarkInstallations != nil`）。为 false 时任何安装流程都
//     不可能成功，UI 直接隐藏该 tab。
//   - install_supported：device-flow 安装路径已端到端打通：存在 RegistrationService（部署配了
//     MULTICA_LARK_SECRET_KEY）且 APIClient.IsConfigured 为 true（真实的 Lark HTTP 客户端就位
//     ——stub 完成不了 poll 之后的 GetBotInfo）。为 false 时，agent 详情的「绑定」按钮保持隐藏，
//     Settings tab 展示「即将上线」提示；已装好的 bot 仍会展示、仍可管理。
func (h *Handler) ListLarkInstallations(w http.ResponseWriter, r *http.Request) {
	if h.LarkInstallations == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"installations":     []LarkInstallationResponse{},
			"configured":        false,
			"install_supported": false,
		})
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	rows, err := h.LarkInstallations.ListByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list lark installations")
		return
	}
	out := make([]LarkInstallationResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, larkInstallationToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installations":     out,
		"configured":        true,
		"install_supported": h.LarkRegistration != nil && h.LarkAPIClient != nil && h.LarkAPIClient.IsConfigured(),
	})
}

// RevokeLarkInstallation (DELETE /api/workspaces/{id}/lark/installations/{installationId})
// 把状态翻成 'revoked'，让 WS hub 在下一轮清扫时断开连接。行本身保留做审计；经 device-flow
// 重新安装会把状态原子地翻回 'active'。
//
// 路由层校验 workspace 成员身份；per-agent 授权（canManageAgent：绑定 agent 的 owner 或
// workspace owner/admin）在这里执行，与 BeginLarkInstall 对称——所以 agent 的 owner 能解绑
// 自己绑的 bot（MUL-4213）。绑定 agent 被硬删后安装即「孤儿」（active 连接查询会跳过它，
// 断开它就是文档里的清理路径）；此时吊销回退成仅 workspace owner/admin——清理入口保持可用，
// 又不把孤儿行权利交给普通成员。
func (h *Handler) RevokeLarkInstallation(w http.ResponseWriter, r *http.Request) {
	if h.LarkInstallations == nil {
		writeError(w, http.StatusServiceUnavailable, "lark integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	instUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "installationId"), "installation id")
	if !ok {
		return
	}
	// 按 workspace 范围查询：防止一个 workspace 靠猜 UUID 吊销另一家的安装。
	inst, err := h.LarkInstallations.GetInWorkspace(r.Context(), instUUID, wsUUID)
	if err != nil {
		if errors.Is(err, lark.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "lark installation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load installation")
		return
	}
	// 对绑定 agent 做授权。一般场景是它的 owner 或 workspace owner/admin 可吊销
	// （canManageAgent 自己会写 403/404）。agent 已被硬删则是孤儿安装——此时回退成仅
	// owner/admin 清理，而不是给「断开连接」入口回 404（对照 ListByWorkspace 与按孤儿过滤的
	// active 列表）。没有外键/级联：缺失的 agent 在应用层处理。
	agent, agentErr := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          inst.AgentID,
		WorkspaceID: wsUUID,
	})
	if agentErr != nil {
		if _, ok := h.requireWorkspaceRole(w, r, uuidToString(wsUUID), "lark installation not found", "owner", "admin"); !ok {
			return
		}
	} else if !h.canManageAgent(w, r, agent) {
		return
	}
	if err := h.LarkInstallations.Revoke(r.Context(), instUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke installation")
		return
	}
	h.publish(protocol.EventLarkInstallationRevoked, uuidToString(wsUUID), "user", userID, map[string]any{
		"id": uuidToString(instUUID),
	})
	w.WriteHeader(http.StatusNoContent)
}

// RedeemLarkBindingTokenRequest 携带用户从 Bot 的「需要绑定」回复卡片里点过来的原始 token。
type RedeemLarkBindingTokenRequest struct {
	Token string `json:"token"`
}

// RedeemLarkBindingTokenResponse 是兑换成功后的响应形态。回显 workspace/安装/open_id，让
// 前端不用再发一次请求就能渲染「你现在经 <agent> 绑到了 <workspace>」。
type RedeemLarkBindingTokenResponse struct {
	WorkspaceID    string `json:"workspace_id"`
	InstallationID string `json:"installation_id"`
	LarkOpenID     string `json:"lark_open_id"`
}

// RedeemLarkBindingToken (POST /api/lark/binding/redeem) 是唯一「由用户动作写 lark_user_binding
// 行」的路径。兑换者身份取自登录会话而不是 token——所以偷来的 token 没法把某个 Lark open_id
// 绑到攻击者的站内账号上。token 只证明「这个 open_id 请求过绑定」；与登录用户结合才产生
// (open_id ↔ user) 映射。
//
// 消费 + 绑定在同一个数据库事务里完成（见 lark.BindingTokenService.RedeemAndBind）。三种失败
// 各映射一个状态码，前端不用另发探测就能渲染对应文案：
//   - 410 Gone:       token 未知 / 已消费 / 已过期
//   - 409 Conflict:   open_id 已绑到别的站内用户
//   - 403 Forbidden:  兑换者不是 workspace 成员
func (h *Handler) RedeemLarkBindingToken(w http.ResponseWriter, r *http.Request) {
	if h.LarkBindingTokens == nil {
		writeError(w, http.StatusServiceUnavailable, "lark integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req RedeemLarkBindingTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	redeemed, err := h.LarkBindingTokens.RedeemAndBind(r.Context(), req.Token, userUUID)
	if err != nil {
		switch {
		case errors.Is(err, lark.ErrBindingTokenInvalid):
			writeError(w, http.StatusGone, "binding token invalid or expired")
		case errors.Is(err, lark.ErrBindingAlreadyAssigned):
			writeError(w, http.StatusConflict, "this Lark account is already bound to a different Multica user")
		case errors.Is(err, lark.ErrBindingNotWorkspaceMember):
			writeError(w, http.StatusForbidden, "binding refused (are you a workspace member?)")
		default:
			writeError(w, http.StatusInternalServerError, "failed to redeem token")
		}
		return
	}

	writeJSON(w, http.StatusOK, RedeemLarkBindingTokenResponse{
		WorkspaceID:    uuidToString(redeemed.WorkspaceID),
		InstallationID: uuidToString(redeemed.InstallationID),
		LarkOpenID:     string(redeemed.LarkOpenID),
	})
}

// BeginLarkInstallResponse 是扫码弹窗消费的 payload。前端把 `qr_code_url` 渲染成二维码图
// （并作为「点开链接」的兜底），并按给到的节奏轮询
// /lark/install/{session_id}/status。
type BeginLarkInstallResponse struct {
	SessionID           string `json:"session_id"`
	QRCodeURL           string `json:"qr_code_url"`
	ExpiresInSeconds    int    `json:"expires_in_seconds"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

// BeginLarkInstall (POST /api/workspaces/{id}/lark/install/begin)
// 向 Lark 开一个新的 device-flow 注册会话。路由只要求 workspace 成员身份；这个 handler 再按
// agent 走 canManageAgent 授权（agent 的 owner 或 workspace owner/admin）——所以 agent 的
// owner 不必是 workspace admin 也能给自己 agent 绑 bot（MUL-4213）。agent_id 查询参数决定新
// Bot 绑到哪个站内 agent；agent 必须属于本 workspace（RegistrationService 会再查一遍做纵深
// 防御）。
//
// 集成没接通时返回 503（无静态密钥、无 HTTP client、无 RegistrationService）；那种情况下 UI
// 隐藏绑定按钮，正常流程走不到这里。
func (h *Handler) BeginLarkInstall(w http.ResponseWriter, r *http.Request) {
	if h.LarkRegistration == nil {
		writeError(w, http.StatusServiceUnavailable, "lark install not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentIDStr := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if agentIDStr == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, agentIDStr, "agent_id")
	if !ok {
		return
	}
	// region 是用户明确选择要绑定的云——"feishu"（国内，accounts.feishu.cn）或 "lark"
	// （海外，accounts.larksuite.com）。前端现在暴露两个 CTA（「绑定飞书」/「绑定 Lark」），
	// 好让二维码一开始就对着正确的云渲染，而不是依赖 poll 中途按租户品牌从飞书优先自动切。
	// 接受 "feishu"、"lark" 与空串（兼容拆分 CTA 之前的旧调用方——服务里的 RegionOrDefault
	// 会把空串映射成飞书）；其它值一律 400——因为服务会把未知值静默归一成飞书，那会掩盖前端
	// 打错 region 字导致用户落到错误云上的回归。
	regionParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("region")))
	switch regionParam {
	case "", "feishu", "lark":
		// ok —— 空串在下游默认成 feishu。
	default:
		writeError(w, http.StatusBadRequest, "region must be 'feishu' or 'lark'")
		return
	}
	// 在 HTTP 边界先做归属预检：agent_id 写错时在这里回 404（而不是服务内部重查时抛出的一团
	// 不透明错误）。
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found in this workspace")
		return
	}
	// 对发起人做目标 agent 授权：agent 的 owner 或 workspace owner/admin 才能绑。
	// canManageAgent 自己会写 403/404，所以既不是 owner 也不是管理员的成员在（已降级到成员
	// 级）路由之前就被挡在这里。
	if !h.canManageAgent(w, r, agent) {
		return
	}
	initiatorUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	res, err := h.LarkRegistration.BeginInstall(r.Context(), lark.BeginInstallParams{
		WorkspaceID: wsUUID,
		AgentID:     agentUUID,
		InitiatorID: initiatorUUID,
		Region:      lark.Region(regionParam),
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to start install: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, BeginLarkInstallResponse{
		SessionID:           res.SessionID,
		QRCodeURL:           res.QRCodeURL,
		ExpiresInSeconds:    res.ExpiresInSeconds,
		PollIntervalSeconds: res.PollIntervalSeconds,
	})
}

// LarkInstallStatusResponse 是轮询的 payload。`status` 取值 "pending" | "success" |
// "error"；成功时填 `installation_id`，失败时 `error_reason` 是稳定错误码
// （见 lark.RegistrationReason*）。
type LarkInstallStatusResponse struct {
	Status         string `json:"status"`
	InstallationID string `json:"installation_id,omitempty"`
	ErrorReason    string `json:"error_reason,omitempty"`
	ErrorMessage   string `json:"error_message,omitempty"`
}

// GetLarkInstallStatus (GET /api/workspaces/{id}/lark/install/{sessionId}/status)
// 返回一次进行中安装会话的当前状态。路由只要求 workspace 成员；这个 handler 把读取范围限定
// 到「会话发起人」或 workspace owner/admin——所以作为 agent owner 发起安装的成员能轮询自己的
// 会话，不会暴露给无关成员（MUL-4213）。未知/跨 workspace/已被 GC 的会话——以及调用方无权读
// 的会话——一律回 404，前端把 404 当成「会话丢了，请重新开始」。
//
// 成功时这个 handler 不会清理会话——前端可能在弹窗关闭后再轮询一次以确认（在进程内 GC 清扫
// 淘汰条目之前）；读取是幂等的。
func (h *Handler) GetLarkInstallStatus(w http.ResponseWriter, r *http.Request) {
	if h.LarkRegistration == nil {
		writeError(w, http.StatusServiceUnavailable, "lark install not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(chi.URLParam(r, "sessionId"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session id is required")
		return
	}
	state, err := h.LarkRegistration.GetSession(wsUUID, sessionID)
	if err != nil {
		if errors.Is(err, lark.ErrRegistrationSessionNotFound) {
			writeError(w, http.StatusNotFound, "install session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load install session")
		return
	}
	// 只有发起人或 workspace owner/admin 能读会话。会话 id 只发还给发起人，所以把「其他人」
	// 一律当「会话丢失」（404，不泄露存在性）与上面跨 workspace 的情形是一致的。
	if uuidToString(state.InitiatorID) != userID {
		member, mErr := h.getWorkspaceMember(r.Context(), userID, uuidToString(wsUUID))
		if mErr != nil || !roleAllowed(member.Role, "owner", "admin") {
			writeError(w, http.StatusNotFound, "install session not found")
			return
		}
	}
	resp := LarkInstallStatusResponse{
		Status:       string(state.Status),
		ErrorReason:  state.ErrorReason,
		ErrorMessage: state.ErrorMessage,
	}
	if state.InstallationID.Valid {
		resp.InstallationID = uuidToString(state.InstallationID)
		// lark_installation:created 事件由 RegistrationService 在「行提交」时发布
		// （见 registration_service.go finishSuccess），而不是这里——这样连接徽标的刷新
		// 不依赖是否有浏览器把状态端点轮询到成功。
	}
	writeJSON(w, http.StatusOK, resp)
}
