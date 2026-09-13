package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// runtimeLocalMcpServerSummary 是在 Agent 能力页展示的“故意不含秘密”的清单条目。
// 千万别往这里加命令参数、URL、请求头或环境变量：这份载荷要离开用户机器。
type runtimeLocalMcpServerSummary struct {
	Name      string `json:"name"`
	Transport string `json:"transport,omitempty"`
	Source    string `json:"source,omitempty"`
	Enabled   bool   `json:"enabled"`
}

// mergeRuntimeAndAgentMcpConfig 构造 agent 由 Multica 托管 MCP server 时使用的
// 任务级 MCP 配置。runtime（用户本机）server 是底层，同名的 agent 条目胜出。合并
// 在本地 daemon 里完成，因此 runtime 的 URL、请求头、命令与环境变量都不需要离开
// 这台机器。
//
// agent 配置为 nil/null 时，保留 provider 原生的继承路径不变。配置存在时（哪怕
// mcpServers 是空 map）就选用这份合并后的任务级配置——这样新增一个托管 server 不再
// 会连带禁用掉无关的 runtime server。
func mergeRuntimeAndAgentMcpConfig(provider string, agentConfig json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(agentConfig)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return agentConfig, nil
	}

	runtimeServers, supported, err := loadRuntimeMcpServerConfigs(provider)
	if err != nil {
		return nil, err
	}
	if !supported {
		return agentConfig, nil
	}

	var agentDocument map[string]any
	if err := json.Unmarshal(trimmed, &agentDocument); err != nil {
		return nil, fmt.Errorf("parse agent MCP config: %w", err)
	}
	agentServers := map[string]any{}
	if servers, ok := nestedRuntimeMcpMap(agentDocument, "mcpServers"); ok {
		agentServers = servers
	} else if provider == "opencode" || provider == "codearts" {
		// 老版本 OpenCode agent 可能把配置存在 provider 原生的顶层 `mcp` map 里。
		// 只要把它们挪进规范的 mcpServers 信封，单条 entry 仍能走既有 OpenCode
		// adapter。
		if servers, ok := nestedRuntimeMcpMap(agentDocument, "mcp"); ok {
			agentServers = servers
		}
	}

	merged := make(map[string]any, len(runtimeServers)+len(agentServers))
	for name, entry := range runtimeServers {
		merged[name] = entry
	}
	for name, entry := range agentServers {
		merged[name] = entry
	}

	raw, err := json.Marshal(map[string]any{"mcpServers": merged})
	if err != nil {
		return nil, fmt.Errorf("marshal merged MCP config: %w", err)
	}
	return raw, nil
}

// codeArtsUserConfigPath 返回 CodeArts 首个存在的用户配置文件，遵循官方启动器的
// codearts_cli.json/codearts_cli.jsonc 优先顺序。
func codeArtsUserConfigPath(home string) string {
	configDir := filepath.Join(home, ".codeartsdoer")
	candidates := []string{
		filepath.Join(configDir, "codearts_cli.json"),
		filepath.Join(configDir, "codearts_cli.jsonc"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return candidates[0]
}

// codebuddyUserMcpConfigPath 返回 CodeBuddy 真正会读取的用户级 MCP 配置文件。
//
// CodeBuddy 是 Claude Code 的分支，但它解析自己的配置，所以永远不会去看
// `~/.claude.json`。它的配置目录是 `$CODEBUDDY_CONFIG_DIR`，缺省 `~/.codebuddy`，
// 用户级 MCP 文件取下列“第一个存在”的——这份列表是回退链，不是合并：
//
//	<configDir>/.mcp.json   (`codebuddy mcp add --scope user` 写的就是它)
//	<configDir>/mcp.json
//	~/.codebuddy.json
//
// 一个都不存在时返回第一个候选，让调用方读文件得到 os.ErrNotExist、按“无 runtime
// server”处理，与其它 provider 行为一致。已对照 CodeBuddy 2.x 逐个写入并读回
// `codebuddy mcp list` 验证过。
func codebuddyUserMcpConfigPath(home string) string {
	configDir := strings.TrimSpace(os.Getenv("CODEBUDDY_CONFIG_DIR"))
	if configDir == "" {
		configDir = filepath.Join(home, ".codebuddy")
	}
	candidates := []string{
		filepath.Join(configDir, ".mcp.json"),
		filepath.Join(configDir, "mcp.json"),
		filepath.Join(home, ".codebuddy.json"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return candidates[0]
}

// unmarshalRuntimeMcpConfig 解码某个 runtime 的配置文件。格式 "jsonc" 指“带注释与
// 尾逗号的 JSON”，CodeBuddy 的 MCP 文件就接受这种写法——用严格 JSON 去解析会因单个
// `//` 丢掉其后所有 server。
func unmarshalRuntimeMcpConfig(raw []byte, format string) (map[string]any, error) {
	var cfg map[string]any
	switch format {
	case "toml":
		if err := toml.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parse runtime MCP config: %w", err)
		}
	case "jsonc":
		stripped, err := stripJSONC(raw)
		if err != nil {
			return nil, fmt.Errorf("parse runtime MCP config: %w", err)
		}
		if err := json.Unmarshal(stripped, &cfg); err != nil {
			return nil, fmt.Errorf("parse runtime MCP config: %w", err)
		}
	default:
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parse runtime MCP config: %w", err)
		}
	}
	return cfg, nil
}

// stripJSONC 把 JSONC 改写成严格 JSON：`//` 与 `/* */` 注释变成空格，`}` / `]`
// 前的一个尾逗号被删掉。字符串字面量原样拷贝，因此命令参数里的 `//` 或逗号不会
// 被误伤。
//
// 输出与输入等长——注释和被删的逗号是“抹成空格”，不是删字符——这样解析报错时的
// 偏移仍指向用户实际写下的那个字节。
//
// 只有闭合符前的“最后一个”逗号会被抹掉，所以真正非法的输入保持非法：`[1,,,]`
// 不会悄悄变成 `[1]`。这是按 CodeBuddy MCP 文件实际用法裁剪出的 JSONC 实用子集；
// 修不了的内容报解析错误，而不是靠猜。
//
// 未闭合的 `/*` 是错误而非“一路抹到文件尾”，因此 Agent > MCP 页不会列出 CodeBuddy
// 自己也拒绝的文件。已验证：对未闭合注释和 `/*/` 这种“开头的 * 想同时充当结尾”的
// 近似命中，CLI 都会报 "No MCP servers configured"。
func stripJSONC(raw []byte) ([]byte, error) {
	out := make([]byte, 0, len(raw))
	// 指向 out 里仍可被删除的那个逗号的偏移；-1 表示没有。遇到任何值 token 都会
	// 重置它，因此只删真正“尾随”的逗号。
	lastComma := -1
	inString := false
	escaped := false

	for i := 0; i < len(raw); i++ {
		c := raw[i]

		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}

		switch {
		case c == '"':
			inString = true
			lastComma = -1
			out = append(out, c)
		case c == '/' && i+1 < len(raw) && raw[i+1] == '/':
			// 整行抹成空格，但保留换行符本身。
			for i < len(raw) && raw[i] != '\n' {
				out = append(out, ' ')
				i++
			}
			if i < len(raw) {
				out = append(out, raw[i])
			}
		case c == '/' && i+1 < len(raw) && raw[i+1] == '*':
			// 先消费掉开头两个字符，让 `/*/` 无法把开头的 `*` 复用作结尾的 `*`；
			// 再把注释体（含换行）抹成空格，保证行号与总字节数都不变。
			out = append(out, ' ', ' ')
			i += 2
			closed := false
			for i < len(raw) {
				if raw[i] == '*' && i+1 < len(raw) && raw[i+1] == '/' {
					out = append(out, ' ', ' ')
					i++ // 消费 '*'; 循环末尾的 i++ 再消费 '/'
					closed = true
					break
				}
				if raw[i] == '\n' {
					out = append(out, '\n')
				} else {
					out = append(out, ' ')
				}
				i++
			}
			if !closed {
				return nil, errors.New("unterminated block comment")
			}
		case c == ',':
			lastComma = len(out)
			out = append(out, c)
		case c == '}' || c == ']':
			if lastComma >= 0 {
				out[lastComma] = ' '
			}
			lastComma = -1
			out = append(out, c)
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			out = append(out, c)
		default:
			lastComma = -1
			out = append(out, c)
		}
	}
	return out, nil
}

// loadRuntimeMcpServerConfigs 返回“含完整秘密”的 runtime MCP 条目，供任务级合并
// 使用。调用方绝不能把结果发给服务器或写进日志；公开的能力端点一直用上面那个脱敏
// 的 summary 类型。
func loadRuntimeMcpServerConfigs(provider string) (map[string]any, bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false, fmt.Errorf("resolve user home: %w", err)
	}

	var path, key, format string
	switch provider {
	case "claude":
		path, key, format = filepath.Join(home, ".claude.json"), "mcpServers", "json"
	case "codearts":
		path, key, format = codeArtsUserConfigPath(home), "mcp", "jsonc"
	// codebuddy 有意缺席。CodeBuddy 每次启动都会自己加载 user、project、local 三个
	// scope（codebuddy.go 从不传 --strict-mcp-config），且托管条目在同名冲突时本来就
	// 会赢，所以 daemon 预合并只会重复 CLI 原生在做的事——还会丢掉保护 project-scope
	// server 的 scope 优先级与审批闸门。
	case "codex":
		codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
		path, key, format = filepath.Join(codexHome, "config.toml"), "mcp_servers", "toml"
	case "cursor":
		path, key, format = filepath.Join(home, ".cursor", "mcp.json"), "mcpServers", "json"
	case "opencode":
		configHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
		if configHome == "" {
			configHome = filepath.Join(home, ".config")
		}
		path, key, format = filepath.Join(configHome, "opencode", "opencode.json"), "mcp", "json"
	default:
		return map[string]any{}, false, nil
	}

	servers := map[string]any{}
	raw, err := os.ReadFile(path)
	if err == nil {
		cfg, err := unmarshalRuntimeMcpConfig(raw, format)
		if err != nil {
			return nil, true, err
		}
		if configured, ok := nestedRuntimeMcpMap(cfg, key); ok {
			for name, entry := range configured {
				servers[name] = normalizeRuntimeMcpEntry(provider, entry)
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, true, fmt.Errorf("read runtime MCP config: %w", err)
	}

	if provider == "claude" {
		// 用户配置与 Claude 的优先级一致：插件 server 只补“用户配置没定义”的名字。
		for name, entry := range loadClaudePluginMcpServerConfigs(home) {
			if _, exists := servers[name]; !exists {
				servers[name] = entry
			}
		}
	}
	return servers, true, nil
}

// normalizeRuntimeMcpEntry 把各 provider 的字段差异归一到 Multica 内部形态；目前只
// 对 codex 特殊处理。
func normalizeRuntimeMcpEntry(provider string, value any) any {
	entry, ok := value.(map[string]any)
	if !ok || provider != "codex" {
		return value
	}
	// Multica 的规范远程形态把这组字段叫 `headers`；Codex 存的是 `http_headers`。
	// 原键也保留，让较少见的 Codex 专用设置在 renderCodexMcpServersBlock 往返时不丢。
	if headers, ok := entry["http_headers"]; ok {
		if _, exists := entry["headers"]; !exists {
			entry["headers"] = headers
		}
	}
	if _, hasURL := entry["url"]; hasURL {
		if _, hasType := entry["type"]; !hasType {
			entry["type"] = "http"
		}
	}
	return entry
}

// loadClaudePluginMcpServerConfigs 汇总所有已启用 Claude 插件声明的 MCP server。
func loadClaudePluginMcpServerConfigs(home string) map[string]any {
	out := map[string]any{}
	for _, plugin := range listEnabledClaudePlugins(home) {
		manifest, _ := readClaudePluginManifest(plugin.InstallPath)
		paths := claudePluginComponentPaths(
			plugin.InstallPath,
			manifest.MCPServers,
			filepath.Join(plugin.InstallPath, ".mcp.json"),
		)
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var cfg map[string]any
			if json.Unmarshal(raw, &cfg) != nil {
				continue
			}
			servers, ok := nestedRuntimeMcpMap(cfg, "mcpServers")
			if !ok {
				continue
			}
			for name, entry := range servers {
				if _, exists := out[name]; !exists {
					out[name] = entry
				}
			}
		}
	}
	return out
}

// listRuntimeLocalMcpServers 返回可展示给用户的脱敏 runtime MCP server 清单
// （不含 URL、命令、环境变量等秘密）。
func listRuntimeLocalMcpServers(provider string) ([]runtimeLocalMcpServerSummary, bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false, fmt.Errorf("resolve user home: %w", err)
	}

	var path, key, source string
	var format string
	switch provider {
	case "claude":
		path, key, source, format = filepath.Join(home, ".claude.json"), "mcpServers", "User config", "json"
	case "codearts":
		path, key, source, format = codeArtsUserConfigPath(home), "mcp", "User config", "jsonc"
	case "codebuddy":
		path, key, source, format = codebuddyUserMcpConfigPath(home), "mcpServers", "User config", "jsonc"
	case "kimi":
		// 只做清单——kimi 有意不进 loadRuntimeMcpServerConfigs。`kimi acp` 会把这份
		// 文件与我们随 session/new 发的临时 `mcpServers` 合并，再合一次就会把每个
		// 用户 server 拉两份。
		kimiHome := strings.TrimSpace(os.Getenv("KIMI_CODE_HOME"))
		if kimiHome == "" {
			kimiHome = filepath.Join(home, ".kimi-code")
		}
		path, key, source, format = filepath.Join(kimiHome, "mcp.json"), "mcpServers", "User config", "json"
	case "codex":
		codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
		path, key, source, format = filepath.Join(codexHome, "config.toml"), "mcp_servers", "User config", "toml"
	case "cursor":
		path, key, source, format = filepath.Join(home, ".cursor", "mcp.json"), "mcpServers", "User config", "json"
	case "opencode":
		configHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
		if configHome == "" {
			configHome = filepath.Join(home, ".config")
		}
		path, key, source, format = filepath.Join(configHome, "opencode", "opencode.json"), "mcp", "User config", "json"
	case "omp":
		// 清单范围的取舍：omp 按多级优先链发现 server（.omp/mcp.json、.omp/.mcp.json、
		// profile/用户级配置，以及第三方工具配置如 .claude.json、.cursor/mcp.json、
		// .vscode/mcp.json 与项目根 mcp.json/.mcp.json）。这里的清单只读
		// ~/.omp/agent/mcp.json——用户级入口——所以“在 UI 里清光所有 server”的用户仍
		// 可能从低优先级来源继承到 server。这与其它 provider 已经做的简化一致，也避免
		// 把整套工具链状态发上线。
		path, key, source, format = filepath.Join(home, ".omp", "agent", "mcp.json"), "mcpServers", "User config", "json"
	default:
		return []runtimeLocalMcpServerSummary{}, false, nil
	}

	out := make([]runtimeLocalMcpServerSummary, 0)
	raw, err := os.ReadFile(path)
	if err == nil {
		cfg, err := unmarshalRuntimeMcpConfig(raw, format)
		if err != nil {
			return nil, true, err
		}
		if servers, ok := nestedRuntimeMcpMap(cfg, key); ok {
			out = append(out, runtimeMcpSummaries(servers, source)...)
		}
	} else if !os.IsNotExist(err) {
		return nil, true, fmt.Errorf("read runtime MCP config: %w", err)
	}

	if provider == "claude" {
		out = append(out, listClaudePluginMcpServers(home)...)
	}

	// 同名冲突时用户配置优先。插件条目在后面追加、只补用户配置没定义的名字。
	deduped := make([]runtimeLocalMcpServerSummary, 0, len(out))
	seen := make(map[string]bool)
	for _, server := range out {
		if seen[server.Name] {
			continue
		}
		seen[server.Name] = true
		deduped = append(deduped, server)
	}
	out = deduped
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, true, nil
}

// runtimeMcpSummaries 把一条条 server 配置条目压缩成对外可见的摘要（名字、传输
// 方式、来源、是否启用）。
func runtimeMcpSummaries(servers map[string]any, source string) []runtimeLocalMcpServerSummary {
	out := make([]runtimeLocalMcpServerSummary, 0, len(servers))
	for name, value := range servers {
		entry, ok := value.(map[string]any)
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		enabled := true
		if value, ok := entry["enabled"].(bool); ok {
			enabled = value
		}
		if value, ok := entry["disabled"].(bool); ok && value {
			enabled = false
		}
		out = append(out, runtimeLocalMcpServerSummary{
			Name:      name,
			Transport: runtimeMcpTransport(entry),
			Source:    source,
			Enabled:   enabled,
		})
	}
	return out
}

// listClaudePluginMcpServers 汇总所有已启用 Claude 插件声明的 MCP server 摘要，
// 来源标注为对应插件名。
func listClaudePluginMcpServers(home string) []runtimeLocalMcpServerSummary {
	out := make([]runtimeLocalMcpServerSummary, 0)
	for _, plugin := range listEnabledClaudePlugins(home) {
		manifest, _ := readClaudePluginManifest(plugin.InstallPath)
		paths := claudePluginComponentPaths(
			plugin.InstallPath,
			manifest.MCPServers,
			filepath.Join(plugin.InstallPath, ".mcp.json"),
		)
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var cfg map[string]any
			if json.Unmarshal(raw, &cfg) != nil {
				continue
			}
			servers, ok := nestedRuntimeMcpMap(cfg, "mcpServers")
			if !ok {
				continue
			}
			out = append(out, runtimeMcpSummaries(servers, "Claude Plugin · "+plugin.Name)...)
		}
	}
	return out
}

// nestedRuntimeMcpMap 按点分路径（如 "mcpServers"）逐层下钻配置 map，全程要求
// 每一层都是 map[string]any；任一层不是或缺失都返回 false。
func nestedRuntimeMcpMap(cfg map[string]any, path string) (map[string]any, bool) {
	current := cfg
	parts := strings.Split(path, ".")
	for index, part := range parts {
		value, exists := current[part]
		if !exists {
			return nil, false
		}
		mapped, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		if index == len(parts)-1 {
			return mapped, true
		}
		current = mapped
	}
	return nil, false
}

// runtimeMcpTransport 从一条 entry 里推断其传输类型：优先看 type 字段，再看有没有
// command（本地 stdio）或 url（远程 http），都判断不了返回 "unknown"。
func runtimeMcpTransport(entry map[string]any) string {
	kind, _ := entry["type"].(string)
	switch strings.ToLower(kind) {
	case "local", "stdio":
		return "stdio"
	case "remote", "http", "streamable-http":
		return "http"
	case "sse":
		return "sse"
	}
	if _, ok := entry["command"]; ok {
		return "stdio"
	}
	if _, ok := entry["url"]; ok {
		return "http"
	}
	return "unknown"
}
