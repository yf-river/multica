package eventoutbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var outboxTestPool *pgxpool.Pool

func TestMain(m *testing.M) {
	ctx := context.Background()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Println("event outbox integration tests skipped: DATABASE_URL is not set")
		os.Exit(0)
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil || pool.Ping(ctx) != nil {
		fmt.Println("event outbox integration tests skipped: database unavailable")
		os.Exit(0)
	}
	outboxTestPool = pool
	code := m.Run()
	pool.Close()
	os.Exit(code)
}

type outboxFixture struct {
	queries     *db.Queries
	workspaceID string
	userID      string
	issueID     string
}

func newOutboxFixture(t *testing.T) outboxFixture {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()
	var userID string
	if err := outboxTestPool.QueryRow(ctx, `
		INSERT INTO "user" (name, account) VALUES ('Outbox Test', $1) RETURNING id
	`, "outbox-"+suffix+"@test.invalid").Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var workspaceID string
	if err := outboxTestPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix) VALUES ('Outbox Test', $1, 'OBX') RETURNING id
	`, "outbox-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := outboxTestPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')
	`, workspaceID, userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	var issueID string
	if err := outboxTestPool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, creator_type, creator_id, number
		) VALUES ($1, 'Outbox projection', 'todo', 'none', 'member', $2, 1)
		RETURNING id
	`, workspaceID, userID).Scan(&issueID); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	t.Cleanup(func() {
		outboxTestPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
		outboxTestPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
	})
	return outboxFixture{
		queries:     db.New(outboxTestPool),
		workspaceID: workspaceID,
		userID:      userID,
		issueID:     issueID,
	}
}

func (fixture outboxFixture) event(eventType string) events.Event {
	return events.Event{
		Type:        eventType,
		WorkspaceID: fixture.workspaceID,
		ActorType:   "member",
		ActorID:     fixture.userID,
		Payload:     map[string]any{"issue_id": fixture.issueID},
	}
}

func (fixture outboxFixture) activityConsumer(action string, failFirst *atomic.Bool, calls *atomic.Int32) Consumer {
	return func(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
		if calls != nil {
			calls.Add(1)
		}
		_, err := queries.CreateActivity(ctx, db.CreateActivityParams{
			WorkspaceID: util.MustParseUUID(fixture.workspaceID),
			IssueID:     util.MustParseUUID(fixture.issueID),
			ActorType:   pgtype.Text{String: event.ActorType, Valid: true},
			ActorID:     util.MustParseUUID(fixture.userID),
			Action:      action,
			Details:     []byte(`{}`),
		})
		if err != nil {
			return nil, err
		}
		if failFirst != nil && failFirst.CompareAndSwap(true, false) {
			return nil, errors.New("injected projection failure")
		}
		return []events.Event{{Type: "test:projection_committed", Payload: map[string]any{"action": action}}}, nil
	}
}

func newTestDispatcher(t *testing.T, fixture outboxFixture, bus *events.Bus) *Dispatcher {
	t.Helper()
	dispatcher, err := NewDispatcher(fixture.queries, outboxTestPool, bus, "test-"+uuid.NewString(), DispatcherConfig{
		BatchSize: 10,
		Lease:     time.Second,
		RetryBase: time.Millisecond,
		MaxRetry:  time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return dispatcher
}

func TestEnqueueRollsBackWithBusinessTransaction(t *testing.T) {
	fixture := newOutboxFixture(t)
	ctx := context.Background()
	tx, err := outboxTestPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	event, err := Enqueue(ctx, fixture.queries.WithTx(tx), fixture.event("test:rollback:"+uuid.NewString()))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	var count int
	if err := outboxTestPool.QueryRow(ctx, `SELECT count(*) FROM domain_event_outbox WHERE id = $1`, event.ID).Scan(&count); err != nil {
		t.Fatalf("count rolled-back event: %v", err)
	}
	if count != 0 {
		t.Fatalf("rolled-back business transaction left %d outbox rows", count)
	}
}

func TestDispatcherRetriesConsumerTransaction(t *testing.T) {
	fixture := newOutboxFixture(t)
	ctx := context.Background()
	eventType := "test:retry:" + uuid.NewString()
	event, err := Enqueue(ctx, fixture.queries, fixture.event(eventType))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	bus := events.New()
	var emitted atomic.Int32
	bus.Subscribe("test:projection_committed", func(events.Event) { emitted.Add(1) })
	dispatcher := newTestDispatcher(t, fixture, bus)
	var failFirst atomic.Bool
	failFirst.Store(true)
	if err := dispatcher.Register(eventType, "activity", fixture.activityConsumer("outbox_retry", &failFirst, nil)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if count, err := dispatcher.ProcessBatch(ctx); count != 1 || err == nil {
		t.Fatalf("first ProcessBatch = (%d, %v), want one failed delivery", count, err)
	}
	assertActivityCount(t, fixture.issueID, "outbox_retry", 0)
	assertDeliveryCount(t, event.ID, "activity", 0)
	time.Sleep(3 * time.Millisecond)
	if count, err := dispatcher.ProcessBatch(ctx); count != 1 || err != nil {
		t.Fatalf("second ProcessBatch = (%d, %v), want one success", count, err)
	}
	assertActivityCount(t, fixture.issueID, "outbox_retry", 1)
	assertDeliveryCount(t, event.ID, "activity", 1)
	if emitted.Load() != 1 {
		t.Fatalf("committed ephemeral events = %d, want 1", emitted.Load())
	}
	assertOutboxComplete(t, event.ID, 1)
}

func TestDispatcherDoesNotRepeatCompletedConsumer(t *testing.T) {
	fixture := newOutboxFixture(t)
	ctx := context.Background()
	eventType := "test:receipts:" + uuid.NewString()
	event, err := Enqueue(ctx, fixture.queries, fixture.event(eventType))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	dispatcher := newTestDispatcher(t, fixture, events.New())
	var firstCalls atomic.Int32
	if err := dispatcher.Register(eventType, "first", fixture.activityConsumer("outbox_first", nil, &firstCalls)); err != nil {
		t.Fatalf("register first: %v", err)
	}
	var secondFails atomic.Bool
	secondFails.Store(true)
	if err := dispatcher.Register(eventType, "second", fixture.activityConsumer("outbox_second", &secondFails, nil)); err != nil {
		t.Fatalf("register second: %v", err)
	}

	if _, err := dispatcher.ProcessBatch(ctx); err == nil {
		t.Fatal("first ProcessBatch unexpectedly succeeded")
	}
	assertActivityCount(t, fixture.issueID, "outbox_first", 1)
	assertActivityCount(t, fixture.issueID, "outbox_second", 0)
	time.Sleep(3 * time.Millisecond)
	if _, err := dispatcher.ProcessBatch(ctx); err != nil {
		t.Fatalf("second ProcessBatch: %v", err)
	}
	if firstCalls.Load() != 1 {
		t.Fatalf("completed consumer ran %d times, want once", firstCalls.Load())
	}
	assertActivityCount(t, fixture.issueID, "outbox_first", 1)
	assertActivityCount(t, fixture.issueID, "outbox_second", 1)
	assertDeliveryCount(t, event.ID, "first", 1)
	assertDeliveryCount(t, event.ID, "second", 1)
}

func assertActivityCount(t *testing.T, issueID, action string, want int) {
	t.Helper()
	var count int
	if err := outboxTestPool.QueryRow(context.Background(), `
		SELECT count(*) FROM activity_log WHERE issue_id = $1 AND action = $2
	`, issueID, action).Scan(&count); err != nil {
		t.Fatalf("count activities: %v", err)
	}
	if count != want {
		t.Fatalf("activity count for %s = %d, want %d", action, count, want)
	}
}

func assertDeliveryCount(t *testing.T, eventID, consumer string, want int) {
	t.Helper()
	var count int
	if err := outboxTestPool.QueryRow(context.Background(), `
		SELECT count(*) FROM domain_event_delivery WHERE event_id = $1 AND consumer = $2
	`, eventID, consumer).Scan(&count); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if count != want {
		t.Fatalf("delivery count for %s = %d, want %d", consumer, count, want)
	}
}

func assertOutboxComplete(t *testing.T, eventID string, wantAttempts int) {
	t.Helper()
	var processed bool
	var attempts int
	if err := outboxTestPool.QueryRow(context.Background(), `
		SELECT processed_at IS NOT NULL, attempts FROM domain_event_outbox WHERE id = $1
	`, eventID).Scan(&processed, &attempts); err != nil {
		t.Fatalf("load outbox row: %v", err)
	}
	if !processed || attempts != wantAttempts {
		t.Fatalf("outbox state = processed:%v attempts:%d, want true/%d", processed, attempts, wantAttempts)
	}
}
