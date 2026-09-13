// token.go：出站用的钉钉 Open-API client——一个 access_token 缓存 + 一个 JSON
// POST 帮手。
//
// 与 Slack 的静态 bot 令牌不同，钉钉的 access_token 有有效期（约 2 小时），所以
// 这里按 AppKey 在进程内缓存、临近过期前刷新——与飞书 tenant_access_token 缓存
// 同一个套路。
package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// tokenSafetyMargin 从钉钉给的 expireIn 里扣掉的安全余量：令牌在真正过期前就被
// 刷新，吸收时钟偏差与「已在路上」的在途使用。
const tokenSafetyMargin = 5 * time.Minute

// tokenMintTimeout 独立于任何一个调用方地约束共享铸令牌请求。调用方可以提前放
// 弃自己的等待，而不会取消同一 AppKey 下其它调用方经 singleflight 共享的请求。
const tokenMintTimeout = 10 * time.Second

// Client 缓存 access_token 并投递机器人消息。所有安装共享同一个实例；缓存按
// AppKey 分键，每个安装的令牌互不干扰。并发安全。
type Client struct {
	httpClient *http.Client
	apiBase    string
	now        func() time.Time

	mu     sync.Mutex
	tokens map[string]cachedToken

	// minting 把同一 AppKey 的并发缓存未命中压成同一条在途的令牌请求。
	minting singleflight.Group
}

// cachedToken 是一条已缓存的令牌：值 + 过期时刻。
type cachedToken struct {
	value     string
	expiresAt time.Time
}

// NewClient 构造出站 client。apiBase 缺省指向钉钉 Open-API 主机；测试可指向
// httptest server。
func NewClient(httpClient *http.Client, apiBase string) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	return &Client{
		httpClient: httpClient,
		apiBase:    strings.TrimRight(apiBase, "/"),
		now:        time.Now,
		tokens:     map[string]cachedToken{},
	}
}

// accessToken 返回 (appKey, appSecret) 可用的 access_token；缓存空或过期时铸一个
// 并缓存。
func (c *Client) accessToken(ctx context.Context, appKey, appSecret string) (string, error) {
	if t, ok := c.cachedToken(appKey); ok {
		return t, nil
	}

	// 把同一 AppKey 的并发未命中压成一次铸令牌：一批出站发送（ack + reply）撞上
	// 同一枚过期令牌时，各自并发铸令牌既浪费又会在写缓存时互相踩。钉钉对令牌
	// 发放是限流的，汇流也避免触发那个限额。
	result := c.minting.DoChan(appKey, func() (any, error) {
		// 排队等 flight 的功夫里，前面的铸令牌可能已经刷好缓存了；直接复用，
		// 别再取一次。
		if t, ok := c.cachedToken(appKey); ok {
			return t, nil
		}
		mintCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenMintTimeout)
		defer cancel()
		token, expireIn, err := fetchAccessToken(mintCtx, c.httpClient, c.apiBase, appKey, appSecret)
		if err != nil {
			return "", err
		}
		ttl := time.Duration(expireIn) * time.Second
		if ttl < tokenSafetyMargin*2 {
			ttl = tokenSafetyMargin * 2
		}
		c.mu.Lock()
		// 存的是「过期时刻」而不是原生 ttl：先扣掉安全余量再算过期点，令牌
		// 实际被换新总比它真的死掉早。
		c.tokens[appKey] = cachedToken{value: token, expiresAt: c.now().Add(ttl - tokenSafetyMargin)}
		c.mu.Unlock()
		return token, nil
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case res := <-result:
		if res.Err != nil {
			return "", res.Err
		}
		return res.Val.(string), nil
	}
}

// cachedToken 返回 appKey 的缓存令牌——存在且未过期时。
func (c *Client) cachedToken(appKey string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.tokens[appKey]; ok && t.expiresAt.After(c.now()) {
		return t.value, true
	}
	return "", false
}

// invalidate 丢掉 appKey 的缓存令牌，让下一次 accessToken 重新取。API 报了
// 过期/无效令牌（HTTP 401）之后调用。
func (c *Client) invalidate(appKey string) {
	c.mu.Lock()
	delete(c.tokens, appKey)
	c.mu.Unlock()
}

// messageFileDownloadURL 通过 POST /v1.0/robot/messageFiles/download 把机器人
// 收到消息的 downloadCode 换成临时下载 URL。遇到 401 会刷新一次缓存令牌（与
// sender.sendOne 同一套路）。返回的 URL 是短命的带签链接——拿到就立刻下载。
func (c *Client) messageFileDownloadURL(ctx context.Context, appKey, appSecret, robotCode, downloadCode string) (string, error) {
	body := map[string]string{"robotCode": robotCode, "downloadCode": downloadCode}
	var out messageFileDownloadResponse
	token, err := c.accessToken(ctx, appKey, appSecret)
	if err != nil {
		return "", err
	}
	err = c.postJSON(ctx, messageFilesDownloadPath, token, body, &out)
	if errors.Is(err, errUnauthorized) {
		c.invalidate(appKey)
		if token, err = c.accessToken(ctx, appKey, appSecret); err != nil {
			return "", err
		}
		err = c.postJSON(ctx, messageFilesDownloadPath, token, body, &out)
	}
	if err != nil {
		return "", err
	}
	if out.DownloadUrl == "" {
		return "", fmt.Errorf("dingtalk: messageFiles/download returned empty downloadUrl")
	}
	return out.DownloadUrl, nil
}

// postJSON 向 path 发一个带 access token 头的 POST，并把 2xx 响应解码进 out
// （out 可为 nil 表示忽略响应体）。HTTP 401 返回 errUnauthorized，让调用方能刷
// 新令牌后重试。
func (c *Client) postJSON(ctx context.Context, path, accessToken string, body, out any) error {
	reqBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("dingtalk: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+path, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("dingtalk: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("dingtalk: request %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusUnauthorized {
		return errUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr apiError
		_ = json.Unmarshal(respBody, &apiErr)
		return &apiRequestError{
			Path:       path,
			StatusCode: resp.StatusCode,
			Code:       apiErr.Code,
			Message:    apiErr.Message,
		}
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("dingtalk: decode %s response: %w", path, err)
		}
	}
	return nil
}
