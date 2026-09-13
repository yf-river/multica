// 本文件实现 MCP 配置的「叠加」（overlay）合并：把一次任务特有的 overlay 叠加在
// agent 保存的 mcp_config 之上，产出供 daemon 认领线上形状用的合并 JSON。
//
// 合并契约刻意做浅：双方都期望是 Claude 风格 `{"mcpServers": {...}}` 形状（各 runtime
// 经 execenv 的 cursor/opencode/codex/hermes sidecar 生成器消费）。按「服务器名」合并，
// 名字冲突时 overlay 赢——overlay 携带的是实时、用户维度的会话 URL（如用户自己的
// Composio MCP bearer），agent 同名保存的条目是过期或管理员共享的占位。
//
// 失败语义：输入畸形时返回 agent 配置原样（附错误）。绝不允许因 overlay JSON 坏了就
// 静默丢掉 agent 已保存的服务器——那会出其不意地禁用已有 MCP 工具。
package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// mergeMCPOverlay 把 per-task overlay 叠加到 agent 保存的 mcp_config 上，返回合并后的
// JSON。合并规则见文件头：按 mcpServers 下的服务器名合并、冲突时 overlay 赢；顶层
// 非 mcpServers 键只保留 agent 侧。两者皆空返回 nil，让 daemon 的
// hasManagedCursorMcpConfig 短路继续把该任务当作「没有受管 MCP」。
//
// 失败模式：输入畸形时 agent 配置原样返回（带错误）。调用方绝不能因为 overlay JSON
// 坏了就静默丢掉 agent 已保存的服务器——那会出其不意地禁用现有 MCP 工具。
func mergeMCPOverlay(agentMcpConfig, overlay json.RawMessage) (json.RawMessage, error) {
	if !hasManagedJSON(overlay) {
		return passthroughAgentMcpConfig(agentMcpConfig), nil
	}
	if !hasManagedJSON(agentMcpConfig) {
		// 单独重序列化 overlay，让 daemon 收到精确的规范形状（入参可能带着 JSONB 表示
		// 留下的任意空白）。
		var oCfg map[string]json.RawMessage
		if err := json.Unmarshal(overlay, &oCfg); err != nil {
			return nil, fmt.Errorf("merge mcp overlay: parse overlay: %w", err)
		}
		out, err := json.Marshal(oCfg)
		if err != nil {
			return nil, fmt.Errorf("merge mcp overlay: marshal overlay: %w", err)
		}
		return out, nil
	}

	var aCfg map[string]json.RawMessage
	if err := json.Unmarshal(agentMcpConfig, &aCfg); err != nil {
		// agent 配置畸形：表面报错，但原样返回原始字节——让 agent 的设置行为与
		// 无 overlay 路径一致（那条路径本来也会把坏字节带下去）。
		return passthroughAgentMcpConfig(agentMcpConfig), fmt.Errorf("merge mcp overlay: parse agent mcp_config: %w", err)
	}
	var oCfg map[string]json.RawMessage
	if err := json.Unmarshal(overlay, &oCfg); err != nil {
		return passthroughAgentMcpConfig(agentMcpConfig), fmt.Errorf("merge mcp overlay: parse overlay: %w", err)
	}

	// 分别取出双方的 `mcpServers` 子 map；缺省为空——顶层合法但没有服务器时按「没有」
	// 处理。
	aServers, err := unmarshalServerMap(aCfg["mcpServers"])
	if err != nil {
		return passthroughAgentMcpConfig(agentMcpConfig), fmt.Errorf("merge mcp overlay: agent mcpServers: %w", err)
	}
	oServers, err := unmarshalServerMap(oCfg["mcpServers"])
	if err != nil {
		return passthroughAgentMcpConfig(agentMcpConfig), fmt.Errorf("merge mcp overlay: overlay mcpServers: %w", err)
	}

	merged := make(map[string]json.RawMessage, len(aServers)+len(oServers))
	for k, v := range aServers {
		merged[k] = v
	}
	// 冲突时 overlay 赢。
	for k, v := range oServers {
		merged[k] = v
	}

	// 重建：保留 agent 配置里除 mcpServers 外的顶层键，再把合并后的 mcpServers 写回。
	// 这是唯一动顶层键的地方；其余一律原样保留。
	out := make(map[string]json.RawMessage, len(aCfg)+1)
	for k, v := range aCfg {
		if k == "mcpServers" {
			continue
		}
		out[k] = v
	}
	if len(merged) > 0 {
		serversBytes, err := json.Marshal(merged)
		if err != nil {
			return nil, fmt.Errorf("merge mcp overlay: marshal merged servers: %w", err)
		}
		out["mcpServers"] = serversBytes
	}
	if len(out) == 0 {
		return nil, nil
	}
	final, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("merge mcp overlay: marshal merged: %w", err)
	}
	return final, nil
}

// hasManagedJSON 判断一段原始 JSON 是否真的带有受管载荷（非空且不是字面 null）。
// 与 daemon 侧 hasManagedCursorMcpConfig 用同一约定，保证合并尊重每个消费者都同意的
// 「缺省」语义。
func hasManagedJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// passthroughAgentMcpConfig 原样返回 agent 配置；配置缺省时返回 nil。用于提前返回
// 路径，让把返回值赋进线上字段的调用方保持与现在一致的 nil/非 nil 划分。
func passthroughAgentMcpConfig(agentMcpConfig json.RawMessage) json.RawMessage {
	if !hasManagedJSON(agentMcpConfig) {
		return nil
	}
	return agentMcpConfig
}

// unmarshalServerMap 把 `mcpServers` 子对象解成按服务器名索引的 map。nil/缺省值返回
// 空 map（而非错误），让调用方少一次 nil 判断。
func unmarshalServerMap(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if !hasManagedJSON(raw) {
		return map[string]json.RawMessage{}, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return map[string]json.RawMessage{}, nil
	}
	// 提前拒绝「服务器条目不是对象」的情况——每个 runtime 都要求内层值是对象，否则
	// sidecar 生成器会 500。镜像 parseCursorManagedMcpServers 的守卫。
	for name, server := range m {
		if name == "" {
			return nil, errors.New("mcp server name must not be empty")
		}
		trimmed := bytes.TrimSpace(server)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return nil, fmt.Errorf("mcpServers.%s must be a JSON object", name)
		}
	}
	return m, nil
}
