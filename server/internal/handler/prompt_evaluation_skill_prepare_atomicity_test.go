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

func TestPreparePromptEvaluationSkillReEvalAssetRecoversExactCompoundResult(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	repoPath := t.TempDir()
	skillPath := ".codebuddy/skills/verify/SKILL.md"
	initSkillTestRepo(t, repoPath)
	writeSkillTestFile(t, repoPath, skillPath, "# Verify\n\nCurrent behavior.\n")
	runSkillTestGit(t, repoPath, "add", ".")
	runSkillTestGit(t, repoPath, "commit", "-m", "add current skill")
	baseCommit := runSkillTestGit(t, repoPath, "rev-parse", "HEAD")

	promptID := createPromptEvaluationTestPromptWithContent(
		t, testWorkspaceID, "prepare re-eval prompt "+uuid.NewString(), "Verify {{skill_path}}.", `[]`,
	)
	var sourceAssetID, sourceRunID, candidateID string
	payload := mustJSONBytes(map[string]any{"skill_case_drafts": []map[string]any{{
		"schema_version": "multica.skill.case.v1", "status": "approved",
		"input": "verify current skill", "expected_behavior": "current behavior",
		"verification": "run tests", "evidence_source": "git history",
		"applicable_scope": "current", "source_commit": baseCommit,
		"commit_subject": "add current skill", "skill_path": skillPath,
	}}})
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_asset (workspace_id, prompt_id, name, asset_type, payload, created_by)
		VALUES ($1, $2, $3, '测试套件', $4, $5) RETURNING id
	`, testWorkspaceID, promptID, "prepare source "+uuid.NewString(), payload, testUserID).Scan(&sourceAssetID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_run (workspace_id, asset_id, prompt_id, run_kind, status, created_by)
		VALUES ($1, $2, $3, '模板渲染检查', '未通过', $4) RETURNING id
	`, testWorkspaceID, sourceAssetID, promptID, testUserID).Scan(&sourceRunID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_optimization_candidate (
			workspace_id, asset_id, run_id, prompt_id, candidate_name,
			candidate_content, source_failure_summary, metrics, created_by
		) VALUES ($1, $2, $3, $4, $5, 'candidate', '{}'::jsonb, '{}'::jsonb, $6)
		RETURNING id
	`, testWorkspaceID, sourceAssetID, sourceRunID, promptID, "prepare candidate "+uuid.NewString(), testUserID).Scan(&candidateID); err != nil {
		t.Fatal(err)
	}
	assetName := "recoverable skill re-eval " + uuid.NewString()
	requestKey := uuid.NewString()
	requestBody := map[string]any{
		"name": assetName, "repo_path": repoPath, "target_branch": "HEAD", "skill_path": skillPath,
		"snapshot": map[string]any{
			"schema_version": promptEvaluationSkillSnapshotSchema, "provider": "local", "repo": "fixture",
			"repo_path": repoPath, "branch": "HEAD", "base_commit": baseCommit,
			"skill_path": skillPath, "skill_hash": "source-hash", "snapshot_time": "2026-07-12T00:00:00Z",
		},
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_optimization_candidate WHERE id=$1`, candidateID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE id=$1 OR name=$2`, sourceAssetID, assetName)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE id=$1`, promptID)
	})
	prepare := func(body map[string]any) *httptest.ResponseRecorder {
		req := newRequest(http.MethodPost, "/api/prompt-evaluation-optimization-candidates/"+candidateID+"/skill-re-eval-asset", body)
		req.Header.Set("Idempotency-Key", requestKey)
		w := httptest.NewRecorder()
		testHandler.PreparePromptEvaluationSkillReEvalAsset(w, withURLParam(req, "id", candidateID))
		return w
	}
	first := prepare(requestBody)
	replay := prepare(requestBody)
	if first.Code != http.StatusCreated {
		t.Fatalf("first prepare = %d %s", first.Code, first.Body.String())
	}
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("prepare replay = %d %s, want exact %s", replay.Code, replay.Body.String(), first.Body.String())
	}
	var response PromptEvaluationSkillReEvalAssetResponse
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Asset.ID != requestKey {
		t.Fatalf("asset id = %s, want request key %s", response.Asset.ID, requestKey)
	}
	const concurrentReplays = 8
	responses := make(chan *httptest.ResponseRecorder, concurrentReplays)
	var group sync.WaitGroup
	for range concurrentReplays {
		group.Add(1)
		go func() {
			defer group.Done()
			responses <- prepare(requestBody)
		}()
	}
	group.Wait()
	close(responses)
	for concurrent := range responses {
		if concurrent.Code != http.StatusCreated || concurrent.Body.String() != first.Body.String() {
			t.Fatalf("concurrent replay = %d %s, want exact %s", concurrent.Code, concurrent.Body.String(), first.Body.String())
		}
	}
	changedRequest := make(map[string]any, len(requestBody))
	for key, value := range requestBody {
		changedRequest[key] = value
	}
	changedRequest["description"] = "different request"
	conflict := prepare(changedRequest)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed request = %d %s, want 409", conflict.Code, conflict.Body.String())
	}
	var assets, cases int
	if err := testPool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM prompt_evaluation_asset WHERE name=$1),
		(SELECT count(*) FROM prompt_evaluation_case WHERE asset_id=$2)
	`, assetName, response.Asset.ID).Scan(&assets, &cases); err != nil {
		t.Fatal(err)
	}
	if assets != 1 || cases != 1 {
		t.Fatalf("prepare writes = assets:%d cases:%d, want 1/1", assets, cases)
	}

	rollbackKey := uuid.NewString()
	rollbackName := "rollback skill re-eval " + uuid.NewString()
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE name=$1`, rollbackName)
	})
	rollbackBody := make(map[string]any, len(requestBody))
	for key, value := range requestBody {
		rollbackBody[key] = value
	}
	rollbackBody["name"] = rollbackName
	var metricsBefore []byte
	if err := testPool.QueryRow(ctx, `SELECT metrics FROM prompt_evaluation_optimization_candidate WHERE id=$1`, candidateID).Scan(&metricsBefore); err != nil {
		t.Fatal(err)
	}
	dropFailureWitness := installResourceCreateCompletionFailure(t, resourceTypePromptReEvalAsset, rollbackKey)
	rollbackRequest := newRequest(http.MethodPost, "/api/prompt-evaluation-optimization-candidates/"+candidateID+"/skill-re-eval-asset", rollbackBody)
	rollbackRequest.Header.Set("Idempotency-Key", rollbackKey)
	rollbackResponse := httptest.NewRecorder()
	testHandler.PreparePromptEvaluationSkillReEvalAsset(rollbackResponse, withURLParam(rollbackRequest, "id", candidateID))
	dropFailureWitness()
	if rollbackResponse.Code != http.StatusInternalServerError {
		t.Fatalf("completion failure = %d %s, want 500", rollbackResponse.Code, rollbackResponse.Body.String())
	}
	var rollbackAssets, rollbackRequests int
	var metricsAfter []byte
	if err := testPool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM prompt_evaluation_asset WHERE name=$1),
		(SELECT count(*) FROM resource_create_request WHERE idempotency_key=$2),
		(SELECT metrics FROM prompt_evaluation_optimization_candidate WHERE id=$3)
	`, rollbackName, rollbackKey, candidateID).Scan(&rollbackAssets, &rollbackRequests, &metricsAfter); err != nil {
		t.Fatal(err)
	}
	if rollbackAssets != 0 || rollbackRequests != 0 || string(metricsAfter) != string(metricsBefore) {
		t.Fatalf("completion failure leaked writes: assets=%d requests=%d metrics_changed=%t",
			rollbackAssets, rollbackRequests, string(metricsAfter) != string(metricsBefore))
	}
}
