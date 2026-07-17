package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// setupRerunTestFixture creates an issue assigned to the integration test
// agent and returns (issueID, agentID, runtimeID).
func setupRerunTestFixture(t *testing.T) (string, string, string) {
	t.Helper()
	ctx := context.Background()

	var agentID, runtimeID string
	if err := testPool.QueryRow(ctx, `
		SELECT a.id, a.runtime_id FROM agent a
		JOIN member m ON m.workspace_id = a.workspace_id
		JOIN "user" u ON u.id = m.user_id
		WHERE u.account = $1
		  AND a.archived_at IS NULL
		LIMIT 1
	`, integrationTestAccount).Scan(&agentID, &runtimeID); err != nil {
		t.Fatalf("failed to find test agent: %v", err)
	}

	var issueID string
	// Pick the next per-workspace number to avoid colliding with the
	// uq_issue_workspace_number unique constraint when multiple fixtures
	// coexist in the same test (e.g. TestRerunIssueRejectsCrossIssueTask).
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, assignee_type, assignee_id, number)
		SELECT $1, 'Rerun test issue', 'todo', 'none', 'member', m.user_id, 'agent', $2,
		       (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1)
		FROM member m WHERE m.workspace_id = $1 LIMIT 1
		RETURNING id
	`, testWorkspaceID, agentID).Scan(&issueID); err != nil {
		t.Fatalf("failed to create test issue: %v", err)
	}

	return issueID, agentID, runtimeID
}

func cleanupRerunFixture(t *testing.T, issueID string) {
	t.Helper()
	ctx := context.Background()
	_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
}

type rerunTestFixture struct {
	ctx       context.Context
	queries   *db.Queries
	issueID   string
	agentID   string
	runtimeID string
}

func setupRerunSessionTest(t *testing.T) rerunTestFixture {
	t.Helper()
	if testPool == nil {
		t.Skip("no database connection")
	}
	issueID, agentID, runtimeID := setupRerunTestFixture(t)
	t.Cleanup(func() { cleanupRerunFixture(t, issueID) })
	return rerunTestFixture{
		ctx:       context.Background(),
		queries:   db.New(testPool),
		issueID:   issueID,
		agentID:   agentID,
		runtimeID: runtimeID,
	}
}

func (f rerunTestFixture) getLastTaskSession() (db.GetLastTaskSessionRow, error) {
	return f.queries.GetLastTaskSession(f.ctx, db.GetLastTaskSessionParams{
		AgentID: pgtype.UUID{Bytes: parseUUIDBytes(f.agentID), Valid: true},
		IssueID: pgtype.UUID{Bytes: parseUUIDBytes(f.issueID), Valid: true},
	})
}

func (f rerunTestFixture) insertFailedTask(t *testing.T, age, sessionID, workDir, failureReason string, errorText *string) {
	t.Helper()
	if _, err := testPool.Exec(f.ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority, started_at, completed_at,
			session_id, work_dir, failure_reason, error
		)
		VALUES ($1, $2, $3, 'failed', 0, now() - $4::interval, now() - $4::interval, $5, $6, $7, $8)
	`, f.agentID, f.runtimeID, f.issueID, age, sessionID, workDir, failureReason, errorText); err != nil {
		t.Fatalf("insert failed task: %v", err)
	}
}

func (f rerunTestFixture) insertSourceTask(t *testing.T, agentID, issueID string, triggerCommentID *string) string {
	t.Helper()
	var taskID string
	if err := testPool.QueryRow(f.ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority, started_at, completed_at,
			failure_reason, trigger_comment_id
		)
		VALUES ($1, $2, $3, 'failed', 0, now() - interval '1 minute', now() - interval '30 seconds', 'agent_error', $4)
		RETURNING id
	`, agentID, f.runtimeID, issueID, triggerCommentID).Scan(&taskID); err != nil {
		t.Fatalf("insert source task: %v", err)
	}
	return taskID
}

func (f rerunTestFixture) insertRetryParentTask(t *testing.T, sessionID, workDir, failureReason string) string {
	t.Helper()

	var parentID string
	if err := testPool.QueryRow(f.ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority,
			started_at, completed_at, session_id, work_dir, failure_reason,
			attempt, max_attempts
		)
		VALUES ($1, $2, $3, 'failed', 0, now() - interval '1 minute', now() - interval '1 minute',
		        $4, $5, $6, 1, 2)
		RETURNING id
	`, f.agentID, f.runtimeID, f.issueID, sessionID, workDir, failureReason).Scan(&parentID); err != nil {
		t.Fatalf("insert retry parent task: %v", err)
	}
	return parentID
}

func (f rerunTestFixture) createRetryTask(t *testing.T, parentID string) db.AgentTaskQueue {
	t.Helper()

	child, err := f.queries.CreateRetryTask(f.ctx, pgtype.UUID{Bytes: parseUUIDBytes(parentID), Valid: true})
	if err != nil {
		t.Fatalf("CreateRetryTask failed: %v", err)
	}
	return child
}

func (f rerunTestFixture) taskService() *service.TaskService {
	hub := realtime.NewHub()
	go hub.Run()
	return service.NewTaskService(f.queries, testPool, hub, events.New(), nil)
}

func (f rerunTestFixture) rerunSourceTask(t *testing.T, sourceTaskID string) *db.AgentTaskQueue {
	t.Helper()

	result, err := f.taskService().RerunIssueInTx(
		f.ctx,
		f.queries,
		pgtype.UUID{Bytes: parseUUIDBytes(f.issueID), Valid: true},
		pgtype.UUID{Bytes: parseUUIDBytes(sourceTaskID), Valid: true},
		pgtype.UUID{},
	)
	if err != nil {
		t.Fatalf("RerunIssue failed: %v", err)
	}
	return &result.Task
}

func TestGetLastTaskSessionFiltersPoisonedFailures(t *testing.T) {
	tests := []struct {
		failureReason string
		errorText     *string
		withFallback  bool
	}{
		{failureReason: "iteration_limit", withFallback: true},
		{failureReason: "agent_fallback_message"},
		{failureReason: "api_invalid_request"},
		{failureReason: "codex_semantic_inactivity", errorText: stringPointer("codex semantic inactivity timeout after 10m0s without agent progress"), withFallback: true},
	}

	for _, tt := range tests {
		t.Run(tt.failureReason, func(t *testing.T) {
			f := setupRerunSessionTest(t)
			if tt.withFallback {
				f.insertFailedTask(t, "2 minutes", "HEALTHY-SESSION", "/tmp/healthy", "timeout", nil)
			}
			f.insertFailedTask(t, "1 minute", "POISONED-SESSION", "/tmp/poisoned", tt.failureReason, tt.errorText)

			prior, err := f.getLastTaskSession()
			if tt.withFallback {
				if err != nil || !prior.SessionID.Valid || prior.SessionID.String != "HEALTHY-SESSION" {
					t.Fatalf("expected healthy fallback, got session=%+v err=%v", prior.SessionID, err)
				}
			} else if err == nil && prior.SessionID.Valid {
				t.Fatalf("expected no resumable session, got %q", prior.SessionID.String)
			}
		})
	}
}

func TestCreateRetryTaskFreshensCodexSemanticInactivity(t *testing.T) {
	f := setupRerunSessionTest(t)
	parentID := f.insertRetryParentTask(t, "CODEX-STUCK-SESSION", "/tmp/codex-stuck", "codex_semantic_inactivity")
	child := f.createRetryTask(t, parentID)

	if child.SessionID.Valid {
		t.Fatalf("expected retry child to drop poisoned session_id, got %q", child.SessionID.String)
	}
	if child.WorkDir.Valid {
		t.Fatalf("expected retry child to drop poisoned work_dir, got %q", child.WorkDir.String)
	}
	if !child.ForceFreshSession {
		t.Fatal("expected retry child to force a fresh session")
	}
	if child.Attempt != 2 {
		t.Fatalf("expected attempt 2, got %d", child.Attempt)
	}
}

func TestCreateRetryTaskKeepsOrdinaryTimeoutSession(t *testing.T) {
	f := setupRerunSessionTest(t)
	parentID := f.insertRetryParentTask(t, "ORDINARY-TIMEOUT-SESSION", "/tmp/ordinary-timeout", "timeout")
	child := f.createRetryTask(t, parentID)

	if !child.SessionID.Valid || child.SessionID.String != "ORDINARY-TIMEOUT-SESSION" {
		t.Fatalf("expected retry child to inherit session_id, got %+v", child.SessionID)
	}
	if !child.WorkDir.Valid || child.WorkDir.String != "/tmp/ordinary-timeout" {
		t.Fatalf("expected retry child to inherit work_dir, got %+v", child.WorkDir)
	}
	if child.ForceFreshSession {
		t.Fatal("expected ordinary timeout retry child to keep resume enabled")
	}
	if child.Attempt != 2 {
		t.Fatalf("expected attempt 2, got %d", child.Attempt)
	}
}

// TestGetLastTaskSessionKeepsBenignAgentErrorWithSession asserts the
// current generic 'agent_error' bucket remains resumable for failures that
// do not poison the provider session.
func TestGetLastTaskSessionKeepsBenignAgentErrorWithSession(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}

	issueID, agentID, runtimeID := setupRerunTestFixture(t)
	t.Cleanup(func() { cleanupRerunFixture(t, issueID) })

	ctx := context.Background()

	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, started_at, completed_at, session_id, work_dir, failure_reason, error)
		VALUES ($1, $2, $3, 'failed', 0, now() - interval '30 seconds', now() - interval '30 seconds', 'HEALTHY-RESUMABLE', '/tmp/healthy', 'agent_error',
		        'tool execution failed: connection refused')
	`, agentID, runtimeID, issueID); err != nil {
		t.Fatalf("insert benign failed task: %v", err)
	}

	queries := db.New(testPool)
	prior, err := queries.GetLastTaskSession(ctx, db.GetLastTaskSessionParams{
		AgentID: pgtype.UUID{Bytes: parseUUIDBytes(agentID), Valid: true},
		IssueID: pgtype.UUID{Bytes: parseUUIDBytes(issueID), Valid: true},
	})
	if err != nil {
		t.Fatalf("GetLastTaskSession failed: %v", err)
	}
	if !prior.SessionID.Valid || prior.SessionID.String != "HEALTHY-RESUMABLE" {
		t.Fatalf("expected to resume HEALTHY-RESUMABLE, got %q (valid=%v)", prior.SessionID.String, prior.SessionID.Valid)
	}
}

// TestRerunIssueSetsForceFreshSession asserts the manual rerun flow flags
// the new task so the daemon claim handler skips the resume lookup. This
// is the call-site half of the fix: even if the SQL filter ever misses a
// poisoned classifier, manual rerun never resumes.
func TestRerunIssueSetsForceFreshSession(t *testing.T) {
	f := setupRerunSessionTest(t)

	result, err := f.taskService().RerunIssueInTx(f.ctx, f.queries, pgtype.UUID{Bytes: parseUUIDBytes(f.issueID), Valid: true}, pgtype.UUID{}, pgtype.UUID{})
	if err != nil {
		t.Fatalf("RerunIssue failed: %v", err)
	}
	if !result.Task.ForceFreshSession {
		t.Fatal("expected manual rerun to set force_fresh_session=true")
	}
}

// TestRerunIssueTargetsSourceTaskAgent asserts that when a source task ID is
// supplied (the execution-log retry-button path), the rerun targets the agent
// that ran that specific past task — not the issue's current assignee.
// Without this, clicking retry on a row whose agent has since been displaced
// (squad worker, @-mention agent, or a prior assignee) re-fires the new
// assignee instead, which is the MUL-2457 bug.
func TestRerunIssueTargetsSourceTaskAgent(t *testing.T) {
	f := setupRerunSessionTest(t)

	// Create a second agent in the same workspace + runtime so we can stand
	// in as a "row whose agent is no longer the issue assignee" — e.g. a
	// squad worker or an @-mentioned agent. The issue's assignee is still
	// the primary agent; the rerun must target this secondary one because
	// that's whose task row the user clicked.
	var secondaryAgentID string
	if err := testPool.QueryRow(f.ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, scope, max_concurrent_tasks, owner_id
		)
		SELECT a.workspace_id, 'Rerun Secondary Agent', '', 'cloud', '{}'::jsonb,
		       a.runtime_id, 'workspace', 1, a.owner_id
		FROM agent a WHERE a.id = $1
		RETURNING id
	`, f.agentID).Scan(&secondaryAgentID); err != nil {
		t.Fatalf("create secondary agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(f.ctx, `DELETE FROM agent_task_queue WHERE agent_id = $1`, secondaryAgentID)
		_, _ = testPool.Exec(f.ctx, `DELETE FROM agent WHERE id = $1`, secondaryAgentID)
	})

	sourceTaskID := f.insertSourceTask(t, secondaryAgentID, f.issueID, nil)
	task := f.rerunSourceTask(t, sourceTaskID)

	gotAgent := util.UUIDToString(task.AgentID)
	if gotAgent != secondaryAgentID {
		t.Fatalf("rerun targeted wrong agent: got %s, want %s (issue assignee is %s — must not be picked)",
			gotAgent, secondaryAgentID, f.agentID)
	}
	if !task.ForceFreshSession {
		t.Fatal("expected per-row rerun to also set force_fresh_session=true")
	}
}

// TestRerunIssueRejectsCrossIssueTask asserts a source task whose IssueID
// does not match the rerun target is rejected — both as defense-in-depth
// against malicious requests and because picking up an unrelated task's
// agent would silently misroute the rerun.
func TestRerunIssueRejectsCrossIssueTask(t *testing.T) {
	f := setupRerunSessionTest(t)

	// Second issue in the same workspace, with a task that does NOT belong
	// to issue A. The handler must reject this. Take the next available
	// per-workspace number so the uq_issue_workspace_number constraint
	// (both issues default to number=0 otherwise) doesn't fire before the
	// rerun assertion can.
	var issueBID string
	if err := testPool.QueryRow(f.ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, assignee_type, assignee_id, number)
		SELECT $1, 'Rerun cross-issue test', 'todo', 'none', 'member', m.user_id, 'agent', $2,
		       (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1)
		FROM member m WHERE m.workspace_id = $1 LIMIT 1
		RETURNING id
	`, testWorkspaceID, f.agentID).Scan(&issueBID); err != nil {
		t.Fatalf("create second issue: %v", err)
	}
	t.Cleanup(func() { cleanupRerunFixture(t, issueBID) })

	crossTaskID := f.insertSourceTask(t, f.agentID, issueBID, nil)

	_, err := f.taskService().RerunIssueInTx(
		f.ctx,
		f.queries,
		pgtype.UUID{Bytes: parseUUIDBytes(f.issueID), Valid: true},
		pgtype.UUID{Bytes: parseUUIDBytes(crossTaskID), Valid: true},
		pgtype.UUID{},
	)
	if err == nil {
		t.Fatal("expected RerunIssue to reject a source task from a different issue")
	}
}

// TestRerunIssueInheritsTriggerCommentFromSourceTask locks the trigger
// provenance contract: a per-row rerun of a comment- or mention-triggered
// task must carry the original trigger_comment_id through to the new task.
// Otherwise the daemon's buildCommentPrompt path (which keys on
// TriggerCommentID) is skipped and the rerun degrades into a generic
// issue run that has lost the original comment context — see MUL-2457
// review feedback.
func TestRerunIssueInheritsTriggerCommentFromSourceTask(t *testing.T) {
	f := setupRerunSessionTest(t)

	// Create a comment to stand in as the original mention / reply trigger.
	var triggerCommentID string
	if err := testPool.QueryRow(f.ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type)
		SELECT $1, $2, 'member', m.user_id, 'please retry this', 'comment'
		FROM member m WHERE m.workspace_id = $2 LIMIT 1
		RETURNING id
	`, f.issueID, testWorkspaceID).Scan(&triggerCommentID); err != nil {
		t.Fatalf("insert trigger comment: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(f.ctx, `DELETE FROM comment WHERE id = $1`, triggerCommentID)
	})

	sourceTaskID := f.insertSourceTask(t, f.agentID, f.issueID, &triggerCommentID)
	task := f.rerunSourceTask(t, sourceTaskID)
	if !task.TriggerCommentID.Valid {
		t.Fatal("expected per-row rerun to inherit trigger_comment_id from source task, got NULL")
	}
	if got := util.UUIDToString(task.TriggerCommentID); got != triggerCommentID {
		t.Fatalf("trigger_comment_id mismatch: got %s, want %s", got, triggerCommentID)
	}
}
