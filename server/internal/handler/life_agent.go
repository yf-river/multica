package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type companionTaskScope struct {
	lifeRequestScope
	agentID pgtype.UUID
	taskID  pgtype.UUID
}

type lifeJobTaskScope struct {
	lifeRequestScope
	agentID pgtype.UUID
	taskID  pgtype.UUID
	job     db.LifeCognitionJob
}

type lifeEvidenceReference struct {
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
}

func (h *Handler) requireLifeJobTaskScope(w http.ResponseWriter, r *http.Request) (lifeJobTaskScope, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return lifeJobTaskScope{}, false
	}
	actorType, actorID := h.resolveActor(r, userID, h.resolveWorkspaceID(r))
	if actorType != "agent" {
		writeError(w, http.StatusForbidden, "life job task authentication is required")
		return lifeJobTaskScope{}, false
	}
	agentID, ok := parseUUIDOrBadRequest(w, actorID, "agent id")
	if !ok {
		return lifeJobTaskScope{}, false
	}
	taskID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task id")
	if !ok {
		return lifeJobTaskScope{}, false
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskID)
	if err != nil || task.AgentID != agentID || task.Status != "running" {
		writeError(w, http.StatusForbidden, "active life job task not found")
		return lifeJobTaskScope{}, false
	}
	job, err := h.Queries.GetLifeCognitionJobForTask(r.Context(), taskID)
	if err != nil || job.CompanionAgentID != agentID {
		writeError(w, http.StatusForbidden, "life cognition job not found")
		return lifeJobTaskScope{}, false
	}
	// The task token is bound to the human who owns the Life data.  Do not
	// derive the scope from the job row alone: a forged/forwarded task header
	// must never let one authenticated user complete another user's job.
	if !strings.EqualFold(strings.TrimSpace(userID), uuidToString(job.UserID)) {
		writeError(w, http.StatusForbidden, "life cognition job user mismatch")
		return lifeJobTaskScope{}, false
	}
	if workspaceID := strings.TrimSpace(h.resolveWorkspaceID(r)); workspaceID != "" &&
		!strings.EqualFold(workspaceID, uuidToString(job.WorkspaceID)) {
		writeError(w, http.StatusForbidden, "life cognition job workspace mismatch")
		return lifeJobTaskScope{}, false
	}
	// New worker claims carry an opaque token and context version in both the
	// job row and task context.  Refuse a task whose context was copied from a
	// different claim; legacy rows without a token remain readable so an
	// upgrade can drain them, but their completion path uses the old CAS.
	if job.ClaimToken.Valid {
		var taskContext struct {
			ClaimToken     string `json:"claim_token"`
			ContextVersion int64  `json:"context_version_number"`
		}
		if json.Unmarshal(task.Context, &taskContext) != nil || taskContext.ClaimToken != job.ClaimToken.String || taskContext.ContextVersion != job.ContextVersion {
			writeError(w, http.StatusConflict, "life cognition task claim is stale")
			return lifeJobTaskScope{}, false
		}
	}
	return lifeJobTaskScope{
		lifeRequestScope: lifeRequestScope{workspaceID: job.WorkspaceID, userID: job.UserID},
		agentID:          agentID, taskID: taskID, job: job,
	}, true
}

func (h *Handler) requireCompanionTaskScope(w http.ResponseWriter, r *http.Request) (companionTaskScope, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return companionTaskScope{}, false
	}
	actorType, actorID := h.resolveActor(r, userID, h.resolveWorkspaceID(r))
	if actorType != "agent" {
		writeError(w, http.StatusForbidden, "companion task authentication is required")
		return companionTaskScope{}, false
	}
	agentID, ok := parseUUIDOrBadRequest(w, actorID, "agent id")
	if !ok {
		return companionTaskScope{}, false
	}
	taskID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task id")
	if !ok {
		return companionTaskScope{}, false
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return companionTaskScope{}, false
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
		ID: taskID, WorkspaceID: workspaceID,
	})
	if err != nil || task.AgentID != agentID || task.Status != "running" {
		writeError(w, http.StatusForbidden, "active companion task not found")
		return companionTaskScope{}, false
	}
	targetUserID := task.InitiatorUserID
	if !targetUserID.Valid && task.ChatSessionID.Valid {
		session, err := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
			ID: task.ChatSessionID, WorkspaceID: workspaceID,
		})
		if err == nil {
			targetUserID = session.CreatorID
		}
	}
	if !targetUserID.Valid {
		writeError(w, http.StatusConflict, "companion task has no attributable user")
		return companionTaskScope{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(userID), uuidToString(targetUserID)) {
		writeError(w, http.StatusForbidden, "companion task user mismatch")
		return companionTaskScope{}, false
	}
	if _, err := h.Queries.GetCompanionProfileForAgent(r.Context(), db.GetCompanionProfileForAgentParams{
		WorkspaceID: workspaceID, UserID: targetUserID, AgentID: agentID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusForbidden, "agent is not this user's companion")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to verify companion")
		}
		return companionTaskScope{}, false
	}
	return companionTaskScope{
		lifeRequestScope: lifeRequestScope{workspaceID: workspaceID, userID: targetUserID},
		agentID:          agentID,
		taskID:           taskID,
	}, true
}

func (h *Handler) requireLifeEvidenceTaskScope(w http.ResponseWriter, r *http.Request) (companionTaskScope, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return companionTaskScope{}, false
	}
	actorType, actorID := h.resolveActor(r, userID, h.resolveWorkspaceID(r))
	if actorType != "agent" {
		writeError(w, http.StatusForbidden, "life task authentication is required")
		return companionTaskScope{}, false
	}
	agentID, ok := parseUUIDOrBadRequest(w, actorID, "agent id")
	if !ok {
		return companionTaskScope{}, false
	}
	taskID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task id")
	if !ok {
		return companionTaskScope{}, false
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskID)
	if err != nil || task.AgentID != agentID || task.Status != "running" {
		writeError(w, http.StatusForbidden, "active life task not found")
		return companionTaskScope{}, false
	}
	if job, err := h.Queries.GetLifeCognitionJobForTask(r.Context(), taskID); err == nil && job.CompanionAgentID == agentID {
		if !strings.EqualFold(strings.TrimSpace(userID), uuidToString(job.UserID)) {
			writeError(w, http.StatusForbidden, "life task user mismatch")
			return companionTaskScope{}, false
		}
		if workspaceID := strings.TrimSpace(h.resolveWorkspaceID(r)); workspaceID != "" &&
			!strings.EqualFold(workspaceID, uuidToString(job.WorkspaceID)) {
			writeError(w, http.StatusForbidden, "life task workspace mismatch")
			return companionTaskScope{}, false
		}
		return companionTaskScope{
			lifeRequestScope: lifeRequestScope{workspaceID: job.WorkspaceID, userID: job.UserID},
			agentID:          agentID, taskID: taskID,
		}, true
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return companionTaskScope{}, false
	}
	targetUserID := task.InitiatorUserID
	if !targetUserID.Valid && task.ChatSessionID.Valid {
		session, loadErr := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
			ID: task.ChatSessionID, WorkspaceID: workspaceID,
		})
		if loadErr == nil {
			targetUserID = session.CreatorID
		}
	}
	if !targetUserID.Valid {
		writeError(w, http.StatusConflict, "life task has no attributable user")
		return companionTaskScope{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(userID), uuidToString(targetUserID)) {
		writeError(w, http.StatusForbidden, "life task user mismatch")
		return companionTaskScope{}, false
	}
	_, companionErr := h.Queries.GetCompanionProfileForAgent(r.Context(), db.GetCompanionProfileForAgentParams{
		WorkspaceID: workspaceID, UserID: targetUserID, AgentID: agentID,
	})
	_, observerErr := h.Queries.GetLifeObserverForAgent(r.Context(), db.GetLifeObserverForAgentParams{
		WorkspaceID: workspaceID, UserID: targetUserID, AgentID: agentID,
	})
	if companionErr != nil && observerErr != nil {
		writeError(w, http.StatusForbidden, "agent is not part of this user's life system")
		return companionTaskScope{}, false
	}
	return companionTaskScope{
		lifeRequestScope: lifeRequestScope{workspaceID: workspaceID, userID: targetUserID},
		agentID:          agentID, taskID: taskID,
	}, true
}

func (h *Handler) ResolveLifeEvidence(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeEvidenceTaskScope(w, r)
	if !ok {
		return
	}
	var req struct {
		References []lifeEvidenceReference `json:"references"`
		Purpose    string                  `json:"purpose"`
		Limit      int                     `json:"limit"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if len(req.References) == 0 {
		writeError(w, http.StatusBadRequest, "references are required")
		return
	}
	if len(req.References) > 32 {
		writeError(w, http.StatusBadRequest, "at most 32 references are allowed")
		return
	}
	if req.Limit <= 0 || req.Limit > len(req.References) {
		req.Limit = len(req.References)
	}
	if strings.TrimSpace(req.Purpose) == "" {
		req.Purpose = "life cognition"
	}
	items := make([]map[string]any, 0, len(req.References))
	seen := make(map[string]struct{}, len(req.References))
	for _, reference := range req.References {
		if len(items) >= req.Limit {
			break
		}
		key := reference.SourceType + ":" + reference.SourceID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		id, err := parseLifeEvidenceID(reference.SourceID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		item := map[string]any{"source_type": reference.SourceType, "source_id": reference.SourceID, "available": false}
		switch reference.SourceType {
		case "material":
			material, loadErr := h.Queries.GetLifeMaterialForUser(r.Context(), db.GetLifeMaterialForUserParams{
				ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID,
			})
			if loadErr == nil {
				forgotten, forgottenErr := lifeMaterialForgottenForRequest(r.Context(), h.Queries, scope, material)
				if forgottenErr != nil {
					writeError(w, http.StatusInternalServerError, "failed to verify life material status")
					return
				}
				if !forgotten {
					item["available"] = true
					resolved := lifeMaterialResponse(material)
					if content, ok := resolved["content"].(string); ok {
						resolved["content"] = lifeContextExcerpt(content, 4000)
					}
					item["material"] = resolved
				}
			} else if !errors.Is(loadErr, pgx.ErrNoRows) {
				writeError(w, http.StatusInternalServerError, "failed to resolve life material")
				return
			}
		case "chronicle":
			entry, loadErr := h.Queries.GetLifeChronicleEntry(r.Context(), db.GetLifeChronicleEntryParams{
				ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID,
			})
			if loadErr == nil {
				resolved, resolveErr := h.lifeChronicleToResponse(r, entry)
				if resolveErr != nil {
					writeError(w, http.StatusInternalServerError, "failed to resolve chronicle evidence")
					return
				}
				item["available"] = true
				item["chronicle"] = resolved
			} else if !errors.Is(loadErr, pgx.ErrNoRows) {
				writeError(w, http.StatusInternalServerError, "failed to resolve life chronicle")
				return
			}
		case "memory":
			memory, loadErr := h.Queries.GetLifeMemory(r.Context(), db.GetLifeMemoryParams{
				ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID,
			})
			forgotten := false
			if loadErr == nil {
				forgotten, loadErr = lifeMemoryIsForgotten(r.Context(), h.Queries, scope.workspaceID, scope.userID, memory)
			}
			if loadErr == nil && !forgotten && memory.Status != "archived" {
				resolved, resolveErr := h.lifeMemoryToResponse(r, memory)
				if resolveErr != nil {
					writeError(w, http.StatusInternalServerError, "failed to resolve memory evidence")
					return
				}
				item["available"] = true
				item["memory"] = resolved
			} else if loadErr == nil {
				// Archived memories are governance records, not valid evidence
				// for new cognition. Keep the reference visible but unavailable.
			} else if !errors.Is(loadErr, pgx.ErrNoRows) {
				writeError(w, http.StatusInternalServerError, "failed to resolve life memory")
				return
			}
		case "observer_knowledge":
			knowledge, loadErr := h.Queries.GetLifeObserverKnowledgeForUser(r.Context(), db.GetLifeObserverKnowledgeForUserParams{
				ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID,
			})
			if loadErr == nil {
				item["available"] = true
				item["observer_knowledge"] = map[string]any{
					"id": uuidToString(knowledge.ID), "observer_id": uuidToString(knowledge.ObserverID),
					"title": knowledge.Title, "content": lifeContextExcerpt(knowledge.Content, 4000), "source": knowledge.Source,
					"created_at": timestampToString(knowledge.CreatedAt), "updated_at": timestampToString(knowledge.UpdatedAt),
				}
			} else if !errors.Is(loadErr, pgx.ErrNoRows) {
				writeError(w, http.StatusInternalServerError, "failed to resolve observer knowledge")
				return
			}
		default:
			writeError(w, http.StatusBadRequest, "source_type must be material, chronicle, memory or observer_knowledge")
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"purpose": req.Purpose, "context_version": lifeContextVersion, "evidence": items})
}

func lifeMaterialForgottenForRequest(ctx context.Context, q *db.Queries, scope companionTaskScope, material db.LifeMaterial) (bool, error) {
	digest := sha256.Sum256([]byte(material.Content))
	return q.IsLifeMaterialForgotten(ctx, db.IsLifeMaterialForgottenParams{
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
		SourceType:  material.SourceType,
		SourceKey:   material.SourceKey,
		ContentHash: fmt.Sprintf("%x", digest[:]),
	})
}

func parseLifeEvidenceID(raw string) (pgtype.UUID, error) {
	id, err := util.ParseUUID(raw)
	if err != nil {
		return pgtype.UUID{}, errors.New("source_id must be a UUID")
	}
	return id, nil
}

func (h *Handler) CreateCompanionMemoryCandidate(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireCompanionTaskScope(w, r)
	if !ok {
		return
	}
	h.createLifeMemory(w, r, scope.lifeRequestScope, "agent", scope.agentID)
}

func (h *Handler) CreateCompanionActionProposal(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireCompanionTaskScope(w, r)
	if !ok {
		return
	}
	var req struct {
		ProposalType string              `json:"proposal_type"`
		Status       string              `json:"status"`
		Title        string              `json:"title"`
		Summary      string              `json:"summary"`
		Payload      lifeProposalPayload `json:"payload"`
		ExpiresAt    string              `json:"expires_at"`
	}
	if !decodeRequiredJSON(w, r, &req) || !validateLifeProposalPayload(w, req.ProposalType, req.Title, req.Payload) {
		return
	}
	if req.Status == "" {
		req.Status = "internal_draft"
	}
	if req.Status != "internal_draft" && req.Status != "pending_confirmation" {
		writeError(w, http.StatusBadRequest, "status must be internal_draft or pending_confirmation")
		return
	}
	expiresAt, ok := parseLifeOptionalTime(w, req.ExpiresAt, "expires_at")
	if !ok {
		return
	}
	payload, err := json.Marshal(req.Payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid proposal payload")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start companion proposal")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	proposal, err := qtx.CreateLifeActionProposal(r.Context(), db.CreateLifeActionProposalParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID, CompanionAgentID: scope.agentID,
		ProposalType: req.ProposalType, Status: req.Status, Title: strings.TrimSpace(req.Title),
		Summary: strings.TrimSpace(req.Summary), Payload: payload, ExpiresAt: expiresAt,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create companion proposal")
		return
	}
	event, err := recordAndCommitLifeChangedAs(r.Context(), tx, qtx, scope.lifeRequestScope, "agent", scope.agentID,
		"action_proposal", uuidToString(proposal.ID), "created", map[string]any{"proposal_type": proposal.ProposalType, "status": proposal.Status})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit companion proposal")
		return
	}
	h.publishLifeEvents(event)
	writeJSON(w, http.StatusCreated, lifeProposalToResponse(proposal))
}

func (h *Handler) PresentCompanionActionProposal(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireCompanionTaskScope(w, r)
	if !ok {
		return
	}
	proposalID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "proposalId"), "proposal id")
	if !ok {
		return
	}
	proposal, err := h.Queries.GetLifeActionProposal(r.Context(), db.GetLifeActionProposalParams{
		ID: proposalID, WorkspaceID: scope.workspaceID, UserID: scope.userID,
	})
	if err != nil || proposal.CompanionAgentID != scope.agentID {
		writeError(w, http.StatusNotFound, "proposal not found")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start proposal presentation")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	presented, err := qtx.PresentLifeActionProposal(r.Context(), db.PresentLifeActionProposalParams{
		ID: proposal.ID, WorkspaceID: scope.workspaceID, UserID: scope.userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "only internal drafts can be presented")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to present proposal")
		return
	}
	event, err := recordAndCommitLifeChangedAs(r.Context(), tx, qtx, scope.lifeRequestScope, "agent", scope.agentID,
		"action_proposal", uuidToString(presented.ID), "presented", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit proposal presentation")
		return
	}
	h.publishLifeEvents(event)
	writeJSON(w, http.StatusOK, lifeProposalToResponse(presented))
}

func (h *Handler) CreateCompanionProactiveCheck(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireCompanionTaskScope(w, r)
	if !ok {
		return
	}
	var req struct {
		Status          string         `json:"status"`
		TriggerSource   string         `json:"trigger_source"`
		Reason          string         `json:"reason"`
		ContextSnapshot map[string]any `json:"context_snapshot"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if req.Status != "silent" && req.Status != "spoke" {
		writeError(w, http.StatusBadRequest, "status must be silent or spoke")
		return
	}
	switch req.TriggerSource {
	case "schedule", "commitment", "risk", "manual":
	default:
		writeError(w, http.StatusBadRequest, "invalid trigger_source")
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeError(w, http.StatusBadRequest, "reason is required")
		return
	}
	if req.ContextSnapshot == nil {
		req.ContextSnapshot = map[string]any{}
	}
	contextSnapshot, _ := json.Marshal(req.ContextSnapshot)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start proactive check")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	check, err := qtx.CreateLifeProactiveCheck(r.Context(), db.CreateLifeProactiveCheckParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID, CompanionAgentID: scope.agentID,
		Status: req.Status, TriggerSource: req.TriggerSource, Reason: strings.TrimSpace(req.Reason), ContextSnapshot: contextSnapshot,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record proactive check")
		return
	}
	event, err := recordAndCommitLifeChangedAs(r.Context(), tx, qtx, scope.lifeRequestScope, "agent", scope.agentID,
		"proactive_check", uuidToString(check.ID), "created", map[string]any{"status": check.Status, "trigger_source": check.TriggerSource})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit proactive check")
		return
	}
	h.publishLifeEvents(event)
	writeJSON(w, http.StatusCreated, lifeProactiveCheckToResponse(check))
}

func (h *Handler) CompleteCompanionCognitionJob(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeJobTaskScope(w, r)
	if !ok {
		return
	}
	jobID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "jobId"), "job id")
	if !ok {
		return
	}
	if scope.job.ID != jobID {
		writeError(w, http.StatusNotFound, "life cognition job not found for this task")
		return
	}
	var req struct {
		Output map[string]any `json:"output"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if req.Output == nil {
		req.Output = map[string]any{}
	}
	raw, err := json.Marshal(req.Output)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid output")
		return
	}
	// Life output and the queue terminal transition share one transaction. A
	// process failure between two independent commits could otherwise leave
	// durable understanding without a completed task (or a task that can never
	// be retried after its output was already applied).
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start life completion")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	completed, err := h.completeLifeCognitionJobInQueries(r.Context(), qtx, scope, raw)
	var outputErr lifeJobOutputError
	if errors.As(err, &outputErr) {
		writeError(w, http.StatusUnprocessableEntity, outputErr.Error())
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		current, currentErr := h.Queries.GetLifeCognitionJobForTask(r.Context(), scope.taskID)
		if currentErr == nil && current.Status == "running" {
			writeError(w, http.StatusUnprocessableEntity, "structured output references a record unavailable for this job")
			return
		}
		writeError(w, http.StatusConflict, "life cognition job is not running")
		return
	}
	if err != nil {
		slog.Error("life cognition job completion failed", "job_id", chi.URLParam(r, "jobId"), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to complete life cognition job")
		return
	}
	task, terminalEvent, err := h.TaskService.CompleteLifeTaskInTx(r.Context(), qtx, scope.taskID, raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "life cognition task is no longer running")
			return
		}
		slog.Error("complete life cognition agent task failed", "job_id", chi.URLParam(r, "jobId"), "task_id", uuidToString(scope.taskID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to complete life cognition task")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit life completion")
		return
	}
	// The durable event is now committed. Publish it after commit and refresh
	// the agent's derived availability outside the transaction.
	if terminalEvent.ID != "" {
		h.TaskService.PublishCommittedEvent(terminalEvent)
	}
	h.TaskService.ReconcileAgentStatus(r.Context(), task.AgentID)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": uuidToString(completed.ID), "status": completed.Status,
		"completed_at": timestampToPtr(completed.CompletedAt), "output": req.Output,
	})
}
