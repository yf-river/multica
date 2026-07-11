package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCreateAgentPlaygroundExperiment_AllowsMoreThanThreeAgents(t *testing.T) {
	agentIDs := make([]string, 0, 4)
	suffix := time.Now().UnixNano()
	for i := 0; i < 4; i++ {
		agentIDs = append(agentIDs, createHandlerTestAgent(t, fmt.Sprintf("agent-playground-unlimited-%d-%d", suffix, i), nil))
	}
	assetID, versionID := createAgentPlaygroundDatasetSnapshot(t, suffix)

	w := httptest.NewRecorder()
	testHandler.CreateAgentPlaygroundExperiment(w, newRequest(http.MethodPost, "/api/agent-playground-experiments", map[string]any{
		"name":               fmt.Sprintf("unlimited agents %d", suffix),
		"dataset_asset_id":   assetID,
		"dataset_version_id": versionID,
		"agent_ids":          agentIDs,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateAgentPlaygroundExperiment status = %d, body = %s", w.Code, w.Body.String())
	}

	var detail AgentPlaygroundDetailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode playground detail: %v", err)
	}
	if len(detail.Agents) != len(agentIDs) {
		t.Fatalf("experiment agents = %d, want %d: %+v", len(detail.Agents), len(agentIDs), detail.Agents)
	}
	if detail.Experiment.AgentCount != int32(len(agentIDs)) {
		t.Fatalf("experiment agent_count = %d, want %d", detail.Experiment.AgentCount, len(agentIDs))
	}
}

func TestCreateAgentPlaygroundExperimentCanonicalizesDuplicateAgentIDs(t *testing.T) {
	suffix := time.Now().UnixNano()
	agentID := createHandlerTestAgent(t, fmt.Sprintf("agent-playground-canonical-%d", suffix), nil)
	assetID, versionID := createAgentPlaygroundDatasetSnapshot(t, suffix)

	w := httptest.NewRecorder()
	testHandler.CreateAgentPlaygroundExperiment(w, newRequest(http.MethodPost, "/api/agent-playground-experiments", map[string]any{
		"name":               fmt.Sprintf("canonical agents %d", suffix),
		"dataset_asset_id":   assetID,
		"dataset_version_id": versionID,
		"agent_ids":          []string{agentID, strings.ToUpper(agentID)},
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateAgentPlaygroundExperiment status = %d, body = %s", w.Code, w.Body.String())
	}

	var detail AgentPlaygroundDetailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode playground detail: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_playground_experiment WHERE id = $1`, detail.Experiment.ID)
	})
	if len(detail.Agents) != 1 || detail.Experiment.AgentCount != 1 {
		t.Fatalf("canonical duplicate created %d agents with count %d", len(detail.Agents), detail.Experiment.AgentCount)
	}
}

func createAgentPlaygroundDatasetSnapshot(t *testing.T, suffix int64) (assetID, versionID string) {
	t.Helper()

	ctx := context.Background()
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_asset (workspace_id, name, description, asset_type, payload, created_by, dataset_row_count)
		VALUES ($1, $2, '', '数据集', '{}'::jsonb, $3, 1)
		RETURNING id
	`, testWorkspaceID, fmt.Sprintf("agent playground dataset %d", suffix), testUserID).Scan(&assetID); err != nil {
		t.Fatalf("create prompt evaluation asset: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE id = $1`, assetID)
	})

	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_dataset_version (workspace_id, dataset_asset_id, version, version_label, row_count, row_fingerprint, metadata, created_by)
		VALUES ($1, $2, 1, 'agent playground test', 1, $3, '{}'::jsonb, $4)
		RETURNING id
	`, testWorkspaceID, assetID, fmt.Sprintf("fingerprint-%d", suffix), testUserID).Scan(&versionID); err != nil {
		t.Fatalf("create prompt evaluation dataset version: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO prompt_evaluation_dataset_version_row (
			workspace_id, dataset_version_id, dataset_asset_id, row_index, row_name,
			variables, expected_contains, expected, tags, source
		)
		VALUES ($1, $2, $3, 0, '默认用例', '{"input":"请完成这个测试用例"}'::jsonb, '[]'::jsonb, '{}'::jsonb, '[]'::jsonb, 'manual')
	`, testWorkspaceID, versionID, assetID); err != nil {
		t.Fatalf("create prompt evaluation dataset version row: %v", err)
	}
	return assetID, versionID
}
