// ws_frame.go：钉钉 Stream 帧的模型——在 WebSocket 上跑的是带 JSON 信封的帧。
// 本文件定义入站 DataFrame、我们要回写的 DataFrameResponse，以及聊天机器人连接
// 用到的那一小撮 type/topic 判别常量，替代原先对厂商 Stream SDK payload 包的
// 依赖。
package dingtalk

const (
	frameTypeSystem   = "SYSTEM"
	frameTypeCallback = "CALLBACK"

	systemTopicPing       = "ping"
	systemTopicDisconnect = "disconnect"
	botMessageTopic       = "/v1.0/im/bot/messages/get"

	frameResponseCodeOK = 200
)

// dataFrame 是一条入站 Stream 帧。网关把路由键放在 headers.topic、把关联 id 放
// 在 headers.messageId；data 是帧体——以 JSON 字符串承载（对机器人消息主题而言
// 就是一条 bot 回调）。
type dataFrame struct {
	Type    string            `json:"type"`
	Headers map[string]string `json:"headers"`
	Data    string            `json:"data"`
}

func (f *dataFrame) topic() string     { return f.Headers["topic"] }
func (f *dataFrame) messageID() string { return f.Headers["messageId"] }

// dataFrameResponse 是每帧都要回写的 ack。网关靠回显的 messageId 头把这帧 ack
// 与送达的帧对上，所以那个头是承重的；普通回调 ack 的 data 留空（真正的回复经
// Open API 带外投送）。
type dataFrameResponse struct {
	Code    int               `json:"code"`
	Headers map[string]string `json:"headers"`
	Message string            `json:"message"`
	Data    string            `json:"data"`
}

// newAckResponse 构造一条 200 ack，回显帧的 messageId。
func newAckResponse(messageID string) dataFrameResponse {
	return dataFrameResponse{
		Code:    frameResponseCodeOK,
		Message: "",
		Data:    "",
		Headers: map[string]string{
			"messageId":   messageID,
			"contentType": "application/json",
		},
	}
}

// newPongResponse 应答 SYSTEM ping：回显 ping 的 messageId 与 data 负载，镜像
// 网关期望的 pong 结构。
func newPongResponse(messageID, data string) dataFrameResponse {
	return dataFrameResponse{
		Code:    frameResponseCodeOK,
		Message: "ok",
		Data:    data,
		Headers: map[string]string{
			"messageId":   messageID,
			"contentType": "application/json",
		},
	}
}
