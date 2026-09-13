// chat.go：飞书入站事件“丢弃审计”的接缝。原本在此（以及 chat_service.go）
// 的会话 ensure/append//issue 机制，已上移到与渠道无关的 engine.ChatSession
// （MUL-3516），飞书现在通过 NewFeishuResolverSet 使用它——飞书专属的会话
// 服务已不存在。本文件留下的是仍带飞书形态的入站丢弃审计。

package lark

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

// AuditLogger 把被丢弃的入站事件写入 lark_inbound_audit。该接口刻意不接收
// 消息正文——丢弃审计策略见 MUL-2671 §4.7。
type AuditLogger interface {
	// RecordDrop 记录一次被丢弃的入站事件。
	RecordDrop(ctx context.Context, p AuditDropParams) error
}

// AuditDropParams 是记录丢弃事件所需的参数集合。
type AuditDropParams struct {
	InstallationID pgtype.UUID // 没有安装记录的事件（如安装前事件）允许为 invalid
	ChatID         ChatID
	EventType      string
	LarkEventID    string
	LarkMessageID  string
	Reason         DropReason
}
