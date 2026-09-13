package util

import "encoding/json"

// JSONObjectOrEmpty 把 API 契约承诺"对象永远存在"的 JSONB 列（如 issue metadata、
// issue 自定义属性）解码为 Go map。空、null 或无法解析的 blob 一律降级为空 map
// 而不是 nil：nil 会序列化成 `null`，而"永远存在"恰恰是客户端可以免 nil 判断直接
// 下标访问该字段的前提。
func JSONObjectOrEmpty(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}
