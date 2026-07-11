package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) CreateAutopilotTrigger(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)

	ap, ok := h.loadAutopilotInWorkspace(w, r, autopilotID, workspaceID)
	if !ok {
		return
	}

	var req CreateAutopilotTriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	prepared, ok := prepareAutopilotTrigger(w, req)
	if !ok {
		return
	}
	trigger, err := createPreparedAutopilotTrigger(
		r.Context(),
		h.Queries,
		ap.ID,
		prepared,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create trigger")
		return
	}

	resp := h.triggerToResponse(trigger)
	userID, _ := requireUserID(w, r)
	h.publish(protocol.EventAutopilotUpdated, workspaceID, "member", userID, map[string]any{
		"autopilot_id": uuidToString(ap.ID),
		"trigger":      resp,
	})
	writeJSON(w, http.StatusCreated, resp)
}

type preparedAutopilotTrigger struct {
	kind           string
	cronExpression pgtype.Text
	timezone       pgtype.Text
	nextRunAt      pgtype.Timestamptz
	label          pgtype.Text
	provider       string
	eventFilters   []byte
}

func prepareAutopilotTrigger(
	w http.ResponseWriter,
	req CreateAutopilotTriggerRequest,
) (preparedAutopilotTrigger, bool) {
	if req.Kind == "" {
		writeError(w, http.StatusBadRequest, "kind is required")
		return preparedAutopilotTrigger{}, false
	}
	if req.Kind != "schedule" && req.Kind != "webhook" {
		writeError(w, http.StatusBadRequest, "kind must be schedule or webhook")
		return preparedAutopilotTrigger{}, false
	}
	if req.Kind == "schedule" && (req.CronExpression == nil || *req.CronExpression == "") {
		writeError(w, http.StatusBadRequest, "cron_expression is required for schedule triggers")
		return preparedAutopilotTrigger{}, false
	}
	if req.Kind == "webhook" && req.Timezone != nil && *req.Timezone != "" {
		// Webhook triggers fire on demand from external POSTs — they have no
		// next_run_at to compute, so a timezone is meaningless. Reject loudly
		// instead of silently dropping the field.
		writeError(w, http.StatusBadRequest, "timezone is not valid for webhook triggers")
		return preparedAutopilotTrigger{}, false
	}
	if req.Kind != "webhook" && len(req.EventFilters) > 0 {
		// event_filters narrows webhook ingress — it has no meaning for a
		// schedule trigger and would otherwise be silently dropped.
		writeError(w, http.StatusBadRequest, "event_filters is only valid for webhook triggers")
		return preparedAutopilotTrigger{}, false
	}
	if err := validateWebhookEventFilters(req.EventFilters); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return preparedAutopilotTrigger{}, false
	}
	// Provider only applies to webhook triggers and the value space is
	// closed — reject unknowns early so a typo on create doesn't quietly
	// degrade into a "generic" trigger that bypasses provider-specific
	// dedupe / signature behaviour.
	provider := "generic"
	if req.Provider != nil && *req.Provider != "" {
		if req.Kind != "webhook" {
			writeError(w, http.StatusBadRequest, "provider is only valid for webhook triggers")
			return preparedAutopilotTrigger{}, false
		}
		if !isAllowedWebhookProvider(*req.Provider) {
			writeError(w, http.StatusBadRequest, "provider must be generic or github")
			return preparedAutopilotTrigger{}, false
		}
		provider = *req.Provider
	}

	if req.Timezone != nil && *req.Timezone != "" {
		if err := service.ValidateTimezone(*req.Timezone); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return preparedAutopilotTrigger{}, false
		}
	}

	prepared := preparedAutopilotTrigger{
		kind:     req.Kind,
		label:    ptrToText(req.Label),
		provider: provider,
	}
	switch req.Kind {
	case "schedule":
		prepared.cronExpression = ptrToText(req.CronExpression)
		prepared.timezone = ptrToText(req.Timezone)
		tz := "UTC"
		if req.Timezone != nil && *req.Timezone != "" {
			tz = *req.Timezone
		}
		t, err := computeNextRun(*req.CronExpression, tz)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return preparedAutopilotTrigger{}, false
		}
		prepared.nextRunAt = pgtype.Timestamptz{Time: t, Valid: true}
	case "webhook":
		eventFiltersBytes, err := encodeWebhookEventFilters(req.EventFilters)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode event_filters")
			return preparedAutopilotTrigger{}, false
		}
		prepared.eventFilters = eventFiltersBytes
	}
	return prepared, true
}

func createPreparedAutopilotTrigger(
	ctx context.Context,
	queries *db.Queries,
	autopilotID pgtype.UUID,
	prepared preparedAutopilotTrigger,
) (db.AutopilotTrigger, error) {
	if prepared.kind == "webhook" {
		return createWebhookTriggerWithMintedToken(ctx, queries, autopilotID, prepared)
	}
	return queries.CreateAutopilotTrigger(ctx, db.CreateAutopilotTriggerParams{
		AutopilotID:    autopilotID,
		Kind:           prepared.kind,
		Enabled:        true,
		CronExpression: prepared.cronExpression,
		Timezone:       prepared.timezone,
		NextRunAt:      prepared.nextRunAt,
		Label:          prepared.label,
	})
}

// createWebhookTriggerWithMintedToken atomically creates a webhook trigger
// with a freshly minted bearer token in the same INSERT. Avoids the older
// two-step (INSERT then UPDATE webhook_token) pattern which could leave a
// kind=webhook row with NULL webhook_token visible in the UI if the second
// statement failed.
//
// Retries on the unique-index collision case so a vanishingly-rare RNG
// collision turns into a clean retry rather than a 500.
func createWebhookTriggerWithMintedToken(
	ctx context.Context,
	queries *db.Queries,
	autopilotID pgtype.UUID,
	prepared preparedAutopilotTrigger,
) (db.AutopilotTrigger, error) {
	for attempt := 0; attempt < 3; attempt++ {
		token, err := generateWebhookToken()
		if err != nil {
			return db.AutopilotTrigger{}, err
		}
		trigger, err := queries.CreateAutopilotTrigger(ctx, db.CreateAutopilotTriggerParams{
			AutopilotID:  autopilotID,
			Kind:         "webhook",
			Enabled:      true,
			Label:        prepared.label,
			WebhookToken: pgtype.Text{String: token, Valid: true},
			Provider:     pgtype.Text{String: prepared.provider, Valid: prepared.provider != ""},
			EventFilters: prepared.eventFilters,
		})
		if err == nil {
			return trigger, nil
		}
		if !isUniqueViolation(err) {
			return db.AutopilotTrigger{}, err
		}
	}
	return db.AutopilotTrigger{}, fmt.Errorf("could not mint unique webhook token")
}

func isAllowedWebhookProvider(p string) bool {
	switch p {
	case "generic", "github":
		return true
	default:
		return false
	}
}

func isValidAutopilotAssigneeType(t string) bool {
	switch t {
	case "agent", "squad":
		return true
	default:
		return false
	}
}

// validateAutopilotAssignee checks that the assignee (agent or squad) exists
// in the given workspace, and for squad assignees that the squad's leader
// agent is in a workable state at create / update time. Writes an HTTP error
// and returns false on any failure.
//
// At dispatch time the same checks (resolveAutopilotLeader + AgentReadiness)
// run again — they live there to handle "leader was online at save time but
// went offline by trigger time". Save-time validation exists so the user gets
// immediate feedback ("can't pick this squad because its leader is archived")
// instead of discovering the autopilot is dead at the next schedule tick.
func (h *Handler) validateAutopilotAssignee(w http.ResponseWriter, r *http.Request, assigneeType string, assigneeID, workspaceID pgtype.UUID) bool {
	switch assigneeType {
	case "agent":
		if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID:          assigneeID,
			WorkspaceID: workspaceID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "assignee must be a valid agent in this workspace")
			return false
		}
		return true
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
			ID:          assigneeID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "assignee must be a valid squad in this workspace")
			return false
		}
		// Archived squads must be rejected at save time: the dispatcher will
		// otherwise produce an unbroken stream of skipped runs against a
		// squad that can never be revived without an explicit un-archive.
		// Pair with TransferSquadAutopilotsToLeader on DeleteSquad so any
		// autopilot that survives the archive flips to assignee_type='agent'
		// (the leader) and stops referencing the dead squad row.
		if squad.ArchivedAt.Valid {
			writeError(w, http.StatusUnprocessableEntity, "squad is archived; pick a different squad")
			return false
		}
		actorType, actorID := h.resolveActor(r, requestUserID(r), util.UUIDToString(workspaceID))
		if !h.canUseSquad(r.Context(), squad, actorType, actorID, util.UUIDToString(workspaceID)) {
			writeError(w, http.StatusForbidden, "cannot assign autopilot to personal squad")
			return false
		}
		leader, err := h.Queries.GetAgent(r.Context(), squad.LeaderID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "squad leader agent not found")
			return false
		}
		if leader.ArchivedAt.Valid {
			writeError(w, http.StatusUnprocessableEntity, "squad leader is archived; pick a different squad or rotate the leader before assigning autopilot")
			return false
		}
		// Private-leader gate: the member configuring the autopilot must have
		// access to the personal leader, same as validateAssigneePair.
		if !h.canAccessPersonalAgent(r.Context(), leader, actorType, actorID, util.UUIDToString(workspaceID)) {
			writeError(w, http.StatusForbidden, "cannot assign autopilot to squad with personal leader")
			return false
		}
		return true
	default:
		writeError(w, http.StatusBadRequest, "assignee_type must be agent or squad")
		return false
	}
}

func (h *Handler) UpdateAutopilotTrigger(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	triggerID := chi.URLParam(r, "triggerId")
	workspaceID := h.resolveWorkspaceID(r)

	ap, ok := h.loadAutopilotInWorkspace(w, r, autopilotID, workspaceID)
	if !ok {
		return
	}

	triggerUUID, ok := parseUUIDOrBadRequest(w, triggerID, "trigger id")
	if !ok {
		return
	}

	prev, err := h.Queries.GetAutopilotTrigger(r.Context(), triggerUUID)
	if err != nil || uuidToString(prev.AutopilotID) != uuidToString(ap.ID) {
		writeError(w, http.StatusNotFound, "trigger not found")
		return
	}

	var req UpdateAutopilotTriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Kind-specific validation. Mirrors the create-path discipline: cron
	// and timezone only make sense on schedule triggers, so reject loudly
	// rather than persisting fields that no code path reads. enabled and
	// label remain valid on every kind.
	if prev.Kind != "schedule" {
		if req.CronExpression != nil {
			writeError(w, http.StatusBadRequest, "cron_expression is only valid for schedule triggers")
			return
		}
		if req.Timezone != nil {
			writeError(w, http.StatusBadRequest, "timezone is only valid for schedule triggers")
			return
		}
	}

	params := db.UpdateAutopilotTriggerParams{
		ID:             prev.ID,
		CronExpression: prev.CronExpression,
		Timezone:       prev.Timezone,
		NextRunAt:      prev.NextRunAt,
		Label:          prev.Label,
	}
	if req.Enabled != nil {
		params.Enabled = pgtype.Bool{Bool: *req.Enabled, Valid: true}
	}
	if req.CronExpression != nil {
		params.CronExpression = pgtype.Text{String: *req.CronExpression, Valid: true}
	}
	if req.Timezone != nil {
		if *req.Timezone != "" {
			if err := service.ValidateTimezone(*req.Timezone); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		params.Timezone = pgtype.Text{String: *req.Timezone, Valid: true}
	}
	if req.Label != nil {
		params.Label = pgtype.Text{String: *req.Label, Valid: true}
	}
	// Tri-state PATCH for event_filters. A nil pointer (field omitted or
	// JSON null) leaves the existing row untouched — params.EventFilters
	// stays unset and the COALESCE in the UPDATE preserves the previous
	// value. A non-nil pointer is authoritative: an empty slice clears
	// filters (encoded as the JSONB literal `[]` so COALESCE replaces
	// rather than preserves), a populated slice replaces.
	if req.EventFilters != nil {
		if prev.Kind != "webhook" {
			writeError(w, http.StatusBadRequest, "event_filters is only valid for webhook triggers")
			return
		}
		if err := validateWebhookEventFilters(*req.EventFilters); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		encoded, err := encodeWebhookEventFiltersAlways(*req.EventFilters)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode event_filters")
			return
		}
		params.EventFilters = encoded
	}

	// Recompute next_run_at if cron or timezone changed.
	cronExpr := prev.CronExpression.String
	if req.CronExpression != nil {
		cronExpr = *req.CronExpression
	}
	tz := "UTC"
	if prev.Timezone.Valid {
		tz = prev.Timezone.String
	}
	if req.Timezone != nil {
		tz = *req.Timezone
	}
	if prev.Kind == "schedule" && cronExpr != "" {
		t, err := computeNextRun(cronExpr, tz)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.NextRunAt = pgtype.Timestamptz{Time: t, Valid: true}
	}

	trigger, err := h.Queries.UpdateAutopilotTrigger(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update trigger")
		return
	}

	resp := h.triggerToResponse(trigger)
	userID, _ := requireUserID(w, r)
	h.publish(protocol.EventAutopilotUpdated, workspaceID, "member", userID, map[string]any{
		"autopilot_id": uuidToString(ap.ID),
		"trigger":      resp,
	})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) DeleteAutopilotTrigger(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	triggerID := chi.URLParam(r, "triggerId")
	workspaceID := h.resolveWorkspaceID(r)

	autopilotUUID, ok := parseUUIDOrBadRequest(w, autopilotID, "autopilot id")
	if !ok {
		return
	}
	triggerUUID, ok := parseUUIDOrBadRequest(w, triggerID, "trigger id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	if _, err := h.Queries.GetAutopilotInWorkspace(r.Context(), db.GetAutopilotInWorkspaceParams{
		ID:          autopilotUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "autopilot not found")
		return
	}

	trigger, err := h.Queries.GetAutopilotTrigger(r.Context(), triggerUUID)
	if err != nil || uuidToString(trigger.AutopilotID) != uuidToString(autopilotUUID) {
		writeError(w, http.StatusNotFound, "trigger not found")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	if err := h.Queries.DeleteAutopilotTrigger(r.Context(), triggerUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete trigger")
		return
	}

	h.publish(protocol.EventAutopilotUpdated, workspaceID, "member", userID, map[string]any{
		"autopilot_id": uuidToString(autopilotUUID),
		"trigger_id":   uuidToString(triggerUUID),
	})
	w.WriteHeader(http.StatusNoContent)
}

// RotateAutopilotTriggerWebhookToken issues a fresh bearer token for an
// existing webhook trigger. The old token stops working immediately because
// the unique-index lookup in the public ingress route is keyed on the
// current row value.
func (h *Handler) RotateAutopilotTriggerWebhookToken(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	triggerID := chi.URLParam(r, "triggerId")
	workspaceID := h.resolveWorkspaceID(r)

	ap, ok := h.loadAutopilotInWorkspace(w, r, autopilotID, workspaceID)
	if !ok {
		return
	}

	triggerUUID, ok := parseUUIDOrBadRequest(w, triggerID, "trigger id")
	if !ok {
		return
	}
	prev, err := h.Queries.GetAutopilotTrigger(r.Context(), triggerUUID)
	if err != nil || uuidToString(prev.AutopilotID) != uuidToString(ap.ID) {
		writeError(w, http.StatusNotFound, "trigger not found")
		return
	}
	if prev.Kind != "webhook" {
		writeError(w, http.StatusBadRequest, "trigger is not a webhook trigger")
		return
	}

	var rotated db.AutopilotTrigger
	for attempt := 0; attempt < 3; attempt++ {
		token, terr := generateWebhookToken()
		if terr != nil {
			writeError(w, http.StatusInternalServerError, "failed to generate webhook token")
			return
		}
		rotated, err = h.Queries.RotateAutopilotTriggerWebhookToken(r.Context(), db.RotateAutopilotTriggerWebhookTokenParams{
			ID:           triggerUUID,
			WebhookToken: pgtype.Text{String: token, Valid: true},
		})
		if err == nil {
			break
		}
		if !isUniqueViolation(err) {
			writeError(w, http.StatusInternalServerError, "failed to rotate webhook token")
			return
		}
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to rotate webhook token")
		return
	}

	resp := h.triggerToResponse(rotated)
	userID, _ := requireUserID(w, r)
	h.publish(protocol.EventAutopilotUpdated, workspaceID, "member", userID, map[string]any{
		"autopilot_id": uuidToString(ap.ID),
		"trigger":      resp,
	})
	writeJSON(w, http.StatusOK, resp)
}

// SetAutopilotTriggerSigningSecret sets (or clears) the HMAC signing secret
// for a webhook trigger. Lives on its own endpoint so the secret value never
// shares a request body with any other field — keeping it out of generic
// request-body logs and audit captures that may include patch payloads.
//
// Empty body / empty `signing_secret` clears the secret and reverts the
// trigger to bearer-token-only authentication. The response carries
// `has_signing_secret` + `signing_secret_hint`; the secret itself is never
// echoed back, matching the GitHub / Stripe industry pattern.
func (h *Handler) SetAutopilotTriggerSigningSecret(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	triggerID := chi.URLParam(r, "triggerId")
	workspaceID := h.resolveWorkspaceID(r)

	ap, ok := h.loadAutopilotInWorkspace(w, r, autopilotID, workspaceID)
	if !ok {
		return
	}
	triggerUUID, ok := parseUUIDOrBadRequest(w, triggerID, "trigger id")
	if !ok {
		return
	}
	prev, err := h.Queries.GetAutopilotTrigger(r.Context(), triggerUUID)
	if err != nil || uuidToString(prev.AutopilotID) != uuidToString(ap.ID) {
		writeError(w, http.StatusNotFound, "trigger not found")
		return
	}
	if prev.Kind != "webhook" {
		writeError(w, http.StatusBadRequest, "trigger is not a webhook trigger")
		return
	}

	var req SetSigningSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	secret := strings.TrimSpace(req.SigningSecret)
	// 16 chars is the floor: enough to make brute force impractical for the
	// SHA-256 HMAC but low enough not to reject providers that mint shorter
	// keys (Slack signing secrets are 32 hex chars; GitHub recommends 32).
	if secret != "" && len(secret) < 16 {
		writeError(w, http.StatusBadRequest, "signing_secret must be at least 16 characters")
		return
	}

	param := db.SetAutopilotTriggerSigningSecretParams{ID: triggerUUID}
	if secret != "" {
		param.SigningSecret = pgtype.Text{String: secret, Valid: true}
	}
	updated, err := h.Queries.SetAutopilotTriggerSigningSecret(r.Context(), param)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update signing secret")
		return
	}

	resp := h.triggerToResponse(updated)
	userID, _ := requireUserID(w, r)
	// Publish the trigger update so the UI can refresh the has_signing_secret
	// badge in real time. The event payload only carries the response shape,
	// which excludes the secret.
	h.publish(protocol.EventAutopilotUpdated, workspaceID, "member", userID, map[string]any{
		"autopilot_id": uuidToString(ap.ID),
		"trigger":      resp,
	})
	writeJSON(w, http.StatusOK, resp)
}

// ── Runs ────────────────────────────────────────────────────────────────────

func (h *Handler) ListAutopilotRuns(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)

	autopilot, ok := h.loadAutopilotInWorkspace(w, r, autopilotID, workspaceID)
	if !ok {
		return
	}

	limit := int32(20)
	offset := int32(0)
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = int32(v)
		}
	}
	if limit > 100 {
		limit = 100
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = int32(v)
		}
	}

	runs, err := h.Queries.ListAutopilotRuns(r.Context(), db.ListAutopilotRunsParams{
		AutopilotID: autopilot.ID,
		Limit:       limit,
		Offset:      offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runs")
		return
	}

	resp := make([]AutopilotRunResponse, len(runs))
	for i, run := range runs {
		// Omit trigger_payload in the list response — a webhook envelope
		// can be up to 256 KiB and `limit` defaults to 20, so the full
		// list would be a ~5 MB worst case. Detail dialog fetches the
		// full payload from GetAutopilotRun.
		resp[i] = runToResponseSlim(run)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": resp, "total": len(resp)})
}

// GetAutopilotRun returns a single run including its full trigger_payload.
// Workspace scoping is enforced via loadAutopilotInWorkspace; the run is
// then re-checked to belong to that autopilot so a guessed runId from
// another workspace cannot leak data.
func (h *Handler) GetAutopilotRun(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	runID := chi.URLParam(r, "runId")
	workspaceID := h.resolveWorkspaceID(r)

	autopilot, ok := h.loadAutopilotInWorkspace(w, r, autopilotID, workspaceID)
	if !ok {
		return
	}

	runUUID, ok := parseUUIDOrBadRequest(w, runID, "run id")
	if !ok {
		return
	}

	run, err := h.Queries.GetAutopilotRun(r.Context(), runUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if uuidToString(run.AutopilotID) != uuidToString(autopilot.ID) {
		// Guard against a runId from another autopilot being requested via
		// this autopilot's URL — fail closed with 404 so the response shape
		// matches the "not found" case and no information is leaked.
		writeError(w, http.StatusNotFound, "run not found")
		return
	}

	writeJSON(w, http.StatusOK, runToResponse(run))
}

// ── Manual trigger ──────────────────────────────────────────────────────────

func (h *Handler) TriggerAutopilot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}

	autopilot, ok := h.loadAutopilotInWorkspace(w, r, id, workspaceID)
	if !ok {
		return
	}
	if autopilot.Status != "active" {
		writeError(w, http.StatusBadRequest, "autopilot is not active")
		return
	}

	run, err := h.AutopilotService.DispatchAutopilotOnce(
		r.Context(),
		autopilot,
		pgtype.UUID{},
		"manual",
		nil,
		idempotencyKey,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to trigger autopilot: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, runToResponse(*run))
}
