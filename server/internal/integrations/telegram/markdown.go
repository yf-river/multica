package telegram

import (
	"html"
	"regexp"
	"strings"
)

// 角色定位：把 agent 输出的标准 Markdown 转换成 Telegram HTML 解析模式，供 sendMessage /
// editMessageText 使用。
//
// 为什么选 HTML 而不是 MarkdownV2（关键设计决策）：MarkdownV2 要求**所有文本**里的 18 个
// 标点字符全部转义，漏转义一个整条 sendMessage 就失败；而 HTML 只需要转义 & < > 三个字符，
// 唯一的失败模式是出现未知标签。两者风险差距巨大。
// 转换是逐行进行的，并且刻意保守：识别不了的 Markdown 一律当作普通文本（只做 HTML 转义）
// 放行，绝不冒险生成会被 Telegram 拒收的消息——宁可显示原样符号，不能丢整条消息。
var (
	reBold       = regexp.MustCompile(`\*\*(.+?)\*\*`)             // **加粗**（非贪婪跨行内容）
	reItalic     = regexp.MustCompile(`(^|[^*])\*([^*]+?)\*`)      // *斜体*，避开与 ** 重叠的星号
	reStrike     = regexp.MustCompile(`~~(.+?)~~`)                 // ~~删除线~~
	reInlineCode = regexp.MustCompile("`([^`]+)`")                 // `行内代码`
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`) // [文字](URL)
	reHeading    = regexp.MustCompile(`^#{1,6}\s+(.*)$`)           // 行首 1~6 个 # 的标题
	reBullet     = regexp.MustCompile(`^(\s*)[-*]\s+`)             // 行首 "-" 或 "*" 的无序列表项
)

// formatHTML 把 Markdown 文本渲染成 Telegram HTML。代码块最先处理（块内内容必须原样保留，
// 只能做实体转义），其余部分逐行转换。
func formatHTML(md string) string {
	var out strings.Builder
	lines := strings.Split(md, "\n")
	inCode := false
	var codeLang string
	var codeBuf []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inCode {
				// 遇到收尾围栏：把攒下的代码块整体渲染并复位状态。
				out.WriteString(renderCodeBlock(codeLang, codeBuf))
				out.WriteString("\n")
				inCode, codeBuf, codeLang = false, nil, ""
			} else {
				// 遇到起始围栏：进入代码块模式，围栏后的残留当语言标识（可为空）。
				inCode = true
				codeLang = strings.TrimPrefix(trimmed, "```")
			}
			continue
		}
		if inCode {
			codeBuf = append(codeBuf, line)
			continue
		}
		out.WriteString(formatLine(line))
		out.WriteString("\n")
	}
	if inCode {
		// 围栏没有收尾（agent 流式输出到一半的快照）：把手头已收到的行渲染出来，
		// 让限速 edit 刷新出来的消息仍然展示出半截代码块，而不是整块消失。
		out.WriteString(renderCodeBlock(codeLang, codeBuf))
		out.WriteString("\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

// renderCodeBlock 把一个代码块渲染成 <pre> 标签；带语言时套上带 language- 类名的
// <code>（Telegram 客户端据此做高亮）。内容一律先做 HTML 转义再嵌入。
func renderCodeBlock(lang string, lines []string) string {
	body := html.EscapeString(strings.Join(lines, "\n"))
	if lang != "" {
		return `<pre><code class="language-` + html.EscapeString(lang) + `">` + body + `</code></pre>`
	}
	return "<pre>" + body + "</pre>"
}

// formatLine 转换一行非代码内容：先整行转义，再按模式把受信任的标签"重新引入"。
// 标题和列表项各自有专属的输出形态；其余一律走 formatInline。
func formatLine(line string) string {
	// Markdown 标题在 Telegram HTML 里没有对应语义，用加粗整行来近似表达层级
	// （这是有意的降级，不是缺陷）。
	if m := reHeading.FindStringSubmatch(line); m != nil {
		return "<b>" + formatInline(m[1]) + "</b>"
	}
	// 列表项：保留原缩进，把 "-"/"*" 替换成圆点符号。
	if m := reBullet.FindStringSubmatchIndex(line); m != nil {
		return line[m[2]:m[3]] + "• " + formatInline(line[m[1]:])
	}
	return formatInline(line)
}

// formatInline 转换一行内联内容。核心手法是"占位符保护"：行内代码和链接是两次正则转换
// 里最容易被误伤的（URL 里的字符、代码里的星号/下划线都可能触发强调规则），所以先各自
// 抽出来换成 \x00 开头的人造占位符，等转义与加粗/斜体/删除线全部做完，再按序换回去。
func formatInline(s string) string {
	// 第 1 步：先抽出行内代码——它必须在任何转义/样式之前离开文本，内容保持原样。
	type span struct{ content string }
	var spans []span
	s = reInlineCode.ReplaceAllStringFunc(s, func(m string) string {
		content := strings.Trim(m, "`")
		spans = append(spans, span{content})
		return "\x00CODE\x00"
	})

	// 第 2 步：抽出链接——同样要在转义前抽走，保证 URL 原样存活不被实体转义改写。
	type link struct{ label, url string }
	var links []link
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		g := reLink.FindStringSubmatch(m)
		links = append(links, link{g[1], g[2]})
		return "\x00LINK\x00"
	})

	// 第 3 步：整体 HTML 转义后，剩下的只是占位符与安全的 Markdown 记号，可以放心套标签。
	s = html.EscapeString(s)
	s = reBold.ReplaceAllString(s, "<b>$1</b>")
	s = reItalic.ReplaceAllString(s, "$1<i>$2</i>")
	s = reStrike.ReplaceAllString(s, "<s>$1</s>")

	// 第 4 步：按原顺序把占位符换回真实标签。标签内部仍各做一次转义，双保险。
	for _, l := range links {
		tag := `<a href="` + html.EscapeString(l.url) + `">` + html.EscapeString(l.label) + `</a>`
		s = strings.Replace(s, "\x00LINK\x00", tag, 1)
	}
	for _, c := range spans {
		s = strings.Replace(s, "\x00CODE\x00", "<code>"+html.EscapeString(c.content)+"</code>", 1)
	}
	return s
}
