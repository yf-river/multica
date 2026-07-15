package lark

import (
	"context"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// NewAuditLogger returns the exact metadata-only write function used by the
// dispatcher. The signature cannot accept a message body, so inbound content
// cannot leak into lark_inbound_audit.
func NewAuditLogger(queries *db.Queries) func(context.Context, AuditDropParams) error {
	return func(ctx context.Context, p AuditDropParams) error {
		return queries.RecordLarkInboundDrop(ctx, db.RecordLarkInboundDropParams{
			EventType:      p.EventType,
			DropReason:     string(p.Reason),
			InstallationID: p.InstallationID,
			LarkChatID:     textOrNull(string(p.ChatID)),
			LarkEventID:    textOrNull(p.LarkEventID),
			LarkMessageID:  textOrNull(p.LarkMessageID),
		})
	}
}
