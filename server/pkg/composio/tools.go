package composio

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// ExecuteToolRequest 是 POST /tools/execute/{tool_slug} 的请求体。
//
// Spec: https://docs.composio.dev/reference/api-reference/tools/postToolsExecuteByToolSlug
//
// ConnectedAccountID 和 (UserID + 该工具所属的 toolkit) 至少要给一个，
// Composio 才知道该用哪套凭证。SDK 不做前置强校验；缺参数时上游会
// 返回带明确提示的 422。
type ExecuteToolRequest struct {
	// Arguments 是传给工具的结构化入参，具体长什么样随工具而异。
	Arguments map[string]any `json:"arguments,omitempty"`

	// ConnectedAccountID 把执行锁定到某一个已连接的账号。
	ConnectedAccountID string `json:"connected_account_id,omitempty"`

	// UserID 用于调用方手头没有显式 `ca_` id 时，让 Composio 按用户解析出对应账号。
	UserID string `json:"user_id,omitempty"`

	// Version 固定工具定义的版本。传 "latest" 或带日期的版本号
	// （如 "20251027_00"）；上游默认取 "00000000_00"。
	//
	// Composio 文档提醒：手动执行工具必须显式指定版本；固定版本可以避免
	// Composio 升级 latest 时行为悄悄漂移。
	Version string `json:"version,omitempty"`

	// AllowTracing 是上游已废弃的调试追踪 flag。
	//
	// Deprecated: Composio v3.1 已把它标记为废弃——这里保留纯粹是为了
	// 兼容老调用方。等 Composio 彻底移除该字段后再删。
	AllowTracing bool `json:"allow_tracing,omitempty"`
}

// ExecuteToolResponse 是执行结果的类型化返回。上游返回的线格式随工具不同，
// 所以 [Data] 刻意做成通用 map；由调用方按工具文档里声明的输出结构自行解析。
type ExecuteToolResponse struct {
	Successful  bool           `json:"successful"`
	Data        map[string]any `json:"data,omitempty"`
	Error       string         `json:"error,omitempty"`
	LogID       string         `json:"log_id,omitempty"`
	SessionInfo map[string]any `json:"session_info,omitempty"`
}

// ExecuteTool 按 slug 调用一个 Composio 工具
// （slug 是大写下划线风格，例如 GITHUB_CREATE_ISSUE）。
//
// 这是「确定性的后端路径」——它不经过 MCP/会话编排，直接执行，
// 适合 autopilot 或内置技能这类固定流程。
func (c *Client) ExecuteTool(ctx context.Context, toolSlug string, req ExecuteToolRequest) (*ExecuteToolResponse, error) {
	if toolSlug == "" {
		return nil, errors.New("composio: ExecuteTool: toolSlug is required")
	}
	if req.ConnectedAccountID == "" && req.UserID == "" {
		return nil, errors.New("composio: ExecuteTool: either ConnectedAccountID or UserID must be set")
	}
	var out ExecuteToolResponse
	if err := c.do(c.newRequest(ctx).SetBody(req),
		http.MethodPost, "/tools/execute/"+url.PathEscape(toolSlug), &out); err != nil {
		return nil, err
	}
	return &out, nil
}
