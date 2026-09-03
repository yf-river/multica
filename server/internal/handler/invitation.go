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
	"github.com/multica-ai/multica/server/internal/seatcapacity"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// InvitationResponse is the JSON shape returned for a workspace invitation.
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
	// Enriched fields (present in list responses).
	InviterName   string `json:"inviter_name,omitempty"`
	InviterEmail  string `json:"inviter_email,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
}

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
// CreateInvitation replaces the old "instant-add" CreateMember flow.
// POST /api/workspaces/{id}/members  (same endpoint, new behaviour)
// ---------------------------------------------------------------------------

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

	// Check if the user is already a member.
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

	// Drop any past-due pending invitations to 'expired' first. The partial unique
	// index idx_invitation_unique_pending only filters by status = 'pending', so a
	// stale row would otherwise block CreateInvitation below — see issue #2055.
	expiredInvitations, err := h.Queries.ExpireStalePendingInvitations(r.Context(), db.ExpireStalePendingInvitationsParams{
		WorkspaceID: requester.WorkspaceID, InviteeEmail: email,
	})
	if err != nil {
		slog.Warn("expire stale invitations failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID, "email", email)...)
		writeError(w, http.StatusInternalServerError, "failed to create invitation")
		return
	}
	if h.seatCapacityEnabled() {
		for _, expired := range expiredInvitations {
			if err := enqueueCapacityRelease(r.Context(), h.Queries, uuid.UUID(expired.WorkspaceID.Bytes), uuid.UUID(expired.ID.Bytes)); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to release expired invitation capacity")
				return
			}
			h.compensateCapacityIntent(r.Context(), uuid.UUID(expired.ID.Bytes))
		}
	}

	// Check if there is still a live pending invitation.
	_, err = h.Queries.GetPendingInvitationByEmail(r.Context(), db.GetPendingInvitationByEmailParams{
		WorkspaceID:  requester.WorkspaceID,
		InviteeEmail: email,
	})
	if err == nil {
		writeError(w, http.StatusConflict, "invitation already pending for this email")
		return
	}

	// Resolve invitee_user_id if the user already exists.
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
	if err := h.reserveInvitationCapacity(r.Context(), uuid.UUID(requester.WorkspaceID.Bytes), invitationID, expiresAt); err != nil {
		if isPersistentSeatCapacityAdmissionRejection(err) {
			// Full and overcommitted workspaces cannot admit another member until
			// their durable capacity facts change. Charge repeated attempts to the
			// actor budget so this endpoint cannot hammer the capacity service,
			// while preserving workspace and recipient budgets for a later valid
			// invitation.
			h.consumeInvitationActorAdmission(r, admission)
		}
		writeSeatCapacityError(w, err)
		return
	}

	// The non-consuming abuse checks run before Cloud. Spend all budgets only
	// after Cloud has secured capacity. Persistent capacity rejections spend the
	// actor budget only in the branch above; transient failures spend none.
	h.consumeInvitationAdmission(r, admission)

	createParams := db.CreateInvitationParams{
		ID: uuidToPG(invitationID), WorkspaceID: requester.WorkspaceID, InviterID: requester.UserID,
		InviteeEmail: email, InviteeUserID: inviteeUserID, Role: role,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}
	var inv db.WorkspaceInvitation
	var createdEvent events.Event
	tx, txErr := h.TxStarter.Begin(r.Context())
	if txErr != nil {
		h.compensateCapacityIntent(r.Context(), invitationID)
		writeError(w, http.StatusInternalServerError, "failed to create invitation")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	inv, err = qtx.CreateInvitation(r.Context(), createParams)
	if err == nil && h.seatCapacityEnabled() {
		err = qtx.DeleteSeatCapacityIntentForAction(r.Context(), db.DeleteSeatCapacityIntentForActionParams{
			OperationToken: uuidToPG(invitationID), Action: seatcapacity.ActionReserveInvitation,
		})
	}
	if err == nil {
		createdEvent, err = service.RecordDurableEventTx(r.Context(), qtx, buildInvitationDomainEvent(protocol.EventInvitationCreated, inv, "member", uuidToString(requester.UserID), "created", nil))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		// Release transaction locks before the out-of-transaction Cloud
		// compensation below reads and transitions the durable intent.
		_ = tx.Rollback(r.Context())
	}
	if err != nil {
		h.compensateCapacityIntent(r.Context(), invitationID)
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

	// Notify the invitee in real time if they are a registered user.
	var workspaceName string
	if ws, err := h.Queries.GetWorkspace(r.Context(), requester.WorkspaceID); err == nil {
		workspaceName = ws.Name
	}
	// The invitation event was committed with the row. Add the optional
	// workspace label only to the in-process response path; replay reads the
	// canonical invitation by id.
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

	// Send invitation email (fire-and-forget).
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
// ListWorkspaceInvitations — pending invitations for a workspace (admin view).
// GET /api/workspaces/{id}/invitations
// ---------------------------------------------------------------------------

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
// RevokeInvitation — admin cancels a pending invitation.
// DELETE /api/workspaces/{id}/invitations/{invitationId}
// ---------------------------------------------------------------------------

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
	if h.seatCapacityEnabled() {
		if err := enqueueCapacityRelease(r.Context(), qtx, uuid.UUID(inv.WorkspaceID.Bytes), uuid.UUID(inv.ID.Bytes)); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to revoke invitation")
			return
		}
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
	if h.seatCapacityEnabled() {
		h.compensateCapacityIntent(r.Context(), uuid.UUID(inv.ID.Bytes))
	}

	slog.Info("invitation revoked", "invitation_id", invitationID, "workspace_id", workspaceID)

	h.publishEvent(revokedEvent)

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// GetMyInvitation — get a single invitation by ID (for the invite accept page).
// GET /api/invitations/{id}
// ---------------------------------------------------------------------------

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

	// Verify the invitation belongs to the current user.
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

	// Enrich with workspace name and inviter name.
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
// ListMyInvitations — current user's pending invitations across all workspaces.
// GET /api/invitations
// ---------------------------------------------------------------------------

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
// AcceptInvitation — user accepts a pending invitation.
// POST /api/invitations/{id}/accept
// ---------------------------------------------------------------------------

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

	// Verify the invitation belongs to the current user.
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

	// Check expiry.
	if inv.ExpiresAt.Valid && inv.ExpiresAt.Time.Before(time.Now()) {
		writeError(w, http.StatusGone, "invitation has expired")
		return
	}
	capacityActive, err := h.beginCapacityConsume(r.Context(), uuid.UUID(inv.WorkspaceID.Bytes), uuid.UUID(inv.ID.Bytes), uuid.UUID(inv.ID.Bytes), uuid.UUID(user.ID.Bytes))
	if err != nil {
		writeSeatCapacityError(w, err)
		return
	}

	// Use a transaction: mark accepted + create member atomically.
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

	// Accepting an invite marks the invitee as onboarded. The web /
	// desktop workspace layout has a hard onboarded_at gate; without
	// this mark, an invitee landing on their first workspace would be
	// redirected back to /onboarding to fill out a questionnaire for a
	// workspace someone else already set up. Atomic with CreateMember so
	// `member` and `onboarded_at` can never disagree. COALESCE in
	// MarkUserOnboarded keeps the call idempotent for users joining
	// additional workspaces after their first.
	firstOnboardingCompletion := !user.OnboardedAt.Valid
	onboardedUser, err := qtx.MarkUserOnboarded(r.Context(), user.ID)
	if err != nil {
		slog.Warn("accept invitation: mark user onboarded failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", uuidToString(accepted.WorkspaceID))...)
		writeError(w, http.StatusInternalServerError, "failed to mark user onboarded")
		return
	}
	if capacityActive {
		if err := transitionCapacityIntentToConfirm(r.Context(), qtx, uuid.UUID(inv.ID.Bytes), uuid.UUID(member.ID.Bytes), seatcapacity.ActionConsumeInvitation); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to accept invitation")
			return
		}
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
	if capacityActive {
		h.confirmCapacityIntent(r.Context(), uuid.UUID(accepted.WorkspaceID.Bytes), uuid.UUID(accepted.ID.Bytes), uuid.UUID(member.ID.Bytes))
	}

	slog.Info("invitation accepted", "invitation_id", invitationID, "user_id", userID, "workspace_id", uuidToString(accepted.WorkspaceID))

	wsID := uuidToString(accepted.WorkspaceID)

	for _, event := range acceptedEvents {
		h.publishEvent(event)
	}

	h.notifyDaemonWorkspacesChanged(userID)

	// days_since_invite rounds down to whole days so the funnel segments
	// "accepted same day" cleanly from "accepted later". inv.CreatedAt is
	// the invitation row's insertion time so this is safe to compute here.
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
// DeclineInvitation — user declines a pending invitation.
// POST /api/invitations/{id}/decline
// ---------------------------------------------------------------------------

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

	// Verify the invitation belongs to the current user.
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
	if h.seatCapacityEnabled() {
		if err := enqueueCapacityRelease(r.Context(), qtx, uuid.UUID(inv.WorkspaceID.Bytes), uuid.UUID(inv.ID.Bytes)); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to decline invitation")
			return
		}
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
	if h.seatCapacityEnabled() {
		h.compensateCapacityIntent(r.Context(), uuid.UUID(inv.ID.Bytes))
	}

	slog.Info("invitation declined", "invitation_id", invitationID, "user_id", userID)

	h.publishEvent(declinedEvent)

	w.WriteHeader(http.StatusNoContent)
}
