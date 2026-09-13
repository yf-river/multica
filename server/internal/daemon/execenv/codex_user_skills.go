package execenv

// 文件导读：本文件把用户安装的技能从共享 ~/.codex/skills/ 链进 per-task CODEX_HOME，
// 使 codex CLI 能原生发现它们（Codex 是唯一把 HOME 重定向到 per-task 目录的运行时，
// 不链过去 CLI 就永远看不到用户的技能）。

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	for _, s := range workspaceSkills {
		reserved[sanitizeSkillName(s.Name)] = struct{}{}
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
		if err := createDirLink(resolved, dst); err != nil {
			logger.Warn("execenv: codex user-skill link failed", "name", name, "error", err)
			continue
		}
	}
	return nil
}
