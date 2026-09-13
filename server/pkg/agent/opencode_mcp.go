package agent

// 文件职责：OpenCode 的 MCP 配置翻译与校验层。
//
// 把 multica 的 agent.mcp_config 负载翻译成 OpenCode 认得的样子，再经环境变量
// OPENCODE_CONFIG_CONTENT 注入。只接受两种输入形态：Claude 风格的
// {"mcpServers": {...}} 与 OpenCode 原生的 {"mcp": {...}}。翻译后的条目会重新走
// OpenCode 原生 schema 做严格校验（DisallowUnknownFields，对齐 schema 的
// additionalProperties:false），让写错的字段在 daemon 侧就以清晰报错拒绝，而不是
// 原样透传给 OpenCode 后在启动时以诡异方式失败/崩掉 CLI。
//
// 被谁消费：opencode backend 的启动路径；输出是 OPENCODE_CONFIG_CONTENT 的
// 取值（只含 OpenCode 配置里 mcp 这一片）。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// opencodeMCPLocal 对应 OpenCode 配置 schema 里 McpLocalConfig 那一片
// （https://opencode.ai/config.json）。解码时开了 DisallowUnknownFields，任何不在
// 本结构里的字段都会在校验阶段直接失败——与 schema 的 `additionalProperties: false`
// 保持一致。
type opencodeMCPLocal struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
	Timeout     *int              `json:"timeout,omitempty"`
}

// opencodeMCPRemote 对应 McpRemoteConfig 那一片。OAuth 用 json.RawMessage 兜住，
// 因为 schema 允许两种形态（oauth-config 对象，或字面量 `false`）；类型判别放在
// validateOpenCodeOAuth 里做。
type opencodeMCPRemote struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	OAuth   json.RawMessage   `json:"oauth,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
	Timeout *int              `json:"timeout,omitempty"`
}

// opencodeMCPEnabledOnly 是 OpenCode schema 接受的第三种形态——裸的
// {"enabled": true|false} 条目，用来开/关一条从全局或项目配置继承来的服务器而不
// 重新定义它。判别方式是"没有 type 字段，但设了 enabled"。
type opencodeMCPEnabledOnly struct {
	Enabled *bool `json:"enabled"`
}

// opencodeMCPOAuth 对应 McpOAuthConfig。callbackPort 在 validateOpenCodeOAuth 里
// 做范围校验（schema 要求 1..65535）。用 `*int` 承载，是为了把"没填"（nil）和
// "显式填了 0"（越界被拒）区分开——Go 的 int 零值会把这两种情况压成一个。
type opencodeMCPOAuth struct {
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	Scope        string `json:"scope,omitempty"`
	CallbackPort *int   `json:"callbackPort,omitempty"`
	RedirectURI  string `json:"redirectUri,omitempty"`
}

// buildOpenCodeMCPConfigContent 把 agent.mcp_config 负载翻译成一段 JSON 字符串，
// 里面只带 OpenCode 配置 schema 的 `mcp` 片，适合塞进 OPENCODE_CONFIG_CONTENT
// 环境变量。raw 为空时返回 ("", nil)，调用方就整个跳过这个环境变量，而不是注入
// 一段空配置。
//
// 背景：OPENCODE_CONFIG_CONTENT 是 OpenCode 的通用"内联配置注入"机制——它接受
// OpenCode schema 的任意子集（model、agent、mode、plugin、mcp……），不限于 MCP。
// 本函数只处理 MCP，因为 agent.mcp_config 当前只打通了这一个字段；将来若有别的
// multica 字段要投影进同一环境变量（比如 agent 级模型覆盖），"拼装后注入"这一步
// 就得挪到上层，把多个片段合并进同一个 OPENCODE_CONFIG_CONTENT。眼下 MCP 是唯一
// 消费方。
//
// 为什么用环境变量注入而不是写 <workdir>/opencode.json：任务工作目录在同一条
// (agent, issue) 的多个回合间会被复用，agent 本身（或用户）可能已经往
// <workdir>/opencode.json 写了模型/工具/权限设置。为了 mcp_config 的生命周期去写
// 或删这个文件，会静默覆盖他们的状态。环境变量方案完全不碰工作目录——不落盘、
// 也无需清理，因为环境变量随派生的进程一起消失。
//
// OPENCODE_CONFIG_CONTENT 是 OpenCode v1.4.10（2025-09）加进来的，官方
// @opencode-ai/sdk 注入运行时配置用的就是它。OpenCode 在项目配置循环之后、以
// "local" 作用域合并它，所以它与全局+项目配置做深合并（可观察行为等同于写进
// <workdir>/opencode.json）；但它更靠后的合并位置，也让 daemon 注入的条目优先于
// 用户恰好写进项目文件里的同名条目——这正符合 agent.mcp_config 是"daemon 权威
// 管理字段"的语义。
func buildOpenCodeMCPConfigContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	servers, err := translateMCPConfigForOpenCode(raw)
	if err != nil {
		return "", err
	}
	// 翻译完为空（没有服务器）时，可观察行为与 raw 为空一样——返回 "" 让调用方
	// 跳过环境变量，JSON null / 空对象就不会覆盖用户在
	// agent.custom_env.OPENCODE_CONFIG_CONTENT 里的东西。
	if len(servers) == 0 {
		return "", nil
	}
	data, err := json.Marshal(map[string]any{"mcp": servers})
	if err != nil {
		return "", fmt.Errorf("opencode mcp_config: marshal: %w", err)
	}
	return string(data), nil
}

// translateMCPConfigForOpenCode 把 agent.mcp_config 负载转成 OpenCode 在 `mcp`
// 键下期望的形态。接受两种输入形态：
//
//   - Claude 风格 {"mcpServers": {name: {url|command, ...}}}——翻译成 OpenCode 的
//     `type: "local"|"remote"` 形态，command 收成数组、env 改名 environment 等。
//   - OpenCode 原生 {"mcp": {name: {type, ...}}}——对每条做 schema 校验后透传。
//     不做校验的话，一份写坏的 agent.mcp_config 会被原样抛给 OpenCode，要么静默
//     禁用该服务器，要么让 CLI 一启动就崩。
func translateMCPConfigForOpenCode(raw json.RawMessage) (map[string]any, error) {
	var payload struct {
		MCPServers map[string]map[string]any  `json:"mcpServers"`
		MCP        map[string]json.RawMessage `json:"mcp"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("opencode mcp_config: parse mcp_config: %w", err)
	}
	if len(payload.MCPServers) == 0 {
		if payload.MCP == nil {
			return map[string]any{}, nil
		}
		return validateOpenCodeNativeMCPMap(payload.MCP)
	}

	// 两种输入可能并存；都转完再合。原生条目直接校验，Claude 风格条目翻译后
	// 还要再校验一遍（见下）。
	servers := make(map[string]any, len(payload.MCPServers)+len(payload.MCP))
	for name, rawEntry := range payload.MCP {
		validated, err := validateOpenCodeNativeMCPEntry(name, rawEntry)
		if err != nil {
			return nil, err
		}
		servers[name] = validated
	}
	for name, server := range payload.MCPServers {
		translated, err := translateMCPServerForOpenCode(name, server)
		if err != nil {
			return nil, err
		}
		// 翻译后的条目再过一遍原生校验器，让两种输入形态（Claude 风格 mcpServers
		// 与 OpenCode 原生 mcp）被同一套 schema 规则把关。缺了这次复查，Claude
		// 风格输入里写坏的 headers/environment/oauth/timeout 就会绕过 daemon 校验，
		// 变成一句让人摸不着头脑的 OpenCode 启动报错，而不是清晰的 daemon 侧拒绝。
		// 一个校验器，一处真相。
		rawTranslated, err := json.Marshal(translated)
		if err != nil {
			return nil, fmt.Errorf("opencode mcp_config: server %q: marshal translated entry: %w", name, err)
		}
		validated, err := validateOpenCodeNativeMCPEntry(name, rawTranslated)
		if err != nil {
			return nil, err
		}
		servers[name] = validated
	}
	return servers, nil
}

// validateOpenCodeNativeMCPMap 校验一张原生形态 mcp 图里的每条，返回并行的
// map[string]any（每条都经过 json 往返，输出就是 OpenCode 在它配置里会看到的
// 逐字形态）。
func validateOpenCodeNativeMCPMap(mcp map[string]json.RawMessage) (map[string]any, error) {
	out := make(map[string]any, len(mcp))
	for name, raw := range mcp {
		validated, err := validateOpenCodeNativeMCPEntry(name, raw)
		if err != nil {
			return nil, err
		}
		out[name] = validated
	}
	return out, nil
}

// validateOpenCodeNativeMCPEntry 按 OpenCode schema 严格解码一条原生形态条目，
// 返回等价的 map[string]any 表示。解码刻意严格（DisallowUnknownFields）——
// 任何落在 McpLocalConfig / McpRemoteConfig / {"enabled": bool} 三种形态之外的
// 字段都会被拒，与 schema 的 `additionalProperties: false` 对齐，让用户的拼写
// 错误在到达 OpenCode 之前就变成报错。
func validateOpenCodeNativeMCPEntry(name string, raw json.RawMessage) (map[string]any, error) {
	wrap := func(err error) error {
		return fmt.Errorf("opencode mcp_config: server %q: %w", name, err)
	}

	// JSON 对象前置检查：下面的类型判别探测与严格解码器都假设输入是对象；
	// 没有这层护栏，原始类型（字符串/数字/数组/null）会抛出一句让人困惑的
	// 解码错误。
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, wrap(errors.New("entry must be a JSON object"))
	}

	// 类型判别探测：先瞄一眼 `type` 决定走哪个严格解码目标。这次首解故意宽松，
	// 让 "type: 5" 报出清晰的 "type must be a string"，而不是严格解码那种笼统的
	// "json: cannot unmarshal number"。
	var probe struct {
		Type *json.RawMessage `json:"type,omitempty"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, wrap(fmt.Errorf("parse: %w", err))
	}
	var typeStr string
	if probe.Type != nil {
		if err := json.Unmarshal(*probe.Type, &typeStr); err != nil {
			return nil, wrap(fmt.Errorf("`type` must be a string, got %s", strings.TrimSpace(string(*probe.Type))))
		}
	}

	switch typeStr {
	case "local":
		var entry opencodeMCPLocal
		if err := strictDecode(raw, &entry); err != nil {
			return nil, wrap(err)
		}
		if len(entry.Command) == 0 {
			return nil, wrap(errors.New("local server missing required field `command`"))
		}
		if entry.Timeout != nil && *entry.Timeout <= 0 {
			return nil, wrap(fmt.Errorf("`timeout` must be a positive integer, got %d", *entry.Timeout))
		}
	case "remote":
		var entry opencodeMCPRemote
		if err := strictDecode(raw, &entry); err != nil {
			return nil, wrap(err)
		}
		if entry.URL == "" {
			return nil, wrap(errors.New("remote server missing required field `url`"))
		}
		if entry.Timeout != nil && *entry.Timeout <= 0 {
			return nil, wrap(fmt.Errorf("`timeout` must be a positive integer, got %d", *entry.Timeout))
		}
		if len(entry.OAuth) > 0 {
			if err := validateOpenCodeOAuth(entry.OAuth); err != nil {
				return nil, wrap(fmt.Errorf("`oauth`: %w", err))
			}
		}
	case "":
		// 没有 type 字段。裸的 {"enabled": bool} 覆盖形态是 OpenCode 的第三种
		// 原生变体；其它没 type 的东西都是写坏的 local/remote。这里只报一句友好
		// 的"缺 type"错误，而不是把严格解码的 "json: unknown field" 透出去——用户
		// 通常只是没意识到自己误用了覆盖形态。
		var entry opencodeMCPEnabledOnly
		if err := strictDecode(raw, &entry); err != nil || entry.Enabled == nil {
			return nil, wrap(errors.New("missing required field `type` (must be \"local\" or \"remote\", or use bare {\"enabled\": bool} to override an inherited server)"))
		}
	default:
		return nil, wrap(fmt.Errorf("invalid type %q (must be \"local\" or \"remote\")", typeStr))
	}

	// 校验通过后，把原始字节重新解成 map[string]any 作输出。可观察表示不变，
	// 只是对调用方以通用 map 形态呈现。
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, wrap(fmt.Errorf("parse: %w", err))
	}
	return out, nil
}

// strictDecode 用开了 DisallowUnknownFields 的 json.Decoder 解码，目标结构体 tag
// 之外的任何字段都会被拒，从而执行 schema 的 `additionalProperties: false`。
func strictDecode(raw json.RawMessage, target any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(target)
}

// validateOpenCodeOAuth 执行 OpenCode schema 里 `oauth: McpOAuthConfig | false`
// 这个联合类型。字面量 `false` 表示完全禁用 OAuth（覆盖默认的自动探测）；其它任何
// 原始类型或 `true` 都被视为有歧义而拒绝。
func validateOpenCodeOAuth(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("false")) {
		return nil
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("must be an object or `false`, got %s", string(trimmed))
	}
	var oauth opencodeMCPOAuth
	if err := strictDecode(raw, &oauth); err != nil {
		return err
	}
	// callbackPort：nil 表示没填（合法）；凡是具体值，按 schema 都必须在 1..65535。
	// 用指针类型才能拒绝显式的 `"callbackPort": 0`，而不是把它当 Go 的 int 零值
	// 悄悄收下。
	if oauth.CallbackPort != nil && (*oauth.CallbackPort < 1 || *oauth.CallbackPort > 65535) {
		return fmt.Errorf("`callbackPort` must be in 1..65535, got %d", *oauth.CallbackPort)
	}
	return nil
}

// translateMCPServerForOpenCode 把一条 Claude 风格 mcpServers 条目转成 OpenCode
// 原生条目。`enabled` 字段只在源显式设置了它时才输出：OpenCode 缺省视为启用，
// 硬塞一个 `enabled: true` 只会给合并后的配置添噪音。
func translateMCPServerForOpenCode(name string, server map[string]any) (map[string]any, error) {
	if url, ok := stringField(server, "url"); ok && url != "" {
		out := map[string]any{
			"type": "remote",
			"url":  url,
		}
		if v, ok := server["enabled"].(bool); ok {
			out["enabled"] = v
		}
		copyIfPresent(out, server, "headers")
		copyIfPresent(out, server, "oauth")
		copyIfPresent(out, server, "timeout")
		return out, nil
	}

	command, err := openCodeCommand(server)
	if err != nil {
		return nil, fmt.Errorf("server %q: %w", name, err)
	}
	// 没有 url 也没有 command 的条目两不是，按配置错误拒绝。
	if len(command) == 0 {
		return nil, fmt.Errorf("server %q has neither url nor command", name)
	}
	out := map[string]any{
		"type":    "local",
		"command": command,
	}
	if v, ok := server["enabled"].(bool); ok {
		out["enabled"] = v
	}
	// Claude 风格叫 env，OpenCode 叫 environment；两种写法都认。
	if env, ok := server["env"]; ok {
		out["environment"] = env
	} else {
		copyIfPresent(out, server, "environment")
	}
	copyIfPresent(out, server, "timeout")
	return out, nil
}

// openCodeCommand 把 `command` 字段归一成字符串数组。Claude 的 mcpServers 接受
// "单条字符串 + 独立 args"；OpenCode 期望合并后的一个数组。已经成数组的（部分 MCP
// 生成器会这么给）做类型检查后原样放行。
func openCodeCommand(server map[string]any) ([]string, error) {
	raw, ok := server["command"]
	if !ok {
		return nil, nil
	}
	switch v := raw.(type) {
	case string:
		cmd := []string{v}
		args, err := stringSliceField(server, "args")
		if err != nil {
			return nil, err
		}
		return append(cmd, args...), nil
	case []any:
		cmd := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("command array must contain only strings")
			}
			cmd = append(cmd, s)
		}
		return cmd, nil
	default:
		return nil, fmt.Errorf("command must be a string or string array")
	}
}

// stringField 读 map 里的字符串字段；类型不对或不存在都返回 (零值, false)。
func stringField(m map[string]any, key string) (string, bool) {
	v, ok := m[key].(string)
	return v, ok
}

// stringSliceField 把字段读成字符串数组；不存在返回 nil，类型/元素不对返回错误。
func stringSliceField(m map[string]any, key string) ([]string, error) {
	raw, ok := m[key]
	if !ok {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", key)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s must contain only strings", key)
		}
		out = append(out, s)
	}
	return out, nil
}

// copyIfPresent 把 src 里存在的 key 原样拷进 dst。
func copyIfPresent(dst, src map[string]any, key string) {
	if v, ok := src[key]; ok {
		dst[key] = v
	}
}
