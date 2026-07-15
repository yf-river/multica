// Package executionpolicy owns the role-derived capabilities shared by the
// claim handler, daemon, and runtime environment.
package executionpolicy

import "strings"

// Policy is the capability envelope for one task. It is derived from the
// agent's role or profile and is independent of the runtime provider.
type Policy struct {
	RoleKey              string   `json:"role_key,omitempty"`
	RoleKind             string   `json:"role_kind,omitempty"`
	CanAccessRepo        bool     `json:"can_access_repo"`
	CanEditRepo          bool     `json:"can_edit_repo"`
	ProjectSkillMode     string   `json:"project_skill_mode,omitempty"`
	AllowedProjectSkills []string `json:"allowed_project_skills,omitempty"`
}

func (p Policy) IsCoordinator() bool {
	return strings.EqualFold(strings.TrimSpace(p.RoleKind), "coordinator")
}

func (p Policy) IsCoordinatorWithoutRepo() bool {
	return p.IsCoordinator() && !p.CanAccessRepo
}

func (p Policy) IsBoundedStage() bool {
	switch strings.ToLower(strings.TrimSpace(p.RoleKind)) {
	case "planning_stage", "verification_stage":
		return true
	default:
		return false
	}
}

func (p Policy) IsNoRepoBoundedStage() bool {
	return p.IsBoundedStage() && !p.CanAccessRepo
}

func (p Policy) IsImplementationStage() bool {
	return strings.EqualFold(strings.TrimSpace(p.RoleKind), "implementation_stage") && p.CanAccessRepo && p.CanEditRepo
}

func (p Policy) UsesFinalOutput() bool {
	return (p.IsBoundedStage() && !p.CanEditRepo) || p.IsCoordinatorWithoutRepo()
}
