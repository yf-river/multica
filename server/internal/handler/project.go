// 本文件实现「项目」（project）的 CRUD 与项目搜索。
//
// 项目是 workspace 内把一批议题组织起来的容器（类似文件夹），带负责人、起止日期、
// 图标等元数据，还能通过 project_resource（见 project_resource.go）挂接仓库 / 本地
// 目录资源。本文件提供：
//   - ListProjects / GetProject：列表与详情，额外附上议题统计（总量/已完成数）和
//     资源数量；
//   - CreateProject：创建项目时可一并打包创建资源（bundled-create），项目与资源、
//     领域事件在同一事务里提交；
//   - UpdateProject：所有可选字段支持「显式清空」——靠原始 map 区分字段没传与
//     传了 null；
//   - DeleteProject：需要 owner/admin，同一事务里清理 chat 会话的项目绑定与
//     项目级保存视图；
//   - SearchProjects / buildProjectSearchQuery：动态拼 SQL 的项目搜索，标题命中
//     优先、取消的项目降级排序。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ProjectResponse 是项目接口对外返回的 JSON 结构。
type ProjectResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	Icon        *string `json:"icon"`
	Status      string  `json:"status"`
	Priority    string  `json:"priority"`
	LeadType    *string `json:"lead_type"`
	LeadID      *string `json:"lead_id"`
	// StartDate / DueDate 是日历日（"YYYY-MM-DD"），不含时刻或时区——与
	// issue.start_date / issue.due_date 同一契约。
	StartDate  *string `json:"start_date"`
	DueDate    *string `json:"due_date"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
	IssueCount int64   `json:"issue_count"`
	DoneCount  int64   `json:"done_count"`
	// ResourceCount 是通向子集合 /api/projects/{id}/resources 的面包屑数字。
	// 资源本体刻意不进这个载荷，让父元数据与子集合保持分离；需要完整列表的
	// 客户端直接调 ListProjectResources。
	ResourceCount int64 `json:"resource_count"`
}

// projectToResponse 把数据库行 db.Project 转成对外 JSON 结构（不含统计字段，
// 统计由各调用方另行填充）。
func projectToResponse(p db.Project) ProjectResponse {
	return ProjectResponse{
		ID:          uuidToString(p.ID),
		WorkspaceID: uuidToString(p.WorkspaceID),
		Title:       p.Title,
		Description: textToPtr(p.Description),
		Icon:        textToPtr(p.Icon),
		Status:      p.Status,
		Priority:    p.Priority,
		LeadType:    textToPtr(p.LeadType),
		LeadID:      uuidToPtr(p.LeadID),
		StartDate:   dateToPtr(p.StartDate),
		DueDate:     dateToPtr(p.DueDate),
		CreatedAt:   timestampToString(p.CreatedAt),
		UpdatedAt:   timestampToString(p.UpdatedAt),
	}
}

// loadProjectIssueStats 加载单个项目的议题统计（总数与已完成数）。失败时返回 0,0
// ——统计是展示性信息，不值得让整个项目接口失败。
func (h *Handler) loadProjectIssueStats(ctx context.Context, workspaceID, projectID pgtype.UUID) (int64, int64) {
	terminalStatusKeys := h.projectTerminalIssueStatusKeys(ctx, workspaceID)
	stats, err := h.Queries.GetProjectIssueStats(ctx, db.GetProjectIssueStatsParams{
		WorkspaceID:        workspaceID,
		ProjectIds:         []pgtype.UUID{projectID},
		TerminalStatusKeys: terminalStatusKeys,
	})
	if err != nil || len(stats) == 0 {
		return 0, 0
	}
	return stats[0].TotalCount, stats[0].DoneCount
}

// projectTerminalIssueStatusKeys 在自定义状态目录读取失败时，仍然让项目响应保持
// 有用。标准终态键没有 workspace 目录那么全，但能避免把每个项目都渲染成 0/0。
func (h *Handler) projectTerminalIssueStatusKeys(ctx context.Context, workspaceID pgtype.UUID) []string {
	keys, err := h.terminalIssueStatusKeys(ctx, workspaceID)
	if err == nil {
		return keys
	}
	slog.Warn("expand project terminal status categories failed; using canonical keys",
		"workspace_id", uuidToString(workspaceID), "error", err)
	return []string{issuestatus.Done, issuestatus.Cancelled}
}

// loadProjectResourceCount 加载项目的资源数量。失败时返回 0（展示性信息）。
func (h *Handler) loadProjectResourceCount(ctx context.Context, projectID pgtype.UUID) int64 {
	rows, err := h.Queries.GetProjectResourceCounts(ctx, []pgtype.UUID{projectID})
	if err != nil || len(rows) == 0 {
		return 0
	}
	return rows[0].ResourceCount
}

// CreateProjectRequest 是创建项目请求体。Resources 允许在创建项目时一并打包附加
// 资源（bundled-create）。
type CreateProjectRequest struct {
	Title       string                                `json:"title"`
	Description *string                               `json:"description"`
	Icon        *string                               `json:"icon"`
	Status      string                                `json:"status"`
	Priority    string                                `json:"priority"`
	LeadType    *string                               `json:"lead_type"`
	LeadID      *string                               `json:"lead_id"`
	StartDate   *string                               `json:"start_date"`
	DueDate     *string                               `json:"due_date"`
	Resources   []CreateProjectResourceRequestPayload `json:"resources,omitempty"`
}

// CreateProjectResourceRequestPayload 与 CreateProjectResourceRequest 字段一致，
// 但嵌在「创建项目」的载荷里。单独拆一个类型，是为了将来改动独立端点时不会
// 静默破坏这个内嵌表面。
type CreateProjectResourceRequestPayload struct {
	ResourceType string          `json:"resource_type"`
	ResourceRef  json.RawMessage `json:"resource_ref"`
	Label        *string         `json:"label"`
	Position     *int32          `json:"position"`
}

// UpdateProjectRequest 是更新项目请求体。指针字段用于区分「没传（保持原值）」
// 与「显式传 null / 空值（清空）」。
type UpdateProjectRequest struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Icon        *string `json:"icon"`
	Status      *string `json:"status"`
	Priority    *string `json:"priority"`
	LeadType    *string `json:"lead_type"`
	LeadID      *string `json:"lead_id"`
	StartDate   *string `json:"start_date"`
	DueDate     *string `json:"due_date"`
}

// ListProjects 返回 workspace 下的项目列表（GET /api/projects），支持按
// status / priority 过滤，并批量附带议题统计与资源数量。
func (h *Handler) ListProjects(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var statusFilter pgtype.Text
	if s := r.URL.Query().Get("status"); s != "" {
		statusFilter = pgtype.Text{String: s, Valid: true}
	}
	var priorityFilter pgtype.Text
	if p := r.URL.Query().Get("priority"); p != "" {
		priorityFilter = pgtype.Text{String: p, Valid: true}
	}
	projects, err := h.Queries.ListProjects(r.Context(), db.ListProjectsParams{
		WorkspaceID: wsUUID,
		Status:      statusFilter,
		Priority:    priorityFilter,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list projects")
		return
	}

	// 一次性批量拉取所有项目的议题统计与资源数量，避免 N+1 查询。
	statsMap := make(map[string]db.GetProjectIssueStatsRow)
	resourceCountMap := make(map[string]int64)
	if len(projects) > 0 {
		projectIDs := make([]pgtype.UUID, len(projects))
		for i, p := range projects {
			projectIDs[i] = p.ID
		}
		terminalStatusKeys := h.projectTerminalIssueStatusKeys(r.Context(), wsUUID)
		stats, statsErr := h.Queries.GetProjectIssueStats(r.Context(), db.GetProjectIssueStatsParams{
			WorkspaceID:        wsUUID,
			ProjectIds:         projectIDs,
			TerminalStatusKeys: terminalStatusKeys,
		})
		if statsErr == nil {
			for _, s := range stats {
				statsMap[uuidToString(s.ProjectID)] = s
			}
		}
		counts, err := h.Queries.GetProjectResourceCounts(r.Context(), projectIDs)
		if err == nil {
			for _, c := range counts {
				resourceCountMap[uuidToString(c.ProjectID)] = c.ResourceCount
			}
		}
	}

	resp := make([]ProjectResponse, len(projects))
	for i, p := range projects {
		resp[i] = projectToResponse(p)
		if s, ok := statsMap[resp[i].ID]; ok {
			resp[i].IssueCount = s.TotalCount
			resp[i].DoneCount = s.DoneCount
		}
		resp[i].ResourceCount = resourceCountMap[resp[i].ID]
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": resp, "total": len(resp)})
}

// GetProject 返回单个项目详情（GET /api/projects/{id}），含议题统计与资源数量。
func (h *Handler) GetProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	idUUID, ok := parseUUIDOrBadRequest(w, id, "project id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID: idUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	resp := projectToResponse(project)
	resp.IssueCount, resp.DoneCount = h.loadProjectIssueStats(r.Context(), wsUUID, project.ID)
	resp.ResourceCount = h.loadProjectResourceCount(r.Context(), project.ID)
	writeJSON(w, http.StatusOK, resp)
}

// validProjectStatuses / validProjectPriorities 与 project 表上的 CHECK 约束一致
// （migrations 034、035）。CreateProject / UpdateProject 先按它们预校验，让非法枚举
// 值返回带合法清单的干净 400，而不是把 DB 的 CHECK 违例暴露成 500——这正是 #3925
// 里 `--status active` 的教训。
var validProjectStatuses = []string{"planned", "in_progress", "paused", "completed", "cancelled"}
var validProjectPriorities = []string{"urgent", "high", "medium", "low", "none"}

// validateProjectEnum 在 value 不在 allowed 清单内时写出 400 并返回 false；调用方
// 拿到 false 应立刻 return。
func validateProjectEnum(w http.ResponseWriter, field, value string, allowed []string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid %s %q; valid values: %s", field, value, strings.Join(allowed, ", ")))
	return false
}

// writeProjectWriteError 把失败的 project INSERT/UPDATE 映射成 HTTP 响应。CHECK
// 约束违例是客户端错误（400）——status/priority 已有预校验兜底，这里再兜住其它受
// 约束的列（如 lead_type）。其余都是真正的服务器故障：记下底层错误，让瞬时 DB
// 故障可诊断（#3925 当时没有任何服务端信号），并返回 500。
func (h *Handler) writeProjectWriteError(w http.ResponseWriter, r *http.Request, err error, action string) {
	if isCheckViolation(err) {
		writeError(w, http.StatusBadRequest, "project "+action+" rejected: a field value failed a database constraint")
		return
	}
	slog.Error("project "+action+" failed", append(logger.RequestAttrs(r), "error", err)...)
	writeError(w, http.StatusInternalServerError, "failed to "+action+" project")
}

// CreateProject 创建项目（POST）。先校验标题/枚举/负责人/日期与每个内嵌资源
// （bundled-create 在开事务前就校验并规范化），再在单个事务里写入项目、所有资源
// 及其领域事件。带资源创建时回显父级字段 + 刚创建的资源列表。
func (h *Handler) CreateProject(w http.ResponseWriter, r *http.Request) {
	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	status := req.Status
	if status == "" {
		status = "planned"
	}
	if !validateProjectEnum(w, "status", status, validProjectStatuses) {
		return
	}
	priority := req.Priority
	if priority == "" {
		priority = "none"
	}
	if !validateProjectEnum(w, "priority", priority, validProjectPriorities) {
		return
	}
	var leadType pgtype.Text
	var leadID pgtype.UUID
	if req.LeadType != nil {
		leadType = pgtype.Text{String: *req.LeadType, Valid: true}
	}
	if req.LeadID != nil {
		id, ok := parseUUIDOrBadRequest(w, *req.LeadID, "lead_id")
		if !ok {
			return
		}
		leadID = id
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	// start_date / due_date 是可选的日历日；字段缺省或为空则列留 NULL。
	// 与 CreateIssue 的日期处理保持一致。
	var startDate pgtype.Date
	if req.StartDate != nil && *req.StartDate != "" {
		d, err := util.ParseCalendarDate(*req.StartDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid start_date format, expected YYYY-MM-DD")
			return
		}
		startDate = d
	}
	var dueDate pgtype.Date
	if req.DueDate != nil && *req.DueDate != "" {
		d, err := util.ParseCalendarDate(*req.DueDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid due_date format, expected YYYY-MM-DD")
			return
		}
		dueDate = d
	}

	// 在开事务前就预校验每个资源载荷，让非法 ref 以干净的 400 返回、不做任何 DB
	// 工作。local_directory 还要在批次内保证每个 daemon_id 只有一行——daemon 端
	// 解析器按 daemon_id 取第一个匹配，同一 daemon 挂两行会让 agent 静默写进排序
	// 靠前的那行。独立 POST/PUT 路径经 findLocalDirectoryConflict 跑同一检查；
	// 这个循环只覆盖 bundled-create 表面——它还没有既有行可比对。
	normalizedRefs := make([]json.RawMessage, len(req.Resources))
	localDirSeen := map[string]int{}
	for i, res := range req.Resources {
		res.ResourceType = strings.TrimSpace(res.ResourceType)
		if res.ResourceType == "" {
			writeError(w, http.StatusBadRequest, "resources[].resource_type is required")
			return
		}
		ref, err := validateAndNormalizeResourceRef(res.ResourceType, res.ResourceRef)
		if err != nil {
			writeError(w, http.StatusBadRequest, "resources["+strconv.Itoa(i)+"]: "+err.Error())
			return
		}
		normalizedRefs[i] = ref
		if res.ResourceType == "local_directory" {
			var ld localDirectoryRef
			if err := json.Unmarshal(ref, &ld); err != nil {
				writeError(w, http.StatusBadRequest, "resources["+strconv.Itoa(i)+"]: "+err.Error())
				return
			}
			if prev, ok := localDirSeen[ld.DaemonID]; ok {
				writeError(w, http.StatusBadRequest, "resources["+strconv.Itoa(i)+"]: duplicate local_directory for daemon (already at index "+strconv.Itoa(prev)+"); each daemon may attach at most one local_directory per project")
				return
			}
			localDirSeen[ld.DaemonID] = i
			// 独立 POST/PUT 路径会跑的同一个 worktree 闸门。bundled-create 曾经漏掉
			// 它，于是用 worktree local_directory 建出来的项目可能存着机器跑不了的
			// 模式——要到后来认领闸取消任务才暴露。它自己写出 422；在事务之前执行，
			// 拒绝时不留下任何脏数据。
			if !h.requireWorktreeCapableDaemon(w, r, wsUUID, res.ResourceType, ref) {
				return
			}
		}
	}

	createParams := db.CreateProjectParams{
		WorkspaceID: wsUUID,
		Title:       req.Title,
		Description: ptrToText(req.Description),
		Icon:        ptrToText(req.Icon),
		Status:      status,
		LeadType:    leadType,
		LeadID:      leadID,
		Priority:    priority,
		StartDate:   startDate,
		DueDate:     dueDate,
	}

	// 项目与任何打包资源连同各自的持久化事件信封一起提交。事件插入一旦失败，
	// 业务写入一并回滚，不会留下一个无人观察得到的项目。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	project, err := qtx.CreateProject(r.Context(), createParams)
	if err != nil {
		h.writeProjectWriteError(w, r, err, "create")
		return
	}

	creator, _ := h.parseUserUUIDOrZero(userID)
	resourceRows := make([]db.ProjectResource, 0, len(req.Resources))
	for i, res := range req.Resources {
		var label pgtype.Text
		if res.Label != nil && strings.TrimSpace(*res.Label) != "" {
			label = pgtype.Text{String: strings.TrimSpace(*res.Label), Valid: true}
		}
		var position int32 = int32(i)
		if res.Position != nil {
			position = *res.Position
		}
		if _, err = tx.Exec(r.Context(), "SELECT id FROM workspace WHERE id=$1 FOR UPDATE", project.WorkspaceID); err != nil {
			writeError(w, 500, "锁定工作区失败")
			return
		}
		normalizedRefs[i], err = bindRepository(r.Context(), qtx, project.WorkspaceID, res.ResourceType, normalizedRefs[i])
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		row, err := qtx.CreateProjectResource(r.Context(), db.CreateProjectResourceParams{
			ProjectID:    project.ID,
			WorkspaceID:  project.WorkspaceID,
			ResourceType: res.ResourceType,
			ResourceRef:  normalizedRefs[i],
			Label:        label,
			Position:     position,
			CreatedBy:    creator,
		})
		if err != nil {
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "resources["+strconv.Itoa(i)+"]: this resource is already attached")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to attach resource at index "+strconv.Itoa(i))
			return
		}
		resourceRows = append(resourceRows, row)
	}
	projectEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildProjectDomainEvent(
		protocol.EventProjectCreated, project, "member", userID, nil,
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record project event")
		return
	}
	resourceRows, err = resolveRepositoryResources(r.Context(), qtx, project.WorkspaceID, resourceRows)
	if err != nil {
		writeError(w, 500, "解析项目仓库失败")
		return
	}
	repoEvent, err := recordRepositoryChange(r.Context(), qtx, project.WorkspaceID, userID)
	if err != nil {
		writeError(w, 500, "记录仓库变更失败")
		return
	}
	resourceEvents := make([]events.Event, 0, len(resourceRows))
	for _, row := range resourceRows {
		event, err := service.RecordDurableEventTx(r.Context(), qtx, buildProjectResourceDomainEvent(
			protocol.EventProjectResourceCreated, row, "member", userID, nil,
		))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to record project resource event")
			return
		}
		resourceEvents = append(resourceEvents, event)
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit project create")
		return
	}

	resourceResp := make([]ProjectResourceResponse, len(resourceRows))
	for i, row := range resourceRows {
		resourceResp[i] = projectResourceToResponse(row)
	}
	resp := projectToResponse(project)
	resp.ResourceCount = int64(len(resourceResp))
	h.publishEvent(projectEvent)
	h.publishEvent(repoEvent)
	for _, event := range resourceEvents {
		h.publishEvent(event)
	}
	if len(resourceResp) == 0 {
		writeJSON(w, http.StatusCreated, resp)
		return
	}
	// 一次性创建回显：父级 ProjectResponse 字段 + 刚创建的资源。这是「创建瞬间」
	// 的临时回显，不是读接口的契约——GET /projects/{id} 仍然只返回元数据 + 资源数。
	writeJSON(w, http.StatusCreated, struct {
		ProjectResponse
		Resources []ProjectResourceResponse `json:"resources"`
	}{
		ProjectResponse: resp,
		Resources:       resourceResp,
	})
}

// UpdateProject 更新项目（PUT /api/projects/{id}）。所有可选字段支持「显式清空」：
// 用 body 的原始 map 区分字段没传（保持原值）与显式传 null/空（置 NULL/清日期）。
func (h *Handler) UpdateProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	idUUID, ok := parseUUIDOrBadRequest(w, id, "project id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	prevProject, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID: idUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	var req UpdateProjectRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var rawFields map[string]json.RawMessage
	json.Unmarshal(bodyBytes, &rawFields)

	params := db.UpdateProjectParams{ID: prevProject.ID}
	_, params.DescriptionSet = rawFields["description"]
	_, params.IconSet = rawFields["icon"]
	_, params.LeadTypeSet = rawFields["lead_type"]
	_, params.LeadIDSet = rawFields["lead_id"]
	_, params.StartDateSet = rawFields["start_date"]
	_, params.DueDateSet = rawFields["due_date"]
	if req.Title != nil {
		params.Title = pgtype.Text{String: *req.Title, Valid: true}
	}
	if req.Status != nil {
		if !validateProjectEnum(w, "status", *req.Status, validProjectStatuses) {
			return
		}
		params.Status = pgtype.Text{String: *req.Status, Valid: true}
	}
	if req.Priority != nil {
		if !validateProjectEnum(w, "priority", *req.Priority, validProjectPriorities) {
			return
		}
		params.Priority = pgtype.Text{String: *req.Priority, Valid: true}
	}
	if _, ok := rawFields["description"]; ok {
		if req.Description != nil {
			params.Description = pgtype.Text{String: *req.Description, Valid: true}
		} else {
			params.Description = pgtype.Text{Valid: false}
		}
	}
	if _, ok := rawFields["icon"]; ok {
		if req.Icon != nil {
			params.Icon = pgtype.Text{String: *req.Icon, Valid: true}
		} else {
			params.Icon = pgtype.Text{Valid: false}
		}
	}
	if _, ok := rawFields["lead_type"]; ok {
		if req.LeadType != nil {
			params.LeadType = pgtype.Text{String: *req.LeadType, Valid: true}
		} else {
			params.LeadType = pgtype.Text{Valid: false}
		}
	}
	if _, ok := rawFields["lead_id"]; ok {
		if req.LeadID != nil {
			leadUUID, ok := parseUUIDOrBadRequest(w, *req.LeadID, "lead_id")
			if !ok {
				return
			}
			params.LeadID = leadUUID
		} else {
			params.LeadID = pgtype.UUID{Valid: false}
		}
	}
	// 日期遵循 issue 契约：键存在但值为空/null 即清空日期；键不存在则保留原值。
	if _, ok := rawFields["start_date"]; ok {
		if req.StartDate != nil && *req.StartDate != "" {
			d, err := util.ParseCalendarDate(*req.StartDate)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid start_date format, expected YYYY-MM-DD")
				return
			}
			params.StartDate = d
		} else {
			params.StartDate = pgtype.Date{Valid: false} // 显式 null = 清空日期
		}
	}
	if _, ok := rawFields["due_date"]; ok {
		if req.DueDate != nil && *req.DueDate != "" {
			d, err := util.ParseCalendarDate(*req.DueDate)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid due_date format, expected YYYY-MM-DD")
				return
			}
			params.DueDate = d
		} else {
			params.DueDate = pgtype.Date{Valid: false} // 显式 null = 清空日期
		}
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start project transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	project, err := qtx.UpdateProject(r.Context(), params)
	if err != nil {
		h.writeProjectWriteError(w, r, err, "update")
		return
	}
	resp := projectToResponse(project)
	resp.IssueCount, resp.DoneCount = h.loadProjectIssueStats(r.Context(), wsUUID, project.ID)
	resp.ResourceCount = h.loadProjectResourceCount(r.Context(), project.ID)
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildProjectDomainEvent(
		protocol.EventProjectUpdated, project, "member", userID, nil,
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record project event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit project update")
		return
	}
	h.publishEvent(event)
	// 修改 1. 项目描述同样存在库里，agent 运行中改写它时文件 diff 看不到这次改动。
	h.recordRunTextChange(r.Context(), h.Queries, r, workspaceID,
		runTextTarget{Kind: "project", ID: uuidToString(project.ID), Name: project.Title},
		"description", prevProject.Description.String, project.Description.String)
	writeJSON(w, http.StatusOK, resp)
}

// DeleteProject 删除项目（DELETE /api/projects/{id}），要求 owner/admin。同一
// 事务里：给项目行加锁防止并发删除、清理 chat 会话指向该项目的绑定、删除项目级
// 保存视图，最后删项目行并记录事件，成功返回 204。
func (h *Handler) DeleteProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	idUUID, ok := parseUUIDOrBadRequest(w, id, "project id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID: idUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	requester, ok := h.requireWorkspaceRole(w, r, uuidToString(project.WorkspaceID), "project not found", "owner", "admin")
	if !ok {
		return
	}
	userID := uuidToString(requester.UserID)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	if _, err := qtx.LockProjectForDelete(r.Context(), db.LockProjectForDeleteParams{
		ID:          project.ID,
		WorkspaceID: project.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock project")
		return
	}
	if err := qtx.ClearChatSessionProjectByProject(r.Context(), db.ClearChatSessionProjectByProjectParams{
		ProjectID:   project.ID,
		WorkspaceID: project.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear project chat context")
		return
	}
	// 项目级保存视图挂在项目页上；项目没了它们就永远访问不到，所以放进同一个
	// 事务一起删。
	if err := qtx.DeleteIssueViewsByProjectScope(r.Context(), db.DeleteIssueViewsByProjectScopeParams{
		WorkspaceID: project.WorkspaceID,
		ScopeID:     project.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete project views")
		return
	}
	if err := qtx.DeleteProject(r.Context(), db.DeleteProjectParams{
		ID:          project.ID,
		WorkspaceID: project.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete project")
		return
	}
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildProjectDomainEvent(
		protocol.EventProjectDeleted, project, "member", userID, nil,
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record project event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit project delete")
		return
	}
	h.publishEvent(event)
	w.WriteHeader(http.StatusNoContent)
}

// SearchProjectResponse 在 ProjectResponse 基础上扩展搜索元数据：MatchSource 表明
// 命中在标题还是描述，MatchedSnippet 是描述命中时截取的上下文片段。
type SearchProjectResponse struct {
	ProjectResponse
	MatchSource    string  `json:"match_source"`
	MatchedSnippet *string `json:"matched_snippet,omitempty"`
}

// buildProjectSearchQuery 为项目搜索拼一条动态 SQL：同一关键词同时匹配标题与描述，
// 多个词要求逐词 AND 命中；排序上标题精确命中 > 标题前缀 > 标题包含 > 标题全词 >
// 描述包含，取消的项目额外降级。参数统一用 $n 占位并由调用方填充。
func buildProjectSearchQuery(phrase string, terms []string, includeClosed bool) (string, []any) {
	phrase = strings.ToLower(phrase)
	for i, t := range terms {
		terms[i] = strings.ToLower(t)
	}

	argIdx := 1
	args := []any{}
	nextArg := func(val any) string {
		args = append(args, val)
		s := fmt.Sprintf("$%d", argIdx)
		argIdx++
		return s
	}

	escapedPhrase := escapeLike(phrase)
	phraseParam := nextArg(escapedPhrase)
	phraseContains := "'%' || " + phraseParam + " || '%'"
	phraseStartsWith := phraseParam + " || '%'"

	wsParam := nextArg(nil) // workspace_id placeholder

	var termParams []string
	if len(terms) > 1 {
		for _, t := range terms {
			et := escapeLike(t)
			termParams = append(termParams, nextArg(et))
		}
	}

	// --- WHERE 子句 ---
	var whereParts []string

	// 短语整体匹配：标题或描述
	phraseMatch := fmt.Sprintf(
		"(LOWER(p.title) LIKE %s OR LOWER(COALESCE(p.description, '')) LIKE %s)",
		phraseContains, phraseContains,
	)
	whereParts = append(whereParts, phraseMatch)

	// 多词 AND 匹配（每个词都要在某处命中）
	if len(termParams) > 1 {
		var termConditions []string
		for _, tp := range termParams {
			tc := "'%' || " + tp + " || '%'"
			termConditions = append(termConditions, fmt.Sprintf(
				"(LOWER(p.title) LIKE %s OR LOWER(COALESCE(p.description, '')) LIKE %s)",
				tc, tc,
			))
		}
		whereParts = append(whereParts, "("+strings.Join(termConditions, " AND ")+")")
	}

	whereClause := "(" + strings.Join(whereParts, " OR ") + ")"

	if !includeClosed {
		whereClause += " AND p.status NOT IN ('completed', 'cancelled')"
	}

	// --- ORDER BY 排序 ---
	var rankCases []string

	// 第 0 档：标题精确等于短语
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(p.title) = %s THEN 0", phraseParam))

	// 第 1 档：标题以短语开头
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(p.title) LIKE %s THEN 1", phraseStartsWith))

	// 第 2 档：标题包含短语
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(p.title) LIKE %s THEN 2", phraseContains))

	// 第 3 档：标题包含全部词（仅多词查询）
	if len(termParams) > 1 {
		var titleTerms []string
		for _, tp := range termParams {
			titleTerms = append(titleTerms, fmt.Sprintf("LOWER(p.title) LIKE '%s' || %s || '%s'", "%", tp, "%"))
		}
		rankCases = append(rankCases, fmt.Sprintf("WHEN (%s) THEN 3", strings.Join(titleTerms, " AND ")))
	}

	// 第 4 档：描述包含短语
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(COALESCE(p.description, '')) LIKE %s THEN 4", phraseContains))

	rankExpr := "CASE " + strings.Join(rankCases, " ") + " ELSE 5 END"

	// 取消的项目是搁置的工作。项目搜索没有别的状态排序，而命令面板把项目渲染在
	// 议题之上，不加处理的话，一个取消的项目会出现在结果列表的第一行。放在
	// rankExpr 之前降级；与议题搜索（见 buildSearchQuery）一样保留「精确命中标题」
	// 例外：标题完全一致说明用户就是在找那个项目。
	cancelledRank := fmt.Sprintf(
		"CASE WHEN p.status = 'cancelled' AND LOWER(p.title) <> %s THEN 1 ELSE 0 END",
		phraseParam,
	)

	// --- match_source 表达式 ---
	matchSourceExpr := fmt.Sprintf(`CASE
		WHEN LOWER(p.title) LIKE %s THEN 'title'
		ELSE 'description'
	END`, phraseContains)

	if len(termParams) > 1 {
		var titleTerms []string
		for _, tp := range termParams {
			titleTerms = append(titleTerms, fmt.Sprintf("LOWER(p.title) LIKE '%s' || %s || '%s'", "%", tp, "%"))
		}
		matchSourceExpr = fmt.Sprintf(`CASE
			WHEN LOWER(p.title) LIKE %s THEN 'title'
			WHEN (%s) THEN 'title'
			ELSE 'description'
		END`,
			phraseContains, strings.Join(titleTerms, " AND "),
		)
	}

	limitParam := nextArg(nil)
	offsetParam := nextArg(nil)

	query := fmt.Sprintf(`SELECT p.id, p.workspace_id, p.title, p.description, p.icon,
		p.status, p.priority, p.lead_type, p.lead_id,
		p.start_date, p.due_date,
		p.created_at, p.updated_at,
		COUNT(*) OVER() AS total_count,
		%s AS match_source
	FROM project p
	WHERE p.workspace_id = %s AND %s
	ORDER BY %s, %s, p.updated_at DESC
	LIMIT %s OFFSET %s`,
		matchSourceExpr,
		wsParam,
		whereClause,
		cancelledRank,
		rankExpr,
		limitParam,
		offsetParam,
	)

	return query, args
}

// SearchProjects 处理项目搜索（GET /api/projects/search）：解析 q / limit / offset /
// include_closed 参数，执行动态 SQL，并为结果批量补上议题统计与资源数量。语句超时
// 映射为 503。
func (h *Handler) SearchProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	workspaceID := h.resolveWorkspaceID(r)

	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "q parameter is required")
		return
	}

	limit := 20
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 50 {
		limit = 50
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	includeClosed := r.URL.Query().Get("include_closed") == "true"

	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	terms := splitSearchTerms(q)

	sqlQuery, args := buildProjectSearchQuery(q, terms, includeClosed)
	args[1] = wsUUID
	args[len(args)-2] = limit
	args[len(args)-1] = offset

	type projectSearchRow struct {
		project     db.Project
		totalCount  int64
		matchSource string
	}

	var results []projectSearchRow
	err := runSearchQuery(ctx, h.TxStarter, sqlQuery, args, func(rows pgx.Rows) error {
		for rows.Next() {
			var row projectSearchRow
			if err := rows.Scan(
				&row.project.ID,
				&row.project.WorkspaceID,
				&row.project.Title,
				&row.project.Description,
				&row.project.Icon,
				&row.project.Status,
				&row.project.Priority,
				&row.project.LeadType,
				&row.project.LeadID,
				&row.project.StartDate,
				&row.project.DueDate,
				&row.project.CreatedAt,
				&row.project.UpdatedAt,
				&row.totalCount,
				&row.matchSource,
			); err != nil {
				return fmt.Errorf("scan: %w", err)
			}
			results = append(results, row)
		}
		return rows.Err()
	})
	if err != nil {
		// 语句超时表现为 SQLSTATE 57014——与 SearchIssues 相同的快速失败契约
		// （见 runSearchQuery）。
		if isSearchStatementTimeout(err) {
			slog.Warn("search projects timed out",
				"workspace_id", workspaceID,
				"query", q,
				"timeout", searchStatementTimeout)
			writeError(w, http.StatusServiceUnavailable, "search timed out; please refine your query or try again")
			return
		}
		slog.Warn("search projects failed", "error", err, "workspace_id", workspaceID, "query", q)
		writeError(w, http.StatusInternalServerError, "failed to search projects")
		return
	}

	var total int64
	if len(results) > 0 {
		total = results[0].totalCount
	}

	// 批量拉取命中项目的议题统计与资源数量，避免逐条查询。
	statsMap := make(map[string]db.GetProjectIssueStatsRow)
	resourceCountMap := make(map[string]int64)
	if len(results) > 0 {
		projectIDs := make([]pgtype.UUID, len(results))
		for i, r := range results {
			projectIDs[i] = r.project.ID
		}
		terminalStatusKeys := h.projectTerminalIssueStatusKeys(ctx, wsUUID)
		stats, statsErr := h.Queries.GetProjectIssueStats(ctx, db.GetProjectIssueStatsParams{
			WorkspaceID:        wsUUID,
			ProjectIds:         projectIDs,
			TerminalStatusKeys: terminalStatusKeys,
		})
		if statsErr == nil {
			for _, s := range stats {
				statsMap[uuidToString(s.ProjectID)] = s
			}
		}
		counts, err := h.Queries.GetProjectResourceCounts(ctx, projectIDs)
		if err == nil {
			for _, c := range counts {
				resourceCountMap[uuidToString(c.ProjectID)] = c.ResourceCount
			}
		}
	}

	resp := make([]SearchProjectResponse, len(results))
	for i, row := range results {
		pr := projectToResponse(row.project)
		if s, ok := statsMap[pr.ID]; ok {
			pr.IssueCount = s.TotalCount
			pr.DoneCount = s.DoneCount
		}
		pr.ResourceCount = resourceCountMap[pr.ID]
		spr := SearchProjectResponse{
			ProjectResponse: pr,
			MatchSource:     row.matchSource,
		}
		if row.matchSource == "description" {
			desc := ""
			if row.project.Description.Valid {
				desc = row.project.Description.String
			}
			if desc != "" {
				snippet := extractSnippet(desc, q)
				spr.MatchedSnippet = &snippet
			}
		}
		resp[i] = spr
	}

	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	writeJSON(w, http.StatusOK, map[string]any{
		"projects": resp,
		"total":    total,
	})
}
