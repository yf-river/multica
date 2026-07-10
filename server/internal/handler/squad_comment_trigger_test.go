package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestCommentMentionsAnyone covers the pure helper that drives the
// "skip leader on @<anyone>" behavior. Routing-style mentions
// (agent/member/squad/all) count; issue cross-references do not. Kept as a
// unit test so it runs without a database connection.
func TestCommentMentionsAnyone(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "empty", content: "", want: false},
		{name: "plain text", content: "please take a look", want: false},
		{name: "literal at sign only", content: "ping @alice", want: false},
		{name: "agent mention", content: "[@A](mention://agent/11111111-1111-1111-1111-111111111111) handle this", want: true},
		{name: "member mention", content: "[@Bob](mention://member/22222222-2222-2222-2222-222222222222)", want: true},
		{name: "squad mention", content: "[@Squad](mention://squad/44444444-4444-4444-4444-444444444444)", want: true},
		{name: "mention all", content: "[@all](mention://all/all)", want: true},
		{name: "issue mention only", content: "see [MUL-1](mention://issue/33333333-3333-3333-3333-333333333333)", want: false},
		{name: "issue + plain text", content: "see [MUL-1](mention://issue/33333333-3333-3333-3333-333333333333) for context", want: false},
		{name: "agent plus member", content: "[@A](mention://agent/11111111-1111-1111-1111-111111111111) cc [@B](mention://member/22222222-2222-2222-2222-222222222222)", want: true},
		{name: "issue plus member", content: "blocks [MUL-1](mention://issue/33333333-3333-3333-3333-333333333333) — [@Bob](mention://member/22222222-2222-2222-2222-222222222222)", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commentMentionsAnyone(tc.content); got != tc.want {
				t.Fatalf("commentMentionsAnyone(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

// shouldEnqueueSquadLeaderOnCommentForTest reports whether the shared comment
// trigger computation would wake the issue's assigned squad leader — the
// boolean view these integration tests assert on.
func shouldEnqueueSquadLeaderOnCommentForTest(ctx context.Context, issue db.Issue, content, authorType, authorID string) bool {
	_, ok := testHandler.computeAssignedSquadLeaderCommentTrigger(ctx, issue, content, authorType, authorID, commentTriggerComputeOptions{})
	return ok
}

// squadCommentTriggerFixture wires a squad assigned to a fresh issue and
// returns the loaded db.Issue plus the leader agent UUID for use in
// computeAssignedSquadLeaderCommentTrigger integration tests.
type squadCommentTriggerFixture struct {
	Issue    db.Issue
	SquadID  string
	LeaderID string
	OtherID  string // second agent in workspace (with runtime), used as a non-leader @mention target
}

func newSquadCommentTriggerFixture(t *testing.T) squadCommentTriggerFixture {
	t.Helper()
	ctx := context.Background()

	// Reuse the seeded "Handler Test Agent" as the leader — it has a runtime.
	var leaderID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM agent WHERE workspace_id = $1 ORDER BY created_at ASC LIMIT 1
	`, testWorkspaceID).Scan(&leaderID); err != nil {
		t.Fatalf("load leader agent: %v", err)
	}

	// Spin up a second agent in the same workspace as a non-leader mention
	// target. createHandlerTestAgent installs a t.Cleanup row deletion.
	otherID := createHandlerTestAgent(t, "Squad Comment Other", nil)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, $2, '', $3, $4)
		RETURNING id
	`, testWorkspaceID, "Squad Comment Trigger", leaderID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	var issueID string
	issueNumber := nextHandlerTestIssueNumber(t)
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, assignee_type, assignee_id, number)
		VALUES ($1, 'member', $2, $3, 'squad', $4, $5)
		RETURNING id
	`, testWorkspaceID, testUserID, "squad comment trigger", squadID, issueNumber).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}

	return squadCommentTriggerFixture{
		Issue:    issue,
		SquadID:  squadID,
		LeaderID: leaderID,
		OtherID:  otherID,
	}
}

func TestEnqueueTaskForSquadLeaderForcesFreshSession(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	fx := newSquadCommentTriggerFixture(t)

	task, err := testHandler.TaskService.EnqueueTaskForSquadLeader(ctx, fx.Issue, util.MustParseUUID(fx.LeaderID), pgtype.UUID{})
	if err != nil {
		t.Fatalf("EnqueueTaskForSquadLeader: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, task.ID)
	})

	var forceFresh bool
	if err := testPool.QueryRow(ctx, `
		SELECT force_fresh_session FROM agent_task_queue WHERE id = $1
	`, task.ID).Scan(&forceFresh); err != nil {
		t.Fatalf("read force_fresh_session: %v", err)
	}
	if !forceFresh {
		t.Fatal("squad leader task should force a fresh provider session")
	}
}

func TestCreateComment_SquadSOPRoleKeyMentionTriggersStageAgent(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1 AND name IN ($2, $3))`, testWorkspaceID, projectSOPAgentPM, projectSOPAgent01)
	_, _ = testPool.Exec(ctx, `DELETE FROM squad_member WHERE member_id IN (SELECT id FROM agent WHERE workspace_id = $1 AND name IN ($2, $3))`, testWorkspaceID, projectSOPAgentPM, projectSOPAgent01)
	_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND name IN ($2, $3)`, testWorkspaceID, projectSOPAgentPM, projectSOPAgent01)

	pmID := createHandlerTestAgent(t, projectSOPAgentPM, nil)
	clarifyID := createHandlerTestAgent(t, projectSOPAgent01, nil)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, $2, '', $3, $4)
		RETURNING id
	`, testWorkspaceID, "SOP Role Key Mention Squad", pmID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})
	if _, err := testPool.Exec(ctx, `
		INSERT INTO squad_member (squad_id, member_type, member_id, role)
		VALUES ($1, 'agent', $2, 'PM'), ($1, 'agent', $3, '01')
	`, squadID, pmID, clarifyID); err != nil {
		t.Fatalf("create squad members: %v", err)
	}

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, status, assignee_type, assignee_id)
		VALUES ($1, 'member', $2, 'sop role-key mention trigger', 'todo', 'squad', $3)
		RETURNING id
	`, testWorkspaceID, testUserID, squadID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "## PM 调度\n\n请 **01-需求澄清** (@01-clarify) 开始澄清。",
	})
	r.Header.Set("X-Actor-Source", "task_token")
	r.Header.Set("X-Agent-ID", pmID)
	r = withURLParam(r, "id", issueID)
	testHandler.CreateComment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var queued int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'
	`, issueID, clarifyID).Scan(&queued); err != nil {
		t.Fatalf("count 01 tasks: %v", err)
	}
	if queued != 1 {
		t.Fatalf("queued 01 tasks = %d, want 1", queued)
	}
}

// TestShouldEnqueueSquadLeaderOnComment_SkipsWhenMemberMentionsAnyone
// encodes Bohan's rule (MUL-2170): a member comment that explicitly @mentions
// anyone — agent, member, squad, or @all — must NOT wake the squad leader.
// Issue cross-references are not routing and do not suppress the leader.
// Agent-authored comments are exempt: the leader still coordinates threads.
func TestShouldEnqueueSquadLeaderOnComment_SkipsWhenMemberMentionsAnyone(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	fx := newSquadCommentTriggerFixture(t)
	ctx := context.Background()

	cases := []struct {
		name        string
		content     string
		authorType  string
		authorID    string
		want        bool
		description string
	}{
		{
			name:        "member plain comment triggers leader",
			content:     "what is the latest on this?",
			authorType:  "member",
			authorID:    testUserID,
			want:        true,
			description: "no @ in body → leader must coordinate as today",
		},
		{
			name:        "member issue cross-reference only triggers leader",
			content:     "blocked by [MUL-1](mention://issue/" + testUserID + ")",
			authorType:  "member",
			authorID:    testUserID,
			want:        true,
			description: "issue mentions are not routing — leader still owns dispatch",
		},
		{
			name:        "member mentions another member skips leader",
			content:     "[@self](mention://member/" + testUserID + ") please weigh in",
			authorType:  "member",
			authorID:    testUserID,
			want:        false,
			description: "user routed at a human — leader stays out (extended rule)",
		},
		{
			name:        "member mentions non-leader agent skips leader",
			content:     "[@Other](mention://agent/" + fx.OtherID + ") please take this",
			authorType:  "member",
			authorID:    testUserID,
			want:        false,
			description: "user routed at an agent — leader stays out",
		},
		{
			name:        "member mentions leader skips leader on comment path",
			content:     "[@Leader](mention://agent/" + fx.LeaderID + ") your call",
			authorType:  "member",
			authorID:    testUserID,
			want:        false,
			description: "even @leader is dispatched via the mention path; comment path must not double-enqueue",
		},
		{
			name:        "member mention all skips leader",
			content:     "[@all](mention://all/all) heads up",
			authorType:  "member",
			authorID:    testUserID,
			want:        false,
			description: "@all is a broadcast — leader does not need to wake to evaluate routing",
		},
		{
			name:        "member mentions a squad skips leader",
			content:     "handing to [@Other Squad](mention://squad/" + fx.SquadID + ")",
			authorType:  "member",
			authorID:    testUserID,
			want:        false,
			description: "@squad routes the issue to that squad's leader — current leader stays out",
		},
		{
			name:        "agent comment with @agent still triggers leader",
			content:     "delegating to [@Other](mention://agent/" + fx.OtherID + ")",
			authorType:  "agent",
			authorID:    fx.OtherID,
			want:        true,
			description: "agent-authored replies always reach leader so it can coordinate next step",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldEnqueueSquadLeaderOnCommentForTest(ctx, fx.Issue, tc.content, tc.authorType, tc.authorID)
			if got != tc.want {
				t.Fatalf("%s\n  content=%q author=%s/%s\n  got=%v want=%v",
					tc.description, tc.content, tc.authorType, tc.authorID, got, tc.want)
			}
		})
	}
}

// TestShouldEnqueueSquadLeaderOnComment_LeaderSelfTriggerByRole covers the
// role-aware self-trigger guard added for MUL-2218. The leader agent itself
// should be skipped only when its last activity on the issue was a leader
// task — never just because the comment author equals the leader ID. This
// matters for dual-role agents (leader + worker of the same squad): a
// comment posted from the worker task must still wake the leader.
func TestShouldEnqueueSquadLeaderOnComment_LeaderSelfTriggerByRole(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	fx := newSquadCommentTriggerFixture(t)
	ctx := context.Background()
	issueID := uuidToString(fx.Issue.ID)

	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	})

	clearTasks := func() {
		if _, err := testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID); err != nil {
			t.Fatalf("clear tasks: %v", err)
		}
	}
	insertTask := func(isLeader bool, status string) {
		t.Helper()
		var runtimeID string
		if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, fx.LeaderID).Scan(&runtimeID); err != nil {
			t.Fatalf("load runtime: %v", err)
		}
		if _, err := testPool.Exec(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, is_leader_task)
			VALUES ($1, $2, $3, $4, $5)
		`, fx.LeaderID, runtimeID, issueID, status, isLeader); err != nil {
			t.Fatalf("insert task: %v", err)
		}
	}

	t.Run("no prior task wakes leader (fresh external trigger)", func(t *testing.T) {
		clearTasks()
		if got := shouldEnqueueSquadLeaderOnCommentForTest(ctx, fx.Issue, "noted", "agent", fx.LeaderID); !got {
			t.Fatalf("no prior task: expected leader to be enqueued, got skip")
		}
	})

	t.Run("prior leader task suppresses self-trigger", func(t *testing.T) {
		clearTasks()
		insertTask(true, "completed")
		if got := shouldEnqueueSquadLeaderOnCommentForTest(ctx, fx.Issue, "noted", "agent", fx.LeaderID); got {
			t.Fatalf("after leader task: expected skip (anti-loop), got enqueue")
		}
	})

	t.Run("prior worker task still wakes leader (dual-role agent)", func(t *testing.T) {
		clearTasks()
		insertTask(false, "completed")
		if got := shouldEnqueueSquadLeaderOnCommentForTest(ctx, fx.Issue, "result", "agent", fx.LeaderID); !got {
			t.Fatalf("after worker task: expected leader to be enqueued (MUL-2218), got skip")
		}
	})

	t.Run("most recent task is the one that matters", func(t *testing.T) {
		clearTasks()
		insertTask(true, "completed")  // older leader task
		insertTask(false, "completed") // newer worker task
		if got := shouldEnqueueSquadLeaderOnCommentForTest(ctx, fx.Issue, "result", "agent", fx.LeaderID); !got {
			t.Fatalf("latest task is worker: expected leader to be enqueued, got skip")
		}
	})
}

func TestCompleteTask_WorkerStageCompletionEnqueuesSquadLeader(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	leaderID := createHandlerTestAgent(t, "SOP Auto Continue Leader", nil)
	workerID := createHandlerTestSOPAgent(t, "SOP Worker Stage 01-clarify", "01-clarify")

	var leaderRuntimeID, workerRuntimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, leaderID).Scan(&leaderRuntimeID); err != nil {
		t.Fatalf("load leader runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, workerID).Scan(&workerRuntimeID); err != nil {
		t.Fatalf("load worker runtime: %v", err)
	}

	profile := `{
		"profile_key":"test-sop",
		"steps":[
			{"key":"pm","name":"pm","role_key":"pm"},
			{"key":"01-clarify","name":"01-需求澄清","role_key":"01-clarify"},
			{"key":"02-design","name":"02-方案设计","role_key":"02-design"}
		]
	}`
	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, sop_profile)
		VALUES ($1, $2, '', $3, $4, $5)
		RETURNING id
	`, testWorkspaceID, "SOP Auto Continue Squad", leaderID, testUserID, profile).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	var issueID string
	issueNumber := nextHandlerTestIssueNumber(t)
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, status, assignee_type, assignee_id, number)
		VALUES ($1, 'member', $2, $3, 'in_progress', 'squad', $4, $5)
		RETURNING id
	`, testWorkspaceID, testUserID, "sop auto continue worker completion", squadID, issueNumber).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	if _, err := testHandler.Queries.CreateSquadSOPRun(ctx, db.CreateSquadSOPRunParams{
		WorkspaceID:    util.MustParseUUID(testWorkspaceID),
		IssueID:        util.MustParseUUID(issueID),
		SquadID:        util.MustParseUUID(squadID),
		ProfileKey:     "test-sop",
		Profile:        []byte(profile),
		Status:         "进行中",
		CurrentStepKey: "01-clarify",
	}); err != nil {
		t.Fatalf("create sop run: %v", err)
	}

	insertRunningTask := func(agentID, runtimeID string, isLeader bool) string {
		t.Helper()
		var taskID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at, started_at, is_leader_task)
			VALUES ($1, $2, $3, 'running', 2, now(), now(), $4)
			RETURNING id
		`, agentID, runtimeID, issueID, isLeader).Scan(&taskID); err != nil {
			t.Fatalf("insert running task: %v", err)
		}
		return taskID
	}
	countQueuedLeaders := func() int {
		t.Helper()
		var count int
		if err := testPool.QueryRow(ctx, `
			SELECT count(*) FROM agent_task_queue
			WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued' AND is_leader_task = TRUE
		`, issueID, leaderID).Scan(&count); err != nil {
			t.Fatalf("count queued leaders: %v", err)
		}
		return count
	}

	workerTaskID := insertRunningTask(workerID, workerRuntimeID, false)
	workerOutput := "worker handoff done"
	if _, err := testHandler.TaskService.CompleteTask(ctx, util.MustParseUUID(workerTaskID), []byte(`{"output":"`+workerOutput+`"}`), "", ""); err != nil {
		t.Fatalf("complete worker task: %v", err)
	}
	if got := countQueuedLeaders(); got != 1 {
		t.Fatalf("queued leader tasks after worker completion = %d, want 1", got)
	}
	var synthesizedWorkerComments int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM comment
		WHERE issue_id = $1
		  AND author_id = $2
		  AND source_task_id = $3
		  AND content = $4
	`, issueID, workerID, workerTaskID, workerOutput).Scan(&synthesizedWorkerComments); err != nil {
		t.Fatalf("count synthesized worker comments: %v", err)
	}
	if synthesizedWorkerComments != 1 {
		t.Fatalf("synthesized worker comments = %d, want 1", synthesizedWorkerComments)
	}

	secondWorkerTaskID := insertRunningTask(workerID, workerRuntimeID, false)
	if _, err := testHandler.TaskService.CompleteTask(ctx, util.MustParseUUID(secondWorkerTaskID), []byte(`{}`), "", ""); err != nil {
		t.Fatalf("complete second worker task: %v", err)
	}
	if got := countQueuedLeaders(); got != 1 {
		t.Fatalf("queued leader tasks after duplicate worker completion = %d, want still 1", got)
	}

	leaderTaskID := insertRunningTask(leaderID, leaderRuntimeID, true)
	if _, err := testHandler.TaskService.CompleteTask(ctx, util.MustParseUUID(leaderTaskID), []byte(`{}`), "", ""); err != nil {
		t.Fatalf("complete leader task: %v", err)
	}
	if got := countQueuedLeaders(); got != 1 {
		t.Fatalf("queued leader tasks after leader completion = %d, want still 1", got)
	}

	if _, err := testPool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, issueID); err != nil {
		t.Fatalf("mark issue done: %v", err)
	}
	lateWorkerTaskID := insertRunningTask(workerID, workerRuntimeID, false)
	if _, err := testHandler.TaskService.CompleteTask(ctx, util.MustParseUUID(lateWorkerTaskID), []byte(`{}`), "", ""); err != nil {
		t.Fatalf("complete late worker task after issue done: %v", err)
	}
	if got := countQueuedLeaders(); got != 1 {
		t.Fatalf("queued leader tasks after issue done and late worker completion = %d, want still 1", got)
	}
}

func TestCompleteTask_FinalSOPStepAutoClosesIssueWithoutPullRequest(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	leaderID := createHandlerTestAgent(t, "SOP Final Auto Close Leader", nil)
	verifyID := createHandlerTestSOPAgent(t, "SOP Final Auto Close 05-verify", "05-verify")

	var verifyRuntimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, verifyID).Scan(&verifyRuntimeID); err != nil {
		t.Fatalf("load verify runtime: %v", err)
	}

	profile := `{
		"profile_key":"test-final-sop",
		"steps":[
			{"key":"pm","name":"pm","role_key":"pm"},
			{"key":"05-verify","name":"05-测试","role_key":"05-verify"}
		]
	}`
	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, sop_profile)
		VALUES ($1, $2, '', $3, $4, $5)
		RETURNING id
	`, testWorkspaceID, "SOP Final Auto Close Squad", leaderID, testUserID, profile).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	var issueID string
	issueNumber := nextHandlerTestIssueNumber(t)
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, status, assignee_type, assignee_id, number)
		VALUES ($1, 'member', $2, $3, 'in_progress', 'squad', $4, $5)
		RETURNING id
	`, testWorkspaceID, testUserID, "sop final auto close", squadID, issueNumber).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	if _, err := testHandler.Queries.CreateSquadSOPRun(ctx, db.CreateSquadSOPRunParams{
		WorkspaceID:    util.MustParseUUID(testWorkspaceID),
		IssueID:        util.MustParseUUID(issueID),
		SquadID:        util.MustParseUUID(squadID),
		ProfileKey:     "test-final-sop",
		Profile:        []byte(profile),
		Status:         "进行中",
		CurrentStepKey: "05-verify",
	}); err != nil {
		t.Fatalf("create sop run: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at, started_at)
		VALUES ($1, $2, $3, 'running', 2, now(), now())
		RETURNING id
	`, verifyID, verifyRuntimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("insert verify task: %v", err)
	}
	if _, err := testHandler.TaskService.CompleteTask(ctx, util.MustParseUUID(taskID), []byte(`{}`), "", ""); err != nil {
		t.Fatalf("complete verify task: %v", err)
	}

	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	if issue.Status != "done" {
		t.Fatalf("issue status = %q, want done", issue.Status)
	}
	run, err := testHandler.Queries.GetSquadSOPRunInWorkspace(ctx, db.GetSquadSOPRunInWorkspaceParams{
		ID: func() pgtype.UUID {
			var id string
			_ = testPool.QueryRow(ctx, `SELECT id FROM squad_sop_run WHERE issue_id = $1 ORDER BY created_at DESC LIMIT 1`, issueID).Scan(&id)
			return util.MustParseUUID(id)
		}(),
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load sop run: %v", err)
	}
	if run.Status != "已完成" {
		t.Fatalf("sop run status = %q, want 已完成", run.Status)
	}
}

func TestCompleteTask_FinalSOPStepBlockedOutputDoesNotAutoCloseIssue(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	leaderID := createHandlerTestAgent(t, "SOP Final Blocked Leader", nil)
	verifyID := createHandlerTestSOPAgent(t, "SOP Final Blocked 05-verify", "05-verify")

	var leaderRuntimeID, verifyRuntimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, leaderID).Scan(&leaderRuntimeID); err != nil {
		t.Fatalf("load leader runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, verifyID).Scan(&verifyRuntimeID); err != nil {
		t.Fatalf("load verify runtime: %v", err)
	}

	profile := `{
		"profile_key":"test-final-sop-blocked",
		"steps":[
			{"key":"pm","name":"pm","role_key":"pm"},
			{"key":"05-verify","name":"05-测试","role_key":"05-verify"}
		]
	}`
	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, sop_profile)
		VALUES ($1, $2, '', $3, $4, $5)
		RETURNING id
	`, testWorkspaceID, "SOP Final Blocked Squad", leaderID, testUserID, profile).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	var issueID string
	issueNumber := nextHandlerTestIssueNumber(t)
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, status, assignee_type, assignee_id, number)
		VALUES ($1, 'member', $2, $3, 'in_progress', 'squad', $4, $5)
		RETURNING id
	`, testWorkspaceID, testUserID, "sop final blocked output", squadID, issueNumber).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	var runID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad_sop_run (workspace_id, issue_id, squad_id, profile_key, profile, status, current_step_key)
		VALUES ($1, $2, $3, 'test-final-sop-blocked', $4, '进行中', '05-verify')
		RETURNING id
	`, testWorkspaceID, issueID, squadID, profile).Scan(&runID); err != nil {
		t.Fatalf("create sop run: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at, started_at)
		VALUES ($1, $2, $3, 'running', 2, now(), now())
		RETURNING id
	`, verifyID, verifyRuntimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("insert verify task: %v", err)
	}
	result, err := json.Marshal(map[string]string{
		"output": "# 05-验证测试\n\n**最终判定：V0/V1 通过，V2 BLOCKED（环境缺失）**",
	})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if _, err := testHandler.TaskService.CompleteTask(ctx, util.MustParseUUID(taskID), result, "", ""); err != nil {
		t.Fatalf("complete verify task: %v", err)
	}

	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	if issue.Status != "blocked" {
		t.Fatalf("issue status = %q, want blocked", issue.Status)
	}
	run, err := testHandler.Queries.GetSquadSOPRunInWorkspace(ctx, db.GetSquadSOPRunInWorkspaceParams{
		ID:          util.MustParseUUID(runID),
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load sop run: %v", err)
	}
	if run.Status != "已阻塞" {
		t.Fatalf("sop run status = %q, want 已阻塞", run.Status)
	}
	var queuedLeaderCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND runtime_id = $3 AND status = 'queued' AND is_leader_task = TRUE
	`, issueID, leaderID, leaderRuntimeID).Scan(&queuedLeaderCount); err != nil {
		t.Fatalf("count queued leader tasks: %v", err)
	}
	if queuedLeaderCount != 1 {
		t.Fatalf("queued leader tasks = %d, want 1", queuedLeaderCount)
	}
}

func TestCompleteTask_FinalSOPStepBlocksGongfengIssueWithoutPullRequestAndComments(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	leaderID := createHandlerTestAgent(t, "SOP Final MR Gate Leader", nil)
	verifyID := createHandlerTestSOPAgent(t, "SOP Final MR Gate 05-verify", "05-verify")

	var verifyRuntimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, verifyID).Scan(&verifyRuntimeID); err != nil {
		t.Fatalf("load verify runtime: %v", err)
	}

	project, err := testHandler.Queries.CreateProject(ctx, db.CreateProjectParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		Title:       "SOP Final MR Gate Gongfeng Project",
		Status:      "in_progress",
		Priority:    "medium",
		Scope:       "workspace",
		OwnerID:     util.MustParseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, project.ID)
	})
	if _, err := testHandler.Queries.CreateProjectResource(ctx, db.CreateProjectResourceParams{
		ProjectID:    project.ID,
		WorkspaceID:  util.MustParseUUID(testWorkspaceID),
		ResourceType: "gongfeng_repo",
		ResourceRef:  []byte(`{"project_path":"ChainWeaver/ida/gateway","repo_url":"https://git.code.tencent.com/ChainWeaver/ida/gateway"}`),
		Label:        pgtype.Text{String: "gateway", Valid: true},
		CreatedBy:    util.MustParseUUID(testUserID),
	}); err != nil {
		t.Fatalf("create project resource: %v", err)
	}

	profile := `{
		"profile_key":"test-final-sop-mr-gate",
		"steps":[
			{"key":"pm","name":"pm","role_key":"pm"},
			{"key":"05-verify","name":"05-测试","role_key":"05-verify"}
		]
	}`
	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, sop_profile)
		VALUES ($1, $2, '', $3, $4, $5)
		RETURNING id
	`, testWorkspaceID, "SOP Final MR Gate Squad", leaderID, testUserID, profile).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	var issueID string
	issueNumber := nextHandlerTestIssueNumber(t)
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, status, project_id, assignee_type, assignee_id, number)
		VALUES ($1, 'member', $2, $3, 'in_progress', $4, 'squad', $5, $6)
		RETURNING id
	`, testWorkspaceID, testUserID, "sop final blocks without gongfeng MR", project.ID, squadID, issueNumber).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	if _, err := testHandler.Queries.CreateSquadSOPRun(ctx, db.CreateSquadSOPRunParams{
		WorkspaceID:    util.MustParseUUID(testWorkspaceID),
		IssueID:        util.MustParseUUID(issueID),
		SquadID:        util.MustParseUUID(squadID),
		ProfileKey:     "test-final-sop-mr-gate",
		Profile:        []byte(profile),
		Status:         "进行中",
		CurrentStepKey: "05-verify",
	}); err != nil {
		t.Fatalf("create sop run: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at, started_at)
		VALUES ($1, $2, $3, 'running', 2, now(), now())
		RETURNING id
	`, verifyID, verifyRuntimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("insert verify task: %v", err)
	}
	if _, err := testHandler.TaskService.CompleteTask(ctx, util.MustParseUUID(taskID), []byte(`{}`), "", ""); err != nil {
		t.Fatalf("complete verify task: %v", err)
	}

	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	if issue.Status != "blocked" {
		t.Fatalf("issue status = %q, want blocked", issue.Status)
	}

	var authorID, content string
	if err := testPool.QueryRow(ctx, `
		SELECT author_id::text, content
		FROM comment
		WHERE issue_id = $1 AND author_type = 'system'
		ORDER BY created_at DESC
		LIMIT 1
	`, issueID).Scan(&authorID, &content); err != nil {
		t.Fatalf("load missing MR gate comment: %v", err)
	}
	if authorID != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("system comment author_id = %q, want zero UUID", authorID)
	}
	if !strings.Contains(content, "平台还没有关联 MR") || !strings.Contains(content, "multica issue mr create") {
		t.Fatalf("missing MR gate comment did not explain recovery: %s", content)
	}
}

func TestCompleteTask_FinalSOPStepClosesIssueAlreadyInReview(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	leaderID := createHandlerTestAgent(t, "SOP Final InReview Leader", nil)
	verifyID := createHandlerTestSOPAgent(t, "SOP Final InReview 05-verify", "05-verify")

	var verifyRuntimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, verifyID).Scan(&verifyRuntimeID); err != nil {
		t.Fatalf("load verify runtime: %v", err)
	}

	profile := `{
		"profile_key":"test-final-sop-in-review",
		"steps":[
			{"key":"pm","name":"pm","role_key":"pm"},
			{"key":"05-verify","name":"05-测试","role_key":"05-verify"}
		]
	}`
	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, sop_profile)
		VALUES ($1, $2, '', $3, $4, $5)
		RETURNING id
	`, testWorkspaceID, "SOP Final InReview Squad", leaderID, testUserID, profile).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	var issueID string
	issueNumber := nextHandlerTestIssueNumber(t)
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, status, assignee_type, assignee_id, number)
		VALUES ($1, 'member', $2, $3, 'in_review', 'squad', $4, $5)
		RETURNING id
	`, testWorkspaceID, testUserID, "sop final auto close from in_review", squadID, issueNumber).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	if _, err := testHandler.Queries.CreateSquadSOPRun(ctx, db.CreateSquadSOPRunParams{
		WorkspaceID:    util.MustParseUUID(testWorkspaceID),
		IssueID:        util.MustParseUUID(issueID),
		SquadID:        util.MustParseUUID(squadID),
		ProfileKey:     "test-final-sop-in-review",
		Profile:        []byte(profile),
		Status:         "进行中",
		CurrentStepKey: "05-verify",
	}); err != nil {
		t.Fatalf("create sop run: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at, started_at)
		VALUES ($1, $2, $3, 'running', 2, now(), now())
		RETURNING id
	`, verifyID, verifyRuntimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("insert verify task: %v", err)
	}
	if _, err := testHandler.TaskService.CompleteTask(ctx, util.MustParseUUID(taskID), []byte(`{}`), "", ""); err != nil {
		t.Fatalf("complete verify task: %v", err)
	}

	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	if issue.Status != "done" {
		t.Fatalf("issue status = %q, want done", issue.Status)
	}
}

func TestCompleteTask_AutoClosedChildIssueWakesParentSquad(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	sq := newSquadCommentTriggerFixture(t)
	verifyID := createHandlerTestSOPAgent(t, "SOP Child Auto Close 05-verify", "05-verify")

	var verifyRuntimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, verifyID).Scan(&verifyRuntimeID); err != nil {
		t.Fatalf("load verify runtime: %v", err)
	}

	project, err := testHandler.Queries.CreateProject(ctx, db.CreateProjectParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		Title:       "SOP Child Auto Close Gongfeng Project",
		Status:      "in_progress",
		Priority:    "medium",
		Scope:       "workspace",
		OwnerID:     util.MustParseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, project.ID)
	})
	if _, err := testHandler.Queries.CreateProjectResource(ctx, db.CreateProjectResourceParams{
		ProjectID:    project.ID,
		WorkspaceID:  util.MustParseUUID(testWorkspaceID),
		ResourceType: "gongfeng_repo",
		ResourceRef:  []byte(`{"project_path":"ChainWeaver/ida/gateway","repo_url":"https://git.code.tencent.com/ChainWeaver/ida/gateway"}`),
		Label:        pgtype.Text{String: "gateway", Valid: true},
		CreatedBy:    util.MustParseUUID(testUserID),
	}); err != nil {
		t.Fatalf("create project resource: %v", err)
	}

	var parentID, childID string
	parentNumber := nextHandlerTestIssueNumber(t)
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, status, assignee_type, assignee_id, number)
		VALUES ($1, 'member', $2, $3, 'in_progress', 'squad', $4, $5)
		RETURNING id
	`, testWorkspaceID, testUserID, "sop parent waits for auto-closed child", sq.SquadID, parentNumber).Scan(&parentID); err != nil {
		t.Fatalf("create parent issue: %v", err)
	}
	childNumber := nextHandlerTestIssueNumber(t)
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, status, parent_issue_id, project_id, assignee_type, assignee_id, number)
		VALUES ($1, 'member', $2, $3, 'in_progress', $4, $5, 'squad', $6, $7)
		RETURNING id
	`, testWorkspaceID, testUserID, "sop child auto closes with MR", parentID, project.ID, sq.SquadID, childNumber).Scan(&childID); err != nil {
		t.Fatalf("create child issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id IN ($1, $2)`, parentID, childID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id IN ($1, $2)`, childID, parentID)
	})

	now := time.Now()
	pr, err := testHandler.Queries.UpsertGitHubPullRequest(ctx, db.UpsertGitHubPullRequestParams{
		WorkspaceID:    util.MustParseUUID(testWorkspaceID),
		InstallationID: 1,
		RepoOwner:      "ChainWeaver/ida",
		RepoName:       "gateway",
		PrNumber:       int32(now.UnixNano() % 1000000),
		Title:          "SOP child auto close linked MR",
		State:          "open",
		HtmlUrl:        "https://git.code.tencent.com/ChainWeaver/ida/gateway/merge_requests/999999",
		PrCreatedAt:    pgtype.Timestamptz{Time: now, Valid: true},
		PrUpdatedAt:    pgtype.Timestamptz{Time: now, Valid: true},
		HeadSha:        "abc123",
		Additions:      1,
		ChangedFiles:   1,
		Branch:         pgtype.Text{String: "goal-test/child-auto-close", Valid: true},
	})
	if err != nil {
		t.Fatalf("upsert PR: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM github_pull_request WHERE id = $1`, pr.ID)
	})
	if err := testHandler.Queries.LinkIssueToPullRequest(ctx, db.LinkIssueToPullRequestParams{
		IssueID:             util.MustParseUUID(childID),
		PullRequestID:       pr.ID,
		CloseIntent:         true,
		LinkedByType:        pgtype.Text{String: "agent", Valid: true},
		LinkedByID:          util.MustParseUUID(verifyID),
		PreserveCloseIntent: false,
	}); err != nil {
		t.Fatalf("link PR to child issue: %v", err)
	}

	profile := `{
		"profile_key":"test-child-auto-close",
		"steps":[
			{"key":"pm","name":"pm","role_key":"pm"},
			{"key":"05-verify","name":"05-测试","role_key":"05-verify"}
		]
	}`
	if _, err := testHandler.Queries.CreateSquadSOPRun(ctx, db.CreateSquadSOPRunParams{
		WorkspaceID:    util.MustParseUUID(testWorkspaceID),
		IssueID:        util.MustParseUUID(childID),
		SquadID:        util.MustParseUUID(sq.SquadID),
		ProfileKey:     "test-child-auto-close",
		Profile:        []byte(profile),
		Status:         "进行中",
		CurrentStepKey: "05-verify",
	}); err != nil {
		t.Fatalf("create child SOP run: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at, started_at)
		VALUES ($1, $2, $3, 'running', 2, now(), now())
		RETURNING id
	`, verifyID, verifyRuntimeID, childID).Scan(&taskID); err != nil {
		t.Fatalf("insert verify task: %v", err)
	}
	if _, err := testHandler.TaskService.CompleteTask(ctx, util.MustParseUUID(taskID), []byte(`{}`), "", ""); err != nil {
		t.Fatalf("complete verify task: %v", err)
	}

	child, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(childID))
	if err != nil {
		t.Fatalf("load child issue: %v", err)
	}
	if child.Status != "done" {
		t.Fatalf("child status = %q, want done", child.Status)
	}
	content := parentSystemCommentContent(t, parentID)
	if !strings.Contains(content, "mention://squad/"+sq.SquadID) {
		t.Fatalf("parent child-done comment should mention parent squad, got: %s", content)
	}
	if got := countPendingTasksForAgent(t, parentID, sq.LeaderID); got != 1 {
		t.Fatalf("pending parent leader tasks = %d, want 1", got)
	}
}

// TestCreateComment_SquadLeaderSkipOnlyInspectsCurrentMention drives the
// full CreateComment handler to lock the call-site wiring (comment.go) for
// the squad-leader-skip rule. Specifically it proves that:
//
//   - A member top-level comment that @mentions another agent does NOT
//     enqueue the squad leader (the mentioned agent owns the next step).
//   - A subsequent member REPLY in the same thread, containing no mentions
//     of its own, DOES enqueue the squad leader — i.e. the parent's
//     @agent mention is not inherited into the leader-skip decision.
//
// The matching unit test above exercises the helper in isolation; this
// test catches a class of regression where someone refactors comment.go
// to pass the parent's content (or the merged thread content) by mistake.
func TestCreateComment_SquadLeaderSkipOnlyInspectsCurrentMention(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	fx := newSquadCommentTriggerFixture(t)
	issueID := uuidToString(fx.Issue.ID)

	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
	})

	countQueued := func(agentID string) int {
		var n int
		if err := testPool.QueryRow(ctx,
			`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`,
			issueID, agentID,
		).Scan(&n); err != nil {
			t.Fatalf("count tasks for %s: %v", agentID, err)
		}
		return n
	}

	postMemberComment := func(body map[string]any) CommentResponse {
		t.Helper()
		w := httptest.NewRecorder()
		r := newRequest("POST", "/api/issues/"+issueID+"/comments", body)
		r = withURLParam(r, "id", issueID)
		testHandler.CreateComment(w, r)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var resp CommentResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode comment: %v", err)
		}
		return resp
	}

	// 1. Member top-level comment mentions OtherAgent.
	//    Leader must be skipped; OtherAgent must be enqueued via the mention path.
	parent := postMemberComment(map[string]any{
		"content": "[@Other](mention://agent/" + fx.OtherID + ") please take this",
	})
	if got := countQueued(fx.LeaderID); got != 0 {
		t.Fatalf("after parent (@OtherAgent): expected 0 leader tasks (skipped), got %d", got)
	}
	if got := countQueued(fx.OtherID); got != 1 {
		t.Fatalf("after parent (@OtherAgent): expected 1 OtherAgent task (mention path), got %d", got)
	}

	// 2. Member posts a reply in the same thread with NO mentions.
	//    The leader-skip helper must inspect only the reply's body (empty),
	//    NOT the parent's @OtherAgent mention. Leader must wake up.
	postMemberComment(map[string]any{
		"content":   "any update?",
		"parent_id": parent.ID,
	})
	if got := countQueued(fx.LeaderID); got != 1 {
		t.Fatalf("after plain reply: expected 1 leader task (no parent inheritance), got %d", got)
	}
}

// TestCreateComment_ActiveWorkerCommentDoesNotWakeLeader is the full-stack
// regression test for comment-trigger concurrency. Scenario:
//
//   - Agent L is the leader of squad S and also a worker assigned tasks on
//     issues belonging to S.
//   - L is still running in its worker role (is_leader_task=false) and posts a
//     progress/result comment.
//   - The comment MUST NOT enqueue a concurrent leader task while the worker
//     task is still active. The task completion or explicit handoff owns the
//     next wakeup.
func TestCreateComment_ActiveWorkerCommentDoesNotWakeLeader(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	fx := newSquadCommentTriggerFixture(t)
	issueID := uuidToString(fx.Issue.ID)

	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
	})

	// Seed a worker task for the leader agent on this issue so the guard
	// infers "agent's last activity was a worker task" — i.e. L is running
	// in its worker role when it posts the comment. We make it running (not
	// completed) so we can hand its ID back through X-Task-ID for the
	// resolveActor agent-identity check.
	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, fx.LeaderID).Scan(&runtimeID); err != nil {
		t.Fatalf("load runtime: %v", err)
	}
	var workerTaskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, is_leader_task)
		VALUES ($1, $2, $3, 'running', FALSE)
		RETURNING id
	`, fx.LeaderID, runtimeID, issueID).Scan(&workerTaskID); err != nil {
		t.Fatalf("seed worker task: %v", err)
	}

	// L posts a comment in its agent identity (X-Agent-ID + X-Task-ID, the
	// pair required by resolveActor to trust the agent header).
	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "done — pushed the change",
	})
	r.Header.Set("X-Agent-ID", fx.LeaderID)
	r.Header.Set("X-Task-ID", workerTaskID)
	r = withURLParam(r, "id", issueID)
	testHandler.CreateComment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// No leader-role task should be enqueued while the worker task is still
	// active. Otherwise a normal result comment can race the current task's
	// completion and create two simultaneous executions on the same issue.
	var leaderTasks int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued' AND is_leader_task = TRUE
	`, issueID, fx.LeaderID).Scan(&leaderTasks); err != nil {
		t.Fatalf("count leader tasks: %v", err)
	}
	if leaderTasks != 0 {
		t.Fatalf("after active worker comment from dual-role agent: expected 0 queued leader tasks, got %d", leaderTasks)
	}
}

type crossProjectGateSOPFixture struct {
	Issue       db.Issue
	IssueID     string
	LeaderID    string
	ImplementID string
	SquadID     string
}

func newCrossProjectGateSOPFixture(t *testing.T, childDone bool) crossProjectGateSOPFixture {
	t.Helper()
	ctx := context.Background()
	leaderID := createHandlerTestAgent(t, "Cross Project Gate PM "+randomID()[:8], nil)
	implementID := createHandlerTestSOPAgent(t, "Cross Project Gate 04 "+randomID()[:8], "04-implement")
	profile := `{
		"mode":"stage_chain",
		"steps":[
			{"key":"pm","role_key":"pm"},
			{"key":"01","role_key":"01-clarify"},
			{"key":"02","role_key":"02-design"},
			{"key":"03","role_key":"03-task-split"},
			{"key":"04","role_key":"04-implement"},
			{"key":"05","role_key":"05-verify"}
		]
	}`
	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, sop_profile)
		VALUES ($1, $2, '', $3, $4, $5)
		RETURNING id
	`, testWorkspaceID, "Cross Project Gate Squad "+randomID()[:8], leaderID, testUserID, profile).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})
	if _, err := testPool.Exec(ctx, `
		INSERT INTO squad_member (squad_id, member_type, member_id, role)
		VALUES ($1, 'agent', $2, '04')
	`, squadID, implementID); err != nil {
		t.Fatalf("create squad member: %v", err)
	}

	var issueID string
	issueNumber := nextHandlerTestIssueNumber(t)
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, status, assignee_type, assignee_id, number)
		VALUES ($1, 'member', $2, $3, 'in_progress', 'squad', $4, $5)
		RETURNING id
	`, testWorkspaceID, testUserID, "cross-project parent gate "+randomID()[:8], squadID, issueNumber).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE parent_issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})
	if childDone {
		childNumber := nextHandlerTestIssueNumber(t)
		if _, err := testPool.Exec(ctx, `
			INSERT INTO issue (workspace_id, creator_type, creator_id, title, status, parent_issue_id, number)
			VALUES ($1, 'member', $2, 'ida-deployment child', 'done', $3, $4)
		`, testWorkspaceID, testUserID, issueID, childNumber); err != nil {
			t.Fatalf("create done child: %v", err)
		}
	}
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content)
		VALUES ($1, $2, 'agent', $3, $4)
	`, testWorkspaceID, issueID, leaderID, "## 03-task-split\n\nrequired cross-project dependencies:\n- ida-deployment: required，待 PM 创建 child issue"); err != nil {
		t.Fatalf("insert 03 comment: %v", err)
	}
	return crossProjectGateSOPFixture{
		Issue:       issue,
		IssueID:     issueID,
		LeaderID:    leaderID,
		ImplementID: implementID,
		SquadID:     squadID,
	}
}

func insertSOPPMMentionComment(t *testing.T, fx crossProjectGateSOPFixture) db.Comment {
	t.Helper()
	ctx := context.Background()
	content := "03 通过，请进入 [@04-开发](mention://agent/" + fx.ImplementID + ")"
	var commentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content)
		VALUES ($1, $2, 'agent', $3, $4)
		RETURNING id
	`, testWorkspaceID, fx.IssueID, fx.LeaderID, content).Scan(&commentID); err != nil {
		t.Fatalf("insert pm comment: %v", err)
	}
	return db.Comment{
		ID:          util.MustParseUUID(commentID),
		IssueID:     util.MustParseUUID(fx.IssueID),
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		AuthorType:  "agent",
		AuthorID:    util.MustParseUUID(fx.LeaderID),
		Content:     content,
	}
}

func TestEnqueueCommentAgentTriggers_BlocksParentSOPStageWhenRequiredChildrenMissing(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	fx := newCrossProjectGateSOPFixture(t, false)
	comment := insertSOPPMMentionComment(t, fx)

	enqueueMentionedAgentTasksForTest(t, ctx, fx.Issue, comment, nil, "agent", fx.LeaderID)

	if got := countQueuedOrDispatched(t, fx.ImplementID, fx.IssueID); got != 0 {
		t.Fatalf("parent 04 task count = %d, want 0 while required child is missing", got)
	}
	var gateComments int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM comment
		WHERE issue_id = $1 AND author_type = 'system' AND content LIKE '%平台已阻止父任务阶段调度%'
	`, fx.IssueID).Scan(&gateComments); err != nil {
		t.Fatalf("count gate comments: %v", err)
	}
	if gateComments != 1 {
		t.Fatalf("gate comment count = %d, want 1", gateComments)
	}
}

func TestEnqueueCommentAgentTriggers_AllowsParentSOPStageWhenRequiredChildrenDone(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	fx := newCrossProjectGateSOPFixture(t, true)
	comment := insertSOPPMMentionComment(t, fx)

	enqueueMentionedAgentTasksForTest(t, ctx, fx.Issue, comment, nil, "agent", fx.LeaderID)

	if got := countQueuedOrDispatched(t, fx.ImplementID, fx.IssueID); got != 1 {
		t.Fatalf("parent 04 task count = %d, want 1 after required child is done", got)
	}
}

func TestEnqueueCommentAgentTriggers_AllowsParentSOPStageWhenLatestTaskSplitHasNoCrossProjectDependency(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	fx := newCrossProjectGateSOPFixture(t, false)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content)
		VALUES ($1, $2, 'system', $3, $4)
	`, testWorkspaceID, fx.IssueID, fx.LeaderID, "平台已阻止父任务阶段调度：03-任务拆分已识别 required 跨项目依赖，但父 issue 的 child issue 仍缺失或未全部完成。"); err != nil {
		t.Fatalf("insert stale gate comment: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content)
		VALUES ($1, $2, 'agent', $3, $4)
	`, testWorkspaceID, fx.IssueID, fx.LeaderID, "## 03-task-split\n\nrequired cross-project dependencies: none\n\nnot required projects:\n- gateway: 不需要\n- ida-deployment: 不需要\n\n结论：无跨项目依赖，无 child issue。"); err != nil {
		t.Fatalf("insert no-dependency 03 comment: %v", err)
	}
	comment := insertSOPPMMentionComment(t, fx)

	enqueueMentionedAgentTasksForTest(t, ctx, fx.Issue, comment, nil, "agent", fx.LeaderID)

	if got := countQueuedOrDispatched(t, fx.ImplementID, fx.IssueID); got != 1 {
		t.Fatalf("parent 04 task count = %d, want 1 when latest 03 has no cross-project dependency", got)
	}
}

func TestEnqueueCommentAgentTriggers_AllowsCrossProjectChildWithoutFurtherChildren(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	fx := newCrossProjectGateSOPFixture(t, false)

	var parentID string
	parentNumber := nextHandlerTestIssueNumber(t)
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, status, number)
		VALUES ($1, 'member', $2, 'parent for cross-project child', 'in_progress', $3)
		RETURNING id
	`, testWorkspaceID, testUserID, parentNumber).Scan(&parentID); err != nil {
		t.Fatalf("create parent issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, parentID)
	})
	if _, err := testPool.Exec(ctx, `
		UPDATE issue SET parent_issue_id = $1 WHERE id = $2
	`, parentID, fx.IssueID); err != nil {
		t.Fatalf("mark issue as child: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content)
		VALUES ($1, $2, 'agent', $3, $4)
	`, testWorkspaceID, fx.IssueID, fx.LeaderID, "本 issue 是父 issue 的跨项目 child。\n\n03-任务拆分已闭环。无跨项目 child issue 需创建（仅目标项目自身变更）。"); err != nil {
		t.Fatalf("insert child context comment: %v", err)
	}
	comment := insertSOPPMMentionComment(t, fx)

	enqueueMentionedAgentTasksForTest(t, ctx, fx.Issue, comment, nil, "agent", fx.LeaderID)

	if got := countQueuedOrDispatched(t, fx.ImplementID, fx.IssueID); got != 1 {
		t.Fatalf("child 04 task count = %d, want 1 when no further child is required", got)
	}
	var gateComments int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM comment
		WHERE issue_id = $1 AND author_type = 'system' AND content LIKE '%平台已阻止父任务阶段调度%'
	`, fx.IssueID).Scan(&gateComments); err != nil {
		t.Fatalf("count gate comments: %v", err)
	}
	if gateComments != 0 {
		t.Fatalf("gate comment count = %d, want 0", gateComments)
	}
}

// TestCreateRetryTask_InheritsIsLeaderTask locks the retry-clone contract for
// MUL-2218: auto-retry of a leader-role task must produce a child task that is
// also is_leader_task=true. Without this, MaybeRetryFailedTask silently
// demotes a retried leader task to a worker task, and the self-trigger guard
// in computeAssignedSquadLeaderCommentTrigger / comment.go stops recognising the
// retried leader's own comments — re-opening the bug this issue fixes.
func TestCreateRetryTask_InheritsIsLeaderTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	fx := newSquadCommentTriggerFixture(t)
	issueID := uuidToString(fx.Issue.ID)

	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	})

	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, fx.LeaderID).Scan(&runtimeID); err != nil {
		t.Fatalf("load runtime: %v", err)
	}

	cases := []struct {
		name     string
		isLeader bool
	}{
		{name: "leader task retry stays leader", isLeader: true},
		{name: "worker task retry stays worker", isLeader: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var parentID string
			if err := testPool.QueryRow(ctx, `
				INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, attempt, max_attempts, is_leader_task)
				VALUES ($1, $2, $3, 'failed', 1, 3, $4)
				RETURNING id
			`, fx.LeaderID, runtimeID, issueID, tc.isLeader).Scan(&parentID); err != nil {
				t.Fatalf("seed parent task: %v", err)
			}
			t.Cleanup(func() {
				testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1 OR parent_task_id = $1`, parentID)
			})

			child, err := testHandler.Queries.CreateRetryTask(ctx, util.MustParseUUID(parentID))
			if err != nil {
				t.Fatalf("CreateRetryTask: %v", err)
			}
			if child.IsLeaderTask != tc.isLeader {
				t.Fatalf("child.IsLeaderTask = %v, want %v (parent role must be inherited)", child.IsLeaderTask, tc.isLeader)
			}
		})
	}
}

// TestCreateComment_SquadMentionPrivateLeaderBlocksPlainMember verifies that
// a plain workspace member cannot trigger a private squad leader via @squad
// mention. This is the regression test for the P1 finding: without the
// canAccessPersonalAgent gate in the squad mention branch, a member could
// bypass the personal-agent restriction by mentioning the squad instead of
// the agent directly.
func TestCreateComment_SquadMentionPrivateLeaderBlocksPlainMember(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	// Use personalAgentTestFixture to get a personal agent + plain member.
	agentID, _, memberID := personalAgentTestFixture(t)

	// Create a squad with the personal agent as leader.
	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, 'Private Leader Squad', '', $2, $3)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	// Create an issue.
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title)
		VALUES ($1, 'member', $2, 'personal leader squad mention test')
		RETURNING id
	`, testWorkspaceID, memberID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	// Plain member posts a comment mentioning the squad.
	w := httptest.NewRecorder()
	r := newRequestAs(memberID, "POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "[@Squad](mention://squad/" + squadID + ") please handle",
	})
	r = withURLParam(r, "id", issueID)
	testHandler.CreateComment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// The personal leader must NOT have a queued task — plain member lacks access.
	var count int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`,
		issueID, agentID,
	).Scan(&count); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if count != 0 {
		t.Fatalf("personal leader got %d queued tasks from plain member squad mention; want 0 (access denied)", count)
	}
}

// TestCreateComment_SquadMentionTriggersLeader verifies that @mentioning a
// squad in a comment triggers the squad's leader agent via the mention path,
// even when the issue is NOT assigned to that squad.
func TestCreateComment_SquadMentionTriggersLeader(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	// Create a squad with a leader agent.
	var leaderID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM agent WHERE workspace_id = $1 ORDER BY created_at ASC LIMIT 1
	`, testWorkspaceID).Scan(&leaderID); err != nil {
		t.Fatalf("load leader agent: %v", err)
	}

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, $2, '', $3, $4)
		RETURNING id
	`, testWorkspaceID, "Mention Trigger Squad", leaderID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	// Create an issue NOT assigned to the squad (assigned to nobody).
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title)
		VALUES ($1, 'member', $2, 'squad mention trigger test')
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	countQueued := func(agentID string) int {
		var n int
		if err := testPool.QueryRow(ctx,
			`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`,
			issueID, agentID,
		).Scan(&n); err != nil {
			t.Fatalf("count tasks for %s: %v", agentID, err)
		}
		return n
	}

	// Post a comment that @mentions the squad.
	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "[@Squad](mention://squad/" + squadID + ") please handle this",
	})
	r = withURLParam(r, "id", issueID)
	testHandler.CreateComment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// The squad's leader should have a queued task.
	if got := countQueued(leaderID); got != 1 {
		t.Fatalf("after @squad mention: expected 1 leader task, got %d", got)
	}
}

func TestCreateComment_MentionAssignedSquadLeaderCreatesLeaderRoleTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	leaderID := createHandlerTestAgent(t, "Assigned Squad Mention Leader", nil)
	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, $2, '', $3, $4)
		RETURNING id
	`, testWorkspaceID, "Assigned Leader Mention Squad "+randomID()[:8], leaderID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, assignee_type, assignee_id)
		VALUES ($1, $2, 'todo', 'member', $3, 'squad', $4)
		RETURNING id
	`, testWorkspaceID, "assigned squad leader mention dedupe "+randomID()[:8], testUserID, squadID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "[@Leader](mention://agent/" + leaderID + ") please close this",
	})
	r = withURLParam(r, "id", issueID)
	testHandler.CreateComment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var taskCount, leaderTaskCount, mentionTaskCount int
	if err := testPool.QueryRow(ctx, `
		SELECT
			count(*)::int,
			count(*) FILTER (WHERE is_leader_task IS TRUE)::int,
			count(*) FILTER (WHERE COALESCE(is_leader_task, false) IS FALSE)::int
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
	`, issueID, leaderID).Scan(&taskCount, &leaderTaskCount, &mentionTaskCount); err != nil {
		t.Fatalf("count leader mention tasks: %v", err)
	}
	if taskCount != 1 || leaderTaskCount != 1 || mentionTaskCount != 0 {
		t.Fatalf("after @leader on assigned squad issue: task=%d leader=%d mention=%d, want 1/1/0", taskCount, leaderTaskCount, mentionTaskCount)
	}
}
