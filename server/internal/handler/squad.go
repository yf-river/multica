package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/requestctx"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ── Response types ──────────────────────────────────────────────────────────

type squadResponse struct {
	ID            string           `json:"id"`
	WorkspaceID   string           `json:"workspace_id"`
	Name          string           `json:"name"`
	Description   string           `json:"description"`
	Instructions  string           `json:"instructions"`
	SOPProfile    map[string]any   `json:"sop_profile"`
	AvatarURL     *string          `json:"avatar_url"`
	Scope         string           `json:"scope"`
	LeaderID      string           `json:"leader_id"`
	CreatorID     string           `json:"creator_id"`
	CreatedAt     string           `json:"created_at"`
	UpdatedAt     string           `json:"updated_at"`
	ArchivedAt    *string          `json:"archived_at"`
	ArchivedBy    *string          `json:"archived_by"`
	MemberCount   int              `json:"member_count"`
	MemberPreview []SquadMemberRef `json:"member_preview"`
}

type SquadMemberRef struct {
	MemberType string `json:"member_type"`
	MemberID   string `json:"member_id"`
	Role       string `json:"role"`
}

type squadMemberSummary struct {
	count   int
	preview []SquadMemberRef
}

type SquadMemberResponse struct {
	ID         string `json:"id"`
	SquadID    string `json:"squad_id"`
	MemberType string `json:"member_type"`
	MemberID   string `json:"member_id"`
	Role       string `json:"role"`
	CreatedAt  string `json:"created_at"`
}

type createSquadRequest struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	LeaderID    string           `json:"leader_id"`
	AvatarURL   *string          `json:"avatar_url"`
	Scope       string           `json:"scope"`
	SOPProfile  json.RawMessage  `json:"sop_profile"`
	Members     []SquadMemberRef `json:"members"`
}

type preparedSquadMember struct {
	memberType string
	memberID   pgtype.UUID
	role       string
}

// ── Converters ──────────────────────────────────────────────────────────────

func squadToResponse(s db.Squad) (squadResponse, error) {
	profile, err := decodeSquadSOPProfile(s.SopProfile)
	if err != nil {
		return squadResponse{}, err
	}
	return squadResponse{
		ID:            uuidToString(s.ID),
		WorkspaceID:   uuidToString(s.WorkspaceID),
		Name:          s.Name,
		Description:   s.Description,
		Instructions:  s.Instructions,
		SOPProfile:    profile,
		AvatarURL:     textToPtr(s.AvatarUrl),
		Scope:         s.Scope,
		LeaderID:      uuidToString(s.LeaderID),
		CreatorID:     uuidToString(s.CreatorID),
		CreatedAt:     timestampToString(s.CreatedAt),
		UpdatedAt:     timestampToString(s.UpdatedAt),
		ArchivedAt:    timestampToPtr(s.ArchivedAt),
		ArchivedBy:    uuidToPtr(s.ArchivedBy),
		MemberPreview: []SquadMemberRef{},
	}, nil
}

func decodeSquadSOPProfile(raw []byte) (map[string]any, error) {
	var profile map[string]any
	if err := json.Unmarshal(raw, &profile); err != nil {
		return nil, fmt.Errorf("decode squad SOP profile: %w", err)
	}
	if profile == nil {
		return nil, errors.New("decode squad SOP profile: expected JSON object")
	}
	return profile, nil
}

func normalizeSquadSOPProfile(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	profile, err := decodeSquadSOPProfile(raw)
	if err != nil {
		return nil, errors.New("sop_profile must be a JSON object")
	}
	rawSteps, exists := profile["steps"]
	if !exists {
		return raw, nil
	}
	steps, ok := rawSteps.([]any)
	if !ok {
		return nil, errors.New("sop_profile.steps must be an array")
	}
	for index, rawStep := range steps {
		step, ok := rawStep.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("sop_profile.steps[%d] must be an object", index)
		}
		for field := range step {
			switch field {
			case "key", "name", "role_key", "skill":
			default:
				return nil, fmt.Errorf("sop_profile.steps[%d].%s is not supported", index, field)
			}
		}
		key, ok := step["key"].(string)
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("sop_profile.steps[%d].key is required", index)
		}
	}
	return raw, nil
}

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

func addSquadMemberPreview(summary *squadMemberSummary, memberType string, memberID pgtype.UUID, role string) {
	summary.count++
	if len(summary.preview) >= 3 {
		return
	}
	summary.preview = append(summary.preview, SquadMemberRef{
		MemberType: memberType,
		MemberID:   uuidToString(memberID),
		Role:       role,
	})
}

func applySquadMemberSummary(resp *squadResponse, summary *squadMemberSummary) {
	if summary == nil {
		return
	}
	resp.MemberCount = summary.count
	resp.MemberPreview = summary.preview
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// loadSquadInWorkspace loads a squad scoped to the current workspace.
func (h *Handler) loadSquadInWorkspace(w http.ResponseWriter, r *http.Request) (db.Squad, string, bool) {
	workspaceID := requestctx.WorkspaceID(r.Context())
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
		writeEntityLoadError(w, err, "squad", "squad_id", squadID)
		return db.Squad{}, "", false
	}
	return squad, workspaceID, true
}

func (h *Handler) requireSquadVisible(w http.ResponseWriter, r *http.Request, squad db.Squad, workspaceID string) bool {
	actorType, actorID := resolveActor(r, requestUserID(r))
	return h.requireSquadAccess(w, r, squad, actorType, actorID, workspaceID, http.StatusNotFound, "squad not found")
}

func (h *Handler) requireSquadManager(w http.ResponseWriter, r *http.Request, squad db.Squad) (db.Member, bool) {
	member, ok := requireWorkspaceMemberContext(w, r)
	if !ok {
		return db.Member{}, false
	}
	if !memberCanManageSquad(squad, member) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return db.Member{}, false
	}
	return member, true
}

func (h *Handler) loadManagedSquad(w http.ResponseWriter, r *http.Request) (db.Squad, string, db.Member, bool) {
	squad, workspaceID, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return db.Squad{}, "", db.Member{}, false
	}
	member, ok := h.requireSquadManager(w, r, squad)
	return squad, workspaceID, member, ok
}

func loadSquadMemberSummary(ctx context.Context, queries *db.Queries, squadID pgtype.UUID) (*squadMemberSummary, error) {
	rows, err := queries.ListSquadMemberPreviewRowsBySquad(ctx, squadID)
	if err != nil {
		return nil, err
	}
	summary := &squadMemberSummary{}
	for _, row := range rows {
		addSquadMemberPreview(summary, row.MemberType, row.MemberID, row.Role)
	}
	return summary, nil
}

func squadToResponseWithPreview(ctx context.Context, queries *db.Queries, squad db.Squad) (squadResponse, error) {
	resp, err := squadToResponse(squad)
	if err != nil {
		return squadResponse{}, err
	}
	summary, err := loadSquadMemberSummary(ctx, queries, squad.ID)
	if err != nil {
		return resp, fmt.Errorf("load squad member preview: %w", err)
	}
	applySquadMemberSummary(&resp, summary)
	return resp, nil
}

// ── Handlers ────────────────────────────────────────────────────────────────

func (h *Handler) ListSquads(w http.ResponseWriter, r *http.Request) {
	workspaceID := requestctx.WorkspaceID(r.Context())
	member, ok := requireWorkspaceMemberContext(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	includeArchived := r.URL.Query().Get("include_archived") == "true"
	squads, err := h.Queries.ListSquads(r.Context(), db.ListSquadsParams{
		WorkspaceID:     wsUUID,
		IncludeArchived: includeArchived,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list squads")
		return
	}

	summaries := make(map[string]*squadMemberSummary, len(squads))
	previewRows, err := h.Queries.ListSquadMemberPreviewRows(r.Context(), db.ListSquadMemberPreviewRowsParams{
		WorkspaceID:     wsUUID,
		IncludeArchived: includeArchived,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list squad member preview")
		return
	}
	for _, row := range previewRows {
		squadID := uuidToString(row.SquadID)
		summary := summaries[squadID]
		if summary == nil {
			summary = &squadMemberSummary{}
			summaries[squadID] = summary
		}
		addSquadMemberPreview(summary, row.MemberType, row.MemberID, row.Role)
	}

	resp := make([]squadResponse, 0, len(squads))
	for _, s := range squads {
		if s.Scope == scopePersonal && !memberCanManageSquad(s, member) {
			continue
		}
		item, err := squadToResponse(s)
		if err != nil {
			slog.Error("decode squad SOP profile failed", append(logger.RequestAttrs(r), "squad_id", uuidToString(s.ID), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to decode squad SOP profile")
			return
		}
		applySquadMemberSummary(&item, summaries[uuidToString(s.ID)])
		resp = append(resp, item)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) CreateSquad(w http.ResponseWriter, r *http.Request) {
	workspaceID := requestctx.WorkspaceID(r.Context())
	member, ok := requireWorkspaceMemberContext(w, r)
	if !ok {
		return
	}

	var req createSquadRequest
	if !decodeRequiredJSON(w, r, &req) {
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
	scope, validScope := normalizeSquadScope(req.Scope)
	if !validScope {
		writeError(w, http.StatusBadRequest, "scope must be 'workspace' or 'personal'")
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

	leader, ok := loadSquadAgent(w, r, h.Queries, wsUUID, leaderUUID, "leader must be a valid agent in this workspace")
	if !ok {
		return
	}
	if !h.requirePersonalAgentAccess(w, r, leader, "member", uuidToString(member.UserID), workspaceID, "cannot use personal leader agent") {
		return
	}
	if err := validateSquadLeaderScope(scope, member.UserID, leader); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	preparedMembers := make([]preparedSquadMember, 0, len(req.Members))
	seenMembers := map[string]struct{}{"agent:" + uuidToString(leaderUUID): {}}
	for i, input := range req.Members {
		if input.MemberType != "agent" && input.MemberType != "member" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("members[%d].member_type must be 'agent' or 'member'", i))
			return
		}
		memberUUID, ok := parseUUIDOrBadRequest(w, input.MemberID, fmt.Sprintf("members[%d].member_id", i))
		if !ok {
			return
		}
		identity := input.MemberType + ":" + uuidToString(memberUUID)
		if _, duplicate := seenMembers[identity]; duplicate {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("members[%d] duplicates the leader or another member", i))
			return
		}
		seenMembers[identity] = struct{}{}
		req.Members[i].MemberID = uuidToString(memberUUID)
		if input.MemberType == "agent" {
			agentMember, ok := loadSquadAgent(w, r, h.Queries, wsUUID, memberUUID, fmt.Sprintf("members[%d] agent not found in this workspace", i))
			if !ok {
				return
			}
			if !h.requirePersonalAgentAccess(w, r, agentMember, "member", uuidToString(member.UserID), workspaceID, fmt.Sprintf("cannot add members[%d] personal agent", i)) {
				return
			}
			if err := validateSquadLeaderScope(scope, member.UserID, agentMember); err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("members[%d]: %s", i, err))
				return
			}
		} else {
			if ok := loadSquadMember(w, r, h.Queries, wsUUID, memberUUID, fmt.Sprintf("members[%d] member not found in this workspace", i)); !ok {
				return
			}
		}
		preparedMembers = append(preparedMembers, preparedSquadMember{
			memberType: input.MemberType,
			memberID:   memberUUID,
			role:       input.Role,
		})
	}

	avatarURL := pgtype.Text{}
	if req.AvatarURL != nil {
		avatarURL = pgtype.Text{String: *req.AvatarURL, Valid: true}
	}
	sopProfile, err := normalizeSquadSOPProfile(req.SOPProfile)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.LeaderID = uuidToString(leaderUUID)
	req.Scope = scope
	requestHash, err := hashRequestFingerprint(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create squad")
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	writeReplayError := resourceCreateReplayErrorWriter(
		"Idempotency-Key was already used with a different request",
		"failed to load squad request",
	)
	loadReplay := func() (squadResponse, bool, error) {
		return loadResourceCreateReplay(
			r.Context(), h.Queries, wsUUID, member.UserID, resourceTypeSquad,
			idempotencyKey, requestHash,
			func(response squadResponse) bool { return response.ID != "" },
		)
	}
	if handleResourceCreateReplay(w, http.StatusCreated, loadReplay, writeReplayError) {
		return
	}

	tx, qtx, ok := h.beginResourceCreateTransaction(w, r.Context(), "failed to start squad create")
	if !ok {
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	err = reserveResourceCreateRequest(r.Context(), qtx, wsUUID, member.UserID, resourceTypeSquad, idempotencyKey, requestHash)
	if !handleResourceCreateReservation(
		w, r.Context(), tx, err, loadReplay,
		writeReplayError,
		"failed to reserve squad request",
		http.StatusCreated,
	) {
		return
	}

	squad, err := qtx.CreateSquad(r.Context(), db.CreateSquadParams{
		WorkspaceID:  wsUUID,
		Name:         req.Name,
		Description:  req.Description,
		LeaderID:     leaderUUID,
		CreatorID:    member.UserID,
		AvatarUrl:    avatarURL,
		Scope:        pgtype.Text{String: scope, Valid: true},
		Instructions: pgtype.Text{},
		SopProfile:   sopProfile,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create squad")
		return
	}

	// Squad identity and its mandatory leader membership are one invariant.
	if _, err := qtx.AddSquadMember(r.Context(), db.AddSquadMemberParams{
		SquadID:    squad.ID,
		MemberType: "agent",
		MemberID:   leaderUUID,
		Role:       "leader",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add squad leader")
		return
	}
	for i, squadMember := range preparedMembers {
		if _, err := qtx.AddSquadMember(r.Context(), db.AddSquadMemberParams{
			SquadID:    squad.ID,
			MemberType: squadMember.memberType,
			MemberID:   squadMember.memberID,
			Role:       squadMember.role,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to add initial squad member at index %d", i))
			return
		}
	}
	resp, err := squadToResponse(squad)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decode squad SOP profile")
		return
	}
	summary := &squadMemberSummary{}
	addSquadMemberPreview(summary, "agent", leaderUUID, "leader")
	for _, squadMember := range preparedMembers {
		addSquadMemberPreview(summary, squadMember.memberType, squadMember.memberID, squadMember.role)
	}
	applySquadMemberSummary(&resp, summary)
	if err := completeResourceCreateRequest(
		r.Context(), qtx, wsUUID, member.UserID, resourceTypeSquad,
		idempotencyKey, requestHash, squad.ID, resp,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to complete squad request")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit squad create")
		return
	}

	h.publish(protocol.EventSquadCreated, workspaceID, "member", uuidToString(member.UserID), map[string]any{"squad": resp})
	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.SquadCreated(
		uuidToString(member.UserID),
		workspaceID,
		uuidToString(squad.ID),
		resp.MemberCount,
	))
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) GetSquad(w http.ResponseWriter, r *http.Request) {
	squad, workspaceID, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if !h.requireSquadVisible(w, r, squad, workspaceID) {
		return
	}
	resp, err := squadToResponseWithPreview(r.Context(), h.Queries, squad)
	if err != nil {
		slog.Error("build squad response failed", append(logger.RequestAttrs(r), "squad_id", uuidToString(squad.ID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load squad")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) UpdateSquad(w http.ResponseWriter, r *http.Request) {
	squad, workspaceID, member, ok := h.loadManagedSquad(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var req struct {
		Name         *string         `json:"name"`
		Description  *string         `json:"description"`
		Instructions *string         `json:"instructions"`
		LeaderID     *string         `json:"leader_id"`
		AvatarURL    *string         `json:"avatar_url"`
		Scope        *string         `json:"scope"`
		SOPProfile   json.RawMessage `json:"sop_profile"`
	}
	if !decodeRequiredJSON(w, r, &req) {
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
		params.AvatarUrl = pgtype.Text{String: *req.AvatarURL, Valid: true}
	}
	nextScope := squad.Scope
	if req.Scope != nil {
		scope, validScope := normalizeSquadScope(*req.Scope)
		if !validScope {
			writeError(w, http.StatusBadRequest, "scope must be 'workspace' or 'personal'")
			return
		}
		nextScope = scope
		params.Scope = pgtype.Text{String: scope, Valid: true}
	}
	if len(req.SOPProfile) > 0 {
		sopProfile, err := normalizeSquadSOPProfile(req.SOPProfile)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.SopProfile = sopProfile
	}
	nextLeaderID := squad.LeaderID
	var nextLeader db.Agent
	haveNextLeader := false
	if req.LeaderID != nil {
		lid, ok := parseUUIDOrBadRequest(w, *req.LeaderID, "leader_id")
		if !ok {
			return
		}
		leader, ok := loadSquadAgent(w, r, h.Queries, wsUUID, lid, "leader must be a valid agent in this workspace")
		if !ok {
			return
		}
		if !h.requirePersonalAgentAccess(w, r, leader, "member", uuidToString(member.UserID), workspaceID, "cannot use personal leader agent") {
			return
		}
		nextLeader = leader
		haveNextLeader = true
		params.LeaderID = lid
		nextLeaderID = lid
	}
	if req.Scope != nil || req.LeaderID != nil {
		if !haveNextLeader {
			leader, ok := loadSquadAgent(w, r, h.Queries, wsUUID, nextLeaderID, "leader must be a valid agent in this workspace")
			if !ok {
				return
			}
			nextLeader = leader
		}
		if err := validateSquadLeaderScope(nextScope, squad.CreatorID, nextLeader); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin squad update")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	if req.LeaderID != nil {
		isMember, err := qtx.IsSquadMember(r.Context(), db.IsSquadMemberParams{
			SquadID: squad.ID, MemberType: "agent", MemberID: params.LeaderID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check squad membership")
			return
		}
		if !isMember {
			if _, err := qtx.AddSquadMember(r.Context(), db.AddSquadMemberParams{
				SquadID: squad.ID, MemberType: "agent", MemberID: params.LeaderID, Role: "leader",
			}); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to add squad leader membership")
				return
			}
		}
	}

	updated, err := qtx.UpdateSquad(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update squad")
		return
	}

	resp, err := squadToResponseWithPreview(r.Context(), qtx, updated)
	if err != nil {
		slog.Error("build squad response failed", append(logger.RequestAttrs(r), "squad_id", uuidToString(updated.ID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load squad")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit squad update")
		return
	}
	h.publish(protocol.EventSquadUpdated, workspaceID, "member", requestUserID(r), map[string]any{"squad": resp})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) DeleteSquad(w http.ResponseWriter, r *http.Request) {
	squad, workspaceID, _, ok := h.loadManagedSquad(w, r)
	if !ok {
		return
	}

	if squad.ArchivedAt.Valid {
		writeError(w, http.StatusBadRequest, "squad is already archived")
		return
	}

	userID := requestUserID(r)
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user_id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin squad archive")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)

	// The assignment transfers and archive state are one invariant: no active
	// record may continue to target an archived squad.
	if err := qtx.TransferSquadAssignees(r.Context(), db.TransferSquadAssigneesParams{
		AssigneeID:   squad.ID,
		AssigneeID_2: squad.LeaderID,
	}); err != nil {
		slog.Error("transfer squad assignees failed", append(logger.RequestAttrs(r), "squad_id", uuidToString(squad.ID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to transfer squad assignments")
		return
	}

	// Mirror the issue-assignee transfer for autopilots that target this
	// squad. Without this, autopilot.assignee_id would still point at the
	// archived squad row and every subsequent dispatch would skip with
	// "assignee squad is archived" — visible to ops but useless to the
	// owner. Rewriting to the leader keeps the autopilot semantics
	// unchanged (Path A from MUL-2429 is leader-only execution anyway).
	if err := qtx.TransferSquadAutopilotsToLeader(r.Context(), db.TransferSquadAutopilotsToLeaderParams{
		AssigneeID:   squad.ID,
		AssigneeID_2: squad.LeaderID,
	}); err != nil {
		slog.Error("transfer squad autopilots failed", append(logger.RequestAttrs(r), "squad_id", uuidToString(squad.ID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to transfer squad autopilots")
		return
	}

	if _, err := qtx.ArchiveSquad(r.Context(), db.ArchiveSquadParams{
		ID:         squad.ID,
		ArchivedBy: userUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive squad")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit squad archive")
		return
	}

	h.publish(protocol.EventSquadDeleted, workspaceID, "member", userID, map[string]any{
		"squad_id":  uuidToString(squad.ID),
		"leader_id": uuidToString(squad.LeaderID),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) RestoreSquad(w http.ResponseWriter, r *http.Request) {
	squad, workspaceID, _, ok := h.loadManagedSquad(w, r)
	if !ok {
		return
	}
	if !squad.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "squad is not archived")
		return
	}

	restored, err := h.Queries.RestoreSquad(r.Context(), squad.ID)
	if err != nil {
		slog.Warn("restore squad failed", append(logger.RequestAttrs(r), "error", err, "squad_id", uuidToString(squad.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to restore squad")
		return
	}
	resp, err := squadToResponseWithPreview(r.Context(), h.Queries, restored)
	if err != nil {
		slog.Error("build squad response failed", append(logger.RequestAttrs(r), "squad_id", uuidToString(restored.ID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load squad")
		return
	}
	userID := requestUserID(r)
	h.publish(protocol.EventSquadRestored, workspaceID, "member", userID, map[string]any{"squad": resp})
	writeJSON(w, http.StatusOK, resp)
}

// ── Squad Members ───────────────────────────────────────────────────────────
