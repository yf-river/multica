// 平台内置 Mika 的身份与创建时默认提示词。
package service

import (
	_ "embed"
	"strings"
)

// MikaSystemKey 标记工作区内置的 Chief of Staff agent“Mika”。它是所有服务端决策所用的 agent 身份——
// 绝不是显示名（显示名 owner 可随意改）。
//
// 该行保持 kind='user'：此 schema 中 kind='system' 表示“不可见的执行载体”（从 agent 列表与指派面隐藏，
// runtime 消失时硬删除），而 Mika 需要与这三者全部相反。
const MikaSystemKey = "mika"

// MikaDefaultName 是 agent 创建时的默认名字。owner 可改名；服务端没有任何逻辑以名字为键，
// 提示词基于当前名字模板化。
const MikaDefaultName = "Mika"

// MikaDefaultInstructions 仅用于创建时填充；后续编辑和执行使用数据库中的完整提示词。
//
//go:embed builtin_agents/mika/INSTRUCTIONS.md
var mikaDefaultInstructions string

func MikaDefaultInstructions(displayName string) string {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = MikaDefaultName
	}
	return strings.ReplaceAll(strings.TrimRight(mikaDefaultInstructions, "\n"), "{{AGENT_NAME}}", name)
}
