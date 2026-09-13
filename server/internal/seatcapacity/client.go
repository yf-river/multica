// Package seatcapacity 是 Multica Cloud「预购 workspace 座位」协议的产品侧执行器：
// 产品代码通过 Executor 接口向 Cloud 发起 预留/认领/消耗/确认/释放 等容量动作，并把
// Cloud 的决策映射成本地可用的返回结构。另含把失败的 Cloud 调用归成可判断的错误
// （404 / 超卖 / 限流）的辅助函数。
package seatcapacity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	defaultTimeout       = 3 * time.Second
	maxResponseBodySize  = 64 << 10
	rateLimitScopeHeader = "X-Multica-RateLimit-Scope"

	RateLimitScopeGlobal    = "global"
	RateLimitScopeWorkspace = "workspace"
)

// ErrInvalidConfig 表示 BaseURL 配置非法。
var ErrInvalidConfig = errors.New("seat capacity: invalid configuration")

// Config 是客户端配置。BaseURL 为空时 New 返回 nil（表示未启用容量执行器）。
type Config struct {
	BaseURL    string
	HTTPClient *http.Client
}

// Capacity 是某 workspace 当前的座位容量快照。
type Capacity struct {
	PurchasedSeats int   `json:"purchased_seats"` // 已购买座位数
	UsedSeats      int   `json:"used_seats"`      // 已使用座位数
	ReservedSeats  int   `json:"reserved_seats"`  // 已预留座位数
	Version        int64 `json:"version"`         // 版本号（乐观并发用）
}

// Operation 描述 Cloud 侧一次容量操作（预留/消耗事务）的状态。
type Operation struct {
	Token       uuid.UUID  `json:"token"`
	WorkspaceID uuid.UUID  `json:"workspace_id"`
	Kind        string     `json:"kind"`
	SubjectID   uuid.UUID  `json:"subject_id"`
	State       string     `json:"state"` // 例如 reserved / consuming / used / released
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// Decision 是 Cloud 对一次容量动作的答复。
type Decision struct {
	Managed   bool       `json:"managed"` // 该 workspace 是否受容量管理
	Allowed   bool       `json:"allowed"`
	Reason    string     `json:"reason,omitempty"`
	Operation *Operation `json:"operation,omitempty"`
	Capacity  *Capacity  `json:"capacity,omitempty"`
}

// Executor 是产品侧代码调用容量系统要依赖的接口。
type Executor interface {
	// RecoveryAvailable 报告该执行器能否结算持久化的产品侧意图。实现与装饰器必须显式
	// 转发这个能力——否则一个不可用的执行器会被包装器藏起来，意外启动恢复 worker。
	RecoveryAvailable() bool
	ReserveInvitation(context.Context, uuid.UUID, uuid.UUID, time.Time) (Decision, error)
	ClaimShareJoin(context.Context, uuid.UUID, uuid.UUID) (Decision, error)
	Consume(context.Context, uuid.UUID, uuid.UUID) (Decision, error)
	Confirm(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (Decision, error)
	Release(context.Context, uuid.UUID, uuid.UUID) (Decision, error)
	ReleaseMember(context.Context, uuid.UUID, uuid.UUID) (Decision, error)
	GetOperation(context.Context, uuid.UUID, uuid.UUID) (Decision, error)
}

// unavailableExecutor 是所有调用都失败的执行器（fail-closed）。
type unavailableExecutor struct{ err error }

// NewUnavailable 在「连 Cloud 的部署配置非法」时保持 fail-closed 行为。
func NewUnavailable(err error) Executor { return &unavailableExecutor{err: err} }

// CanRunWorker 报告该执行器能否安全地结算持久化意图。
func CanRunWorker(executor Executor) bool {
	return executor != nil && executor.RecoveryAvailable()
}

func (*unavailableExecutor) RecoveryAvailable() bool { return false }

func (u *unavailableExecutor) fail() (Decision, error) {
	return Decision{}, fmt.Errorf("seat capacity executor unavailable: %w", u.err)
}
func (u *unavailableExecutor) ReserveInvitation(context.Context, uuid.UUID, uuid.UUID, time.Time) (Decision, error) {
	return u.fail()
}
func (u *unavailableExecutor) ClaimShareJoin(context.Context, uuid.UUID, uuid.UUID) (Decision, error) {
	return u.fail()
}
func (u *unavailableExecutor) Consume(context.Context, uuid.UUID, uuid.UUID) (Decision, error) {
	return u.fail()
}
func (u *unavailableExecutor) Confirm(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (Decision, error) {
	return u.fail()
}
func (u *unavailableExecutor) Release(context.Context, uuid.UUID, uuid.UUID) (Decision, error) {
	return u.fail()
}
func (u *unavailableExecutor) ReleaseMember(context.Context, uuid.UUID, uuid.UUID) (Decision, error) {
	return u.fail()
}
func (u *unavailableExecutor) GetOperation(context.Context, uuid.UUID, uuid.UUID) (Decision, error) {
	return u.fail()
}

// Client 是连到 Cloud 内部容量端点的 HTTP 执行器。
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

// 编译期断言：*Client 实现 Executor。
var _ Executor = (*Client)(nil)

func (*Client) RecoveryAvailable() bool { return true }

// New 构造容量执行器。BaseURL 为空返回 (nil, nil)——调用方应把 nil 当作「未启用」。
func New(cfg Config) (Executor, error) {
	rawURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if rawURL == "" {
		return nil, nil
	}
	baseURL, err := url.Parse(rawURL)
	if err != nil || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" ||
		baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, fmt.Errorf("%w: base URL must be absolute and contain no credentials, query, or fragment", ErrInvalidConfig)
	}
	httpClient := &http.Client{}
	if cfg.HTTPClient != nil {
		clone := *cfg.HTTPClient
		httpClient = &clone
	}
	// 内部 Cloud 调用绝不能跟随重定向跳到别的源站。
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		baseURL:    baseURL,
		httpClient: httpClient,
	}, nil
}

// ReserveInvitation 为一次邀请预留座位。邀请到期时间由 expiresAt 给出。
func (c *Client) ReserveInvitation(ctx context.Context, workspaceID, invitationID uuid.UUID, expiresAt time.Time) (Decision, error) {
	return c.post(ctx, workspaceID, "reserve", map[string]any{
		"token": invitationID, "kind": "invitation", "subject_id": invitationID, "expires_at": expiresAt,
	})
}

// ClaimShareJoin 认领一次「共享加入」占用的容量。
func (c *Client) ClaimShareJoin(ctx context.Context, workspaceID, intentID uuid.UUID) (Decision, error) {
	return c.post(ctx, workspaceID, "claim", map[string]any{
		"token": intentID, "kind": "share_join", "subject_id": intentID,
	})
}

// Consume 消耗一次已预留的操作（把预留变成正式占用）。
func (c *Client) Consume(ctx context.Context, workspaceID, token uuid.UUID) (Decision, error) {
	return c.post(ctx, workspaceID, "consume", map[string]any{"token": token})
}

// Confirm 确认某成员入座。
func (c *Client) Confirm(ctx context.Context, workspaceID, token, memberID uuid.UUID) (Decision, error) {
	return c.post(ctx, workspaceID, "confirm", map[string]any{"token": token, "member_id": memberID})
}

// Release 释放一次容量操作占用的座位。
func (c *Client) Release(ctx context.Context, workspaceID, token uuid.UUID) (Decision, error) {
	return c.post(ctx, workspaceID, "release", map[string]any{"token": token})
}

// ReleaseMember 释放某成员占用的座位。
func (c *Client) ReleaseMember(ctx context.Context, workspaceID, memberID uuid.UUID) (Decision, error) {
	return c.post(ctx, workspaceID, "release-member", map[string]any{"member_id": memberID})
}

// GetOperation 查询一次容量操作的当前状态（供对账恢复使用）。
func (c *Client) GetOperation(ctx context.Context, workspaceID, token uuid.UUID) (Decision, error) {
	return c.do(ctx, http.MethodGet, workspaceID, "operations/"+token.String(), nil)
}

func (c *Client) post(ctx context.Context, workspaceID uuid.UUID, action string, value any) (Decision, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return Decision{}, err
	}
	return c.do(ctx, http.MethodPost, workspaceID, action, body)
}

// do 是真正发请求的地方：拼 URL、限时、发请求、读受限大小的响应体，并把非 2xx
// 统一折成 *HTTPError（带 code、Retry-After、限流作用域等可操作信息）。
func (c *Client) do(ctx context.Context, method string, workspaceID uuid.UUID, suffix string, body []byte) (Decision, error) {
	if workspaceID == uuid.Nil {
		return Decision{}, fmt.Errorf("seat capacity: workspace ID is required")
	}
	u := *c.baseURL
	u.Path = strings.TrimRight(c.baseURL.Path, "/") + "/api/v1/internal/subscriptions/" + workspaceID.String() + "/capacity/" + suffix

	requestCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(requestCtx, method, u.String(), reader)
	if err != nil {
		return Decision{}, err
	}
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Decision{}, fmt.Errorf("seat capacity request failed: %w", err)
	}
	defer resp.Body.Close()
	// 限定响应体大小（+1 用于判断是否超限）。
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize+1))
	if err != nil {
		return Decision{}, fmt.Errorf("seat capacity response read failed: %w", err)
	}
	if len(payload) > maxResponseBodySize {
		return Decision{}, fmt.Errorf("seat capacity response exceeded %d bytes", maxResponseBodySize)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var remote struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		_ = json.Unmarshal(payload, &remote)
		return Decision{}, &HTTPError{
			StatusCode:     resp.StatusCode,
			Code:           remote.Code,
			Message:        remote.Error,
			RetryAfter:     retryAfterDuration(resp.Header.Get("Retry-After")),
			RateLimitScope: normalizedRateLimitScope(resp.Header.Get(rateLimitScopeHeader)),
		}
	}
	var out Decision
	if err := json.Unmarshal(payload, &out); err != nil {
		return Decision{}, fmt.Errorf("seat capacity response decode failed: %w", err)
	}
	return out, nil
}

// HTTPError 是一次失败的容量调用的结构化错误。
type HTTPError struct {
	StatusCode     int
	Code           string        // Cloud 侧的稳定错误码（代理生成的 429 没有）
	Message        string        // 错误描述
	RetryAfter     time.Duration // 限流时建议等待时长
	RateLimitScope string        // global / workspace
}

func (e *HTTPError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("seat capacity request returned %d (%s)", e.StatusCode, e.Code)
	}
	return fmt.Sprintf("seat capacity request returned %d", e.StatusCode)
}

// IsNotFound 判断错误是否为「对象不存在」(404)。
func IsNotFound(err error) bool {
	var remote *HTTPError
	return errors.As(err, &remote) && remote.StatusCode == http.StatusNotFound
}

// IsCapacityOvercommitted 判断是否为「容量超卖冲突」(409 + capacity_overcommitted)。
func IsCapacityOvercommitted(err error) bool {
	var remote *HTTPError
	return errors.As(err, &remote) && remote.StatusCode == http.StatusConflict && remote.Code == "capacity_overcommitted"
}

// IsRateLimited 判断是否为限流 (429)。
func IsRateLimited(err error) bool {
	var remote *HTTPError
	// 代理/入口/WAF 可能在请求到达 Cloud 前就生成 429，因此带不上 Cloud 的 JSON
	// 错误码。HTTP 429 本身已足够保留「可重试」语义与 Retry-After 值。
	return errors.As(err, &remote) && remote.StatusCode == http.StatusTooManyRequests
}

// RateLimitRetryAfter 取出限流错误的建议等待时长（非限流返回 0）。
func RateLimitRetryAfter(err error) time.Duration {
	var remote *HTTPError
	if !errors.As(err, &remote) || !IsRateLimited(remote) {
		return 0
	}
	return remote.RetryAfter
}

// RateLimitScopeOf 返回可信的 Cloud 限流作用域提示。代理生成的 429 通常没有作用域，
// 对调用方保守地按全局处理。
func RateLimitScopeOf(err error) string {
	var remote *HTTPError
	if !errors.As(err, &remote) || !IsRateLimited(remote) {
		return ""
	}
	return normalizedRateLimitScope(remote.RateLimitScope)
}

// normalizedRateLimitScope 把作用域头规整成已知枚举（不认识返回空）。
func normalizedRateLimitScope(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case RateLimitScopeGlobal:
		return RateLimitScopeGlobal
	case RateLimitScopeWorkspace:
		return RateLimitScopeWorkspace
	default:
		return ""
	}
}

func retryAfterDuration(value string) time.Duration {
	return retryAfterDurationAt(value, time.Now())
}

// retryAfterDurationAt 解析 Retry-After 头：数字秒数或 HTTP 时间都支持；过去时间返回 0。
func retryAfterDurationAt(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err == nil {
		if seconds < 1 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	retryAt, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	delay := retryAt.Sub(now)
	if delay <= 0 {
		return 0
	}
	return delay
}
