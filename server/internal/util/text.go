package util

import (
	"strings"
	"unicode/utf8"
)

// UnescapeBackslashEscapes 解码常见的反斜杠转义序列（\n、\r、\t、\\）——LLM agent
// 因为默认沿用 Python/JSON 风格字符串约定，经常把换行写成 4 个字符的字面量。同一工具
// 既被 CLI 用来修正 bash 双引号正文（shell 不展开 \n），也被 daemon 任务完成路径用来
// 修正段间带字面 `\n\n` 的原始 agent 输出。
//
// 只解码 \n / \r / \t / \\。其它转义（\d、\w、\s、\u、\0、\" 等）原样保留，保证正则
// 字面量与 printf 格式串不被意外改写。调用方若需要保留完整的 4 字面量序列，应绕开本
// 工具（CLI 为此暴露了 --content-stdin / --description-stdin）。
func UnescapeBackslashEscapes(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 'r':
				b.WriteByte('\r')
				i++
				continue
			case 't':
				b.WriteByte('\t')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

// sanitizeJSONMaxDepth bounds SanitizeJSONForPostgres's recursion. Task tool
// sanitizeJSONMaxDepth 限制 SanitizeJSONForPostgres 的递归深度。任务工具输入实际
// 只有几层深；再深的就是病态或敌意数据，直接丢弃而非继续下钻，免得一个深层嵌套
// 载荷把持久化防护变成栈溢出。
const sanitizeJSONMaxDepth = 32

// SanitizeTextForPostgres 把任意外部产生的字符串清洗到可安全写入 PostgreSQL TEXT
// 或 JSONB 列的程度。
//
// 有两种会令整个 INSERT 失败并把周边事务回滚的失效模式：
//
//   - 内嵌 NUL（0x00）。TEXT 列里 PostgreSQL 以 SQLSTATE 22021（"invalid byte
//     sequence for encoding UTF8"）拒绝；经 encoding/json 转义后 JSONB 列则以 22P05
//     （"unsupported Unicode escape sequence"）拒绝。这里直接删掉：它没有任何有意义的
//     呈现，调用方更需要的是周边诊断信息，而不是一个看不见的字节。
//   - 其它任何非法 UTF-8 字节序列——截断多字节 rune 的字节尾、Windows-1252 智能引号、
//     崩溃子进程吐出的原始二进制。统一替换为 U+FFFD，让文本保持形状，读者也能看出
//     这里原本有什么。
//
// 重要提醒：单靠 strings.ToValidUTF8 覆盖不了 NUL 情况——0x00 本身就是 U+0000 的合法
// UTF-8 编码，会原样穿过。这里的显式 ReplaceAll 是承重墙，别"简化"掉；GH #7098 让
// 任务卡在 'running' 的缺口正是它。
func SanitizeTextForPostgres(s string) string {
	// 绝大多数是普通文本，直接放行，完全不碰。
	if !strings.ContainsRune(s, 0) && utf8.ValidString(s) {
		return s
	}
	return strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
}

// SanitizeJSONForPostgres 遍历解码后的 JSON 值——encoding/json 产出的 `any` 树——
// 对其中的每个字符串（键也包含，被投毒的键和值一样会让 INSERT 失败）应用
// SanitizeTextForPostgres。
//
// 之所以需要它：当某个顶层字符串字段本身就是一个 JSONB blob（例如 agent 工具输入）
// 时，只清洗请求的顶层字符串字段是不够的，有毒字节可能藏在任意深度。
//
// 深度超过 sanitizeJSONMaxDepth 的子树用 nil 顶替而不继续遍历。这是刻意的、有界
// 的损失：否则敌意载荷可以决定我们的递归深度。
func SanitizeJSONForPostgres(v any) any {
	return sanitizeJSONValue(v, 0)
}

// sanitizeJSONValue 递归清洗 JSON 值；每深入一层 depth 加一，超过上限直接返回 nil。
func sanitizeJSONValue(v any, depth int) any {
	if depth > sanitizeJSONMaxDepth {
		return nil
	}
	switch t := v.(type) {
	case string:
		return SanitizeTextForPostgres(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[SanitizeTextForPostgres(k)] = sanitizeJSONValue(val, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = sanitizeJSONValue(val, depth+1)
		}
		return out
	default:
		// 数字、布尔、nil —— TEXT/JSONB 列不会因为它们报错。
		return v
	}
}
