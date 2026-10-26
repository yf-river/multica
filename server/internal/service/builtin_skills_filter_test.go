package service

import "testing"

func TestFilterBuiltinSkillsForRole(t *testing.T) {
	skills := []AgentSkillData{{Name: "multica-working-on-issues"}, {Name: "multica-mentioning"}}
	for _, role := range []string{SupervisionSystemKey, "issue_review"} {
		if got := FilterBuiltinSkillsForRole(role, skills); len(got) != 0 {
			t.Fatalf("role %q should not receive platform playbooks, got %d", role, len(got))
		}
	}
	if got := FilterBuiltinSkillsForRole("", skills); len(got) != len(skills) {
		t.Fatalf("ordinary agent should keep all builtins, got %d", len(got))
	}
	if got := FilterBuiltinSkillsForRole(MikaSystemKey, skills); len(got) != len(skills) {
		t.Fatalf("mika keeps builtins, got %d", len(got))
	}
}
