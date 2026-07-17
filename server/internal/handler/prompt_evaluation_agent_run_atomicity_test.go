package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRunPromptEvaluationAssetAgentRecoversExactCompoundResult(t *testing.T) {
	requireHandlerDatabase(t)
	cleanupPromptEvaluationAgentRunTest(t)
	assetName := "recoverable agent evaluation " + uuid.NewString()
	asset, _ := createPromptEvaluationAgentRunAssetFixture(t, assetName, "response loss")
	key := uuid.NewString()
	run := func() *httptest.ResponseRecorder {
		req := withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+asset.ID+"/agent-run", nil), "id", asset.ID)
		req.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		testHandler.RunPromptEvaluationAssetAgent(w, req)
		return w
	}
	first := run()
	replay := run()
	if first.Code != http.StatusAccepted {
		t.Fatalf("first agent run = %d %s", first.Code, first.Body.String())
	}
	if replay.Code != http.StatusAccepted || replay.Body.String() != first.Body.String() {
		t.Fatalf("agent run replay = %d %s, want exact %s", replay.Code, replay.Body.String(), first.Body.String())
	}
	var firstResponse promptEvaluationAgentRunResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstResponse); err != nil {
		t.Fatal(err)
	}
	if firstResponse.Run.ID != key {
		t.Fatalf("run id = %s, want request identity %s", firstResponse.Run.ID, key)
	}
	responses := make(chan *httptest.ResponseRecorder, 8)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			responses <- run()
		}()
	}
	wait.Wait()
	close(responses)
	for response := range responses {
		if response.Code != http.StatusAccepted || response.Body.String() != first.Body.String() {
			t.Fatalf("concurrent agent run replay = %d %s, want exact", response.Code, response.Body.String())
		}
	}
	otherAsset, _ := createPromptEvaluationAgentRunAssetFixture(t, "different agent evaluation "+uuid.NewString(), "different")
	conflictReq := withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+otherAsset.ID+"/agent-run", nil), "id", otherAsset.ID)
	conflictReq.Header.Set("Idempotency-Key", key)
	conflict := httptest.NewRecorder()
	testHandler.RunPromptEvaluationAssetAgent(conflict, conflictReq)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed asset replay = %d %s, want 409", conflict.Code, conflict.Body.String())
	}

	var sessions, tasks, runs, trials int
	if err := testPool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM chat_session WHERE workspace_id=$1 AND title=$2),
		(SELECT count(*) FROM agent_task_queue WHERE chat_session_id IN (
			SELECT id FROM chat_session WHERE workspace_id=$1 AND title=$2
		)),
		(SELECT count(*) FROM prompt_evaluation_run WHERE asset_id=$3),
		(SELECT count(*) FROM prompt_evaluation_trial WHERE asset_id=$3)
	`, testWorkspaceID, "训练评估："+assetName, asset.ID).Scan(&sessions, &tasks, &runs, &trials); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || tasks != 1 || runs != 1 || trials != 1 {
		t.Fatalf("agent run compound writes = sessions:%d tasks:%d runs:%d trials:%d, want 1/1/1/1", sessions, tasks, runs, trials)
	}
}

func TestRunPromptEvaluationAssetAgentRollsBackEveryWrite(t *testing.T) {
	requireHandlerDatabase(t)
	cleanupPromptEvaluationAgentRunTest(t)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'online', 'atomic prompt evaluation runtime', '{}'::jsonb, $4, 'personal', now())
	`, testWorkspaceID, "prompt-eval-atomic-daemon-"+suffix, "prompt-eval-codebuddy-"+suffix, testUserID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	promptID := createPromptEvaluationTestPromptWithContent(
		t,
		testWorkspaceID,
		"atomic evaluation prompt "+suffix,
		"请评估 {{issue_title}}。",
		`[]`,
	)
	assetName := "atomic evaluation " + suffix
	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       assetName,
		"asset_type": "测试套件",
		"payload": map[string]any{
			"cases": []map[string]any{{
				"case_name":         "atomic case",
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

	functionName := "prompt_eval_run_fail_fn_" + suffix
	triggerName := "prompt_eval_run_fail_" + suffix
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON prompt_evaluation_run`, triggerName))
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})
	if _, err := testPool.Exec(context.Background(), fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'forced prompt evaluation run failure';
		END;
		$$;
		CREATE TRIGGER %s
		BEFORE INSERT ON prompt_evaluation_run
		FOR EACH ROW WHEN (NEW.asset_id = '%s')
		EXECUTE FUNCTION %s();
	`, functionName, triggerName, asset.ID, functionName)); err != nil {
		t.Fatalf("install run failure: %v", err)
	}

	runW := httptest.NewRecorder()
	runKey := uuid.NewString()
	runReq := withURLParam(
		newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+asset.ID+"/agent-run", nil),
		"id",
		asset.ID,
	)
	runReq.Header.Set("Idempotency-Key", runKey)
	testHandler.RunPromptEvaluationAssetAgent(runW, runReq)
	if runW.Code != http.StatusInternalServerError {
		t.Fatalf("forced agent run failure: expected 500, got %d: %s", runW.Code, runW.Body.String())
	}

	var sessions, messages, tasks, runs, trials, requests int
	title := "训练评估：" + assetName
	if err := testPool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM chat_session WHERE workspace_id = $1 AND title = $2),
			(SELECT count(*) FROM chat_message WHERE chat_session_id IN (SELECT id FROM chat_session WHERE workspace_id = $1 AND title = $2)),
			(SELECT count(*) FROM agent_task_queue WHERE chat_session_id IN (SELECT id FROM chat_session WHERE workspace_id = $1 AND title = $2)),
			(SELECT count(*) FROM prompt_evaluation_run WHERE asset_id = $3),
			(SELECT count(*) FROM prompt_evaluation_trial WHERE asset_id = $3),
			(SELECT count(*) FROM resource_create_request WHERE resource_type='prompt_evaluation_agent_run' AND idempotency_key=$4)
	`, testWorkspaceID, title, asset.ID, runKey).Scan(&sessions, &messages, &tasks, &runs, &trials, &requests); err != nil {
		t.Fatalf("count agent run writes: %v", err)
	}
	if sessions != 0 || messages != 0 || tasks != 0 || runs != 0 || trials != 0 || requests != 0 {
		t.Fatalf("failed agent run left writes: sessions=%d messages=%d tasks=%d runs=%d trials=%d requests=%d", sessions, messages, tasks, runs, trials, requests)
	}
}

func TestRunPromptEvaluationAssetAgentCompletionFailureRollsBackEveryWrite(t *testing.T) {
	requireHandlerDatabase(t)
	cleanupPromptEvaluationAgentRunTest(t)
	assetName := "agent run completion rollback " + uuid.NewString()
	asset, _ := createPromptEvaluationAgentRunAssetFixture(t, assetName, "completion failure")
	key := uuid.NewString()
	installResourceCreateCompletionFailure(t, resourceTypePromptEvaluationRun, key)
	req := withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+asset.ID+"/agent-run", nil), "id", asset.ID)
	req.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	testHandler.RunPromptEvaluationAssetAgent(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("completion failure = %d %s, want 500", w.Code, w.Body.String())
	}
	var sessions, tasks, runs, trials, requests int
	if err := testPool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM chat_session WHERE workspace_id=$1 AND title=$2),
		(SELECT count(*) FROM agent_task_queue WHERE chat_session_id IN (SELECT id FROM chat_session WHERE workspace_id=$1 AND title=$2)),
		(SELECT count(*) FROM prompt_evaluation_run WHERE asset_id=$3),
		(SELECT count(*) FROM prompt_evaluation_trial WHERE asset_id=$3),
		(SELECT count(*) FROM resource_create_request WHERE resource_type='prompt_evaluation_agent_run' AND idempotency_key=$4)
	`, testWorkspaceID, "训练评估："+assetName, asset.ID, key).Scan(&sessions, &tasks, &runs, &trials, &requests); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 || tasks != 0 || runs != 0 || trials != 0 || requests != 0 {
		t.Fatalf("completion failure left sessions:%d tasks:%d runs:%d trials:%d requests:%d", sessions, tasks, runs, trials, requests)
	}
}
