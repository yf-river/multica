// 本文件是 WeCom 智能机器人（"智能机器人" / aibot）集成中面向 Web UI 的管理端点，
// 是 slack.go 的平行实现：通过 JSON 暴露 list / install / revoke，让设置页和各 agent
// 的 Integrations 标签页驱动 wecom.InstallationService.Upsert / Revoke。
//
// 这里没有回调（callback）面——智能机器人是客户端主动发起的 WebSocket 长连接，
// 不需要对外公开的回调 URL。另有一个把 WeCom 用户与 Multica 用户绑定的赎回端点
// （RedeemWecomBindingToken）。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/wecom"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// wecomBodyLimit 限制两个 WeCom JSON 端点可读取的 body 大小。两个请求体都只有
// 几个短字段，而 encoding/json 在 Decode 返回前会把字符串值整体物化——不设上限的话，
// 一个已认证调用者 POST 几 GB 的 token 字符串，每次请求就会让 API 进程占用那么多
// RSS。
const wecomBodyLimit = 16 * 1024

// WecomBindingRedeemer 是赎回端点所驱动的 wecom.BindingTokenService 的一个切片。
// 生产值是 *wecom.BindingTokenService；定义成接口是为了让测试能在没有真实
// channel_binding_token 表的情况下给 body 上限测试打桩。
type WecomBindingRedeemer interface {
	RedeemAndBind(ctx context.Context, raw string, multicaUserID pgtype.UUID) (wecom.RedeemedBindingToken, error)
}

// WecomInstallationResponse 是一条 wecom 安装行的线上结构。secret 绝不外泄——它一直
// 封存在行上。暴露 BotID 是因为运维需要看到绑定的是哪个机器人。
type WecomInstallationResponse struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	AgentID         string `json:"agent_id"`
	BotID           string `json:"bot_id"`
	InstallerUserID string `json:"installer_user_id"`
	Status          string `json:"status"`
}

// wecomInstallationToResponse 把安装行转成对外 JSON 结构。
func wecomInstallationToResponse(inst wecom.Installation) WecomInstallationResponse {
	return WecomInstallationResponse{
		ID:              uuidToString(inst.ID),
		WorkspaceID:     uuidToString(inst.WorkspaceID),
		AgentID:         uuidToString(inst.AgentID),
		BotID:           inst.BotID,
		InstallerUserID: uuidToString(inst.InstallerUserID),
		Status:          string(inst.Status),
	}
}

// wecomIntegrationConfigured 报告本部署是否接入了 wecom 集成。两个界面（列表 + 注册）
// 都会在未接入时短路：列表降级成空响应，注册返回 503。
func (h *Handler) wecomIntegrationConfigured() bool {
	return h.WecomStore != nil && h.WecomCredentials != nil && h.ChannelRouter != nil
}

// ListWecomInstallations（GET /api/workspaces/{id}/wecom/installations）对成员可见，
// 让非管理员的 Integrations 标签页也能渲染。响应信封与 Slack / Lark 一致。
func (h *Handler) ListWecomInstallations(w http.ResponseWriter, r *http.Request) {
	if !h.wecomIntegrationConfigured() {
		writeJSON(w, http.StatusOK, map[string]any{
			"installations":     []WecomInstallationResponse{},
			"configured":        false,
			"install_supported": false,
		})
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	svc := h.wecomInstallService()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, "wecom integration not enabled")
		return
	}
	rows, err := svc.ListByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list wecom installations")
		return
	}
	out := make([]WecomInstallationResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, wecomInstallationToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installations":     out,
		"configured":        true,
		"install_supported": true,
	})
}

// RegisterWecomBYORequest 是管理员从 Web UI 的 BYO 对话框提交的 body。机器人的稳定
// 标识（BotID）与长连接 secret 从 WeCom 管理后台复制而来；secret 直接穿过
// secretbox 写入，明文在本文件栈上存活不超过 wecom.InstallationService.Upsert 调用。
type RegisterWecomBYORequest struct {
	BotID  string `json:"bot_id"`
	Secret string `json:"secret"`

	// BotName 是机器人在聊天里显示的名字。可选，只用于一件事：识别机器人在群里被
	// @ 到的自身 mention（WeCom 以纯文本形式下发）。同一机器人在重复安装时省略它，
	// 会保留行上已有的名字。
	BotName string `json:"bot_name,omitempty"`
}

// RegisterWecomBYO（POST /api/workspaces/{id}/wecom/install/byo?agent_id=…）为某个
// agent 安装用户自备（"bring your own"）的 wecom 智能机器人。路由层限定 admin。
func (h *Handler) RegisterWecomBYO(w http.ResponseWriter, r *http.Request) {
	if !h.wecomIntegrationConfigured() {
		writeError(w, http.StatusServiceUnavailable, "wecom integration not enabled")
		return
	}
	svc := h.wecomInstallService()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, "wecom integration not enabled")
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
	var body RegisterWecomBYORequest
	r.Body = http.MaxBytesReader(w, r.Body, wecomBodyLimit)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	inst, err := svc.Upsert(r.Context(), wecom.InstallationParams{
		WorkspaceID:     wsUUID,
		AgentID:         agentUUID,
		InstallerUserID: initiatorUUID,
		BotID:           strings.TrimSpace(body.BotID),
		Secret:          strings.TrimSpace(body.Secret),
		BotDisplayName:  strings.TrimSpace(body.BotName),
	})
	if err != nil {
		writeWecomInstallError(w, err, wsUUID, agentUUID)
		return
	}
	h.publish(protocol.EventWecomInstallationCreated, uuidToString(inst.WorkspaceID), "user", userID, map[string]any{
		"id": uuidToString(inst.ID),
	})
	writeJSON(w, http.StatusOK, wecomInstallationToResponse(inst))
}

// writeWecomInstallError 是「安装失败」的完整响应矩阵，收在一处，以便把它当作矩阵
// 阅读和测试，而不是去 handler 深处的 switch 里推断。
//
// 它划分的依据是「这次失败该谁去处理」：
//
//	机器人已被占用（同 ws / 已归档 agent / 别的 ws） → 409，去那边先断开
//	缺少必填字段                                      → 400，管理员的输入问题
//	WeCom 拒绝这对凭据                                → 400，唯一可以把责任归到凭据的分支
//	WeCom 连不上、无法核验                             → 503，说明凭据并未被改动
//	其它任何情况（DB、加密密钥、bug）                  → 500，记日志，别怪管理员
//
// 一个机器人只有一条活的长连接，所以 (wecom, bot_id) 槽位只有一个主人。Upsert 在
// (workspace_id, agent_id, channel_type) 上冲突——这意味着把一个已连接的机器人连到
// 第二个 agent 时不会命中 ON CONFLICT，而是撞上 idx_channel_installation_type_appid。
// 每个哨兵都配上「告诉管理员机器人实际在哪」的句子；以前这里直接返回 err.Error()，
// 会把原始的 "duplicate key value violates unique constraint" 弹给管理员——同时把
// 其它数据库错误也一字不差地转给 API 调用方。
//
// 每个分支除了句子还带稳定 code，让设置页可以用管理员自己的语言来表述，而不是朝他
// 们弹英文。句子保留为不认识 code 的客户端的兜底。code 是已发布的契约：一个 code
// 只对应一种结局并一直保持该含义，因为不同结局管理员要采取的动作不同。
func writeWecomInstallError(w http.ResponseWriter, err error, wsUUID, agentUUID pgtype.UUID) {
	switch {
	case errors.Is(err, wecom.ErrBotOwnedBySameWorkspace):
		writeErrorCode(w, http.StatusConflict, "wecom_bot_owned_by_same_workspace",
			"this bot is already connected to another agent in this workspace — disconnect it there first, then connect it here")
	case errors.Is(err, wecom.ErrBotOwnedByArchivedAgent):
		writeErrorCode(w, http.StatusConflict, "wecom_bot_owned_by_archived_agent",
			"this bot is connected to an archived agent in this workspace — restore that agent, or disconnect its bot, before connecting it here")
	case errors.Is(err, wecom.ErrBotOwnedByAnotherWorkspace):
		writeErrorCode(w, http.StatusConflict, "wecom_bot_owned_by_another_workspace",
			"this bot is already connected to a different Multica workspace — disconnect it there before connecting it here")
	case errors.Is(err, wecom.ErrInvalidInstallationParams):
		// 管理员漏了某样东西。他的输入，他来修。这个 code 只表达这件事、不掺别的——
		// 下面两种凭据结局各有各的 code，因为「字段漏填」「WeCom 说不行」「没能问成
		// WeCom」对管理员是三种不同的下一步动作，只靠一个 code 分不出来。
		writeErrorCode(w, http.StatusBadRequest, "wecom_install_rejected",
			"could not connect the WeCom bot — check the Bot ID and secret from the WeCom admin console, and that the bot is a smart bot with the long connection enabled")
	case errors.Is(err, wecom.ErrCredentialsRejected):
		// WeCom 亲自拒绝了这对凭据。这是唯一有权把责任归到凭据的分支，也是管理员
		// 唯一能靠回管理后台去改凭据解决的分支。
		writeErrorCode(w, http.StatusBadRequest, "wecom_credentials_rejected",
			"WeCom rejected this Bot ID and secret — check both on the WeCom admin console, and that the bot is a smart bot with the long connection enabled")
	case errors.Is(err, wecom.ErrCredentialsUnverifiable):
		// 部署环境连不上 WeCom。把它报成坏凭据，会让管理员去轮换一个其实没问题的
		// secret——而轮换后的 WeCom secret 无法恢复。503：输入没有问题，只是检查
		// 做不了。
		slog.Warn("wecom install could not verify the bot",
			"error", err, "workspace_id", uuidToString(wsUUID), "agent_id", uuidToString(agentUUID))
		writeErrorCode(w, http.StatusServiceUnavailable, "wecom_credentials_unverifiable",
			"could not reach WeCom to verify this bot — the credentials were not changed; try again in a moment")
	default:
		// 我们的问题：数据库、加密密钥、一个 bug。这个分支以前回 400 并把责任归到
		// 凭据，把管理员送进 WeCom 后台去轮换一个本来没问题的 secret——而轮换后的
		// secret 无法恢复，于是三十秒的 Postgres 故障切换就让他们失去一个正常工作的
		// 机器人。无论这里发生了什么，都不是他们的 Bot ID 的问题。
		slog.Error("wecom install failed",
			"error", err, "workspace_id", uuidToString(wsUUID), "agent_id", uuidToString(agentUUID))
		writeErrorCode(w, http.StatusInternalServerError, "wecom_install_failed",
			"could not save this bot — something went wrong on our side. Your credentials were not changed; please try again, and contact support if it keeps failing")
	}
}

// RevokeWecomInstallation（DELETE /api/workspaces/{id}/wecom/installations/{installationId}）
// 把状态翻成 'revoked'。路由层限定 admin。保留行——这样经 Upsert 重新安装时能原子地
// 翻回 'active'。
func (h *Handler) RevokeWecomInstallation(w http.ResponseWriter, r *http.Request) {
	if !h.wecomIntegrationConfigured() {
		writeError(w, http.StatusServiceUnavailable, "wecom integration not enabled")
		return
	}
	svc := h.wecomInstallService()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, "wecom integration not enabled")
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
	if _, err := svc.GetInWorkspace(r.Context(), instUUID, wsUUID); err != nil {
		if errors.Is(err, wecom.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "wecom installation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load installation")
		return
	}
	if err := svc.Revoke(r.Context(), instUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke installation")
		return
	}
	h.publish(protocol.EventWecomInstallationRevoked, uuidToString(wsUUID), "user", userID, map[string]any{
		"id": uuidToString(instUUID),
	})
	w.WriteHeader(http.StatusNoContent)
}

// wecomInstallService 按需从 handler 装配好的零件构建 InstallationService。它很便宜
// （不过是 *db.Queries 加 CredentialsResolver 背后共享 secretbox.Box 的结构体包装），
// 用这种惰性模式可以避免只为 Web UI 这条路径给 Handler 结构再加第四个 wecom 字段。
//
// SecretboxCredentialsResolver 是 wecom.CredentialsResolver 唯一的线上实现，所以类型
// 断言是安全的。测试若注入假 resolver，断言会落空，两个端点于是都报 503——这正是
// 「没接加密密钥」想要的失败行为。
func (h *Handler) wecomInstallService() *wecom.InstallationService {
	res, ok := h.WecomCredentials.(*wecom.SecretboxCredentialsResolver)
	if !ok || res == nil || res.Box == nil {
		return nil
	}
	// 探针（probe）是让 Upsert 内部的回收（reclaim）能安全地针对一行可能并非该调用方
	// 所有的行执行的关键。h.WecomCredentialProbe 是测试接缝；生产环境保持 nil，
	// NewInstallationService 会补上真正的握手。没有任何装配会把这个检查漏掉——nil 探针
	// 在那里属于构造错误，不是放行模式。
	var opts []wecom.InstallationOption
	if h.WecomCredentialProbe != nil {
		opts = append(opts, wecom.WithCredentialProbe(h.WecomCredentialProbe))
	}
	svc, err := wecom.NewInstallationService(h.Queries, h.TxStarter, res.Box, opts...)
	if err != nil {
		return nil
	}
	return svc
}

// RedeemWecomBindingTokenRequest 携带用户从机器人「关联你的 Multica 账号」提示里
// 点过来的原始 token。
type RedeemWecomBindingTokenRequest struct {
	Token string `json:"token"`
}

// RedeemWecomBindingTokenResponse 回显绑定的 workspace / installation / wecom 用户，
// 让前端无需再拉一次即可确认。
type RedeemWecomBindingTokenResponse struct {
	WorkspaceID    string `json:"workspace_id"`
	InstallationID string `json:"installation_id"`
	WecomUserID    string `json:"wecom_user_id"`
}

// RedeemWecomBindingToken（POST /api/wecom/binding/redeem）把 token 携带的 WeCom aibot
// userid 绑定到当前登录的 Multica 用户。赎回者的身份来自会话而不是 token，所以偷来的
// token 无法把一个 WeCom id 绑到攻击者自己的账号上。失败映射到不同状态码：
//
//   - 410 Gone:     token 未知 / 已消费 / 已过期
//   - 409 Conflict: 该 WeCom id 已绑定到别的用户
//   - 403 Forbidden: 赎回者不是 workspace 成员
func (h *Handler) RedeemWecomBindingToken(w http.ResponseWriter, r *http.Request) {
	if h.WecomBindingTokens == nil {
		writeError(w, http.StatusServiceUnavailable, "wecom integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req RedeemWecomBindingTokenRequest
	r.Body = http.MaxBytesReader(w, r.Body, wecomBodyLimit)
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
	redeemed, err := h.WecomBindingTokens.RedeemAndBind(r.Context(), req.Token, userUUID)
	if err != nil {
		switch {
		case errors.Is(err, wecom.ErrBindingTokenInvalid):
			writeError(w, http.StatusGone, "binding token invalid or expired")
		case errors.Is(err, wecom.ErrBindingAlreadyAssigned):
			writeError(w, http.StatusConflict, "this WeCom user is already bound to a different Multica user")
		case errors.Is(err, wecom.ErrBindingNotWorkspaceMember):
			writeError(w, http.StatusForbidden, "binding refused (are you a workspace member?)")
		default:
			writeError(w, http.StatusInternalServerError, "failed to redeem token")
		}
		return
	}
	writeJSON(w, http.StatusOK, RedeemWecomBindingTokenResponse{
		WorkspaceID:    uuidToString(redeemed.WorkspaceID),
		InstallationID: uuidToString(redeemed.InstallationID),
		WecomUserID:    redeemed.WecomUserID,
	})
}
