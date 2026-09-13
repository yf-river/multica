// 本文件：mcp 传输方式 hook 的管理端点——admin 对插件 MCP server 暴露的工具做「发现 +
// 审批」，以及 daemon 在连接时按需解析插件 MCP 的凭据。
package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

// mcp 传输 hook 的管理员审批。
//
// 发现是只读的、不采纳任何东西。审批才是授权，并且按 schema 摘要钉住工具——因为 MCP
// server 与 http 端点不同，它在运行时自己决定工具清单，管理员读完 manifest 之后它可能变。

type pluginMCPToolResponse struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	SchemaDigest string `json:"schema_digest"`
	Approved     bool   `json:"approved"`
	// Drifted 标记「已批准但 schema 已与批准时不再一致」的工具。要亮出来而不是悄悄重新批准：
	// 管理员批准的是一份特定形状，变了就该重新决策。
	Drifted bool `json:"drifted,omitempty"`
}

// ListPluginMCPTools 处理 GET /api/workspaces/{id}/plugins/{installationId}/mcp/{hookKey}/tools——
// 发现该 MCP hook 当前暴露的工具，并标注每个工具是否已批准/是否漂移。
func (h *Handler) ListPluginMCPTools(w http.ResponseWriter, r *http.Request) {
	installation, ok := h.pluginInstallationFromURL(w, r)
	if !ok {
		return
	}
	hookKey := chi.URLParam(r, "hookKey")

	discovered, err := h.PluginService.DiscoverMCPHookTools(r.Context(), installation, hookKey)
	if err != nil {
		writePluginError(w, err, "failed to reach the Plugin's MCP server")
		return
	}
	approved := h.PluginService.ApprovedMCPTools(installation, hookKey)

	items := make([]pluginMCPToolResponse, 0, len(discovered))
	for _, tool := range discovered {
		pinned, isApproved := approved[tool.Name]
		items = append(items, pluginMCPToolResponse{
			Name:         tool.Name,
			Description:  tool.Description,
			SchemaDigest: tool.SchemaDigest,
			Approved:     isApproved,
			Drifted:      isApproved && pinned.SchemaDigest != tool.SchemaDigest,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": items})
}

type approvePluginMCPToolsRequest struct {
	// Tools 是完整的已批准集合，不是增量。管理员移除一个工具与新增一个工具请求形状相同，
	// 所以不可能出现「你以为吊销了、结果它还在」。
	Tools []string `json:"tools"`
}

// ApprovePluginMCPTools 处理 PUT /api/workspaces/{id}/plugins/{installationId}/mcp/{hookKey}/tools——
// 用完整集合覆盖批准（记操作者 user id），并返回更新后的安装记录。
func (h *Handler) ApprovePluginMCPTools(w http.ResponseWriter, r *http.Request) {
	installation, ok := h.pluginInstallationFromURL(w, r)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	parsedUserID, ok := parseUUIDOrBadRequest(w, userID, "user_id")
	if !ok {
		return
	}

	var req approvePluginMCPToolsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updated, err := h.PluginService.ApproveMCPHookTools(
		r.Context(), installation, chi.URLParam(r, "hookKey"), req.Tools, parsedUserID)
	if err != nil {
		writePluginError(w, err, "failed to approve the MCP tools")
		return
	}
	payload, err := h.pluginInstallationPayload(r.Context(), updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load the Plugin")
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// splitPluginContributionID 解析回 AgentMCPConnections 拼出的
// "plugin:<installation uuid>:<hook key>" 形式的贡献 id。
//
// 前缀是强制的、不是可容忍的：它正是「插件贡献」与「工作区自己的 Remote MCP 连接」的
// 区分点，两者从完全不同的地方解析凭据。前缀之后的两半也必须齐全——只有裸的安装 id 会
// 解析成空 hook key，空的安装 id 会变成工作区查找对 "" 的处理，于是持有一条贡献 id 的
// daemon 靠裁剪字符串就能指到另一条。
func splitPluginContributionID(contribution string) (string, string, bool) {
	trimmed, hasPrefix := strings.CutPrefix(contribution, remotemcp.PluginContributionPrefix)
	if !hasPrefix {
		return "", "", false
	}
	installationID, hookKey, found := strings.Cut(trimmed, ":")
	if !found || installationID == "" || hookKey == "" {
		return "", "", false
	}
	return installationID, hookKey, true
}

// ResolvePluginMCPCredential 处理 GET /api/daemon/tasks/{id}/plugin-mcp/{contributionId}/credential。
//
// daemon 的 broker 在连接时才来要凭据，而不是在认领 payload 里收到——这样机密从不落进
// 任务记录。与其它 /api/daemon 路由一样：以 daemon 身份认证、限定到一条已认领的任务。
func (h *Handler) ResolvePluginMCPCredential(w http.ResponseWriter, r *http.Request) {
	if !h.requirePluginsV1(w, r) {
		return
	}
	_, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	installationID, hookKey, ok := splitPluginContributionID(chi.URLParam(r, "contributionId"))
	if !ok {
		writeError(w, http.StatusBadRequest, "malformed contribution id")
		return
	}
	installation, err := h.PluginService.InstallationForWorkspace(r.Context(), parseUUID(workspaceID), installationID)
	if err != nil {
		writePluginError(w, err, "failed to load the Plugin")
		return
	}

	header, credential, err := h.PluginService.MCPHookCredential(r.Context(), installation, hookKey)
	if err != nil {
		writePluginError(w, err, "failed to resolve the credential")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"credential_header": header,
		"credential":        credential,
	})
}
