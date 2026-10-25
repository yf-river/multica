// 本文件：squad（智能体小队）的 HTTP 接口层——小队 CRUD、成员管理、成员状态汇总、领袖
// 评估记录与触发逻辑。职责边界要点：小队全工作区可见，但「管理」（改名/加人/归档）仅限
// 工作区 owner/admin 或小队的创建者（canManageSquad）；普通创建者只能把「自己可 @ 触发」
// 的 Agent 接入小队（memberCanWireAgent），防止把人拽进路由就能调他调不了的 Agent。
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ── 响应类型 ─────────────────────────────────────────────────────────────

// SquadResponse 是小队对外返回的 JSON 结构（含成员数与头像预览）。
type SquadResponse struct {
	ID            string                       `json:"id"`
	WorkspaceID   string                       `json:"workspace_id"`
	Name          string                       `json:"name"`
	Description   string                       `json:"description"`
	Instructions  string                       `json:"instructions"`
	AvatarURL     *string                      `json:"avatar_url"`
	LeaderID      string                       `json:"leader_id"`
	CreatorID     string                       `json:"creator_id"`
	CreatedAt     string                       `json:"created_at"`
	UpdatedAt     string                       `json:"updated_at"`
	ArchivedAt    *string                      `json:"archived_at"`
	ArchivedBy    *string                      `json:"archived_by"`
	MemberCount   int                          `json:"member_count"`
	MemberPreview []SquadMemberPreviewResponse `json:"member_preview"`
}

// SquadMemberPreviewResponse 是成员预览行（列表里头像那排），只含最小标识。
type SquadMemberPreviewResponse struct {
	MemberType string `json:"member_type"`
	MemberID   string `json:"member_id"`
	Role       string `json:"role"`
}

// squadMemberSummary 是内部聚合：成员总数 + 最多 3 行的预览。
type squadMemberSummary struct {
	count   int
	preview []SquadMemberPreviewResponse
}

// SquadMemberResponse 是单个成员的对外 JSON。
type SquadMemberResponse struct {
	ID         string `json:"id"`
	SquadID    string `json:"squad_id"`
	MemberType string `json:"member_type"`
	MemberID   string `json:"member_id"`
	Role       string `json:"role"`
	CreatedAt  string `json:"created_at"`
}

// ── 转换器 ─────────────────────────────────────────────────────────────

// squadToResponse 把 db.Squad 转成对外 JSON（MemberPreview 先置空数组，等摘要回填）。
func (h *Handler) squadToResponse(s db.Squad) SquadResponse {
	return SquadResponse{
		ID:            uuidToString(s.ID),
		WorkspaceID:   uuidToString(s.WorkspaceID),
		Name:          s.Name,
		Description:   s.Description,
		Instructions:  s.Instructions,
		AvatarURL:     h.resolveAvatarURLPtr(textToPtr(s.AvatarUrl)),
		LeaderID:      uuidToString(s.LeaderID),
		CreatorID:     uuidToString(s.CreatorID),
		CreatedAt:     timestampToString(s.CreatedAt),
		UpdatedAt:     timestampToString(s.UpdatedAt),
		ArchivedAt:    timestampToPtr(s.ArchivedAt),
		ArchivedBy:    uuidToPtr(s.ArchivedBy),
		MemberPreview: []SquadMemberPreviewResponse{},
	}
}

// squadMemberToResponse 把 db.SquadMember 转成对外 JSON。
func squadMemberToResponse(m db.SquadMember) SquadMemberResponse {
	return SquadMemberResponse{
		ID:         uuidToString(m.ID),
		SquadID:    uuidToString(m.SquadID),
		MemberType: m.MemberType,
		MemberID:   uuidToString(m.MemberID),
		Role:       m.Role,
		CreatedAt:  timestampToString(m.CreatedAt),
	}
}

// addSquadMemberPreview 累加计数并把成员塞进预览；预览超过 3 行就不再追加。
func addSquadMemberPreview(summary *squadMemberSummary, memberType string, memberID pgtype.UUID, role string) {
	summary.count++
	if len(summary.preview) >= 3 {
		return
	}
	summary.preview = append(summary.preview, SquadMemberPreviewResponse{
		MemberType: memberType,
		MemberID:   uuidToString(memberID),
		Role:       role,
	})
}

// applySquadMemberSummary 把聚合结果回填进响应结构。
func applySquadMemberSummary(resp *SquadResponse, summary *squadMemberSummary) {
	if summary == nil {
		return
	}
	resp.MemberCount = summary.count
	resp.MemberPreview = summary.preview
}

// ── 辅助函数 ─────────────────────────────────────────────────────────────

// canManageSquad 判断成员能否改动该小队。工作区 owner/admin 可管理任何小队；普通成员只能
// 管理自己创建的小队。管理权保持在创建者范围内，小队本身却全工作区可见（ListSquads 不过滤）。
// 与前端每个小队的 `canManage` 门槛保持一致，让 UI 与 API 对「谁能改名/加人/归档」的判断
// 一致（MUL-4223）。
func canManageSquad(member db.Member, squad db.Squad) bool {
	if roleAllowed(member.Role, "owner", "admin") {
		return true
	}
	return uuidToString(squad.CreatorID) == uuidToString(member.UserID)
}

// memberCanWireAgent 判断操作成员能否把指定 Agent 接进小队（当领袖或成员）。工作区
// owner/admin 可接任何工作区 Agent——他们的管理面不变。普通成员（管理自己小队的创建者）
// 只能接自己可 @ 触发的 Agent：以成员本人身份按 canInvokeAgent 判断，于是他们白名单上的
// public_to Agent 和自己名下的私有 Agent 通过，别人的私有/不在白名单的 Agent 被拒。这防止
// 创建者把一个自己调不动的 Agent 偷渡进小队、再借 squad 路由去够它（MUL-4223）。
func (h *Handler) memberCanWireAgent(ctx context.Context, member db.Member, agent db.Agent, workspaceID string) bool {
	if roleAllowed(member.Role, "owner", "admin") {
		return true
	}
	uid := uuidToString(member.UserID)
	return h.canInvokeAgent(ctx, agent, "member", uid, uid, workspaceID)
}

// loadSquadInWorkspace 按当前工作区范围加载小队；找不到就写 404 并返回 ok=false。
func (h *Handler) loadSquadInWorkspace(w http.ResponseWriter, r *http.Request) (db.Squad, string, bool) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	squadID := chi.URLParam(r, "id")
	squadUUID, ok := parseUUIDOrBadRequest(w, squadID, "squad id")
	if !ok {
		return db.Squad{}, "", false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return db.Squad{}, "", false
	}
	squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
		ID:          squadUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "squad not found")
		return db.Squad{}, "", false
	}
	return squad, workspaceID, true
}

// loadSquadMemberSummary 汇总小队成员：总数 + 预览行。
func (h *Handler) loadSquadMemberSummary(ctx context.Context, squadID pgtype.UUID) (*squadMemberSummary, error) {
	rows, err := h.Queries.ListSquadMemberPreviewRowsBySquad(ctx, squadID)
	if err != nil {
		return nil, err
	}
	summary := &squadMemberSummary{}
	for _, row := range rows {
		addSquadMemberPreview(summary, row.MemberType, row.MemberID, row.Role)
	}
	return summary, nil
}

// squadToResponseWithPreview 组小队响应并带上成员摘要。
func (h *Handler) squadToResponseWithPreview(ctx context.Context, squad db.Squad) (SquadResponse, error) {
	resp := h.squadToResponse(squad)
	summary, err := h.loadSquadMemberSummary(ctx, squad.ID)
	if err != nil {
		return resp, err
	}
	applySquadMemberSummary(&resp, summary)
	return resp, nil
}

// ── Handlers ─────────────────────────────────────────────────────────────

// ListSquads 处理 GET /api/workspaces/{workspaceId}/squads——列出工作区全部小队（不过滤），
// 每行带成员预览。
func (h *Handler) ListSquads(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	squads, err := h.Queries.ListSquads(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list squads")
		return
	}

	previewRows, err := h.Queries.ListSquadMemberPreviewRows(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list squad member preview")
		return
	}
	summaries := make(map[string]*squadMemberSummary, len(squads))
	for _, row := range previewRows {
		squadID := uuidToString(row.SquadID)
		summary := summaries[squadID]
		if summary == nil {
			summary = &squadMemberSummary{}
			summaries[squadID] = summary
		}
		addSquadMemberPreview(summary, row.MemberType, row.MemberID, row.Role)
	}

	resp := make([]SquadResponse, len(squads))
	for i, s := range squads {
		resp[i] = h.squadToResponse(s)
		applySquadMemberSummary(&resp[i], summaries[uuidToString(s.ID)])
	}
	writeJSON(w, http.StatusOK, resp)
}

// CreateSquad 处理 POST /api/workspaces/{workspaceId}/squads——创建工作区小队并自动把领袖
// 加成 role="leader" 的成员。
func (h *Handler) CreateSquad(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	// 任何工作区成员都能建小队并成为其创建者（下面的 CreatorID）。这与 Agent/项目一致——
	// 它们也是成员可建的；管理权保持在创建者范围内（MUL-4223）。
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}

	var req struct {
		Name        string  `json:"name"`
		Description string  `json:"description"`
		LeaderID    string  `json:"leader_id"`
		AvatarURL   *string `json:"avatar_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.LeaderID == "" {
		writeError(w, http.StatusBadRequest, "leader_id is required")
		return
	}

	leaderUUID, ok := parseUUIDOrBadRequest(w, req.LeaderID, "leader_id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	// 校验领袖是本工作区的 Agent。
	leaderAgent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          leaderUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "leader must be a valid agent in this workspace")
		return
	}
	// 非 admin 创建者只能用自己可 @ 触发的 Agent 当领袖；admin 可接任何工作区 Agent（MUL-4223）。
	if !h.memberCanWireAgent(r.Context(), member, leaderAgent, workspaceID) {
		writeError(w, http.StatusForbidden, "you can only use an agent you have access to as leader")
		return
	}

	avatarURL := pgtype.Text{}
	if req.AvatarURL != nil {
		accepted, ok := h.acceptAvatarURL(w, r, *req.AvatarURL, "")
		if !ok {
			return
		}
		avatarURL = pgtype.Text{String: accepted, Valid: true}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start squad transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	squad, err := qtx.CreateSquad(r.Context(), db.CreateSquadParams{
		WorkspaceID: wsUUID,
		Name:        req.Name,
		Description: req.Description,
		LeaderID:    leaderUUID,
		CreatorID:   member.UserID,
		AvatarUrl:   avatarURL,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create squad")
		return
	}

	// 自动把领袖加为 role="leader" 的成员。
	if _, err := qtx.AddSquadMember(r.Context(), db.AddSquadMemberParams{
		SquadID:    squad.ID,
		MemberType: "agent",
		MemberID:   leaderUUID,
		Role:       "leader",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add squad leader")
		return
	}
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildSquadDomainEvent(
		protocol.EventSquadCreated, squad, "member", uuidToString(member.UserID), nil,
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record squad event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit squad")
		return
	}

	resp, err := h.squadToResponseWithPreview(r.Context(), squad)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load squad member preview")
		return
	}
	h.publishEvent(event)
	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.SquadCreated(
		uuidToString(member.UserID),
		workspaceID,
		uuidToString(squad.ID),
		1,
	))
	writeJSON(w, http.StatusCreated, resp)
}

// GetSquad 处理 GET /api/workspaces/{workspaceId}/squads/{id}——返回单个小队详情（含成员
// 预览）。找不到回 404。
func (h *Handler) GetSquad(w http.ResponseWriter, r *http.Request) {
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	resp, err := h.squadToResponseWithPreview(r.Context(), squad)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load squad member preview")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// UpdateSquad 处理 PATCH /api/workspaces/{workspaceId}/squads/{id}——局部更新小队
// （名/描述/指示/领袖/头像）。需 canManageSquad 管理权。
func (h *Handler) UpdateSquad(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}

	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if !canManageSquad(member, squad) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var req struct {
		Name         *string `json:"name"`
		Description  *string `json:"description"`
		Instructions *string `json:"instructions"`
		LeaderID     *string `json:"leader_id"`
		AvatarURL    *string `json:"avatar_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	params := db.UpdateSquadParams{ID: squad.ID}
	if req.Name != nil {
		params.Name = pgtype.Text{String: *req.Name, Valid: true}
	}
	if req.Description != nil {
		params.Description = pgtype.Text{String: *req.Description, Valid: true}
	}
	if req.Instructions != nil {
		params.Instructions = pgtype.Text{String: *req.Instructions, Valid: true}
	}
	if req.AvatarURL != nil {
		accepted, ok := h.acceptAvatarURL(w, r, *req.AvatarURL, squad.AvatarUrl.String)
		if !ok {
			return
		}
		params.AvatarUrl = pgtype.Text{String: accepted, Valid: true}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update squad")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// Autopilot 指派在锁领袖 Agent 之前先对 squad 取 FOR SHARE。这里按同样顺序取排他侧，
	// 让领袖轮换与进行中的 Autopilot 保存不会留下一个指向未绑定有效 Agent 的自动化。
	if _, err := qtx.LockSquadForUpdate(r.Context(), db.LockSquadForUpdateParams{
		ID:          squad.ID,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update squad")
		return
	}

	newLeaderRuntimeBound := true
	if req.LeaderID != nil {
		lid, ok := parseUUIDOrBadRequest(w, *req.LeaderID, "leader_id")
		if !ok {
			return
		}
		// 让 runtime_id 稳定到提交后。runtime 拆解对该行取 FOR UPDATE、并遵循同样的
		// Agent→Autopilot 锁顺序，所以谁先开始谁就产出完整结果。
		newLeader, err := qtx.LockAgentForAutopilotAssignment(r.Context(), db.LockAgentForAutopilotAssignmentParams{
			ID:          lid,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "leader must be a valid agent in this workspace")
			return
		}
		// 非 admin 创建者只能提拔自己可 @ 触发的 Agent。
		if !h.memberCanWireAgent(r.Context(), member, newLeader, workspaceID) {
			writeError(w, http.StatusForbidden, "you can only use an agent you have access to as leader")
			return
		}
		// 确保新领袖是小队成员；不是就自动补加。
		isMember, err := qtx.IsSquadMember(r.Context(), db.IsSquadMemberParams{
			SquadID: squad.ID, MemberType: "agent", MemberID: lid,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update squad")
			return
		}
		if !isMember {
			if _, err := qtx.AddSquadMember(r.Context(), db.AddSquadMemberParams{
				SquadID: squad.ID, MemberType: "agent", MemberID: lid, Role: "leader",
			}); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to update squad")
				return
			}
		}
		params.LeaderID = lid
		newLeaderRuntimeBound = newLeader.RuntimeID.Valid
	}

	updated, err := qtx.UpdateSquad(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update squad")
		return
	}
	var pausedAutopilots []db.Autopilot
	if req.LeaderID != nil && !newLeaderRuntimeBound {
		pausedAutopilots, err = qtx.PauseAutopilotsByUnrunnableSquad(r.Context(), squad.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update squad")
			return
		}
	}
	eventsToPublish := make([]events.Event, 0, 1+len(pausedAutopilots))
	squadEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildSquadDomainEvent(
		protocol.EventSquadUpdated, updated, "member", requestUserID(r), nil,
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record squad event")
		return
	}
	eventsToPublish = append(eventsToPublish, squadEvent)
	for _, autopilot := range pausedAutopilots {
		autopilotEvent, err := service.RecordDurableEventTx(r.Context(), qtx, events.Event{
			Type:           protocol.EventAutopilotUpdated,
			IdempotencyKey: "autopilot:squad_pause:" + uuidToString(autopilot.ID) + ":" + autopilot.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
			StreamKey:      "autopilot:" + uuidToString(autopilot.ID), WorkspaceID: workspaceID,
			ActorType: "member", ActorID: requestUserID(r),
			Payload: map[string]any{"autopilot": autopilotToResponse(autopilot, nil)},
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to record autopilot event")
			return
		}
		eventsToPublish = append(eventsToPublish, autopilotEvent)
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update squad")
		return
	}

	resp, err := h.squadToResponseWithPreview(r.Context(), updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load squad member preview")
		return
	}
	for _, event := range eventsToPublish {
		h.publishEvent(event)
	}
	writeJSON(w, http.StatusOK, resp)
}

// DeleteSquad 处理 DELETE /api/workspaces/{workspaceId}/squads/{id}——把该小队负责的 issue
// 与 autopilot 交接给领袖，再归档小队。需 canManageSquad；已归档回 400。
func (h *Handler) DeleteSquad(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}

	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if !canManageSquad(member, squad) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	if squad.ArchivedAt.Valid {
		writeError(w, http.StatusBadRequest, "squad is already archived")
		return
	}

	// 在同一个事务里转移指派并归档小队，这样删除事件不会与仍看到旧负责人的消费者赛跑。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start squad transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := qtx.TransferSquadAssignees(r.Context(), db.TransferSquadAssigneesParams{
		AssigneeID:   squad.ID,
		AssigneeID_2: squad.LeaderID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to transfer squad assignees")
		return
	}

	// 把指向该小队的 autopilot 一并镜像成指向领袖的 issue-指派转移。不做的话
	// autopilot.assignee_id 仍指向已归档的小队行，之后的每次派发都会以 "assignee squad is
	// archived" 跳过——ops 看得见，对 owner 却毫无用处。改写给领袖后 autopilot 语义不变
	// （MUL-2429 的 Path A 本来就是领袖独跑）。
	if err := qtx.TransferSquadAutopilotsToLeader(r.Context(), db.TransferSquadAutopilotsToLeaderParams{
		AssigneeID:   squad.ID,
		AssigneeID_2: squad.LeaderID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to transfer squad autopilots")
		return
	}

	userID := requestUserID(r)
	userUUID, _ := parseUUIDOrBadRequest(w, userID, "user_id")

	archived, err := qtx.ArchiveSquad(r.Context(), db.ArchiveSquadParams{
		ID:         squad.ID,
		ArchivedBy: userUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive squad")
		return
	}
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildSquadDomainEvent(protocol.EventSquadDeleted, archived, "member", userID, map[string]any{
		"leader_id": uuidToString(squad.LeaderID),
	}))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record squad event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit squad archive")
		return
	}
	h.publishEvent(event)
	w.WriteHeader(http.StatusNoContent)
}

// ── Squad 成员 ───────────────────────────────────────────────────────────

// ListSquadMembers 处理 GET /api/workspaces/{workspaceId}/squads/{id}/members——列出小队
// 全部成员。
func (h *Handler) ListSquadMembers(w http.ResponseWriter, r *http.Request) {
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	members, err := h.Queries.ListSquadMembers(r.Context(), squad.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list squad members")
		return
	}
	resp := make([]SquadMemberResponse, len(members))
	for i, m := range members {
		resp[i] = squadMemberToResponse(m)
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── Squad 成员状态 ────────────────────────────────────────────────────────

// SquadMemberStatusResponse 是成员状态响应里的「每个成员一行」。Agent 成员带派生的
// working/idle/offline/unstable 状态和进行中的 issue；人类成员只带 member_type 返回，前端
// 能在同一张表里渲染而不必重排。
type SquadMemberStatusResponse struct {
	MemberType   string                  `json:"member_type"`
	MemberID     string                  `json:"member_id"`
	Status       *string                 `json:"status"`
	ActiveIssues []SquadActiveIssueBrief `json:"active_issues"`
	LastActiveAt *string                 `json:"last_active_at"`
}

// SquadActiveIssueBrief 是状态列表里某个成员当前进行中 issue 的摘要。
type SquadActiveIssueBrief struct {
	IssueID     string `json:"issue_id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	IssueStatus string `json:"issue_status"`
}

// SquadMemberStatusListResponse 是成员状态列表的整体响应。
type SquadMemberStatusListResponse struct {
	Members []SquadMemberStatusResponse `json:"members"`
}

// deriveRuntimeAvailability owns the shared three-state runtime reachability
// policy. Agent projections use these values directly; squad presence maps an
// online runtime to its user-facing "idle" bucket after workload precedence.
func deriveRuntimeAvailability(
	runtimeStatus pgtype.Text,
	lastSeen pgtype.Timestamptz,
	now time.Time,
) string {
	if !runtimeStatus.Valid {
		return "offline"
	}
	if runtimeStatus.String == "online" {
		return "online"
	}
	if !lastSeen.Valid {
		return "offline"
	}
	if now.Sub(lastSeen.Time) < 5*time.Minute {
		return "unstable"
	}
	return "offline"
}

// deriveSquadMemberStatus collapses runtime + task signals into the five
// status buckets used by the squad UI. Mirrors the workload+availability
// split in packages/core/agents/derive-presence.ts: working wins over
// runtime health (an agent that is in the middle of dispatched/running
// work counts as working even if the runtime briefly drops), then
// availability buckets decide between idle / unstable / offline.
//
// 阈值与 deriveRuntimeHealth 一致：任何 offline runtime 只要 last_seen_at 在过去 5 分钟内，
// 就报 "unstable"，让 squad UI 与 agent 圆点用同样方式呈现瞬态掉线。
//
// 已归档的 Agent 永远报 `archived`，不管是否残留 runtime 行或任务——它们应该出现在列表里，
// 但绝不能看起来还在干活或只是离线（残留的 online runtime 行会被误读成 "offline"，掩盖
// Agent 已被归档的事实）。按 RFC 决定（见 MUL-2319），本端点保留归档 Agent 而不在 SQL 里
// 过滤。
func deriveSquadMemberStatus(
	archived bool,
	runtimeStatus pgtype.Text,
	lastSeen pgtype.Timestamptz,
	hasWorkingTask bool,
	now time.Time,
) string {
	if archived {
		return "archived"
	}
	if hasWorkingTask {
		return "working"
	}
	availability := deriveRuntimeAvailability(runtimeStatus, lastSeen, now)
	if availability == "online" {
		return "idle"
	}
	return availability
}

// ListSquadMemberStatus 返回每个小队成员一行：派生状态、该 Agent 成员当前正在跑/排队等的
// issue，以及最近一次 runtime 活动。端点只读，继承路由中间件的工作区成员守卫——工作区任何
// 成员都能读。
func (h *Handler) ListSquadMemberStatus(w http.ResponseWriter, r *http.Request) {
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}

	rows, err := h.Queries.ListSquadMemberStatusRows(r.Context(), squad.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list squad member status")
		return
	}

	prefix := h.getIssuePrefix(r.Context(), squad.WorkspaceID)
	now := time.Now()

	// 按 member_id 分组，同时保留 SQL 的 ORDER BY（squad_member 插入序）。一个成员有多条
	// 活跃任务时会在多行里出现。
	type memberAcc struct {
		response       SquadMemberStatusResponse
		archived       bool
		hasWorkingTask bool
		runtimeStatus  pgtype.Text
		runtimeSeenAt  pgtype.Timestamptz
		latestActiveAt pgtype.Timestamptz
	}
	order := make([]string, 0, len(rows))
	acc := make(map[string]*memberAcc, len(rows))

	for _, row := range rows {
		memberID := uuidToString(row.MemberID)
		entry, exists := acc[memberID]
		if !exists {
			entry = &memberAcc{
				response: SquadMemberStatusResponse{
					MemberType:   row.MemberType,
					MemberID:     memberID,
					ActiveIssues: []SquadActiveIssueBrief{},
				},
				archived:      row.AgentArchivedAt.Valid,
				runtimeStatus: row.RuntimeStatus,
				runtimeSeenAt: row.RuntimeLastSeenAt,
			}
			acc[memberID] = entry
			order = append(order, memberID)
		}

		if row.MemberType != "agent" {
			continue
		}

		// waiting_local_directory 行保留着供 issue 可见性用，但只有 dispatched/running 才推
		// 动 `working` 桶。工作中的任务可能没有 issue（聊天/quick-create），所以桶的判定与
		// 能否渲染 issue 链接互不依赖。
		if row.TaskID.Valid {
			if row.TaskStatus.Valid &&
				(row.TaskStatus.String == "dispatched" || row.TaskStatus.String == "running") {
				entry.hasWorkingTask = true
			}

			if row.TaskIssueID.Valid {
				brief := SquadActiveIssueBrief{
					IssueID:    uuidToString(row.TaskIssueID),
					Identifier: prefix + "-" + strconv.Itoa(int(row.IssueNumber.Int32)),
					Title:      row.IssueTitle.String,
					IssueStatus: func() string {
						if row.IssueStatus.Valid {
							return row.IssueStatus.String
						}
						return ""
					}(),
				}
				entry.response.ActiveIssues = append(entry.response.ActiveIssues, brief)
			}

			if row.TaskDispatchedAt.Valid && (!entry.latestActiveAt.Valid ||
				row.TaskDispatchedAt.Time.After(entry.latestActiveAt.Time)) {
				entry.latestActiveAt = row.TaskDispatchedAt
			}
		}
	}

	resp := SquadMemberStatusListResponse{
		Members: make([]SquadMemberStatusResponse, 0, len(order)),
	}
	for _, id := range order {
		entry := acc[id]
		if entry.response.MemberType == "agent" {
			status := deriveSquadMemberStatus(
				entry.archived,
				entry.runtimeStatus,
				entry.runtimeSeenAt,
				entry.hasWorkingTask,
				now,
			)
			entry.response.Status = &status
			// last_active_at 优先取最新一次活跃任务派发时刻而不是 runtime 心跳：正在干活的
			// Agent 不该因为 runtime 心跳慢了几秒就显得陈旧。否则回退到 runtime 的
			// last_seen_at。
			if entry.latestActiveAt.Valid {
				entry.response.LastActiveAt = timestampToPtr(entry.latestActiveAt)
			} else if entry.runtimeSeenAt.Valid {
				entry.response.LastActiveAt = timestampToPtr(entry.runtimeSeenAt)
			}
		}
		resp.Members = append(resp.Members, entry.response)
	}

	writeJSON(w, http.StatusOK, resp)
}

// AddSquadMember 处理 POST /api/workspaces/{workspaceId}/squads/{id}/members——把 Agent 或
// 人类成员加进小队。需 canManageSquad；已在队内回 409。
func (h *Handler) AddSquadMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}

	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if !canManageSquad(member, squad) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var req struct {
		MemberType string `json:"member_type"`
		MemberID   string `json:"member_id"`
		Role       string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.MemberType != "agent" && req.MemberType != "member" {
		writeError(w, http.StatusBadRequest, "member_type must be 'agent' or 'member'")
		return
	}
	if req.MemberID == "" {
		writeError(w, http.StatusBadRequest, "member_id is required")
		return
	}

	memberUUID, ok := parseUUIDOrBadRequest(w, req.MemberID, "member_id")
	if !ok {
		return
	}

	// 校验成员属于本工作区。
	if req.MemberType == "agent" {
		agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID: memberUUID, WorkspaceID: wsUUID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "agent not found in this workspace")
			return
		}
		// 非 admin 创建者只能加自己可 @ 触发的 Agent（public 或自己/白名单的）；admin 可加
		// 任何工作区 Agent（MUL-4223）。
		if !h.memberCanWireAgent(r.Context(), member, agent, workspaceID) {
			writeError(w, http.StatusForbidden, "you can only add an agent you have access to")
			return
		}
	} else {
		if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
			UserID: memberUUID, WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "member not found in this workspace")
			return
		}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start squad transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	sm, err := qtx.AddSquadMember(r.Context(), db.AddSquadMemberParams{
		SquadID:    squad.ID,
		MemberType: req.MemberType,
		MemberID:   memberUUID,
		Role:       req.Role,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "member already in squad")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to add squad member")
		return
	}
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildSquadMemberEvent(
		protocol.EventSquadUpdated, uuidToString(squad.ID), workspaceID, req.MemberType, uuidToString(memberUUID), "member", requestUserID(r), "add",
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record squad event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit squad member")
		return
	}

	writeJSON(w, http.StatusCreated, squadMemberToResponse(sm))
	h.publishEvent(event)
}

// RemoveSquadMember 处理 DELETE /api/workspaces/{workspaceId}/squads/{id}/members——把成员
// 移出小队。不能移除领袖（需先换领袖）；需 canManageSquad。
func (h *Handler) RemoveSquadMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}

	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if !canManageSquad(member, squad) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	var req struct {
		MemberType string `json:"member_type"`
		MemberID   string `json:"member_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	memberUUID, ok := parseUUIDOrBadRequest(w, req.MemberID, "member_id")
	if !ok {
		return
	}

	// 不许移除领袖。
	if req.MemberType == "agent" && uuidToString(squad.LeaderID) == req.MemberID {
		writeError(w, http.StatusBadRequest, "cannot remove the squad leader; change leader first")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start squad transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	rows, err := qtx.RemoveSquadMember(r.Context(), db.RemoveSquadMemberParams{
		SquadID:    squad.ID,
		MemberType: req.MemberType,
		MemberID:   memberUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove squad member")
		return
	}
	if rows == 0 {
		writeError(w, http.StatusNotFound, "squad member not found")
		return
	}
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildSquadMemberEvent(protocol.EventSquadUpdated, uuidToString(squad.ID), workspaceID, req.MemberType, uuidToString(memberUUID), "member", requestUserID(r), "remove"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record squad event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit squad member")
		return
	}
	h.publishEvent(event)
	w.WriteHeader(http.StatusNoContent)
}

// UpdateSquadMemberRole 处理 PATCH /api/workspaces/{workspaceId}/squads/{id}/members/role——
// 改成员角色。需 canManageSquad；成员不存在回 404。
func (h *Handler) UpdateSquadMemberRole(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}

	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if !canManageSquad(member, squad) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	var req struct {
		MemberType string `json:"member_type"`
		MemberID   string `json:"member_id"`
		Role       string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	memberUUID, ok := parseUUIDOrBadRequest(w, req.MemberID, "member_id")
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start squad transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	sm, err := qtx.UpdateSquadMemberRole(r.Context(), db.UpdateSquadMemberRoleParams{
		SquadID:    squad.ID,
		MemberType: req.MemberType,
		MemberID:   memberUUID,
		Role:       req.Role,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "squad member not found")
		return
	}
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildSquadMemberEvent(protocol.EventSquadUpdated, uuidToString(squad.ID), workspaceID, req.MemberType, uuidToString(memberUUID), "member", requestUserID(r), "role"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record squad event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit squad member")
		return
	}
	h.publishEvent(event)
	writeJSON(w, http.StatusOK, squadMemberToResponse(sm))
}

// ── Squad 领袖评估 ────────────────────────────────────────────────────────

// RecordSquadLeaderEvaluation 记录小队领袖在每次触发后的评估结论（action / no_action /
// failed），写入统一 activity_log。领袖 Agent 通过 CLI 每次触发后调用。
//
// 「这轮是否是领袖轮」的判据基于任务来源（X-Task-ID 行上的 is_leader_task + squad_id）——
// 与认领路径注入 squad 简报和强制记录指令用的是同一来源。刻意不查目标 issue 自己的负责人：
// 领袖本来就会在非 squad 归属的 issue 上跑。见 MUL-6622 / GH #7487。
//
// 两道授权闸门按顺序：调用方必须拥有该任务，且必须仍是该小队领袖。第一道让「在错误里引用
// 任务所属 issue id」变得安全；第二道防止一次已被降级的认领运行（见闸门 2 的注释）去写领袖
// 结论。
func (h *Handler) RecordSquadLeaderEvaluation(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	var req struct {
		Outcome string `json:"outcome"` // action | no_action | failed
		Reason  string `json:"reason"`  // short explanation from leader
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Outcome != "action" && req.Outcome != "no_action" && req.Outcome != "failed" {
		writeError(w, http.StatusBadRequest, "outcome must be 'action', 'no_action', or 'failed'")
		return
	}

	// 「这轮是不是领袖轮」的权威在任务行，不在目标 issue 的负责人（MUL-6622 / GH #7487）。
	// is_leader_task + squad_id 在入队时打上，正是认领路径把 squad 简报和强制 `squad activity`
	// 指令挂上去的同一依据（handler/daemon.go、daemon/prompt.go taskIsSquadLeader）。若把本端点
	// 挂在 issue.assignee_type == "squad" 上，反而会让领袖合法跑在非 squad 归属 issue 的路径
	// ——某条普通 Agent 的 issue 上被 @squad、或绑到子 issue 的领袖任务——上永远无法记录；又
	// 因 no_action 指令禁止用评论代替，那个决定就完全不留痕了。
	//
	// 检查顺序是承重的：拥有权闸门以下的每次拒绝都可以引用任务派生的 id；之上的则一概不能。
	// `GetAgentTask` 是按 id 的全局查找，所以租户隔离（GetAgentTaskInWorkspace 会连所在工作区）
	// 与「调用方拥有该任务」闸门都必须先跑——否则一个无关的任务 id 经调用方合法可读的 issue
	// 试探，会把别的工作区的 issue id 回显出来。
	taskID := r.Header.Get("X-Task-ID")
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task id")
	if !ok {
		return
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
		ID:          taskUUID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || !task.IssueID.Valid {
		writeError(w, http.StatusBadRequest, "task does not belong to issue")
		return
	}

	// 安全闸门 1：调用方必须是该任务入队时指定的那个 Agent。
	workspaceID := uuidToString(issue.WorkspaceID)
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if actorType != "agent" || !task.AgentID.Valid || actorID != uuidToString(task.AgentID) {
		writeError(w, http.StatusForbidden, "only the squad leader agent can record evaluations")
		return
	}

	// 过了拥有权闸门，指名任务自己的 issue 就是安全且有用的：被 stage barrier / 子完成回调
	// 唤醒的领袖跑在 PARENT issue 上，所以记录到它刚读的那个子 issue 上是最常见的错误。
	if uuidToString(task.IssueID) != uuidToString(issue.ID) {
		writeError(w, http.StatusBadRequest,
			"task does not belong to issue; record the evaluation on issue "+uuidToString(task.IssueID)+" (the issue this task is running on)")
		return
	}
	// 相对旧行为的收窄：领袖 Agent 在 squad 归属 issue 上跑一条非领袖任务（例如同队成员任务）
	// 以前会被这里接受，现在拒绝了——他不是以领袖身份在跑，runtime 只在 taskIsSquadLeader
	// 为真时才强制此调用。
	if !task.IsLeaderTask {
		writeError(w, http.StatusBadRequest, "task is not a squad leader task")
		return
	}
	if !task.SquadID.Valid {
		// MUL-3730 之前的行可能是领袖任务却没盖 squad 戳。记日志而不是对运营方静默失败。
		slog.Warn("squad leader evaluation: leader task has no squad_id",
			append(logger.RequestAttrs(r),
				"task_id", uuidToString(task.ID),
				"issue_id", uuidToString(issue.ID),
			)...)
		writeError(w, http.StatusBadRequest, "leader task has no squad_id")
		return
	}

	squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
		ID:          task.SquadID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "squad not found")
		return
	}

	// 安全闸门 2：调用方必须仍是该小队领袖。
	//
	// 行上的 `is_leader_task` 记录的是入队时的意图，不总是认领实际交付的角色：领袖在入队与
	// 认领之间被换掉时，认领路径会清掉 resp.IsLeaderTask、让这次运行按普通 Agent 轮进行，
	// 而行上仍留着 is_leader_task = true（handler/daemon.go "claim delivered as a non-leader
	// task"）。只信行的话，这种被降级的运行就能写领袖结论——而且在 no_action 时压制自己的
	// 评论。在交付角色落库之前，实时的领袖检查是区分二者的唯一手段，所以要保留。
	//
	// 保守选择的代价：一轮运行中途被换掉的领袖会被拒绝。那不再意味着沉默——注入的规则现在
	// 会告诉记录失败且没发评论的领袖留一句短评。
	if actorID != uuidToString(squad.LeaderID) {
		writeError(w, http.StatusForbidden, "only the squad leader agent can record evaluations")
		return
	}

	details, _ := json.Marshal(map[string]string{
		"squad_id": uuidToString(squad.ID),
		"task_id":  util.UUIDToString(taskUUID),
		"outcome":  req.Outcome,
		"reason":   req.Reason,
	})

	activity, err := h.Queries.CreateActivity(r.Context(), db.CreateActivityParams{
		ID:          dbid.NewV7(),
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
		ActorType:   pgtype.Text{String: "agent", Valid: true},
		// 用 task.AgentID 而非 squad.LeaderID：no_action 评论压制查找按 actor_id 对
		// task.agent_id 匹配（service.HasSquadLeaderNoActionEvaluationForTask），所以这列必须带
		// 任务的 Agent，压制逻辑才找得到这行。
		ActorID: task.AgentID,
		Action:  "squad_leader_evaluated",
		Details: details,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record evaluation")
		return
	}

	h.publish(protocol.EventActivityCreated, uuidToString(issue.WorkspaceID), "agent", actorID, map[string]any{
		"issue_id": uuidToString(issue.ID),
		"entry": map[string]any{
			"type":       "activity",
			"id":         uuidToString(activity.ID),
			"actor_type": "agent",
			"actor_id":   actorID,
			"action":     activity.Action,
			"details":    json.RawMessage(details),
			"created_at": timestampToString(activity.CreatedAt),
		},
	})

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":         uuidToString(activity.ID),
		"action":     activity.Action,
		"created_at": timestampToString(activity.CreatedAt),
	})
}

// ── Squad 触发逻辑 ─────────────────────────────────────────────────────────

// shouldSuppressSquadLeaderSelfTrigger 判断领袖自己发的评论是否应被阻止去重新入队这位领袖。
// 唯一允许唤醒已指派领袖的「领袖自写非领袖任务」是同队成员任务；普通 Agent 任务（直接
// mention、线程父回复等）不能证明 worker 身份，不得自我触发。
func (h *Handler) shouldSuppressSquadLeaderSelfTrigger(ctx context.Context, issueID, leaderID, squadID pgtype.UUID) bool {
	latest, err := h.Queries.GetLatestTaskRoleForIssueAndAgent(ctx, db.GetLatestTaskRoleForIssueAndAgentParams{
		IssueID: issueID,
		AgentID: leaderID,
	})
	if err != nil {
		return false
	}
	if latest.IsLeaderTask {
		return true
	}
	return !latest.SquadID.Valid || uuidToString(latest.SquadID) != uuidToString(squadID)
}

// commentMentionsAnyone 判断评论正文里是否至少有一个路由型 mention——
// [@Name](mention://agent|member|squad|all/<id>)。issue 交叉引用（mention://issue/...）不算，
// 它不指向参与者。只检查当前这条评论——父（线程根）mention 不在这里继承。
func commentMentionsAnyone(content string) bool {
	for _, m := range util.ParseMentions(content) {
		switch m.Type {
		case "agent", "member", "squad", "all":
			return true
		}
	}
	return false
}

// squad 领袖的指派/升格「是否就绪」判定现在统一住在 service.IssueService.WillEnqueueRun
// 谓词里（MUL-3375），issue 写路径与预览端点共享。原先 handler 本地的镜像
// （shouldEnqueueSquadLeaderOnAssign / isSquadLeaderReady）已删除，避免四个入口各自漂移。
// squad 入队副作用仍走下面的 enqueueSquadLeaderTask，把领袖访问闸门与 pending 去重收敛在
// 一处。

// enqueueSquadLeaderTask 为指派给小队所属的 issue 触发领袖 Agent。指派与积压升格路径直接用它；
// 评论路径走 computeCommentAgentTriggers，让预览与创建共享同一份触发集合。
// 返回 true 表示确实入队了一条领袖任务（调用方可以只在真正跑起来时记一条 handoff 轨迹）。
func (h *Handler) enqueueSquadLeaderTask(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, authorType, authorID, handoffNote string) bool {
	squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return false
	}

	// 闸门必须判断与入队路径将要落库到领袖任务行上的同一个「链顶人类」，否则会漂移：一条
	// 正确继承发起人的 Agent 创建 issue（MUL-4305），若闸门用空发起人去判仍会被拒。成员作者
	// 自己是发起人；Agent/系统触发的指派则按 issue 的 origin 链接解析发起人（与
	// EnqueueTaskForSquadLeader* 完全一致）。triggerCommentID 在指派/升格路径恒为空，所以传
	// 非法 UUID 对齐。发起人解析不出来则 leaderOriginator 为空——对 member/team 目标正确
	// fail-closed，对 workspace 目标仍放行 Agent 主体。
	leaderOriginator := ""
	if authorType == "member" {
		leaderOriginator = authorID
	} else {
		leaderOriginator = uuidToString(h.TaskService.OriginatorForIssueTask(ctx, issue, pgtype.UUID{}))
	}
	if !h.canEnqueueSquadLeader(ctx, squad.LeaderID, authorType, authorID, leaderOriginator, uuidToString(issue.WorkspaceID)) {
		return false
	}

	hasPending, err := h.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: issue.ID,
		AgentID: squad.LeaderID,
		// 按评审过的 head 去重（TEN-356）。
		HeadSha: h.TaskService.ResolveIssueReviewSHAParam(ctx, issue.ID),
	})
	if err != nil || hasPending {
		return false
	}

	// triggerCommentID 在指派/升格路径恒为空；handoff 备注走它自己的任务列，绝不进
	// trigger_comment_id。
	_ = triggerCommentID
	// 执行指派/升格的成员是这次领袖运行的负责人类（MUL-4302 §4）——也是上面闸门评判的同一
	// 主体。Agent 作者不是人类，所以只有 member actor 会穿过去。
	if _, err := h.TaskService.EnqueueTaskForSquadLeaderWithHandoff(ctx, issue, squad.LeaderID, squad.ID, handoffNote, memberActorUserID(authorType, authorID)); err != nil {
		slog.Warn("enqueue squad leader task failed",
			"issue_id", uuidToString(issue.ID),
			"squad_id", uuidToString(squad.ID),
			"leader_id", uuidToString(squad.LeaderID),
			"error", err)
		return false
	}
	return true
}
