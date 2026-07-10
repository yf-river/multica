package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// computeNextRun delegates to the shared cron helper in the service package.
func computeNextRun(cronExpr, timezone string) (time.Time, error) {
	return service.ComputeNextRun(cronExpr, timezone)
}

// ── Response types ──────────────────────────────────────────────────────────

type AutopilotResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	ProjectID   *string `json:"project_id"`
	// AssigneeType is "agent" or "squad". Path A from MUL-2429: when set
	// to "squad", AssigneeID points at squad(id) rather than agent(id) and
	// dispatch resolves to squad.leader_id at run time.
	AssigneeType       string  `json:"assignee_type"`
	AssigneeID         string  `json:"assignee_id"`
	Status             string  `json:"status"`
	ExecutionMode      string  `json:"execution_mode"`
	IssueTitleTemplate *string `json:"issue_title_template"`
	CreatedByType      string  `json:"created_by_type"`
	CreatedByID        string  `json:"created_by_id"`
	LastRunAt          *string `json:"last_run_at"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`

	// List-endpoint-only derived fields (absent on the detail/create/update
	// responses and on older servers — clients must treat them as optional).
	// Enabled triggers only; last_run_status is the most recent run's status.
	TriggerKinds  []string `json:"trigger_kinds,omitempty"`
	NextRunAt     *string  `json:"next_run_at,omitempty"`
	LastRunStatus *string  `json:"last_run_status,omitempty"`

	// Always non-nil (empty slice when no subscribers configured) so
	// frontend optional-chain rules can treat the field as authoritative.
	Subscribers []AutopilotSubscriberEntry `json:"subscribers"`
}

// user_type is restricted to "member" at the DB layer; the field is kept on
// the wire so a future expansion to agents/squads is additive, not breaking.
type AutopilotSubscriberEntry struct {
	UserType  string `json:"user_type"`
	UserID    string `json:"user_id"`
	CreatedAt string `json:"created_at"`
}

type AutopilotTriggerResponse struct {
	ID             string  `json:"id"`
	AutopilotID    string  `json:"autopilot_id"`
	Kind           string  `json:"kind"`
	Enabled        bool    `json:"enabled"`
	CronExpression *string `json:"cron_expression"`
	Timezone       *string `json:"timezone"`
	NextRunAt      *string `json:"next_run_at"`
	WebhookToken   *string `json:"webhook_token"`
	// WebhookPath is computed from webhook_token. Always present for webhook
	// triggers; nil for schedule/api. Not stored — see triggerToResponse.
	WebhookPath *string `json:"webhook_path"`
	// WebhookURL is the absolute URL composed from the server's
	// MULTICA_PUBLIC_URL setting. Nil when the server has no public URL
	// configured; clients then build the URL themselves from webhook_path
	// plus their API base / current origin.
	WebhookURL *string `json:"webhook_url"`
	// Provider names the per-endpoint signing/dedupe convention. For now:
	// "generic" (bearer URL only, Idempotency-Key for dedupe) or "github"
	// (X-Hub-Signature-256 + X-GitHub-Delivery). Omitted for non-webhook
	// triggers.
	Provider *string `json:"provider"`
	// HasSigningSecret indicates whether a signing secret is configured on
	// the trigger. The secret itself is never returned — it is set via a
	// dedicated write-only endpoint. Always false for non-webhook triggers.
	HasSigningSecret bool `json:"has_signing_secret"`
	// SigningSecretHint is the last 4 characters of the configured secret,
	// surfaced to help operators tell two secrets apart in the UI. Nil when
	// no secret is configured.
	SigningSecretHint *string `json:"signing_secret_hint"`
	Label             *string `json:"label"`
	LastFiredAt       *string `json:"last_fired_at"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
	// EventFilters is the declared event scope. Only present for webhook
	// triggers; omitted when the trigger accepts all events. Serializes as
	// a JSON array of {event, actions?} objects — never as a base64 string
	// (which is what []byte would produce through encoding/json).
	EventFilters []WebhookEventFilter `json:"event_filters,omitempty"`
}

type AutopilotRunResponse struct {
	ID             string  `json:"id"`
	AutopilotID    string  `json:"autopilot_id"`
	TriggerID      *string `json:"trigger_id"`
	Source         string  `json:"source"`
	Status         string  `json:"status"`
	IssueID        *string `json:"issue_id"`
	TaskID         *string `json:"task_id"`
	TriggeredAt    string  `json:"triggered_at"`
	CompletedAt    *string `json:"completed_at"`
	FailureReason  *string `json:"failure_reason"`
	TriggerPayload any     `json:"trigger_payload"`
	Result         any     `json:"result"`
	CreatedAt      string  `json:"created_at"`
}

// ── Converters ──────────────────────────────────────────────────────────────

func autopilotToResponse(a db.Autopilot, subscribers []db.AutopilotSubscriber) AutopilotResponse {
	assigneeType := a.AssigneeType
	if assigneeType == "" {
		// Older rows pre-MUL-2429 may surface as "" against an out-of-date
		// schema view; default to "agent" so the API contract stays
		// non-null.
		assigneeType = "agent"
	}
	subResp := make([]AutopilotSubscriberEntry, len(subscribers))
	for i, s := range subscribers {
		subResp[i] = AutopilotSubscriberEntry{
			UserType:  s.UserType,
			UserID:    uuidToString(s.UserID),
			CreatedAt: timestampToString(s.CreatedAt),
		}
	}
	return AutopilotResponse{
		ID:                 uuidToString(a.ID),
		WorkspaceID:        uuidToString(a.WorkspaceID),
		Title:              a.Title,
		Description:        textToPtr(a.Description),
		ProjectID:          uuidToPtr(a.ProjectID),
		AssigneeType:       assigneeType,
		AssigneeID:         uuidToString(a.AssigneeID),
		Status:             a.Status,
		ExecutionMode:      a.ExecutionMode,
		IssueTitleTemplate: textToPtr(a.IssueTitleTemplate),
		CreatedByType:      a.CreatedByType,
		CreatedByID:        uuidToString(a.CreatedByID),
		LastRunAt:          timestampToPtr(a.LastRunAt),
		CreatedAt:          timestampToString(a.CreatedAt),
		UpdatedAt:          timestampToString(a.UpdatedAt),
		Subscribers:        subResp,
	}
}

func (h *Handler) triggerToResponse(t db.AutopilotTrigger) AutopilotTriggerResponse {
	resp := AutopilotTriggerResponse{
		ID:             uuidToString(t.ID),
		AutopilotID:    uuidToString(t.AutopilotID),
		Kind:           t.Kind,
		Enabled:        t.Enabled,
		CronExpression: textToPtr(t.CronExpression),
		Timezone:       textToPtr(t.Timezone),
		NextRunAt:      timestampToPtr(t.NextRunAt),
		WebhookToken:   textToPtr(t.WebhookToken),
		Label:          textToPtr(t.Label),
		LastFiredAt:    timestampToPtr(t.LastFiredAt),
		CreatedAt:      timestampToString(t.CreatedAt),
		UpdatedAt:      timestampToString(t.UpdatedAt),
	}
	if t.Kind == "webhook" && t.WebhookToken.Valid && t.WebhookToken.String != "" {
		path := webhookPathForToken(t.WebhookToken.String)
		resp.WebhookPath = &path
		if h.cfg.PublicURL != "" {
			full := h.cfg.PublicURL + path
			resp.WebhookURL = &full
		}
		provider := t.Provider
		if provider == "" {
			provider = "generic"
		}
		resp.Provider = &provider
		if t.SigningSecret.Valid && t.SigningSecret.String != "" {
			resp.HasSigningSecret = true
			hint := signingSecretHint(t.SigningSecret.String)
			resp.SigningSecretHint = &hint
		}
		if len(t.EventFilters) > 0 {
			var filters []WebhookEventFilter
			if err := json.Unmarshal(t.EventFilters, &filters); err == nil {
				resp.EventFilters = filters
			}
			// On unmarshal error we deliberately drop the field instead of
			// surfacing raw bytes or 500ing — strict write-time validation
			// is supposed to make this branch unreachable, and the matcher
			// fails closed if a corrupt row ever slips through.
		}
	}
	return resp
}

// signingSecretHint returns the last 4 characters of the signing secret so a
// configured-vs-rotated state is visible in the UI without exposing the
// secret itself. Truncating below 4 chars (which the validator already
// rejects) just returns an empty string.
func signingSecretHint(secret string) string {
	if len(secret) < 4 {
		return ""
	}
	return secret[len(secret)-4:]
}

// webhookPathForToken composes the path used by the public ingress route.
// Kept as a free function (no Handler receiver) so test code that builds
// expected URLs without instantiating a Handler can call it.
func webhookPathForToken(token string) string {
	return "/api/webhooks/autopilots/" + token
}

func runToResponse(r db.AutopilotRun) AutopilotRunResponse {
	var payload any
	if r.TriggerPayload != nil {
		json.Unmarshal(r.TriggerPayload, &payload)
	}
	var result any
	if r.Result != nil {
		json.Unmarshal(r.Result, &result)
	}
	return AutopilotRunResponse{
		ID:             uuidToString(r.ID),
		AutopilotID:    uuidToString(r.AutopilotID),
		TriggerID:      uuidToPtr(r.TriggerID),
		Source:         r.Source,
		Status:         r.Status,
		IssueID:        uuidToPtr(r.IssueID),
		TaskID:         uuidToPtr(r.TaskID),
		TriggeredAt:    timestampToString(r.TriggeredAt),
		CompletedAt:    timestampToPtr(r.CompletedAt),
		FailureReason:  textToPtr(r.FailureReason),
		TriggerPayload: payload,
		Result:         result,
		CreatedAt:      timestampToString(r.CreatedAt),
	}
}

// runToResponseSlim mirrors runToResponse but omits TriggerPayload, intended
// for list endpoints where echoing the full webhook envelope (up to
// 256 KiB × N rows) would dominate response size. Clients fetch the full
// payload via GET /api/autopilots/{id}/runs/{runId} when the user opens
// the run detail dialog.
func runToResponseSlim(r db.AutopilotRun) AutopilotRunResponse {
	resp := runToResponse(r)
	resp.TriggerPayload = nil
	return resp
}

// ── Request types ───────────────────────────────────────────────────────────

type CreateAutopilotRequest struct {
	Title       string  `json:"title"`
	Description *string `json:"description"`
	ProjectID   *string `json:"project_id"`
	// AssigneeType is optional and defaults to "agent" — preserves backward
	// compatibility with desktop clients shipped before MUL-2429.
	AssigneeType       *string           `json:"assignee_type"`
	AssigneeID         string            `json:"assignee_id"`
	ExecutionMode      string            `json:"execution_mode"`
	IssueTitleTemplate *string           `json:"issue_title_template"`
	Subscribers        []SubscriberInput `json:"subscribers"`
}

type UpdateAutopilotRequest struct {
	Title              *string `json:"title"`
	Description        *string `json:"description"`
	ProjectID          *string `json:"project_id"`
	AssigneeType       *string `json:"assignee_type"`
	AssigneeID         *string `json:"assignee_id"`
	Status             *string `json:"status"`
	ExecutionMode      *string `json:"execution_mode"`
	IssueTitleTemplate *string `json:"issue_title_template"`
	// Wholesale replacement when present; omit to leave subscribers untouched.
	Subscribers []SubscriberInput `json:"subscribers"`
}

type SubscriberInput struct {
	UserType string `json:"user_type"`
	UserID   string `json:"user_id"`
}

type CreateAutopilotTriggerRequest struct {
	Kind           string  `json:"kind"`
	CronExpression *string `json:"cron_expression"`
	Timezone       *string `json:"timezone"`
	Label          *string `json:"label"`
	// Provider is currently only meaningful for kind=webhook. Allowed
	// values: "generic" (default) or "github". Unset → "generic".
	Provider *string `json:"provider"`
	// EventFilters is an optional list of {event, actions?} scopes. Only
	// meaningful for webhook triggers. nil/empty means "accept all events".
	EventFilters []WebhookEventFilter `json:"event_filters,omitempty"`
}

// SetSigningSecretRequest is the body shape for PUT
// /api/autopilots/{id}/triggers/{triggerId}/signing-secret. Lives in its own
// type so the secret never appears alongside other fields on the trigger
// update path — handlers that log request bodies for debugging cannot pick it
// up by accident.
type SetSigningSecretRequest struct {
	// SigningSecret is the new HMAC key. Sending an empty string explicitly
	// clears the secret (disables signature verification). Pass any
	// reasonably entropic value — GitHub's docs recommend at least 32 random
	// characters; we enforce a 16-char minimum on non-empty input.
	SigningSecret string `json:"signing_secret"`
}

type UpdateAutopilotTriggerRequest struct {
	Enabled        *bool   `json:"enabled"`
	CronExpression *string `json:"cron_expression"`
	Timezone       *string `json:"timezone"`
	Label          *string `json:"label"`
	// EventFilters is the desired event-filter set with tri-state PATCH
	// semantics:
	//
	//   - omitted / explicit null (nil pointer) → leave the existing value
	//     untouched.
	//   - explicit [] (non-nil, length 0)       → clear filters (the trigger
	//     reverts to "accept all events").
	//   - explicit [...]                        → replace with the supplied
	//     list.
	//
	// This is why the pointer matters: with a plain []WebhookEventFilter
	// there is no way to tell "field absent from the PATCH body" from "field
	// present but empty", and the user can never clear filters once set.
	EventFilters *[]WebhookEventFilter `json:"event_filters,omitempty"`
}

// ── Handlers ────────────────────────────────────────────────────────────────

func (h *Handler) ListAutopilots(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)

	var statusFilter pgtype.Text
	if s := r.URL.Query().Get("status"); s != "" {
		statusFilter = pgtype.Text{String: s, Valid: true}
	}

	autopilots, err := h.Queries.ListAutopilots(r.Context(), db.ListAutopilotsParams{
		WorkspaceID: parseUUID(workspaceID),
		Status:      statusFilter,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list autopilots")
		return
	}

	resp := make([]AutopilotResponse, len(autopilots))
	for i, row := range autopilots {
		// Omit subscribers to avoid an N+1; GET /api/autopilots/{id} is
		// the source of truth for the populated template.
		r := autopilotToResponse(row.Autopilot, nil)
		r.TriggerKinds = row.TriggerKinds
		if row.NextRunAt.Valid {
			r.NextRunAt = timestampToPtr(row.NextRunAt)
		}
		if row.LastRunStatus != "" {
			s := row.LastRunStatus
			r.LastRunStatus = &s
		}
		resp[i] = r
	}
	writeJSON(w, http.StatusOK, map[string]any{"autopilots": resp, "total": len(resp)})
}

func (h *Handler) GetAutopilot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)

	autopilot, ok := h.loadAutopilotInWorkspace(w, r, id, workspaceID)
	if !ok {
		return
	}

	subs, err := h.Queries.ListAutopilotSubscribers(r.Context(), autopilot.ID)
	if err != nil {
		// Don't 500 the detail fetch over template metadata.
		subs = nil
	}
	resp := autopilotToResponse(autopilot, subs)

	// Include triggers.
	triggers, err := h.Queries.ListAutopilotTriggers(r.Context(), autopilot.ID)
	if err != nil {
		triggers = nil
	}
	triggerResp := make([]AutopilotTriggerResponse, len(triggers))
	for i, t := range triggers {
		triggerResp[i] = h.triggerToResponse(t)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"autopilot": resp,
		"triggers":  triggerResp,
	})
}

func (h *Handler) loadAutopilotInWorkspace(w http.ResponseWriter, r *http.Request, autopilotID, workspaceID string) (db.Autopilot, bool) {
	autopilotUUID, ok := parseUUIDOrBadRequest(w, autopilotID, "autopilot id")
	if !ok {
		return db.Autopilot{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.Autopilot{}, false
	}

	autopilot, err := h.Queries.GetAutopilotInWorkspace(r.Context(), db.GetAutopilotInWorkspaceParams{
		ID:          autopilotUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "autopilot not found")
		return db.Autopilot{}, false
	}
	return autopilot, true
}

func (h *Handler) CreateAutopilot(w http.ResponseWriter, r *http.Request) {
	var req CreateAutopilotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if req.AssigneeID == "" {
		writeError(w, http.StatusBadRequest, "assignee_id is required")
		return
	}
	if req.ExecutionMode == "" {
		writeError(w, http.StatusBadRequest, "execution_mode is required")
		return
	}
	if req.ExecutionMode != "create_issue" && req.ExecutionMode != "run_only" {
		writeError(w, http.StatusBadRequest, "execution_mode must be create_issue or run_only")
		return
	}
	if req.IssueTitleTemplate != nil {
		if err := service.ValidateIssueTitleTemplate(*req.IssueTitleTemplate); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	assigneeUUID, ok := parseUUIDOrBadRequest(w, req.AssigneeID, "assignee_id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	assigneeType := "agent"
	if req.AssigneeType != nil && *req.AssigneeType != "" {
		assigneeType = *req.AssigneeType
	}
	if !isValidAutopilotAssigneeType(assigneeType) {
		writeError(w, http.StatusBadRequest, "assignee_type must be agent or squad")
		return
	}
	if !h.validateAutopilotAssignee(w, r, assigneeType, assigneeUUID, wsUUID) {
		return
	}
	projectID, ok := h.parseAutopilotProjectID(w, r, req.ProjectID, wsUUID)
	if !ok {
		return
	}

	// Validate before insert so a bad payload doesn't half-create the row.
	subscriberUUIDs, ok := h.validateAutopilotSubscribers(w, r, req.Subscribers, workspaceID)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create autopilot")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	autopilot, err := qtx.CreateAutopilot(r.Context(), db.CreateAutopilotParams{
		WorkspaceID:        wsUUID,
		Title:              req.Title,
		AssigneeType:       assigneeType,
		AssigneeID:         assigneeUUID,
		Status:             "active",
		ExecutionMode:      req.ExecutionMode,
		CreatedByType:      "member",
		CreatedByID:        parseUUID(userID),
		Description:        ptrToText(req.Description),
		IssueTitleTemplate: ptrToText(req.IssueTitleTemplate),
		ProjectID:          projectID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create autopilot")
		return
	}

	for _, uid := range subscriberUUIDs {
		if err := qtx.AddAutopilotSubscriber(r.Context(), db.AddAutopilotSubscriberParams{
			AutopilotID: autopilot.ID,
			UserType:    "member",
			UserID:      uid,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to add autopilot subscriber")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create autopilot")
		return
	}
	subs, err := h.Queries.ListAutopilotSubscribers(r.Context(), autopilot.ID)
	if err != nil {
		subs = nil
	}

	resp := autopilotToResponse(autopilot, subs)
	h.publish(protocol.EventAutopilotCreated, workspaceID, "member", userID, map[string]any{"autopilot": resp})
	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.AutopilotCreated(
		userID,
		workspaceID,
		uuidToString(autopilot.ID),
		"manual",
		"manual",
	))
	writeJSON(w, http.StatusCreated, resp)
}

// Writes an HTTP error and returns ok=false on the first invalid entry.
// Returns (nil, true) when raw is empty — caller distinguishes "leave alone"
// from "replace with empty" via the raw-fields map, not this return.
func (h *Handler) validateAutopilotSubscribers(
	w http.ResponseWriter,
	r *http.Request,
	raw []SubscriberInput,
	workspaceID string,
) ([]pgtype.UUID, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	out := make([]pgtype.UUID, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for i, entry := range raw {
		if entry.UserType != "member" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("subscribers[%d].user_type must be 'member'", i))
			return nil, false
		}
		if entry.UserID == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("subscribers[%d].user_id is required", i))
			return nil, false
		}
		uid, ok := parseUUIDOrBadRequest(w, entry.UserID, fmt.Sprintf("subscribers[%d].user_id", i))
		if !ok {
			return nil, false
		}
		if seen[entry.UserID] {
			continue
		}
		seen[entry.UserID] = true
		if !h.isWorkspaceEntity(r.Context(), entry.UserType, entry.UserID, workspaceID) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("subscribers[%d] is not a member of this workspace", i))
			return nil, false
		}
		out = append(out, uid)
	}
	return out, true
}

func (h *Handler) UpdateAutopilot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)

	prev, ok := h.loadAutopilotInWorkspace(w, r, id, workspaceID)
	if !ok {
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
	var req UpdateAutopilotRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var rawFields map[string]json.RawMessage
	json.Unmarshal(bodyBytes, &rawFields)

	params := db.UpdateAutopilotParams{
		ID:                 prev.ID,
		Description:        prev.Description,
		AssigneeID:         prev.AssigneeID,
		IssueTitleTemplate: prev.IssueTitleTemplate,
		ProjectID:          prev.ProjectID,
	}
	if req.Title != nil {
		params.Title = pgtype.Text{String: *req.Title, Valid: true}
	}
	if req.Status != nil {
		params.Status = pgtype.Text{String: *req.Status, Valid: true}
	}
	if req.ExecutionMode != nil {
		params.ExecutionMode = pgtype.Text{String: *req.ExecutionMode, Valid: true}
	}
	if _, ok := rawFields["description"]; ok {
		params.Description = ptrToText(req.Description)
	}
	if _, ok := rawFields["issue_title_template"]; ok {
		if req.IssueTitleTemplate != nil {
			if err := service.ValidateIssueTitleTemplate(*req.IssueTitleTemplate); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		params.IssueTitleTemplate = ptrToText(req.IssueTitleTemplate)
	}
	if _, ok := rawFields["project_id"]; ok {
		projectID, ok := h.parseAutopilotProjectID(w, r, req.ProjectID, prev.WorkspaceID)
		if !ok {
			return
		}
		params.ProjectID = projectID
	}
	// assignee_type and assignee_id are validated as a pair: switching
	// between agent and squad without supplying a new id would leave the
	// row pointing at the wrong table. The client is expected to send both
	// fields on any change; partial updates that change only one are
	// rejected.
	_, typeSent := rawFields["assignee_type"]
	_, idSent := rawFields["assignee_id"]
	if typeSent || idSent {
		nextType := prev.AssigneeType
		if typeSent && req.AssigneeType != nil && *req.AssigneeType != "" {
			nextType = *req.AssigneeType
		}
		if !isValidAutopilotAssigneeType(nextType) {
			writeError(w, http.StatusBadRequest, "assignee_type must be agent or squad")
			return
		}
		nextID := prev.AssigneeID
		if idSent {
			if req.AssigneeID == nil {
				writeError(w, http.StatusBadRequest, "assignee_id cannot be null")
				return
			}
			parsed, ok := parseUUIDOrBadRequest(w, *req.AssigneeID, "assignee_id")
			if !ok {
				return
			}
			nextID = parsed
		}
		// Reject the agent↔squad switch without a paired id, otherwise the
		// row would address agent(id) under assignee_type='squad' or vice
		// versa.
		if typeSent && !idSent && nextType != prev.AssigneeType {
			writeError(w, http.StatusBadRequest, "assignee_id is required when changing assignee_type")
			return
		}
		if !h.validateAutopilotAssignee(w, r, nextType, nextID, prev.WorkspaceID) {
			return
		}
		if typeSent {
			params.AssigneeType = pgtype.Text{String: nextType, Valid: true}
		}
		if idSent {
			params.AssigneeID = nextID
		}
	}

	// Subscribers are validated up-front (before any write) so a bad payload
	// doesn't leave the autopilot row updated but the template stale.
	var (
		subscriberUUIDs    []pgtype.UUID
		replaceSubscribers bool
	)
	if _, sent := rawFields["subscribers"]; sent {
		replaceSubscribers = true
		validated, vok := h.validateAutopilotSubscribers(w, r, req.Subscribers, workspaceID)
		if !vok {
			return
		}
		subscriberUUIDs = validated
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update autopilot")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	autopilot, err := qtx.UpdateAutopilot(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update autopilot")
		return
	}

	if replaceSubscribers {
		if err := qtx.DeleteAutopilotSubscribersForAutopilot(r.Context(), autopilot.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update subscribers")
			return
		}
		for _, uid := range subscriberUUIDs {
			if err := qtx.AddAutopilotSubscriber(r.Context(), db.AddAutopilotSubscriberParams{
				AutopilotID: autopilot.ID,
				UserType:    "member",
				UserID:      uid,
			}); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to add autopilot subscriber")
				return
			}
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update autopilot")
		return
	}

	subs, err := h.Queries.ListAutopilotSubscribers(r.Context(), autopilot.ID)
	if err != nil {
		subs = nil
	}
	resp := autopilotToResponse(autopilot, subs)
	h.publish(protocol.EventAutopilotUpdated, workspaceID, "member", userID, map[string]any{"autopilot": resp})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) parseAutopilotProjectID(
	w http.ResponseWriter,
	r *http.Request,
	raw *string,
	workspaceID pgtype.UUID,
) (pgtype.UUID, bool) {
	if raw == nil || *raw == "" {
		return pgtype.UUID{}, true
	}
	projectID, ok := parseUUIDOrBadRequest(w, *raw, "project_id")
	if !ok {
		return pgtype.UUID{}, false
	}
	if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID:          projectID,
		WorkspaceID: workspaceID,
	}); err != nil {
		writeError(w, http.StatusBadRequest, "project_id must reference a project in this workspace")
		return pgtype.UUID{}, false
	}
	return projectID, true
}

func (h *Handler) DeleteAutopilot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)

	idUUID, ok := parseUUIDOrBadRequest(w, id, "autopilot id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	if _, err := h.Queries.GetAutopilotInWorkspace(r.Context(), db.GetAutopilotInWorkspaceParams{
		ID:          idUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "autopilot not found")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// autopilot_subscriber carries no DB-level foreign key/cascade (repo rule:
	// referential cleanup lives in the application layer), so delete the
	// subscriber template alongside the autopilot in one transaction. Without
	// this, deleting an autopilot would orphan its subscriber rows.
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete autopilot")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	if err := qtx.DeleteAutopilotSubscribersForAutopilot(r.Context(), idUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete autopilot")
		return
	}
	if err := qtx.DeleteAutopilot(r.Context(), idUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete autopilot")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete autopilot")
		return
	}

	h.publish(protocol.EventAutopilotDeleted, workspaceID, "member", userID, map[string]any{"autopilot_id": uuidToString(idUUID)})
	w.WriteHeader(http.StatusNoContent)
}

// ── Trigger management ──────────────────────────────────────────────────────

