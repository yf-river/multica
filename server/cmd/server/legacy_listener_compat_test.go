package main

// These helpers exercise projection functions in focused tests without
// adding a second in-process delivery path to the production server.

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func addSubscriber(bus *events.Bus, queries *db.Queries, workspaceID, issueID, userType, userID, reason string) {
	affected, err := queries.AddIssueSubscriber(context.Background(), db.AddIssueSubscriberParams{
		IssueID: util.MustParseUUID(issueID), UserType: userType,
		UserID: util.MustParseUUID(userID), Reason: reason,
	})
	if err != nil {
		slog.Error("in-process subscriber projection failed", "issue_id", issueID, "error", err)
		return
	}
	if affected == 0 {
		return
	}
	publishSubscriberAdded(bus, workspaceID, issueID, userType, userID, reason)
}

func publishSubscriberAdded(bus *events.Bus, workspaceID, issueID, userType, userID, reason string) {
	bus.Publish(events.Event{
		Type: protocol.EventSubscriberAdded, WorkspaceID: workspaceID,
		Payload: map[string]any{"issue_id": issueID, "user_type": userType, "user_id": userID, "reason": reason},
	})
}

func publishCompatProjection(bus *events.Bus, run func() ([]events.Event, error)) {
	emitted, err := run()
	if err != nil {
		slog.Error("in-process projection failed", "error", err)
		return
	}
	for _, event := range emitted {
		bus.Publish(event)
	}
}

func registerActivityListeners(bus *events.Bus, queries *db.Queries) {
	ctx := context.Background()
	bus.Subscribe(protocol.EventIssueCreated, func(event events.Event) {
		publishCompatProjection(bus, func() ([]events.Event, error) {
			payload, ok := decodeIssueEvent(event)
			if !ok {
				return nil, nil
			}
			created, err := createIssueActivity(ctx, queries, event, payload.Issue, "created", []byte("{}"))
			if err != nil {
				return nil, err
			}
			return []events.Event{created}, nil
		})
	})
	bus.Subscribe(protocol.EventIssueUpdated, func(event events.Event) {
		publishCompatProjection(bus, func() ([]events.Event, error) {
			_, ok := decodeIssueEvent(event)
			if !ok {
				return nil, nil
			}
			return consumeIssueUpdatedActivities(ctx, queries, event)
		})
	})
	for _, eventType := range []string{protocol.EventTaskCompleted, protocol.EventTaskFailed, protocol.EventTaskCancelled} {
		bus.Subscribe(eventType, func(event events.Event) {
			publishCompatProjection(bus, func() ([]events.Event, error) {
				return consumeTaskTerminalIssueProjection(ctx, queries, event)
			})
		})
	}
}

func registerNotificationListeners(bus *events.Bus, queries *db.Queries) {
	ctx := context.Background()
	bus.Subscribe(protocol.EventIssueCreated, func(event events.Event) {
		publishCompatProjection(bus, func() ([]events.Event, error) {
			payload, ok := decodeIssueEvent(event)
			if !ok {
				return nil, nil
			}
			return projectIssueCreatedNotifications(ctx, queries, event, payload)
		})
	})
	bus.Subscribe(protocol.EventIssueUpdated, func(event events.Event) {
		publishCompatProjection(bus, func() ([]events.Event, error) {
			payload, ok := decodeIssueEvent(event)
			if !ok {
				return nil, nil
			}
			return projectIssueUpdatedNotifications(ctx, queries, event, payload)
		})
	})
	bus.Subscribe(protocol.EventCommentCreated, func(event events.Event) {
		publishCompatProjection(bus, func() ([]events.Event, error) {
			payload, ok := decodeCommentEvent(event)
			if !ok {
				return nil, nil
			}
			return projectCommentCreatedNotifications(ctx, queries, event, payload)
		})
	})
	bus.Subscribe(protocol.EventTaskFailed, func(event events.Event) {
		publishCompatProjection(bus, func() ([]events.Event, error) {
			payload, ok := decodeEventPayload[taskEventPayload](event)
			if !ok {
				return nil, nil
			}
			return projectTaskFailedNotifications(ctx, queries, event, payload)
		})
	})
}

func registerSubscriberListeners(bus *events.Bus, pool *pgxpool.Pool) {
	queries := db.New(pool)
	ctx := context.Background()
	bus.Subscribe(protocol.EventIssueCreated, func(event events.Event) {
		publishCompatProjection(bus, func() ([]events.Event, error) {
			payload, ok := decodeIssueEvent(event)
			if !ok {
				return nil, nil
			}
			out, err := projectIssueCreatedSubscribers(ctx, queries, payload)
			if err != nil {
				return nil, err
			}
			compatSubscribeDelegatedHuman(ctx, pool, queries, bus, event, payload.Issue)
			return out, nil
		})
	})
	bus.Subscribe(protocol.EventIssueUpdated, func(event events.Event) {
		publishCompatProjection(bus, func() ([]events.Event, error) {
			payload, ok := decodeIssueEvent(event)
			if !ok {
				return nil, nil
			}
			return projectIssueUpdatedSubscribers(ctx, queries, payload)
		})
	})
	bus.Subscribe(protocol.EventCommentCreated, func(event events.Event) {
		publishCompatProjection(bus, func() ([]events.Event, error) {
			payload, ok := decodeCommentEvent(event)
			if !ok {
				return nil, nil
			}
			return projectCommentCreatedSubscriber(ctx, queries, event, payload)
		})
	})
}

func compatSubscribeDelegatedHuman(ctx context.Context, pool *pgxpool.Pool, queries *db.Queries, bus *events.Bus, event events.Event, issue eventIssue) {
	if issue.CreatorType != "agent" || issue.ID == "" {
		return
	}
	issueRow, err := queries.GetIssue(ctx, util.MustParseUUID(issue.ID))
	if err != nil || !issueRow.OriginType.Valid || !issueRow.OriginID.Valid {
		return
	}
	origin, err := queries.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{
		ID: issueRow.OriginID, WorkspaceID: util.MustParseUUID(event.WorkspaceID),
	})
	if err != nil {
		return
	}
	human, reason, ok := attribution.DelegatedSubscriber(attribution.SubscriptionFacts{
		CreatorType: issueRow.CreatorType, OriginType: issueRow.OriginType.String,
		OriginOriginator: origin.OriginatorUserID,
	})
	if !ok {
		return
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	qtx := queries.WithTx(tx)
	if err := qtx.LockSubscriberWrites(ctx, db.LockSubscriberWritesParams{
		WorkspaceID: util.MustParseUUID(event.WorkspaceID), UserID: human,
	}); err != nil {
		return
	}
	affected, err := qtx.AddDelegatedSubscriber(ctx, db.AddDelegatedSubscriberParams{
		IssueID: util.MustParseUUID(issue.ID), UserID: human, Reason: reason,
		WorkspaceID: util.MustParseUUID(event.WorkspaceID),
	})
	if err != nil || affected == 0 || tx.Commit(ctx) != nil {
		return
	}
	bus.Publish(events.Event{
		Type: protocol.EventSubscriberAdded, WorkspaceID: event.WorkspaceID,
		Payload: map[string]any{"issue_id": issue.ID, "user_type": "member", "user_id": util.UUIDToString(human), "reason": reason},
	})
}
