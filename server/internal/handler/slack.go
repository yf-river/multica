// 本文件是 Slack 集成面向 Web UI 的管理端点：列出某 workspace 的安装、BYO 安装
// （bring-your-own-app）、撤销安装，以及把 Slack 用户与 Multica 用户绑定的赎回端点。
//
// 与托管 OAuth 安装路径不同，BYO 安装只需要「静态加密密钥」已配置（SlackInstall 非
// nil），不需要托管 OAuth 客户端凭据——它正是给没有托管应用的部署用的路径：多个
// agent 可以在同一个 Slack workspace 里各有一个机器人身份。channel_installation 行
// 上的 bot token 只在服务端加密保存，出站发送方才解密，绝不通过 API 回显。
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/integrations/slack"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// SlackInstallationResponse 是一条 Slack 安装行的线上结构。config 里的加密 bot token
// 刻意缺席——它是服务器内部件（只有出站发送方会解密）。WS lease 各列是运行时状态而非
// API 表面，所以也一并省略。
type SlackInstallationResponse struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	AgentID         string `json:"agent_id"`
	TeamID          string `json:"team_id"`
	BotUserID       string `json:"bot_user_id"`
	InstallerUserID string `json:"installer_user_id"`
	Status          string `json:"status"`
	InstalledAt     string `json:"installed_at"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// slackInstallationToResponse 把安装行（解码 config 里的公开字段）转成对外结构。
func slackInstallationToResponse(row db.ChannelInstallation) SlackInstallationResponse {
	info := slack.DecodePublicConfig(row.Config)
	return SlackInstallationResponse{
		ID:              uuidToString(row.ID),
		WorkspaceID:     uuidToString(row.WorkspaceID),
		AgentID:         uuidToString(row.AgentID),
		TeamID:          info.TeamID,
		BotUserID:       info.BotUserID,
		InstallerUserID: uuidToString(row.InstallerUserID),
		Status:          row.Status,
		InstalledAt:     row.InstalledAt.Time.UTC().Format(time.RFC3339),
		CreatedAt:       row.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt:       row.UpdatedAt.Time.UTC().Format(time.RFC3339),
	}
}

// ListSlackInstallations（GET /api/workspaces/{id}/slack/installations）对成员可见，
// 让非管理员的 Integrations 标签页也能渲染。响应旗标镜像 Lark：
//   - configured：静态加密密钥已设置（SlackInstall != nil）。
//   - install_supported：给管理 UI 用；只要 configured 就为 true，因为 BYO 安装只需
//     静态加密密钥（无需托管 OAuth 凭据）。
func (h *Handler) ListSlackInstallations(w http.ResponseWriter, r *http.Request) {
	if h.SlackInstall == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"installations":     []SlackInstallationResponse{},
			"configured":        false,
			"install_supported": false,
		})
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	rows, err := h.SlackInstall.ListByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list slack installations")
		return
	}
	out := make([]SlackInstallationResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, slackInstallationToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installations":     out,
		"configured":        true,
		"install_supported": true,
	})
}

// RegisterSlackBYORequest 是 BYO 安装请求体：用户在自家 Slack app 里粘出的两个 token。
type RegisterSlackBYORequest struct {
	BotToken string `json:"bot_token"`
	AppToken string `json:"app_token"`
}

// RegisterSlackBYO（POST /api/workspaces/{id}/slack/install/byo?agent_id=…）为某个
// agent 安装用户自备（"bring your own"）的 Slack app——于是多个 agent 可以在同一个
// Slack workspace 里各有一个机器人身份。路由层限定 admin。与托管 OAuth 路径不同，
// 这里只需要静态加密密钥（SlackInstall != nil），不需要托管 OAuth 客户端凭据——BYO
// 正是给没有托管应用的部署用的路径。
func (h *Handler) RegisterSlackBYO(w http.ResponseWriter, r *http.Request) {
	if h.SlackInstall == nil {
		writeError(w, http.StatusServiceUnavailable, "slack integration not enabled")
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
	// 在边界做归属预检，让错误的 agent_id 得到清晰的 404。
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
	var body RegisterSlackBYORequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	row, err := h.SlackInstall.RegisterBYO(r.Context(), slack.RegisterBYOParams{
		WorkspaceID: wsUUID,
		AgentID:     agentUUID,
		InitiatorID: initiatorUUID,
		BotToken:    body.BotToken,
		AppToken:    body.AppToken,
	})
	if err != nil {
		switch {
		case errors.Is(err, slack.ErrInvalidBotToken), errors.Is(err, slack.ErrInvalidAppToken), errors.Is(err, slack.ErrTokenAppMismatch):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, slack.ErrTeamOwnedBySameWorkspace):
			writeError(w, http.StatusConflict, "this Slack app is already connected to another agent in this workspace — disconnect it there first, then connect it here")
		case errors.Is(err, slack.ErrTeamOwnedByArchivedAgent):
			writeError(w, http.StatusConflict, "this Slack app is connected to an archived agent in this workspace — restore that agent, or disconnect its bot, before connecting it here")
		case errors.Is(err, slack.ErrTeamOwnedByAnotherWorkspace):
			writeError(w, http.StatusConflict, "this Slack app is already connected to a different Multica workspace — disconnect it there before connecting it here")
		default:
			// 这里最主要的非哨兵失败是 auth.test 拒绝了粘贴的 bot token（用户输入
			// 错误），所以引导用户复查 token，而不是抛一个不透明的 500。
			writeError(w, http.StatusBadRequest, "could not verify the Slack tokens — check the bot token and app-level token, that the app is installed to your workspace, and that it has the users:read scope")
		}
		return
	}
	// 广播让所有打开的客户端（Settings、Agent Integrations、其它标签页）失效自己的
	// installations 查询、显示新机器人——与撤销事件及 Lark 的安装语义一致。安装者自己的
	// 标签页也会本地失效，但其它客户端依赖这条事件。
	h.publishSlackInstallationCreated(row, userID)
	writeJSON(w, http.StatusOK, slackInstallationToResponse(row))
}

// publishSlackInstallationCreated 为新接入的机器人发 slack_installation:created。
// 实时层把它扇出到 workspace；web 应用监听 slack_installation:* 以失效 installations
// 查询。
func (h *Handler) publishSlackInstallationCreated(row db.ChannelInstallation, actorID string) {
	h.publish(protocol.EventSlackInstallationCreated, uuidToString(row.WorkspaceID), "user", actorID, map[string]any{
		"id": uuidToString(row.ID),
	})
}

// RevokeSlackInstallation（DELETE /api/workspaces/{id}/slack/installations/{installationId}）
// 把状态翻成 'revoked'。路由层限定 admin。行保留供审计；重新安装（再次粘贴 app 的
// token）会把状态翻回 'active'。
func (h *Handler) RevokeSlackInstallation(w http.ResponseWriter, r *http.Request) {
	if h.SlackInstall == nil {
		writeError(w, http.StatusServiceUnavailable, "slack integration not configured")
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
	if _, err := h.SlackInstall.GetInWorkspace(r.Context(), instUUID, wsUUID); err != nil {
		if errors.Is(err, slack.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "slack installation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load installation")
		return
	}
	if err := h.SlackInstall.Revoke(r.Context(), instUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke installation")
		return
	}
	h.publish(protocol.EventSlackInstallationRevoked, uuidToString(wsUUID), "user", userID, map[string]any{
		"id": uuidToString(instUUID),
	})
	w.WriteHeader(http.StatusNoContent)
}

// RedeemSlackBindingTokenRequest 携带用户从机器人「关联你的账号」提示里点过来的
// 原始 token。
type RedeemSlackBindingTokenRequest struct {
	Token string `json:"token"`
}

// RedeemSlackBindingTokenResponse 回显绑定的 workspace/installation/用户，让前端无需
// 再拉一次即可确认。
type RedeemSlackBindingTokenResponse struct {
	WorkspaceID    string `json:"workspace_id"`
	InstallationID string `json:"installation_id"`
	SlackUserID    string `json:"slack_user_id"`
}

// RedeemSlackBindingToken（POST /api/slack/binding/redeem）把 token 携带的 Slack 用户
// id 绑定到当前登录的 Multica 用户。赎回者的身份来自会话而不是 token，所以偷来的
// token 无法把一个 Slack id 绑到攻击者自己的账号。失败映射到不同状态码：
//   - 410 Gone:     token 未知 / 已消费 / 已过期
//   - 409 Conflict: 该 Slack id 已绑定到别的用户
//   - 403 Forbidden: 赎回者不是 workspace 成员
func (h *Handler) RedeemSlackBindingToken(w http.ResponseWriter, r *http.Request) {
	if h.SlackBindingTokens == nil {
		writeError(w, http.StatusServiceUnavailable, "slack integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req RedeemSlackBindingTokenRequest
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

	redeemed, err := h.SlackBindingTokens.RedeemAndBind(r.Context(), req.Token, userUUID)
	if err != nil {
		switch {
		case errors.Is(err, slack.ErrBindingTokenInvalid):
			writeError(w, http.StatusGone, "binding token invalid or expired")
		case errors.Is(err, slack.ErrBindingAlreadyAssigned):
			writeError(w, http.StatusConflict, "this Slack account is already bound to a different Multica user")
		case errors.Is(err, slack.ErrBindingNotWorkspaceMember):
			writeError(w, http.StatusForbidden, "binding refused (are you a workspace member?)")
		default:
			writeError(w, http.StatusInternalServerError, "failed to redeem token")
		}
		return
	}
	writeJSON(w, http.StatusOK, RedeemSlackBindingTokenResponse{
		WorkspaceID:    uuidToString(redeemed.WorkspaceID),
		InstallationID: uuidToString(redeemed.InstallationID),
		SlackUserID:    redeemed.SlackUserID,
	})
}
