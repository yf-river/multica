package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCreateAgentPlaygroundExperimentPreservesAgentReadFailures(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "playground validation failure agent", nil)
	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "GetAgentInWorkspace"})
	w := httptest.NewRecorder()

	h.CreateAgentPlaygroundExperiment(w, newRequest(http.MethodPost, "/api/agent-playground-experiments", map[string]any{
		"name":               "playground validation read failure",
		"dataset_asset_id":   "11111111-1111-4111-8111-111111111111",
		"dataset_version_id": "22222222-2222-4222-8222-222222222222",
		"agent_ids":          []string{agentID},
	}))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("agent lookup failure: expected 500, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateAgentPlaygroundExperimentPreservesDatasetVersionReadFailures(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "playground dataset validation failure agent", nil)
	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "GetPromptEvaluationDatasetVersionInAsset"})
	w := httptest.NewRecorder()

	h.CreateAgentPlaygroundExperiment(w, newRequest(http.MethodPost, "/api/agent-playground-experiments", map[string]any{
		"name":               "playground dataset validation read failure",
		"dataset_asset_id":   "11111111-1111-4111-8111-111111111111",
		"dataset_version_id": "22222222-2222-4222-8222-222222222222",
		"agent_ids":          []string{agentID},
	}))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("dataset version lookup failure: expected 500, got %d: %s", w.Code, w.Body.String())
	}
}
