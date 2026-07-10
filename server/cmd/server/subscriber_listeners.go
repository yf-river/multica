package main

import (
	"context"
	"fmt"

	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func registerDurableAudienceConsumers(dispatcher *eventoutbox.Dispatcher) error {
	if err := dispatcher.Register(protocol.EventIssueCreated, "issue_audience", consumeIssueCreatedAudience); err != nil {
		return err
	}
	if err := dispatcher.Register(protocol.EventIssueUpdated, "issue_audience", consumeIssueUpdatedAudience); err != nil {
		return err
	}
	return dispatcher.Register(protocol.EventCommentCreated, "comment_audience", consumeCommentCreatedAudience)
}

func consumeIssueCreatedAudience(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, exists, err := loadIssueProjection(ctx, queries, event, "issue-created")
	if err != nil || !exists {
		return nil, err
	}
	subscriberEvents, err := projectIssueCreatedSubscribers(ctx, queries, event, payload)
	if err != nil {
		return nil, err
	}
	notificationEvents, err := projectIssueCreatedNotifications(ctx, queries, event, payload)
	if err != nil {
		return nil, err
	}
	return append(subscriberEvents, notificationEvents...), nil
}

func consumeIssueUpdatedAudience(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, exists, err := loadIssueProjection(ctx, queries, event, "issue-updated")
	if err != nil || !exists {
		return nil, err
	}
	subscriberEvents, err := projectIssueUpdatedSubscribers(ctx, queries, event, payload)
	if err != nil {
		return nil, err
	}
	notificationEvents, err := projectIssueUpdatedNotifications(ctx, queries, event, payload)
	if err != nil {
		return nil, err
	}
	return append(subscriberEvents, notificationEvents...), nil
}

func consumeCommentCreatedAudience(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, exists, err := loadCommentProjection(ctx, queries, event)
	if err != nil || !exists {
		return nil, err
	}
	if payload.Comment.AuthorType == "system" {
		return nil, nil
	}
	subscriberEvents, err := projectCommentCreatedSubscriber(ctx, queries, event, payload)
	if err != nil {
		return nil, err
	}
	notificationEvents, err := projectCommentCreatedNotifications(ctx, queries, event, payload)
	if err != nil {
		return nil, err
	}
	return append(subscriberEvents, notificationEvents...), nil
}

func consumeIssueCreatedSubscribers(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, exists, err := loadIssueProjection(ctx, queries, event, "issue-created")
	if err != nil || !exists {
		return nil, err
	}
	return projectIssueCreatedSubscribers(ctx, queries, event, payload)
}

func projectIssueCreatedSubscribers(ctx context.Context, queries *db.Queries, event events.Event, payload issueEventPayload) ([]events.Event, error) {
	issue := payload.Issue
	emitted := make([]events.Event, 0, 4)
	appendSubscriber := func(userType, userID, reason string) error {
		created, ok, err := addSubscriber(ctx, queries, issue.WorkspaceID, issue.ID, userType, userID, reason)
		if err != nil {
			return err
		}
		if ok {
			emitted = append(emitted, created)
		}
		return nil
	}
	if err := appendSubscriber(issue.CreatorType, issue.CreatorID, "creator"); err != nil {
		return nil, err
	}
	if issue.AssigneeType != nil && issue.AssigneeID != nil &&
		!(*issue.AssigneeType == issue.CreatorType && *issue.AssigneeID == issue.CreatorID) {
		if err := appendSubscriber(*issue.AssigneeType, *issue.AssigneeID, "assignee"); err != nil {
			return nil, err
		}
	}
	if issue.Description != nil {
		for _, mentioned := range parseMentions(*issue.Description) {
			if err := appendSubscriber(mentioned.Type, mentioned.ID, "mentioned"); err != nil {
				return nil, err
			}
		}
	}
	return emitted, nil
}

func consumeIssueUpdatedSubscribers(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, exists, err := loadIssueProjection(ctx, queries, event, "issue-updated")
	if err != nil || !exists {
		return nil, err
	}
	return projectIssueUpdatedSubscribers(ctx, queries, event, payload)
}

func projectIssueUpdatedSubscribers(ctx context.Context, queries *db.Queries, event events.Event, payload issueEventPayload) ([]events.Event, error) {
	issue := payload.Issue
	emitted := make([]events.Event, 0, 4)
	appendSubscriber := func(userType, userID, reason string) error {
		created, ok, err := addSubscriber(ctx, queries, issue.WorkspaceID, issue.ID, userType, userID, reason)
		if err != nil {
			return err
		}
		if ok {
			emitted = append(emitted, created)
		}
		return nil
	}
	if payload.AssigneeChanged && issue.AssigneeType != nil && issue.AssigneeID != nil {
		if err := appendSubscriber(*issue.AssigneeType, *issue.AssigneeID, "assignee"); err != nil {
			return nil, err
		}
	}
	if payload.DescriptionChanged && issue.Description != nil {
		previous := make(map[string]bool)
		if payload.PrevDescription != nil {
			for _, mentioned := range parseMentions(*payload.PrevDescription) {
				previous[mentioned.Type+":"+mentioned.ID] = true
			}
		}
		for _, mentioned := range parseMentions(*issue.Description) {
			if previous[mentioned.Type+":"+mentioned.ID] {
				continue
			}
			if err := appendSubscriber(mentioned.Type, mentioned.ID, "mentioned"); err != nil {
				return nil, err
			}
		}
	}
	return emitted, nil
}

func consumeCommentCreatedSubscriber(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, exists, err := loadCommentProjection(ctx, queries, event)
	if err != nil || !exists {
		return nil, err
	}
	return projectCommentCreatedSubscriber(ctx, queries, event, payload)
}

func projectCommentCreatedSubscriber(ctx context.Context, queries *db.Queries, event events.Event, payload commentEventPayload) ([]events.Event, error) {
	comment := payload.Comment
	if comment.AuthorType == "system" || comment.AuthorID == "" {
		return nil, nil
	}
	created, ok, err := addSubscriber(ctx, queries, event.WorkspaceID, comment.IssueID, comment.AuthorType, comment.AuthorID, "commenter")
	if err != nil || !ok {
		return nil, err
	}
	return []events.Event{created}, nil
}

func supportsIssueSubscriberUserType(userType string) bool {
	return userType == "member" || userType == "agent"
}

func addSubscriber(
	ctx context.Context,
	queries *db.Queries,
	workspaceID string,
	issueID string,
	userType string,
	userID string,
	reason string,
) (events.Event, bool, error) {
	if !supportsIssueSubscriberUserType(userType) {
		return events.Event{}, false, nil
	}
	parsedIssueID, err := util.ParseUUID(issueID)
	if err != nil {
		return events.Event{}, false, fmt.Errorf("subscriber projection has invalid issue ID: %w", err)
	}
	parsedUserID, err := util.ParseUUID(userID)
	if err != nil {
		return events.Event{}, false, fmt.Errorf("subscriber projection has invalid %s ID: %w", userType, err)
	}
	if err := queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
		IssueID:  parsedIssueID,
		UserType: userType,
		UserID:   parsedUserID,
		Reason:   reason,
	}); err != nil {
		return events.Event{}, false, fmt.Errorf("add %s issue subscriber %s: %w", reason, userID, err)
	}
	return events.Event{
		Type:        protocol.EventSubscriberAdded,
		WorkspaceID: workspaceID,
		Payload: map[string]any{
			"issue_id":  issueID,
			"user_type": userType,
			"user_id":   userID,
			"reason":    reason,
		},
	}, true, nil
}

func publishProjectedEvents(bus *events.Bus, emitted []events.Event) {
	for _, event := range emitted {
		bus.Publish(event)
	}
}
