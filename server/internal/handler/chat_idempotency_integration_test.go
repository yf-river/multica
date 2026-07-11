package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	chatCreateKey = "20000000-0000-4000-8000-000000000001"
	chatSendKey   = "20000000-0000-4000-8000-000000000002"
)

func TestCreateChatSessionRequiresCanonicalIdempotencyKey(t *testing.T) {
	agentID := handlerTestChatAgentID(t)
	for _, testCase := range []struct {
		name string
		key  string
	}{
		{name: "missing"},
		{name: "not uuid", key: "retry-me"},
		{name: "not v4", key: "20000000-0000-1000-8000-000000000001"},
		{name: "not canonical", key: "20000000-0000-4000-8000-00000000000A"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			title := "invalid idempotency " + uuid.NewString()
			req := newRequest(http.MethodPost, "/api/chat/sessions", CreateChatSessionRequest{
				AgentID: agentID,
				Title:   title,
			})
			if testCase.key != "" {
				req.Header.Set("Idempotency-Key", testCase.key)
			}
			w := httptest.NewRecorder()
			testHandler.CreateChatSession(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
			var count int
			if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM chat_session WHERE title = $1`, title).Scan(&count); err != nil {
				t.Fatalf("count sessions: %v", err)
			}
			if count != 0 {
				t.Fatalf("invalid key created %d sessions", count)
			}
		})
	}
}

func TestCreateChatSessionReplaysOneConcurrentResult(t *testing.T) {
	bus := events.New()
	h := newChatIdempotencyTestHandler(bus)
	agentID := handlerTestChatAgentID(t)
	key := uuid.NewString()
	title := "concurrent create " + uuid.NewString()

	const callers = 6
	recorders := make([]*httptest.ResponseRecorder, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			req := newChatIdempotentRequest(http.MethodPost, "/api/chat/sessions", CreateChatSessionRequest{
				AgentID: agentID,
				Title:   title,
			}, key)
			w := httptest.NewRecorder()
			h.CreateChatSession(w, req)
			recorders[index] = w
		}(i)
	}
	wg.Wait()

	var first ChatSessionResponse
	for index, recorder := range recorders {
		if recorder.Code != http.StatusCreated {
			t.Fatalf("caller %d status = %d: %s", index, recorder.Code, recorder.Body.String())
		}
		var response ChatSessionResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode caller %d response: %v", index, err)
		}
		if index == 0 {
			first = response
		} else if response.ID != first.ID {
			t.Fatalf("caller %d session = %s, want %s", index, response.ID, first.ID)
		}
	}
	cleanupChatIdempotencyTestRows(t, []string{first.ID}, []string{key})

	var sessions, records int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM chat_session WHERE title = $1`, title).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM chat_idempotency_record WHERE idempotency_key = $1`, key).Scan(&records); err != nil {
		t.Fatalf("count idempotency records: %v", err)
	}
	if sessions != 1 || records != 1 {
		t.Fatalf("sessions/records = %d/%d, want 1/1", sessions, records)
	}

	conflict := newChatIdempotentRequest(http.MethodPost, "/api/chat/sessions", CreateChatSessionRequest{
		AgentID: agentID,
		Title:   title + " changed",
	}, key)
	w := httptest.NewRecorder()
	h.CreateChatSession(w, conflict)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "idempotency_conflict") {
		t.Fatalf("changed replay = %d %s, want 409 idempotency_conflict", w.Code, w.Body.String())
	}
}

func TestSendChatMessageReplaysWithoutDuplicateWritesOrEvents(t *testing.T) {
	bus := events.New()
	h := newChatIdempotencyTestHandler(bus)
	var queuedEvents atomic.Int32
	var messageEvents atomic.Int32
	bus.Subscribe(protocol.EventTaskQueued, func(events.Event) { queuedEvents.Add(1) })
	bus.Subscribe(protocol.EventChatMessage, func(events.Event) { messageEvents.Add(1) })

	session := createIdempotentChatSession(t, h, chatCreateKey, "idempotent send "+uuid.NewString())
	first := sendIdempotentChatMessage(t, h, session.ID, chatSendKey, "hello exactly once", http.StatusCreated)
	if _, err := testPool.Exec(context.Background(), `UPDATE chat_session SET status = 'archived' WHERE id = $1`, session.ID); err != nil {
		t.Fatalf("archive session after committed send: %v", err)
	}
	second := sendIdempotentChatMessage(t, h, session.ID, chatSendKey, "hello exactly once", http.StatusCreated)
	if first.MessageID != second.MessageID || first.TaskID != second.TaskID || first.CreatedAt != second.CreatedAt {
		t.Fatalf("replay response changed: first=%+v second=%+v", first, second)
	}
	if first.AttachmentIDs == nil || len(first.AttachmentIDs) != 0 {
		t.Fatalf("attachment_ids = %#v, want non-nil []", first.AttachmentIDs)
	}
	if queuedEvents.Load() != 1 || messageEvents.Load() != 1 {
		t.Fatalf("queued/message events = %d/%d, want 1/1", queuedEvents.Load(), messageEvents.Load())
	}

	var messages, tasks, records int
	ctx := context.Background()
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM chat_message WHERE chat_session_id = $1 AND role = 'user'`, session.ID).Scan(&messages); err != nil {
		t.Fatalf("count user messages: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id = $1`, session.ID).Scan(&tasks); err != nil {
		t.Fatalf("count chat tasks: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM chat_idempotency_record WHERE idempotency_key = $1`, chatSendKey).Scan(&records); err != nil {
		t.Fatalf("count send idempotency records: %v", err)
	}
	if messages != 1 || tasks != 1 || records != 1 {
		t.Fatalf("messages/tasks/records = %d/%d/%d, want 1/1/1", messages, tasks, records)
	}

	conflictReq := newChatIdempotentRequest(http.MethodPost, "/api/chat/sessions/"+session.ID+"/messages", SendChatMessageRequest{
		Content: "different content",
	}, chatSendKey)
	conflictReq = withURLParam(conflictReq, "sessionId", session.ID)
	conflict := httptest.NewRecorder()
	h.SendChatMessage(conflict, conflictReq)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "idempotency_conflict") {
		t.Fatalf("changed send replay = %d %s, want 409", conflict.Code, conflict.Body.String())
	}

	cleanupChatIdempotencyTestRows(t, []string{session.ID}, []string{chatCreateKey, chatSendKey})
}

func TestChatLogicalOperationKeySpansCreateAndSendNamespaces(t *testing.T) {
	h := newChatIdempotencyTestHandler(events.New())
	key := uuid.NewString()
	session := createIdempotentChatSession(t, h, key, "shared logical operation "+uuid.NewString())
	response := sendIdempotentChatMessage(t, h, session.ID, key, "same key, separate operation", http.StatusCreated)
	if response.MessageID == "" || response.TaskID == "" {
		t.Fatalf("send response missing ids: %+v", response)
	}

	var records int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM chat_idempotency_record
		WHERE idempotency_key = $1 AND operation IN ('create_session', 'send_message')
	`, key).Scan(&records); err != nil {
		t.Fatalf("count logical operation records: %v", err)
	}
	if records != 2 {
		t.Fatalf("logical operation records = %d, want 2 operation namespaces", records)
	}
	cleanupChatIdempotencyTestRows(t, []string{session.ID}, []string{key})
}

func TestSendChatMessageConcurrentReplayCreatesOneTask(t *testing.T) {
	bus := events.New()
	h := newChatIdempotencyTestHandler(bus)
	createKey := uuid.NewString()
	sendKey := uuid.NewString()
	session := createIdempotentChatSession(t, h, createKey, "concurrent send "+uuid.NewString())
	var queuedEvents atomic.Int32
	bus.Subscribe(protocol.EventTaskQueued, func(events.Event) { queuedEvents.Add(1) })

	const callers = 6
	responses := make([]SendChatMessageResponse, callers)
	errorsByCaller := make([]string, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			req := newChatIdempotentRequest(
				http.MethodPost,
				"/api/chat/sessions/"+session.ID+"/messages",
				SendChatMessageRequest{Content: "one concurrent intent"},
				sendKey,
			)
			req = withURLParam(req, "sessionId", session.ID)
			w := httptest.NewRecorder()
			h.SendChatMessage(w, req)
			if w.Code != http.StatusCreated {
				errorsByCaller[index] = fmt.Sprintf("status=%d body=%s", w.Code, w.Body.String())
				return
			}
			if err := json.Unmarshal(w.Body.Bytes(), &responses[index]); err != nil {
				errorsByCaller[index] = err.Error()
			}
		}(i)
	}
	wg.Wait()
	for index, callerErr := range errorsByCaller {
		if callerErr != "" {
			t.Fatalf("caller %d: %s", index, callerErr)
		}
		if responses[index].MessageID != responses[0].MessageID || responses[index].TaskID != responses[0].TaskID {
			t.Fatalf("caller %d response = %+v, want ids from %+v", index, responses[index], responses[0])
		}
	}
	if queuedEvents.Load() != 1 {
		t.Fatalf("queued events = %d, want 1", queuedEvents.Load())
	}
	var messages, tasks int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM chat_message WHERE chat_session_id = $1 AND role = 'user'`, session.ID).Scan(&messages); err != nil {
		t.Fatalf("count concurrent messages: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE chat_session_id = $1`, session.ID).Scan(&tasks); err != nil {
		t.Fatalf("count concurrent tasks: %v", err)
	}
	if messages != 1 || tasks != 1 {
		t.Fatalf("messages/tasks = %d/%d, want 1/1", messages, tasks)
	}
	cleanupChatIdempotencyTestRows(t, []string{session.ID}, []string{createKey, sendKey})
}

func TestSendChatMessageRollsBackEveryWriteBeforeRetry(t *testing.T) {
	bus := events.New()
	h := newChatIdempotencyTestHandler(bus)
	createKey := uuid.NewString()
	sendKey := uuid.NewString()
	session := createIdempotentChatSession(t, h, createKey, "rollback send "+uuid.NewString())
	removeFailureTrigger := installChatMessageTaskLinkFailure(t, session.ID)

	req := newChatIdempotentRequest(http.MethodPost, "/api/chat/sessions/"+session.ID+"/messages", SendChatMessageRequest{
		Content: "must roll back",
	}, sendKey)
	req = withURLParam(req, "sessionId", session.ID)
	w := httptest.NewRecorder()
	h.SendChatMessage(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("injected failure status = %d: %s", w.Code, w.Body.String())
	}

	assertNoChatSendWrites(t, session.ID, sendKey)
	removeFailureTrigger()
	response := sendIdempotentChatMessage(t, h, session.ID, sendKey, "must roll back", http.StatusCreated)
	if response.MessageID == "" || response.TaskID == "" {
		t.Fatalf("retry response = %+v", response)
	}
	cleanupChatIdempotencyTestRows(t, []string{session.ID}, []string{createKey, sendKey})
}

func TestSendChatMessageRollsBackWhenIdempotencyCompletionFails(t *testing.T) {
	bus := events.New()
	h := newChatIdempotencyTestHandler(bus)
	createKey := uuid.NewString()
	sendKey := uuid.NewString()
	session := createIdempotentChatSession(t, h, createKey, "idempotency completion rollback "+uuid.NewString())
	removeFailureTrigger := installChatIdempotencyCompletionFailure(t, sendKey)

	req := newChatIdempotentRequest(http.MethodPost, "/api/chat/sessions/"+session.ID+"/messages", SendChatMessageRequest{
		Content: "response record must be atomic",
	}, sendKey)
	req = withURLParam(req, "sessionId", session.ID)
	w := httptest.NewRecorder()
	h.SendChatMessage(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("injected idempotency failure status = %d: %s", w.Code, w.Body.String())
	}
	assertNoChatSendWrites(t, session.ID, sendKey)

	removeFailureTrigger()
	response := sendIdempotentChatMessage(t, h, session.ID, sendKey, "response record must be atomic", http.StatusCreated)
	if response.MessageID == "" || response.TaskID == "" {
		t.Fatalf("retry response = %+v", response)
	}
	cleanupChatIdempotencyTestRows(t, []string{session.ID}, []string{createKey, sendKey})
}

func newChatIdempotencyTestHandler(bus *events.Bus) *Handler {
	return New(db.New(testPool), testPool, nil, bus, nil, nil, analytics.NoopClient{}, Config{AllowSignup: true})
}

func handlerTestChatAgentID(t *testing.T) string {
	t.Helper()
	var agentID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT id FROM agent
		WHERE workspace_id = $1 AND owner_id = $2 AND archived_at IS NULL
		ORDER BY created_at ASC
		LIMIT 1
	`, testWorkspaceID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("load handler test chat agent: %v", err)
	}
	return agentID
}

func newChatIdempotentRequest(method, path string, body any, key string) *http.Request {
	req := newRequest(method, path, body)
	req.Header.Set("Idempotency-Key", key)
	return req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, db.Member{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		UserID:      util.MustParseUUID(testUserID),
		Role:        "owner",
	}))
}

func createIdempotentChatSession(t *testing.T, h *Handler, key, title string) ChatSessionResponse {
	t.Helper()
	req := newChatIdempotentRequest(http.MethodPost, "/api/chat/sessions", CreateChatSessionRequest{
		AgentID: handlerTestChatAgentID(t),
		Title:   title,
	}, key)
	w := httptest.NewRecorder()
	h.CreateChatSession(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create chat session = %d: %s", w.Code, w.Body.String())
	}
	var response ChatSessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode create chat response: %v", err)
	}
	return response
}

func sendIdempotentChatMessage(t *testing.T, h *Handler, sessionID, key, content string, wantStatus int) SendChatMessageResponse {
	t.Helper()
	req := newChatIdempotentRequest(http.MethodPost, "/api/chat/sessions/"+sessionID+"/messages", SendChatMessageRequest{Content: content}, key)
	req = withURLParam(req, "sessionId", sessionID)
	w := httptest.NewRecorder()
	h.SendChatMessage(w, req)
	if w.Code != wantStatus {
		t.Fatalf("send chat message = %d: %s", w.Code, w.Body.String())
	}
	var response SendChatMessageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode send chat response: %v", err)
	}
	return response
}

func assertNoChatSendWrites(t *testing.T, sessionID, key string) {
	t.Helper()
	ctx := context.Background()
	var messages, tasks, records int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM chat_message WHERE chat_session_id = $1 AND role = 'user'`, sessionID).Scan(&messages); err != nil {
		t.Fatalf("count rolled-back messages: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id = $1`, sessionID).Scan(&tasks); err != nil {
		t.Fatalf("count rolled-back tasks: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM chat_idempotency_record WHERE idempotency_key = $1`, key).Scan(&records); err != nil {
		t.Fatalf("count rolled-back idempotency records: %v", err)
	}
	if messages != 0 || tasks != 0 || records != 0 {
		t.Fatalf("partial send writes = messages:%d tasks:%d records:%d", messages, tasks, records)
	}
}

func installChatMessageTaskLinkFailure(t *testing.T, sessionID string) func() {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := pgx.Identifier{"fail_chat_message_task_link_" + suffix}.Sanitize()
	triggerName := pgx.Identifier{"fail_chat_message_task_link_" + suffix}.Sanitize()
	createFunction := fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			IF NEW.chat_session_id = %s::uuid AND NEW.task_id IS NOT NULL THEN
				RAISE EXCEPTION 'injected chat message task link failure';
			END IF;
			RETURN NEW;
		END
		$body$
	`, functionName, quoteChatSQLLiteral(sessionID))
	if _, err := testPool.Exec(context.Background(), createFunction); err != nil {
		t.Fatalf("create chat link failure function: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), fmt.Sprintf(`
		CREATE TRIGGER %s BEFORE UPDATE ON chat_message
		FOR EACH ROW EXECUTE FUNCTION %s()
	`, triggerName, functionName)); err != nil {
		t.Fatalf("create chat link failure trigger: %v", err)
	}
	var once sync.Once
	remove := func() {
		once.Do(func() {
			_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON chat_message`, triggerName))
			_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
		})
	}
	t.Cleanup(remove)
	return remove
}

func installChatIdempotencyCompletionFailure(t *testing.T, idempotencyKey string) func() {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := pgx.Identifier{"fail_chat_idempotency_completion_" + suffix}.Sanitize()
	triggerName := pgx.Identifier{"fail_chat_idempotency_completion_" + suffix}.Sanitize()
	createFunction := fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			IF NEW.idempotency_key = %s::uuid AND NEW.response_status IS NOT NULL THEN
				RAISE EXCEPTION 'injected chat idempotency completion failure';
			END IF;
			RETURN NEW;
		END
		$body$
	`, functionName, quoteChatSQLLiteral(idempotencyKey))
	if _, err := testPool.Exec(context.Background(), createFunction); err != nil {
		t.Fatalf("create idempotency failure function: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), fmt.Sprintf(`
		CREATE TRIGGER %s BEFORE UPDATE ON chat_idempotency_record
		FOR EACH ROW EXECUTE FUNCTION %s()
	`, triggerName, functionName)); err != nil {
		t.Fatalf("create idempotency failure trigger: %v", err)
	}
	var once sync.Once
	remove := func() {
		once.Do(func() {
			_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON chat_idempotency_record`, triggerName))
			_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
		})
	}
	t.Cleanup(remove)
	return remove
}

func quoteChatSQLLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func cleanupChatIdempotencyTestRows(t *testing.T, sessionIDs, keys []string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if len(sessionIDs) > 0 {
			_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE chat_session_id = ANY($1::uuid[])`, sessionIDs)
			_, _ = testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = ANY($1::uuid[])`, sessionIDs)
		}
		if len(keys) > 0 {
			_, _ = testPool.Exec(ctx, `DELETE FROM chat_idempotency_record WHERE idempotency_key = ANY($1::uuid[])`, keys)
		}
	})
}
