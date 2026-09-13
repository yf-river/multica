package execenv

// 文件导读：本文件负责为 codex provider 构建「per-task 的 CODEX_HOME」。
//
// 角色：daemon 把每个 Codex 任务的 CODEX_HOME 设成一个任务私有目录（隔离于用户
// 真实的 ~/.codex），本文件从共享 home（$CODEX_HOME 或 ~/.codex）向这个目录播种
// 配置：auth.json 用 symlink 共享（让 token 刷新自动生效），config.json /
// config.toml / instructions.md 用复制隔离（任务本地改动不影响共享 home）。之后还
// 会往里写入一段 daemon 管理的 sandbox 配置块，并把每个任务各自的 sessions/ 指到
// 只含本 issue 历史的持久会话存储（避免 Codex 0.143+ 为整机 ~/.codex/sessions 做
// 全量 backfill 时卡死 initialize）。
//
// 关心的几类事：Windows 沙箱策略判定（fail-closed，不许误放宽到 danger-full-access）、
// 复制配置时把被引用的外部文件（model_catalog_json、instructions 等）一并物化到
// 任务 home 内、model 缓存与来源配置绑定、以及所有落盘操作都走 root-scoped 句柄，
// 防止被复用的任务 home 里塞一个指向他处的 symlink 把 daemon 的写入带出隔离区。
//
// 边界：真正的沙箱策略内容在 codex_sandbox.go；技能注入在 codex_skill_strip.go /
// codex_user_skills.go；native 多 agent 与记忆的禁用开关分别在 codex_multi_agent.go
// / codex_memory.go。本文件只做「home 目录的内容编排」。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// 从共享 ~/.codex/ 软链进 per-task CODEX_HOME 的文件。软链共享状态（如 auth
// token），改动会自动传播。
var codexSymlinkedFiles = []string{
	"auth.json",
}

// 从共享 ~/.codex/ 复制进 per-task CODEX_HOME 的文件。复制是隔离的——任务本地的
// 配置与缓存刷新不会改动共享 home。
var codexCopiedFiles = []string{
	"config.json",
	"config.toml",
	"instructions.md",
}

const (
	codexModelsCacheFile        = "models_cache.json"
	codexModelsCacheBindingFile = ".models_cache_config.sha256"
)

// 这些文件的内容决定 Codex 所用的模型 provider/目录。任务本地的 models 缓存仅在
// 这份来源配置保持不变时可复用。config.toml 引用的 model_catalog_json 会由
// codexModelsCacheConfigFingerprint 单独折进绑定指纹。
var codexModelsCacheConfigFiles = []string{
	"config.json",
	"config.toml",
}

// CodexHomeOptions 携带影响「生成出来的 per-task config.toml」的可选输入。
type CodexHomeOptions struct {
	// CodexVersion 是探测到的 Codex CLI 版本（如 "0.121.0"）。为空 = 未知；macOS
	// 上「未知」被当作「很可能有问题」，daemon 网络访问因此回退到
	// danger-full-access。详见 codex_sandbox.go。
	CodexVersion string
	// GOOS 覆盖目标平台用于决定沙箱策略。空 = 使用 runtime.GOOS。主要让测试能
	// 确定性地跑 macOS 与 Linux 两条路径。
	GOOS string
	// ResumeSessionID 是本 run 打算续接的 Codex thread/session ID（若有）。在填充
	// per-issue 会话存储（local_directory 任务）或迁移「sessions/ 仍软链共享
	// ~/.codex/sessions」的遗留 per-task home 时被查询：只暴露该 ID 的那一份 rollout，
	// 使 thread/resume 能找到它，而无须把整段共享历史拉回来。空 = 全新 thread
	// （没有 rollout 可暴露）。参见 prepareCodexSessionsDir（MUL-4424）。
	ResumeSessionID string
	// IsLocalDirectory 标记「env root 永远不会跨 task ID 复用」的任务——每个
	// local_directory 任务（in_place 或 worktree 都一样）。worktree 任务和 in-place
	// 一样每个任务都拿全新 env root（shouldReusePriorWorkdir 拒绝任何本地分配），
	// 所以它们需要同样的 per-issue 会话存储；若改成按「跑在用户目录里」来判定，就会
	// 在同一个 issue 的轮次之间静默丢掉 Codex agent 的对话历史。
	//
	// 这些任务每个 task ID 都拿全新 codex-home（daemon 从不复用它们的 workdir），
	// 所以它们的 sessions/ 指向跨 task ID 存活、且「只装这个 issue 的 rollout」的
	// per-issue 存储（SessionStoreKey）——绝不是整机的 ~/.codex/sessions。参见
	// prepareCodexSessionsDir（MUL-4424）。
	IsLocalDirectory bool
	// SessionStoreKey 是稳定的、per-(agent, issue-or-chat) 相对路径，标识本任务的
	// 持久 Codex sessions 存储。它跨 task ID 存活（不像 task-scoped envRoot 那样会被
	// GC 回收），因此后续 run 能续接同一 thread。没有稳定 key（如无 issue 的任务）
	// 时为空，此时 sessions/ 保持任务本地。参见 codexSessionStoreDir 与
	// prepareCodexSessionsDir（MUL-4424）。
	SessionStoreKey string
	// CodexCustomArgs 是本任务实际启动 Codex 时携带的 CLI 参数（daemon 默认 +
	// profile 固定 + per-agent custom_args）。只有 Windows 沙箱决策会读它，以尊重
	// 一个永远不会进 config.toml 的 `-c windows.sandbox=...` 覆盖。参见
	// resolveWindowsSandboxState 与 MUL-4957。
	CodexCustomArgs []string
}

// prepareCodexHome 是 prepareCodexHomeWithOpts 的薄包装，供「不在乎平台感知沙箱
// 配置」的测试使用。它把 GOOS 钉在 linux——解析为 danger-full-access 默认值
// （MUL-5578）——因此写出的 sandbox 块与运行测试的主机无关、稳定可预期。
func prepareCodexHome(codexHome string, logger *slog.Logger) error {
	return prepareCodexHomeWithOpts(codexHome, CodexHomeOptions{GOOS: "linux"}, logger)
}

// sharedConfigPresence 是共享 ~/.codex/config.toml 复制来源的三态存在性。做成三值，
// 使一次「非 not-found 原因」失败的 stat（权限/IO）绝不会伪装成确定的「用户没有配
// 置」——那会纵容 daemon 在存疑时放宽到 danger-full-access。参见
// resolveWindowsSandboxState（MUL-4957）。
type sharedConfigPresence int

const (
	// sharedConfigAbsent：共享 config.toml 确定不存在（os.IsNotExist），因此 per-task
	// 副本缺失是货真价实的「未配置」。
	sharedConfigAbsent sharedConfigPresence = iota
	// sharedConfigPresent：共享 config.toml 存在。
	sharedConfigPresent
	// sharedConfigUndecidable：stat 因 not-found 之外的原因失败；daemon 无法判断用户
	// 是否有配置。
	sharedConfigUndecidable
)

// statSharedCodexConfig 把共享 ~/.codex/config.toml（复制来源）归类进上面三态，
// 区分「真缺失」与「无法完成的 stat」。
func statSharedCodexConfig(sharedHome string) sharedConfigPresence {
	if sharedHome == "" {
		return sharedConfigAbsent
	}
	_, err := os.Stat(filepath.Join(sharedHome, "config.toml"))
	switch {
	case err == nil:
		return sharedConfigPresent
	case os.IsNotExist(err):
		return sharedConfigAbsent
	default:
		return sharedConfigUndecidable
	}
}

// resolveWindowsSandboxState 对 Windows 任务判断「是否配置了原生 Codex 沙箱」——
// 综合 per-task config.toml 与生效的 custom args——无法判断时关闭即失败（Undecidable）。
//
// 两个信号不由它自己采集（由调用方给），这使 fail-closed 逻辑不必动文件系统也能
// 单测，并封上 MUL-4957 第 3 轮必修项里「一次失败的同步可能被误读成未配置」的坑：
//
//   - configSyncErr：把共享 config.toml 同步进 per-task home 时的错误（若有）。非
//     nil 表示 per-task config.toml 不可信——可能是上一轮的陈旧副本，或从未(重)写过
//     ——所以无论它「有内容」还是「缺失」都不能反映用户意图。关闭即失败。
//   - sharedPresence：共享 config.toml 来源是否存在。只有「确定缺失」才能让 per-task
//     副本的缺失算作真·未配置；来源「存在或不可判定」而 per-task 副本缺失，说明副本
//     静默没落地，同样关闭即失败。
func resolveWindowsSandboxState(configFile string, configSyncErr error, sharedPresence sharedConfigPresence, customArgs []string, logger *slog.Logger) windowsSandboxConfig {
	configState := classifyPerTaskWindowsSandbox(configFile, configSyncErr, sharedPresence)
	state := resolveWindowsSandbox(configState, windowsSandboxFromCustomArgs(customArgs))
	if state == windowsSandboxUndecidable && logger != nil {
		logger.Error("codex sandbox: cannot determine Windows native sandbox config; keeping workspace-write and refusing to loosen to danger-full-access",
			"config_file", configFile)
	}
	return state
}

// classifyPerTaskWindowsSandbox 依据「从共享来源同步到 per-task」的结果检查 per-task
// config.toml；文件不可信或不可读时一律关闭即失败。
func classifyPerTaskWindowsSandbox(configFile string, configSyncErr error, sharedPresence sharedConfigPresence) windowsSandboxConfig {
	// 共享→per-task 的同步一旦失败，config.toml 就是陈旧或缺失的；无论有内容还是
	// 缺失都不反映用户意图。关闭即失败。
	if configSyncErr != nil {
		return windowsSandboxUndecidable
	}
	data, err := os.ReadFile(configFile)
	switch {
	case err == nil:
		return windowsSandboxFromConfig(string(data))
	case os.IsNotExist(err):
		// 同步成功而 per-task 配置缺失。只有共享来源也「确定缺失」时，这才算真的
		// 「没有配置」；来源「存在或不可判定」而副本缺失，意味着副本没落地——
		// 关闭即失败，而不是放宽。
		if sharedPresence == sharedConfigAbsent {
			return windowsSandboxAbsent
		}
		return windowsSandboxUndecidable
	default:
		// 对一个 daemon 刚写过的文件，读失败（权限/IO）。
		return windowsSandboxUndecidable
	}
}

// prepareCodexHomeWithOpts 创建 per-task 的 CODEX_HOME 目录，并从共享 ~/.codex/ home
// 播种配置。auth 用 symlink（共享）、配置文件用复制（隔离）。per-task config.toml
// 会写进一段由 daemon 管理、经 codexSandboxPolicyFor 挑选的 sandbox 块。
func prepareCodexHomeWithOpts(codexHome string, opts CodexHomeOptions, logger *slog.Logger) error {
	sharedHome := resolveSharedCodexHome()
	freshHome := false
	if _, err := os.Lstat(codexHome); os.IsNotExist(err) {
		freshHome = true
	}

	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		return fmt.Errorf("create codex-home dir: %w", err)
	}

	// 给任务一个自己本地的 sessions/ 目录，而不是软链共享 ~/.codex/sessions——
	// 庞大的共享历史会卡住 Codex `initialize` 的状态回填（MUL-4424）。参见
	// prepareCodexSessionsDir。
	if err := prepareCodexSessionsDir(codexHome, sharedHome, opts, logger); err != nil {
		logger.Warn("execenv: codex-home sessions dir prepare failed", "error", err)
	}

	// 软链共享文件（auth）。
	for _, name := range codexSymlinkedFiles {
		src := filepath.Join(sharedHome, name)
		dst := filepath.Join(codexHome, name)
		if err := ensureSymlink(src, dst); err != nil {
			logger.Warn("execenv: codex-home symlink failed", "file", name, "error", err)
		}
	}

	// 暴露最终 auth.json 的状态（只报文件类型，绝不报内容），让排查 token 刷新失败
	// 的运维人员能一眼看出 per-task home 是在跟踪共享 ~/.codex/auth.json，还是漂移
	// 进了陈旧的本地副本。
	logCodexAuthState(filepath.Join(codexHome, "auth.json"), logger)

	// 从共享来源同步隔离文件。单独跟踪 config.toml 的同步结果：Windows 上同步失败
	// 会让 per-task 配置不可信，沙箱决策必须关闭即失败，而不是把陈旧/缺失的副本读
	// 成「未配置」而放宽（MUL-4957）。
	var configSyncErr error
	for _, name := range codexCopiedFiles {
		src := filepath.Join(sharedHome, name)
		dst := filepath.Join(codexHome, name)
		if err := syncCopiedFile(src, dst); err != nil {
			logger.Warn("execenv: codex-home sync failed", "file", name, "error", err)
			if name == "config.toml" {
				configSyncErr = err
			}
		}
	}
	// 去掉从用户 ~/.codex/config.toml 继承来的 `[[skills.config]]` 条目。Codex
	// Desktop 会写带 `name` 却无 `path` 的插件型技能，CLI 更严格的 TOML 解析器会以
	// `missing field path` 拒绝并中止 `thread/start`。Multica 把 agent 的活跃技能直接
	// 写进 `codex-home/skills/`，所以这里的用户级注册表是多余的。参见
	// codex_skill_strip.go。
	if err := sanitizeCopiedCodexConfig(filepath.Join(codexHome, "config.toml")); err != nil {
		logger.Warn("execenv: codex-home sanitize config failed", "error", err)
	}

	if err := syncCodexReferencedFiles(codexHome, sharedHome); err != nil {
		return fmt.Errorf("sync codex config file references: %w", err)
	}

	// 只为全新任务 home 播种共享 model 缓存。复用时保留一份「Codex 可能刷新过」的
	// 任务本地缓存，但前提是来源 provider/目录配置仍是该缓存绑定时的那个。绑定失败
	// 就丢弃这份可选缓存，让 Codex 自行刷新，而不是可能用到错误 provider 的模型。
	if err := syncCodexModelsCache(codexHome, sharedHome, freshHome); err != nil {
		logger.Warn("execenv: codex-home models cache sync failed; discarding cache", "error", err)
		if removeErr := os.RemoveAll(filepath.Join(codexHome, codexModelsCacheFile)); removeErr != nil {
			return fmt.Errorf("sync codex models cache: %v; discard unsafe cache: %w", err, removeErr)
		}
	}

	if err := exposeSharedCodexPluginCache(codexHome, sharedHome); err != nil {
		logger.Warn("execenv: codex-home plugin cache exposure failed", "error", err)
	}

	// 往 config.toml 写 daemon 管理的 sandbox 块。macOS 上因 openai/codex#10390 可能
	// 需要回退到 danger-full-access；Windows 上除非用户选择原生 windows.sandbox，否则
	// daemon 默认 danger-full-access。完整缘由见 codex_sandbox.go。Windows 上综合已复
	// 制的配置与生效 custom args 判定原生沙箱状态：显式用户选择被尊重，不可判定的
	// 配置关闭即失败而不是放宽。
	configFile := filepath.Join(codexHome, "config.toml")
	winState := windowsSandboxAbsent
	if resolveGOOS(opts.GOOS) == "windows" {
		winState = resolveWindowsSandboxState(configFile, configSyncErr, statSharedCodexConfig(sharedHome), opts.CodexCustomArgs, logger)
	}
	policy := codexSandboxPolicyForConfig(opts.GOOS, opts.CodexVersion, winState)
	if err := ensureCodexSandboxConfig(configFile, policy, opts.CodexVersion, logger); err != nil {
		// 这块受管配置是「磁盘上权威的沙箱策略」。写不进去的话，config.toml 保留原
		// 有内容——复用 home 上可能是上一轮陈旧的 danger-full-access——那么刚算出的
		// fail-closed 策略只存在于内存里，而实际配置悄悄保持宽松。与其在沙箱未被
		// 落实的情况下启动 Codex，不如中止：全新 Prepare 会让任务失败；Reuse 时调用
		// 方把 env.CodexHome 置空，configureCodexTaskShellEnvironment 随后拒绝启动
		// （MUL-4957）。
		return fmt.Errorf("ensure codex sandbox config: %w", err)
	}

	// 在 daemon 管理的任务会话里禁用 Codex 原生多 agent：让父线程的 `turn/completed`
	// 不会在派生子 agent 仍在运行时被误当成任务完成。完整缘由与逃生门见
	// codex_multi_agent.go。
	if err := ensureCodexMultiAgentConfig(filepath.Join(codexHome, "config.toml"), logger); err != nil {
		logger.Warn("execenv: codex-home ensure multi-agent config failed", "error", err)
	}

	// 在 daemon 管理的任务会话里禁用 Codex 原生自动记忆，使跨任务、跨 workspace 的
	// 上下文泄漏（multica#3130）无法经 `codex-home/memories/` 或 `~/.codex/memories/`
	// 发生。完整缘由与逃生门见 codex_memory.go。
	if err := ensureCodexMemoryConfig(filepath.Join(codexHome, "config.toml"), logger); err != nil {
		logger.Warn("execenv: codex-home ensure memory config failed", "error", err)
	}

	return nil
}

// resolveSharedCodexHome 返回用户共享 Codex home 的路径：先查 $CODEX_HOME，回退到
// ~/.codex。
func resolveSharedCodexHome() string {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		abs, err := filepath.Abs(v)
		if err == nil {
			return abs
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".codex") // last resort fallback
	}
	return filepath.Join(home, ".codex")
}

// codexSessionStateGlobs 是 Codex 在某个 CODEX_HOME 里、通过索引 sessions/ 之下所有
// 内容构建出的「会话派生」SQLite 状态。旧版软链迁移（prepareCodexSessionsDir）会删掉
// 它们，让 Codex 从「现在已是任务本地」的 sessions 重建，而不是保留它从共享
// ~/.codex/sessions 历史回填的成千上万行陈旧数据。这里匹配到的都是可重建的派生
// 索引——绝不是权威数据。
//
// 刻意不列出：session_index.jsonl——Codex 0.144.x 用它当「thread-id → 用户自定义
// 线程名」的权威存储（改名会写进 SQLite 和这个文件，从不会写回 rollout），因此无法
// 从 rollout 重建；以及其它前缀（goals_*、logs_*、memories_*）的非会话派生 per-task
// DB。它们都保持原样。
var codexSessionStateGlobs = []string{
	"state_*.sqlite",
	"state_*.sqlite-shm",
	"state_*.sqlite-wal",
}

// codexSessionStoreRoot 是共享 Codex home 下存放 per-issue 会话存储的目录。它位于
// 用户自己的 sessions/ 旁边，共享同一卷（让续接 rollout 的硬链接零拷贝），但从不会被
// 一次普通 `codex` 运行枚举到，Multica 的任务历史因此不会混进用户自己的线程列表。
const codexSessionStoreRoot = "multica-sessions"

// codexSessionStoreDir 返回 key 的持久、per-(agent, issue) Codex sessions 存储，
// 根落在共享 Codex home 所在的卷上。它跨 task ID 存活（不像 task-scoped envRoot 会被
// GC 回收），只装该 issue 的 rollout。key 为空 → ""（调用方让 sessions/ 保持任务
// 本地）。
func codexSessionStoreDir(sharedHome, key string) string {
	if key == "" {
		return ""
	}
	return filepath.Join(sharedHome, codexSessionStoreRoot, key)
}

// codexSessionStoreNamespace 把 daemon 的 profile 映射为路径段，用于在「多台 daemon
// 共享一个 ~/.codex 跑在同一台机器上」时（profile 各有独立 daemon 状态，但 Codex
// home 相同）隔离各 profile 的会话存储。每个 daemon 只在自己的命名空间下写入、也只
// 回收自己的——因此一个 staging daemon 的 GC 永远删不掉生产任务在用的 live store，
// 反之亦然。
//
// 该映射必须「无碰撞」（不同 profile 是不同的 daemon，绝不共享命名空间）且「定长」
// （profile 最长可到文件系统单段上限约 255 字节，任何会拉长长度的编码都会溢出 255
// 字节限制、导致建目录失败）。有损的「丢弃不安全字符」方案会碰撞（"" vs "default"、
// "staging.prod" vs "stagingprod"）；完整 hex 编码会把长度翻倍而溢出。因此空
// （default）profile 用一个保留的裸字面量，每个具名 profile 用其 SHA-256 的 hex——
// 恒 64 个 hex 字符、文件系统安全、抗碰撞——前缀 "p_" 使裸字面量永不与它碰撞
// （MUL-4424）。
func codexSessionStoreNamespace(profile string) string {
	if profile == "" {
		return "default"
	}
	sum := sha256.Sum256([]byte(profile))
	return "p_" + hex.EncodeToString(sum[:])
}

// codexSessionStoreKey 为持久 Codex sessions 构建「profile + 任务」键。issue ID 保留
// 其既有路径；直接聊天用带前缀的 chat_session_id，使两个命名空间不会碰撞。两个稳定
// 标识都没有时返回 ""。
func codexSessionStoreKey(profile string, task TaskContextForEnv) string {
	storeID := sanitizePathSegment(task.IssueID)
	if storeID == "" {
		chatID := sanitizePathSegment(task.ChatSessionID)
		if chatID == "" {
			return ""
		}
		storeID = "chat_" + chatID
	}
	agent := sanitizePathSegment(task.AgentID)
	if agent == "" {
		agent = "_"
	}
	return filepath.Join(codexSessionStoreNamespace(profile), agent, storeID)
}

// sanitizePathSegment 把 s 缩减成 UUID 会用的字符（hex 加横线/下划线），丢掉其余
// 一切，使结果恒为单个安全路径段——无分隔符、无 ".."、无盘符。
func sanitizePathSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// PruneCodexSessionStores 回收共享 home 的 multica-sessions 根下「在 retention 内没被
// 触碰过」的 per-issue Codex 会话存储，为其中承载的每段对话历史设下生存期限。
//
// 这些存储刻意位于任务 GC 回收的 task-scoped envRoot 之外（这样续接能跨 task ID
// 存活），也正因如此，没有本函数它们会无限堆积——一个已完成或废弃 issue 的提示词
// 与完整 rollout（有人见过单个 1.5 GiB 的 rollout）永远不会被释放，删除
// issue/agent/workspace 也删不掉它们。存储的最新 mtime 就是它的最近活动：Codex 随
// 线程推进写/追加 rollout，因此活跃或最近续接的任务不断刷新存储、绝不会被回收；
// 闲置超过 retention 的存储被移除，让已删除的 issue 获得「最终必被回收」的保证。
// retention <= 0 时完全禁用清理。
//
// 它只扫「调用方 profile 的命名空间」——一个 daemon 永远不会回收共享同一 ~/.codex
// 的另一个 profile-daemon 的存储——进程内预留守卫无法跨进程，而命名空间让它们的
// 存储树彼此不相交，也就不需要跨进程（MUL-4424）。
//
// reserve（可为 nil）原子地为一次删除认领某个存储：当活动任务持有该存储时返回
// ok=false（放过它）；否则返回一个「删除完成后要跑」的 commit。由于调用方的预留与
// 任务的 mark-active 走同一个进程里的同一把锁，一个任务正要挂载的存储绝不会在它
// 脚下被删——「确认不活跃」与「删除」实际原子，封死了朴素的时间点活跃检查留下的
// stat→remove 竞态。nil 时禁用守卫（测试用）：每个闲置存储都被删。
func PruneCodexSessionStores(profile string, retention time.Duration, now time.Time, reserve func(storeDir string) (commit func(), ok bool), logger *slog.Logger) (removed int, bytesFreed int64) {
	if retention <= 0 {
		return 0, 0
	}
	root := filepath.Join(resolveSharedCodexHome(), codexSessionStoreRoot, codexSessionStoreNamespace(profile))
	agents, err := os.ReadDir(root)
	if err != nil {
		return 0, 0 // not created yet, or unreadable — nothing to prune
	}
	for _, a := range agents {
		if !a.IsDir() {
			continue
		}
		agentDir := filepath.Join(root, a.Name())
		issues, err := os.ReadDir(agentDir)
		if err != nil {
			continue
		}
		kept := 0
		for _, is := range issues {
			if !is.IsDir() {
				continue
			}
			storeDir := filepath.Join(agentDir, is.Name())
			newest, size := dirStat(storeDir)
			if newest.IsZero() || now.Sub(newest) <= retention {
				kept++
				continue
			}
			// 删除前先原子认领该存储。活动任务持有它（或此刻正要挂载它）→ 跳过；
			// 否则认领会阻止任何任务在「删除 + commit 完成」前占用它。
			var commit func()
			if reserve != nil {
				c, ok := reserve(storeDir)
				if !ok {
					kept++
					continue
				}
				commit = c
			}
			err := os.RemoveAll(storeDir)
			if commit != nil {
				commit()
			}
			if err != nil {
				logger.Warn("execenv: prune codex session store failed", "store", storeDir, "error", err)
				kept++
				continue
			}
			removed++
			bytesFreed += size
		}
		// 最后一个 issue 存储也删掉后，顺手移除 agent 目录，别让树里留下空空的
		// <agent>/ 外壳。
		if kept == 0 {
			_ = os.Remove(agentDir)
		}
	}
	return removed, bytesFreed
}

// dirStat 遍历 dir 一遍，返回见过的最新修改时间（目录的最近活动）与总字节数（GC
// 记账用）。由会话存储清理器与任务临时文件清扫共用。
func dirStat(dir string) (newest time.Time, size int64) {
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if !d.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return newest, size
}

// prepareCodexSessionsDir 把 codex-home/sessions 指向一个「只装本任务自己历史」的
// 会话存储，绝不装整机 ~/.codex/sessions。
//
// 背景（MUL-4424）：Codex 0.143+ 会在 `initialize` 期间枚举 sessions/ 下每一个可见
// rollout 来回填 per-home 会话状态 DB。当 per-task home 软链了整个共享 sessions 目录
// 时，一台积累了大量 rollout 的机器（有人报告约 2000 个文件 / 约 22 GiB）会让
// `initialize` 卡住几十秒——app-server 已启动，但任务在超时取消前没有产出任何输出。
// 因此把 sessions/ 收敛到单个 task/issue：
//
//   - local_directory 任务：envRoot（连同 codex-home）每个 task ID 全新、从不复用，
//     所以 sessions/ 软链到共享 Codex 卷上的 per-issue 存储——跨 task ID 稳定、
//     GC 安全、只装该 issue 的 rollout。参见 linkCodexSessionsToStore。
//   - 全新受管任务：sessions/ 不存在——建一个空的本地目录，让回填微不足道；被复用
//     的 envRoot 会把它带进下一轮。
//   - 复用的受管任务且 sessions/ 已是真目录：它是权威的——上一轮的 rollout 已在
//     里面——保持原样。
//   - 复用的受管任务仍持有指向共享 sessions 的旧版软链（更早的构建）：迁移。带
//     resume 时改经 per-issue 存储（跨卷安全）；不带则用空本地目录替换软链。两种
//     方式都会丢掉陈旧的会话状态 DB，让 Codex 从收敛过的 sessions 重建它。
func prepareCodexSessionsDir(codexHome, sharedHome string, opts CodexHomeOptions, logger *slog.Logger) error {
	dst := filepath.Join(codexHome, "sessions")
	sharedSessions := filepath.Join(sharedHome, "sessions")
	storeDir := codexSessionStoreDir(sharedHome, opts.SessionStoreKey)

	// local_directory 任务没有可复用的 envRoot，它们的历史只能存在 per-issue 存储里
	// 以跨 task ID 延续。daemon 仍会在声称 resume 前核验具体 rollout 在不在（见
	// CodexResumeRolloutPresent），因此「缺失的 rollout」不再伪装成存在。
	if opts.IsLocalDirectory {
		if storeDir == "" {
			// 没有稳定的 per-issue key（如非 issue 任务）。退回空本地目录，而不是
			// 重新暴露整段共享历史。
			return os.MkdirAll(dst, 0o755)
		}
		return linkCodexSessionsToStore(dst, storeDir, sharedSessions, opts.ResumeSessionID, logger)
	}

	fi, err := os.Lstat(dst)
	switch {
	case os.IsNotExist(err):
		return os.MkdirAll(dst, 0o755) // 全新受管任务——空本地目录
	case err != nil:
		return fmt.Errorf("stat sessions dir %s: %w", dst, err)
	}

	if fi.Mode()&os.ModeSymlink == 0 {
		// 已是真目录（任务本地、权威）。确保它存在（空操作）并保持内容不动。
		return os.MkdirAll(dst, 0o755)
	}

	// 是 symlink/junction。若它已指向本 issue 的存储（一次带 resume 的迁移在先前复用
	// 时做过），它就是权威的——重新确保存储链接与 resume rollout，然后保持原样。
	if storeDir != "" {
		if target, rlErr := os.Readlink(dst); rlErr == nil && sameCodexPath(target, storeDir) {
			return linkCodexSessionsToStore(dst, storeDir, sharedSessions, opts.ResumeSessionID, logger)
		}
	}

	// 是指向共享 ~/.codex/sessions 的旧版软链——迁移它。丢掉会话派生的状态，让
	// Codex 重新索引收敛过的 sessions，而不是保留它从整段共享 home 建出的陈旧行。
	if err := os.Remove(dst); err != nil {
		return fmt.Errorf("remove legacy sessions symlink %s: %w", dst, err)
	}
	resetCodexSessionState(codexHome, logger)

	// 带 resume 时改经 per-issue 存储，使 rollout 以跨卷安全的方式暴露（共享卷内硬
	// 链接 + 进任务 home 的目录链接）。不带 resume——或没有稳定 key——时，全新 thread
	// 只需要一个空本地目录。
	if opts.ResumeSessionID != "" && storeDir != "" {
		logger.Info("execenv: migrated codex-home sessions from shared symlink to per-issue store",
			"codex_home", codexHome, "resume_session", true)
		return linkCodexSessionsToStore(dst, storeDir, sharedSessions, opts.ResumeSessionID, logger)
	}
	logger.Info("execenv: migrated codex-home sessions from shared symlink to task-local dir",
		"codex_home", codexHome, "resume_session", false)
	return os.MkdirAll(dst, 0o755)
}

// linkCodexSessionsToStore 通过一个幂等的目录链接把 codex-home/sessions（dst）指向
// per-issue 存储（storeDir）——Unix 用 symlink、Windows 用 junction——两者都能无特殊
// 权限地跨文件系统卷。存储落在共享 Codex home 的卷上，所以链「目录」而非把 rollout
// 文件复制进任务 home，正是当 WorkspacesRoot 与 ~/.codex 不在同一块盘时 resume 暴露
// 依然安全的原因（MUL-4424、Windows 跨卷）。
//
// 续接而存储里还没有该 rollout 时——例如从旧版全量共享 sessions 布局升级后的首轮，
// 历史只存在于 ~/.codex/sessions——就把那份 rollout 硬链进存储。两个路径都在共享卷
// 上，硬链零拷贝，绝不会把可能上 GiB 的 rollout 放到 initialize 的临界路径上。
func linkCodexSessionsToStore(dst, storeDir, sharedSessions, resumeID string, logger *slog.Logger) error {
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return fmt.Errorf("create codex session store %s: %w", storeDir, err)
	}
	if resumeID != "" && len(findCodexRollouts(storeDir, resumeID)) == 0 {
		if err := exposeResumeRollout(sharedSessions, storeDir, resumeID, logger); err != nil {
			logger.Warn("execenv: bootstrap resume rollout into session store failed; task will fall back to a fresh thread",
				"session_id", resumeID, "error", err)
		}
	}
	if err := ensureCodexSessionsLink(dst, storeDir); err != nil {
		return err
	}
	// 把存储盖成「刚用过」。挂载它（MkdirAll、rollout 查找、链接）并不会动它的 mtime，
	// 没有这步，GC 的闲置检查仍会看到一个 >TTL 的旧存储，可能在续接轮次写出第一份
	// rollout 之前就把它回收——重新打开一个长期闲置的 issue 不应丢失上下文。这就是
	// 活跃刷新；daemon 进程内的活跃存储守卫封住剩余的 stat→remove 竞态（MUL-4424）。
	touchCodexSessionStore(storeDir, logger)
	return nil
}

// touchCodexSessionStore 把 storeDir 的修改时间刷新成现在——dirStat 把它读作存储的
// 最近活动。尽力而为：一次失败的 touch 只带来过度积极清理的风险，而活跃存储守卫仍
// 会兜住。
func touchCodexSessionStore(storeDir string, logger *slog.Logger) {
	now := time.Now()
	if err := os.Chtimes(storeDir, now, now); err != nil {
		logger.Warn("execenv: refresh codex session store activity failed", "store", storeDir, "error", err)
	}
}

// CodexSessionStorePath 返回共享 home 上、per-conversation 的 Codex 会话存储路径；
// 没有稳定 issue/chat key 时返回 ""。daemon 会在任务期间把该路径标记为使用中，使
// PruneCodexSessionStores 绝不会在挂载中途回收某个存储，封住单独的 mtime 刷新封不
// 住的 stat→remove 竞态（MUL-4424）。
func CodexSessionStorePath(profile string, task TaskContextForEnv) string {
	key := codexSessionStoreKey(profile, task)
	if key == "" {
		return ""
	}
	return codexSessionStoreDir(resolveSharedCodexHome(), key)
}

// sameCodexPath 报告两个文件系统路径是否指向同一位置，容忍分隔符/整洁度差异。用于
// 侦测「已指向 per-issue 存储」的 sessions 链接，使被复用的 home 不会被重复迁移。
func sameCodexPath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// resetCodexSessionState 从 per-task CODEX_HOME 移除可重建、会话派生的 Codex 状态
// 文件，让下一次 `initialize` 从任务本地 sessions 重新推导它们。只碰会话派生的索引；
// 无关的 per-task DB（goals_*、logs_*、memories_*）保持原样。
func resetCodexSessionState(codexHome string, logger *slog.Logger) {
	for _, pattern := range codexSessionStateGlobs {
		matches, err := filepath.Glob(filepath.Join(codexHome, pattern))
		if err != nil {
			continue
		}
		for _, m := range matches {
			if err := os.Remove(m); err != nil && !os.IsNotExist(err) {
				logger.Warn("execenv: codex-home reset session state failed", "path", m, "error", err)
			}
		}
	}
}

// ensureCodexSessionsLink 用目录链接把 codex-home/sessions（dst）指向 per-issue 会话
// 存储（src），必要时先建存储。幂等：已指向 src 的链接原样保留；dst 上任何别的东西
// （真目录、陈旧链接、旧版共享 sessions 软链）都被替换。链接无需特权即可跨卷
// （Unix 用 symlink、Windows 用 junction），因此存储可放在共享 Codex 卷上、任务 home
// 位于 WorkspacesRoot 之下（见 linkCodexSessionsToStore）。
func ensureCodexSessionsLink(dst, src string) error {
	if err := os.MkdirAll(src, 0o755); err != nil {
		return fmt.Errorf("create codex session store %s: %w", src, err)
	}
	if fi, err := os.Lstat(dst); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, rlErr := os.Readlink(dst); rlErr == nil && sameCodexPath(target, src) {
				return nil
			}
		}
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("remove stale sessions path %s: %w", dst, err)
		}
	}
	return createDirLink(src, dst)
}

// codexRolloutGlobs 返回能匹配某个会话 rollout（位于某个 Codex sessions 目录下）的
// glob 模式。覆盖 Codex 0.14x 写出的布局：按日期嵌套（sessions/YYYY/MM/DD/）与平铺
// （直接在 sessions/ 下），各自是普通 .jsonl 或后台压缩的 .jsonl.zst。
func codexRolloutGlobs(sessionsDir, sessionID string) []string {
	name := "rollout-*-" + sessionID + ".jsonl*" // .jsonl 与 .jsonl.zst
	return []string{
		filepath.Join(sessionsDir, name),
		filepath.Join(sessionsDir, "*", "*", "*", name),
	}
}

// findCodexRollouts 返回 sessionsDir 下、sessionID 在受支持布局中的所有 rollout 文件。
func findCodexRollouts(sessionsDir, sessionID string) []string {
	if sessionsDir == "" || sessionID == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, pattern := range codexRolloutGlobs(sessionsDir, sessionID) {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, m := range matches {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}

// CodexResumeRolloutPresent 报告 sessionID 的 rollout 是否出现在任务的 codex-home
// sessions 目录。daemon 在准备好环境后用它对「resume」把关，避免声称要续接、结果
// Codex 却静默从头开始——rollout 可能在旧 home 迁移时定位不到而缺失，或 local_directory
// 任务的共享历史已被清理（MUL-4424）。
func CodexResumeRolloutPresent(codexHome, sessionID string) bool {
	if codexHome == "" || sessionID == "" {
		return false
	}
	return len(findCodexRollouts(filepath.Join(codexHome, "sessions"), sessionID)) > 0
}

// exposeResumeRollout 把 sessionID 的 rollout 从共享 sessions 历史链进任务本地 sessions
// 目录，保留相对布局使 thread/resume 能找到它。覆盖嵌套与平铺两种布局下的普通与
// 压缩 rollout。
//
// 它用链接而非复制：rollout 可能很大（有人见过单个 1.5 GiB 文件），且这步跑在
// `initialize` 的临界路径上，无界复制会重新引入我们正要修掉的那个卡顿。硬链→软链的
// 策略见 linkCodexRollout；两者都不成时调用方把 resume 视为不可用、回退到全新线程。
func exposeResumeRollout(sharedSessions, localSessions, sessionID string, logger *slog.Logger) error {
	matches := findCodexRollouts(sharedSessions, sessionID)
	if len(matches) == 0 {
		return fmt.Errorf("no rollout found for session %s under %s", sessionID, sharedSessions)
	}
	linked := 0
	for _, src := range matches {
		rel, err := filepath.Rel(sharedSessions, src)
		if err != nil {
			rel = filepath.Base(src)
		}
		dst := filepath.Join(localSessions, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("create rollout dir %s: %w", filepath.Dir(dst), err)
		}
		if err := linkCodexRollout(src, dst); err != nil {
			return fmt.Errorf("link rollout %s: %w", src, err)
		}
		linked++
	}
	logger.Info("execenv: exposed resume rollout into task-local sessions", "session_id", sessionID, "files", linked)
	return nil
}

// linkCodexRollout 在不复制字节的情况下把 src 物化到 dst：先硬链（零拷贝、无需特殊
// 权限、同卷内 Windows 也可用），跨文件系统时回退到 symlink。它从不复制——rollout
// 可能达数 GiB 且这步在 initialize 的临界路径上，复制会重新引入 MUL-4424 修掉的那个
// 卡顿。
func linkCodexRollout(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return os.Symlink(src, dst)
}

// syncCodexReferencedFiles 把「被复制的 config.toml 里引用的每个文件」都物化进
// per-task CODEX_HOME。
//
// config.toml 被原样复制，因此它的路径值键随配置一起进了任务 home，而键所指的文件
// 却没有。Codex 按 CODEX_HOME（现在已是任务 home）解析相对值，物化不到的引用会让
// Codex 在加载配置时就失败——在任务 prompt 送达之前（MUL-5623 / #6271：
// `failed to read model instructions file <task-home>/gpt-unrestricted.md`）。
//
// 只跟进下面列出的键。跟随配置可能提到的每个路径，等于把任意用户配置变成「把宿
// 主机任意文件拉进任务沙箱」的通道；把整段共享 home 复制过来又会把 auth.json 与整机
// 会话历史一起拖进来。
func syncCodexReferencedFiles(codexHome, sharedHome string) error {
	configPath := filepath.Join(codexHome, "config.toml")
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", configPath, err)
	}

	var cfg struct {
		ModelCatalogJSON string `toml:"model_catalog_json"`
		// 替代 Codex 内置 instructions 的文件。experimental_instructions_file 是
		// Codex 仍在接受的废弃别名，改名之前写的配置也要能读。
		ModelInstructionsFile        string `toml:"model_instructions_file"`
		ExperimentalInstructionsFile string `toml:"experimental_instructions_file"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse %s: %w", configPath, err)
	}

	for _, ref := range []struct{ key, path string }{
		{"model_catalog_json", cfg.ModelCatalogJSON},
		{"model_instructions_file", cfg.ModelInstructionsFile},
		{"experimental_instructions_file", cfg.ExperimentalInstructionsFile},
	} {
		if err := syncCodexReferencedFile(codexHome, sharedHome, ref.key, ref.path); err != nil {
			return err
		}
	}
	return nil
}

// syncCodexReferencedFile 把一个被配置引用的文件从共享 Codex home 复制进任务 home
// 的同一相对位置，使复制过去的 config.toml 在 CODEX_HOME 被搬迁后仍能继续解析。
//
// 绝对路径与以 ~ 为根的取值保持原样：它们指向与任务同一台宿主机，Codex 直接读即可，
// 复制反而有提供陈旧快照的风险。
//
// 目标侧写入以 codexHome 为根做限定（见 materialiseInCodexHome）：filepath.IsLocal
// 只能证明配置串没有词法上的 `..`，而被复用的任务 home 对其上一个任务可写，所以除
// 了根限定的 mkdir/remove/write，没有任何东西能保证 daemon 留在任务 home 内。
//
// 源侧刻意保持普通路径语义：相对路径在共享 Codex home 内解析、那里的 symlink 被跟随。
// 那个目录是用户自己的配置、文件只被读取，而本宿主机上的任务本就能读 daemon 用户
// 能读的一切——所以从 ~/.codex 链出去并不授予任务本没有的访问权。
//
// 每次 prepare 都会刷新复制，因此复用任务 home 会拾取「已编辑过的源」，而不是守着陈
// 旧的 instruction 文件。某个键后来被删除或改指后，旧复制会无人引用地留在原地；Codex
// 永远不会读它，因为配置已不再点名它。
func syncCodexReferencedFile(codexHome, sharedHome, key, configValue string) error {
	referencedPath := strings.TrimSpace(configValue)
	if referencedPath == "" {
		return nil
	}

	src, err := resolveCodexConfigPath(referencedPath, sharedHome, key)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if os.IsNotExist(err) {
		return fmt.Errorf("%s %q resolved to missing file %s: %w", key, referencedPath, src, err)
	}
	if err != nil {
		// 不是确定的缺失——权限或 IO 失败不能被报成「文件不在」，那会把运维人员引向
		// 错误的问题。
		return fmt.Errorf("%s %q: cannot stat source %s: %w", key, referencedPath, src, err)
	}

	if filepath.IsAbs(referencedPath) || strings.HasPrefix(referencedPath, "~") {
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s %q resolved to %s, which is not a regular file", key, referencedPath, src)
	}
	cleanReferencedPath := filepath.Clean(referencedPath)
	if !filepath.IsLocal(cleanReferencedPath) {
		return fmt.Errorf("%s %q must be a local relative path or an absolute path", key, referencedPath)
	}
	return materialiseInCodexHome(codexHome, cleanReferencedPath, src, key)
}

// openVerifiedCodexHomeRoot 把 codexHome 打开成约束根（confinement root），并且只在
// 「打开后的目录被证明仍是 codexHome」后才返回它。
//
// 「先查路径、再开路径」是不够的：os.OpenRoot 会解析给它的路径，所以若在检查与打开
// 之间目录被换成指向别处的链接，得到的 root 会「愉快地被约束」——约束到错误的树。
// 之后每个 root 限定的写入都会落在任务 home 之外，却从不「逃出」它的 root。
//
// 这个窗口在这里不是假设性的。任务 home 会被复用，而 Windows 上只有直接的 Codex
// 进程被终止——后代进程的清理无法确认（见 server/pkg/agent/proc_windows.go）——
// 所以一个记得自己旧 CODEX_HOME 的遗留进程仍能对它动手。任何本机进程都能在其它
// 平台制造同一窗口。
//
// 因此身份校验绑定在句柄而非路径上：把打开的目录与 codexHome 的一次 no-follow
// stat 比对。打开前的替换会在这里失败（symlink，或该路径上是另一个目录）；打开后的
// 替换无关紧要，因为下游一切都用这个句柄而不是路径。
//
// 范围：它覆盖下面「配置引用的复制」。prepareCodexHomeWithOpts 更早的步骤仍按路径
// 寻址任务 home，所以「整个 prepare 对 symlink 化的任务 home 都安全」尚未成立——
// 那项改造记录在 MUL-5647。
func openVerifiedCodexHomeRoot(codexHome, key string) (*os.Root, error) {
	root, err := os.OpenRoot(codexHome)
	if err != nil {
		return nil, fmt.Errorf("open codex home %s: %w", codexHome, err)
	}
	if err := verifyCodexHomeRoot(root, codexHome, key); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

// verifyCodexHomeRoot 证明 root 正是 codexHome 现在指的那个目录：不是经由 symlink
// 到达，且与 os.Lstat 在该路径上看到的同一目录。它与 openVerifiedCodexHomeRoot 分开，
// 使「替换」场景能确定性测试而不必竞态。
func verifyCodexHomeRoot(root *os.Root, codexHome, key string) error {
	opened, err := root.Stat(".")
	if err != nil {
		return fmt.Errorf("stat opened codex home %s: %w", codexHome, err)
	}
	current, err := os.Lstat(codexHome)
	if err != nil {
		return fmt.Errorf("stat codex home %s: %w", codexHome, err)
	}
	if current.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("codex home %s is a symlink; refusing to write %s through it", codexHome, key)
	}
	if !os.SameFile(opened, current) {
		return fmt.Errorf("codex home %s was replaced while opening it; refusing to write %s through it", codexHome, key)
	}
	return nil
}

// materialiseInCodexHome 用 root 限定操作把 src 写进 codexHome 内的 relPath，使任务
// home 之下任何 symlink 都无法把 daemon 的 mkdir、remove 或 write 重定向到 home 之外。
//
// 这很重要，因为任务 home 会被复用：一次 prepare 可能作用于上一个任务已写过的目录。
// 没有 root 时，一个把自己 home 的某个中间目录换成「指向别处的链接」的任务，会让
// daemon 在下次 prepare 时删除并覆写链接目标。os.Root 仍允许停留在任务 home 内的
// 链接（无害），拒绝离开它的。root 本身的身份由 openVerifiedCodexHomeRoot 校验。
func materialiseInCodexHome(codexHome, relPath, src, key string) error {
	root, err := openVerifiedCodexHomeRoot(codexHome, key)
	if err != nil {
		return err
	}
	defer root.Close()

	if dir := filepath.Dir(relPath); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s directory %s: %w", key, dir, err)
		}
	}
	if _, err := root.Lstat(relPath); err == nil {
		if err := root.Remove(relPath); err != nil {
			return fmt.Errorf("remove stale %s copy %s: %w", key, relPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s copy %s: %w", key, relPath, err)
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s %s: %w", key, src, err)
	}
	defer in.Close()

	out, err := root.OpenFile(relPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create %s copy %s: %w", key, relPath, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s %s to %s: %w", key, src, relPath, err)
	}
	// 显式 Close，让延迟到函数返回后的写回失败不被吞掉；随后 defer 中的 Close 变成
	// 空操作。
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s copy %s: %w", key, relPath, err)
	}
	return nil
}

// syncCodexModelsCache 为全新任务 home 播种一次 models_cache.json，并把它绑定到共享
// 的 provider/目录配置。Codex 启动后可能替换任务本地缓存，因此「绑定未变」保留任务
// 最后写入的内容，而不是恢复一份可能陈旧的共享副本。
//
// 绑定缺失或变化会让既有缓存不安全：Codex 的缓存格式（截至 0.144.x）记录客户端版本
// 与抓取时间，却不记录 provider 身份。config.toml 切换 provider 后复用该缓存，可能
// 把 provider B 与 provider A 的模型目录配到一起。丢掉它，让 Codex 为新生效配置抓取
// 目录。此情形刻意不为共享缓存播种，因为那份缓存同样存在 provider 身份歧义。
func syncCodexModelsCache(codexHome, sharedHome string, freshHome bool) error {
	fingerprint, err := codexModelsCacheConfigFingerprint(sharedHome)
	if err != nil {
		return err
	}

	bindingPath := filepath.Join(codexHome, codexModelsCacheBindingFile)
	previous, bound, err := readCodexModelsCacheBinding(bindingPath)
	if err != nil {
		return err
	}

	cachePath := filepath.Join(codexHome, codexModelsCacheFile)
	cacheInfo, cacheErr := os.Lstat(cachePath)
	cacheExists := cacheErr == nil
	if cacheErr != nil && !os.IsNotExist(cacheErr) {
		return fmt.Errorf("stat codex models cache %s: %w", cachePath, cacheErr)
	}

	if bound && previous == fingerprint {
		// 缓存属于当前配置。既保留任务刷新过的既有缓存，也保留一次失败抓取后「有意
		// 的缺失」；复用时播种可能重新引入一份无绑定的共享快照。
		if cacheExists && !cacheInfo.Mode().IsRegular() {
			if err := os.RemoveAll(cachePath); err != nil {
				return fmt.Errorf("remove non-regular codex models cache %s: %w", cachePath, err)
			}
		}
		return nil
	}

	if cacheExists {
		if err := os.RemoveAll(cachePath); err != nil {
			return fmt.Errorf("remove unbound codex models cache %s: %w", cachePath, err)
		}
	}

	if freshHome && !bound && !cacheExists {
		// 共享快照只在「任务 home 本身还不存在」的那条路径上有用；此后任务本地刷新
		// 保持隔离。没有绑定的既有旧 home 从不播种，因为即便它的缓存缺失，其先前生效
		// 的配置也未知。
		if err := seedCopiedFile(filepath.Join(sharedHome, codexModelsCacheFile), cachePath); err != nil {
			return fmt.Errorf("seed codex models cache: %w", err)
		}
	}

	if err := writeCodexModelsCacheBinding(bindingPath, fingerprint); err != nil {
		return err
	}
	return nil
}

// codexModelsCacheConfigFingerprint 对共享配置文件外加它们引用的任何 model_catalog_json
// 的内容做哈希。摘要存在隔离的任务 home 里；不会持久化任何配置内容或凭据。
func codexModelsCacheConfigFingerprint(sharedHome string) (string, error) {
	h := sha256.New()
	var configTOML []byte

	for _, name := range codexModelsCacheConfigFiles {
		path := filepath.Join(sharedHome, name)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			fmt.Fprintf(h, "%s\x00missing\x00", name)
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read codex model cache config %s: %w", path, err)
		}
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(data))
		_, _ = h.Write(data)
		if name == "config.toml" {
			configTOML = data
		}
	}

	if len(configTOML) > 0 {
		var cfg struct {
			ModelCatalogJSON string `toml:"model_catalog_json"`
		}
		if err := toml.Unmarshal(configTOML, &cfg); err != nil {
			return "", fmt.Errorf("parse codex model cache config %s: %w", filepath.Join(sharedHome, "config.toml"), err)
		}
		catalogPath := strings.TrimSpace(cfg.ModelCatalogJSON)
		if catalogPath != "" {
			resolved, err := resolveCodexConfigPath(catalogPath, sharedHome, "model_catalog_json")
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(resolved)
			if err != nil {
				return "", fmt.Errorf("read model_catalog_json %s: %w", resolved, err)
			}
			fmt.Fprintf(h, "model_catalog_json\x00%d\x00", len(data))
			_, _ = h.Write(data)
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// readCodexModelsCacheBinding 对缺失或非普通文件返回 bound=false。非普通路径会被
// 移除，使被复用的任务无法把之后的绑定写入重定向到其隔离 CODEX_HOME 之外。
func readCodexModelsCacheBinding(path string) (fingerprint string, bound bool, err error) {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("stat codex models cache binding %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		if err := os.RemoveAll(path); err != nil {
			return "", false, fmt.Errorf("remove non-regular codex models cache binding %s: %w", path, err)
		}
		return "", false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("read codex models cache binding %s: %w", path, err)
	}
	return strings.TrimSpace(string(data)), true, nil
}

func writeCodexModelsCacheBinding(path, fingerprint string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove prior codex models cache binding %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create codex models cache binding %s: %w", path, err)
	}
	if _, err := io.WriteString(f, fingerprint+"\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("write codex models cache binding %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close codex models cache binding %s: %w", path, err)
	}
	return nil
}

// resolveCodexConfigPath 把 config.toml 中一个路径值键映射为本宿主机上的源文件。key
// 是路径来源的配置键，只用于塑造诊断信息，让读错误的运维人员知道该修哪项配置。
// 相对路径在共享 Codex home 内解析——那是配置被复制出来的目录，也是 per-task home
// 搬迁 CODEX_HOME 之前 Codex 自己本会使用的基准。
func resolveCodexConfigPath(configPath, sharedHome, key string) (string, error) {
	if filepath.IsAbs(configPath) {
		return filepath.Clean(configPath), nil
	}
	if strings.HasPrefix(configPath, "~/") || strings.HasPrefix(configPath, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve %s %q: user home: %w", key, configPath, err)
		}
		return filepath.Join(home, configPath[2:]), nil
	}
	if strings.HasPrefix(configPath, "~") {
		return "", fmt.Errorf("%s %q uses unsupported ~user expansion", key, configPath)
	}
	return filepath.Join(sharedHome, filepath.Clean(configPath)), nil
}

func exposeSharedCodexPluginCache(codexHome, sharedHome string) error {
	src := filepath.Join(sharedHome, "plugins", "cache")
	dst := filepath.Join(codexHome, "plugins", "cache")
	if err := os.MkdirAll(src, 0o755); err != nil {
		return fmt.Errorf("create shared plugin cache dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create codex plugin dir: %w", err)
	}

	if fi, err := os.Lstat(dst); err == nil {
		isLink := fi.Mode()&os.ModeSymlink != 0
		if isLink {
			if target, readlinkErr := os.Readlink(dst); readlinkErr == nil && target == src {
				return nil
			}
			if err := os.Remove(dst); err != nil {
				return fmt.Errorf("remove stale plugin cache link: %w", err)
			}
		} else {
			if err := os.RemoveAll(dst); err != nil {
				return fmt.Errorf("remove stale plugin cache path: %w", err)
			}
		}
	}

	if err := createDirLink(src, dst); err != nil {
		return fmt.Errorf("expose shared plugin cache: %w", err)
	}
	return nil
}

// ensureSymlink 确保 dst 跟踪 src。src 不存在时为空操作。dst 已是「指向 src」的
// symlink 时也是空操作。其它情况——目标错误或损坏的 symlink、或先前 createFileLink
// 复制回退留下的普通文件——删除 dst 并经由 createFileLink 重建，使 per-task home 不
// 与共享来源漂移。
//
// 「普通文件」分支在 Windows 上很关键：os.Symlink 失败时（无开发者模式/未提权），
// createFileLink 回退为复制文件。没有这步重建，一次陈旧化的 auth.json 将永远收不到
// 共享 ~/.codex/auth.json 的 token 刷新，让 Codex 在环境复用期间困在已撤销的刷新
// token 上（issue #2081）。
func ensureSymlink(src, dst string) error {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil // 源不存在——跳过
	}

	if fi, err := os.Lstat(dst); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(dst); err == nil && target == src {
				return nil // symlink 已指向 src
			}
		}
		// 目标错误的 symlink、损坏的 symlink、或陈旧的普通文件——丢弃它，让
		// createFileLink 从当前 src 重新链/重新复制。
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("remove stale dst %s: %w", dst, err)
		}
	}

	return createFileLink(src, dst)
}

// logCodexAuthState 记录 per-task CODEX_HOME 最终得到的 auth.json 类型——symlink
// （带目标）、普通文件（带大小 + mtime）、或缺失——使追查 refresh_token_reused /
// token_expired 报告的运维人员能立刻看出 per-task home 是在跟踪共享 ~/.codex/auth.json
// 还是漂移成了陈旧的本地副本。
//
// 永不记录文件内容。
func logCodexAuthState(authPath string, logger *slog.Logger) {
	fi, err := os.Lstat(authPath)
	if err != nil {
		logger.Info("execenv: codex auth.json absent", "path", authPath, "error", err)
		return
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(authPath)
		logger.Info("execenv: codex auth.json is symlink", "path", authPath, "target", target)
		return
	}
	logger.Info("execenv: codex auth.json is regular file",
		"path", authPath,
		"size", fi.Size(),
		"mtime", fi.ModTime().UTC(),
	)
}

// （daemon 过去会在这里写一份极简内联配置；如今权威的 sandbox/network 指令位于
// codex_sandbox.go 的 ensureCodexSandboxConfig 渲染的受管块里，可幂等更新而不碰用户
// 管理的键。）

// syncCopiedFile 把 per-task 的 dst 镜像到共享 src 的当前状态，使 per-task 副本在
// 历次 Reuse() 之间跟踪共享来源：
//
//   - src 在、dst 缺：复制 src → dst
//   - src 在、dst 在：丢弃 dst 再复制 src → dst（刷新）
//   - src 缺、dst 在：丢弃 dst（共享来源已删，per-task 陈旧副本不得残留）
//   - src 缺、dst 缺：空操作
//
// MUL-2646 的回归：早先的「不要覆盖」守卫让 per-task 的 config.toml / config.json /
// instructions.md 死死停在首次 Prepare 播种时的快照上。用户在两次运行之间编辑了
// ~/.codex/config.toml——切换活动的 [model_providers.X] base_url、把 env_key 指向
// 刚轮换过的 API key、或直接删掉文件以丢弃某个 provider——却总在会话续接时撞上
// 陈旧的 per-task 副本，Codex 用旧 key 调新 URL（或重放一个用户已从共享配置删掉的
// provider）。
//
// 对 config.toml：后续的 ensureCodex{Sandbox,MultiAgent,Memory}Config 在共享来源消失
// 时会从零重建文件，因此 per-task home 保留 daemon 管理的默认值，但丢掉共享配置里
// 已不存在的每条用户管理 [model_providers.X] / model_provider 行。config.json /
// instructions.md 没有 daemon 管理的默认值，所以它们与共享来源同步消失。
func syncCopiedFile(src, dst string) error {
	_, srcErr := os.Stat(src)
	srcMissing := os.IsNotExist(srcErr)
	if srcErr != nil && !srcMissing {
		return fmt.Errorf("stat src %s: %w", src, srcErr)
	}

	if _, err := os.Lstat(dst); err == nil {
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("remove stale dst %s: %w", dst, err)
		}
	}

	if srcMissing {
		return nil
	}
	return copyFile(src, dst)
}

// seedCopiedFile 只在 dst 没有任务本地普通文件时复制 src。与 syncCopiedFile 不同，
// 它绝不覆盖或移除上一轮刷新过的缓存。非普通目标会被防御性移除，使被复用的任务无法
// 把缓存路径变成指向其隔离 CODEX_HOME 之外的链接。
func seedCopiedFile(src, dst string) error {
	if fi, err := os.Lstat(dst); err == nil {
		if fi.Mode().IsRegular() {
			return nil
		}
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("remove non-regular dst %s: %w", dst, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat dst %s: %w", dst, err)
	}

	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat src %s: %w", src, err)
	}
	return copyFile(src, dst)
}

// copyFile 无条件把 src 复制到 dst。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s → %s: %w", src, dst, err)
	}
	return nil
}
