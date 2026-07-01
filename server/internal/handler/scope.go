package handler

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	scopePersonal  = "personal"
	scopeWorkspace = "workspace"

	squadScopePersonal  = scopePersonal
	squadScopeWorkspace = scopeWorkspace
)

func normalizeScope(value string, fallback string) (string, bool) {
	if value == "" {
		value = fallback
	}
	switch value {
	case scopePersonal, scopeWorkspace:
		return value, true
	default:
		return "", false
	}
}

func sameUUID(a, b pgtype.UUID) bool {
	return a.Valid && b.Valid && uuidToString(a) == uuidToString(b)
}

func validateAgentRuntimeScope(agentScope string, agentOwner pgtype.UUID, runtime db.AgentRuntime) error {
	switch agentScope {
	case scopeWorkspace:
		if runtime.Scope != scopeWorkspace {
			return fmt.Errorf("workspace agents can only use workspace runtimes")
		}
	case scopePersonal:
		if runtime.Scope != scopePersonal {
			return fmt.Errorf("personal agents can only use personal runtimes")
		}
		if !sameUUID(runtime.OwnerID, agentOwner) {
			return fmt.Errorf("personal agents can only use personal runtimes owned by the same user")
		}
	default:
		return fmt.Errorf("scope must be 'personal' or 'workspace'")
	}
	return nil
}

func validateSquadLeaderScope(squadScope string, squadOwner pgtype.UUID, leader db.Agent) error {
	switch squadScope {
	case scopeWorkspace:
		if leader.Scope != scopeWorkspace {
			return fmt.Errorf("workspace squads can only use workspace agents")
		}
	case scopePersonal:
		if leader.Scope != scopePersonal {
			return fmt.Errorf("personal squads can only use personal agents")
		}
		if !sameUUID(leader.OwnerID, squadOwner) {
			return fmt.Errorf("personal squads can only use personal agents owned by the same user")
		}
	default:
		return fmt.Errorf("scope must be 'personal' or 'workspace'")
	}
	return nil
}
