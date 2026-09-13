// outbox.go 实现 domain event 的持久化事务发件箱(outbox)：业务代码在自己的数据库
// 事务里把领域事件一起落库（Enqueue），Dispatcher 后台轮询这张 outbox 表，按注册的
// 消费方逐事件投递（每个投影一次事务），并在同一事务里记录投递回执与消费方派生的新
// 事件。所有行以「认领 + 租约续期」的方式被分发，多副本不重复处理；失败进入指数退避
// 重试，超过上限进入死信(dead-letter)。
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

// Enqueue 通过调用方的查询句柄持久化一条领域事件。传「事务作用域」的 *db.Queries 时，
// 事件与业务改动会作为一次原子提交一起落库。
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
	// nil payload 也是合法事件（只是没有额外字段）。持久化成空对象 {}，让 outbox 契约
	// 对消费方保持「对象形状」。
	if event.Payload == nil {
		payload = []byte("{}")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return event, fmt.Errorf("event outbox: %s payload must be a JSON object", event.Type)
	}
	// 幂等键标识的是「逻辑事件」，而不是某次投递尝试。知道稳定业务 id 的生产方应提供
	// 它；老生产方会得到一个确定性摘要，这样进程重启后的重试也不会插出第二行。键里纳入
	// 信封作用域字段，避免「不同流里负载相同」的事件互相撞键。
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

// optionalText 把空串变成 pgtype.Text 的 NULL（空值语义：未设置而非空字符串）。
func optionalText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

// TxStarter 抽象「开事务」的能力，便于测试注入替身。
type TxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Consumer 用一个事务作用域的查询句柄应用一次持久投影。返回的事件会记进同一次投影
// 事务里，并且只在事务提交后才发布。
type Consumer func(context.Context, *db.Queries, events.Event) ([]events.Event, error)

// DeadLetterHandler 把某消费方的终态失败投影到它的领域聚合上。它在「把 outbox 行标记
// 为死信」的同一事务里运行，保证「用户可见的失败状态」不会与「运维侧的重试状态」漂移。
type DeadLetterHandler func(context.Context, *db.Queries, events.Event, error) error

// registeredConsumer 是一个事件类型下注册的消费方。
type registeredConsumer struct {
	name       string
	handler    Consumer
	deadLetter DeadLetterHandler
}

// dispatcherConfig 是 Dispatcher 的计时/批量参数（零值由 withDefaults 补齐）。
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

// withDefaults 用生产默认值补齐零值字段。
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

// Dispatcher 轮询 outbox 并把事件分发给已注册的消费方。
type Dispatcher struct {
	queries    *db.Queries
	txStarter  TxStarter
	bus        *events.Bus
	leaseOwner string // 认领租约的进程标识
	config     dispatcherConfig

	mu        sync.RWMutex
	consumers map[string][]registeredConsumer // 事件类型 -> 消费方列表
}

// NewDispatcher 构造 Dispatcher。queries、txStarter、bus、leaseOwner 都是必填。
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

// Register 注册某事件类型的消费方（不带死信处理器）。
func (d *Dispatcher) Register(eventType, name string, handler Consumer) error {
	return d.RegisterWithDeadLetter(eventType, name, handler, nil)
}

// RegisterWithDeadLetter 注册某事件类型的消费方与（可选的）死信处理器。
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

// Run 轮询直到 ctx 被取消。瞬时的认领/投递错误会被记录并重试；它们绝不会让 worker
// 静默退出。
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

// pruneExpired 删除超过保留期的已处理/死信事件，并清理没有归属 outbox 行的孤儿投递
// 回执记录。
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

// processBatch 认领一批到期事件并逐个处理。返回认领数与聚合的错误。
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

// processEvent 处理一条认领到的事件：对该事件的所有消费方逐个投递；全程保持租约存活。
func (d *Dispatcher) processEvent(ctx context.Context, row db.DomainEventOutbox) error {
	event := eventFromRow(row)
	d.mu.RLock()
	consumers := append([]registeredConsumer(nil), d.consumers[event.Type]...)
	d.mu.RUnlock()
	if len(consumers) == 0 {
		return fmt.Errorf("event %s has no registered consumers", event.Type)
	}

	// 一次投影可能涉及多轮数据库往返。运行期间要持续续期认领，否则别的 dispatcher
	// 可能只因为原租约到期就并发投递同一条事件。在每个消费方前后做同步续期，也让
	// 「租约丢失」在 handler 不理会 context 取消时依然可被观察到。
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

// checkLease 同步续一次租约；行已不再归我们时返回错误。
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

// renewLeaseLoop 在后台按租约的 1/3 周期持续续期；发现租约丢失就把错误推到 lost。
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

// deliver 对某消费方执行一次投递：在事务里查重（已有回执就直接成功）、跑 handler、
// 记录回执、持久化 handler 派生的事件，然后提交；提交成功后才把派生事件发布到进程内
// bus。
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

// retry 处理一次投递失败：超过最大尝试就死信；否则按指数退避（RetryBase 起步、
// MaxRetry 封顶）安排重试。
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

// deadLetter 把事件标记为死信。有死信处理器时，在同一事务里先跑投影再标记，保证两者
// 原子。
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

// eventFromRow 把持久化行还原成进程内事件。payload 解析失败时保留原始 JSON 字节，
// 让畸形历史行仍可被检查，而不是整条丢弃。
func eventFromRow(row db.DomainEventOutbox) events.Event {
	var payload any
	if err := json.Unmarshal(row.Payload, &payload); err != nil || payload == nil {
		// 本包写出的 payload 都是对象形状。把畸形的历史行保留成可检查状态，而不是
		// 直接丢弃整条事件；消费方可以拒绝它们并把失败路由到死信。
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

// EventFromRow 把持久化信封还原成进程内事件形态。要做「提交后通知」的调用方必须用
// 事务真正写下的那行，而不是从业务对象再造一份不持久的事件。
func EventFromRow(row db.DomainEventOutbox) events.Event {
	return eventFromRow(row)
}

// interval 把 time.Duration 换算成 pgtype.Interval（仅微秒字段）。
func interval(duration time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: duration.Microseconds(), Valid: true}
}
