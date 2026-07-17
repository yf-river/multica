package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSyncPromptEvaluationRunFromTaskRollsBackAllProjections(t *testing.T) {
	requireHandlerDatabase(t)
	cleanupPromptEvaluationAgentRunTest(t)
	_, fixture, _ := createPromptEvaluationAgentRunFixture(t, "manual sync atomicity", "manual sync rollback")
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue SET status='completed', started_at=now()-interval '1 second', completed_at=now()
		WHERE id=$1
	`, fixture.TaskID); err != nil {
		t.Fatal(err)
	}
	var initialAssetPayload []byte
	if err := testPool.QueryRow(ctx, `SELECT payload FROM prompt_evaluation_asset WHERE id=$1`, fixture.Run.AssetID).Scan(&initialAssetPayload); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "_")
	functionName := "fail_manual_eval_sync_" + suffix
	triggerName := "fail_manual_eval_sync_" + suffix
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected manual sync asset failure'; END $$;
		CREATE TRIGGER %s BEFORE UPDATE ON prompt_evaluation_asset
		FOR EACH ROW WHEN (OLD.id = '%s'::uuid)
		EXECUTE FUNCTION %s()
	`, functionName, triggerName, fixture.Run.AssetID, functionName)); err != nil {
		t.Fatal(err)
	}
	dropFailureWitness := func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(
			`DROP TRIGGER IF EXISTS %s ON prompt_evaluation_asset; DROP FUNCTION IF EXISTS %s()`,
			triggerName, functionName,
		))
	}
	t.Cleanup(dropFailureWitness)
	syncRun := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		testHandler.SyncPromptEvaluationRunFromTask(w, withURLParam(newRequest(
			http.MethodPost, "/api/prompt-evaluation-runs/"+fixture.Run.ID+"/sync", nil,
		), "id", fixture.Run.ID))
		return w
	}
	failed := syncRun()
	dropFailureWitness()
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("injected sync failure = %d %s, want 500", failed.Code, failed.Body.String())
	}
	var runStatus, trialStatus string
	var assetPayload []byte
	loadState := func() {
		if err := testPool.QueryRow(ctx, `
			SELECT r.status, t.status, a.payload
			FROM prompt_evaluation_run r
			JOIN prompt_evaluation_trial t ON t.run_id=r.id
			JOIN prompt_evaluation_asset a ON a.id=r.asset_id
			WHERE r.id=$1 LIMIT 1
		`, fixture.Run.ID).Scan(&runStatus, &trialStatus, &assetPayload); err != nil {
			t.Fatal(err)
		}
	}
	loadState()
	if runStatus != "已入队" || trialStatus != "待执行" || string(assetPayload) != string(initialAssetPayload) {
		t.Fatalf("failed sync leaked projections: run=%s trial=%s payload=%s", runStatus, trialStatus, assetPayload)
	}
	recovered := syncRun()
	if recovered.Code != http.StatusOK {
		t.Fatalf("sync recovery = %d %s", recovered.Code, recovered.Body.String())
	}
	loadState()
	if runStatus != "需人工复核" || trialStatus != "需人工复核" {
		t.Fatalf("recovered sync state: run=%s trial=%s", runStatus, trialStatus)
	}
	var payload map[string]any
	if err := json.Unmarshal(assetPayload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["最近Agent运行"] == nil {
		t.Fatalf("recovered sync did not persist asset snapshot: %s", assetPayload)
	}
	latest, ok := payload["最近Agent运行"].(map[string]any)
	if !ok || latest["状态"] != "需人工复核" {
		t.Fatalf("recovered latest run snapshot = %#v", payload["最近Agent运行"])
	}
}
