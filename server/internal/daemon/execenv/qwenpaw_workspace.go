// 准备 QwenPaw 智能体的"每任务工作区"：把绑定到该任务的技能展开成
// QwenPaw 能原生识别的目录布局。daemon 随后会把 --workspace <dir> 传给
// `qwenpaw acp`，让 QwenPaw agent 通过自身的工作区技能发现机制加载这些技能。

package execenv

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// prepareQwenpawWorkspace 为单次任务创建 QwenPaw 工作区目录，并把绑定的技能
// 写入其中。daemon 会把 --workspace <dir> 传给 `qwenpaw acp`，使 QwenPaw
// agent 能通过自身的工作区技能发现机制原生加载这些技能。
//
// 技能被写成 <workspace>/skills/<slug>/SKILL.md，同时预生成一份 skill.json
// 清单，对每个绑定技能标记 enabled: true——这样 QwenPaw 的
// reconcile_workspace_manifest（registry.py）会把它们判定为已生效，
// 用户无需手动启用。
//
// 函数是幂等的：重建前总是先删除旧的 skills 目录和清单，因此新增/移除/修改
// 技能都能生效而不会累积陈旧状态。这也覆盖"空技能"情形——当所有技能都被
// 解绑时，之前写过的文件会被清干净，符合用户"解绑即撤销"的预期。
//
// 此模式与 writeHermesBoundSkills（hermes_home.go:738）的做法一致。
func prepareQwenpawWorkspace(qwenpawWorkspace string, workspaceSkills []SkillContextForEnv, logger *slog.Logger) error {
	// 创建工作区根目录。
	if err := os.MkdirAll(qwenpawWorkspace, 0o700); err != nil {
		return fmt.Errorf("create qwenpaw workspace dir: %w", err)
	}

	// 先删掉旧的 skills 目录与清单，避免增量写入累积陈旧状态
	// （与 writeHermesBoundSkills 的做法一致，hermes_home.go:738）。
	skillsDir := filepath.Join(qwenpawWorkspace, "skills")
	if err := os.RemoveAll(skillsDir); err != nil {
		return fmt.Errorf("remove existing skills dir: %w", err)
	}
	manifestPath := filepath.Join(qwenpawWorkspace, "skill.json")
	if err := os.RemoveAll(manifestPath); err != nil {
		return fmt.Errorf("remove existing manifest: %w", err)
	}

	if len(workspaceSkills) == 0 {
		// 没有技能可写。上面的清理已经把旧文件清掉，
		// 于是解绑全部技能就等于正确撤销了它们。
		return nil
	}

	// 把技能写进 <workspace>/skills/。QwenPaw 的 store.py 通过
	// get_workspace_skills_dir() 从 "skills" 目录读取工作区技能；
	// 见 qwenpaw/agents/skill_system/store.py。
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("create qwenpaw skills dir: %w", err)
	}

	skillNames := make([]string, 0, len(workspaceSkills))
	slugs := resolveSkillSlugs(workspaceSkills)
	for i, skill := range workspaceSkills {
		slug := slugs[i]
		skillDir := filepath.Join(skillsDir, slug)

		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			return fmt.Errorf("create skill dir for %q: %w", skill.Name, err)
		}

		body := ensureSkillFrontmatter(skill.Content, slug, skill.Description)
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
			return fmt.Errorf("write SKILL.md for %q: %w", skill.Name, err)
		}

		// 写入附属文件（技能引用的脚本/资源等）。
		for _, f := range skill.Files {
			fpath := filepath.Join(skillDir, f.Path)
			if err := os.MkdirAll(filepath.Dir(fpath), 0o755); err != nil {
				return fmt.Errorf("create dir for skill file %q: %w", f.Path, err)
			}
			if err := os.WriteFile(fpath, []byte(f.Content), 0o644); err != nil {
				return fmt.Errorf("write skill file %q: %w", f.Path, err)
			}
		}

		skillNames = append(skillNames, slug)
	}

	// 写 skill.json 清单，对每个绑定技能标记 enabled: true。
	// 该格式正是 QwenPaw 的 reconcile_workspace_manifest 所期待的
	// （见 store.py get_workspace_skill_manifest_path / registry.py
	// resolve_effective_skills），因此技能立即生效，用户无需经 UI 或 CLI 启用。
	now := time.Now().UTC().Format(time.RFC3339)
	skills := make(map[string]any, len(skillNames))
	for _, slug := range skillNames {
		skills[slug] = map[string]any{
			"enabled":  true,
			"channels": []string{"all"},
			"source":   "customized",
			"metadata": map[string]any{
				"name":        slug,
				"description": "",
				"source":      "customized",
				"protected":   false,
				"updated_at":  now,
			},
			"updated_at": now,
		}
	}

	manifest := map[string]any{
		"schema_version": "workspace-skill-manifest.v1",
		"version":        0,
		"skills":         skills,
	}

	manifestPath = filepath.Join(qwenpawWorkspace, "skill.json")
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal skill manifest: %w", err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		return fmt.Errorf("write skill manifest: %w", err)
	}

	logger.Info("qwenpaw workspace prepared", "workspace", qwenpawWorkspace, "skills", len(skillNames))
	return nil
}
