package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestListChatMessagesPreservesAttachmentFailures(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	agentID := createHandlerTestAgent(t, "chat attachment failure agent", nil)
	sessionID := createHandlerTestChatSession(t, agentID)
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO chat_message (chat_session_id, role, content)
		VALUES ($1, 'user', 'visible chat message')
	`, sessionID); err != nil {
		t.Fatalf("insert chat message: %v", err)
	}

	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "ListAttachmentsByChatMessageIDs"})
	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/"+sessionID+"/messages/page", nil)
	req.Header.Set("X-User-ID", testUserID)
	req = withURLParam(req, "sessionId", sessionID)
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()

	h.ListChatMessagesPage(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("chat attachment query failure: expected 500, got %d: %s", w.Code, w.Body.String())
	}
}
