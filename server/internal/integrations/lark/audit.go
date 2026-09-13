// audit.go：AuditLogger 的 DB 实现，负责把被丢弃的入站事件写入审计表。
package lark

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// dbAuditLogger 是基于 lark_inbound_audit 表的 AuditLogger 具体实现。它刻意
// 不缓存、不做任何间接层——RecordLarkInboundDrop 查询会拒绝任何可能携带
// 消息正文的列，这个结构体同样如此。
type dbAuditLogger struct {
	queries *ChannelStore
}

// NewAuditLogger 返回一个把丢弃事件写入 channel_inbound_audit 表的
// AuditLogger。接口签名不接受消息正文，对应设计文档 §4.7（只做非正文审计）。
func NewAuditLogger(queries *db.Queries) AuditLogger {
	return &dbAuditLogger{queries: NewChannelStore(queries)}
}

// RecordDrop 实现 AuditLogger，把一次丢弃事件落到审计表。
func (l *dbAuditLogger) RecordDrop(ctx context.Context, p AuditDropParams) error {
	return l.queries.RecordLarkInboundDrop(ctx, RecordInboundDropParams{
		EventType:        p.EventType,
		DropReason:       string(p.Reason),
		InstallationID:   p.InstallationID,
		ChannelChatID:    textIfNonEmpty(string(p.ChatID)),
		ChannelEventID:   textIfNonEmpty(p.LarkEventID),
		ChannelMessageID: textIfNonEmpty(p.LarkMessageID),
	})
}

// textIfNonEmpty 对非空字符串返回 Valid 的 pgtype.Text，否则返回 NULL。
// 目的是避免在审计表里存字面空串——空串会掩盖“事件缺少该字段”和“该字段
// 被刻意置空”这两种情况的区别。
func textIfNonEmpty(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
