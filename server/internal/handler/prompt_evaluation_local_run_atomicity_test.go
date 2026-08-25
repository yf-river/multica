package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRunPromptEvaluationAssetRecoversExactLocalRun(t *testing.T) {
	requireHandlerDatabase(t)
	cleanupPromptEvaluationAgentRunTest(t)
	assetName := "recoverable local evaluation " + uuid.NewString()
	asset, _ := createPromptEvaluationAgentRunAssetFixture(t, assetName, "local response loss")
	key := uuid.NewString()
	run := func() *httptest.ResponseRecorder {
		req := withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+asset.ID+"/run", nil), "id", asset.ID)
		req.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		testHandler.RunPromptEvaluationAsset(w, req)
		return w
	}
	first := run()
	replay := run()
	if first.Code != http.StatusOK {
		t.Fatalf("first local run = %d %s", first.Code, first.Body.String())
	}
	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() {
		t.Fatalf("local run replay = %d %s, want exact %s", replay.Code, replay.Body.String(), first.Body.String())
	}
	var runs, trials int
	if err := testPool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM prompt_evaluation_run WHERE asset_id=$1),
		(SELECT count(*) FROM prompt_evaluation_trial WHERE asset_id=$1)
	`, asset.ID).Scan(&runs, &trials); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || trials != 1 {
		t.Fatalf("local run writes = runs:%d trials:%d, want 1/1", runs, trials)
	}
}

func TestRunPromptEvaluationAssetRollsBackPartialRun(t *testing.T) {
	requireHandlerDatabase(t)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	promptID := createPromptEvaluationTestPromptWithContent(
		t,
		testWorkspaceID,
		"atomic local evaluation prompt "+suffix,
		"请评估 {{issue_title}}。",
		`[]`,
	)
	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "atomic local evaluation " + suffix,
		"asset_type": "测试套件",
		"payload": map[string]any{
			"experiment_dimensions": []string{"命中率"},
			"cases": []map[string]any{{
				"case_name":         "atomic local case",
				"variables":         map[string]any{"issue_title": "atomic"},
				"expected_contains": []string{"atomic"},
			}},
		},
	}))
	if createW.Code != http.StatusCreated {
		t.Fatalf("create asset: expected 201, got %d: %s", createW.Code, createW.Body.String())
	}
	var asset PromptEvaluationAssetResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &asset); err != nil {
		t.Fatalf("decode asset: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE id = $1`, asset.ID)
	})

	functionName := "prompt_eval_trial_fail_fn_" + suffix
	triggerName := "prompt_eval_trial_fail_" + suffix
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON prompt_evaluation_trial`, triggerName))
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})
	if _, err := testPool.Exec(context.Background(), fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'forced local prompt evaluation trial failure';
		END;
		$$;
		CREATE TRIGGER %s
		BEFORE INSERT ON prompt_evaluation_trial
		FOR EACH ROW WHEN (NEW.asset_id = '%s')
		EXECUTE FUNCTION %s();
	`, functionName, triggerName, asset.ID, functionName)); err != nil {
		t.Fatalf("install local trial failure: %v", err)
	}

	runW := httptest.NewRecorder()
	testHandler.RunPromptEvaluationAsset(runW, withURLParam(
		newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+asset.ID+"/run", nil),
		"id",
		asset.ID,
	))
	if runW.Code != http.StatusInternalServerError {
		t.Fatalf("forced local run failure: expected 500, got %d: %s", runW.Code, runW.Body.String())
	}

	var runs, trials, scores int
	if err := testPool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM prompt_evaluation_run WHERE asset_id = $1),
			(SELECT count(*) FROM prompt_evaluation_trial WHERE asset_id = $1),
			(SELECT count(*) FROM prompt_evaluation_dimension_score WHERE asset_id = $1)
	`, asset.ID).Scan(&runs, &trials, &scores); err != nil {
		t.Fatalf("count local run writes: %v", err)
	}
	if runs != 0 || trials != 0 || scores != 0 {
		t.Fatalf("failed local run left writes: runs=%d trials=%d scores=%d", runs, trials, scores)
	}
}
