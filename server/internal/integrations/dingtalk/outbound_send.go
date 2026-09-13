// outbound_send.go：出站发送的共享路径——EventChatDone 订阅器（outbound.go）
// 与 OutboundReplier（replier.go）都用它。
//
// 它把一段回复正文变成一条或多条钉钉 sampleMarkdown 消息，用该安装的
// access_token 发出，并按 1:1 / 群聊选择正确的机器人端点。
package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// msgKeyMarkdown 渲染一张 {title, text} 的 sampleMarkdown 卡片。
	msgKeyMarkdown = "sampleMarkdown"

	// 下面是机器人发消息的两个端点：p2p（1:1 主动推送）与群聊发送。
	pathSendP2P   = "/v1.0/robot/oToMessages/batchSend"
	pathSendGroup = "/v1.0/robot/groupMessages/send"
)

// sendTarget 是回复解析后的钉钉投递目标。ConversationType 决定走哪个端点；
// StaffID 是 1:1 发送的接收人；ConversationID 是群发要用的群 openConversationId。
type sendTarget struct {
	ConversationType string
	ConversationID   string
	StaffID          string
}

// sender 为某个安装发回复。robotCode + 凭据来自安装；共享的 Client 持有令牌缓存
// 与传输层。
type sender struct {
	client    *Client
	robotCode string
	appKey    string
	appSecret string
}

// markdownParam 是 sampleMarkdown 消息的 msgParam 负载。
type markdownParam struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// send 把 text 以一条或多条 sampleMarkdown 消息发给 target（按钉钉单条字节上限
// 分片），返回最后一条消息的发送 key。遇到 401 会做一次令牌刷新 + 重试，覆盖
// 「缓存填好之后、使用之前服务端把令牌吊销了」的窗口。
func (s *sender) send(ctx context.Context, target sendTarget, text string) (string, error) {
	if text == "" {
		return "", nil
	}
	title := markdownTitle(text)
	var lastKey string
	for _, chunk := range chunkMarkdown(text) {
		param, err := json.Marshal(markdownParam{Title: title, Text: chunk})
		if err != nil {
			return "", fmt.Errorf("marshal msgParam: %w", err)
		}
		key, err := s.sendOne(ctx, target, string(param))
		if err != nil {
			return "", err
		}
		lastKey = key
	}
	return lastKey, nil
}

// sendOne 发单条已渲染的消息，401 时刷新一次令牌重试。
func (s *sender) sendOne(ctx context.Context, target sendTarget, msgParam string) (string, error) {
	path, body, err := s.request(target, msgParam)
	if err != nil {
		return "", err
	}
	var resp struct {
		ProcessQueryKey string `json:"processQueryKey"`
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, err := s.client.accessToken(ctx, s.appKey, s.appSecret)
		if err != nil {
			return "", fmt.Errorf("access token: %w", err)
		}
		err = s.client.postJSON(ctx, path, token, body, &resp)
		if err == nil {
			return resp.ProcessQueryKey, nil
		}
		if errors.Is(err, errUnauthorized) && attempt == 0 {
			s.client.invalidate(s.appKey)
			continue
		}
		return "", err
	}
	return "", errUnauthorized
}

// request 按目标类型拼出端点与请求体。1:1 发送要接收人的 staff id；群发送要群的
// openConversationId。
func (s *sender) request(target sendTarget, msgParam string) (string, map[string]any, error) {
	if target.ConversationType == convTypeP2P {
		if target.StaffID == "" {
			return "", nil, errors.New("dingtalk: 1:1 send missing recipient staff id")
		}
		return pathSendP2P, map[string]any{
			"robotCode": s.robotCode,
			"userIds":   []string{target.StaffID},
			"msgKey":    msgKeyMarkdown,
			"msgParam":  msgParam,
		}, nil
	}
	if target.ConversationID == "" {
		return "", nil, errors.New("dingtalk: group send missing conversation id")
	}
	return pathSendGroup, map[string]any{
		"robotCode":          s.robotCode,
		"openConversationId": target.ConversationID,
		"msgKey":             msgKeyMarkdown,
		"msgParam":           msgParam,
	}, nil
}
