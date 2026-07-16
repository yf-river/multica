package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestPublishPromptEvaluationOptimizationCandidateRecoversExactResult(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	promptID := createPromptEvaluationTestPromptWithContent(
		t, testWorkspaceID, "publish recovery prompt "+uuid.NewString(), "Current prompt", `[]`,
	)
	var assetID, runID, candidateID, otherCandidateID, rollbackCandidateID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_asset (workspace_id, prompt_id, name, asset_type, payload, created_by)
		VALUES ($1, $2, $3, '测试套件', '{"cases":[]}'::jsonb, $4) RETURNING id
	`, testWorkspaceID, promptID, "publish recovery asset "+uuid.NewString(), testUserID).Scan(&assetID); err != nil {
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
			candidate_content, rationale, created_by
		) VALUES ($1, $2, $3, $4, $5, 'Improved current prompt', 'Current evidence', $6)
		RETURNING id
	`, testWorkspaceID, assetID, runID, promptID, "publish recovery candidate "+uuid.NewString(), testUserID).Scan(&candidateID); err != nil {
		t.Fatal(err)
	}
	for index, target := range []*string{&otherCandidateID, &rollbackCandidateID} {
		if err := testPool.QueryRow(ctx, `
			INSERT INTO prompt_evaluation_optimization_candidate (
				workspace_id, asset_id, run_id, prompt_id, candidate_name,
				candidate_content, rationale, created_by
			) VALUES ($1, $2, $3, $4, $5, 'Another improved prompt', 'Current evidence', $6)
			RETURNING id
		`, testWorkspaceID, assetID, runID, promptID, fmt.Sprintf("publish recovery candidate %d %s", index, uuid.NewString()), testUserID).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE id=$1`, assetID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE id=$1 OR name LIKE 'publish recovery prompt % 优化发布 %'`, promptID)
	})
	requestKey := uuid.NewString()
	publish := func(targetCandidateID, key string) *httptest.ResponseRecorder {
		req := newRequest(http.MethodPost, "/api/prompt-evaluation-optimization-candidates/"+targetCandidateID+"/publish", nil)
		req.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		testHandler.PublishPromptEvaluationOptimizationCandidate(w, withURLParam(req, "id", targetCandidateID))
		return w
	}
	first := publish(candidateID, requestKey)
	replay := publish(candidateID, requestKey)
	if first.Code != http.StatusOK {
		t.Fatalf("first publish = %d %s", first.Code, first.Body.String())
	}
	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() {
		t.Fatalf("publish replay = %d %s, want exact %s", replay.Code, replay.Body.String(), first.Body.String())
	}
	var response PublishPromptEvaluationOptimizationCandidateResponse
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Prompt.ID != requestKey {
		t.Fatalf("published prompt id = %s, want request key %s", response.Prompt.ID, requestKey)
	}
	const concurrentReplays = 8
	responses := make(chan *httptest.ResponseRecorder, concurrentReplays)
	var group sync.WaitGroup
	for range concurrentReplays {
		group.Add(1)
		go func() {
			defer group.Done()
			responses <- publish(candidateID, requestKey)
		}()
	}
	group.Wait()
	close(responses)
	for concurrent := range responses {
		if concurrent.Code != http.StatusOK || concurrent.Body.String() != first.Body.String() {
			t.Fatalf("concurrent replay = %d %s, want exact %s", concurrent.Code, concurrent.Body.String(), first.Body.String())
		}
	}
	conflict := publish(otherCandidateID, requestKey)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed candidate = %d %s, want 409", conflict.Code, conflict.Body.String())
	}
	var prompts, versions int
	if err := testPool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM prompt_library_item WHERE id=$2),
		(SELECT count(*) FROM prompt_library_version WHERE source_candidate_id=$1)
	`, candidateID, response.Prompt.ID).Scan(&prompts, &versions); err != nil {
		t.Fatal(err)
	}
	if prompts != 1 || versions != 1 {
		t.Fatalf("publish writes = prompts:%d versions:%d, want 1/1", prompts, versions)
	}

	rollbackKey := uuid.NewString()
	dropFailureWitness := installResourceCreateCompletionFailure(t, resourceTypePromptPublish, rollbackKey)
	failed := publish(rollbackCandidateID, rollbackKey)
	dropFailureWitness()
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("completion failure = %d %s, want 500", failed.Code, failed.Body.String())
	}
	var rollbackPrompts, rollbackVersions, rollbackRequests int
	var rollbackStatus string
	var rollbackPublishedID *string
	if err := testPool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM prompt_library_item WHERE id=$1),
		(SELECT count(*) FROM prompt_library_version WHERE source_candidate_id=$2),
		(SELECT count(*) FROM resource_create_request WHERE idempotency_key=$1),
		(SELECT status FROM prompt_evaluation_optimization_candidate WHERE id=$2),
		(SELECT published_prompt_id::text FROM prompt_evaluation_optimization_candidate WHERE id=$2)
	`, rollbackKey, rollbackCandidateID).Scan(
		&rollbackPrompts, &rollbackVersions, &rollbackRequests, &rollbackStatus, &rollbackPublishedID,
	); err != nil {
		t.Fatal(err)
	}
	if rollbackPrompts != 0 || rollbackVersions != 0 || rollbackRequests != 0 || rollbackStatus != "待确认" || rollbackPublishedID != nil {
		t.Fatalf("completion failure leaked writes: prompts=%d versions=%d requests=%d status=%s published=%v",
			rollbackPrompts, rollbackVersions, rollbackRequests, rollbackStatus, rollbackPublishedID)
	}

	rejectKey := uuid.NewString()
	reject := func(reason, key string) *httptest.ResponseRecorder {
		req := newRequest(http.MethodPost, "/api/prompt-evaluation-optimization-candidates/"+otherCandidateID+"/reject", map[string]any{
			"reason": reason,
		})
		req.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		testHandler.RejectPromptEvaluationOptimizationCandidate(w, withURLParam(req, "id", otherCandidateID))
		return w
	}
	firstReject := reject("current evidence is insufficient", rejectKey)
	replayedReject := reject("current evidence is insufficient", rejectKey)
	if firstReject.Code != http.StatusOK {
		t.Fatalf("first reject = %d %s", firstReject.Code, firstReject.Body.String())
	}
	if replayedReject.Code != http.StatusOK || replayedReject.Body.String() != firstReject.Body.String() {
		t.Fatalf("reject replay = %d %s, want exact %s", replayedReject.Code, replayedReject.Body.String(), firstReject.Body.String())
	}
	changedReject := reject("different reason", rejectKey)
	if changedReject.Code != http.StatusConflict {
		t.Fatalf("changed reject = %d %s, want 409", changedReject.Code, changedReject.Body.String())
	}
	var rejectedStatus string
	if err := testPool.QueryRow(ctx, `SELECT status FROM prompt_evaluation_optimization_candidate WHERE id=$1`, otherCandidateID).Scan(&rejectedStatus); err != nil {
		t.Fatal(err)
	}
	if rejectedStatus != "已拒绝" {
		t.Fatalf("rejected status = %s, want 已拒绝", rejectedStatus)
	}
}
