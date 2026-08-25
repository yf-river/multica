package handler

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type commentEnrichment struct {
	reactions   map[string][]ReactionResponse
	attachments map[string][]AttachmentResponse
}

func (e commentEnrichment) response(comment db.Comment) CommentResponse {
	id := uuidToString(comment.ID)
	return commentToResponse(comment, e.reactions[id], e.attachments[id])
}

func (h *Handler) loadCommentEnrichment(ctx context.Context, queries *db.Queries, workspaceID pgtype.UUID, commentIDs []pgtype.UUID) (commentEnrichment, error) {
	reactions, err := loadCommentReactions(ctx, queries, commentIDs)
	if err != nil {
		return commentEnrichment{}, err
	}
	attachments, err := h.loadCommentAttachments(ctx, queries, workspaceID, commentIDs)
	if err != nil {
		return commentEnrichment{}, err
	}
	return commentEnrichment{reactions: reactions, attachments: attachments}, nil
}

func loadCommentReactions(ctx context.Context, queries *db.Queries, commentIDs []pgtype.UUID) (map[string][]ReactionResponse, error) {
	if len(commentIDs) == 0 {
		return nil, nil
	}
	reactions, err := queries.ListReactionsByCommentIDs(ctx, commentIDs)
	if err != nil {
		return nil, fmt.Errorf("list comment reactions: %w", err)
	}
	grouped := make(map[string][]ReactionResponse, len(commentIDs))
	for _, reaction := range reactions {
		commentID := uuidToString(reaction.CommentID)
		grouped[commentID] = append(grouped[commentID], reactionToResponse(reaction))
	}
	return grouped, nil
}

func (h *Handler) loadCommentAttachments(ctx context.Context, queries *db.Queries, workspaceID pgtype.UUID, commentIDs []pgtype.UUID) (map[string][]AttachmentResponse, error) {
	if len(commentIDs) == 0 {
		return nil, nil
	}
	attachments, err := queries.ListAttachmentsByCommentIDs(ctx, db.ListAttachmentsByCommentIDsParams{
		Column1:     commentIDs,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return nil, fmt.Errorf("list comment attachments: %w", err)
	}
	grouped := make(map[string][]AttachmentResponse, len(commentIDs))
	for _, attachment := range attachments {
		commentID := uuidToString(attachment.CommentID)
		grouped[commentID] = append(grouped[commentID], h.attachmentToResponse(attachment))
	}
	return grouped, nil
}
