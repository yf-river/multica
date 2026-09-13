// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（life_observation.go）实现"人生档案"里的主动关怀回执查询与
// 编年史（chronicle）接口：主动关怀检查列表、编年史条目的创建/列出/补写
// "后来的理解"，每条编年史都带修订历史与证据引用。
package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// lifeProactiveCheckResponse 是主动关怀检查（proactive check）的对外 JSON 结构。
type lifeProactiveCheckResponse struct {
	ID              string          `json:"id"`
	Status          string          `json:"status"`
	TriggerSource   string          `json:"trigger_source"`
	Reason          string          `json:"reason"`
	ContextSnapshot json.RawMessage `json:"context_snapshot"`
	CheckedAt       string          `json:"checked_at"`
	Message         string          `json:"message"`
	UserRespondedAt *string         `json:"user_responded_at"`
	ValueAssessment string          `json:"value_assessment"`
}

// lifeProactiveCheckToResponse 把主动关怀检查行转成对外结构。
func lifeProactiveCheckToResponse(check db.LifeProactiveCheck) lifeProactiveCheckResponse {
	return lifeProactiveCheckResponse{
		ID: uuidToString(check.ID), Status: check.Status, TriggerSource: check.TriggerSource,
		Reason: check.Reason, ContextSnapshot: json.RawMessage(check.ContextSnapshot), CheckedAt: timestampToString(check.CheckedAt),
		Message: check.Message, UserRespondedAt: timestampToPtr(check.UserRespondedAt), ValueAssessment: check.ValueAssessment,
	}
}

// ListLifeProactiveChecks 列出最近 100 次主动关怀检查记录。
func (h *Handler) ListLifeProactiveChecks(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListLifeProactiveChecks(r.Context(), db.ListLifeProactiveChecksParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID, Limit: 100,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list proactive checks")
		return
	}
	items := make([]lifeProactiveCheckResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, lifeProactiveCheckToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": items})
}

// lifeChronicleEvidenceResponse 是编年史证据引用的对外 JSON 结构。
type lifeChronicleEvidenceResponse struct {
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
}

// lifeChronicleResponse 是编年史条目（含证据列表）的对外 JSON 结构。
type lifeChronicleResponse struct {
	ID                 string                          `json:"id"`
	PeriodStart        string                          `json:"period_start"`
	PeriodEnd          string                          `json:"period_end"`
	Facts              string                          `json:"facts"`
	Feelings           string                          `json:"feelings"`
	UnderstandingThen  string                          `json:"understanding_then"`
	UnderstandingLater string                          `json:"understanding_later"`
	Actions            string                          `json:"actions"`
	Evidence           []lifeChronicleEvidenceResponse `json:"evidence"`
	CreatedAt          string                          `json:"created_at"`
	UpdatedAt          string                          `json:"updated_at"`
	PeriodKind         string                          `json:"period_kind"`
	GeneratedBy        string                          `json:"generated_by"`
	Revision           int32                           `json:"revision"`
}

// lifeChronicleToResponse 加载编年史条目关联的证据并整理成对外结构。
func (h *Handler) lifeChronicleToResponse(r *http.Request, entry db.LifeChronicleEntry) (lifeChronicleResponse, error) {
	evidence, err := h.Queries.ListLifeChronicleEvidence(r.Context(), entry.ID)
	if err != nil {
		return lifeChronicleResponse{}, err
	}
	items := make([]lifeChronicleEvidenceResponse, 0, len(evidence))
	for _, item := range evidence {
		items = append(items, lifeChronicleEvidenceResponse{SourceType: item.SourceType, SourceID: uuidToString(item.SourceID)})
	}
	return lifeChronicleResponse{
		ID: uuidToString(entry.ID), PeriodStart: timestampToString(entry.PeriodStart), PeriodEnd: timestampToString(entry.PeriodEnd),
		Facts: entry.Facts, Feelings: entry.Feelings, UnderstandingThen: entry.UnderstandingThen,
		UnderstandingLater: entry.UnderstandingLater, Evidence: items,
		Actions:   entry.Actions,
		CreatedAt: timestampToString(entry.CreatedAt), UpdatedAt: timestampToString(entry.UpdatedAt),
		PeriodKind: entry.PeriodKind, GeneratedBy: entry.GeneratedBy, Revision: entry.Revision,
	}, nil
}

// ListLifeChronicleEntries 列出该用户的全部编年史条目。
func (h *Handler) ListLifeChronicleEntries(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListLifeChronicleEntries(r.Context(), db.ListLifeChronicleEntriesParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID,
	})
	if err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to list chronicle entries")
		return
	}
	items := make([]lifeChronicleResponse, 0, len(rows))
	for _, row := range rows {
		item, err := h.lifeChronicleToResponse(r, row)
		if err != nil {
			if writeClientClosedIfCanceled(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to load chronicle evidence")
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": items})
}

// CreateLifeChronicleEntry 手动创建一条编年史：校验时间段与每一条证据确实
// 属于该用户后，事务内落条目、第 1 版修订与证据链接，理解"后来的理解"留空
// 待后续补写。
func (h *Handler) CreateLifeChronicleEntry(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	var req struct {
		PeriodStart       string                      `json:"period_start"`
		PeriodEnd         string                      `json:"period_end"`
		Facts             string                      `json:"facts"`
		Feelings          string                      `json:"feelings"`
		UnderstandingThen string                      `json:"understanding_then"`
		Actions           string                      `json:"actions"`
		Evidence          []lifeMemoryEvidenceRequest `json:"evidence"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Facts) == "" || len(req.Evidence) == 0 {
		writeError(w, http.StatusBadRequest, "facts and evidence are required")
		return
	}
	periodStart, ok := parseLifeOptionalTime(w, req.PeriodStart, "period_start")
	if !ok || !periodStart.Valid {
		if ok {
			writeError(w, http.StatusBadRequest, "period_start is required")
		}
		return
	}
	periodEnd, ok := parseLifeOptionalTime(w, req.PeriodEnd, "period_end")
	if !ok || !periodEnd.Valid {
		if ok {
			writeError(w, http.StatusBadRequest, "period_end is required")
		}
		return
	}
	if periodEnd.Time.Before(periodStart.Time) {
		writeError(w, http.StatusBadRequest, "period_end must not be before period_start")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start chronicle transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	type evidenceLink struct {
		sourceType string
		sourceID   string
	}
	links := make([]evidenceLink, 0, len(req.Evidence))
	for _, evidence := range req.Evidence {
		sourceID, valid := parseUUIDOrBadRequest(w, evidence.SourceID, "evidence.source_id")
		if !valid {
			return
		}
		switch evidence.SourceType {
		case "memory":
			_, err = qtx.GetLifeMemory(r.Context(), db.GetLifeMemoryParams{ID: sourceID, WorkspaceID: scope.workspaceID, UserID: scope.userID})
		case "chat_message":
			_, err = qtx.GetChatMessageForLifeEvidence(r.Context(), db.GetChatMessageForLifeEvidenceParams{ID: sourceID, WorkspaceID: scope.workspaceID, CreatorID: scope.userID})
		case "experiment_round":
			_, err = qtx.GetLifeExperimentRoundForUser(r.Context(), db.GetLifeExperimentRoundForUserParams{ID: sourceID, WorkspaceID: scope.workspaceID, UserID: scope.userID})
		default:
			writeError(w, http.StatusBadRequest, "unsupported chronicle evidence source_type")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "chronicle evidence not found")
			return
		}
		links = append(links, evidenceLink{sourceType: evidence.SourceType, sourceID: evidence.SourceID})
	}
	entry, err := qtx.CreateLifeChronicleEntry(r.Context(), db.CreateLifeChronicleEntryParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID, PeriodStart: periodStart, PeriodEnd: periodEnd,
		Facts: strings.TrimSpace(req.Facts), Feelings: strings.TrimSpace(req.Feelings),
		UnderstandingThen: strings.TrimSpace(req.UnderstandingThen), UnderstandingLater: "",
		Actions: strings.TrimSpace(req.Actions),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create chronicle entry")
		return
	}
	if _, err := qtx.CreateLifeChronicleRevision(r.Context(), db.CreateLifeChronicleRevisionParams{EntryID: entry.ID, Revision: 1, Facts: entry.Facts, Feelings: entry.Feelings, UnderstandingThen: entry.UnderstandingThen, UnderstandingLater: entry.UnderstandingLater, Actions: entry.Actions, ChangeReason: "由用户建立"}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create chronicle revision")
		return
	}
	for _, link := range links {
		if err := qtx.CreateLifeChronicleEvidence(r.Context(), db.CreateLifeChronicleEvidenceParams{
			EntryID: entry.ID, SourceType: link.sourceType, SourceID: parseUUID(link.sourceID),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to link chronicle evidence")
			return
		}
	}
	event, err := recordLifeChangedTx(r.Context(), qtx, scope, "member", scope.userID,
		"chronicle", uuidToString(entry.ID), "created", map[string]any{"period_kind": entry.PeriodKind})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record chronicle event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit chronicle entry")
		return
	}
	h.publishLifeEvents(event)
	response, err := h.lifeChronicleToResponse(r, entry)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chronicle entry")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

// UpdateLifeChronicleLaterUnderstanding 给编年史条目补写"后来的理解"，
// 并记一条新修订推进 revision。
func (h *Handler) UpdateLifeChronicleLaterUnderstanding(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	entryID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "entryId"), "entry id")
	if !ok {
		return
	}
	var req struct {
		UnderstandingLater string `json:"understanding_later"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to start chronicle revision")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	q := h.Queries.WithTx(tx)
	entry, err := q.UpdateLifeChronicleLaterUnderstanding(r.Context(), db.UpdateLifeChronicleLaterUnderstandingParams{
		ID: entryID, WorkspaceID: scope.workspaceID, UserID: scope.userID,
		UnderstandingLater: strings.TrimSpace(req.UnderstandingLater),
	})
	if err != nil {
		writeEntityLoadError(w, err, "chronicle entry", "entry_id", chi.URLParam(r, "entryId"))
		return
	}
	revision, err := q.GetNextLifeChronicleRevision(r.Context(), entry.ID)
	if err != nil {
		writeError(w, 500, "failed to reserve chronicle revision")
		return
	}
	if _, err := q.CreateLifeChronicleRevision(r.Context(), db.CreateLifeChronicleRevisionParams{EntryID: entry.ID, Revision: revision, Facts: entry.Facts, Feelings: entry.Feelings, UnderstandingThen: entry.UnderstandingThen, UnderstandingLater: entry.UnderstandingLater, Actions: entry.Actions, ChangeReason: "补充后来的理解"}); err != nil {
		writeError(w, 500, "failed to record chronicle revision")
		return
	}
	if err := q.SetLifeChronicleRevision(r.Context(), db.SetLifeChronicleRevisionParams{ID: entry.ID, Revision: revision}); err != nil {
		writeError(w, 500, "failed to advance chronicle revision")
		return
	}
	entry.Revision = revision
	event, err := recordLifeChangedTx(r.Context(), q, scope, "member", scope.userID,
		"chronicle", uuidToString(entry.ID), "later_understanding_updated", map[string]any{"revision": revision})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record chronicle event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to commit chronicle revision")
		return
	}
	h.publishLifeEvents(event)
	response, err := h.lifeChronicleToResponse(r, entry)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chronicle entry")
		return
	}
	writeJSON(w, http.StatusOK, response)
}
