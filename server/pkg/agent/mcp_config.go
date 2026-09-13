package agent

// 文件职责：agent.mcp_config 字段"三段语义"的判定——区分"没配置"与"显式配置了
// 一个空集合"。被各 backend 启动路径（qwen、opencode、ACP 系列等）用来决定是否把
// MCP 配置注入。

import (
	"bytes"
	"encoding/json"
)

// hasManagedMcpConfig 保留该 API 的三态 MCP 语义。只有 SQL NULL / JSON null 表示
// "继承运行时配置"；任何对象——包括显式的空对象——都算受管集合，并启用严格模式。
func hasManagedMcpConfig(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}
