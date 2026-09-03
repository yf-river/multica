package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issueposition"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type lifeProposalPayload struct {
	ExperimentID         string         `json:"experiment_id,omitempty"`
	PreviousRoundID      string         `json:"previous_round_id,omitempty"`
	Problem              string         `json:"problem"`
	Hypothesis           string         `json:"hypothesis"`
	Method               map[string]any `json:"method"`
	Plan                 map[string]any `json:"plan"`
	StartsAt             string         `json:"starts_at"`
	EndsAt               string         `json:"ends_at"`
	MemoryIDs            []string       `json:"memory_ids"`
	IssueTitle           string         `json:"issue_title"`
	IssueDescription     string         `json:"issue_description"`
	ActionTitle          string         `json:"action_title,omitempty"`
	ActionInstructions   string         `json:"action_instructions,omitempty"`
	ModuleName           string         `json:"module_name,omitempty"`
	ModuleID             string         `json:"module_id,omitempty"`
	ModuleDefinition     map[string]any `json:"module_definition,omitempty"`
	SourceExperimentID   string         `json:"source_experiment_id,omitempty"`
	MemoryID             string         `json:"memory_id,omitempty"`
	MemoryAction         string         `json:"memory_action,omitempty"`
	MemoryKind           string         `json:"memory_kind,omitempty"`
	MemoryContent        string         `json:"memory_content,omitempty"`
	MemoryConfidence     *float64       `json:"memory_confidence,omitempty"`
	MemoryUrgency        *float64       `json:"memory_urgency,omitempty"`
	MemoryUncertainty    string         `json:"memory_uncertainty,omitempty"`
	ProjectTitle         string         `json:"project_title,omitempty"`
	ProjectDescription   string         `json:"project_description,omitempty"`
	StableCore           map[string]any `json:"stable_core,omitempty"`
	RelationshipContract map[string]any `json:"relationship_contract,omitempty"`
	GrowthProfile        map[string]any `json:"growth_profile,omitempty"`
	ExpressionProfile    map[string]any `json:"expression_profile,omitempty"`
	Interests            []string       `json:"interests,omitempty"`
	ChangeReason         string         `json:"change_reason,omitempty"`
}

type lifeProposalResponse struct {
	ID               string          `json:"id"`
	ProposalType     string          `json:"proposal_type"`
	Status           string          `json:"status"`
	Title            string          `json:"title"`
	Summary          string          `json:"summary"`
	Payload          json.RawMessage `json:"payload"`
	ExpiresAt        *string         `json:"expires_at"`
	ConfirmedAt      *string         `json:"confirmed_at"`
	ExecutedAt       *string         `json:"executed_at"`
	ExecutionReceipt json.RawMessage `json:"execution_receipt,omitempty"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
}

func lifeProposalToResponse(proposal db.LifeActionProposal) lifeProposalResponse {
	return lifeProposalResponse{
		ID:               uuidToString(proposal.ID),
		ProposalType:     proposal.ProposalType,
		Status:           proposal.Status,
		Title:            proposal.Title,
		Summary:          proposal.Summary,
		Payload:          json.RawMessage(proposal.Payload),
		ExpiresAt:        timestampToPtr(proposal.ExpiresAt),
		ConfirmedAt:      timestampToPtr(proposal.ConfirmedAt),
		ExecutedAt:       timestampToPtr(proposal.ExecutedAt),
		ExecutionReceipt: json.RawMessage(proposal.ExecutionReceipt),
		CreatedAt:        timestampToString(proposal.CreatedAt),
		UpdatedAt:        timestampToString(proposal.UpdatedAt),
	}
}

func validateLifeProposalPayload(w http.ResponseWriter, proposalType, title string, payload lifeProposalPayload) bool {
	if strings.TrimSpace(title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return false
	}
	switch proposalType {
	case "workspace_issue":
		if strings.TrimSpace(payload.IssueTitle) == "" {
			writeError(w, http.StatusBadRequest, "issue_title is required")
			return false
		}
		return true
	case "agent_action":
		if strings.TrimSpace(payload.ActionTitle) == "" || strings.TrimSpace(payload.ActionInstructions) == "" {
			writeError(w, http.StatusBadRequest, "action_title and action_instructions are required")
			return false
		}
		return true
	case "module_adoption":
		if strings.TrimSpace(payload.ModuleName) == "" || payload.ModuleDefinition == nil {
			writeError(w, http.StatusBadRequest, "module_name and module_definition are required")
			return false
		}
		return true
	case "project_create":
		if strings.TrimSpace(payload.ProjectTitle) == "" {
			writeError(w, http.StatusBadRequest, "project_title is required")
			return false
		}
		return true
	case "memory_change":
		if strings.TrimSpace(payload.MemoryID) == "" {
			writeError(w, http.StatusBadRequest, "memory_id is required")
			return false
		}
		switch payload.MemoryAction {
		case "confirm", "archive":
			return true
		case "downgrade":
			if !validLifeMemoryKind(payload.MemoryKind) {
				writeError(w, http.StatusBadRequest, "memory_kind is required for downgrade")
				return false
			}
			return true
		case "correct":
			if payload.MemoryConfidence == nil || payload.MemoryUrgency == nil || !validateLifeMemoryContent(w, payload.MemoryKind, payload.MemoryContent, *payload.MemoryConfidence, *payload.MemoryUrgency) {
				return false
			}
			return true
		default:
			writeError(w, http.StatusBadRequest, "memory_action must be confirm, correct, downgrade or archive")
			return false
		}
	case "identity_change":
		if payload.StableCore == nil || payload.RelationshipContract == nil || payload.GrowthProfile == nil || payload.ExpressionProfile == nil || strings.TrimSpace(payload.ChangeReason) == "" {
			writeError(w, http.StatusBadRequest, "identity profiles and change_reason are required")
			return false
		}
		return true
	case "experiment_start", "experiment_extend":
	default:
		writeError(w, http.StatusBadRequest, "unsupported proposal_type")
		return false
	}
	if strings.TrimSpace(payload.Problem) == "" || strings.TrimSpace(payload.Hypothesis) == "" {
		writeError(w, http.StatusBadRequest, "title, problem and hypothesis are required")
		return false
	}
	if proposalType == "experiment_extend" && strings.TrimSpace(payload.ExperimentID) == "" {
		writeError(w, http.StatusBadRequest, "experiment_id is required for rerun proposals")
		return false
	}
	start, err := time.Parse(time.RFC3339, payload.StartsAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "starts_at must be RFC3339")
		return false
	}
	end, err := time.Parse(time.RFC3339, payload.EndsAt)
	if err != nil || !end.After(start) {
		writeError(w, http.StatusBadRequest, "ends_at must be RFC3339 and after starts_at")
		return false
	}
	return true
}

func (h *Handler) CreateLifeActionProposal(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	profile, err := h.Queries.GetCompanionProfile(r.Context(), db.GetCompanionProfileParams{
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "configure a companion before creating proposals")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load companion")
		return
	}
	var req struct {
		ProposalType string              `json:"proposal_type"`
		Title        string              `json:"title"`
		Summary      string              `json:"summary"`
		Payload      lifeProposalPayload `json:"payload"`
		ExpiresAt    string              `json:"expires_at"`
	}
	if !decodeRequiredJSON(w, r, &req) || !validateLifeProposalPayload(w, req.ProposalType, req.Title, req.Payload) {
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
		writeError(w, http.StatusInternalServerError, "failed to start proposal transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	proposal, err := qtx.CreateLifeActionProposal(r.Context(), db.CreateLifeActionProposalParams{
		WorkspaceID:      scope.workspaceID,
		UserID:           scope.userID,
		CompanionAgentID: profile.AgentID,
		ProposalType:     req.ProposalType,
		Status:           "pending_confirmation",
		Title:            strings.TrimSpace(req.Title),
		Summary:          strings.TrimSpace(req.Summary),
		Payload:          payload,
		ExpiresAt:        expiresAt,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create proposal")
		return
	}
	event, err := recordLifeChangedTx(r.Context(), qtx, scope, "member", scope.userID,
		"action_proposal", uuidToString(proposal.ID), "created", map[string]any{"proposal_type": proposal.ProposalType})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record proposal event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit proposal")
		return
	}
	h.publishLifeEvents(event)
	writeJSON(w, http.StatusCreated, lifeProposalToResponse(proposal))
}

func (h *Handler) ListLifeActionProposals(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListLifeActionProposals(r.Context(), db.ListLifeActionProposalsParams{
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list proposals")
		return
	}
	items := make([]lifeProposalResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, lifeProposalToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposals": items})
}

type lifeExperimentRoundResponse struct {
	ID              string           `json:"id"`
	ExperimentID    string           `json:"experiment_id"`
	PreviousRoundID *string          `json:"previous_round_id"`
	ProposalID      *string          `json:"proposal_id"`
	IssueID         *string          `json:"issue_id"`
	Status          string           `json:"status"`
	Plan            json.RawMessage  `json:"plan"`
	StartsAt        *string          `json:"starts_at"`
	EndsAt          *string          `json:"ends_at"`
	StoppedAt       *string          `json:"stopped_at"`
	StopReason      string           `json:"stop_reason"`
	Review          json.RawMessage  `json:"review,omitempty"`
	ReviewDraft     json.RawMessage  `json:"review_draft,omitempty"`
	Observations    []map[string]any `json:"observations"`
	ReviewedAt      *string          `json:"reviewed_at"`
	CreatedAt       string           `json:"created_at"`
}

func lifeExperimentRoundToResponse(round db.LifeExperimentRound) lifeExperimentRoundResponse {
	return lifeExperimentRoundResponse{
		ID:              uuidToString(round.ID),
		ExperimentID:    uuidToString(round.ExperimentID),
		PreviousRoundID: uuidToPtr(round.PreviousRoundID),
		ProposalID:      uuidToPtr(round.ProposalID),
		IssueID:         uuidToPtr(round.IssueID),
		Status:          round.Status,
		Plan:            json.RawMessage(round.Plan),
		StartsAt:        timestampToPtr(round.StartsAt),
		EndsAt:          timestampToPtr(round.EndsAt),
		StoppedAt:       timestampToPtr(round.StoppedAt),
		StopReason:      round.StopReason,
		Review:          json.RawMessage(round.Review),
		ReviewDraft:     json.RawMessage(round.ReviewDraft),
		Observations:    []map[string]any{},
		ReviewedAt:      timestampToPtr(round.ReviewedAt),
		CreatedAt:       timestampToString(round.CreatedAt),
	}
}

func (h *Handler) ConfirmLifeActionProposal(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	proposalID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "proposalId"), "proposal id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start proposal transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	proposal, err := qtx.GetLifeActionProposalForUpdate(r.Context(), db.GetLifeActionProposalForUpdateParams{
		ID:          proposalID,
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
	})
	if err != nil {
		writeEntityLoadError(w, err, "proposal", "proposal_id", chi.URLParam(r, "proposalId"))
		return
	}
	if proposal.Status != "pending_confirmation" {
		writeError(w, http.StatusConflict, "proposal is not awaiting confirmation")
		return
	}
	var payload lifeProposalPayload
	if err := json.Unmarshal(proposal.Payload, &payload); err != nil || !validateLifeProposalPayload(w, proposal.ProposalType, proposal.Title, payload) {
		return
	}
	if proposal.ProposalType == "memory_change" {
		memoryID, parseErr := parseUUIDText(payload.MemoryID)
		if parseErr != nil {
			writeError(w, 400, "invalid memory_id")
			return
		}
		memory, err := qtx.GetLifeMemory(r.Context(), db.GetLifeMemoryParams{ID: memoryID, WorkspaceID: scope.workspaceID, UserID: scope.userID})
		if err != nil {
			writeError(w, 400, "memory not found")
			return
		}
		changeType := payload.MemoryAction
		switch payload.MemoryAction {
		case "confirm":
			memory, err = qtx.ConfirmLifeMemory(r.Context(), db.ConfirmLifeMemoryParams{ID: memory.ID, WorkspaceID: scope.workspaceID, UserID: scope.userID, ConfirmedByID: scope.userID})
			changeType = "confirmed"
		case "correct":
			memory, err = qtx.UpdateLifeMemoryContent(r.Context(), db.UpdateLifeMemoryContentParams{ID: memory.ID, WorkspaceID: scope.workspaceID, UserID: scope.userID, Kind: payload.MemoryKind, Content: strings.TrimSpace(payload.MemoryContent), Confidence: *payload.MemoryConfidence, Urgency: *payload.MemoryUrgency, Uncertainty: strings.TrimSpace(payload.MemoryUncertainty), ValidFrom: memory.ValidFrom, ValidTo: memory.ValidTo})
			changeType = "corrected"
		case "downgrade":
			if lifeMemoryKindRank[payload.MemoryKind] >= lifeMemoryKindRank[memory.Kind] {
				writeError(w, 400, "memory_kind must be lower confidence than the current kind")
				return
			}
			memory, err = qtx.DowngradeLifeMemory(r.Context(), db.DowngradeLifeMemoryParams{ID: memory.ID, WorkspaceID: scope.workspaceID, UserID: scope.userID, Kind: payload.MemoryKind})
			changeType = "downgraded"
		case "archive":
			memory, err = qtx.ArchiveLifeMemory(r.Context(), db.ArchiveLifeMemoryParams{ID: memory.ID, WorkspaceID: scope.workspaceID, UserID: scope.userID})
			changeType = "archived"
		}
		if err != nil {
			writeError(w, 500, "failed to apply confirmed memory change")
			return
		}
		if err := createLifeMemoryRevision(r.Context(), qtx, memory, changeType, proposal.Summary, "member", scope.userID); err != nil {
			writeError(w, 500, "failed to record memory revision")
			return
		}
		receipt, _ := json.Marshal(map[string]any{"memory_id": uuidToString(memory.ID), "action": payload.MemoryAction, "status": memory.Status})
		if _, err = qtx.MarkLifeActionProposalExecuted(r.Context(), db.MarkLifeActionProposalExecutedParams{ID: proposal.ID, WorkspaceID: scope.workspaceID, UserID: scope.userID, ExecutionReceipt: receipt}); err != nil {
			writeError(w, 500, "failed to finalize proposal")
			return
		}
		event, err := recordAndCommitLifeChanged(r.Context(), tx, qtx, scope, "memory", uuidToString(memory.ID), "proposal_executed", map[string]any{"proposal_id": uuidToString(proposal.ID), "action": payload.MemoryAction})
		if err != nil {
			writeError(w, 500, "failed to commit memory change")
			return
		}
		h.publishLifeEvents(event)
		writeJSON(w, http.StatusOK, map[string]any{"memory_id": uuidToString(memory.ID), "status": memory.Status})
		return
	}
	if proposal.ProposalType == "identity_change" {
		stableCore, _ := json.Marshal(payload.StableCore)
		relationshipContract, _ := json.Marshal(payload.RelationshipContract)
		growthProfile, _ := json.Marshal(payload.GrowthProfile)
		expressionProfile, _ := json.Marshal(payload.ExpressionProfile)
		interests, _ := json.Marshal(payload.Interests)
		version, err := qtx.GetNextLifeIdentityVersion(r.Context(), db.GetNextLifeIdentityVersionParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
		if err != nil {
			writeError(w, 500, "failed to reserve identity version")
			return
		}
		if err = qtx.SupersedeActiveLifeIdentity(r.Context(), db.SupersedeActiveLifeIdentityParams{WorkspaceID: scope.workspaceID, UserID: scope.userID}); err != nil {
			writeError(w, 500, "failed to supersede current identity")
			return
		}
		identity, err := qtx.CreateLifeIdentityVersion(r.Context(), db.CreateLifeIdentityVersionParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, Version: version, Status: "active", StableCore: stableCore, RelationshipContract: relationshipContract, GrowthProfile: growthProfile, ExpressionProfile: expressionProfile, Interests: interests, ChangeReason: strings.TrimSpace(payload.ChangeReason), ConfirmedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}, ConfirmedByID: scope.userID})
		if err != nil {
			writeError(w, 500, "failed to create confirmed identity version")
			return
		}
		if err = qtx.SetCompanionCurrentIdentity(r.Context(), db.SetCompanionCurrentIdentityParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, CurrentIdentityVersionID: identity.ID}); err != nil {
			writeError(w, 500, "failed to activate identity version")
			return
		}
		receipt, _ := json.Marshal(map[string]any{"identity_version_id": uuidToString(identity.ID), "version": version})
		if _, err = qtx.MarkLifeActionProposalExecuted(r.Context(), db.MarkLifeActionProposalExecutedParams{ID: proposal.ID, WorkspaceID: scope.workspaceID, UserID: scope.userID, ExecutionReceipt: receipt}); err != nil {
			writeError(w, 500, "failed to finalize proposal")
			return
		}
		event, err := recordAndCommitLifeChanged(r.Context(), tx, qtx, scope, "identity", uuidToString(identity.ID), "proposal_executed", map[string]any{"proposal_id": uuidToString(proposal.ID), "version": version})
		if err != nil {
			writeError(w, 500, "failed to commit identity change")
			return
		}
		h.publishLifeEvents(event)
		writeJSON(w, http.StatusCreated, map[string]any{"identity_version_id": uuidToString(identity.ID), "version": version})
		return
	}
	if proposal.ProposalType == "project_create" {
		project, err := qtx.CreateProject(r.Context(), db.CreateProjectParams{WorkspaceID: scope.workspaceID, Title: strings.TrimSpace(payload.ProjectTitle), Description: pgtype.Text{String: strings.TrimSpace(payload.ProjectDescription), Valid: strings.TrimSpace(payload.ProjectDescription) != ""}, Icon: pgtype.Text{}, Status: "planned", LeadType: pgtype.Text{String: "member", Valid: true}, LeadID: scope.userID, Priority: "none"})
		if err != nil {
			writeError(w, 500, "failed to create confirmed project")
			return
		}
		receipt, _ := json.Marshal(map[string]any{"project_id": uuidToString(project.ID)})
		if _, err = qtx.MarkLifeActionProposalExecuted(r.Context(), db.MarkLifeActionProposalExecutedParams{ID: proposal.ID, WorkspaceID: scope.workspaceID, UserID: scope.userID, ExecutionReceipt: receipt}); err != nil {
			writeError(w, 500, "failed to finalize proposal")
			return
		}
		event, err := recordAndCommitLifeChanged(r.Context(), tx, qtx, scope, "project", uuidToString(project.ID), "proposal_executed", map[string]any{"proposal_id": uuidToString(proposal.ID)})
		if err != nil {
			writeError(w, 500, "failed to commit project")
			return
		}
		h.publishLifeEvents(event)
		writeJSON(w, http.StatusCreated, map[string]any{"project_id": uuidToString(project.ID)})
		return
	}
	if proposal.ProposalType == "agent_action" {
		agent, err := qtx.GetAgent(r.Context(), proposal.CompanionAgentID)
		if err != nil || agent.ArchivedAt.Valid {
			writeError(w, 409, "companion agent is unavailable")
			return
		}
		issueNumber, err := qtx.IncrementIssueCounter(r.Context(), scope.workspaceID)
		if err != nil {
			writeError(w, 500, "failed to reserve action number")
			return
		}
		position, err := issueposition.NextTopPosition(r.Context(), tx, scope.workspaceID, "todo")
		if err != nil {
			writeError(w, 500, "failed to position confirmed action")
			return
		}
		issue, err := qtx.CreateIssueWithOrigin(r.Context(), db.CreateIssueWithOriginParams{
			ID:          dbid.NewV7(),
			WorkspaceID: scope.workspaceID, Title: strings.TrimSpace(payload.ActionTitle),
			Description: pgtype.Text{String: strings.TrimSpace(payload.ActionInstructions), Valid: true},
			Status:      "todo", Priority: "none", AssigneeType: pgtype.Text{String: "agent", Valid: true}, AssigneeID: proposal.CompanionAgentID,
			CreatorType: "member", CreatorID: scope.userID, Position: position, Number: issueNumber,
		})
		if err != nil {
			writeError(w, 500, "failed to create confirmed action")
			return
		}
		task, err := qtx.CreateAgentTask(r.Context(), db.CreateAgentTaskParams{
			ID:      dbid.NewV7(),
			AgentID: proposal.CompanionAgentID, RuntimeID: agent.RuntimeID, IssueID: issue.ID, Priority: 5,
			TriggerSummary: pgtype.Text{String: "用户已确认的人生搭子现实动作", Valid: true}, ForceFreshSession: pgtype.Bool{Bool: true, Valid: true},
		})
		if err != nil {
			writeError(w, 500, "failed to queue confirmed action")
			return
		}
		queuedEvent, err := service.RecordTaskQueuedEventTx(r.Context(), qtx, uuidToString(scope.workspaceID), task)
		if err != nil {
			writeError(w, 500, "failed to record confirmed action")
			return
		}
		receipt, _ := json.Marshal(map[string]any{"issue_id": uuidToString(issue.ID), "task_id": uuidToString(task.ID)})
		if _, err = qtx.MarkLifeActionProposalExecuted(r.Context(), db.MarkLifeActionProposalExecutedParams{ID: proposal.ID, WorkspaceID: scope.workspaceID, UserID: scope.userID, ExecutionReceipt: receipt}); err != nil {
			writeError(w, 500, "failed to finalize confirmed action")
			return
		}
		event, err := recordAndCommitLifeChanged(r.Context(), tx, qtx, scope, "agent_action", uuidToString(issue.ID), "proposal_executed", map[string]any{"proposal_id": uuidToString(proposal.ID), "task_id": uuidToString(task.ID)})
		if err != nil {
			writeError(w, 500, "failed to commit confirmed action")
			return
		}
		h.publishLifeEvents(event, queuedEvent)
		writeJSON(w, http.StatusCreated, map[string]any{"issue_id": uuidToString(issue.ID), "task_id": uuidToString(task.ID)})
		return
	}
	if proposal.ProposalType == "module_adoption" {
		sourceExperimentID := pgtype.UUID{}
		if payload.SourceExperimentID != "" {
			sourceExperimentID, err = parseUUIDText(payload.SourceExperimentID)
			if err != nil {
				writeError(w, 400, "invalid source_experiment_id")
				return
			}
		}
		var module db.LifeModule
		if payload.ModuleID == "" {
			module, err = qtx.CreateLifeModule(r.Context(), db.CreateLifeModuleParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, SourceExperimentID: sourceExperimentID, Name: strings.TrimSpace(payload.ModuleName), Status: "active"})
		} else {
			moduleID, parseErr := parseUUIDText(payload.ModuleID)
			if parseErr != nil {
				writeError(w, 400, "invalid module_id")
				return
			}
			module, err = qtx.GetLifeModuleForUser(r.Context(), db.GetLifeModuleForUserParams{ID: moduleID, WorkspaceID: scope.workspaceID, UserID: scope.userID})
		}
		if err != nil {
			writeError(w, 500, "failed to prepare life module")
			return
		}
		definition, _ := json.Marshal(payload.ModuleDefinition)
		version, err := qtx.GetNextLifeModuleVersion(r.Context(), module.ID)
		if err != nil {
			writeError(w, 500, "failed to reserve life module version")
			return
		}
		if _, err = qtx.CreateLifeModuleVersion(r.Context(), db.CreateLifeModuleVersionParams{ModuleID: module.ID, Version: version, Definition: definition, ChangeReason: proposal.Summary, ConfirmedByID: scope.userID}); err != nil {
			writeError(w, 500, "failed to create life module version")
			return
		}
		if err = qtx.SetLifeModuleCurrentVersion(r.Context(), db.SetLifeModuleCurrentVersionParams{ID: module.ID, CurrentVersion: version}); err != nil {
			writeError(w, 500, "failed to activate life module version")
			return
		}
		receipt, _ := json.Marshal(map[string]any{"module_id": uuidToString(module.ID), "version": version, "status": "active"})
		if _, err = qtx.MarkLifeActionProposalExecuted(r.Context(), db.MarkLifeActionProposalExecutedParams{ID: proposal.ID, WorkspaceID: scope.workspaceID, UserID: scope.userID, ExecutionReceipt: receipt}); err != nil {
			writeError(w, 500, "failed to finalize proposal")
			return
		}
		event, err := recordAndCommitLifeChanged(r.Context(), tx, qtx, scope, "module", uuidToString(module.ID), "proposal_executed", map[string]any{"proposal_id": uuidToString(proposal.ID), "version": version})
		if err != nil {
			writeError(w, 500, "failed to commit life module")
			return
		}
		h.publishLifeEvents(event)
		writeJSON(w, http.StatusCreated, map[string]any{"module_id": uuidToString(module.ID), "status": module.Status})
		return
	}
	if proposal.ProposalType == "workspace_issue" {
		issueNumber, err := qtx.IncrementIssueCounter(r.Context(), scope.workspaceID)
		if err != nil {
			writeError(w, 500, "failed to reserve task number")
			return
		}
		position, err := issueposition.NextTopPosition(r.Context(), tx, scope.workspaceID, "todo")
		if err != nil {
			writeError(w, 500, "failed to position task")
			return
		}
		issue, err := qtx.CreateIssueWithOrigin(r.Context(), db.CreateIssueWithOriginParams{ID: dbid.NewV7(), WorkspaceID: scope.workspaceID, Title: strings.TrimSpace(payload.IssueTitle), Description: pgtype.Text{String: strings.TrimSpace(payload.IssueDescription), Valid: strings.TrimSpace(payload.IssueDescription) != ""}, Status: "todo", Priority: "none", AssigneeType: pgtype.Text{String: "member", Valid: true}, AssigneeID: scope.userID, CreatorType: "member", CreatorID: scope.userID, Position: position, Number: issueNumber})
		if err != nil {
			writeError(w, 500, "failed to create confirmed task")
			return
		}
		receipt, _ := json.Marshal(map[string]any{"issue_id": uuidToString(issue.ID)})
		if _, err = qtx.MarkLifeActionProposalExecuted(r.Context(), db.MarkLifeActionProposalExecutedParams{ID: proposal.ID, WorkspaceID: scope.workspaceID, UserID: scope.userID, ExecutionReceipt: receipt}); err != nil {
			writeError(w, 500, "failed to finalize proposal")
			return
		}
		event, err := recordAndCommitLifeChanged(r.Context(), tx, qtx, scope, "issue", uuidToString(issue.ID), "proposal_executed", map[string]any{"proposal_id": uuidToString(proposal.ID)})
		if err != nil {
			writeError(w, 500, "failed to commit confirmed task")
			return
		}
		h.publishLifeEvents(event)
		writeJSON(w, http.StatusCreated, map[string]any{"issue_id": uuidToString(issue.ID)})
		return
	}
	startsAt, _ := time.Parse(time.RFC3339, payload.StartsAt)
	endsAt, _ := time.Parse(time.RFC3339, payload.EndsAt)
	method := payload.Method
	if method == nil {
		method = map[string]any{}
	}
	plan := payload.Plan
	if plan == nil {
		plan = map[string]any{}
	}
	methodJSON, _ := json.Marshal(method)
	planJSON, _ := json.Marshal(plan)

	var experiment db.LifeExperiment
	if proposal.ProposalType == "experiment_start" {
		experiment, err = qtx.CreateLifeExperiment(r.Context(), db.CreateLifeExperimentParams{
			WorkspaceID:   scope.workspaceID,
			UserID:        scope.userID,
			Title:         proposal.Title,
			Problem:       strings.TrimSpace(payload.Problem),
			Hypothesis:    strings.TrimSpace(payload.Hypothesis),
			Method:        methodJSON,
			CreatedByType: "member",
			CreatedByID:   scope.userID,
		})
	} else {
		experimentID, valid := parseUUIDOrBadRequest(w, payload.ExperimentID, "experiment_id")
		if !valid {
			return
		}
		experiment, err = qtx.GetLifeExperiment(r.Context(), db.GetLifeExperimentParams{
			ID: experimentID, WorkspaceID: scope.workspaceID, UserID: scope.userID,
		})
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "experiment could not be prepared")
		return
	}

	issueNumber, err := qtx.IncrementIssueCounter(r.Context(), scope.workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reserve task number")
		return
	}
	position, err := issueposition.NextTopPosition(r.Context(), tx, scope.workspaceID, "todo")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to position experiment task")
		return
	}
	issueTitle := strings.TrimSpace(payload.IssueTitle)
	if issueTitle == "" {
		issueTitle = proposal.Title
	}
	issue, err := qtx.CreateIssueWithOrigin(r.Context(), db.CreateIssueWithOriginParams{
		ID:          dbid.NewV7(),
		WorkspaceID: scope.workspaceID,
		Title:       issueTitle,
		Description: pgtype.Text{String: strings.TrimSpace(payload.IssueDescription), Valid: strings.TrimSpace(payload.IssueDescription) != ""},
		Status:      "todo", Priority: "none",
		AssigneeType: pgtype.Text{String: "member", Valid: true}, AssigneeID: scope.userID,
		CreatorType: "member", CreatorID: scope.userID,
		ParentIssueID: pgtype.UUID{}, Position: position, StartDate: pgtype.Date{}, DueDate: pgtype.Date{},
		Number: issueNumber, ProjectID: pgtype.UUID{}, OriginType: pgtype.Text{}, OriginID: pgtype.UUID{},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create experiment task")
		return
	}
	previousRoundID := pgtype.UUID{}
	if strings.TrimSpace(payload.PreviousRoundID) != "" {
		previousRoundID, ok = parseUUIDOrBadRequest(w, payload.PreviousRoundID, "previous_round_id")
		if !ok {
			return
		}
		previous, err := qtx.GetLifeExperimentRoundForUser(r.Context(), db.GetLifeExperimentRoundForUserParams{
			ID: previousRoundID, WorkspaceID: scope.workspaceID, UserID: scope.userID,
		})
		if err != nil || previous.LifeExperimentRound.ExperimentID != experiment.ID {
			writeError(w, http.StatusBadRequest, "previous round not found for this experiment")
			return
		}
	}
	round, err := qtx.CreateLifeExperimentRound(r.Context(), db.CreateLifeExperimentRoundParams{
		ExperimentID: experiment.ID, PreviousRoundID: previousRoundID,
		ProposalID: proposal.ID, IssueID: issue.ID, Status: "running", Plan: planJSON,
		StartsAt:    pgtype.Timestamptz{Time: startsAt, Valid: true},
		EndsAt:      pgtype.Timestamptz{Time: endsAt, Valid: true},
		ConfirmedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}, ConfirmedByID: scope.userID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create experiment round")
		return
	}
	for _, rawMemoryID := range payload.MemoryIDs {
		memoryID, valid := parseUUIDOrBadRequest(w, rawMemoryID, "memory_id")
		if !valid {
			return
		}
		if _, err := qtx.GetLifeMemory(r.Context(), db.GetLifeMemoryParams{
			ID: memoryID, WorkspaceID: scope.workspaceID, UserID: scope.userID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "memory not found")
			return
		}
		if err := qtx.LinkLifeExperimentMemory(r.Context(), db.LinkLifeExperimentMemoryParams{
			RoundID: round.ID, MemoryID: memoryID, Role: "input",
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to link experiment memory")
			return
		}
	}
	receipt, _ := json.Marshal(map[string]any{"experiment_id": uuidToString(experiment.ID), "issue_id": uuidToString(issue.ID), "round_id": uuidToString(round.ID)})
	if _, err := qtx.MarkLifeActionProposalExecuted(r.Context(), db.MarkLifeActionProposalExecutedParams{
		ID: proposal.ID, WorkspaceID: scope.workspaceID, UserID: scope.userID, ExecutionReceipt: receipt,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to finalize proposal")
		return
	}
	event, err := recordAndCommitLifeChanged(r.Context(), tx, qtx, scope, "experiment_round", uuidToString(round.ID), "proposal_executed", map[string]any{
		"proposal_id": uuidToString(proposal.ID), "experiment_id": uuidToString(experiment.ID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit experiment")
		return
	}
	h.publishLifeEvents(event)
	writeJSON(w, http.StatusCreated, map[string]any{
		"experiment_id": uuidToString(experiment.ID),
		"issue_id":      uuidToString(issue.ID),
		"round":         lifeExperimentRoundToResponse(round),
	})
}

type lifeExperimentResponse struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Problem    string          `json:"problem"`
	Hypothesis string          `json:"hypothesis"`
	Method     json.RawMessage `json:"method"`
	CreatedAt  string          `json:"created_at"`
}

func (h *Handler) ListLifeExperiments(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	if _, err := h.Queries.StopExpiredLifeExperimentRounds(r.Context(), db.StopExpiredLifeExperimentRoundsParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to stop expired experiment rounds")
		return
	}
	experiments, err := h.Queries.ListLifeExperiments(r.Context(), db.ListLifeExperimentsParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list experiments")
		return
	}
	rounds, err := h.Queries.ListLifeExperimentRounds(r.Context(), db.ListLifeExperimentRoundsParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list experiment rounds")
		return
	}
	experimentItems := make([]lifeExperimentResponse, 0, len(experiments))
	for _, experiment := range experiments {
		experimentItems = append(experimentItems, lifeExperimentResponse{
			ID: uuidToString(experiment.ID), Title: experiment.Title, Problem: experiment.Problem,
			Hypothesis: experiment.Hypothesis, Method: json.RawMessage(experiment.Method), CreatedAt: timestampToString(experiment.CreatedAt),
		})
	}
	roundItems := make([]lifeExperimentRoundResponse, 0, len(rounds))
	for _, round := range rounds {
		item := lifeExperimentRoundToResponse(round)
		observations, err := h.Queries.ListLifeExperimentObservations(r.Context(), round.ID)
		if err != nil {
			writeError(w, 500, "failed to list experiment observations")
			return
		}
		for _, observation := range observations {
			item.Observations = append(item.Observations, map[string]any{"id": uuidToString(observation.ID), "observation_type": observation.ObservationType, "content": observation.Content, "captured_by": observation.CapturedBy, "observed_at": timestampToString(observation.ObservedAt)})
		}
		roundItems = append(roundItems, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"experiments": experimentItems, "rounds": roundItems})
}

func (h *Handler) StopLifeExperimentRound(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	roundID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "roundId"), "round id")
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "stopped_by_user"
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start experiment stop")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	round, err := qtx.StopLifeExperimentRound(r.Context(), db.StopLifeExperimentRoundParams{
		ID: roundID, WorkspaceID: scope.workspaceID, UserID: scope.userID, StopReason: reason,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "only running rounds can be stopped")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to stop experiment round")
		return
	}
	event, err := recordLifeChangedTx(r.Context(), qtx, scope, "member", scope.userID,
		"experiment_round", uuidToString(round.ID), "stopped", map[string]any{"reason": reason})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record experiment event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit experiment stop")
		return
	}
	h.publishLifeEvents(event)
	writeJSON(w, http.StatusOK, lifeExperimentRoundToResponse(round))
}

func (h *Handler) ReviewLifeExperimentRound(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	roundID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "roundId"), "round id")
	if !ok {
		return
	}
	var req struct {
		Outcome             string `json:"outcome"`
		Feelings            string `json:"feelings"`
		Burden              string `json:"burden"`
		CompanionCorrection string `json:"companion_correction"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Outcome) == "" || strings.TrimSpace(req.Feelings) == "" || strings.TrimSpace(req.Burden) == "" || strings.TrimSpace(req.CompanionCorrection) == "" {
		writeError(w, http.StatusBadRequest, "outcome, feelings, burden and companion_correction are required")
		return
	}
	review, _ := json.Marshal(req)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to start experiment review")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	q := h.Queries.WithTx(tx)
	before, err := q.GetLifeExperimentRoundForUser(r.Context(), db.GetLifeExperimentRoundForUserParams{ID: roundID, WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		writeError(w, 404, "experiment round not found")
		return
	}
	round, err := q.ReviewLifeExperimentRound(r.Context(), db.ReviewLifeExperimentRoundParams{
		ID: roundID, WorkspaceID: scope.workspaceID, UserID: scope.userID, Review: review,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "round must stop before review")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to review experiment round")
		return
	}
	if len(before.LifeExperimentRound.ReviewDraft) > 0 {
		var draft struct {
			ModuleProposal map[string]any `json:"module_proposal"`
		}
		if json.Unmarshal(before.LifeExperimentRound.ReviewDraft, &draft) == nil && len(draft.ModuleProposal) > 0 {
			name, _ := draft.ModuleProposal["module_name"].(string)
			if name == "" {
				name, _ = draft.ModuleProposal["name"].(string)
			}
			definition, _ := draft.ModuleProposal["module_definition"].(map[string]any)
			if definition == nil {
				definition, _ = draft.ModuleProposal["definition"].(map[string]any)
			}
			if strings.TrimSpace(name) != "" && definition != nil {
				profile, profileErr := q.GetCompanionProfile(r.Context(), db.GetCompanionProfileParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
				if profileErr != nil {
					writeError(w, 500, "failed to load companion for module proposal")
					return
				}
				payload, _ := json.Marshal(map[string]any{"module_name": name, "module_definition": definition, "source_experiment_id": uuidToString(before.LifeExperimentRound.ExperimentID)})
				if _, err := q.CreateLifeActionProposal(r.Context(), db.CreateLifeActionProposalParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, CompanionAgentID: profile.AgentID, ProposalType: "module_adoption", Status: "pending_confirmation", Title: "沉淀模块：" + name, Summary: "由已确认的实验复盘提出，仍需再次确认后才会启用。", Payload: payload}); err != nil {
					writeError(w, 500, "failed to create module proposal")
					return
				}
			}
		}
	}
	event, err := recordLifeChangedTx(r.Context(), q, scope, "member", scope.userID,
		"experiment_round", uuidToString(round.ID), "reviewed", map[string]any{"reviewed": true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record experiment review event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to commit experiment review")
		return
	}
	h.publishLifeEvents(event)
	writeJSON(w, http.StatusOK, lifeExperimentRoundToResponse(round))
}
