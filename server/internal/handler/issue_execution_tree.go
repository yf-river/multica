package handler

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type IssueExecutionTreeResponse struct {
	Root          IssueExecutionNodeResponse   `json:"root"`
	Summary       map[string]int               `json:"summary"`
	TimelineNodes []IssueTimelineNodeResponse  `json:"timeline_nodes"`
	IssueSummary  IssueTimelineSummaryResponse `json:"issue_summary"`
}

type IssueTimelineNodeResponse struct {
	IssueID               string                      `json:"issue_id"`
	RootTaskID            string                      `json:"root_task_id,omitempty"`
	NodeID                string                      `json:"node_id"`
	ParentNodeID          string                      `json:"parent_node_id,omitempty"`
	NodeType              string                      `json:"node_type"`
	AgentID               string                      `json:"agent_id,omitempty"`
	AgentName             string                      `json:"agent_name,omitempty"`
	SquadID               string                      `json:"squad_id,omitempty"`
	ProjectID             string                      `json:"project_id,omitempty"`
	ChildIssueID          string                      `json:"child_issue_id,omitempty"`
	Status                string                      `json:"status"`
	StartedAt             string                      `json:"started_at,omitempty"`
	CompletedAt           string                      `json:"completed_at,omitempty"`
	DurationMs            int64                       `json:"duration_ms"`
	InputTokens           int64                       `json:"input_tokens"`
	OutputTokens          int64                       `json:"output_tokens"`
	CacheReadTokens       int64                       `json:"cache_read_tokens"`
	CacheWriteTokens      int64                       `json:"cache_write_tokens"`
	MessageCount          int                         `json:"message_count"`
	AgentTurnCount        int                         `json:"agent_turn_count"`
	TraceEventCount       int                         `json:"trace_event_count"`
	UsageUnavailableTrace bool                        `json:"usage_unavailable_trace"`
	Summary               string                      `json:"summary"`
	EvidenceRefs          []IssueTimelineEvidenceRef  `json:"evidence_refs"`
	Artifacts             []AgentTaskArtifactResponse `json:"artifacts"`
}

type IssueTimelineEvidenceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Href string `json:"href,omitempty"`
}

type IssueTimelineSummaryResponse struct {
	IssueID               string `json:"issue_id"`
	NodeCount             int    `json:"node_count"`
	TotalDurationMs       int64  `json:"total_duration_ms"`
	TotalInputTokens      int64  `json:"total_input_tokens"`
	TotalOutputTokens     int64  `json:"total_output_tokens"`
	TotalCacheReadTokens  int64  `json:"total_cache_read_tokens"`
	TotalCacheWriteTokens int64  `json:"total_cache_write_tokens"`
	MessageCount          int    `json:"message_count"`
	AgentTurnCount        int    `json:"agent_turn_count"`
	TraceEventCount       int    `json:"trace_event_count"`
	UsageUnavailable      bool   `json:"usage_unavailable"`
	FailureSummary        string `json:"failure_summary,omitempty"`
	AcceptanceStatus      string `json:"acceptance_status"`
	FullAnalysisDeepLink  string `json:"full_analysis_deep_link"`
}

type IssueExecutionNodeResponse struct {
	Issue           IssueResponse                             `json:"issue"`
	Tasks           []AgentTaskResponse                       `json:"tasks"`
	SOPRuns         []SquadSOPRunResponse                     `json:"sop_runs"`
	TaskMessages    []protocol.TaskMessagePayload             `json:"task_messages"`
	TraceEvents     []TaskTraceEventResponse                  `json:"trace_events"`
	ToolCallChains  []PromptEvaluationToolCallChainResponse   `json:"tool_call_chains"`
	ToolCallSummary []PromptEvaluationToolCallSummaryResponse `json:"tool_call_summary"`
	Artifacts       []AgentTaskArtifactResponse               `json:"artifacts"`
	WakeupComments  []IssueWakeupCommentBrief                 `json:"wakeup_comments"`
	Children        []IssueExecutionNodeResponse              `json:"children"`
}

type AgentTaskArtifactResponse struct {
	ID          string `json:"id"`
	TaskID      string `json:"task_id"`
	CommentID   string `json:"comment_id"`
	IssueID     string `json:"issue_id"`
	Filename    string `json:"filename"`
	Title       string `json:"title"`
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	DownloadURL string `json:"download_url"`
	MarkdownURL string `json:"markdown_url"`
	CreatedAt   string `json:"created_at"`
}

type IssueWakeupCommentBrief struct {
	ID         string  `json:"id"`
	IssueID    string  `json:"issue_id"`
	AuthorType string  `json:"author_type"`
	Type       string  `json:"type"`
	Content    string  `json:"content"`
	ParentID   *string `json:"parent_id"`
	CreatedAt  string  `json:"created_at"`
}

// GetIssueExecutionTree returns the cross-issue execution evidence rooted at
// one issue: issue hierarchy, task runs, SOP runs, trace events and child-done
// wakeup comments. It is intentionally read-only and derives from existing
// facts instead of introducing another persistence layer.
func (h *Handler) GetIssueExecutionTree(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	root, err := h.buildIssueExecutionNode(r.Context(), issue, prefix, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build issue execution tree")
		return
	}
	timelineNodes := buildIssueTimelineNodes(root)
	writeJSON(w, http.StatusOK, IssueExecutionTreeResponse{
		Root:          root,
		Summary:       summarizeIssueExecutionTree(root),
		TimelineNodes: timelineNodes,
		IssueSummary:  summarizeIssueTimeline(root.Issue.ID, timelineNodes),
	})
}

const issueExecutionTreeMaxDepth = 2

func (h *Handler) buildIssueExecutionNode(ctx context.Context, issue db.Issue, prefix string, depth int) (IssueExecutionNodeResponse, error) {
	tasks, err := h.Queries.ListTasksByIssue(ctx, issue.ID)
	if err != nil {
		return IssueExecutionNodeResponse{}, err
	}
	taskResp := make([]AgentTaskResponse, 0, len(tasks))
	taskMessages := make([]protocol.TaskMessagePayload, 0)
	workspaceID := uuidToString(issue.WorkspaceID)
	agentNameByID := make(map[string]string)
	for _, task := range tasks {
		resp := taskToResponse(task, workspaceID)
		agentID := uuidToString(task.AgentID)
		if name, ok := agentNameByID[agentID]; ok {
			resp.Agent = &TaskAgentData{ID: agentID, Name: name}
		} else if agent, err := h.Queries.GetAgent(ctx, task.AgentID); err == nil {
			agentNameByID[agentID] = agent.Name
			resp.Agent = &TaskAgentData{ID: agentID, Name: agent.Name}
		}
		taskResp = append(taskResp, resp)
		messages, err := h.Queries.ListTaskMessages(ctx, task.ID)
		if err != nil {
			return IssueExecutionNodeResponse{}, err
		}
		taskID := uuidToString(task.ID)
		issueID := uuidToString(issue.ID)
		for _, message := range messages {
			taskMessages = append(taskMessages, taskMessageToPayload(message, taskID, issueID))
		}
	}
	toolCallChains := buildPromptEvaluationToolCallChains(taskMessages)
	toolCallSummary := buildPromptEvaluationToolCallSummary(toolCallChains)

	runs, err := h.Queries.ListIssueSquadSOPRuns(ctx, db.ListIssueSquadSOPRunsParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return IssueExecutionNodeResponse{}, err
	}
	runResp := make([]SquadSOPRunResponse, 0, len(runs))
	for _, run := range runs {
		events, err := h.Queries.ListSquadSOPStepEventsByRun(ctx, run.ID)
		if err != nil {
			return IssueExecutionNodeResponse{}, err
		}
		eventResp := make([]SquadSOPEventResponse, 0, len(events))
		for _, event := range events {
			eventResp = append(eventResp, squadSOPEventToResponse(event))
		}
		enrichedRun, err := h.squadSOPRunToResponseWithStageMetrics(ctx, run, eventResp)
		if err != nil {
			return IssueExecutionNodeResponse{}, err
		}
		runResp = append(runResp, enrichedRun)
	}

	traces, err := h.Queries.ListIssueTaskTraceEvents(ctx, issue.ID)
	if err != nil {
		return IssueExecutionNodeResponse{}, err
	}
	traceResp := make([]TaskTraceEventResponse, 0, len(traces))
	for _, event := range traces {
		traceResp = append(traceResp, taskTraceEventToResponse(event))
	}

	comments, err := h.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       200,
	})
	if err != nil {
		return IssueExecutionNodeResponse{}, err
	}
	commentIDs := make([]pgtype.UUID, 0, len(comments))
	commentByID := make(map[string]db.Comment, len(comments))
	for _, comment := range comments {
		if comment.SourceTaskID.Valid {
			commentIDs = append(commentIDs, comment.ID)
			commentByID[uuidToString(comment.ID)] = comment
		}
	}
	artifacts := make([]AgentTaskArtifactResponse, 0)
	if len(commentIDs) > 0 {
		attachments, err := h.Queries.ListAttachmentsByCommentIDs(ctx, db.ListAttachmentsByCommentIDsParams{
			Column1:     commentIDs,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return IssueExecutionNodeResponse{}, err
		}
		for _, attachment := range attachments {
			comment, ok := commentByID[uuidToString(attachment.CommentID)]
			if !ok || !comment.SourceTaskID.Valid {
				continue
			}
			artifacts = append(artifacts, h.agentTaskArtifactToResponse(attachment, comment.SourceTaskID, issue.ID))
		}
	}
	wakeupComments := make([]IssueWakeupCommentBrief, 0)
	for _, comment := range comments {
		if comment.AuthorType == "system" && comment.Type == "system" && strings.Contains(comment.Content, "子任务") && strings.Contains(comment.Content, "已完成") {
			wakeupComments = append(wakeupComments, IssueWakeupCommentBrief{
				ID:         uuidToString(comment.ID),
				IssueID:    uuidToString(comment.IssueID),
				AuthorType: comment.AuthorType,
				Type:       comment.Type,
				Content:    comment.Content,
				ParentID:   uuidToPtr(comment.ParentID),
				CreatedAt:  timestampToString(comment.CreatedAt),
			})
		}
	}

	childrenResp := []IssueExecutionNodeResponse{}
	if depth < issueExecutionTreeMaxDepth {
		children, err := h.Queries.ListChildIssues(ctx, issue.ID)
		if err != nil {
			return IssueExecutionNodeResponse{}, err
		}
		childrenResp = make([]IssueExecutionNodeResponse, 0, len(children))
		for _, child := range children {
			childNode, err := h.buildIssueExecutionNode(ctx, child, prefix, depth+1)
			if err != nil {
				return IssueExecutionNodeResponse{}, err
			}
			childrenResp = append(childrenResp, childNode)
		}
	}

	return IssueExecutionNodeResponse{
		Issue:           issueToResponse(issue, prefix),
		Tasks:           taskResp,
		SOPRuns:         runResp,
		TaskMessages:    taskMessages,
		TraceEvents:     traceResp,
		ToolCallChains:  toolCallChains,
		ToolCallSummary: toolCallSummary,
		Artifacts:       artifacts,
		WakeupComments:  wakeupComments,
		Children:        childrenResp,
	}, nil
}

func (h *Handler) agentTaskArtifactToResponse(attachment db.Attachment, taskID, issueID pgtype.UUID) AgentTaskArtifactResponse {
	att := h.attachmentToResponse(attachment)
	return AgentTaskArtifactResponse{
		ID:          att.ID,
		TaskID:      uuidToString(taskID),
		CommentID:   uuidToString(attachment.CommentID),
		IssueID:     uuidToString(issueID),
		Filename:    att.Filename,
		Title:       artifactTitle(att.Filename),
		Kind:        artifactKind(att.Filename, att.ContentType),
		ContentType: att.ContentType,
		SizeBytes:   att.SizeBytes,
		DownloadURL: att.DownloadURL,
		MarkdownURL: att.MarkdownURL,
		CreatedAt:   att.CreatedAt,
	}
}

func summarizeIssueExecutionTree(root IssueExecutionNodeResponse) map[string]int {
	summary := map[string]int{
		"任务数":    0,
		"子任务数":   0,
		"SOP执行数": 0,
		"SOP事件数": 0,
		"观测事件数":  0,
		"工具调用数":  0,
		"异常工具数":  0,
		"唤醒评论数":  0,
		"完成任务数":  0,
		"失败任务数":  0,
		"取消任务数":  0,
	}
	var walk func(node IssueExecutionNodeResponse, isRoot bool)
	walk = func(node IssueExecutionNodeResponse, isRoot bool) {
		if !isRoot {
			summary["子任务数"]++
		}
		summary["任务数"] += len(node.Tasks)
		summary["SOP执行数"] += len(node.SOPRuns)
		summary["观测事件数"] += len(node.TraceEvents)
		for _, tool := range node.ToolCallSummary {
			summary["工具调用数"] += tool.TotalCalls
			summary["异常工具数"] += tool.FailureSignalCalls + tool.MissingResultCalls + tool.OrphanResultCalls
		}
		summary["唤醒评论数"] += len(node.WakeupComments)
		for _, run := range node.SOPRuns {
			summary["SOP事件数"] += len(run.Events)
		}
		for _, task := range node.Tasks {
			switch task.Status {
			case "completed":
				summary["完成任务数"]++
			case "failed":
				summary["失败任务数"]++
			case "cancelled":
				summary["取消任务数"]++
			}
		}
		for _, child := range node.Children {
			walk(child, false)
		}
	}
	walk(root, true)
	return summary
}

func buildIssueTimelineNodes(root IssueExecutionNodeResponse) []IssueTimelineNodeResponse {
	nodes := make([]IssueTimelineNodeResponse, 0)
	rootTaskID := ""
	if len(root.Tasks) > 0 {
		rootTaskID = root.Tasks[0].ID
	}
	messageCounts := map[string]int{}
	agentTurnCounts := map[string]int{}
	for _, chain := range root.ToolCallChains {
		if chain.TaskID == "" {
			continue
		}
		messageCounts[chain.TaskID] += 2
		agentTurnCounts[chain.TaskID]++
	}
	traceByTask := map[string][]TaskTraceEventResponse{}
	for _, event := range root.TraceEvents {
		traceByTask[event.TaskID] = append(traceByTask[event.TaskID], event)
	}
	artifactsByTask := map[string][]AgentTaskArtifactResponse{}
	for _, artifact := range root.Artifacts {
		artifactsByTask[artifact.TaskID] = append(artifactsByTask[artifact.TaskID], artifact)
	}
	for _, task := range root.Tasks {
		taskTraces := traceByTask[task.ID]
		taskArtifacts := artifactsByTask[task.ID]
		node := IssueTimelineNodeResponse{
			IssueID:               root.Issue.ID,
			RootTaskID:            rootTaskID,
			NodeID:                "task:" + task.ID,
			NodeType:              "agent_task",
			AgentID:               task.AgentID,
			Status:                task.Status,
			StartedAt:             firstNonEmpty(ptrString(task.StartedAt), task.CreatedAt),
			CompletedAt:           ptrString(task.CompletedAt),
			DurationMs:            durationFromTraceOrTask(taskTraces, task),
			MessageCount:          messageCounts[task.ID],
			AgentTurnCount:        agentTurnCounts[task.ID],
			TraceEventCount:       len(taskTraces),
			UsageUnavailableTrace: hasUsageUnavailableTrace(taskTraces),
			Summary:               timelineTaskSummary(task),
			EvidenceRefs:          []IssueTimelineEvidenceRef{{Type: "agent_task", ID: task.ID}},
			Artifacts:             taskArtifacts,
		}
		for _, artifact := range taskArtifacts {
			node.EvidenceRefs = append(node.EvidenceRefs, IssueTimelineEvidenceRef{
				Type: "attachment",
				ID:   artifact.ID,
				Href: artifact.DownloadURL,
			})
		}
		if task.Agent != nil {
			node.AgentName = task.Agent.Name
		}
		for _, event := range taskTraces {
			node.InputTokens += event.InputTokens
			node.OutputTokens += event.OutputTokens
			node.CacheReadTokens += event.CacheReadTokens
			node.CacheWriteTokens += event.CacheWriteTokens
			if node.ProjectID == "" {
				node.ProjectID = ptrString(event.ProjectID)
			}
			if node.SquadID == "" {
				node.SquadID = ptrString(event.SquadID)
			}
			if node.AgentName == "" && event.AgentID != "" {
				node.AgentName = event.AgentID
			}
		}
		nodes = append(nodes, node)
	}
	for _, run := range root.SOPRuns {
		runNodeID := "squad_step:" + run.ID
		nodes = append(nodes, IssueTimelineNodeResponse{
			IssueID:         root.Issue.ID,
			RootTaskID:      rootTaskID,
			NodeID:          runNodeID,
			NodeType:        "squad_step",
			SquadID:         run.SquadID,
			Status:          run.Status,
			StartedAt:       run.StartedAt,
			CompletedAt:     ptrString(run.CompletedAt),
			DurationMs:      int64PtrValue(run.TotalDurationMs),
			Summary:         firstNonEmpty(run.CurrentStepKey, run.ProfileKey, "SOP run"),
			EvidenceRefs:    []IssueTimelineEvidenceRef{{Type: "sop_run", ID: run.ID}},
			TraceEventCount: len(run.Events),
		})
		for _, event := range run.Events {
			nodes = append(nodes, IssueTimelineNodeResponse{
				IssueID:         root.Issue.ID,
				RootTaskID:      rootTaskID,
				NodeID:          "squad_step_event:" + event.ID,
				ParentNodeID:    runNodeID,
				NodeType:        "squad_step",
				SquadID:         event.SquadID,
				Status:          event.Status,
				StartedAt:       event.CreatedAt,
				CompletedAt:     event.CreatedAt,
				DurationMs:      int64PtrValue(event.DurationMs),
				Summary:         firstNonEmpty(event.StepName, event.StepKey, event.EventType),
				EvidenceRefs:    []IssueTimelineEvidenceRef{{Type: "sop_step_event", ID: event.ID}},
				TraceEventCount: 1,
			})
		}
	}
	for _, chain := range root.ToolCallChains {
		nodes = append(nodes, IssueTimelineNodeResponse{
			IssueID:      root.Issue.ID,
			RootTaskID:   rootTaskID,
			NodeID:       "tool_call:" + chain.ID,
			ParentNodeID: "task:" + chain.TaskID,
			NodeType:     "tool_call",
			Status:       chain.Status,
			StartedAt:    chain.CreatedAt,
			CompletedAt:  chain.CompletedAt,
			DurationMs:   chain.DurationMs,
			Summary:      chain.Summary,
			EvidenceRefs: []IssueTimelineEvidenceRef{{Type: "tool_call_chain", ID: chain.ID}},
		})
	}
	for _, event := range root.TraceEvents {
		nodeType := "status_change"
		if strings.Contains(event.EventType, "source") || strings.Contains(event.EventType, "fetch") {
			nodeType = "source_fetch"
		}
		nodes = append(nodes, IssueTimelineNodeResponse{
			IssueID:               root.Issue.ID,
			RootTaskID:            rootTaskID,
			NodeID:                "trace:" + event.ID,
			ParentNodeID:          "task:" + event.TaskID,
			NodeType:              nodeType,
			AgentID:               event.AgentID,
			SquadID:               ptrString(event.SquadID),
			ProjectID:             ptrString(event.ProjectID),
			Status:                event.Status,
			StartedAt:             event.CreatedAt,
			CompletedAt:           event.CreatedAt,
			DurationMs:            traceDurationMs(event),
			InputTokens:           event.InputTokens,
			OutputTokens:          event.OutputTokens,
			CacheReadTokens:       event.CacheReadTokens,
			CacheWriteTokens:      event.CacheWriteTokens,
			TraceEventCount:       1,
			UsageUnavailableTrace: event.EventType == "llm.usage_unavailable",
			Summary:               firstNonEmpty(event.EventName, event.EventType, event.FailureReason),
			EvidenceRefs:          []IssueTimelineEvidenceRef{{Type: "trace_event", ID: event.ID}},
		})
	}
	if status, ok := root.Issue.Metadata["source_fetch_status"].(string); ok && status != "" {
		nodes = append(nodes, IssueTimelineNodeResponse{
			IssueID:      root.Issue.ID,
			RootTaskID:   rootTaskID,
			NodeID:       "source_fetch:" + root.Issue.ID,
			NodeType:     "source_fetch",
			Status:       status,
			StartedAt:    stringValue(root.Issue.Metadata["source_fetch_observed_at"]),
			CompletedAt:  stringValue(root.Issue.Metadata["source_fetch_observed_at"]),
			Summary:      firstNonEmpty(stringValue(root.Issue.Metadata["source_fetch_summary"]), stringValue(root.Issue.Metadata["source_fetch_error"]), "Source fetch "+status),
			EvidenceRefs: []IssueTimelineEvidenceRef{{Type: "issue_metadata", ID: root.Issue.ID}},
		})
	}
	for _, child := range root.Children {
		nodes = append(nodes, IssueTimelineNodeResponse{
			IssueID:      root.Issue.ID,
			RootTaskID:   rootTaskID,
			NodeID:       "child_issue_ref:" + child.Issue.ID,
			NodeType:     "child_issue_ref",
			ProjectID:    ptrString(child.Issue.ProjectID),
			ChildIssueID: child.Issue.ID,
			Status:       child.Issue.Status,
			StartedAt:    child.Issue.CreatedAt,
			CompletedAt:  child.Issue.UpdatedAt,
			Summary:      firstNonEmpty(child.Issue.Identifier, child.Issue.Title),
			EvidenceRefs: []IssueTimelineEvidenceRef{{Type: "child_issue", ID: child.Issue.ID, Href: "/issues/" + child.Issue.ID}},
		})
	}
	for _, comment := range root.WakeupComments {
		nodes = append(nodes, IssueTimelineNodeResponse{
			IssueID:      root.Issue.ID,
			RootTaskID:   rootTaskID,
			NodeID:       "approval:" + comment.ID,
			NodeType:     "approval",
			Status:       "completed",
			StartedAt:    comment.CreatedAt,
			CompletedAt:  comment.CreatedAt,
			Summary:      comment.Content,
			EvidenceRefs: []IssueTimelineEvidenceRef{{Type: "comment", ID: comment.ID}},
		})
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		left := firstNonEmpty(nodes[i].StartedAt, nodes[i].CompletedAt, nodes[i].NodeID)
		right := firstNonEmpty(nodes[j].StartedAt, nodes[j].CompletedAt, nodes[j].NodeID)
		if left == right {
			return nodes[i].NodeID < nodes[j].NodeID
		}
		return left < right
	})
	return nodes
}

func summarizeIssueTimeline(issueID string, nodes []IssueTimelineNodeResponse) IssueTimelineSummaryResponse {
	summary := IssueTimelineSummaryResponse{
		IssueID:              issueID,
		NodeCount:            len(nodes),
		AcceptanceStatus:     "unknown",
		FullAnalysisDeepLink: "/issues/" + issueID + "?panel=execution",
	}
	hasTaskUsage := false
	for _, node := range nodes {
		if node.NodeType != "agent_task" {
			continue
		}
		if node.InputTokens+node.OutputTokens+node.CacheReadTokens+node.CacheWriteTokens > 0 {
			hasTaskUsage = true
			break
		}
	}
	for _, node := range nodes {
		summary.TotalDurationMs += node.DurationMs
		if !hasTaskUsage || node.NodeType == "agent_task" {
			summary.TotalInputTokens += node.InputTokens
			summary.TotalOutputTokens += node.OutputTokens
			summary.TotalCacheReadTokens += node.CacheReadTokens
			summary.TotalCacheWriteTokens += node.CacheWriteTokens
		}
		summary.MessageCount += node.MessageCount
		summary.AgentTurnCount += node.AgentTurnCount
		summary.TraceEventCount += node.TraceEventCount
		if node.UsageUnavailableTrace {
			summary.UsageUnavailable = true
		}
		if summary.FailureSummary == "" && (node.Status == "failed" || node.Status == "blocked") {
			summary.FailureSummary = node.Summary
			summary.AcceptanceStatus = node.Status
		}
		if summary.AcceptanceStatus == "unknown" && (node.Status == "completed" || node.Status == "已完成") {
			summary.AcceptanceStatus = "completed"
		}
	}
	return summary
}

func artifactTitle(filename string) string {
	base := strings.TrimSpace(filename)
	if base == "" {
		return "产物"
	}
	if dot := strings.LastIndex(base, "."); dot > 0 {
		return base[:dot]
	}
	return base
}

func artifactKind(filename, contentType string) string {
	lowerName := strings.ToLower(strings.TrimSpace(filename))
	lowerType := strings.ToLower(strings.TrimSpace(contentType))
	if strings.HasSuffix(lowerName, ".md") || strings.Contains(lowerType, "markdown") {
		return "stage_markdown"
	}
	return "agent_attachment"
}

func durationFromTraceOrTask(events []TaskTraceEventResponse, task AgentTaskResponse) int64 {
	var total int64
	for _, event := range events {
		total += traceDurationMs(event)
	}
	if total > 0 {
		return total
	}
	if task.StartedAt != nil && task.CompletedAt != nil {
		start, startErr := time.Parse(time.RFC3339, *task.StartedAt)
		end, endErr := time.Parse(time.RFC3339, *task.CompletedAt)
		if startErr == nil && endErr == nil && end.After(start) {
			return end.Sub(start).Milliseconds()
		}
	}
	return 0
}

func timelineTaskSummary(task AgentTaskResponse) string {
	if summary := ptrString(task.TriggerSummary); summary != "" {
		return summary
	}
	if task.IsLeaderTask {
		if task.Agent != nil && task.Agent.Name != "" {
			return "SOP leader: " + task.Agent.Name
		}
		return "SOP leader task"
	}
	return firstNonEmpty(task.FailureReason, "Agent task "+task.Status)
}

func traceDurationMs(event TaskTraceEventResponse) int64 {
	for _, value := range []*int64{event.DurationMs, event.TotalMs, event.RunMs} {
		if value != nil && *value > 0 {
			return *value
		}
	}
	return 0
}

func hasUsageUnavailableTrace(events []TaskTraceEventResponse) bool {
	for _, event := range events {
		if event.EventType == "llm.usage_unavailable" {
			return true
		}
	}
	return false
}

func int64PtrValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
