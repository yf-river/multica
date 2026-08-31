package main

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func activityReceiptEvent(issueID, activityID, workspaceID string) events.Event {
	return events.Event{
		Type:        protocol.EventActivityCreated,
		WorkspaceID: workspaceID,
		ActorType:   "agent",
		ActorID:     testUserID,
		Payload: map[string]any{
			"issue_id": issueID,
			"entry": map[string]any{
				"type":   "activity",
				"id":     activityID,
				"action": "squad_leader_evaluated",
			},
		},
	}
}

func TestConsumeActivityCreatedReceiptSkipsDeletedIssue(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	event := activityReceiptEvent(uuid.NewString(), uuid.NewString(), testWorkspaceID)
	emitted, err := consumeActivityCreatedReceipt(context.Background(), db.New(testPool), event)
	if err != nil {
		t.Fatalf("deleted issue should be acknowledged: %v", err)
	}
	if len(emitted) != 0 {
		t.Fatalf("receipt consumer emitted %d events, want 0", len(emitted))
	}
}

func TestConsumeActivityCreatedReceiptRejectsMalformedPayload(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	_, err := consumeActivityCreatedReceipt(context.Background(), db.New(testPool), events.Event{
		Type:        protocol.EventActivityCreated,
		WorkspaceID: testWorkspaceID,
		Payload:     map[string]any{"issue_id": "not-an-issue"},
	})
	if err == nil {
		t.Fatal("malformed activity event was acknowledged")
	}
}

func TestConsumeActivityCreatedReceiptRejectsInvalidActivityID(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	_, err := consumeActivityCreatedReceipt(context.Background(), db.New(testPool), activityReceiptEvent(
		uuid.NewString(), "not-an-activity", testWorkspaceID,
	))
	if err == nil {
		t.Fatal("invalid activity ID was acknowledged")
	}
}

func TestConsumeActivityCreatedReceiptRejectsWorkspaceMismatch(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() { cleanupTestIssue(t, issueID) })
	_, err := consumeActivityCreatedReceipt(context.Background(), db.New(testPool), activityReceiptEvent(
		issueID, uuid.NewString(), uuid.NewString(),
	))
	if err == nil {
		t.Fatal("cross-workspace activity event was acknowledged")
	}
}

func TestDurableActivityReceiptIsRegisteredAndCompleted(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	queries := db.New(testPool)
	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	activity, err := queries.CreateActivity(ctx, db.CreateActivityParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		IssueID:     util.MustParseUUID(issueID),
		ActorType:   util.StrToText("agent"),
		ActorID:     util.MustParseUUID(testUserID),
		Action:      "squad_leader_evaluated",
		Details:     []byte(`{"outcome":"no_action"}`),
	})
	if err != nil {
		t.Fatalf("create activity: %v", err)
	}
	event := activityReceiptEvent(issueID, util.UUIDToString(activity.ID), testWorkspaceID)
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin outbox transaction: %v", err)
	}
	event, err = eventoutbox.Enqueue(ctx, queries.WithTx(tx), event)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("enqueue activity event: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit outbox event: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM domain_event_outbox WHERE id = $1`, util.MustParseUUID(event.ID))
		cleanupActivities(t, issueID)
		cleanupTestIssue(t, issueID)
	})

	dispatcher, err := eventoutbox.NewDispatcher(queries, testPool, events.New(), "activity-receipt-test-"+uuid.NewString())
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	if err := registerDurableActivityConsumers(dispatcher); err != nil {
		t.Fatalf("register durable activity consumers: %v", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go dispatcher.Run(runCtx)

	deadline := time.Now().Add(3 * time.Second)
	for {
		var processed, delivered bool
		if err := testPool.QueryRow(ctx, `
			SELECT processed_at IS NOT NULL,
			       EXISTS (
					SELECT 1 FROM domain_event_delivery
					WHERE event_id = $1 AND consumer = 'activity_receipt'
				)
			FROM domain_event_outbox WHERE id = $1
		`, util.MustParseUUID(event.ID)).Scan(&processed, &delivered); err != nil {
			t.Fatalf("read activity event state: %v", err)
		}
		if processed && delivered {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("activity event was not completed (processed=%v delivered=%v)", processed, delivered)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
