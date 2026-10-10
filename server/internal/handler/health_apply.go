package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/health"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ApplyHealthReport 应用既有提取，不执行模型、不覆盖原件或历史提取结果。
func (h *Handler) ApplyHealthReport(w http.ResponseWriter, r *http.Request) {
	s, ok := h.healthRequestScope(w, r, true)
	if !ok {
		return
	}
	p, ok := h.loadHealthReport(w, r, s)
	if !ok {
		return
	}
	var input struct {
		Revision int64 `json:"revision"`
	}
	if !decodeHealth(w, r, &input) {
		return
	}
	if err := h.enrichHealthReport(r.Context(), s, &p); err != nil {
		healthFailure(w, err)
		return
	}
	if p.Result == nil || len(p.Result.Observations) == 0 {
		writeError(w, 409, "报告尚未提取指标")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		healthFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	old, rev, _, err := h.healthState(r.Context(), tx, s, true)
	if err != nil {
		healthFailure(w, err)
		return
	}
	if rev != input.Revision {
		healthFailure(w, errHealthConflict)
		return
	}
	existing, err := h.healthObservations(r.Context(), tx, s, old.ObservationIDs)
	if err != nil {
		healthFailure(w, err)
		return
	}
	byID := map[string]health.Observation{}
	for _, o := range existing {
		byID[o.ID] = o
		if o.Supersedes != "" {
			byID[o.Supersedes] = o
		}
	}
	incoming := []health.Observation{}
	for _, original := range p.Result.Observations {
		o := original
		if active, ok := byID[original.ID]; ok && active.Status == "accepted" && active.Source != "manual" && active.Value == original.Value && active.Unit == original.Unit {
			incoming = append(incoming, active)
			continue
		}
		o.ID = uuid.NewString()
		o.Supersedes = original.ID
		o.Status = "accepted"
		raw, err := json.Marshal(o)
		if err != nil {
			healthFailure(w, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO health_observation(id,workspace_id,user_id,data) VALUES($1,$2,$3,$4)`, o.ID, s.workspace, s.user, raw); err != nil {
			healthFailure(w, err)
			return
		}
		incoming = append(incoming, o)
	}
	next := health.Apply(old, existing, incoming)
	unchanged := len(old.CurrentObservationIDs) == len(next.CurrentObservationIDs) && len(old.ObservationIDs) == len(next.ObservationIDs)
	current := map[string]bool{}
	for _, id := range old.CurrentObservationIDs {
		current[id] = true
	}
	for _, id := range next.CurrentObservationIDs {
		if !current[id] {
			unchanged = false
		}
	}
	if unchanged {
		writeJSON(w, 200, map[string]any{"revision": rev, "already_applied": true})
		return
	}
	var updated *db.Issue
	if p.IssueID != nil && p.Result.Analysis != "" && p.IssueStatus == "blocked" {
		issue, updateErr := db.New(tx).UpdateIssueStatus(r.Context(), db.UpdateIssueStatusParams{ID: parseUUID(*p.IssueID), Status: "done", WorkspaceID: parseUUID(s.workspace)})
		if updateErr != nil {
			healthFailure(w, updateErr)
			return
		}
		updated = &issue
	}
	if err = h.saveHealthState(r.Context(), tx, s, old, next, rev, "应用体检报告结果", false); err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		healthFailure(w, err)
		return
	}
	if updated != nil {
		h.publish(protocol.EventIssueUpdated, s.workspace, "member", s.user, map[string]any{"issue": issueToResponse(*updated, h.getIssuePrefix(r.Context(), updated.WorkspaceID))})
	}
	writeJSON(w, 200, map[string]any{"revision": rev + 1, "imported": len(incoming), "report_id": p.ID})
}

func (h *Handler) syncHealthReportIssue(r *http.Request, s healthScope, id string) {
	p, err := scanHealthReport(h.DB.QueryRow(r.Context(), healthReportSelect+` WHERE id=$1 AND workspace_id=$2 AND user_id=$3`, id, s.workspace, s.user))
	if err != nil || p.IssueID == nil || h.enrichHealthReport(r.Context(), s, &p) != nil || p.Result == nil || p.Result.Analysis == "" || p.IssueStatus != "blocked" {
		return
	}
	issue, err := h.Queries.GetIssue(r.Context(), parseUUID(*p.IssueID))
	if err != nil {
		return
	}
	updated, err := h.Queries.UpdateIssueStatus(r.Context(), db.UpdateIssueStatusParams{ID: issue.ID, Status: "done", WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return
	}
	h.publish(protocol.EventIssueUpdated, s.workspace, "member", s.user, map[string]any{"issue": issueToResponse(updated, h.getIssuePrefix(r.Context(), updated.WorkspaceID))})
}
