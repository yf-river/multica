// 本文件实现「自托管 Git 提供方」连接的 Web 管理端点（仅自托管部署启用，托管云
// 关闭）：列出、连接、断开与轮换 webhook 密钥。
//
// 一次连接 = workspace 到某个自托管实例（Forgejo/Gitea/GitLab 等）的接入：管理员
// 提供实例 URL + 访问令牌，服务器先拿它对真实实例验证（取回账号名），再铸造 webhook
// 密钥，两者都加密落库。连接的 webhook 入站处理见 vcs_webhook.go。
//
// 安全约定：secret 从不通过 API 回显；webhook 密钥只在 connect / rotate 时明文返回
// 一次（VCSConnectResponse），让用户粘进提供方配置里。isVCSAvailable 是产品开关
// （自托管才有），isVCSConfigured 表示加密密钥是否已配置，两者相互独立。
package handler

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/vcs"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ── 响应结构 ──────────────────────────────────────────────────────────────

// VCSConnectionResponse 是一条已存 Git 提供方连接的 JSON 结构。secret 从不包含；
// webhook 密钥仅在创建时刻经 VCSConnectResponse 返回一次。
type VCSConnectionResponse struct {
	ID           string `json:"id"`
	WorkspaceID  string `json:"workspace_id"`
	Provider     string `json:"provider"`
	InstanceURL  string `json:"instance_url"`
	AccountLogin string `json:"account_login"`
	WebhookURL   string `json:"webhook_url"`
	WebhookPath  string `json:"webhook_path"`
	CreatedAt    string `json:"created_at"`
}

// VCSConnectResponse 内嵌已存连接，外加用户必须粘进提供方配置的一次性明文 webhook
// 密钥（Forgejo/Gitea 用 HMAC secret，GitLab 用 X-Gitlab-Token 值）。此后不可再取。
type VCSConnectResponse struct {
	VCSConnectionResponse
	WebhookSecret string `json:"webhook_secret"`
}

// vcsWebhookPathPrefix 是所有 VCS webhook 入站地址的前缀，path 尾部是 connection id。
const vcsWebhookPathPrefix = "/api/webhooks/vcs/"

// isVCSAvailable 报告本部署是否提供自托管 Git 提供方集成。它是产品边界（仅自托管），
// 与 isVCSConfigured（加密密钥是否已设置）无关：托管云把它关掉，于是 connect/rotate/
// webhook 都拒绝、UI 隐藏整个区块，而不是暴露一条「缺密钥」的运维提示。
func (h *Handler) isVCSAvailable() bool { return h.cfg.VCSIntegrationEnabled }

// isVCSConfigured 判断 VCS secret 加密盒是否已就绪（需要 MULTICA_VCS_SECRET_KEY）。
func (h *Handler) isVCSConfigured() bool { return h.VCSSecretBox != nil }

// vcsWebhookPath 返回某连接的 webhook 相对路径。
func (h *Handler) vcsWebhookPath(connID string) string { return vcsWebhookPathPrefix + connID }

// vcsWebhookURL 用配置的公网基址拼出某连接的完整 webhook URL；基址为空时返回空串。
func (h *Handler) vcsWebhookURL(connID string) string {
	base := strings.TrimRight(h.cfg.PublicURL, "/")
	if base == "" {
		return ""
	}
	return base + h.vcsWebhookPath(connID)
}

// vcsConnectionToResponse 把连接行转成对外结构（含拼出的 webhook URL/路径）。
func (h *Handler) vcsConnectionToResponse(c db.VcsConnection) VCSConnectionResponse {
	id := uuidToString(c.ID)
	return VCSConnectionResponse{
		ID:           id,
		WorkspaceID:  uuidToString(c.WorkspaceID),
		Provider:     c.Provider,
		InstanceURL:  c.InstanceUrl,
		AccountLogin: c.AccountLogin,
		WebhookURL:   h.vcsWebhookURL(id),
		WebhookPath:  h.vcsWebhookPath(id),
		CreatedAt:    timestampToString(c.CreatedAt),
	}
}

// sealVCSSecret 用 secret 盒加密明文，返回 base64 密文供落库。
func (h *Handler) sealVCSSecret(plaintext string) (string, error) {
	sealed, err := h.VCSSecretBox.Seal([]byte(plaintext))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// openVCSSecret 解开 base64 密文并解密；空串直接返回空明文（未设置）。
func (h *Handler) openVCSSecret(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	ciphertext, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	plaintext, err := h.VCSSecretBox.Open(ciphertext)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// ── 端点 ────────────────────────────────────────────────────────────────────

// ListVCSConnections（GET /workspaces/{id}/vcs/connections）对成员可见；connect/
// disconnect 在路由层限定 admin。不返回任何 secret。
func (h *Handler) ListVCSConnections(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	member, _ := middleware.MemberFromContext(r.Context())
	canManage := roleAllowed(member.Role, "owner", "admin")

	// 集成关闭的部署（托管云）只回 available=false，UI 据此隐藏整个区块，而不是
	// 显示一条仅运维可见的「缺密钥」提示。这种部署反正不可能存在连接（connect 会被
	// 拒），所以连查询都省了。
	if !h.isVCSAvailable() {
		writeJSON(w, http.StatusOK, map[string]any{
			"connections": []VCSConnectionResponse{},
			"available":   false,
			"configured":  false,
			"can_manage":  false,
		})
		return
	}

	rows, err := h.Queries.ListVCSConnectionsByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list connections")
		return
	}
	out := make([]VCSConnectionResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, h.vcsConnectionToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connections": out,
		"available":   true,
		"configured":  h.isVCSConfigured(),
		"can_manage":  canManage,
	})
}

// connectVCSRequest 是连接请求体：提供方类型、实例 URL 与访问令牌。
type connectVCSRequest struct {
	Provider    string `json:"provider"`
	InstanceURL string `json:"instance_url"`
	AccessToken string `json:"access_token"`
}

// ConnectVCS（POST /workspaces/{id}/vcs/connections）用实例 URL + token 对该提供方
// 的真实实例做校验，铸造 webhook 密钥、把两个 secret 加密落库，返回连接与一次性
// webhook 密钥。重复连接同一实例即轮换（upsert）。
func (h *Handler) ConnectVCS(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if !h.isVCSAvailable() {
		writeError(w, http.StatusNotFound, "vcs integration is not available on this deployment")
		return
	}
	if !h.isVCSConfigured() {
		writeFeatureDisabled(w, "vcs_not_configured", "vcs integration not configured (MULTICA_VCS_SECRET_KEY unset)")
		return
	}

	var req connectVCSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	provider, ok := vcs.For(req.Provider)
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported provider")
		return
	}
	instanceURL := vcs.NormalizeInstanceURL(req.InstanceURL)
	token := strings.TrimSpace(req.AccessToken)
	if instanceURL == "" || token == "" {
		writeError(w, http.StatusBadRequest, "instance_url and access_token are required")
		return
	}
	parsed, err := url.Parse(instanceURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		writeError(w, http.StatusBadRequest, "instance_url must be an absolute http(s) URL")
		return
	}

	account, err := provider.ValidateToken(r.Context(), instanceURL, token)
	if err != nil {
		if errors.Is(err, vcs.ErrUnauthorized) {
			writeError(w, http.StatusBadRequest, "the provider rejected the access token")
			return
		}
		writeError(w, http.StatusBadGateway, "could not reach the provider instance")
		return
	}

	webhookSecret, err := newVCSWebhookSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mint webhook secret")
		return
	}
	tokenEnc, err := h.sealVCSSecret(token)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encrypt token")
		return
	}
	secretEnc, err := h.sealVCSSecret(webhookSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encrypt webhook secret")
		return
	}

	var connectedBy pgtype.UUID
	if member, ok := middleware.MemberFromContext(r.Context()); ok {
		connectedBy = member.UserID
	}

	conn, err := h.Queries.UpsertVCSConnection(r.Context(), db.UpsertVCSConnectionParams{
		WorkspaceID:            wsUUID,
		Provider:               string(provider.Kind()),
		InstanceUrl:            instanceURL,
		AccountLogin:           account.Login,
		AccessTokenEncrypted:   tokenEnc,
		WebhookSecretEncrypted: secretEnc,
		ConnectedByID:          connectedBy,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save connection")
		return
	}

	resp := h.vcsConnectionToResponse(conn)
	h.publish(protocol.EventVCSConnectionCreated, workspaceID, "system", "", map[string]any{"id": resp.ID})
	writeJSON(w, http.StatusOK, VCSConnectResponse{
		VCSConnectionResponse: resp,
		WebhookSecret:         webhookSecret,
	})
}

// DeleteVCSConnection（DELETE /workspaces/{id}/vcs/connections/{connectionId}）
// 删除一条连接（同一 workspace 内校验）。
func (h *Handler) DeleteVCSConnection(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "connectionId"), "connection id")
	if !ok {
		return
	}
	if err := h.Queries.DeleteVCSConnection(r.Context(), db.DeleteVCSConnectionParams{
		ID:          idUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove connection")
		return
	}
	h.publish(protocol.EventVCSConnectionDeleted, workspaceID, "system", "", map[string]any{
		"id": chi.URLParam(r, "connectionId"),
	})
	w.WriteHeader(http.StatusNoContent)
}

// RotateVCSConnectionWebhook（POST /workspaces/{id}/vcs/connections/{connectionId}/rotate-webhook）
// 为已有连接生成新的 webhook 密钥、加密落库，并返回连接与一次性明文密钥。
func (h *Handler) RotateVCSConnectionWebhook(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	connID := chi.URLParam(r, "connectionId")
	connUUID, ok := parseUUIDOrBadRequest(w, connID, "connection id")
	if !ok {
		return
	}
	if !h.isVCSAvailable() {
		writeError(w, http.StatusNotFound, "vcs integration is not available on this deployment")
		return
	}
	if !h.isVCSConfigured() {
		writeFeatureDisabled(w, "vcs_not_configured", "vcs integration not configured (MULTICA_VCS_SECRET_KEY unset)")
		return
	}

	conn, err := h.Queries.GetVCSConnectionByID(r.Context(), connUUID)
	if err != nil || uuidToString(conn.WorkspaceID) != uuidToString(wsUUID) {
		writeError(w, http.StatusNotFound, "vcs connection not found")
		return
	}

	webhookSecret, err := newVCSWebhookSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mint webhook secret")
		return
	}
	secretEnc, err := h.sealVCSSecret(webhookSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encrypt webhook secret")
		return
	}

	updated, err := h.Queries.RotateVCSConnectionWebhookSecret(r.Context(), db.RotateVCSConnectionWebhookSecretParams{
		ID:                     connUUID,
		WorkspaceID:            wsUUID,
		WebhookSecretEncrypted: secretEnc,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to rotate webhook secret")
		return
	}

	resp := h.vcsConnectionToResponse(updated)
	h.publish(protocol.EventVCSConnectionCreated, workspaceID, "system", "", map[string]any{"id": resp.ID})
	writeJSON(w, http.StatusOK, VCSConnectResponse{
		VCSConnectionResponse: resp,
		WebhookSecret:         webhookSecret,
	})
}

// newVCSWebhookSecret 返回 32 字节随机数的 hex 编码（64 字符）。
func newVCSWebhookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
