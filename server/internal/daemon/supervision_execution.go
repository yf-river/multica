package daemon

import (
	"encoding/json"
	"fmt"
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
	var descriptions []string
	for _, root := range roots {
		availability := "可只读调查"
		if root.Path == "" {
			availability = "目录不在本机可读范围，不能核验"
		}
		descriptions = append(descriptions, fmt.Sprintf("root=%s label=%s run_id=%s %s", root.ID, root.Label, root.RunID, availability))
	}
	return config, "\n本批次授权来源：\n" + strings.Join(descriptions, "\n") + "\n可用supervision_material按kind/UUID分页补读本任务历史。工具返回的id属于kind=investigation；发现问题和解决旧问题时引用实际返回的id、run_id。covered仍只确认初始材料。工具失败或来源不可用应保留证据缺口。运行目录读取到的是调查时的当前内容，不代表原运行当时的源码；只有指定完整提交的Git show才能作为历史源码证据。", err
}
