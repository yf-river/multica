// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（life_events.go）是"人生"写接口与实时/恢复层之间的桥梁：在调用方
// 事务里把一次 Life 变更记成一条持久化领域事件（life 变更信封），并在事务
// 提交后把信封发布出去，避免"回滚了却还通知"。
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

// recordLifeChangedTx 在调用方事务里记录一次已提交的 Life 变更。Life 表本身
// 是权威状态，这个信封只是给实时层与恢复层一份有序、可检视的事实，
// 而不必把整行复制进第二个存储。
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

// publishLifeEvents 只发布调用方事务已提交的信封。守住"提交后"这个边界，
// 让 websocket 更新始终是对权威 Life 状态的通知，而不是第二数据源。
func (h *Handler) publishLifeEvents(items ...events.Event) {
	for _, item := range items {
		h.publishEvent(item)
	}
}

// recordAndCommitLifeChanged 供"事务里只有一次用户可见 Life 变更"的处理器使用：
// 记录事件后立即提交，并把已提交的信封返回给调用方，让其在提交完成后发布，
// 避免在回滚路径上误发通知。
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

// recordAndCommitLifeChangedAs 是 recordAndCommitLifeChanged 的可指定操作者版本，
// 供 agent 等非成员主体写入 Life 变更时使用。
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
