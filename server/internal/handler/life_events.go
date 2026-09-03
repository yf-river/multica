package handler

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// recordLifeChangedTx records one committed Life mutation in the caller's
// transaction. Life rows are the canonical state; this envelope gives the
// realtime and recovery layers an ordered, inspectable fact without copying
// the row into a second store.
func recordLifeChangedTx(
	ctx context.Context,
	queries *db.Queries,
	scope lifeRequestScope,
	actorType string,
	actorID pgtype.UUID,
	entityType string,
	entityID string,
	action string,
	extra map[string]any,
) (events.Event, error) {
	payload := map[string]any{
		"entity_type": entityType,
		"entity_id":   entityID,
		"action":      action,
	}
	for key, value := range extra {
		payload[key] = value
	}
	event := events.Event{
		Type:        protocol.EventLifeChanged,
		StreamKey:   "life:" + util.UUIDToString(scope.workspaceID) + ":" + util.UUIDToString(scope.userID),
		WorkspaceID: util.UUIDToString(scope.workspaceID),
		ActorType:   actorType,
		ActorID:     util.UUIDToString(actorID),
		Payload:     payload,
	}
	return service.RecordDurableEventTx(ctx, queries, event)
}

// publishLifeEvents emits only envelopes that were committed by the caller's
// transaction.  Keeping this after-commit boundary makes a websocket update a
// notification of canonical Life state rather than a second source of truth.
func (h *Handler) publishLifeEvents(items ...events.Event) {
	for _, item := range items {
		h.publishEvent(item)
	}
}

// recordAndCommitLifeChanged is for handlers whose transaction contains one
// user-visible Life mutation.  It returns the committed envelope so callers
// can publish it after the commit without accidentally notifying on rollback.
func recordAndCommitLifeChanged(
	ctx context.Context,
	tx pgx.Tx,
	queries *db.Queries,
	scope lifeRequestScope,
	entityType, entityID, action string,
	extra map[string]any,
) (events.Event, error) {
	return recordAndCommitLifeChangedAs(ctx, tx, queries, scope, "member", scope.userID, entityType, entityID, action, extra)
}

func recordAndCommitLifeChangedAs(
	ctx context.Context,
	tx pgx.Tx,
	queries *db.Queries,
	scope lifeRequestScope,
	actorType string,
	actorID pgtype.UUID,
	entityType, entityID, action string,
	extra map[string]any,
) (events.Event, error) {
	event, err := recordLifeChangedTx(ctx, queries, scope, actorType, actorID, entityType, entityID, action, extra)
	if err != nil {
		return events.Event{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return events.Event{}, err
	}
	return event, nil
}
