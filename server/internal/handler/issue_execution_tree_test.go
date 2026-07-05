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

func TestSummarizeIssueTimelineComputesHumanConfirmationRemainder(t *testing.T) {
	workStartedAt := "2026-06-09T10:00:00Z"
	workCompletedAt := "2026-06-09T10:10:00Z"
	summary := summarizeIssueTimeline(IssueResponse{
		ID:              "issue-1",
		WorkStartedAt:   &workStartedAt,
		WorkCompletedAt: &workCompletedAt,
	}, []IssueTimelineNodeResponse{
		{
			NodeID:      "task:1",
			NodeType:    "agent_task",
			Status:      "completed",
			StartedAt:   "2026-06-09T10:00:00Z",
			CompletedAt: "2026-06-09T10:04:00Z",
			DurationMs:  240000,
		},
		{
			NodeID:      "task:2",
			NodeType:    "agent_task",
			Status:      "completed",
			StartedAt:   "2026-06-09T10:03:00Z",
			CompletedAt: "2026-06-09T10:06:00Z",
			DurationMs:  180000,
		},
	})

	if summary.WallClockDurationMs == nil || *summary.WallClockDurationMs != 600000 {
		t.Fatalf("wall clock = %v, want 600000", summary.WallClockDurationMs)
	}
	if summary.AgentExecutionDurationMs != 360000 {
		t.Fatalf("agent execution = %d, want merged 360000", summary.AgentExecutionDurationMs)
	}
	if summary.HumanConfirmationDurationMs == nil || *summary.HumanConfirmationDurationMs != 240000 {
		t.Fatalf("human confirmation = %v, want 240000", summary.HumanConfirmationDurationMs)
	}
	if summary.TotalDurationMs != 600000 {
		t.Fatalf("total duration = %d, want agent + waiting 600000", summary.TotalDurationMs)
	}
}

func TestSummarizeIssueTimelineFallsBackToAgentTaskBoundsWithoutWorkCycle(t *testing.T) {
	summary := summarizeIssueTimeline(IssueResponse{ID: "issue-1"}, []IssueTimelineNodeResponse{
		{
			NodeID:      "task:1",
			NodeType:    "agent_task",
			Status:      "completed",
			StartedAt:   "2026-06-09T10:00:00Z",
			CompletedAt: "2026-06-09T10:01:00Z",
			DurationMs:  60000,
		},
	})

	if summary.WorkStartedAt != "2026-06-09T10:00:00Z" || summary.WorkCompletedAt != "2026-06-09T10:01:00Z" {
		t.Fatalf("work bounds = %q / %q, want task bounds", summary.WorkStartedAt, summary.WorkCompletedAt)
	}
	if summary.WallClockDurationMs == nil || *summary.WallClockDurationMs != 60000 {
		t.Fatalf("wall clock = %v, want 60000", summary.WallClockDurationMs)
	}
	if summary.AgentExecutionDurationMs != 60000 {
		t.Fatalf("agent execution = %d, want 60000", summary.AgentExecutionDurationMs)
	}
	if summary.HumanConfirmationDurationMs == nil || *summary.HumanConfirmationDurationMs != 0 {
		t.Fatalf("human confirmation = %v, want 0", summary.HumanConfirmationDurationMs)
	}
	if summary.TotalDurationMs != 60000 {
		t.Fatalf("total duration = %d, want agent duration 60000", summary.TotalDurationMs)
	}
}

func TestBuildIssueTimelineNodesAddsHumanConfirmationWait(t *testing.T) {
	root := IssueExecutionNodeResponse{
		Issue: IssueResponse{ID: "issue-1"},
		Tasks: []AgentTaskResponse{
			{
				ID:          "task-1",
				AgentID:     "agent-pm",
				Status:      "completed",
				StartedAt:   timelineTestStringPtr("2026-06-09T10:00:00Z"),
				CompletedAt: timelineTestStringPtr("2026-06-09T10:02:00Z"),
				CreatedAt:   "2026-06-09T10:00:00Z",
			},
			{
				ID:                    "task-2",
				AgentID:               "agent-pm",
				Status:                "completed",
				StartedAt:             timelineTestStringPtr("2026-06-09T10:12:00Z"),
				CompletedAt:           timelineTestStringPtr("2026-06-09T10:14:00Z"),
				CreatedAt:             "2026-06-09T10:12:00Z",
				TriggerCommentID:      timelineTestStringPtr("comment-1"),
				TriggerAuthorType:     "member",
				TriggerAuthorName:     "Alice",
				TriggerCommentContent: "确认继续",
			},
		},
		ManualComments: []IssueCommentBrief{
			{
				ID:         "comment-1",
				IssueID:    "issue-1",
				AuthorType: "member",
				Type:       "comment",
				Content:    "确认继续",
				CreatedAt:  "2026-06-09T10:10:00Z",
			},
		},
	}

	nodes := buildIssueTimelineNodes(root)
	var waitNode IssueTimelineNodeResponse
	for _, node := range nodes {
		if node.NodeType == "human_confirmation" {
			waitNode = node
			break
		}
	}

	if waitNode.NodeID != "human_confirmation:comment-1:task-2" {
		t.Fatalf("human confirmation node = %+v, want comment-triggered wait", waitNode)
	}
	if waitNode.StartedAt != "2026-06-09T10:02:00Z" || waitNode.CompletedAt != "2026-06-09T10:12:00Z" {
		t.Fatalf("human confirmation bounds = %q / %q", waitNode.StartedAt, waitNode.CompletedAt)
	}
	if waitNode.DurationMs != 600000 {
		t.Fatalf("human confirmation duration = %d, want 600000", waitNode.DurationMs)
	}
	refs := map[string]bool{}
	for _, ref := range waitNode.EvidenceRefs {
		refs[ref.Type+":"+ref.ID] = true
	}
	for _, want := range []string{"agent_task:task-1", "comment:comment-1", "agent_task:task-2"} {
		if !refs[want] {
			t.Fatalf("human confirmation evidence missing %s: %+v", want, waitNode.EvidenceRefs)
		}
	}
}

func TestSummarizeIssueTimelineUsesExplicitHumanConfirmationNodes(t *testing.T) {
	workStartedAt := "2026-06-09T10:00:00Z"
	workCompletedAt := "2026-06-09T10:20:00Z"
	summary := summarizeIssueTimeline(IssueResponse{
		ID:              "issue-1",
		WorkStartedAt:   &workStartedAt,
		WorkCompletedAt: &workCompletedAt,
	}, []IssueTimelineNodeResponse{
		{
			NodeID:      "task:1",
			NodeType:    "agent_task",
			Status:      "completed",
			StartedAt:   "2026-06-09T10:00:00Z",
			CompletedAt: "2026-06-09T10:02:00Z",
			DurationMs:  120000,
		},
		{
			NodeID:      "task:2",
			NodeType:    "agent_task",
			Status:      "completed",
			StartedAt:   "2026-06-09T10:12:00Z",
			CompletedAt: "2026-06-09T10:14:00Z",
			DurationMs:  120000,
		},
		{
			NodeID:      "human_confirmation:comment-1:task-2",
			NodeType:    "human_confirmation",
			Status:      "completed",
			StartedAt:   "2026-06-09T10:02:00Z",
			CompletedAt: "2026-06-09T10:12:00Z",
			DurationMs:  600000,
		},
	})

	if summary.WallClockDurationMs == nil || *summary.WallClockDurationMs != 1200000 {
		t.Fatalf("wall clock = %v, want 1200000", summary.WallClockDurationMs)
	}
	if summary.AgentExecutionDurationMs != 240000 {
		t.Fatalf("agent execution = %d, want 240000", summary.AgentExecutionDurationMs)
	}
	if summary.HumanConfirmationDurationMs == nil || *summary.HumanConfirmationDurationMs != 600000 {
		t.Fatalf("human confirmation = %v, want explicit 600000", summary.HumanConfirmationDurationMs)
	}
	if summary.TotalDurationMs != 840000 {
		t.Fatalf("total duration = %d, want agent + explicit waiting 840000", summary.TotalDurationMs)
	}
}

func TestSummarizeIssueTimelineIgnoresLowLevelFailureForAcceptanceAndTotals(t *testing.T) {
	summary := summarizeIssueTimeline(IssueResponse{ID: "issue-1"}, []IssueTimelineNodeResponse{
		{
			NodeID:          "task:1",
			NodeType:        "agent_task",
			Status:          "completed",
			StartedAt:       "2026-06-09T10:00:00Z",
			CompletedAt:     "2026-06-09T10:02:00Z",
			DurationMs:      120000,
			MessageCount:    3,
			AgentTurnCount:  2,
			TraceEventCount: 4,
		},
		{
			NodeID:          "trace:1",
			NodeType:        "evidence",
			Status:          "failed",
			StartedAt:       "2026-06-09T10:01:00Z",
			CompletedAt:     "2026-06-09T10:01:10Z",
			DurationMs:      10000,
			MessageCount:    1,
			TraceEventCount: 1,
			Summary:         "工具输出包含失败字样",
		},
	})

	if summary.AcceptanceStatus != "completed" || summary.FailureSummary != "" {
		t.Fatalf("acceptance = %q failure = %q, want completed without low-level failure", summary.AcceptanceStatus, summary.FailureSummary)
	}
	if summary.TotalDurationMs != 120000 {
		t.Fatalf("total duration = %d, want agent duration only", summary.TotalDurationMs)
	}
	if summary.MessageCount != 3 || summary.AgentTurnCount != 2 || summary.TraceEventCount != 4 {
		t.Fatalf("counts = messages %d turns %d traces %d, want task counts only", summary.MessageCount, summary.AgentTurnCount, summary.TraceEventCount)
	}
}

func TestSummarizeIssueTimelineIgnoresRecoveredRuntimeFailureForDoneIssue(t *testing.T) {
	summary := summarizeIssueTimeline(IssueResponse{ID: "issue-1", Status: "done"}, []IssueTimelineNodeResponse{
		{
			NodeID:        "task:pm-recovered",
			NodeType:      "agent_task",
			Status:        "failed",
			FailureReason: "runtime_recovery",
			StartedAt:     "2026-06-09T10:00:00Z",
			CompletedAt:   "2026-06-09T10:01:00Z",
			DurationMs:    60000,
			Summary:       "daemon restarted while task was in flight",
		},
		{
			NodeID:      "task:verify",
			NodeType:    "agent_task",
			Status:      "completed",
			StartedAt:   "2026-06-09T10:02:00Z",
			CompletedAt: "2026-06-09T10:04:00Z",
			DurationMs:  120000,
			Summary:     "verify done",
		},
	})

	if summary.AcceptanceStatus != "completed" || summary.FailureSummary != "" {
		t.Fatalf("acceptance = %q failure = %q, want recovered runtime failure ignored", summary.AcceptanceStatus, summary.FailureSummary)
	}
}

func timelineTestStringPtr(value string) *string {
	return &value
}

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
	var commentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type, source_task_id)
		VALUES ($1, $2, 'agent', $3, '阶段产物已上传', 'comment', $4)
		RETURNING id::text
	`, fx.parent.ID, testWorkspaceID, agentID, taskID).Scan(&commentID); err != nil {
		t.Fatalf("create artifact comment: %v", err)
	}
	var attachmentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO attachment (
			workspace_id, issue_id, comment_id, uploader_type, uploader_id,
			filename, url, content_type, size_bytes
		)
		VALUES ($1, $2, $3, 'agent', $4, '01-需求澄清.md', '/uploads/clarify.md', 'text/markdown', 128)
		RETURNING id::text
	`, testWorkspaceID, fx.parent.ID, commentID, agentID).Scan(&attachmentID); err != nil {
		t.Fatalf("create artifact attachment: %v", err)
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
	if len(resp.Root.Artifacts) != 1 {
		t.Fatalf("root artifacts = %+v, want one artifact", resp.Root.Artifacts)
	}
	if resp.Root.Artifacts[0].ID != attachmentID || resp.Root.Artifacts[0].TaskID != taskID || resp.Root.Artifacts[0].CommentID != commentID {
		t.Fatalf("root artifact = %+v, want attachment/task/comment linkage", resp.Root.Artifacts[0])
	}
	if resp.Root.Artifacts[0].Title != "01-需求澄清" || resp.Root.Artifacts[0].Kind != "stage_markdown" {
		t.Fatalf("root artifact title/kind = %+v", resp.Root.Artifacts[0])
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
	var taskNode IssueTimelineNodeResponse
	for _, node := range resp.TimelineNodes {
		if node.NodeID == "task:"+taskID {
			taskNode = node
			break
		}
	}
	if len(taskNode.Artifacts) != 1 || taskNode.Artifacts[0].ID != attachmentID {
		t.Fatalf("task node artifacts = %+v, want uploaded attachment", taskNode.Artifacts)
	}
	hasAttachmentRef := false
	for _, ref := range taskNode.EvidenceRefs {
		if ref.Type == "attachment" && ref.ID == attachmentID && ref.Href != "" {
			hasAttachmentRef = true
			break
		}
	}
	if !hasAttachmentRef {
		t.Fatalf("task node evidence refs = %+v, want attachment ref", taskNode.EvidenceRefs)
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
