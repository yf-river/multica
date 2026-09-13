// 文件职责：实现「本地路径链接 lint」——agent 交付物绝不能带指向本机文件系统的
// 链接（MUL-4899）。runtime 简报把这条写成了约定，但提示词只是建议；这里是三道
// 「把 agent 写的 markdown 发布给人类读者」命令上的强制执行兜底。
//
// 作用范围刻意在三个轴向上收窄，因为这里的误报会直接挡掉一件合法交付物：
//
//  1. 只在 agent 任务上下文生效。人类用 PAT 跑 CLI 不在 lint 范围：链接是他们
//     自己的事，而且他们可能真有读者能打开的共享路径。
//  2. 只查真正的 markdown 链接/图片目标。正文按 CommonMark 解析，只检查 Link /
//     Image / AutoLink 的目标——代码片段或围栏块里的路径（agent 正常「讨论」某个
//     路径的写法）结构上根本不可见。全文扫描做不出这种区分，会恰恰把解释这个 bug
//     的注释误杀。
//  3. 只收高置信目标。见 classifyLocalPathTarget。
package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// localPathLinkFinding 是一条解析到本机文件系统的 markdown 链接/图片目标。
// Reason 给出证据，让报错能告诉 agent 它的链接为什么被拒，而不只是「被拒了」。
type localPathLinkFinding struct {
	Target string
	Reason string
}

// findLocalPathLinks 把 body 按 CommonMark 解析，返回每个高置信为「本机路径」的
// link/image/autolink 目标。
//
// 只检查目标本身——绝不看链接文本或上下文文字。命中按目标去重：同一路径被链五
// 次只报一次。
func findLocalPathLinks(body string) []localPathLinkFinding {
	source := []byte(body)
	doc := goldmark.New().Parser().Parse(text.NewReader(source))

	var findings []localPathLinkFinding
	seen := make(map[string]struct{})
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var target string
		switch node := n.(type) {
		case *ast.Link:
			target = string(node.Destination)
		case *ast.Image:
			target = string(node.Destination)
		case *ast.AutoLink:
			// `<file:///Users/me/shot.png>` 渲染出来跟行内链接一样可点，是同一类
			// 缺陷，应当纳入检查范围。
			target = string(node.URL(source))
		default:
			return ast.WalkContinue, nil
		}

		reason := classifyLocalPathTarget(target)
		if reason == "" {
			return ast.WalkContinue, nil
		}
		if _, dup := seen[target]; dup {
			return ast.WalkContinue, nil
		}
		seen[target] = struct{}{}
		findings = append(findings, localPathLinkFinding{Target: target, Reason: reason})
		return ast.WalkContinue, nil
	})
	return findings
}

// classifyLocalPathTarget 返回「target 是本机路径」的证据；不是（或拿不准）返回 ""。
//
// 只有三类信号算数，且都是能证明目标指向「这台机器」的正向证据：
//
//  1. `file://` URL。任何读者都解析不到，结构上就是死链。
//  2. 当前工作目录——即任务工作目录（按任务隔离、私有）内的绝对路径。
//  3. 此刻在本机真实存在某个文件、且给出绝对路径的。
//
// 其余一律放行，而「漏掉什么」正是关键。裸 `/foo` 是合法的 origin-relative URI
// 引用（RFC 3986 §4.2），`/acme/issues/123` 正是这么写的；所以一个看着像绝对路径、
// 但既不在工作目录内、磁盘上也不存在的目标，与应用内合法链接无法区分，绝不能猜。
// 相对目标同理放行。runtime 简报里的提示词约定——而不是这个 lint——负责覆盖剩余
// 情况。
func classifyLocalPathTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}

	// 顺序很关键：Windows 的盘符绝对路径（"C:\Users\me\shot.png"）在 RFC 3986
	// 下会把 "c" 解析成 scheme，所以文件系统判断必须先于任何 scheme 嗅探，
	// 否则 Windows 路径会被误当 URL 打发掉。
	if filepath.IsAbs(target) {
		if within, err := fileWithinWorkingDir(target); err == nil && within {
			return "it is inside this task's working directory"
		}
		if info, err := os.Stat(target); err == nil && !info.IsDir() {
			return "it names a file that exists only on this machine"
		}
		return ""
	}

	if parsed, err := url.Parse(target); err == nil && strings.EqualFold(parsed.Scheme, "file") {
		return "it is a file:// URL"
	}
	return ""
}

// guardLocalPathLinks 在 agent 创作的正文把本机路径当链接发出去时让命令失败；
// 非 agent 任务上下文直接返回 nil 放行。
//
// deliveryHint 是调用方自己的修复指引。做成参数而不是常量，是因为每个命令的正解
// 不同，而其中一个还是坑：`multica issue update` 没有 --attachment flag，共用一句
// 「传 --attachment」会把 agent 指到不存在的参数上，一次失败变两次。
func guardLocalPathLinks(body, field, deliveryHint string) error {
	if !inAgentExecutionContext() {
		return nil
	}
	findings := findLocalPathLinks(body)
	if len(findings) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s links %d runtime-local path(s), which no reader can open:\n", field, len(findings))
	for _, f := range findings {
		fmt.Fprintf(&b, "  - %q — %s\n", f.Target, f.Reason)
	}
	b.WriteString("\nThe path exists only on the machine running you; for everyone else the link is dead. ")
	b.WriteString(deliveryHint)
	b.WriteString("\nTo merely reference a code location, use inline code instead of a link (`path/to/file.ts:42`) — code spans and fenced blocks are not checked.")
	return fmt.Errorf("%s", b.String())
}
