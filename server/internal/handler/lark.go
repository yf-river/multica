package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/integrations/lark"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// LarkInstallationResponse excludes encrypted credentials and WS lease state;
// those remain server-only.
type LarkInstallationResponse struct {
	ID              string  `json:"id"`
	WorkspaceID     string  `json:"workspace_id"`
	AgentID         string  `json:"agent_id"`
	AppID           string  `json:"app_id"`
	TenantKey       *string `json:"tenant_key,omitempty"`
	BotOpenID       string  `json:"bot_open_id"`
	InstallerUserID string  `json:"installer_user_id"`
	Status          string  `json:"status"`
	// Region selects the Feishu or international Lark cloud.
	Region      string `json:"region"`
	InstalledAt string `json:"installed_at"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func larkInstallationToResponse(row db.LarkInstallation) LarkInstallationResponse {
	resp := LarkInstallationResponse{
		ID:              uuidToString(row.ID),
		WorkspaceID:     uuidToString(row.WorkspaceID),
		AgentID:         uuidToString(row.AgentID),
		AppID:           row.AppID,
		BotOpenID:       row.BotOpenID,
		InstallerUserID: uuidToString(row.InstallerUserID),
		Status:          row.Status,
		Region:          row.Region,
		InstalledAt:     row.InstalledAt.Time.UTC().Format(time.RFC3339),
		CreatedAt:       row.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt:       row.UpdatedAt.Time.UTC().Format(time.RFC3339),
	}
	if row.TenantKey.Valid {
		tk := row.TenantKey.String
		resp.TenantKey = &tk
	}
	return resp
}

// ListLarkInstallations is member-visible. configured reports credential
// storage availability; install_supported reports the complete device flow.
func (h *Handler) ListLarkInstallations(w http.ResponseWriter, r *http.Request) {
	if h.LarkInstallations == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"installations":     []LarkInstallationResponse{},
			"configured":        false,
			"install_supported": false,
		})
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	rows, err := h.LarkInstallations.ListByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list lark installations")
		return
	}
	out := make([]LarkInstallationResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, larkInstallationToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installations":     out,
		"configured":        true,
		"install_supported": h.LarkRegistration != nil && h.LarkAPIClient != nil,
	})
}

// RevokeLarkInstallation preserves the row for audit while making the WS hub
// drop its connection.
func (h *Handler) RevokeLarkInstallation(w http.ResponseWriter, r *http.Request) {
	if h.LarkInstallations == nil {
		writeError(w, http.StatusServiceUnavailable, "lark integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	instUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "installationId"), "installation id")
	if !ok {
		return
	}
	// Recheck workspace ownership before mutating a guessed UUID.
	if _, err := h.LarkInstallations.GetInWorkspace(r.Context(), instUUID, wsUUID); err != nil {
		if errors.Is(err, lark.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "lark installation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load installation")
		return
	}
	if err := h.LarkInstallations.Revoke(r.Context(), instUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke installation")
		return
	}
	h.publish(protocol.EventLarkInstallationRevoked, uuidToString(wsUUID), "user", userID, map[string]any{
		"id": uuidToString(instUUID),
	})
	w.WriteHeader(http.StatusNoContent)
}

// RedeemLarkBindingTokenRequest carries the one-time Bot binding token.
type RedeemLarkBindingTokenRequest struct {
	Token string `json:"token"`
}

// RedeemLarkBindingTokenResponse lets the client render success without a
// second lookup.
type RedeemLarkBindingTokenResponse struct {
	WorkspaceID    string `json:"workspace_id"`
	InstallationID string `json:"installation_id"`
	LarkOpenID     string `json:"lark_open_id"`
}

// RedeemLarkBindingToken combines the one-time token with the authenticated
// user in one transaction. Invalid/expired, conflicting and cross-workspace
// bindings remain distinct 410, 409 and 403 outcomes.
func (h *Handler) RedeemLarkBindingToken(w http.ResponseWriter, r *http.Request) {
	if h.LarkBindingTokens == nil {
		writeError(w, http.StatusServiceUnavailable, "lark integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req RedeemLarkBindingTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	redeemed, err := h.LarkBindingTokens.RedeemAndBind(r.Context(), req.Token, userUUID)
	if err != nil {
		switch {
		case errors.Is(err, lark.ErrBindingTokenInvalid):
			writeError(w, http.StatusGone, "binding token invalid or expired")
		case errors.Is(err, lark.ErrBindingAlreadyAssigned):
			writeError(w, http.StatusConflict, "this Lark account is already bound to a different Multica user")
		case errors.Is(err, lark.ErrBindingNotWorkspaceMember):
			writeError(w, http.StatusForbidden, "binding refused (are you a workspace member?)")
		default:
			writeError(w, http.StatusInternalServerError, "failed to redeem token")
		}
		return
	}

	writeJSON(w, http.StatusOK, RedeemLarkBindingTokenResponse{
		WorkspaceID:    uuidToString(redeemed.WorkspaceID),
		InstallationID: uuidToString(redeemed.InstallationID),
		LarkOpenID:     string(redeemed.LarkOpenID),
	})
}

// BeginLarkInstallResponse drives the QR dialog and polling cadence.
type BeginLarkInstallResponse struct {
	SessionID           string `json:"session_id"`
	QRCodeURL           string `json:"qr_code_url"`
	ExpiresInSeconds    int    `json:"expires_in_seconds"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

// BeginLarkInstall opens an admin-only device-flow session for a workspace
// Agent. The service repeats the workspace check at the transaction boundary.
func (h *Handler) BeginLarkInstall(w http.ResponseWriter, r *http.Request) {
	if h.LarkRegistration == nil {
		writeError(w, http.StatusServiceUnavailable, "lark install not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentIDStr := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if agentIDStr == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, agentIDStr, "agent_id")
	if !ok {
		return
	}
	// Require an explicit cloud so credentials cannot be sent to the wrong
	// account system.
	regionParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("region")))
	switch regionParam {
	case "feishu", "lark":
		// valid
	default:
		writeError(w, http.StatusBadRequest, "region must be 'feishu' or 'lark'")
		return
	}
	// Return an HTTP-scoped 404 before the service repeats this ownership check.
	if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "agent not found in this workspace")
			return
		}
		slog.Error("load Lark installation agent failed", "workspace_id", uuidToString(wsUUID), "agent_id", uuidToString(agentUUID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load agent")
		return
	}
	initiatorUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	res, err := h.LarkRegistration.BeginInstall(r.Context(), lark.BeginInstallParams{
		WorkspaceID: wsUUID,
		AgentID:     agentUUID,
		InitiatorID: initiatorUUID,
		Region:      lark.Region(regionParam),
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to start install: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, BeginLarkInstallResponse{
		SessionID:           res.SessionID,
		QRCodeURL:           res.QRCodeURL,
		ExpiresInSeconds:    res.ExpiresInSeconds,
		PollIntervalSeconds: res.PollIntervalSeconds,
	})
}

// LarkInstallStatusResponse is the pending/success/error polling payload.
type LarkInstallStatusResponse struct {
	Status         string `json:"status"`
	InstallationID string `json:"installation_id,omitempty"`
	ErrorReason    string `json:"error_reason,omitempty"`
	ErrorMessage   string `json:"error_message,omitempty"`
}

// GetLarkInstallStatus is admin-only and workspace-scoped. Reads are
// idempotent; the registration service owns eventual session cleanup.
func (h *Handler) GetLarkInstallStatus(w http.ResponseWriter, r *http.Request) {
	if h.LarkRegistration == nil {
		writeError(w, http.StatusServiceUnavailable, "lark install not configured")
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(chi.URLParam(r, "sessionId"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session id is required")
		return
	}
	state, err := h.LarkRegistration.GetSession(wsUUID, sessionID)
	if err != nil {
		if errors.Is(err, lark.ErrRegistrationSessionNotFound) {
			writeError(w, http.StatusNotFound, "install session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load install session")
		return
	}
	resp := LarkInstallStatusResponse{
		Status:       string(state.Status),
		ErrorReason:  state.ErrorReason,
		ErrorMessage: state.ErrorMessage,
	}
	if state.InstallationID.Valid {
		resp.InstallationID = uuidToString(state.InstallationID)
		// The lark_installation:created event is published by the
		// RegistrationService at the row-commit point (see
		// registration_service.go finishSuccess), not here — that keeps
		// the connection-badge refresh independent of whether any browser
		// polls this status endpoint to success.
	}
	writeJSON(w, http.StatusOK, resp)
}
