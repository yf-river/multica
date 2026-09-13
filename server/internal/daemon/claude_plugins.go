package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// claudePluginInstall 描述一个已安装并被 Claude Code 启用的插件实例。
type claudePluginInstall struct {
	ID          string
	Name        string
	InstallPath string
}

// claudeInstalledPluginsFile 对应 ~/.claude/plugins/installed_plugins.json 的结构：
// 每个插件 ID 可有多条安装记录（不同 scope）。
type claudeInstalledPluginsFile struct {
	Plugins map[string][]struct {
		Scope       string `json:"scope"`
		InstallPath string `json:"installPath"`
	} `json:"plugins"`
}

// claudeSettingsFile 对应 ~/.claude/settings.json：插件是否启用。
type claudeSettingsFile struct {
	EnabledPlugins map[string]bool `json:"enabledPlugins"`
}

// claudePluginManifest 对应插件目录里 .claude-plugin/plugin.json 的子集字段。
type claudePluginManifest struct {
	Name       string          `json:"name"`
	Skills     json.RawMessage `json:"skills"`
	MCPServers json.RawMessage `json:"mcpServers"`
}

// listEnabledClaudePlugins 解析 Claude Code 自己在用户 scope 内启用的插件安装。
// 特意读安装注册表而不是递归扫 ~/.claude/plugins：后者会同时浮出 marketplace
// checkout 与同一插件的每个缓存版本。
func listEnabledClaudePlugins(home string) []claudePluginInstall {
	settingsRaw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return nil
	}
	var settings claudeSettingsFile
	if json.Unmarshal(settingsRaw, &settings) != nil || len(settings.EnabledPlugins) == 0 {
		return nil
	}

	installedRaw, err := os.ReadFile(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"))
	if err != nil {
		return nil
	}
	var installed claudeInstalledPluginsFile
	if json.Unmarshal(installedRaw, &installed) != nil {
		return nil
	}

	pluginIDs := make([]string, 0, len(settings.EnabledPlugins))
	for id, enabled := range settings.EnabledPlugins {
		if enabled {
			pluginIDs = append(pluginIDs, id)
		}
	}
	sort.Strings(pluginIDs)

	plugins := make([]claudePluginInstall, 0, len(pluginIDs))
	for _, id := range pluginIDs {
		installs := installed.Plugins[id]
		if len(installs) == 0 {
			continue
		}
		selected := installs[len(installs)-1]
		for _, install := range installs {
			if install.Scope == "user" {
				selected = install
			}
		}
		installPath := strings.TrimSpace(selected.InstallPath)
		if installPath == "" {
			continue
		}

		name := strings.TrimSpace(strings.SplitN(id, "@", 2)[0])
		if manifest, ok := readClaudePluginManifest(installPath); ok && strings.TrimSpace(manifest.Name) != "" {
			name = strings.TrimSpace(manifest.Name)
		}
		if name == "" {
			continue
		}
		plugins = append(plugins, claudePluginInstall{ID: id, Name: name, InstallPath: installPath})
	}
	return plugins
}

// readClaudePluginManifest 读取并解析某安装目录下的插件清单；缺失或非法返回
// (零值, false)。
func readClaudePluginManifest(installPath string) (claudePluginManifest, bool) {
	raw, err := os.ReadFile(filepath.Join(installPath, ".claude-plugin", "plugin.json"))
	if err != nil {
		return claudePluginManifest{}, false
	}
	var manifest claudePluginManifest
	if json.Unmarshal(raw, &manifest) != nil {
		return claudePluginManifest{}, false
	}
	return manifest, true
}

// claudePluginComponentPaths 把插件清单里的 skills/mcpServers 字段（可能是单个
// 字符串也可能是数组）解析成绝对路径集合，追加到 defaults 后去重并做包含性校验：
// 相对路径以 installPath 为基准解析，任何逃逸出安装目录的候选都被丢弃。
func claudePluginComponentPaths(installPath string, raw json.RawMessage, defaults ...string) []string {
	paths := append([]string(nil), defaults...)
	var one string
	if json.Unmarshal(raw, &one) == nil && strings.TrimSpace(one) != "" {
		paths = append(paths, one)
	} else {
		var many []string
		if json.Unmarshal(raw, &many) == nil {
			paths = append(paths, many...)
		}
	}

	seen := make(map[string]bool)
	out := make([]string, 0, len(paths))
	for _, candidate := range paths {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(installPath, filepath.FromSlash(candidate))
		}
		candidate = filepath.Clean(candidate)
		rel, err := filepath.Rel(installPath, candidate)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if !seen[candidate] {
			seen[candidate] = true
			out = append(out, candidate)
		}
	}
	return out
}
