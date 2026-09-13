// 职责定位：issue 写入触发的「干跑」基础设施：为 write/preview 两条路径构造 WillEnqueueRun
// 探针、干跑预览端点，以及把 WillEnqueueRun 的决策落成入队副作用。
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	agentver "github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// maxPreviewTriggerIssues 限制单次预览请求，病态的大范围选择不能扇出成千上万次就绪探测。
const maxPreviewTriggerIssues = 500

// issueTriggerWriteProbe 构建写路径喂给 WillEnqueueRun 的探针。私密 agent 闸门已在 HTTP 边界
// 强制（分配走 validateAssigneePair、squad leader 走 canEnqueueSquadLeader），所以写路径不得
// 重跑或下沉它——传 allow-all。自循环检查需要请求的 X-Task-ID 头。
func (h *Handler) issueTriggerWriteProbe(r *http.Request, actorType, actorID string, issue db.Issue) service.IssueTriggerProbe {
	return service.IssueTriggerProbe{
		CanAccessAgent: nil, // allow-all；闸门在写边界
		IsSelfLoop: func() bool {
			return h.isAgentRunningOnIssue(r, actorType, issue)
		},
		SuppressActiveSelfAssignment: func(agentID pgtype.UUID) bool {
			suppress := h.shouldSuppressActiveSelfAssignment(r.Context(), actorType, actorID, issue.ID, agentID)
			if suppress {
				slog.Info("suppressing duplicate self-assignment enqueue",
					"issue_id", uuidToString(issue.ID),
					"agent_id", uuidToString(agentID),
				)
			}
			return suppress
		},
	}
}

// issueTriggerPreviewProbe 为只读预览镜像真实写时的闸门：私密 agent 闸门（预览不会向看不见它
// 的成员泄露私密 agent 的就绪状态——与 validateAssigneePair / canEnqueueSquadLeader 一致）与
// 同样的自循环守卫。
func (h *Handler) issueTriggerPreviewProbe(r *http.Request, actorType, actorID, workspaceID string, issue db.Issue) service.IssueTriggerProbe {
	originatorUserID := h.invokeOriginatorFromRequest(r, actorType, actorID)
	return service.IssueTriggerProbe{
		CanAccessAgent: func(agent db.Agent) bool {
			return h.canInvokeAgent(r.Context(), agent, actorType, actorID, originatorUserID, workspaceID)
		},
		IsSelfLoop: func() bool {
			return h.isAgentRunningOnIssue(r, actorType, issue)
		},
		SuppressActiveSelfAssignment: func(agentID pgtype.UUID) bool {
			return h.shouldSuppressActiveSelfAssignment(r.Context(), actorType, actorID, issue.ID, agentID)
		},
	}
}

// shouldSuppressActiveSelfAssignment 防止受信的任务作用域 agent 角色只为「声称 issue 所有权」
// 再给目标对建一条运行。它有意查「目标对」，而不是 actor 是否在忙：跨 issue 的自我交接是受
// 支持的工作流，目标没有活动运行时必须仍能入队。查询错误对外部入队副作用失败关闭，同时保留
// 归属写本身。该归属写仍向 API 返回成功；只有服务端日志暴露这次失败的咨询查询——入队是可选
// 副作用，抑制它比冒重复工作的险更安全。
func (h *Handler) shouldSuppressActiveSelfAssignment(ctx context.Context, actorType, actorID string, issueID, targetAgentID pgtype.UUID) bool {
	if actorType != "agent" || actorID == "" || actorID != uuidToString(targetAgentID) {
		return false
	}
	active, err := h.hasActiveTaskForIssueAndAgent(ctx, issueID, targetAgentID)
	return active || err != nil
}

// dispatchIssueRun 执行 WillEnqueueRun 产出的入队副作用，可把手写交接说明带进运行开场上下文。
// squad 路径仍走 enqueueSquadLeaderTask，leader 访问闸门与 pending 去重保持在一处。
func (h *Handler) dispatchIssueRun(ctx context.Context, issue db.Issue, trigger service.IssueRunTrigger, actorType, actorID, handoffNote string) {
	switch trigger.AssigneeType {
	case "agent":
		// 执行这次 assign/promote 的成员就是该运行的 accountable 人类（MUL-4302 §4）。
		// agent 角色不是人类，因此只有 member 角色会被带入；否则归属回退到链上。
		_, _ = h.TaskService.EnqueueTaskForIssueWithHandoff(ctx, issue, handoffNote, memberActorUserID(actorType, actorID))
	case "squad":
		h.enqueueSquadLeaderTask(ctx, issue, pgtype.UUID{}, actorType, actorID, handoffNote)
	}
}

// memberActorUserID 在 actor 是 member 时返回其用户 id（pgtype.UUID）；否则返回非法 UUID
// （agent 角色 id 不是人类，绝不能成为 accountable 人类）。用于把 assign/promote actor 带进
// 归属解析器（MUL-4302 §4）。
func memberActorUserID(actorType, actorID string) pgtype.UUID {
	if actorType != "member" {
		return pgtype.UUID{}
	}
	uid, err := util.ParseUUID(actorID)
	if err != nil {
		return pgtype.UUID{}
	}
	return uid
}

// IssueTriggerPreviewRequest 问「如果我给这些 issue 应用这个受理人和/或状态（或新建一条），会
// 启动哪些运行」。所有字段可选；某前瞻字段为 nil 表示「保持不变」。
type IssueTriggerPreviewRequest struct {
	// IssueIDs 是要评估的既有 issue（单条分配、单条状态、或批量）。空 + IsCreate=true 时评估
	// 一条候选新 issue。
	IssueIDs []string `json:"issue_ids"`
	// IsCreate 用 AssigneeType/ID/Status 预览一条尚未持久化的 issue。
	IsCreate     bool    `json:"is_create"`
	AssigneeType *string `json:"assignee_type"`
	AssigneeID   *string `json:"assignee_id"`
	Status       *string `json:"status"`
}

// IssueTriggerPreviewItem 是前瞻写操作下「会启动运行」的一条 issue。AgentID 是可运行 agent
// （squad 为 leader）。HandoffSupported 是软闸门信号：目标 runtime 的 daemon 太老无法渲染交接
// 说明时为 false，UI 可以把说明框置灰而不是悄悄丢文本。分配本身仍生效。
type IssueTriggerPreviewItem struct {
	IssueID          string `json:"issue_id"`
	AgentID          string `json:"agent_id"`
	Source           string `json:"source"`
	HandoffSupported bool   `json:"handoff_supported"`
}

// IssueTriggerPreviewResponse 列出每一条会入队的 issue 与可直接展示的总数（「将启动 N 个」）。
// 不会启动运行的 issue 直接不出现在列表里，所以 total_count == len(triggers)。
type IssueTriggerPreviewResponse struct {
	Triggers   []IssueTriggerPreviewItem `json:"triggers"`
	TotalCount int                       `json:"total_count"`
}

// PreviewIssueTrigger 对一次前瞻 issue 写操作干跑 WillEnqueueRun，返回会启动的运行、零副作用。
// 它是四个入口（创建 / 单条分配 / 单条状态 / 批量）共同咨询的唯一权威，前端不再自己实现
// 入队规则（MUL-3375）。对应 PreviewCommentTriggers。
func (h *Handler) PreviewIssueTrigger(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace is required")
		return
	}

	var req IssueTriggerPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.IssueIDs) > maxPreviewTriggerIssues {
		writeError(w, http.StatusBadRequest, "too many issue_ids")
		return
	}

	// 前瞻受理人只解析一次——畸形 id 是确定的 400，绝不是静默的数错。
	var (
		newAssigneeType pgtype.Text
		newAssigneeID   pgtype.UUID
		hasNewAssignee  bool
	)
	if req.AssigneeType != nil && *req.AssigneeType != "" && req.AssigneeID != nil && *req.AssigneeID != "" {
		id, parseOK := parseUUIDOrBadRequest(w, *req.AssigneeID, "assignee_id")
		if !parseOK {
			return
		}
		newAssigneeType = pgtype.Text{String: *req.AssigneeType, Valid: true}
		newAssigneeID = id
		hasNewAssignee = true
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	resp := IssueTriggerPreviewResponse{Triggers: make([]IssueTriggerPreviewItem, 0)}

	appendTrigger := func(issue db.Issue, in service.IssueTriggerInput) {
		probe := h.issueTriggerPreviewProbe(r, actorType, actorID, workspaceID, issue)
		if trigger, ok := h.IssueService.WillEnqueueRun(r.Context(), in, probe); ok {
			resp.Triggers = append(resp.Triggers, IssueTriggerPreviewItem{
				IssueID:          uuidToString(trigger.IssueID),
				AgentID:          uuidToString(trigger.AgentID),
				Source:           string(trigger.Source),
				HandoffSupported: h.runtimeSupportsHandoff(r.Context(), trigger.AgentID),
			})
		}
	}

	if req.IsCreate {
		wsUUID, err := util.ParseUUID(workspaceID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid workspace")
			return
		}
		status := "todo"
		if req.Status != nil && *req.Status != "" {
			status = *req.Status
		}
		candidate := db.Issue{
			WorkspaceID:  wsUUID,
			Status:       status,
			AssigneeType: newAssigneeType,
			AssigneeID:   newAssigneeID,
		}
		appendTrigger(candidate, service.IssueTriggerInput{Issue: candidate, IsCreate: true})
		resp.TotalCount = len(resp.Triggers)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	for _, rawID := range req.IssueIDs {
		issueUUID, err := util.ParseUUID(rawID)
		if err != nil {
			continue // 畸形 id 不贡献任何触发；行为确定
		}
		loaded, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
			ID:          issueUUID,
			WorkspaceID: parseUUID(workspaceID),
		})
		if err != nil {
			continue // 跨工作区 / 未知 id 不贡献任何触发
		}

		post := loaded
		in := service.IssueTriggerInput{PrevStatus: loaded.Status}
		if hasNewAssignee {
			post.AssigneeType = newAssigneeType
			post.AssigneeID = newAssigneeID
			in.AssigneeChanged = loaded.AssigneeType.String != newAssigneeType.String ||
				uuidToString(loaded.AssigneeID) != uuidToString(newAssigneeID)
		}
		if req.Status != nil && *req.Status != "" {
			post.Status = *req.Status
			in.StatusChanged = loaded.Status != *req.Status
		}
		in.Issue = post
		appendTrigger(post, in)
	}

	resp.TotalCount = len(resp.Triggers)
	writeJSON(w, http.StatusOK, resp)
}

// runtimeSupportsHandoff 判断 agent 绑定的 runtime 上报的 CLI 版本是否足以渲染交接说明。
// 驱动预览的 handoff_supported 软闸门信号。任何解析失败 → false（降级）。
func (h *Handler) runtimeSupportsHandoff(ctx context.Context, agentID pgtype.UUID) bool {
	agent, err := h.Queries.GetAgent(ctx, agentID)
	if err != nil || !agent.RuntimeID.Valid {
		return false
	}
	rt, err := h.getAgentRuntime(ctx, obsmetrics.RuntimeLookupSourceIssue, agent.RuntimeID)
	if err != nil {
		return false
	}
	return agentver.HandoffSupported(readRuntimeCLIVersion(rt.Metadata))
}
