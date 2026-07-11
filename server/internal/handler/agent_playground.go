package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const agentPlaygroundMaxInputs = 20

type AgentPlaygroundExperimentResponse struct {
	ID               string  `json:"id"`
	WorkspaceID      string  `json:"workspace_id"`
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	DatasetAssetID   *string `json:"dataset_asset_id"`
	DatasetVersionID *string `json:"dataset_version_id"`
	JudgeAgentID     *string `json:"judge_agent_id"`
	Status           string  `json:"status"`
	CreatedBy        *string `json:"created_by"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
	InputCount       int32   `json:"input_count"`
	AgentCount       int32   `json:"agent_count"`
}

type AgentPlaygroundInputResponse struct {
	ID           string         `json:"id"`
	RowIndex     int32          `json:"row_index"`
	Name         string         `json:"name"`
	Input        string         `json:"input"`
	Variables    map[string]any `json:"variables"`
	Expected     string         `json:"expected"`
	DatasetRowID *string        `json:"dataset_row_id"`
	CreatedAt    string         `json:"created_at"`
}

type AgentPlaygroundAgentResponse struct {
	ID           string  `json:"id"`
	AgentID      string  `json:"agent_id"`
	AgentName    string  `json:"agent_name"`
	AgentModel   *string `json:"agent_model"`
	DisplayOrder int32   `json:"display_order"`
}

type AgentPlaygroundResultResponse struct {
	ID                string  `json:"id"`
	InputID           string  `json:"input_id"`
	ExperimentAgentID string  `json:"experiment_agent_id"`
	AgentID           string  `json:"agent_id"`
	ChatSessionID     *string `json:"chat_session_id"`
	TaskID            *string `json:"task_id"`
	RenderedInput     string  `json:"rendered_input"`
	Status            string  `json:"status"`
	Output            string  `json:"output"`
	Error             string  `json:"error"`
	StartedAt         *string `json:"started_at"`
	CompletedAt       *string `json:"completed_at"`
	UpdatedAt         string  `json:"updated_at"`
}

type AgentPlaygroundJudgementResponse struct {
	ID            string  `json:"id"`
	InputID       string  `json:"input_id"`
	JudgeAgentID  string  `json:"judge_agent_id"`
	ChatSessionID *string `json:"chat_session_id"`
	TaskID        *string `json:"task_id"`
	Status        string  `json:"status"`
	Output        string  `json:"output"`
	UpdatedAt     string  `json:"updated_at"`
}

type AgentPlaygroundDetailResponse struct {
	Experiment AgentPlaygroundExperimentResponse  `json:"experiment"`
	Inputs     []AgentPlaygroundInputResponse     `json:"inputs"`
	Agents     []AgentPlaygroundAgentResponse     `json:"agents"`
	Results    []AgentPlaygroundResultResponse    `json:"results"`
	Judgements []AgentPlaygroundJudgementResponse `json:"judgements"`
}

type CreateAgentPlaygroundExperimentRequest struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	DatasetAssetID   string   `json:"dataset_asset_id"`
	DatasetVersionID string   `json:"dataset_version_id"`
	JudgeAgentID     string   `json:"judge_agent_id"`
	AgentIDs         []string `json:"agent_ids"`
}

type SetAgentPlaygroundJudgeRequest struct {
	JudgeAgentID string `json:"judge_agent_id"`
}

func (h *Handler) ListAgentPlaygroundExperiments(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	rows, err := h.Queries.ListAgentPlaygroundExperiments(r.Context(), db.ListAgentPlaygroundExperimentsParams{
		WorkspaceID: workspaceUUID,
		Limit:       50,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent playground experiments")
		return
	}
	resp := make([]AgentPlaygroundExperimentResponse, 0, len(rows))
	for _, row := range rows {
		resp = append(resp, agentPlaygroundExperimentRowToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) GetAgentPlaygroundExperiment(w http.ResponseWriter, r *http.Request) {
	detail, ok := h.agentPlaygroundDetail(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) CreateAgentPlaygroundExperiment(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var req CreateAgentPlaygroundExperimentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.AgentIDs) == 0 {
		writeError(w, http.StatusBadRequest, "agent_ids is required")
		return
	}

	agentIDs := make([]pgtype.UUID, 0, len(req.AgentIDs))
	seenAgents := map[string]bool{}
	for _, rawID := range req.AgentIDs {
		rawID = strings.TrimSpace(rawID)
		if rawID == "" || seenAgents[rawID] {
			continue
		}
		agentID, ok := parseUUIDOrBadRequest(w, rawID, "agent_id")
		if !ok {
			return
		}
		agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: workspaceUUID})
		if err != nil {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		if agent.ArchivedAt.Valid {
			writeError(w, http.StatusBadRequest, "agent is archived")
			return
		}
		actorType, actorID := h.resolveActor(r, userID, workspaceID)
		if !h.canAccessPersonalAgent(r.Context(), agent, actorType, actorID, workspaceID) {
			writeError(w, http.StatusForbidden, "you do not have access to this agent")
			return
		}
		seenAgents[rawID] = true
		agentIDs = append(agentIDs, agentID)
	}
	if len(agentIDs) == 0 {
		writeError(w, http.StatusBadRequest, "agent_ids is required")
		return
	}

	var datasetAssetID pgtype.UUID
	var datasetVersionID pgtype.UUID
	var datasetRows []db.PromptEvaluationDatasetVersionRow
	if strings.TrimSpace(req.DatasetAssetID) == "" {
		writeError(w, http.StatusBadRequest, "dataset_asset_id is required")
		return
	}
	if strings.TrimSpace(req.DatasetVersionID) == "" {
		writeError(w, http.StatusBadRequest, "dataset_version_id is required")
		return
	}
	parsedAssetID, ok := parseUUIDOrBadRequest(w, req.DatasetAssetID, "dataset_asset_id")
	if !ok {
		return
	}
	parsedVersionID, ok := parseUUIDOrBadRequest(w, req.DatasetVersionID, "dataset_version_id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetPromptEvaluationDatasetVersionInAsset(r.Context(), db.GetPromptEvaluationDatasetVersionInAssetParams{
		WorkspaceID:    workspaceUUID,
		DatasetAssetID: parsedAssetID,
		ID:             parsedVersionID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "dataset version not found")
		return
	}
	rows, err := h.Queries.ListPromptEvaluationDatasetVersionRows(r.Context(), db.ListPromptEvaluationDatasetVersionRowsParams{
		WorkspaceID:      workspaceUUID,
		DatasetVersionID: parsedVersionID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load dataset rows")
		return
	}
	if len(rows) == 0 {
		writeError(w, http.StatusBadRequest, "dataset snapshot has no rows")
		return
	}
	if len(rows) > agentPlaygroundMaxInputs {
		rows = rows[:agentPlaygroundMaxInputs]
	}
	datasetAssetID = parsedAssetID
	datasetVersionID = parsedVersionID
	datasetRows = rows

	var judgeAgentID pgtype.UUID
	if strings.TrimSpace(req.JudgeAgentID) != "" {
		parsedJudgeID, ok := parseUUIDOrBadRequest(w, req.JudgeAgentID, "judge_agent_id")
		if !ok {
			return
		}
		if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: parsedJudgeID, WorkspaceID: workspaceUUID}); err != nil {
			writeError(w, http.StatusNotFound, "judge agent not found")
			return
		}
		judgeAgentID = parsedJudgeID
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start agent playground transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)

	experiment, err := qtx.CreateAgentPlaygroundExperiment(r.Context(), db.CreateAgentPlaygroundExperimentParams{
		WorkspaceID:      workspaceUUID,
		Name:             req.Name,
		Description:      strings.TrimSpace(req.Description),
		DatasetAssetID:   datasetAssetID,
		DatasetVersionID: datasetVersionID,
		JudgeAgentID:     judgeAgentID,
		Status:           "ready",
		CreatedBy:        parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create agent playground experiment")
		return
	}
	for i, agentID := range agentIDs {
		if _, err := qtx.CreateAgentPlaygroundAgent(r.Context(), db.CreateAgentPlaygroundAgentParams{
			ExperimentID: experiment.ID,
			WorkspaceID:  workspaceUUID,
			AgentID:      agentID,
			DisplayOrder: int32(i),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create experiment agents")
			return
		}
	}
	for i, row := range datasetRows {
		vars := jsonObjectBytes(row.Variables)
		if _, err := qtx.CreateAgentPlaygroundInput(r.Context(), db.CreateAgentPlaygroundInputParams{
			ExperimentID: experiment.ID,
			WorkspaceID:  workspaceUUID,
			RowIndex:     int32(i),
			Input:        datasetRowInput(row),
			DatasetRowID: row.ID,
			Name:         row.RowName,
			Variables:    vars,
			Expected:     expectedJSONText(row.Expected),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create experiment inputs")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent playground experiment")
		return
	}

	detail, err := h.loadAgentPlaygroundDetail(r.Context(), experiment.ID, workspaceUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent playground experiment")
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

func (h *Handler) RunAgentPlaygroundExperiment(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	detail, ok := h.agentPlaygroundDetail(w, r)
	if !ok {
		return
	}
	workspaceID := parseUUID(detail.Experiment.WorkspaceID)
	experimentID := parseUUID(detail.Experiment.ID)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start agent playground run")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	queuedTasks := make([]db.AgentTaskQueue, 0, len(detail.Inputs)*len(detail.Agents))
	lockedAgents := make(map[string]db.Agent, len(detail.Agents))

	for _, input := range detail.Inputs {
		for _, experimentAgent := range detail.Agents {
			rendered := input.Input
			result, err := qtx.CreateAgentPlaygroundResult(r.Context(), db.CreateAgentPlaygroundResultParams{
				ExperimentID:      experimentID,
				InputID:           parseUUID(input.ID),
				ExperimentAgentID: parseUUID(experimentAgent.ID),
				WorkspaceID:       workspaceID,
				AgentID:           parseUUID(experimentAgent.AgentID),
				RenderedInput:     rendered,
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to create experiment result")
				return
			}
			if result.TaskID.Valid {
				continue
			}

			agentID := experimentAgent.AgentID
			agent, exists := lockedAgents[agentID]
			if !exists {
				agent, err = qtx.LockAgentInWorkspaceForChat(r.Context(), db.LockAgentInWorkspaceForChatParams{
					ID:          parseUUID(agentID),
					WorkspaceID: workspaceID,
				})
				if err != nil {
					writeError(w, http.StatusInternalServerError, "failed to load experiment agent")
					return
				}
				lockedAgents[agentID] = agent
			}
			if agent.ArchivedAt.Valid || !agent.RuntimeID.Valid {
				reason := "agent is archived"
				if !agent.RuntimeID.Valid {
					reason = "agent has no runtime"
				}
				if _, err := qtx.SyncAgentPlaygroundResult(r.Context(), db.SyncAgentPlaygroundResultParams{
					ID:          result.ID,
					WorkspaceID: workspaceID,
					Status:      "failed",
					Error:       pgtype.Text{String: reason, Valid: true},
				}); err != nil {
					writeError(w, http.StatusInternalServerError, "failed to record unavailable experiment agent")
					return
				}
				continue
			}

			session, err := qtx.CreateChatSession(r.Context(), db.CreateChatSessionParams{
				WorkspaceID: workspaceID,
				AgentID:     parseUUID(experimentAgent.AgentID),
				CreatorID:   parseUUID(userID),
				Title:       fmt.Sprintf("Agent 调试场 · %s · %s", detail.Experiment.Name, input.Name),
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to create playground chat session")
				return
			}
			msg, err := qtx.CreateChatMessage(r.Context(), db.CreateChatMessageParams{
				ChatSessionID: session.ID,
				Role:          "user",
				Content:       rendered,
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to create playground chat message")
				return
			}
			task, err := h.TaskService.CreateChatTaskInTx(r.Context(), qtx, session, agent, parseUUID(userID))
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to create experiment task")
				return
			}
			if err := qtx.LinkChatMessageToTask(r.Context(), db.LinkChatMessageToTaskParams{ID: msg.ID, TaskID: task.ID}); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to link experiment message")
				return
			}
			if _, err := qtx.StartAgentPlaygroundResult(r.Context(), db.StartAgentPlaygroundResultParams{
				ID:            result.ID,
				WorkspaceID:   workspaceID,
				ChatSessionID: session.ID,
				TaskID:        task.ID,
				Status:        task.Status,
			}); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to update experiment result")
				return
			}
			queuedTasks = append(queuedTasks, task)
		}
	}
	if _, err := qtx.UpdateAgentPlaygroundExperimentStatus(r.Context(), db.UpdateAgentPlaygroundExperimentStatusParams{
		ID:          experimentID,
		WorkspaceID: workspaceID,
		Status:      "running",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update experiment status")
		return
	}
	refreshed, err := loadAgentPlaygroundDetailWithQueries(r.Context(), qtx, experimentID, workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load committed playground run")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent playground run")
		return
	}
	for _, task := range queuedTasks {
		h.TaskService.PublishChatTaskEnqueued(r.Context(), task)
	}
	writeJSON(w, http.StatusAccepted, refreshed)
}

func (h *Handler) SyncAgentPlaygroundExperiment(w http.ResponseWriter, r *http.Request) {
	experiment, ok := h.loadAgentPlaygroundExperiment(w, r)
	if !ok {
		return
	}
	detail, err := h.syncAgentPlaygroundExperiment(r, experiment.ID, experiment.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync agent playground experiment")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) JudgeAgentPlaygroundExperiment(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	experiment, ok := h.loadAgentPlaygroundExperiment(w, r)
	if !ok {
		return
	}
	var req SetAgentPlaygroundJudgeRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.JudgeAgentID) != "" {
		judgeID, ok := parseUUIDOrBadRequest(w, req.JudgeAgentID, "judge_agent_id")
		if !ok {
			return
		}
		updated, err := h.Queries.SetAgentPlaygroundJudgeAgent(r.Context(), db.SetAgentPlaygroundJudgeAgentParams{
			ID:           experiment.ID,
			WorkspaceID:  experiment.WorkspaceID,
			JudgeAgentID: judgeID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to set judge agent")
			return
		}
		experiment = updated
	}
	if !experiment.JudgeAgentID.Valid {
		writeError(w, http.StatusBadRequest, "judge_agent_id is required")
		return
	}
	synced, err := h.syncAgentPlaygroundExperiment(r, experiment.ID, experiment.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync before judge")
		return
	}
	resultByInput := map[string][]AgentPlaygroundResultResponse{}
	for _, result := range synced.Results {
		if result.Status == "completed" {
			resultByInput[result.InputID] = append(resultByInput[result.InputID], result)
		}
	}
	for _, input := range synced.Inputs {
		results := resultByInput[input.ID]
		if len(results) == 0 {
			continue
		}
		judgement, err := h.Queries.CreateAgentPlaygroundJudgement(r.Context(), db.CreateAgentPlaygroundJudgementParams{
			ExperimentID: experiment.ID,
			InputID:      parseUUID(input.ID),
			WorkspaceID:  experiment.WorkspaceID,
			JudgeAgentID: experiment.JudgeAgentID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create judgement")
			return
		}
		if judgement.TaskID.Valid {
			continue
		}
		message := buildAgentPlaygroundJudgeMessage(input, synced.Agents, results)
		session, err := h.Queries.CreateChatSession(r.Context(), db.CreateChatSessionParams{
			WorkspaceID: experiment.WorkspaceID,
			AgentID:     experiment.JudgeAgentID,
			CreatorID:   parseUUID(userID),
			Title:       fmt.Sprintf("Agent 调试场裁判 · %s · %s", synced.Experiment.Name, input.Name),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create judge chat session")
			return
		}
		msg, err := h.Queries.CreateChatMessage(r.Context(), db.CreateChatMessageParams{
			ChatSessionID: session.ID,
			Role:          "user",
			Content:       message,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create judge chat message")
			return
		}
		task, err := h.TaskService.EnqueueChatTask(r.Context(), session, parseUUID(userID))
		if err != nil {
			_, _ = h.Queries.SyncAgentPlaygroundJudgement(r.Context(), db.SyncAgentPlaygroundJudgementParams{
				ID:          judgement.ID,
				WorkspaceID: experiment.WorkspaceID,
				Status:      "failed",
				Output:      pgtype.Text{String: err.Error(), Valid: true},
			})
			continue
		}
		_ = h.Queries.LinkChatMessageToTask(r.Context(), db.LinkChatMessageToTaskParams{ID: msg.ID, TaskID: task.ID})
		if _, err := h.Queries.StartAgentPlaygroundJudgement(r.Context(), db.StartAgentPlaygroundJudgementParams{
			ID:            judgement.ID,
			WorkspaceID:   experiment.WorkspaceID,
			ChatSessionID: session.ID,
			TaskID:        task.ID,
			Status:        task.Status,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update judgement")
			return
		}
	}
	refreshed, err := h.syncAgentPlaygroundExperiment(r, experiment.ID, experiment.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync judgement")
		return
	}
	writeJSON(w, http.StatusAccepted, refreshed)
}

func (h *Handler) loadAgentPlaygroundExperiment(w http.ResponseWriter, r *http.Request) (db.AgentPlaygroundExperiment, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return db.AgentPlaygroundExperiment{}, false
	}
	experimentID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "experiment id")
	if !ok {
		return db.AgentPlaygroundExperiment{}, false
	}
	experiment, err := h.Queries.GetAgentPlaygroundExperiment(r.Context(), db.GetAgentPlaygroundExperimentParams{
		ID:          experimentID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "agent playground experiment not found")
			return db.AgentPlaygroundExperiment{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load agent playground experiment")
		return db.AgentPlaygroundExperiment{}, false
	}
	return experiment, true
}

func (h *Handler) agentPlaygroundDetail(w http.ResponseWriter, r *http.Request) (AgentPlaygroundDetailResponse, bool) {
	experiment, ok := h.loadAgentPlaygroundExperiment(w, r)
	if !ok {
		return AgentPlaygroundDetailResponse{}, false
	}
	detail, err := h.loadAgentPlaygroundDetail(r.Context(), experiment.ID, experiment.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent playground detail")
		return AgentPlaygroundDetailResponse{}, false
	}
	return detail, true
}

func (h *Handler) loadAgentPlaygroundDetail(ctx context.Context, experimentID, workspaceID pgtype.UUID) (AgentPlaygroundDetailResponse, error) {
	return loadAgentPlaygroundDetailWithQueries(ctx, h.Queries, experimentID, workspaceID)
}

func loadAgentPlaygroundDetailWithQueries(ctx context.Context, queries *db.Queries, experimentID, workspaceID pgtype.UUID) (AgentPlaygroundDetailResponse, error) {
	experiment, err := queries.GetAgentPlaygroundExperiment(ctx, db.GetAgentPlaygroundExperimentParams{ID: experimentID, WorkspaceID: workspaceID})
	if err != nil {
		return AgentPlaygroundDetailResponse{}, err
	}
	inputs, err := queries.ListAgentPlaygroundInputs(ctx, db.ListAgentPlaygroundInputsParams{ExperimentID: experimentID, WorkspaceID: workspaceID})
	if err != nil {
		return AgentPlaygroundDetailResponse{}, err
	}
	agents, err := queries.ListAgentPlaygroundAgents(ctx, db.ListAgentPlaygroundAgentsParams{ExperimentID: experimentID, WorkspaceID: workspaceID})
	if err != nil {
		return AgentPlaygroundDetailResponse{}, err
	}
	results, err := queries.ListAgentPlaygroundResults(ctx, db.ListAgentPlaygroundResultsParams{ExperimentID: experimentID, WorkspaceID: workspaceID})
	if err != nil {
		return AgentPlaygroundDetailResponse{}, err
	}
	judgements, err := queries.ListAgentPlaygroundJudgements(ctx, db.ListAgentPlaygroundJudgementsParams{ExperimentID: experimentID, WorkspaceID: workspaceID})
	if err != nil {
		return AgentPlaygroundDetailResponse{}, err
	}

	inputResp := make([]AgentPlaygroundInputResponse, 0, len(inputs))
	for _, input := range inputs {
		inputResp = append(inputResp, agentPlaygroundInputToResponse(input))
	}
	agentResp := make([]AgentPlaygroundAgentResponse, 0, len(agents))
	for _, agent := range agents {
		agentResp = append(agentResp, agentPlaygroundAgentToResponse(agent))
	}
	resultResp := make([]AgentPlaygroundResultResponse, 0, len(results))
	for _, result := range results {
		resultResp = append(resultResp, agentPlaygroundResultToResponse(result))
	}
	judgementResp := make([]AgentPlaygroundJudgementResponse, 0, len(judgements))
	for _, judgement := range judgements {
		judgementResp = append(judgementResp, agentPlaygroundJudgementToResponse(judgement))
	}
	return AgentPlaygroundDetailResponse{
		Experiment: agentPlaygroundExperimentToResponse(experiment, int32(len(inputs)), int32(len(agents))),
		Inputs:     inputResp,
		Agents:     agentResp,
		Results:    resultResp,
		Judgements: judgementResp,
	}, nil
}

func (h *Handler) syncAgentPlaygroundExperiment(r *http.Request, experimentID, workspaceID pgtype.UUID) (AgentPlaygroundDetailResponse, error) {
	results, err := h.Queries.ListAgentPlaygroundResults(r.Context(), db.ListAgentPlaygroundResultsParams{ExperimentID: experimentID, WorkspaceID: workspaceID})
	if err != nil {
		return AgentPlaygroundDetailResponse{}, err
	}
	completedResults := 0
	totalResults := len(results)
	for _, result := range results {
		if !result.TaskID.Valid {
			continue
		}
		task, err := h.Queries.GetAgentTask(r.Context(), result.TaskID)
		if err != nil {
			continue
		}
		status := task.Status
		output := pgtype.Text{}
		errText := pgtype.Text{}
		completedAt := pgtype.Timestamptz{}
		if isAgentPlaygroundTerminalStatus(status) {
			completedResults++
			completedAt = task.CompletedAt
			if task.Error.Valid {
				errText = task.Error
			}
			if result.ChatSessionID.Valid {
				if assistant := h.latestAssistantMessage(r, result.ChatSessionID); assistant != "" {
					output = pgtype.Text{String: assistant, Valid: true}
				}
			}
		}
		_, _ = h.Queries.SyncAgentPlaygroundResult(r.Context(), db.SyncAgentPlaygroundResultParams{
			ID:          result.ID,
			WorkspaceID: workspaceID,
			Status:      status,
			Output:      output,
			Error:       errText,
			CompletedAt: completedAt,
		})
	}

	judgements, err := h.Queries.ListAgentPlaygroundJudgements(r.Context(), db.ListAgentPlaygroundJudgementsParams{ExperimentID: experimentID, WorkspaceID: workspaceID})
	if err != nil {
		return AgentPlaygroundDetailResponse{}, err
	}
	for _, judgement := range judgements {
		if !judgement.TaskID.Valid {
			continue
		}
		task, err := h.Queries.GetAgentTask(r.Context(), judgement.TaskID)
		if err != nil {
			continue
		}
		output := pgtype.Text{}
		if isAgentPlaygroundTerminalStatus(task.Status) && judgement.ChatSessionID.Valid {
			if assistant := h.latestAssistantMessage(r, judgement.ChatSessionID); assistant != "" {
				output = pgtype.Text{String: assistant, Valid: true}
			}
		}
		_, _ = h.Queries.SyncAgentPlaygroundJudgement(r.Context(), db.SyncAgentPlaygroundJudgementParams{
			ID:          judgement.ID,
			WorkspaceID: workspaceID,
			Status:      task.Status,
			Output:      output,
		})
	}

	if totalResults > 0 && completedResults >= totalResults {
		_, _ = h.Queries.UpdateAgentPlaygroundExperimentStatus(r.Context(), db.UpdateAgentPlaygroundExperimentStatusParams{ID: experimentID, WorkspaceID: workspaceID, Status: "completed"})
	}
	return h.loadAgentPlaygroundDetail(r.Context(), experimentID, workspaceID)
}

func (h *Handler) latestAssistantMessage(r *http.Request, chatSessionID pgtype.UUID) string {
	messages, err := h.Queries.ListChatMessages(r.Context(), chatSessionID)
	if err != nil {
		return ""
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return messages[i].Content
		}
	}
	return ""
}

func agentPlaygroundExperimentRowToResponse(row db.ListAgentPlaygroundExperimentsRow) AgentPlaygroundExperimentResponse {
	return AgentPlaygroundExperimentResponse{
		ID:               uuidToString(row.ID),
		WorkspaceID:      uuidToString(row.WorkspaceID),
		Name:             row.Name,
		Description:      row.Description,
		DatasetAssetID:   uuidToPtr(row.DatasetAssetID),
		DatasetVersionID: uuidToPtr(row.DatasetVersionID),
		JudgeAgentID:     uuidToPtr(row.JudgeAgentID),
		Status:           row.Status,
		CreatedBy:        uuidToPtr(row.CreatedBy),
		CreatedAt:        timestampToString(row.CreatedAt),
		UpdatedAt:        timestampToString(row.UpdatedAt),
		InputCount:       row.InputCount,
		AgentCount:       row.AgentCount,
	}
}

func agentPlaygroundExperimentToResponse(experiment db.AgentPlaygroundExperiment, inputCount, agentCount int32) AgentPlaygroundExperimentResponse {
	return AgentPlaygroundExperimentResponse{
		ID:               uuidToString(experiment.ID),
		WorkspaceID:      uuidToString(experiment.WorkspaceID),
		Name:             experiment.Name,
		Description:      experiment.Description,
		DatasetAssetID:   uuidToPtr(experiment.DatasetAssetID),
		DatasetVersionID: uuidToPtr(experiment.DatasetVersionID),
		JudgeAgentID:     uuidToPtr(experiment.JudgeAgentID),
		Status:           experiment.Status,
		CreatedBy:        uuidToPtr(experiment.CreatedBy),
		CreatedAt:        timestampToString(experiment.CreatedAt),
		UpdatedAt:        timestampToString(experiment.UpdatedAt),
		InputCount:       inputCount,
		AgentCount:       agentCount,
	}
}

func agentPlaygroundInputToResponse(input db.AgentPlaygroundInput) AgentPlaygroundInputResponse {
	var variables map[string]any
	_ = json.Unmarshal(input.Variables, &variables)
	if variables == nil {
		variables = map[string]any{}
	}
	return AgentPlaygroundInputResponse{
		ID:           uuidToString(input.ID),
		RowIndex:     input.RowIndex,
		Name:         input.Name,
		Input:        input.Input,
		Variables:    variables,
		Expected:     input.Expected,
		DatasetRowID: uuidToPtr(input.DatasetRowID),
		CreatedAt:    timestampToString(input.CreatedAt),
	}
}

func agentPlaygroundAgentToResponse(agent db.ListAgentPlaygroundAgentsRow) AgentPlaygroundAgentResponse {
	return AgentPlaygroundAgentResponse{
		ID:           uuidToString(agent.ID),
		AgentID:      uuidToString(agent.AgentID),
		AgentName:    agent.AgentName,
		AgentModel:   textToPtr(agent.AgentModel),
		DisplayOrder: agent.DisplayOrder,
	}
}

func agentPlaygroundResultToResponse(result db.AgentPlaygroundResult) AgentPlaygroundResultResponse {
	return AgentPlaygroundResultResponse{
		ID:                uuidToString(result.ID),
		InputID:           uuidToString(result.InputID),
		ExperimentAgentID: uuidToString(result.ExperimentAgentID),
		AgentID:           uuidToString(result.AgentID),
		ChatSessionID:     uuidToPtr(result.ChatSessionID),
		TaskID:            uuidToPtr(result.TaskID),
		RenderedInput:     result.RenderedInput,
		Status:            result.Status,
		Output:            result.Output,
		Error:             result.Error,
		StartedAt:         timeToPtr(result.StartedAt),
		CompletedAt:       timeToPtr(result.CompletedAt),
		UpdatedAt:         timestampToString(result.UpdatedAt),
	}
}

func agentPlaygroundJudgementToResponse(judgement db.AgentPlaygroundJudgement) AgentPlaygroundJudgementResponse {
	return AgentPlaygroundJudgementResponse{
		ID:            uuidToString(judgement.ID),
		InputID:       uuidToString(judgement.InputID),
		JudgeAgentID:  uuidToString(judgement.JudgeAgentID),
		ChatSessionID: uuidToPtr(judgement.ChatSessionID),
		TaskID:        uuidToPtr(judgement.TaskID),
		Status:        judgement.Status,
		Output:        judgement.Output,
		UpdatedAt:     timestampToString(judgement.UpdatedAt),
	}
}

func timeToPtr(value pgtype.Timestamptz) *string {
	if !value.Valid {
		return nil
	}
	v := timestampToString(value)
	return &v
}

func jsonObjectBytes(raw []byte) []byte {
	if len(raw) == 0 {
		return []byte(`{}`)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil && obj != nil {
		return raw
	}
	return []byte(`{}`)
}

func datasetRowInput(row db.PromptEvaluationDatasetVersionRow) string {
	var variables map[string]any
	_ = json.Unmarshal(row.Variables, &variables)
	for _, key := range []string{"input", "用户输入", "需求", "question", "content", "text"} {
		if value, ok := variables[key]; ok {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
				return text
			}
		}
	}
	if row.RowName != "" {
		return row.RowName
	}
	if len(row.Variables) > 0 {
		return string(row.Variables)
	}
	return fmt.Sprintf("用例 %d", row.RowIndex+1)
}

func expectedJSONText(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	switch v := value.(type) {
	case string:
		return v
	default:
		buf, _ := json.MarshalIndent(v, "", "  ")
		return string(buf)
	}
}

func isAgentPlaygroundTerminalStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

func buildAgentPlaygroundJudgeMessage(input AgentPlaygroundInputResponse, agents []AgentPlaygroundAgentResponse, results []AgentPlaygroundResultResponse) string {
	agentName := map[string]string{}
	for _, agent := range agents {
		agentName[agent.AgentID] = agent.AgentName
	}
	var b strings.Builder
	b.WriteString("你是 Agent 调试场的裁判。请基于同一个输入，对比多个 Agent 的输出质量。\n")
	b.WriteString("请用中文输出：1. 总体结论；2. 每个 Agent 的优缺点；3. 推荐选择；4. 如果期望不满足，请说明原因。\n\n")
	b.WriteString("<输入>\n")
	b.WriteString(input.Input)
	b.WriteString("\n</输入>\n\n")
	if strings.TrimSpace(input.Expected) != "" {
		b.WriteString("<期望>\n")
		b.WriteString(input.Expected)
		b.WriteString("\n</期望>\n\n")
	}
	for _, result := range results {
		name := agentName[result.AgentID]
		if name == "" {
			name = result.AgentID
		}
		b.WriteString("<Agent name=\"")
		b.WriteString(name)
		b.WriteString("\">\n")
		b.WriteString(result.Output)
		b.WriteString("\n</Agent>\n\n")
	}
	return b.String()
}
