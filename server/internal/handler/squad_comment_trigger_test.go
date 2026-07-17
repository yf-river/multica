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

func shouldEnqueueSquadLeaderOnCommentForTest(ctx context.Context, issue db.Issue, content, authorType, authorID string) bool {
	_, ok, err := testHandler.computeAssignedSquadLeaderCommentTrigger(ctx, issue, content, authorType, authorID, commentTriggerComputeOptions{})
	if err != nil {
		return false
	}
	return ok
}

func squadAgentRuntimeID(t *testing.T, agentID string) string {
	t.Helper()
	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("load Agent runtime: %v", err)
	}
	return runtimeID
}

func cleanupSquadCommentIssue(t *testing.T, issueID string) {
	t.Helper()
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		mustExec(t, context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})
}

func TestAssignedSquadCommentTriggerPreservesSquadLookupFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	issue := db.Issue{
		WorkspaceID:  util.MustParseUUID(testWorkspaceID),
		AssigneeType: pgtype.Text{String: "squad", Valid: true},
		AssigneeID:   util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
	}

	_, ok, err := testHandler.computeAssignedSquadLeaderCommentTrigger(ctx, issue, "continue", "member", testUserID, commentTriggerComputeOptions{})
	if ok || err == nil {
		t.Fatalf("computeAssignedSquadLeaderCommentTrigger() ok=%t err=%v, want false with squad lookup error", ok, err)
	}
}

type squadCommentTriggerFixture struct {
	Issue    db.Issue
	SquadID  string
	LeaderID string
	OtherID  string
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

	squadID := createHandlerTestSquad(t, "Squad Comment Trigger", leaderID)

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
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
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
	requireHandlerDatabase(t)
	ctx := context.Background()
	fx := newSquadCommentTriggerFixture(t)

	task, err := testHandler.TaskService.EnqueueTaskForSquadLeader(ctx, fx.Issue, util.MustParseUUID(fx.LeaderID), pgtype.UUID{})
	if err != nil {
		t.Fatalf("EnqueueTaskForSquadLeader: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, task.ID)
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
	requireHandlerDatabase(t)
	ctx := context.Background()
	_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1 AND name IN ($2, $3))`, testWorkspaceID, projectSOPAgentPM, projectSOPAgent01)
	_, _ = testPool.Exec(ctx, `DELETE FROM squad_member WHERE member_id IN (SELECT id FROM agent WHERE workspace_id = $1 AND name IN ($2, $3))`, testWorkspaceID, projectSOPAgentPM, projectSOPAgent01)
	_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND name IN ($2, $3)`, testWorkspaceID, projectSOPAgentPM, projectSOPAgent01)

	pmID := createHandlerTestSOPAgent(t, projectSOPAgentPM, "pm")
	clarifyID := createHandlerTestSOPAgent(t, projectSOPAgent01, "01-clarify")

	squadID := createHandlerTestSquad(t, "SOP Role Key Mention Squad", pmID)
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
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	w, _ := (handlerCommentIssueFixture{ID: issueID}).postComment(t, map[string]any{
		"content": "## PM 调度\n\n请 **01-需求澄清** (@01-clarify) 开始澄清。",
	}, map[string]string{
		"X-Actor-Source": "task_token",
		"X-Agent-ID":     pmID,
	})
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

func TestShouldEnqueueSquadLeaderOnComment_SkipsWhenMemberMentionsAnyone(t *testing.T) {
	requireHandlerDatabase(t)
	fx := newSquadCommentTriggerFixture(t)
	ctx := context.Background()

	cases := []struct {
		name       string
		content    string
		authorType string
		authorID   string
		want       bool
	}{
		{
			name:       "member plain comment triggers leader",
			content:    "what is the latest on this?",
			authorType: "member",
			authorID:   testUserID,
			want:       true,
		},
		{
			name:       "member issue cross-reference only triggers leader",
			content:    "blocked by [MUL-1](mention://issue/" + testUserID + ")",
			authorType: "member",
			authorID:   testUserID,
			want:       true,
		},
		{
			name:       "member mentions another member skips leader",
			content:    "[@self](mention://member/" + testUserID + ") please weigh in",
			authorType: "member",
			authorID:   testUserID,
			want:       false,
		},
		{
			name:       "member mentions non-leader agent skips leader",
			content:    "[@Other](mention://agent/" + fx.OtherID + ") please take this",
			authorType: "member",
			authorID:   testUserID,
			want:       false,
		},
		{
			name:       "member mentions leader skips leader on comment path",
			content:    "[@Leader](mention://agent/" + fx.LeaderID + ") your call",
			authorType: "member",
			authorID:   testUserID,
			want:       false,
		},
		{
			name:       "member mention all skips leader",
			content:    "[@all](mention://all/all) heads up",
			authorType: "member",
			authorID:   testUserID,
			want:       false,
		},
		{
			name:       "member mentions a squad skips leader",
			content:    "handing to [@Other Squad](mention://squad/" + fx.SquadID + ")",
			authorType: "member",
			authorID:   testUserID,
			want:       false,
		},
		{
			name:       "agent comment with @agent still triggers leader",
			content:    "delegating to [@Other](mention://agent/" + fx.OtherID + ")",
			authorType: "agent",
			authorID:   fx.OtherID,
			want:       true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldEnqueueSquadLeaderOnCommentForTest(ctx, fx.Issue, tc.content, tc.authorType, tc.authorID)
			if got != tc.want {
				t.Fatalf("content=%q author=%s/%s: got=%v want=%v", tc.content, tc.authorType, tc.authorID, got, tc.want)
			}
		})
	}
}

func TestShouldEnqueueSquadLeaderOnComment_LeaderSelfTriggerByRole(t *testing.T) {
	requireHandlerDatabase(t)
	fx := newSquadCommentTriggerFixture(t)
	ctx := context.Background()
	issueID := uuidToString(fx.Issue.ID)

	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	})

	clearTasks := func() {
		if _, err := testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID); err != nil {
			t.Fatalf("clear tasks: %v", err)
		}
	}
	runtimeID := squadAgentRuntimeID(t, fx.LeaderID)
	insertTask := func(isLeader bool) {
		t.Helper()
		if _, err := testPool.Exec(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, is_leader_task)
			VALUES ($1, $2, $3, 'completed', $4)
		`, fx.LeaderID, runtimeID, issueID, isLeader); err != nil {
			t.Fatalf("insert task: %v", err)
		}
	}
	tests := []struct {
		name       string
		priorRoles []bool
		want       bool
	}{
		{name: "no prior task wakes leader", want: true},
		{name: "prior leader task suppresses self-trigger", priorRoles: []bool{true}},
		{name: "prior worker task wakes leader", priorRoles: []bool{false}, want: true},
		{name: "most recent worker task wins", priorRoles: []bool{true, false}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearTasks()
			for _, isLeader := range test.priorRoles {
				insertTask(isLeader)
			}
			if got := shouldEnqueueSquadLeaderOnCommentForTest(ctx, fx.Issue, "result", "agent", fx.LeaderID); got != test.want {
				t.Fatalf("enqueue = %t, want %t", got, test.want)
			}
		})
	}
}

func TestCompleteTask_WorkerStageCompletionEnqueuesSquadLeader(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	leaderID := createHandlerTestAgent(t, "SOP Auto Continue Leader", nil)
	workerID := createHandlerTestSOPAgent(t, "SOP Worker Stage 01-clarify", "01-clarify")

	leaderRuntimeID := squadAgentRuntimeID(t, leaderID)
	workerRuntimeID := squadAgentRuntimeID(t, workerID)

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
		mustExec(t, context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
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
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
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

const finalSOPTestProfile = `{
	"profile_key":"test-final-sop",
	"steps":[
		{"key":"pm","name":"pm","role_key":"pm"},
		{"key":"05-verify","name":"05-测试","role_key":"05-verify"}
	]
}`

func createGongfengSOPProject(t *testing.T, title string) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	project, err := testHandler.Queries.CreateProject(ctx, db.CreateProjectParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		Title:       title, Status: "in_progress", Priority: "medium", Scope: "workspace",
		OwnerID: util.MustParseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { mustExec(t, context.Background(), `DELETE FROM project WHERE id = $1`, project.ID) })
	if _, err := testHandler.Queries.CreateProjectResource(ctx, db.CreateProjectResourceParams{
		ProjectID: project.ID, WorkspaceID: util.MustParseUUID(testWorkspaceID),
		ResourceType: "gongfeng_repo",
		ResourceRef:  []byte(`{"project_path":"ChainWeaver/ida/gateway","repo_url":"https://git.code.tencent.com/ChainWeaver/ida/gateway"}`),
		Label:        pgtype.Text{String: "gateway", Valid: true},
		CreatedBy:    util.MustParseUUID(testUserID),
	}); err != nil {
		t.Fatalf("create project resource: %v", err)
	}
	return project.ID
}

type finalSOPTaskFixture struct {
	Ctx             context.Context
	LeaderID        string
	LeaderRuntimeID string
	VerifyID        string
	VerifyRuntimeID string
	IssueID         string
	RunID           pgtype.UUID
}

func newFinalSOPTaskFixture(t *testing.T, name, issueStatus string, projectID pgtype.UUID) finalSOPTaskFixture {
	t.Helper()
	requireHandlerDatabase(t)
	ctx := context.Background()
	leaderID := createHandlerTestAgent(t, name+" Leader", nil)
	verifyID := createHandlerTestSOPAgent(t, name+" 05-verify", "05-verify")

	leaderRuntimeID := squadAgentRuntimeID(t, leaderID)
	verifyRuntimeID := squadAgentRuntimeID(t, verifyID)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, sop_profile)
		VALUES ($1, $2, '', $3, $4, $5)
		RETURNING id
	`, testWorkspaceID, name+" Squad", leaderID, testUserID, finalSOPTestProfile).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	var issueID string
	issueNumber := nextHandlerTestIssueNumber(t)
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, status, project_id, assignee_type, assignee_id, number)
		VALUES ($1, 'member', $2, $3, $4, $5, 'squad', $6, $7)
		RETURNING id
	`, testWorkspaceID, testUserID, name, issueStatus, projectID, squadID, issueNumber).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	run, err := testHandler.Queries.CreateSquadSOPRun(ctx, db.CreateSquadSOPRunParams{
		WorkspaceID:    util.MustParseUUID(testWorkspaceID),
		IssueID:        util.MustParseUUID(issueID),
		SquadID:        util.MustParseUUID(squadID),
		ProfileKey:     "test-final-sop",
		Profile:        []byte(finalSOPTestProfile),
		Status:         "进行中",
		CurrentStepKey: "05-verify",
	})
	if err != nil {
		t.Fatalf("create sop run: %v", err)
	}

	return finalSOPTaskFixture{
		Ctx:             ctx,
		LeaderID:        leaderID,
		LeaderRuntimeID: leaderRuntimeID,
		VerifyID:        verifyID,
		VerifyRuntimeID: verifyRuntimeID,
		IssueID:         issueID,
		RunID:           run.ID,
	}
}

func (fx finalSOPTaskFixture) completeFinalTask(t *testing.T, result []byte) {
	t.Helper()
	var taskID string
	if err := testPool.QueryRow(fx.Ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at, started_at)
		VALUES ($1, $2, $3, 'running', 2, now(), now())
		RETURNING id
	`, fx.VerifyID, fx.VerifyRuntimeID, fx.IssueID).Scan(&taskID); err != nil {
		t.Fatalf("insert verify task: %v", err)
	}
	if _, err := testHandler.TaskService.CompleteTask(fx.Ctx, util.MustParseUUID(taskID), result, "", ""); err != nil {
		t.Fatalf("complete verify task: %v", err)
	}
}

func (fx finalSOPTaskFixture) requireIssueStatus(t *testing.T, want string) {
	t.Helper()
	issue, err := testHandler.Queries.GetIssue(fx.Ctx, util.MustParseUUID(fx.IssueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	if issue.Status != want {
		t.Fatalf("issue status = %q, want %q", issue.Status, want)
	}
}

func (fx finalSOPTaskFixture) requireRunStatus(t *testing.T, want string) {
	t.Helper()
	var got string
	if err := testPool.QueryRow(fx.Ctx, `SELECT status FROM squad_sop_run WHERE id = $1 AND workspace_id = $2`, fx.RunID, testWorkspaceID).Scan(&got); err != nil {
		t.Fatalf("load sop run: %v", err)
	}
	if got != want {
		t.Fatalf("sop run status = %q, want %q", got, want)
	}
}

func TestCompleteTask_FinalSOPStepAutoClosesIssueWithoutPullRequest(t *testing.T) {
	fx := newFinalSOPTaskFixture(t, "SOP Final Auto Close", "in_progress", pgtype.UUID{})
	fx.completeFinalTask(t, []byte(`{}`))
	fx.requireIssueStatus(t, "done")
	fx.requireRunStatus(t, "已完成")
}

func TestCompleteTask_FinalSOPStepBlockedOutputDoesNotAutoCloseIssue(t *testing.T) {
	fx := newFinalSOPTaskFixture(t, "SOP Final Blocked", "in_progress", pgtype.UUID{})
	result, err := json.Marshal(map[string]string{
		"output": "# 05-验证测试\n\n**最终判定：V0/V1 通过，V2 BLOCKED（环境缺失）**",
	})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	fx.completeFinalTask(t, result)
	fx.requireIssueStatus(t, "blocked")
	fx.requireRunStatus(t, "已阻塞")
	var queuedLeaderCount int
	if err := testPool.QueryRow(fx.Ctx, `
		SELECT count(*)
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND runtime_id = $3 AND status = 'queued' AND is_leader_task = TRUE
	`, fx.IssueID, fx.LeaderID, fx.LeaderRuntimeID).Scan(&queuedLeaderCount); err != nil {
		t.Fatalf("count queued leader tasks: %v", err)
	}
	if queuedLeaderCount != 1 {
		t.Fatalf("queued leader tasks = %d, want 1", queuedLeaderCount)
	}
}

func TestCompleteTask_FinalSOPStepBlocksGongfengIssueWithoutPullRequestAndComments(t *testing.T) {
	requireHandlerDatabase(t)
	projectID := createGongfengSOPProject(t, "SOP Final MR Gate Gongfeng Project")
	fx := newFinalSOPTaskFixture(t, "SOP Final MR Gate", "in_progress", projectID)
	fx.completeFinalTask(t, []byte(`{}`))
	fx.requireIssueStatus(t, "blocked")

	var authorID, content string
	if err := testPool.QueryRow(fx.Ctx, `
		SELECT author_id::text, content
		FROM comment
		WHERE issue_id = $1 AND author_type = 'system'
		ORDER BY created_at DESC
		LIMIT 1
	`, fx.IssueID).Scan(&authorID, &content); err != nil {
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
	fx := newFinalSOPTaskFixture(t, "SOP Final InReview", "in_review", pgtype.UUID{})
	fx.completeFinalTask(t, []byte(`{}`))
	fx.requireIssueStatus(t, "done")
}

func TestCompleteTask_AutoClosedChildIssueWakesParentSquad(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	sq := newSquadCommentTriggerFixture(t)
	verifyID := createHandlerTestSOPAgent(t, "SOP Child Auto Close 05-verify", "05-verify")

	verifyRuntimeID := squadAgentRuntimeID(t, verifyID)

	projectID := createGongfengSOPProject(t, "SOP Child Auto Close Gongfeng Project")

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
	`, testWorkspaceID, testUserID, "sop child auto closes with MR", parentID, projectID, sq.SquadID, childNumber).Scan(&childID); err != nil {
		t.Fatalf("create child issue: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id IN ($1, $2)`, parentID, childID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id IN ($1, $2)`, childID, parentID)
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
		mustExec(t, context.Background(), `DELETE FROM github_pull_request WHERE id = $1`, pr.ID)
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

func TestCreateComment_SquadLeaderSkipOnlyInspectsCurrentMention(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	fx := newSquadCommentTriggerFixture(t)
	issueID := uuidToString(fx.Issue.ID)

	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		mustExec(t, context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
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

func TestCreateComment_ActiveWorkerCommentDoesNotWakeLeader(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	fx := newSquadCommentTriggerFixture(t)
	issueID := uuidToString(fx.Issue.ID)

	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		mustExec(t, context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
	})

	// Seed a worker task for the leader agent on this issue so the guard
	// infers "agent's last activity was a worker task" — i.e. L is running
	// in its worker role when it posts the comment. We make it running (not
	// completed) so we can hand its ID back through X-Task-ID for the
	// resolveActor agent-identity check.
	runtimeID := squadAgentRuntimeID(t, fx.LeaderID)
	var workerTaskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, is_leader_task)
		VALUES ($1, $2, $3, 'running', FALSE)
		RETURNING id
	`, fx.LeaderID, runtimeID, issueID).Scan(&workerTaskID); err != nil {
		t.Fatalf("seed worker task: %v", err)
	}

	// L posts a comment through its current task-token identity.
	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "done — pushed the change",
	})
	setTaskTokenActor(r, fx.LeaderID, workerTaskID)
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
		mustExec(t, context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
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
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		mustExec(t, context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE parent_issue_id = $1`, issueID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
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

func countCrossProjectGateComments(t *testing.T, issueID string) int {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM comment
		WHERE issue_id = $1 AND author_type = 'system' AND content LIKE '%平台已阻止父任务阶段调度%'
	`, issueID).Scan(&count); err != nil {
		t.Fatalf("count gate comments: %v", err)
	}
	return count
}

func TestEnqueueCommentAgentTriggers_BlocksParentSOPStageWhenRequiredChildrenMissing(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	fx := newCrossProjectGateSOPFixture(t, false)
	comment := insertSOPPMMentionComment(t, fx)

	enqueueMentionedAgentTasksForTest(t, ctx, fx.Issue, comment, "agent", fx.LeaderID)

	if got := countQueuedOrDispatched(t, fx.ImplementID, fx.IssueID); got != 0 {
		t.Fatalf("parent 04 task count = %d, want 0 while required child is missing", got)
	}
	if got := countCrossProjectGateComments(t, fx.IssueID); got != 1 {
		t.Fatalf("gate comment count = %d, want 1", got)
	}
}

func TestCreateCommentFailsClosedWhenCrossProjectGateProfileIsInvalid(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	fx := newCrossProjectGateSOPFixture(t, false)
	if _, err := testPool.Exec(ctx, `
		UPDATE squad SET sop_profile = '{"mode":"stage_chain","steps":"invalid"}'::jsonb
		WHERE id = $1
	`, fx.SquadID); err != nil {
		t.Fatalf("corrupt squad SOP profile fixture: %v", err)
	}
	content := "03 通过，请进入 [@04-开发](mention://agent/" + fx.ImplementID + ")"

	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+fx.IssueID+"/comments", map[string]any{"content": content}), "id", fx.IssueID)
	testHandler.CreateComment(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	var commentCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content = $2`, fx.IssueID, content).Scan(&commentCount); err != nil {
		t.Fatalf("count rolled-back gate comments: %v", err)
	}
	if commentCount != 0 {
		t.Fatalf("comment committed while cross-project gate was unreadable: %d rows", commentCount)
	}
	if got := countQueuedOrDispatched(t, fx.ImplementID, fx.IssueID); got != 0 {
		t.Fatalf("implementation task escaped unreadable cross-project gate: %d", got)
	}
}

func TestEnqueueCommentAgentTriggers_AllowsParentSOPStageWhenRequiredChildrenDone(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	fx := newCrossProjectGateSOPFixture(t, true)
	comment := insertSOPPMMentionComment(t, fx)

	enqueueMentionedAgentTasksForTest(t, ctx, fx.Issue, comment, "agent", fx.LeaderID)

	if got := countQueuedOrDispatched(t, fx.ImplementID, fx.IssueID); got != 1 {
		t.Fatalf("parent 04 task count = %d, want 1 after required child is done", got)
	}
}

func TestEnqueueCommentAgentTriggers_AllowsParentSOPStageWhenLatestTaskSplitHasNoCrossProjectDependency(t *testing.T) {
	requireHandlerDatabase(t)
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

	enqueueMentionedAgentTasksForTest(t, ctx, fx.Issue, comment, "agent", fx.LeaderID)

	if got := countQueuedOrDispatched(t, fx.ImplementID, fx.IssueID); got != 1 {
		t.Fatalf("parent 04 task count = %d, want 1 when latest 03 has no cross-project dependency", got)
	}
}

func TestEnqueueCommentAgentTriggers_AllowsCrossProjectChildWithoutFurtherChildren(t *testing.T) {
	requireHandlerDatabase(t)
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
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, parentID)
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

	enqueueMentionedAgentTasksForTest(t, ctx, fx.Issue, comment, "agent", fx.LeaderID)

	if got := countQueuedOrDispatched(t, fx.ImplementID, fx.IssueID); got != 1 {
		t.Fatalf("child 04 task count = %d, want 1 when no further child is required", got)
	}
	if got := countCrossProjectGateComments(t, fx.IssueID); got != 0 {
		t.Fatalf("gate comment count = %d, want 0", got)
	}
}

func TestCreateRetryTask_InheritsIsLeaderTask(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	fx := newSquadCommentTriggerFixture(t)
	issueID := uuidToString(fx.Issue.ID)

	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	})

	runtimeID := squadAgentRuntimeID(t, fx.LeaderID)

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
				mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE id = $1 OR parent_task_id = $1`, parentID)
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

func TestCreateComment_SquadMentionPrivateLeaderBlocksPlainMember(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	// Use personalAgentTestFixture to get a personal agent + plain member.
	agentID, _, memberID := personalAgentTestFixture(t)

	// Create a squad with the personal agent as leader.
	squadID := createHandlerTestSquad(t, "Private Leader Squad", agentID)

	// Create an issue.
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title)
		VALUES ($1, 'member', $2, 'personal leader squad mention test')
		RETURNING id
	`, testWorkspaceID, memberID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	cleanupSquadCommentIssue(t, issueID)

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

func TestCreateComment_SquadMentionTriggersLeader(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	// Create a squad with a leader agent.
	var leaderID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM agent WHERE workspace_id = $1 ORDER BY created_at ASC LIMIT 1
	`, testWorkspaceID).Scan(&leaderID); err != nil {
		t.Fatalf("load leader agent: %v", err)
	}

	squadID := createHandlerTestSquad(t, "Mention Trigger Squad", leaderID)

	// Create an issue NOT assigned to the squad (assigned to nobody).
	issue := createHandlerCommentIssueFixture(t, "squad mention trigger test")

	// Post a comment that @mentions the squad.
	w, _ := issue.postComment(t, map[string]any{
		"content": "[@Squad](mention://squad/" + squadID + ") please handle this",
	}, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// The squad's leader should have a queued task.
	if got := issue.countQueuedTasks(t, leaderID); got != 1 {
		t.Fatalf("after @squad mention: expected 1 leader task, got %d", got)
	}
}

func TestCreateComment_MentionAssignedSquadLeaderCreatesLeaderRoleTask(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	leaderID := createHandlerTestAgent(t, "Assigned Squad Mention Leader", nil)
	squadID := createHandlerTestSquad(t, "Assigned Leader Mention Squad "+randomID()[:8], leaderID)

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, assignee_type, assignee_id)
		VALUES ($1, $2, 'todo', 'member', $3, 'squad', $4)
		RETURNING id
	`, testWorkspaceID, "assigned squad leader mention dedupe "+randomID()[:8], testUserID, squadID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	cleanupSquadCommentIssue(t, issueID)

	w, _ := (handlerCommentIssueFixture{ID: issueID}).postComment(t, map[string]any{
		"content": "[@Leader](mention://agent/" + leaderID + ") please close this",
	}, nil)
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
