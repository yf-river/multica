package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// PrepareCodebuddySupervisionConfig 隔离启动 hooks 与插件，保留原生模型选择。
// 账号认证仍由未改动的 HOME 提供；目录随任务环境保留，供 SDK 用量与轨迹采集。
func PrepareCodebuddySupervisionConfig(root string, env map[string]string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", errors.New("CodeBuddy supervision requires an absolute task root")
	}
	directory, err := os.MkdirTemp(root, "codebuddy-supervision-")
	if err != nil {
		return "", err
	}
	ready := false
	defer func() {
		if !ready {
			_ = os.RemoveAll(directory)
		}
	}()
	source := resolveSDKConfigDir(env)
	if source != "" {
		for _, name := range []string{"models.json", "settings.json"} {
			path := filepath.Join(source, name)
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				continue
			}
			raw, ok := readRegularFileBounded(path)
			if !ok {
				return "", errors.New("CodeBuddy supervision model settings are unavailable")
			}
			if name == "settings.json" {
				var settings map[string]json.RawMessage
				if json.Unmarshal(raw, &settings) != nil || settings == nil {
					return "", errors.New("CodeBuddy supervision model settings must be an object")
				}
				model := map[string]json.RawMessage{}
				for _, key := range []string{"model", "variantModels"} {
					if value, exists := settings[key]; exists {
						model[key] = value
					}
				}
				raw, _ = json.Marshal(model)
			}
			if err := os.WriteFile(filepath.Join(directory, name), raw, 0600); err != nil {
				return "", err
			}
		}
	}
	ready = true
	return directory, nil
}
