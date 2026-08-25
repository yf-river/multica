package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func taskStatus(t *testing.T, taskID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(),
		`SELECT status FROM agent_task_queue WHERE id = $1`, taskID,
	).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	return status
}

func cleanupCancelTask(t *testing.T, taskID string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM domain_event_outbox WHERE stream_key = 'task:' || $1`, taskID)
	})
}

func createAutopilotRunOnlyTask(t *testing.T, agentID string) string {
	t.Helper()
	ctx := context.Background()

	var workspaceID, runtimeID string
	if err := testPool.QueryRow(ctx,
		`SELECT workspace_id, runtime_id FROM agent WHERE id = $1`, agentID,
	).Scan(&workspaceID, &runtimeID); err != nil {
		t.Fatalf("load agent workspace/runtime: %v", err)
	}

	var autopilotID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO autopilot (workspace_id, title, assignee_id, execution_mode, created_by_type, created_by_id)
		VALUES ($1, 'cancel-runonly-ap', $2, 'run_only', 'member', $3)
		RETURNING id
	`, workspaceID, agentID, testUserID).Scan(&autopilotID); err != nil {
		t.Fatalf("create autopilot: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, autopilotID) })

	var runID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO autopilot_run (autopilot_id, source, status)
		VALUES ($1, 'manual', 'running')
		RETURNING id
	`, autopilotID).Scan(&runID); err != nil {
		t.Fatalf("create autopilot_run: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, autopilot_run_id)
		VALUES ($1, $2, 'queued', 0, $3)
		RETURNING id
	`, agentID, runtimeID, runID).Scan(&taskID); err != nil {
		t.Fatalf("create run_only task: %v", err)
	}
	cleanupCancelTask(t, taskID)
	return taskID
}

func createForeignWorkspaceAgent(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	var workspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Foreign Cancel WS', 'foreign-cancel-ws', 'cross-tenant cancel test', 'FCW')
		RETURNING id
	`).Scan(&workspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID) })

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at)
		VALUES ($1, NULL, 'Foreign Cancel Runtime', 'cloud', 'foreign_runtime', 'online', 'Foreign runtime', '{}'::jsonb, now())
		RETURNING id
	`, workspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("create foreign runtime: %v", err)
	}

	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, runtime_id, scope, max_concurrent_tasks)
		VALUES ($1, 'Foreign Cancel Agent', '', 'cloud', '{}'::jsonb, $2, 'workspace', 1)
		RETURNING id
	`, workspaceID, runtimeID).Scan(&agentID); err != nil {
		t.Fatalf("create foreign agent: %v", err)
	}
	return agentID
}

func createWorkspaceMemberUser(t *testing.T, name, account string) string {
	t.Helper()
	ctx := context.Background()

	var userID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO "user" (name, account) VALUES ($1, $2) RETURNING id`, name, account,
	).Scan(&userID); err != nil {
		t.Fatalf("create user %s: %v", account, err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID) })

	if _, err := testPool.Exec(ctx,
		`INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')`, testWorkspaceID, userID,
	); err != nil {
		t.Fatalf("add member %s: %v", account, err)
	}
	return userID
}

func cancelTaskByUserRequest(t *testing.T, userID, taskID string) *http.Request {
	t.Helper()
	req := newRequestAs(userID, "POST", "/api/tasks/"+taskID+"/cancel", nil)
	req = withURLParam(req, "taskId", taskID)
	return withChatTestWorkspaceCtx(t, req)
}

func cancelTaskAs(t *testing.T, userID, taskID string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.CancelTaskByUser(w, cancelTaskByUserRequest(t, userID, taskID))
	if w.Code != wantStatus {
		t.Fatalf("expected %d, got %d: %s", wantStatus, w.Code, w.Body.String())
	}
	return w
}

func cancelTaskAndRequireStatus(t *testing.T, userID, taskID string, wantHTTPStatus int, wantTaskStatus string) *httptest.ResponseRecorder {
	t.Helper()
	response := cancelTaskAs(t, userID, taskID, wantHTTPStatus)
	if got := taskStatus(t, taskID); got != wantTaskStatus {
		t.Fatalf("task status = %q, want %q", got, wantTaskStatus)
	}
	return response
}

func decodeCancelTaskResponse(t *testing.T, response *httptest.ResponseRecorder) cancelTaskByUserResponse {
	t.Helper()
	var body cancelTaskByUserResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	return body
}

func createLinkedChatUserMessage(t *testing.T, sessionID, taskID, content string) string {
	t.Helper()
	var messageID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO chat_message (chat_session_id, role, content, task_id)
		VALUES ($1, 'user', $2, $3)
		RETURNING id
	`, sessionID, content, taskID).Scan(&messageID); err != nil {
		t.Fatalf("create linked user chat message: %v", err)
	}
	return messageID
}

func createRunningChatTask(t *testing.T, agentID, sessionID string) string {
	t.Helper()
	var taskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, chat_session_id, created_at)
		VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), 'running', 0, $2, now() - interval '5 seconds')
		RETURNING id
	`, agentID, sessionID).Scan(&taskID); err != nil {
		t.Fatalf("create running chat task: %v", err)
	}
	cleanupCancelTask(t, taskID)
	return taskID
}

func TestCancelTaskByUser_RunOnlyAutopilot_Succeeds(t *testing.T) {
	requireHandlerDatabase(t)

	agentID := createHandlerTestAgent(t, "CancelRunOnlyAgent", nil)
	taskID := createAutopilotRunOnlyTask(t, agentID)

	cancelTaskAndRequireStatus(t, testUserID, taskID, http.StatusOK, "cancelled")
}

func TestCancelTaskByUser_RunOnlyAutopilot_CrossWorkspace_Returns404(t *testing.T) {
	requireHandlerDatabase(t)

	foreignAgentID := createForeignWorkspaceAgent(t)
	taskID := createAutopilotRunOnlyTask(t, foreignAgentID)

	cancelTaskAndRequireStatus(t, testUserID, taskID, http.StatusNotFound, "queued")
}

func TestCancelTaskByUser_QuickCreate_Succeeds(t *testing.T) {
	requireHandlerDatabase(t)

	agentID := createHandlerTestAgent(t, "CancelQuickCreateAgent", nil)

	var taskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, issue_id, context)
		VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), 'running', 0, NULL,
		        jsonb_build_object('type', 'quick_create', 'workspace_id', $2::text, 'prompt', 'do a thing'))
		RETURNING id
	`, agentID, testWorkspaceID).Scan(&taskID); err != nil {
		t.Fatalf("create quick_create task: %v", err)
	}
	cleanupCancelTask(t, taskID)

	cancelTaskAndRequireStatus(t, testUserID, taskID, http.StatusOK, "cancelled")
}

func TestCancelTaskByUser_RetryClone_Autopilot_Succeeds(t *testing.T) {
	requireHandlerDatabase(t)

	agentID := createHandlerTestAgent(t, "CancelRetryCloneAgent", nil)
	parentID := createAutopilotRunOnlyTask(t, agentID)

	var cloneID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, autopilot_run_id, parent_task_id, attempt)
		SELECT agent_id, runtime_id, 'queued', priority, autopilot_run_id, id, 1
		FROM agent_task_queue WHERE id = $1
		RETURNING id
	`, parentID).Scan(&cloneID); err != nil {
		t.Fatalf("create retry clone: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, cloneID)
	})

	cancelTaskAndRequireStatus(t, testUserID, cloneID, http.StatusOK, "cancelled")
}

func TestCancelTaskByUser_IssueTask_Succeeds(t *testing.T) {
	requireHandlerDatabase(t)

	agentID := createHandlerTestAgent(t, "CancelIssueTaskAgent", nil)

	var issueID, taskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
		VALUES ($1, 'cancel-byid-issue', 'todo', 'medium', $2, 'member', 92001, 0)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, issue_id)
		VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), 'queued', 0, $2)
		RETURNING id
	`, agentID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create issue task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	cancelTaskAndRequireStatus(t, testUserID, taskID, http.StatusOK, "cancelled")
}

func TestCancelTaskByUser_ChatTask_NonCreator_Returns403(t *testing.T) {
	requireHandlerDatabase(t)

	agentID := createHandlerTestAgent(t, "CancelChatAgent", nil)
	sessionID := createHandlerTestChatSession(t, agentID) // creator = testUserID
	otherUserID := createWorkspaceMemberUser(t, "Chat Bystander", "cancel-chat-bystander@multica.test")

	taskID := createRunningChatTask(t, agentID, sessionID)
	cancelTaskAndRequireStatus(t, otherUserID, taskID, http.StatusForbidden, "running")
}

func TestCancelTaskByUser_ChatTaskWithTranscript_PersistsAssistantSnapshot(t *testing.T) {
	requireHandlerDatabase(t)

	agentID := createHandlerTestAgent(t, "CancelChatTranscriptAgent", nil)
	sessionID := createHandlerTestChatSession(t, agentID)

	taskID := createRunningChatTask(t, agentID, sessionID)

	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO chat_message (chat_session_id, role, content, task_id)
		VALUES ($1, 'user', 'please answer', $2)
	`, sessionID, taskID); err != nil {
		t.Fatalf("create linked user chat message: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO task_message (task_id, seq, type, content)
		VALUES ($1, 1, 'text', 'partial answer')
	`, taskID); err != nil {
		t.Fatalf("create task message: %v", err)
	}

	resp := decodeCancelTaskResponse(t, cancelTaskAndRequireStatus(t, testUserID, taskID, http.StatusOK, "cancelled"))
	if resp.CancelledChatMessage != nil {
		t.Fatalf("expected no restore payload when transcript exists, got %#v", resp.CancelledChatMessage)
	}
	var role, content, messageTaskID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT role, content, COALESCE(task_id::text, '')
		FROM chat_message
		WHERE chat_session_id = $1 AND role = 'assistant'
	`, sessionID).Scan(&role, &content, &messageTaskID); err != nil {
		t.Fatalf("read cancelled assistant chat message: %v", err)
	}
	if role != "assistant" || content != "Stopped." || messageTaskID != taskID {
		t.Fatalf("assistant snapshot mismatch: role=%q content=%q task_id=%q", role, content, messageTaskID)
	}
}

func TestCancelTaskByUser_ChatTaskWithoutTranscript_RestoresUserDraft(t *testing.T) {
	requireHandlerDatabase(t)

	agentID := createHandlerTestAgent(t, "CancelChatNoTranscriptAgent", nil)
	sessionID := createHandlerTestChatSession(t, agentID)

	taskID := createRunningChatTask(t, agentID, sessionID)

	const userContent = "keep this prompt"
	userMessageID := createLinkedChatUserMessage(t, sessionID, taskID, userContent)
	resp := decodeCancelTaskResponse(t, cancelTaskAs(t, testUserID, taskID, http.StatusOK))
	if resp.CancelledChatMessage == nil {
		t.Fatal("expected restore payload for empty transcript cancel")
	}
	if resp.CancelledChatMessage.MessageID != userMessageID ||
		resp.CancelledChatMessage.Content != userContent ||
		!resp.CancelledChatMessage.RestoreToInput {
		t.Fatalf("restore payload mismatch: %#v", resp.CancelledChatMessage)
	}

	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM chat_message
		WHERE chat_session_id = $1 AND role = 'assistant'
	`, sessionID).Scan(&count); err != nil {
		t.Fatalf("count assistant chat messages: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no assistant snapshot for empty transcript, got %d", count)
	}
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM chat_message
		WHERE id = $1
	`, userMessageID).Scan(&count); err != nil {
		t.Fatalf("count deleted user chat message: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected linked user message to be deleted, got %d", count)
	}
}

func TestCancelChatTaskRollsBackDraftRestoreWhenEventInsertFails(t *testing.T) {
	requireHandlerDatabase(t)

	agentID := createHandlerTestAgent(t, "CancelChatRollbackAgent", nil)
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID := createRunningChatTask(t, agentID, sessionID)

	userMessageID := createLinkedChatUserMessage(t, sessionID, taskID, "draft must survive rollback")
	installOutboxStreamFailure(t, "task:"+taskID)

	if _, err := testHandler.TaskService.CancelTask(context.Background(), parseUUID(taskID)); err == nil {
		t.Fatal("CancelTask succeeded while its durable event insert failed")
	}
	if got := taskStatus(t, taskID); got != "running" {
		t.Fatalf("failed cancel changed task status to %q", got)
	}
	var messageCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM chat_message WHERE id = $1 AND task_id = $2
	`, userMessageID, taskID).Scan(&messageCount); err != nil {
		t.Fatalf("count rollback chat message: %v", err)
	}
	if messageCount != 1 {
		t.Fatalf("failed cancel removed the user's draft message")
	}
}

// TestCancelTaskByUser_ChatTaskWithBoundAttachment_SurvivesCancelAndRebinds
// guards the data-loss path on the empty-chat cancel: the user message bound to
// an attachment is deleted, and attachment.chat_message_id is ON DELETE CASCADE
// in the current schema, so without the
// detach-before-delete step the cancel would silently destroy the user's
// attachment. The detach (chat_message_id -> NULL, chat_session_id retained) is
// load-bearing, not an optimization; nothing else covered it. This pins:
//
//	(a) the attachment row survives the cascade — still present, chat_message_id
//	    NULL, chat_session_id retained;
//	(b) the cancel response returns it via cancelled_chat_message.attachments so
//	    the restored draft can re-show it;
//	(c) re-sending the restored draft re-binds the surviving attachment to the
//	    new message in the same session.
func TestCancelTaskByUser_ChatTaskWithBoundAttachment_SurvivesCancelAndRebinds(t *testing.T) {
	requireHandlerDatabase(t)

	agentID := createHandlerTestAgent(t, "CancelChatAttachAgent", nil)
	sessionID := createHandlerTestChatSession(t, agentID)

	taskID := createRunningChatTask(t, agentID, sessionID)

	const userContent = "look at this attachment"
	userMessageID := createLinkedChatUserMessage(t, sessionID, taskID, userContent)

	// Bind an attachment to that user message, exactly as a real send does:
	// workspace-scoped, uploaded by the session creator, pointing at both the
	// session and the message.
	var attachmentID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO attachment (workspace_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, chat_session_id, chat_message_id)
		VALUES ($1, 'member', $2, 'cancel-survive.png', 'https://cdn.example.com/cancel-survive.png', 'image/png', 9, $3, $4)
		RETURNING id::text
	`, testWorkspaceID, testUserID, sessionID, userMessageID).Scan(&attachmentID); err != nil {
		t.Fatalf("seed bound attachment: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM attachment WHERE id = $1`, attachmentID)
	})

	// Cancel the empty chat task (no transcript) — this deletes the user message.
	resp := decodeCancelTaskResponse(t, cancelTaskAs(t, testUserID, taskID, http.StatusOK))
	if resp.CancelledChatMessage == nil {
		t.Fatal("expected restore payload for empty transcript cancel")
	}

	// (b) The cancel response carries the detached attachment back.
	var returned *AttachmentResponse
	for i := range resp.CancelledChatMessage.Attachments {
		if resp.CancelledChatMessage.Attachments[i].ID == attachmentID {
			returned = &resp.CancelledChatMessage.Attachments[i]
			break
		}
	}
	if returned == nil {
		t.Fatalf("cancel response did not return the detached attachment: %#v", resp.CancelledChatMessage.Attachments)
	}

	// (a) The row survived the ON DELETE CASCADE: still present, detached from
	//     the deleted message, but still scoped to the session.
	var count int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM attachment WHERE id = $1`, attachmentID,
	).Scan(&count); err != nil {
		t.Fatalf("count attachment: %v", err)
	}
	if count != 1 {
		t.Fatalf("attachment was cascade-deleted on cancel: count = %d", count)
	}
	var dbMessageID, dbSessionID *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT chat_message_id::text, chat_session_id::text FROM attachment WHERE id = $1`, attachmentID,
	).Scan(&dbMessageID, &dbSessionID); err != nil {
		t.Fatalf("read attachment after cancel: %v", err)
	}
	if dbMessageID != nil {
		t.Fatalf("expected chat_message_id detached to NULL, got %q", *dbMessageID)
	}
	if dbSessionID == nil || *dbSessionID != sessionID {
		t.Fatalf("expected chat_session_id retained as %q, got %v", sessionID, dbSessionID)
	}

	// Sanity: the empty-cancel still deleted the user message itself.
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM chat_message WHERE id = $1`, userMessageID,
	).Scan(&count); err != nil {
		t.Fatalf("count user message: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected linked user message to be deleted, got %d", count)
	}

	// (c) Re-sending the restored draft re-binds the surviving attachment to a
	//     fresh message in the same session — the whole reason for detaching.
	sendReq := newRequest("POST", "/api/chat-sessions/"+sessionID+"/messages", map[string]any{
		"content":        userContent,
		"attachment_ids": []string{attachmentID},
	})
	sendReq.Header.Set("Idempotency-Key", uuid.NewString())
	sendReq = withURLParam(sendReq, "sessionId", sessionID)
	sendReq = withChatTestWorkspaceCtx(t, sendReq)
	sendW := httptest.NewRecorder()
	testHandler.SendChatMessage(sendW, sendReq)
	if sendW.Code != http.StatusCreated {
		t.Fatalf("resend: expected 201, got %d: %s", sendW.Code, sendW.Body.String())
	}
	var sendResp SendChatMessageResponse
	if err := json.Unmarshal(sendW.Body.Bytes(), &sendResp); err != nil {
		t.Fatalf("decode resend response: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, sendResp.TaskID)
	})

	rebound := false
	for _, id := range sendResp.AttachmentIDs {
		if id == attachmentID {
			rebound = true
			break
		}
	}
	if !rebound {
		t.Fatalf("attachment not re-bound on resend: %#v", sendResp.AttachmentIDs)
	}
	if err := testPool.QueryRow(context.Background(),
		`SELECT chat_message_id::text FROM attachment WHERE id = $1`, attachmentID,
	).Scan(&dbMessageID); err != nil {
		t.Fatalf("read attachment after resend: %v", err)
	}
	if dbMessageID == nil || *dbMessageID != sendResp.MessageID {
		t.Fatalf("expected attachment re-bound to new message %q, got %v", sendResp.MessageID, dbMessageID)
	}
}

func TestCancelTaskByUser_PrivateAgent_PlainMember_Returns403(t *testing.T) {
	requireHandlerDatabase(t)

	agentID, _, memberID := personalAgentTestFixture(t)
	taskID := createAutopilotRunOnlyTask(t, agentID)

	cancelTaskAndRequireStatus(t, memberID, taskID, http.StatusForbidden, "queued")
}
