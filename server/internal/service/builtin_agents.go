// builtin_agents.go —— 平台内置系统 agent（Mika）：常量与提示词组装。
// 提示词正文随编译期 embed 打包，避免建号时复制进 agent.instructions，
// 使改嵌入文件并发布即可更新每个既有工作区。
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

// mikaNamePlaceholder 是内嵌提示词里被替换成 agent 当前显示名的占位符。
const mikaNamePlaceholder = "{{AGENT_NAME}}"

// 提示词的“系统半区”。`{{AGENT_NAME}}` 会被替换成 agent 当前显示名——用占位符而非格式动词，提示词里的偶然 %
// 永远不会变成格式化错误：runtime brief 已宣告 “**You are: <name>**”，若这里硬编码 “You are Mika”，
// owner 一改名就会自相矛盾。
//
//go:embed builtin_agents/mika/INSTRUCTIONS.md
var mikaSystemInstructions string

// mikaWorkspaceNotesSection 引出工作区自己的增补，并说明它们相对系统半区的优先级。
//
// 它放在这里而非嵌入文件末尾：没有笔记的工作区——最初每个工作区都是——若放在末尾，提示词会宣告一个其下
// 空空如也的章节。与笔记一起输出还能让规则紧邻其管辖的文本。
const mikaWorkspaceNotesSection = `## Workspace notes

Workspace notes below add this team's context and preferences — repositories, languages, conventions, routing defaults. Follow them ahead of your own defaults; they refine how you apply these instructions, and they do not remove the identity or confirmation duties above.

Added by this workspace's admins:`

// MikaSystemInstructions 返回给定显示名下 agent 的、产品拥有的半区提示词。
//
// 这正是 system-agent 模型的意义：文本随服务器二进制发布，而不是在创建时复制进 agent.instructions，
// 编辑嵌入文件并发布即可在下一次任务时更新每个既有工作区。不会向任何 agent 行写入，因此工作区自己的笔记
// 绝不会被一次发布覆盖。
func MikaSystemInstructions(displayName string) string {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = MikaDefaultName
	}
	return strings.ReplaceAll(
		strings.TrimRight(mikaSystemInstructions, "\n"),
		mikaNamePlaceholder,
		name,
	)
}

// ComposeMikaInstructions 把工作区笔记层叠到产品拥有的系统提示词之下。workspaceNotes 即 agent.instructions——
// 唯一可由工作区写入的半区。
func ComposeMikaInstructions(displayName, workspaceNotes string) string {
	system := MikaSystemInstructions(displayName)
	notes := strings.TrimSpace(workspaceNotes)
	if notes == "" {
		return system
	}
	return system + "\n\n" + mikaWorkspaceNotesSection + "\n\n" + notes
}
