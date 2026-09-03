package eventoutbox

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Enqueue persists a domain event through the caller's query handle. Passing a
// transaction-scoped *db.Queries makes the event and the business mutation one
// atomic commit.
func Enqueue(ctx context.Context, queries *db.Queries, event events.Event) (events.Event, error) {
	if queries == nil {
		return event, errors.New("event outbox: queries are required")
	}
	if strings.TrimSpace(event.Type) == "" {
		return event, errors.New("event outbox: event type is required")
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return event, fmt.Errorf("event outbox: marshal %s payload: %w", event.Type, err)
	}
	// A nil payload is a valid event with no additional fields. Persist it as
	// an empty object so the outbox contract stays object-shaped for consumers.
	if event.Payload == nil {
		payload = []byte("{}")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return event, fmt.Errorf("event outbox: %s payload must be a JSON object", event.Type)
	}
	// The key is the identity of the logical occurrence, not the delivery
	// attempt.  Producers that know a stable business id should provide it;
	// older producers get a deterministic digest so a retry after a process
	// restart cannot create a second row.  Including the envelope scope keeps
	// equal payloads from unrelated streams independent.
	if strings.TrimSpace(event.IdempotencyKey) == "" {
		if strings.TrimSpace(event.ID) != "" {
			event.IdempotencyKey = event.ID
		} else {
			hashInput := struct {
				Type          string          `json:"type"`
				StreamKey     string          `json:"stream_key"`
				WorkspaceID   string          `json:"workspace_id"`
				ActorType     string          `json:"actor_type"`
				ActorID       string          `json:"actor_id"`
				TaskID        string          `json:"task_id"`
				ChatSessionID string          `json:"chat_session_id"`
				Payload       json.RawMessage `json:"payload"`
			}{event.Type, event.StreamKey, event.WorkspaceID, event.ActorType, event.ActorID, event.TaskID, event.ChatSessionID, payload}
			keyBytes, _ := json.Marshal(hashInput)
			digest := sha256.Sum256(keyBytes)
			event.IdempotencyKey = "sha256:" + fmt.Sprintf("%x", digest[:])
		}
	}

	workspaceID := pgtype.UUID{}
	if event.WorkspaceID != "" {
		workspaceID, err = util.ParseUUID(event.WorkspaceID)
		if err != nil {
			return event, fmt.Errorf("event outbox: invalid workspace ID: %w", err)
		}
	}
	row, err := queries.CreateDomainEvent(ctx, db.CreateDomainEventParams{
		IdempotencyKey: event.IdempotencyKey,
		EventType:      event.Type,
		StreamKey:      optionalText(event.StreamKey),
		WorkspaceID:    workspaceID,
		ActorType:      optionalText(event.ActorType),
		ActorID:        optionalText(event.ActorID),
		TaskID:         optionalText(event.TaskID),
		ChatSessionID:  optionalText(event.ChatSessionID),
		Payload:        payload,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return event, fmt.Errorf("event outbox: idempotency key %q is already bound to a different event", event.IdempotencyKey)
		}
		return event, fmt.Errorf("event outbox: insert %s: %w", event.Type, err)
	}
	event.ID = util.UUIDToString(row.ID)
	event.IdempotencyKey = row.IdempotencyKey
	return event, nil
}

func optionalText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

type TxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Consumer applies one durable projection using a transaction-scoped query
// handle. Returned events are recorded in the same projection transaction and
// published only after that transaction commits.
type Consumer func(context.Context, *db.Queries, events.Event) ([]events.Event, error)

// DeadLetterHandler projects a consumer's terminal failure into its domain
// aggregate. It runs in the same transaction that marks the outbox row as
// dead-lettered, so user-visible failure state cannot drift from the
// operational retry state.
type DeadLetterHandler func(context.Context, *db.Queries, events.Event, error) error

type registeredConsumer struct {
	name       string
	handler    Consumer
	deadLetter DeadLetterHandler
}

type dispatcherConfig struct {
	BatchSize           int32
	PollInterval        time.Duration
	Lease               time.Duration
	RetryBase           time.Duration
	MaxRetry            time.Duration
	MaxAttempts         int32
	CleanupInterval     time.Duration
	ProcessedRetention  time.Duration
	DeadLetterRetention time.Duration
	CleanupBatchSize    int32
	Logger              *slog.Logger
}

func (cfg dispatcherConfig) withDefaults() dispatcherConfig {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.Lease <= 0 {
		cfg.Lease = 30 * time.Second
	}
	if cfg.RetryBase <= 0 {
		cfg.RetryBase = time.Second
	}
	if cfg.MaxRetry <= 0 {
		cfg.MaxRetry = time.Minute
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 12
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 10 * time.Minute
	}
	if cfg.ProcessedRetention <= 0 {
		cfg.ProcessedRetention = 7 * 24 * time.Hour
	}
	if cfg.DeadLetterRetention <= 0 {
		cfg.DeadLetterRetention = 30 * 24 * time.Hour
	}
	if cfg.CleanupBatchSize <= 0 {
		cfg.CleanupBatchSize = 5000
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return cfg
}

type Dispatcher struct {
	queries    *db.Queries
	txStarter  TxStarter
	bus        *events.Bus
	leaseOwner string
	config     dispatcherConfig

	mu        sync.RWMutex
	consumers map[string][]registeredConsumer
}

func NewDispatcher(queries *db.Queries, txStarter TxStarter, bus *events.Bus, leaseOwner string) (*Dispatcher, error) {
	if queries == nil || txStarter == nil || bus == nil {
		return nil, errors.New("event outbox: queries, transaction starter, and bus are required")
	}
	leaseOwner = strings.TrimSpace(leaseOwner)
	if leaseOwner == "" {
		return nil, errors.New("event outbox: lease owner is required")
	}
	return &Dispatcher{
		queries:    queries,
		txStarter:  txStarter,
		bus:        bus,
		leaseOwner: leaseOwner,
		config:     (dispatcherConfig{}).withDefaults(),
		consumers:  make(map[string][]registeredConsumer),
	}, nil
}

func (d *Dispatcher) Register(eventType, name string, handler Consumer) error {
	return d.RegisterWithDeadLetter(eventType, name, handler, nil)
}

func (d *Dispatcher) RegisterWithDeadLetter(eventType, name string, handler Consumer, deadLetter DeadLetterHandler) error {
	eventType = strings.TrimSpace(eventType)
	name = strings.TrimSpace(name)
	if eventType == "" || name == "" || handler == nil {
		return errors.New("event outbox: event type, consumer name, and handler are required")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, existing := range d.consumers[eventType] {
		if existing.name == name {
			return fmt.Errorf("event outbox: consumer %q already registered for %s", name, eventType)
		}
	}
	d.consumers[eventType] = append(d.consumers[eventType], registeredConsumer{name: name, handler: handler, deadLetter: deadLetter})
	return nil
}

// Run polls until ctx is cancelled. Transient claim/delivery errors are logged
// and retried; they never terminate the worker silently.
func (d *Dispatcher) Run(ctx context.Context) {
	pollTicker := time.NewTicker(d.config.PollInterval)
	cleanupTicker := time.NewTicker(d.config.CleanupInterval)
	defer pollTicker.Stop()
	defer cleanupTicker.Stop()
	for {
		if _, err := d.processBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
			d.config.Logger.Error("domain event dispatch failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-pollTicker.C:
		case <-cleanupTicker.C:
			if _, err := d.pruneExpired(ctx); err != nil && !errors.Is(err, context.Canceled) {
				d.config.Logger.Error("domain event retention cleanup failed", "error", err)
			}
		}
	}
}

func (d *Dispatcher) pruneExpired(ctx context.Context) (int64, error) {
	now := time.Now()
	deleted, err := d.queries.DeleteExpiredDomainEvents(ctx, db.DeleteExpiredDomainEventsParams{
		ProcessedBefore:    pgtype.Timestamptz{Time: now.Add(-d.config.ProcessedRetention), Valid: true},
		DeadLetteredBefore: pgtype.Timestamptz{Time: now.Add(-d.config.DeadLetterRetention), Valid: true},
		BatchSize:          d.config.CleanupBatchSize,
	})
	if err != nil {
		return 0, fmt.Errorf("delete expired domain events: %w", err)
	}
	if _, err := d.queries.DeleteOrphanedDomainEventDeliveries(ctx); err != nil {
		return deleted, fmt.Errorf("delete orphaned domain event deliveries: %w", err)
	}
	if deleted > 0 {
		d.config.Logger.Info("expired domain events deleted", "count", deleted)
	}
	return deleted, nil
}

func (d *Dispatcher) processBatch(ctx context.Context) (int, error) {
	d.mu.RLock()
	eventTypes := make([]string, 0, len(d.consumers))
	for eventType := range d.consumers {
		eventTypes = append(eventTypes, eventType)
	}
	d.mu.RUnlock()
	sort.Strings(eventTypes)
	if len(eventTypes) == 0 {
		return 0, nil
	}
	rows, err := d.queries.ClaimDomainEvents(ctx, db.ClaimDomainEventsParams{
		LeaseOwner:    optionalText(d.leaseOwner),
		LeaseDuration: interval(d.config.Lease),
		BatchSize:     d.config.BatchSize,
		EventTypes:    eventTypes,
	})
	if err != nil {
		return 0, fmt.Errorf("claim domain events: %w", err)
	}
	var failures []error
	for _, row := range rows {
		if err := d.processEvent(ctx, row); err != nil {
			failures = append(failures, err)
		}
	}
	return len(rows), errors.Join(failures...)
}

func (d *Dispatcher) processEvent(ctx context.Context, row db.DomainEventOutbox) error {
	event := eventFromRow(row)
	d.mu.RLock()
	consumers := append([]registeredConsumer(nil), d.consumers[event.Type]...)
	d.mu.RUnlock()
	if len(consumers) == 0 {
		return fmt.Errorf("event %s has no registered consumers", event.Type)
	}

	// A projection can involve more than one database round trip. Keep the
	// claim alive while it runs so another dispatcher cannot start a concurrent
	// delivery merely because the original lease elapsed. The synchronous
	// renewal before/after every consumer also makes lease loss observable even
	// when the handler does not honour context cancellation.
	leaseCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	leaseLost := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	go d.renewLeaseLoop(leaseCtx, row.ID, leaseLost, heartbeatDone)
	defer func() {
		cancel()
		<-heartbeatDone
	}()

	for _, consumer := range consumers {
		if err := d.checkLease(ctx, row.ID); err != nil {
			return err
		}
		select {
		case err := <-leaseLost:
			return err
		default:
		}
		if err := d.deliver(ctx, row.ID, event, consumer); err != nil {
			retryErr := d.retry(ctx, row, event, consumer, err)
			return errors.Join(fmt.Errorf("deliver %s to %s: %w", event.Type, consumer.name, err), retryErr)
		}
		if err := d.checkLease(ctx, row.ID); err != nil {
			return err
		}
	}

	updated, err := d.queries.CompleteDomainEvent(ctx, db.CompleteDomainEventParams{
		ID:         row.ID,
		LeaseOwner: optionalText(d.leaseOwner),
	})
	if err != nil {
		return fmt.Errorf("complete domain event %s: %w", event.ID, err)
	}
	if updated != 1 {
		return fmt.Errorf("complete domain event %s: lease lost", event.ID)
	}
	return nil
}

func (d *Dispatcher) checkLease(ctx context.Context, eventID pgtype.UUID) error {
	updated, err := d.queries.RenewDomainEventLease(ctx, db.RenewDomainEventLeaseParams{
		ID:            eventID,
		LeaseOwner:    optionalText(d.leaseOwner),
		LeaseDuration: interval(d.config.Lease),
	})
	if err != nil {
		return fmt.Errorf("renew domain event %s lease: %w", util.UUIDToString(eventID), err)
	}
	if updated != 1 {
		return fmt.Errorf("renew domain event %s lease: lease lost", util.UUIDToString(eventID))
	}
	return nil
}

func (d *Dispatcher) renewLeaseLoop(ctx context.Context, eventID pgtype.UUID, lost chan<- error, done chan<- struct{}) {
	defer close(done)
	intervalDuration := d.config.Lease / 3
	if intervalDuration <= 0 {
		intervalDuration = time.Millisecond
	}
	ticker := time.NewTicker(intervalDuration)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.checkLease(ctx, eventID); err != nil {
				select {
				case lost <- err:
				default:
				}
				return
			}
		}
	}
}

func (d *Dispatcher) deliver(ctx context.Context, eventID pgtype.UUID, event events.Event, consumer registeredConsumer) error {
	tx, err := d.txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin consumer transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := d.queries.WithTx(tx)

	delivered, err := queries.HasDomainEventDelivery(ctx, db.HasDomainEventDeliveryParams{
		EventID:  eventID,
		Consumer: consumer.name,
	})
	if err != nil {
		return fmt.Errorf("check delivery receipt: %w", err)
	}
	if delivered {
		return nil
	}

	emitted, err := consumer.handler(ctx, queries, event)
	if err != nil {
		return err
	}
	if err := queries.RecordDomainEventDelivery(ctx, db.RecordDomainEventDeliveryParams{
		EventID:  eventID,
		Consumer: consumer.name,
	}); err != nil {
		return fmt.Errorf("record delivery receipt: %w", err)
	}
	for i, emittedEvent := range emitted {
		persisted, err := Enqueue(ctx, queries, emittedEvent)
		if err != nil {
			return fmt.Errorf("persist emitted event %d (%s): %w", i, emittedEvent.Type, err)
		}
		emitted[i] = persisted
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit consumer transaction: %w", err)
	}
	for _, emittedEvent := range emitted {
		d.bus.PublishRecovered(emittedEvent)
	}
	return nil
}

func (d *Dispatcher) retry(ctx context.Context, row db.DomainEventOutbox, event events.Event, consumer registeredConsumer, cause error) error {
	message := cause.Error()
	if len(message) > 2000 {
		message = message[:2000]
	}
	if row.Attempts+1 >= d.config.MaxAttempts {
		reason := "consumer " + consumer.name + ": " + message
		updated, err := d.deadLetter(ctx, row.ID, event, consumer, cause, reason)
		if err != nil {
			return err
		}
		if updated != 1 {
			return fmt.Errorf("dead-letter domain event: lease lost")
		}
		d.config.Logger.Error("domain event dead-lettered",
			"event_id", util.UUIDToString(row.ID),
			"event_type", row.EventType,
			"consumer", consumer.name,
			"attempts", row.Attempts+1,
		)
		return nil
	}
	delay := d.config.RetryBase
	for attempt := int32(0); attempt < row.Attempts && delay < d.config.MaxRetry; attempt++ {
		if delay > d.config.MaxRetry/2 {
			delay = d.config.MaxRetry
			break
		}
		delay *= 2
	}
	updated, err := d.queries.RetryDomainEvent(ctx, db.RetryDomainEventParams{
		RetryDelay: interval(delay),
		LastError:  optionalText(message),
		ID:         row.ID,
		LeaseOwner: optionalText(d.leaseOwner),
	})
	if err != nil {
		return fmt.Errorf("schedule domain event retry: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("schedule domain event retry: lease lost")
	}
	return nil
}

func (d *Dispatcher) deadLetter(
	ctx context.Context,
	eventID pgtype.UUID,
	event events.Event,
	consumer registeredConsumer,
	cause error,
	reason string,
) (int64, error) {
	if consumer.deadLetter == nil {
		updated, err := d.queries.DeadLetterDomainEvent(ctx, db.DeadLetterDomainEventParams{
			DeadLetterReason: optionalText(reason),
			ID:               eventID,
			LeaseOwner:       optionalText(d.leaseOwner),
		})
		if err != nil {
			return 0, fmt.Errorf("dead-letter domain event: %w", err)
		}
		return updated, nil
	}
	tx, err := d.txStarter.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin dead-letter transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := d.queries.WithTx(tx)
	if err := consumer.deadLetter(ctx, queries, event, cause); err != nil {
		return 0, fmt.Errorf("project domain event dead letter: %w", err)
	}
	updated, err := queries.DeadLetterDomainEvent(ctx, db.DeadLetterDomainEventParams{
		DeadLetterReason: optionalText(reason),
		ID:               eventID,
		LeaseOwner:       optionalText(d.leaseOwner),
	})
	if err != nil {
		return 0, fmt.Errorf("dead-letter domain event: %w", err)
	}
	if updated != 1 {
		return 0, fmt.Errorf("dead-letter domain event: lease lost")
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit domain event dead letter: %w", err)
	}
	return updated, nil
}

func eventFromRow(row db.DomainEventOutbox) events.Event {
	var payload any
	if err := json.Unmarshal(row.Payload, &payload); err != nil || payload == nil {
		// Payloads written by this package are object-shaped. Keep malformed
		// historical rows inspectable instead of dropping the event entirely;
		// consumers can reject them and route the failure to dead letter.
		payload = json.RawMessage(row.Payload)
	}
	return events.Event{
		ID:             util.UUIDToString(row.ID),
		IdempotencyKey: row.IdempotencyKey,
		Type:           row.EventType,
		StreamKey:      row.StreamKey.String,
		WorkspaceID:    util.UUIDToString(row.WorkspaceID),
		ActorType:      row.ActorType.String,
		ActorID:        row.ActorID.String,
		TaskID:         row.TaskID.String,
		ChatSessionID:  row.ChatSessionID.String,
		Payload:        payload,
	}
}

// EventFromRow converts a persisted envelope back to the in-process event
// shape.  Callers that publish a post-commit notification must use the row
// written by the transaction rather than rebuilding a second, non-durable
// event from the business object.
func EventFromRow(row db.DomainEventOutbox) events.Event {
	return eventFromRow(row)
}

func interval(duration time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: duration.Microseconds(), Valid: true}
}
