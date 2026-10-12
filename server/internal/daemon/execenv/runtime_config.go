// 运行时配置文件（CLAUDE.md / AGENTS.md / CODEBUDDY.md 等）的注入与清理：
// 把 Multica 生成的运行时 brief 写进 agent 原生会读取的配置文件，同时绝不
// 破坏用户自己写在同一文件里的内容，并能在任务结束后精确回滚到注入前的
// 原始字节。

package execenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// runtimeMarkerBegin 与 runtimeMarkerEnd 是运行时配置文件里"Multica 托管
// brief"区块的起止标记。标记存在的目的，是让 writeRuntimeConfigFile 能：
//
//   - 保留同一文件里用户撰写的内容（当 agent 被指向 local_directory 项目
//     资源时，用户的仓库可能本来就带着一份 CLAUDE.md / AGENTS.md），
//   - 在同一工作目录的后续运行中幂等地替换 brief，而不是越堆越多的重复副本，
//   - 为将来的清理流程留下一个精确的切除目标。
//
// 选用 HTML 注释形式，使标记在任何 Markdown 渲染器里都惰性无害、被当作指令
// 喂给 agent 也无妨。改动标记文本对任何已携带旧标记的文件都是一次破坏性
// 变更——要改请刻意为之。
const (
	runtimeMarkerBegin = "<!-- BEGIN MULTICA-RUNTIME (auto-managed; do not edit) -->"
	runtimeMarkerEnd   = "<!-- END MULTICA-RUNTIME -->"

	// runtimeManagedSeparator 是 Inject 每次向"已存在文件"追加时，插在既有
	// 用户内容与标记区块之间的固定分隔符。分隔符算作托管区域的一部分：
	// Cleanup 会把分隔符连同区块一起剥离，于是无论用户文件原本以"无换行、
	// 一个换行还是多个尾随换行"结尾，文件都能精确回滚到注入前的字节。
	// 没有定宽分隔符的话，清理路径就不得不重新归一化用户的尾随字节，
	// 每次运行都会留下一处细微但真实的 diff（见 PR #3438 的 MUL-2753 review）。
	//
	// Cleanup 靠它区分两种状态："文件是我们创建的"（区块前没有托管分隔符——
	// 原本缺文件，从零写出）与"文件本来就有"（区块前有托管分隔符），从而在
	// 注入→清理的完整周期里精确保留文件的存在性——包括原本为空或只有空白
	// 的已存在文件。
	runtimeManagedSeparator = "\n\n"
)

// runtimeGOOS 是 buildMetaSkillContent 与 BuildCommentReplyInstructions 用来
// 输出 Windows 特有指引的主机平台字符串。默认取 runtime.GOOS；测试通过覆盖
// 它来确定性走各跨平台分支，而无需真的在每种目标 OS 上跑。
var runtimeGOOS = runtime.GOOS

// sanitizeNameForBriefMarkdown 把可能含多行的显示名压成"单行、纯文本"令牌，
// 使其可以安全嵌进 agent brief 的 markdown 行内结构（如 `**%s**`）。brief 是
// 被当作可信指令加载的，因此用户可控的姓名字段绝不能引入标题、列表或闭合
// 周围加粗跨度。
//
// CR/LF 与其他空白控制字节塌缩成单个空格；其余 C0 控制字符与 DEL 直接丢弃；
// 在行内上下文有结构含义的 markdown 字符（`*`、`_`、“ ` “、`\`、`[`、`]`、
// `<`）用反斜杠转义。末尾空白被修剪。
func sanitizeNameForBriefMarkdown(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	prevSpace := false
	for _, r := range name {
		switch {
		case r == '\r' || r == '\n' || r == '\t' || r == '\v' || r == '\f':
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
				prevSpace = true
			}
		case r < 0x20 || r == 0x7f:
			continue
		case r == '*' || r == '_' || r == '`' || r == '\\' || r == '[' || r == ']' || r == '<':
			b.WriteByte('\\')
			b.WriteRune(r)
			prevSpace = false
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

// sanitizeEmailForBrief 当邮件地址可安全嵌进 brief 行内时原样返回；若带有
// 真实地址绝不会有的字符（空白、控制字符或 markdown 截断风险）则返回 ""。
// 与 sanitizeNameForBriefMarkdown 不同，它"不"用反斜杠转义 markdown 特殊
// 字符：agent 可能要逐字符匹配发起人的地址，转义 `_`/`+` 反而会破坏它；而
// 合法邮箱不可能含换行，也就无法注入标题。邮箱在注册时已被校验，所以这里
// 只是纵深防御，而非主要防线。参见 MUL-2645。
func sanitizeEmailForBrief(email string) string {
	email = strings.TrimSpace(email)
	if email == "" || !strings.Contains(email, "@") {
		return ""
	}
	for _, r := range email {
		if r < 0x20 || r == 0x7f || r == ' ' || r == '\\' || r == '`' || r == '*' || r == '<' || r == '>' || r == '[' || r == ']' {
			return ""
		}
	}
	return email
}

// formatProjectResource 把单个资源渲染成人类可读的项目符号条目。未知资源
// 类型回退为 JSON 编码的 ref，让 agent 仍能读到用户挂了什么。新增资源类型
// 要同时改这里与 API 校验器（handler/project_resource.go）。
func formatProjectResource(r ProjectResourceForEnv) string {
	label := r.Label
	switch r.ResourceType {
	case "github_repo":
		var payload struct {
			URL               string `json:"url"`
			DefaultBranchHint string `json:"default_branch_hint,omitempty"`
			Ref               string `json:"ref,omitempty"`
		}
		_ = json.Unmarshal(r.ResourceRef, &payload)
		out := fmt.Sprintf("**GitHub repo**: %s", payload.URL)
		details := make([]string, 0, 2)
		if payload.Ref != "" {
			details = append(details, fmt.Sprintf("checkout ref: `%s`", payload.Ref))
		}
		if payload.DefaultBranchHint != "" {
			details = append(details, fmt.Sprintf("default branch hint: `%s`", payload.DefaultBranchHint))
		}
		if len(details) > 0 {
			out += " (" + strings.Join(details, ", ") + ")"
		}
		if label != "" {
			out += " — " + label
		}
		return out
	default:
		ref := string(r.ResourceRef)
		if ref == "" {
			ref = "{}"
		}
		out := fmt.Sprintf("**%s**: `%s`", r.ResourceType, ref)
		if label != "" {
			out += " — " + label
		}
		return out
	}
}

// InjectRuntimeConfig 把 meta skill 内容写进"运行时特有"的配置文件，让 agent
// 通过它的原生机制发现自身环境。
//
// Claude:     写 {workDir}/CLAUDE.md（技能从 .claude/skills/ 原生发现）
// CodeBuddy:  写 {workDir}/CODEBUDDY.md（CodeBuddy 的原生记忆文件名；技能从 .codebuddy/skills/ 原生发现）
// Codex:      写 {workDir}/AGENTS.md（技能经 CODEX_HOME 原生发现）
// Copilot:    写 {workDir}/AGENTS.md（技能从 .github/skills/ 原生发现）
// OpenCode:   写 {workDir}/AGENTS.md（技能从 .opencode/skills/ 原生发现）
// CodeArts:   写 {workDir}/AGENTS.md（技能从 .codeartsdoer/skills/ 原生发现）
// DevEco Code:写 {workDir}/AGENTS.md（技能从 .deveco/skills/ 原生发现）
// Hermes:     写 {workDir}/AGENTS.md（技能从 daemon 播种的每任务 HERMES_HOME/skills 原生发现；见 hermes_home.go）
// Pi:         写 {workDir}/AGENTS.md（技能从 .pi/skills/ 原生发现）
// Oh-My-Pi(omp): 写 {workDir}/AGENTS.md（omp 是 pi 的分支；技能从 .omp/skills/ 发现）
// Cursor:     写 {workDir}/AGENTS.md（技能从 .cursor/skills/ 原生发现）
// Kimi:       写 {workDir}/AGENTS.md（Kimi Code CLI 原生读 AGENTS.md；技能从项目技能目录自动发现）
// Reasonix:   写 {workDir}/AGENTS.md（Reasonix 原生读 AGENTS.md 与 .reasonix/skills/）
// DSH:        写 {workDir}/AGENTS.md（DSH 原生读 AGENTS.md 与 .dsh/skills/）
// Kiro:       写 {workDir}/AGENTS.md（Kiro CLI 原生读 AGENTS.md；技能从项目技能目录自动发现）
// Qoder/Qoder CN: 写 {workDir}/AGENTS.md（技能从 .qoder/skills/ 发现；用户级根不受影响）
// Antigravity:写 {workDir}/AGENTS.md（agy CLI 原生读 AGENTS.md；技能从 .agents/skills/ 原生发现——见 https://antigravity.google/docs/gcli-migration）
// Traecli:    写 {workDir}/AGENTS.md（traecli 读 .trae/rules/ 而非 AGENTS.md，所以 brief 经 providerNeedsInlineSystemPrompt 内联投递；写文件仅为对齐/可见性）
// Grok:       写 {workDir}/AGENTS.md（Grok Build CLI 从工作目录原生读 AGENTS.md）
// Qwen:       写 {workDir}/QWEN.md（Qwen Code 的原生上下文文件；它也读 AGENTS.md，但用 QWEN.md 可避开跨运行时歧义）
func InjectRuntimeConfig(workDir, provider string, ctx TaskContextForEnv) (string, error) {
	content := buildMetaSkillContent(provider, ctx)
	path := runtimeConfigPath(workDir, provider)
	if path == "" {
		// 未知 provider——跳过配置注入，仅走提示词内联模式。
		return content, nil
	}
	return content, writeRuntimeConfigFile(path, content)
}

// runtimeConfigPath 返回 InjectRuntimeConfig 为给定 provider 写入的运行时
// 配置文件的绝对路径；provider 没有"基于文件"的配置目标时返回 ""。把映射
// 集中到一处能让 Inject / Cleanup 保持步调一致——两侧查询同一张表，新增到
// 某一侧的 provider 就不可能漂移到另一侧之外。
func runtimeConfigPath(workDir, provider string) string {
	// 内置运行时身份（如 "omp"）从其协议家族继承配置文件——先从描述符解析
	// 出家族，再委托给家族的 switch case。这样就不必为每个描述符硬编码
	// "AGENTS.md"；否则一个兼容 Claude、CodeBuddy 或 Qwen 的运行时就会
	// 写错文件。
	if desc, ok := agent.BuiltinRuntimeByID(provider); ok {
		return runtimeConfigPath(workDir, desc.ProtocolFamily)
	}
	switch provider {
	case "claude":
		return filepath.Join(workDir, "CLAUDE.md")
	case "codebuddy":
		// CodeBuddy Code 的原生记忆文件是 CODEBUDDY.md 而非 CLAUDE.md——
		// 见 https://www.codebuddy.ai/docs/cli/codebuddy-dir
		// （"CODEBUDDY.md / .codebuddy/CODEBUDDY.md — Project-level memory
		// file"）。只有当用户手动迁移/软链进去时 CodeBuddy 才会读 CLAUDE.md。
		return filepath.Join(workDir, "CODEBUDDY.md")
	case "qwen":
		return filepath.Join(workDir, "QWEN.md")
	case "codex", "copilot", "opencode", "codearts", "deveco", "hermes", "pi", "cursor", "kimi", "reasonix", "dsh", "kiro", "antigravity", "qoder", "qoderclicn", "traecli", "grok", "qwenpaw", "mcode", "dim", "zeroclaw":
		return filepath.Join(workDir, "AGENTS.md")
	default:
		return ""
	}
}

// writeRuntimeConfigFile 把 Multica 运行时 brief 写入 path，且不清掉文件中
// 已有的用户内容。按文件状态的行为：
//
//   - 文件缺失 → 创建只含标记区块的文件，前面没有分隔符。Cleanup 依据
//     "分隔符缺失"识别出此状态，直接删除文件以还原"文件不存在"。
//   - 文件存在（任意内容，包括空），但无标记区块 → 追加
//     `<runtimeManagedSeparator>` + 标记区块。分隔符字节属于托管区域，
//     因此 Cleanup 能精确还原用户注入前的字节（不归一化尾随换行——无论
//     原本无换行结尾还是带多余尾随换行，都不会有意外的 diff）。
//   - 文件存在且已有标记区块 → 原地替换两标记之间的正文，使同一工作目录
//     的重复运行不会让文件无限增长。区块前的内容（含首次注入建立的托管
//     分隔符）原样保留。
//
// 之前的实现无条件调用 os.WriteFile，导致第一次经 local_directory 项目
// 资源流程把 agent 指向用户自己的目录时，会静默截断仓库里的 CLAUDE.md /
// AGENTS.md。见 MUL-2753。
func writeRuntimeConfigFile(path, brief string) error {
	block := runtimeMarkerBegin + "\n" + strings.TrimRight(brief, "\n") + "\n" + runtimeMarkerEnd + "\n"

	existing, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return os.WriteFile(path, []byte(block), 0o644)
	}
	if err != nil {
		return fmt.Errorf("read existing runtime config %s: %w", path, err)
	}

	existingStr := string(existing)
	if start, end, ok := locateMarkerBlock(existingStr); ok {
		// 原地替换既有区块。locateMarkerBlock 已把上一个区块收尾的换行一起
		// 吞掉，所以连续运行不会在区块周围累积空行。托管分隔符（若有）位于
		// existingStr[:start]，原样保留不受影响。
		newContent := existingStr[:start] + block + existingStr[end:]
		return os.WriteFile(path, []byte(newContent), 0o644)
	}

	// 没有标记区块。先追加定宽托管分隔符再加区块。分隔符是无条件的——包括
	// 那些本来就以两个以上换行结尾的文件——因此用户内容与托管区域之间的
	// 字节边界是确定的，这正是 Cleanup 能回滚到用户精确原始字节的根据。
	return os.WriteFile(path, []byte(existingStr+runtimeManagedSeparator+block), 0o644)
}

// locateMarkerBlock 在 content 中定位 Multica 标记区块的 [start, end) 字节
// 区间。返回的 end 越过区块的尾随换行（若有），使调用方切除区块后不会留下
// 一行孤立的空行。
//
// 结束标记被"严格在开始标记之后"搜索。这对两种旧实现用朴素 strings.Index
// 会处理错的畸形情况很关键：
//
//   - 用户内容在开始标记之前就带着一个游离的 `<!-- END MULTICA-RUNTIME -->`
//     （例如某段展示线格式的文档片段）。朴素解析器会先找到那个 end 并判定
//     区块非法（endIdx > startIdx 不成立），然后追加新块——而游离的 end 仍在
//     原位，于是之后的每次运行都会再追加一块，文件无限增长。
//   - 某次先前运行在写完 begin 与 end 之间崩溃，留下半个区块。朴素解析器
//     找不到 end，落入追加分支，在半块后面再叠一个新块。把"找到 begin 但
//     其后无 end"视为"区块直到 EOF"则能让下一次写入原地替换那半个区块。
func locateMarkerBlock(content string) (start, end int, found bool) {
	start = strings.Index(content, runtimeMarkerBegin)
	if start < 0 {
		return 0, 0, false
	}
	afterBegin := start + len(runtimeMarkerBegin)
	endRel := strings.Index(content[afterBegin:], runtimeMarkerEnd)
	if endRel < 0 {
		// 畸形——begin 之后没有 end。把文件其余部分都当作区块，下次写入
		// 就能干净地替换它，而不是在半块下面再叠一块。
		return start, len(content), true
	}
	end = afterBegin + endRel + len(runtimeMarkerEnd)
	if end < len(content) && content[end] == '\n' {
		end++
	}
	return start, end, true
}

// CleanupRuntimeConfig 从指定 provider 的运行时配置文件中切除 Multica 标记
// 区块，并把文件逐字节恢复到注入前的确切状态。清理是 writeRuntimeConfigFile
// 所立契约的后半段：二者合起来必须在任意多次 Multica 运行间对用户的本地
// 仓库配置做无损往返，绝不动任何一个非托管字节。
//
// 行为与三种 Inject 状态一一对应：
//
//   - 文件无标记区块 → 无操作（这里从未注入过任何东西）；
//   - 区块位于文件开头、且前面没有托管分隔符 → 该文件由 Inject 从"文件
//     缺失"状态创建。直接删除文件，使清理后的目录清单与注入前逐字节一致。
//   - 区块前有定宽托管分隔符 → 把分隔符与区块一起剥掉；剩下的（可能是
//     一个原本为空的已存在文件、纯空白文件或任意用户内容）就是用户的原始
//     文件，原样写回——不做尾随换行归一化、也不用 TrimSpace 式的文件删除
//     启发。这两者在 PR #3438 的 review 反馈里都是细微 diff 的来源。
//
// local_directory 流程（WorkDir 是用户自己的仓库）必须做这次清理：没有它，
// 用户在 Multica 任务结束后于同一目录手动起一个 `claude` / `codex` 运行，
// 就会读到那份过期的 brief，并照着上一个任务的 issue id、触发评论 id 与
// 回复规则行动。云端工作区运行不会触发这种污染——其工作目录是 GC 整棵
// 删除的 daemon 暂存区；daemon 在这些工作目录上跳过本次 Cleanup。
//
// 文件缺失、未知 provider、无标记区块的文件都是无操作——Cleanup 可以放心
// 地防御性调用。
func CleanupRuntimeConfig(workDir, provider string) error {
	path := runtimeConfigPath(workDir, provider)
	if path == "" {
		return nil
	}
	existing, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read runtime config %s: %w", path, err)
	}
	existingStr := string(existing)
	start, end, ok := locateMarkerBlock(existingStr)
	if !ok {
		return nil
	}
	pre := existingStr[:start]
	post := existingStr[end:]

	// 检测——并剥掉——Inject 在"向已存在文件追加"时紧贴在区块前的定宽
	// 托管分隔符。"分隔符缺失"即"Inject 是从零创建这个文件"的标记，也是
	// Cleanup 唯一允许删除文件的情形。
	hadManagedSeparator := strings.HasSuffix(pre, runtimeManagedSeparator)
	if hadManagedSeparator {
		pre = pre[:len(pre)-len(runtimeManagedSeparator)]
	}
	remainder := pre + post

	if !hadManagedSeparator && remainder == "" {
		// 文件是 Inject 创建的（无托管分隔符 → 区块是唯一内容）。
		// 还原"文件缺失"状态。
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove runtime config %s: %w", path, err)
		}
		return nil
	}
	// 文件本来就存在（可能为空、可能纯空白、可能带用户内容）——把剩余部分
	// 原样写回、不做任何归一化。此处的 remainder 为空表示用户原文件是空的；
	// 我们仍要写它（零字节文件），以保留文件的存在性。
	return os.WriteFile(path, []byte(remainder), 0o644)
}

// buildMetaSkillContent 生成教会 agent 认识 Multica 运行时环境与可用 CLI
// 工具的 meta skill markdown。
//
// brief 由 buildMetaSkillContentSlim（runtime_config_sections.go）组装，
// 后者应用"按任务类别裁剪区块 + 逐区块正文压缩"。此路径曾由 `runtime_brief_slim`
// 特性开关把"精简 brief"与旧版冗长 brief 隔开；该开关已退役（MUL-4297），
// 现在精简 brief 是唯一路径。
func buildMetaSkillContent(provider string, ctx TaskContextForEnv) string {
	return buildMetaSkillContentSlim(provider, ctx)
}
