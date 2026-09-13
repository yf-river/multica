// 本文件：由「人」发起的 hook 调用（surface 里的按钮 = `ui`；issue 操作菜单和命令面板里的
// 入口 = `manual`），外加安装级的 hook 管理与 token 轮换接口。
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

// 由人发起的 hook 调用：surface 里的按钮（`ui`）或 issue 操作菜单与命令面板的入口（`manual`）。
//
// 两者都阻塞——但只阻塞这一个请求。人在等他主动要的答案，立即返回等于什么都没给。绝不能
// 阻塞的是宿主自己要做的事，那是 `event` 触发器的事——它根本不走这里：它从事件总线派发到
// worker 池，没有任何请求在等它。

type invokePluginHookRequest struct {
	// Trigger 指出这是哪个声明过的调用点。客户端说明自己在用哪个，服务端核对 manifest
	// 是否声明过它——客户端无法凭空造一个触发器，去够一个从没开放过它的 hook。
	Trigger string          `json:"trigger"`
	IssueID string          `json:"issue_id,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
}

// InvokePluginHook 处理 POST /api/plugin-bridge/v1/hooks/{key}——由工作区成员在 UI/manual
// 调用点上发起一次 hook 调用，同步等待插件服务器返回。
func (h *Handler) InvokePluginHook(w http.ResponseWriter, r *http.Request) {
	// 没有自己的 scope：hook 是插件自身的能力，返回路上它能做什么，由安装的 scopes 在
	// 兑换回调 token 时约束。
	caller, actor, ok := h.pluginCaller(w, r, "")
	if !ok {
		return
	}
	// 从 UI 发起的 hook 一定是由「某人」发起的。插件自己的服务器来调这个接口等于让我们
	// 回叫它——那是个没有人在其中的循环，没有存在的理由。
	if !actor.requireMember(w, r) {
		return
	}

	var req invokePluginHookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch req.Trigger {
	case plugincontract.TriggerUI, plugincontract.TriggerManual:
	default:
		// event 由宿主自己派发、从不被请求触发；agent 走 MCP 到达。在浏览器里接受这两种
		// 触发器，等于让浏览器驱动本应挂着不同身份的调用点。
		writeError(w, http.StatusBadRequest, "trigger must be ui or manual")
		return
	}

	hook, err := service.FindHook(caller.Installation, chi.URLParam(r, "key"))
	if err != nil {
		writePluginError(w, err, "failed to load the hook")
		return
	}

	invocation := service.HookInvocation{
		Installation: caller.Installation,
		Hook:         hook,
		Trigger:      req.Trigger,
		// 按下按钮的那个人。handler 拿回调 token 做的写入都记成是他的，并打上 via_plugin_id。
		Actor: service.HookActor{Type: "member", ID: actor.Member.UserID},
		Input: rawOrNil(req.Input),
	}
	if req.IssueID != "" {
		issue, ok := h.pluginIssueForUser(w, r, caller, req.IssueID)
		if !ok {
			return
		}
		invocation.IssueID = issue.ID
	}

	result, err := h.PluginService.InvokeHook(r.Context(), invocation, 1)
	if err != nil {
		// 状态对正在看的人很重要：「管理员没授权」和「插件作者的服务挂了」是两个不同问题、
		// 由不同的人负责，笼统的 500 会让人分不清是哪一个。
		writePluginError(w, err, "the hook call failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// rawOrNil 把「没传 input」保持在请求体里彻底不出现，而不是发一个必须让 handler 特判的
// 字面 null。
func rawOrNil(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// --- 安装级的 hook 管理 ---

// pluginInvocationResponse 是单次 hook 调用记录的对外结构（不含请求/响应体，见下）。
type pluginInvocationResponse struct {
	ID         string  `json:"id"`
	HookKey    string  `json:"hook_key"`
	Trigger    string  `json:"trigger"`
	Status     string  `json:"status"`
	EventType  *string `json:"event_type,omitempty"`
	Attempt    int     `json:"attempt"`
	LatencyMs  int     `json:"latency_ms"`
	Error      *string `json:"error,omitempty"`
	DeliveryID *string `json:"delivery_id,omitempty"`
	PlannedAt  *string `json:"planned_at,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

// ListPluginInvocations 处理 GET /api/workspaces/{id}/plugins/{installationId}/invocations。
//
// 排障视图：这个 hook 为什么失败。请求/响应体从不入库，所以这里能答的是「发生了什么、
// 多频繁」，而不是「发送了什么内容」。
func (h *Handler) ListPluginInvocations(w http.ResponseWriter, r *http.Request) {
	installation, ok := h.pluginInstallationFromURL(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListPluginInvocations(r.Context(), db.ListPluginInvocationsParams{
		InstallationID: installation.ID,
		Limit:          100,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load the Plugin activity")
		return
	}
	items := make([]pluginInvocationResponse, 0, len(rows))
	for _, row := range rows {
		item := pluginInvocationResponse{
			ID:        uuidToString(row.ID),
			HookKey:   row.HookKey,
			Trigger:   row.Trigger,
			Status:    row.Status,
			Attempt:   int(row.Attempt),
			LatencyMs: int(row.LatencyMs),
			CreatedAt: row.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
		}
		if row.EventType.Valid {
			value := row.EventType.String
			item.EventType = &value
		}
		if row.Error.Valid {
			value := row.Error.String
			item.Error = &value
		}
		if row.DeliveryID.Valid {
			value := row.DeliveryID.String
			item.DeliveryID = &value
		}
		if row.PlannedAt.Valid {
			value := row.PlannedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00")
			item.PlannedAt = &value
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"invocations": items})
}

type pluginTokenResponse struct {
	// Token 只在铸造它的那次请求里返回一次。没有读取端点：弄丢的 admin 只能轮换而不是找回。
	Token string `json:"token"`
	// SigningSecret 是插件作者配置在自己服务器上、用来验证我们签名的密钥。它由部署密钥
	// 派生而非存储，所以对一次安装是稳定的，不需要数据库拷贝也能复现。
	SigningSecret string `json:"signing_secret,omitempty"`
}

// RotatePluginToken 处理 POST /api/workspaces/{id}/plugins/{installationId}/token——铸造
// 新的调用 token（旧 token 即刻失效）。
func (h *Handler) RotatePluginToken(w http.ResponseWriter, r *http.Request) {
	installation, ok := h.pluginInstallationFromURL(w, r)
	if !ok {
		return
	}
	credentials, err := h.PluginService.RotateInstallCredentials(r.Context(), installation.ID)
	if err != nil {
		writePluginError(w, err, "failed to issue the Plugin token")
		return
	}
	writeJSON(w, http.StatusOK, pluginTokenResponse{Token: credentials.Token, SigningSecret: credentials.SigningSecret})
}

// RevokePluginToken 处理 DELETE /api/workspaces/{id}/plugins/{installationId}/token——
// 吊销当前 token，成功返回 204。
func (h *Handler) RevokePluginToken(w http.ResponseWriter, r *http.Request) {
	installation, ok := h.pluginInstallationFromURL(w, r)
	if !ok {
		return
	}
	if err := h.PluginService.RevokeInstallToken(r.Context(), installation.ID); err != nil {
		writePluginError(w, err, "failed to revoke the Plugin token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
