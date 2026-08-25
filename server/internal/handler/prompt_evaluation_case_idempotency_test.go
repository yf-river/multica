package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func createPromptEvaluationCaseWithKey(t *testing.T, key string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/prompt-evaluation-cases?workspace_id="+testWorkspaceID, body)
	req.Header.Set("Idempotency-Key", key)
	testHandler.CreatePromptEvaluationCase(w, req)
	return w
}

func TestCreatePromptEvaluationCase_ConcurrentAutomaticIndexesAreUnique(t *testing.T) {
	requireHandlerDatabase(t)
	assetKey := uuid.NewString()
	assetName := "concurrent case asset " + uuid.NewString()
	assetRecorder := createPromptEvaluationAssetWithKey(t, assetKey, map[string]any{
		"name": assetName, "asset_type": "数据集", "status": "启用", "payload": map[string]any{},
	})
	if assetRecorder.Code != http.StatusCreated {
		t.Fatalf("create asset = %d %s", assetRecorder.Code, assetRecorder.Body.String())
	}
	var asset PromptEvaluationAssetResponse
	if err := json.Unmarshal(assetRecorder.Body.Bytes(), &asset); err != nil {
		t.Fatalf("decode asset: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM resource_create_request WHERE resource_type = 'prompt_evaluation_case' AND resource_id IN (SELECT id FROM prompt_evaluation_case WHERE asset_id = $1)`, asset.ID)
		_, _ = testPool.Exec(ctx, `DELETE FROM prompt_evaluation_case WHERE asset_id = $1`, asset.ID)
		_, _ = testPool.Exec(ctx, `DELETE FROM resource_create_request WHERE resource_type = 'prompt_evaluation_asset' AND idempotency_key = $1`, assetKey)
		_, _ = testPool.Exec(ctx, `DELETE FROM prompt_evaluation_asset WHERE id = $1`, asset.ID)
	})

	const callers = 8
	responses := make(chan *httptest.ResponseRecorder, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			responses <- createPromptEvaluationCaseWithKey(t, uuid.NewString(), map[string]any{
				"asset_id": asset.ID, "case_name": "case " + string(rune('a'+index)), "status": "启用",
			})
		}(i)
	}
	wg.Wait()
	close(responses)

	indexes := map[int32]struct{}{}
	for response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("concurrent create = %d %s", response.Code, response.Body.String())
		}
		var created PromptEvaluationCaseResponse
		if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
			t.Fatalf("decode case: %v", err)
		}
		indexes[created.CaseIndex] = struct{}{}
	}
	if len(indexes) != callers {
		t.Fatalf("automatic indexes = %v, want %d unique indexes", indexes, callers)
	}
}

func TestCreatePromptEvaluationCase_IdempotentReplayAndConflict(t *testing.T) {
	requireHandlerDatabase(t)
	assetKey := uuid.NewString()
	caseKey := uuid.NewString()
	assetName := "case replay asset " + uuid.NewString()
	assetRecorder := createPromptEvaluationAssetWithKey(t, assetKey, map[string]any{
		"name": assetName, "asset_type": "数据集", "status": "启用", "payload": map[string]any{},
	})
	if assetRecorder.Code != http.StatusCreated {
		t.Fatalf("create asset = %d %s", assetRecorder.Code, assetRecorder.Body.String())
	}
	var asset PromptEvaluationAssetResponse
	if err := json.Unmarshal(assetRecorder.Body.Bytes(), &asset); err != nil {
		t.Fatalf("decode asset: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM resource_create_request WHERE resource_type = 'prompt_evaluation_case' AND resource_id IN (SELECT id FROM prompt_evaluation_case WHERE asset_id = $1)`, asset.ID)
		_, _ = testPool.Exec(ctx, `DELETE FROM prompt_evaluation_case WHERE workspace_id = $1 AND asset_id = $2`, testWorkspaceID, asset.ID)
		_, _ = testPool.Exec(ctx, `DELETE FROM resource_create_request WHERE workspace_id = $1 AND resource_type = 'prompt_evaluation_asset' AND idempotency_key = $2`, testWorkspaceID, assetKey)
		_, _ = testPool.Exec(ctx, `DELETE FROM prompt_evaluation_asset WHERE id = $1`, asset.ID)
	})
	body := map[string]any{
		"asset_id":  asset.ID,
		"case_name": "response loss case",
		"input":     map[string]any{"prompt": "current"},
		"expected":  map[string]any{"answer": "current"},
		"status":    "启用",
	}

	first := createPromptEvaluationCaseWithKey(t, caseKey, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d %s", first.Code, first.Body.String())
	}
	replay := createPromptEvaluationCaseWithKey(t, caseKey, body)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body differs\nfirst:  %s\nreplay: %s", first.Body, replay.Body)
	}

	var created PromptEvaluationCaseResponse
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode case: %v", err)
	}
	var cases, requests int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM prompt_evaluation_case WHERE id = $1`, created.ID).Scan(&cases); err != nil {
		t.Fatalf("count cases: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM resource_create_request WHERE workspace_id = $1 AND resource_type = 'prompt_evaluation_case' AND idempotency_key = $2`, testWorkspaceID, caseKey).Scan(&requests); err != nil {
		t.Fatalf("count requests: %v", err)
	}
	if cases != 1 || requests != 1 {
		t.Fatalf("cases=%d requests=%d, want 1/1", cases, requests)
	}

	changed := map[string]any{
		"asset_id": asset.ID, "case_name": "changed", "status": "启用",
	}
	conflict := createPromptEvaluationCaseWithKey(t, caseKey, changed)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed replay = %d %s, want 409", conflict.Code, conflict.Body.String())
	}

	explicit := createPromptEvaluationCaseWithKey(t, uuid.NewString(), map[string]any{
		"asset_id": asset.ID, "case_name": "explicit gap", "case_index": 2, "status": "启用",
	})
	if explicit.Code != http.StatusCreated {
		t.Fatalf("explicit index create = %d %s", explicit.Code, explicit.Body.String())
	}
	automatic := createPromptEvaluationCaseWithKey(t, uuid.NewString(), map[string]any{
		"asset_id": asset.ID, "case_name": "after gap", "status": "启用",
	})
	if automatic.Code != http.StatusCreated {
		t.Fatalf("automatic index after gap = %d %s", automatic.Code, automatic.Body.String())
	}
	var afterGap PromptEvaluationCaseResponse
	if err := json.Unmarshal(automatic.Body.Bytes(), &afterGap); err != nil {
		t.Fatalf("decode automatic case: %v", err)
	}
	if afterGap.CaseIndex != 3 {
		t.Fatalf("automatic case index = %d, want max+1 = 3", afterGap.CaseIndex)
	}
}
