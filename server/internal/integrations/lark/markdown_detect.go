// markdown_detect.go：判断 agent 的回复正文是否需要用 Markdown 形态渲染。
package lark

import (
	"regexp"
	"strings"
)

// markdownPatterns 枚举了我们视作“agent 回复是 markdown 而非普通白话”证据的
// 语法形态。每个 pattern 都刻意保守——宁可误报（把纯文本也走 markdown 卡片，
// 反正渲染出来也正常），也不漏报（让 `**bold**` 原样显示在用户对话里）。
//
// patterns 在包初始化时只编译一次；containsMarkdown 在聊天回复热路径上被调用。
var markdownPatterns = []*regexp.Regexp{
	regexp.MustCompile("(?m)^#{1,6}[ \t]"),                        // 标题：# H1 到 ###### H6
	regexp.MustCompile("(?m)^[ \t]*[-*+][ \t]"),                   // 无序列表
	regexp.MustCompile("(?m)^[ \t]*\\d+\\.[ \t]"),                 // 有序列表
	regexp.MustCompile("(?m)^>[ \t]"),                             // 引用块 blockquote
	regexp.MustCompile("(?m)^[ \t]*(?:---|\\*\\*\\*|___)[ \t]*$"), // 分隔线 / thematic break
	regexp.MustCompile("\\*\\*[^*\\n]+\\*\\*"),                    // **加粗**
	regexp.MustCompile("__[^_\\n]+__"),                            // __加粗__
	regexp.MustCompile("(?m)^[ \t]*\\|.+\\|[ \t]*$"),              // 表格行（必须首尾都有 |）
	regexp.MustCompile("\\[[^\\]\\n]+\\]\\([^)\\n]+\\)"),          // [text](url) 链接 / 图片
}

// containsMarkdown 在正文几乎可以确定含 markdown 语法时返回 true——这些语法
// 飞书的纯文本 msg_type=text 渲染器不会渲染（会露出裸星号、井号、竖线等）。
// 返回 true 时，聊天回复路由器会升级走 schema-2.0 交互卡片路径，让用户看到
// 格式化文本。
//
// 先检查快路径 token（反引号、星号、竖线、井号、任意行行首的 -），命中才跑
// 更慢的正则 pass；空字符串短路返回 false，避免把空的 agent 回复包进卡片。
func containsMarkdown(s string) bool {
	if s == "" {
		return false
	}
	// 围栏代码块——很强的 markdown 信号，且用廉价的子串检查即可命中。
	if strings.Contains(s, "```") {
		return true
	}
	// 行内代码：只把“看起来成对、且中间含非空格字符”的 `…` 串算上。裸反引号
	// （比如正文里引用一个按键名）不应该触发。
	if i := strings.Index(s, "`"); i >= 0 {
		if j := strings.Index(s[i+1:], "`"); j > 0 {
			return true
		}
	}
	for _, re := range markdownPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}
