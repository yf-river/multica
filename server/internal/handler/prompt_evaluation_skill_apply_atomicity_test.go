package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestApplyPromptEvaluationSkillCandidateRecoversFilesAfterDatabaseRollback(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	repoPath := t.TempDir()
	skillPath := ".codebuddy/skills/verify/SKILL.md"
	v1 := "# Verify\n\nRun focused checks.\n"
	v2 := "# Verify\n\nRun focused checks and retain evidence.\n"
	runSkillTestGit(t, repoPath, "init")
	runSkillTestGit(t, repoPath, "config", "user.email", "test@multica.local")
	runSkillTestGit(t, repoPath, "config", "user.name", "Multica Test")
	writeSkillTestFile(t, repoPath, skillPath, v1)
	runSkillTestGit(t, repoPath, "add", ".")
	runSkillTestGit(t, repoPath, "commit", "-m", "add verify skill")
	baseCommit := runSkillTestGit(t, repoPath, "rev-parse", "HEAD")
	writeSkillTestFile(t, repoPath, skillPath, v2)
	patch := runSkillTestGit(t, repoPath, "diff", "--", skillPath)
	writeSkillTestFile(t, repoPath, skillPath, v1)

	promptID := createPromptEvaluationTestPromptWithContent(
		t, testWorkspaceID, "skill apply recovery "+uuid.NewString(), "Verify {{input}}.", `[]`,
	)
	var assetID, runID, candidateID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_asset (workspace_id, prompt_id, name, asset_type, payload, created_by)
		VALUES ($1, $2, $3, '测试套件', '{}'::jsonb, $4) RETURNING id
	`, testWorkspaceID, promptID, "skill apply asset "+uuid.NewString(), testUserID).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_run (workspace_id, asset_id, prompt_id, run_kind, status, created_by)
		VALUES ($1, $2, $3, '模板渲染检查', '未通过', $4) RETURNING id
	`, testWorkspaceID, assetID, promptID, testUserID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_optimization_candidate (
			workspace_id, asset_id, run_id, prompt_id, candidate_name,
			candidate_content, source_failure_summary, metrics, created_by
		) VALUES ($1, $2, $3, $4, $5, $6, '{}'::jsonb, '{}'::jsonb, $7)
		RETURNING id
	`, testWorkspaceID, assetID, runID, promptID, "recover skill apply "+uuid.NewString(), patch, testUserID).Scan(&candidateID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_optimization_candidate WHERE id=$1`, candidateID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE id=$1`, assetID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE id=$1`, promptID)
	})

	requestKey := uuid.NewString()
	body := map[string]any{
		"repo_path": repoPath, "target_branch": "HEAD", "skill_path": skillPath,
		"candidate_patch": patch,
		"snapshot": map[string]any{
			"schema_version": promptEvaluationSkillSnapshotSchema, "provider": "local", "repo": "fixture",
			"repo_path": repoPath, "branch": "HEAD", "base_commit": baseCommit,
			"skill_path": skillPath, "skill_hash": sha256Hex([]byte(v1)),
			"snapshot_time": time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	apply := func() *httptest.ResponseRecorder {
		req := newRequest(http.MethodPost, "/api/prompt-evaluation-optimization-candidates/"+candidateID+"/skill-apply", body)
		req.Header.Set("Idempotency-Key", requestKey)
		w := httptest.NewRecorder()
		testHandler.ApplyPromptEvaluationSkillCandidate(w, withURLParam(req, "id", candidateID))
		return w
	}

	dropFailureWitness := installResourceCreateCompletionFailure(t, resourceTypePromptSkillApply, requestKey)
	failed := apply()
	dropFailureWitness()
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("injected completion failure = %d %s, want 500", failed.Code, failed.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(repoPath, filepath.FromSlash(skillPath)))
	if err != nil || string(content) != v2 {
		t.Fatalf("file side effect after DB rollback = %q, err=%v", content, err)
	}
	var requestCount int
	var metrics []byte
	if err := testPool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM resource_create_request WHERE idempotency_key=$1), metrics
		FROM prompt_evaluation_optimization_candidate WHERE id=$2`, requestKey, candidateID).Scan(&requestCount, &metrics); err != nil {
		t.Fatal(err)
	}
	if requestCount != 0 || strings.Contains(string(metrics), "skill_apply") {
		t.Fatalf("failed transaction leaked DB state: requests=%d metrics=%s", requestCount, metrics)
	}

	recovered := apply()
	if recovered.Code != http.StatusOK {
		t.Fatalf("recovery = %d %s", recovered.Code, recovered.Body.String())
	}
	var response promptEvaluationSkillApplyCandidateResponse
	if err := json.Unmarshal(recovered.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Apply.Status != "applied" || response.Apply.PatchCheck != "already_applied" {
		t.Fatalf("recovered apply = %+v", response.Apply)
	}
	replay := apply()
	if replay.Code != http.StatusOK || replay.Body.String() != recovered.Body.String() {
		t.Fatalf("replay = %d %s, want exact %s", replay.Code, replay.Body.String(), recovered.Body.String())
	}
	appliedRequestKey := requestKey
	requestKey = uuid.NewString()
	distinctRequest := apply()
	if distinctRequest.Code != http.StatusConflict {
		t.Fatalf("distinct request after apply = %d %s, want 409", distinctRequest.Code, distinctRequest.Body.String())
	}
	changelog, err := os.ReadFile(filepath.Join(repoPath, ".codebuddy/skills/verify/CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	marker := "<!-- multica-skill-apply:" + appliedRequestKey + " -->"
	if strings.Count(string(changelog), marker) != 1 || strings.Count(string(changelog), " - Skill optimization candidate\n") != 1 {
		t.Fatalf("changelog was duplicated: %s", changelog)
	}
}
