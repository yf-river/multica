package execenv

// 文件导读：本文件把用户 ~/.codex/config.toml 里继承下来的 `[[skills.config]]`
// 条目从 per-task config.toml 中剥掉。
//
// 背景见下方 stripSkillsConfigEntries 的详细说明：Codex Desktop 写的插件型技能条目
// 只有 name 没有 path，CLI 0.114 更严格的 TOML 解析器会以 `missing field path` 拒绝
// 并中止 `codex thread/start`。由于 daemon 会把用户配置原样复制进每个任务的隔离
// codex-home，坏条目会一并传染；而 Multica 自己把活跃技能直接写进 codex-home/skills/，
// Codex 会从那里原生发现，因此用户级技能注册表对 per-task 运行毫无用处——剥掉它
// 既安全也正当。

import (
	"fmt"
	"os"
	"strings"
)

// stripSkillsConfigEntries 从给定 config.toml 内容中移除每个 `[[skills.config]]`
// 的数组-of-tables 块。
//
// 背景：Codex Desktop 给它认识的每个技能写一条 `[[skills.config]]`——文件型技能带
// `path = "..."`，而插件型技能（如 `name = "superpowers:brainstorming"`）只有
// `name`。Codex CLI 0.114 的 TOML 反序列化器把 `path` 当作必填字段，于是以
// `missing field path` 拒绝插件条目并拒绝启动。Multica 把用户的 ~/.codex/config.toml
// 原样复制进每个任务的隔离 codex-home，坏条目也随之进入 per-task 配置、阻塞
// `codex thread/start`。
//
// 整段剥掉 `[[skills.config]]` 数组绕开了该问题：Multica 把 agent 当前分配的技能
// 直接写进 `codex-home/skills/<name>/SKILL.md`，Codex 会从那个目录自动发现它们。
// 用户级技能注册表对 per-task 运行无关紧要，删掉它既安全、也是正确范围的隔离。
//
// `[[skills.config]]` 块之外的行原样保留。
func stripSkillsConfigEntries(content string) string {
	if !strings.Contains(content, "[[skills.config]]") {
		return content
	}

	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	inSkillsConfig := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// 任何一个新的 TOML 头都会结束当前 `[[skills.config]]` 块——无论它是同一数组
		// 的下一条还是别的表。
		if strings.HasPrefix(trimmed, "[") {
			if trimmed == "[[skills.config]]" {
				inSkillsConfig = true
				continue
			}
			inSkillsConfig = false
			out = append(out, line)
			continue
		}

		if inSkillsConfig {
			continue
		}
		out = append(out, line)
	}

	stripped := strings.Join(out, "\n")
	// 收拢删除可能留下的尾部空行簇，别让重复复制把文件无限撑大。
	stripped = strings.TrimRight(stripped, "\n") + "\n"
	if strings.TrimSpace(stripped) == "" {
		return ""
	}
	return stripped
}

// sanitizeCopiedCodexConfig 就地重写 per-task config.toml，剥掉从共享
// `~/.codex/config.toml` 继承来的 `[[skills.config]]` 条目。文件不存在或无变化时是
// 空操作。
func sanitizeCopiedCodexConfig(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read config.toml: %w", err)
	}
	stripped := stripSkillsConfigEntries(string(data))
	if stripped == string(data) {
		return nil
	}
	if err := os.WriteFile(configPath, []byte(stripped), 0o644); err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}
	return nil
}
