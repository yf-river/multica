package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestUpdatePromptEvaluationOptimizationCandidateDoesNotPartiallyCommitSkillPatch(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	promptID := createPromptEvaluationTestPromptWithContent(
		t, testWorkspaceID, "candidate update atomicity "+uuid.NewString(), "Verify {{input}}.", `[]`,
	)
	var assetID, runID, candidateID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_asset (workspace_id, prompt_id, name, asset_type, payload, created_by)
		VALUES ($1, $2, $3, '测试套件', '{}'::jsonb, $4) RETURNING id
	`, testWorkspaceID, promptID, "candidate update asset "+uuid.NewString(), testUserID).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_run (workspace_id, asset_id, prompt_id, run_kind, status, created_by)
		VALUES ($1, $2, $3, '本地渲染', '未通过', $4) RETURNING id
	`, testWorkspaceID, assetID, promptID, testUserID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_optimization_candidate (
			workspace_id, asset_id, run_id, prompt_id, candidate_name,
			candidate_content, rationale, source_failure_summary, metrics, created_by
		) VALUES ($1, $2, $3, $4, 'original name', 'original content', 'original rationale', '{}'::jsonb, '{}'::jsonb, $5)
		RETURNING id
	`, testWorkspaceID, assetID, runID, promptID, testUserID).Scan(&candidateID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_optimization_candidate WHERE id=$1`, candidateID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE id=$1`, assetID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE id=$1`, promptID)
	})
	update := func(body map[string]any) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		testHandler.UpdatePromptEvaluationOptimizationCandidate(w, withURLParam(newRequest(
			http.MethodPut, "/api/prompt-evaluation-optimization-candidates/"+candidateID, body,
		), "id", candidateID))
		return w
	}
	assertOriginal := func() {
		var name, content, rationale string
		var metrics []byte
		if err := testPool.QueryRow(ctx, `SELECT candidate_name, candidate_content, rationale, metrics
			FROM prompt_evaluation_optimization_candidate WHERE id=$1`, candidateID).Scan(&name, &content, &rationale, &metrics); err != nil {
			t.Fatal(err)
		}
		if name != "original name" || content != "original content" || rationale != "original rationale" || strings.Contains(string(metrics), "skill_patch") {
			t.Fatalf("candidate partially changed: name=%q content=%q rationale=%q metrics=%s", name, content, rationale, metrics)
		}
	}
	invalid := update(map[string]any{
		"candidate_name": "invalid patch name", "candidate_content": "invalid patch content",
		"rationale": "invalid patch rationale", "skill_patch": map[string]any{"patch": "diff"},
	})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid patch update = %d %s, want 400", invalid.Code, invalid.Body.String())
	}
	assertOriginal()

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "_")
	functionName := "fail_candidate_skill_patch_" + suffix
	triggerName := "fail_candidate_skill_patch_" + suffix
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected candidate skill patch failure'; END $$;
		CREATE TRIGGER %s BEFORE UPDATE ON prompt_evaluation_optimization_candidate
		FOR EACH ROW WHEN (OLD.id = '%s'::uuid AND NEW.metrics ? 'skill_patch' AND NOT (OLD.metrics ? 'skill_patch'))
		EXECUTE FUNCTION %s()
	`, functionName, triggerName, candidateID, functionName)); err != nil {
		t.Fatal(err)
	}
	dropFailureWitness := func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(
			`DROP TRIGGER IF EXISTS %s ON prompt_evaluation_optimization_candidate; DROP FUNCTION IF EXISTS %s()`,
			triggerName, functionName,
		))
	}
	t.Cleanup(dropFailureWitness)
	failed := update(map[string]any{
		"candidate_name": "new name", "candidate_content": "new content", "rationale": "new rationale",
		"skill_patch": map[string]any{
			"patch": "diff --git a/a b/a\n", "candidate_intent": "update_existing_skill", "source_snapshot": map[string]any{
				"schema_version": promptEvaluationSkillSnapshotSchema, "repo_path": "/repo", "branch": "HEAD",
				"skill_path": ".codebuddy/skills/verify/SKILL.md", "skill_hash": "source-hash",
			},
		},
	})
	dropFailureWitness()
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("injected skill patch failure = %d %s, want 500", failed.Code, failed.Body.String())
	}
	assertOriginal()
}
