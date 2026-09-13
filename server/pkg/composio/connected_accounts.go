package composio

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// --- 创建连接链接 --------------------------------------------------------

// CreateLinkRequest 是 POST /connected_accounts/link 的请求体。
//
// Spec: https://docs.composio.dev/reference/api-reference/connected-accounts/postConnectedAccountsLink
type CreateLinkRequest struct {
	// AuthConfigID 是你在 Composio 项目里注册的某个 auth config 的 `ac_…` id
	// （每个 toolkit / 每种 OAuth client 变体各一个）。
	AuthConfigID string `json:"auth_config_id"`

	// UserID 是你自己的用户标识——Composio 用它给「生成的已连接账号」划定归属。
	UserID string `json:"user_id"`

	// CallbackURL 是用户完成托管授权流程后被 Composio 重定向回的位置。
	// 可选；不填 Composio 有默认落地页。
	CallbackURL string `json:"callback_url,omitempty"`

	// Alias 是这条连接的给人看的名字。同一个用户给同一 toolkit 连多个账号时，
	// 用它区分很实用。可选。
	Alias string `json:"alias,omitempty"`

	// ConnectionData 允许调用方按 Composio 文档给连接字段预填默认值。
	// 保持自由结构，避免和不同授权方案各自的子结构耦合。
	ConnectionData map[string]any `json:"connection_data,omitempty"`
}

// CreateLinkResponse 是 POST /connected_accounts/link 返回的响应体。
type CreateLinkResponse struct {
	LinkToken          string `json:"link_token"`
	RedirectURL        string `json:"redirect_url"`
	ExpiresAt          string `json:"expires_at"`
	ConnectedAccountID string `json:"connected_account_id"`
}

// CreateLink 开启一次托管的 Composio Connect Link 授权会话。
// 返回的 RedirectURL 就是调用方该让用户去的地方
// （弹窗、整页跳转或 SFSafariViewController 都行）。
func (c *Client) CreateLink(ctx context.Context, req CreateLinkRequest) (*CreateLinkResponse, error) {
	if req.AuthConfigID == "" {
		return nil, errors.New("composio: CreateLink: AuthConfigID is required")
	}
	if req.UserID == "" {
		return nil, errors.New("composio: CreateLink: UserID is required")
	}
	var out CreateLinkResponse
	if err := c.do(c.newRequest(ctx).SetBody(req), http.MethodPost, "/connected_accounts/link", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- 列表 --------------------------------------------------------------

// ListConnectedAccountsRequest 汇总 GET /connected_accounts 支持的可选过滤参数。
// 零值不会拼进查询串。
//
// 按 Composio v3.1 规范，这些过滤器都是复数形式的数组参数：SDK 对每个切片元素
// 单独发一个同名查询项（`user_ids=u1&user_ids=u2`）。最常见的「查某个用户的
// 全部账号」就传单元素切片。
//
// Spec: https://docs.composio.dev/reference/api-reference/connected-accounts/getConnectedAccounts
type ListConnectedAccountsRequest struct {
	UserIDs             []string
	ToolkitSlugs        []string
	AuthConfigIDs       []string
	ConnectedAccountIDs []string
	Statuses            []string // ACTIVE、EXPIRED、INACTIVE……
	OrderBy             string   // "created_at"（默认）| "updated_at"
	OrderDirection      string   // "asc" | "desc"（默认）
	AccountType         string   // 实验性：PRIVATE | SHARED | ALL
	Limit               int      // 0 = 用上游默认值
	Cursor              string
}

// ConnectedAccount 是 Composio 返回结构的一个子集镜像。只把 MVP 真正消费的
// 字段类型化了；多余字段放在 Extra 里，调用方不用等 SDK 更新就能读到。
type ConnectedAccount struct {
	ID           string         `json:"id"`
	UserID       string         `json:"user_id"`
	AuthConfigID string         `json:"auth_config_id"`
	AuthConfig   AuthConfigRef  `json:"auth_config"`
	Toolkit      Toolkit        `json:"toolkit"`
	Status       string         `json:"status"`
	StatusReason string         `json:"status_reason,omitempty"`
	CreatedAt    string         `json:"created_at,omitempty"`
	UpdatedAt    string         `json:"updated_at,omitempty"`
	LastUsedAt   string         `json:"last_used_at,omitempty"`
	Extra        map[string]any `json:"-"`
}

// AuthConfigRef 是已连接账号里嵌套的 auth_config 对象。部分响应还会带老的
// 顶层 auth_config_id 字段。
type AuthConfigRef struct {
	ID                string `json:"id"`
	AuthScheme        string `json:"auth_scheme,omitempty"`
	IsComposioManaged bool   `json:"is_composio_managed,omitempty"`
	IsDisabled        bool   `json:"is_disabled,omitempty"`
}

// ListConnectedAccountsResponse 是分页响应的类型化表示。
type ListConnectedAccountsResponse struct {
	Items      []ConnectedAccount `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
	TotalItems int                `json:"total_items,omitempty"`
}

// ListConnectedAccounts 返回与所给过滤条件匹配的连接。
func (c *Client) ListConnectedAccounts(ctx context.Context, req ListConnectedAccountsRequest) (*ListConnectedAccountsResponse, error) {
	q := url.Values{}
	for _, v := range req.UserIDs {
		if v != "" {
			q.Add("user_ids", v)
		}
	}
	for _, v := range req.ToolkitSlugs {
		if v != "" {
			q.Add("toolkit_slugs", v)
		}
	}
	for _, v := range req.AuthConfigIDs {
		if v != "" {
			q.Add("auth_config_ids", v)
		}
	}
	for _, v := range req.ConnectedAccountIDs {
		if v != "" {
			q.Add("connected_account_ids", v)
		}
	}
	for _, v := range req.Statuses {
		if v != "" {
			q.Add("statuses", v)
		}
	}
	if req.OrderBy != "" {
		q.Set("order_by", req.OrderBy)
	}
	if req.OrderDirection != "" {
		q.Set("order_direction", req.OrderDirection)
	}
	if req.AccountType != "" {
		q.Set("account_type", req.AccountType)
	}
	if req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	if req.Cursor != "" {
		q.Set("cursor", req.Cursor)
	}

	path := "/connected_accounts"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var out ListConnectedAccountsResponse
	if err := c.do(c.newRequest(ctx), http.MethodGet, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- 撤销 / 删除 ----------------------------------------------------------

// RevokeConnection 撤销上游提供方那边的 OAuth 授权，但保留 Composio 里的记录。
// 用户在应用里「断开连接」时用它，可以立刻让提供方侧的 token 失效。
func (c *Client) RevokeConnection(ctx context.Context, connectedAccountID string) error {
	if connectedAccountID == "" {
		return errors.New("composio: RevokeConnection: connectedAccountID is required")
	}
	return c.do(c.newRequest(ctx),
		http.MethodPost, "/connected_accounts/"+url.PathEscape(connectedAccountID)+"/revoke", nil)
}

// DeleteConnectedAccount 从 Composio 移除这条连接的记录。这个调用不会撤销
// 提供方侧的 token——需要上游一并失效的话，先调 [Client.RevokeConnection]。
//
// 对 404 返回 nil，这样调用方可以把删除当成幂等操作处理。
func (c *Client) DeleteConnectedAccount(ctx context.Context, connectedAccountID string) error {
	if connectedAccountID == "" {
		return errors.New("composio: DeleteConnectedAccount: connectedAccountID is required")
	}
	err := c.do(c.newRequest(ctx),
		http.MethodDelete, "/connected_accounts/"+url.PathEscape(connectedAccountID), nil)
	if err == nil {
		return nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.IsNotFound() {
		return nil
	}
	return err
}
