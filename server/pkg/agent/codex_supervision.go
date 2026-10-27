package agent

import (
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

var codexSupervisionKeyRe = regexp.MustCompile(`^\s*(?:profiles\s*\.\s*` + codexProfileNameKeyPattern + `\s*\.\s*)?(?:features|tools|apps|plugins|hooks|agents|web_search|mcp_servers|"features"|"tools"|"apps"|"plugins"|"hooks"|"agents"|"web_search"|"mcp_servers"|'features'|'tools'|'apps'|'plugins'|'hooks'|'agents'|'web_search'|'mcp_servers')\s*(?:\.|=|$)`)
var codexInlineProfilesRe = regexp.MustCompile(`^\s*(?:profiles|"profiles"|'profiles')(?:\s*\.\s*` + codexProfileNameKeyPattern + `)?\s*=`)
var codexSupervisionVersionRe = regexp.MustCompile(`(?:^|[^0-9.])0\.164\.0(?:$|[^0-9A-Za-z.+_-])`)

func codexSupervisionVersionSupported(version string) bool {
	parsed, err := parseSemver(version)
	return err == nil && parsed == (semver{Minor: 164}) && codexSupervisionVersionRe.MatchString(version)
}

// 业务读取只经监督 MCP；Code Mode 的纯计算桥和原生资源查询仍由运行时提供。
// 文件工具通过空执行环境拒绝，不能将 approval=never 当作文件权限约束。
func codexSupervisionConfig() map[string]any {
	config := map[string]any{
		"project_doc_max_bytes": 0, "features.skip_host_skill_discovery": true,
		"web_search": "disabled", "apps._default.enabled": false,
		"tools.update_plan.enabled": false, "tools.experimental_request_user_input.enabled": false,
		"features.code_mode.enabled":                     false,
		"features.code_mode.direct_only_tool_namespaces": []string{"mcp__supervision"},
		"features.current_time_reminder.enabled":         false, "features.current_time_reminder.sleep_tool": false,
		"features.sleep_tool.enabled": false, "features.multi_agent_v2.enabled": false,
		"features.runtime_agent_instances.enabled":                false,
		"features.non_prefixed_mcp_tool_names.enabled":            false,
		"features.tool_registry.turn_metadata_includes_tool_info": true,
		"agents.enabled": false,
	}
	for _, feature := range []string{
		"shell_tool", "unified_exec", "experimental_use_unified_exec_tool", "apply_patch_freeform",
		"js_repl", "js_repl_tools_only", "code_mode_only", "code_mode_only_strict_3p_tools",
		"code_mode_tool_search", "code_mode_host_grpc",
		"apps", "connectors", "plugins", "remote_plugin", "codex_apps_mcp_2026_07_28", "enable_mcp_apps",
		"hooks", "codex_hooks", "plugin_hooks", "goals", "view_image",
		"image_generation", "imagegenext", "computer_use", "browser_use", "browser_use_external",
		"browser_use_full_cdp_access", "in_app_browser", "memory_tool", "memories", "external_agent_memory_import",
		"skill_search", "skill_mcp_dependency_install",
		"tool_search", "tool_search_always_defer_mcp_tools", "tool_suggest", "search_tool",
		"standalone_web_search", "web_search", "web_search_cached", "web_search_request",
		"request_permissions_tool", "stable_environment_tools", "guardian_conversation_history_tools",
		"agent_message_board", "multi_agent", "collab", "multi_agent_v2_dynamic_tools", "send_message_to_user_async",
	} {
		config["features."+feature] = false
	}
	return config
}

func filterCodexSupervisionArgs(args []string, logger *slog.Logger) []string {
	args = filterCodexConfigOverrides(args, codexSupervisionKeyRe, "supervision", logger)
	return filterCustomArgs(args, map[string]blockedArgMode{"--enable": blockedWithValue}, logger)
}

func codexSupervisionArgs(prefix string) []string {
	config := codexSupervisionConfig()
	keys := make([]string, 0, len(config))
	for key := range config {
		// Profile 只承载 features、tools 和 web_search；其余策略取根配置。
		if prefix != "" && !strings.HasPrefix(key, "features.") && !strings.HasPrefix(key, "tools.") && key != "web_search" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var args []string
	for _, key := range keys {
		value, _ := json.Marshal(config[key])
		args = append(args, "-c", prefix+key+"="+string(value))
	}
	return args
}

func codexSupervisionProfileArgs(home string) ([]string, error) {
	raw, ok := readRegularFileBounded(filepath.Join(home, "config.toml"))
	if !ok {
		return nil, errors.New("Codex supervision configuration is unavailable")
	}
	var config map[string]any
	if toml.Unmarshal(raw, &config) != nil {
		return nil, errors.New("Codex supervision configuration is invalid")
	}
	profiles, _ := config["profiles"].(map[string]any)
	var names []string
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	var args []string
	for _, name := range names {
		quoted, _ := json.Marshal(name)
		args = append(args, codexSupervisionArgs("profiles."+string(quoted)+".")...)
	}
	return args, nil
}

func validateCodexSupervision(cfg Config, opts ExecOptions) error {
	if !codexSupervisionVersionSupported(cfg.CodexVersion) || !cfg.BuiltinRuntime || cfg.CodexPool != nil || len(cfg.LaunchPrefix) != 0 {
		return errors.New("Codex MCP-only supervision requires the verified builtin 0.164.0 one-shot runtime")
	}
	if opts.ResumeSessionID != "" || opts.NativeGoal || opts.Command != "" || opts.CommandOnly || opts.PlanMode != nil && *opts.PlanMode {
		return errors.New("Codex MCP-only supervision requires a fresh task without native controls")
	}
	var mcp struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(opts.McpConfig, &mcp) != nil || len(mcp.Servers) != 1 || len(mcp.Servers["supervision"]) == 0 {
		return errors.New("Codex supervision requires exactly its dedicated MCP server")
	}
	args := NormalizeCodexLaunchArgs(opts.ExtraArgs, opts.CustomArgs, opts.McpConfig, nil)
	for _, arg := range args {
		value := strings.TrimPrefix(strings.TrimPrefix(arg, "--config="), "-c=")
		if codexInlineProfilesRe.MatchString(value) {
			return errors.New("Codex supervision requires model profiles in its task configuration file")
		}
	}
	return nil
}
