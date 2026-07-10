package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestTerminalTaskTokenRevocationCommitsWithTerminalState(t *testing.T) {
	tests := []struct {
		name        string
		status      string
		eventType   string
		terminalize func(context.Context, *chatCompletionFixture) error
	}{
		{
			name:      "completed",
			status:    "completed",
			eventType: protocol.EventTaskCompleted,
			terminalize: func(ctx context.Context, fixture *chatCompletionFixture) error {
				_, err := fixture.taskService.CompleteTask(ctx, fixture.task.ID, []byte(`{"output":"done"}`), "", "")
				return err
			},
		},
		{
			name:      "failed",
			status:    "failed",
			eventType: protocol.EventTaskFailed,
			terminalize: func(ctx context.Context, fixture *chatCompletionFixture) error {
				_, err := fixture.taskService.FailTask(ctx, fixture.task.ID, "failed", "", "", "test_failure")
				return err
			},
		},
		{
			name:      "cancelled",
			status:    "cancelled",
			eventType: protocol.EventTaskCancelled,
			terminalize: func(ctx context.Context, fixture *chatCompletionFixture) error {
				_, err := fixture.taskService.CancelTask(ctx, fixture.task.ID)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := setupChatCompletionFixture(t, ctx)
			createTaskToken(t, ctx, fixture)
			removeFailure := installTaskTokenDeleteFailure(t, fixture.task.ID)

			if err := test.terminalize(ctx, fixture); err == nil {
				t.Fatal("terminal transition succeeded despite forced token revocation failure")
			}
			assertTaskStatus(t, ctx, fixture.task.ID, "running")
			assertTaskTokenCount(t, ctx, fixture.task.ID, 1)
			assertTaskTerminalEventCount(t, ctx, fixture.task.ID, test.eventType, 0)

			removeFailure()
			if err := test.terminalize(ctx, fixture); err != nil {
				t.Fatalf("retry terminal transition: %v", err)
			}
			assertTaskStatus(t, ctx, fixture.task.ID, test.status)
			assertTaskTokenCount(t, ctx, fixture.task.ID, 0)
			assertTaskTerminalEventCount(t, ctx, fixture.task.ID, test.eventType, 1)
		})
	}
}

func createTaskToken(t *testing.T, ctx context.Context, fixture *chatCompletionFixture) {
	t.Helper()
	if _, err := fixture.queries.CreateTaskToken(ctx, db.CreateTaskTokenParams{
		TokenHash:   uuid.NewString(),
		TaskID:      fixture.task.ID,
		AgentID:     fixture.task.AgentID,
		WorkspaceID: parseUUID(testWorkspaceID),
		UserID:      parseUUID(testUserID),
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("CreateTaskToken: %v", err)
	}
}

func assertTaskStatus(t *testing.T, ctx context.Context, taskID pgtype.UUID, want string) {
	t.Helper()
	var got string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&got); err != nil {
		t.Fatalf("load task status: %v", err)
	}
	if got != want {
		t.Fatalf("task status = %q, want %q", got, want)
	}
}

func assertTaskTokenCount(t *testing.T, ctx context.Context, taskID pgtype.UUID, want int) {
	t.Helper()
	var got int
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM task_token WHERE task_id = $1`, taskID).Scan(&got); err != nil {
		t.Fatalf("count task tokens: %v", err)
	}
	if got != want {
		t.Fatalf("task token count = %d, want %d", got, want)
	}
}

func assertTaskTerminalEventCount(t *testing.T, ctx context.Context, taskID pgtype.UUID, eventType string, want int) {
	t.Helper()
	var got int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)::int FROM domain_event_outbox
		WHERE event_type = $1 AND payload->>'task_id' = $2
	`, eventType, util.UUIDToString(taskID)).Scan(&got); err != nil {
		t.Fatalf("count terminal task events: %v", err)
	}
	if got != want {
		t.Fatalf("terminal event count = %d, want %d", got, want)
	}
}

func installTaskTokenDeleteFailure(t *testing.T, taskID pgtype.UUID) func() {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	functionName := "task_token_delete_fail_fn_" + suffix
	triggerName := "task_token_delete_fail_" + suffix
	ctx := context.Background()
	remove := func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON task_token`, triggerName))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	}
	t.Cleanup(remove)
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.task_id = '%s' THEN
				RAISE EXCEPTION 'forced task token deletion failure';
			END IF;
			RETURN OLD;
		END;
		$$;
		CREATE TRIGGER %s
		BEFORE DELETE ON task_token
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, util.UUIDToString(taskID), triggerName, functionName)); err != nil {
		t.Fatalf("install task token deletion failure: %v", err)
	}
	return remove
}
