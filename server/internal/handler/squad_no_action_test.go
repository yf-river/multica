package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

type runningSquadLeaderTaskFixture struct {
	IssueID          string
	LeaderID         string
	TaskID           string
	TriggerCommentID string
}

func (fx runningSquadLeaderTaskFixture) postTaskComment(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	request := withURLParam(newRequest(http.MethodPost, "/api/issues/"+fx.IssueID+"/comments", body), "id", fx.IssueID)
	setTaskTokenActor(request, fx.LeaderID, fx.TaskID)
	testHandler.CreateComment(response, request)
	return response
}

func newRunningSquadLeaderTaskFixture(t *testing.T) runningSquadLeaderTaskFixture {
	t.Helper()
	ctx := context.Background()

	fx := newSquadCommentTriggerFixture(t)
	issueID := uuidToString(fx.Issue.ID)

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		SELECT runtime_id FROM agent WHERE id = $1
	`, fx.LeaderID).Scan(&runtimeID); err != nil {
		t.Fatalf("load leader runtime: %v", err)
	}

	var triggerCommentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type)
		VALUES ($1, $2, 'member', $3, 'LGTM', 'comment')
		RETURNING id
	`, issueID, testWorkspaceID, testUserID).Scan(&triggerCommentID); err != nil {
		t.Fatalf("create trigger comment: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, trigger_comment_id,
			status, priority, started_at, is_leader_task
		)
		VALUES ($1, $2, $3, $4, 'running', 0, now(), true)
		RETURNING id
	`, fx.LeaderID, runtimeID, issueID, triggerCommentID).Scan(&taskID); err != nil {
		t.Fatalf("create running squad leader task: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	return runningSquadLeaderTaskFixture{
		IssueID:          issueID,
		LeaderID:         fx.LeaderID,
		TaskID:           taskID,
		TriggerCommentID: triggerCommentID,
	}
}

func recordSquadLeaderEvaluationForTask(t *testing.T, fx runningSquadLeaderTaskFixture, outcome string) {
	t.Helper()
	recordSquadLeaderEvaluationForTaskWithHeader(t, fx, outcome, fx.TaskID)
}

func recordSquadLeaderEvaluationForTaskWithHeader(t *testing.T, fx runningSquadLeaderTaskFixture, outcome, taskIDHeader string) map[string]string {
	t.Helper()

	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/issues/"+fx.IssueID+"/squad-evaluated", map[string]any{
		"outcome": outcome,
		"reason":  "test reason",
	})
	r = withURLParam(r, "id", fx.IssueID)
	setTaskTokenActor(r, fx.LeaderID, taskIDHeader)

	testHandler.RecordSquadLeaderEvaluation(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("RecordSquadLeaderEvaluation: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var response map[string]string
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode evaluation response: %v", err)
	}
	return response
}

func TestRecordSquadLeaderEvaluationPreservesCanceledSquadLookup(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fx := newRunningSquadLeaderTaskFixture(t)
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin blocker: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(context.Background(), `LOCK TABLE squad IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("lock squad table: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)
	r := newRequest(http.MethodPost, "/api/issues/"+fx.IssueID+"/squad-evaluated", map[string]any{
		"outcome": "no_action",
		"reason":  "cancel test",
	}).WithContext(ctx)
	r = withURLParam(r, "id", fx.IssueID)
	setTaskTokenActor(r, fx.LeaderID, fx.TaskID)
	w := httptest.NewRecorder()
	testHandler.RecordSquadLeaderEvaluation(w, r)

	if w.Code != 499 {
		t.Fatalf("canceled squad lookup = %d %s, want 499 instead of false not-found", w.Code, w.Body.String())
	}
}

func TestRecordSquadLeaderEvaluation_RetryReplaysSingleActivity(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newRunningSquadLeaderTaskFixture(t)
	first := recordSquadLeaderEvaluationForTaskWithHeader(t, fx, "no_action", fx.TaskID)
	second := recordSquadLeaderEvaluationForTaskWithHeader(t, fx, "no_action", fx.TaskID)

	if first["id"] != second["id"] {
		t.Fatalf("retry created a different activity: first=%s second=%s", first["id"], second["id"])
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM activity_log
		WHERE action = 'squad_leader_evaluated' AND details->>'task_id' = $1
	`, fx.TaskID).Scan(&count); err != nil {
		t.Fatalf("count squad leader evaluations: %v", err)
	}
	if count != 1 {
		t.Fatalf("retry must preserve one evaluation activity, got %d", count)
	}
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = 'activity:created' AND task_id = $1
	`, fx.TaskID).Scan(&count); err != nil {
		t.Fatalf("count squad leader evaluation events: %v", err)
	}
	if count != 1 {
		t.Fatalf("retry must preserve one durable activity event, got %d", count)
	}
}

func TestRecordSquadLeaderEvaluation_RejectsDifferentRetry(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newRunningSquadLeaderTaskFixture(t)
	recordSquadLeaderEvaluationForTaskWithHeader(t, fx, "no_action", fx.TaskID)

	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/issues/"+fx.IssueID+"/squad-evaluated", map[string]any{
		"outcome": "action",
		"reason":  "changed after an ambiguous response",
	})
	r = withURLParam(r, "id", fx.IssueID)
	setTaskTokenActor(r, fx.LeaderID, fx.TaskID)
	testHandler.RecordSquadLeaderEvaluation(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("different retry: expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRecordSquadLeaderEvaluation_RejectsNonLeaderTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newRunningSquadLeaderTaskFixture(t)
	mustExec(t, context.Background(), `
		UPDATE agent_task_queue SET is_leader_task = false WHERE id = $1
	`, fx.TaskID)

	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/issues/"+fx.IssueID+"/squad-evaluated", map[string]any{
		"outcome": "no_action",
		"reason":  "ordinary task must not project a leader decision",
	})
	r = withURLParam(r, "id", fx.IssueID)
	setTaskTokenActor(r, fx.LeaderID, fx.TaskID)
	testHandler.RecordSquadLeaderEvaluation(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("non-leader task: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func completeRunningTask(t *testing.T, fx runningSquadLeaderTaskFixture, output string) {
	t.Helper()

	w := httptest.NewRecorder()
	r := newDaemonUserRequest("POST", "/api/daemon/tasks/"+fx.TaskID+"/complete",
		map[string]any{"output": output},
		testWorkspaceID, "legit-daemon")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("taskId", fx.TaskID)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	testHandler.CompleteTask(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("CompleteTask: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func countAgentCommentsForIssue(t *testing.T, issueID, agentID string) int {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM comment
		WHERE issue_id = $1 AND author_type = 'agent' AND author_id = $2
	`, issueID, agentID).Scan(&count); err != nil {
		t.Fatalf("count agent comments: %v", err)
	}
	return count
}

func TestCompleteTask_SquadLeaderNoActionDoesNotSynthesizeComment(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newRunningSquadLeaderTaskFixture(t)
	recordSquadLeaderEvaluationForTask(t, fx, "no_action")

	completeRunningTask(t, fx, "No action needed. Exiting silently.")

	if got := countAgentCommentsForIssue(t, fx.IssueID, fx.LeaderID); got != 0 {
		t.Fatalf("expected no squad leader comment after no_action completion, got %d", got)
	}
}

func TestCompleteTask_SquadLeaderNoActionCanonicalizesTaskID(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newRunningSquadLeaderTaskFixture(t)
	recordSquadLeaderEvaluationForTaskWithHeader(t, fx, "no_action", strings.ToUpper(fx.TaskID))

	completeRunningTask(t, fx, "No action needed. Exiting silently.")

	if got := countAgentCommentsForIssue(t, fx.IssueID, fx.LeaderID); got != 0 {
		t.Fatalf("expected no comment when no_action was recorded with uppercase task id header, got %d", got)
	}
}

func TestCompleteTask_SquadLeaderActionStillSynthesizesComment(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newRunningSquadLeaderTaskFixture(t)
	recordSquadLeaderEvaluationForTask(t, fx, "action")

	completeRunningTask(t, fx, "Delegated the review.")

	if got := countAgentCommentsForIssue(t, fx.IssueID, fx.LeaderID); got != 1 {
		t.Fatalf("expected action completion to synthesize one comment, got %d", got)
	}
}

func TestCreateComment_SquadLeaderNoActionRejectsComment(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newRunningSquadLeaderTaskFixture(t)
	recordSquadLeaderEvaluationForTask(t, fx, "no_action")

	w := fx.postTaskComment(t, map[string]any{
		"content":   "No action needed.",
		"parent_id": fx.TriggerCommentID,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("CreateComment: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if got := countAgentCommentsForIssue(t, fx.IssueID, fx.LeaderID); got != 0 {
		t.Fatalf("expected rejected no_action comment not to be stored, got %d", got)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body["error"] == "" {
		t.Fatalf("expected error message in response, got %v", body)
	}
}

func TestCreateComment_SquadLeaderNoActionAllowsMentionDispatch(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newRunningSquadLeaderTaskFixture(t)
	recordSquadLeaderEvaluationForTask(t, fx, "no_action")

	w := fx.postTaskComment(t, map[string]any{
		"content":   "继续调度 [@协调者](mention://agent/" + fx.LeaderID + ") 处理。",
		"parent_id": fx.TriggerCommentID,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if got := countAgentCommentsForIssue(t, fx.IssueID, fx.LeaderID); got != 1 {
		t.Fatalf("expected dispatch comment to be stored, got %d", got)
	}
}

func TestCreateComment_CommentTriggeredAgentAllowsTopLevelFallback(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newRunningSquadLeaderTaskFixture(t)

	w := fx.postTaskComment(t, map[string]any{
		"content": "Recovered with a top-level result comment after the thread reply failed.",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if got := countAgentCommentsForIssue(t, fx.IssueID, fx.LeaderID); got != 1 {
		t.Fatalf("expected top-level fallback comment to be stored, got %d", got)
	}
}

func TestCreateComment_CommentTriggeredAgentRejectsWrongParent(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newRunningSquadLeaderTaskFixture(t)
	var otherCommentID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type)
		VALUES ($1, $2, 'member', $3, 'another thread', 'comment')
		RETURNING id
	`, fx.IssueID, testWorkspaceID, testUserID).Scan(&otherCommentID); err != nil {
		t.Fatalf("create other comment: %v", err)
	}

	w := fx.postTaskComment(t, map[string]any{
		"content":   "This should not be allowed on a different thread.",
		"parent_id": otherCommentID,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("CreateComment: expected 409, got %d: %s", w.Code, w.Body.String())
	}
}
