package execenv

// 文件导读：本文件负责「把任务的上下文落成 agent 工作目录里的文件」。
//
// 角色：daemon 准备环境时，会向任务 workdir 写入两类内容——(1) .agent_context/
// issue_context.md 一类的任务简介（issue/autopilot/quick-create 三种形态）；
// (2) 给不同 agent 提供商的「原生位置」写技能目录（Claude 写 .claude/skills/、
// Cursor 写 .cursor/skills/ 等，让各运行时按自己方式原生发现技能）。Codex 与
// Hermes 的技能不走这里，分别在 Prepare 里经 codex-home / hermes-home 处理。
//
// 它还写下两种「任务标记」：workdir 内的 .multica/daemon_task_context.json，以及
// workspacesRoot 上的根级标记——两者都是 CLI 判断「本进程是否仍处于 daemon 管辖的
// 任务里」的非机密信号（沙箱剥掉全部 MULTICA_* 环境变量时用，避免回落去用用户
// 自己的 PAT）。
//
// 其它内容：把写过的文件/目录记入 sidecarManifest，供 local_directory 任务拆除时
// 精确回滚；项目资源写出 .multica/project/resources.json。此外本文件收录了 SKILL.md
// 前置元信息（frontmatter）的整修逻辑：确保每个技能目录里的 SKILL.md 开头 YAML 块
// 都有一个可解析、非空的 `name` 键，且与目录名一致——否则 OpenCode、Codex 等运行
// 时会在加载时静默丢弃该技能。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	skillpkg "github.com/multica-ai/multica/server/internal/skill"
	"github.com/multica-ai/multica/server/pkg/agent"
	"gopkg.in/yaml.v3"
)

// TaskContextMarkerRelPath 是 daemon 写到任务 workdir 下的非机密标记路径。当某个
// 子沙箱在调用 `multica` 前剥掉了全部 MULTICA_* 环境变量时，CLI 把它作为「处于
// daemon 任务中」的回退信号。
const TaskContextMarkerRelPath = ".multica/daemon_task_context.json"

// TaskContextMarkerManagedBy 是 CLI 在把 TaskContextMarkerRelPath 当作 daemon 自有
// 标记之前检查的判别值。
const TaskContextMarkerManagedBy = "multica-daemon-task"

type taskContextMarkerFile struct {
	ManagedBy     string `json:"managed_by"`
	AgentID       string `json:"agent_id,omitempty"`
	IssueID       string `json:"issue_id,omitempty"`
	ChatSessionID string `json:"chat_session_id,omitempty"`
}

// EnsureWorkspacesRootMarker 在 {workspacesRoot}/.multica/daemon_task_context.json
// 写一份持久化的 daemon 任务标记。
//
// per-workdir 标记只保护「cwd 落在 workdir 内」的 `multica` 调用，因为 CLI 发现标记
// 的方式是从 cwd 往上走。一个丢光了 MULTICA_* 环境变量、又逃逸到 workdir 父目录的
// 沙箱子进程位于该标记之上，找不到任何 daemon 信号，就会回落去用用户的配置 PAT——
// 一条已确认的冒名路径。workspacesRoot 下的每个目录都是 daemon 自有的，所以根级
// 标记把整棵树放回 fail-closed 防护之下，且不碰任何用户工作的目录。
//
// 已存在且归 daemon 所有的标记保持不动，这份节点级共享文件不必每次任务启动都重写。
// 被截断或无法解析的文件——一次被杀的 daemon 写到一半留下的撕坏 os.WriteFile 的
// 特征——会被回收并重写，所以一次崩溃永远无法让整个节点的防护报废；只有「可解析却
// 归别人所有」的标记才被当作真正的外来文件拒绝。(重)写是原子的（临时文件 + rename）：
// 一个从逃逸 cwd 往上走的并发 CLI，要么看到旧字节、要么看到完整新文件，绝不会看到
// 半份会被它误读成「没有信号」而门户大开的标记。
func EnsureWorkspacesRootMarker(workspacesRoot string) error {
	if strings.TrimSpace(workspacesRoot) == "" {
		return errors.New("execenv: workspaces root is required")
	}
	path := filepath.Join(workspacesRoot, TaskContextMarkerRelPath)
	if existing, err := os.ReadFile(path); err == nil {
		var marker taskContextMarkerFile
		if json.Unmarshal(existing, &marker) == nil {
			if marker.ManagedBy == TaskContextMarkerManagedBy {
				return nil
			}
			// 可解析但归别人所有：绝不覆盖。
			return fmt.Errorf("foreign file at workspaces root marker path %s; refusing to overwrite", path)
		}
		// 不可解析的内容几乎可以肯定是我们自己被撕坏的写入；落到下面把它回收重写。
	} else if !os.IsNotExist(err) {
		// 真正的读错误（例如路径上是目录）不是可以安全覆盖的信号；暴露出来。
		// 调用方以非致命方式降级。
		return fmt.Errorf("read workspaces root marker %s: %w", path, err)
	}
	payload := taskContextMarkerFile{ManagedBy: TaskContextMarkerManagedBy}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workspaces root marker: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create workspaces root marker dir: %w", err)
	}
	if err := writeWorkspacesRootMarkerAtomic(path, data); err != nil {
		return fmt.Errorf("write workspaces root marker: %w", err)
	}
	return nil
}

// writeWorkspacesRootMarkerAtomic 通过「同目录临时文件 + rename」把 data 写到 path，
// 使并发读者要么看到旧文件、要么看到完整新文件——绝不会看到写了一半的内容。镜像
// daemon-id / CLI-config 的写入习惯（见 writeDaemonIDFile）。权限 0644：CLI 向上的
// 目录遍历必须能让「可能跑在不同 uid 下」的子进程读到标记；载荷不机密。
func writeWorkspacesRootMarkerAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".daemon_task_context-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp workspaces root marker: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp workspaces root marker: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp workspaces root marker: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp workspaces root marker: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename workspaces root marker: %w", err)
	}
	return nil
}

// writeContextFiles 渲染并写出 .agent_context/issue_context.md 与技能，放到对应
// provider 原生的位置。
//
// Claude:      skills → {workDir}/.claude/skills/{name}/SKILL.md  （原生发现）
// CodeBuddy:   skills → {workDir}/.codebuddy/skills/{name}/SKILL.md  （原生发现——CodeBuddy 是 Claude Code 的分支，但用自己的配置目录 .codebuddy/，不用 .claude/；见 https://www.codebuddy.ai/docs/cli/skills）
// Codex:       skills → 由 Prepare 经 codex-home 另行处理
// Hermes:      skills → 由 Prepare 经 hermes-home 另行处理（HERMES_HOME/skills；Hermes 没有 workspace 相对发现，见 hermes_home.go）
// Copilot:     skills → {workDir}/.github/skills/{name}/SKILL.md  （原生项目级发现）
// OpenCode:    skills → {workDir}/.opencode/skills/{name}/SKILL.md  （原生发现）
// Pi:          skills → {workDir}/.pi/skills/{name}/SKILL.md  （原生发现）
// Cursor:      skills → {workDir}/.cursor/skills/{name}/SKILL.md  （原生发现）
// Kimi:        skills → {workDir}/.kimi/skills/{name}/SKILL.md  （原生发现）
// Reasonix:    skills → {workDir}/.reasonix/skills/{name}/SKILL.md  （原生发现）
// DSH:         skills → {workDir}/.dsh/skills/{name}/SKILL.md  （原生发现）
// Kiro:        skills → {workDir}/.kiro/skills/{name}/SKILL.md  （原生发现）
// Qoder/Qoder CN: skills → {workDir}/.qoder/skills/{name}/SKILL.md  （项目级；见 provider 文档）
// Qwen Code:    skills → {workDir}/.qwen/skills/{name}/SKILL.md  （原生项目级发现）
// QwenPaw:      skills → {workDir}/.qwenpaw/skills/{name}/SKILL.md  （原生项目级发现）
// MiniMax Code: skills → {workDir}/.minimax/skills/{name}/SKILL.md  （原生项目级发现）
// Antigravity: skills → {workDir}/.agents/skills/{name}/SKILL.md  （原生发现——见 https://antigravity.google/docs/gcli-migration 的 "Workspace skills"）
// Default:     skills → {workDir}/.agent_context/skills/{name}/SKILL.md  （未匹配任何
// provider 时的回退位置，被 meta 配置引用）
//
// manifest 非 nil 时，会记下我们创建的每个文件以及每个不得不 MkdirAll 的中间目录
// （此前已存在的跳过）。CleanupSidecars 用它把 local_directory 任务的 workdir 回滚到
// Prepare 前的状态。不需要清理的调用方——envRoot 会被 GC 整棵清掉的云端任务——
// 可以传 nil 完全跳过这本账。
func writeContextFiles(workDir, provider string, ctx TaskContextForEnv, manifest *sidecarManifest) error {
	if err := writeTaskContextMarker(workDir, ctx, manifest); err != nil {
		return err
	}

	contextDir := filepath.Join(workDir, ".agent_context")
	if err := recordMkdirAll(contextDir, 0o755, manifest); err != nil {
		return fmt.Errorf("create .agent_context dir: %w", err)
	}

	content := renderIssueContext(provider, ctx)
	path := filepath.Join(contextDir, "issue_context.md")
	if err := recordWriteFile(path, []byte(content), 0o644, manifest); err != nil {
		// 路径已存在意味着用户已经占着 .agent_context/issue_context.md——可能是他们
		// 自己建的，也可能是某次崩溃的先前运行残留、而我们无法安全地与有意内容区分。
		// 拒绝写入是正确选择：运行时 brief（CLAUDE.md / AGENTS.md）已经带上这个文件
		// 该有的每个事实，agent 没有 sidecar 副本也能正常跑。其它任何情况都是真正的
		// 失败。
		if !errors.Is(err, errPathPreExists) {
			return fmt.Errorf("write issue_context.md: %w", err)
		}
	}

	if len(ctx.AgentSkills) > 0 {
		// Hermes 在 Prepare 时把技能物化进 per-task 的 HERMES_HOME/skills（Hermes
		// 没有 workspace 相对发现），所以它完全不需要 workdir 本地技能目录——跳过
		// resolve，避免留下一个空的 .agent_context/skills/。
		if provider != "hermes" {
			skillsDir, err := resolveSkillsDir(workDir, provider, manifest)
			if err != nil {
				return fmt.Errorf("resolve skills dir: %w", err)
			}
			// Codex 技能在 Prepare 时写进 codex-home；这里跳过。
			if provider != "codex" {
				if err := writeSkillFiles(skillsDir, ctx.AgentSkills, manifest); err != nil {
					return fmt.Errorf("write skill files: %w", err)
				}
			}
		}
	}

	// 项目资源是尽力而为：写失败只记日志，不阻塞任务启动。资源缺失表现为 agent
	// 看不到文件——这与「按需提供、而非全量倾倒」的设计一致（meta skill 内容总是
	// 列出 agent 应该期待什么）。
	if err := writeProjectResources(workDir, ctx, manifest); err != nil {
		// 调用方负责记 warning；非致命的上下文不要 noisy 返回。
		return fmt.Errorf("write project resources: %w", err)
	}

	return nil
}

func writeTaskContextMarker(workDir string, ctx TaskContextForEnv, manifest *sidecarManifest) error {
	dir := filepath.Dir(filepath.Join(workDir, TaskContextMarkerRelPath))
	if err := recordMkdirAll(dir, 0o755, manifest); err != nil {
		return fmt.Errorf("create .multica dir: %w", err)
	}
	// sidecar manifest 会在正常 local_directory 清理时移除这个标记。若崩溃把它留在
	// 原地，CLI 有意把它当作 daemon 上下文处理并关闭即失败，而不是去用用户 PAT。
	payload := taskContextMarkerFile{
		ManagedBy:     TaskContextMarkerManagedBy,
		AgentID:       ctx.AgentID,
		IssueID:       ctx.IssueID,
		ChatSessionID: ctx.ChatSessionID,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal task context marker: %w", err)
	}
	if err := recordWriteFile(filepath.Join(workDir, TaskContextMarkerRelPath), data, 0o644, manifest); err != nil {
		if errors.Is(err, errPathPreExists) {
			path := filepath.Join(workDir, TaskContextMarkerRelPath)
			existing, readErr := os.ReadFile(path)
			if readErr != nil {
				return fmt.Errorf("read existing task context marker: %w", readErr)
			}
			var marker taskContextMarkerFile
			if json.Unmarshal(existing, &marker) != nil || marker.ManagedBy != TaskContextMarkerManagedBy {
				return fmt.Errorf("write task context marker: %w", err)
			}
			if writeErr := os.WriteFile(path, data, 0o644); writeErr != nil {
				return fmt.Errorf("refresh task context marker: %w", writeErr)
			}
			if manifest != nil {
				manifest.Files = append(manifest.Files, path)
			}
			return nil
		}
		return fmt.Errorf("write task context marker: %w", err)
	}
	return nil
}

// projectResourceFile 是写进 agent 工作目录的磁盘 JSON。结构刻意做成 API 响应的薄
// 透传，使消费方（技能、未来的工具）不需要单独的解析器。
type projectResourceFile struct {
	ProjectID          string                  `json:"project_id,omitempty"`
	ProjectTitle       string                  `json:"project_title,omitempty"`
	ProjectDescription string                  `json:"project_description,omitempty"`
	Resources          []ProjectResourceForEnv `json:"resources"`
}

// MarshalJSON 把 resource_ref 字段渲染成原始 JSON 而不是 base64 blob。结构体其余
// 字段都是简单字符串。
func (p ProjectResourceForEnv) MarshalJSON() ([]byte, error) {
	type alias struct {
		ID           string          `json:"id"`
		ResourceType string          `json:"resource_type"`
		ResourceRef  json.RawMessage `json:"resource_ref"`
		Label        string          `json:"label,omitempty"`
	}
	ref := p.ResourceRef
	if len(ref) == 0 {
		ref = json.RawMessage("{}")
	}
	return json.Marshal(alias{
		ID:           p.ID,
		ResourceType: p.ResourceType,
		ResourceRef:  ref,
		Label:        p.Label,
	})
}

// writeProjectResources 在任务携带项目上下文时，把 .multica/project/resources.json
// 写进工作目录。只要挂了项目就总会写（哪怕资源数为零），使 agent 能把它的存在当成
// 「有项目」的信号。
//
// manifest 非 nil 时，会记下 .multica/project 这一串新建目录与 resources.json 文件，
// 供 CleanupSidecars 在 local_directory 拆除时撤销它们。
func writeProjectResources(workDir string, ctx TaskContextForEnv, manifest *sidecarManifest) error {
	if ctx.ProjectID == "" && len(ctx.ProjectResources) == 0 {
		return nil
	}
	dir := filepath.Join(workDir, ".multica", "project")
	if err := recordMkdirAll(dir, 0o755, manifest); err != nil {
		return err
	}
	resources := ctx.ProjectResources
	if resources == nil {
		resources = []ProjectResourceForEnv{}
	}
	payload := projectResourceFile{
		ProjectID:          ctx.ProjectID,
		ProjectTitle:       ctx.ProjectTitle,
		ProjectDescription: ctx.ProjectDescription,
		Resources:          resources,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := recordWriteFile(filepath.Join(dir, "resources.json"), data, 0o644, manifest); err != nil {
		// .multica/project/resources.json 归 Multica 所有，路径已存在几乎肯定是
		// manifest 不得摧毁的用户内容。运行时 brief 已列出每个项目资源，agent 没有
		// JSON sidecar 也能正常跑——撞名时降级为仅 brief 模式。
		if !errors.Is(err, errPathPreExists) {
			return err
		}
	}
	return nil
}

// resolveSkillsDir 按 agent provider 返回技能应写入的目录并创建它。manifest 非 nil
// 时，会记下每个不得不 MkdirAll 的中间目录，供 CleanupSidecars 在 local_directory
// 拆除时 rmdir 它们。
func resolveSkillsDir(workDir, provider string, manifest *sidecarManifest) (string, error) {
	skillsDir := skillsDirPath(workDir, provider)
	if err := recordMkdirAll(skillsDir, 0o755, manifest); err != nil {
		return "", err
	}
	return skillsDir, nil
}

// skillsDirPath 返回 workDir 下、该 provider 原生的技能父目录，但不创建、也不记录
// 任何东西。resolveSkillsDir 用 MkdirAll + manifest 记账包一层；复用路径的技能回滚
// （removeReusedManagedSkillDirs）需要这条裸路径且无副作用，好与先前 manifest 记录
// 的受管技能根做匹配。
func skillsDirPath(workDir, provider string) string {
	// 内置运行时身份（如 "omp"）在描述符里声明自己的技能目录；在按协议家族分派
	// 前先通用解析。
	if desc, ok := agent.BuiltinRuntimeByID(provider); ok {
		return filepath.Join(workDir, desc.SkillsDir)
	}
	switch provider {
	case "claude":
		// Claude Code 原生从 workdir 的 .claude/skills/ 发现技能。
		return filepath.Join(workDir, ".claude", "skills")
	case "codebuddy":
		// CodeBuddy Code 是 Claude Code 的分支，但使用自己的原生项目级技能目录
		// .codebuddy/skills/，而非 .claude/skills/。见
		// https://www.codebuddy.ai/docs/cli/skills。
		return filepath.Join(workDir, ".codebuddy", "skills")
	case "copilot":
		// GitHub Copilot CLI 原生从 .github/skills/<name>/SKILL.md 发现项目级技能
		// （优先级高于 ~/.copilot/skills/ 下的用户级技能）。
		// 见：https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-config-dir-reference
		return filepath.Join(workDir, ".github", "skills")
	case "opencode":
		// OpenCode 原生从 workdir 的 .opencode/skills/ 发现项目技能。
		// ConfigPaths.directories() 从发现根向上找裸 `.opencode` 目录（无需
		// opencode.json 信号），然后 skill/index.ts 在每个匹配下扫
		// `{skill,skills}/**/SKILL.md`。发现锚定在任务 workdir——通过 opencodeBackend
		// 里的 `opencode run --dir <workDir>` + PWD 覆盖实现——没有这些，OpenCode
		// 会从 daemon 继承的 PWD 出发，完全错过 .opencode/skills 与 AGENTS.md
		// （MUL-2416）。
		return filepath.Join(workDir, ".opencode", "skills")
	case "codearts":
		// CodeArts Agent 从其 provider 专属的 .codeartsdoer/skills 目录发现项目技能。
		return filepath.Join(workDir, ".codeartsdoer", "skills")
	case "deveco":
		// DevEco Code（华为的 OpenCode 分支）原生从 workdir 的 .deveco/skills/ 发现
		// 项目技能，镜像 OpenCode 的 .opencode/skills 布局、改用其 DEVECO_ 前缀品牌。
		// 发现锚定在任务 workdir——通过 devecoBackend 的 `deveco run --dir <workDir>` +
		// PWD 覆盖，与 OpenCode 用同一锚点。
		return filepath.Join(workDir, ".deveco", "skills")
	case "pi":
		// Pi 原生从 workdir 的 .pi/skills/ 发现技能。
		return filepath.Join(workDir, ".pi", "skills")
	case "cursor":
		// Cursor 原生从 workdir 的 .cursor/skills/ 发现技能。
		return filepath.Join(workDir, ".cursor", "skills")
	case "kimi":
		// Kimi Code CLI 自动从 workdir 的 .kimi/skills/ 发现项目级技能。
		// 见 https://moonshotai.github.io/kimi-cli/en/customization/skills.html
		return filepath.Join(workDir, ".kimi", "skills")
	case "reasonix":
		// Reasonix 从 .reasonix/skills/ 发现项目技能并独立加载 AGENTS.md，因此仓库
		// 记忆与任务技能共存。
		return filepath.Join(workDir, ".reasonix", "skills")
	case "dsh":
		// DSH 同时扫 .dsh/skills 与 .agents/skills。优先用其品牌项目根，让运行时
		// 专属技能保持隔离。
		return filepath.Join(workDir, ".dsh", "skills")
	case "kiro":
		// Kiro CLI 自动从 workdir 的 .kiro/skills/ 发现项目级技能。
		return filepath.Join(workDir, ".kiro", "skills")
	case "qoder", "qoderclicn":
		// 两个 Qoder CLI 版本都在 .qoder/skills/ 下发现项目级技能。它们的用户级根
		// 不同，由 listRuntimeLocalSkills 处理。
		return filepath.Join(workDir, ".qoder", "skills")
	case "qwen":
		// Qwen Code 从 workdir 的 .qwen/skills/ 发现项目级技能。
		return filepath.Join(workDir, ".qwen", "skills")
	case "qwenpaw":
		// QwenPaw 从 <workDir>/skill_pool/ 发现 workspace 级技能。见 QwenPaw
		// skill_system/store.py 里的 get_workspace_skills_dir。
		return filepath.Join(workDir, "skill_pool")
	case "mcode":
		// MiniMax Code 从 .minimax/skills/ 发现项目级技能。
		return filepath.Join(workDir, ".minimax", "skills")
	case "traecli":
		// 官方 TRAE CLI 从 workdir 的 .traecli/skills/ 发现项目级技能（全局技能位于
		// ~/.traecli/skills）。见 https://docs.trae.cn/cli_skills
		return filepath.Join(workDir, ".traecli", "skills")
	case "antigravity":
		// Antigravity（`agy`）自动从 workdir 的 .agents/skills/ 发现 workspace 级
		// 技能。CLI 继承 Gemini CLI 的 workspace 技能布局；见
		// https://antigravity.google/docs/gcli-migration 的 "Workspace skills"。
		return filepath.Join(workDir, ".agents", "skills")
	case "grok":
		// Grok Build CLI 从 .grok/skills/ 发现项目级技能（也会扫 .agents/skills/）。
		// 优先原生 .grok 树。见 Grok user-guide skills.md。
		return filepath.Join(workDir, ".grok", "skills")
	default:
		// 回退：写入 .agent_context/skills/（被 meta 配置引用）。
		return filepath.Join(workDir, ".agent_context", "skills")
	}
}

var nonAlphaNum = regexp.MustCompile(`[^a-z0-9]+`)

// ensureSkillFrontmatter 返回保证以 YAML frontmatter 块开头、且带着可解析非空 `name`
// 键的 SKILL.md 内容。
//
// OpenCode 等运行时会静默丢弃「frontmatter 缺失」或「`name` 无法解析」的 SKILL.md，
// 所以这里处理四种情况：
//
//   - 完全没有 frontmatter → 合成一份带 `name: <slug>` 的（DB 里有 description 也带上）。
//   - 有 frontmatter、有非空 `name`、且能解析成合法 YAML → 把 `name` 重写成 slug，
//     其余键逐字保留。至于为什么偏偏不能放过 `name`，见下文。
//   - 有 frontmatter、有非空 `name`，但 YAML 非法（例如 description 里有未加引号的
//     冒号）→ 剥掉并重新合成，免得 Codex 这类运行时因解析错误丢弃技能。
//   - 有 frontmatter 却缺 `name`（例如上游技能只设了 `description`、导入时靠目录
//     slug 补 `name`）→ 在现有块首键位置前插 `name: <slug>`，让 OpenCode 仍能路由
//     该技能。
//
// `name` 是 Multica 必须占有的那个键。各运行时对「哪个字段标识技能」意见不一——
// Claude 按目录名路由、OpenCode 按 frontmatter 的 `name`——让两者背离会给同一个
// 技能两个不同的可调用名，取决于它在哪跑（MUL-5529）。slug 是权威的，因为它就是落
// 盘的东西，且是唯一带唯一性保证的值（allocateCollisionFreeSkillDir）；frontmatter
// 的 `name` 由作者提供，两个被导入的技能可能自称同一个名字。其余每个键都字节不变，
// 所以刻意塑形的上游 frontmatter 能在往返中存活。
func ensureSkillFrontmatter(content, slug, description string) string {
	fmStart, ok := frontmatterBodyStart(content)
	if !ok {
		return synthesizeFrontmatter(content, slug, description)
	}
	if isFrontmatterValidYAML(content) {
		// 「name 是否存在」由解析器而非拼写决定。词法扫描只认得「同一行带值」的裸
		// `name:`，于是 `"name": x` 与无值 `name:` 会被读成无名、进而注入第二个
		// `name`——一个严格的加载器会直接拒绝的重复映射键，让技能不是「只是名字错」
		// 而是「根本加载不了」。
		if frontmatterHasNameKey(content) {
			if rewritten, verified := setFrontmatterName(content, fmStart, slug); verified {
				return rewritten
			}
			// 手术式重写无法证明自身正确——锚点在 name 值上就是途径之一，因为替换该
			// 行会移除锚点、让指向它的别名悬空。块本身仍是合法 YAML，所以改从已解析
			// 节点重建，而不是重新合成：这能保住每个其它键，包括 disable-model-invocation
			// 这类策略字段——丢掉它等于重新暴露一个作者特意藏起、不让模型调用的技能。
			if rebuilt, ok := renameFrontmatterNameViaNode(content, slug); ok {
				return rebuilt
			}
			_, body, _ := frontmatterParts(content)
			return synthesizeFrontmatter(body, slug, description)
		}
		// 完全没有 name 键（由解析器确认而非推断）。作为首个键注入一个，其余逐字
		// 保留（包括 `description`、正文与导入路径保住的任何运行时专属键）。
		return content[:fmStart] + "name: " + slug + "\n" + content[fmStart:]
	}

	// YAML 非法：没有可参考的解析，于是退回词法扫描。带 name 的块被剥掉并重新合成，
	// 免得 Codex 这类运行时在加载期整个硬拒技能；frontmatterParts 找不到闭合定界符时
	// 把全文当正文返回，因此格式错误的块被保留而非静默丢弃。
	if hasFrontmatterName(content[fmStart:]) {
		_, body, _ := frontmatterParts(content)
		return synthesizeFrontmatter(body, slug, description)
	}
	return content[:fmStart] + "name: " + slug + "\n" + content[fmStart:]
}

// frontmatterHasNameKey 报告 content 的 frontmatter 是否能解析成「带顶层 `name` 键」
// 的映射，无论其拼写或值是什么。
//
// 引号是语法不是身份：`"name":` 与 `name:` 是同一个键；值为空的键也仍是这个键。
// 只有嵌套才有区别——`metadata:\n  name: x` 没有顶层 name，仍须补一个。
func frontmatterHasNameKey(content string) bool {
	fmBody, _, ok := frontmatterParts(content)
	if !ok {
		return false
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(fmBody), &doc); err != nil {
		return false
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return false
	}
	mapping := doc.Content[0]
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "name" {
			return true
		}
	}
	return false
}

// synthesizeFrontmatter 产出带 YAML frontmatter 块的 SKILL.md 正文，块里至少有
// `name`、（非空时）还有 `description`。description 一律转义成双引号 YAML 字符串，
// 使含冒号、方括号等 YAML 显著字符的值也能安全解析。
func synthesizeFrontmatter(body, slug, description string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", slug)
	if d := strings.TrimSpace(description); d != "" {
		fmt.Fprintf(&b, "description: %s\n", yamlEscapeInline(d))
	}
	b.WriteString("---\n\n")
	b.WriteString(body)
	return b.String()
}

// isFrontmatterValidYAML 报告 content 开头那段 YAML frontmatter 能否解析成 YAML
// 映射。无 frontmatter、块没有闭合定界符、为空或反序列化失败时返回 false。
func isFrontmatterValidYAML(content string) bool {
	fmBody, _, ok := frontmatterParts(content)
	if !ok || strings.TrimSpace(fmBody) == "" {
		return false
	}
	var m map[string]any
	return yaml.Unmarshal([]byte(fmBody), &m) == nil
}

// frontmatterParts 把 content 拆成原始 YAML frontmatter 正文（开头 `---` 行与闭合
// `---` 行之间的文本）以及跟在闭合定界符之后的文档正文。content 没有起始定界符或
// 没有闭合定界符行时 ok 为 false；此时 body 返回完整 content，调用方因此可以保留
// 畸形块而不是丢弃它。
//
// 闭合定界符是「唯一内容为 `---`、以 `\n`、`\r\n` 或文件结尾收尾」的行。把这条规则
// 收拢到此处，使合法性检查与重新合成路径不会对「块在哪里结束」产生分歧（例如 EOF
// 或 CRLF 收尾的 frontmatter）——过去两处定义一旦背离，就会留下一个陈旧的块。
func frontmatterParts(content string) (fmBody, body string, ok bool) {
	start, ok := frontmatterBodyStart(content)
	if !ok {
		return "", content, false
	}
	rest := content[start:]
	for searchFrom := 0; ; {
		nl := strings.Index(rest[searchFrom:], "\n---")
		if nl < 0 {
			return "", content, false
		}
		closeAt := searchFrom + nl
		after := rest[closeAt+len("\n---"):]
		switch {
		case after == "" || after == "\r":
			return rest[:closeAt], "", true
		case strings.HasPrefix(after, "\n"):
			return rest[:closeAt], after[len("\n"):], true
		case strings.HasPrefix(after, "\r\n"):
			return rest[:closeAt], after[len("\r\n"):], true
		default:
			// 不是独立的定界符行（如 "----" 或 "--- text"）；继续找真正的闭合。
			searchFrom = closeAt + len("\n---")
		}
	}
}

// frontmatterBodyStart 返回 YAML 正文开始的字节偏移（紧随起始 `---` 行之后），以及
// 是否找到了合法起始定界符。
func frontmatterBodyStart(content string) (int, bool) {
	if strings.HasPrefix(content, "---\n") {
		return 4, true
	}
	if strings.HasPrefix(content, "---\r\n") {
		return 5, true
	}
	return 0, false
}

// hasFrontmatterName 报告 frontmatter 正文（紧随起始 `---` 行之后的切片）在闭合
// `---` 之前是否含顶层 `name` 键，无论其拼写或值。
//
// 它是 frontmatterHasNameKey 面向畸形块的孪生：对无法解析的块回答同一问题，此时调用
// 方在「重新合成」（有 name）与「注入新 `name`」（无 name）之间选择。不变量是键的
// 存在性，而非值的存在性——`"name":`、`'name':`、无值的 `name:` 都算有这个键，在
// 其中任何一个上方再注入一个，会产生严格的加载器拒绝的重复映射键，使技能不是被治好
// 而是根本加载不了（MUL-5529）。把 `name:value` 这种普通标量读成键属于过度检测，
// 且是有意为之：块反正已不可解析，重新合成正是修复它的路径。
//
// 只统计无缩进的键。缩进的 `name:` 属于嵌套映射（`metadata:\n  name: foo`），此时块
// 仍没有顶层 name。
func hasFrontmatterName(fmBody string) bool {
	closeIdx := strings.Index(fmBody, "\n---")
	if closeIdx < 0 {
		// 缺闭合——把手里有的全扫一遍。frontmatter 已畸形、严格运行时反正会拒，
		// 但侦测到既有 name 能避免在上面再叠一层。
		closeIdx = len(fmBody)
	}
	for _, line := range strings.Split(fmBody[:closeIdx], "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "name:") ||
			strings.HasPrefix(line, `"name":`) ||
			strings.HasPrefix(line, `'name':`) {
			return true
		}
	}
	return false
}

// frontmatterNameValueSpan 返回已知「合法 YAML」的 frontmatter 正文里、整条顶层
// `name` 条目的字节范围——键加值、值占多少行算多少——在已确认合法 YAML 的
// frontmatter 正文内部。
//
// 边界取自 yaml.v3 自己的行号而不是缩进，因为缩进约束不住 YAML 值。带引号的标量
// 可能折到与其键「同一缩进」的下一行，flow 集合同理：
//
//	name: "upstream
//	continued"
//	name: [a,
//	b]
//
// 两例都能解析；按缩进定界会在键的那行停下，把剩余部分搁浅，让一次改写产出非法
// YAML。问解析器「下一个顶层键从哪开始」是唯一可靠的边界。
func frontmatterNameValueSpan(fmBody string) (start, end int, ok bool) {
	closeIdx := strings.Index(fmBody, "\n---")
	if closeIdx < 0 {
		closeIdx = len(fmBody)
	}
	block := fmBody[:closeIdx]

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(block), &doc); err != nil {
		return 0, 0, false
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return 0, 0, false
	}
	mapping := doc.Content[0]

	// yaml.Node 的行号从 1 开始。
	nameLine, nextKeyLine := 0, 0
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		if key.Value != "name" {
			continue
		}
		nameLine = key.Line
		if i+2 < len(mapping.Content) {
			nextKeyLine = mapping.Content[i+2].Line
		}
		break
	}
	if nameLine == 0 {
		return 0, 0, false
	}

	lines := strings.Split(block, "\n")
	lastLine := len(lines)
	if nextKeyLine > 0 {
		lastLine = nextKeyLine - 1
	}
	if lastLine > len(lines) {
		lastLine = len(lines)
	}
	// 位于本条目与下一个键之间的空行与无缩进注释不属于该值：块标量内容必须缩进，
	// 所以无缩进的 `#` 只可能是注释。把它们留在原地，而不是吞进替换内容。
	for lastLine > nameLine {
		line := strings.TrimSuffix(lines[lastLine-1], "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			lastLine--
			continue
		}
		break
	}
	if lastLine < nameLine {
		return 0, 0, false
	}

	offset := 0
	for i, line := range lines {
		if i == nameLine-1 {
			start = offset
		}
		if i == lastLine-1 {
			return start, offset + len(line), true
		}
		offset += len(line) + 1 // +1 是补偿 Split 消耗掉的换行
	}
	return 0, 0, false
}

// setFrontmatterName 把顶层 frontmatter 的 `name` 条目——键加完整值、占多少行算
// 多少——替换成单行 `name: <slug>`，content 其余每个字节都不动。fmStart 是 YAML
// 正文开始处的偏移（见 frontmatterBodyStart）。
//
// verified 报告结果是否经过重新解析且 `name` 真的是 slug。调用方必须把 false 当作
// 「不要用这份输出」：目录 == frontmatter-name 不变量正是本函数存在的全部理由，一次
// 无法证明自身的改写比不改更糟。
func setFrontmatterName(content string, fmStart int, slug string) (result string, verified bool) {
	fmBody := content[fmStart:]
	start, end, ok := frontmatterNameValueSpan(fmBody)
	if !ok {
		return "", false
	}
	replacement := "name: " + slug
	// strings.Split 按 "\n" 切分时，CRLF 结尾的 "\r" 会留在行内；把它带上，别让块
	// 结束处混用两种换行符。
	if strings.HasSuffix(fmBody[start:end], "\r") {
		replacement += "\r"
	}
	rewritten := content[:fmStart+start] + replacement + content[fmStart+end:]
	if !frontmatterNameIs(rewritten, slug) {
		return "", false
	}
	return rewritten, true
}

// renameFrontmatterNameViaNode 从「已解析的 YAML 节点」重建 frontmatter 块并把
// `name` 设成 slug，返回重装好的文档。
//
// 这是「手术式字节改写」与「整块重新合成」之间的中间路线。它放弃原块的精确保留格式
// ——重新 marshal 会规整引号与缩进——但保住每个键与值，而重新合成保不住。这个差别
// 不止于整洁：synthesizeFrontmatter 只输出 name 与 description，于是带
// `disable-model-invocation: true` 的块回来时会丢掉它——技能一旦落盘，会被运行时
// 的原生发现广而告之，尽管作者把它藏了起来。
//
// name 值上的锚点两边都要小心。直接丢弃会让引用它的任何 `*alias` 悬空、使文档非法。
// 直接保留更糟：每个别名于是都解析到 slug，一份「复用 name 的值表达某设置」的文档
// 会悄悄换成另一设置。`disable-model-invocation: *shared` 配 `name: &shared "true"`
// 会从 true 翻成 false——与整键丢弃相同的技能泄露回归，只是走了另一条路。
//
// 所以别名先「物化」成它们当前解析到的值，再做改名；随后锚点因无人引用而被移除。
// 每个字段都保留原有值；只有 `name` 变化。
func renameFrontmatterNameViaNode(content, slug string) (string, bool) {
	fmBody, body, ok := frontmatterParts(content)
	if !ok {
		return "", false
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(fmBody), &doc); err != nil {
		return "", false
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", false
	}

	mapping := doc.Content[0]
	var value *yaml.Node
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "name" {
			value = mapping.Content[i+1]
			break
		}
	}
	if value == nil {
		return "", false
	}

	// 趁改名还没改变它们指向的值，先把每个别名钉在它当前解析到的值上。先做这一步，
	// 好让克隆捕捉到原始值而不是 slug。
	if value.Anchor != "" {
		materializeAliasesOf(&doc, value)
	}

	// 把原值（标量、flow 序列……）塌缩成纯字符串。锚点一并带走：已经没人引用它。
	value.Kind = yaml.ScalarNode
	value.Tag = "!!str"
	value.Style = 0
	value.Value = slug
	value.Content = nil
	value.Anchor = ""

	marshaled, err := yaml.Marshal(&doc)
	if err != nil {
		return "", false
	}
	rebuilt := "---\n" + string(marshaled) + "---\n" + body
	if !frontmatterNameIs(rebuilt, slug) {
		return "", false
	}
	return rebuilt, true
}

// materializeAliasesOf 把 root 下每个「指向 target 的别名节点」替换成 target 当前值
// 的内联副本，使这些字段在 target 被改写后仍保有该值。
func materializeAliasesOf(root, target *yaml.Node) {
	for _, child := range root.Content {
		if child.Kind == yaml.AliasNode && child.Alias == target {
			// 每个别名拿自己的一份副本；共享同一节点会让编码器重新引入锚点/别名对。
			*child = *cloneYAMLNode(target)
			continue
		}
		materializeAliasesOf(child, target)
	}
}

// cloneYAMLNode 深拷贝一个节点，剥离锚点/别名身份，使副本以内联形式而非引用形式
// 输出。
func cloneYAMLNode(n *yaml.Node) *yaml.Node {
	clone := *n
	clone.Anchor = ""
	clone.Alias = nil
	if len(n.Content) > 0 {
		clone.Content = make([]*yaml.Node, len(n.Content))
		for i, child := range n.Content {
			clone.Content[i] = cloneYAMLNode(child)
		}
	}
	return &clone
}

// frontmatterNameIs 报告 content 的 frontmatter 能否解析、且其顶层 `name` 恰好等于
// want。
func frontmatterNameIs(content, want string) bool {
	fmBody, _, ok := frontmatterParts(content)
	if !ok {
		return false
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte(fmBody), &m); err != nil {
		return false
	}
	name, _ := m["name"].(string)
	return name == want
}

// yamlEscapeInline 返回恒能解析成字符串的双引号 YAML 标量。刻意避开纯标量：像
// `[foo]`、`{x: y}`、`false`、`null` 或 `2024-01-01` 这样的值在 YAML 1.2 下会解析成
// flow 序列、flow 映射、布尔、null 或时间戳，而 OpenCode 的 frontmatter 检查会直接
// 拒绝非字符串的 description。这里把换行拍平（frontmatter 每个键单行）并转义 `\` 与
// `"`，使任意输入都是安全的内联字符串。
func yamlEscapeInline(s string) string {
	flat := strings.ReplaceAll(s, "\r\n", " ")
	flat = strings.ReplaceAll(flat, "\n", " ")
	flat = strings.ReplaceAll(flat, "\r", " ")
	escaped := strings.ReplaceAll(flat, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// sanitizeSkillName 把技能名转成安全的目录名。
func sanitizeSkillName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = nonAlphaNum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "skill"
	}
	return s
}

// writeSkillFiles 把技能目录写进给定父目录。每个技能一个子目录，内含 SKILL.md 与
// 附属文件。manifest 非 nil 时，会记下每个新建目录与文件，使 CleanupSidecars 在
// local_directory 拆除时能移除它们，而不碰「恰好与我们的技能同处一个 skills/ 父目录」
// 的用户自有技能目录。
//
// 当某个 Multica 技能的自然 slug 与同路径上用户安装的技能撞名时，分配一个无冲突的
// 兄弟 slug（如 `issue-review-multica`）写在那里。provider 原生发现仍会拾取它——因为
// skillsDir 下每个子目录都是独立技能；用户原目录则逐字节完好。没有这个回退，
// writeSkillFiles 要么覆盖用户字节（PR #3444 review 抓到的 bug），要么整个跳过技能
// （静默丢掉 agent 期待看到的 Multica 技能）。
func writeSkillFiles(skillsDir string, skills []SkillContextForEnv, manifest *sidecarManifest) error {
	if err := recordMkdirAll(skillsDir, 0o755, manifest); err != nil {
		return fmt.Errorf("create skills dir: %w", err)
	}

	// resolveSkillSlugs 先在批次内去重，使两个名字清洗后相同（"A B" / "A-B"）的技能
	// 在这里就拿到不同的基底，而不是抢同一个目录。列目录用的同一个函数，正因如此
	// 才保证它们命名的正是本循环创建的目录。allocateCollisionFreeSkillDir 仍在其上再
	// 兜一层，应对「与不是我们写的目录（用户安装的技能）」的冲突。
	batchSlugs := resolveSkillSlugs(skills)

	for i, skill := range skills {
		slug, dir, err := allocateCollisionFreeSkillDir(skillsDir, batchSlugs[i])
		if err != nil {
			return fmt.Errorf("allocate skill dir for %q: %w", skill.Name, err)
		}
		if err := recordMkdirAll(dir, 0o755, manifest); err != nil {
			return err
		}

		// ensureSkillFrontmatter 在上游技能缺 `name:` 时合成一个。使用最终选定的
		// slug（撞名时可能与 baseSlug 不同），让 YAML name 与目录名一致；依赖二者
		// 任一作键的运行时都保持一致。
		body := ensureSkillFrontmatter(skill.Content, slug, skill.Description)
		if err := recordSkillFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644, manifest); err != nil {
			return err
		}

		// 写附属文件。技能目录按构造已无冲突，所以 recordWriteFile 在它之下的撞名
		// 只会是「技能自带的文件清单里同一路径列了两份」——这是上游数据 bug，不是
		// 用户内容冲突，要暴露出来。
		//
		// 一个常见的数据 bug 是：把 SKILL.md 同时作为主内容（skill.Content）与一份
		// 附属文件存着。跳过这份重复，让 agent 仍拿到每个唯一文件。该检查是规范形式
		//（见 skillpkg.IsReservedContentPath），所以像 "./SKILL.md" 这类非规范拼写——
		// filepath.Join 会解析到同一个 dir/SKILL.md——也会被抓到，而不是撞上
		// errPathPreExists 让准备失败。
		for _, f := range skill.Files {
			if skillpkg.IsReservedContentPath(f.Path) {
				continue
			}
			if !skillpkg.SafePath(f.Path) {
				return fmt.Errorf("unsafe skill file path: %s", f.Path)
			}
			fpath := filepath.Join(dir, f.Path)
			if err := recordMkdirAll(filepath.Dir(fpath), 0o755, manifest); err != nil {
				return err
			}
			data := []byte(f.Content)
			if f.Data != nil {
				data = f.Data
			}
			if err := recordSkillFile(fpath, data, os.FileMode(skillpkg.FileMode(f.Mode)), manifest); err != nil {
				return err
			}
		}
	}

	return nil
}

// renderIssueContext 构建 issue_context.md 的 markdown 内容。
func renderIssueContext(provider string, ctx TaskContextForEnv) string {
	if ctx.AutopilotRunID != "" {
		return renderAutopilotContext(ctx)
	}
	if ctx.QuickCreatePrompt != "" {
		return renderQuickCreateContext(ctx)
	}

	var b strings.Builder

	b.WriteString("# Task Assignment\n\n")
	fmt.Fprintf(&b, "**Issue ID:** %s\n\n", ctx.IssueID)

	if ctx.TriggerCommentID != "" {
		b.WriteString("**Trigger:** Comment Reply\n")
		b.WriteString("**Triggering comment ID:** `" + ctx.TriggerCommentID + "`\n\n")
	} else {
		b.WriteString("**Trigger:** New Assignment\n\n")
	}

	// 交接班说明（MUL-3375）：指派人对本轮的范围指令。与评论不同——没有可回复的
	// 线程。
	if ctx.HandoffNote != "" {
		b.WriteString("## Handoff Note\n\n")
		b.WriteString("The person who assigned this issue left this instruction for the run. Treat it as scope guidance and follow it before doing anything broader:\n\n")
		fmt.Fprintf(&b, "> %s\n\n", ctx.HandoffNote)
	}

	b.WriteString("## Quick Start\n\n")
	fmt.Fprintf(&b, "Run `multica issue get %s --output json` to fetch the full issue details.\n\n", ctx.IssueID)

	return b.String()
}

// renderQuickCreateContext 为 quick-create 任务渲染 issue_context.md。此文件只携带
// 任务数据（用户输入）。行为规则与护栏放在 AGENTS.md（运行时配置）与每轮 prompt 中，
// 以免冗余与指令冲突；技能索引与其它一切类型一样放在运行时 brief 里（MUL-5529）。
func renderQuickCreateContext(ctx TaskContextForEnv) string {
	var b strings.Builder
	b.WriteString("# Quick Create\n\n")
	b.WriteString("**Trigger:** Quick-create modal\n\n")
	b.WriteString("## User input\n\n")
	b.WriteString("> ")
	b.WriteString(ctx.QuickCreatePrompt)
	b.WriteString("\n\n")
	return b.String()
}

// renderAutopilotContext 为 autopilot run 任务渲染 issue_context.md：只装 autopilot
// 本体数据与指令；由于没有 issue 可 fetch，Quick Start 也相应调整。
func renderAutopilotContext(ctx TaskContextForEnv) string {
	var b strings.Builder

	b.WriteString("# Autopilot Run\n\n")
	fmt.Fprintf(&b, "**Autopilot run ID:** %s\n\n", ctx.AutopilotRunID)
	if ctx.AutopilotID != "" {
		fmt.Fprintf(&b, "**Autopilot ID:** %s\n\n", ctx.AutopilotID)
	}
	if ctx.AutopilotTitle != "" {
		fmt.Fprintf(&b, "**Title:** %s\n\n", ctx.AutopilotTitle)
	}
	if ctx.AutopilotSource != "" {
		fmt.Fprintf(&b, "**Trigger source:** %s\n\n", ctx.AutopilotSource)
	}
	if ctx.AutopilotTriggerPayload != "" {
		fmt.Fprintf(&b, "## Trigger Payload\n\n```json\n%s\n```\n\n", ctx.AutopilotTriggerPayload)
	}

	b.WriteString("## Quick Start\n\n")
	b.WriteString("This is a run-only autopilot task with no assigned issue. Do not run `multica issue get` unless the autopilot instructions explicitly ask you to create or update an issue.\n\n")
	if ctx.AutopilotID != "" {
		fmt.Fprintf(&b, "Run `multica autopilot get %s --output json` if you need the full autopilot configuration.\n\n", ctx.AutopilotID)
	}
	if strings.TrimSpace(ctx.AutopilotDescription) != "" {
		b.WriteString("## Autopilot Instructions\n\n")
		b.WriteString(ctx.AutopilotDescription)
		b.WriteString("\n\n")
	}

	return b.String()
}
