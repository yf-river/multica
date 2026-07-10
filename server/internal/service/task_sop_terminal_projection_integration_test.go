package service

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type squadTerminalProjectionFixture struct {
	workspaceID pgtype.UUID
	issueID     pgtype.UUID
	taskID      pgtype.UUID
	runID       pgtype.UUID
	workerID    pgtype.UUID
	leaderID    pgtype.UUID
}

func TestCompleteTaskRollsBackWhenSquadTerminalProjectionFails(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "running", time.Now().Add(-time.Minute), 1, 1)
	installSquadTerminalProjectionFailureTrigger(t, pool, fixture.taskID, "步骤完成")

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	_, err := service.CompleteTask(
		context.Background(),
		fixture.taskID,
		[]byte(`{"output":"stage complete"}`),
		"",
		"",
	)
	if err == nil {
		t.Fatal("CompleteTask succeeded even though the automatic SOP event could not be persisted")
	}

	assertSquadTerminalProjectionRolledBack(t, pool, fixture, "running")
}

func TestFailTaskRollsBackWhenSquadTerminalProjectionFails(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "running", time.Now().Add(-time.Minute), 1, 1)
	installSquadTerminalProjectionFailureTrigger(t, pool, fixture.taskID, "步骤失败")

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	_, err := service.FailTask(
		context.Background(),
		fixture.taskID,
		"worker failed",
		"",
		"",
		"agent_error",
	)
	if err == nil {
		t.Fatal("FailTask succeeded even though the automatic SOP event could not be persisted")
	}

	assertSquadTerminalProjectionRolledBack(t, pool, fixture, "running")
}

func TestFailStaleTasksRollsBackWhenSquadTerminalProjectionFails(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "running", time.Now().Add(-2*time.Hour), 1, 1)
	installSquadTerminalProjectionFailureTrigger(t, pool, fixture.taskID, "步骤失败")

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	_, err := service.FailStaleTasks(context.Background(), db.FailStaleTasksParams{
		DispatchTimeoutSecs: 60,
		RunningTimeoutSecs:  60,
	})
	if err == nil {
		t.Fatal("FailStaleTasks succeeded even though the automatic SOP event could not be persisted")
	}

	assertSquadTerminalProjectionRolledBack(t, pool, fixture, "running")
}

func TestCompleteTaskRollsBackWhenMissingMRGateCommentFails(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "running", time.Now().Add(-time.Minute), 1, 1)
	ctx := context.Background()
	projectID := testPGUUID()
	if _, err := pool.Exec(ctx, `
		INSERT INTO project (id, workspace_id, title, status, priority, scope)
		VALUES ($1, $2, 'SOP atomic MR gate', 'in_progress', 'medium', 'workspace')
	`, projectID, fixture.workspaceID); err != nil {
		t.Fatalf("create Gongfeng project: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_resource (project_id, workspace_id, resource_type, resource_ref)
		VALUES ($1, $2, 'gongfeng_repo', '{"project_path":"example/repo"}')
	`, projectID, fixture.workspaceID); err != nil {
		t.Fatalf("create Gongfeng project resource: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET project_id = $2 WHERE id = $1`, fixture.issueID, projectID); err != nil {
		t.Fatalf("attach Gongfeng project to issue: %v", err)
	}
	installMissingMRGateCommentFailureTrigger(t, pool, fixture.taskID)

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	_, err := service.CompleteTask(ctx, fixture.taskID, []byte(`{"output":"verified"}`), "", "")
	if err == nil {
		t.Fatal("CompleteTask succeeded even though the required missing-MR comment could not be persisted")
	}
	assertSquadTerminalProjectionRolledBack(t, pool, fixture, "running")
}

func TestCompleteTaskRollsBackWhenLeaderContinuationFails(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "running", time.Now().Add(-time.Minute), 1, 1)
	ctx := context.Background()
	profile := `{"key":"test","steps":[{"key":"worker","name":"Worker","role_key":"worker"},{"key":"review","name":"Review","role_key":"review"}]}`
	if _, err := pool.Exec(ctx, `UPDATE squad_sop_run SET profile = $2::jsonb WHERE id = $1`, fixture.runID, profile); err != nil {
		t.Fatalf("make worker step intermediate: %v", err)
	}
	installLeaderContinuationFailureTrigger(t, pool, fixture.issueID)

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	_, err := service.CompleteTask(ctx, fixture.taskID, []byte(`{"output":"worker complete"}`), "", "")
	if err == nil {
		t.Fatal("CompleteTask succeeded even though the leader continuation could not be persisted")
	}
	assertSquadTerminalProjectionRolledBack(t, pool, fixture, "running")
}

func TestCompleteTaskRepairsProjectionWhenAutomaticEventAlreadyExists(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "completed", time.Now().Add(-time.Minute), 1, 1)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO squad_sop_step_event (
			run_id, workspace_id, issue_id, squad_id,
			step_key, step_name, role_key, event_type, status,
			reason, created_by_type, task_id
		)
		SELECT id, workspace_id, issue_id, squad_id,
		       'worker', 'Worker', 'worker', '步骤完成', '已完成',
		       'pre-existing automatic event', 'system', $2
		FROM squad_sop_run
		WHERE id = $1
	`, fixture.runID, fixture.taskID); err != nil {
		t.Fatalf("seed existing automatic event: %v", err)
	}

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	task, err := service.CompleteTask(ctx, fixture.taskID, []byte(`{"output":"ignored replay body"}`), "", "")
	if err != nil {
		t.Fatalf("reconcile already-completed task: %v", err)
	}
	if task == nil || task.Status != "completed" {
		t.Fatalf("replayed task = %+v, want completed", task)
	}

	assertSquadTerminalProjectionCommitted(t, pool, fixture, "已完成", "done", "步骤完成")
}

func TestCompleteTaskRepairsMissingMRCommentAfterPartialLegacyProjection(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "completed", time.Now().Add(-time.Minute), 1, 1)
	ctx := context.Background()
	attachGongfengProjectToSquadProjectionIssue(t, pool, fixture)
	if _, err := pool.Exec(ctx, `
		UPDATE squad_sop_run SET status = '已完成', completed_at = now() WHERE id = $1
	`, fixture.runID); err != nil {
		t.Fatalf("seed completed legacy run: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET status = 'blocked' WHERE id = $1`, fixture.issueID); err != nil {
		t.Fatalf("seed blocked legacy issue: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO squad_sop_step_event (
			run_id, workspace_id, issue_id, squad_id,
			step_key, step_name, role_key, event_type, status,
			reason, created_by_type, task_id
		)
		SELECT id, workspace_id, issue_id, squad_id,
		       'worker', 'Worker', 'worker', '步骤完成', '已完成',
		       'legacy partial projection', 'system', $2
		FROM squad_sop_run
		WHERE id = $1
	`, fixture.runID, fixture.taskID); err != nil {
		t.Fatalf("seed legacy missing-MR partial projection: %v", err)
	}

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	if _, err := service.CompleteTask(ctx, fixture.taskID, nil, "", ""); err != nil {
		t.Fatalf("repair missing-MR partial projection: %v", err)
	}
	var comments int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM comment
		WHERE issue_id = $1
		  AND author_type = 'system'
		  AND content LIKE '%平台还没有关联 MR%'
	`, fixture.issueID).Scan(&comments); err != nil {
		t.Fatalf("count repaired missing-MR comments: %v", err)
	}
	if comments != 1 {
		t.Fatalf("repaired missing-MR comments = %d, want 1", comments)
	}
}

func TestCompleteTaskRepairsUsingStartEventProvenance(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "completed", time.Now().Add(-time.Minute), 1, 1)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO squad_sop_step_event (
			run_id, workspace_id, issue_id, squad_id,
			step_key, step_name, role_key, event_type, status,
			reason, created_by_type, task_id
		)
		SELECT id, workspace_id, issue_id, squad_id,
		       'worker', 'Worker', 'worker', '步骤开始', '进行中',
		       'legacy task start provenance', 'system', $2
		FROM squad_sop_run
		WHERE id = $1
	`, fixture.runID, fixture.taskID); err != nil {
		t.Fatalf("seed task start provenance: %v", err)
	}

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	if _, err := service.CompleteTask(ctx, fixture.taskID, nil, "", ""); err != nil {
		t.Fatalf("repair terminal projection from start event: %v", err)
	}
	assertSquadTerminalProjectionCommitted(t, pool, fixture, "已完成", "done", "步骤完成")
}

func TestCompleteTaskReplayIgnoresFormerSquadAssignment(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "completed", time.Now().Add(-time.Minute), 1, 1)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO squad_sop_step_event (
			run_id, workspace_id, issue_id, squad_id,
			step_key, step_name, role_key, event_type, status,
			reason, created_by_type, task_id
		)
		SELECT id, workspace_id, issue_id, squad_id,
		       'worker', 'Worker', 'worker', '步骤完成', '已完成',
		       'former Squad terminal event', 'system', $2
		FROM squad_sop_run
		WHERE id = $1
	`, fixture.runID, fixture.taskID); err != nil {
		t.Fatalf("seed former Squad terminal event: %v", err)
	}
	newSquadID := testPGUUID()
	if _, err := pool.Exec(ctx, `
		INSERT INTO squad (id, workspace_id, name, leader_id, creator_id, scope, sop_profile)
		VALUES ($1, $2, 'Replacement Squad', $3, $3, 'workspace', '{"key":"replacement","steps":[]}')
	`, newSquadID, fixture.workspaceID, fixture.leaderID); err != nil {
		t.Fatalf("create replacement Squad: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE squad_sop_run
		SET status = '已完成', completed_at = now()
		WHERE id = $1
	`, fixture.runID); err != nil {
		t.Fatalf("close former Squad run without a terminal event: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET assignee_id = $2 WHERE id = $1`, fixture.issueID, newSquadID); err != nil {
		t.Fatalf("reassign issue to replacement Squad: %v", err)
	}

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	task, err := service.CompleteTask(ctx, fixture.taskID, nil, "", "")
	if err != nil {
		t.Fatalf("replay terminal task after Squad reassignment: %v", err)
	}
	if task == nil || task.Status != "completed" {
		t.Fatalf("replayed task = %+v, want completed", task)
	}
	var terminalEvents, leaderContinuations int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM squad_sop_step_event
		WHERE task_id = $1 AND event_type = '步骤完成' AND created_by_type = 'system'
	`, fixture.taskID).Scan(&terminalEvents); err != nil {
		t.Fatalf("count former Squad terminal events: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND context->>'type' = 'squad_sop_leader_continuation'
	`, fixture.issueID).Scan(&leaderContinuations); err != nil {
		t.Fatalf("count reassignment replay leader continuations: %v", err)
	}
	if terminalEvents != 1 || leaderContinuations != 0 {
		t.Fatalf("reassignment replay created terminal_events=%d leader_continuations=%d, want 1/0", terminalEvents, leaderContinuations)
	}
}

func TestCompleteTaskReplayDoesNotGuessReplacementSquadRun(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "completed", time.Now().Add(-time.Minute), 1, 1)
	ctx := context.Background()
	newSquadID := testPGUUID()
	newRunID := testPGUUID()
	if _, err := pool.Exec(ctx, `
		INSERT INTO squad (id, workspace_id, name, leader_id, creator_id, scope, sop_profile)
		VALUES (
			$1, $2, 'Replacement Squad', $3, $3, 'workspace',
			'{"key":"replacement","steps":[{"key":"worker","name":"Worker","role_key":"worker"}]}'
		)
	`, newSquadID, fixture.workspaceID, fixture.leaderID); err != nil {
		t.Fatalf("create replacement Squad: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE squad_sop_run
		SET status = '已完成', completed_at = now()
		WHERE id = $1
	`, fixture.runID); err != nil {
		t.Fatalf("close former Squad run without a terminal event: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET assignee_id = $2 WHERE id = $1`, fixture.issueID, newSquadID); err != nil {
		t.Fatalf("reassign issue to replacement Squad: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO squad_sop_run (
			id, workspace_id, issue_id, squad_id, profile_key, profile,
			status, current_step_key
		) VALUES (
			$1, $2, $3, $4, 'replacement',
			'{"key":"replacement","steps":[{"key":"worker","name":"Worker","role_key":"worker"}]}',
			'进行中', 'worker'
		)
	`, newRunID, fixture.workspaceID, fixture.issueID, newSquadID); err != nil {
		t.Fatalf("create replacement run: %v", err)
	}

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	task, err := service.CompleteTask(ctx, fixture.taskID, nil, "", "")
	if err != nil {
		t.Fatalf("replay terminal task without run provenance: %v", err)
	}
	if task == nil || task.Status != "completed" {
		t.Fatalf("replayed task = %+v, want completed", task)
	}
	var runStatus, currentStep, issueStatus string
	if err := pool.QueryRow(ctx, `SELECT status, current_step_key FROM squad_sop_run WHERE id = $1`, newRunID).Scan(&runStatus, &currentStep); err != nil {
		t.Fatalf("read replacement run: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, fixture.issueID).Scan(&issueStatus); err != nil {
		t.Fatalf("read reassigned issue: %v", err)
	}
	var terminalEvents, leaderContinuations int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM squad_sop_step_event
		WHERE task_id = $1 AND event_type IN ('步骤完成', '步骤失败')
	`, fixture.taskID).Scan(&terminalEvents); err != nil {
		t.Fatalf("count unproven replay terminal events: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND context->>'type' = 'squad_sop_leader_continuation'
	`, fixture.issueID).Scan(&leaderContinuations); err != nil {
		t.Fatalf("count unproven replay leader continuations: %v", err)
	}
	if runStatus != "进行中" || currentStep != "worker" || issueStatus != "in_progress" || terminalEvents != 0 || leaderContinuations != 0 {
		t.Fatalf(
			"unproven replay mutated replacement state = run:%s step:%s issue:%s events:%d leaders:%d, want 进行中/worker/in_progress/0/0",
			runStatus,
			currentStep,
			issueStatus,
			terminalEvents,
			leaderContinuations,
		)
	}
}

func TestCompleteTaskCommitsSquadTerminalProjection(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "running", time.Now().Add(-time.Minute), 1, 1)

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	task, err := service.CompleteTask(
		context.Background(),
		fixture.taskID,
		[]byte(`{"output":"stage complete"}`),
		"",
		"",
	)
	if err != nil {
		t.Fatalf("complete Squad worker task: %v", err)
	}
	if task == nil || task.Status != "completed" {
		t.Fatalf("completed task = %+v, want completed", task)
	}
	assertSquadTerminalProjectionCommitted(t, pool, fixture, "已完成", "done", "步骤完成")
}

func TestFailTaskCommitsSquadTerminalProjection(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "running", time.Now().Add(-time.Minute), 1, 1)

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	task, err := service.FailTask(
		context.Background(),
		fixture.taskID,
		"worker failed",
		"",
		"",
		"agent_error",
	)
	if err != nil {
		t.Fatalf("fail Squad worker task: %v", err)
	}
	if task == nil || task.Status != "failed" {
		t.Fatalf("failed task = %+v, want failed", task)
	}
	assertSquadTerminalProjectionCommitted(t, pool, fixture, "已失败", "blocked", "步骤失败")
}

func TestFailStaleTasksCommitsSquadTerminalProjection(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "running", time.Now().Add(-2*time.Hour), 1, 1)

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	failed, err := service.FailStaleTasks(context.Background(), db.FailStaleTasksParams{
		DispatchTimeoutSecs: 60,
		RunningTimeoutSecs:  60,
	})
	if err != nil {
		t.Fatalf("sweep stale Squad worker task: %v", err)
	}
	if len(failed) != 1 || failed[0].ID != fixture.taskID {
		t.Fatalf("failed tasks = %+v, want only %s", failed, uuid.UUID(fixture.taskID.Bytes))
	}
	assertSquadTerminalProjectionCommitted(t, pool, fixture, "已失败", "blocked", "步骤失败")
}

func TestCompleteTaskCreatesOneAtomicLeaderContinuation(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "running", time.Now().Add(-time.Minute), 1, 1)
	ctx := context.Background()
	profile := `{"key":"test","steps":[{"key":"worker","name":"Worker","role_key":"worker"},{"key":"review","name":"Review","role_key":"review"}]}`
	if _, err := pool.Exec(ctx, `UPDATE squad_sop_run SET profile = $2::jsonb WHERE id = $1`, fixture.runID, profile); err != nil {
		t.Fatalf("make worker step intermediate: %v", err)
	}

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	if _, err := service.CompleteTask(ctx, fixture.taskID, []byte(`{"output":"worker complete"}`), "", ""); err != nil {
		t.Fatalf("complete intermediate Squad worker task: %v", err)
	}
	assertIntermediateSquadProjection(t, pool, fixture)
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'completed', completed_at = now()
		WHERE issue_id = $1
		  AND context->>'type' = 'squad_sop_leader_continuation'
	`, fixture.issueID); err != nil {
		t.Fatalf("complete leader continuation before replay: %v", err)
	}

	// The already-finalized replay must repair missing state if necessary but
	// cannot duplicate either the automatic event or the leader continuation,
	// even after the original continuation is no longer pending.
	if _, err := service.CompleteTask(ctx, fixture.taskID, []byte(`{"output":"replayed"}`), "", ""); err != nil {
		t.Fatalf("replay intermediate Squad worker completion: %v", err)
	}
	assertIntermediateSquadProjection(t, pool, fixture)
}

func TestFailTaskRepairsProjectionWhenAutomaticEventAlreadyExists(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "failed", time.Now().Add(-time.Minute), 1, 1)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO squad_sop_step_event (
			run_id, workspace_id, issue_id, squad_id,
			step_key, step_name, role_key, event_type, status,
			reason, created_by_type, task_id
		)
		SELECT id, workspace_id, issue_id, squad_id,
		       'worker', 'Worker', 'worker', '步骤失败', '已失败',
		       'pre-existing automatic event', 'system', $2
		FROM squad_sop_run
		WHERE id = $1
	`, fixture.runID, fixture.taskID); err != nil {
		t.Fatalf("seed existing automatic failure event: %v", err)
	}

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	task, err := service.FailTask(ctx, fixture.taskID, "ignored replay error", "", "", "agent_error")
	if err != nil {
		t.Fatalf("reconcile already-failed task: %v", err)
	}
	if task == nil || task.Status != "failed" {
		t.Fatalf("replayed task = %+v, want failed", task)
	}
	assertSquadTerminalProjectionCommitted(t, pool, fixture, "已失败", "blocked", "步骤失败")
}

func TestFailTaskWithRetryLeavesSquadRunOpen(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "running", time.Now().Add(-time.Minute), 1, 2)
	ctx := context.Background()

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	if _, err := service.FailTask(ctx, fixture.taskID, "runtime unavailable", "", "", "runtime_offline"); err != nil {
		t.Fatalf("fail retryable Squad worker task: %v", err)
	}

	var runStatus, issueStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM squad_sop_run WHERE id = $1`, fixture.runID).Scan(&runStatus); err != nil {
		t.Fatalf("read retryable run status: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, fixture.issueID).Scan(&issueStatus); err != nil {
		t.Fatalf("read retryable issue status: %v", err)
	}
	var terminalEvents, retries int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM squad_sop_step_event
		WHERE task_id = $1 AND event_type IN ('步骤完成', '步骤失败')
	`, fixture.taskID).Scan(&terminalEvents); err != nil {
		t.Fatalf("count retryable terminal events: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE parent_task_id = $1 AND status = 'queued'
	`, fixture.taskID).Scan(&retries); err != nil {
		t.Fatalf("count retry tasks: %v", err)
	}
	if runStatus != "进行中" || issueStatus != "in_progress" || terminalEvents != 0 || retries != 1 {
		t.Fatalf(
			"retry projection = run:%s issue:%s terminal_events:%d retries:%d, want 进行中/in_progress/0/1",
			runStatus,
			issueStatus,
			terminalEvents,
			retries,
		)
	}
}

func TestFailStaleTaskWithDeliveryAndRetryLeavesSquadRunOpen(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "running", time.Now().Add(-2*time.Hour), 1, 2)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO comment (
			issue_id, workspace_id, author_type, author_id, content, type, source_task_id
		) VALUES ($1, $2, 'agent', $3, 'delivery written before the worker became stale', 'comment', $4)
	`, fixture.issueID, fixture.workspaceID, fixture.workerID, fixture.taskID); err != nil {
		t.Fatalf("seed stale task delivery comment: %v", err)
	}

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	failed, err := service.FailStaleTasks(ctx, db.FailStaleTasksParams{
		DispatchTimeoutSecs: 60,
		RunningTimeoutSecs:  60,
	})
	if err != nil {
		t.Fatalf("sweep retryable stale Squad worker task: %v", err)
	}
	if len(failed) != 1 || failed[0].ID != fixture.taskID {
		t.Fatalf("failed tasks = %+v, want only %s", failed, uuid.UUID(fixture.taskID.Bytes))
	}

	var runStatus, issueStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM squad_sop_run WHERE id = $1`, fixture.runID).Scan(&runStatus); err != nil {
		t.Fatalf("read retryable stale run status: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, fixture.issueID).Scan(&issueStatus); err != nil {
		t.Fatalf("read retryable stale issue status: %v", err)
	}
	var terminalEvents, retries, leaderContinuations int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM squad_sop_step_event
		WHERE task_id = $1 AND event_type IN ('步骤完成', '步骤失败')
	`, fixture.taskID).Scan(&terminalEvents); err != nil {
		t.Fatalf("count retryable stale terminal events: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE parent_task_id = $1 AND status = 'queued'
	`, fixture.taskID).Scan(&retries); err != nil {
		t.Fatalf("count retryable stale child tasks: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND context->>'type' = 'squad_sop_leader_continuation'
	`, fixture.issueID).Scan(&leaderContinuations); err != nil {
		t.Fatalf("count retryable stale leader continuations: %v", err)
	}
	if runStatus != "进行中" || issueStatus != "in_progress" || terminalEvents != 0 || retries != 1 || leaderContinuations != 0 {
		t.Fatalf(
			"retryable stale projection = run:%s issue:%s terminal_events:%d retries:%d leader_continuations:%d, want 进行中/in_progress/0/1/0",
			runStatus,
			issueStatus,
			terminalEvents,
			retries,
			leaderContinuations,
		)
	}
}

func TestConcurrentWorkerCompletionsCreateOneLeaderContinuation(t *testing.T) {
	pool := openSquadTerminalProjectionTestPool(t)
	fixture := seedSquadTerminalProjectionFixture(t, pool, "running", time.Now().Add(-time.Minute), 1, 1)
	ctx := context.Background()
	profile := `{"key":"test","steps":[{"key":"worker","name":"Worker","role_key":"worker"},{"key":"review","name":"Review","role_key":"review"}]}`
	if _, err := pool.Exec(ctx, `UPDATE squad_sop_run SET profile = $2::jsonb WHERE id = $1`, fixture.runID, profile); err != nil {
		t.Fatalf("make concurrent worker step intermediate: %v", err)
	}
	secondTaskID := testPGUUID()
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue (
			id, agent_id, issue_id, status, runtime_id, dispatched_at,
			started_at, attempt, max_attempts, is_leader_task
		)
		SELECT $1, agent_id, issue_id, 'running', runtime_id, now(), now(), 1, 1, false
		FROM agent_task_queue
		WHERE id = $2
	`, secondTaskID, fixture.taskID); err != nil {
		t.Fatalf("create concurrent worker task: %v", err)
	}

	service := NewTaskService(db.New(pool), pool, nil, events.New())
	taskIDs := []pgtype.UUID{fixture.taskID, secondTaskID}
	errs := make([]error, len(taskIDs))
	var wg sync.WaitGroup
	for index, taskID := range taskIDs {
		wg.Add(1)
		go func(index int, taskID pgtype.UUID) {
			defer wg.Done()
			_, errs[index] = service.CompleteTask(ctx, taskID, []byte(`{"output":"worker complete"}`), "", "")
		}(index, taskID)
	}
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent completion %d: %v", index, err)
		}
	}

	assertIntermediateSquadProjection(t, pool, fixture)
	var completedEvents int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM squad_sop_step_event
		WHERE run_id = $1
		  AND task_id = ANY($2::uuid[])
		  AND event_type = '步骤完成'
		  AND created_by_type = 'system'
	`, fixture.runID, []uuid.UUID{uuid.UUID(fixture.taskID.Bytes), uuid.UUID(secondTaskID.Bytes)}).Scan(&completedEvents); err != nil {
		t.Fatalf("count concurrent completion events: %v", err)
	}
	if completedEvents != 2 {
		t.Fatalf("concurrent completion events = %d, want 2", completedEvents)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'completed', completed_at = now()
		WHERE issue_id = $1
		  AND context->>'type' = 'squad_sop_leader_continuation'
	`, fixture.issueID); err != nil {
		t.Fatalf("complete coalesced leader continuation: %v", err)
	}
	for _, taskID := range taskIDs {
		if _, err := service.CompleteTask(ctx, taskID, nil, "", ""); err != nil {
			t.Fatalf("replay coalesced worker completion: %v", err)
		}
	}
	assertIntermediateSquadProjection(t, pool, fixture)
}

func openSquadTerminalProjectionTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set; skipping live PostgreSQL transaction test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Skipf("connect to PostgreSQL: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("ping PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedSquadTerminalProjectionFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	taskStatus string,
	startedAt time.Time,
	attempt int32,
	maxAttempts int32,
) squadTerminalProjectionFixture {
	t.Helper()
	ctx := context.Background()
	fixture := squadTerminalProjectionFixture{
		workspaceID: testPGUUID(),
		issueID:     testPGUUID(),
		taskID:      testPGUUID(),
		runID:       testPGUUID(),
		workerID:    testPGUUID(),
		leaderID:    testPGUUID(),
	}
	runtimeID := testPGUUID()
	squadID := testPGUUID()
	creatorID := testPGUUID()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture transaction: %v", err)
	}
	defer tx.Rollback(ctx)
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed Squad terminal projection fixture: %v", err)
		}
	}
	mustExec(`
		INSERT INTO workspace (id, name, slug, issue_prefix)
		VALUES ($1, $2, $3, $4)
	`, fixture.workspaceID, "SOP projection "+suffix, "sop-projection-"+suffix, "SP"+strings.ToUpper(suffix[:4]))
	mustExec(`
		INSERT INTO agent_runtime (
			id, workspace_id, name, runtime_mode, provider, status, scope
		) VALUES ($1, $2, $3, 'local', 'codex', 'online', 'workspace')
	`, runtimeID, fixture.workspaceID, "Runtime "+suffix)
	mustExec(`
		INSERT INTO agent (
			id, workspace_id, name, runtime_mode, runtime_config, scope,
			status, runtime_id
		) VALUES
			($1, $3, 'Leader', 'local', '{"internal_squad":{"role_key":"pm"}}', 'workspace', 'idle', $4),
			($2, $3, 'Worker', 'local', '{"internal_squad":{"role_key":"worker"}}', 'workspace', 'working', $4)
	`, fixture.leaderID, fixture.workerID, fixture.workspaceID, runtimeID)
	mustExec(`
		INSERT INTO squad (
			id, workspace_id, name, leader_id, creator_id, scope, sop_profile
		) VALUES (
			$1, $2, $3, $4, $5, 'workspace',
			'{"key":"test","steps":[{"key":"worker","name":"Worker","role_key":"worker"}]}'
		)
	`, squadID, fixture.workspaceID, "Squad "+suffix, fixture.leaderID, creatorID)
	mustExec(`
		INSERT INTO issue (
			id, workspace_id, title, status, priority, assignee_type,
			assignee_id, creator_type, creator_id, number
		) VALUES ($1, $2, $3, 'in_progress', 'medium', 'squad', $4, 'member', $5, 1)
	`, fixture.issueID, fixture.workspaceID, "Terminal projection "+suffix, squadID, creatorID)
	mustExec(`
		INSERT INTO agent_task_queue (
			id, agent_id, issue_id, status, runtime_id, dispatched_at,
			started_at, completed_at, result, attempt, max_attempts, is_leader_task
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$6, CASE WHEN $4 IN ('completed', 'failed') THEN $7::timestamptz ELSE NULL END,
			CASE WHEN $4 = 'completed' THEN '{"output":"stage complete"}'::jsonb ELSE NULL END,
			$8, $9, false
		)
	`, fixture.taskID, fixture.workerID, fixture.issueID, taskStatus, runtimeID, startedAt, time.Now(), attempt, maxAttempts)
	mustExec(`
		INSERT INTO squad_sop_run (
			id, workspace_id, issue_id, squad_id, profile_key, profile,
			status, current_step_key
		) VALUES (
			$1, $2, $3, $4, 'test',
			'{"key":"test","steps":[{"key":"worker","name":"Worker","role_key":"worker"}]}',
			'进行中', 'worker'
		)
	`, fixture.runID, fixture.workspaceID, fixture.issueID, squadID)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit fixture transaction: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, fixture.workspaceID); err != nil {
			t.Errorf("clean Squad terminal projection fixture: %v", err)
		}
	})
	return fixture
}

func attachGongfengProjectToSquadProjectionIssue(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture squadTerminalProjectionFixture,
) {
	t.Helper()
	ctx := context.Background()
	projectID := testPGUUID()
	if _, err := pool.Exec(ctx, `
		INSERT INTO project (id, workspace_id, title, status, priority, scope)
		VALUES ($1, $2, 'SOP atomic MR gate', 'in_progress', 'medium', 'workspace')
	`, projectID, fixture.workspaceID); err != nil {
		t.Fatalf("create Gongfeng project: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_resource (project_id, workspace_id, resource_type, resource_ref)
		VALUES ($1, $2, 'gongfeng_repo', '{"project_path":"example/repo"}')
	`, projectID, fixture.workspaceID); err != nil {
		t.Fatalf("create Gongfeng project resource: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET project_id = $2 WHERE id = $1`, fixture.issueID, projectID); err != nil {
		t.Fatalf("attach Gongfeng project to issue: %v", err)
	}
}

func installSquadTerminalProjectionFailureTrigger(
	t *testing.T,
	pool *pgxpool.Pool,
	taskID pgtype.UUID,
	eventType string,
) {
	t.Helper()
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := pgx.Identifier{"fail_squad_terminal_projection_" + suffix}.Sanitize()
	triggerName := pgx.Identifier{"fail_squad_terminal_projection_" + suffix}.Sanitize()
	taskIDText := uuid.UUID(taskID.Bytes).String()
	createFunction := fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			IF NEW.task_id = %s::uuid AND NEW.event_type = %s THEN
				RAISE EXCEPTION 'injected Squad terminal projection failure';
			END IF;
			RETURN NEW;
		END
		$body$
	`, functionName, quoteSQLLiteral(taskIDText), quoteSQLLiteral(eventType))
	if _, err := pool.Exec(ctx, createFunction); err != nil {
		t.Fatalf("create failure injection function: %v", err)
	}
	createTrigger := fmt.Sprintf(`
		CREATE TRIGGER %s
		BEFORE INSERT ON squad_sop_step_event
		FOR EACH ROW EXECUTE FUNCTION %s()
	`, triggerName, functionName)
	if _, err := pool.Exec(ctx, createTrigger); err != nil {
		_, _ = pool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
		t.Fatalf("create failure injection trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON squad_sop_step_event`, triggerName))
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})
}

func installMissingMRGateCommentFailureTrigger(
	t *testing.T,
	pool *pgxpool.Pool,
	taskID pgtype.UUID,
) {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := pgx.Identifier{"fail_missing_mr_comment_" + suffix}.Sanitize()
	triggerName := pgx.Identifier{"fail_missing_mr_comment_" + suffix}.Sanitize()
	taskIDText := uuid.UUID(taskID.Bytes).String()
	createFunction := fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			IF NEW.source_task_id = %s::uuid
			   AND NEW.author_type = 'system'
			   AND NEW.content LIKE '%%平台还没有关联 MR%%' THEN
				RAISE EXCEPTION 'injected missing-MR comment failure';
			END IF;
			RETURN NEW;
		END
		$body$
	`, functionName, quoteSQLLiteral(taskIDText))
	installProjectionFailureTrigger(t, pool, functionName, triggerName, createFunction, "comment")
}

func installLeaderContinuationFailureTrigger(
	t *testing.T,
	pool *pgxpool.Pool,
	issueID pgtype.UUID,
) {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := pgx.Identifier{"fail_squad_leader_continuation_" + suffix}.Sanitize()
	triggerName := pgx.Identifier{"fail_squad_leader_continuation_" + suffix}.Sanitize()
	issueIDText := uuid.UUID(issueID.Bytes).String()
	createFunction := fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			IF NEW.issue_id = %s::uuid AND NEW.is_leader_task THEN
				RAISE EXCEPTION 'injected Squad leader continuation failure';
			END IF;
			RETURN NEW;
		END
		$body$
	`, functionName, quoteSQLLiteral(issueIDText))
	installProjectionFailureTrigger(t, pool, functionName, triggerName, createFunction, "agent_task_queue")
}

func installProjectionFailureTrigger(
	t *testing.T,
	pool *pgxpool.Pool,
	functionName string,
	triggerName string,
	createFunction string,
	tableName string,
) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, createFunction); err != nil {
		t.Fatalf("create projection failure function: %v", err)
	}
	tableIdentifier := pgx.Identifier{tableName}.Sanitize()
	createTrigger := fmt.Sprintf(`
		CREATE TRIGGER %s
		BEFORE INSERT ON %s
		FOR EACH ROW EXECUTE FUNCTION %s()
	`, triggerName, tableIdentifier, functionName)
	if _, err := pool.Exec(ctx, createTrigger); err != nil {
		_, _ = pool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
		t.Fatalf("create projection failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s`, triggerName, tableIdentifier))
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})
}

func assertSquadTerminalProjectionRolledBack(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture squadTerminalProjectionFixture,
	wantTaskStatus string,
) {
	t.Helper()
	ctx := context.Background()
	var taskStatus, runStatus, issueStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, fixture.taskID).Scan(&taskStatus); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM squad_sop_run WHERE id = $1`, fixture.runID).Scan(&runStatus); err != nil {
		t.Fatalf("read run status: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, fixture.issueID).Scan(&issueStatus); err != nil {
		t.Fatalf("read issue status: %v", err)
	}
	if taskStatus != wantTaskStatus || runStatus != "进行中" || issueStatus != "in_progress" {
		t.Fatalf(
			"state after projection failure = task:%s run:%s issue:%s, want %s/进行中/in_progress",
			taskStatus,
			runStatus,
			issueStatus,
			wantTaskStatus,
		)
	}
	var eventsCount, commentsCount, leaderTasksCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM squad_sop_step_event WHERE task_id = $1`, fixture.taskID).Scan(&eventsCount); err != nil {
		t.Fatalf("count step events: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comment WHERE source_task_id = $1`, fixture.taskID).Scan(&commentsCount); err != nil {
		t.Fatalf("count projected comments: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND status IN ('queued', 'dispatched')
	`, fixture.issueID, fixture.leaderID).Scan(&leaderTasksCount); err != nil {
		t.Fatalf("count leader continuation tasks: %v", err)
	}
	if eventsCount != 0 || commentsCount != 0 || leaderTasksCount != 0 {
		t.Fatalf(
			"partial projection survived rollback: events=%d comments=%d leader_tasks=%d",
			eventsCount,
			commentsCount,
			leaderTasksCount,
		)
	}
}

func assertSquadTerminalProjectionCommitted(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture squadTerminalProjectionFixture,
	wantRunStatus string,
	wantIssueStatus string,
	eventType string,
) {
	t.Helper()
	ctx := context.Background()
	var runStatus, issueStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM squad_sop_run WHERE id = $1`, fixture.runID).Scan(&runStatus); err != nil {
		t.Fatalf("read run status: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, fixture.issueID).Scan(&issueStatus); err != nil {
		t.Fatalf("read issue status: %v", err)
	}
	var eventCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM squad_sop_step_event
		WHERE run_id = $1 AND task_id = $2 AND event_type = $3 AND created_by_type = 'system'
	`, fixture.runID, fixture.taskID, eventType).Scan(&eventCount); err != nil {
		t.Fatalf("count automatic terminal events: %v", err)
	}
	if runStatus != wantRunStatus || issueStatus != wantIssueStatus || eventCount != 1 {
		t.Fatalf(
			"projected state = run:%s issue:%s events:%d, want %s/%s/1",
			runStatus,
			issueStatus,
			eventCount,
			wantRunStatus,
			wantIssueStatus,
		)
	}
}

func assertIntermediateSquadProjection(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture squadTerminalProjectionFixture,
) {
	t.Helper()
	ctx := context.Background()
	var runStatus, currentStep, issueStatus string
	if err := pool.QueryRow(ctx, `
		SELECT status, current_step_key FROM squad_sop_run WHERE id = $1
	`, fixture.runID).Scan(&runStatus, &currentStep); err != nil {
		t.Fatalf("read intermediate run state: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, fixture.issueID).Scan(&issueStatus); err != nil {
		t.Fatalf("read intermediate issue state: %v", err)
	}
	var eventCount, leaderTaskCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM squad_sop_step_event
		WHERE run_id = $1 AND task_id = $2 AND event_type = '步骤完成' AND created_by_type = 'system'
	`, fixture.runID, fixture.taskID).Scan(&eventCount); err != nil {
		t.Fatalf("count intermediate completion events: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1
		  AND agent_id = $2
		  AND context->>'type' = 'squad_sop_leader_continuation'
	`, fixture.issueID, fixture.leaderID).Scan(&leaderTaskCount); err != nil {
		t.Fatalf("count leader continuation tasks: %v", err)
	}
	if runStatus != "进行中" || currentStep != "review" || issueStatus != "in_progress" || eventCount != 1 || leaderTaskCount != 1 {
		t.Fatalf(
			"intermediate projection = run:%s step:%s issue:%s events:%d leader_tasks:%d, want 进行中/review/in_progress/1/1",
			runStatus,
			currentStep,
			issueStatus,
			eventCount,
			leaderTaskCount,
		)
	}
}

func testPGUUID() pgtype.UUID {
	id := uuid.New()
	return pgtype.UUID{Bytes: id, Valid: true}
}

func quoteSQLLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
