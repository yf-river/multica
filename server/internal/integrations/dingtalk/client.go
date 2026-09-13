// client.go：钉钉 Open-API REST 的最小封装——安装与出站路径共用的 HTTP 缝。
// 目前只做一件事：从 AppKey/AppSecret 现铸 access_token。
//
// 刻意用 net/http 手写而不是上 Stream SDK：服务器在 Stream 连接之外要发的
// REST 调用只有「铸令牌」和「发消息」这两个；收在这里，两者都能借着 apiBase
// 覆盖、对着 httptest server 轻松测试。
//
// 钉钉 Open-API 的并发保护：401 会被翻译成 errUnauthorized，让出站发送器丢掉
// 缓存的 access_token、用新令牌重试一次（令牌过期是这类调用的常态而非异常）。
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
)

// errUnauthorized 由 postJSON 在收到 HTTP 401 时返回，让出站发送器丢掉缓存的
// access_token 并拿新令牌重试一次。
var errUnauthorized = errors.New("dingtalk: unauthorized (access token expired or invalid)")

// defaultAPIBase 是钉钉 Open-API 主机。大陆云是钉钉这些端点唯一开放的部署区域，
// 因此与飞书不同，没有按安装分地域的问题。
const defaultAPIBase = "https://api.dingtalk.com"

// accessTokenPath 用应用的 AppKey/AppSecret 铸一个企业内部应用的 access_token。
// 响应里带令牌本身和以秒计的有效期。
const accessTokenPath = "/v1.0/oauth2/accessToken"

// accessTokenResponse 是 accessTokenPath 的成功响应结构。
type accessTokenResponse struct {
	AccessToken string `json:"accessToken"`
	ExpireIn    int64  `json:"expireIn"`
}

// apiError 是钉钉 Open-API 的错误信封（非 2xx 响应）。
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// apiRequestError 保留钉钉机器可读的错误码，让调用方能区分「可处理的权限问题」
// 与「一次性的瞬时故障」。
type apiRequestError struct {
	Path       string
	StatusCode int
	Code       string
	Message    string
}

func (e *apiRequestError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("dingtalk: %s: code=%q message=%q", e.Path, e.Code, e.Message)
	}
	return fmt.Sprintf("dingtalk: %s: http %d", e.Path, e.StatusCode)
}

// messageFilesDownloadPath 把机器人消息的 downloadCode 解析成一条短命下载 URL。
// code 和返回的 URL 都是临时的（钉钉没文档化确切 TTL）——拿到就立刻下载，两样
// 都绝不落库或写日志。
const messageFilesDownloadPath = "/v1.0/robot/messageFiles/download"

// messageFileDownloadResponse 是 messageFilesDownloadPath 的成功响应结构。
type messageFileDownloadResponse struct {
	DownloadUrl string `json:"downloadUrl"`
}

// fetchAccessToken 为 (appKey, appSecret) 铸一个 access_token。baseURL 缺省指
// 向钉钉 Open-API 主机，测试可指向 httptest server；httpClient 缺省用
// http.DefaultClient。返回令牌与以秒计的有效期。非 2xx 响应或缺令牌都算错误——
// 安装路径正是把这里的失败当「这些凭据是错的」。
func fetchAccessToken(ctx context.Context, httpClient *http.Client, baseURL, appKey, appSecret string) (string, int64, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	base := baseURL
	if base == "" {
		base = defaultAPIBase
	}
	base = strings.TrimRight(base, "/")

	reqBody, err := json.Marshal(map[string]string{"appKey": appKey, "appSecret": appSecret})
	if err != nil {
		return "", 0, fmt.Errorf("dingtalk: marshal access token request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+accessTokenPath, bytes.NewReader(reqBody))
	if err != nil {
		return "", 0, fmt.Errorf("dingtalk: build access token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("dingtalk: access token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, fmt.Errorf("dingtalk: read access token response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr apiError
		_ = json.Unmarshal(body, &apiErr)
		if apiErr.Message != "" {
			return "", 0, fmt.Errorf("dingtalk: access token: code=%q message=%q", apiErr.Code, apiErr.Message)
		}
		return "", 0, fmt.Errorf("dingtalk: access token: http %d", resp.StatusCode)
	}

	var tok accessTokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", 0, fmt.Errorf("dingtalk: decode access token response: %w", err)
	}
	if tok.AccessToken == "" {
		return "", 0, fmt.Errorf("dingtalk: access token response missing accessToken")
	}
	return tok.AccessToken, tok.ExpireIn, nil
}
