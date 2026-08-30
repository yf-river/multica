package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func lifeIdentityResponse(row db.LifeIdentityVersion) map[string]any {
	return map[string]any{
		"id": uuidToString(row.ID), "version": row.Version, "status": row.Status,
		"stable_core": json.RawMessage(row.StableCore), "relationship_contract": json.RawMessage(row.RelationshipContract),
		"growth_profile": json.RawMessage(row.GrowthProfile), "expression_profile": json.RawMessage(row.ExpressionProfile),
		"interests": json.RawMessage(row.Interests), "change_reason": row.ChangeReason,
		"confirmed_at": timestampToPtr(row.ConfirmedAt), "created_at": timestampToString(row.CreatedAt),
	}
}

func (h *Handler) ListLifeIdentityVersions(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListLifeIdentityVersions(r.Context(), db.ListLifeIdentityVersionsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list identity versions")
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, lifeIdentityResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": items})
}

func (h *Handler) ListLifeMemoryRevisions(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "memoryId"), "memory id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetLifeMemory(r.Context(), db.GetLifeMemoryParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID}); err != nil {
		writeError(w, 404, "memory not found")
		return
	}
	rows, err := h.Queries.ListLifeMemoryRevisions(r.Context(), id)
	if err != nil {
		writeError(w, 500, "failed to list memory revisions")
		return
	}
	writeJSON(w, 200, map[string]any{"revisions": rows})
}

type lifeIdentityRequest struct {
	StableCore, RelationshipContract, GrowthProfile, ExpressionProfile json.RawMessage
	Interests                                                          json.RawMessage `json:"interests"`
	ChangeReason                                                       string          `json:"change_reason"`
}

func (req *lifeIdentityRequest) UnmarshalJSON(raw []byte) error {
	type wire struct {
		StableCore           json.RawMessage `json:"stable_core"`
		RelationshipContract json.RawMessage `json:"relationship_contract"`
		GrowthProfile        json.RawMessage `json:"growth_profile"`
		ExpressionProfile    json.RawMessage `json:"expression_profile"`
		Interests            json.RawMessage `json:"interests"`
		ChangeReason         string          `json:"change_reason"`
	}
	var v wire
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	req.StableCore, req.RelationshipContract, req.GrowthProfile, req.ExpressionProfile = v.StableCore, v.RelationshipContract, v.GrowthProfile, v.ExpressionProfile
	req.Interests, req.ChangeReason = v.Interests, v.ChangeReason
	return nil
}

func validJSONObject(raw json.RawMessage) bool {
	var value map[string]any
	return json.Unmarshal(raw, &value) == nil && value != nil
}
func validJSONArray(raw json.RawMessage) bool {
	var value []any
	return json.Unmarshal(raw, &value) == nil && value != nil
}

func (h *Handler) CreateLifeIdentityVersion(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	var req lifeIdentityRequest
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if !validJSONObject(req.StableCore) || !validJSONObject(req.RelationshipContract) || !validJSONObject(req.GrowthProfile) || !validJSONObject(req.ExpressionProfile) || !validJSONArray(req.Interests) {
		writeError(w, http.StatusBadRequest, "identity sections must be valid JSON objects and interests must be an array")
		return
	}
	version, err := h.Queries.GetNextLifeIdentityVersion(r.Context(), db.GetNextLifeIdentityVersionParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reserve identity version")
		return
	}
	row, err := h.Queries.CreateLifeIdentityVersion(r.Context(), db.CreateLifeIdentityVersionParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID, Version: version, Status: "draft",
		StableCore: req.StableCore, RelationshipContract: req.RelationshipContract, GrowthProfile: req.GrowthProfile,
		ExpressionProfile: req.ExpressionProfile, Interests: req.Interests, ChangeReason: strings.TrimSpace(req.ChangeReason),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create identity version")
		return
	}
	writeJSON(w, http.StatusCreated, lifeIdentityResponse(row))
}

func (h *Handler) ActivateLifeIdentityVersion(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "versionId"), "identity version id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to start identity activation")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	q := h.Queries.WithTx(tx)
	if _, err = q.GetLifeIdentityVersionForUser(r.Context(), db.GetLifeIdentityVersionForUserParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID}); err != nil {
		writeEntityLoadError(w, err, "identity version", "version_id", chi.URLParam(r, "versionId"))
		return
	}
	if err = q.SupersedeActiveLifeIdentity(r.Context(), db.SupersedeActiveLifeIdentityParams{WorkspaceID: scope.workspaceID, UserID: scope.userID}); err != nil {
		writeError(w, 500, "failed to supersede current identity")
		return
	}
	row, err := q.ActivateExistingLifeIdentityVersion(r.Context(), db.ActivateExistingLifeIdentityVersionParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID, ConfirmedByID: scope.userID})
	if err != nil {
		writeError(w, http.StatusConflict, "identity version cannot be activated")
		return
	}
	if err = q.SetCompanionCurrentIdentity(r.Context(), db.SetCompanionCurrentIdentityParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, CurrentIdentityVersionID: row.ID}); err != nil {
		writeError(w, 500, "failed to link active identity")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to commit identity activation")
		return
	}
	writeJSON(w, http.StatusOK, lifeIdentityResponse(row))
}

func (h *Handler) ListLifeRelationshipEvents(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListLifeRelationshipEvents(r.Context(), db.ListLifeRelationshipEventsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, 500, "failed to list relationship events")
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, map[string]any{
			"id": uuidToString(row.ID), "event_type": row.EventType, "status": row.Status, "user_position": row.UserPosition,
			"companion_position": row.CompanionPosition, "context": row.Context, "revisit_after": timestampToPtr(row.RevisitAfter),
			"resolution": row.Resolution, "created_at": timestampToString(row.CreatedAt),
		})
	}
	writeJSON(w, 200, map[string]any{"events": items})
}

func (h *Handler) ResolveLifeRelationshipEvent(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "eventId"), "relationship event id")
	if !ok {
		return
	}
	var req struct {
		Status     string `json:"status"`
		Resolution string `json:"resolution"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if req.Status != "resolved" && req.Status != "retained_difference" {
		writeError(w, 400, "status must be resolved or retained_difference")
		return
	}
	row, err := h.Queries.ResolveLifeRelationshipEvent(r.Context(), db.ResolveLifeRelationshipEventParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID, Status: req.Status, Resolution: strings.TrimSpace(req.Resolution)})
	if err != nil {
		writeEntityLoadError(w, err, "relationship event", "event_id", chi.URLParam(r, "eventId"))
		return
	}
	writeJSON(w, 200, map[string]any{"id": uuidToString(row.ID), "status": row.Status, "resolution": row.Resolution})
}

func lifeMaterialResponse(row db.LifeMaterial) map[string]any {
	return map[string]any{
		"id": uuidToString(row.ID), "source_type": row.SourceType, "source_key": row.SourceKey, "source_revision": row.SourceRevision,
		"content": row.Content, "metadata": json.RawMessage(row.Metadata), "occurred_at": timestampToString(row.OccurredAt), "ingested_at": timestampToString(row.IngestedAt),
	}
}

func (h *Handler) ListLifeMaterials(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListLifeMaterials(r.Context(), db.ListLifeMaterialsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, Limit: 200})
	if err != nil {
		writeError(w, 500, "failed to list life materials")
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, lifeMaterialResponse(row))
	}
	writeJSON(w, 200, map[string]any{"materials": items})
}

func (h *Handler) CreateLifeMaterial(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	var req struct {
		Content    string         `json:"content"`
		Metadata   map[string]any `json:"metadata"`
		OccurredAt string         `json:"occurred_at"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		writeError(w, 400, "content is required")
		return
	}
	when := time.Now()
	if req.OccurredAt != "" {
		parsed, err := time.Parse(time.RFC3339, req.OccurredAt)
		if err != nil {
			writeError(w, 400, "occurred_at must be RFC3339")
			return
		}
		when = parsed
	}
	metadata, _ := json.Marshal(req.Metadata)
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s", req.Content, when.UTC().Format(time.RFC3339Nano))))
	row, err := h.Queries.UpsertLifeMaterial(r.Context(), db.UpsertLifeMaterialParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, SourceType: "manual", SourceKey: hex.EncodeToString(digest[:]), SourceRevision: "1", Content: req.Content, Metadata: metadata, OccurredAt: pgtype.Timestamptz{Time: when, Valid: true}})
	if err != nil {
		writeError(w, 500, "failed to create life material")
		return
	}
	profile, err := h.Queries.GetCompanionProfile(r.Context(), db.GetCompanionProfileParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to resolve life companion")
		return
	}
	if err == nil {
		if _, err = h.Queries.QueueLifeMaterialUnderstanding(r.Context(), db.QueueLifeMaterialUnderstandingParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID,
			CompanionAgentID: profile.AgentID, MaterialID: row.ID,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to schedule life material understanding")
			return
		}
	}
	writeJSON(w, 201, lifeMaterialResponse(row))
}

func (h *Handler) ListLifeInternalThoughts(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListAllLifeInternalThoughts(r.Context(), db.ListAllLifeInternalThoughtsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, 500, "failed to list companion thoughts")
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, map[string]any{"id": uuidToString(row.ID), "thought_type": row.ThoughtType, "title": row.Title, "content": row.Content, "status": row.Status, "metadata": json.RawMessage(row.Metadata), "last_developed_at": timestampToString(row.LastDevelopedAt), "created_at": timestampToString(row.CreatedAt), "updated_at": timestampToString(row.UpdatedAt)})
	}
	writeJSON(w, 200, map[string]any{"thoughts": items})
}

func (h *Handler) ListLifeTopics(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListLifeTopics(r.Context(), db.ListLifeTopicsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, 500, "failed to list topics")
		return
	}
	writeJSON(w, 200, map[string]any{"topics": rows})
}

func (h *Handler) UpdateLifeTopic(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "topicId"), "topic id")
	if !ok {
		return
	}
	row, err := h.Queries.GetLifeTopicForUser(r.Context(), db.GetLifeTopicForUserParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, 404, "topic not found")
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if req.Status != "active" && req.Status != "contradicted" && req.Status != "resolved" && req.Status != "archived" {
		writeError(w, 400, "invalid topic status")
		return
	}
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	updated, err := h.Queries.UpdateLifeTopic(r.Context(), db.UpdateLifeTopicParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID, Title: row.Title, Summary: row.Summary, Status: req.Status, Confidence: row.Confidence, Uncertainty: row.Uncertainty, LastObservedAt: row.LastObservedAt, LastReviewedAt: now, ReviewAfter: pgtype.Timestamptz{Time: time.Now().Add(30 * 24 * time.Hour), Valid: true}})
	if err != nil {
		writeError(w, 500, "failed to update topic")
		return
	}
	writeJSON(w, 200, updated)
}

func (h *Handler) ListLifeCommitments(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListLifeCommitments(r.Context(), db.ListLifeCommitmentsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, 500, "failed to list commitments")
		return
	}
	writeJSON(w, 200, map[string]any{"commitments": rows})
}

func (h *Handler) UpdateLifeCommitment(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "commitmentId"), "commitment id")
	if !ok {
		return
	}
	var req struct {
		Status       string `json:"status"`
		Outcome      string `json:"outcome"`
		DueAt        string `json:"due_at"`
		RevisitAfter string `json:"revisit_after"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	var row db.LifeCommitment
	var err error
	switch req.Status {
	case "confirmed":
		due, ok := parseLifeOptionalTime(w, req.DueAt, "due_at")
		if !ok {
			return
		}
		revisit, ok := parseLifeOptionalTime(w, req.RevisitAfter, "revisit_after")
		if !ok {
			return
		}
		row, err = h.Queries.ConfirmLifeCommitment(r.Context(), db.ConfirmLifeCommitmentParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID, DueAt: due, RevisitAfter: revisit})
	case "completed", "cancelled", "expired":
		row, err = h.Queries.UpdateLifeCommitmentStatus(r.Context(), db.UpdateLifeCommitmentStatusParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID, Status: req.Status, Outcome: strings.TrimSpace(req.Outcome)})
	default:
		writeError(w, 400, "unsupported commitment status")
		return
	}
	if err != nil {
		writeEntityLoadError(w, err, "commitment", "commitment_id", chi.URLParam(r, "commitmentId"))
		return
	}
	writeJSON(w, 200, row)
}

func (h *Handler) GetLifeProactivePolicy(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	row, err := h.Queries.GetLifeProactivePolicy(r.Context(), db.GetLifeProactivePolicyParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, 404, "proactive policy not found")
		return
	}
	writeJSON(w, 200, map[string]any{"enabled": row.Enabled, "timezone": row.Timezone, "quiet_hours": json.RawMessage(row.QuietHours), "minimum_interval_hours": float64(row.MinimumInterval.Microseconds) / float64(time.Hour/time.Microsecond), "next_check_at": timestampToString(row.NextCheckAt), "unanswered_count": row.UnansweredCount})
}

func (h *Handler) UpdateLifeProactivePolicy(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled              bool           `json:"enabled"`
		Timezone             string         `json:"timezone"`
		QuietHours           map[string]any `json:"quiet_hours"`
		MinimumIntervalHours int64          `json:"minimum_interval_hours"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if req.MinimumIntervalHours < 1 || req.MinimumIntervalHours > 24*30 {
		writeError(w, 400, "minimum_interval_hours must be between 1 and 720")
		return
	}
	if strings.TrimSpace(req.Timezone) == "" {
		req.Timezone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		writeError(w, 400, "invalid timezone")
		return
	}
	quiet, _ := json.Marshal(req.QuietHours)
	now := time.Now()
	row, err := h.Queries.UpsertLifeProactivePolicy(r.Context(), db.UpsertLifeProactivePolicyParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, Enabled: req.Enabled, Timezone: req.Timezone, QuietHours: quiet, MinimumInterval: pgtype.Interval{Microseconds: req.MinimumIntervalHours * int64(time.Hour/time.Microsecond), Valid: true}, NextCheckAt: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		writeError(w, 500, "failed to update proactive policy")
		return
	}
	writeJSON(w, 200, row)
}

func lifeObserverResponse(row db.LifeObserver) map[string]any {
	return map[string]any{"id": uuidToString(row.ID), "agent_id": uuidToString(row.AgentID), "name": row.Name, "basis_type": row.BasisType, "status": row.Status, "current_version": row.CurrentVersion, "next_run_at": timestampToString(row.NextRunAt), "last_run_at": timestampToPtr(row.LastRunAt)}
}

func (h *Handler) ListLifeObservers(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListLifeObservers(r.Context(), db.ListLifeObserversParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, 500, "failed to list observers")
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		item := lifeObserverResponse(row)
		version, _ := h.Queries.GetCurrentLifeObserverVersion(r.Context(), row.ID)
		item["personality"] = json.RawMessage(version.Personality)
		item["perspective"] = json.RawMessage(version.Perspective)
		item["expression_profile"] = json.RawMessage(version.ExpressionProfile)
		knowledge, _ := h.Queries.ListLifeObserverKnowledge(r.Context(), row.ID)
		item["knowledge"] = knowledge
		items = append(items, item)
	}
	writeJSON(w, 200, map[string]any{"observers": items})
}

func (h *Handler) CreateLifeObserver(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	var req struct {
		AgentID           string          `json:"agent_id"`
		Name              string          `json:"name"`
		BasisType         string          `json:"basis_type"`
		Personality       json.RawMessage `json:"personality"`
		Perspective       json.RawMessage `json:"perspective"`
		ExpressionProfile json.RawMessage `json:"expression_profile"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
	if !ok {
		return
	}
	if req.BasisType != "real_person" && req.BasisType != "reconstructed" && req.BasisType != "virtual" {
		writeError(w, 400, "invalid basis_type")
		return
	}
	if strings.TrimSpace(req.Name) == "" || !validJSONObject(req.Personality) || !validJSONObject(req.Perspective) || !validJSONObject(req.ExpressionProfile) {
		writeError(w, 400, "name and observer personality objects are required")
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), agentID)
	if err != nil || agent.WorkspaceID != scope.workspaceID || agent.ArchivedAt.Valid {
		writeError(w, 404, "agent not found")
		return
	}
	profile, err := h.Queries.GetCompanionProfile(r.Context(), db.GetCompanionProfileParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 409, "configure the companion before creating observers")
		return
	}
	if err != nil {
		writeError(w, 500, "failed to validate observer runtime")
		return
	}
	if profile.AgentID == agentID {
		writeError(w, 409, "the companion and an independent observer must use different agents")
		return
	}
	companion, err := h.Queries.GetAgent(r.Context(), profile.AgentID)
	if err != nil || companion.ArchivedAt.Valid {
		writeError(w, 409, "companion agent is unavailable")
		return
	}
	if companion.RuntimeID != agent.RuntimeID || strings.TrimSpace(companion.Model.String) != strings.TrimSpace(agent.Model.String) {
		writeError(w, 409, "observer runtime and model must match the companion")
		return
	}
	if !h.requirePersonalAgentAccess(w, r, agent, "member", uuidToString(scope.userID), uuidToString(scope.workspaceID), "you do not have access to this agent") {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to start observer creation")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	q := h.Queries.WithTx(tx)
	observer, err := q.CreateLifeObserver(r.Context(), db.CreateLifeObserverParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, AgentID: agentID, Name: strings.TrimSpace(req.Name), BasisType: req.BasisType})
	if err != nil {
		writeError(w, 409, "observer already exists or is invalid")
		return
	}
	_, err = q.CreateLifeObserverVersion(r.Context(), db.CreateLifeObserverVersionParams{ObserverID: observer.ID, Version: 1, Personality: req.Personality, Perspective: req.Perspective, ExpressionProfile: req.ExpressionProfile, ChangeReason: "建立观察者", ConfirmedByID: scope.userID})
	if err != nil {
		writeError(w, 500, "failed to create observer personality")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to commit observer")
		return
	}
	writeJSON(w, 201, lifeObserverResponse(observer))
}

func (h *Handler) UpdateLifeObserver(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "observerId"), "observer id")
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if req.Status != "active" && req.Status != "paused" && req.Status != "archived" {
		writeError(w, 400, "invalid observer status")
		return
	}
	row, err := h.Queries.UpdateLifeObserverStatus(r.Context(), db.UpdateLifeObserverStatusParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID, Status: req.Status})
	if err != nil {
		writeEntityLoadError(w, err, "observer", "observer_id", chi.URLParam(r, "observerId"))
		return
	}
	writeJSON(w, 200, lifeObserverResponse(row))
}

func (h *Handler) AddLifeObserverKnowledge(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "observerId"), "observer id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetLifeObserverForUser(r.Context(), db.GetLifeObserverForUserParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID}); err != nil {
		writeError(w, 404, "observer not found")
		return
	}
	var req struct{ Title, Content, Source string }
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Content) == "" {
		writeError(w, 400, "title and content are required")
		return
	}
	row, err := h.Queries.CreateLifeObserverKnowledge(r.Context(), db.CreateLifeObserverKnowledgeParams{ObserverID: id, Title: strings.TrimSpace(req.Title), Content: strings.TrimSpace(req.Content), Source: strings.TrimSpace(req.Source)})
	if err != nil {
		writeError(w, 500, "failed to add observer knowledge")
		return
	}
	writeJSON(w, 201, row)
}

func (h *Handler) CreateLifeObserverVersion(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "observerId"), "observer id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetLifeObserverForUser(r.Context(), db.GetLifeObserverForUserParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID}); err != nil {
		writeError(w, 404, "observer not found")
		return
	}
	var req struct {
		Personality       json.RawMessage `json:"personality"`
		Perspective       json.RawMessage `json:"perspective"`
		ExpressionProfile json.RawMessage `json:"expression_profile"`
		ChangeReason      string          `json:"change_reason"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if !validJSONObject(req.Personality) || !validJSONObject(req.Perspective) || !validJSONObject(req.ExpressionProfile) || strings.TrimSpace(req.ChangeReason) == "" {
		writeError(w, 400, "observer personality objects and change_reason are required")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to start observer update")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	q := h.Queries.WithTx(tx)
	version, err := q.GetNextLifeObserverVersion(r.Context(), id)
	if err != nil {
		writeError(w, 500, "failed to reserve observer version")
		return
	}
	row, err := q.CreateLifeObserverVersion(r.Context(), db.CreateLifeObserverVersionParams{ObserverID: id, Version: version, Personality: req.Personality, Perspective: req.Perspective, ExpressionProfile: req.ExpressionProfile, ChangeReason: strings.TrimSpace(req.ChangeReason), ConfirmedByID: scope.userID})
	if err != nil {
		writeError(w, 500, "failed to create observer version")
		return
	}
	if err := q.SetLifeObserverCurrentVersion(r.Context(), db.SetLifeObserverCurrentVersionParams{ID: id, CurrentVersion: version}); err != nil {
		writeError(w, 500, "failed to activate observer version")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to commit observer version")
		return
	}
	writeJSON(w, 201, row)
}

func (h *Handler) RunLifeObserver(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "observerId"), "observer id")
	if !ok {
		return
	}
	observer, err := h.Queries.GetLifeObserverForUser(r.Context(), db.GetLifeObserverForUserParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, 404, "observer not found")
		return
	}
	now := time.Now()
	interval := time.Duration(observer.MinimumInterval.Microseconds)*time.Microsecond + time.Duration(observer.MinimumInterval.Days)*24*time.Hour + time.Duration(observer.MinimumInterval.Months)*30*24*time.Hour
	if interval <= 0 {
		interval = 12 * time.Hour
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to start observer run")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	q := h.Queries.WithTx(tx)
	if err := q.AdvanceLifeObserverSchedule(r.Context(), db.AdvanceLifeObserverScheduleParams{ID: id, NextRunAt: pgtype.Timestamptz{Time: now.Add(interval), Valid: true}}); err != nil {
		writeError(w, 500, "failed to schedule next observer run")
		return
	}
	key := "manual:" + uuidToString(id) + ":" + now.UTC().Format(time.RFC3339Nano)
	input, _ := json.Marshal(map[string]any{"observer_id": uuidToString(id), "reason": "用户手动要求观察"})
	job, err := q.CreateLifeCognitionJob(r.Context(), db.CreateLifeCognitionJobParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, CompanionAgentID: observer.AgentID, JobType: "observer_run", DedupeKey: key, Input: input, ScheduledAt: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		writeError(w, 500, "failed to schedule observer")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to commit observer run")
		return
	}
	writeJSON(w, 202, map[string]any{"job_id": uuidToString(job.ID)})
}

func (h *Handler) ListLifeObservationSeat(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	judgements, err := h.Queries.ListLifeObserverJudgementsForUser(r.Context(), db.ListLifeObserverJudgementsForUserParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, 500, "failed to list observer judgements")
		return
	}
	topics, err := h.Queries.ListLifeObservationTopics(r.Context(), db.ListLifeObservationTopicsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, 500, "failed to list observation topics")
		return
	}
	writeJSON(w, 200, map[string]any{"judgements": judgements, "topics": topics})
}

func (h *Handler) UpdateLifeObservationTopic(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "topicId"), "observation topic id")
	if !ok {
		return
	}
	var req struct {
		Status            string `json:"status"`
		CompanionResponse string `json:"companion_response"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if req.Status != "open" && req.Status != "surfaced" && req.Status != "discussing" && req.Status != "resolved" && req.Status != "archived" {
		writeError(w, 400, "invalid observation topic status")
		return
	}
	row, err := h.Queries.UpdateLifeObservationTopic(r.Context(), db.UpdateLifeObservationTopicParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID, Status: req.Status, CompanionResponse: strings.TrimSpace(req.CompanionResponse)})
	if err != nil {
		writeEntityLoadError(w, err, "observation topic", "topic_id", chi.URLParam(r, "topicId"))
		return
	}
	writeJSON(w, 200, row)
}

func (h *Handler) ListLifeModules(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListLifeModules(r.Context(), db.ListLifeModulesParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, 500, "failed to list modules")
		return
	}
	writeJSON(w, 200, map[string]any{"modules": rows})
}

func (h *Handler) UpdateLifeModule(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "moduleId"), "module id")
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if req.Status != "active" && req.Status != "paused" && req.Status != "retired" {
		writeError(w, 400, "invalid module status")
		return
	}
	row, err := h.Queries.UpdateLifeModuleStatus(r.Context(), db.UpdateLifeModuleStatusParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID, Status: req.Status})
	if err != nil {
		writeEntityLoadError(w, err, "module", "module_id", chi.URLParam(r, "moduleId"))
		return
	}
	writeJSON(w, 200, row)
}

func (h *Handler) ListLifeCognitionJobs(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListLifeCognitionJobs(r.Context(), db.ListLifeCognitionJobsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, Limit: 200})
	if err != nil {
		writeError(w, 500, "failed to list cognition jobs")
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		input := json.RawMessage(row.Input)
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		var output any
		if len(row.Output) > 0 {
			output = json.RawMessage(row.Output)
		}
		items = append(items, map[string]any{
			"id":           uuidToString(row.ID),
			"job_type":     row.JobType,
			"status":       row.Status,
			"input":        input,
			"output":       output,
			"scheduled_at": timestampToString(row.ScheduledAt),
			"completed_at": timestampToPtr(row.CompletedAt),
			"error":        row.Error,
			"attempt":      row.Attempt,
			"max_attempts": row.MaxAttempts,
		})
	}
	writeJSON(w, 200, map[string]any{"jobs": items})
}

func (h *Handler) RetryLifeCognitionJob(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "jobId"), "cognition job id")
	if !ok {
		return
	}
	job, err := h.Queries.RetryLifeCognitionJob(r.Context(), db.RetryLifeCognitionJobParams{
		ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "cognition job is not retryable")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to retry cognition job")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": uuidToString(job.ID), "status": job.Status})
}

func (h *Handler) RejectLifeActionProposal(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "proposalId"), "proposal id")
	if !ok {
		return
	}
	row, err := h.Queries.RejectLifeActionProposal(r.Context(), db.RejectLifeActionProposalParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeEntityLoadError(w, err, "proposal", "proposal_id", chi.URLParam(r, "proposalId"))
		return
	}
	writeJSON(w, 200, lifeProposalToResponse(row))
}

func (h *Handler) ListLifeUpgradeEvaluations(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListLifeUpgradeEvaluations(r.Context(), db.ListLifeUpgradeEvaluationsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, 500, "failed to list upgrade evaluations")
		return
	}
	writeJSON(w, 200, map[string]any{"evaluations": rows})
}

func (h *Handler) CreateLifeUpgradeEvaluation(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	var req struct {
		IdentityVersionID string           `json:"identity_version_id"`
		CandidateLabel    string           `json:"candidate_label"`
		BaselineLabel     string           `json:"baseline_label"`
		Scenarios         []map[string]any `json:"scenarios"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.CandidateLabel) == "" || strings.TrimSpace(req.BaselineLabel) == "" || len(req.Scenarios) == 0 {
		writeError(w, 400, "labels and scenarios are required")
		return
	}
	identityID := pgtype.UUID{}
	var err error
	if req.IdentityVersionID != "" {
		identityID, err = parseUUIDText(req.IdentityVersionID)
		if err != nil {
			writeError(w, 400, "invalid identity_version_id")
			return
		}
	}
	scenarios, _ := json.Marshal(req.Scenarios)
	profile, err := h.Queries.GetCompanionProfile(r.Context(), db.GetCompanionProfileParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, 409, "companion is not configured")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to start upgrade evaluation")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	q := h.Queries.WithTx(tx)
	eval, err := q.CreateLifeUpgradeEvaluation(r.Context(), db.CreateLifeUpgradeEvaluationParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, IdentityVersionID: identityID, CandidateLabel: strings.TrimSpace(req.CandidateLabel), BaselineLabel: strings.TrimSpace(req.BaselineLabel), Scenarios: scenarios})
	if err != nil {
		writeError(w, 500, "failed to create upgrade evaluation")
		return
	}
	eval, err = q.StartLifeUpgradeEvaluation(r.Context(), db.StartLifeUpgradeEvaluationParams{ID: eval.ID, WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, 500, "failed to start upgrade evaluation")
		return
	}
	input, _ := json.Marshal(map[string]any{"evaluation_id": uuidToString(eval.ID), "candidate_label": eval.CandidateLabel, "baseline_label": eval.BaselineLabel, "scenarios": json.RawMessage(eval.Scenarios)})
	_, err = q.CreateLifeCognitionJob(r.Context(), db.CreateLifeCognitionJobParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, CompanionAgentID: profile.AgentID, JobType: "upgrade_evaluation", DedupeKey: "evaluation:" + uuidToString(eval.ID), Input: input, ScheduledAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}})
	if err != nil {
		writeError(w, 500, "failed to schedule upgrade evaluation")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to commit upgrade evaluation")
		return
	}
	writeJSON(w, 202, eval)
}

func parseUUIDText(raw string) (pgtype.UUID, error) {
	id := parseUUID(raw)
	if !id.Valid {
		return pgtype.UUID{}, errors.New("invalid uuid")
	}
	return id, nil
}
