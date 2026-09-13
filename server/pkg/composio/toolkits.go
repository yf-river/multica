package composio

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// Toolkit 是最小化的 toolkit 描述，用作已连接账号、会话和 toolkit 列表接口里的
// 嵌套字段。只把 UI / 分发决策用到的字段类型化了。
type Toolkit struct {
	Slug        string         `json:"slug"`
	Name        string         `json:"name,omitempty"`
	LogoURL     string         `json:"logo,omitempty"`
	Description string         `json:"description,omitempty"`
	Categories  []string       `json:"categories,omitempty"`
	AuthSchemes []string       `json:"auth_schemes,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

// ListToolkitsRequest 携带 GET /toolkits 的可选过滤参数。
type ListToolkitsRequest struct {
	Category string
	Limit    int
	Cursor   string
	// SortBy 是上游的排序方式。按 v3.1 规范，合法枚举值是 "usage"
	// （按使用量）和 "alphabetically"（按字母序）。
	SortBy string
}

// ListToolkitsResponse 是分页响应的类型化表示。
type ListToolkitsResponse struct {
	Items      []Toolkit `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
	TotalItems int       `json:"total_items,omitempty"`
}

// ListToolkits 返回该项目可用（已授权/可连接）的 toolkit 列表。
func (c *Client) ListToolkits(ctx context.Context, req ListToolkitsRequest) (*ListToolkitsResponse, error) {
	q := url.Values{}
	if req.Category != "" {
		q.Set("category", req.Category)
	}
	if req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	if req.Cursor != "" {
		q.Set("cursor", req.Cursor)
	}
	if req.SortBy != "" {
		q.Set("sort_by", req.SortBy)
	}
	path := "/toolkits"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out ListToolkitsResponse
	if err := c.do(c.newRequest(ctx), http.MethodGet, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetToolkit 按 slug（例如 "notion"、"github"）拉取单个 toolkit。
func (c *Client) GetToolkit(ctx context.Context, slug string) (*Toolkit, error) {
	if slug == "" {
		return nil, errors.New("composio: GetToolkit: slug is required")
	}
	var out Toolkit
	if err := c.do(c.newRequest(ctx),
		http.MethodGet, "/toolkits/"+url.PathEscape(slug), &out); err != nil {
		return nil, err
	}
	return &out, nil
}
