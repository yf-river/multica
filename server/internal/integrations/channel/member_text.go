package channel

import "strings"

// 角色定位：处理"不可信文本"里危险 Markdown 记号的公共工具。典型场景是把用户可控的
// 短文本（如 issue 标题）拼进带 Markdown 渲染的消息前先过一遍——对方若精心构造
// "[文字](恶意网址)" 这样的行内链接语法，直接拼接会被平台渲染成可点击链接；拆开
// "]( " 相邻记号就断掉了链接结构，只剩无害的普通文本。

// BreakMarkdownLinkAdjacency 把不可信文本里的标准行内 Markdown "](" 链接相邻记号拆开。
// 平台自带的原生标记（Slack mrkdwn、企微等各自的链接语法）各自还需要自己的防护。
// 不包含 "](" 的文本原样返回（逐字节不变），且结果幂等——处理过一次的文本再处理
// 不会二次变形。
func BreakMarkdownLinkAdjacency(s string) string {
	return strings.ReplaceAll(s, "](", "] (")
}
