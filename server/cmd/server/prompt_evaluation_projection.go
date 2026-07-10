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

const promptEvaluationProjectionConsumer = "prompt_evaluation_projection"

func registerDurablePromptEvaluationConsumers(dispatcher *eventoutbox.Dispatcher) error {
	for _, eventType := range []string{
		protocol.EventTaskCompleted,
		protocol.EventTaskFailed,
		protocol.EventTaskCancelled,
	} {
		if err := dispatcher.Register(eventType, promptEvaluationProjectionConsumer, consumePromptEvaluationProjection); err != nil {
			return err
		}
	}
	return nil
}

func consumePromptEvaluationProjection(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	_, task, exists, err := loadTaskProjectionRow(ctx, queries, event)
	if err != nil || !exists {
		return nil, err
	}
	if _, err := service.ProjectPromptEvaluationTerminalTask(ctx, queries, task); err != nil {
		return nil, fmt.Errorf("project prompt evaluation task %s: %w", task.Status, err)
	}
	return nil, nil
}
