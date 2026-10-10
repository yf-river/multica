// 职责定位：构建并返回单条 issue 的「执行证据树」与时间线（GET .../execution-tree）：
// 自 root issue 向下最多 issueExecutionTreeMaxDepth 层的 issue 层级，各层附任务运行、
// SOP 运行、trace 事件、产物与唤醒评论，并归纳出时间线节点与摘要。只读，全部从既有事实推导。
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// issueExecutionTreeResponse 是 GET .../execution-tree 的响应体：以 root 为根的层级树、全树
// 计数摘要、拍平成一条条的 timeline 节点（按时间排序），以及整棵树的汇总统计。
type issueExecutionTreeResponse struct {
	Root          issueExecutionNodeResponse   `json:"root"`
	Summary       map[string]int               `json:"summary"`
	TimelineNodes []issueTimelineNodeResponse  `json:"timeline_nodes"`
	IssueSummary  issueTimelineSummaryResponse `json:"issue_summary"`
}

// issueTimelineNodeResponse 是时间线上一个节点：统一把 task、工具调用、trace 事件、来源抓取、
// 子 issue 引用、唤醒评论、人工确认等来源归一成这种平铺结构，前端按 NodeType 渲染。节点间的
// 父子关系由 ParentNodeID 指向同数组内其它节点表达（跨层级见 ChildIssueID 等定位字段）。
type issueTimelineNodeResponse struct {
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
	FailureReason         string                      `json:"failure_reason,omitempty"`
	StartedAt             string                      `json:"started_at,omitempty"`
	ActualStartedAt       string                      `json:"actual_started_at,omitempty"`
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
	EvidenceRefs          []issueTimelineEvidenceRef  `json:"evidence_refs"`
	Artifacts             []agentTaskArtifactResponse `json:"artifacts"`
	Metadata              map[string]any              `json:"metadata,omitempty"`
}

// issueTimelineEvidenceRef 是时间线节点指向证据对象（task/附件/评论/活动等）的引用，供前端
// 跳转或归并证据。
type issueTimelineEvidenceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Href string `json:"href,omitempty"`
}

// issueTimelineSummaryResponse 是根 issue 全树的时间线汇总：起止时刻、各段耗时拆分（agent 执行 /
// 人工确认 / 子 issue 等待）、token 用量与消息数、失败摘要与验收状态。
type issueTimelineSummaryResponse struct {
	IssueID                     string `json:"issue_id"`
	NodeCount                   int    `json:"node_count"`
	TotalDurationMs             int64  `json:"total_duration_ms"`
	WorkStartedAt               string `json:"work_started_at,omitempty"`
	WorkCompletedAt             string `json:"work_completed_at,omitempty"`
	WallClockDurationMs         *int64 `json:"wall_clock_duration_ms"`
	AgentExecutionDurationMs    int64  `json:"agent_execution_duration_ms"`
	HumanConfirmationDurationMs *int64 `json:"human_confirmation_duration_ms"`
	ChildIssueWaitDurationMs    *int64 `json:"child_issue_wait_duration_ms"`
	TotalInputTokens            int64  `json:"total_input_tokens"`
	TotalOutputTokens           int64  `json:"total_output_tokens"`
	TotalCacheReadTokens        int64  `json:"total_cache_read_tokens"`
	TotalCacheWriteTokens       int64  `json:"total_cache_write_tokens"`
	MessageCount                int    `json:"message_count"`
	AgentTurnCount              int    `json:"agent_turn_count"`
	TraceEventCount             int    `json:"trace_event_count"`
	UsageUnavailable            bool   `json:"usage_unavailable"`
	FailureSummary              string `json:"failure_summary,omitempty"`
	AcceptanceStatus            string `json:"acceptance_status"`
	FullAnalysisDeepLink        string `json:"full_analysis_deep_link"`
}

// ptrString 解引用字符串指针；nil 返回空串。
func ptrString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// issueExecutionNodeResponse 是执行证据树上的一个 issue 节点：包含该 issue 自身的任务运行、
// 任务消息、trace 事件、工具调用链/摘要、产物、各角色评论与活动日志，以及按层级递归的
// Children。ManualComments/AgentComments/WakeupComments 按作者与语义分桶，便于时间线归类。
type issueExecutionNodeResponse struct {
	Issue           IssueResponse                 `json:"issue"`
	Tasks           []AgentTaskResponse           `json:"tasks"`
	TaskMessages    []protocol.TaskMessagePayload `json:"task_messages"`
	TraceEvents     []TaskTraceEventResponse      `json:"trace_events"`
	ToolCallChains  []toolCallChainResponse       `json:"tool_call_chains"`
	ToolCallSummary []toolCallSummaryResponse     `json:"tool_call_summary"`
	Artifacts       []agentTaskArtifactResponse   `json:"artifacts"`
	WakeupComments  []issueCommentBriefResponse   `json:"wakeup_comments"`
	ManualComments  []issueCommentBriefResponse   `json:"manual_comments,omitempty"`
	AgentComments   []issueCommentBriefResponse   `json:"agent_comments,omitempty"`
	ActivityLogs    []issueActivityBriefResponse  `json:"activity_logs,omitempty"`
	Children        []issueExecutionNodeResponse  `json:"children"`
}

// agentTaskArtifactResponse 是 agent 任务产物（附件）的时间线视图：标出归属的任务/评论/issue，
// 并给出文件名、kind、下载与 markdown 渲染地址。
type agentTaskArtifactResponse struct {
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

// issueCommentBriefResponse 是评论的精简视图（执行树只展示评论元信息与内容，不展开评论的
// 回复结构），SourceTaskID 标注该评论由哪条 agent 任务触发产生。
type issueCommentBriefResponse struct {
	ID           string  `json:"id"`
	IssueID      string  `json:"issue_id"`
	AuthorType   string  `json:"author_type"`
	Type         string  `json:"type"`
	Content      string  `json:"content"`
	ParentID     *string `json:"parent_id"`
	SourceTaskID *string `json:"source_task_id,omitempty"`
	CreatedAt    string  `json:"created_at"`
}

// issueActivityBriefResponse 是活动日志条目的精简视图，Details 保留原始 JSON 字典供前端按
// action 类型解读。
type issueActivityBriefResponse struct {
	ID        string         `json:"id"`
	IssueID   string         `json:"issue_id"`
	ActorType string         `json:"actor_type"`
	ActorID   string         `json:"actor_id"`
	Action    string         `json:"action"`
	Details   map[string]any `json:"details"`
	CreatedAt string         `json:"created_at"`
}

// GetIssueExecutionTree 处理 GET /api/issues/:id/execution-tree：返回以该 issue 为根的跨 issue
// 执行证据——issue 层级、任务运行、SOP 运行、trace 事件与 child-done 唤醒评论。它刻意只读，
// 从既有事实推导，而不是再加一层持久化。
func (h *Handler) GetIssueExecutionTree(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	root, err := h.buildIssueExecutionNode(r.Context(), issue, prefix, 0, requestUserID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build issue execution tree")
		return
	}
	timelineNodes := buildIssueTimelineNodes(root)
	writeJSON(w, http.StatusOK, issueExecutionTreeResponse{
		Root:          root,
		Summary:       summarizeIssueExecutionTree(root),
		TimelineNodes: timelineNodes,
		IssueSummary:  summarizeIssueTimeline(root.Issue, timelineNodes),
	})
}

// issueExecutionTreeMaxDepth 是执行证据树的最大向下深度。树按需随层级展开，子 issue 层级每层
// 都要加载任务/评论/活动等，深树会放大查询量；2 层是「父 + 直属子」的实用窗口。
const issueExecutionTreeMaxDepth = 2

// buildIssueExecutionNode 递归构建单条 issue 的执行证据节点：加载其任务、评论、trace、产物、
// 活动日志并按角色分桶，深度未达上限时继续展开子 issue。所有数据都来自查询，无持久化副作用。
func (h *Handler) buildIssueExecutionNode(ctx context.Context, issue db.Issue, prefix string, depth int, viewers ...string) (issueExecutionNodeResponse, error) {
	viewer := ""
	if len(viewers) > 0 {
		viewer = viewers[0]
	}
	tasks, err := h.Queries.ListTasksByIssue(ctx, issue.ID)
	if err != nil {
		return issueExecutionNodeResponse{}, err
	}
	comments, err := h.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       200,
	})
	if err != nil {
		return issueExecutionNodeResponse{}, err
	}
	commentByID := make(map[string]db.Comment, len(comments))
	for _, comment := range comments {
		commentByID[uuidToString(comment.ID)] = comment
	}
	taskResp := make([]AgentTaskResponse, 0, len(tasks))
	taskMessages := make([]protocol.TaskMessagePayload, 0)
	workspaceID := uuidToString(issue.WorkspaceID)
	agentNameByID := make(map[string]string)
	for _, task := range tasks {
		resp := taskToResponse(task, workspaceID)
		if resp.TriggerCommentID != nil {
			if comment, ok := commentByID[*resp.TriggerCommentID]; ok {
				resp.TriggerAuthorType = comment.AuthorType
				resp.TriggerCommentContent = comment.Content
				resp.TriggerCommentCreatedAt = timestampToString(comment.CreatedAt)
			}
		}
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
			return issueExecutionNodeResponse{}, err
		}
		taskID := uuidToString(task.ID)
		issueID := uuidToString(issue.ID)
		for _, message := range messages {
			taskMessages = append(taskMessages, taskMessageToPayload(message, taskID, issueID))
		}
	}
	toolCallChains := buildToolCallChains(taskMessages)
	toolCallSummary := buildToolCallSummary(toolCallChains)

	traces, err := h.Queries.ListIssueTaskTraceEvents(ctx, issue.ID)
	if err != nil {
		return issueExecutionNodeResponse{}, err
	}
	traceResp, err := taskTraceEventsToResponse(traces)
	if err != nil {
		return issueExecutionNodeResponse{}, err
	}

	commentIDs := make([]pgtype.UUID, 0, len(comments))
	for _, comment := range comments {
		if comment.SourceTaskID.Valid {
			commentIDs = append(commentIDs, comment.ID)
		}
	}
	artifacts := make([]agentTaskArtifactResponse, 0)
	if len(commentIDs) > 0 {
		attachments, err := h.Queries.ListAttachmentsByCommentIDs(ctx, db.ListAttachmentsByCommentIDsParams{
			Column1:     commentIDs,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return issueExecutionNodeResponse{}, err
		}
		artifactIndexByKey := make(map[string]int, len(attachments))
		for _, attachment := range attachments {
			comment, ok := commentByID[uuidToString(attachment.CommentID)]
			if !ok || !comment.SourceTaskID.Valid {
				continue
			}
			artifact := h.agentTaskArtifactToResponse(attachment, comment.SourceTaskID, issue.ID)
			key := agentTaskArtifactSemanticKey(artifact)
			if index, ok := artifactIndexByKey[key]; ok {
				if artifactCreatedAtAfterOrEqual(artifact, artifacts[index]) {
					artifacts[index] = artifact
				}
				continue
			}
			artifactIndexByKey[key] = len(artifacts)
			artifacts = append(artifacts, artifact)
		}
	}
	wakeupComments := make([]issueCommentBriefResponse, 0)
	manualComments := make([]issueCommentBriefResponse, 0)
	agentComments := make([]issueCommentBriefResponse, 0)
	for _, comment := range comments {
		if comment.AuthorType == "member" {
			manualComments = append(manualComments, issueCommentBrief(comment))
		}
		if comment.AuthorType == "agent" {
			agentComments = append(agentComments, issueCommentBrief(comment))
		}
		if comment.AuthorType == "system" && comment.Type == "system" &&
			((strings.Contains(comment.Content, "子任务") && strings.Contains(comment.Content, "已完成")) ||
				strings.Contains(comment.Content, "sub-issues are complete")) {
			brief := issueCommentBrief(comment)
			brief.SourceTaskID = nil
			wakeupComments = append(wakeupComments, brief)
		}
	}
	activities, err := h.Queries.ListActivitiesForIssue(ctx, db.ListActivitiesForIssueParams{
		IssueID: issue.ID,
		Limit:   200,
	})
	if err != nil {
		return issueExecutionNodeResponse{}, err
	}
	activityResp := make([]issueActivityBriefResponse, 0, len(activities))
	for _, activity := range activities {
		activityResp = append(activityResp, issueActivityBrief(activity))
	}

	childrenResp := []issueExecutionNodeResponse{}
	if depth < issueExecutionTreeMaxDepth {
		children, err := h.Queries.ListChildIssues(ctx, issue.ID)
		if err != nil {
			return issueExecutionNodeResponse{}, err
		}
		childrenResp = make([]issueExecutionNodeResponse, 0, len(children))
		for _, child := range children {
			if !healthIssueVisible(child, viewer) {
				continue
			}
			childNode, err := h.buildIssueExecutionNode(ctx, child, prefix, depth+1, viewer)
			if err != nil {
				return issueExecutionNodeResponse{}, err
			}
			childrenResp = append(childrenResp, childNode)
		}
	}

	return issueExecutionNodeResponse{
		Issue:           issueToResponse(issue, prefix),
		Tasks:           taskResp,
		TaskMessages:    taskMessages,
		TraceEvents:     traceResp,
		ToolCallChains:  toolCallChains,
		ToolCallSummary: toolCallSummary,
		Artifacts:       artifacts,
		WakeupComments:  wakeupComments,
		ManualComments:  manualComments,
		AgentComments:   agentComments,
		ActivityLogs:    activityResp,
		Children:        childrenResp,
	}, nil
}

// issueCommentBrief 把一条数据库评论行压缩成时间线用的精简视图。
func issueCommentBrief(comment db.Comment) issueCommentBriefResponse {
	return issueCommentBriefResponse{
		ID:           uuidToString(comment.ID),
		IssueID:      uuidToString(comment.IssueID),
		AuthorType:   comment.AuthorType,
		Type:         comment.Type,
		Content:      comment.Content,
		ParentID:     uuidToPtr(comment.ParentID),
		SourceTaskID: uuidToPtr(comment.SourceTaskID),
		CreatedAt:    timestampToString(comment.CreatedAt),
	}
}

// issueActivityBrief 把一条活动日志转成精简视图；Details 是存储的 JSON，这里解析成 map 供
// 前端按 action 类型解读，解析失败则给空 map（不影响响应）。
func issueActivityBrief(activity db.ActivityLog) issueActivityBriefResponse {
	details := map[string]any{}
	if len(activity.Details) > 0 {
		_ = json.Unmarshal(activity.Details, &details)
	}
	return issueActivityBriefResponse{
		ID:        uuidToString(activity.ID),
		IssueID:   uuidToString(activity.IssueID),
		ActorType: activity.ActorType.String,
		ActorID:   uuidToString(activity.ActorID),
		Action:    activity.Action,
		Details:   details,
		CreatedAt: timestampToString(activity.CreatedAt),
	}
}

// agentTaskArtifactToResponse 把附件转成任务产物响应，补上所属 task / issue 与标题、kind、
// 下载地址。用 stable 的 URL 模式，保证同一产物在不同请求里得到一致链接。
func (h *Handler) agentTaskArtifactToResponse(attachment db.Attachment, taskID, issueID pgtype.UUID) agentTaskArtifactResponse {
	att := h.attachmentToResponse(attachment, attachmentURLModeStable)
	return agentTaskArtifactResponse{
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

// agentTaskArtifactSemanticKey 从产物字段拼出不区分大小写的语义键，用于对同一次运行里重复
// 上传的产物去重（只保留最新一份）。
func agentTaskArtifactSemanticKey(artifact agentTaskArtifactResponse) string {
	return strings.ToLower(strings.Join([]string{
		strings.TrimSpace(artifact.TaskID),
		strings.TrimSpace(artifact.Kind),
		strings.TrimSpace(artifact.Title),
		strings.TrimSpace(artifact.Filename),
	}, ":"))
}

// artifactCreatedAtAfterOrEqual 比较两个产物的 CreatedAt：都能解析时按时间比，否则退回字符串
// 比较，保证任一输入都得到确定答案（时间戳都来自同一格式化函数，两种比较结论基本一致）。
func artifactCreatedAtAfterOrEqual(left, right agentTaskArtifactResponse) bool {
	leftAt, leftErr := time.Parse(time.RFC3339, left.CreatedAt)
	rightAt, rightErr := time.Parse(time.RFC3339, right.CreatedAt)
	if leftErr == nil && rightErr == nil {
		return !leftAt.Before(rightAt)
	}
	return left.CreatedAt >= right.CreatedAt
}

// summarizeIssueExecutionTree 遍历整棵执行树，累加一份中文标签的计数摘要（任务/子任务/观测
// 事件/工具调用/唤醒评论等），作为响应里的 Summary。
func summarizeIssueExecutionTree(root issueExecutionNodeResponse) map[string]int {
	summary := map[string]int{
		"任务数":   0,
		"子任务数":  0,
		"观测事件数": 0,
		"工具调用数": 0,
		"异常工具数": 0,
		"唤醒评论数": 0,
		"完成任务数": 0,
		"失败任务数": 0,
		"取消任务数": 0,
	}
	var walk func(node issueExecutionNodeResponse, isRoot bool)
	walk = func(node issueExecutionNodeResponse, isRoot bool) {
		if !isRoot {
			summary["子任务数"]++
		}
		summary["任务数"] += len(node.Tasks)
		summary["观测事件数"] += len(node.TraceEvents)
		for _, tool := range node.ToolCallSummary {
			summary["工具调用数"] += tool.TotalCalls
			summary["异常工具数"] += tool.FailureSignalCalls + tool.MissingResultCalls + tool.OrphanResultCalls
		}
		summary["唤醒评论数"] += len(node.WakeupComments)
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

// buildIssueTimelineNodes 把单条 issue 的执行证据拍平、统一成时间线节点数组：agent 任务、其
// 下的工具调用链、trace 事件、issue 元数据里的来源抓取、子 issue 引用、唤醒（评审）评论与
// 人工确认段。最后按开始时间稳定排序，并保证产物字段非 nil（见末尾注释）。
func buildIssueTimelineNodes(root issueExecutionNodeResponse) []issueTimelineNodeResponse {
	nodes := make([]issueTimelineNodeResponse, 0)
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
	artifactsByTask := map[string][]agentTaskArtifactResponse{}
	for _, artifact := range root.Artifacts {
		artifactsByTask[artifact.TaskID] = append(artifactsByTask[artifact.TaskID], artifact)
	}
	for _, task := range root.Tasks {
		taskTraces := traceByTask[task.ID]
		taskArtifacts := artifactsByTask[task.ID]
		responsibilityStartedAt := firstNonEmpty(task.TriggerCommentCreatedAt, task.CreatedAt, ptrString(task.StartedAt))
		node := issueTimelineNodeResponse{
			IssueID:               root.Issue.ID,
			RootTaskID:            rootTaskID,
			NodeID:                "task:" + task.ID,
			NodeType:              "agent_task",
			AgentID:               task.AgentID,
			Status:                task.Status,
			FailureReason:         task.FailureReason,
			StartedAt:             responsibilityStartedAt,
			ActualStartedAt:       ptrString(task.StartedAt),
			CompletedAt:           ptrString(task.CompletedAt),
			DurationMs:            responsibilityDurationMs(task, responsibilityStartedAt),
			MessageCount:          messageCounts[task.ID],
			AgentTurnCount:        agentTurnCounts[task.ID],
			TraceEventCount:       len(taskTraces),
			UsageUnavailableTrace: hasUsageUnavailableTrace(taskTraces),
			Summary:               timelineTaskSummary(task),
			EvidenceRefs:          []issueTimelineEvidenceRef{{Type: "agent_task", ID: task.ID}},
			Artifacts:             taskArtifacts,
		}
		for _, artifact := range taskArtifacts {
			node.EvidenceRefs = append(node.EvidenceRefs, issueTimelineEvidenceRef{
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
	for _, chain := range root.ToolCallChains {
		nodes = append(nodes, issueTimelineNodeResponse{
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
			EvidenceRefs: []issueTimelineEvidenceRef{{Type: "tool_call_chain", ID: chain.ID}},
		})
	}
	for _, event := range root.TraceEvents {
		nodeType := "status_change"
		if strings.Contains(event.EventType, "source") || strings.Contains(event.EventType, "fetch") {
			nodeType = "source_fetch"
		}
		nodes = append(nodes, issueTimelineNodeResponse{
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
			EvidenceRefs:          []issueTimelineEvidenceRef{{Type: "trace_event", ID: event.ID}},
		})
	}
	if status, ok := root.Issue.Metadata["source_fetch_status"].(string); ok && status != "" {
		nodes = append(nodes, issueTimelineNodeResponse{
			IssueID:      root.Issue.ID,
			RootTaskID:   rootTaskID,
			NodeID:       "source_fetch:" + root.Issue.ID,
			NodeType:     "source_fetch",
			Status:       status,
			StartedAt:    stringValue(root.Issue.Metadata["source_fetch_observed_at"]),
			CompletedAt:  stringValue(root.Issue.Metadata["source_fetch_observed_at"]),
			Summary:      firstNonEmpty(stringValue(root.Issue.Metadata["source_fetch_summary"]), stringValue(root.Issue.Metadata["source_fetch_error"]), "Source fetch "+status),
			EvidenceRefs: []issueTimelineEvidenceRef{{Type: "issue_metadata", ID: root.Issue.ID}},
		})
	}
	for _, child := range root.Children {
		nodes = append(nodes, issueTimelineNodeResponse{
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
			EvidenceRefs: []issueTimelineEvidenceRef{{Type: "child_issue", ID: child.Issue.ID, Href: "/issues/" + child.Issue.ID}},
		})
	}
	for _, comment := range root.WakeupComments {
		nodes = append(nodes, issueTimelineNodeResponse{
			IssueID:      root.Issue.ID,
			RootTaskID:   rootTaskID,
			NodeID:       "approval:" + comment.ID,
			NodeType:     "approval",
			Status:       "completed",
			StartedAt:    comment.CreatedAt,
			CompletedAt:  comment.CreatedAt,
			Summary:      comment.Content,
			EvidenceRefs: []issueTimelineEvidenceRef{{Type: "comment", ID: comment.ID}},
		})
	}
	nodes = append(nodes, buildHumanConfirmationTimelineNodes(root, rootTaskID)...)
	sort.SliceStable(nodes, func(i, j int) bool {
		left := firstNonEmpty(nodes[i].StartedAt, nodes[i].CompletedAt, nodes[i].NodeID)
		right := firstNonEmpty(nodes[j].StartedAt, nodes[j].CompletedAt, nodes[j].NodeID)
		if left == right {
			return nodes[i].NodeID < nodes[j].NodeID
		}
		return left < right
	})
	// 产物数组属于执行树响应的线格式契约：nil 切片会序列化成 JSON null，导致客户端丢弃整棵
	// 本来有效的树（包括 token 汇总与子 issue 泳道）。统一补成空数组。
	for i := range nodes {
		if nodes[i].Artifacts == nil {
			nodes[i].Artifacts = []agentTaskArtifactResponse{}
		}
	}
	return nodes
}

// buildHumanConfirmationTimelineNodes 构造「人工确认」段的时间线节点：当用户（手动评论）在上
// 一段 agent 任务完成后、下一段任务启动前给出了确认/推进，就为每段这样的确认生成一个
// human_confirmation 节点，其跨度即两段任务间的等待。若还有一段未收到回应的确认，则追加一个
// 状态为 running 的待确认节点（见 buildPendingHumanConfirmationTimelineNode）。
func buildHumanConfirmationTimelineNodes(root issueExecutionNodeResponse, rootTaskID string) []issueTimelineNodeResponse {
	if len(root.Tasks) == 0 {
		return nil
	}
	tasks := append([]AgentTaskResponse(nil), root.Tasks...)
	sort.SliceStable(tasks, func(i, j int) bool {
		left := firstNonEmpty(ptrString(tasks[i].StartedAt), ptrString(tasks[i].DispatchedAt), tasks[i].CreatedAt, tasks[i].ID)
		right := firstNonEmpty(ptrString(tasks[j].StartedAt), ptrString(tasks[j].DispatchedAt), tasks[j].CreatedAt, tasks[j].ID)
		if left == right {
			return tasks[i].ID < tasks[j].ID
		}
		return left < right
	})

	tasksByTriggerCommentID := make(map[string]AgentTaskResponse)
	for _, task := range tasks {
		if task.TriggerCommentID == nil {
			continue
		}
		commentID := *task.TriggerCommentID
		existing, ok := tasksByTriggerCommentID[commentID]
		if !ok || compareTimelineTasks(task, existing) < 0 {
			tasksByTriggerCommentID[commentID] = task
		}
	}

	nodes := make([]issueTimelineNodeResponse, 0)
	if len(root.ManualComments) > 0 && len(root.Tasks) >= 2 {
		for _, comment := range root.ManualComments {
			task, ok := tasksByTriggerCommentID[comment.ID]
			if !ok {
				continue
			}
			commentAt, commentErr := time.Parse(time.RFC3339, comment.CreatedAt)
			if commentErr != nil {
				continue
			}
			previousTask, startAt, ok := latestCompletedTaskBefore(tasks, commentAt)
			if !ok || !commentAt.After(startAt) {
				continue
			}
			nodes = append(nodes, issueTimelineNodeResponse{
				IssueID:     root.Issue.ID,
				RootTaskID:  rootTaskID,
				NodeID:      "human_confirmation:" + comment.ID + ":" + task.ID,
				NodeType:    "human_confirmation",
				AgentID:     task.AgentID,
				AgentName:   task.TriggerAuthorName,
				Status:      "completed",
				StartedAt:   startAt.Format(time.RFC3339Nano),
				CompletedAt: commentAt.Format(time.RFC3339Nano),
				DurationMs:  commentAt.Sub(startAt).Milliseconds(),
				Summary:     humanConfirmationSummary(comment),
				EvidenceRefs: []issueTimelineEvidenceRef{
					{Type: "agent_task", ID: previousTask.ID},
					{Type: "comment", ID: comment.ID},
					{Type: "agent_task", ID: task.ID},
				},
			})
		}
	}
	if pending := buildPendingHumanConfirmationTimelineNode(root, rootTaskID, tasks); pending != nil {
		nodes = append(nodes, *pending)
	}
	return nodes
}

// pendingHumanConfirmationCandidate 是一次「尚在等待人工确认」的候选信息：相关任务、等待起点、
// 信号时刻与摘要，以及指向证据的引用和元数据。多条候选按信号时刻取最新者（见
// laterPendingHumanConfirmationCandidate）。
type pendingHumanConfirmationCandidate struct {
	task         AgentTaskResponse
	startAt      time.Time
	signalAt     time.Time
	summary      string
	evidenceRefs []issueTimelineEvidenceRef
	metadata     map[string]any
}

// buildPendingHumanConfirmationTimelineNode 在 issue 未完成、且确实存在一条已发出等待信号却
// 尚未收到回应的确认段时，构造一个 running 状态的待确认节点，让时间线在 issue 中途也能提示
// 「此刻正卡在等用户」。信号来源有三种：squad leader 评估的活动日志、leader 任务的评论正文、
// 或 leader 任务结果里的输出文案；取信号时刻最新的一种。issue 已 done 时返回 nil。
func buildPendingHumanConfirmationTimelineNode(root issueExecutionNodeResponse, rootTaskID string, tasks []AgentTaskResponse) *issueTimelineNodeResponse {
	if isCompletedIssueStatus(root.Issue.Status) {
		return nil
	}
	taskByID := make(map[string]AgentTaskResponse, len(tasks))
	for _, task := range tasks {
		taskByID[task.ID] = task
	}
	var selected *pendingHumanConfirmationCandidate
	for _, activity := range root.ActivityLogs {
		if activity.Action != "squad_leader_evaluated" || stringValue(activity.Details["wait_kind"]) != "human_confirmation" {
			continue
		}
		taskID := stringValue(activity.Details["task_id"])
		task, ok := taskByID[taskID]
		if !ok {
			continue
		}
		signalAt, err := time.Parse(time.RFC3339, activity.CreatedAt)
		if err != nil {
			continue
		}
		startAt, commentRef := pendingHumanConfirmationStart(root, task, signalAt)
		summary := firstNonEmpty(stringValue(activity.Details["wait_summary"]), stringValue(activity.Details["reason"]), pendingHumanConfirmationTaskSummary(task))
		refs := []issueTimelineEvidenceRef{{Type: "agent_task", ID: task.ID}, {Type: "activity", ID: activity.ID}}
		if commentRef != "" {
			refs = append(refs, issueTimelineEvidenceRef{Type: "comment", ID: commentRef})
		}
		candidate := pendingHumanConfirmationCandidate{
			task:         task,
			startAt:      startAt,
			signalAt:     signalAt,
			summary:      summary,
			evidenceRefs: refs,
			metadata: map[string]any{
				"pending":     true,
				"wait_kind":   "human_confirmation",
				"source":      "activity_log",
				"activity_id": activity.ID,
			},
		}
		selected = laterPendingHumanConfirmationCandidate(selected, candidate)
	}
	for _, task := range tasks {
		if !task.IsLeaderTask || !isTerminalTimelineStatus(task.Status) {
			continue
		}
		taskSignalAt, ok := parseTaskCompletedAt(task)
		if !ok {
			continue
		}
		for _, comment := range root.AgentComments {
			if ptrString(comment.SourceTaskID) != task.ID || !isPendingHumanConfirmationText(comment.Content) {
				continue
			}
			commentAt, err := time.Parse(time.RFC3339, comment.CreatedAt)
			if err != nil {
				continue
			}
			candidate := pendingHumanConfirmationCandidate{
				task:         task,
				startAt:      commentAt,
				signalAt:     commentAt,
				summary:      pendingHumanConfirmationSummary(comment.Content),
				evidenceRefs: []issueTimelineEvidenceRef{{Type: "agent_task", ID: task.ID}, {Type: "comment", ID: comment.ID}},
				metadata: map[string]any{
					"pending":   true,
					"wait_kind": "human_confirmation",
					"source":    "agent_comment",
				},
			}
			selected = laterPendingHumanConfirmationCandidate(selected, candidate)
		}
		if output := pendingHumanConfirmationTaskSummary(task); output != "" && isPendingHumanConfirmationText(output) {
			candidate := pendingHumanConfirmationCandidate{
				task:         task,
				startAt:      taskSignalAt,
				signalAt:     taskSignalAt,
				summary:      pendingHumanConfirmationSummary(output),
				evidenceRefs: []issueTimelineEvidenceRef{{Type: "agent_task", ID: task.ID}},
				metadata: map[string]any{
					"pending":   true,
					"wait_kind": "human_confirmation",
					"source":    "task_result",
				},
			}
			selected = laterPendingHumanConfirmationCandidate(selected, candidate)
		}
	}
	if selected == nil || !isStillPendingHumanConfirmation(root, selected.task.ID, selected.startAt) {
		return nil
	}
	return &issueTimelineNodeResponse{
		IssueID:      root.Issue.ID,
		RootTaskID:   rootTaskID,
		NodeID:       "human_confirmation:pending:" + selected.task.ID,
		NodeType:     "human_confirmation",
		AgentID:      selected.task.AgentID,
		AgentName:    taskAgentName(selected.task),
		Status:       "running",
		StartedAt:    selected.startAt.Format(time.RFC3339Nano),
		DurationMs:   0,
		Summary:      firstNonEmpty(selected.summary, "等待用户确认"),
		EvidenceRefs: selected.evidenceRefs,
		Metadata:     selected.metadata,
	}
}

// laterPendingHumanConfirmationCandidate 返回两个候选中「信号时刻更晚」的那个；current 为 nil
// 时直接返回 candidate。用于从多条来源信号里选出时间线上最新的一次等待。
func laterPendingHumanConfirmationCandidate(current *pendingHumanConfirmationCandidate, candidate pendingHumanConfirmationCandidate) *pendingHumanConfirmationCandidate {
	if current == nil || candidate.signalAt.After(current.signalAt) {
		copy := candidate
		return &copy
	}
	return current
}

// pendingHumanConfirmationStart 推断一次待确认的起点时刻与触发评论：任务已完成后，取该任务在
// completedAt 之后的第一条「等待确认」评论为起点；找不到则退回任务完成时刻，无评论引用。
func pendingHumanConfirmationStart(root issueExecutionNodeResponse, task AgentTaskResponse, fallback time.Time) (time.Time, string) {
	if completedAt, ok := parseTaskCompletedAt(task); ok {
		for _, comment := range root.AgentComments {
			if ptrString(comment.SourceTaskID) != task.ID || !isPendingHumanConfirmationText(comment.Content) {
				continue
			}
			commentAt, err := time.Parse(time.RFC3339, comment.CreatedAt)
			if err != nil || commentAt.Before(completedAt) {
				continue
			}
			return commentAt, comment.ID
		}
		return completedAt, ""
	}
	return fallback, ""
}

// isStillPendingHumanConfirmation 判断 startAt 之后是否仍没有「回应」：一旦 startAt 之后出现
// 任何人工评论、或启动了另一条任务，就认为本次确认已经结束（收到回应或已搁置），返回 false。
func isStillPendingHumanConfirmation(root issueExecutionNodeResponse, taskID string, startAt time.Time) bool {
	for _, comment := range root.ManualComments {
		commentAt, err := time.Parse(time.RFC3339, comment.CreatedAt)
		if err == nil && commentAt.After(startAt) {
			return false
		}
	}
	for _, task := range root.Tasks {
		if task.ID == taskID {
			continue
		}
		taskAtText := firstNonEmpty(task.TriggerCommentCreatedAt, task.CreatedAt, ptrString(task.StartedAt), ptrString(task.DispatchedAt))
		taskAt, err := time.Parse(time.RFC3339, taskAtText)
		if err == nil && taskAt.After(startAt) {
			return false
		}
	}
	return true
}

// taskAgentName 返回任务响应上挂的 agent 显示名；未解析到时返回空串。
func taskAgentName(task AgentTaskResponse) string {
	if task.Agent != nil && task.Agent.Name != "" {
		return task.Agent.Name
	}
	return ""
}

// isTerminalTimelineStatus 判断时间线状态是否已终局（完成或失败），终局任务才可能携带待人工
// 确认的尾声信号。
func isTerminalTimelineStatus(status string) bool {
	return isCompletedTimelineStatus(status) || isFailedTimelineStatus(status)
}

// parseTaskCompletedAt 解析任务的 CompletedAt（RFC3339）；为空或解析失败返回 ok=false。
func parseTaskCompletedAt(task AgentTaskResponse) (time.Time, bool) {
	if task.CompletedAt == nil || *task.CompletedAt == "" {
		return time.Time{}, false
	}
	completedAt, err := time.Parse(time.RFC3339, *task.CompletedAt)
	return completedAt, err == nil
}

// pendingHumanConfirmationTaskSummary 取任务结果里的输出文案作为待确认摘要，没有输出时退回
// timelineTaskSummary。
func pendingHumanConfirmationTaskSummary(task AgentTaskResponse) string {
	if output := taskResultOutput(task.Result); output != "" {
		return output
	}
	return timelineTaskSummary(task)
}

// taskResultOutput 从任务结果（map 形态）里提取 output 字符串字段；nil 或类型不符返回空串。
func taskResultOutput(result any) string {
	if result == nil {
		return ""
	}
	if value, ok := result.(map[string]any); ok {
		if output, ok := value["output"].(string); ok {
			return strings.TrimSpace(output)
		}
	}
	return ""
}

// pendingHumanConfirmationSummary 从一段原始文案里提炼待确认摘要：取第一行有语义的 markdown
// 文本，超出长度截断；拿不到就用整段并截断；实在为空才退回「等待用户确认」。
func pendingHumanConfirmationSummary(value string) string {
	content := firstSemanticMarkdownLine(value)
	if content == "" {
		content = strings.TrimSpace(value)
	}
	if content == "" {
		return "等待用户确认"
	}
	return truncateTimelineSummary(content)
}

// isPendingHumanConfirmationText 用关键词组（如「等待用户确认」「需人工确认」「确认后继续」）
// 判断一段文本是否表达了「正在等用户确认」。任一组词全部出现即命中，覆盖 agent / 提示词在
// 措辞上的常见变化。
func isPendingHumanConfirmationText(value string) bool {
	text := strings.TrimSpace(value)
	if text == "" {
		return false
	}
	positiveGroups := [][]string{
		{"等待", "用户", "确认"},
		{"待", "用户", "确认"},
		{"需", "用户", "确认"},
		{"需要", "用户", "确认"},
		{"人工", "确认"},
		{"补充", "确认"},
		{"确认后", "继续"},
	}
	for _, group := range positiveGroups {
		matched := true
		for _, word := range group {
			if !strings.Contains(text, word) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// latestCompletedTaskBefore 返回 `before` 时刻之前最后完成的一条任务及其完成时刻；不存在返回
// ok=false。用于把一条人工评论对齐到它之前刚结束的那段任务上。
func latestCompletedTaskBefore(tasks []AgentTaskResponse, before time.Time) (AgentTaskResponse, time.Time, bool) {
	var selected AgentTaskResponse
	var selectedAt time.Time
	for _, task := range tasks {
		if task.CompletedAt == nil {
			continue
		}
		completedAt, err := time.Parse(time.RFC3339, *task.CompletedAt)
		if err != nil || completedAt.After(before) {
			continue
		}
		if selectedAt.IsZero() || completedAt.After(selectedAt) {
			selected = task
			selectedAt = completedAt
		}
	}
	if selectedAt.IsZero() {
		return AgentTaskResponse{}, time.Time{}, false
	}
	return selected, selectedAt, true
}

// compareTimelineTasks 比较两条任务的先后：优先触发评论时间、其次开始/派发/创建时间，最后用
// id 兜底定序，使同一触发评论只选一条代表任务时结果确定。
func compareTimelineTasks(left, right AgentTaskResponse) int {
	leftValue := firstNonEmpty(left.TriggerCommentCreatedAt, ptrString(left.StartedAt), ptrString(left.DispatchedAt), left.CreatedAt, left.ID)
	rightValue := firstNonEmpty(right.TriggerCommentCreatedAt, ptrString(right.StartedAt), ptrString(right.DispatchedAt), right.CreatedAt, right.ID)
	if leftValue < rightValue {
		return -1
	}
	if leftValue > rightValue {
		return 1
	}
	if left.ID < right.ID {
		return -1
	}
	if left.ID > right.ID {
		return 1
	}
	return 0
}

// responsibilityDurationMs 计算任务从「责任人视角的开始」到完成之间经过的毫秒数——startedAt 是
// 触发评论/创建时间这类早于实际开跑的时点；任一时间缺失或结束不晚于开始都返回 0。
func responsibilityDurationMs(task AgentTaskResponse, startedAt string) int64 {
	if startedAt == "" || task.CompletedAt == nil {
		return 0
	}
	start, startErr := time.Parse(time.RFC3339, startedAt)
	end, endErr := time.Parse(time.RFC3339, *task.CompletedAt)
	if startErr != nil || endErr != nil || !end.After(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

// humanConfirmationSummary 把一段人工确认评论压成「等待人工确认：<摘要>」的节点文案。
func humanConfirmationSummary(comment issueCommentBriefResponse) string {
	content := strings.TrimSpace(comment.Content)
	if content == "" {
		return "等待人工确认"
	}
	return "等待人工确认：" + truncateTimelineSummary(content)
}

// summarizeIssueTimeline 汇总根 issue 整条时间线：节点数、起止与各段耗时（墙钟/agent 执行/
// 人工确认/子 issue 等待）、token 与消息统计、失败摘要与验收状态。优先用 issue 上落库的
// WorkStartedAt/WorkCompletedAt，缺失时才从节点时间回推；失败仅取「影响验收」的首条。
func summarizeIssueTimeline(issue IssueResponse, nodes []issueTimelineNodeResponse) issueTimelineSummaryResponse {
	summary := issueTimelineSummaryResponse{
		IssueID:              issue.ID,
		NodeCount:            len(nodes),
		AcceptanceStatus:     "unknown",
		FullAnalysisDeepLink: "/issues/" + issue.ID + "?panel=execution",
	}
	fallbackStartedAt, fallbackCompletedAt := timelineAgentWorkBounds(nodes)
	if issue.WorkStartedAt != nil {
		summary.WorkStartedAt = *issue.WorkStartedAt
	} else if fallbackStartedAt != "" {
		summary.WorkStartedAt = fallbackStartedAt
	}
	if issue.WorkCompletedAt != nil {
		summary.WorkCompletedAt = *issue.WorkCompletedAt
	} else if fallbackCompletedAt != "" {
		summary.WorkCompletedAt = fallbackCompletedAt
	}
	hasTaskUsage := false
	hasTaskNodes := false
	for _, node := range nodes {
		if node.NodeType != "agent_task" {
			continue
		}
		hasTaskNodes = true
		if node.InputTokens+node.OutputTokens+node.CacheReadTokens+node.CacheWriteTokens > 0 {
			hasTaskUsage = true
			break
		}
	}
	for _, node := range nodes {
		if !hasTaskUsage || node.NodeType == "agent_task" {
			summary.TotalInputTokens += node.InputTokens
			summary.TotalOutputTokens += node.OutputTokens
			summary.TotalCacheReadTokens += node.CacheReadTokens
			summary.TotalCacheWriteTokens += node.CacheWriteTokens
		}
		if !hasTaskNodes || node.NodeType == "agent_task" {
			summary.MessageCount += node.MessageCount
			summary.AgentTurnCount += node.AgentTurnCount
			summary.TraceEventCount += node.TraceEventCount
		}
		if node.UsageUnavailableTrace {
			summary.UsageUnavailable = true
		}
		if timelineNodeAffectsAcceptance(node) && summary.FailureSummary == "" && isFailedTimelineStatus(node.Status) && !isRecoveredRuntimeFailure(issue, node) {
			summary.FailureSummary = node.Summary
			summary.AcceptanceStatus = node.Status
		}
		if timelineNodeAffectsAcceptance(node) && summary.AcceptanceStatus == "unknown" && isCompletedTimelineStatus(node.Status) {
			summary.AcceptanceStatus = "completed"
		}
	}
	if isCompletedIssueStatus(issue.Status) {
		summary.AcceptanceStatus = "done"
		summary.FailureSummary = ""
	}
	summary.AgentExecutionDurationMs = mergedWaitDurationMs(nodes, "agent_task")
	humanConfirmationMs := mergedWaitDurationMs(nodes, "human_confirmation")
	childIssueWaitMs := mergedWaitDurationMs(nodes, "child_issue_ref")
	if summary.WorkStartedAt != "" && summary.WorkCompletedAt != "" {
		if started, startErr := time.Parse(time.RFC3339, summary.WorkStartedAt); startErr == nil {
			if completed, completedErr := time.Parse(time.RFC3339, summary.WorkCompletedAt); completedErr == nil && completed.After(started) {
				wall := completed.Sub(started).Milliseconds()
				summary.WallClockDurationMs = &wall
				if humanConfirmationMs > 0 {
					summary.HumanConfirmationDurationMs = &humanConfirmationMs
				} else {
					human := wall - summary.AgentExecutionDurationMs - childIssueWaitMs
					if human < 0 {
						human = 0
					}
					summary.HumanConfirmationDurationMs = &human
				}
				summary.ChildIssueWaitDurationMs = &childIssueWaitMs
			}
		}
	} else if humanConfirmationMs > 0 {
		summary.HumanConfirmationDurationMs = &humanConfirmationMs
		if childIssueWaitMs > 0 {
			summary.ChildIssueWaitDurationMs = &childIssueWaitMs
		}
	} else if childIssueWaitMs > 0 {
		summary.ChildIssueWaitDurationMs = &childIssueWaitMs
	}
	childIssueWaitDurationMs := int64(0)
	if summary.ChildIssueWaitDurationMs != nil {
		childIssueWaitDurationMs = *summary.ChildIssueWaitDurationMs
	}
	if summary.HumanConfirmationDurationMs != nil {
		summary.TotalDurationMs = summary.AgentExecutionDurationMs + *summary.HumanConfirmationDurationMs + childIssueWaitDurationMs
	} else {
		summary.TotalDurationMs = summary.AgentExecutionDurationMs + childIssueWaitDurationMs
	}
	return summary
}

// timelineNodeAffectsAcceptance 判断某类节点是否参与「验收判定」：只有 agent 任务与子 issue
// 引用影响验收结论，人工确认/工具调用等中间节点不算。
func timelineNodeAffectsAcceptance(node issueTimelineNodeResponse) bool {
	return node.NodeType == "agent_task" || node.NodeType == "child_issue_ref"
}

// isRecoveredRuntimeFailure 判断某失败节点是否为「运行时可恢复」故障：只有整条 issue 已 done、
// 且失败原因为 runtime_recovery（被守护逻辑救回）时才算，这类失败不写入最终验收摘要。
func isRecoveredRuntimeFailure(issue IssueResponse, node issueTimelineNodeResponse) bool {
	return isCompletedIssueStatus(issue.Status) && strings.TrimSpace(node.FailureReason) == "runtime_recovery"
}

// isCompletedIssueStatus 判断 issue 状态是否算「已完成」：目前只有 done 命中。验收摘要把它
// 当作整条 issue 的终态依据。
func isCompletedIssueStatus(status string) bool {
	return status == "done"
}

// isFailedTimelineStatus 判断状态是否算失败/异常：兼容英文关键字与中文业务文案两类取值，命中
// failed/blocked/cancelled/失败/异常/阻塞/已取消任一即视为失败。
func isFailedTimelineStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "blocked", "cancelled", "失败", "异常", "阻塞", "已取消":
		return true
	default:
		return false
	}
}

// isCompletedTimelineStatus 判断状态是否算完成：兼容 completed / done / 已完成 等取值。
func isCompletedTimelineStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "done", "已完成":
		return true
	default:
		return false
	}
}

// timelineAgentWorkBounds 扫所有 agent_task 节点，返回最早开始与最晚完成的时刻（RFC3339Nano），
// 作为整条时间线工作段的边界；没有任何任务节点时两个值都为空串。
func timelineAgentWorkBounds(nodes []issueTimelineNodeResponse) (startedAt string, completedAt string) {
	var started *time.Time
	var completed *time.Time
	for _, node := range nodes {
		if node.NodeType != "agent_task" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339, node.StartedAt); err == nil {
			if started == nil || parsed.Before(*started) {
				value := parsed
				started = &value
			}
		}
		if parsed, err := time.Parse(time.RFC3339, node.CompletedAt); err == nil {
			if completed == nil || parsed.After(*completed) {
				value := parsed
				completed = &value
			}
		}
	}
	if started != nil {
		startedAt = started.Format(time.RFC3339Nano)
	}
	if completed != nil {
		completedAt = completed.Format(time.RFC3339Nano)
	}
	return startedAt, completedAt
}

// timelineInterval 表示时间线上一个半开区间，供同类节点（agent 执行、人工确认、子 issue 等待）
// 合并统计耗时。
type timelineInterval struct {
	start time.Time
	end   time.Time
}

// mergedWaitDurationMs 对指定 nodeTypes 的节点先各取其 [开始, 完成] 区间，合并重叠后再求总
// 时长（毫秒）——重叠的等待不计两次。无有效区间返回 0。
func mergedWaitDurationMs(nodes []issueTimelineNodeResponse, nodeTypes ...string) int64 {
	allowed := make(map[string]bool, len(nodeTypes))
	for _, nodeType := range nodeTypes {
		allowed[nodeType] = true
	}
	intervals := make([]timelineInterval, 0)
	for _, node := range nodes {
		if !allowed[node.NodeType] {
			continue
		}
		if node.StartedAt == "" || node.CompletedAt == "" {
			continue
		}
		start, startErr := time.Parse(time.RFC3339, node.StartedAt)
		end, endErr := time.Parse(time.RFC3339, node.CompletedAt)
		if startErr != nil || endErr != nil || !end.After(start) {
			continue
		}
		intervals = append(intervals, timelineInterval{start: start, end: end})
	}
	merged := mergeTimelineIntervals(intervals)
	if len(merged) == 0 {
		return 0
	}
	var total int64
	for _, interval := range merged {
		total += interval.end.Sub(interval.start).Milliseconds()
	}
	return total
}

// mergeTimelineIntervals 把区间按起点排序并合并所有重叠/相接区间，返回不相交的并集。
func mergeTimelineIntervals(intervals []timelineInterval) []timelineInterval {
	if len(intervals) == 0 {
		return nil
	}
	sorted := append([]timelineInterval(nil), intervals...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].start.Equal(sorted[j].start) {
			return sorted[i].end.Before(sorted[j].end)
		}
		return sorted[i].start.Before(sorted[j].start)
	})
	merged := []timelineInterval{sorted[0]}
	for _, interval := range sorted[1:] {
		current := &merged[len(merged)-1]
		if !interval.start.After(current.end) {
			if interval.end.After(current.end) {
				current.end = interval.end
			}
			continue
		}
		merged = append(merged, interval)
	}
	return merged
}

// artifactTitle 由文件名去掉扩展名得到展示标题；空文件名退回「产物」。
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

// artifactKind 依据文件名与 ContentType 把产物归类：.md / markdown 类型归 stage_markdown
// （阶段产物文档），其余一律 agent_attachment（普通任务附件）。kind 决定前端展示方式。
func artifactKind(filename, contentType string) string {
	lowerName := strings.ToLower(strings.TrimSpace(filename))
	lowerType := strings.ToLower(strings.TrimSpace(contentType))
	if strings.HasSuffix(lowerName, ".md") || strings.Contains(lowerType, "markdown") {
		return "stage_markdown"
	}
	return "agent_attachment"
}

// timelineTaskSummary 给任务节点选一条时间线摘要：leader 任务或已终局任务优先取结果输出的
// 意图行；其次取触发摘要；都没有时对 leader 任务退回「SOP leader[: <name>]」，普通任务退回
// 失败原因或「Agent task <status>」。
func timelineTaskSummary(task AgentTaskResponse) string {
	resultSummary := taskResultIntentSummary(task.Result)
	if (task.IsLeaderTask || isTerminalTimelineStatus(task.Status)) && resultSummary != "" {
		return resultSummary
	}
	if summary := ptrString(task.TriggerSummary); summary != "" {
		return summary
	}
	if resultSummary != "" {
		return resultSummary
	}
	if task.IsLeaderTask {
		if task.Agent != nil && task.Agent.Name != "" {
			return "SOP leader: " + task.Agent.Name
		}
		return "SOP leader task"
	}
	return firstNonEmpty(task.FailureReason, "Agent task "+task.Status)
}

// taskResultIntentSummary 从任务结果里取 output，再提取其中的语义首行并截断，作为任务在结果
// 里自述的意图摘要；拿不到则空串。
func taskResultIntentSummary(result any) string {
	output := taskResultOutput(result)
	if output == "" {
		return ""
	}
	line := firstSemanticMarkdownLine(output)
	if line != "" {
		return truncateTimelineSummary(line)
	}
	return ""
}

// truncateTimelineSummary 把时间线摘要截到 maxRunes=80 字符内，超出加省略号，保证时间线文案
// 长度可控。
func truncateTimelineSummary(value string) string {
	const maxRunes = 80
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return value
}

// firstSemanticMarkdownLine 扫描 markdown 文本，返回第一条「有语义」的非空行：跳过空行、分隔
// 线、代码块标记，并剥掉行首 # 标题、两侧 * 强调与 ` 反引号后再判一次；都剥空仍算无意义。
func firstSemanticMarkdownLine(value string) string {
	for _, rawLine := range strings.Split(value, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || isNonSemanticMarkdownLine(line) {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		line = strings.TrimSpace(strings.Trim(line, "*"))
		line = strings.TrimSpace(strings.Trim(line, "`"))
		if line == "" || isNonSemanticMarkdownLine(line) {
			continue
		}
		return line
	}
	return ""
}

// isNonSemanticMarkdownLine 判断一行是否为「无语义」的 markdown 噪声：空行、---/... 分隔线、
// ``` 代码块标记，或全由装饰符组成。
func isNonSemanticMarkdownLine(line string) bool {
	if line == "" {
		return true
	}
	if line == "---" || line == "..." || strings.HasPrefix(line, "```") {
		return true
	}
	trimmed := strings.Trim(line, "-=_*`~ ")
	return trimmed == ""
}

// traceDurationMs 从 trace 事件的三个候选时长字段里取首个 >0 的值（DurationMs/TotalMs/RunMs，
// 兼容不同来源 trace 的字段差异）；全为空返回 0。
func traceDurationMs(event TaskTraceEventResponse) int64 {
	for _, value := range []*int64{event.DurationMs, event.TotalMs, event.RunMs} {
		if value != nil && *value > 0 {
			return *value
		}
	}
	return 0
}

// hasUsageUnavailableTrace 报告这批 trace 事件里是否含 llm.usage_unavailable——有则时间线应打
// 上「用量不可用」标记，避免把缺 token 数据误当成零用量。
func hasUsageUnavailableTrace(events []TaskTraceEventResponse) bool {
	for _, event := range events {
		if event.EventType == "llm.usage_unavailable" {
			return true
		}
	}
	return false
}

// int64PtrValue 解引用 *int64；nil 返回 0。
func int64PtrValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// stringValue 从 any 值里取出字符串；类型不是 string 或缺失返回空串。
func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
