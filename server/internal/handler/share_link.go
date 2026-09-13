// 本文件实现 workspace「分享链接」（share link）的创建、列表、撤销与通过链接加入。
//
// 分享链接允许管理员生成一条带 code 的邀请链接：访客先经公开预览接口（GetShareLinkInfo，
// 无需登录）看到 workspace 名字与邀请人，登录后用 code 走 JoinByShareLink 加入。
//
// 关键不变量：一个 workspace 同一时刻至多一条激活中的分享链接——创建新链接前先在同一
// 事务里把旧的停用（partial unique index 兜底并发）。加入走「先认领后落库」：认领是
// 一条条件 UPDATE（复检 active/未过期/未超 max_uses 并自增 use_count），天然防止并发
// 超用；席位容量（seat capacity）开启时还须经容量编排。
package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/seatcapacity"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// expiresInMaxHours 是「小时数 × time.Hour」不会溢出有符号 64 位 time.Duration 的
// 上限。拒绝超出它的值，防止 expires_in 回绕成负时长——那会让刚建的链接立刻过期。
const expiresInMaxHours = math.MaxInt64 / int64(time.Hour)

// ShareLinkResponse 是分享链接接口返回的 JSON 结构。Enriched 部分的创建者名字/邮箱
// 仅列表（含 join 后的视图）场景补充。
type ShareLinkResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Code        string  `json:"code"`
	CreatedBy   string  `json:"created_by"`
	Role        string  `json:"role"`
	ExpiresAt   *string `json:"expires_at,omitempty"`
	MaxUses     *int32  `json:"max_uses,omitempty"`
	UseCount    int32   `json:"use_count"`
	IsActive    bool    `json:"is_active"`
	CreatedAt   string  `json:"created_at"`
	// Enriched：创建者展示信息（列表端点补齐）
	CreatorName  string `json:"creator_name,omitempty"`
	CreatorEmail string `json:"creator_email,omitempty"`
}

// CreateShareLinkRequest 是创建分享链接的请求体。Role 支持 member/admin；ExpiresIn
// 是「从现在起多少小时」过期；MaxUses 是最大使用次数。
type CreateShareLinkRequest struct {
	Role      string `json:"role"`
	ExpiresIn *int   `json:"expires_in,omitempty"` // 从现在起的小时数
	MaxUses   *int   `json:"max_uses,omitempty"`
}

// JoinByShareLinkRequest 是加入请求体，携带链接 code。
type JoinByShareLinkRequest struct {
	Code string `json:"code"`
}

// generateShareCode 用 12 字节加密随机数生成链接 code（hex 编码后 24 个字符）。
func generateShareCode() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// CreateShareLink——管理员创建一条可分享的邀请链接。
// POST /api/workspaces/{id}/share-links
func (h *Handler) CreateShareLink(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	requester, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	var req CreateShareLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	role := "member"
	if req.Role != "" {
		r := strings.ToLower(strings.TrimSpace(req.Role))
		if r == "admin" {
			role = "admin"
		} else if r != "member" {
			writeError(w, http.StatusBadRequest, "invalid role")
			return
		}
	}

	// 校验过期与使用上限，而不是把坏输入静默当成「不限」：零/负数与超范围的值直接
	// 拒绝。max_uses 以 int32 存储，expires_in 要乘 time.Hour 得到 time.Duration，
	// 两者都必须在转换前给出显式上界，避免回绕或溢出。
	var expiresAt pgtype.Timestamptz
	if req.ExpiresIn != nil {
		if *req.ExpiresIn <= 0 || int64(*req.ExpiresIn) > expiresInMaxHours {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("expires_in must be between 1 and %d hours", expiresInMaxHours))
			return
		}
		expiresAt = pgtype.Timestamptz{Time: time.Now().Add(time.Duration(*req.ExpiresIn) * time.Hour), Valid: true}
	}

	var maxUses pgtype.Int4
	if req.MaxUses != nil {
		if *req.MaxUses <= 0 || *req.MaxUses > math.MaxInt32 {
			writeError(w, http.StatusBadRequest, "max_uses must be between 1 and 2147483647")
			return
		}
		maxUses = pgtype.Int4{Int32: int32(*req.MaxUses), Valid: true}
	}

	// 「换一条新链接并停用旧链接」必须原子：在事务外停用（以前的做法）会在插入失败
	// 时把仍可用的旧链接一起毁掉；忽略停用错误又会留下多条激活链接。
	// (workspace_id) WHERE is_active 的部分唯一索引在并发创建时也会失败关闭。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create share link")
		return
	}
	defer tx.Rollback(r.Context())

	qtx := h.Queries.WithTx(tx)

	if err := qtx.DeactivateWorkspaceShareLinks(r.Context(), requester.WorkspaceID); err != nil {
		slog.Warn("deactivate share links failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create share link")
		return
	}

	code, err := generateShareCode()
	if err != nil {
		slog.Warn("generate share code failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create share link")
		return
	}

	link, err := qtx.CreateShareLink(r.Context(), db.CreateShareLinkParams{
		WorkspaceID: requester.WorkspaceID,
		Code:        code,
		CreatedBy:   requester.UserID,
		Role:        role,
		ExpiresAt:   expiresAt,
		MaxUses:     maxUses,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a share link is already active for this workspace")
			return
		}
		slog.Warn("create share link failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create share link")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit share link failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create share link")
		return
	}

	slog.Info("share link created", append(logger.RequestAttrs(r), "share_link_id", uuidToString(link.ID), "workspace_id", workspaceID)...)

	resp := shareLinkToResponse(link)
	writeJSON(w, http.StatusCreated, resp)
}

// ListShareLinks——列出 workspace 的分享链接（管理员视图）。
// GET /api/workspaces/{id}/share-links
func (h *Handler) ListShareLinks(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	rows, err := h.Queries.ListShareLinksByWorkspace(r.Context(), workspaceUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list share links")
		return
	}

	resp := make([]ShareLinkResponse, len(rows))
	for i, row := range rows {
		resp[i] = shareLinkListToResponse(row)
	}
	if resp == nil {
		resp = []ShareLinkResponse{}
	}

	writeJSON(w, http.StatusOK, resp)
}

// RevokeShareLink——管理员撤销一条分享链接。
// DELETE /api/workspaces/{id}/share-links/{linkId}
func (h *Handler) RevokeShareLink(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	linkID := chi.URLParam(r, "linkId")
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	linkUUID, ok := parseUUIDOrBadRequest(w, linkID, "link id")
	if !ok {
		return
	}

	if err := h.Queries.RevokeShareLink(r.Context(), db.RevokeShareLinkParams{
		ID:          linkUUID,
		WorkspaceID: workspaceUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke share link")
		return
	}

	slog.Info("share link revoked", "share_link_id", linkID, "workspace_id", workspaceID)
	w.WriteHeader(http.StatusNoContent)
}

// ShareLinkInfoResponse 是分享链接的公开（加入前）预览：只含访客决定是否加入所需的
// 信息——workspace 名、slug、邀请人显示名与链接授予的角色。刻意排除内部 ID
// （workspace_id）、邀请 code 与邀请人邮箱。
type ShareLinkInfoResponse struct {
	WorkspaceName string `json:"workspace_name"`
	WorkspaceSlug string `json:"workspace_slug"`
	CreatorName   string `json:"creator_name,omitempty"`
	Role          string `json:"role"`
}

// GetShareLinkInfo——分享链接的公开预览。无需鉴权，让还没登录的访客在登录前先看到
// 自己要加入的是什么。只暴露 workspace 名字/slug 与邀请人。
// GET /api/share-links/{code}
func (h *Handler) GetShareLinkInfo(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	code = strings.TrimSpace(code)
	if code == "" {
		writeError(w, http.StatusBadRequest, "code is required")
		return
	}

	row, err := h.Queries.GetShareLinkInfoByCode(r.Context(), code)
	if err != nil {
		writeError(w, http.StatusNotFound, "share link not found or expired")
		return
	}

	writeJSON(w, http.StatusOK, ShareLinkInfoResponse{
		WorkspaceName: row.WorkspaceName,
		WorkspaceSlug: row.WorkspaceSlug,
		CreatorName:   row.CreatorName,
		Role:          row.Role,
	})
}

// JoinByShareLink——已登录用户经分享链接加入一个 workspace。
// POST /api/share-links/join
func (h *Handler) JoinByShareLink(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req JoinByShareLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	code := strings.TrimSpace(req.Code)
	if code == "" {
		writeError(w, http.StatusBadRequest, "code is required")
		return
	}

	user, err := h.Queries.GetUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load user")
		return
	}

	linkPreview, err := h.Queries.GetActiveShareLinkByCode(r.Context(), code)
	if err != nil {
		writeError(w, http.StatusNotFound, "share link not found or expired")
		return
	}
	if _, memberErr := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID: user.ID, WorkspaceID: linkPreview.WorkspaceID,
	}); memberErr == nil {
		writeError(w, http.StatusConflict, "you are already a member of this workspace")
		return
	} else if !errors.Is(memberErr, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to join workspace")
		return
	}

	var capacityToken uuid.UUID
	if h.seatCapacityEnabled() {
		capacityToken, err = h.beginShareJoinCapacity(r.Context(), uuid.UUID(linkPreview.WorkspaceID.Bytes), uuid.UUID(linkPreview.ID.Bytes), uuid.UUID(user.ID.Bytes))
		if err != nil {
			writeSeatCapacityError(w, err)
			return
		}
	}

	// 先开事务，再在事务里原子认领链接。认领是一条条件 UPDATE：一条语句里复检
	// active / 未过期 / 未超 max_uses 并自增 use_count，所以并发加入不可能超出
	// max_uses，加入也不可能与撤销/过期赛跑。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to join workspace")
		return
	}
	defer tx.Rollback(r.Context())

	qtx := h.Queries.WithTx(tx)

	link, err := qtx.ClaimShareLinkByID(r.Context(), linkPreview.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "share link not found or expired")
		return
	}

	// 事务内再查一次是否已是成员——是则回滚掉刚才认领增加的 use_count。
	_, memberErr := qtx.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      user.ID,
		WorkspaceID: link.WorkspaceID,
	})
	if memberErr == nil {
		writeError(w, http.StatusConflict, "you are already a member of this workspace")
		return
	}

	member, err := qtx.CreateMember(r.Context(), db.CreateMemberParams{
		WorkspaceID: link.WorkspaceID,
		UserID:      user.ID,
		Role:        link.Role,
	})
	if err != nil {
		if isUniqueViolation(err) {
			h.compensateCapacityIntent(r.Context(), capacityToken)
			writeError(w, http.StatusConflict, "you are already a member of this workspace")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create membership")
		return
	}

	// 标记为已完成 onboarding。这一步失败必须让整个加入回滚，否则首次使用的用户
	// 可能成了成员却仍被 onboarding 卡住。
	if _, err := qtx.MarkUserOnboarded(r.Context(), user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to finalize onboarding")
		return
	}
	if capacityToken != uuid.Nil {
		if err := transitionCapacityIntentToConfirm(r.Context(), qtx, capacityToken, uuid.UUID(member.ID.Bytes), seatcapacity.ActionClaimShareJoin); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to join workspace")
			return
		}
	}
	memberEvent, err := service.RecordDurableEventTx(r.Context(), qtx, events.Event{
		Type:           protocol.EventMemberAdded,
		IdempotencyKey: "member:" + protocol.EventMemberAdded + ":" + uuidToString(member.ID),
		StreamKey:      "workspace:" + uuidToString(link.WorkspaceID),
		WorkspaceID:    uuidToString(link.WorkspaceID), ActorType: "member", ActorID: userID,
		Payload: map[string]any{"member": h.memberWithUserResponse(member, user), "action": "share_link_join"},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record membership event")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to join workspace")
		return
	}
	if capacityToken != uuid.Nil {
		h.confirmCapacityIntent(r.Context(), uuid.UUID(link.WorkspaceID.Bytes), capacityToken, uuid.UUID(member.ID.Bytes))
	}

	wsID := uuidToString(link.WorkspaceID)
	slog.Info("user joined via share link", "user_id", userID, "workspace_id", wsID)

	ws, err := h.Queries.GetWorkspace(r.Context(), link.WorkspaceID)
	wsSlug := ""
	if err == nil {
		wsSlug = ws.Slug
	}

	memberResp := h.memberWithUserResponse(member, user)
	h.publishEvent(memberEvent)
	h.notifyDaemonWorkspacesChanged(userID)

	writeJSON(w, http.StatusOK, map[string]any{
		"member":         memberResp,
		"workspace_id":   wsID,
		"workspace_slug": wsSlug,
	})
}

// shareLinkToResponse 把单条链接行转成对外结构（不含创建者展示信息）。
func shareLinkToResponse(link db.WorkspaceShareLink) ShareLinkResponse {
	var expiresAt *string
	if link.ExpiresAt.Valid {
		s := timestampToString(link.ExpiresAt)
		expiresAt = &s
	}
	var maxUses *int32
	if link.MaxUses.Valid {
		maxUses = &link.MaxUses.Int32
	}
	return ShareLinkResponse{
		ID:          uuidToString(link.ID),
		WorkspaceID: uuidToString(link.WorkspaceID),
		Code:        link.Code,
		CreatedBy:   uuidToString(link.CreatedBy),
		Role:        link.Role,
		ExpiresAt:   expiresAt,
		MaxUses:     maxUses,
		UseCount:    link.UseCount,
		IsActive:    link.IsActive,
		CreatedAt:   timestampToString(link.CreatedAt),
	}
}

// shareLinkListToResponse 把列表行（含创建者名字/邮箱）转成对外结构。
func shareLinkListToResponse(row db.ListShareLinksByWorkspaceRow) ShareLinkResponse {
	var expiresAt *string
	if row.ExpiresAt.Valid {
		s := timestampToString(row.ExpiresAt)
		expiresAt = &s
	}
	var maxUses *int32
	if row.MaxUses.Valid {
		maxUses = &row.MaxUses.Int32
	}
	return ShareLinkResponse{
		ID:           uuidToString(row.ID),
		WorkspaceID:  uuidToString(row.WorkspaceID),
		Code:         row.Code,
		CreatedBy:    uuidToString(row.CreatedBy),
		Role:         row.Role,
		ExpiresAt:    expiresAt,
		MaxUses:      maxUses,
		UseCount:     row.UseCount,
		IsActive:     row.IsActive,
		CreatedAt:    timestampToString(row.CreatedAt),
		CreatorName:  row.CreatorName,
		CreatorEmail: row.CreatorEmail,
	}
}
