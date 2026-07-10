package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) EnsureInternalSquadTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var req struct {
		TemplateKey     string  `json:"template_key"`
		RuntimeProvider string  `json:"runtime_provider"`
		Model           string  `json:"model"`
		Scope           string  `json:"scope"`
		Name            *string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	template, ok := internalSquadTemplateByKey(req.TemplateKey)
	if !ok {
		writeError(w, http.StatusBadRequest, "template_key must be user-center or multica-coding")
		return
	}
	if model := strings.TrimSpace(req.Model); model != "" {
		template.Model = model
	}
	customName := false
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		template.Name = name
		customName = true
	}
	provider := normalizeProvider(req.RuntimeProvider)
	if provider == "" {
		provider = internalSquadDefaultProvider
	}
	scope, validScope := normalizeSquadScope(req.Scope)
	if !validScope {
		writeError(w, http.StatusBadRequest, "scope must be 'workspace' or 'personal'")
		return
	}

	agentScope := internalSquadAgentScope(scope)
	runtime, ok := h.selectInternalSquadRuntime(w, r, wsUUID, member, provider, agentScope)
	if !ok {
		return
	}
	if err := validateAgentRuntimeScope(agentScope, member.UserID, runtime); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agents, err := h.ensureInternalSquadAgents(r.Context(), wsUUID, member.UserID, runtime, template, scope)
	if err != nil {
		slog.Warn("ensure internal squad agents failed", append(logger.RequestAttrs(r), "error", err, "template", template.Key)...)
		writeError(w, http.StatusInternalServerError, "failed to create internal squad agents")
		return
	}
	squad, err := h.ensureInternalSquad(r.Context(), wsUUID, member.UserID, template, scope, agents, customName)
	if err != nil {
		slog.Warn("ensure internal squad failed", append(logger.RequestAttrs(r), "error", err, "template", template.Key)...)
		writeError(w, http.StatusInternalServerError, "failed to create internal squad")
		return
	}
	resp, err := h.squadToResponseWithPreview(r.Context(), squad)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load internal squad")
		return
	}
	writeJSON(w, http.StatusOK, InternalSquadTemplateResponse{Squad: resp, Agents: agents})
}

func (h *Handler) selectInternalSquadRuntime(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, member db.Member, provider string, agentScope string) (db.AgentRuntime, bool) {
	runtimes, err := h.Queries.ListAgentRuntimes(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtimes")
		return db.AgentRuntime{}, false
	}
	checkedAt := time.Now().UTC()
	provider = normalizeProvider(provider)
	if provider == "" {
		provider = internalSquadDefaultProvider
	}
	providerName := provider
	if len(providerName) > 0 {
		providerName = strings.ToUpper(providerName[:1]) + providerName[1:]
	}
	var best *db.AgentRuntime
	for i := range runtimes {
		runtime := runtimes[i]
		if !strings.EqualFold(runtime.Provider, provider) || !canUseRuntimeForAgent(member, runtime) {
			continue
		}
		if !agentRuntimeScopeCompatible(agentScope, member.UserID, runtime) {
			continue
		}
		if best == nil || runtimeReadinessRank(runtime, checkedAt) > runtimeReadinessRank(*best, checkedAt) {
			best = &runtime
		}
	}
	if best == nil {
		writeError(w, http.StatusServiceUnavailable, "当前 workspace 没有可用于"+internalSquadRuntimeScopeLabel(agentScope)+"小队的 "+providerName+" runtime，无法创建真实可执行的内部小队。请先启动 multica daemon，并确认 /api/runtimes 出现 provider="+provider+" 且范围匹配的在线 runtime。")
		return db.AgentRuntime{}, false
	}
	if best.Status != "online" || !best.LastSeenAt.Valid || checkedAt.Sub(best.LastSeenAt.Time) > promptEvaluationRuntimeFreshTTL {
		writeError(w, http.StatusServiceUnavailable, providerName+" runtime 当前未就绪，无法创建真实可执行的内部小队。请启动 daemon 并等待 runtime 心跳刷新。")
		return db.AgentRuntime{}, false
	}
	return *best, true
}

func internalSquadRuntimeScopeLabel(agentScope string) string {
	if agentScope == scopePersonal {
		return "个人"
	}
	return "工作区"
}

func (h *Handler) ensureInternalSquadAgents(ctx context.Context, workspaceID pgtype.UUID, ownerID pgtype.UUID, runtime db.AgentRuntime, template internalSquadTemplate, squadScope string) ([]InternalSquadAgent, error) {
	existing, err := h.Queries.ListAllAgents(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	result := make([]InternalSquadAgent, 0, len(template.Roles))
	agentScope := internalSquadAgentScope(squadScope)
	for _, role := range template.Roles {
		name := strings.TrimSpace(role.AgentName)
		if name == "" {
			name = template.Name + " · " + role.Name
		}
		runtimeConfig := internalSquadAgentRuntimeConfig(runtime, template, role, squadScope, agentScope, ownerID)
		instructions := "你是" + template.Name + "小队的" + role.Name + "。" + role.Instruction + "所有输出必须使用中文，并保留可验收证据。"
		description := internalSquadRoleDescription(template, role)
		model := pgtype.Text{String: template.Model, Valid: true}
		agentRow, ok := findInternalSquadAgent(existing, name, template, role, squadScope, agentScope, ownerID)
		if !ok {
			agentRow, err = h.Queries.CreateAgent(ctx, db.CreateAgentParams{
				WorkspaceID:        workspaceID,
				Name:               name,
				Description:        description,
				Instructions:       instructions,
				RuntimeMode:        runtime.RuntimeMode,
				RuntimeConfig:      runtimeConfig,
				RuntimeID:          runtime.ID,
				Scope:              agentScope,
				MaxConcurrentTasks: defaultAgentMaxConcurrentTasks,
				OwnerID:            ownerID,
				CustomEnv:          []byte("{}"),
				CustomArgs:         []byte("[]"),
				McpConfig:          role.MCPConfig,
				Model:              model,
			})
			if err != nil {
				return nil, err
			}
		} else {
			if agentRow.ArchivedAt.Valid {
				agentRow, err = h.Queries.RestoreAgent(ctx, agentRow.ID)
				if err != nil {
					return nil, err
				}
			}
			if internalSquadAgentNeedsSync(agentRow, runtime, template, role, runtimeConfig, instructions, description, model, agentScope) {
				agentRow, err = h.Queries.UpdateAgent(ctx, db.UpdateAgentParams{
					ID:                 agentRow.ID,
					Description:        pgtype.Text{String: description, Valid: true},
					RuntimeConfig:      runtimeConfig,
					RuntimeMode:        pgtype.Text{String: runtime.RuntimeMode, Valid: true},
					RuntimeID:          runtime.ID,
					Scope:              pgtype.Text{String: agentScope, Valid: true},
					MaxConcurrentTasks: pgtype.Int4{Int32: defaultAgentMaxConcurrentTasks, Valid: true},
					Instructions:       pgtype.Text{String: instructions, Valid: true},
					CustomArgs:         []byte("[]"),
					McpConfig:          role.MCPConfig,
					Model:              model,
				})
				if err != nil {
					return nil, err
				}
			}
		}
		result = append(result, InternalSquadAgent{
			ID:      uuidToString(agentRow.ID),
			Name:    agentRow.Name,
			RoleKey: role.Key,
			Role:    role.Name,
		})
	}
	return result, nil
}

func internalSquadAgentScope(squadScope string) string {
	if squadScope == squadScopePersonal {
		return scopePersonal
	}
	return scopeWorkspace
}

func internalSquadAgentRuntimeConfig(runtime db.AgentRuntime, template internalSquadTemplate, role internalSquadRole, squadScope string, agentScope string, ownerID pgtype.UUID) []byte {
	scopeOwnerID := ""
	if squadScope == squadScopePersonal {
		scopeOwnerID = uuidToString(ownerID)
	}
	return mustJSONBytes(map[string]any{
		"provider": runtime.Provider,
		"用途":       template.Name,
		"角色":       role.Name,
		"模板":       template.Key,
		"internal_squad": map[string]any{
			"template_key": template.Key,
			"role_key":     role.Key,
			"squad_scope":  squadScope,
			"agent_scope":  agentScope,
			"owner_id":     scopeOwnerID,
		},
	})
}

func findInternalSquadAgent(agents []db.Agent, name string, template internalSquadTemplate, role internalSquadRole, squadScope string, agentScope string, ownerID pgtype.UUID) (db.Agent, bool) {
	var archivedMatch db.Agent
	for _, agent := range agents {
		if !matchesInternalSquadAgent(agent, name, template, role, squadScope, agentScope, ownerID) {
			continue
		}
		if !agent.ArchivedAt.Valid {
			return agent, true
		}
		if uuidToString(archivedMatch.ID) == "" || agent.UpdatedAt.Time.After(archivedMatch.UpdatedAt.Time) {
			archivedMatch = agent
		}
	}
	if uuidToString(archivedMatch.ID) != "" {
		return archivedMatch, true
	}
	return db.Agent{}, false
}

func matchesInternalSquadAgent(agent db.Agent, name string, template internalSquadTemplate, role internalSquadRole, squadScope string, agentScope string, ownerID pgtype.UUID) bool {
	if agent.Name != name || agent.Scope != agentScope {
		return false
	}
	if squadScope == squadScopePersonal && uuidToString(agent.OwnerID) != uuidToString(ownerID) {
		return false
	}
	var runtimeConfig map[string]any
	if len(bytes.TrimSpace(agent.RuntimeConfig)) == 0 || json.Unmarshal(agent.RuntimeConfig, &runtimeConfig) != nil {
		return false
	}
	if scope, ok := runtimeConfig["internal_squad"].(map[string]any); ok {
		if stringFromAny(scope["template_key"]) != template.Key ||
			stringFromAny(scope["role_key"]) != role.Key ||
			stringFromAny(scope["squad_scope"]) != squadScope ||
			stringFromAny(scope["agent_scope"]) != agentScope {
			return false
		}
		if squadScope == squadScopePersonal && stringFromAny(scope["owner_id"]) != uuidToString(ownerID) {
			return false
		}
		return true
	}
	return stringFromAny(runtimeConfig["模板"]) == template.Key && stringFromAny(runtimeConfig["角色"]) == role.Name
}

func internalSquadAgentNeedsSync(agent db.Agent, runtime db.AgentRuntime, template internalSquadTemplate, role internalSquadRole, runtimeConfig []byte, instructions string, description string, model pgtype.Text, scope string) bool {
	if agent.Description != description ||
		agent.RuntimeMode != runtime.RuntimeMode ||
		uuidToString(agent.RuntimeID) != uuidToString(runtime.ID) ||
		agent.Scope != scope ||
		agent.MaxConcurrentTasks != defaultAgentMaxConcurrentTasks ||
		agent.Instructions != instructions ||
		!bytes.Equal(bytes.TrimSpace(agent.RuntimeConfig), bytes.TrimSpace(runtimeConfig)) ||
		!bytes.Equal(bytes.TrimSpace(agent.CustomArgs), []byte("[]")) ||
		!bytes.Equal(bytes.TrimSpace(agent.McpConfig), bytes.TrimSpace(role.MCPConfig)) {
		return true
	}
	if model.Valid != agent.Model.Valid {
		return true
	}
	if model.Valid && model.String != agent.Model.String {
		return true
	}
	return false
}

func internalSquadRoleDescription(template internalSquadTemplate, role internalSquadRole) string {
	if strings.TrimSpace(role.Description) != "" {
		return strings.TrimSpace(role.Description)
	}
	return template.Description
}

func (h *Handler) ensureInternalSquad(ctx context.Context, workspaceID pgtype.UUID, creatorID pgtype.UUID, template internalSquadTemplate, scope string, agents []InternalSquadAgent, archiveSuperseded bool) (db.Squad, error) {
	if len(agents) == 0 {
		return db.Squad{}, pgx.ErrNoRows
	}
	squads, err := h.Queries.ListAllSquads(ctx, workspaceID)
	if err != nil {
		return db.Squad{}, err
	}
	var squad db.Squad
	var archivedMatch db.Squad
	for _, item := range squads {
		if !matchesInternalSquadTarget(item, template, scope, creatorID, archiveSuperseded) {
			continue
		}
		if !item.ArchivedAt.Valid {
			squad = item
			break
		}
		if uuidToString(archivedMatch.ID) == "" || item.UpdatedAt.Time.After(archivedMatch.UpdatedAt.Time) {
			archivedMatch = item
		}
	}
	if uuidToString(squad.ID) == "" {
		if uuidToString(archivedMatch.ID) != "" {
			squad, err = h.Queries.RestoreSquad(ctx, archivedMatch.ID)
			if err != nil {
				return db.Squad{}, err
			}
		} else {
			sopProfile := mustJSONBytes(template.Profile)
			squad, err = h.Queries.CreateSquad(ctx, db.CreateSquadParams{
				WorkspaceID:  workspaceID,
				Name:         template.Name,
				Description:  template.Description,
				LeaderID:     parseUUID(agents[0].ID),
				CreatorID:    creatorID,
				Scope:        pgtype.Text{String: scope, Valid: true},
				Instructions: pgtype.Text{String: template.Instructions, Valid: true},
				SopProfile:   sopProfile,
			})
			if err != nil {
				return db.Squad{}, err
			}
		}
	}
	if uuidToString(squad.ID) != "" {
		profileBytes := mustJSONBytes(template.Profile)
		leaderID := parseUUID(agents[0].ID)
		if itemNeedsInternalSquadSync(squad, template, profileBytes, leaderID, scope) {
			params := db.UpdateSquadParams{
				ID:           squad.ID,
				Name:         pgtype.Text{String: template.Name, Valid: squad.Name != template.Name},
				Description:  pgtype.Text{String: template.Description, Valid: true},
				LeaderID:     leaderID,
				Scope:        pgtype.Text{String: scope, Valid: true},
				Instructions: pgtype.Text{String: template.Instructions, Valid: true},
				SopProfile:   profileBytes,
			}
			squad, err = h.Queries.UpdateSquad(ctx, params)
			if err != nil {
				return db.Squad{}, err
			}
		}
	}
	existingMembers, err := h.Queries.ListSquadMembers(ctx, squad.ID)
	if err != nil {
		return db.Squad{}, err
	}
	desiredAgentMembers := map[string]struct{}{}
	for _, agent := range agents {
		desiredAgentMembers[agent.ID] = struct{}{}
	}
	existingMemberRoles := map[string]string{}
	for _, member := range existingMembers {
		memberID := uuidToString(member.MemberID)
		if member.MemberType == "agent" {
			if _, keep := desiredAgentMembers[memberID]; !keep {
				if _, err := h.Queries.RemoveSquadMember(ctx, db.RemoveSquadMemberParams{
					SquadID:    squad.ID,
					MemberType: member.MemberType,
					MemberID:   member.MemberID,
				}); err != nil {
					return db.Squad{}, err
				}
				continue
			}
		}
		existingMemberRoles[member.MemberType+":"+memberID] = member.Role
	}
	for _, agent := range agents {
		role := "member"
		for _, templateRole := range template.Roles {
			if templateRole.Key == agent.RoleKey {
				role = templateRole.MemberRole
				break
			}
		}
		memberID := parseUUID(agent.ID)
		memberKey := "agent:" + agent.ID
		if existingRole, exists := existingMemberRoles[memberKey]; exists {
			if existingRole != role {
				if _, err := h.Queries.UpdateSquadMemberRole(ctx, db.UpdateSquadMemberRoleParams{
					SquadID:    squad.ID,
					MemberType: "agent",
					MemberID:   memberID,
					Role:       role,
				}); err != nil {
					return db.Squad{}, err
				}
			}
			continue
		}
		if _, err := h.Queries.AddSquadMember(ctx, db.AddSquadMemberParams{
			SquadID:    squad.ID,
			MemberType: "agent",
			MemberID:   memberID,
			Role:       role,
		}); err != nil {
			return db.Squad{}, err
		}
		existingMemberRoles[memberKey] = role
	}
	if archiveSuperseded {
		if err := h.archiveSupersededInternalSquads(ctx, squads, squad.ID, template, scope, creatorID); err != nil {
			return db.Squad{}, err
		}
	}
	return squad, nil
}

func matchesInternalSquadTemplate(squad db.Squad, template internalSquadTemplate, scope string, creatorID pgtype.UUID) bool {
	sameTemplate := squad.Name == template.Name || matchesInternalSquadProfileKey(squad, template)
	sameScope := squad.Scope == scope
	sameCreator := scope != squadScopePersonal || uuidToString(squad.CreatorID) == uuidToString(creatorID)
	return sameTemplate && sameScope && sameCreator
}

func internalSquadProfileKey(squad db.Squad) string {
	profile := decodeJSONDefault(squad.SopProfile, map[string]any{})
	profileMap, _ := profile.(map[string]any)
	return stringFromAny(profileMap["profile_key"])
}

func matchesInternalSquadProfileKey(squad db.Squad, template internalSquadTemplate) bool {
	key := internalSquadProfileKey(squad)
	return key != "" && (key == template.Key || key == stringFromAny(template.Profile["profile_key"]))
}

func matchesInternalSquadTarget(squad db.Squad, template internalSquadTemplate, scope string, creatorID pgtype.UUID, requireName bool) bool {
	if requireName {
		sameTemplate := matchesInternalSquadProfileKey(squad, template)
		sameScope := squad.Scope == scope
		sameCreator := scope != squadScopePersonal || uuidToString(squad.CreatorID) == uuidToString(creatorID)
		return sameTemplate && sameScope && sameCreator && squad.Name == template.Name
	}
	if !matchesInternalSquadTemplate(squad, template, scope, creatorID) {
		return false
	}
	return true
}

func (h *Handler) archiveSupersededInternalSquads(ctx context.Context, squads []db.Squad, currentID pgtype.UUID, template internalSquadTemplate, scope string, creatorID pgtype.UUID) error {
	current := uuidToString(currentID)
	for _, item := range squads {
		if item.ArchivedAt.Valid || uuidToString(item.ID) == current {
			continue
		}
		sameTemplate := matchesInternalSquadProfileKey(item, template)
		sameScope := item.Scope == scope
		sameCreator := scope != squadScopePersonal || uuidToString(item.CreatorID) == uuidToString(creatorID)
		if !sameTemplate || !sameScope || !sameCreator {
			continue
		}
		if _, err := h.DB.Exec(ctx, `
			UPDATE issue
			SET assignee_id = $2, updated_at = now()
			WHERE assignee_type = 'squad' AND assignee_id = $1
		`, item.ID, currentID); err != nil {
			return err
		}
		if _, err := h.DB.Exec(ctx, `
			UPDATE autopilot
			SET assignee_id = $2, updated_at = now()
			WHERE assignee_type = 'squad' AND assignee_id = $1
		`, item.ID, currentID); err != nil {
			return err
		}
		if _, err := h.Queries.ArchiveSquad(ctx, db.ArchiveSquadParams{
			ID:         item.ID,
			ArchivedBy: creatorID,
		}); err != nil {
			return err
		}
	}
	return nil
}

func itemNeedsInternalSquadSync(squad db.Squad, template internalSquadTemplate, profileBytes []byte, leaderID pgtype.UUID, scope string) bool {
	return squad.Name != template.Name ||
		squad.Description != template.Description ||
		uuidToString(squad.LeaderID) != uuidToString(leaderID) ||
		squad.Scope != scope ||
		!bytes.Equal(bytes.TrimSpace(squad.SopProfile), bytes.TrimSpace(profileBytes)) ||
		squad.Instructions != template.Instructions
}

