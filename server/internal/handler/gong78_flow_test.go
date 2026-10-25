package handler

import (
	"context"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/review"
	"github.com/multica-ai/multica/server/internal/testutil"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGong78SweepBeyondFirstFifty(t *testing.T) {
	reviewer := enableReviewSettings(t, "")
	for n := 0; n < 50; n++ {
		id := dbfx.Issue(t, "already passed", testutil.Cols{"status": "in_review", "updated_at": testutil.Raw("now()-interval '10 minutes'"), "metadata": testutil.Raw(`'{"review_passed":true}'::jsonb`)})
		seedReviewState(t, id, reviewer, review.StatePassed, 1, "")
	}
	missed := dbfx.Issue(t, "missed review", testutil.Cols{"status": "in_review", "updated_at": testutil.Raw("now()-interval '5 minutes'")})
	cleanupIssueTasks(t, missed)
	testHandler.SweepIssueReviews(context.Background())
	if got := reviewTasksFor(t, missed, reviewer); got != 1 {
		t.Fatalf("51st task dispatches=%d", got)
	}
	testHandler.SweepIssueReviews(context.Background())
	if got := reviewTasksFor(t, missed, reviewer); got != 1 {
		t.Fatalf("duplicate sweep dispatches=%d", got)
	}
}
func TestGong78SummaryDoesNotReturnPatch(t *testing.T) {
	issue := dbfx.Issue(t, "bounded history", nil)
	synthetic := strings.Repeat("large-patch", 10000)
	agent := dbfx.Agent(t, "summary agent", testRuntimeID)
	task := dbfx.Task(t, agent, testutil.Cols{"issue_id": issue, "runtime_id": testRuntimeID, "status": "completed", "result": map[string]any{"code_diff": map[string]any{"files": []any{map[string]any{"patch": synthetic}}}}})
	req := withURLParam(newRequest("GET", "/api/issues/"+issue+"/task-runs", nil), "id", issue)
	rec := httptest.NewRecorder()
	testHandler.ListTasksByIssue(rec, req)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), synthetic) || rec.Body.Len() > 20000 {
		t.Fatalf("unbounded list: %d %d", rec.Code, rec.Body.Len())
	}
	var tasks []AgentTaskResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tasks); err != nil || len(tasks) != 1 {
		t.Fatalf("summary: %v %s", err, rec.Body.String())
	}
	req = withURLParam(newRequest("GET", "/detail", nil), "id", issue)
	chi.RouteContext(req.Context()).URLParams.Add("taskId", task)
	rec = httptest.NewRecorder()
	testHandler.GetIssueTaskRun(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), synthetic) {
		t.Fatalf("detail missing immutable patch: %d", rec.Code)
	}
}

func TestGong78NoActionDoesNotWakeDeveloper(t *testing.T) {
	reviewer := enableReviewSettings(t, "")
	dev := dbfx.Agent(t, "consultation owner", testRuntimeID)
	issue := dbfx.Issue(t, "investigation only", testutil.Cols{"status": "todo", "assignee_type": "agent", "assignee_id": dev})
	if rec := moveIssueTo(t, issue, "in_review"); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if rec := submitReviewVerdict(t, issue, reviewer, "no_action", map[string]any{"summary": "本轮仅调查，无待处理交付"}); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	finishReviewTasks(t, issue, reviewer)
	loaded, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issue))
	if err != nil {
		t.Fatal(err)
	}
	testHandler.startReviewRound(context.Background(), loaded)
	if got := reviewTasksFor(t, issue, reviewer); got != 1 {
		t.Fatalf("unchanged review requeued: %d", got)
	}
	var count int
	dbfx.QueryRow(t, "SELECT count(*) FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2", issue, dev).Scan(&count)
	if count != 0 || issueStatus(t, issue) != "in_review" {
		t.Fatalf("no_action changed workflow: %d", count)
	}
	dbfx.Comment(t, issue, "新增要求：现在实施修复")
	loaded, _ = testHandler.Queries.GetIssue(context.Background(), parseUUID(issue))
	testHandler.startReviewRound(context.Background(), loaded)
	if got := reviewTasksFor(t, issue, reviewer); got != 2 {
		t.Fatalf("new substantive input not reviewed: %d", got)
	}
}

func TestGong78AuditFailureDoesNotRollbackPrimaryEdit(t *testing.T) {
	ctx := context.Background()
	agent := dbfx.Agent(t, "audit transaction", testRuntimeID)
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": testRuntimeID, "status": "running"})
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE agent SET description='primary edit saved' WHERE id=$1`, agent); err != nil {
		t.Fatal(err)
	}
	req := newRequest("PUT", "/audit", nil)
	req.Header.Set("X-Task-ID", task)
	testHandler.recordRunTextChangeTx(ctx, tx, req, testWorkspaceID, runTextTarget{Kind: "agent", ID: agent}, "instructions", "before", "audit\x00invalid")
	if err = tx.Commit(ctx); err != nil {
		t.Fatalf("audit poisoned primary transaction: %v", err)
	}
	var description string
	dbfx.QueryRow(t, `SELECT description FROM agent WHERE id=$1`, agent).Scan(&description)
	if description != "primary edit saved" {
		t.Fatal("primary edit lost")
	}
}

func TestGong78ExternalConfirmationDoesNotDispatchOrComment(t *testing.T) {
	delivery, _ := batchFixture(t)
	var issue, plan string
	var revision int
	dbfx.QueryRow(t, `SELECT d.issue_id::text,d.plan_id::text,p.comment_revision FROM issue_delivery d JOIN issue_delivery_plan p ON p.id=d.plan_id WHERE d.id=$1`, delivery).Scan(&issue, &plan, &revision)
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(issue, "claim", "external-gong78", coordinationOwner{}, "")).Want(200)
	var before int
	dbfx.QueryRow(t, `SELECT count(*) FROM comment WHERE issue_id=$1`, issue).Scan(&before)
	rec := httptest.NewRecorder()
	testHandler.ConfirmIssuePlan(rec, withURLParam(newRequest("POST", "/confirm", map[string]any{"plan_id": plan, "revision": revision}), "id", issue))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var after, tasks int
	dbfx.QueryRow(t, `SELECT count(*) FROM comment WHERE issue_id=$1`, issue).Scan(&after)
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1`, issue).Scan(&tasks)
	if before != after || tasks != 0 {
		t.Fatalf("external confirmation triggered work: comments %d -> %d, tasks=%d", before, after, tasks)
	}
}

func TestGong78PaginationKeepsLegacyRunIdentityCoverage(t *testing.T) {
	issue := dbfx.Issue(t, "history compatibility", nil)
	agent := dbfx.Agent(t, "history", testRuntimeID)
	for n := 0; n < 51; n++ {
		dbfx.Task(t, agent, testutil.Cols{"issue_id": issue, "runtime_id": testRuntimeID, "status": "completed"})
	}
	for _, tc := range []struct {
		query string
		count int
	}{{"", 51}, {"?offset=0", 50}, {"?offset=50", 1}} {
		req := withURLParam(newRequest("GET", "/history"+tc.query, nil), "id", issue)
		rec := httptest.NewRecorder()
		testHandler.ListTasksByIssue(rec, req)
		var runs []AgentTaskResponse
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &runs) != nil || len(runs) != tc.count {
			t.Fatalf("%s: HTTP %d count %d", tc.query, rec.Code, len(runs))
		}
	}
}

func TestGong78CapabilityProbeUsesBooleanAndPreservesManagementGate(t *testing.T) {
	t.Setenv("MULTICA_ENV_ID", "prod")
	t.Setenv("MULTICA_TEST_AGENT_CONTROL_WORKSPACE_ID", "00000000-0000-0000-0000-000000000099")
	check := func(want bool) {
		rec := httptest.NewRecorder()
		testHandler.TestAgentControlCapability(rec, newRequest("GET", "/capability", nil))
		var value struct {
			Available bool `json:"available"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &value) != nil || value.Available != want {
			t.Fatalf("capability %d %s", rec.Code, rec.Body.String())
		}
	}
	check(false)
	rec := httptest.NewRecorder()
	testHandler.TestAgentControl(rec, newRequest("GET", "/control", nil))
	if rec.Code != 403 {
		t.Fatal("management permission widened")
	}
	t.Setenv("MULTICA_TEST_AGENT_CONTROL_WORKSPACE_ID", testWorkspaceID)
	check(true)
	t.Setenv("MULTICA_ENV_ID", "int")
	check(false)
}
