package eventoutbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
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

func TestEventFromRowDecodesObjectPayload(t *testing.T) {
	id := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	event := eventFromRow(db.DomainEventOutbox{ID: id, EventType: "test:event", Payload: []byte(`{"issue_id":"abc","count":2}`)})
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", event.Payload)
	}
	if payload["issue_id"] != "abc" || payload["count"] != float64(2) {
		t.Fatalf("decoded payload = %#v", payload)
	}
}

func TestEventFromRowKeepsMalformedPayloadInspectable(t *testing.T) {
	raw := []byte(`not-json`)
	event := eventFromRow(db.DomainEventOutbox{EventType: "test:event", Payload: raw})
	payload, ok := event.Payload.(json.RawMessage)
	if !ok || string(payload) != string(raw) {
		t.Fatalf("payload = %#v (%T), want raw malformed payload", event.Payload, event.Payload)
	}
}

func TestDispatcherRequiresDependencies(t *testing.T) {
	if _, err := NewDispatcher(nil, nil, nil, ""); err == nil {
		t.Fatal("NewDispatcher unexpectedly accepted missing dependencies")
	}
}

// Database-backed outbox tests are intentionally opt-in. The normal suite must
// never require a developer's database or execute against a shared environment.
func requireOutboxIntegrationDB(t *testing.T) (*pgxpool.Pool, *db.Queries) {
	t.Helper()
	if os.Getenv("MULTICA_OUTBOX_INTEGRATION") != "1" {
		t.Skip("set MULTICA_OUTBOX_INTEGRATION=1 with an isolated DATABASE_URL to run outbox integration tests")
	}
	url := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if url == "" {
		t.Fatal("DATABASE_URL is required when MULTICA_OUTBOX_INTEGRATION=1")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("configure database: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatalf("ping database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, db.New(pool)
}

type integrationFixture struct {
	pool        *pgxpool.Pool
	queries     *db.Queries
	workspaceID string
	userID      string
	issueID     string
}

func newIntegrationFixture(t *testing.T) integrationFixture {
	t.Helper()
	pool, queries := requireOutboxIntegrationDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	var userID, workspaceID string
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('Outbox test', $1) RETURNING id`, "outbox-"+suffix+"@invalid").Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug, issue_prefix) VALUES ('Outbox test', $1, 'OBX') RETURNING id`, "outbox-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, workspaceID, userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	var issueID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, creator_type, creator_id, number
		) VALUES ($1, 'Outbox projection', 'todo', 'none', 'member', $2, 1)
		RETURNING id
	`, workspaceID, userID).Scan(&issueID); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
	})
	return integrationFixture{pool: pool, queries: queries, workspaceID: workspaceID, userID: userID, issueID: issueID}
}

func (f integrationFixture) event(eventType string) events.Event {
	return events.Event{Type: eventType, WorkspaceID: f.workspaceID, ActorType: "member", ActorID: f.userID, StreamKey: "workspace:" + f.workspaceID, Payload: map[string]any{"ok": true}}
}

func newIntegrationDispatcher(t *testing.T, f integrationFixture) *Dispatcher {
	t.Helper()
	d, err := NewDispatcher(f.queries, f.pool, events.New(), "test-"+uuid.NewString())
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	d.config.BatchSize = 20
	d.config.Lease = time.Second
	d.config.RetryBase = time.Millisecond
	d.config.MaxRetry = time.Millisecond
	d.config.MaxAttempts = 3
	return d
}

func (f integrationFixture) activityConsumer(action string, failFirst *atomic.Bool, calls *atomic.Int32) Consumer {
	return func(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
		if calls != nil {
			calls.Add(1)
		}
		_, err := queries.CreateActivity(ctx, db.CreateActivityParams{
			WorkspaceID: util.MustParseUUID(f.workspaceID),
			IssueID:     util.MustParseUUID(f.issueID),
			ActorType:   pgtype.Text{String: event.ActorType, Valid: true},
			ActorID:     util.MustParseUUID(f.userID),
			Action:      action,
			Details:     []byte(`{}`),
		})
		if err != nil {
			return nil, err
		}
		if failFirst != nil && failFirst.CompareAndSwap(true, false) {
			return nil, errors.New("injected projection failure")
		}
		return nil, nil
	}
}

func TestEnqueueRollsBackWithBusinessTransaction(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	event := f.event("test:rollback:" + uuid.NewString())
	event.IdempotencyKey = "rollback:" + uuid.NewString()
	persisted, err := Enqueue(ctx, f.queries.WithTx(tx), event)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM domain_event_outbox WHERE id = $1`, persisted.ID).Scan(&count); err != nil {
		t.Fatalf("count rolled-back event: %v", err)
	}
	if count != 0 {
		t.Fatalf("rolled-back transaction left %d outbox rows", count)
	}
}

func TestDispatcherRetriesAndReceiptsAreTransactional(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	typ := "test:retry:" + uuid.NewString()
	event, err := Enqueue(ctx, f.queries, f.event(typ))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	d := newIntegrationDispatcher(t, f)
	var attempts atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	if err := d.Register(typ, "test-consumer", func(ctx context.Context, q *db.Queries, got events.Event) ([]events.Event, error) {
		attempts.Add(1)
		if fail.CompareAndSwap(true, false) {
			return nil, context.DeadlineExceeded
		}
		return nil, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if n, err := d.processBatch(ctx); n != 1 || err == nil {
		t.Fatalf("first batch = (%d, %v), want retry", n, err)
	}
	time.Sleep(3 * time.Millisecond)
	if n, err := d.processBatch(ctx); n != 1 || err != nil {
		t.Fatalf("second batch = (%d, %v), want success", n, err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
	row, err := f.queries.GetDomainEventByIdempotencyKey(ctx, event.IdempotencyKey)
	if err != nil {
		t.Fatalf("get event: %v", err)
	}
	if !row.ProcessedAt.Valid || row.Attempts != 1 {
		t.Fatalf("event state processed=%v attempts=%d", row.ProcessedAt.Valid, row.Attempts)
	}
}

func TestDispatcherRollsBackFailedConsumerProjection(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	typ := "test:projection-rollback:" + uuid.NewString()
	event, err := Enqueue(ctx, f.queries, f.event(typ))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	d := newIntegrationDispatcher(t, f)
	var failFirst atomic.Bool
	failFirst.Store(true)
	if err := d.Register(typ, "activity", f.activityConsumer("outbox_projection_rollback", &failFirst, nil)); err != nil {
		t.Fatalf("register: %v", err)
	}
	if count, err := d.processBatch(ctx); count != 1 || err == nil {
		t.Fatalf("failed batch = (%d, %v), want one failure", count, err)
	}
	assertActivityCount(t, f, "outbox_projection_rollback", 0)
	assertDeliveryCount(t, f, event.ID, "activity", 0)
	time.Sleep(3 * time.Millisecond)
	if count, err := d.processBatch(ctx); count != 1 || err != nil {
		t.Fatalf("retry batch = (%d, %v), want success", count, err)
	}
	assertActivityCount(t, f, "outbox_projection_rollback", 1)
	assertDeliveryCount(t, f, event.ID, "activity", 1)
}

func TestDispatcherDoesNotRepeatCompletedConsumer(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	typ := "test:receipts:" + uuid.NewString()
	event, err := Enqueue(ctx, f.queries, f.event(typ))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	d := newIntegrationDispatcher(t, f)
	var firstCalls atomic.Int32
	if err := d.Register(typ, "first", f.activityConsumer("outbox_first", nil, &firstCalls)); err != nil {
		t.Fatalf("register first: %v", err)
	}
	var secondFails atomic.Bool
	secondFails.Store(true)
	if err := d.Register(typ, "second", f.activityConsumer("outbox_second", &secondFails, nil)); err != nil {
		t.Fatalf("register second: %v", err)
	}
	if _, err := d.processBatch(ctx); err == nil {
		t.Fatal("first batch unexpectedly succeeded")
	}
	time.Sleep(3 * time.Millisecond)
	if _, err := d.processBatch(ctx); err != nil {
		t.Fatalf("second batch: %v", err)
	}
	if firstCalls.Load() != 1 {
		t.Fatalf("completed consumer ran %d times, want once", firstCalls.Load())
	}
	assertActivityCount(t, f, "outbox_first", 1)
	assertActivityCount(t, f, "outbox_second", 1)
	assertDeliveryCount(t, f, event.ID, "first", 1)
	assertDeliveryCount(t, f, event.ID, "second", 1)
}

func TestStreamKeySerializesEventsAcrossDispatchers(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	typ := "test:ordered:" + uuid.NewString()
	streamKey := "issue:" + f.issueID
	first := enqueueStreamSequence(t, f, typ, streamKey, 1)
	second := enqueueStreamSequence(t, f, typ, streamKey, 2)

	var failFirst atomic.Bool
	failFirst.Store(true)
	var mu sync.Mutex
	completed := make([]int, 0, 2)
	consumer := func(_ context.Context, _ *db.Queries, event events.Event) ([]events.Event, error) {
		var payload struct {
			Sequence int `json:"sequence"`
		}
		raw, _ := json.Marshal(event.Payload)
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
		d, err := NewDispatcher(f.queries, f.pool, events.New(), owner)
		if err != nil {
			t.Fatalf("new dispatcher: %v", err)
		}
		d.config.BatchSize = 10
		d.config.Lease = time.Second
		d.config.RetryBase = 50 * time.Millisecond
		d.config.MaxRetry = 50 * time.Millisecond
		if err := d.Register(typ, "ordered", consumer); err != nil {
			t.Fatalf("register: %v", err)
		}
		return d
	}
	firstWorker := newDispatcher("stream-first-" + uuid.NewString())
	secondWorker := newDispatcher("stream-second-" + uuid.NewString())
	if count, err := firstWorker.processBatch(ctx); count != 1 || err == nil {
		t.Fatalf("first worker = (%d, %v), want failed stream head", count, err)
	}
	if count, err := secondWorker.processBatch(ctx); count != 0 || err != nil {
		t.Fatalf("second worker bypassed stream head: (%d, %v)", count, err)
	}
	time.Sleep(60 * time.Millisecond)
	if count, err := secondWorker.processBatch(ctx); count != 1 || err != nil {
		t.Fatalf("retry first = (%d, %v)", count, err)
	}
	if count, err := firstWorker.processBatch(ctx); count != 1 || err != nil {
		t.Fatalf("process second = (%d, %v)", count, err)
	}
	mu.Lock()
	got := append([]int(nil), completed...)
	mu.Unlock()
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("completed order = %v, want [1 2]", got)
	}
	assertOutboxComplete(t, f, first.ID, 1)
	assertOutboxComplete(t, f, second.ID, 0)
}

func TestDeadLetterUnblocksStream(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	typ := "test:dead-letter:" + uuid.NewString()
	streamKey := "issue:" + f.issueID
	first := enqueueStreamSequence(t, f, typ, streamKey, 1)
	second := enqueueStreamSequence(t, f, typ, streamKey, 2)
	d := newIntegrationDispatcher(t, f)
	d.config.MaxAttempts = 2
	var completed atomic.Int32
	if err := d.Register(typ, "poisonable", func(_ context.Context, _ *db.Queries, event events.Event) ([]events.Event, error) {
		var payload struct {
			Sequence int `json:"sequence"`
		}
		raw, _ := json.Marshal(event.Payload)
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		if payload.Sequence == 1 {
			return nil, errors.New("permanent projection failure")
		}
		completed.Store(int32(payload.Sequence))
		return nil, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if count, err := d.processBatch(ctx); count != 1 || err == nil {
		t.Fatalf("first failure = (%d, %v)", count, err)
	}
	time.Sleep(3 * time.Millisecond)
	if count, err := d.processBatch(ctx); count != 1 || err == nil {
		t.Fatalf("dead-letter batch = (%d, %v)", count, err)
	}
	assertOutboxDeadLettered(t, f, first.ID, 2)
	if count, err := d.processBatch(ctx); count != 1 || err != nil {
		t.Fatalf("successor batch = (%d, %v)", count, err)
	}
	if completed.Load() != 2 {
		t.Fatalf("completed successor = %d, want 2", completed.Load())
	}
	assertOutboxComplete(t, f, second.ID, 0)
}

func TestStreamOrderingIncludesDifferentEventTypes(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	firstType := "test:stream-head:" + uuid.NewString()
	secondType := "test:stream-tail:" + uuid.NewString()
	streamKey := "issue:" + f.issueID
	first := enqueueStreamSequence(t, f, firstType, streamKey, 1)
	second := enqueueStreamSequence(t, f, secondType, streamKey, 2)

	d := newIntegrationDispatcher(t, f)
	if err := d.Register(secondType, "tail", func(context.Context, *db.Queries, events.Event) ([]events.Event, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("register tail: %v", err)
	}
	if count, err := d.processBatch(ctx); count != 0 || err != nil {
		t.Fatalf("tail bypassed unregistered stream head: (%d, %v)", count, err)
	}

	if _, err := f.pool.Exec(ctx, `UPDATE domain_event_outbox SET dead_lettered_at = now(), dead_letter_reason = 'test stream head' WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("dead-letter stream head: %v", err)
	}
	if count, err := d.processBatch(ctx); count != 1 || err != nil {
		t.Fatalf("tail after terminal stream head = (%d, %v)", count, err)
	}
	assertOutboxComplete(t, f, second.ID, 0)
}

func TestExpiredLeaseIsRecoveredByAnotherDispatcher(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	typ := "test:lease-recovery:" + uuid.NewString()
	event, err := Enqueue(ctx, f.queries, f.event(typ))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	staleOwner := "stale-" + uuid.NewString()
	rows, err := f.queries.ClaimDomainEvents(ctx, db.ClaimDomainEventsParams{
		LeaseOwner:    optionalText(staleOwner),
		LeaseDuration: interval(time.Millisecond),
		BatchSize:     1,
		EventTypes:    []string{typ},
	})
	if err != nil || len(rows) != 1 {
		t.Fatalf("stale claim = (%d, %v), want one", len(rows), err)
	}
	time.Sleep(3 * time.Millisecond)
	d := newIntegrationDispatcher(t, f)
	var calls atomic.Int32
	if err := d.Register(typ, "recovered", func(context.Context, *db.Queries, events.Event) ([]events.Event, error) {
		calls.Add(1)
		return nil, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if count, err := d.processBatch(ctx); count != 1 || err != nil {
		t.Fatalf("recovery batch = (%d, %v)", count, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("recovered consumer calls = %d, want 1", calls.Load())
	}
	assertOutboxComplete(t, f, event.ID, 0)
}

func TestDeadLetterProjectionCommitsAtomically(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()
	typ := "test:dead-letter-projection:" + uuid.NewString()
	event, err := Enqueue(ctx, f.queries, f.event(typ))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	d := newIntegrationDispatcher(t, f)
	d.config.MaxAttempts = 1
	action := "dead_letter_projection_" + uuid.NewString()
	if err := d.RegisterWithDeadLetter(typ, "terminal_projection",
		func(context.Context, *db.Queries, events.Event) ([]events.Event, error) {
			return nil, errors.New("permanent projection failure")
		},
		func(ctx context.Context, queries *db.Queries, event events.Event, cause error) error {
			_, err := queries.CreateActivity(ctx, db.CreateActivityParams{
				WorkspaceID: util.MustParseUUID(f.workspaceID),
				IssueID:     util.MustParseUUID(f.issueID),
				ActorType:   pgtype.Text{String: event.ActorType, Valid: true},
				ActorID:     util.MustParseUUID(f.userID),
				Action:      action,
				Details:     []byte(fmt.Sprintf(`{"error":%q}`, cause.Error())),
			})
			return err
		}); err != nil {
		t.Fatalf("register with dead letter: %v", err)
	}
	if count, err := d.processBatch(ctx); count != 1 || err == nil {
		t.Fatalf("dead-letter projection batch = (%d, %v)", count, err)
	}
	assertOutboxDeadLettered(t, f, event.ID, 1)
	assertActivityCount(t, f, action, 1)
}

func TestEnqueueRejectsNonObjectPayload(t *testing.T) {
	f := newIntegrationFixture(t)
	_, err := Enqueue(context.Background(), f.queries, events.Event{Type: "test:scalar", WorkspaceID: f.workspaceID, Payload: "scalar"})
	if err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("enqueue error = %v, want object validation", err)
	}
}

func enqueueStreamSequence(t *testing.T, f integrationFixture, eventType, streamKey string, sequence int) events.Event {
	t.Helper()
	event := f.event(eventType)
	event.StreamKey = streamKey
	event.IdempotencyKey = fmt.Sprintf("%s:%d:%s", eventType, sequence, uuid.NewString())
	event.Payload = map[string]any{"issue_id": f.issueID, "sequence": sequence}
	created, err := Enqueue(context.Background(), f.queries, event)
	if err != nil {
		t.Fatalf("enqueue sequence %d: %v", sequence, err)
	}
	return created
}

func assertActivityCount(t *testing.T, f integrationFixture, action string, want int) {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM activity_log WHERE issue_id = $1 AND action = $2`, f.issueID, action).Scan(&count); err != nil {
		t.Fatalf("count activities: %v", err)
	}
	if count != want {
		t.Fatalf("activity count for %s = %d, want %d", action, count, want)
	}
}

func assertDeliveryCount(t *testing.T, f integrationFixture, eventID, consumer string, want int) {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM domain_event_delivery WHERE event_id = $1 AND consumer = $2`, eventID, consumer).Scan(&count); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if count != want {
		t.Fatalf("delivery count for %s = %d, want %d", consumer, count, want)
	}
}

func assertOutboxComplete(t *testing.T, f integrationFixture, eventID string, wantAttempts int) {
	t.Helper()
	var processed bool
	var attempts int
	if err := f.pool.QueryRow(context.Background(), `SELECT processed_at IS NOT NULL, attempts FROM domain_event_outbox WHERE id = $1`, eventID).Scan(&processed, &attempts); err != nil {
		t.Fatalf("load outbox row: %v", err)
	}
	if !processed || attempts != wantAttempts {
		t.Fatalf("outbox state = processed:%v attempts:%d, want true/%d", processed, attempts, wantAttempts)
	}
}

func assertOutboxDeadLettered(t *testing.T, f integrationFixture, eventID string, wantAttempts int) {
	t.Helper()
	var deadLettered, processed bool
	var attempts int
	var reason string
	if err := f.pool.QueryRow(context.Background(), `
		SELECT dead_lettered_at IS NOT NULL, processed_at IS NOT NULL, attempts, COALESCE(dead_letter_reason, '')
		FROM domain_event_outbox WHERE id = $1
	`, eventID).Scan(&deadLettered, &processed, &attempts, &reason); err != nil {
		t.Fatalf("load dead-letter row: %v", err)
	}
	if !deadLettered || processed || attempts != wantAttempts || reason == "" {
		t.Fatalf("dead-letter state = dead:%v processed:%v attempts:%d reason:%q", deadLettered, processed, attempts, reason)
	}
}
