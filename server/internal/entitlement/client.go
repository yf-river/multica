// client.go 实现 entitlement 的 Client：从 Multica Cloud 内部端点拉取某个 workspace
// 的策略并缓存。每个 workspace 同一时刻至多一个在途刷新（singleflight 合并并发请求）。
// Client 没有 goroutine、没有后台生命周期——所有刷新都是请求路径上同步完成的。
package entitlement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

const (
	defaultRequestTimeout = 3 * time.Second
	defaultMaxEntries     = 10_000
	defaultStaleGrace     = 15 * time.Minute
	defaultFailureRetry   = 5 * time.Second
	maxPolicyTTL          = 5 * time.Minute
	maxResponseBodySize   = 64 << 10
)

var (
	ErrInvalidConfig = errors.New("entitlement: invalid configuration")
	ErrInvalidPolicy = errors.New("entitlement: invalid policy response")
)

// Config 没有任何「加载环境变量」的副作用。零值 Config 表示禁用；设置 BaseURL 会把
// 客户端连到内部 Cloud 策略端点。
type Config struct {
	BaseURL    string
	HTTPClient *http.Client
	Observer   Observer
	Logger     *slog.Logger
}

// Client 实现 Provider：带一个「有界 workspace 缓存」与「每 workspace 至多一个在途
// 刷新」。它没有 goroutine 或后台生命周期。
type Client struct {
	enabled    bool
	baseURL    *url.URL
	timeout    time.Duration
	staleGrace time.Duration
	httpClient *http.Client
	observer   Observer
	logger     *slog.Logger
	now        func() time.Time
	cache      *policyCache
	refreshes  singleflight.Group
}

// 编译期断言：*Client 实现 Provider。
var _ Provider = (*Client)(nil)

// New 构造 Client。BaseURL 为空则返回禁用实例（仅保留 observer/时钟）。BaseURL 必须
// 是「无凭证、无 query、无 fragment」的绝对 http(s) URL。
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return &Client{observer: cfg.Observer, now: time.Now}, nil
	}

	rawBaseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	baseURL, err := url.Parse(rawBaseURL)
	if err != nil || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" ||
		baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, fmt.Errorf("%w: base URL must be an absolute URL without credentials, query, or fragment", ErrInvalidConfig)
	}
	httpClient := &http.Client{}
	if cfg.HTTPClient != nil {
		clone := *cfg.HTTPClient
		httpClient = &clone
	}
	// 内部 Cloud 调用绝不能跟随重定向跳到别的源站。
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Client{
		enabled:    true,
		baseURL:    baseURL,
		timeout:    defaultRequestTimeout,
		staleGrace: defaultStaleGrace,
		httpClient: httpClient,
		observer:   cfg.Observer,
		logger:     logger,
		now:        time.Now,
		cache:      newPolicyCache(defaultMaxEntries),
	}, nil
}

// Enabled 报告该客户端是否启用（nil 或未配置 BaseURL 都为 false）。
func (c *Client) Enabled() bool { return c != nil && c.enabled }

// Gate 返回某 workspace 上某个门当前应怎么做的判词。缓存/刷新决策都在这里完成；
// 一切降级路径都以 ActionOff 或「陈旧快照降级为 observe」的形式返回，绝不抛错。
func (c *Client) Gate(ctx context.Context, workspaceID uuid.UUID, name GateName) Decision {
	if !c.Enabled() {
		return c.recordDecision(name, offDecision(ReasonDisabled))
	}
	if workspaceID == uuid.Nil {
		return c.recordDecision(name, offDecision(ReasonInvalidWorkspace))
	}
	if !name.valid() {
		return c.recordDecision(name, offDecision(ReasonUnknownGate))
	}

	now := c.now()
	cached, cachedOK := c.cache.get(workspaceID)
	if cachedOK && cached.hasPolicy && now.Before(cached.freshUntil) {
		c.recordCache("hit")
		return c.recordDecision(name, decisionFromEntry(cached, name, ReasonCacheFresh, false))
	}
	// 上次刷新刚失败、还在退避期内：尽量用缓存；没有可用缓存则直接判 off。
	if cachedOK && now.Before(cached.retryAfter) {
		c.recordCache("retry_suppressed")
		if cached.hasPolicy && now.Before(cached.staleUntil) {
			return c.recordDecision(name, decisionFromEntry(cached, name, ReasonStale, true))
		}
		return c.recordDecision(name, offDecision(ReasonUnavailable))
	}
	if cachedOK && cached.hasPolicy {
		c.recordCache("expired")
	} else {
		c.recordCache("miss")
	}

	entry, refreshed, err := c.refresh(ctx, workspaceID)
	if err == nil {
		reason := ReasonRefreshed
		if !refreshed {
			reason = ReasonCacheFresh
		}
		return c.recordDecision(name, decisionFromEntry(entry, name, reason, false))
	}

	// 陈旧策略只在「执行等级已降为 observe」之后才安全。重新读缓存，因为等待
	// singleflight 期间别的调用方可能已经刷新过了。
	now = c.now()
	if cached, ok := c.cache.get(workspaceID); ok && cached.hasPolicy && now.Before(cached.staleUntil) {
		return c.recordDecision(name, decisionFromEntry(cached, name, ReasonStale, true))
	}
	reason := ReasonUnavailable
	if errors.Is(err, ErrInvalidPolicy) {
		reason = ReasonInvalidPolicy
	}
	if errors.Is(err, errVersionRegression) {
		reason = ReasonVersionRegression
	}
	return c.recordDecision(name, offDecision(reason))
}

// refreshResult 是一次刷新的结果：refreshed=false 表示没有真正发请求（缓存仍新鲜）。
type refreshResult struct {
	entry     cacheEntry
	refreshed bool
}

// refresh 为该 workspace 刷新策略（同一 workspace 的所有调用方共享一次在途刷新）。
func (c *Client) refresh(ctx context.Context, workspaceID uuid.UUID) (cacheEntry, bool, error) {
	// 一次刷新被该 workspace 的所有调用方共享，所以它的生命周期不能属于「恰好先进入
	// singleflight 的那个请求」。保留请求作用域的值，但用独立的 fetch 超时来约束它。
	refreshCtx := context.WithoutCancel(ctx)
	value, err, _ := c.refreshes.Do(workspaceID.String(), func() (any, error) {
		now := c.now()
		if cached, ok := c.cache.get(workspaceID); ok {
			if cached.hasPolicy && now.Before(cached.freshUntil) {
				return refreshResult{entry: cached}, nil
			}
			if now.Before(cached.retryAfter) {
				return nil, &fetchError{outcome: "retry_suppressed"}
			}
		}

		started := time.Now()
		policy, err := c.fetch(refreshCtx, workspaceID)
		if err != nil {
			c.cache.markFailure(workspaceID, c.now().Add(defaultFailureRetry))
			c.recordRefresh(refreshOutcome(err), time.Since(started))
			c.logger.DebugContext(refreshCtx, "entitlement policy refresh failed",
				"workspace_id", workspaceID.String(), "outcome", refreshOutcome(err))
			return nil, err
		}
		receivedAt := c.now()
		ttl := time.Duration(policy.validForSeconds) * time.Second
		entry := cacheEntry{
			hasPolicy:  true,
			policy:     policy.snapshot,
			freshUntil: receivedAt.Add(ttl),
			staleUntil: receivedAt.Add(ttl).Add(c.staleGrace),
		}
		stored, err := c.cache.put(workspaceID, entry, receivedAt)
		if err != nil {
			c.cache.markFailure(workspaceID, c.now().Add(defaultFailureRetry))
			if c.observer != nil {
				c.observer.RecordEntitlementVersionRegression()
			}
			c.recordRefresh("version_regression", time.Since(started))
			c.logger.WarnContext(refreshCtx, "entitlement subscription version regressed",
				"workspace_id", workspaceID.String())
			return nil, err
		}
		c.recordRefresh("ok", time.Since(started))
		return refreshResult{entry: stored, refreshed: true}, nil
	})
	if err != nil {
		return cacheEntry{}, false, err
	}
	result := value.(refreshResult)
	return result.entry, result.refreshed, nil
}

// wirePolicy 是 Cloud 策略端点的线上 JSON 结构（schema 见 SchemaVersion）。
type wirePolicy struct {
	SchemaVersion       int                 `json:"schema_version"`
	PolicyRevision      int64               `json:"policy_revision"`
	SubscriptionVersion int64               `json:"subscription_version"`
	ValidUntil          time.Time           `json:"valid_until"`
	ValidForSeconds     int64               `json:"valid_for_seconds"`
	Gates               map[string]wireGate `json:"gates"`
}

// wireGate 是单个门的线上 JSON 形态。
type wireGate struct {
	Action      string     `json:"action"`
	Limit       *int       `json:"limit"`
	PeriodStart *time.Time `json:"period_start"`
	PeriodEnd   *time.Time `json:"period_end"`
	ResetAt     *time.Time `json:"reset_at"`
}

// fetchedPolicy 是一次成功拉取的结果：快照 + 有效秒数。
type fetchedPolicy struct {
	snapshot        policySnapshot
	validForSeconds int64
}

// fetch 真正向 Cloud 策略端点发一次 GET。
func (c *Client) fetch(ctx context.Context, workspaceID uuid.UUID) (fetchedPolicy, error) {
	u := *c.baseURL
	u.Path = strings.TrimRight(c.baseURL.Path, "/") + "/api/v1/internal/entitlement-policies/" + workspaceID.String()
	u.RawQuery = ""
	u.Fragment = ""

	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fetchedPolicy{}, &fetchError{outcome: "request"}
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return fetchedPolicy{}, &fetchError{outcome: "timeout"}
		}
		return fetchedPolicy{}, &fetchError{outcome: "network"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fetchedPolicy{}, &fetchError{outcome: statusOutcome(resp.StatusCode)}
	}
	// 限定响应体大小（+1 是为了能判断是否超限）。
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize+1))
	if err != nil {
		return fetchedPolicy{}, &fetchError{outcome: "read"}
	}
	if len(body) > maxResponseBodySize {
		return fetchedPolicy{}, fmt.Errorf("%w: response exceeds %d bytes", ErrInvalidPolicy, maxResponseBodySize)
	}
	var wire wirePolicy
	if err := json.Unmarshal(body, &wire); err != nil {
		return fetchedPolicy{}, fmt.Errorf("%w: malformed JSON", ErrInvalidPolicy)
	}
	return normalizePolicy(wire)
}

// normalizePolicy 把线上结构校验并归一化成内部快照。schema 版本不符、修订号/有效期
// 缺失、TTL 非法等一律判 ErrInvalidPolicy。
func normalizePolicy(wire wirePolicy) (fetchedPolicy, error) {
	if wire.SchemaVersion != SchemaVersion || wire.PolicyRevision <= 0 || wire.SubscriptionVersion < 0 ||
		wire.ValidUntil.IsZero() || wire.ValidForSeconds <= 0 || wire.ValidForSeconds > int64(maxPolicyTTL/time.Second) {
		return fetchedPolicy{}, ErrInvalidPolicy
	}
	gates := make(map[GateName]Gate, 2)
	for _, name := range []GateName{GateIssueCount, GateAutopilotRuns} {
		wireGate, ok := wire.Gates[string(name)]
		if !ok {
			return fetchedPolicy{}, ErrInvalidPolicy
		}
		gate, err := normalizeGate(name, wireGate)
		if err != nil {
			return fetchedPolicy{}, err
		}
		gates[name] = gate
	}
	return fetchedPolicy{
		snapshot: policySnapshot{
			policyRevision:      wire.PolicyRevision,
			subscriptionVersion: wire.SubscriptionVersion,
			cloudValidUntil:     wire.ValidUntil,
			gates:               gates,
		},
		validForSeconds: wire.ValidForSeconds,
	}, nil
}

// normalizeGate 校验并归一化单个门：off 不带额度；enforce 必须有非负额度；周期字段
// 要么全给、要么全不给；autopilot_runs 门强制要求完整周期。
func normalizeGate(name GateName, wire wireGate) (Gate, error) {
	action := Action(wire.Action)
	switch action {
	case ActionOff:
		return Gate{Action: ActionOff}, nil
	case ActionEnforce:
	default:
		return Gate{}, ErrInvalidPolicy
	}
	if wire.Limit == nil || *wire.Limit < 0 {
		return Gate{}, ErrInvalidPolicy
	}
	periodFields := 0
	for _, value := range []*time.Time{wire.PeriodStart, wire.PeriodEnd, wire.ResetAt} {
		if value != nil {
			periodFields++
		}
	}
	if periodFields != 0 && periodFields != 3 {
		return Gate{}, ErrInvalidPolicy
	}
	if name == GateAutopilotRuns && periodFields != 3 {
		return Gate{}, ErrInvalidPolicy
	}
	if periodFields == 3 && (!wire.PeriodStart.Before(*wire.PeriodEnd) || !wire.PeriodStart.Before(*wire.ResetAt)) {
		return Gate{}, ErrInvalidPolicy
	}
	limit := *wire.Limit
	gate := Gate{Action: action, Limit: &limit}
	if periodFields == 3 {
		start, end, reset := *wire.PeriodStart, *wire.PeriodEnd, *wire.ResetAt
		gate.PeriodStart, gate.PeriodEnd, gate.ResetAt = &start, &end, &reset
	}
	return gate, nil
}

// decisionFromEntry 从缓存条目构造一条决策。stale=true 且原动作是 enforce 时降级为
// observe——陈旧数据不能再用来拦人。
func decisionFromEntry(entry cacheEntry, name GateName, reason Reason, stale bool) Decision {
	gate := cloneGate(entry.policy.gates[name])
	if stale && gate.Action == ActionEnforce {
		gate.Action = ActionObserve
	}
	return Decision{
		Gate:                gate,
		Reason:              reason,
		PolicyRevision:      entry.policy.policyRevision,
		SubscriptionVersion: entry.policy.subscriptionVersion,
		CloudValidUntil:     entry.policy.cloudValidUntil,
	}
}

// fetchError 是一次拉取失败的细分原因（供观测/日志，不暴露给调用方）。
type fetchError struct {
	outcome string
}

func (e *fetchError) Error() string { return "entitlement policy fetch failed: " + e.outcome }

// statusOutcome 把 HTTP 状态码归类成简短结果词。
func statusOutcome(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "unauthorized"
	case status == http.StatusNotFound:
		return "not_found"
	case status >= 500 && status < 600:
		return "5xx"
	case status >= 400 && status < 500:
		return "4xx"
	default:
		return "status"
	}
}

// refreshOutcome 把刷新错误归类成观测用的结果词。
func refreshOutcome(err error) string {
	var fetchErr *fetchError
	if errors.As(err, &fetchErr) {
		return fetchErr.outcome
	}
	if errors.Is(err, ErrInvalidPolicy) {
		return "invalid_policy"
	}
	return "error"
}

func (c *Client) recordCache(outcome string) {
	if c != nil && c.observer != nil {
		c.observer.RecordEntitlementCache(outcome)
	}
}

func (c *Client) recordRefresh(outcome string, duration time.Duration) {
	if c != nil && c.observer != nil {
		c.observer.RecordEntitlementRefresh(outcome, duration.Seconds())
	}
}

func (c *Client) recordDecision(name GateName, decision Decision) Decision {
	if c != nil && c.observer != nil {
		gate := "unknown"
		if name.valid() {
			gate = string(name)
		}
		c.observer.RecordEntitlementDecision(gate, string(decision.Gate.Action), string(decision.Reason))
	}
	return cloneDecision(decision)
}
