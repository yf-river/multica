package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func registerDurableReactionConsumers(dispatcher *eventoutbox.Dispatcher) error {
	if err := dispatcher.Register(protocol.EventIssueReactionAdded, "reaction_notification", consumeIssueReactionNotification); err != nil {
		return err
	}
	return dispatcher.Register(protocol.EventReactionAdded, "reaction_notification", consumeCommentReactionNotification)
}

func consumeIssueReactionNotification(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, ok := decodeIssueReactionEvent(event)
	if !ok {
		return nil, fmt.Errorf("decode issue reaction notification payload")
	}
	reaction := payload.Reaction
	if err := validateReactionActor(event, reaction.ActorType, reaction.ActorID); err != nil {
		return nil, err
	}
	issue, exists, err := getIssueForProjection(ctx, queries, eventIssue{
		ID:          reaction.IssueID,
		WorkspaceID: event.WorkspaceID,
	})
	if err != nil || !exists {
		return nil, err
	}
	details, _ := json.Marshal(map[string]string{"emoji": reaction.Emoji})
	collector := newNotificationEventCollector()
	if err := notifyDirect(
		ctx,
		queries,
		collector.bus,
		issue.CreatorType,
		util.UUIDToString(issue.CreatorID),
		event.WorkspaceID,
		event,
		reaction.IssueID,
		issue.Status,
		"reaction_added",
		"info",
		issue.Title,
		"",
		details,
	); err != nil {
		return nil, fmt.Errorf("notify issue reaction recipient: %w", err)
	}
	return collector.events, nil
}

func consumeCommentReactionNotification(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, ok := decodeCommentReactionEvent(event)
	if !ok {
		return nil, fmt.Errorf("decode comment reaction notification payload")
	}
	reaction := payload.Reaction
	if err := validateReactionActor(event, reaction.ActorType, reaction.ActorID); err != nil {
		return nil, err
	}
	commentID, err := util.ParseUUID(reaction.CommentID)
	if err != nil {
		return nil, fmt.Errorf("reaction event has invalid comment ID: %w", err)
	}
	workspaceID, err := util.ParseUUID(event.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("reaction event has invalid workspace ID: %w", err)
	}
	comment, err := queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
		ID:          commentID,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load comment before reaction projection: %w", err)
	}
	issue, err := queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          comment.IssueID,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load issue before comment reaction projection: %w", err)
	}
	details, _ := json.Marshal(map[string]string{
		"emoji":      reaction.Emoji,
		"comment_id": reaction.CommentID,
	})
	collector := newNotificationEventCollector()
	if err := notifyDirect(
		ctx,
		queries,
		collector.bus,
		comment.AuthorType,
		util.UUIDToString(comment.AuthorID),
		event.WorkspaceID,
		event,
		util.UUIDToString(issue.ID),
		issue.Status,
		"reaction_added",
		"info",
		issue.Title,
		"",
		details,
	); err != nil {
		return nil, fmt.Errorf("notify comment reaction recipient: %w", err)
	}
	return collector.events, nil
}

func validateReactionActor(event events.Event, actorType, actorID string) error {
	if actorType != event.ActorType || actorID != event.ActorID {
		return fmt.Errorf("reaction event actor mismatch")
	}
	if _, err := util.ParseUUID(actorID); err != nil {
		return fmt.Errorf("reaction event has invalid actor ID: %w", err)
	}
	return nil
}
