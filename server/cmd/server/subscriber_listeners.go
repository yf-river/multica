package main

import (
	"context"
	"log/slog"

	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// registerSubscriberListeners wires up event bus listeners that auto-subscribe
// relevant users to issues. This ensures creators, assignees, and commenters
// are automatically tracked as issue subscribers.
func registerSubscriberListeners(bus *events.Bus, queries *db.Queries) {
	// issue:created — subscribe creator + assignee (if different)
	bus.Subscribe(protocol.EventIssueCreated, func(e events.Event) {
		payload, ok := decodeIssueEvent(e)
		if !ok {
			return
		}
		issue := payload.Issue

		// Subscribe the creator
		addSubscriber(bus, queries, e.WorkspaceID, issue.ID, issue.CreatorType, issue.CreatorID, "creator")

		// Subscribe the assignee if exists and different from creator
		if issue.AssigneeType != nil && issue.AssigneeID != nil &&
			!(*issue.AssigneeType == issue.CreatorType && *issue.AssigneeID == issue.CreatorID) {
			addSubscriber(bus, queries, e.WorkspaceID, issue.ID, *issue.AssigneeType, *issue.AssigneeID, "assignee")
		}

		// Subscribe @mentioned users in description
		if issue.Description != nil && *issue.Description != "" {
			for _, m := range parseMentions(*issue.Description) {
				addSubscriber(bus, queries, e.WorkspaceID, issue.ID, m.Type, m.ID, "mentioned")
			}
		}
	})

	// issue:updated — subscribe new assignee or @mentioned users
	bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
		payload, ok := decodeIssueEvent(e)
		if !ok {
			return
		}
		issue := payload.Issue

		// Subscribe new assignee if assignee changed
		if payload.AssigneeChanged {
			if issue.AssigneeType != nil && issue.AssigneeID != nil {
				addSubscriber(bus, queries, e.WorkspaceID, issue.ID, *issue.AssigneeType, *issue.AssigneeID, "assignee")
			}
		}

		// Subscribe newly @mentioned users in description
		if payload.DescriptionChanged && issue.Description != nil {
			newMentions := parseMentions(*issue.Description)
			if len(newMentions) > 0 {
				prevMentioned := map[string]bool{}
				if payload.PrevDescription != nil {
					for _, m := range parseMentions(*payload.PrevDescription) {
						prevMentioned[m.Type+":"+m.ID] = true
					}
				}
				for _, m := range newMentions {
					if !prevMentioned[m.Type+":"+m.ID] {
						addSubscriber(bus, queries, e.WorkspaceID, issue.ID, m.Type, m.ID, "mentioned")
					}
				}
			}
		}
	})

	// comment:created — subscribe the commenter
	bus.Subscribe(protocol.EventCommentCreated, func(e events.Event) {
		payload, ok := decodeCommentEvent(e)
		if !ok {
			return
		}
		issueID := payload.Comment.IssueID
		authorType := payload.Comment.AuthorType
		authorID := payload.Comment.AuthorID
		if issueID == "" || authorID == "" {
			return
		}

		// Platform-authored system comments (MUL-2538 child-done parent notify)
		// have author_type='system' and a zero UUID author. They must NOT
		// add a subscriber row: issue_subscriber.user_type is constrained to
		// ('member','agent'), and a "system" subscriber has no inbox to read
		// anyway. Skip them at the side-effect boundary so the system event
		// stays a pure WS broadcast for the timeline.
		if authorType == "system" {
			return
		}

		addSubscriber(bus, queries, e.WorkspaceID, issueID, authorType, authorID, "commenter")
	})
}

func supportsIssueSubscriberUserType(userType string) bool {
	return userType == "member" || userType == "agent"
}

// addSubscriber adds a user as an issue subscriber and publishes a
// subscriber:added event for real-time frontend sync.
func addSubscriber(bus *events.Bus, queries *db.Queries, workspaceID, issueID, userType, userID, reason string) {
	if !supportsIssueSubscriberUserType(userType) {
		return
	}
	err := queries.AddIssueSubscriber(context.Background(), db.AddIssueSubscriberParams{
		IssueID:  parseUUID(issueID),
		UserType: userType,
		UserID:   parseUUID(userID),
		Reason:   reason,
	})
	if err != nil {
		slog.Error("failed to add issue subscriber",
			"issue_id", issueID,
			"user_type", userType,
			"user_id", userID,
			"reason", reason,
			"error", err,
		)
		return
	}

	bus.Publish(events.Event{
		Type:        protocol.EventSubscriberAdded,
		WorkspaceID: workspaceID,
		Payload: map[string]any{
			"issue_id":  issueID,
			"user_type": userType,
			"user_id":   userID,
			"reason":    reason,
		},
	})
}
