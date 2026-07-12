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

func createPromptEvaluationAssetWithKey(t *testing.T, key string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/prompt-evaluation-assets?workspace_id="+testWorkspaceID, body)
	req.Header.Set("Idempotency-Key", key)
	testHandler.CreatePromptEvaluationAsset(w, req)
	return w
}

func TestCreatePromptEvaluationAsset_ConcurrentReplayCreatesOneAsset(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	key := uuid.NewString()
	name := "concurrent evaluation asset " + uuid.NewString()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM prompt_evaluation_asset WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, name)
		_, _ = testPool.Exec(ctx, `DELETE FROM resource_create_request WHERE workspace_id = $1 AND resource_type = 'prompt_evaluation_asset' AND idempotency_key = $2`, testWorkspaceID, key)
	})
	body := map[string]any{
		"name": name, "asset_type": "数据集", "status": "启用", "payload": map[string]any{},
	}

	const callers = 8
	responses := make(chan *httptest.ResponseRecorder, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- createPromptEvaluationAssetWithKey(t, key, body)
		}()
	}
	wg.Wait()
	close(responses)

	ids := map[string]struct{}{}
	for response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("concurrent create = %d %s", response.Code, response.Body.String())
		}
		var asset PromptEvaluationAssetResponse
		if err := json.Unmarshal(response.Body.Bytes(), &asset); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		ids[asset.ID] = struct{}{}
	}
	if len(ids) != 1 {
		t.Fatalf("concurrent responses returned %d asset ids: %v", len(ids), ids)
	}
	var assets int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM prompt_evaluation_asset WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, name).Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if assets != 1 {
		t.Fatalf("assets=%d, want 1", assets)
	}
}

func TestCreatePromptEvaluationAsset_IdempotentReplayAndConflict(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	key := uuid.NewString()
	name := "idempotent evaluation asset " + uuid.NewString()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM prompt_evaluation_asset WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, name)
		_, _ = testPool.Exec(ctx, `DELETE FROM resource_create_request WHERE workspace_id = $1 AND resource_type = 'prompt_evaluation_asset' AND idempotency_key = $2`, testWorkspaceID, key)
	})
	body := map[string]any{
		"name": name, "asset_type": "数据集", "status": "启用", "payload": map[string]any{},
	}

	first := createPromptEvaluationAssetWithKey(t, key, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d %s", first.Code, first.Body.String())
	}
	replay := createPromptEvaluationAssetWithKey(t, key, body)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body differs\nfirst:  %s\nreplay: %s", first.Body, replay.Body)
	}

	var asset PromptEvaluationAssetResponse
	if err := json.Unmarshal(first.Body.Bytes(), &asset); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var assets, requests int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM prompt_evaluation_asset WHERE id = $1`, asset.ID).Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM resource_create_request WHERE workspace_id = $1 AND resource_type = 'prompt_evaluation_asset' AND idempotency_key = $2`, testWorkspaceID, key).Scan(&requests); err != nil {
		t.Fatalf("count requests: %v", err)
	}
	if assets != 1 || requests != 1 {
		t.Fatalf("assets=%d requests=%d, want 1/1", assets, requests)
	}

	changed := map[string]any{
		"name": name, "asset_type": "数据集", "status": "归档", "payload": map[string]any{},
	}
	conflict := createPromptEvaluationAssetWithKey(t, key, changed)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed replay = %d %s, want 409", conflict.Code, conflict.Body.String())
	}
}
