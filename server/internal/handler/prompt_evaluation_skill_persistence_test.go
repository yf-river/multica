package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCheckPromptEvaluationSkillFreshnessFailsWhenEvidenceCannotPersist(t *testing.T) {
	repoPath := t.TempDir()
	runSkillTestGit(t, repoPath, "init")
	runSkillTestGit(t, repoPath, "config", "user.email", "test@example.com")
	runSkillTestGit(t, repoPath, "config", "user.name", "Test User")
	skillPath := ".codebuddy/skills/05-verify/SKILL.md"
	writeSkillTestFile(t, repoPath, skillPath, "# Verify\n\n- Persist evidence.\n")
	runSkillTestGit(t, repoPath, "add", skillPath)
	runSkillTestGit(t, repoPath, "commit", "-m", "add verify skill")
	snapshot, err := buildPromptEvaluationSkillSnapshot(CreatePromptEvaluationSkillSnapshotRequest{
		Provider:  "local_directory",
		Repo:      filepath.Base(repoPath),
		RepoPath:  repoPath,
		Branch:    "HEAD",
		SkillPath: skillPath,
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("build skill snapshot: %v", err)
	}

	ctx := context.Background()
	suffix := uuid.NewString()
	var promptID, assetID, runID, candidateID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_library_item (workspace_id, name, content, created_by)
		VALUES ($1, $2, 'test prompt', $3) RETURNING id
	`, testWorkspaceID, "freshness persistence "+suffix, testUserID).Scan(&promptID); err != nil {
		t.Fatalf("create prompt: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_asset (workspace_id, prompt_id, name, asset_type, created_by)
		VALUES ($1, $2, $3, '测试套件', $4) RETURNING id
	`, testWorkspaceID, promptID, "freshness persistence "+suffix, testUserID).Scan(&assetID); err != nil {
		t.Fatalf("create asset: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_run (workspace_id, asset_id, prompt_id, run_kind, status, created_by)
		VALUES ($1, $2, $3, '本地渲染', '未通过', $4) RETURNING id
	`, testWorkspaceID, assetID, promptID, testUserID).Scan(&runID); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_optimization_candidate (
			workspace_id, asset_id, run_id, prompt_id, candidate_name,
			candidate_content, source_failure_summary, created_by
		) VALUES ($1, $2, $3, $4, 'freshness candidate', 'candidate', $5, $6)
		RETURNING id
	`, testWorkspaceID, assetID, runID, promptID, mustJSONBytes(map[string]any{"skill_snapshot": snapshot}), testUserID).Scan(&candidateID); err != nil {
		t.Fatalf("create candidate: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM prompt_evaluation_asset WHERE id = $1`, assetID)
		mustExec(t, context.Background(), `DELETE FROM prompt_library_item WHERE id = $1`, promptID)
	})

	triggerSuffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "test_skill_freshness_persist_failure_" + triggerSuffix
	triggerName := "test_skill_freshness_persist_failure_trigger_" + triggerSuffix
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger AS $$
		BEGIN
			IF NEW.id = '%s'::uuid AND NEW.metrics IS DISTINCT FROM OLD.metrics THEN
				RAISE EXCEPTION 'forced freshness evidence persistence failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER %s BEFORE UPDATE ON prompt_evaluation_optimization_candidate
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, candidateID, triggerName, functionName)); err != nil {
		t.Fatalf("install freshness persistence failure: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON prompt_evaluation_optimization_candidate`, triggerName))
		mustExec(t, context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/prompt-evaluation-optimization-candidates/"+candidateID+"/skill-freshness", nil)
	req = withURLParam(req, "id", candidateID)
	testHandler.CheckPromptEvaluationSkillCandidateFreshness(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when freshness evidence cannot persist, got %d: %s", w.Code, w.Body.String())
	}
}
