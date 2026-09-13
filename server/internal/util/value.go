package util

import "strconv"

// StringFromAny 把常见标量类型尽量转成字符串；不认识的类型返回空串。
// 多用于把 UI/JSON 里"任意类型"的输入归一化后落库或做字符串比较。
func StringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case int:
		return strconv.Itoa(typed)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

// ValueOrEmpty 解引用可空字符串指针，nil 时返回空串。
func ValueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
