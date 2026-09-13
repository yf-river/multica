package daemon

import (
	"regexp"
	"strings"
)

// slashSkillRe 匹配 markdown 里的斜杠技能链接：[/标签](slash://skill/ID)，
// 标签与 ID 都允许反斜杠转义，ID 内不允许 ]。
var slashSkillRe = regexp.MustCompile(
	`\[/((?:[^\]\\]|\\.)+)\]\(slash://skill/([^)]+)\)`,
)

// SlashSkillRef 是从 markdown 中提取出的一次斜杠技能引用（展示标签 + 技能 ID）。
type SlashSkillRef struct {
	Label string
	ID    string
}

// ExtractSlashSkills 从 md 文本里找出全部 slash://skill/ 链接，按 ID 去重后返回
// 引用列表（保留首次出现顺序）；标签会做反斜杠转义还原。
func ExtractSlashSkills(md string) []SlashSkillRef {
	matches := slashSkillRe.FindAllStringSubmatch(md, -1)
	seen := make(map[string]struct{}, len(matches))
	refs := make([]SlashSkillRef, 0, len(matches))

	for _, m := range matches {
		id := m[2]
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}

		label := strings.ReplaceAll(m[1], `\[`, "[")
		label = strings.ReplaceAll(label, `\]`, "]")
		refs = append(refs, SlashSkillRef{Label: label, ID: id})
	}

	return refs
}
