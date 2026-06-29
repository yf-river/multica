package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestGetIssueExecutionTreeAggregatesHierarchySOPTraceAndWakeups(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	fx := newChildDoneFixture(t, "in_progress")

	var agentID string
	if err := testPool.QueryRow(ctx, `
		SELECT id::text FROM agent
		WHERE workspace_id = $1 AND runtime_id IS NOT NULL
		ORDER BY created_at ASC
		LIMIT 1
	`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load seeded agent: %v", err)
	}

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, sop_profile)
		VALUES ($1, '执行树测试小队', '', $2, $3, '{"profile_key":"execution-tree-test","steps":[{"key":"clarify","name":"需求澄清","role_key":"captain"}]}'::jsonb)
		RETURNING id::text
	`, testWorkspaceID, agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority,
			started_at, completed_at, result, is_leader_task
		)
		VALUES ($1, $2, $3, 'completed', 0, now() - interval '3 seconds', now(), '{"结论":"已完成"}'::jsonb, true)
		RETURNING id::text
	`, agentID, testRuntimeID, fx.parent.ID).Scan(&taskID); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_message (task_id, seq, type, tool, content, input, output, created_at)
		VALUES
			($1, 1, 'tool_use', 'curl-check', '', '{"tool_call_id":"tree-call-1","url":"/health"}'::jsonb, NULL, now() - interval '2 seconds'),
			($1, 2, 'tool_result', 'curl-check', '', '{}'::jsonb, 'Error: HTTP 500 from upstream', now() - interval '1 seconds')
	`, taskID); err != nil {
		t.Fatalf("create task messages: %v", err)
	}

	run, err := testHandler.Queries.CreateSquadSOPRun(ctx, db.CreateSquadSOPRunParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		IssueID:        parseUUID(fx.parent.ID),
		SquadID:        parseUUID(squadID),
		LeaderTaskID:   parseUUID(taskID),
		ProfileKey:     "execution-tree-test",
		Profile:        []byte(`{"profile_key":"execution-tree-test","steps":[{"key":"clarify","name":"需求澄清","role_key":"captain"}]}`),
		Status:         "进行中",
		CurrentStepKey: "clarify",
	})
	if err != nil {
		t.Fatalf("create SOP run: %v", err)
	}
	if _, err := testHandler.Queries.CreateSquadSOPStepEvent(ctx, db.CreateSquadSOPStepEventParams{
		RunID:         run.ID,
		WorkspaceID:   parseUUID(testWorkspaceID),
		IssueID:       parseUUID(fx.parent.ID),
		SquadID:       parseUUID(squadID),
		StepKey:       "clarify",
		StepName:      "需求澄清",
		RoleKey:       "captain",
		EventType:     "步骤完成",
		Status:        "已完成",
		Evidence:      []byte(`{"证据":"执行树接口测试"}`),
		Reason:        "聚合接口需要回读 SOP 证据",
		CreatedByType: "system",
		TaskID:        parseUUID(taskID),
	}); err != nil {
		t.Fatalf("create SOP event: %v", err)
	}

	if _, err := testHandler.Queries.CreateTaskTraceEvent(ctx, db.CreateTaskTraceEventParams{
		WorkspaceID:  parseUUID(testWorkspaceID),
		TaskID:       parseUUID(taskID),
		IssueID:      parseUUID(fx.parent.ID),
		AgentID:      parseUUID(agentID),
		RuntimeID:    parseUUID(testRuntimeID),
		SquadID:      parseUUID(squadID),
		Source:       "squad_sop",
		EventType:    "task.completed",
		EventName:    "任务已完成",
		Status:       "completed",
		Attempt:      1,
		DurationMs:   pgtype.Int8{Int64: 3000, Valid: true},
		Provider:     "codex",
		Model:        "gpt-5.3-codex-spark",
		InputTokens:  36,
		OutputTokens: 19,
		Metadata:     []byte(`{"阶段":"父子协作"}`),
	}); err != nil {
		t.Fatalf("create trace: %v", err)
	}

	updateChildStatus(t, fx.child.ID, "done")

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/"+fx.parent.ID+"/execution-tree", nil)
	req = withURLParam(req, "id", fx.parent.ID)
	testHandler.GetIssueExecutionTree(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetIssueExecutionTree: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp IssueExecutionTreeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode execution tree: %v", err)
	}
	if resp.Root.Issue.ID != fx.parent.ID {
		t.Fatalf("root issue id = %q, want %q", resp.Root.Issue.ID, fx.parent.ID)
	}
	if len(resp.Root.Children) != 1 || resp.Root.Children[0].Issue.ID != fx.child.ID {
		t.Fatalf("children = %+v, want child %s", resp.Root.Children, fx.child.ID)
	}
	if resp.Summary["任务数"] != 1 || resp.Summary["完成任务数"] != 1 {
		t.Fatalf("task summary = %+v, want one completed task", resp.Summary)
	}
	if resp.Summary["SOP执行数"] != 1 || resp.Summary["SOP事件数"] != 1 {
		t.Fatalf("SOP summary = %+v, want one run and one event", resp.Summary)
	}
	if resp.Summary["观测事件数"] != 1 {
		t.Fatalf("trace summary = %+v, want one trace event", resp.Summary)
	}
	if resp.Summary["工具调用数"] != 1 || resp.Summary["异常工具数"] != 1 {
		t.Fatalf("tool summary = %+v, want one tool call with attention", resp.Summary)
	}
	if len(resp.Root.TaskMessages) != 2 {
		t.Fatalf("root task messages = %+v, want persisted task messages", resp.Root.TaskMessages)
	}
	if resp.Root.TaskMessages[0].Type != "tool_use" || resp.Root.TaskMessages[0].Tool != "curl-check" {
		t.Fatalf("first task message = %+v, want curl-check tool_use", resp.Root.TaskMessages[0])
	}
	if resp.Root.TaskMessages[1].Type != "tool_result" || !strings.Contains(resp.Root.TaskMessages[1].Output, "HTTP 500") {
		t.Fatalf("second task message = %+v, want HTTP 500 tool_result", resp.Root.TaskMessages[1])
	}
	if len(resp.Root.ToolCallSummary) != 1 {
		t.Fatalf("root tool summary = %+v, want one row", resp.Root.ToolCallSummary)
	}
	if resp.Root.ToolCallSummary[0].Tool != "curl-check" || resp.Root.ToolCallSummary[0].FailureSignalCalls != 1 || !resp.Root.ToolCallSummary[0].NeedsAttention {
		t.Fatalf("root tool summary row = %+v", resp.Root.ToolCallSummary[0])
	}
	if len(resp.Root.ToolCallChains) != 1 {
		t.Fatalf("root tool chains = %+v, want one chain", resp.Root.ToolCallChains)
	}
	chain := resp.Root.ToolCallChains[0]
	if chain.Tool != "curl-check" || chain.Status != "已配对" || chain.ResultCategory != "异常线索" || !chain.FailureSignal {
		t.Fatalf("root tool chain = %+v, want paired failure signal", chain)
	}
	if chain.UseSeq != 1 || chain.ResultSeq != 2 || chain.FailureReason == "" {
		t.Fatalf("root tool chain seq/failure reason = %+v, want call/result seq and reason", chain)
	}
	if resp.Summary["唤醒评论数"] != 1 || len(resp.Root.WakeupComments) != 1 {
		t.Fatalf("wakeup comments = %+v / %+v, want one", resp.Summary, resp.Root.WakeupComments)
	}
	if !strings.Contains(resp.Root.WakeupComments[0].Content, fx.child.Identifier) {
		t.Fatalf("wakeup comment does not mention child identifier: %s", resp.Root.WakeupComments[0].Content)
	}
	if len(resp.TimelineNodes) == 0 {
		t.Fatalf("timeline_nodes missing")
	}
	nodeTypes := map[string]int{}
	for _, node := range resp.TimelineNodes {
		nodeTypes[node.NodeType]++
		if node.IssueID != fx.parent.ID {
			t.Fatalf("timeline node issue id = %q, want parent %q: %+v", node.IssueID, fx.parent.ID, node)
		}
	}
	for _, nodeType := range []string{"agent_task", "squad_step", "tool_call", "status_change", "child_issue_ref", "approval"} {
		if nodeTypes[nodeType] == 0 {
			t.Fatalf("timeline node type %s missing: %+v", nodeType, nodeTypes)
		}
	}
	var childRef IssueTimelineNodeResponse
	for _, node := range resp.TimelineNodes {
		if node.NodeType == "child_issue_ref" {
			childRef = node
			break
		}
	}
	if childRef.ChildIssueID != fx.child.ID {
		t.Fatalf("child_issue_ref = %+v, want child %s", childRef, fx.child.ID)
	}
	if childRef.TraceEventCount != 0 || childRef.MessageCount != 0 || childRef.AgentTurnCount != 0 {
		t.Fatalf("child_issue_ref must not expand child internals: %+v", childRef)
	}
	if resp.IssueSummary.IssueID != fx.parent.ID || resp.IssueSummary.NodeCount != len(resp.TimelineNodes) {
		t.Fatalf("issue_summary = %+v, timeline nodes = %d", resp.IssueSummary, len(resp.TimelineNodes))
	}
	if resp.IssueSummary.TotalInputTokens < 36 || resp.IssueSummary.TotalOutputTokens < 19 {
		t.Fatalf("issue_summary token totals = %+v, want trace usage included", resp.IssueSummary)
	}
	if resp.IssueSummary.FullAnalysisDeepLink == "" {
		t.Fatalf("issue_summary missing deep link: %+v", resp.IssueSummary)
	}
}
