package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestCreatePromptEvaluationOptimizationCandidateRecoversExactResult(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	promptID := createPromptEvaluationTestPromptWithContent(
		t, testWorkspaceID, "candidate recovery prompt "+uuid.NewString(), "Current {{input}}", `[]`,
	)
	var assetID, runID, otherRunID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_asset (workspace_id, prompt_id, name, asset_type, payload, created_by)
		VALUES ($1, $2, $3, '测试套件', '{"cases":[]}'::jsonb, $4) RETURNING id
	`, testWorkspaceID, promptID, "candidate recovery asset "+uuid.NewString(), testUserID).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_run (
			workspace_id, asset_id, prompt_id, run_kind, status,
			total_cases, failed_cases, failure_reason, created_by
		) VALUES ($1, $2, $3, '本地渲染', '未通过', 1, 1, 'missing current result', $4)
		RETURNING id
	`, testWorkspaceID, assetID, promptID, testUserID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_run (
			workspace_id, asset_id, prompt_id, run_kind, status,
			total_cases, failed_cases, failure_reason, created_by
		) VALUES ($1, $2, $3, '本地渲染', '未通过', 1, 1, 'another failure', $4)
		RETURNING id
	`, testWorkspaceID, assetID, promptID, testUserID).Scan(&otherRunID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE id=$1`, assetID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE id=$1`, promptID)
	})
	requestKey := uuid.NewString()
	create := func(targetRunID, key string) *httptest.ResponseRecorder {
		req := newRequest(http.MethodPost, "/api/prompt-evaluation-runs/"+targetRunID+"/optimization-candidates", nil)
		req.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		testHandler.CreatePromptEvaluationOptimizationCandidate(w, withURLParam(req, "id", targetRunID))
		return w
	}
	first := create(runID, requestKey)
	replay := create(runID, requestKey)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d %s", first.Code, first.Body.String())
	}
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("candidate replay = %d %s, want exact %s", replay.Code, replay.Body.String(), first.Body.String())
	}
	var response PromptEvaluationOptimizationCandidateResponse
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ID != requestKey {
		t.Fatalf("candidate id = %s, want request key %s", response.ID, requestKey)
	}
	const concurrentReplays = 8
	responses := make(chan *httptest.ResponseRecorder, concurrentReplays)
	var group sync.WaitGroup
	for range concurrentReplays {
		group.Add(1)
		go func() {
			defer group.Done()
			responses <- create(runID, requestKey)
		}()
	}
	group.Wait()
	close(responses)
	for concurrent := range responses {
		if concurrent.Code != http.StatusCreated || concurrent.Body.String() != first.Body.String() {
			t.Fatalf("concurrent replay = %d %s, want exact %s", concurrent.Code, concurrent.Body.String(), first.Body.String())
		}
	}
	conflict := create(otherRunID, requestKey)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed run = %d %s, want 409", conflict.Code, conflict.Body.String())
	}
	var candidates int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM prompt_evaluation_optimization_candidate WHERE run_id=$1`, runID).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 1 {
		t.Fatalf("candidate rows = %d, want 1", candidates)
	}

	rollbackKey := uuid.NewString()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "_")
	functionName := "fail_candidate_complete_" + suffix
	triggerName := "fail_candidate_complete_" + suffix
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected candidate completion failure'; END $$;
		CREATE TRIGGER %s BEFORE UPDATE ON resource_create_request
		FOR EACH ROW WHEN (NEW.idempotency_key = '%s'::uuid)
		EXECUTE FUNCTION %s()
	`, functionName, triggerName, rollbackKey, functionName)); err != nil {
		t.Fatal(err)
	}
	dropFailureWitness := func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(
			`DROP TRIGGER IF EXISTS %s ON resource_create_request; DROP FUNCTION IF EXISTS %s()`,
			triggerName, functionName,
		))
	}
	t.Cleanup(dropFailureWitness)
	failed := create(otherRunID, rollbackKey)
	dropFailureWitness()
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("completion failure = %d %s, want 500", failed.Code, failed.Body.String())
	}
	var rollbackCandidates, rollbackRequests int
	if err := testPool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM prompt_evaluation_optimization_candidate WHERE id=$1),
		(SELECT count(*) FROM resource_create_request WHERE idempotency_key=$1)
	`, rollbackKey).Scan(&rollbackCandidates, &rollbackRequests); err != nil {
		t.Fatal(err)
	}
	if rollbackCandidates != 0 || rollbackRequests != 0 {
		t.Fatalf("completion failure leaked writes: candidates=%d requests=%d", rollbackCandidates, rollbackRequests)
	}
}
