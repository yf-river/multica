package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestGetChatSessionPreservesAgentReadFailures(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	agentID := createHandlerTestAgent(t, "ChatAgentReadFailure", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "GetAgent"})
	req := newRequest(http.MethodGet, "/api/chat/sessions/"+sessionID, nil)
	req = withChatTestWorkspaceCtx(t, req)
	req = withURLParam(req, "sessionId", sessionID)
	w := httptest.NewRecorder()

	h.GetChatSession(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("agent lookup failure: expected 500, got %d: %s", w.Code, w.Body.String())
	}
}
