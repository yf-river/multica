package daemon

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/internal/skill"
	"github.com/multica-ai/multica/server/pkg/agent"
)

const (
	maxLocalSkillFileSize   int64 = skill.MaxFileBytes
	maxLocalSkillBundleSize int64 = skill.MaxBundleBytes
	// 与服务端导入器的 maxImportFileCount 保持同步，使“从 URL/压缩包导入的技能”也能
	// 从 runtime 本地目录导入。8 MiB 的包上限才是技能大小的真正闸门。
	maxLocalSkillFileCount = skill.MaxFiles
	// 限制技能发现在 runtime 根目录之下最多下钻多少层。opencode 把技能存两层深
	// （例如 `release/reporter/SKILL.md`）；再多几层能覆盖任何现实中的未来布局，同时
	// 万一安装器把我们指向 $HOME 也限制住了工作量。
	maxLocalSkillDirDepth = 4
)

// runtimeLocalSkillSummary 是某个 runtime 本地技能对外可见的摘要。
type runtimeLocalSkillSummary struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	SourcePath  string `json:"source_path"`
	Provider    string `json:"provider"`
	// Root 归类这个技能出自哪个发现根：localSkillRootProvider（"provider"）指 runtime
	// 自己的技能目录（如 ~/.claude/skills），localSkillRootUniversal（"universal"）指
	// 跨工具共享的 ~/.agents/skills 兜底。UI 用它给技能标注来源，并在导入对话框里提示
	// 技能来自“provider 专用”还是“共享”位置。早于多根发现的老 daemon 省略此字段；
	// 服务端把空值当 "unknown"，而不是当成 provider/universal 断言。
	Root       string `json:"root,omitempty"`
	Plugin     string `json:"plugin,omitempty"`
	CanDisable bool   `json:"can_disable,omitempty"`
	FileCount  int    `json:"file_count"`
}

// runtimeLocalSkillBundle 是某个 runtime 本地技能的完整内容（含支撑文件），供导入
// 到服务器用。
type runtimeLocalSkillBundle struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Content     string          `json:"content"`
	SourcePath  string          `json:"source_path"`
	Provider    string          `json:"provider"`
	Files       []SkillFileData `json:"files,omitempty"`
}

// localSkillRoot 是一个发现位置外加“它来自哪类根”的分类。根按优先级顺序由
// localSkillRootsForProvider 返回；kind 会随每个发现的技能暴露给 UI（见
// runtimeLocalSkillSummary.Root）。
type localSkillRoot struct {
	path      string
	kind      string
	keyPrefix string
	plugin    string
}

const (
	// localSkillRootProvider 标记 runtime 自己的技能目录（如 ~/.claude/skills）。
	// 它优先于 universal 根。
	localSkillRootProvider = "provider"
	// localSkillRootUniversal 标记跨工具的 ~/.agents/skills 根——Codex、Gemini CLI、
	// Augment 等共同采用的“用户级通用技能库”约定。它总是最后搜，让 provider 目录里
	// 同 key 的技能继续获胜。
	localSkillRootUniversal = "universal"
	// localSkillRootPlugin 标记由已启用的 runtime 插件贡献的技能。插件根带命名空间
	// 前缀，调用 key 与 Claude Code 对齐（例如 paper-desktop:design-to-code），且绝不
	// 与独立的用户技能撞名。
	localSkillRootPlugin = "plugin"
)

// localSkillRootsForProvider 返回每个 runtime/provider 要扫描的、有序的用户级技能根。
// 切片按优先级排列：
//
//  1. 该 runtime 的 provider 专用根（向后兼容 ~/.agents/skills 支持之前“单根”
//     行为），然后
//  2. 跨工具 universal 根 ~/.agents/skills。
//
// 列表与导入都按此顺序走根，同一技能 key 的“首个匹配”获胜——因此加入 universal 根
// 绝不改变已有 provider 根技能解析出的内容——它只会额外浮现不冲突的技能。
//
// provider 根要与上游文档/约定保持同步：
//   - GitHub Copilot: https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/add-skills
//   - OpenCode: https://opencode.ai/docs/skills
//   - Pi: https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/docs/skills.md
//   - Cursor: 官方论坛指内置 /create-skill 流程的指引
//     (https://forum.cursor.com/t/cursor-doesnt-know-new-skills-arens-saved/158507)
//   - Hermes: ~/.hermes/skills 是 Hermes Agent 的主技能目录
//     (https://hermes-agent.nousresearch.com/docs/user-guide/features/skills)
//   - Kimi: ~/.kimi/skills 镜像 Kimi CLI 的项目级 .kimi/skills 布局
//   - Kiro: Kiro CLI 发现的项目级与用户级 .kiro/skills 目录
//   - Qoder: ~/.qoder/skills 镜像 Qoder CLI 的项目级 .qoder/skills 布局
//   - Antigravity: ~/.gemini/antigravity-cli/skills 用户级技能根
//     (https://antigravity.google/docs/gcli-migration 的 "Global skills")
//   - Grok: $GROK_HOME/skills，缺省 ~/.grok/skills
//   - Qwen Code: $QWEN_HOME/skills，缺省 ~/.qwen/skills
//
// 通用 ~/.agents/skills 根被 Codex (https://developers.openai.com/codex/skills) 与
// Gemini CLI (https://geminicli.com/docs/cli/skills/) 等文档化为跨工具技能位置。
//
// 长期看这个映射最好与 server/pkg/agent 下的 provider 定义放一起，那样新增一个 runtime
// 就不会悄悄漏掉本地技能这一表面。
func localSkillRootsForProvider(provider string) ([]localSkillRoot, bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false, fmt.Errorf("resolve user home: %w", err)
	}

	var providerRoot string
	// 内置 runtime 身份（如 "omp"）在描述符里声明了它们的用户技能目录；解析成
	// providerRoot 后落到下面的公共构造逻辑，让 universal 根、合并与导入兜底仍然全部
	// 适用——与所有协议族 provider 相同。
	if desc, ok := agent.BuiltinRuntimeByID(provider); ok {
		providerRoot = filepath.Join(home, desc.UserSkillsDir)
	} else {
		switch provider {
		case "claude":
			providerRoot = filepath.Join(home, ".claude", "skills")
		case "codebuddy":
			// CodeBuddy Code 是 Claude Code 的分支，但自带原生的配置目录；它*不*读
			// ~/.claude/skills，除非用户手动符号链接进来（厂商文档里给 Claude Code
			// 的迁移路径就是如此）。见
			// https://www.codebuddy.ai/docs/cli/codebuddy-dir（"Global directory
			// ~/.codebuddy/"）与
			// https://www.codebuddy.ai/docs/cli/skills（"User-level Skills:
			// ~/.codebuddy/skills/"）。
			providerRoot = filepath.Join(home, ".codebuddy", "skills")
		case "codex":
			codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
			if codexHome == "" {
				codexHome = filepath.Join(home, ".codex")
			}
			providerRoot = filepath.Join(codexHome, "skills")
		case "copilot":
			providerRoot = filepath.Join(home, ".copilot", "skills")
		case "opencode":
			providerRoot = filepath.Join(home, ".config", "opencode", "skills")
		case "codearts":
			providerRoot = filepath.Join(home, ".codeartsdoer", "skills")
		case "deveco":
			providerRoot = filepath.Join(home, ".config", "deveco", "skills")
		case "pi":
			providerRoot = filepath.Join(home, ".pi", "agent", "skills")
		case "cursor":
			providerRoot = filepath.Join(home, ".cursor", "skills")
		case "hermes":
			providerRoot = filepath.Join(home, ".hermes", "skills")
		case "kimi":
			providerRoot = filepath.Join(home, ".kimi", "skills")
		case "reasonix":
			reasonixHome := strings.TrimSpace(os.Getenv("REASONIX_HOME"))
			if reasonixHome == "" {
				reasonixHome = filepath.Join(home, ".reasonix")
			}
			providerRoot = filepath.Join(reasonixHome, "skills")
		case "dsh":
			dshHome := strings.TrimSpace(os.Getenv("DSH_HOME"))
			if dshHome == "" {
				dshHome = filepath.Join(home, ".dsh")
			}
			providerRoot = filepath.Join(dshHome, "skills")
		case "kiro":
			providerRoot = filepath.Join(home, ".kiro", "skills")
		case "qoder":
			providerRoot = filepath.Join(home, ".qoder", "skills")
		case "qoderclicn":
			providerRoot = filepath.Join(home, ".qoder-cn", "skills")
		case "traecli":
			// 官方 TRAE CLI 的全局技能在 ~/.traecli/skills。见
			// https://docs.trae.cn/cli_skills
			providerRoot = filepath.Join(home, ".traecli", "skills")
		case "antigravity":
			// agy 继承 Gemini CLI 的全局技能根；见
			// https://antigravity.google/docs/gcli-migration（"Global skills"）。
			providerRoot = filepath.Join(home, ".gemini", "antigravity-cli", "skills")
		case "grok":
			// GROK_HOME 取代缺省的 ~/.grok home，管理设置、会话与用户级技能。
			grokHome := strings.TrimSpace(os.Getenv("GROK_HOME"))
			if grokHome == "" {
				grokHome = filepath.Join(home, ".grok")
			}
			providerRoot = filepath.Join(grokHome, "skills")
		case "qwen":
			// QWEN_HOME 取代 Qwen Code 的全局 ~/.qwen 目录。它管理设置、会话、凭据与
			// 个人技能；项目级 .qwen/skills 仍根植于任务 workdir。
			qwenHome := strings.TrimSpace(os.Getenv("QWEN_HOME"))
			if qwenHome == "" {
				qwenHome = filepath.Join(home, ".qwen")
			}
			providerRoot = filepath.Join(qwenHome, "skills")
		case "qwenpaw":
			// QWENPAW_WORKING_DIR（或旧名 COPAW_WORKING_DIR）覆盖 QwenPaw 的全局
			// ~/.qwenpaw 目录，后者管理设置、会话、凭据与个人技能。runtime 按
			// QWENPAW_WORKING_DIR → COPAW_WORKING_DIR → ~/.copaw（旧）→ ~/.qwenpaw
			// （缺省）的顺序解析根。见 QwenPaw 源码里的 constant.py。
			qwenpawHome := strings.TrimSpace(os.Getenv("QWENPAW_WORKING_DIR"))
			if qwenpawHome == "" {
				qwenpawHome = strings.TrimSpace(os.Getenv("COPAW_WORKING_DIR"))
			}
			if qwenpawHome == "" {
				legacyCopaw := filepath.Join(home, ".copaw")
				if _, err := os.Stat(legacyCopaw); err == nil {
					qwenpawHome = legacyCopaw
				} else {
					qwenpawHome = filepath.Join(home, ".qwenpaw")
				}
			}
			providerRoot = filepath.Join(qwenpawHome, "skill_pool")
		case "mcode":
			// MCode 的缺省数据目录是 ~/.minimax；全局技能直接在它下面。项目技能单独
			// 注入到 <workDir>/.minimax/skills。
			providerRoot = filepath.Join(home, ".minimax", "skills")
		default:
			return nil, false, nil
		}
	}

	roots := []localSkillRoot{
		{path: providerRoot, kind: localSkillRootProvider},
		{path: filepath.Join(home, ".agents", "skills"), kind: localSkillRootUniversal},
	}
	if provider == "claude" {
		for _, plugin := range listEnabledClaudePlugins(home) {
			manifest, _ := readClaudePluginManifest(plugin.InstallPath)
			for _, path := range claudePluginComponentPaths(
				plugin.InstallPath,
				manifest.Skills,
				filepath.Join(plugin.InstallPath, "skills"),
			) {
				roots = append(roots, localSkillRoot{
					path:      path,
					kind:      localSkillRootPlugin,
					keyPrefix: plugin.Name + ":",
					plugin:    plugin.ID,
				})
			}
		}
	}
	return roots, true, nil
}

// isIgnoredLocalSkillEntry 判断技能目录下的条目是否应被忽略（空名、点开头，或
// license 相关文件）。
func isIgnoredLocalSkillEntry(name string) bool {
	switch name {
	case "", ".git", ".svn", "__MACOSX", ".DS_Store":
		return true
	}
	return false
}

// normalizeLocalSkillKey 把技能 key 清洗成“斜杠分隔的相对路径”形态：去空白、防
// 目录逃逸（Clean 后不得是 "."、绝对路径或以 ".." 开头）。
func normalizeLocalSkillKey(key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("skill key is required")
	}
	cleaned := filepath.Clean(filepath.FromSlash(strings.TrimSpace(key)))
	if cleaned == "." || filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("invalid skill key")
	}
	return filepath.ToSlash(cleaned), nil
}

// relativizeHomePath 把位于用户家目录下的路径转成以 ~ 开头的相对展示形态，避免把
// 账号名发上线；其余路径原样转斜杠。
func relativizeHomePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.ToSlash(path)
	}
	if path == home {
		return "~"
	}
	prefix := home + string(filepath.Separator)
	if strings.HasPrefix(path, prefix) {
		return filepath.ToSlash("~" + string(filepath.Separator) + strings.TrimPrefix(path, prefix))
	}
	return filepath.ToSlash(path)
}

// readLocalSkillMainFile 读技能目录的 SKILL.md 主文件，超限即拒绝。
func readLocalSkillMainFile(skillDir string) (string, error) {
	mainPath := filepath.Join(skillDir, "SKILL.md")
	info, err := os.Stat(mainPath)
	if err != nil {
		return "", err
	}
	if info.Size() > maxLocalSkillFileSize {
		return "", fmt.Errorf("SKILL.md exceeds %d bytes", maxLocalSkillFileSize)
	}
	content, err := os.ReadFile(mainPath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// collectLocalSkillFiles 收集技能目录下除 SKILL.md 外的支撑文件（可含内容），对
// 单文件大小、文件数与总大小设限，并跳过“无法无损往返传输”的二进制/坏 UTF-8 文件。
func collectLocalSkillFiles(skillDir string, includeContent bool) ([]SkillFileData, error) {
	files := make([]SkillFileData, 0)
	var totalSize int64

	// filepath.WalkDir 不跟随“符号链接的根”，所以当 runtime 根内含指向共享技能安装器
	// 的符号链接（如 lark-cli 的 ~/.agents/skills/<name>）时，从链接路径出发只能数出
	// 零个子项，每个这类技能最终都上报 0 个文件。先解析真实路径，让遍历真正进入目录。
	walkRoot := skillDir
	if resolved, err := filepath.EvalSymlinks(skillDir); err == nil {
		walkRoot = resolved
	}

	err := filepath.WalkDir(walkRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == walkRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if isIgnoredLocalSkillEntry(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if isIgnoredLocalSkillEntry(entry.Name()) || path == filepath.Join(walkRoot, "SKILL.md") {
			return nil
		}

		rel, err := filepath.Rel(walkRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.Clean(rel)
		if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxLocalSkillFileSize {
			return fmt.Errorf("file %s exceeds 16 MiB", rel)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(files) >= maxLocalSkillFileCount {
			return fmt.Errorf("local skill exceeds %d files", maxLocalSkillFileCount)
		}
		totalSize += info.Size()
		if totalSize > maxLocalSkillBundleSize {
			return fmt.Errorf("local skill exceeds %d bytes in total", maxLocalSkillBundleSize)
		}

		file := SkillFileData{Path: filepath.ToSlash(rel), Kind: skill.FileKind(rel, content), Mode: skill.FileMode(int32(info.Mode().Perm()))}
		if includeContent {
			if file.Kind == "binary" {
				file.Data = content
			} else {
				file.Content = string(content)
			}
		}
		files = append(files, file)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

// listRuntimeLocalSkills 列出某 provider 的 runtime 本地技能摘要（供 UI 展示）。
func listRuntimeLocalSkills(provider string) ([]runtimeLocalSkillSummary, bool, error) {
	roots, supported, err := localSkillRootsForProvider(provider)
	if err != nil || !supported {
		return nil, supported, err
	}

	// 走每个 runtime 根，在 filepath.WalkDir 之上做两个扩展：
	//   - 每层都跟随符号链接。lark-cli 这类安装器把每个技能都做成指向共享
	//     ~/.agents/skills/<name> 的符号链接；旧的 WalkDir 路径经 os.ModeSymlink
	//     提前返回，把它们静默丢掉了。
	//   - 允许嵌套布局。opencode 把技能存成 `release/reporter/SKILL.md`，而
	//     `loadRuntimeLocalSkillBundle` 已接受斜杠分隔的 key，所以列表端点也必须暴露
	//     这些嵌套技能。
	skills := make([]runtimeLocalSkillSummary, 0)
	// 严格按 Key 去重。根按优先级访问（provider 专用先、~/.agents/skills 后）；Key
	// 的首次出现获胜。这让向后兼容可证明：单根行为下可见的每个技能都保住它的 Key、
	// SourcePath 与 FileCount，我们只会*新增* universal 根下发现的不冲突 Key。
	seenKeys := make(map[string]bool)
	for _, root := range roots {
		if _, err := os.Stat(root.path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, true, err
		}

		// 每个根有自己的 `visited` 集合，*绝不*跨根共享：用户可以靠符号链接刻意把
		// 同一份磁盘技能暴露成两个名字，例如 ~/.claude/skills/bar ->
		// ~/.agents/skills/foo。用 per-root visited 集合时 `bar`（claude 根）与 `foo`
		// （agents 根）都会列出；共享集合会把第一个根已标记过的真实路径忽略掉，悄悄
		// 丢掉合法的第二个条目。
		rootSkills := make([]runtimeLocalSkillSummary, 0)
		visited := make(map[string]bool)
		enumerateLocalSkills(provider, root, root.path, root.path, 0, visited, &rootSkills)

		for _, s := range rootSkills {
			if seenKeys[s.Key] {
				continue
			}
			seenKeys[s.Key] = true
			skills = append(skills, s)
		}
	}

	// 所有根合并完再排一次序，无论多少个根参与，结果都稳定。
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Key < skills[j].Key
	})
	return skills, true, nil
}

// enumerateLocalSkills 在 currentDir 里寻找技能目录（含 SKILL.md 的目录）。找到时，
// 以相对 walkRoot 的 key 注册它，并在该分支停止递归——绝不下钻进一个已算作技能的
// 目录，哪怕它里面恰好还有自己的嵌套 SKILL.md。
//
// `visited` 以“（跟随符号链接后的）解析绝对路径”为键，让循环符号链接不会永远转
// 圈；这是唯一需要预先急切 EvalSymlinks 的原因。EvalSymlinks 出错就停掉该分支的下
// 钻——多数情况是悬空链接，本来就该忽略。
func enumerateLocalSkills(
	provider string,
	root localSkillRoot,
	walkRoot, currentDir string,
	depth int,
	visited map[string]bool,
	skills *[]runtimeLocalSkillSummary,
) {
	if depth > maxLocalSkillDirDepth {
		return
	}
	resolved, err := filepath.EvalSymlinks(currentDir)
	if err != nil {
		return
	}
	if visited[resolved] {
		return
	}
	visited[resolved] = true

	entries, err := os.ReadDir(currentDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		name := entry.Name()
		if isIgnoredLocalSkillEntry(name) {
			continue
		}
		path := filepath.Join(currentDir, name)
		info, statErr := os.Stat(path) // 跟随符号链接
		if statErr != nil || !info.IsDir() {
			continue
		}

		mainPath := filepath.Join(path, "SKILL.md")
		if _, err := os.Stat(mainPath); err == nil {
			rel, err := filepath.Rel(walkRoot, path)
			if err != nil {
				continue
			}
			key, err := normalizeLocalSkillKey(rel)
			if err != nil {
				continue
			}
			key = root.keyPrefix + key

			content, err := readLocalSkillMainFile(path)
			if err != nil {
				continue
			}
			skillName, description := skill.ParseSkillFrontmatter(content)
			if root.plugin != "" {
				skillName = key
			} else if skillName == "" {
				skillName = filepath.Base(path)
			}

			files, err := collectLocalSkillFiles(path, false)
			if err != nil {
				continue
			}

			*skills = append(*skills, runtimeLocalSkillSummary{
				Key:         key,
				Name:        skillName,
				Description: description,
				SourcePath:  relativizeHomePath(path),
				Provider:    provider,
				Root:        root.kind,
				Plugin:      root.plugin,
				CanDisable:  provider == "codex" || provider == "claude",
				// `files` 是支撑包（collectLocalSkillFiles 有意排除 SKILL.md，让包的
				// `Content` 字段能装下它而不在导入时重复）。列表摘要里用户要的是文件总
				// 数，因此把 SKILL.md 自己加回来。
				FileCount: len(files) + 1,
			})
			continue
		}

		// 这里没有 SKILL.md——继续下钻找嵌套技能。
		enumerateLocalSkills(provider, root, walkRoot, path, depth+1, visited, skills)
	}
}

// loadRuntimeLocalSkillBundle 按技能 key 加载某 provider 的完整技能包（含内容），
// 供导入服务器使用。
func loadRuntimeLocalSkillBundle(provider, skillKey string) (*runtimeLocalSkillBundle, bool, error) {
	roots, supported, err := localSkillRootsForProvider(provider)
	if err != nil || !supported {
		return nil, supported, err
	}

	key, err := normalizeLocalSkillKey(skillKey)
	if err != nil {
		return nil, true, err
	}

	// 与 listRuntimeLocalSkills 相同优先级顺序走根，让导入精确解析到列表端点暴露的
	// 那个技能。指导性不变量是“列表/加载一致”：只有当某根下该 key 是“一个带着
	// SKILL.md 的目录”——listRuntimeLocalSkills 注册的精确条件——才算根“有”这个
	// 技能。任何在该 key 下不是合法技能的情况（无条目、不是目录、或没有 SKILL.md 的
	// 目录）都表示“该根没有它”，于是落到下一个根。只有真正的 IO/权限故障会被返回，
	// 因此绝不会悄悄用低优先级根里“同 key 但内容不同”的技能顶替。
	for _, root := range roots {
		rootKey := key
		if root.keyPrefix != "" {
			if !strings.HasPrefix(key, root.keyPrefix) {
				continue
			}
			rootKey = strings.TrimPrefix(key, root.keyPrefix)
		}
		skillDir := filepath.Join(root.path, filepath.FromSlash(rootKey))
		info, err := os.Stat(skillDir)
		if err != nil {
			// IsNotExist => 该根只是没有这个技能，试下一个。其它 stat 错误（权限、IO）
			// 原样返回而不是静默跳过——跳过可能从低优先级根加载出与用户在列表里选的
			// 不匹配的“同 key 另一技能”（Eve review #1）。
			if os.IsNotExist(err) {
				continue
			}
			return nil, true, err
		}
		if !info.IsDir() {
			// 不是目录：listRuntimeLocalSkills 从不把非目录当技能，所以该根在这个 key
			// 下没有技能。落到下一个根而不是报错。
			continue
		}

		// 目录只有当它确实含 SKILL.md 才算技能。同 key 但没 SKILL.md 的目录（例如只是
		// 一个空/无关文件夹的 ~/.claude/skills/foo）*不是*这个技能——列表会跳过它下钻
		// ——因此它绝不能遮蔽合法的 ~/.agents/skills/foo。把“SKILL.md 缺失”当作“该根
		// 没有它”并继续；只有对 SKILL.md 的 stat 出错且非 IsNotExist（权限、IO）才返回。
		mainPath := filepath.Join(skillDir, "SKILL.md")
		if _, err := os.Stat(mainPath); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, true, err
		}

		content, err := readLocalSkillMainFile(skillDir)
		if err != nil {
			return nil, true, err
		}
		name, description := skill.ParseSkillFrontmatter(content)
		if root.plugin != "" {
			name = key
		} else if name == "" {
			name = filepath.Base(skillDir)
		}

		files, err := collectLocalSkillFiles(skillDir, true)
		if err != nil {
			return nil, true, err
		}

		return &runtimeLocalSkillBundle{
			Name:        name,
			Description: description,
			Content:     content,
			SourcePath:  relativizeHomePath(skillDir),
			Provider:    provider,
			Files:       files,
		}, true, nil
	}

	return nil, true, fmt.Errorf("local skill not found")
}
