package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type IssueExecutionTreeResponse struct {
	Root    IssueExecutionNodeResponse `json:"root"`
	Summary map[string]int             `json:"summary"`
}

type IssueExecutionNodeResponse struct {
	Issue           IssueResponse                             `json:"issue"`
	Tasks           []AgentTaskResponse                       `json:"tasks"`
	SOPRuns         []SquadSOPRunResponse                     `json:"sop_runs"`
	TraceEvents     []TaskTraceEventResponse                  `json:"trace_events"`
	ToolCallSummary []PromptEvaluationToolCallSummaryResponse `json:"tool_call_summary"`
	WakeupComments  []IssueWakeupCommentBrief                 `json:"wakeup_comments"`
	Children        []IssueExecutionNodeResponse              `json:"children"`
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
	writeJSON(w, http.StatusOK, IssueExecutionTreeResponse{
		Root:    root,
		Summary: summarizeIssueExecutionTree(root),
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
	for _, task := range tasks {
		taskResp = append(taskResp, taskToResponse(task, workspaceID))
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
	toolCallSummary := buildPromptEvaluationToolCallSummary(buildPromptEvaluationToolCallChains(taskMessages))

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
		runResp = append(runResp, squadSOPRunToResponse(run, eventResp))
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
		TraceEvents:     traceResp,
		ToolCallSummary: toolCallSummary,
		WakeupComments:  wakeupComments,
		Children:        childrenResp,
	}, nil
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
