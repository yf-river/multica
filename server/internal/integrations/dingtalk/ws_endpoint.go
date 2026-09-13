// ws_endpoint.go：打开一条钉钉 Stream 连接——向网关 POST 一次，换回一次性
// WebSocket 端点 + ticket。
//
// 本文件替代厂商 SDK 的 client.Start 握手：POST /v1.0/gateway/connections/open
// 返回的 URL（endpoint 上拼好 ticket）就是连接器要拨的地址。apiBase 与
// httpClient 沿用共享的出站 Client，测试可把它们指向 httptest server。
package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	connectionsOpenPath = "/v1.0/gateway/connections/open"
	streamUserAgent     = "multica-dingtalk/1.0"
	openConnectTimeout  = 5 * time.Second
)

// streamSubscription 是 open 请求里的一个 {type, topic} 订阅条目。聊天机器人
// 连接订阅两个 SYSTEM 控制主题 + 一个机器人消息回调主题。
type streamSubscription struct {
	Type  string `json:"type"`
	Topic string `json:"topic"`
}

// chatbotSubscriptions 是机器人消息流固定的订阅集合：心跳 ping、断连通知，以及
// 机器人消息回调。
func chatbotSubscriptions() []streamSubscription {
	return []streamSubscription{
		{Type: frameTypeSystem, Topic: systemTopicPing},
		{Type: frameTypeSystem, Topic: systemTopicDisconnect},
		{Type: frameTypeCallback, Topic: botMessageTopic},
	}
}

// openConnectionRequest 是 connections/open 的请求体：凭据 + 订阅清单。
type openConnectionRequest struct {
	ClientID      string               `json:"clientId"`
	ClientSecret  string               `json:"clientSecret"`
	Subscriptions []streamSubscription `json:"subscriptions"`
	UserAgent     string               `json:"ua"`
}

// openConnectionResponse 是 connections/open 的成功响应：可拨的端点与一次性票。
type openConnectionResponse struct {
	Endpoint string `json:"endpoint"`
	Ticket   string `json:"ticket"`
}

// openConnection 注册一条 Stream 连接，返回可以直接拨号的 wss URL
// （endpoint?ticket=…）。apiBase + httpClient 来自共享的出站 Client，测试可指向
// httptest server。
func openConnection(ctx context.Context, httpClient *http.Client, apiBase, appKey, appSecret string) (string, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	reqBody, err := json.Marshal(openConnectionRequest{
		ClientID:      appKey,
		ClientSecret:  appSecret,
		Subscriptions: chatbotSubscriptions(),
		UserAgent:     streamUserAgent,
	})
	if err != nil {
		return "", fmt.Errorf("marshal open request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, openConnectTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+connectionsOpenPath, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("build open request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("open connection: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("open connection: status %d: %s", resp.StatusCode, string(body))
	}
	var out openConnectionResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode open response: %w", err)
	}
	if out.Endpoint == "" || out.Ticket == "" {
		return "", fmt.Errorf("open connection: empty endpoint or ticket")
	}
	endpoint, err := url.Parse(out.Endpoint)
	// 强校验只接受 wss 且带主机的端点：ticket 会拼在 URL 查询串里当凭据用，
	// 走非 TLS 或不可信地址等于把入场券交给中间人。
	if err != nil || endpoint.Scheme != "wss" || endpoint.Host == "" {
		return "", errors.New("open connection: invalid secure websocket endpoint")
	}
	query := endpoint.Query()
	query.Set("ticket", out.Ticket)
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}
