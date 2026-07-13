package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	skillpkg "github.com/multica-ai/multica/server/internal/skill"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// sanitizePostgresText makes a string safe for a PostgreSQL TEXT column.
//
// Two failure modes covered:
//   - Embedded NUL (0x00) — PG rejects with SQLSTATE 22021. Removed.
//   - Other invalid-UTF-8 byte sequences (e.g. 0x91 = Windows-1252 smart
//     quote, which crashed agent-template import of skills containing
//     Windows-encoded prose). `strings.ToValidUTF8` drops them.
func sanitizePostgresText(s string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "")
}

// --- Response structs ---

type SkillResponse struct {
	ID          string         `json:"id"`
	WorkspaceID string         `json:"workspace_id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Content     string         `json:"content"`
	Config      map[string]any `json:"config"`
	CreatedBy   *string        `json:"created_by"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

// SkillSummaryResponse is the list-endpoint shape: everything SkillResponse
// has except `content`. SKILL.md bodies routinely run 50–200KB and shipping
// them in list payloads bloats responses past CLI timeouts on high-latency
// links (GH multica-ai/multica#2174). Detail endpoints still return the full
// SkillResponse with content.
type SkillSummaryResponse struct {
	ID          string         `json:"id"`
	WorkspaceID string         `json:"workspace_id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Config      map[string]any `json:"config"`
	CreatedBy   *string        `json:"created_by"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

// AgentSkillSummary is the still-narrower shape used for skills embedded in
// an Agent payload (`GET /api/agents`, `GET /api/agents/{id}`). The agent
// list batch query only joins enough columns to render the assignee chip in
// the UI; the standalone `/api/agents/{id}/skills` endpoint returns the full
// SkillSummaryResponse for callers that need the source/origin info.
type AgentSkillSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type SkillFileResponse struct {
	ID        string `json:"id"`
	SkillID   string `json:"skill_id"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type SkillSearchCandidateResponse struct {
	Name         string  `json:"name"`
	URL          string  `json:"url"`
	Source       string  `json:"source"`
	Repo         *string `json:"repo"`
	InstallCount *int64  `json:"install_count"`
	GitHubStars  *int64  `json:"github_stars"`
	Description  string  `json:"description"`
}

type SkillWithFilesResponse struct {
	SkillResponse
	Files []SkillFileResponse `json:"files"`
}

type SkillImportResult struct {
	Status        string                  `json:"status"`
	Reason        string                  `json:"reason,omitempty"`
	Skill         *SkillWithFilesResponse `json:"skill,omitempty"`
	ExistingSkill *ExistingSkillIdentity  `json:"existing_skill,omitempty"`
}

type ExistingSkillIdentity struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	CreatedBy    string `json:"created_by,omitempty"`
	CanOverwrite bool   `json:"can_overwrite,omitempty"`
}

func skillToResponse(s db.Skill) (SkillResponse, error) {
	config, err := decodeSkillConfig(s.Config)
	if err != nil {
		return SkillResponse{}, err
	}
	return SkillResponse{
		ID:          uuidToString(s.ID),
		WorkspaceID: uuidToString(s.WorkspaceID),
		Name:        s.Name,
		Description: s.Description,
		Content:     s.Content,
		Config:      config,
		CreatedBy:   uuidToPtr(s.CreatedBy),
		CreatedAt:   timestampToString(s.CreatedAt),
		UpdatedAt:   timestampToString(s.UpdatedAt),
	}, nil
}

func existingSkillIdentity(skill db.Skill, userID string) ExistingSkillIdentity {
	identity := ExistingSkillIdentity{
		ID:           uuidToString(skill.ID),
		Name:         skill.Name,
		CanOverwrite: canOverwriteSkillByLocalImport(userID, skill),
	}
	if skill.CreatedBy.Valid {
		identity.CreatedBy = uuidToString(skill.CreatedBy)
	}
	return identity
}

func decodeSkillConfig(raw []byte) (map[string]any, error) {
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("decode skill config: %w", err)
	}
	if config == nil {
		return nil, fmt.Errorf("decode skill config: expected JSON object")
	}
	return config, nil
}

func writeSkillConfigDecodeError(w http.ResponseWriter, r *http.Request, skillID string, err error) {
	slog.Error("decode skill config failed", append(logger.RequestAttrs(r), "skill_id", skillID, "error", err)...)
	writeError(w, http.StatusInternalServerError, "failed to decode skill config")
}

func skillSummaryToResponse(
	id, workspaceID pgtype.UUID,
	name, description string,
	config []byte,
	createdBy pgtype.UUID,
	createdAt, updatedAt pgtype.Timestamptz,
) (SkillSummaryResponse, error) {
	decodedConfig, err := decodeSkillConfig(config)
	if err != nil {
		return SkillSummaryResponse{}, err
	}
	return SkillSummaryResponse{
		ID:          uuidToString(id),
		WorkspaceID: uuidToString(workspaceID),
		Name:        name,
		Description: description,
		Config:      decodedConfig,
		CreatedBy:   uuidToPtr(createdBy),
		CreatedAt:   timestampToString(createdAt),
		UpdatedAt:   timestampToString(updatedAt),
	}, nil
}

func skillFileToResponse(f db.SkillFile) SkillFileResponse {
	return SkillFileResponse{
		ID:        uuidToString(f.ID),
		SkillID:   uuidToString(f.SkillID),
		Path:      f.Path,
		Content:   f.Content,
		CreatedAt: timestampToString(f.CreatedAt),
		UpdatedAt: timestampToString(f.UpdatedAt),
	}
}

// --- Request structs ---

type CreateSkillRequest struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Content     string                   `json:"content"`
	Config      map[string]any           `json:"config"`
	Files       []CreateSkillFileRequest `json:"files,omitempty"`
}

type CreateSkillFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type UpdateSkillRequest struct {
	Name        *string                  `json:"name"`
	Description *string                  `json:"description"`
	Content     *string                  `json:"content"`
	Config      *map[string]any          `json:"config"`
	Files       []CreateSkillFileRequest `json:"files,omitempty"`
}

type AgentSkillsRequest struct {
	SkillIDs []string `json:"skill_ids"`
}

// --- Helpers ---

// validateFilePath checks that a file path is safe (no traversal, no absolute paths).
func validateFilePath(p string) bool {
	if p == "" {
		return false
	}
	if filepath.IsAbs(p) {
		return false
	}
	cleaned := filepath.Clean(p)
	return !strings.HasPrefix(cleaned, "..")
}

func (h *Handler) loadSkillForUser(w http.ResponseWriter, r *http.Request, id string) (db.Skill, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.Skill{}, false
	}

	skillUUID, ok := parseUUIDOrBadRequest(w, id, "skill id")
	if !ok {
		return db.Skill{}, false
	}

	skill, err := h.Queries.GetSkillInWorkspace(r.Context(), db.GetSkillInWorkspaceParams{
		ID:          skillUUID,
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		writeEntityLoadError(w, r, err, "skill", "skill_id", id)
		return db.Skill{}, false
	}
	return skill, true
}

// --- Skill CRUD ---

func (h *Handler) ListSkills(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)

	skills, err := h.Queries.ListSkillSummariesByWorkspace(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list skills")
		return
	}

	resp := make([]SkillSummaryResponse, len(skills))
	for i, s := range skills {
		resp[i], err = skillSummaryToResponse(
			s.ID, s.WorkspaceID, s.Name, s.Description, s.Config,
			s.CreatedBy, s.CreatedAt, s.UpdatedAt,
		)
		if err != nil {
			writeSkillConfigDecodeError(w, r, uuidToString(s.ID), err)
			return
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) SearchSkills(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	candidates, err := searchClawHubSkills(httpClient, query)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"code":  "upstream_unavailable",
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, candidates)
}

func (h *Handler) GetSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}

	files, err := h.Queries.ListSkillFiles(r.Context(), skill.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list skill files")
		return
	}

	fileResps := make([]SkillFileResponse, len(files))
	for i, f := range files {
		fileResps[i] = skillFileToResponse(f)
	}

	skillResp, err := skillToResponse(skill)
	if err != nil {
		writeSkillConfigDecodeError(w, r, id, err)
		return
	}
	writeJSON(w, http.StatusOK, SkillWithFilesResponse{
		SkillResponse: skillResp,
		Files:         fileResps,
	})
}

func (h *Handler) CreateSkill(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)

	creatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	creatorUUID := parseUUID(creatorID)

	var req CreateSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	for _, f := range req.Files {
		if !validateFilePath(f.Path) {
			writeError(w, http.StatusBadRequest, "invalid file path: "+f.Path)
			return
		}
	}
	requestHash, err := hashRequestFingerprint(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create skill")
		return
	}
	idempotencyKey, ok := optionalIdempotencyKey(w, r)
	if !ok {
		return
	}
	if replayed, found, err := h.loadSkillCreateReplay(
		r.Context(), workspaceUUID, creatorUUID, idempotencyKey, requestHash,
	); err != nil {
		h.writeSkillCreateReplayError(w, err)
		return
	} else if found {
		writeJSON(w, http.StatusCreated, replayed)
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start skill create")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	_, err = qtx.ReserveResourceCreateRequest(r.Context(), db.ReserveResourceCreateRequestParams{
		WorkspaceID:    workspaceUUID,
		ActorID:        creatorUUID,
		ResourceType:   resourceTypeSkill,
		IdempotencyKey: idempotencyKey,
		RequestHash:    requestHash,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(r.Context())
		replayed, found, replayErr := h.loadSkillCreateReplay(
			r.Context(), workspaceUUID, creatorUUID, idempotencyKey, requestHash,
		)
		if replayErr != nil || !found {
			if replayErr == nil {
				replayErr = errors.New("skill create replay disappeared after conflict")
			}
			h.writeSkillCreateReplayError(w, replayErr)
			return
		}
		writeJSON(w, http.StatusCreated, replayed)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reserve skill request")
		return
	}

	resp, err := createSkillWithFilesInTx(r.Context(), qtx, skillCreateInput{
		WorkspaceID: workspaceUUID,
		CreatorID:   creatorUUID,
		Name:        req.Name,
		Description: req.Description,
		Content:     req.Content,
		Config:      req.Config,
		Files:       req.Files,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a skill with this name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create skill: "+err.Error())
		return
	}
	responseBody, err := json.Marshal(resp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode skill response")
		return
	}
	if _, err := qtx.CompleteResourceCreateRequest(r.Context(), db.CompleteResourceCreateRequestParams{
		WorkspaceID:    workspaceUUID,
		ActorID:        creatorUUID,
		ResourceType:   resourceTypeSkill,
		IdempotencyKey: idempotencyKey,
		RequestHash:    requestHash,
		ResourceID:     parseUUID(resp.ID),
		ResponseBody:   responseBody,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to complete skill request")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit skill create")
		return
	}
	actorType, actorID := resolveActor(r, creatorID)
	h.publish(protocol.EventSkillCreated, workspaceID, actorType, actorID, map[string]any{"skill": resp})
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) loadSkillCreateReplay(
	ctx context.Context,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	idempotencyKey pgtype.UUID,
	requestHash string,
) (SkillWithFilesResponse, bool, error) {
	return loadResourceCreateReplay(
		ctx, h.Queries, workspaceID, actorID, resourceTypeSkill,
		idempotencyKey, requestHash,
		func(response SkillWithFilesResponse) bool { return response.ID != "" },
	)
}

func (h *Handler) writeSkillCreateReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to replay skill create")
}

// canManageSkill checks whether the current user can update or delete a skill.
// The skill creator or workspace owner/admin can manage any skill.
func (h *Handler) canManageSkill(w http.ResponseWriter, r *http.Request, skill db.Skill) bool {
	wsID := uuidToString(skill.WorkspaceID)
	member, ok := h.requireWorkspaceRole(w, r, wsID, "skill not found", "owner", "admin", "member")
	if !ok {
		return false
	}
	isAdmin := roleAllowed(member.Role, "owner", "admin")
	isSkillCreator := skill.CreatedBy.Valid && uuidToString(skill.CreatedBy) == requestUserID(r)
	if !isAdmin && !isSkillCreator {
		writeError(w, http.StatusForbidden, "only the skill creator can manage this skill")
		return false
	}
	return true
}

// canOverwriteSkillByLocalImport reports whether userID may overwrite skill via
// a runtime-local-skill re-import. This is intentionally NARROWER than
// canManageSkill: only the original creator may overwrite by re-importing.
// Workspace owners/admins who want to change a skill they did not create must
// edit it in-app instead. See MUL-2701 / MUL-2800.
func canOverwriteSkillByLocalImport(userID string, skill db.Skill) bool {
	return skill.CreatedBy.Valid && uuidToString(skill.CreatedBy) == userID
}

func (h *Handler) UpdateSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageSkill(w, r, skill) {
		return
	}

	var req UpdateSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	for _, f := range req.Files {
		if !validateFilePath(f.Path) {
			writeError(w, http.StatusBadRequest, "invalid file path: "+f.Path)
			return
		}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	qtx := h.Queries.WithTx(tx)

	params := db.UpdateSkillParams{
		ID: parseUUID(id),
	}
	if req.Name != nil {
		params.Name = pgtype.Text{String: sanitizePostgresText(*req.Name), Valid: true}
	}
	if req.Description != nil {
		params.Description = pgtype.Text{String: sanitizePostgresText(*req.Description), Valid: true}
	}
	if req.Content != nil {
		params.Content = pgtype.Text{String: sanitizePostgresText(*req.Content), Valid: true}
	}
	if req.Config != nil {
		config, err := json.Marshal(*req.Config)
		if err != nil {
			writeError(w, http.StatusBadRequest, "config must be a JSON object")
			return
		}
		params.Config = config
	}

	skill, err = qtx.UpdateSkill(r.Context(), params)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a skill with this name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update skill: "+err.Error())
		return
	}

	// If files are provided, replace all files.
	var fileResps []SkillFileResponse
	if req.Files != nil {
		if err := qtx.DeleteSkillFilesBySkill(r.Context(), skill.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete old skill files")
			return
		}
		fileResps = make([]SkillFileResponse, 0, len(req.Files))
		for _, f := range req.Files {
			// SKILL.md is reserved for the primary skill content (skill.Content).
			if skillpkg.IsReservedContentPath(f.Path) {
				continue
			}
			sf, err := qtx.UpsertSkillFile(r.Context(), db.UpsertSkillFileParams{
				SkillID: skill.ID,
				Path:    sanitizePostgresText(f.Path),
				Content: sanitizePostgresText(f.Content),
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to upsert skill file: "+err.Error())
				return
			}
			fileResps = append(fileResps, skillFileToResponse(sf))
		}
	} else {
		files, err := qtx.ListSkillFiles(r.Context(), skill.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list skill files")
			return
		}
		fileResps = make([]SkillFileResponse, len(files))
		for i, f := range files {
			fileResps[i] = skillFileToResponse(f)
		}
	}

	skillResp, err := skillToResponse(skill)
	if err != nil {
		writeSkillConfigDecodeError(w, r, id, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit")
		return
	}

	resp := SkillWithFilesResponse{
		SkillResponse: skillResp,
		Files:         fileResps,
	}
	wsID := h.resolveWorkspaceID(r)
	actorType, actorID := resolveActor(r, requestUserID(r))
	h.publish(protocol.EventSkillUpdated, wsID, actorType, actorID, map[string]any{"skill": resp})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) DeleteSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageSkill(w, r, skill) {
		return
	}

	if err := h.Queries.DeleteSkill(r.Context(), db.DeleteSkillParams{
		ID:          skill.ID,
		WorkspaceID: skill.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete skill")
		return
	}
	actorType, actorID := resolveActor(r, requestUserID(r))
	h.publish(protocol.EventSkillDeleted, uuidToString(skill.WorkspaceID), actorType, actorID, map[string]any{"skill_id": uuidToString(skill.ID)})
	w.WriteHeader(http.StatusNoContent)
}

// --- Skill import ---

type ImportSkillRequest struct {
	URL        string `json:"url"`
	OnConflict string `json:"on_conflict,omitempty"`
}

const (
	importOnConflictFail      = "fail"
	importOnConflictOverwrite = "overwrite"
	importOnConflictRename    = "rename"
	importOnConflictSkip      = "skip"
)

const maxImportRenameAttempts = 50
