package handler

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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

	task, err := testHandler.TaskService.EnqueueQuickCreateTask(ctx, service.EnqueueQuickCreateTaskParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		RequesterID: parseUUID(testUserID),
		AgentID:     parseUUID(agentID),
		SquadID:     parseUUID(squadID),
		Prompt:      "用中文创建一个用于验证小队 trace 归属的 issue",
		ProjectID:   parseUUID(projectID),
	})
	if err != nil {
		t.Fatalf("enqueue quick-create task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, uuidToString(task.ID))
	})

	var inputKind, inputSummary, inputSnapshot, contentSHA string
	if err := testPool.QueryRow(ctx, `
		SELECT metadata->>'input_kind', metadata->>'summary', metadata->>'content_snapshot', metadata->>'content_sha256'
		FROM task_trace_event
		WHERE task_id = $1 AND event_type = 'user_input.received'
		ORDER BY created_at DESC
		LIMIT 1
	`, uuidToString(task.ID)).Scan(&inputKind, &inputSummary, &inputSnapshot, &contentSHA); err != nil {
		t.Fatalf("load quick-create user input trace: %v", err)
	}
	if inputKind != "quick_create" || inputSummary != "用中文创建一个用于验证小队 trace 归属的 issue" || inputSnapshot != inputSummary || contentSHA == "" {
		t.Fatalf("quick-create user input trace = kind=%q summary=%q snapshot=%q sha=%q", inputKind, inputSummary, inputSnapshot, contentSHA)
	}

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

func TestIssueTaskUserInputTraceCapturesOriginalIssue(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	ctx := context.Background()

	runtimeID := createClaimReclaimRuntime(t, ctx, "User input trace runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "User input trace agent")
	if _, err := testPool.Exec(ctx, `
		UPDATE issue
		SET title = '用户原始输入 trace 标题', description = '第一段需求描述'
		WHERE id = $1
	`, issueID); err != nil {
		t.Fatalf("update issue fixture: %v", err)
	}

	task, err := testHandler.Queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		AgentID:   parseUUID(agentID),
		RuntimeID: parseUUID(runtimeID),
		IssueID:   parseUUID(issueID),
		Priority:  1,
	})
	if err != nil {
		t.Fatalf("create issue task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM task_trace_event WHERE task_id = $1`, uuidToString(task.ID))
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, uuidToString(task.ID))
	})

	testHandler.TaskService.NotifyTaskEnqueued(ctx, task)

	var inputKind, sourceType, title, snapshot string
	if err := testPool.QueryRow(ctx, `
		SELECT metadata->>'input_kind', metadata->>'source_type', metadata->>'title', metadata->>'content_snapshot'
		FROM task_trace_event
		WHERE task_id = $1 AND event_type = 'user_input.received'
		ORDER BY created_at DESC
		LIMIT 1
	`, uuidToString(task.ID)).Scan(&inputKind, &sourceType, &title, &snapshot); err != nil {
		t.Fatalf("load issue user input trace: %v", err)
	}
	if inputKind != "issue" || sourceType != "issue" || title != "用户原始输入 trace 标题" || snapshot != "用户原始输入 trace 标题\n\n第一段需求描述" {
		t.Fatalf("issue user input trace = kind=%q source=%q title=%q snapshot=%q", inputKind, sourceType, title, snapshot)
	}
}
