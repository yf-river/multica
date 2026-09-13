// content_flatten.go：把飞书消息 body.content 里的富文本内容压平成纯文本。
package lark

import (
	"encoding/json"
	"strings"
)

// flattenContent 把飞书消息 body.content——即飞书做了“二次 JSON 编码”的原始
// 字符串——按 msg_type 分派转换成纯文本。它是两条入站路径共用的结构处理步骤：
//
//   - 入站 decoder：处理用户自己的 text / post 消息；
//   - enricher：处理它经 IM REST API 拉回的“引用回复的父消息”和
//     merge_forward 的子消息。
//
// @ 占位符（@_user_N）原样保留；由调用方负责结合消息的 mentions[] 数组通过
// resolveMentions 解析。两条入站形态（WS receive 事件 vs IM REST item）携带
// mentions 数组的方式不同——只有调用方知道是哪种——所以扁平化本身不感知
// mention。
//
// 非文本媒体类型渲染成稳定的方括号占位符，让 agent 知道“挂了附件”而不必在
// 这条快路径上下载二进制；独立的媒体解析器会另外拉取资源并绑定为聊天附件，
// 占位符作为持久兜底。merge_forward 在到达这里之前就被 enricher 拦截了
// （展开它需要一次 HTTP 往返）；这里的行内占位符只是“forward 嵌套 forward”
// 场景的兜底。
func flattenContent(msgType, rawContent string) string {
	switch msgType {
	case "text":
		return extractTextBody(rawContent)
	case "post":
		return flattenPostContent(rawContent)
	case "image":
		return "[Image]"
	case "file":
		return "[File]"
	case "audio":
		return "[Audio]"
	case "media", "video":
		return "[Video]"
	case "sticker":
		return "[Sticker]"
	case "interactive":
		return "[interactive card]"
	case "share_chat":
		return "[Shared Chat]"
	case "share_user":
		return "[Shared User Card]"
	case "system":
		return "[System Message]"
	case "merge_forward":
		return "[forwarded messages]"
	default:
		return ""
	}
}

// larkPostContent 镜像“接收侧”post 富文本 body.content 的形态。关键点：它
// 不是“发送 API”那种带 locale 包装的形态（{"zh_cn": {...}}）。入站的 post
// body.content 会直接反序列化成 {title, content}。content 是二维数组——
// 外层数组是按顺序排列的段落，内层数组是段落的多个 span；段落之间的换行由
// 数组边界隐含，而不是某个 span 自带。
type larkPostContent struct {
	Title   string           `json:"title"`
	Content [][]larkPostSpan `json:"content"`
}

// larkPostSpan 是 post 段落里的一个节点。只建模了携带可渲染文本的字段；
// tag 集合是可扩展的，所以扁平化遇到不认识的 tag 时当作 text 输出，而不是
// 直接报错失败。
type larkPostSpan struct {
	Tag      string `json:"tag"`
	Text     string `json:"text"`
	Href     string `json:"href"`
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	ImageKey string `json:"image_key"`
	FileKey  string `json:"file_key"`
	FileName string `json:"file_name"`
	Name     string `json:"name"`
	MimeType string `json:"mime_type"`
}

// flattenPostContent 把收到的 post body.content 压平成纯文本：标题（若有）
// 独占首行，之后每个段落一行。段落内的 span 用单个空格连接——这与飞书自身
// 的渲染一致：逻辑上独立的片段（比如"Lark 集成"后面跟链接 "PR #3277"）
// 读起来是空格分隔的词。
//
// 链接 span 渲染成 "text (href)"，这样 URL 能进到 agent 上下文；@ span
// 渲染成 @_user_N 占位符，让后续的 resolveMentions 能替换成显示名（占位符
// 缺失时退回到行内 user_name）。媒体 span 退化成与 flattenContent 相同的
// 方括号占位符。
func flattenPostContent(raw string) string {
	if raw == "" {
		return ""
	}
	var doc larkPostContent
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return ""
	}

	var b strings.Builder
	write := func(line string) {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	if doc.Title != "" {
		write(doc.Title)
	}
	for _, para := range doc.Content {
		write(flattenPostParagraph(para))
	}
	return strings.TrimRight(b.String(), "\n")
}

// flattenPostParagraph 把某个段落里的一组 span 拼成一个字符串（span 间以
// 单空格连接）。不同 tag 决定渲染方式：纯文本/代码块原样输出；链接带上
// href；@ 优先用占位符、其次显示名；图片/视频退化成占位符；表情是枚举键
// 跳过；分隔线渲染成 "---"。
func flattenPostParagraph(spans []larkPostSpan) string {
	parts := make([]string, 0, len(spans))
	for _, s := range spans {
		switch s.Tag {
		case "text", "code_block":
			if s.Text != "" {
				parts = append(parts, s.Text)
			}
		case "a":
			switch {
			case s.Text != "" && s.Href != "":
				parts = append(parts, s.Text+" ("+s.Href+")")
			case s.Text != "":
				parts = append(parts, s.Text)
			case s.Href != "":
				parts = append(parts, s.Href)
			}
		case "at":
			// 优先用 @_user_N 占位符，让后续 resolveMentions 能映射成显示名并
			// 去掉对 Bot 自己的 @；占位符缺失时退回行内 user_name。
			switch {
			case s.UserID != "":
				parts = append(parts, s.UserID)
			case s.UserName != "":
				parts = append(parts, "@"+s.UserName)
			}
		case "img":
			parts = append(parts, "[Image]")
		case "media":
			parts = append(parts, "[Video]")
		case "emotion":
			// emoji_type 是枚举键（如 "SMILE"）而不是展示文本——跳过，避免
			// 把键名泄漏给用户。
		case "hr":
			parts = append(parts, "---")
		default:
			if s.Text != "" {
				parts = append(parts, s.Text)
			}
		}
	}
	return strings.Join(parts, " ")
}
