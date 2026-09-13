// 本文件是 Telegram 集成面向 Web UI 的管理端点：列出某 workspace 的安装、BYO 安装
// （把 @BotFather 给的 bot token 粘进来）、撤销安装，以及把 Telegram 用户与 Multica
// 用户绑定的赎回端点。整体镜像 slack.go：channel_installation 行上的加密 bot token
// 只在服务端解密使用、绝不回显，路由层限定 admin。
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/integrations/telegram"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TelegramInstallationResponse 是一条 Telegram 安装行的线上结构。config 里的加密
// bot token 刻意缺席——它是服务器内部件。WS lease 各列是运行时状态而非 API 表面。
type TelegramInstallationResponse struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	AgentID         string `json:"agent_id"`
	BotID           string `json:"bot_id"`
	BotUsername     string `json:"bot_username"`
	InstallerUserID string `json:"installer_user_id"`
	Status          string `json:"status"`
	InstalledAt     string `json:"installed_at"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// telegramInstallationToResponse 把安装行（解码 config 的公开字段）转成对外结构。
func telegramInstallationToResponse(row db.ChannelInstallation) TelegramInstallationResponse {
	info := telegram.DecodePublicConfig(row.Config)
	return TelegramInstallationResponse{
		ID:              uuidToString(row.ID),
		WorkspaceID:     uuidToString(row.WorkspaceID),
		AgentID:         uuidToString(row.AgentID),
		BotID:           info.BotID,
		BotUsername:     info.BotUsername,
		InstallerUserID: uuidToString(row.InstallerUserID),
		Status:          row.Status,
		InstalledAt:     row.InstalledAt.Time.UTC().Format(time.RFC3339),
		CreatedAt:       row.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt:       row.UpdatedAt.Time.UTC().Format(time.RFC3339),
	}
}

// ListTelegramInstallations（GET /api/workspaces/{id}/telegram/installations）对成员
// 可见，让非管理员的 Integrations 标签页也能渲染。响应旗标镜像 Slack：configured =
// 静态加密密钥已设置；configured 时 install_supported 恒为 true（粘 token 不需要托管
// 凭据）。
func (h *Handler) ListTelegramInstallations(w http.ResponseWriter, r *http.Request) {
	if h.TelegramInstall == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"installations":     []TelegramInstallationResponse{},
			"configured":        false,
			"install_supported": false,
		})
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	rows, err := h.TelegramInstall.ListByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list telegram installations")
		return
	}
	out := make([]TelegramInstallationResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, telegramInstallationToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installations":     out,
		"configured":        true,
		"install_supported": true,
	})
}

// RegisterTelegramRequest 是安装请求体：用户从 @BotFather 粘出的 bot token。
type RegisterTelegramRequest struct {
	BotToken string `json:"bot_token"`
}

// RegisterTelegramBot（POST /api/workspaces/{id}/telegram/install?agent_id=…）为某个
// agent 安装用户提供的 Telegram bot。路由层限定 admin。镜像 RegisterSlackBYO。
func (h *Handler) RegisterTelegramBot(w http.ResponseWriter, r *http.Request) {
	if h.TelegramInstall == nil {
		writeError(w, http.StatusServiceUnavailable, "telegram integration not enabled")
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
	// 边界归属预检，让错误的 agent_id 得到清晰的 404。
	if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "agent not found in this workspace")
		return
	}
	initiatorUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	var body RegisterTelegramRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	row, err := h.TelegramInstall.Register(r.Context(), telegram.RegisterParams{
		WorkspaceID: wsUUID,
		AgentID:     agentUUID,
		InitiatorID: initiatorUUID,
		BotToken:    body.BotToken,
	})
	if err != nil {
		switch {
		case errors.Is(err, telegram.ErrInvalidBotToken):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, telegram.ErrCredentialsRejected):
			writeError(w, http.StatusBadRequest, "Telegram rejected this bot token — generate a current token in @BotFather and try again")
		case errors.Is(err, telegram.ErrCredentialsUnverifiable):
			writeError(w, http.StatusServiceUnavailable, "could not reach Telegram to verify this bot — check the server network or proxy and try again; the token was not saved")
		case errors.Is(err, telegram.ErrBotOwnedBySameWorkspace):
			writeError(w, http.StatusConflict, "this Telegram bot is already connected to another agent in this workspace — disconnect it there first, then connect it here")
		case errors.Is(err, telegram.ErrBotOwnedByArchivedAgent):
			writeError(w, http.StatusConflict, "this Telegram bot is connected to an archived agent in this workspace — restore that agent, or disconnect its bot, before connecting it here")
		case errors.Is(err, telegram.ErrBotOwnedByAnotherWorkspace):
			writeError(w, http.StatusConflict, "this Telegram bot is already connected to a different Multica workspace — disconnect it there before connecting it here")
		case errors.Is(err, telegram.ErrWebhookConfigured):
			writeError(w, http.StatusBadRequest, "this Telegram bot has a webhook configured — remove the webhook before connecting it with long polling")
		default:
			writeError(w, http.StatusInternalServerError, "could not save this Telegram bot — something went wrong on the server; the token was not saved")
		}
		return
	}
	// 广播让所有打开的客户端失效 installations 查询并显示新机器人——与 Slack 的
	// 安装语义一致。
	h.publish(protocol.EventTelegramInstallationCreated, uuidToString(row.WorkspaceID), "user", userID, map[string]any{
		"id": uuidToString(row.ID),
	})
	writeJSON(w, http.StatusOK, telegramInstallationToResponse(row))
}

// RevokeTelegramInstallation（DELETE /api/workspaces/{id}/telegram/installations/{installationId}）
// 把状态翻成 'revoked'。路由层限定 admin。行保留供审计、聊天历史留在 Multica；重新
// 安装（再次粘 bot token）会把状态翻回 'active'。
func (h *Handler) RevokeTelegramInstallation(w http.ResponseWriter, r *http.Request) {
	if h.TelegramInstall == nil {
		writeError(w, http.StatusServiceUnavailable, "telegram integration not configured")
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
	// workspace 范围内的查找，防止一个 workspace 靠猜 UUID 撤销另一家的安装。
	if _, err := h.TelegramInstall.GetInWorkspace(r.Context(), instUUID, wsUUID); err != nil {
		if errors.Is(err, telegram.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "telegram installation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load installation")
		return
	}
	if err := h.TelegramInstall.Revoke(r.Context(), instUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke installation")
		return
	}
	h.publish(protocol.EventTelegramInstallationRevoked, uuidToString(wsUUID), "user", userID, map[string]any{
		"id": uuidToString(instUUID),
	})
	w.WriteHeader(http.StatusNoContent)
}

// RedeemTelegramBindingTokenRequest 携带机器人「关联你的账号」提示里返回的原始 token。
type RedeemTelegramBindingTokenRequest struct {
	Token string `json:"token"`
}

// RedeemTelegramBindingTokenResponse 回显绑定的标识，让前端无需再拉一次即可确认。
type RedeemTelegramBindingTokenResponse struct {
	WorkspaceID    string `json:"workspace_id"`
	InstallationID string `json:"installation_id"`
	TelegramUserID string `json:"telegram_user_id"`
}

// RedeemTelegramBindingToken（POST /api/telegram/binding/redeem）把 token 携带的
// Telegram 用户 id 绑定到当前登录的 Multica 用户。赎回者身份来自会话而不是 token。
// 状态码镜像 Slack 的赎回 handler。
func (h *Handler) RedeemTelegramBindingToken(w http.ResponseWriter, r *http.Request) {
	if h.TelegramBindingTokens == nil {
		writeError(w, http.StatusServiceUnavailable, "telegram integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req RedeemTelegramBindingTokenRequest
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
	redeemed, err := h.TelegramBindingTokens.RedeemAndBind(r.Context(), req.Token, userUUID)
	if err != nil {
		switch {
		case errors.Is(err, telegram.ErrBindingTokenInvalid):
			writeError(w, http.StatusGone, "binding token invalid or expired")
		case errors.Is(err, telegram.ErrBindingAlreadyAssigned):
			writeError(w, http.StatusConflict, "this Telegram account is already bound to a different Multica user")
		case errors.Is(err, telegram.ErrBindingNotWorkspaceMember):
			writeError(w, http.StatusForbidden, "binding refused (are you a workspace member?)")
		default:
			writeError(w, http.StatusInternalServerError, "failed to redeem token")
		}
		return
	}
	writeJSON(w, http.StatusOK, RedeemTelegramBindingTokenResponse{
		WorkspaceID:    uuidToString(redeemed.WorkspaceID),
		InstallationID: uuidToString(redeemed.InstallationID),
		TelegramUserID: redeemed.TelegramUserID,
	})
}
