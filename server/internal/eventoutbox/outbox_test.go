package eventoutbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
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
		_, _ = outboxTestPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
		_, _ = outboxTestPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
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

func TestStreamKeySerializesEventsAcrossDispatchers(t *testing.T) {
	fixture := newOutboxFixture(t)
	ctx := context.Background()
	eventType := "test:ordered:" + uuid.NewString()
	streamKey := "issue:" + fixture.issueID
	enqueue := func(sequence int) events.Event {
		event := fixture.event(eventType)
		event.StreamKey = streamKey
		event.Payload = map[string]any{"issue_id": fixture.issueID, "sequence": sequence}
		created, err := Enqueue(ctx, fixture.queries, event)
		if err != nil {
			t.Fatalf("enqueue sequence %d: %v", sequence, err)
		}
		return created
	}
	first := enqueue(1)
	second := enqueue(2)

	var failFirst atomic.Bool
	failFirst.Store(true)
	var mu sync.Mutex
	completed := make([]int, 0, 2)
	consumer := func(_ context.Context, _ *db.Queries, event events.Event) ([]events.Event, error) {
		var payload struct {
			Sequence int `json:"sequence"`
		}
		raw, err := json.Marshal(event.Payload)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		if payload.Sequence == 1 && failFirst.CompareAndSwap(true, false) {
			return nil, errors.New("injected first-event failure")
		}
		mu.Lock()
		completed = append(completed, payload.Sequence)
		mu.Unlock()
		return nil, nil
	}
	newDispatcher := func(owner string) *Dispatcher {
		dispatcher, err := NewDispatcher(fixture.queries, outboxTestPool, events.New(), owner, DispatcherConfig{
			BatchSize: 10,
			Lease:     time.Second,
			RetryBase: 50 * time.Millisecond,
			MaxRetry:  50 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("NewDispatcher(%s): %v", owner, err)
		}
		if err := dispatcher.Register(eventType, "ordered", consumer); err != nil {
			t.Fatalf("Register(%s): %v", owner, err)
		}
		return dispatcher
	}
	firstWorker := newDispatcher("stream-first-" + uuid.NewString())
	secondWorker := newDispatcher("stream-second-" + uuid.NewString())

	if count, err := firstWorker.ProcessBatch(ctx); count != 1 || err == nil {
		t.Fatalf("first worker batch = (%d, %v), want one failed first event", count, err)
	}
	if count, err := secondWorker.ProcessBatch(ctx); count != 0 || err != nil {
		t.Fatalf("second worker bypassed failed stream head: (%d, %v)", count, err)
	}
	time.Sleep(60 * time.Millisecond)
	if count, err := secondWorker.ProcessBatch(ctx); count != 1 || err != nil {
		t.Fatalf("retry first event = (%d, %v), want one success", count, err)
	}
	if count, err := firstWorker.ProcessBatch(ctx); count != 1 || err != nil {
		t.Fatalf("process second event = (%d, %v), want one success", count, err)
	}
	mu.Lock()
	gotOrder := append([]int(nil), completed...)
	mu.Unlock()
	if len(gotOrder) != 2 || gotOrder[0] != 1 || gotOrder[1] != 2 {
		t.Fatalf("completed stream order = %v, want [1 2]", gotOrder)
	}
	assertOutboxComplete(t, first.ID, 1)
	assertOutboxComplete(t, second.ID, 0)
}

func TestDeadLetterUnblocksStreamAndCanBeRequeued(t *testing.T) {
	fixture := newOutboxFixture(t)
	ctx := context.Background()
	eventType := "test:dead-letter:" + uuid.NewString()
	streamKey := "issue:" + fixture.issueID
	enqueue := func(sequence int) events.Event {
		event := fixture.event(eventType)
		event.StreamKey = streamKey
		event.Payload = map[string]any{"issue_id": fixture.issueID, "sequence": sequence}
		created, err := Enqueue(ctx, fixture.queries, event)
		if err != nil {
			t.Fatalf("enqueue sequence %d: %v", sequence, err)
		}
		return created
	}
	first := enqueue(1)
	second := enqueue(2)

	poisonFirst := true
	var mu sync.Mutex
	completed := make([]int, 0, 2)
	dispatcher, err := NewDispatcher(fixture.queries, outboxTestPool, events.New(), "dead-letter-"+uuid.NewString(), DispatcherConfig{
		BatchSize:   10,
		Lease:       time.Second,
		RetryBase:   time.Millisecond,
		MaxRetry:    time.Millisecond,
		MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if err := dispatcher.Register(eventType, "poisonable", func(_ context.Context, _ *db.Queries, event events.Event) ([]events.Event, error) {
		var payload struct {
			Sequence int `json:"sequence"`
		}
		raw, _ := json.Marshal(event.Payload)
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		if payload.Sequence == 1 && poisonFirst {
			return nil, errors.New("permanent projection failure")
		}
		mu.Lock()
		completed = append(completed, payload.Sequence)
		mu.Unlock()
		return nil, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if count, err := dispatcher.ProcessBatch(ctx); count != 1 || err == nil {
		t.Fatalf("first failure batch = (%d, %v), want one retry", count, err)
	}
	time.Sleep(3 * time.Millisecond)
	if count, err := dispatcher.ProcessBatch(ctx); count != 1 || err == nil {
		t.Fatalf("dead-letter batch = (%d, %v), want one terminal failure", count, err)
	}
	assertOutboxDeadLettered(t, first.ID, 2)

	if count, err := dispatcher.ProcessBatch(ctx); count != 1 || err != nil {
		t.Fatalf("stream successor after dead-letter = (%d, %v)", count, err)
	}
	assertOutboxComplete(t, second.ID, 0)
	mu.Lock()
	got := append([]int(nil), completed...)
	mu.Unlock()
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("completed after dead-letter = %v, want [2]", got)
	}

	poisonFirst = false
	if updated, err := fixture.queries.RequeueDeadLetterDomainEvent(ctx, util.MustParseUUID(first.ID)); err != nil || updated != 1 {
		t.Fatalf("requeue dead letter = (%d, %v), want one row", updated, err)
	}
	if count, err := dispatcher.ProcessBatch(ctx); count != 1 || err != nil {
		t.Fatalf("requeued event batch = (%d, %v)", count, err)
	}
	assertOutboxComplete(t, first.ID, 0)
}

func TestDeadLetterProjectionCommitsWithTerminalEventState(t *testing.T) {
	fixture := newOutboxFixture(t)
	ctx := context.Background()
	eventType := "test:dead-letter-projection:" + uuid.NewString()
	event, err := Enqueue(ctx, fixture.queries, fixture.event(eventType))
	if err != nil {
		t.Fatalf("enqueue dead-letter projection event: %v", err)
	}
	dispatcher, err := NewDispatcher(fixture.queries, outboxTestPool, events.New(), "dead-letter-projection-"+uuid.NewString(), DispatcherConfig{
		BatchSize:   10,
		Lease:       time.Second,
		RetryBase:   time.Millisecond,
		MaxRetry:    time.Millisecond,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	action := "dead_letter_projection_" + uuid.NewString()
	if err := dispatcher.RegisterWithDeadLetter(
		eventType,
		"terminal_projection",
		func(context.Context, *db.Queries, events.Event) ([]events.Event, error) {
			return nil, errors.New("permanent projection failure")
		},
		func(ctx context.Context, queries *db.Queries, event events.Event, cause error) error {
			_, err := queries.CreateActivity(ctx, db.CreateActivityParams{
				WorkspaceID: util.MustParseUUID(fixture.workspaceID),
				IssueID:     util.MustParseUUID(fixture.issueID),
				ActorType:   pgtype.Text{String: event.ActorType, Valid: true},
				ActorID:     util.MustParseUUID(fixture.userID),
				Action:      action,
				Details:     []byte(fmt.Sprintf(`{"error":%q}`, cause.Error())),
			})
			return err
		},
	); err != nil {
		t.Fatalf("RegisterWithDeadLetter: %v", err)
	}
	if count, err := dispatcher.ProcessBatch(ctx); count != 1 || err == nil {
		t.Fatalf("dead-letter projection batch = (%d, %v)", count, err)
	}
	assertOutboxDeadLettered(t, event.ID, 1)
	var activities int
	if err := outboxTestPool.QueryRow(ctx, `
		SELECT count(*) FROM activity_log WHERE issue_id = $1 AND action = $2
	`, fixture.issueID, action).Scan(&activities); err != nil {
		t.Fatalf("count dead-letter projection activities: %v", err)
	}
	if activities != 1 {
		t.Fatalf("dead-letter projection activities = %d, want 1", activities)
	}
}

func TestPruneExpiredKeepsFreshAndPendingEvents(t *testing.T) {
	fixture := newOutboxFixture(t)
	ctx := context.Background()
	create := func(label string) events.Event {
		event, err := Enqueue(ctx, fixture.queries, fixture.event("test:retention:"+label+":"+uuid.NewString()))
		if err != nil {
			t.Fatalf("enqueue %s: %v", label, err)
		}
		return event
	}
	oldProcessed := create("old-processed")
	oldDead := create("old-dead")
	freshProcessed := create("fresh-processed")
	freshDead := create("fresh-dead")
	pending := create("pending")
	retentionUpdates := []struct {
		sql string
		id  string
	}{
		{`UPDATE domain_event_outbox SET processed_at = now() - interval '8 days' WHERE id = $1`, oldProcessed.ID},
		{`UPDATE domain_event_outbox SET dead_lettered_at = now() - interval '31 days', dead_letter_reason = 'old' WHERE id = $1`, oldDead.ID},
		{`UPDATE domain_event_outbox SET processed_at = now() WHERE id = $1`, freshProcessed.ID},
		{`UPDATE domain_event_outbox SET dead_lettered_at = now(), dead_letter_reason = 'fresh' WHERE id = $1`, freshDead.ID},
	}
	for _, update := range retentionUpdates {
		if _, err := outboxTestPool.Exec(ctx, update.sql, update.id); err != nil {
			t.Fatalf("seed retention state for %s: %v", update.id, err)
		}
	}

	dispatcher, err := NewDispatcher(fixture.queries, outboxTestPool, events.New(), "retention-"+uuid.NewString(), DispatcherConfig{
		ProcessedRetention:  7 * 24 * time.Hour,
		DeadLetterRetention: 30 * 24 * time.Hour,
		CleanupBatchSize:    10,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	deleted, err := dispatcher.PruneExpired(ctx)
	if err != nil {
		t.Fatalf("PruneExpired: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("PruneExpired deleted %d rows, want 2", deleted)
	}
	for _, eventID := range []string{freshProcessed.ID, freshDead.ID, pending.ID} {
		var count int
		if err := outboxTestPool.QueryRow(ctx, `SELECT count(*) FROM domain_event_outbox WHERE id = $1`, eventID).Scan(&count); err != nil {
			t.Fatalf("count retained event %s: %v", eventID, err)
		}
		if count != 1 {
			t.Fatalf("fresh or pending event %s was pruned", eventID)
		}
	}
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

func assertOutboxDeadLettered(t *testing.T, eventID string, wantAttempts int) {
	t.Helper()
	var deadLettered bool
	var processed bool
	var attempts int
	var reason string
	if err := outboxTestPool.QueryRow(context.Background(), `
		SELECT dead_lettered_at IS NOT NULL, processed_at IS NOT NULL, attempts, COALESCE(dead_letter_reason, '')
		FROM domain_event_outbox WHERE id = $1
	`, eventID).Scan(&deadLettered, &processed, &attempts, &reason); err != nil {
		t.Fatalf("load dead-letter row: %v", err)
	}
	if !deadLettered || processed || attempts != wantAttempts || reason == "" {
		t.Fatalf("dead-letter state = dead:%v processed:%v attempts:%d reason:%q", deadLettered, processed, attempts, reason)
	}
}
