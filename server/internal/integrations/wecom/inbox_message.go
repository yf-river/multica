// inbox_message.go —— 企微智能机器人在 inbox:new 事件上推送的 markdown 正文。
// 用 markdown 是为了让它经 aibot_send_msg 渲染干净（aibot_send_msg 不收
// msgtype=text）。单独放一个文件，让出站 handler 专心做投递，本模块负责文案和
// 链接的构造。
package wecom

import (
	"net/url"
	"os"
	"strings"
	"unicode/utf8"
)

// aibot markdown 的大小上限。超过约 4096 字符企微会拒掉整帧，所以我们按这个
// 预算截断正文。用 4000 是给前缀 + 链接后缀留出富余。
const inboxMarkdownMaxLen = 4000

// inboxTypeLabels 是通知开头用的中文显示名。放在本包内，wecom 就不必伸到
// cmd/server 去取；两份名单靠约定保持一致。
var inboxTypeLabels = map[string]string{
	"issue_assigned":     "任务指派",
	"mentioned":          "提及你",
	"status_changed":     "状态变更",
	"comment_added":      "新评论",
	"new_comment":        "新评论",
	"reaction_added":     "表情反应",
	"task_failed":        "task 失败",
	"unassigned":         "取消指派",
	"assignee_changed":   "指派人变更",
	"priority_changed":   "优先级变更",
	"due_date_changed":   "截止日期变更",
	"start_date_changed": "开始日期变更",
}

func inboxTypeLabel(t string) string {
	if label, ok := inboxTypeLabels[t]; ok {
		return label
	}
	return "新消息"
}

// inboxAppURL 解析出前端地址，用于构造「查看详情」链接。优先级：
// WECOM_APP_URL → MULTICA_APP_URL → FRONTEND_ORIGIN。只收 HTTPS 值；非 HTTPS
// 的覆盖项被静默丢弃，防止配错的环境变量把 http:// URL 泄漏进用户会话。
func inboxAppURL() string {
	for _, name := range []string{"WECOM_APP_URL", "MULTICA_APP_URL", "FRONTEND_ORIGIN"} {
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" {
			continue
		}
		if !strings.HasPrefix(v, "https://") {
			continue
		}
		return strings.TrimRight(v, "/")
	}
	return ""
}

// buildInboxMarkdown 根据一张 inbox_item map 构造 aibot 友好的 markdown 正文。
// 格式：
//
//	**[{类型}] {标题}**
//	{正文}
//	[查看详情]({appURL}/{slug|workspaceID}/inbox?issue={issueID})
//
// 没有配置 appURL 时整段链接直接省略——发一张只有标题的卡片，也好过发一条坏链
// 接。
func buildInboxMarkdown(item map[string]any, workspaceID, slug string) string {
	title, _ := item["title"].(string)
	typeStr, _ := item["type"].(string)
	if title == "" && typeStr == "" {
		return ""
	}
	body := inboxItemBody(item)
	link := inboxItemLink(item, workspaceID, slug)

	// title 与 body 是成员写的。它们会落进一张接收方完全有理由信任的 aibot
	// markdown 卡片里——卡片来自机器人、不是作者——所以其中的链接语法绝不能
	// 渲染。名为 "[click here](http://evil.example)" 的任务曾以「可点击链接」的
	// 形态送达，正文里带 "[重置密码]: https://evil.example" 外加 "[重置密码]"
	// 的也照样渲染成了链接。
	//
	// 按「字段」逐个处理、而不是等整张卡片拼完再统一处理——下面做长度预算时需
	// 要的正是这样——而且覆盖并不因此打折。成员文本的每一行都从卡片自己那行的
	// 行首开始，唯独标题首行例外，而那里的机器人前缀 "**[" 让成员永远到不了定义
	// 列表（reference definition）必须占据的块位置。扫描本来就把每个字段的开头
	// 当作行首读，所以那一处是双保险、而不是没保险。
	//
	// 要赶在下面任何量长度之前做完：每打断一处 `](` 会插入一个空格，而 `](` 密
	// 集的正文会长出三分之一。按「未扩张」的文本来做预算，卡片发出去就会超上限，
	// 被企微整帧拒掉——同时还告诉发送者「已送达」。
	title = breakMemberLinks(title)
	body = breakMemberLinks(body)

	var b strings.Builder
	b.WriteString("**[")
	b.WriteString(inboxTypeLabel(typeStr))
	b.WriteString("] ")
	b.WriteString(title)
	b.WriteString("**")
	if body != "" {
		b.WriteString("\n")
		b.WriteString(body)
	}
	if link != "" {
		b.WriteString("\n[查看详情](")
		b.WriteString(link)
		b.WriteString(")")
	}
	result := b.String()
	if utf8.RuneCountInString(result) <= inboxMarkdownMaxLen {
		return result
	}
	// 只截正文。前缀与链接必须完整幸存，用户才仍保有「查看详情」这个出口。
	prefix := "**[" + inboxTypeLabel(typeStr) + "] " + title + "**"
	suffix := ""
	if link != "" {
		suffix = "\n[查看详情](" + link + ")"
	}
	// 下面两种截断都不可能让 "]" 重新贴到 "(" 或 ":" 旁边。truncateRunes 只会
	// 丢弃后缀，无法再造出 breakMemberLinks 已经拆开的那对组合；而这里在成员文
	// 本之后拼进来的每个字面量都以 "."、"*" 或 "\n" 开头——永远不会是 "(" 或
	// ":"。丢弃一段后缀也不可能补全一条引用定义：它只能把某条定义的目的地截断。
	// 由 TestInboxCardNeverPutsCloseBracketNextToOpenParen 与
	// TestInboxCardDefinesNoResolvableLinkReference 里的接缝用例钉死。
	room := inboxMarkdownMaxLen - utf8.RuneCountInString(prefix) - utf8.RuneCountInString(suffix) - 4 // "\n...\n"
	if room > 0 {
		return prefix + "\n" + truncateRunes(body, room) + "..." + suffix
	}
	// 一点正文的空间都没有，说明问题出在前缀自己身上——标题足够长时，前缀+后
	// 缀就自己顶破上限了；原样发出去意味着企微拒掉整帧、而发送者被告知已送达。
	// 所以截标题，保证发出去的东西总能装下。
	fixed := utf8.RuneCountInString(prefix) - utf8.RuneCountInString(title)
	titleRoom := inboxMarkdownMaxLen - fixed - utf8.RuneCountInString(suffix) - 3 // "..."
	if titleRoom < 0 {
		// 这里钳到 0 后返回的仍是 fixed+3+len(suffix) 个 rune，并没有过上限校验，
		// 所以这个分支理论上可能交回一张超限卡片。要走到它需要链接后缀超过约
		// 3987 个 rune，也就是那么长的 appURL 加 workspace slug；两者都受运维与
		// DB 约束，绝不是成员能写出来的。刻意保持原样：另一条路是丢链接腾地方，
		// 而现实里没有任何输入会走到这一步。
		titleRoom = 0
	}
	return "**[" + inboxTypeLabel(typeStr) + "] " +
		truncateRunes(title, titleRoom) + "...**" + suffix
}

// inboxItemBody 从 inbox_item map 里抽出 body/description 字符串。body 可能以
// *string（可空的 JSON 字段）、string、或缺席三种形态到达。
func inboxItemBody(item map[string]any) string {
	switch v := item["body"].(type) {
	case *string:
		if v != nil {
			return *v
		}
	case string:
		return v
	}
	return ""
}

// inboxItemLink 构造 {appURL}/{slug|wsUUID}/inbox?issue={issueID} 深链。
// 未配置 appURL 时返回 ""——调用方把它当作「整段链接都不用放」的信号。
func inboxItemLink(item map[string]any, workspaceID, slug string) string {
	appURL := inboxAppURL()
	if appURL == "" {
		return ""
	}
	seg := slug
	if seg == "" {
		seg = workspaceID
	}
	var b strings.Builder
	b.WriteString(appURL)
	b.WriteString("/")
	b.WriteString(url.PathEscape(seg))
	b.WriteString("/inbox")
	// 可选的 ?issue=... ——纯会话类 inbox 条目没有 issue。
	if issueID := inboxItemIssueID(item); issueID != "" {
		b.WriteString("?issue=")
		b.WriteString(url.QueryEscape(issueID))
	}
	return b.String()
}

// inboxItemIssueID 在存在时取出 issue_id。会话类通知没有 issue_id，我们返回
// ""——此时链接省略查询参数。
func inboxItemIssueID(item map[string]any) string {
	switch v := item["issue_id"].(type) {
	case *string:
		if v != nil {
			return *v
		}
	case string:
		return v
	}
	return ""
}

// truncateRunes 把 s 剪到最多 maxRunes 个 rune。按 rune 而非字节裁剪，保证截
// 断绝不会把一个汉字劈成两半。
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	i := 0
	for pos := range s {
		if i == maxRunes {
			return s[:pos]
		}
		i++
	}
	return s
}
