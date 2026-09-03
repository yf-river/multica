package service

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// noteRuntimeUnusable leaves the refusal on the issue when an assignment cannot
// be enqueued because the assignee's agent CLI cannot run on its machine.
//
// Assignment is the trigger with no reply anyone reads: the API returns the
// updated issue, and nothing in it says "and by the way, nothing will run".
// Without this the user gets exactly the silence this change exists to remove
// (MUL-6164).
//
// Best-effort: the refusal is correct whether or not the note lands, so a
// failure here is logged rather than returned.
func (s *IssueService) noteRuntimeUnusable(ctx context.Context, issue db.Issue, verdict AgentVerdict) {
	if s.TxStarter == nil {
		slog.Warn("runtime unusable notice: transaction starter unavailable", "issue_id", util.UUIDToString(issue.ID))
		return
	}
	name := ""
	if agent, err := s.Queries.GetAgent(ctx, issue.AssigneeID); err == nil {
		name = agent.Name
	}
	// author_type='system', author_id=zero UUID. The zero UUID is a valid 16
	// byte value and the column is NOT NULL; clients branch on author_type.
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		slog.Warn("runtime unusable notice: begin transaction failed", "error", err, "issue_id", util.UUIDToString(issue.ID))
		return
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	created, err := qtx.CreateComment(ctx, db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     RuntimeUnusableNotice(name, verdict),
		Type:        "system",
		ParentID:    pgtype.UUID{Valid: false},
	})
	if err != nil {
		slog.Warn("runtime unusable notice: create system comment failed",
			"error", err, "issue_id", util.UUIDToString(issue.ID))
		return
	}
	comment := created.Comment()
	commentFields := commentEventFields(comment)
	commentFields["revision"] = comment.Revision
	event, err := eventoutbox.Enqueue(ctx, qtx, events.Event{
		Type:           protocol.EventCommentCreated,
		IdempotencyKey: "comment:runtime-unusable:" + util.UUIDToString(comment.ID),
		StreamKey:      "issue:" + util.UUIDToString(issue.ID),
		WorkspaceID:    util.UUIDToString(issue.WorkspaceID),
		ActorType:      "system",
		Payload: map[string]any{
			"comment":        commentFields,
			"issue_title":    issue.Title,
			"issue_status":   issue.Status,
			"issue_revision": created.IssueRevision,
		},
	})
	if err != nil {
		slog.Warn("runtime unusable notice: enqueue event failed", "error", err, "issue_id", util.UUIDToString(issue.ID))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("runtime unusable notice: commit transaction failed", "error", err, "issue_id", util.UUIDToString(issue.ID))
		return
	}
	publishCommittedEvent(s.Bus, event)
}
