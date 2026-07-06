package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestSquadSOPTaskStepMatchingAndState(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"steps": []map[string]string{
			{"key": "pm", "name": "pm", "role_key": "pm"},
			{"key": "02-design", "name": "02-方案设计", "role_key": "02-design"},
			{"key": "05-verify", "name": "05-测试", "role_key": "05-verify"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	steps := parseSquadSOPProfileSteps(raw)
	step, index, ok := matchSquadSOPStepForAgent(steps, "02-design")
	if !ok {
		t.Fatal("expected 02-design agent to match profile step")
	}
	if step.Key != "02-design" || index != 1 {
		t.Fatalf("matched step=%+v index=%d, want 02-design/1", step, index)
	}
	status, nextStep, ok := nextSquadSOPStateForTaskEvent(db.Issue{Status: "in_progress"}, steps, index, step.Key, "步骤完成")
	if !ok || status != "进行中" || nextStep != "05-verify" {
		t.Fatalf("next state = %s/%s/%v, want 进行中/05-verify/true", status, nextStep, ok)
	}
	status, nextStep, ok = nextSquadSOPStateForTaskEvent(db.Issue{Status: "done"}, steps, index, step.Key, "步骤完成")
	if !ok || status != "已完成" || nextStep != "02-design" {
		t.Fatalf("done state = %s/%s/%v, want 已完成/02-design/true", status, nextStep, ok)
	}
}

// mockRow implements pgx.Row, returning either a scanned task or pgx.ErrNoRows.
type mockRow struct {
	task *db.AgentTaskQueue
	err  error
}

func (r *mockRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	t := r.task
	ptrs := []any{
		&t.ID, &t.AgentID, &t.IssueID, &t.Status, &t.Priority,
		&t.DispatchedAt, &t.StartedAt, &t.CompletedAt, &t.Result,
		&t.Error, &t.CreatedAt, &t.Context, &t.RuntimeID,
		&t.SessionID, &t.WorkDir, &t.TriggerCommentID,
		&t.ChatSessionID, &t.AutopilotRunID,
	}
	for i, p := range ptrs {
		if i >= len(dest) {
			break
		}
		// Copy value from source to dest by assigning through the pointer.
		switch d := dest[i].(type) {
		case *pgtype.UUID:
			*d = *(p.(*pgtype.UUID))
		case *string:
			*d = *(p.(*string))
		case *int32:
			*d = *(p.(*int32))
		case *pgtype.Timestamptz:
			*d = *(p.(*pgtype.Timestamptz))
		case *[]byte:
			*d = *(p.(*[]byte))
		case *pgtype.Text:
			*d = *(p.(*pgtype.Text))
		}
	}
	return nil
}

// mockDBTX routes QueryRow calls: complete/fail queries return ErrNoRows,
// getAgentTask returns the stored task.
type mockDBTX struct {
	task db.AgentTaskQueue
}

func (m *mockDBTX) Exec(_ context.Context, _ string, _ ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag(""), nil
}

func (m *mockDBTX) Query(_ context.Context, _ string, _ ...interface{}) (pgx.Rows, error) {
	return nil, pgx.ErrNoRows
}

func (m *mockDBTX) QueryRow(_ context.Context, sql string, _ ...interface{}) pgx.Row {
	// CompleteAgentTask and FailAgentTask SQL contain "SET status ="
	if strings.Contains(sql, "SET status =") {
		return &mockRow{err: pgx.ErrNoRows}
	}
	// GetAgentTask — return the existing task
	return &mockRow{task: &m.task}
}

func testUUID(b byte) pgtype.UUID {
	var u pgtype.UUID
	u.Valid = true
	u.Bytes[0] = b
	return u
}

func runAlreadyFinalizedTaskCases(t *testing.T, run func(*TaskService, pgtype.UUID) (*db.AgentTaskQueue, error)) {
	t.Helper()
	taskID := testUUID(1)
	agentID := testUUID(2)

	tests := []struct {
		name   string
		status string
	}{
		{"already completed", "completed"},
		{"already cancelled", "cancelled"},
		{"already failed", "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockDBTX{task: db.AgentTaskQueue{
				ID:      taskID,
				AgentID: agentID,
				Status:  tt.status,
			}}
			svc := &TaskService{
				Queries: db.New(mock),
				Bus:     events.New(),
			}

			got, err := run(svc, taskID)
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if got == nil {
				t.Fatal("expected task, got nil")
			}
			if got.Status != tt.status {
				t.Errorf("expected status %q, got %q", tt.status, got.Status)
			}
			if got.ID != taskID {
				t.Error("returned task ID doesn't match")
			}
		})
	}
}

func TestCompleteTask_AlreadyFinalized(t *testing.T) {
	runAlreadyFinalizedTaskCases(t, func(svc *TaskService, taskID pgtype.UUID) (*db.AgentTaskQueue, error) {
		return svc.CompleteTask(context.Background(), taskID, nil, "", "")
	})
}

func TestFailTask_AlreadyFinalized(t *testing.T) {
	runAlreadyFinalizedTaskCases(t, func(svc *TaskService, taskID pgtype.UUID) (*db.AgentTaskQueue, error) {
		return svc.FailTask(context.Background(), taskID, "agent crashed", "", "", "")
	})
}

func TestTaskFailureClassifiers(t *testing.T) {
	cases := []struct {
		reason       string
		wantType     string
		wantResumeOK bool
		wantRetry    bool
	}{
		{reason: "timeout", wantType: "timeout", wantResumeOK: true, wantRetry: true},
		{reason: "codex_semantic_inactivity", wantType: "timeout", wantResumeOK: false, wantRetry: true},
		{reason: "runtime_recovery", wantType: "runtime", wantResumeOK: true, wantRetry: true},
		{reason: "agent_error.provider_capacity_or_rate_limit", wantType: "agent_error", wantResumeOK: true, wantRetry: true},
		{reason: "agent_error.provider_server_error", wantType: "agent_error", wantResumeOK: true, wantRetry: true},
		{reason: "agent_error.provider_network", wantType: "agent_error", wantResumeOK: true, wantRetry: true},
		{reason: "agent_error.model_not_found_or_unavailable", wantType: "agent_error", wantResumeOK: true, wantRetry: true},
		{reason: "iteration_limit", wantType: "agent_output", wantResumeOK: false, wantRetry: false},
		{reason: "api_invalid_request", wantType: "agent_error", wantResumeOK: false, wantRetry: false},
		{reason: "agent_error", wantType: "agent_error", wantResumeOK: true, wantRetry: false},
	}

	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			if got := taskErrorType(tc.reason); got != tc.wantType {
				t.Fatalf("taskErrorType(%q) = %q, want %q", tc.reason, got, tc.wantType)
			}
			if got := !resumeUnsafeFailureReason(tc.reason); got != tc.wantResumeOK {
				t.Fatalf("resume-safe(%q) = %v, want %v", tc.reason, got, tc.wantResumeOK)
			}
			if got := retryableReasons[tc.reason]; got != tc.wantRetry {
				t.Fatalf("retryableReasons[%q] = %v, want %v", tc.reason, got, tc.wantRetry)
			}
		})
	}
}

func TestTaskIssueStatusAutomationPredicates(t *testing.T) {
	issueID := testUUID(10)
	agentID := testUUID(11)
	otherAgentID := testUUID(12)
	commentID := testUUID(13)
	chatID := testUUID(14)
	autopilotRunID := testUUID(15)
	sourceContext, err := json.Marshal(IssueSourceSummaryContext{Type: IssueSourceSummaryContextType})
	if err != nil {
		t.Fatal(err)
	}

	baseTask := db.AgentTaskQueue{
		ID:      testUUID(20),
		IssueID: issueID,
		AgentID: agentID,
	}

	cases := []struct {
		name      string
		task      db.AgentTaskQueue
		wantStart bool
		wantBlock bool
	}{
		{
			name:      "ordinary assignment task",
			task:      baseTask,
			wantStart: true,
			wantBlock: true,
		},
		{
			name:      "comment-triggered task",
			task:      func() db.AgentTaskQueue { t := baseTask; t.TriggerCommentID = commentID; return t }(),
			wantStart: false,
			wantBlock: false,
		},
		{
			name:      "chat task",
			task:      func() db.AgentTaskQueue { t := baseTask; t.ChatSessionID = chatID; return t }(),
			wantStart: false,
			wantBlock: false,
		},
		{
			name:      "autopilot task",
			task:      func() db.AgentTaskQueue { t := baseTask; t.AutopilotRunID = autopilotRunID; return t }(),
			wantStart: false,
			wantBlock: false,
		},
		{
			name:      "source summary task",
			task:      func() db.AgentTaskQueue { t := baseTask; t.Context = sourceContext; return t }(),
			wantStart: false,
			wantBlock: false,
		},
		{
			name:      "quick create style task without issue",
			task:      db.AgentTaskQueue{ID: testUUID(21), AgentID: agentID},
			wantStart: false,
			wantBlock: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAutoStartIssueForTask(tc.task); got != tc.wantStart {
				t.Fatalf("shouldAutoStartIssueForTask = %v, want %v", got, tc.wantStart)
			}
			if got := shouldAutoBlockIssueForTaskFailure(tc.task); got != tc.wantBlock {
				t.Fatalf("shouldAutoBlockIssueForTaskFailure = %v, want %v", got, tc.wantBlock)
			}
		})
	}

	assignedIssue := db.Issue{
		Status:       "in_progress",
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
		AssigneeID:   agentID,
	}
	if !shouldAutoReviewIssueForTask(baseTask, assignedIssue) {
		t.Fatal("direct agent assignment should auto-advance completed work to review")
	}

	leaderTask := baseTask
	leaderTask.IsLeaderTask = true
	if shouldAutoReviewIssueForTask(leaderTask, assignedIssue) {
		t.Fatal("leader/coordinator task must not auto-advance issue to review")
	}

	commentTask := baseTask
	commentTask.TriggerCommentID = commentID
	if shouldAutoReviewIssueForTask(commentTask, assignedIssue) {
		t.Fatal("comment-triggered task must not auto-advance issue to review")
	}

	squadIssue := assignedIssue
	squadIssue.AssigneeType = pgtype.Text{String: "squad", Valid: true}
	if shouldAutoReviewIssueForTask(baseTask, squadIssue) {
		t.Fatal("squad-assigned issue must not auto-advance to review from a worker task")
	}

	otherAssigneeIssue := assignedIssue
	otherAssigneeIssue.AssigneeID = otherAgentID
	if shouldAutoReviewIssueForTask(baseTask, otherAssigneeIssue) {
		t.Fatal("task from a non-assignee agent must not auto-advance issue to review")
	}
}
