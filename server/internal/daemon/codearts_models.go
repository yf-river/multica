package daemon

import (
	"os"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// loadCodeArtsConfiguredModels 从 CodeArts 的用户配置里读取 OpenCode 兼容的自定义
// provider。CodeArts 的 TUI 即使在其 `models` 命令被华为云登录预检挡住时也能使用
// 这些 provider，所以这是 daemon 无需联网的模型发现兜底方案。
func loadCodeArtsConfiguredModels(home string) ([]agent.Model, error) {
	raw, err := os.ReadFile(codeArtsUserConfigPath(home))
	if os.IsNotExist(err) {
		return []agent.Model{}, nil
	}
	if err != nil {
		return nil, err
	}
	document, err := unmarshalRuntimeMcpConfig(raw, "jsonc")
	if err != nil {
		return nil, err
	}
	providers, ok := document["provider"].(map[string]any)
	if !ok {
		return []agent.Model{}, nil
	}

	// provider 名字排序后再逐个处理，保证输出顺序稳定、可复现。
	providerNames := make([]string, 0, len(providers))
	for name := range providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)

	models := make([]agent.Model, 0)
	for _, rawProviderName := range providerNames {
		providerName := strings.TrimSpace(rawProviderName)
		providerConfig, ok := providers[rawProviderName].(map[string]any)
		if !ok || providerName == "" {
			continue
		}
		configuredModels, ok := providerConfig["models"].(map[string]any)
		if !ok {
			continue
		}
		modelNames := make([]string, 0, len(configuredModels))
		for name := range configuredModels {
			modelNames = append(modelNames, name)
		}
		sort.Strings(modelNames)
		for _, rawModelName := range modelNames {
			modelName := strings.TrimSpace(rawModelName)
			if modelName == "" {
				continue
			}
			label := modelName
			// 配置里可为模型提供一个人类可读的 name，作为展示用 label。
			if modelConfig, ok := configuredModels[rawModelName].(map[string]any); ok {
				if configuredLabel, ok := modelConfig["name"].(string); ok && strings.TrimSpace(configuredLabel) != "" {
					label = strings.TrimSpace(configuredLabel)
				}
			}
			models = append(models, agent.Model{
				ID:       providerName + "/" + modelName,
				Label:    label,
				Provider: providerName,
			})
		}
	}
	return models, nil
}
