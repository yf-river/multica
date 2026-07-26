// Package prompteval 提供提示词评估相关的通用工具函数，
// 由 handler 与 service 层共享，避免跨层重复实现。
package prompteval

// TruncatePromptEvaluationEvidence 按 rune 截断证据文本并追加省略号。
func TruncatePromptEvaluationEvidence(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "..."
}
