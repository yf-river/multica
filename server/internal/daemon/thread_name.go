package daemon

import "strings"

// codexThreadNameMaxRunes 限制派发给 Codex 的线程名最大长度（按 rune 计），
// 避免超长标题被截断成不可读的省略号或影响 thread 标识。
const codexThreadNameMaxRunes = 120

// deriveTaskThreadName 按优先级从任务各字段里挑一个可读名称作为 agent 线程名：
// 显式 ThreadName 优先，其次 autopilot 标题、quick-create 提示、chat 消息与
// 触发评论内容；全部为空则返回空串。
func deriveTaskThreadName(task Task) string {
	candidates := []string{
		task.ThreadName,
		task.AutopilotTitle,
		task.QuickCreatePrompt,
		task.ChatMessage,
		task.TriggerCommentContent,
	}
	for _, candidate := range candidates {
		if name := normalizeThreadName(candidate, codexThreadNameMaxRunes); name != "" {
			return name
		}
	}
	return ""
}

// normalizeThreadName 把候选字符串压成单行（空白折叠为空格）并截断到 maxRunes
// 个 rune；超过上限时在末尾追加 "..."（除非上限过小）。
func normalizeThreadName(s string, maxRunes int) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	normalized := strings.Join(fields, " ")
	if maxRunes <= 0 {
		return normalized
	}
	rs := []rune(normalized)
	if len(rs) <= maxRunes {
		return normalized
	}
	if maxRunes <= 3 {
		return string(rs[:maxRunes])
	}
	return string(rs[:maxRunes-3]) + "..."
}
