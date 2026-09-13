package agent

// 文件职责：在 Windows 上"加固"浏览器类 MCP 服务器的配置——Playwright 补
// --config 指向禁 GPU 的临时浏览器配置；chrome-devtools-mcp 在用户没显式指定时补
// --executablePath 指向系统自带的 Edge。非 Windows 平台原样放行。
//
// 背景：Windows 上没有可靠的系统 Chrome 路径，浏览器自动化（Playwright / CDP）
// 首次运行往往因找不到浏览器或 GPU 初始化失败而挂。把浏览器相关的坑在注入前
// 就地解决，比把坏配置原样交给 agent 再让它在运行中失败好。
//
// 注意：Go 运行时可注入（browserMcpStat/browserMcpEnv/browserMcpGOOS），便于测试。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	// 以下三个变量可注入，让非 Windows 主机也能单测 Windows 逻辑。
	browserMcpGOOS = runtime.GOOS
	browserMcpStat = os.Stat
	browserMcpEnv  = os.Getenv
)

// hardenBrowserMcpConfig 只对 Windows 做加固，其它平台原样返回。
func hardenBrowserMcpConfig(raw json.RawMessage, tempDir string) ([]byte, error) {
	if browserMcpGOOS != "windows" {
		return raw, nil
	}
	return hardenWindowsBrowserMcpConfig(raw, tempDir)
}

// hardenWindowsBrowserMcpConfig 遍历配置里的 mcpServers，识别 Playwright 与
// chrome-devtools 两个浏览器服务器并就地加固。任何一步解析失败都原样放行（不做
// 破坏性改写）。有改动才重新序列化。
func hardenWindowsBrowserMcpConfig(raw json.RawMessage, tempDir string) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return raw, nil
	}
	serversRaw, ok := top["mcpServers"]
	if !ok {
		return raw, nil
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(serversRaw, &servers); err != nil {
		return raw, nil
	}

	changed := false
	for name, serverRaw := range servers {
		var entry map[string]any
		if err := json.Unmarshal(serverRaw, &entry); err != nil {
			continue
		}
		args, ok := stringSlice(entry["args"])
		if !ok {
			continue
		}

		lowerName := strings.ToLower(name)
		switch {
		// 识别 Playwright：按名字或包路径（兼容 `\` 与 `/` 两种写法）。
		case lowerName == "playwright" || argsContain(args, "@playwright/mcp") || argsContain(args, `@playwright\mcp`):
			nextArgs, err := hardenWindowsPlaywrightMcpArgs(args, tempDir)
			if err != nil {
				return nil, err
			}
			if !sameStringSlice(args, nextArgs) {
				entry["args"] = nextArgs
				servers[name], changed = mustMarshalRaw(entry), true
			}
		// 识别 chrome-devtools-mcp：用户没固定浏览器时补 --executablePath=Edge。
		case lowerName == "chrome-devtools" || argsContain(args, "chrome-devtools-mcp"):
			if path, ok := windowsChromiumFallbackExecutable(); ok && shouldPinChromeDevToolsExecutable(args) {
				entry["args"] = append(args, "--executablePath="+path)
				servers[name], changed = mustMarshalRaw(entry), true
			}
		}
	}
	if !changed {
		return raw, nil
	}

	top["mcpServers"] = mustMarshalRaw(servers)
	data, err := json.Marshal(top)
	if err != nil {
		return nil, fmt.Errorf("marshal hardened mcp config: %w", err)
	}
	return data, nil
}

// hardenWindowsPlaywrightMcpArgs 在用户没显式给 Playwright 指定 --config /
// --cdp-endpoint / --extension 时，补一份"禁 GPU"的临时浏览器配置（写进
// tempDir）。GPU 初始化失败是 Windows 无头环境最常见的启动失败源。
func hardenWindowsPlaywrightMcpArgs(args []string, tempDir string) ([]string, error) {
	// 用户已有浏览器相关显式配置，别覆盖他们的意图。
	if hasFlag(args, "--config") || hasFlag(args, "--cdp-endpoint") || hasFlag(args, "--extension") {
		return args, nil
	}
	configPath := filepath.Join(tempDir, "playwright-windows-browser.json")
	config := map[string]any{
		"browser": map[string]any{
			"launchOptions": map[string]any{
				"args": []string{"--disable-gpu"},
			},
		},
	}
	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal playwright mcp browser config: %w", err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return nil, fmt.Errorf("write playwright mcp browser config: %w", err)
	}
	return append(args, "--config", configPath), nil
}

// windowsChromiumFallbackExecutable 找一个可用的 Chromium 系浏览器：优先
// MULTICA_CHROME_DEVTOOLS_EXECUTABLE_PATH 环境变量，否则在常见安装根里找 Edge
// （msedge.exe）。
func windowsChromiumFallbackExecutable() (string, bool) {
	if path := strings.TrimSpace(browserMcpEnv("MULTICA_CHROME_DEVTOOLS_EXECUTABLE_PATH")); path != "" {
		return path, true
	}
	for _, root := range []string{
		browserMcpEnv("ProgramFiles(x86)"),
		browserMcpEnv("ProgramFiles"),
		browserMcpEnv("LocalAppData"),
	} {
		if strings.TrimSpace(root) == "" {
			continue
		}
		path := windowsPathJoin(root, "Microsoft", "Edge", "Application", "msedge.exe")
		if _, err := browserMcpStat(path); err == nil {
			return path, true
		}
	}
	return "", false
}

// windowsPathJoin 按 Windows 习惯用反斜杠拼路径（避免 filepath 在测试环境
// 按宿主系统拼出 `/`）。
func windowsPathJoin(root string, elems ...string) string {
	root = strings.TrimRight(root, `\/`)
	if root == "" {
		return ""
	}
	return root + `\` + strings.Join(elems, `\`)
}

// shouldPinChromeDevToolsExecutable 报告是否该为 chrome-devtools-mcp 固定
// 浏览器路径：用户已通过任一旗标指定（可执行路径、频道、URL、端口、自动连接等）
// 时返回 false，尊重用户选择。
func shouldPinChromeDevToolsExecutable(args []string) bool {
	for _, flag := range []string{
		"--executablePath",
		"--executable-path",
		"-e",
		"--channel",
		"--browserUrl",
		"--browser-url",
		"-u",
		"--wsEndpoint",
		"--ws-endpoint",
		"-w",
		"--autoConnect",
		"--auto-connect",
	} {
		if hasFlag(args, flag) {
			return false
		}
	}
	return true
}

// stringSlice 把 any 断言成 []string；不是字符串数组返回 false。
func stringSlice(v any) ([]string, bool) {
	raw, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// argsContain 报告 args 里是否有某个参数包含 needle（大小写不敏感）。
func argsContain(args []string, needle string) bool {
	needle = strings.ToLower(needle)
	for _, arg := range args {
		if strings.Contains(strings.ToLower(arg), needle) {
			return true
		}
	}
	return false
}

// hasFlag 报告 args 里是否有该旗标（完全相等或以 `flag=` 开头）。
func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag || strings.HasPrefix(arg, flag+"=") {
			return true
		}
	}
	return false
}

// sameStringSlice 比较两个字符串切片是否逐元素相等。
func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mustMarshalRaw 序列化失败即 panic——这里的输入都是上面刚构造/解析过的合法值，
// panic 表示编程错误而非运行时数据问题。
func mustMarshalRaw(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
