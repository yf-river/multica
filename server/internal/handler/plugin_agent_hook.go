// 本文件：Agent 触发插件的 daemon 侧端点——Agent 调工具 → daemon 本地 MCP server 转成
// 本请求 → 服务端代为发起带签名的 hook 调用。设计说明见下。
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// 这是 Agent 触发的 daemon 端。
//
// Agent 调一个工具；daemon 的本地 MCP server 把它转成这个请求；服务端代为执行带签名的
// hook 调用。daemon 从不持有签名密钥、也从不直接连插件的端点——正因如此，限流、熔断、
// `net:` 目标校验和调用记录都集中在这条代码路径上，而不是散在两处。
//
// 认证方式与 /api/daemon 其余接口一致：以 daemon 身份登录、并限定到一条已认领的任务——
// 工作区来自任务行，所以一台 daemon 无法触达它并未运行任务的那个工作区里的安装。

type invokeAgentHookRequest struct {
	InstallationID string          `json:"installation_id"`
	HookKey        string          `json:"hook_key"`
	Input          json.RawMessage `json:"input,omitempty"`
}

// InvokeAgentPluginHook 处理 POST /api/daemon/tasks/{id}/plugin-hooks——以 daemon 身份、
// 限定在指定任务上，代为发起一次插件 hook 调用。
func (h *Handler) InvokeAgentPluginHook(w http.ResponseWriter, r *http.Request) {
	if !h.requirePluginsV1(w, r) {
		return
	}
	task, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	var req invokeAgentHookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.InstallationID == "" || req.HookKey == "" {
		writeError(w, http.StatusBadRequest, "installation_id and hook_key are required")
		return
	}

	// 安装必须属于任务所在工作区。没有这层校验，一台持有一条合法任务的 daemon 就能报出
	// 部署里任意安装 id，让服务端替它去调用。
	installation, err := h.PluginService.InstallationForWorkspace(r.Context(), parseUUID(workspaceID), req.InstallationID)
	if err != nil {
		writePluginError(w, err, "failed to load the Plugin")
		return
	}

	result, err := h.PluginService.InvokeAgentHook(
		r.Context(), uuidToString(installation.ID), req.HookKey, task.AgentID, req.Input)
	if err != nil {
		// 刻意用 200 带错误体。
		//
		// daemon 会把这次返回变成工具 ERROR，而工具错误是 Agent 读得懂、能绕过去的。
		// 传输层失败则会被 daemon 看成 broker 本身坏了——于是一个插件端点不可达就会弄挂
		// 整个任务，这正是结果验收标准 3 禁止的。
		writeJSON(w, http.StatusOK, map[string]any{
			"status": result.Status,
			"error":  result.Error,
		})
		return
	}
	writeJSON(w, http.StatusOK, result)
}
