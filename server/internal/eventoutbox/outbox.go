package eventoutbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func Enqueue(ctx context.Context, q *db.Queries, event events.Event) (events.Event, error) {
	if q == nil || strings.TrimSpace(event.Type) == "" {
		return event, errors.New("event outbox: query handle and event type are required")
	}
	if event.Payload == nil {
		event.Payload = map[string]any{}
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return event, fmt.Errorf("marshal event payload: %w", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return event, errors.New("event outbox: payload must be a JSON object")
	}
	workspace := pgtype.UUID{}
	if event.WorkspaceID != "" {
		workspace, err = util.ParseUUID(event.WorkspaceID)
		if err != nil {
			return event, err
		}
	}
	row, err := q.CreateDomainEvent(ctx, db.CreateDomainEventParams{EventType: event.Type, StreamKey: text(event.StreamKey), WorkspaceID: workspace, ActorType: text(event.ActorType), ActorID: text(event.ActorID), TaskID: text(event.TaskID), ChatSessionID: text(event.ChatSessionID), Payload: payload})
	if err != nil {
		return event, err
	}
	event.ID = util.UUIDToString(row.ID)
	return event, nil
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
func interval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}

type TxStarter interface {
	Begin(context.Context) (pgx.Tx, error)
}
type Dispatcher struct {
	queries *db.Queries
	tx      TxStarter
	bus     *events.Bus
	owner   string
	logger  *slog.Logger
}

func NewDispatcher(q *db.Queries, tx TxStarter, bus *events.Bus, owner string) (*Dispatcher, error) {
	if q == nil || tx == nil || bus == nil || strings.TrimSpace(owner) == "" {
		return nil, errors.New("event outbox: invalid dispatcher configuration")
	}
	return &Dispatcher{queries: q, tx: tx, bus: bus, owner: owner, logger: slog.Default()}, nil
}

// Run replays events which were durable before a process restart. Processing
// is serialized per stream by the claim query and completion is lease fenced.
func (d *Dispatcher) Run(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		d.process(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
func (d *Dispatcher) process(ctx context.Context) {
	rows, err := d.queries.ClaimDomainEvents(ctx, db.ClaimDomainEventsParams{LeaseOwner: text(d.owner), LeaseDuration: interval(30 * time.Second), BatchSize: 50, EventTypes: []string{"task:completed", "task:failed", "task:cancelled", "comment:created", "comment:updated", "comment:deleted", "issue:created", "issue:updated", "autopilot:run_done", "reaction:added", "reaction:removed"}})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			d.logger.Warn("event outbox claim failed", "error", err)
		}
		return
	}
	for _, row := range rows {
		var payload any
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			d.fail(ctx, row, err)
			continue
		}
		d.bus.PublishRecovered(events.Event{ID: util.UUIDToString(row.ID), Type: row.EventType, StreamKey: row.StreamKey.String, WorkspaceID: util.UUIDToString(row.WorkspaceID), ActorType: row.ActorType.String, ActorID: row.ActorID.String, TaskID: row.TaskID.String, ChatSessionID: row.ChatSessionID.String, Payload: payload})
		_, _ = d.queries.CompleteDomainEvent(ctx, db.CompleteDomainEventParams{ID: row.ID, LeaseOwner: text(d.owner)})
	}
}
func (d *Dispatcher) fail(ctx context.Context, row db.DomainEventOutbox, err error) {
	_, _ = d.queries.RetryDomainEvent(ctx, db.RetryDomainEventParams{RetryDelay: interval(time.Second), LastError: text(err.Error()), ID: row.ID, LeaseOwner: text(d.owner)})
}
