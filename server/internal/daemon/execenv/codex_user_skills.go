package execenv

// 文件导读：本文件把用户安装的技能从共享 ~/.codex/skills/ 链进 per-task CODEX_HOME，
// 使 codex CLI 能原生发现它们（Codex 是唯一把 HOME 重定向到 per-task 目录的运行时，
// 不链过去 CLI 就永远看不到用户的技能）。

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// seedUserCodexSkills 把共享 ~/.codex/skills/ 里用户安装的技能目录链进 per-task
// CODEX_HOME，让 codex CLI 原生发现它们。Codex 是唯一经 CODEX_HOME 环境变量把 home
// 重定向到 per-task 目录的运行时，没有这步 CLI 永远看不到用户的 ~/.codex/skills/
// 内容。
//
// 用链接、绝不复制。复制会把整棵技能树记到每个任务目录上——npm 型技能装了几个就
// 是 100 MB——而且 hydrateCodexSkills 每次任务启动、在临界路径上都会重做一遍，只要
// issue 还开着就没有任何 GC 路径回收它。同一 per-task home 里另两个共享资源也出于
// 同样理由用链接：sessions（linkCodexSessionsToStore）与插件缓存
// （exposeSharedCodexPluginCache）。技能目录对 CLI 是只读输入，复制顺带买来的写入
// 隔离没有任何需求方。
//
// 名字冲突时 workspace 分配技能优先：任何清洗名与 workspace 技能清洗名相同的用户
// 技能都在这里跳过，随后 writeSkillFiles 把 workspace 版本写进干净的位置。
// writeSkillFiles 从不经由它没创建的链接写入：allocateCollisionFreeSkillDir 用
// os.Lstat 探测，所以这里留下的链接算「已被占用」，workspace 技能会落到 `-multica`
// 兄弟目录。
//
// 单个技能失败只记日志并跳过——一个坏的用户技能不能阻止任务运行。返回错误只留给
// 影响每个技能的那类失败：列共享技能目录，或建 per-task 技能目录。
func seedUserCodexSkills(codexHome string, workspaceSkills []SkillContextForEnv, logger *slog.Logger) error {
	sharedSkillsDir := filepath.Join(resolveSharedCodexHome(), "skills")

	info, err := os.Stat(sharedSkillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat shared skills dir: %w", err)
	}
	if !info.IsDir() {
		return nil
	}

	reserved := make(map[string]struct{}, len(workspaceSkills))
	for _, slug := range resolveSkillSlugs(workspaceSkills) {
		reserved[slug] = struct{}{}
	}

	entries, err := os.ReadDir(sharedSkillsDir)
	if err != nil {
		return fmt.Errorf("read shared skills dir: %w", err)
	}

	targetSkillsDir := filepath.Join(codexHome, "skills")
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		if _, claimed := reserved[sanitizeSkillName(name)]; claimed {
			logger.Info("execenv: codex user-skill yields to workspace skill", "name", name)
			continue
		}
		src := filepath.Join(sharedSkillsDir, name)
		// lark-cli 这类安装器把每个技能装成指向共享 ~/.agents/skills/<name>/ 的
		// symlink。先解析它，让 per-task 链接指向真实目录而非另一条链接，也让下面的
		// IsDir 检查看到真实目标。
		resolved, err := filepath.EvalSymlinks(src)
		if err != nil {
			logger.Warn("execenv: codex user-skill resolve failed", "name", name, "error", err)
			continue
		}
		fi, err := os.Stat(resolved)
		if err != nil || !fi.IsDir() {
			continue
		}
		// hydrateCodexSkills 每次播种前会清空 skills 目录，所以父目录通常不存在；
		// createDirLink 需要它在。延迟创建，使「没有合格用户技能」的任务连技能目录
		// 都不会有。
		if err := os.MkdirAll(targetSkillsDir, 0o755); err != nil {
			return fmt.Errorf("create codex skills dir %s: %w", targetSkillsDir, err)
		}
		dst := filepath.Join(targetSkillsDir, name)
		// 移除的是链接本身、绝不是链接目标：os.RemoveAll 对 symlink 只解链；Windows
		// 上 RemoveDirectory 会去掉 junction 而保持其指向的目录完好。
		if err := os.RemoveAll(dst); err != nil {
			logger.Warn("execenv: codex user-skill clean dst failed", "name", name, "error", err)
			continue
		}
		if err := seedCodexUserSkill(resolved, dst, name); err != nil {
			_ = os.RemoveAll(dst)
			logger.Warn("execenv: codex user-skill normalization failed", "name", name, "error", err)
			continue
		}
	}
	return nil
}

// normalizeUserCodexSkillContent repairs only what Codex's skill parser
// rejects, and otherwise returns content unchanged so the skill stays linked:
//
//   - no frontmatter at all: synthesize one around the body;
//   - description missing, blank or not a string: fill it (Codex requires a
//     non-empty description);
//   - name present but not a string: replace it with the slug. A missing or
//     blank name is left alone — Codex falls back to the directory name.
//
// Every repair is re-parsed and must keep all other fields semantically
// identical; anything that cannot be repaired that way returns an error and
// the caller skips the skill instead of publishing a file Codex cannot load.
func normalizeUserCodexSkillContent(content, slug string) (string, error) {
	fm, body, ok := frontmatterParts(content)
	description := "Instructions for the " + slug + " skill."
	if !ok {
		if strings.HasPrefix(content, "---") {
			return "", fmt.Errorf("unterminated skill frontmatter")
		}
		return synthesizeFrontmatter(content, slug, description), nil
	}
	// Decoding into a map is the validity gate: unlike a Node decode it
	// rejects duplicate keys, which Codex's loader rejects too.
	var before map[string]any
	if err := yaml.Unmarshal([]byte(fm), &before); err != nil || before == nil {
		return "", fmt.Errorf("invalid skill frontmatter")
	}
	desc, _ := before["description"].(string)
	needDesc := strings.TrimSpace(desc) == ""
	rawName, nameKey := before["name"]
	_, nameIsString := rawName.(string)
	needName := nameKey && rawName != nil && !nameIsString
	if !needDesc && !needName {
		return content, nil
	}
	_, descKey := before["description"]
	if needDesc && !descKey && !needName {
		// Common case: a plain block mapping simply lacks description. Insert
		// one line after the opening delimiter so the rest stays byte-for-byte.
		fmStart, _ := frontmatterBodyStart(content)
		eol := strings.TrimPrefix(content[:fmStart], "---")
		candidate := content[:fmStart] + "description: " + yamlEscapeInline(description) + eol + content[fmStart:]
		if verifyUserSkillRepair(candidate, before, description) {
			return candidate, nil
		}
		// Flow mappings, indented mappings, root tags etc. cannot take a
		// prepended line; fall through to the node rewrite.
	}
	candidate, err := rewriteUserSkillFrontmatterNode(fm, body, slug, description, needName, needDesc)
	if err != nil {
		return "", err
	}
	if !verifyUserSkillRepair(candidate, before, description) {
		return "", fmt.Errorf("skill frontmatter could not be repaired safely")
	}
	return candidate, nil
}

// verifyUserSkillRepair reports whether repaired parses, carries the filled
// description, and leaves every other field equal to the original — the
// guarantee that a repair neither breaks nor changes the author's metadata.
func verifyUserSkillRepair(repaired string, before map[string]any, description string) bool {
	fm, _, ok := frontmatterParts(repaired)
	if !ok {
		return false
	}
	var after map[string]any
	if err := yaml.Unmarshal([]byte(fm), &after); err != nil || after == nil {
		return false
	}
	if d, _ := after["description"].(string); strings.TrimSpace(d) == "" {
		return false
	}
	if n, present := after["name"]; present && n != nil {
		if _, isString := n.(string); !isString {
			return false
		}
	}
	for k, v := range before {
		if k == "description" || k == "name" {
			continue
		}
		if !reflect.DeepEqual(after[k], v) {
			return false
		}
	}
	for k := range after {
		if _, existed := before[k]; !existed && k != "description" && k != "name" {
			return false
		}
	}
	return true
}

// rewriteUserSkillFrontmatterNode edits the parsed node tree: an existing
// value is swapped in place keeping its comments; aliases that pointed at it
// are expanded first so they keep their original value instead of dangling.
// A missing key is prepended, in whatever style (block or flow) the mapping
// already uses.
func rewriteUserSkillFrontmatterNode(fm, body, slug, description string, needName, needDesc bool) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(fm), &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("invalid skill frontmatter")
	}
	mapping := doc.Content[0]
	set := func(key, value string) {
		replacement := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value != key {
				continue
			}
			old := mapping.Content[i+1]
			if old.Anchor != "" {
				expandAliasesOf(&doc, old)
			}
			replacement.HeadComment = old.HeadComment
			replacement.LineComment = old.LineComment
			replacement.FootComment = old.FootComment
			mapping.Content[i+1] = replacement
			return
		}
		mapping.Content = append([]*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			replacement,
		}, mapping.Content...)
	}
	if needDesc {
		set("description", description)
	}
	if needName {
		set("name", slug)
	}
	var out strings.Builder
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return "---\n" + out.String() + "---\n" + body, nil
}

// expandAliasesOf replaces every alias of target under n with an anchor-free
// copy of target, so target can be removed without stranding them.
func expandAliasesOf(n, target *yaml.Node) {
	for i, child := range n.Content {
		if child.Kind == yaml.AliasNode && child.Alias == target {
			expanded := *target
			expanded.Anchor = ""
			n.Content[i] = &expanded
			continue
		}
		expandAliasesOf(child, target)
	}
}

func seedCodexUserSkill(src, dst, name string) error {
	data, err := os.ReadFile(filepath.Join(src, "SKILL.md"))
	if err != nil {
		return err
	}
	normalized, err := normalizeUserCodexSkillContent(string(data), sanitizeSkillName(name))
	if err != nil {
		return err
	}
	if normalized == string(data) {
		return createDirLink(src, dst)
	}
	// A hidden sibling is owned and reclaimed with the per-task skills tree.
	// Keeping dst a link also preserves junction-based hydration on Windows.
	overlay := filepath.Join(filepath.Dir(dst), ".multica-normalized", name)
	if err := os.RemoveAll(overlay); err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(overlay)
		}
	}()
	if err := os.MkdirAll(overlay, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(overlay, "SKILL.md"), []byte(normalized), 0o644); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "SKILL.md" {
			continue
		}
		source, target := filepath.Join(src, entry.Name()), filepath.Join(overlay, entry.Name())
		info, err := os.Stat(source)
		if err != nil {
			return err
		}
		if info.IsDir() {
			err = createDirLink(source, target)
		} else {
			err = createFileLink(source, target)
		}
		if err != nil {
			return err
		}
	}
	if err := createDirLink(overlay, dst); err != nil {
		return err
	}
	complete = true
	return nil
}
