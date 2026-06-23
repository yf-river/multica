package handler

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestQuickCreateSquadTaskTraceCarriesSquadAndProject(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	ctx := context.Background()

	var agentID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM agent
		WHERE workspace_id = $1 AND runtime_id IS NOT NULL
		ORDER BY created_at ASC
		LIMIT 1
	`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load test agent: %v", err)
	}

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, $2, '', $3, $4)
		RETURNING id
	`, testWorkspaceID, "quick-create trace squad", agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, description, status)
		VALUES ($1, $2, '', 'planned')
		RETURNING id
	`, testWorkspaceID, "quick-create trace project").Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
	})

	task, err := testHandler.TaskService.EnqueueQuickCreateTask(
		ctx,
		parseUUID(testWorkspaceID),
		parseUUID(testUserID),
		parseUUID(agentID),
		parseUUID(squadID),
		"用中文创建一个用于验证小队 trace 归属的 issue",
		parseUUID(projectID),
		pgtype.UUID{},
		nil,
	)
	if err != nil {
		t.Fatalf("enqueue quick-create task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, uuidToString(task.ID))
	})

	testHandler.TaskService.CaptureTaskUsage(ctx, task, "codex", "gpt-5.3-codex-spark", 11, 7, 0, 0)

	var gotSquadID, gotProjectID string
	if err := testPool.QueryRow(ctx, `
		SELECT squad_id::text, project_id::text
		FROM task_trace_event
		WHERE task_id = $1 AND event_type = 'llm.usage_reported'
		ORDER BY created_at DESC
		LIMIT 1
	`, uuidToString(task.ID)).Scan(&gotSquadID, &gotProjectID); err != nil {
		t.Fatalf("load quick-create trace: %v", err)
	}
	if gotSquadID != squadID || gotProjectID != projectID {
		t.Fatalf("trace attribution squad=%s project=%s, want squad=%s project=%s", gotSquadID, gotProjectID, squadID, projectID)
	}
}
