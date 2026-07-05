package handler

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func normalizeSquadScope(value string) (string, bool) {
	switch strings.TrimSpace(value) {
	case "", scopeWorkspace:
		return scopeWorkspace, true
	case scopePersonal:
		return scopePersonal, true
	default:
		return "", false
	}
}

// canAccessPersonalAgent gates the four protected surfaces for personal
// agents: chat / @-mention dispatch, viewing the agent's history, editing
// configuration, and deletion.
//
// Workspace agents are unrestricted — the predicate returns true unconditionally.
//
// Agent-to-agent traffic is always allowed (actorType == "agent"); this is
// what preserves A2A collaboration even with personal agents. The trust
// boundary is at member↔agent, not agent↔agent.
//
// For members, the implicit allowed_principals set is computed inline as:
// {agent.owner_id} ∪ workspace owner/admin members. Manual configuration of
// allowed_principals is not exposed in v1; future work can extend this set
// without changing call sites.
func (h *Handler) canAccessPersonalAgent(ctx context.Context, agent db.Agent, actorType, actorID, workspaceID string) bool {
	if agent.Scope != scopePersonal {
		return true
	}
	if actorType == "agent" {
		return true
	}
	if uuidToString(agent.OwnerID) == actorID {
		return true
	}
	member, err := h.getWorkspaceMember(ctx, actorID, workspaceID)
	if err != nil {
		return false
	}
	return roleAllowed(member.Role, "owner", "admin")
}

// memberAllowedForPersonalAgent is the pure predicate used by both
// canAccessPersonalAgent and the ListAgents filter loop. Caller must have
// already confirmed agent.Scope == "personal".
func memberAllowedForPersonalAgent(agent db.Agent, userID, role string) bool {
	if roleAllowed(role, "owner", "admin") {
		return true
	}
	return uuidToString(agent.OwnerID) == userID
}

func memberCanManageSquad(squad db.Squad, member db.Member) bool {
	if roleAllowed(member.Role, "owner", "admin") {
		return true
	}
	return uuidToString(squad.CreatorID) == uuidToString(member.UserID)
}

func memberCanUseSquad(squad db.Squad, member db.Member) bool {
	if squad.Scope != scopePersonal {
		return true
	}
	return memberCanManageSquad(squad, member)
}

func (h *Handler) canUseSquad(ctx context.Context, squad db.Squad, actorType, actorID, workspaceID string) bool {
	if squad.Scope != scopePersonal {
		return true
	}
	if actorType == "agent" {
		if actorID == uuidToString(squad.LeaderID) {
			return true
		}
		if h.DB == nil {
			return false
		}
		agentID, err := util.ParseUUID(actorID)
		if err != nil {
			return false
		}
		var ok bool
		if err := h.DB.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				  FROM squad_member
				 WHERE squad_id = $1
				   AND member_type = 'agent'
				   AND member_id = $2
			)
		`, squad.ID, agentID).Scan(&ok); err != nil {
			return false
		}
		return ok
	}
	if actorType != "member" {
		return false
	}
	member, err := h.getWorkspaceMember(ctx, actorID, workspaceID)
	if err != nil {
		return false
	}
	return memberCanManageSquad(squad, member)
}

// accessibleAgentIDs returns the set of agent IDs in the workspace the actor
// is allowed to see, for use by workspace-wide aggregation endpoints
// (run counts, activity histograms, task snapshots) that need to filter out
// personal agents the member can't access.
func (h *Handler) accessibleAgentIDs(ctx context.Context, workspaceID, actorType, actorID, role string) (map[string]struct{}, error) {
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	agents, err := h.Queries.ListAllAgents(ctx, wsUUID)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		if a.Scope == scopePersonal && actorType == "member" {
			if !memberAllowedForPersonalAgent(a, actorID, role) {
				continue
			}
		}
		allowed[uuidToString(a.ID)] = struct{}{}
	}
	return allowed, nil
}

// canEnqueueSquadLeader returns true when the given actor is allowed to
// trigger the squad's personal leader. It loads the leader agent and delegates
// to canAccessPersonalAgent. Workspace leaders always pass. System-initiated
// triggers (e.g. github webhooks) pass by treating "system" like "agent".
func (h *Handler) canEnqueueSquadLeader(ctx context.Context, leaderID pgtype.UUID, actorType, actorID, workspaceID string) bool {
	agent, err := h.Queries.GetAgent(ctx, leaderID)
	if err != nil {
		return false
	}
	if actorType == "system" {
		actorType = "agent"
	}
	return h.canAccessPersonalAgent(ctx, agent, actorType, actorID, workspaceID)
}
