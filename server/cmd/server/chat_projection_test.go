package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type chatCompletionFixture struct {
	queries       *db.Queries
	taskService   *service.TaskService
	task          db.AgentTaskQueue
	session       db.ChatSession
	chatDoneCount int
}

func TestChatCompletionLeavesMessageProjectionToOutbox(t *testing.T) {
	ctx := context.Background()
	fixture := setupChatCompletionFixture(t, ctx)
	installAssistantMessageFailure(t)

	if _, err := fixture.taskService.CompleteTask(ctx, fixture.task.ID, []byte(`{"output":"hello\\nworld"}`), "", ""); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if fixture.chatDoneCount != 0 {
		t.Fatalf("CompleteTask emitted %d chat:done events before durable message projection", fixture.chatDoneCount)
	}
	assertAssistantMessageCount(t, ctx, fixture.task.ID, 0)
	session, err := fixture.queries.GetChatSession(ctx, fixture.session.ID)
	if err != nil {
		t.Fatalf("GetChatSession: %v", err)
	}
	if session.UnreadSince.Valid {
		t.Fatal("CompleteTask marked chat unread before durable message projection")
	}
}

func TestChatCompletionProjectionRollsBackAndRetries(t *testing.T) {
	ctx := context.Background()
	fixture := setupChatCompletionFixture(t, ctx)
	if _, err := fixture.taskService.CompleteTask(ctx, fixture.task.ID, []byte(`{"output":"hello\\nworld"}`), "", ""); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	event := latestTaskTerminalEvent(t, fixture.task.ID)
	removeFailure := installAssistantMessageFailure(t)

	if _, err := runChatProjection(ctx, fixture.queries, event, consumeChatCompletionProjection); err == nil {
		t.Fatal("chat completion projection succeeded despite forced message failure")
	}
	assertAssistantMessageCount(t, ctx, fixture.task.ID, 0)
	assertChatSessionUnread(t, ctx, fixture.queries, fixture.session.ID, false)

	removeFailure()
	emitted, err := runChatProjection(ctx, fixture.queries, event, consumeChatCompletionProjection)
	if err != nil {
		t.Fatalf("retry chat completion projection: %v", err)
	}
	if len(emitted) != 1 || emitted[0].Type != protocol.EventChatDone {
		t.Fatalf("chat completion emitted events = %#v", emitted)
	}
	payload, ok := emitted[0].Payload.(protocol.ChatDonePayload)
	if !ok {
		t.Fatalf("chat:done payload type = %T", emitted[0].Payload)
	}
	if payload.MessageID == "" || payload.Content != "hello\nworld" || payload.ChatSessionID != util.UUIDToString(fixture.session.ID) {
		t.Fatalf("chat:done payload = %+v", payload)
	}
	assertAssistantMessageCount(t, ctx, fixture.task.ID, 1)
	assertChatSessionUnread(t, ctx, fixture.queries, fixture.session.ID, true)
}

func TestChatCompletionProjectionRollsBackMessageWhenUnreadUpdateFails(t *testing.T) {
	ctx := context.Background()
	fixture := setupChatCompletionFixture(t, ctx)
	if _, err := fixture.taskService.CompleteTask(ctx, fixture.task.ID, []byte(`{"output":"transactional reply"}`), "", ""); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	removeFailure := installChatUnreadFailure(t, fixture.session.ID)

	if _, err := runChatProjection(ctx, fixture.queries, latestTaskTerminalEvent(t, fixture.task.ID), consumeChatCompletionProjection); err == nil {
		t.Fatal("chat completion projection succeeded despite forced unread update failure")
	}
	assertAssistantMessageCount(t, ctx, fixture.task.ID, 0)
	assertChatSessionUnread(t, ctx, fixture.queries, fixture.session.ID, false)

	removeFailure()
	if _, err := runChatProjection(ctx, fixture.queries, latestTaskTerminalEvent(t, fixture.task.ID), consumeChatCompletionProjection); err != nil {
		t.Fatalf("retry chat completion after unread failure: %v", err)
	}
	assertAssistantMessageCount(t, ctx, fixture.task.ID, 1)
	assertChatSessionUnread(t, ctx, fixture.queries, fixture.session.ID, true)
}

func TestChatCompletionProjectionKeepsEmptyOutputContract(t *testing.T) {
	ctx := context.Background()
	fixture := setupChatCompletionFixture(t, ctx)
	if _, err := fixture.taskService.CompleteTask(ctx, fixture.task.ID, []byte(`{}`), "", ""); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	emitted, err := runChatProjection(ctx, fixture.queries, latestTaskTerminalEvent(t, fixture.task.ID), consumeChatCompletionProjection)
	if err != nil {
		t.Fatalf("project empty chat completion: %v", err)
	}
	payload := emitted[0].Payload.(protocol.ChatDonePayload)
	if payload.MessageID != "" || payload.Content != "" {
		t.Fatalf("empty completion unexpectedly created message payload: %+v", payload)
	}
	assertAssistantMessageCount(t, ctx, fixture.task.ID, 0)
	assertChatSessionUnread(t, ctx, fixture.queries, fixture.session.ID, false)
}

func TestChatFailureLeavesMessageProjectionToOutbox(t *testing.T) {
	ctx := context.Background()
	fixture := setupChatCompletionFixture(t, ctx)
	installChatUnreadFailure(t, fixture.session.ID)

	if _, err := fixture.taskService.FailTask(ctx, fixture.task.ID, "invalid agent request", "", "", "api_invalid_request"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	assertAssistantMessageCount(t, ctx, fixture.task.ID, 0)
	assertChatSessionUnread(t, ctx, fixture.queries, fixture.session.ID, false)
}

func TestChatFailureProjectionRollsBackAndRedacts(t *testing.T) {
	ctx := context.Background()
	fixture := setupChatCompletionFixture(t, ctx)
	const secret = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJmYWlsZWQtY2hhdCJ9.signature"
	if _, err := fixture.taskService.FailTask(
		ctx,
		fixture.task.ID,
		"Authorization: Bearer "+secret,
		"",
		"",
		"api_invalid_request",
	); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	event := latestTaskTerminalEvent(t, fixture.task.ID)
	removeFailure := installChatUnreadFailure(t, fixture.session.ID)

	if _, err := runChatProjection(ctx, fixture.queries, event, consumeChatFailureProjection); err == nil {
		t.Fatal("chat failure projection succeeded despite forced unread update failure")
	}
	assertAssistantMessageCount(t, ctx, fixture.task.ID, 0)
	assertChatSessionUnread(t, ctx, fixture.queries, fixture.session.ID, false)

	removeFailure()
	emitted, err := runChatProjection(ctx, fixture.queries, event, consumeChatFailureProjection)
	if err != nil {
		t.Fatalf("retry chat failure projection: %v", err)
	}
	if len(emitted) != 1 || emitted[0].Type != protocol.EventChatMessage {
		t.Fatalf("chat failure emitted events = %#v", emitted)
	}
	payload, ok := emitted[0].Payload.(protocol.ChatMessagePayload)
	if !ok || payload.MessageID == "" || payload.Role != "assistant" {
		t.Fatalf("chat failure payload = %#v", emitted[0].Payload)
	}
	if strings.Contains(payload.Content, secret) {
		t.Fatal("chat failure realtime payload leaked bearer token")
	}
	var content, failureReason string
	if err := testPool.QueryRow(ctx, `
		SELECT content, COALESCE(failure_reason, '') FROM chat_message
		WHERE task_id = $1 AND role = 'assistant'
	`, fixture.task.ID).Scan(&content, &failureReason); err != nil {
		t.Fatalf("load failed chat message: %v", err)
	}
	if strings.Contains(content, secret) || failureReason != "api_invalid_request" {
		t.Fatalf("failed chat message = content %q reason %q", content, failureReason)
	}
	assertAssistantMessageCount(t, ctx, fixture.task.ID, 1)
	assertChatSessionUnread(t, ctx, fixture.queries, fixture.session.ID, true)
}

func TestChatFailureProjectionSkipsRetryingAttempt(t *testing.T) {
	ctx := context.Background()
	fixture := setupChatCompletionFixture(t, ctx)
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET max_attempts = 2 WHERE id = $1`, fixture.task.ID); err != nil {
		t.Fatalf("increase retry budget: %v", err)
	}
	if _, err := fixture.taskService.FailTask(ctx, fixture.task.ID, "task timed out", "", "", "timeout"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	emitted, err := runChatProjection(ctx, fixture.queries, latestTaskTerminalEvent(t, fixture.task.ID), consumeChatFailureProjection)
	if err != nil {
		t.Fatalf("project retrying chat failure: %v", err)
	}
	if len(emitted) != 0 {
		t.Fatalf("retrying chat failure emitted events = %#v", emitted)
	}
	assertAssistantMessageCount(t, ctx, fixture.task.ID, 0)
	assertChatSessionUnread(t, ctx, fixture.queries, fixture.session.ID, false)
}

func runChatProjection(
	ctx context.Context,
	queries *db.Queries,
	event events.Event,
	project func(context.Context, *db.Queries, events.Event) ([]events.Event, error),
) ([]events.Event, error) {
	tx, err := testPool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	emitted, err := project(ctx, queries.WithTx(tx), event)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return emitted, nil
}

func assertChatSessionUnread(t *testing.T, ctx context.Context, queries *db.Queries, sessionID pgtype.UUID, want bool) {
	t.Helper()
	session, err := queries.GetChatSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetChatSession: %v", err)
	}
	if session.UnreadSince.Valid != want {
		t.Fatalf("chat session unread = %v, want %v", session.UnreadSince.Valid, want)
	}
}

func setupChatCompletionFixture(t *testing.T, ctx context.Context) *chatCompletionFixture {
	t.Helper()
	queries := db.New(testPool)
	fixture := &chatCompletionFixture{queries: queries}
	bus := events.New()
	bus.Subscribe(protocol.EventChatDone, func(events.Event) { fixture.chatDoneCount++ })
	fixture.taskService = service.NewTaskService(queries, testPool, nil, bus)

	var agentID string
	if err := testPool.QueryRow(ctx, `
		SELECT id::text FROM agent WHERE workspace_id = $1 ORDER BY created_at LIMIT 1
	`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load chat fixture agent: %v", err)
	}
	agent, err := queries.GetAgent(ctx, parseUUID(agentID))
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	fixture.session, err = queries.CreateChatSession(ctx, db.CreateChatSessionParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		AgentID:     agent.ID,
		CreatorID:   parseUUID(testUserID),
		Title:       "durable chat completion",
	})
	if err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}
	fixture.task, err = queries.CreateChatTask(ctx, db.CreateChatTaskParams{
		AgentID:         agent.ID,
		RuntimeID:       agent.RuntimeID,
		Priority:        0,
		ChatSessionID:   fixture.session.ID,
		InitiatorUserID: parseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("CreateChatTask: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue SET status = 'dispatched', dispatched_at = now() WHERE id = $1
	`, fixture.task.ID); err != nil {
		t.Fatalf("dispatch chat task: %v", err)
	}
	if _, err := queries.StartAgentTask(ctx, fixture.task.ID); err != nil {
		t.Fatalf("StartAgentTask: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE parent_task_id = $1 OR id = $1`, fixture.task.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, fixture.session.ID)
	})
	return fixture
}

func installAssistantMessageFailure(t *testing.T) func() {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	functionName := "assistant_message_fail_fn_" + suffix
	triggerName := "assistant_message_fail_" + suffix
	ctx := context.Background()
	remove := func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON chat_message`, triggerName))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	}
	t.Cleanup(remove)
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.role = 'assistant' THEN
				RAISE EXCEPTION 'forced assistant message failure';
			END IF;
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER %s
		BEFORE INSERT ON chat_message
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, triggerName, functionName)); err != nil {
		t.Fatalf("install assistant message failure: %v", err)
	}
	return remove
}

func installChatUnreadFailure(t *testing.T, sessionID pgtype.UUID) func() {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	functionName := "chat_unread_fail_fn_" + suffix
	triggerName := "chat_unread_fail_" + suffix
	ctx := context.Background()
	remove := func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON chat_session`, triggerName))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	}
	t.Cleanup(remove)
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.id = '%s' AND NEW.unread_since IS NOT NULL THEN
				RAISE EXCEPTION 'forced chat unread failure';
			END IF;
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER %s
		BEFORE UPDATE ON chat_session
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, util.UUIDToString(sessionID), triggerName, functionName)); err != nil {
		t.Fatalf("install chat unread failure: %v", err)
	}
	return remove
}

func assertAssistantMessageCount(t *testing.T, ctx context.Context, taskID pgtype.UUID, want int) {
	t.Helper()
	var count int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM chat_message WHERE task_id = $1 AND role = 'assistant'
	`, taskID).Scan(&count); err != nil {
		t.Fatalf("count assistant messages: %v", err)
	}
	if count != want {
		t.Fatalf("assistant messages = %d, want %d", count, want)
	}
}
