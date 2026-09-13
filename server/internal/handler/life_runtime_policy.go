// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（life_runtime_policy.go）定义 Life 系统允许绑定 agent 的"运行时+
// 模型"白名单策略：只允许主（codebuddy/deepseek-v4-pro-ioa）与兜底
// （codex/gpt-5.6-luna）两对组合。Life 的 agent 要么绑定到这两对之一，
// 要么干脆不参与 Life——拒绝绑定比悄悄跑不同模型更安全，否则后续关系证据
// 会失去可比性。
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

// 主/兜底两对受支持的（provider, model）组合常量。
const (
	lifePrimaryRuntimeProvider  = "codebuddy"
	lifePrimaryModel            = "deepseek-v4-pro-ioa"
	lifeFallbackRuntimeProvider = "codex"
	lifeFallbackModel           = "gpt-5.6-luna"
)

// lifeAgentModelSelection 描述 Life 系统允许绑定的仅有两对模型组合。
// Life agent 若不满足其中一对，就是"未完全配置"：拒绝绑定比静默换模型更
// 安全——换模型会让后续关系证据失去可比性。
type lifeAgentModelSelection struct {
	Provider string
	Model    string
	SetModel bool
}

// lifeAgentSelection 用默认查询器委托给带查询器参数的实现。
func (h *Handler) lifeAgentSelection(ctx context.Context, agent db.Agent, workspaceID pgtype.UUID) (lifeAgentModelSelection, error) {
	return h.lifeAgentSelectionWithQueries(ctx, h.Queries, agent, workspaceID)
}

// lifeAgentSelectionWithQueries 校验 agent 的运行时 provider/模型是否命中
// 白名单：主 provider 且模型为空时允许"回填默认主模型"（SetModel=true）；
// 其他任何组合不匹配都返回错误，绝不静默放行。
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

// enforceLifeAgentSelection 校验已绑定的 agent：当只选了运行时、模型留空时
// 回填主模型。它刻意只改动已参与 Life 的 agent。
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

// applyLifeAgentSelections 在写任何模型之前先逐个校验参与 Life 的 agent。
// 绑定 companion 是一次关系变更：不能让某个坏观察者把 companion 悄悄改写
// 成别的模型。
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
