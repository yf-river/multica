package composio

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// AuthConfig 是 Composio auth config 的一个子集镜像——auth config 是项目
// 级别的记录，定义「用户要用什么方式给某个 toolkit 授权」（OAuth client、
// API-key 方案等）。Connect Link 流程需要它的不透明 `id`（ac_…）；
// 其余字段用于一个 toolkit 挂多个 config 时做选择。
//
// Spec: https://docs.composio.dev/reference/v3/api-reference/auth-configs/getAuthConfigs
type AuthConfig struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Toolkit 至少带上该 config 所属 toolkit 的 slug（和一个 logo）。
	Toolkit    Toolkit `json:"toolkit"`
	AuthScheme string  `json:"auth_scheme,omitempty"`
	// IsComposioManaged 为 true 表示用的是 Composio 托管的 OAuth app；
	// false 表示自定义（自带 client_id/client_secret）的 config，即白标场景。
	IsComposioManaged bool `json:"is_composio_managed"`
	// Status 取值 "ENABLED" 或 "DISABLED"。列表接口默认会隐藏被禁用的
	// config（show_disabled=false）。
	Status        string `json:"status,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	LastUpdatedAt string `json:"last_updated_at,omitempty"`
}

// ListAuthConfigsRequest 汇总 GET /auth_configs 支持的过滤参数。
// 零值不会拼进查询串。
type ListAuthConfigsRequest struct {
	// ToolkitSlugs 只查指定 toolkit；按 v3 规范以逗号分隔的单个
	// `toolkit_slug` 查询参数发送。
	ToolkitSlugs []string
	// IsComposioManaged 非 nil 时按「托管 vs 自定义」过滤。
	IsComposioManaged *bool
	// ShowDisabled 是否把禁用的 config 也查出来（默认 false = 只要启用的）。
	ShowDisabled bool
	// Search 按名称或 id 匹配 auth config。
	Search string
	// Limit 是每页大小（上游上限 1000）。0 = 用上游默认值。
	Limit int
	// Cursor 用于翻页。
	Cursor string
}

// ListAuthConfigsResponse 是分页响应的类型化表示。
type ListAuthConfigsResponse struct {
	Items      []AuthConfig `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
	TotalItems int          `json:"total_items,omitempty"`
}

// ListAuthConfigs 返回项目里已注册的 auth config，支持可选过滤。
// 项目身份由 x-api-key 决定（一把项目 API key 恰好对应一个项目），
// 所以这里不需要传 project id。
func (c *Client) ListAuthConfigs(ctx context.Context, req ListAuthConfigsRequest) (*ListAuthConfigsResponse, error) {
	q := url.Values{}
	if len(req.ToolkitSlugs) > 0 {
		q.Set("toolkit_slug", strings.Join(req.ToolkitSlugs, ","))
	}
	if req.IsComposioManaged != nil {
		q.Set("is_composio_managed", strconv.FormatBool(*req.IsComposioManaged))
	}
	if req.ShowDisabled {
		q.Set("show_disabled", "true")
	}
	if req.Search != "" {
		q.Set("search", req.Search)
	}
	if req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	if req.Cursor != "" {
		q.Set("cursor", req.Cursor)
	}

	path := "/auth_configs"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var out ListAuthConfigsResponse
	if err := c.do(c.newRequest(ctx), http.MethodGet, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
