package main

import (
	"context"
	"fmt"

	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 委托结果使用已有 outbox 消费回执；重投复用同一恢复评论，不重新派发已投递结果。
func registerDurableDelegatedResultConsumers(dispatcher *eventoutbox.Dispatcher, tasks *service.TaskService) error {
	return registerDurableEvents(dispatcher, "delegated_result", func(ctx context.Context, q *db.Queries, event events.Event) ([]events.Event, error) {
		return consumeDelegatedResult(ctx, q, event, tasks)
	}, protocol.EventTaskCompleted, protocol.EventTaskCancelled)
}

func consumeDelegatedResult(ctx context.Context, q *db.Queries, event events.Event, tasks *service.TaskService) ([]events.Event, error) {
	_, task, exists, err := loadTaskProjectionRow(ctx, q, event)
	if err != nil || !exists {
		return nil, err
	}
	if event.WorkspaceID == "" || event.WorkspaceID != tasks.ResolveTaskWorkspaceID(ctx, task) {
		return nil, fmt.Errorf("delegated result event workspace mismatch")
	}
	return nil, tasks.RecoverDelegatedTaskResult(ctx, task)
}
