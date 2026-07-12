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

func createPromptEvaluationDatasetVersionWithKey(t *testing.T, assetID, key string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+assetID+"/dataset-versions", body), "id", assetID)
	req.Header.Set("Idempotency-Key", key)
	testHandler.CreatePromptEvaluationDatasetVersion(w, req)
	return w
}

func setupDatasetForVersionCreate(t *testing.T) PromptEvaluationAssetResponse {
	t.Helper()
	assetKey := uuid.NewString()
	assetName := "versioned dataset " + uuid.NewString()
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
	caseRecorder := createPromptEvaluationCaseWithKey(t, uuid.NewString(), map[string]any{
		"asset_id": asset.ID, "case_name": "version row", "status": "启用", "input": map[string]any{"current": true},
	})
	if caseRecorder.Code != http.StatusCreated {
		t.Fatalf("create case = %d %s", caseRecorder.Code, caseRecorder.Body.String())
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM resource_create_request WHERE resource_id IN (SELECT id FROM prompt_evaluation_dataset_version WHERE dataset_asset_id = $1)`, asset.ID)
		_, _ = testPool.Exec(ctx, `DELETE FROM resource_create_request WHERE resource_id IN (SELECT id FROM prompt_evaluation_case WHERE asset_id = $1)`, asset.ID)
		_, _ = testPool.Exec(ctx, `DELETE FROM prompt_evaluation_asset WHERE id = $1`, asset.ID)
		_, _ = testPool.Exec(ctx, `DELETE FROM resource_create_request WHERE resource_type = 'prompt_evaluation_asset' AND idempotency_key = $1`, assetKey)
	})
	return asset
}

func TestCreatePromptEvaluationDatasetVersion_IdempotentReplayAndConflict(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	asset := setupDatasetForVersionCreate(t)
	key := uuid.NewString()
	body := map[string]any{"version_label": "current", "metadata": map[string]any{"source": "test"}}

	first := createPromptEvaluationDatasetVersionWithKey(t, asset.ID, key, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d %s", first.Code, first.Body.String())
	}
	replay := createPromptEvaluationDatasetVersionWithKey(t, asset.ID, key, body)
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay = %d %s, want exact %s", replay.Code, replay.Body.String(), first.Body.String())
	}
	conflict := createPromptEvaluationDatasetVersionWithKey(t, asset.ID, key, map[string]any{"version_label": "changed"})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed replay = %d %s, want 409", conflict.Code, conflict.Body.String())
	}
	var versions int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM prompt_evaluation_dataset_version WHERE dataset_asset_id = $1`, asset.ID).Scan(&versions); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versions != 1 {
		t.Fatalf("versions after replay = %d, want 1", versions)
	}
}

func TestCreatePromptEvaluationDatasetVersion_ConcurrentRequestsGetUniqueVersions(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	asset := setupDatasetForVersionCreate(t)
	const callers = 8
	responses := make(chan *httptest.ResponseRecorder, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- createPromptEvaluationDatasetVersionWithKey(t, asset.ID, uuid.NewString(), map[string]any{})
		}()
	}
	wg.Wait()
	close(responses)

	versions := map[int32]struct{}{}
	for response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("concurrent create = %d %s", response.Code, response.Body.String())
		}
		var version PromptEvaluationDatasetVersionResponse
		if err := json.Unmarshal(response.Body.Bytes(), &version); err != nil {
			t.Fatalf("decode version: %v", err)
		}
		versions[version.Version] = struct{}{}
	}
	if len(versions) != callers {
		t.Fatalf("versions = %v, want %d unique numbers", versions, callers)
	}
}
