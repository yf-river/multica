package eventoutbox

import (
	"context"
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
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return event, fmt.Errorf("event outbox: %s payload must be a JSON object", event.Type)
	}

	workspaceID := pgtype.UUID{}
	if event.WorkspaceID != "" {
		workspaceID, err = util.ParseUUID(event.WorkspaceID)
		if err != nil {
			return event, fmt.Errorf("event outbox: invalid workspace ID: %w", err)
		}
	}
	row, err := queries.CreateDomainEvent(ctx, db.CreateDomainEventParams{
		EventType:     event.Type,
		WorkspaceID:   workspaceID,
		ActorType:     optionalText(event.ActorType),
		ActorID:       optionalText(event.ActorID),
		TaskID:        optionalText(event.TaskID),
		ChatSessionID: optionalText(event.ChatSessionID),
		Payload:       payload,
	})
	if err != nil {
		return event, fmt.Errorf("event outbox: insert %s: %w", event.Type, err)
	}
	event.ID = util.UUIDToString(row.ID)
	return event, nil
}

func optionalText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

type TxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Consumer applies one durable projection using a transaction-scoped query
// handle. Returned events are ephemeral notifications published only after the
// projection transaction commits.
type Consumer func(context.Context, *db.Queries, events.Event) ([]events.Event, error)

type registeredConsumer struct {
	name    string
	handler Consumer
}

type DispatcherConfig struct {
	BatchSize    int32
	PollInterval time.Duration
	Lease        time.Duration
	RetryBase    time.Duration
	MaxRetry     time.Duration
	Logger       *slog.Logger
}

func (cfg DispatcherConfig) withDefaults() DispatcherConfig {
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
	config     DispatcherConfig

	mu        sync.RWMutex
	consumers map[string][]registeredConsumer
}

func NewDispatcher(queries *db.Queries, txStarter TxStarter, bus *events.Bus, leaseOwner string, cfg DispatcherConfig) (*Dispatcher, error) {
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
		config:     cfg.withDefaults(),
		consumers:  make(map[string][]registeredConsumer),
	}, nil
}

func (d *Dispatcher) Register(eventType, name string, handler Consumer) error {
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
	d.consumers[eventType] = append(d.consumers[eventType], registeredConsumer{name: name, handler: handler})
	return nil
}

// Run polls until ctx is cancelled. Transient claim/delivery errors are logged
// and retried; they never terminate the worker silently.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.config.PollInterval)
	defer ticker.Stop()
	for {
		if _, err := d.ProcessBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
			d.config.Logger.Error("domain event dispatch failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) ProcessBatch(ctx context.Context) (int, error) {
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

	for _, consumer := range consumers {
		if err := d.deliver(ctx, row.ID, event, consumer); err != nil {
			retryErr := d.retry(ctx, row, err)
			return errors.Join(fmt.Errorf("deliver %s to %s: %w", event.Type, consumer.name, err), retryErr)
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

func (d *Dispatcher) deliver(ctx context.Context, eventID pgtype.UUID, event events.Event, consumer registeredConsumer) error {
	tx, err := d.txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin consumer transaction: %w", err)
	}
	defer tx.Rollback(ctx)
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
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit consumer transaction: %w", err)
	}
	for _, emittedEvent := range emitted {
		d.bus.Publish(emittedEvent)
	}
	return nil
}

func (d *Dispatcher) retry(ctx context.Context, row db.DomainEventOutbox, cause error) error {
	delay := d.config.RetryBase
	for attempt := int32(0); attempt < row.Attempts && delay < d.config.MaxRetry; attempt++ {
		if delay > d.config.MaxRetry/2 {
			delay = d.config.MaxRetry
			break
		}
		delay *= 2
	}
	message := cause.Error()
	if len(message) > 2000 {
		message = message[:2000]
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

func eventFromRow(row db.DomainEventOutbox) events.Event {
	return events.Event{
		ID:            util.UUIDToString(row.ID),
		Type:          row.EventType,
		WorkspaceID:   util.UUIDToString(row.WorkspaceID),
		ActorType:     row.ActorType.String,
		ActorID:       row.ActorID.String,
		TaskID:        row.TaskID.String,
		ChatSessionID: row.ChatSessionID.String,
		Payload:       json.RawMessage(row.Payload),
	}
}

func interval(duration time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: duration.Microseconds(), Valid: true}
}
