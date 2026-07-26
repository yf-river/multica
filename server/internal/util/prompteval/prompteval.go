// Package prompteval 提供提示词评估相关的通用工具函数，
// 由 handler 与 service 层共享，避免跨层重复实现。
package prompteval

import (
	"encoding/json"
	"unicode"
)

// TruncatePromptEvaluationEvidence 按 rune 截断证据文本并追加省略号。
func TruncatePromptEvaluationEvidence(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "..."
}

// MustJSONBytes 序列化为 JSON，失败时返回空对象字面量。
func MustJSONBytes(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

// DecodePayloadObject 将 JSON 字节解码为对象，空输入或解码失败返回空对象。
func DecodePayloadObject(raw []byte) map[string]any {
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	if payload == nil {
		return map[string]any{}
	}
	return payload
}

// ContainsHanRune 判断字符串是否包含汉字。
func ContainsHanRune(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
