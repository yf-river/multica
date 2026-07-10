package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// UnresolveThreadOnReply atomically reopens a resolved thread within the
// caller's transaction. The returned realtime event must only be published
// after that transaction commits.
func UnresolveThreadOnReply(
	ctx context.Context,
	queries *db.Queries,
	root *db.Comment,
	workspaceID string,
	actorType string,
	actorID string,
) (events.Event, bool, error) {
	if root == nil {
		return events.Event{}, false, nil
	}

	updated, err := queries.UnresolveCommentIfResolved(ctx, root.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return events.Event{}, false, nil
	}
	if err != nil {
		return events.Event{}, false, fmt.Errorf("unresolve thread root %s: %w", util.UUIDToString(root.ID), err)
	}

	return events.Event{
		Type:        protocol.EventCommentUnresolved,
		WorkspaceID: workspaceID,
		ActorType:   actorType,
		ActorID:     actorID,
		Payload: map[string]any{
			"comment": map[string]any{
				"id":               util.UUIDToString(updated.ID),
				"issue_id":         util.UUIDToString(updated.IssueID),
				"author_type":      updated.AuthorType,
				"author_id":        util.UUIDToString(updated.AuthorID),
				"content":          updated.Content,
				"type":             updated.Type,
				"parent_id":        util.UUIDToPtr(updated.ParentID),
				"created_at":       util.TimestampToString(updated.CreatedAt),
				"updated_at":       util.TimestampToString(updated.UpdatedAt),
				"resolved_at":      util.TimestampToPtr(updated.ResolvedAt),
				"resolved_by_type": util.TextToPtr(updated.ResolvedByType),
				"resolved_by_id":   util.UUIDToPtr(updated.ResolvedByID),
			},
		},
	}, true, nil
}
