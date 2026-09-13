package composio

import (
	"context"
	"errors"
	"net/http"
)

// --- 会话创建 ------------------------------------------------------------

// CreateSessionRequest 是 POST /tool_router/session 的请求体。
//
// 必填字段只有 [UserID]，其余全部可选，且直接对应 v3.1 线格式：
// https://docs.composio.dev/reference/api-reference/tool-router/postToolRouterSession
//
// 嵌套的 `toolkits`、`auth_configs`、`tools`、`tags`、`multi_account` 等字段
// 刻意保持松散的类型（map），因为它们带很多子属性、后续还预期会演进；
// 调用方仍可在上面包一层自己的强类型封装。
type CreateSessionRequest struct {
	UserID            string             `json:"user_id"`
	Toolkits          map[string]any     `json:"toolkits,omitempty"`
	AuthConfigs       map[string]any     `json:"auth_configs,omitempty"`
	ConnectedAccounts map[string]any     `json:"connected_accounts,omitempty"`
	ManageConnections *ManageConnections `json:"manage_connections,omitempty"`
	Tools             map[string]any     `json:"tools,omitempty"`
	Tags              any                `json:"tags,omitempty"`
	Workbench         map[string]any     `json:"workbench,omitempty"`
	MultiAccount      map[string]any     `json:"multi_account,omitempty"`
	Preload           map[string]any     `json:"preload,omitempty"`
	Search            map[string]any     `json:"search,omitempty"`
	Execute           map[string]any     `json:"execute,omitempty"`
	Experimental      map[string]any     `json:"experimental,omitempty"`
}

// ManageConnections 是 `manage_connections` 对象的强类型化——这是集成方
// 最常用到的那个字段。
type ManageConnections struct {
	Enable                   *bool  `json:"enable,omitempty"`
	CallbackURL              string `json:"callback_url,omitempty"`
	EnableWaitForConnections *bool  `json:"enable_wait_for_connections,omitempty"`
	EnableConnectionRemoval  *bool  `json:"enable_connection_removal,omitempty"`
}

// MCPDescriptor 是该会话 MCP 的 streamable HTTP 入口点描述。
type MCPDescriptor struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// CreateSessionResponse 是上游响应中 SDK 当前提供强类型的子集镜像；
// 后续加字段不会破坏已有调用方。
type CreateSessionResponse struct {
	SessionID       string           `json:"session_id"`
	MCP             MCPDescriptor    `json:"mcp"`
	ToolRouterTools []string         `json:"tool_router_tools,omitempty"`
	Config          map[string]any   `json:"config,omitempty"`
	ConfigVersion   int              `json:"config_version,omitempty"`
	Experimental    map[string]any   `json:"experimental,omitempty"`
	Warnings        []SessionWarning `json:"warnings,omitempty"`
}

// SessionWarning 是创建会话时发出的非致命警告。
type SessionWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CreateSession 为给定用户开一个新的 tool-router（也叫 MCP）会话。
// 返回的 [CreateSessionResponse.MCP.URL] 就是 MCP 兼容客户端要连的地址。
//
// 配套的鉴权头请用 [Client.MCPAuthHeaders] 获取——SDK 刻意不把密钥直接塞进
// 响应里，而是单独返回，防止调用方不小心把 API key 打进了日志。
func (c *Client) CreateSession(ctx context.Context, req CreateSessionRequest) (*CreateSessionResponse, error) {
	if req.UserID == "" {
		return nil, errors.New("composio: CreateSession: UserID is required")
	}
	var out CreateSessionResponse
	if err := c.do(c.newRequest(ctx).SetBody(req), http.MethodPost, "/tool_router/session", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MCPAuthHeaders 返回 MCP 客户端在连 [Client.CreateSession] 生成的会话 URL 时
// 必须带上的请求头。
//
// Composio 对 MCP streaming 的鉴权方式与 REST API 相同——都在项目级
// `x-api-key` 头里。单独拆出这个辅助函数，是为了让调用点在「凭证离开 SDK 边界」
// 这件事上看得清清楚楚，方便走自己的密钥脱敏管线（见 server/pkg/redact）。
func (c *Client) MCPAuthHeaders() map[string]string {
	return c.APIKeyHeader()
}
