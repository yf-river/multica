package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/investigation"
)

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
	config, prompt, err := supervisionMCPConfig(task, env, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "supervision-mcp") || !strings.Contains(prompt, "不代表原运行当时的源码") {
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
}
