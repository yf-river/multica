package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRunPromptEvaluationAssetAgentPreservesAgentReadFailures(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	asset, run, _ := createPromptEvaluationAgentRunFixture(t, "agent lookup failure experiment", "agent lookup failure case")
	if _, err := testPool.Exec(context.Background(), `
		UPDATE prompt_evaluation_asset
		SET payload = jsonb_set(payload, '{agent_id}', to_jsonb($2::text), true)
		WHERE id = $1
	`, asset.ID, run.AgentID); err != nil {
		t.Fatalf("pin requested agent: %v", err)
	}
	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "GetAgentInWorkspace"})
	w := httptest.NewRecorder()
	r := newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+asset.ID+"/agent-run", nil)
	r = withURLParam(r, "id", asset.ID)

	h.RunPromptEvaluationAssetAgent(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("agent lookup failure: expected 500, got %d: %s", w.Code, w.Body.String())
	}
}
