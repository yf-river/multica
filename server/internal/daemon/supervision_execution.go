package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/pkg/investigation"
)

func withinInvestigationRoot(root, candidate string) bool {
	if root == "" || candidate == "" || !filepath.IsAbs(candidate) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// supervisionMCPConfig 仅挂载服务器授权的当前任务来源；源任务目录不承载监督运行。
func supervisionMCPConfig(task Task, envRoot, workspacesRoot, provider string) (json.RawMessage, string, error) {
	var roots []investigation.Root
	if err := json.Unmarshal(task.SupervisionInvestigation, &roots); err != nil {
		return nil, "", err
	}
	for i, root := range roots {
		if strings.HasPrefix(root.ID, "run:") {
			allowed := withinInvestigationRoot(workspacesRoot, root.Path)
			for _, resource := range roots {
				if strings.HasPrefix(resource.ID, "resource:") && withinInvestigationRoot(resource.Path, root.Path) {
					allowed = true
				}
			}
			if !allowed {
				roots[i].Path = ""
			}
		}
	}
	manifest := filepath.Join(envRoot, "supervision-investigation.json")
	body, err := json.Marshal(map[string]any{"roots": roots, "task_id": task.ID})
	if err != nil {
		return nil, "", err
	}
	if err = os.WriteFile(manifest, body, 0600); err != nil {
		return nil, "", err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, "", err
	}
	server := map[string]any{"command": executable, "args": []string{"supervision-mcp", manifest}}
	if provider == "codex" {
		// Codex 的 MCP 子进程仅继承白名单变量；这里只传任务鉴权所需的变量名。
		server["env_vars"] = []string{"MULTICA_TOKEN", "MULTICA_SERVER_URL", "MULTICA_WORKSPACE_ID", "MULTICA_AGENT_ID", "MULTICA_TASK_ID", "MULTICA_TASK_CONFIG_ROOT"}
	}
	config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"supervision": server}})
	sources := make([]map[string]any, 0, len(roots))
	for _, root := range roots {
		sources = append(sources, map[string]any{"id": root.ID, "label": root.Label, "run_id": root.RunID, "available": root.Path != ""})
	}
	sourceData, marshalErr := json.Marshal(map[string]any{"investigation_sources": sources})
	if marshalErr != nil {
		return nil, "", marshalErr
	}
	return config, string(sourceData), err
}
