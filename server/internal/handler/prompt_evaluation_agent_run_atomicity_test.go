package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunPromptEvaluationAssetAgentRollsBackEveryWrite(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
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
				"名称":   "atomic case",
				"变量":   map[string]any{"issue_title": "atomic"},
				"期望包含": []string{"atomic"},
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
	testHandler.RunPromptEvaluationAssetAgent(runW, withURLParam(
		newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+asset.ID+"/agent-run", nil),
		"id",
		asset.ID,
	))
	if runW.Code != http.StatusInternalServerError {
		t.Fatalf("forced agent run failure: expected 500, got %d: %s", runW.Code, runW.Body.String())
	}

	var sessions, messages, tasks, runs, trials int
	title := "训练评估：" + assetName
	if err := testPool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM chat_session WHERE workspace_id = $1 AND title = $2),
			(SELECT count(*) FROM chat_message WHERE chat_session_id IN (SELECT id FROM chat_session WHERE workspace_id = $1 AND title = $2)),
			(SELECT count(*) FROM agent_task_queue WHERE chat_session_id IN (SELECT id FROM chat_session WHERE workspace_id = $1 AND title = $2)),
			(SELECT count(*) FROM prompt_evaluation_run WHERE asset_id = $3),
			(SELECT count(*) FROM prompt_evaluation_trial WHERE asset_id = $3)
	`, testWorkspaceID, title, asset.ID).Scan(&sessions, &messages, &tasks, &runs, &trials); err != nil {
		t.Fatalf("count agent run writes: %v", err)
	}
	if sessions != 0 || messages != 0 || tasks != 0 || runs != 0 || trials != 0 {
		t.Fatalf("failed agent run left writes: sessions=%d messages=%d tasks=%d runs=%d trials=%d", sessions, messages, tasks, runs, trials)
	}
}
