// builtin_skills.go —— 平台内置技能：随编译期 embed 打包，每个 agent 在 workspace 技能之外都会收到，
// 用于教授平台级“怎么做”工作流（如 @提及），这些是 runtime brief 刻意留给技能的部分。
package service

import (
	"embed"
	"io/fs"
	"path"
	"strings"
)

//go:embed builtin_skills
var builtinSkillsFS embed.FS

const builtinSkillsRoot = "builtin_skills"

// BuiltinSkills 返回平台内置技能（编译期内嵌）。目录结构：builtin_skills/<name>/SKILL.md 加可选支持文件；
// <name> 目录带 “multica-” 前缀，使其磁盘 slug 永远不会与用户自建 workspace 技能冲突。
func (s *TaskService) BuiltinSkills() []AgentSkillData {
	return loadBuiltinSkills()
}

// loadBuiltinSkills 逐个读取内嵌目录中的技能目录并组装技能列表。
func loadBuiltinSkills() []AgentSkillData {
	entries, err := fs.ReadDir(builtinSkillsFS, builtinSkillsRoot)
	if err != nil {
		return nil
	}
	var skills []AgentSkillData
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if skill, ok := loadBuiltinSkill(entry.Name()); ok {
			skills = append(skills, skill)
		}
	}
	return skills
}

// loadBuiltinSkill 读取单个内置技能：SKILL.md 作为正文，目录内其它文件（保留相对路径）作为支持文件。
func loadBuiltinSkill(name string) (AgentSkillData, bool) {
	dir := path.Join(builtinSkillsRoot, name)
	content, err := fs.ReadFile(builtinSkillsFS, path.Join(dir, "SKILL.md"))
	if err != nil {
		// 没有 SKILL.md 的技能目录视为畸形——跳过而不是下发空技能。
		return AgentSkillData{}, false
	}
	skill := AgentSkillData{Name: name, Content: string(content)}
	// 目录里其它文件都作为支持文件，保留相对路径使子目录（如 rules/styling.md）得以存活。
	_ = fs.WalkDir(builtinSkillsFS, dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		rel := strings.TrimPrefix(p, dir+"/")
		if rel == "SKILL.md" {
			return nil
		}
		data, readErr := fs.ReadFile(builtinSkillsFS, p)
		if readErr != nil {
			return nil
		}
		skill.Files = append(skill.Files, AgentSkillFileData{Path: rel, Content: string(data)})
		return nil
	})
	return skill, true
}
