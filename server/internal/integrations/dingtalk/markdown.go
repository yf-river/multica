// markdown.go：把一段较长的回复正文切成多条钉钉 sampleMarkdown 消息的分片器，
// 以及从正文第一条标题推导聊天列表预览（title）的小工具。
//
// 背景：钉钉的机器人发消息接口（oToMessages.batchSend / groupMessages.send）
// 对 sampleMarkdown 正文硬性拒绝约 20000 字节以上的负载，而且超限会整条消息
// 丢弃、连个错误都不给。所以 agent 的长回复必须先在这里切成小块再逐条发送，
// 每条都控制在硬限制之下。
//
// 分片注意两件事：1) 限额按 UTF-8「字节」而非「字符」计，因为钉钉量的是编码后
// 的载荷；2) 如果分片边界正好落在代码围栏（```）中间，要在前片末尾补上收尾围栏、
// 在后片开头重新打开，保证每一片都是自洽的 markdown，而不是一半被渲染成坏块。
package dingtalk

import (
	"strings"
	"unicode/utf8"
)

const (
	// markdownByteBudget 限制单片正文的字节数。选 16000 远低于钉钉约 20000 字节的
	// 硬上限，给标题、围栏收尾等其它开销留出余量。
	markdownByteBudget = 16000
	// 落在代码块中间切分时，会在片尾补上 "\n```" 让该片自含。这段合成开销不计入
	// 内容预算，保证真正上线的整条负载（而不是预渲染时的切片）也在硬限制之内。
	markdownSyntheticFenceCloseBytes = len("\n```")
	markdownContentByteBudget        = markdownByteBudget - markdownSyntheticFenceCloseBytes
	// 续片要重复写一遍开头那行围栏。给这段合成前缀设上限，防止恶意超长 info 串
	// 在切下一行代码时吃光整片预算（甚至算成负数）。
	maxMarkdownFenceInfoBytes = 256
	// defaultMarkdownTitle 是正文没有前导标题时，聊天列表推送预览用的兜底文案。
	// 钉钉只在推送预览里显示 title，正文里不出现。
	defaultMarkdownTitle = "Multica has replied."
)

// markdownTitle 从正文第一个 ATX 标题（# 开头行）推导 sampleMarkdown 的
// title（通知预览），没有就落到默认文案。标题行本身仍保留在正文里，只去掉行首的
// 井号用来做预览文字。
func markdownTitle(body string) string {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			if heading != "" {
				return heading
			}
		}
		if trimmed != "" {
			break
		}
	}
	return defaultMarkdownTitle
}

// chunkMarkdown 把 body 切成每片不超过 markdownByteBudget 字节的多条消息，优先
// 在行边界切。若某片末尾代码围栏仍开着，就补收尾围栏、在下片开头重新打开，保证
// 单片自含。比预算还长的单行，按 UTF-8 字符边界硬切（绝不从多字节字符中间下刀）。
func chunkMarkdown(body string) []string {
	if len(body) <= markdownByteBudget {
		return []string{body}
	}

	var (
		chunks    []string
		cur       strings.Builder
		fenceOpen bool
		// fenceInfo 是当前打开代码块的围栏行原文（如 "```go"），续片用它重开围栏
		// 以保留同样的语言标注——钉钉按语言 tag 做高亮，续片若用裸 "```" 重开会
		// 失去高亮。
		fenceInfo string
	)
	flush := func(reopen bool) {
		if cur.Len() == 0 {
			return
		}
		text := cur.String()
		if fenceOpen {
			text += "\n```"
		}
		// 丢弃只含围栏/空行的片——比如开围栏正好撞上超长行、或重开围栏后没有内容
		// 的空片——避免渲染出一个空代码块。
		if !isBlankChunk(text) {
			chunks = append(chunks, text)
		}
		cur.Reset()
		if reopen && fenceOpen {
			cur.WriteString(fenceInfo + "\n")
		}
	}

	for _, line := range splitKeepNewline(body) {
		// 单行超预算装不进任何一片，直接硬切。
		if len(line) > markdownContentByteBudget {
			flush(true)
			pieceBudget := markdownContentByteBudget
			if fenceOpen {
				pieceBudget = markdownByteBudget - len(fenceInfo) - len("\n") - len("\n```")
			}
			for _, piece := range hardSplit(line, pieceBudget) {
				// 从代码块内超长行硬切出来的片，必须自带完整围栏，否则会按纯文本渲染。
				if fenceOpen {
					piece = fenceInfo + "\n" + piece + "\n```"
				}
				chunks = append(chunks, piece)
			}
			continue
		}
		if cur.Len()+len(line) > markdownContentByteBudget {
			flush(true)
		}
		if isFenceLine(line) {
			if fenceOpen {
				fenceOpen = false
				fenceInfo = ""
			} else {
				fenceOpen = true
				fenceInfo = continuationFence(line)
			}
		}
		cur.WriteString(line)
	}
	flush(false)
	return chunks
}

func continuationFence(line string) string {
	fence := strings.TrimRight(line, "\r\n")
	if len(fence) > maxMarkdownFenceInfoBytes {
		return "```"
	}
	return fence
}

// splitKeepNewline 把 s 按行切开，但保留每行结尾的 "\n"，这样用 strings.Join
// 重拼回去时字节完全一致。s 以 "\n" 结尾时 SplitAfter 会多产一个空串尾巴，
// 这里把它去掉，避免「恰好以换行结尾」的正文被凭空多补一行空行。
func splitKeepNewline(s string) []string {
	lines := strings.SplitAfter(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// isFenceLine 判断一行是不是围栏代码块的开关（去掉行首空格后以 ``` 开头）。
func isFenceLine(line string) bool {
	return strings.HasPrefix(strings.TrimLeft(line, " \t"), "```")
}

// isBlankChunk 判断 text 是否没有任何可渲染内容——每一行都是空行或围栏标记。
// 这样的片发出去只会渲染成一个空代码块，所以分片器直接丢弃而不发送。
func isBlankChunk(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "```") {
			return false
		}
	}
	return true
}

// hardSplit 把 s 按字节预算切成多段，切点绝不落在 UTF-8 多字节字符中间。
func hardSplit(s string, budget int) []string {
	// 生产调用方给的预算都远大于此。这里仍保留下限，是为了让函数「总不会出错」：
	// 将来若有人把合成前缀算成负预算，也不至于死循环、负切片或切开多字节字符。
	if budget < utf8.UTFMax {
		budget = utf8.UTFMax
	}
	var pieces []string
	for len(s) > budget {
		cut := budget
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			cut = budget
		}
		pieces = append(pieces, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		pieces = append(pieces, s)
	}
	return pieces
}
