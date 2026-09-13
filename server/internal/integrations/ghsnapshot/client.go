// Package ghsnapshot 从 GitHub API 拉取某个 Pull Request 的 CI + 可合并状态，并把
// 该响应当作 PR 卡片的唯一事实来源（MUL-5265，Plan C）。webhook 与页面访问只负责
// 触发刷新；本包绝不根据 webhook 载荷做增量推断。
//
// 本包分三层：
//   - Client（本文件）：GitHub App 认证——App JWT → 安装访问令牌（按安装缓存、提前
//     续期）→ GraphQL/REST 调用。
//   - snapshot.go：唯一一条 GraphQL 查询、contexts 分页，以及归一化成扁平的
//     逐检查快照。
//   - refresh.go：出站工作队列（去重、每个 PR 单飞行、有界并发、Retry-After 退避）、
//     带 head-SHA 守卫的原子写入，以及触发面（webhook / 页面访问 / TTL 巡检）。
//
// 凭据卫生（验收标准 6）：App 私钥与每一个安装令牌都按不透明秘密对待，绝不写入日志，
// 也绝不嵌进错误消息。
package ghsnapshot

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/sync/singleflight"
)

const (
	defaultAPIBase = "https://api.github.com"
	// 安装令牌在真正过期前这么久就续期，避免飞行中的请求撞上过期边界。GitHub 令牌寿命
	// 为一小时。
	tokenRenewSkew = 5 * time.Minute
)

// RateLimitError 表示 GitHub 要求我们退避。RetryAfter 是重试前应等待的时长，取自
// Retry-After 头，其次 X-RateLimit-Reset 头，都没有则用保守默认值。
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("github rate limited, retry after %s", e.RetryAfter)
}

// Client 是以安装令牌认证的 GitHub API 客户端。nil 的 *Client 是合法的「功能已禁用」
// 取值——刷新管线调用的每个方法都能容忍它——所以没有配置 GitHub App 私钥的部署会干净
// 地降级关闭整个功能（验收标准 4）。
type Client struct {
	appID      string
	privateKey *rsa.PrivateKey
	apiBase    string
	httpClient *http.Client
	now        func() time.Time

	mu     sync.Mutex
	tokens map[int64]cachedToken
	// sf 把同一安装的并发 token 铸造收敛成一次 HTTP 调用，让某个安装的 N 个 worker 在
	// 冷缓存或并发续期时不会各自铸一枚 token。
	sf singleflight.Group
}

type cachedToken struct {
	token  string
	expiry time.Time
}

// NewClientFromEnv 从 GITHUB_APP_ID 与 GITHUB_APP_PRIVATE_KEY 构造 Client。
//
//   - 两者都未设置 → (nil, nil)：本部署没有配置 App API；调用方把整个功能降级关闭。
//   - 密钥存在但损坏 → (nil, err)：属于运维可处理的错误，直接浮出来。
func NewClientFromEnv() (*Client, error) {
	appID := strings.TrimSpace(os.Getenv("GITHUB_APP_ID"))
	pemKey := strings.TrimSpace(os.Getenv("GITHUB_APP_PRIVATE_KEY"))
	if appID == "" || pemKey == "" {
		return nil, nil
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(pemKey))
	if err != nil {
		// 刻意不把密钥材料带进错误。
		return nil, fmt.Errorf("parse GITHUB_APP_PRIVATE_KEY: %w", err)
	}
	return &Client{
		appID:      appID,
		privateKey: key,
		apiBase:    defaultAPIBase,
		httpClient: &http.Client{Timeout: 20 * time.Second},
		now:        time.Now,
		tokens:     map[int64]cachedToken{},
	}, nil
}

// Enabled 报告 App API 是否已配置。nil 客户端即已禁用。
func (c *Client) Enabled() bool { return c != nil && c.privateKey != nil }

// signAppJWT 铸造 GitHub 对 App 认证调用要求的短寿命 RS256 JWT。iat 回拨 60 秒吸收
// 时钟偏移，exp 封顶 9 分钟（GitHub 上限是 10）。
func (c *Client) signAppJWT(now time.Time) (string, error) {
	claims := jwt.MapClaims{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": c.appID,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(c.privateKey)
	if err != nil {
		return "", errors.New("sign App JWT failed")
	}
	return signed, nil
}

// installationToken 返回缓存的安装访问令牌；缓存为空或已进入续期窗口时，经
// POST /app/installations/{id}/access_tokens 铸造新令牌。同一安装的并发调用被
// singleflight 收敛，所以 N 个 worker 下的冷缓存只会铸造一次。
func (c *Client) installationToken(ctx context.Context, installationID int64) (string, error) {
	if tok, ok := c.cachedToken(installationID); ok {
		return tok, nil
	}
	v, err, _ := c.sf.Do(strconv.FormatInt(installationID, 10), func() (any, error) {
		// 等 flight 的间隙，并发调用方可能已经铸造好了。
		if tok, ok := c.cachedToken(installationID); ok {
			return tok, nil
		}
		return c.mintInstallationToken(ctx, installationID)
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// cachedToken 返回仍处于续期窗口外的缓存令牌。
func (c *Client) cachedToken(installationID int64) (string, bool) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.tokens[installationID]; ok && now.Add(tokenRenewSkew).Before(t.expiry) {
		return t.token, true
	}
	return "", false
}

// mintInstallationToken 用 App JWT 换取全新安装令牌并缓存。在 singleflight 下执行，
// 因此每个安装只有一份在跑。
func (c *Client) mintInstallationToken(ctx context.Context, installationID int64) (string, error) {
	now := c.now()
	appJWT, err := c.signAppJWT(now)
	if err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("%s/app/installations/%d/access_tokens", strings.TrimRight(c.apiBase, "/"), installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+appJWT)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return "", rateLimitFromResponse(resp, c.now())
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		// 绝不回显 body——token 铸造失败时 body 可能含敏感线索；诊断只需状态码。
		return "", fmt.Errorf("github installation token: unexpected status %d", resp.StatusCode)
	}
	var parsed struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", errors.New("github installation token: malformed response")
	}
	if parsed.Token == "" {
		return "", errors.New("github installation token: empty token")
	}
	expiry := now.Add(time.Hour)
	if parsed.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, parsed.ExpiresAt); err == nil {
			expiry = t
		}
	}
	c.mu.Lock()
	c.tokens[installationID] = cachedToken{token: parsed.Token, expiry: expiry}
	c.mu.Unlock()
	return parsed.Token, nil
}

// graphQL 以指定安装身份跑一条 GraphQL 查询，返回原始 `data` 对象。GitHub 即使查询级
// 报错也回 HTTP 200，所以这里也检查 `errors` 数组，把 RATE_LIMITED 错误类型映射成
// RateLimitError。
func (c *Client) graphQL(ctx context.Context, installationID int64, query string, variables map[string]any) (json.RawMessage, error) {
	token, err := c.installationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(c.apiBase, "/") + "/graphql"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, rateLimitFromResponse(resp, c.now())
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github graphql: unexpected status %d", resp.StatusCode)
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.New("github graphql: malformed response")
	}
	if len(envelope.Errors) > 0 {
		for _, e := range envelope.Errors {
			if e.Type == "RATE_LIMITED" {
				return nil, &RateLimitError{RetryAfter: time.Minute}
			}
		}
		// 只透出 message 本身，其余不带；GraphQL 错误消息不含凭据。
		return nil, fmt.Errorf("github graphql error: %s", envelope.Errors[0].Message)
	}
	if len(envelope.Data) == 0 {
		return nil, errors.New("github graphql: empty data")
	}
	return envelope.Data, nil
}

// rateLimitFromResponse 根据 GitHub 的限流响应头构造 RateLimitError。Retry-After
// （秒）优先；其次 X-RateLimit-Reset（unix 秒）；都没有则取保守的 60s。等待时长被
// 钳制在 [1s, 5m]。
func rateLimitFromResponse(resp *http.Response, now time.Time) *RateLimitError {
	wait := time.Minute
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
			wait = time.Duration(secs) * time.Second
		}
	} else if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		if unix, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			if d := time.Unix(unix, 0).Sub(now); d > 0 {
				wait = d
			}
		}
	}
	if wait < time.Second {
		wait = time.Second
	}
	if wait > 5*time.Minute {
		wait = 5 * time.Minute
	}
	return &RateLimitError{RetryAfter: wait}
}
