package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRunPromptEvaluationAssetAgentPreservesAgentReadFailures(t *testing.T) {
	requireHandlerDatabase(t)

	var agentID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT id FROM agent WHERE workspace_id = $1 ORDER BY created_at LIMIT 1
	`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load existing agent: %v", err)
	}
	member, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      parseUUID(testUserID),
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load test member: %v", err)
	}

	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "GetAgentInWorkspace"})
	w := httptest.NewRecorder()
	r := newRequest(http.MethodPost, "/api/prompt-evaluation-assets/agent-run", nil)

	_, _, ok := h.selectPromptEvaluationExecutionAgent(
		w,
		r,
		parseUUID(testWorkspaceID),
		parseUUID(testUserID),
		member,
		map[string]any{"agent_id": agentID},
	)

	if ok {
		t.Fatal("agent lookup failure unexpectedly selected an agent")
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("agent lookup failure: expected 500, got %d: %s", w.Code, w.Body.String())
	}
}
