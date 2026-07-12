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

func TestRunPromptEvaluationSkillReEvalRollsBackWhenCandidateEvidenceFails(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	promptID := createPromptEvaluationTestPromptWithContent(
		t,
		testWorkspaceID,
		"atomic skill re-eval prompt "+suffix,
		"Verify {{skill_path}}.",
		`[]`,
	)

	var sourceAssetID, sourceRunID, candidateID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_asset (workspace_id, prompt_id, name, asset_type, created_by)
		VALUES ($1, $2, $3, '测试套件', $4)
		RETURNING id
	`, testWorkspaceID, promptID, "atomic skill source "+suffix, testUserID).Scan(&sourceAssetID); err != nil {
		t.Fatalf("create source asset: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_run (workspace_id, asset_id, prompt_id, run_kind, status, created_by)
		VALUES ($1, $2, $3, '本地渲染', '未通过', $4)
		RETURNING id
	`, testWorkspaceID, sourceAssetID, promptID, testUserID).Scan(&sourceRunID); err != nil {
		t.Fatalf("create source run: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_optimization_candidate (
			workspace_id, asset_id, run_id, prompt_id, candidate_name,
			candidate_content, source_failure_summary, metrics, created_by
		) VALUES ($1, $2, $3, $4, $5, 'candidate', '{}'::jsonb, '{}'::jsonb, $6)
		RETURNING id
	`, testWorkspaceID, sourceAssetID, sourceRunID, promptID, "atomic skill candidate "+suffix, testUserID).Scan(&candidateID); err != nil {
		t.Fatalf("create candidate: %v", err)
	}

	snapshot := map[string]any{
		"schema_version": promptEvaluationSkillSnapshotSchema,
		"provider":       "gongfeng",
		"repo":           "example/goal-test",
		"branch":         "current",
		"base_commit":    "abc123456789",
		"skill_path":     ".codebuddy/skills/verify/SKILL.md",
		"skill_hash":     "current-skill-hash",
		"snapshot_time":  "2026-07-11T00:00:00Z",
	}
	createAssetW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createAssetW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "atomic skill re-eval " + suffix,
		"asset_type": "测试套件",
		"payload": map[string]any{
			"skill_re_eval_contract": "multica.skill.re_eval.v1",
			"source_candidate_id":    candidateID,
			"source_skill_snapshot":  snapshot,
			"re_eval_snapshot":       snapshot,
			"cases": []map[string]any{{
				"name":              "atomic skill re-eval case",
				"variables":         map[string]any{"skill_path": ".codebuddy/skills/verify/SKILL.md"},
				"expected_contains": []string{".codebuddy/skills/verify/SKILL.md"},
			}},
		},
	}))
	if createAssetW.Code != http.StatusCreated {
		t.Fatalf("create re-eval asset: expected 201, got %d: %s", createAssetW.Code, createAssetW.Body.String())
	}
	var reEvalAsset PromptEvaluationAssetResponse
	if err := json.Unmarshal(createAssetW.Body.Bytes(), &reEvalAsset); err != nil {
		t.Fatalf("decode re-eval asset: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_optimization_candidate WHERE id = $1`, candidateID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE id IN ($1, $2)`, reEvalAsset.ID, sourceAssetID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE id = $1`, promptID)
	})

	var originalPayload string
	if err := testPool.QueryRow(ctx, `SELECT payload::text FROM prompt_evaluation_asset WHERE id = $1`, reEvalAsset.ID).Scan(&originalPayload); err != nil {
		t.Fatalf("load original re-eval payload: %v", err)
	}

	functionName := "test_skill_re_eval_candidate_failure_" + suffix
	triggerName := "test_skill_re_eval_candidate_failure_trigger_" + suffix
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger AS $$
		BEGIN
			IF NEW.id = '%s'::uuid AND NEW.metrics IS DISTINCT FROM OLD.metrics THEN
				RAISE EXCEPTION 'forced skill re-eval candidate evidence failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER %s BEFORE UPDATE ON prompt_evaluation_optimization_candidate
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, candidateID, triggerName, functionName)); err != nil {
		t.Fatalf("install candidate evidence failure: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON prompt_evaluation_optimization_candidate`, triggerName))
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})

	runW := httptest.NewRecorder()
	runReq := newRequest(http.MethodPost, "/api/prompt-evaluation-optimization-candidates/"+candidateID+"/skill-re-eval/run", map[string]any{
		"asset_id": reEvalAsset.ID,
	})
	testHandler.RunPromptEvaluationSkillReEval(runW, withURLParam(runReq, "id", candidateID))
	if runW.Code != http.StatusInternalServerError {
		t.Fatalf("forced candidate evidence failure: expected 500, got %d: %s", runW.Code, runW.Body.String())
	}

	var runs, trials, scores int
	var currentPayload string
	if err := testPool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM prompt_evaluation_run WHERE asset_id = $1),
			(SELECT count(*) FROM prompt_evaluation_trial WHERE asset_id = $1),
			(SELECT count(*) FROM prompt_evaluation_dimension_score WHERE asset_id = $1),
			(SELECT payload::text FROM prompt_evaluation_asset WHERE id = $1)
	`, reEvalAsset.ID).Scan(&runs, &trials, &scores, &currentPayload); err != nil {
		t.Fatalf("load failed re-eval writes: %v", err)
	}
	if runs != 0 || trials != 0 || scores != 0 || currentPayload != originalPayload {
		t.Fatalf(
			"failed skill re-eval left partial writes: runs=%d trials=%d scores=%d payload_changed=%t",
			runs,
			trials,
			scores,
			currentPayload != originalPayload,
		)
	}
}
