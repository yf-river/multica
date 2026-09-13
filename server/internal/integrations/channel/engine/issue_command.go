package engine

import "strings"

// 角色定位：/issue 命令的共享解析器（跨平台产品行为，所以住在共享引擎而不是某个适配器）
// 以及"从完整消息正文里抠出 issue 描述"的定位辅助。

// issueCommandPrefix 是命令的字面 token。我们精确匹配——`/Issue` 或 `/ISSUE` **不会**
// 触发建单。大小写敏感是产品层面的刻意选择：避免把句子中顺带提及 "/issue" 的消息误提升
// 成建单指令。
const issueCommandPrefix = "/issue"

// ParseIssueCommand 从一条聊天消息正文里抽出 /issue 命令。当消息够格、调用方应派发到
// IssueService.Create 时返回 (cmd, true)；否则返回 (nil, false)。识别的形态：
//
//   - `/issue <标题>`              → Title = "<标题>"，Description = ""
//   - `/issue <标题>\n<其余...>`   → Title = "<标题>"，Description = "<其余>"
//   - `/issue`（单独、无标题）      → Title = ""，Description = ""
//     （Router 会返回"用法"提示；它永远不会从历史里猜标题）
//
// 只有第一个非空行参与判定：正文以空行开头再跟 `/issue ...` 仍然够格；若第一个非空行
// 不是这个字面前缀，即使后面出现 `/issue` 也不算命令。`/issue` 必须是完整 token，
// 不能是更长词的前缀（"/issuetracker" 不匹配）。
func ParseIssueCommand(body string) (*IssueCommand, bool) {
	lines := strings.Split(body, "\n")

	// 定位第一个非空行。
	firstIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			firstIdx = i
			break
		}
	}
	if firstIdx == -1 {
		return nil, false
	}

	trimmed := strings.TrimLeft(lines[firstIdx], " \t")
	if !strings.HasPrefix(trimmed, issueCommandPrefix) {
		return nil, false
	}

	// 前缀后必须紧跟空白或行尾，保证 "/issuetracker" 不被当成 "/issue"。
	rest := trimmed[len(issueCommandPrefix):]
	if rest != "" {
		if r0 := rest[0]; r0 != ' ' && r0 != '\t' {
			return nil, false
		}
	}

	title := strings.TrimSpace(rest)
	description := ""
	if firstIdx+1 < len(lines) {
		description = strings.TrimRight(strings.Join(lines[firstIdx+1:], "\n"), " \t\n")
	}
	return &IssueCommand{Title: title, Description: description}, true
}

// issueDescriptionFromCommandBody 从完整规范化消息里去掉 /issue 指令行、保留其后的排版。
// 与 CommandText 不同，完整 body 里可能仍带着适配器生成的行内媒体占位符——把这些占位符
// 留在 issue 初始描述里，让解耦的媒体绑定器之后有稳定的位置去物化。
//
// 指令行**之前**的内容刻意排除：有些适配器会把引用上下文拼进 Body、而让 CommandText
// 保持用户自己的命令原文；把那段前缀复制进 issue 会改变既有命令契约。
func issueDescriptionFromCommandBody(body, commandText, fallback string) string {
	_, end, ok := issueCommandLineBounds(body, commandText)
	if !ok {
		return fallback
	}
	return strings.TrimSpace(body[end:])
}

type textLineBounds struct {
	start int
	end   int
}

// issueCommandLineBounds 在规范化正文里定位**用户亲手写的**那条 /issue 指令行。适配器
// 可能在前面拼接带更早 /issue 行的增强历史，所以"匹配第一个 token 边界的指令"不够。
// CommandText 是未经增强的命令源：先数它里面相同的指令行出现几次，再到正文的用户亲写
// 后缀里取对应次序的那一条。当描述行恰好重复了指令行文本时，这种算法仍然稳定。
func issueCommandLineBounds(body, commandText string) (start, end int, ok bool) {
	expected, ok := firstIssueCommandLine(commandText)
	if !ok {
		return 0, 0, false
	}
	commandOccurrences := countMatchingLines(commandText, expected)
	candidates := matchingLineBounds(body, expected)
	// 正文里的候选行 = 增强前缀中出现的 + 用户亲写的 commandOccurrences 条。
	// 从后往前数 commandOccurrences 条，取第一条用户亲写的。
	target := len(candidates) - commandOccurrences
	if target < 0 || target >= len(candidates) {
		return 0, 0, false
	}
	return candidates[target].start, candidates[target].end, true
}

// firstIssueCommandLine 返回 commandText 里第一条真正成形的 /issue 指令行（整行 trim 后
// 以 /issue 开头且后随空白或行尾）。遇到第一个非空行不是命令就失败——命令只允许在
// 用户消息的开头部分。
func firstIssueCommandLine(commandText string) (string, bool) {
	for _, line := range strings.Split(commandText, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, issueCommandPrefix) {
			rest := trimmed[len(issueCommandPrefix):]
			if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
				return trimmed, true
			}
		}
		if trimmed != "" {
			return "", false
		}
	}
	return "", false
}

// countMatchingLines 统计 text 里有多少行 trim 后恰好等于 expected（等宽判断，去掉
// 行首尾空白的影响）。
func countMatchingLines(text, expected string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == expected {
			count++
		}
	}
	return count
}

// matchingLineBounds 返回 body 里所有 trim 后等于 expected 的行的 [start,end) 边界
// （end 含换行符，便于从指令行后继续切）。
func matchingLineBounds(body, expected string) []textLineBounds {
	bounds := make([]textLineBounds, 0, 1)
	for offset := 0; offset <= len(body); {
		lineEnd := strings.IndexByte(body[offset:], '\n')
		next := len(body)
		line := body[offset:]
		if lineEnd >= 0 {
			lineEnd += offset
			line = body[offset:lineEnd]
			next = lineEnd + 1
		}

		if strings.TrimSpace(line) == expected {
			bounds = append(bounds, textLineBounds{start: offset, end: next})
		}
		if lineEnd < 0 {
			break
		}
		offset = next
	}
	return bounds
}
