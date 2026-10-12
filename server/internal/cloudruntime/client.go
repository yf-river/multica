// client.go 定义 cloudruntime 包的 HTTP 客户端：multica 服务器（product 侧）作为
// 中转，向 Multica Cloud 的「运行时/云资源管理」端点转发请求。请求可带用户身份头与
// 请求 id，响应体有大小上限；每次调用还会通过可选的 RequestRecorder 上报一条指标
// 观测（操作名、状态桶、耗时）。
package cloudruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout      = 35 * time.Second
	maxResponseBodySize = 1 << 20
)

var (
	ErrDisabled       = errors.New("multica-cloud URL is not configured")
	ErrInvalidBaseURL = errors.New("multica-cloud URL is invalid")
)

// RequestRecorder 是客户端给每次出站请求埋点要依赖的最小接口，避免对 metrics 包形成
// 硬依赖。recorder 为 nil 时安全地什么都不做。
type RequestRecorder interface {
	RecordCloudRuntimeRequest(op, status string, durationSeconds float64)
}

// Config 配置云运行时客户端。
type Config struct {
	BaseURL    string
	Timeout    time.Duration
	HTTPClient *http.Client
	// Recorder 非 nil 时，每次 Do() 都会收到一条观测：推断出的 op、状态桶与耗时。
	// 生产环境接到 BusinessMetrics 采集器上；测试里保持 nil。
	Recorder RequestRecorder
}

// Request 描述一次转发到 Cloud 的请求。
type Request struct {
	Method    string
	Path      string
	Query     url.Values
	Body      []byte
	UserID    string
	RequestID string

	// Op 是喂给请求指标的高级操作标签（例如 "provision"、"terminate"、"status"、
	// "gateway"）。留空则按路径推断一个桶——适合那些没有明确符号名的临时代理调用。
	Op string

	// Headers 携带调用方想「原样转发」的任意出站头。它们在客户端默认头（Accept、
	// Content-Type、X-User-ID、X-Request-ID）之后应用，因此调用方提供的同名头会覆盖
	// 默认值——做请求代理时很关键，例如某个请求的 Content-Type 不是 application/json，
	// 或签名 body 不能动（Stripe webhook 透传要保留 Stripe-Signature 与原始 body）。
	//
	// nil / 空是常见情况；既有调用点可以直接忽略本字段。
	Headers http.Header
}

// Response 是 Cloud 响应的简化表示。
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// Client 是云运行时 HTTP 客户端。
type Client struct {
	baseURL    string
	httpClient *http.Client
	recorder   RequestRecorder
}

// NewClient 构造客户端。Timeout 非正用默认值；HTTPClient 为 nil 时新建一个。
func NewClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		httpClient: httpClient,
		recorder:   cfg.Recorder,
	}
}

// SetRecorder 允许调用方在构造之后再挂指标记录器——比如 handler.New 在 main.go 还没
// 决定是否启用指标监听时就先构造了 cloudruntime 客户端。
func (c *Client) SetRecorder(r RequestRecorder) {
	if c == nil {
		return
	}
	c.recorder = r
}

// Enabled 报告客户端是否可用（配置了 BaseURL）。
func (c *Client) Enabled() bool {
	return c != nil && c.baseURL != ""
}

// Do 执行一次云运行时请求。会记录一条指标观测（若 recorder 已设置）。
func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	if c == nil || c.baseURL == "" {
		return nil, ErrDisabled
	}

	op := inferCloudRuntimeOp(req.Op, req.Method, req.Path)
	start := time.Now()
	resp, err := c.doInner(ctx, req)
	if c.recorder != nil {
		status := requestStatusBucket(resp, err)
		c.recorder.RecordCloudRuntimeRequest(op, status, time.Since(start).Seconds())
	}
	return resp, err
}

// doInner 真正发出请求：拼 URL、设置默认头、应用调用方自定义头、读受限大小的响应体。
func (c *Client) doInner(ctx context.Context, req Request) (*Response, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("%w: %s", ErrInvalidBaseURL, c.baseURL)
	}
	if !strings.HasPrefix(req.Path, "/") {
		return nil, fmt.Errorf("cloud runtime path must start with /: %s", req.Path)
	}

	u := *base
	u.Path = strings.TrimRight(base.Path, "/") + req.Path
	u.RawQuery = req.Query.Encode()

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, u.String(), body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/json")
	if len(req.Body) > 0 {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	// 调用方头在 X-User-ID / X-Request-ID 打戳之前应用，因为后两者来自已认证的上下文、
	// 绝不能被调用方覆盖。默认头（Accept、Content-Type）则可以覆盖——webhook 透传可能
	// 需要保留非 JSON 的 Content-Type。
	for k, vs := range req.Headers {
		// 跳过下面要「权威打戳」的头。每个键只做一次 Canonicalize——
		// http.CanonicalHeaderKey 的快路径有分配，每迭代调两次等于把每次请求的头开销
		// 白白翻倍。
		canon := http.CanonicalHeaderKey(k)
		if canon == "X-User-Id" || canon == "X-Request-Id" {
			continue
		}
		httpReq.Header.Del(k)
		for _, v := range vs {
			httpReq.Header.Add(k, v)
		}
	}
	if req.UserID != "" {
		httpReq.Header.Set("X-User-ID", req.UserID)
	}
	if req.RequestID != "" {
		httpReq.Header.Set("X-Request-ID", req.RequestID)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 限读响应体（+1 用于判断是否超限）。
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBodySize {
		return nil, fmt.Errorf("cloud runtime response exceeds %d bytes", maxResponseBodySize)
	}
	return &Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       data,
	}, nil
}

// inferCloudRuntimeOp 返回请求指标用的符号操作名。调用方可以显式钉 Request.Op；
// 否则按路径推导桶，让既有调用点无需改动。
func inferCloudRuntimeOp(op, method, path string) string {
	op = strings.ToLower(strings.TrimSpace(op))
	if op != "" {
		return op
	}
	switch {
	case strings.Contains(path, "/gateway") || strings.Contains(path, "/proxy"):
		return "gateway"
	case strings.Contains(path, "/exec"):
		return "gateway"
	case strings.Contains(path, "/start") || strings.Contains(path, "/provision") || strings.Contains(path, "/nodes/create"):
		return "provision"
	case strings.Contains(path, "/stop") || strings.Contains(path, "/terminate") || strings.Contains(path, "/reboot"):
		return "terminate"
	case strings.Contains(path, "/status") || strings.Contains(path, "/health") || strings.Contains(path, "/ready"):
		return "status"
	case strings.Contains(path, "/nodes"):
		switch strings.ToUpper(strings.TrimSpace(method)) {
		case "POST":
			return "provision"
		case "DELETE":
			return "terminate"
		default:
			return "status"
		}
	}
	return "fleet"
}

// requestStatusBucket 把 (response, error) 归入固定指标状态枚举 {ok, 4xx, 5xx,
// timeout, error}。
func requestStatusBucket(resp *Response, err error) string {
	if err != nil {
		// context.DeadlineExceeded 会以 net.Error.Timeout() 的形式出现；这里把子串
		// 判断放宽松些，让被包装过的超时也仍能归入 timeout 桶。
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded") {
			return "timeout"
		}
		return "error"
	}
	if resp == nil {
		return "error"
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 400:
		return "ok"
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return "4xx"
	case resp.StatusCode >= 500 && resp.StatusCode < 600:
		return "5xx"
	}
	return "error"
}
