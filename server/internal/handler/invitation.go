// 职责定位：workspace 邀请（invitation）的 HTTP 处理器：发起邀请、按 workspace 列待处理
// 邀请（管理员视角）、撤销邀请、被邀请人侧查自己的邀请/接受/拒绝。CreateInvitation 取代了
// 老的「即时拉人」CreateMember 流程，同一端点 POST /api/workspaces/{id}/members 现在的行为
// 是发邀请而非直接加成员。
//
// 状态机：pending（待处理）→ accepted / declined / revoked / expired。库里有一条「只按
// status='pending' 过滤」的部分唯一索引防同一邮箱重复邀请（见 idx_invitation_unique_pending），
// 所以过期的 pending 必须先主动翻成 expired，否则会挡住新邀请。接受邀请与建成员在一个事务
// 里原子完成，并顺带把被邀请人标记为已完成 onboarding。
//
// 容量（capacity）联动：创建/撤销/拒绝邀请会预占/释放席位，所有相关操作都要做容量意图
// 补偿，这里只在 capacityEnabled 时启用。
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// InvitationResponse 是邀请的接口响应形态。末尾三个「富化字段」只在列表类接口里填充
// （连带查询出邀请人姓名/邮箱、workspace 名）。
type InvitationResponse struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	InviterID     string  `json:"inviter_id"`
	InviteeEmail  string  `json:"invitee_email"`
	InviteeUserID *string `json:"invitee_user_id"`
	Role          string  `json:"role"`
	Status        string  `json:"status"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
	ExpiresAt     string  `json:"expires_at"`
	// 富化字段（仅列表响应携带）。
	InviterName   string `json:"inviter_name,omitempty"`
	InviterEmail  string `json:"inviter_email,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
}

// invitationToResponse 把数据库行转成接口响应（不填充富化字段）。
func invitationToResponse(inv db.WorkspaceInvitation) InvitationResponse {
	return InvitationResponse{
		ID:            uuidToString(inv.ID),
		WorkspaceID:   uuidToString(inv.WorkspaceID),
		InviterID:     uuidToString(inv.InviterID),
		InviteeEmail:  inv.InviteeEmail,
		InviteeUserID: uuidToPtr(inv.InviteeUserID),
		Role:          inv.Role,
		Status:        inv.Status,
		CreatedAt:     timestampToString(inv.CreatedAt),
		UpdatedAt:     timestampToString(inv.UpdatedAt),
		ExpiresAt:     timestampToString(inv.ExpiresAt),
	}
}

// ---------------------------------------------------------------------------
// CreateInvitation 取代了老的「即时拉人」CreateMember 流程。
// POST /api/workspaces/{id}/members  （同一端点，新行为）
// ---------------------------------------------------------------------------

// CreateInvitation 发起邀请（有效期 7 天）。发邀请的人必须是 workspace 成员；不能邀成 owner。
// 已在本空间、已有在途 pending 邀请、seat 容量已满都会被拒。整体编排是：先过期掉同邮箱的
// 陈旧 pending（部分唯一索引只管 pending 状态，不主动过期会挡住新邀请）→ 检查重复 → 先占
// 席位容量（Cloud 全满时只有消耗发起人自己的滥用预算，且不可恢复性拒绝才计）→ 事务里建行、
// 清意图、落事件 → 提交后广播给被邀请人（若已注册）。
func (h *Handler) CreateInvitation(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	requester, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	var req CreateMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}

	role, valid := normalizeMemberRole(req.Role)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid member role")
		return
	}
	if role == "owner" {
		writeError(w, http.StatusBadRequest, "cannot invite as owner")
		return
	}

	// 先查「已注册用户」：若已是本空间成员则直接 409（不发邀请）。
	existingUser, err := h.Queries.GetUserByEmail(r.Context(), email)
	if err == nil {
		_, memberErr := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
			UserID:      existingUser.ID,
			WorkspaceID: requester.WorkspaceID,
		})
		if memberErr == nil {
			writeError(w, http.StatusConflict, "user is already a member")
			return
		}
	}

	// 先把同邮箱已过期的 pending 邀请翻成 'expired'。部分唯一索引
	// idx_invitation_unique_pending 只按 status='pending' 过滤，过期残留行会把下面的
	// CreateInvitation 卡死——见 issue #2055。
	_, err = h.Queries.ExpireStalePendingInvitations(r.Context(), db.ExpireStalePendingInvitationsParams{
		WorkspaceID: requester.WorkspaceID, InviteeEmail: email,
	})
	if err != nil {
		slog.Warn("expire stale invitations failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID, "email", email)...)
		writeError(w, http.StatusInternalServerError, "failed to create invitation")
		return
	}

	// 再确认现在没有在途的 live pending 邀请（刚清掉的过期行不算）。
	_, err = h.Queries.GetPendingInvitationByEmail(r.Context(), db.GetPendingInvitationByEmailParams{
		WorkspaceID:  requester.WorkspaceID,
		InviteeEmail: email,
	})
	if err == nil {
		writeError(w, http.StatusConflict, "invitation already pending for this email")
		return
	}

	// 被邀请人若已注册，把 invitee_user_id 一并记上（没注册则为空）。
	var inviteeUserID pgtype.UUID
	if existingUser.ID.Valid {
		inviteeUserID = existingUser.ID
	}
	admission, ok := h.checkInvitationAdmission(
		w,
		r,
		uuidToString(requester.UserID),
		uuidToString(requester.WorkspaceID),
		email,
	)
	if !ok {
		return
	}

	invitationID := uuid.New()
	expiresAt := time.Now().Add(7 * 24 * time.Hour)

	// 非消耗性的滥用检查先做；只有 Cloud 把容量锁定了才花各预算。持久性容量拒绝只在上面的
	// 分支里消耗发起人预算；瞬时失败不消耗任何预算。
	h.consumeInvitationAdmission(r, admission)

	createParams := db.CreateInvitationParams{
		ID: parseUUID(invitationID.String()), WorkspaceID: requester.WorkspaceID, InviterID: requester.UserID,
		InviteeEmail: email, InviteeUserID: inviteeUserID, Role: role,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}
	var inv db.WorkspaceInvitation
	var createdEvent events.Event
	tx, txErr := h.TxStarter.Begin(r.Context())
	if txErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to create invitation")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	// 事务里一次做完：建邀请行 → 清掉这次创建对应的容量意图（同一事务内，防止行建好了意图
	// 却残留）→ 记 EventInvitationCreated 事件 → 提交。
	inv, err = qtx.CreateInvitation(r.Context(), createParams)
	if err == nil {
		createdEvent, err = service.RecordDurableEventTx(r.Context(), qtx, buildInvitationDomainEvent(protocol.EventInvitationCreated, inv, "member", uuidToString(requester.UserID), "created", nil))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		// 先释放事务锁，再去事务外做 Cloud 容量补偿（补偿要读并翻转持久化意图）。
		_ = tx.Rollback(r.Context())
	}
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "invitation already pending for this email")
			return
		}
		slog.Warn("create invitation failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID, "email", email)...)
		writeError(w, http.StatusInternalServerError, "failed to create invitation")
		return
	}

	slog.Info("invitation created", append(logger.RequestAttrs(r), "invitation_id", uuidToString(inv.ID), "workspace_id", workspaceID, "email", email, "role", role)...)

	resp := invitationToResponse(inv)

	// 被邀请人若是已注册用户，实时通知 ta 收到了新邀请。
	var workspaceName string
	if ws, err := h.Queries.GetWorkspace(r.Context(), requester.WorkspaceID); err == nil {
		workspaceName = ws.Name
	}
	// 邀请事件是同事务落库的。workspace_name 这个可选标签只补在「进程内实时发布」这份
	// payload 上；持久化重放路径是按 id 重新读标准邀请记录，不依赖它。
	if payload, ok := createdEvent.Payload.(map[string]any); ok && workspaceName != "" {
		payload["workspace_name"] = workspaceName
	}
	h.publishEvent(createdEvent)

	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.TeamInviteSent(
		uuidToString(requester.UserID),
		uuidToString(requester.WorkspaceID),
		email,
		"email",
	))

	// 发邀请邮件（fire-and-forget：异步发，失败只记日志，不阻塞主流程）。
	if h.EmailService != nil && workspaceName != "" {
		inviterName := email // fallback
		if inviter, err := h.Queries.GetUser(r.Context(), requester.UserID); err == nil {
			inviterName = inviter.Name
		}
		invID := uuidToString(inv.ID)
		go func() {
			if err := h.EmailService.SendInvitationEmail(email, inviterName, workspaceName, invID); err != nil {
				slog.Warn("failed to send invitation email", "email", email, "error", err)
			}
		}()
	}

	writeJSON(w, http.StatusCreated, resp)
}

// ---------------------------------------------------------------------------
// ListWorkspaceInvitations —— 某 workspace 的待处理邀请清单（管理员视图）。
// GET /api/workspaces/{id}/invitations
// ---------------------------------------------------------------------------

// ListWorkspaceInvitations 列出某 workspace 下所有仍 pending 的邀请，附带邀请人的名字与
// 邮箱（便于管理界面展示）。路由层已保证只有 workspace 管理员能访问。
func (h *Handler) ListWorkspaceInvitations(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	rows, err := h.Queries.ListPendingInvitationsByWorkspace(r.Context(), workspaceUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list invitations")
		return
	}

	resp := make([]InvitationResponse, len(rows))
	for i, row := range rows {
		resp[i] = InvitationResponse{
			ID:            uuidToString(row.ID),
			WorkspaceID:   uuidToString(row.WorkspaceID),
			InviterID:     uuidToString(row.InviterID),
			InviteeEmail:  row.InviteeEmail,
			InviteeUserID: uuidToPtr(row.InviteeUserID),
			Role:          row.Role,
			Status:        row.Status,
			CreatedAt:     timestampToString(row.CreatedAt),
			UpdatedAt:     timestampToString(row.UpdatedAt),
			ExpiresAt:     timestampToString(row.ExpiresAt),
			InviterName:   row.InviterName,
			InviterEmail:  row.InviterEmail,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// RevokeInvitation —— 管理员撤销一条 pending 邀请。
// DELETE /api/workspaces/{id}/invitations/{invitationId}
// ---------------------------------------------------------------------------

// RevokeInvitation 撤销邀请：先确认该邀请属于当前 workspace 且仍是 pending，然后事务里
// 翻状态、释放预留的席位容量、记 EventInvitationRevoked 事件并发布。座位容量开启时还要
// 做容量意图补偿。
func (h *Handler) RevokeInvitation(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	invitationID := chi.URLParam(r, "invitationId")
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	invitationUUID, ok := parseUUIDOrBadRequest(w, invitationID, "invitation id")
	if !ok {
		return
	}

	inv, err := h.Queries.GetInvitation(r.Context(), invitationUUID)
	if err != nil || uuidToString(inv.WorkspaceID) != uuidToString(workspaceUUID) || inv.Status != "pending" {
		writeError(w, http.StatusNotFound, "invitation not found")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke invitation")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	var revokedEvent events.Event
	rows, err := qtx.RevokeInvitation(r.Context(), inv.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke invitation")
		return
	}
	if rows != 1 {
		writeError(w, http.StatusNotFound, "invitation not found")
		return
	}
	revokedEvent, err = service.RecordDurableEventTx(r.Context(), qtx, buildInvitationDomainEvent(
		protocol.EventInvitationRevoked, inv, "member", requestUserID(r), "revoked", nil,
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record invitation event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke invitation")
		return
	}

	slog.Info("invitation revoked", "invitation_id", invitationID, "workspace_id", workspaceID)

	h.publishEvent(revokedEvent)

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// GetMyInvitation —— 按 id 取单条邀请（给「接受邀请」落地页用）。
// GET /api/invitations/{id}
// ---------------------------------------------------------------------------

// GetMyInvitation 取单条邀请并富化 workspace 名与邀请人信息。会校验邀请确实属于当前用户
// （邮箱或用户 id 匹配），否则 403。
func (h *Handler) GetMyInvitation(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	invitationID := chi.URLParam(r, "id")
	invitationUUID, ok := parseUUIDOrBadRequest(w, invitationID, "invitation id")
	if !ok {
		return
	}
	inv, err := h.Queries.GetInvitation(r.Context(), invitationUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "invitation not found")
		return
	}

	// 校验邀请确实属于当前用户：邀请邮箱或已记录的 invitee_user_id 命中才算。
	user, err := h.Queries.GetUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load user")
		return
	}
	if strings.ToLower(user.Email) != inv.InviteeEmail && uuidToString(inv.InviteeUserID) != userID {
		writeError(w, http.StatusForbidden, "invitation does not belong to you")
		return
	}

	resp := invitationToResponse(inv)

	// 富化字段：workspace 名与邀请人姓名/邮箱（多次小查询，失败则静默跳过，不算接口失败）。
	if ws, err := h.Queries.GetWorkspace(r.Context(), inv.WorkspaceID); err == nil {
		resp.WorkspaceName = ws.Name
	}
	if inviter, err := h.Queries.GetUser(r.Context(), inv.InviterID); err == nil {
		resp.InviterName = inviter.Name
		resp.InviterEmail = inviter.Email
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// ListMyInvitations —— 当前用户在所有 workspace 下的待处理邀请。
// GET /api/invitations
// ---------------------------------------------------------------------------

// ListMyInvitations 返回当前用户的全部 pending 邀请（跨 workspace），同时按邮箱与用户 id
// 两个口径匹配（注册前后收到的邀请都能查出来），并带上 workspace 名与邀请人信息。
func (h *Handler) ListMyInvitations(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	user, err := h.Queries.GetUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load user")
		return
	}

	rows, err := h.Queries.ListPendingInvitationsForUser(r.Context(), db.ListPendingInvitationsForUserParams{
		InviteeUserID: user.ID,
		InviteeEmail:  user.Email,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list invitations")
		return
	}

	resp := make([]InvitationResponse, len(rows))
	for i, row := range rows {
		resp[i] = InvitationResponse{
			ID:            uuidToString(row.ID),
			WorkspaceID:   uuidToString(row.WorkspaceID),
			InviterID:     uuidToString(row.InviterID),
			InviteeEmail:  row.InviteeEmail,
			InviteeUserID: uuidToPtr(row.InviteeUserID),
			Role:          row.Role,
			Status:        row.Status,
			CreatedAt:     timestampToString(row.CreatedAt),
			UpdatedAt:     timestampToString(row.UpdatedAt),
			ExpiresAt:     timestampToString(row.ExpiresAt),
			WorkspaceName: row.WorkspaceName,
			InviterName:   row.InviterName,
			InviterEmail:  row.InviterEmail,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// AcceptInvitation —— 用户接受一条 pending 邀请。
// POST /api/invitations/{id}/accept
// ---------------------------------------------------------------------------

// AcceptInvitation 接受邀请并入伙：校验归属/状态/未过期后，用事务原子完成「邀请翻 accepted
// + 建成员行 + 把用户标记为已完成 onboarding」，同时把预占的席位容量从「意图」转成「已确认」。
// 接受成功后给该用户第一个 workspace 补发 onboarding 完成埋点，并通知 daemon 刷新
// workspace 列表。若用户其实已是成员则 409。
func (h *Handler) AcceptInvitation(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	invitationID := chi.URLParam(r, "id")
	invitationUUID, ok := parseUUIDOrBadRequest(w, invitationID, "invitation id")
	if !ok {
		return
	}
	inv, err := h.Queries.GetInvitation(r.Context(), invitationUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "invitation not found")
		return
	}

	// 校验邀请属于当前用户（邮箱或用户 id 匹配）。
	user, err := h.Queries.GetUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load user")
		return
	}
	if strings.ToLower(user.Email) != inv.InviteeEmail && uuidToString(inv.InviteeUserID) != userID {
		writeError(w, http.StatusForbidden, "invitation does not belong to you")
		return
	}

	if inv.Status != "pending" {
		writeError(w, http.StatusBadRequest, "invitation is not pending")
		return
	}

	// 过期检查：过期邀请视为已失效（410），不再接受。
	if inv.ExpiresAt.Valid && inv.ExpiresAt.Time.Before(time.Now()) {
		writeError(w, http.StatusGone, "invitation has expired")
		return
	}
	// 先把预占的席位容量意图置为「消费中」，防止接受期间别的操作抢走座位。

	// 用同一个事务：标记邀请 accepted + 创建成员，二者必须原子。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to accept invitation")
		return
	}
	defer tx.Rollback(r.Context())

	qtx := h.Queries.WithTx(tx)
	acceptedEvents := make([]events.Event, 0, 2)

	accepted, err := qtx.AcceptInvitation(r.Context(), inv.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to accept invitation")
		return
	}

	member, err := qtx.CreateMember(r.Context(), db.CreateMemberParams{
		WorkspaceID: accepted.WorkspaceID,
		UserID:      user.ID,
		Role:        accepted.Role,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "you are already a member of this workspace")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create membership")
		return
	}

	// 接受邀请即视为被邀请人已完成 onboarding。web/桌面端 workspace 布局对 onboarded_at 有
	// 硬闸门：不打这个标记，第一次进别人建好的 workspace 的受邀者会被弹回 /onboarding 去填
	// 一份早有人配过的问卷。与 CreateMember 同事务，保证 member 与 onboarded_at 永不一致。
	// MarkUserOnboarded 里的 COALESCE 保证「用户第二次加入别的 workspace」时调用幂等。
	firstOnboardingCompletion := !user.OnboardedAt.Valid
	onboardedUser, err := qtx.MarkUserOnboarded(r.Context(), user.ID)
	if err != nil {
		slog.Warn("accept invitation: mark user onboarded failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", uuidToString(accepted.WorkspaceID))...)
		writeError(w, http.StatusInternalServerError, "failed to mark user onboarded")
		return
	}
	memberResp := h.memberWithUserResponse(member, user)
	memberEvent, eventErr := service.RecordDurableEventTx(r.Context(), qtx, events.Event{
		Type:           protocol.EventMemberAdded,
		IdempotencyKey: "member:" + protocol.EventMemberAdded + ":" + uuidToString(member.ID),
		StreamKey:      "workspace:" + uuidToString(accepted.WorkspaceID),
		WorkspaceID:    uuidToString(accepted.WorkspaceID), ActorType: "member", ActorID: userID,
		Payload: map[string]any{"member": memberResp, "action": "invitation_accepted"},
	})
	if eventErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to record membership event")
		return
	}
	acceptedEvents = append(acceptedEvents, memberEvent)
	acceptedEvent, eventErr := service.RecordDurableEventTx(r.Context(), qtx, buildInvitationDomainEvent(
		protocol.EventInvitationAccepted, accepted, "member", userID, "accepted", map[string]any{"member": memberResp},
	))
	if eventErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to record invitation event")
		return
	}
	acceptedEvents = append(acceptedEvents, acceptedEvent)

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to accept invitation")
		return
	}

	slog.Info("invitation accepted", "invitation_id", invitationID, "user_id", userID, "workspace_id", uuidToString(accepted.WorkspaceID))

	wsID := uuidToString(accepted.WorkspaceID)

	for _, event := range acceptedEvents {
		h.publishEvent(event)
	}

	h.notifyDaemonWorkspacesChanged(userID)

	// days_since_invite 向下取整为整天，让漏斗可以干净地把「当天接受」和「之后才接受」分成
	// 两段。inv.CreatedAt 是邀请行插入时间，所以在这里算是安全的。
	var daysSinceInvite int64
	if inv.CreatedAt.Valid {
		daysSinceInvite = int64(time.Since(inv.CreatedAt.Time).Hours() / 24)
	}
	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.TeamInviteAccepted(
		userID,
		wsID,
		daysSinceInvite,
	))
	if firstOnboardingCompletion {
		onboardedAt := ""
		if onboardedUser.OnboardedAt.Valid {
			onboardedAt = onboardedUser.OnboardedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.OnboardingCompleted(
			userID,
			wsID,
			analytics.OnboardingPathInviteAccept,
			onboardedAt,
			onboardedUser.CloudWaitlistEmail.Valid,
		))
	}

	writeJSON(w, http.StatusOK, memberResp)
}

// ---------------------------------------------------------------------------
// DeclineInvitation —— 用户拒绝一条 pending 邀请。
// POST /api/invitations/{id}/decline
// ---------------------------------------------------------------------------

// DeclineInvitation 拒绝邀请：校验归属与 pending 状态后，事务里翻状态、释放预留席位
// （enqueueCapacityRelease）、记 EventInvitationDeclined 事件；提交后再做容量意图补偿并
// 广播事件。
func (h *Handler) DeclineInvitation(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	invitationID := chi.URLParam(r, "id")
	invitationUUID, ok := parseUUIDOrBadRequest(w, invitationID, "invitation id")
	if !ok {
		return
	}
	inv, err := h.Queries.GetInvitation(r.Context(), invitationUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "invitation not found")
		return
	}

	// 校验邀请属于当前用户（邮箱或用户 id 匹配）。
	user, err := h.Queries.GetUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load user")
		return
	}
	if strings.ToLower(user.Email) != inv.InviteeEmail && uuidToString(inv.InviteeUserID) != userID {
		writeError(w, http.StatusForbidden, "invitation does not belong to you")
		return
	}

	if inv.Status != "pending" {
		writeError(w, http.StatusBadRequest, "invitation is not pending")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decline invitation")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	declined, err := qtx.DeclineInvitation(r.Context(), inv.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decline invitation")
		return
	}
	declinedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildInvitationDomainEvent(
		protocol.EventInvitationDeclined, declined, "member", userID, "declined", nil,
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record invitation event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decline invitation")
		return
	}

	slog.Info("invitation declined", "invitation_id", invitationID, "user_id", userID)

	h.publishEvent(declinedEvent)

	w.WriteHeader(http.StatusNoContent)
}
