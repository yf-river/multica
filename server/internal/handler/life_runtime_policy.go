package handler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	lifePrimaryRuntimeProvider  = "codebuddy"
	lifePrimaryModel            = "deepseek-v4-pro-ioa"
	lifeFallbackRuntimeProvider = "codex"
	lifeFallbackModel           = "gpt-5.6-luna"
)

// lifeAgentModelSelection describes the only two model pairs that the Life
// system may bind.  A Life agent without one of these pairs is not partially
// configured: refusing the binding is safer than silently running a different
// model and making later relationship evidence incomparable.
type lifeAgentModelSelection struct {
	Provider string
	Model    string
	SetModel bool
}

func (h *Handler) lifeAgentSelection(ctx context.Context, agent db.Agent, workspaceID pgtype.UUID) (lifeAgentModelSelection, error) {
	return h.lifeAgentSelectionWithQueries(ctx, h.Queries, agent, workspaceID)
}

func (h *Handler) lifeAgentSelectionWithQueries(ctx context.Context, queries *db.Queries, agent db.Agent, workspaceID pgtype.UUID) (lifeAgentModelSelection, error) {
	if agent.RuntimeID == (pgtype.UUID{}) || !agent.RuntimeID.Valid {
		return lifeAgentModelSelection{}, fmt.Errorf("life agent must be bound to a runtime")
	}
	runtime, err := queries.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{
		ID: agent.RuntimeID, WorkspaceID: workspaceID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return lifeAgentModelSelection{}, fmt.Errorf("life agent runtime is unavailable")
		}
		return lifeAgentModelSelection{}, fmt.Errorf("load life agent runtime: %w", err)
	}
	provider := strings.ToLower(strings.TrimSpace(runtime.Provider))
	model := strings.TrimSpace(agent.Model.String)
	switch provider {
	case lifePrimaryRuntimeProvider:
		if model == "" {
			return lifeAgentModelSelection{Provider: provider, Model: lifePrimaryModel, SetModel: true}, nil
		}
		if model != lifePrimaryModel {
			return lifeAgentModelSelection{}, fmt.Errorf("Life requires %s/%s; agent is configured for %s", lifePrimaryRuntimeProvider, lifePrimaryModel, model)
		}
	case lifeFallbackRuntimeProvider:
		if model != lifeFallbackModel {
			return lifeAgentModelSelection{}, fmt.Errorf("Life fallback requires %s/%s", lifeFallbackRuntimeProvider, lifeFallbackModel)
		}
	default:
		return lifeAgentModelSelection{}, fmt.Errorf("Life requires %s/%s or %s/%s; runtime provider %q is unsupported", lifePrimaryRuntimeProvider, lifePrimaryModel, lifeFallbackRuntimeProvider, lifeFallbackModel, provider)
	}
	return lifeAgentModelSelection{Provider: provider, Model: model}, nil
}

// enforceLifeAgentSelection validates a bound agent and fills the primary
// model when the runtime itself was selected but its model was left empty.
// It intentionally changes only agents already participating in Life.
func (h *Handler) enforceLifeAgentSelection(ctx context.Context, agent db.Agent, workspaceID pgtype.UUID) (db.Agent, error) {
	selection, err := h.lifeAgentSelection(ctx, agent, workspaceID)
	if err != nil {
		return db.Agent{}, err
	}
	if !selection.SetModel {
		return agent, nil
	}
	changed, err := h.Queries.SetLifeAgentModel(ctx, db.SetLifeAgentModelParams{
		AgentID: agent.ID, WorkspaceID: workspaceID,
		Model: pgtype.Text{String: selection.Model, Valid: true},
	})
	if err != nil {
		return db.Agent{}, fmt.Errorf("set Life model: %w", err)
	}
	if changed != 1 {
		return db.Agent{}, fmt.Errorf("life agent disappeared while setting its model")
	}
	agent.Model = pgtype.Text{String: selection.Model, Valid: true}
	return agent, nil
}

// applyLifeAgentSelections validates every participating Life agent before it
// writes any model. Binding a companion is one relationship change: a bad
// observer must not leave the companion silently rewritten to another model.
func (h *Handler) applyLifeAgentSelections(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, agentIDs []pgtype.UUID) (map[string]db.Agent, error) {
	unique := make(map[string]pgtype.UUID, len(agentIDs))
	for _, id := range agentIDs {
		if id.Valid {
			unique[util.UUIDToString(id)] = id
		}
	}
	ids := make([]pgtype.UUID, 0, len(unique))
	for _, id := range unique {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return util.UUIDToString(ids[i]) < util.UUIDToString(ids[j]) })
	agents := make(map[string]db.Agent, len(ids))
	selections := make(map[string]lifeAgentModelSelection, len(ids))
	for _, id := range ids {
		agent, err := q.LockAgentForLifeBinding(ctx, db.LockAgentForLifeBindingParams{AgentID: id, WorkspaceID: workspaceID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("life agent is unavailable")
			}
			return nil, fmt.Errorf("lock life agent: %w", err)
		}
		selection, err := h.lifeAgentSelectionWithQueries(ctx, q, agent, workspaceID)
		if err != nil {
			return nil, err
		}
		key := util.UUIDToString(id)
		agents[key] = agent
		selections[key] = selection
	}
	if len(ids) > 0 {
		first := agents[util.UUIDToString(ids[0])]
		firstSelection := selections[util.UUIDToString(ids[0])]
		for _, id := range ids[1:] {
			agent := agents[util.UUIDToString(id)]
			selection := selections[util.UUIDToString(id)]
			if agent.RuntimeID != first.RuntimeID || selection.Model != firstSelection.Model {
				return nil, fmt.Errorf("companion runtime and model must match every observer")
			}
		}
	}
	for _, id := range ids {
		key := util.UUIDToString(id)
		selection := selections[key]
		if !selection.SetModel {
			continue
		}
		changed, err := q.SetLifeAgentModel(ctx, db.SetLifeAgentModelParams{
			AgentID: id, WorkspaceID: workspaceID,
			Model: pgtype.Text{String: selection.Model, Valid: true},
		})
		if err != nil {
			return nil, fmt.Errorf("set Life model: %w", err)
		}
		if changed != 1 {
			return nil, fmt.Errorf("life agent disappeared while setting its model")
		}
		agent := agents[key]
		agent.Model = pgtype.Text{String: selection.Model, Valid: true}
		agents[key] = agent
	}
	return agents, nil
}
