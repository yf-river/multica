package handler

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func createLifeMemoryRevision(ctx context.Context, q *db.Queries, memory db.LifeMemory, changeType, reason, actorType string, actorID pgtype.UUID) error {
	revision, err := q.GetNextLifeMemoryRevision(ctx, memory.ID)
	if err != nil {
		return err
	}
	_, err = q.CreateLifeMemoryRevision(ctx, db.CreateLifeMemoryRevisionParams{
		MemoryID: memory.ID, Revision: revision, Kind: memory.Kind, Status: memory.Status,
		Content: memory.Content, Confidence: memory.Confidence, Urgency: memory.Urgency,
		Uncertainty: memory.Uncertainty, Scope: memory.Scope, ChangeType: changeType,
		ChangeReason: reason, ChangedByType: actorType, ChangedByID: actorID,
	})
	return err
}

const lifeMemoryEvidenceExcerptMaxRunes = 2_000

var lifeMemoryKindRank = map[string]int{
	"current_expression": 0,
	"weak_signal":        1,
	"understanding":      2,
	"fact":               3,
	"plan":               3,
	"commitment":         4,
}

type lifeRequestScope struct {
	workspaceID pgtype.UUID
	userID      pgtype.UUID
}

func (h *Handler) requireLifeRequestScope(w http.ResponseWriter, r *http.Request) (lifeRequestScope, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return lifeRequestScope{}, false
	}
	actorType, _ := resolveActor(r, userID)
	if actorType != "member" {
		writeError(w, http.StatusForbidden, "life settings require a member")
		return lifeRequestScope{}, false
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return lifeRequestScope{}, false
	}
	parsedUserID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return lifeRequestScope{}, false
	}
	return lifeRequestScope{workspaceID: workspaceID, userID: parsedUserID}, true
}

type companionProfileResponse struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	AgentID     string `json:"agent_id"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func companionProfileToResponse(profile db.CompanionProfile) companionProfileResponse {
	return companionProfileResponse{
		WorkspaceID: uuidToString(profile.WorkspaceID),
		UserID:      uuidToString(profile.UserID),
		AgentID:     uuidToString(profile.AgentID),
		CreatedAt:   timestampToString(profile.CreatedAt),
		UpdatedAt:   timestampToString(profile.UpdatedAt),
	}
}

func (h *Handler) GetCompanionProfile(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	profile, err := h.Queries.GetCompanionProfile(r.Context(), db.GetCompanionProfileParams{
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"profile": nil})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load companion")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": companionProfileToResponse(profile)})
}

func (h *Handler) UpsertCompanionProfile(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
	if !ok {
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), agentID)
	if err != nil || agent.WorkspaceID != scope.workspaceID || agent.ArchivedAt.Valid {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if !h.requirePersonalAgentAccess(w, r, agent, "member", uuidToString(scope.userID), uuidToString(scope.workspaceID), "you do not have access to this agent") {
		return
	}
	observers, err := h.Queries.ListLifeObservers(r.Context(), db.ListLifeObserversParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate life runtimes")
		return
	}
	for _, observer := range observers {
		observerAgent, loadErr := h.Queries.GetAgent(r.Context(), observer.AgentID)
		if loadErr != nil || observerAgent.ArchivedAt.Valid {
			writeError(w, http.StatusConflict, "observer agent is unavailable")
			return
		}
		if observerAgent.RuntimeID != agent.RuntimeID || strings.TrimSpace(observerAgent.Model.String) != strings.TrimSpace(agent.Model.String) {
			writeError(w, http.StatusConflict, "companion runtime and model must match every observer")
			return
		}
	}
	profile, err := h.Queries.UpsertCompanionProfile(r.Context(), db.UpsertCompanionProfileParams{
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
		AgentID:     agentID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save companion")
		return
	}
	if err := h.ensureCompanionLifeDefaults(r.Context(), scope); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to initialize companion life capabilities")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": companionProfileToResponse(profile)})
}

type lifeMemoryEvidenceResponse struct {
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
	Excerpt    string `json:"excerpt"`
	Stance     string `json:"stance"`
	ObservedAt string `json:"observed_at"`
}

type lifeMemoryResponse struct {
	ID            string                       `json:"id"`
	Kind          string                       `json:"kind"`
	Status        string                       `json:"status"`
	Content       string                       `json:"content"`
	Confidence    float64                      `json:"confidence"`
	Urgency       float64                      `json:"urgency"`
	Uncertainty   string                       `json:"uncertainty"`
	ValidFrom     *string                      `json:"valid_from"`
	ValidTo       *string                      `json:"valid_to"`
	ConfirmedAt   *string                      `json:"confirmed_at"`
	CreatedByType string                       `json:"created_by_type"`
	CreatedByID   string                       `json:"created_by_id"`
	CreatedAt     string                       `json:"created_at"`
	UpdatedAt     string                       `json:"updated_at"`
	Evidence      []lifeMemoryEvidenceResponse `json:"evidence"`
}

func (h *Handler) lifeMemoryToResponse(r *http.Request, memory db.LifeMemory) (lifeMemoryResponse, error) {
	evidence, err := h.Queries.ListLifeMemoryEvidence(r.Context(), memory.ID)
	if err != nil {
		return lifeMemoryResponse{}, err
	}
	items := make([]lifeMemoryEvidenceResponse, 0, len(evidence))
	for _, item := range evidence {
		items = append(items, lifeMemoryEvidenceResponse{
			SourceType: item.SourceType,
			SourceID:   uuidToString(item.SourceID),
			Excerpt:    item.Excerpt,
			Stance:     item.Stance,
			ObservedAt: timestampToString(item.ObservedAt),
		})
	}
	return lifeMemoryResponse{
		ID:            uuidToString(memory.ID),
		Kind:          memory.Kind,
		Status:        memory.Status,
		Content:       memory.Content,
		Confidence:    memory.Confidence,
		Urgency:       memory.Urgency,
		Uncertainty:   memory.Uncertainty,
		ValidFrom:     timestampToPtr(memory.ValidFrom),
		ValidTo:       timestampToPtr(memory.ValidTo),
		ConfirmedAt:   timestampToPtr(memory.ConfirmedAt),
		CreatedByType: memory.CreatedByType,
		CreatedByID:   uuidToString(memory.CreatedByID),
		CreatedAt:     timestampToString(memory.CreatedAt),
		UpdatedAt:     timestampToString(memory.UpdatedAt),
		Evidence:      items,
	}, nil
}

func validLifeMemoryKind(kind string) bool {
	_, ok := lifeMemoryKindRank[kind]
	return ok
}

func validLifeMemoryStatus(status string) bool {
	switch status {
	case "candidate", "confirmed", "archived":
		return true
	default:
		return false
	}
}

func validateLifeMemoryContent(w http.ResponseWriter, kind, content string, confidence, urgency float64) bool {
	if !validLifeMemoryKind(kind) {
		writeError(w, http.StatusBadRequest, "invalid memory kind")
		return false
	}
	if strings.TrimSpace(content) == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return false
	}
	if confidence < 0 || confidence > 1 {
		writeError(w, http.StatusBadRequest, "confidence must be between 0 and 1")
		return false
	}
	if urgency < 0 || urgency > 1 {
		writeError(w, http.StatusBadRequest, "urgency must be between 0 and 1")
		return false
	}
	return true
}

func parseLifeOptionalTime(w http.ResponseWriter, raw, field string) (pgtype.Timestamptz, bool) {
	if strings.TrimSpace(raw) == "" {
		return pgtype.Timestamptz{}, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, field+" must be RFC3339")
		return pgtype.Timestamptz{}, false
	}
	return pgtype.Timestamptz{Time: parsed, Valid: true}, true
}

func (h *Handler) ListLifeMemories(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && !validLifeMemoryStatus(status) {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	var memories []db.LifeMemory
	var err error
	if status == "" {
		memories, err = h.Queries.ListLifeMemories(r.Context(), db.ListLifeMemoriesParams{
			WorkspaceID: scope.workspaceID,
			UserID:      scope.userID,
		})
	} else {
		memories, err = h.Queries.ListLifeMemoriesByStatus(r.Context(), db.ListLifeMemoriesByStatusParams{
			WorkspaceID: scope.workspaceID,
			UserID:      scope.userID,
			Status:      status,
		})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list memories")
		return
	}
	items := make([]lifeMemoryResponse, 0, len(memories))
	for _, memory := range memories {
		item, err := h.lifeMemoryToResponse(r, memory)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load memory evidence")
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"memories": items})
}

type lifeMemoryEvidenceRequest struct {
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
	Excerpt    string `json:"excerpt"`
	Stance     string `json:"stance"`
}

type createLifeMemoryRequest struct {
	Kind        string                      `json:"kind"`
	Content     string                      `json:"content"`
	Confidence  float64                     `json:"confidence"`
	Urgency     float64                     `json:"urgency"`
	Uncertainty string                      `json:"uncertainty"`
	ValidFrom   string                      `json:"valid_from"`
	ValidTo     string                      `json:"valid_to"`
	Evidence    []lifeMemoryEvidenceRequest `json:"evidence"`
}

type resolvedLifeMemoryEvidence struct {
	sourceType     string
	sourceID       pgtype.UUID
	excerpt        string
	stance         string
	observedAt     pgtype.Timestamptz
	sourceMemoryID pgtype.UUID
}

func (h *Handler) resolveLifeMemoryEvidence(w http.ResponseWriter, r *http.Request, q *db.Queries, scope lifeRequestScope, req lifeMemoryEvidenceRequest) (resolvedLifeMemoryEvidence, bool) {
	sourceID, ok := parseUUIDOrBadRequest(w, req.SourceID, "evidence.source_id")
	if !ok {
		return resolvedLifeMemoryEvidence{}, false
	}
	excerpt := strings.TrimSpace(req.Excerpt)
	stance := strings.TrimSpace(req.Stance)
	if stance == "" {
		stance = "supports"
	}
	if stance != "supports" && stance != "contradicts" && stance != "context" {
		writeError(w, http.StatusBadRequest, "evidence.stance must be supports, contradicts or context")
		return resolvedLifeMemoryEvidence{}, false
	}
	if len([]rune(excerpt)) > lifeMemoryEvidenceExcerptMaxRunes {
		writeError(w, http.StatusBadRequest, "evidence excerpt is too long")
		return resolvedLifeMemoryEvidence{}, false
	}
	switch req.SourceType {
	case "chat_message":
		row, err := q.GetChatMessageForLifeEvidence(r.Context(), db.GetChatMessageForLifeEvidenceParams{
			ID:          sourceID,
			WorkspaceID: scope.workspaceID,
			CreatorID:   scope.userID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "chat evidence not found")
			return resolvedLifeMemoryEvidence{}, false
		}
		if excerpt == "" {
			excerpt = row.ChatMessage.Content
		}
		return resolvedLifeMemoryEvidence{
			sourceType: req.SourceType,
			sourceID:   sourceID,
			excerpt:    excerpt,
			observedAt: row.ChatMessage.CreatedAt,
			stance:     stance,
		}, true
	case "memory":
		memory, err := q.GetLifeMemory(r.Context(), db.GetLifeMemoryParams{
			ID:          sourceID,
			WorkspaceID: scope.workspaceID,
			UserID:      scope.userID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "memory evidence not found")
			return resolvedLifeMemoryEvidence{}, false
		}
		if excerpt == "" {
			excerpt = memory.Content
		}
		return resolvedLifeMemoryEvidence{
			sourceType:     req.SourceType,
			sourceID:       sourceID,
			excerpt:        excerpt,
			observedAt:     memory.UpdatedAt,
			stance:         stance,
			sourceMemoryID: sourceID,
		}, true
	default:
		writeError(w, http.StatusBadRequest, "unsupported evidence source_type")
		return resolvedLifeMemoryEvidence{}, false
	}
}

func (h *Handler) CreateLifeMemory(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	h.createLifeMemory(w, r, scope, "member", scope.userID)
}

func (h *Handler) createLifeMemory(w http.ResponseWriter, r *http.Request, scope lifeRequestScope, createdByType string, createdByID pgtype.UUID) {
	var req createLifeMemoryRequest
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if !validateLifeMemoryContent(w, req.Kind, req.Content, req.Confidence, req.Urgency) {
		return
	}
	if len(req.Evidence) == 0 {
		writeError(w, http.StatusBadRequest, "at least one evidence item is required")
		return
	}
	validFrom, ok := parseLifeOptionalTime(w, req.ValidFrom, "valid_from")
	if !ok {
		return
	}
	validTo, ok := parseLifeOptionalTime(w, req.ValidTo, "valid_to")
	if !ok {
		return
	}
	if validFrom.Valid && validTo.Valid && validTo.Time.Before(validFrom.Time) {
		writeError(w, http.StatusBadRequest, "valid_to must not be before valid_from")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start memory transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	resolved := make([]resolvedLifeMemoryEvidence, 0, len(req.Evidence))
	for _, item := range req.Evidence {
		evidence, ok := h.resolveLifeMemoryEvidence(w, r, qtx, scope, item)
		if !ok {
			return
		}
		resolved = append(resolved, evidence)
	}
	memory, err := qtx.CreateLifeMemory(r.Context(), db.CreateLifeMemoryParams{
		WorkspaceID:   scope.workspaceID,
		UserID:        scope.userID,
		CreatedByType: createdByType,
		CreatedByID:   createdByID,
		Kind:          req.Kind,
		Content:       strings.TrimSpace(req.Content),
		Confidence:    req.Confidence,
		Urgency:       req.Urgency,
		Uncertainty:   strings.TrimSpace(req.Uncertainty),
		ValidFrom:     validFrom,
		ValidTo:       validTo,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create memory")
		return
	}
	if err := createLifeMemoryRevision(r.Context(), qtx, memory, "created", "由用户建立", createdByType, createdByID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create memory revision")
		return
	}
	for _, item := range resolved {
		if _, err := qtx.CreateLifeMemoryEvidence(r.Context(), db.CreateLifeMemoryEvidenceParams{
			MemoryID:   memory.ID,
			SourceType: item.sourceType,
			SourceID:   item.sourceID,
			Excerpt:    item.excerpt,
			ObservedAt: item.observedAt,
			Stance:     item.stance,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create memory evidence")
			return
		}
		if item.sourceMemoryID.Valid {
			if err := qtx.CreateLifeMemoryDependency(r.Context(), db.CreateLifeMemoryDependencyParams{
				SourceMemoryID:  item.sourceMemoryID,
				DerivedMemoryID: memory.ID,
			}); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to link memory dependency")
				return
			}
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit memory")
		return
	}
	response, err := h.lifeMemoryToResponse(r, memory)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load memory")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) loadLifeMemory(w http.ResponseWriter, r *http.Request, scope lifeRequestScope) (db.LifeMemory, bool) {
	memoryID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "memoryId"), "memory id")
	if !ok {
		return db.LifeMemory{}, false
	}
	memory, err := h.Queries.GetLifeMemory(r.Context(), db.GetLifeMemoryParams{
		ID:          memoryID,
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
	})
	if err != nil {
		writeEntityLoadError(w, err, "memory", "memory_id", chi.URLParam(r, "memoryId"))
		return db.LifeMemory{}, false
	}
	return memory, true
}

func (h *Handler) UpdateLifeMemory(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	memory, ok := h.loadLifeMemory(w, r, scope)
	if !ok {
		return
	}
	var req createLifeMemoryRequest
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if !validateLifeMemoryContent(w, req.Kind, req.Content, req.Confidence, req.Urgency) {
		return
	}
	validFrom, ok := parseLifeOptionalTime(w, req.ValidFrom, "valid_from")
	if !ok {
		return
	}
	validTo, ok := parseLifeOptionalTime(w, req.ValidTo, "valid_to")
	if !ok {
		return
	}
	if validFrom.Valid && validTo.Valid && validTo.Time.Before(validFrom.Time) {
		writeError(w, http.StatusBadRequest, "valid_to must not be before valid_from")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start memory correction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	updated, err := qtx.UpdateLifeMemoryContent(r.Context(), db.UpdateLifeMemoryContentParams{
		ID:          memory.ID,
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
		Kind:        req.Kind,
		Content:     strings.TrimSpace(req.Content),
		Confidence:  req.Confidence,
		Urgency:     req.Urgency,
		Uncertainty: strings.TrimSpace(req.Uncertainty),
		ValidFrom:   validFrom,
		ValidTo:     validTo,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update memory")
		return
	}
	if err := createLifeMemoryRevision(r.Context(), qtx, updated, "corrected", "由用户纠正", "member", scope.userID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record memory correction")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit memory correction")
		return
	}
	response, err := h.lifeMemoryToResponse(r, updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load memory")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) ConfirmLifeMemory(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	memory, ok := h.loadLifeMemory(w, r, scope)
	if !ok {
		return
	}
	if memory.Status != "candidate" {
		writeError(w, http.StatusConflict, "only candidate memories can be confirmed")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start memory confirmation")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	confirmed, err := qtx.ConfirmLifeMemory(r.Context(), db.ConfirmLifeMemoryParams{
		ID:            memory.ID,
		WorkspaceID:   scope.workspaceID,
		UserID:        scope.userID,
		ConfirmedByID: scope.userID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to confirm memory")
		return
	}
	if err := createLifeMemoryRevision(r.Context(), qtx, confirmed, "confirmed", "由用户确认", "member", scope.userID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record memory confirmation")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit memory confirmation")
		return
	}
	response, err := h.lifeMemoryToResponse(r, confirmed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load memory")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) DowngradeLifeMemory(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	memory, ok := h.loadLifeMemory(w, r, scope)
	if !ok {
		return
	}
	var req struct {
		Kind string `json:"kind"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	targetRank, valid := lifeMemoryKindRank[req.Kind]
	if !valid || targetRank >= lifeMemoryKindRank[memory.Kind] {
		writeError(w, http.StatusBadRequest, "kind must be lower confidence than the current kind")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start memory downgrade")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	downgraded, err := qtx.DowngradeLifeMemory(r.Context(), db.DowngradeLifeMemoryParams{
		ID:          memory.ID,
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
		Kind:        req.Kind,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to downgrade memory")
		return
	}
	if err := createLifeMemoryRevision(r.Context(), qtx, downgraded, "downgraded", "由用户降低可信等级", "member", scope.userID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record memory downgrade")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit memory downgrade")
		return
	}
	response, err := h.lifeMemoryToResponse(r, downgraded)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load memory")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) ArchiveLifeMemory(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	memory, ok := h.loadLifeMemory(w, r, scope)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start memory archive")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	archived, err := qtx.ArchiveLifeMemory(r.Context(), db.ArchiveLifeMemoryParams{
		ID:          memory.ID,
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive memory")
		return
	}
	if err := createLifeMemoryRevision(r.Context(), qtx, archived, "archived", "由用户归档", "member", scope.userID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record memory archive")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit memory archive")
		return
	}
	response, err := h.lifeMemoryToResponse(r, archived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load memory")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) DeleteLifeMemory(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	memoryID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "memoryId"), "memory id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start deletion transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.GetLifeMemory(r.Context(), db.GetLifeMemoryParams{
		ID:          memoryID,
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
	}); err != nil {
		writeEntityLoadError(w, err, "memory", "memory_id", chi.URLParam(r, "memoryId"))
		return
	}
	memoryIDs, err := qtx.ListDerivedLifeMemoryIDs(r.Context(), db.ListDerivedLifeMemoryIDsParams{
		ID:          memoryID,
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve memory dependencies")
		return
	}
	materials, err := qtx.ListLifeMaterialsByEvidenceSources(r.Context(), db.ListLifeMaterialsByEvidenceSourcesParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID, Column3: memoryIDs,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve memory source materials")
		return
	}
	materialIDs := make([]pgtype.UUID, 0, len(materials))
	sourceIDs := make([]string, 0, len(materials)+len(memoryIDs))
	sourceTokens := make([]string, 0, len(materials)*2+len(memoryIDs))
	for _, material := range materials {
		digest := sha256.Sum256([]byte(material.Content))
		if err := qtx.CreateLifeForgetTombstone(r.Context(), db.CreateLifeForgetTombstoneParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID, SourceType: material.SourceType,
			SourceKey: material.SourceKey, ContentHash: fmt.Sprintf("%x", digest[:]),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to record permanent forgetting")
			return
		}
		materialIDs = append(materialIDs, material.ID)
		sourceIDs = append(sourceIDs, material.SourceKey)
		sourceTokens = append(sourceTokens,
			material.SourceType+":"+material.SourceKey,
			material.SourceType+":"+uuidToString(material.ID),
			"material:"+uuidToString(material.ID),
		)
	}
	memorySet := make(map[string]pgtype.UUID, len(memoryIDs))
	for _, id := range memoryIDs {
		key := uuidToString(id)
		memorySet[key] = id
		sourceIDs = append(sourceIDs, key)
		sourceTokens = append(sourceTokens, "memory:"+key)
	}
	// A source can feed several model-generated records without an explicit
	// memory-to-memory edge. Walk the provenance graph until every derived
	// memory is part of the same atomic deletion.
	for {
		derivations, err := qtx.ListLifeDerivationsBySources(r.Context(), db.ListLifeDerivationsBySourcesParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID, Column3: sourceTokens,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to resolve derived life records")
			return
		}
		changed := false
		for _, derivation := range derivations {
			if derivation.TargetType != "memory" {
				continue
			}
			key := uuidToString(derivation.TargetID)
			if _, exists := memorySet[key]; exists {
				continue
			}
			derivedIDs, err := qtx.ListDerivedLifeMemoryIDs(r.Context(), db.ListDerivedLifeMemoryIDsParams{
				ID: derivation.TargetID, WorkspaceID: scope.workspaceID, UserID: scope.userID,
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to resolve derived memory dependencies")
				return
			}
			for _, derivedID := range derivedIDs {
				derivedKey := uuidToString(derivedID)
				if _, exists := memorySet[derivedKey]; exists {
					continue
				}
				memorySet[derivedKey] = derivedID
				memoryIDs = append(memoryIDs, derivedID)
				sourceIDs = append(sourceIDs, derivedKey)
				sourceTokens = append(sourceTokens, "memory:"+derivedKey)
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	if len(materialIDs) > 0 {
		materialIDStrings := make([]string, 0, len(materialIDs))
		for _, id := range materialIDs {
			materialIDStrings = append(materialIDStrings, uuidToString(id))
		}
		if err := qtx.ScrubLifeCognitionTasksByMaterialIDs(r.Context(), db.ScrubLifeCognitionTasksByMaterialIDsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, Column3: materialIDStrings}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scrub background task copies")
			return
		}
	}
	if len(sourceTokens) > 0 {
		if err := qtx.DeleteLifeDerivedRecordsByTargets(r.Context(), db.DeleteLifeDerivedRecordsByTargetsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, Column3: sourceTokens}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete derived life records")
			return
		}
	}
	roundIDs, err := qtx.ListLifeExperimentRoundIDsByMemoryIDs(r.Context(), memoryIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve experiment dependencies")
		return
	}
	if len(roundIDs) > 0 {
		if err := qtx.DeleteLifeActionProposalsByRoundIDs(r.Context(), roundIDs); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete proposal dependencies")
			return
		}
	}
	for _, id := range roundIDs {
		sourceIDs = append(sourceIDs, uuidToString(id))
	}
	if err := qtx.DeleteLifeCommitmentsByMemoryIDs(r.Context(), db.DeleteLifeCommitmentsByMemoryIDsParams{Column1: memoryIDs, WorkspaceID: scope.workspaceID, UserID: scope.userID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete commitment dependencies")
		return
	}
	if len(sourceIDs) > 0 {
		if err := qtx.DeleteLifeObserverJudgementsBySources(r.Context(), db.DeleteLifeObserverJudgementsBySourcesParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, Column3: sourceIDs}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete observer dependencies")
			return
		}
	}
	if err := qtx.DeleteLifeChronicleEntriesBySources(r.Context(), db.DeleteLifeChronicleEntriesBySourcesParams{
		Column1: memoryIDs,
		Column2: roundIDs,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete chronicle dependencies")
		return
	}
	if len(roundIDs) > 0 {
		if err := qtx.DeleteLifeExperimentRoundsByIDs(r.Context(), roundIDs); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete experiment dependencies")
			return
		}
	}
	if err := qtx.DeleteLifeMemoriesByIDs(r.Context(), db.DeleteLifeMemoriesByIDsParams{
		Column1:     memoryIDs,
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete memory")
		return
	}
	if err := qtx.DeleteLifeDerivationsByMemoryIDs(r.Context(), db.DeleteLifeDerivationsByMemoryIDsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, Column3: memoryIDs}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete memory provenance")
		return
	}
	if len(materialIDs) > 0 {
		if err := qtx.DeleteLifeMaterialsByIDs(r.Context(), db.DeleteLifeMaterialsByIDsParams{Column1: materialIDs, WorkspaceID: scope.workspaceID, UserID: scope.userID}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete source materials")
			return
		}
	}
	if err := qtx.DeleteEmptyLifeTopics(r.Context(), db.DeleteEmptyLifeTopicsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID}); err != nil {
		writeError(w, 500, "failed to delete empty topics")
		return
	}
	if err := qtx.DeleteEmptyLifeObservationTopics(r.Context(), db.DeleteEmptyLifeObservationTopicsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID}); err != nil {
		writeError(w, 500, "failed to delete empty observation topics")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit memory deletion")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
