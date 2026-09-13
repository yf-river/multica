package composio

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

// DefaultBaseURL 是 Composio v3.1 REST API 的标准根地址。
const DefaultBaseURL = "https://backend.composio.dev/api/v3.1"

// DefaultUserAgent 是默认的用户代理头；除非在 [Options.UserAgent] 里覆盖。
const DefaultUserAgent = "multica-composio-go/0.1"

// DefaultTimeout 是底层 resty 客户端在 [Options.Timeout] 为零时用的
// 单请求超时时间。
const DefaultTimeout = 30 * time.Second

// Options 用来配置一个 [Client]。只有 APIKey 是必填的。
type Options struct {
	// APIKey 是 Composio 项目的 API key，会以 `x-api-key` 头发送。
	APIKey string

	// BaseURL 覆盖 API 根地址。主要用于针对 httptest.Server 的测试。
	// 默认 [DefaultBaseURL]。
	BaseURL string

	// UserAgent 覆盖 User-Agent 头。默认 [DefaultUserAgent]。
	UserAgent string

	// Timeout 是单请求超时。0 表示 [DefaultTimeout]；负值表示完全关闭超时。
	Timeout time.Duration

	// HTTPClient 允许调用方注入自定义 *http.Client（比如带公司代理 transport、
	// 自定义 CookieJar、重定向策略或可观测性插桩的）。非 nil 时会通过
	// resty.NewWithClient 整个采用——调用方的 Transport、Jar、CheckRedirect
	// 和内置 Timeout 都会跟着生效。若该 client 的 Timeout 与 [Options.Timeout]
	// 同时设置，[Options.Timeout] 优先。
	HTTPClient *http.Client

	// RetryCount 是 resty 在瞬时故障上重试的次数。0 表示不重试
	// （调用方可以自己套重试）。
	RetryCount int

	// RetryWaitTime 是 RetryCount > 0 时的基础重试间隔。
	RetryWaitTime time.Duration
}

// Client 是 Composio REST 客户端。
//
// 可被多个 goroutine 并发安全使用。
type Client struct {
	rc        *resty.Client
	baseURL   string
	apiKey    string
	userAgent string
}

// NewClient 根据 [Options] 构造 Client。当配置明显有问题
// （API key 为空、BaseURL 非法）时返回错误。
func NewClient(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, errors.New("composio: APIKey is required")
	}

	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("composio: invalid BaseURL %q: %w", baseURL, err)
	}
	baseURL = strings.TrimRight(baseURL, "/")

	ua := opts.UserAgent
	if ua == "" {
		ua = DefaultUserAgent
	}

	timeout := opts.Timeout
	switch {
	case timeout == 0:
		timeout = DefaultTimeout
	case timeout < 0:
		timeout = 0 // resty 把 0 视为「不设超时」
	}

	rc := newRestyClient(opts.HTTPClient).
		SetBaseURL(baseURL).
		SetHeader("Content-Type", "application/json").
		SetHeader("Accept", "application/json").
		SetHeader("User-Agent", ua).
		SetHeader("x-api-key", opts.APIKey).
		SetTimeout(timeout)

	if opts.RetryCount > 0 {
		rc = rc.SetRetryCount(opts.RetryCount)
		if opts.RetryWaitTime > 0 {
			rc = rc.SetRetryWaitTime(opts.RetryWaitTime)
		}
	}

	return &Client{
		rc:        rc,
		baseURL:   baseURL,
		apiKey:    opts.APIKey,
		userAgent: ua,
	}, nil
}

// newRestyClient 构造 resty.Client：如果传入了注入的 *http.Client 就整体采用。
// resty.NewWithClient 会完整接管调用方的 http.Client——Transport、Jar、
// CheckRedirect 和 Timeout 全部跟着生效，这与 [Options.HTTPClient] 的文档
// 契约一致。
func newRestyClient(hc *http.Client) *resty.Client {
	if hc != nil {
		return resty.NewWithClient(hc)
	}
	return resty.New()
}

// BaseURL 返回做完全部默认补齐后的 API 根地址。
func (c *Client) BaseURL() string { return c.baseURL }

// APIKeyHeader 返回调用方在 SDK 之外连 MCP streaming 客户端、或发任何其它
// Composio 请求时需要附上的请求头对。
//
// 返回的是拷贝，保证内部 map 不被外部篡改。
func (c *Client) APIKeyHeader() map[string]string {
	return map[string]string{"x-api-key": c.apiKey}
}

// newRequest 返回一个绑定了给定 context 的 resty.Request。
// 所有端点方法都经由这个辅助函数发请求。
func (c *Client) newRequest(ctx context.Context) *resty.Request {
	return c.rc.R().SetContext(ctx)
}

// do 执行一次请求，并把成功响应的 body 反序列化到 out。
// 非 2xx 时返回从响应体解析出的 *APIError。
//
// out 可以为 nil——调用方不关心响应体时（例如 DELETE 返回 204）就传 nil。
func (c *Client) do(req *resty.Request, method, path string, out any) error {
	if out != nil {
		req = req.SetResult(out)
	}
	resp, err := req.Execute(method, path)
	if err != nil {
		return fmt.Errorf("composio: %s %s: %w", method, path, err)
	}
	if resp.IsError() {
		return parseAPIError(resp.StatusCode(), resp.Body())
	}
	return nil
}
