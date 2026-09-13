// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（workspace_mcp.go）负责把"显式绑定到某 agent 的工作区级 MCP 服务器"
// 合入该 agent 自己的 mcp_config，算出任务运行时真正携带的配置。核心入口是
// ResolveAgentMcpConfig（任务级覆盖 mergeMCPOverlay 之前调用），其余是配套的
// 文档解析、服务名校验等辅助函数。
package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// mcpServerContainers 是一份 mcp_config 文档可能放服务器的两个顶层键。
// mcpServers 是 Claude 风格、所有 runtime 都读的标准容器；mcp 是少数 agent
// 仍在携带的旧写法，agent 设置 UI（mcp-config-model.ts）也还在原地编辑它。
// 判断"agent 已经声明了哪些服务器"时两个键都会读；写入时只写 mcpServers。
var mcpServerContainers = [...]string{"mcpServers", "mcp"}

// WorkspaceMcpBinding 表示"某个 agent 被显式授予的一个工作区 MCP 服务器"：
// 它在 agent 名下挂载的名字（Name），以及该服务器的原始配置（Config）。
type WorkspaceMcpBinding struct {
	Name   string
	Config json.RawMessage
}

// ResolveAgentMcpConfig 把"绑定到 agent 的工作区 MCP 服务器"合进该 agent 的
// mcp_config，返回任务（claim）应当携带的最终配置。它在任务级覆盖层
// （mergeMCPOverlay）之前执行，所以一份 claim 上的完整优先级是：
//
//	绑定的工作区服务器  <  agent 自己的服务器  <  每次任务覆盖
//
// 合约（GH #6062、MUL-5421）：
//
//   - 只有"显式绑定到该 agent 且仍处于启用状态"的服务器才被合入。工作区 MCP
//     服务器是"库"条目，跟工作区技能一样：建好默认不给任何人用。这一层不会
//     隐式流到任何 agent——这正是共享凭据不扩散到未授权 agent 的机制。
//
//   - 名字冲突时 agent 自己的条目优先：那是更具体的声明，也是 agent 所有者
//     直接编辑的那份。
//
//   - 一个既无绑定、自身也无配置的 agent 解析结果为 nil——"没有受管内容"——
//     它的 runtime 保持原生 MCP 继承。
//
// 形状：结果一律归一化到标准的 mcpServers 容器，包括 agent 旧式顶层 mcp 里
// 的条目也搬进去。归一化不是美化而是必须：daemon 侧做运行时合并时，只有当
// mcpServers 键不存在才会回退读旧容器（internal/daemon/runtime_mcp.go）。
// 若把绑定服务器写进 mcpServers、却把 agent 旧式条目留在 mcp 里，daemon 会
// 只读绑定集合，静默丢掉 agent 自己的服务器。
//
// 失败模式：agent 配置损坏时，原配置原样返回并附上错误——绑定一个共享服务器
// 绝不能反过来夺走 agent 今天正在跑的服务器。
func ResolveAgentMcpConfig(bound []WorkspaceMcpBinding, agentMcpConfig json.RawMessage) (json.RawMessage, error) {
	if len(bound) == 0 {
		return passthroughAgentMcpConfig(agentMcpConfig), nil
	}

	shared := make(map[string]json.RawMessage, len(bound))
	for _, server := range bound {
		if server.Name == "" || !hasManagedJSON(server.Config) {
			continue
		}
		shared[server.Name] = server.Config
	}
	if len(shared) == 0 {
		return passthroughAgentMcpConfig(agentMcpConfig), nil
	}

	if !hasManagedJSON(agentMcpConfig) {
		// agent 自己什么都没声明：它就跑着被显式授予的这些服务器。
		out, err := json.Marshal(map[string]any{"mcpServers": shared})
		if err != nil {
			return nil, fmt.Errorf("resolve agent mcp_config: marshal bound servers: %w", err)
		}
		return out, nil
	}

	agentDoc, agentServers, err := parseMcpDocument(agentMcpConfig)
	if err != nil {
		return passthroughAgentMcpConfig(agentMcpConfig), fmt.Errorf("resolve agent mcp_config: parse agent mcp_config: %w", err)
	}

	merged := make(map[string]json.RawMessage, len(shared)+len(agentServers))
	for name, server := range shared {
		// agent 可能在任意一个容器里声明了该名字；agentServers 横跨两个容器，
		// 所以这份命中检查对两种拼法都覆盖。
		if _, taken := agentServers[name]; taken {
			continue
		}
		merged[name] = server
	}
	// 再合入 agent 自己的条目。先读旧容器 mcp 再读 mcpServers，同一个名字在两处
	// 都出现时以规范容器为准——这正是 agent 设置 UI 展示的优先级
	// （mcp-config-model.ts 先读 mcpServers 再读 mcp）。
	for _, container := range [...]string{"mcp", "mcpServers"} {
		own, err := unmarshalServerMap(agentDoc[container])
		if err != nil {
			return passthroughAgentMcpConfig(agentMcpConfig), fmt.Errorf("resolve agent mcp_config: agent %s: %w", container, err)
		}
		for name, server := range own {
			merged[name] = server
		}
	}

	out := make(map[string]json.RawMessage, len(agentDoc)+1)
	for k, v := range agentDoc {
		// 两个服务器容器都已被消费进上面的规范 map；把旧键留在原处等于再给
		// daemon 递一份过期的第二副本。
		if k == "mcpServers" || k == "mcp" {
			continue
		}
		out[k] = v
	}
	serversBytes, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("resolve agent mcp_config: marshal merged servers: %w", err)
	}
	out["mcpServers"] = serversBytes

	final, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("resolve agent mcp_config: marshal merged document: %w", err)
	}
	return final, nil
}

// parseMcpDocument 把一份 mcp_config 文档解成"顶层键的 map"，外加一份
// "跨两个容器声明的全部服务器名"集合。名字集合是判断"该名字 agent 是否
// 已占有"的依据，所以必须也覆盖旧式拼法。
func parseMcpDocument(raw json.RawMessage) (map[string]json.RawMessage, map[string]struct{}, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, err
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	names := map[string]struct{}{}
	for _, container := range mcpServerContainers {
		servers, err := unmarshalServerMap(doc[container])
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", container, err)
		}
		for name := range servers {
			names[name] = struct{}{}
		}
	}
	return doc, names, nil
}

// validateWorkspaceMcpServerEntry 在单条服务器条目入库前检查它的形状。
// 刻意做得很浅——只查形状、绝不碰内容：内容是 runtime 相关的，里面常常带着
// 密钥，既不想检视，也不想在报错时把它原样回显出来。
func validateWorkspaceMcpServerEntry(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return errors.New("config must be a JSON object")
	}
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &entry); err != nil {
		// 不包装底层错误：它可能回显条目里的片段，而条目经常内嵌 API token。
		return errors.New("config must be a JSON object")
	}
	if len(entry) == 0 {
		return errors.New("config must not be empty")
	}
	return nil
}

// validateWorkspaceMcpServerName 校验服务器名。这个名字是 runtime 把服务器
// 挂载到 agent 下所用的名字，也会与 agent 自己的配置撞名，因此规则与 agent
// 设置弹窗里强制的一致。
func validateWorkspaceMcpServerName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return errors.New("name may only contain letters, digits, hyphens, and underscores")
		}
	}
	return nil
}
