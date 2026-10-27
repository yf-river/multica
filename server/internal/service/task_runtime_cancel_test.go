package service

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestRuntimeCancellationPreservesSessionUsageAndTerminalEvent(t *testing.T) {
	pool := newCancelFinalizePool(t)
	ctx := context.Background()
	fixture := createCancelFinalizeFixture(t, ctx, pool, "running", true)
	taskID := util.MustParseUUID(fixture.taskID)
	if _, err := pool.Exec(ctx, `INSERT INTO task_usage(task_id,provider,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES($1,'codebuddy','observed-model',123,17,0,0)`, taskID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM task_usage WHERE task_id=$1`, taskID)
		_, _ = pool.Exec(ctx, `DELETE FROM task_trace_event WHERE task_id=$1`, taskID)
	})
	bus := events.New()
	cancelled, failed := 0, 0
	bus.Subscribe(protocol.EventTaskCancelled, func(event events.Event) {
		cancelled++
		if event.TaskID != fixture.taskID {
			t.Errorf("cancel event task = %s", event.TaskID)
		}
	})
	bus.Subscribe(protocol.EventTaskFailed, func(events.Event) { failed++ })
	svc := NewTaskService(db.New(pool), pool, nil, bus)
	task, changed, err := svc.FailTaskWithTransition(ctx, taskID, "", "native-paused-session", "/owned/goal-work", "", "cancelled", false, "", "/owned/goal-work")
	if err != nil {
		t.Fatal(err)
	}
	if !changed || task.Status != "cancelled" || task.FailureReason.String != "cancelled" {
		t.Fatalf("runtime cancellation became failure: changed=%v task=%+v", changed, task)
	}
	if task.SessionID.String != "native-paused-session" || task.WorkDir.String != "/owned/goal-work" || task.DurableWorkDir.String != "/owned/goal-work" {
		t.Fatalf("cancel lost resume metadata: %+v", task)
	}
	var session, work string
	if err := pool.QueryRow(ctx, `SELECT session_id,work_dir FROM chat_session WHERE id=$1`, fixture.chatSessionID).Scan(&session, &work); err != nil {
		t.Fatal(err)
	}
	if session != "native-paused-session" || work != "/owned/goal-work" {
		t.Fatalf("chat resume pointer = %q %q", session, work)
	}
	_, changed, err = svc.FailTaskWithTransition(ctx, taskID, "", "native-paused-session", "/owned/goal-work", "", "cancelled", false, "", "/owned/goal-work")
	if err != nil || changed {
		t.Fatalf("cancellation replay changed terminal row: changed=%v err=%v", changed, err)
	}
	if cancelled != 1 || failed != 0 {
		t.Fatalf("terminal events: cancelled=%d failed=%d", cancelled, failed)
	}
	var cancelTraces, failTraces, usage, userMessages, assistantMessages, retries int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE event_type='task.cancelled'),count(*) FILTER(WHERE event_type='task.failed') FROM task_trace_event WHERE task_id=$1`, taskID).Scan(&cancelTraces, &failTraces); err != nil {
		t.Fatal(err)
	}
	if cancelTraces != 1 || failTraces != 0 {
		t.Fatalf("terminal traces: cancelled=%d failed=%d", cancelTraces, failTraces)
	}
	if err := pool.QueryRow(ctx, `SELECT sum(input_tokens+output_tokens)::int FROM task_usage WHERE task_id=$1`, taskID).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if usage != 140 {
		t.Fatalf("cancel lost observed usage: %d", usage)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE role='user'),count(*) FILTER(WHERE role='assistant' AND failure_reason='cancelled') FROM chat_message WHERE chat_session_id=$1`, fixture.chatSessionID).Scan(&userMessages, &assistantMessages); err != nil {
		t.Fatal(err)
	}
	if userMessages != 1 || assistantMessages != 1 {
		t.Fatalf("cancel chat transcript: user=%d assistant=%d", userMessages, assistantMessages)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id=$1`, taskID).Scan(&retries); err != nil {
		t.Fatal(err)
	}
	if retries != 0 {
		t.Fatalf("native user stop created retry: %d", retries)
	}
}
