package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// 只记录显式、无命令行覆盖的模型选择；缺项由测试创建入口拒绝继承，不影响原执行。
func writeTestAgentModelContext(dir, taskID, provider string, selection taskModelSelection, args []string) error {
	resolved := selection.Model != ""
	for _, arg := range args {
		if arg == "-m" || arg == "--model" || strings.HasPrefix(arg, "--model=") || arg == "-c" || arg == "--config" || strings.HasPrefix(arg, "--config=") || strings.Contains(arg, "model=") || strings.Contains(arg, "model_reasoning_effort=") || strings.Contains(arg, "service_tier=") || strings.Contains(arg, "fast_mode") {
			resolved = false
		}
	}
	value := map[string]any{"task_id": taskID, "provider": provider, "model": selection.Model, "thinking_level": selection.ThinkingLevel, "service_tier": selection.ServiceTier, "resolved": resolved}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "multica-execution-model.json"), raw, 0600)
}
