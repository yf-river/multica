package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func loadCommentProjection(ctx context.Context, queries *db.Queries, event events.Event) (commentEventPayload, bool, error) {
	payload, ok := decodeCommentEvent(event)
	if !ok {
		return commentEventPayload{}, false, fmt.Errorf("decode comment-created projection payload")
	}
	commentID, err := util.ParseUUID(payload.Comment.ID)
	if err != nil {
		return commentEventPayload{}, false, fmt.Errorf("projection event has invalid comment ID: %w", err)
	}
	workspaceID, err := util.ParseUUID(event.WorkspaceID)
	if err != nil {
		return commentEventPayload{}, false, fmt.Errorf("projection event has invalid workspace ID: %w", err)
	}
	comment, err := queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
		ID:          commentID,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return payload, false, nil
	}
	if err != nil {
		return commentEventPayload{}, false, fmt.Errorf("load comment before projection: %w", err)
	}
	if util.UUIDToString(comment.IssueID) != payload.Comment.IssueID {
		return commentEventPayload{}, false, fmt.Errorf("comment projection issue mismatch")
	}
	return payload, true, nil
}
