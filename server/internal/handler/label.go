package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type LabelResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func labelToResponse(l db.IssueLabel) LabelResponse {
	return LabelResponse{
		ID:          uuidToString(l.ID),
		WorkspaceID: uuidToString(l.WorkspaceID),
		Name:        l.Name,
		Color:       l.Color,
		CreatedAt:   timestampToString(l.CreatedAt),
		UpdatedAt:   timestampToString(l.UpdatedAt),
	}
}

func labelsToResponse(list []db.IssueLabel) []LabelResponse {
	out := make([]LabelResponse, len(list))
	for i, l := range list {
		out[i] = labelToResponse(l)
	}
	return out
}

// 6-digit hex, with or without leading '#'.
var hexColorRE = regexp.MustCompile(`^#?[0-9a-fA-F]{6}$`)

// normalizeColor returns a canonical "#rrggbb" form or an error if invalid.
//
// LOAD-BEARING INVARIANT: LabelChip renders `style={{ backgroundColor: color }}`
// directly in the frontend. If this regex is ever relaxed to accept arbitrary
// CSS (named colors, `url(...)`, etc.), that inline style becomes an injection
// surface. Keep the regex strict.
func normalizeColor(c string) (string, error) {
	c = strings.TrimSpace(c)
	if !hexColorRE.MatchString(c) {
		return "", errors.New("color must be a 6-digit hex value like #3b82f6")
	}
	if !strings.HasPrefix(c, "#") {
		c = "#" + c
	}
	return strings.ToLower(c), nil
}

const maxLabelNameLen = 32

// validateLabelName trims and validates a label name. Returns the trimmed
// name or an error suitable for a 400 response.
func validateLabelName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("name is required")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", errors.New("name must not contain control characters")
	}
	if utf8.RuneCountInString(name) > maxLabelNameLen {
		return "", errors.New("name must be 32 characters or fewer")
	}
	return name, nil
}

type labelRouteScope struct {
	id          pgtype.UUID
	workspace   pgtype.UUID
	workspaceID string
}

func (h *Handler) parseLabelRouteScope(w http.ResponseWriter, r *http.Request) (labelRouteScope, bool) {
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "label id")
	if !ok {
		return labelRouteScope{}, false
	}
	workspaceID := h.resolveWorkspaceID(r)
	workspace, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	return labelRouteScope{id: id, workspace: workspace, workspaceID: workspaceID}, ok
}

// ---------------------------------------------------------------------------
// Handlers — label CRUD
// ---------------------------------------------------------------------------

func (h *Handler) ListLabels(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	labels, err := h.Queries.ListLabels(r.Context(), parseUUID(workspaceID))
	if err != nil {
		slog.Warn("ListLabels failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list labels")
		return
	}
	resp := labelsToResponse(labels)
	writeJSON(w, http.StatusOK, map[string]any{"labels": resp, "total": len(resp)})
}

func (h *Handler) GetLabel(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.parseLabelRouteScope(w, r)
	if !ok {
		return
	}
	label, err := h.Queries.GetLabel(r.Context(), db.GetLabelParams{
		ID: scope.id, WorkspaceID: scope.workspace,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "label not found")
			return
		}
		slog.Warn("GetLabel failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get label")
		return
	}
	writeJSON(w, http.StatusOK, labelToResponse(label))
}

func (h *Handler) CreateLabel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name, err := validateLabelName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	color, err := normalizeColor(req.Color)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceUUID := parseUUID(workspaceID)
	actorID := parseUUID(userID)
	req.Name = name
	req.Color = color
	requestHash, err := hashRequestFingerprint(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create label")
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	writeReplayError := resourceCreateReplayMessageErrorWriter(
		"Idempotency-Key was already used with a different label request",
		"failed to replay label create",
	)
	loadReplay := func() (LabelResponse, bool, error) {
		return loadResourceCreateReplay(
			r.Context(), h.Queries, workspaceUUID, actorID, resourceTypeLabel,
			idempotencyKey, requestHash, func(response LabelResponse) bool { return response.ID != "" },
		)
	}
	if handleResourceCreateReplay(w, http.StatusCreated, loadReplay, writeReplayError) {
		return
	}

	tx, qtx, ok := h.beginResourceCreateTransaction(w, r.Context(), "failed to create label")
	if !ok {
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	err = reserveResourceCreateRequest(r.Context(), qtx, workspaceUUID, actorID, resourceTypeLabel, idempotencyKey, requestHash)
	if !handleResourceCreateReservation(
		w, r.Context(), tx, err, loadReplay,
		writeReplayError,
		"failed to create label",
		http.StatusCreated,
	) {
		return
	}

	label, err := qtx.CreateLabel(r.Context(), db.CreateLabelParams{
		WorkspaceID: workspaceUUID,
		Name:        name,
		Color:       color,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a label with that name already exists")
			return
		}
		slog.Warn("CreateLabel failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create label")
		return
	}
	resp := labelToResponse(label)
	if err := completeResourceCreateRequest(
		r.Context(), qtx, workspaceUUID, actorID, resourceTypeLabel,
		idempotencyKey, requestHash, label.ID, resp,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create label")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create label")
		return
	}
	h.publish(protocol.EventLabelCreated, workspaceID, "member", userID, map[string]any{"label": resp})
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) UpdateLabel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  *string `json:"name"`
		Color *string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	scope, ok := h.parseLabelRouteScope(w, r)
	if !ok {
		return
	}
	params := db.UpdateLabelParams{
		ID:          scope.id,
		WorkspaceID: scope.workspace,
	}
	if req.Name != nil {
		name, err := validateLabelName(*req.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.Name = pgtype.Text{String: name, Valid: true}
	}
	if req.Color != nil {
		color, err := normalizeColor(*req.Color)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.Color = pgtype.Text{String: color, Valid: true}
	}

	// Branch on pgx.ErrNoRows directly from the UPDATE — the WHERE clause
	// already enforces (id, workspace_id), so a missing row means either the
	// label doesn't exist or it's not in this workspace. Dropping the prior
	// GetLabel precheck removes a TOCTOU window and saves a round-trip.
	label, err := h.Queries.UpdateLabel(r.Context(), params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "label not found")
			return
		}
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a label with that name already exists")
			return
		}
		slog.Warn("UpdateLabel failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update label")
		return
	}
	resp := labelToResponse(label)
	h.publish(protocol.EventLabelUpdated, scope.workspaceID, "member", userID, map[string]any{"label": resp})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) DeleteLabel(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	scope, ok := h.parseLabelRouteScope(w, r)
	if !ok {
		return
	}
	// DeleteLabel is :one RETURNING id — ErrNoRows means the label wasn't in
	// this workspace (404). Any other error is a real 500.
	if _, err := h.Queries.DeleteLabel(r.Context(), db.DeleteLabelParams{
		ID: scope.id, WorkspaceID: scope.workspace,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "label not found")
			return
		}
		slog.Warn("DeleteLabel failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to delete label")
		return
	}
	h.publish(protocol.EventLabelDeleted, scope.workspaceID, "member", userID, map[string]any{"label_id": uuidToString(scope.id)})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Handlers — issue↔label attach/detach
// ---------------------------------------------------------------------------

// ListLabelsForIssue returns the labels currently attached to an issue.
func (h *Handler) ListLabelsForIssue(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	// Authorize via the issue — if it's not in this workspace, the caller
	// shouldn't see its labels.
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	labels, err := h.Queries.ListLabelsByIssue(r.Context(), db.ListLabelsByIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("ListLabelsForIssue failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list labels")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": labelsToResponse(labels)})
}

type issueLabelMutation func(context.Context, *db.Queries, pgtype.UUID, pgtype.UUID, pgtype.UUID) error

func (h *Handler) mutateIssueLabel(
	w http.ResponseWriter,
	r *http.Request,
	issueID string,
	rawLabelID string,
	labelField string,
	userID string,
	action string,
	transaction string,
	mutate issueLabelMutation,
) {
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	labelID, ok := parseUUIDOrBadRequest(w, rawLabelID, labelField)
	if !ok {
		return
	}
	if _, err := h.Queries.GetLabel(r.Context(), db.GetLabelParams{
		ID: labelID, WorkspaceID: issue.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "label not found")
			return
		}
		slog.Warn("GetLabel before issue label mutation failed", append(logger.RequestAttrs(r), "action", action, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to "+action+" label")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin label "+transaction)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	if err := mutate(r.Context(), qtx, issue.ID, labelID, issue.WorkspaceID); err != nil {
		slog.Warn("Issue label mutation failed", append(logger.RequestAttrs(r), "action", action, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to "+action+" label")
		return
	}

	labels, err := qtx.ListLabelsByIssue(r.Context(), db.ListLabelsByIssueParams{
		IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("ListLabelsByIssue failed after mutation", append(logger.RequestAttrs(r), "action", action, "error", err, "issue_id", uuidToString(issue.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to "+action+" label")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit label "+transaction)
		return
	}
	resp := labelsToResponse(labels)
	h.publish(protocol.EventIssueLabelsChanged, uuidToString(issue.WorkspaceID), "member", userID, map[string]any{
		"issue_id": uuidToString(issue.ID),
		"labels":   resp,
	})
	writeJSON(w, http.StatusOK, map[string]any{"labels": resp})
}

// AttachLabel attaches a label to an issue.
func (h *Handler) AttachLabel(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req struct {
		LabelID string `json:"label_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.LabelID == "" {
		writeError(w, http.StatusBadRequest, "label_id is required")
		return
	}

	h.mutateIssueLabel(w, r, issueID, req.LabelID, "label_id", userID, "attach", "attachment", func(
		ctx context.Context,
		queries *db.Queries,
		issueID, labelID, workspaceID pgtype.UUID,
	) error {
		return queries.AttachLabelToIssue(ctx, db.AttachLabelToIssueParams{
			IssueID: issueID, LabelID: labelID, WorkspaceID: workspaceID,
		})
	})
}

// DetachLabel removes a label from an issue.
func (h *Handler) DetachLabel(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	labelID := chi.URLParam(r, "labelId")
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	h.mutateIssueLabel(w, r, issueID, labelID, "label id", userID, "detach", "detachment", func(
		ctx context.Context,
		queries *db.Queries,
		issueID, labelID, workspaceID pgtype.UUID,
	) error {
		return queries.DetachLabelFromIssue(ctx, db.DetachLabelFromIssueParams{
			IssueID: issueID, LabelID: labelID, WorkspaceID: workspaceID,
		})
	})
}
