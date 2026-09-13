// The formatMrkdwn function below is a Go port of the Markdown-to-mrkdwn
// converter (format_message) from Nous Research's Hermes Agent, used under the
// MIT License. Source:
// https://github.com/NousResearch/hermes-agent/blob/main/plugins/platforms/slack/adapter.py
//
// Copyright (c) 2025 Nous Research
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

// mrkdwn.go：标准 Markdown -> Slack mrkdwn 的格式转换器（formatMrkdwn）。
//
// 背景：Slack 认的是自家 mrkdwn 方言而不是标准 Markdown——加粗是【单个】星号
// （不是两个），斜体用下划线，链接是 <url|label>，标题和 ~~删除线~~ 都不支持。
// agent 输出的是标准 Markdown，不做转换的话，回复在 Slack 里会直接显示字面的
// `**`、`##`、`[text](url)`。本转换器是 Hermes Agent 的 slack `format_message`
// 的忠实 Go 移植（MIT，许可见本文件顶部）：被保护的区块（代码块、已转换链接、
// 既有 Slack 实体）先藏到 NUL 分隔的占位符后面，免得后续替换把它们弄坏，最后
// 按插入顺序倒序还原，让嵌套的占位符都能正确解出。
package slack

import (
	"regexp"
	"strconv"
	"strings"
)

// 以下 var 块是转换用到的一整套正则。整体策略见文件头；几个最容易看走眼的
// 逐个标注了作用。
var (
	reFenced      = regexp.MustCompile("(?s)(```(?:[^\\n]*\\n)?.*?```)")                    // ``` 围栏代码块（含首行标注语言）
	reInlineCode  = regexp.MustCompile("(`[^`]+`)")                                         // 行内代码 `xxx`
	reMdLink      = regexp.MustCompile(`(!?)\[([^\]]+)\]\(([^()]*(?:\([^()]*\)[^()]*)*)\)`) // Markdown 链接，支持 URL 里嵌套一层括号
	reSlackEntity = regexp.MustCompile(`(<(?:[@#!]|(?:https?|mailto|tel):)[^>\n]+>)`)       // 已是 Slack 实体/<@U123|名> 这类
	reBlockquote  = regexp.MustCompile(`(?m)^(>+\s)`)                                       // 行首引用标记 >
	reHeader      = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)                               // 标题 ## xxx
	reInnerBold   = regexp.MustCompile(`\*\*(.+?)\*\*`)                                     // 标题内部残留的 **加粗**（标题转 * 前先剥掉）
	reBoldItalic  = regexp.MustCompile(`\*\*\*(.+?)\*\*\*`)                                 // ***加粗斜体***
	reBold        = regexp.MustCompile(`\*\*(.+?)\*\*`)                                     // **加粗**
	reItalic      = regexp.MustCompile(`\*(\S(?:[^*\n]*?\S)?)\*`)                           // *斜体*（避开两头的星号，只命中单词内成对单星）
	reStrike      = regexp.MustCompile(`~~(.+?)~~`)                                         // ~~删除线~~
)

// formatMrkdwn 把标准 Markdown 转成 Slack 的 mrkdwn。步骤编号沿用上游移植的
// 顺序：先藏后换、最后倒序还原，保证代码块/链接/实体不被误伤。
func formatMrkdwn(content string) string {
	if content == "" {
		return content
	}
	p := &mrkdwnPlaceholders{values: map[string]string{}}
	text := content

	// 第 1、2 步：先藏起 ``` 围栏代码块，再藏起行内代码——它们内部的所有标记
	// 都必须按字面保留，绝不能参与后面的转换。
	text = reFenced.ReplaceAllStringFunc(text, p.stash)
	text = reInlineCode.ReplaceAllStringFunc(text, p.stash)

	// 第 3 步：Markdown 链接 [text](url) -> <url|text>。图片链接（![..]）保持
	// 原样——Slack 不会从 markdown 里渲染行内图片。
	text = reMdLink.ReplaceAllStringFunc(text, func(m string) string {
		sub := reMdLink.FindStringSubmatch(m)
		if sub[1] == "!" {
			return m
		}
		url := strings.TrimSpace(sub[3])
		if strings.HasPrefix(url, "<") && strings.HasSuffix(url, ">") {
			url = strings.TrimSpace(url[1 : len(url)-1])
		}
		return p.stash("<" + url + "|" + sub[2] + ">")
	})

	// 第 4、5 步：藏起既有 Slack 实体/手写链接，再藏起行首引用 >。
	text = reSlackEntity.ReplaceAllStringFunc(text, p.stash)
	text = reBlockquote.ReplaceAllStringFunc(text, p.stash)

	// 第 6 步：转义 Slack 控制字符。先反转义一次，保证原本就带 &amp; 这类实体
	// 的输入不会被二次转义。
	text = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(text)
	text = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)

	// 第 7 步：标题（## Title）-> *Title*（标题里的冗余 **加粗** 先剥掉）。
	text = reHeader.ReplaceAllStringFunc(text, func(m string) string {
		inner := strings.TrimSpace(reHeader.FindStringSubmatch(m)[1])
		inner = reInnerBold.ReplaceAllString(inner, "$1")
		return p.stash("*" + inner + "*")
	})

	// 第 8~11 步：***加粗斜体*** -> *_text_*，**加粗** -> *加粗*，*斜体* ->
	// _斜体_，~~删除线~~ -> ~删除线~。顺序必须先粗斜体后粗体再斜体，否则粗体
	// 的 *** 会被先匹配掉一部分。
	text = reBoldItalic.ReplaceAllStringFunc(text, func(m string) string {
		return p.stash("*_" + reBoldItalic.FindStringSubmatch(m)[1] + "_*")
	})
	text = reBold.ReplaceAllStringFunc(text, func(m string) string {
		return p.stash("*" + reBold.FindStringSubmatch(m)[1] + "*")
	})
	text = reItalic.ReplaceAllStringFunc(text, func(m string) string {
		return p.stash("_" + reItalic.FindStringSubmatch(m)[1] + "_")
	})
	text = reStrike.ReplaceAllStringFunc(text, func(m string) string {
		return p.stash("~" + reStrike.FindStringSubmatch(m)[1] + "~")
	})

	// 第 13 步：按插入顺序【倒序】还原占位符——后藏的先还原，嵌套的占位符才能
	// 层层解开而不是把另一半替换坏。
	for i := len(p.order) - 1; i >= 0; i-- {
		k := p.order[i]
		text = strings.ReplaceAll(text, k, p.values[k])
	}
	return text
}

// mrkdwnPlaceholders 管理转换期的「暂存区」：values 记下每个占位符藏的原内容，
// order 按藏入顺序排好，n 是自增计数器。占位符形如 \x00SL<n>\x00，用 NUL 包裹
// 是为了让占位符在正文里几乎不可能与真实内容撞车。
type mrkdwnPlaceholders struct {
	values map[string]string
	order  []string
	n      int
}

// stash 把一段「之后要按字面放回」的文本藏进暂存区，返回它在正文里的占位符。
func (p *mrkdwnPlaceholders) stash(v string) string {
	key := "\x00SL" + strconv.Itoa(p.n) + "\x00"
	p.n++
	p.values[key] = v
	p.order = append(p.order, key)
	return key
}
