// 职责定位：issue 数量上限（Cloud 订阅配额）相关的 HTTP 处理器：查询当前用量与在写路径上
// 把「已达上限」翻译成客户端可读的 402 响应。
package handler

import (
	"errors"
	"net/http"

	"github.com/multica-ai/multica/server/internal/entitlement"
	"github.com/multica-ai/multica/server/internal/service"
)

// IssueLimitUsageResponse 是 GET issue-limit-usage 的响应体：已用数量与上限。
type IssueLimitUsageResponse struct {
	Used  int64 `json:"used"`
	Limit int64 `json:"limit"`
}

// GetIssueLimitUsage 处理 GET /api/issues/limit-usage：返回「当前生效的 Cloud 限额」下的本地
// 用量。限额模式只来自 Cloud 的订阅摘要；本端点绝不从缓存或刷新原因推断「无限使用」。
// 未执行限额（不 enforce）时返回 204。
func (h *Handler) GetIssueLimitUsage(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	policy := service.ResolveIssueCountPolicy(r.Context(), h.Entitlements, workspaceID)
	if policy.Action != entitlement.ActionEnforce {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	used, err := service.CountIssueUsage(r.Context(), h.Queries, workspaceID, policy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load issue limit usage")
		return
	}
	writeJSON(w, http.StatusOK, IssueLimitUsageResponse{Used: used, Limit: policy.Limit})
}

// writeIssueLimitReached 把 service.IssueLimitReachedError 渲染成 402 + 稳定 code，并返回
// 是否由它处理了错误（供调用方短路通用错误分支）。
func writeIssueLimitReached(w http.ResponseWriter, err error) bool {
	var limitErr *service.IssueLimitReachedError
	if !errors.As(err, &limitErr) {
		return false
	}
	writeJSON(w, http.StatusPaymentRequired, map[string]any{
		"code":            "issue_limit_reached",
		"error":           "workspace has reached its issue limit",
		"limit":           limitErr.Limit,
		"policy_revision": limitErr.PolicyRevision,
	})
	return true
}
