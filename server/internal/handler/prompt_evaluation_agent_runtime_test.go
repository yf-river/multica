package handler

import (
	"fmt"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestPromptEvaluationSOPVerifierUsesPersistedRoleKeys(t *testing.T) {
	roles := []string{
		"pm",
		"01-clarify",
		"02-design",
		"03-task-split",
		"04-implement",
		"05-verify",
	}
	agents := make([]db.Agent, 0, len(roles))
	for i, role := range roles {
		agents = append(agents, db.Agent{
			Name:          fmt.Sprintf("自定义展示名 %d", i),
			RuntimeConfig: []byte(fmt.Sprintf(`{"internal_squad":{"role_key":%q}}`, role)),
		})
	}

	verifier, ok := promptEvaluationSOPVerifier(agents)
	if !ok {
		t.Fatal("complete persisted SOP role chain was not selected")
	}
	if verifier.Name != "自定义展示名 5" {
		t.Fatalf("verifier = %q, want role-key-selected agent", verifier.Name)
	}
}

func TestPromptEvaluationSOPVerifierRejectsDisplayNameAliasesWithoutRoleKeys(t *testing.T) {
	agents := []db.Agent{
		{Name: "PM-项目经理"},
		{Name: "01-需求澄清"},
		{Name: "02-方案设计"},
		{Name: "03-任务拆分"},
		{Name: "04-开发"},
		{Name: "05-验证测试"},
	}

	if _, ok := promptEvaluationSOPVerifier(agents); ok {
		t.Fatal("display names must not substitute for persisted SOP role identity")
	}
}

func TestPromptEvaluationSOPVerifierRequiresCompleteRoleChain(t *testing.T) {
	agents := []db.Agent{
		{RuntimeConfig: []byte(`{"internal_squad":{"role_key":"pm"}}`)},
		{RuntimeConfig: []byte(`{"internal_squad":{"role_key":"05-verify"}}`)},
	}

	if _, ok := promptEvaluationSOPVerifier(agents); ok {
		t.Fatal("partial SOP role chain must not be selected")
	}
}
