package util

import "regexp"

// Mention 表示从 markdown 内容解析出的一条 @提及。
type Mention struct {
	Type string // "member"（成员）、"agent"（代理）、"issue"（issue）或 "all"（全体）
	ID   string // user_id、agent_id、issue_id 或 "all"
}

// MentionRe 匹配 markdown 里的 [@Label](mention://type/id) 或 [Label](mention://issue/id)。
// @ 前缀可选，以支持 issue 引用形式 [MUL-123](mention://issue/...)。
// 用 .+?（非贪婪）而非 [^\]]*，这样含方括号的标签（如 "David[TF]"）也能正确匹配——
// ](mention:// 锚点本身足够特异，不会过度匹配。
var MentionRe = regexp.MustCompile(`\[@?(.+?)\]\(mention://(member|agent|squad|issue|all)/([0-9a-fA-F-]+|all)\)`)

// IsMentionAll 判断该提及是否为 @all 全体提及。
func (m Mention) IsMentionAll() bool {
	return m.Type == "all"
}

// ParseMentions 从 markdown 内容提取去重后的提及列表。
func ParseMentions(content string) []Mention {
	matches := MentionRe.FindAllStringSubmatch(content, -1)
	seen := make(map[string]bool)
	var result []Mention
	for _, m := range matches {
		key := m[2] + ":" + m[3]
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, Mention{Type: m[2], ID: m[3]})
	}
	return result
}

// HasMentionAll 报告切片中是否含 @all 全体提及。
func HasMentionAll(mentions []Mention) bool {
	for _, m := range mentions {
		if m.IsMentionAll() {
			return true
		}
	}
	return false
}
