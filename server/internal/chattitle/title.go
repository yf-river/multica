// Package chattitle 实现"根据消息正文推导聊天标题"的确定性规则，
// 供由频道创建的 Chat 使用。
package chattitle

import (
	"regexp"
	"strings"
	"unicode"
)

// deterministicTitleLimit 确定性标题的最大字符数（超出则截断并补省略号）。
const deterministicTitleLimit = 30

var (
	markdownFence = regexp.MustCompile("```.*?```")
	markdownMarks = regexp.MustCompile("[#*`>~_]")
	markdownLink  = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	titleSpace    = regexp.MustCompile(`[[:space:]]+`)
)

// Derive 复刻既有第一方"确定性标题"策略，用于频道创建的 Chat。
// 与旧实现不同的是按有效的 Unicode 码点（而非 UTF-16 码元）截断，同时保留
// 原有行为：取首个非空行、剥掉 Markdown、压缩空白、超 30 字符时用单个省略号截断。
func Derive(body string) string {
	line := ""
	// 取第一个非空行作为候选标题。
	for _, candidate := range strings.Split(body, "\n") {
		if strings.TrimSpace(candidate) != "" {
			line = candidate
			break
		}
	}
	// 依次清除代码块围栏、Markdown 标记，并把链接换成其显示文字，最后压缩连续空白。
	line = markdownFence.ReplaceAllString(line, " ")
	line = markdownMarks.ReplaceAllString(line, "")
	line = markdownLink.ReplaceAllString(line, "$1")
	line = strings.TrimSpace(titleSpace.ReplaceAllString(line, " "))
	if line == "" {
		return ""
	}
	runes := []rune(line)
	if len(runes) <= deterministicTitleLimit {
		return line
	}
	// 超过 30 字符时截到第 29 个码点，去尾随空白后补一个省略号。
	return strings.TrimRightFunc(string(runes[:deterministicTitleLimit-1]), unicode.IsSpace) + "…"
}
