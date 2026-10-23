package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/investigation"
)

func TestSupervisionInstructionsIncludeOnlyBoundRoleSkill(t *testing.T) {
	task := Task{SupervisionPrompt: `{"materials":[]}`, Agent: &AgentData{
		Instructions: "最高优先级：只读调查",
		Skills: []SkillData{
			{Name: "ida-development", Content: "开发与部署操作"},
			{Name: "delivery-review", Content: "提交审查结论"},
			{Name: "delivery-supervision", Content: "按准确提交核对用量证据"},
		},
	}}
	got := supervisionInstructions(task)
	if !strings.HasPrefix(got, task.Agent.Instructions) || !strings.Contains(got, task.Agent.Skills[2].Content) {
		t.Fatal("supervision lost its priority instructions or bound role skill")
	}
	for _, unrelated := range task.Agent.Skills[:2] {
		if strings.Contains(got, unrelated.Content) {
			t.Fatal("unrelated workflow leaked into supervision instructions")
		}
	}
	if task.Agent.Instructions != "最高优先级：只读调查" {
		t.Fatal("instruction snapshot was mutated")
	}
	task.SupervisionPrompt = ""
	if supervisionInstructions(task) != task.Agent.Instructions {
		t.Fatal("ordinary task instructions changed")
	}
	task.SupervisionPrompt = "{}"
	task.Agent.Skills = task.Agent.Skills[:2]
	if supervisionInstructions(task) != task.Agent.Instructions {
		t.Fatal("an unbound role skill was injected")
	}
}

func TestSupervisionSourceScopeDoesNotBroadenRunDirectories(t *testing.T) {
	workspace := t.TempDir()
	project := t.TempDir()
	env := t.TempDir()
	for _, candidate := range []string{workspace + "-other", filepath.Join(workspace, "..", "elsewhere"), "relative"} {
		if withinInvestigationRoot(workspace, candidate) {
			t.Fatalf("allowed %s", candidate)
		}
	}
	roots := []investigation.Root{{ID: "run:local", Path: filepath.Join(workspace, "task", "workdir")}, {ID: "run:foreign", Path: t.TempDir()}, {ID: "resource:project", Path: project}, {ID: "run:project", Path: filepath.Join(project, "subdir")}}
	raw, _ := json.Marshal(roots)
	task := Task{ID: "review", SupervisionInvestigation: raw}
	config, prompt, err := supervisionMCPConfig(task, env, workspace, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "supervision-mcp") || !strings.Contains(prompt, `"investigation_sources"`) {
		t.Fatal("missing governed entry or source identity")
	}
	manifest, err := os.ReadFile(filepath.Join(env, "supervision-investigation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Roots []investigation.Root `json:"roots"`
	}
	if err = json.Unmarshal(manifest, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Roots[1].Path != "" || saved.Roots[0].Path == "" || saved.Roots[3].Path == "" {
		t.Fatalf("unexpected allowed roots: %+v", saved.Roots)
	}
	info, _ := os.Stat(filepath.Join(env, "supervision-investigation.json"))
	if info.Mode().Perm()&0077 != 0 {
		t.Fatal("manifest not private")
	}
	var mcp struct {
		Servers map[string]struct {
			EnvVars []string `json:"env_vars"`
		} `json:"mcpServers"`
	}
	if err = json.Unmarshal(config, &mcp); err != nil {
		t.Fatal(err)
	}
	expected := []string{"MULTICA_TOKEN", "MULTICA_SERVER_URL", "MULTICA_WORKSPACE_ID", "MULTICA_AGENT_ID", "MULTICA_TASK_ID", "MULTICA_TASK_CONFIG_ROOT"}
	if !slices.Equal(mcp.Servers["supervision"].EnvVars, expected) {
		t.Fatal("native MCP lost scoped authentication or inherited unrelated credentials")
	}
	other, _, err := supervisionMCPConfig(task, env, workspace, "codebuddy")
	if err != nil || strings.Contains(string(other), "env_vars") {
		t.Fatal("Codex-specific MCP options leaked into another runtime")
	}
}

func TestSupervisionSourceAvailabilityUsesFilesystem(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "file.txt")
	if err := os.WriteFile(file, []byte("evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", file, filepath.Join(directory, "deleted")} {
		if investigationRootAvailable(path) || investigationRootReason(path) == "" {
			t.Fatalf("unavailable source advertised as readable: %s", path)
		}
	}
	if !investigationRootAvailable(directory) || investigationRootReason(directory) != "" {
		t.Fatal("existing directory unavailable")
	}
}
