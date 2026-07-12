package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCancelTaskByUserPreservesTaskReadFailures(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	agentID := createHandlerTestAgent(t, "ChatCancelTaskReadFailure", nil)
	sessionID := createHandlerTestChatSession(t, agentID)
	sent := sendChatMessageForTest(t, sessionID, map[string]any{"content": "cancel task read failure"})
	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "GetAgentTaskInWorkspace"})
	req := newRequest(http.MethodPost, "/api/tasks/"+sent.TaskID+"/cancel", nil)
	req = withChatTestWorkspaceCtx(t, req)
	req = withURLParam(req, "taskId", sent.TaskID)
	w := httptest.NewRecorder()

	h.CancelTaskByUser(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("task lookup failure: expected 500, got %d: %s", w.Code, w.Body.String())
	}
}
