package execenv

// 文件导读：本文件为 hermes provider 构建「per-task 的 HERMES_HOME 兼容 overlay」。
//
// Hermes 只从两处发现技能（对照随附 Hermes agent 源码 agent/skill_utils.py 的
// get_all_skills_dirs 核实）：先 <HERMES_HOME>/skills/，再 <HERMES_HOME>/config.yaml
// 里 `skills.external_dirs` 列出的目录（按配置顺序）。它没有 workspace 相对发现，
// 因此 daemon 过去用的通用 .agent_context/skills/ 回退从未被扫描到、workspace 分配
// 的技能静默失效（issue #5242）。
//
// 本文件不做「拿固定白名单重建一个 HERMES_HOME」（那会静默改变不用技能任务的行为，
// 还会丢掉任何不在名单上的 home 状态：插件、OAuth 状态、hooks、SOUL、scripts 以及
// Hermes 未来新增的东西），而是搭一个最小兼容 overlay，且只在 agent 真的绑定了技能
// 时启用（在 Prepare/Reuse 的调用点处把关；无技能的 Hermes 任务保持 HERMES_HOME
// 原样）。overlay 的行为细目见下方 prepareHermesHome 的说明。
//
// 关键策略：镜像 + 派生 + 白名单式任务技能 + 隔离的 memories/state.db——让宿主
// 本地的记忆与会话历史既不漏进任务，也不从任务漏回宿主；所有覆盖条目都写回 per-task
// 目录（见 hermesOverriddenEntries）。共享 ~/.hermes/ 本身永不被修改。

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/multica-ai/multica/server/internal/util"
)

// Hermes 只从恰好两个地方发现技能（对照随附 Hermes agent 源码 agent/skill_utils.py
// 的 get_all_skills_dirs 核实）：先是 home 技能目录 <HERMES_HOME>/skills/，然后
// <HERMES_HOME>/config.yaml 里 `skills.external_dirs` 列出的目录（按配置顺序）。它
// 没有 workspace 相对发现，因此 daemon 以前用的通用 `.agent_context/skills/` 回退从未
// 被扫描过，workspace 分配技能静默地从未生效（issue #5242）。
//
// 与其用一份固定白名单重建 HERMES_HOME——那会静默改变「根本不用技能」任务的行为，
// 还会丢掉任何不在名单上的 home 状态（插件、OAuth 状态、hooks、SOUL、scripts，以及
// Hermes 之后新增的任何东西）——这里改为搭一个最小兼容 overlay，且只在 agent 真的
// 绑定了技能时做（在 Prepare/Reuse 的调用点把关；无技能的 Hermes 任务保持
// HERMES_HOME 原样）。
//
// 该 overlay：
//   - 把共享 ~/.hermes/ 的每个顶层条目都经 symlink 镜像进 per-task home，唯独它要
//     覆盖的条目除外——于是黑名单保持极小，未来新增的 home 状态会被自动继承，而不是
//     被白名单漏掉；
//   - 派生一份 task-local config.yaml，其 `skills.external_dirs` 指向「共享技能目录 +
//     用户既有的 external_dirs」，针对 agent 生效环境做变量展开、归一化成绝对路径
//     （Hermes 按 HERMES_HOME 解析相对 external_dirs，重定向后仍留相对形式等于悄悄
//     改指）——这让用户全局/内置技能以只读方式暴露，而无需复制；
//   - 只用「Multica 绑定的技能」填充 task-local `skills/` 目录，它们优先级最高，因为
//     Hermes 先列出 home 技能目录；
//   - 让 `memories/` 归 overlay 所有，绝不软链共享 home：Hermes 在那里读写
//     MEMORY.md/USER.md，而 per-agent 记忆是 Multica 产品关注点——宿主本地 Hermes
//     记忆不得漏进任务、任务记忆也不得漏回宿主。overlay 把它链到 agent 的持久存储，
//     让它跨任务、跨 issue 存活（hermes_memory.go）；只在没有适用存储时才退化为全新
//     per-task 目录；
//   - 让 state.db SQLite 会话存储及其日志 sidecar 归 overlay 所有：宿主的对话历史
//     永不链接或复制。state.db 随后链到该对话自己的持久存储（hermes_sessions.go），
//     让多轮对话跨任务存活；只在没有可关联存储、或宿主机无法建链时才保持 task-local
//     文件；
//   - 在派生配置里禁用外部 `memory.provider`，使宿主配置的 Supermemory/Hindsight
//     等后端不会跨任务共享。这是「磁盘 + 外部后端」的记忆隔离；受管、按 agent 归类
//     的记忆后端是另一项未来产品决策。
//
// 共享 ~/.hermes/ 永不被这套设置修改。但注意：镜像条目是「可写」的 symlink，因此若
// Hermes 运行中经某条写入（例如刷新镜像 auth/OAuth 路径下的 token 状态），那次写入
// 确实会到达共享 home——这个传播是有意为之。

// hermesOverriddenEntries 是共享 ~/.hermes/ 里「overlay 提供自己的 task-local 版本、
// 因此不从共享 home 镜像、并在跨 Reuse 对账时保留」的固定顶层条目：
//   - skills/       task-local，只装绑定的技能
//   - config.yaml   派生配置，external_dirs 已绝对化
//   - memories/     链到 agent 的持久存储，与宿主隔离
//   - 下方 marker   记录旧的共享 SQLite 状态已被摘除
//
// state.db 这一 SQLite 家族由 isHermesOverlayOwnedEntry 动态分类，使每个日志 sidecar
// 也保持 task-local。共享 home 里其它一切都被泛化镜像。
//
// active_profile 与 profiles 也归 overlay 所有（绝不镜像）：Hermes 启动时读
// <HERMES_HOME>/active_profile，若它点名一个非 default profile，就把 HERMES_HOME
// 重定向到那个真实 profile——那会绕过 overlay 的技能与记忆隔离。让 active_profile
// 不进 overlay，Hermes 找不到它就留在原地。overlay 已从正确的（可能带 profile 的）
// 源 home 播种。
//
// .env 也归 overlay 所有，但与其它条目不同，它是「派生」而非仅仅省略（见
// writeDerivedHermesEnv）：Hermes 会跑 _apply_profile_override() 然后执行
// load_hermes_dotenv()（以 override=True 加载 <HERMES_HOME>/.env）。携带越带
// HERMES_HOME= 的源 .env 会在 argv/粘性 profile 防护之后覆写 overlay 的 HERMES_HOME，
// 把技能发现与记忆重新指回源 home。派生的 overlay .env 保留源凭据/设置，但把
// HERMES_HOME 钉在 overlay 上；即使源没有 .env 也写，使 Hermes 的项目级 .env 回退
// （只在没有用户 .env 加载时以 override=True 生效）也无法搬走 home。
const hermesTaskLocalStateMarker = ".multica-task-local-state-v1"

var hermesOverriddenEntries = map[string]struct{}{
	"skills":                   {},
	"config.yaml":              {},
	"memories":                 {},
	"active_profile":           {},
	"profiles":                 {},
	".env":                     {},
	hermesTaskLocalStateMarker: {},
}

// isHermesOverlayOwnedEntry 报告 name 属于 per-task overlay 而非共享 Hermes home。
// Hermes 的 state.db 是规范会话存储并使用 WAL 模式；分别镜像它的主文件与日志 sidecar
// 可能产生不一致快照、暴露宿主对话历史，且 Windows 上 SQLite 字节范围锁 state.db-shm
// 会失败。
func isHermesOverlayOwnedEntry(name string) bool {
	if _, owned := hermesOverriddenEntries[name]; owned {
		return true
	}
	return isHermesTaskLocalStateEntry(name)
}

// isHermesTaskLocalStateEntry 报告 name 是否为 task-local Hermes SQLite 状态文件
// （state.db 及其 WAL/journal sidecar）。
func isHermesTaskLocalStateEntry(name string) bool {
	return name == "state.db" || strings.HasPrefix(name, "state.db-")
}

// platformDefaultHermesHome 返回 Hermes 的平台原生默认 home：原生 Windows 上为
// %LOCALAPPDATA%\hermes，其它平台为 ~/.hermes——对照
// hermes_constants._get_platform_default_hermes_home。没有 Windows 分支时，一个未设
// HERMES_HOME 的 Windows 用户会从空的 ~/.hermes 播种 overlay，丢掉真实配置/
// auth/全局技能。
func platformDefaultHermesHome() string {
	la, _ := os.LookupEnv("LOCALAPPDATA")
	home, _ := os.UserHomeDir()
	return platformDefaultHermesHomeFor(runtime.GOOS, la, home)
}

// platformDefaultHermesHomeFor 是 platformDefaultHermesHome 的纯核心，拆出来让
// Windows 分支能在非 Windows 宿主上测试。对照 hermes_constants.
// _get_platform_default_hermes_home：Windows 上基准是 %LOCALAPPDATA%，LOCALAPPDATA
// 未设时用 %USERPROFILE%\AppData\Local，末尾加 `hermes`；POSIX 用 ~/.hermes。
func platformDefaultHermesHomeFor(goos, localAppData, userHome string) string {
	if goos == "windows" {
		base := strings.TrimSpace(localAppData)
		if base == "" && userHome != "" {
			base = filepath.Join(userHome, "AppData", "Local")
		}
		if base != "" {
			return filepath.Join(base, "hermes")
		}
	}
	if userHome != "" {
		return filepath.Join(userHome, ".hermes")
	}
	return filepath.Join(os.TempDir(), ".hermes") // 最后保底
}

// hermesProfileNameRe 镜像 Hermes 的 hermes_cli.profiles._PROFILE_ID_RE——profile
// 标识符在磁盘与 argv 上必须具备的形状。
var hermesProfileNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// hermesReservedProfileNames 镜像 hermes_cli.profiles._RESERVED_NAMES：Hermes 的
// validate_profile_name 拒绝的名字（会与安装自身或常见系统二进制冲突）。"default"
// 也在 Hermes 的集合里，但那边有特殊透传——它命名根 home——所以在本检查之前就被
// 处理，不列在这里。
var hermesReservedProfileNames = map[string]struct{}{
	"hermes": {}, "test": {}, "tmp": {}, "root": {}, "sudo": {},
}

// HermesProfileResolution 是解析 Hermes profile 选择的唯一权威结果：seed overlay 的
// 源 home（也是展开 ${HERMES_HOME} 的基准）、该 home 是否必须已存在，以及当选择是
// Hermes 拒绝启动的名字时非 nil 的 Err。
type HermesProfileResolution struct {
	// SourceHome 是构建 overlay 所依据的已解析 HERMES_HOME。它也是 profile 的
	// skills.external_dirs 中 ${HERMES_HOME} 展开成的值，与「原生 Hermes 在加载
	// config.yaml 前先应用 profile 覆盖」一致。
	SourceHome string
	// MustExist 在 SourceHome 缺失时让 overlay 关闭即失败——为具名/profile 范围的源
	// 而设，使拼写错误不会静默地从空目录播种、丢掉用户 auth/config——与 Hermes 自己
	// 对缺失 profile 的 FileNotFoundError sys.exit 一致。
	MustExist bool
	// Err 在选择点名「保留或其它非法 profile」（含空内联 `--profile=` 值）时被设置。
	// 这些情形 Hermes 会 sys.exit(1)，因此 daemon 必须关闭即失败，而不是在默认
	// profile 下启动任务。
	Err error
}

// ResolveHermesProfile 是 Hermes profile 选择的唯一解析契约。给定 agent custom_env
// 的 HERMES_HOME 与已经从 custom_args 解析出的 profile 选择（agent.ParseHermesProfileArgs），
// 它复刻 hermes_cli.main._apply_profile_override + hermes_cli.profiles 的语义：
//
//   - Hermes 根与 get_default_hermes_root 的推导方式一致：显式 custom_env HERMES_HOME，
//     否则进程 HERMES_HOME，否则平台默认；若该 home 本身是 <root>/profiles/<name>，
//     则根取 <root>（profile 永远相对根解析，绝不嵌套）。
//   - 显式 -p/--profile 胜出。否则「已经是 profile 范围」的 HERMES_HOME 原样信任
//     （第 1.5 步），只有连它都不成立时才查粘性的 <root>/active_profile（第 2 步）。
//   - 选中的名字像 normalize_profile_name / validate_profile_name 那样归一化并校验：
//     "default"（不区分大小写）指根 home；空、畸形或保留名是硬错误（设置 Err）。
//   - 合法具名 profile 解析成 <root>/profiles/<name> 且 MustExist。
//
// found/inline 来自已解析选择：found 表示「带值的显式 flag 匹配上了」；inline 区分
// `--profile=<value>` 形式——它的空值必须硬失败，而不能回退到默认。
func ResolveHermesProfile(customEnvHome, name string, found, inline bool) HermesProfileResolution {
	base := strings.TrimSpace(customEnvHome)
	if base == "" {
		base = strings.TrimSpace(os.Getenv("HERMES_HOME"))
	}
	if base == "" {
		base = platformDefaultHermesHome()
	}
	if abs, err := filepath.Abs(base); err == nil {
		base = abs
	}
	root := hermesRootFromHome(base)

	profile := name
	if !found {
		// 第 1.5 步：信任「已经是 profile 范围」的 HERMES_HOME（直接父目录名为
		// "profiles"），不咨询 active_profile。
		if base != "" && filepath.Base(filepath.Dir(base)) == "profiles" {
			return HermesProfileResolution{SourceHome: base, MustExist: true}
		}
		// 第 2 步：尊重粘性的 <root>/active_profile。（Hermes 中仅容器场景的
		// HERMES_S6_SUPERVISED_CHILD 例外不适用于 daemon 任务启动。）没有粘性选择时，
		// base home（根/default）就是源。
		profile = readHermesActiveProfile(root)
		if profile == "" {
			return HermesProfileResolution{SourceHome: base}
		}
	}

	// 显式选择（found）总是被校验——空内联值（`--profile=`）是硬错误而非回退默认——
	// 粘性名字同理，与 Hermes 对两者都调用 resolve_profile_env 一致。
	home, mustExist, err := hermesProfileDir(root, profile)
	if err != nil {
		return HermesProfileResolution{Err: err}
	}
	return HermesProfileResolution{SourceHome: home, MustExist: mustExist}
}

// hermesRootFromHome 复刻 hermes_constants.get_default_hermes_root：profile 级操作的
// 根。若 base 是平台默认或位于其下（普通或 profile 模式），根取平台默认；否则
// （Docker/自定义 home）`<...>/profiles/<name>` 布局以祖父目录为根，任何其它路径以
// 自身为根。
func hermesRootFromHome(base string) string {
	return hermesRootFromHomeFor(base, platformDefaultHermesHome())
}

// hermesRootFromHomeFor 是 hermesRootFromHome 的纯核心，注入原生 home 以便测试。
// 包含性测试对两侧做 symlink 解析（像 get_default_hermes_root 的
// env_path.resolve().relative_to(native_home.resolve())），因此一个软链进
// <native>/profiles/<x> 的 HERMES_HOME 仍以 native 为根。返回的值保持未解析，与
// Hermes 返回 native_home / 原 env_path 的词法祖父一致。
func hermesRootFromHomeFor(base, native string) string {
	if base == "" {
		return native
	}
	if isPathUnder(resolvePathBestEffort(native), resolvePathBestEffort(base)) {
		return native
	}
	if filepath.Base(filepath.Dir(base)) == "profiles" {
		return filepath.Dir(filepath.Dir(base))
	}
	return base
}

// resolvePathBestEffort 像 Python 的 Path.resolve(strict=False) 那样解析 symlink：
// 跟随 p 的已存在前缀中的每个 symlink、把剩余的不存在尾部原样接回，而不是像
// filepath.EvalSymlinks 那样在 p 并不完整存在时直接失败。结果绝对。
func resolvePathBestEffort(p string) string {
	resolved, err := util.ResolveSymlinksBestEffort(p)
	if err != nil {
		return p
	}
	return resolved
}

// isPathUnder 报告 child 是否为 parent 本身或其嵌套后代。
func isPathUnder(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// readHermesActiveProfile 从 <root>/active_profile 返回粘性 profile 名；缺失、
// 不可读、为空或为 "default" 时返回 ""（对照 _apply_profile_override 第 2 步——它
// 会忽略 "default" 粘性）。
func readHermesActiveProfile(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "active_profile"))
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(data))
	if name == "default" {
		return ""
	}
	return name
}

// hermesProfileDir 相对 root 解析一个 profile 名，复刻 normalize_profile_name +
// validate_profile_name + get_profile_dir。返回 home 目录、该 home 是否必须已存在
// （具名 profile 为 true），或对空/畸形/保留名的错误（Hermes 会 sys.exit(1)）。
func hermesProfileDir(root, name string) (home string, mustExist bool, err error) {
	stripped := strings.TrimSpace(name)
	if stripped == "" {
		return "", false, fmt.Errorf("hermes profile name cannot be empty")
	}
	var canon string
	if strings.EqualFold(stripped, "default") {
		canon = "default"
	} else {
		canon = strings.ToLower(stripped)
	}
	if canon == "default" {
		return root, false, nil // default profile 就是根 home
	}
	if !hermesProfileNameRe.MatchString(canon) {
		return "", false, fmt.Errorf("invalid hermes profile name %q", canon)
	}
	if _, reserved := hermesReservedProfileNames[canon]; reserved {
		return "", false, fmt.Errorf("hermes profile name %q is reserved", canon)
	}
	return filepath.Join(root, "profiles", canon), true, nil
}

// prepareHermesHome 构建上述的 per-task HERMES_HOME 兼容 overlay。daemon 把给定路径
// 作为 HERMES_HOME 导出给 hermes 子进程，使 CLI 原生发现绑定的技能。
//
// 调用方只在 agent 绑定了技能时调用它；每次都是完整重建（镜像对账、config 重新派生、
// 绑定技能重写），使「技能/配置变更后的 Reuse」干净落地。它「关闭即失败」：镜像、
// 派生 config 或绑定技能任一项建立不了，整个 overlay 就不可用，错误向外传播，调用方
// 绝不能对着半成品 home 启动 Hermes。
//
// sourceHome 是要播种的共享 home（daemon 经 ResolveHermesProfile 解析，尊重 agent
// 的 HERMES_HOME/profile）；为空则回退平台默认。sourceMustExist 在源 home 缺失时关闭
// 即失败——为显式具名 profile 而设，使拼写错误不会静默地从空目录播种、丢掉用户
// auth/config。env 是清洗后的有效环境，用来展开 external_dirs 里的 ${VAR}，与 Hermes
// 子进程所见一致。memoryStore 是 agent 的持久记忆存储（execenv.HermesMemoryStorePath），
// memories/ 链到它；为空则本任务 memories/ 保持 task-local。sessionStore 是会话的
// 持久会话存储（execenv.HermesSessionStorePath），state.db 链到它；为空（或宿主机
// 无法建链）则会话数据库保持 task-local。返回的 hermesSessionMount 同时报告挂载了
// 什么、以及存储是否真的持有会话记录，让调用方绝不会对「并不存在的历史」宣称可续接。
func prepareHermesHome(hermesHome, sourceHome string, sourceMustExist bool, workspaceSkills []SkillContextForEnv, env map[string]string, memoryStore, sessionStore string, logger *slog.Logger) (sessions hermesSessionMount, err error) {
	sharedHome := strings.TrimSpace(sourceHome)
	if sharedHome == "" {
		sharedHome = platformDefaultHermesHome()
	}
	if sourceMustExist {
		if fi, err := os.Stat(sharedHome); err != nil || !fi.IsDir() {
			return hermesSessionMount{}, fmt.Errorf("hermes profile home %q not found (create it with `hermes profile create`)", sharedHome)
		}
	}

	if err := os.MkdirAll(hermesHome, 0o700); err != nil {
		return hermesSessionMount{}, fmt.Errorf("create hermes-home dir: %w", err)
	}
	// 复用时也收紧权限——MkdirAll 对已存在目录会保留原模式，而下面派生的 config
	// 可能带内联 api_key 密文。
	if err := os.Chmod(hermesHome, 0o700); err != nil {
		return hermesSessionMount{}, fmt.Errorf("chmod hermes-home dir: %w", err)
	}
	if err := prepareHermesTaskLocalState(hermesHome); err != nil {
		return hermesSessionMount{}, fmt.Errorf("prepare task-local state: %w", err)
	}
	// 会话：把 state.db 链到对话的持久存储，让会话记录活过任务。挂载不了的存储让
	// 数据库保持 task-local——既有行为——而不是让任务失败。见 hermes_sessions.go。
	if sessionStore != "" {
		mounted, err := mountHermesSessionDB(hermesHome, sessionStore, logger)
		if err != nil {
			return hermesSessionMount{}, fmt.Errorf("mount conversation sessions: %w", err)
		}
		sessions = mounted
	}
	// 记忆：把 memories/ 链到 agent 的持久存储使记忆活过任务；没有存储（无可关联的
	// agent，或 profile 目录解析不出）时回退为全新 task-local 目录。见 hermes_memory.go。
	if memoryStore != "" {
		if err := mountHermesMemories(hermesHome, memoryStore, logger); err != nil {
			return hermesSessionMount{}, fmt.Errorf("mount agent memories: %w", err)
		}
	} else if err := detachHermesMemories(hermesHome); err != nil {
		return hermesSessionMount{}, fmt.Errorf("create task memories dir: %w", err)
	}

	if err := mirrorSharedHermesHome(sharedHome, hermesHome, logger); err != nil {
		return hermesSessionMount{}, fmt.Errorf("mirror shared hermes home: %w", err)
	}
	if err := writeDerivedHermesConfig(sharedHome, hermesHome, env, logger); err != nil {
		return hermesSessionMount{}, fmt.Errorf("derive hermes config: %w", err)
	}
	if err := writeDerivedHermesEnv(sharedHome, hermesHome); err != nil {
		return hermesSessionMount{}, fmt.Errorf("derive hermes .env: %w", err)
	}
	if err := writeHermesBoundSkills(hermesHome, workspaceSkills, logger); err != nil {
		return hermesSessionMount{}, err
	}
	return sessions, nil
}

// writeDerivedHermesEnv 写 task-local .env：保留源 home .env 的内容（凭据/设置），
// 去掉任何 HERMES_HOME 赋值，最后追加一条指向 overlay、保证生效的 HERMES_HOME。
// Hermes 在 profile 解析后立即以 override=True 加载 <HERMES_HOME>/.env，没有这步，
// 源 .env 里带外设置的 HERMES_HOME= 会把 home 迁出 overlay（丢掉绑定技能与记忆
// 隔离）。这个文件总会写——即使源没有——使 overlay .env「被加载」，Hermes 的项目
// .env 回退（只在没有用户 .env 加载时以 override=True 生效）也无法搬走 home。用
// 原子替换以 0600 写入，因为它可能含 API-key 密文；复用也会修复先前的权限。
func writeDerivedHermesEnv(sharedHome, hermesHome string) error {
	dst := filepath.Join(hermesHome, ".env")

	var body []byte
	src, err := os.ReadFile(filepath.Join(sharedHome, ".env"))
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read shared .env: %w", err)
		}
	} else {
		body = stripDotenvAssignment(src, "HERMES_HOME")
	}

	var buf strings.Builder
	if len(body) > 0 {
		buf.Write(body)
		if body[len(body)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	// 把 HERMES_HOME 钉在 overlay 上。值用单引号，让 python-dotenv 按字面处理（不做
	// 转义/变量展开）——任务 home 路径在 workspaces root 下可能含空格或其它字符。
	fmt.Fprintf(&buf, "HERMES_HOME='%s'\n", hermesHome)

	return writeFileAtomic(dst, []byte(buf.String()), 0o600)
}

// stripDotenvAssignment 去掉 .env 文件里每一行对 key 的赋值，按 python-dotenv 的方式
// 容忍前导 `export ` 与周围空白。其它行（含注释与空行）逐字保留。
func stripDotenvAssignment(content []byte, key string) []byte {
	lines := strings.Split(string(content), "\n")
	out := lines[:0]
	for _, line := range lines {
		if dotenvLineKey(line) == key {
			continue
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}

// dotenvLineKey 返回 .env 某行赋值的变量名；注释/空行/非赋值行返回 ""。
func dotenvLineKey(line string) string {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return ""
	}
	if rest := strings.TrimPrefix(s, "export"); rest != s && rest != "" &&
		(rest[0] == ' ' || rest[0] == '\t') {
		s = strings.TrimSpace(rest)
	}
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		return ""
	}
	return strings.TrimSpace(s[:eq])
}

// mirrorSharedHermesHome 把共享 ~/.hermes/ 的每个顶层条目软链进 per-task home，唯独
// overlay 自有条目除外；随后对账目标目录，让「从共享 home 删掉的条目」（或上次复用
// 遗留的）不会以可读的陈旧状态残留。symlink 与用户真实 home 共享状态（auth/OAuth
// 刷新自动传播，任务临时目录里不留凭据副本）。共享 home 本身从不被写——我们只读它
// 并创建指向它的链接。
func mirrorSharedHermesHome(sharedHome, hermesHome string, logger *slog.Logger) error {
	entries, err := os.ReadDir(sharedHome)
	if err != nil {
		if os.IsNotExist(err) {
			// 没有可镜像的共享 home。派生 config + 绑定技能仍给 Hermes 一个可用的
			// home，所以这件事本身不致命。
			return reconcileMirroredEntries(hermesHome, nil)
		}
		return fmt.Errorf("read shared home: %w", err)
	}
	mirrored := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if isHermesOverlayOwnedEntry(name) {
			continue
		}
		src := filepath.Join(sharedHome, name)
		dst := filepath.Join(hermesHome, name)
		if err := linkSharedHermesEntry(src, dst); err != nil {
			return fmt.Errorf("mirror %s: %w", name, err)
		}
		mirrored[name] = struct{}{}
	}
	return reconcileMirroredEntries(hermesHome, mirrored)
}

// reconcileMirroredEntries 移除既非 overlay 自有、也未从共享 home 镜像的 overlay
// 条目，使「两次运行之间从共享删除的条目」（或 Windows 复制回退留下的）不会作为
// 陈旧状态存活。
func reconcileMirroredEntries(hermesHome string, mirrored map[string]struct{}) error {
	entries, err := os.ReadDir(hermesHome)
	if err != nil {
		return fmt.Errorf("read overlay home: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if isHermesOverlayOwnedEntry(name) {
			continue
		}
		if _, keep := mirrored[name]; keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(hermesHome, name)); err != nil {
			return fmt.Errorf("reconcile stale %s: %w", name, err)
		}
	}
	return nil
}

// prepareHermesTaskLocalState 把更老 daemon 构建的 overlay 从共享 Hermes SQLite 会话
// 存储迁移走。没有这个标记时，state.db 与其 sidecar 可能是 symlink 或独立复制文件；
// 作为 task-local 状态复用二者都不安全。只移除生成 overlay 里的这些条目，然后原子
// 记录迁移。Hermes 会惰性建一份全新数据库；因为标记已存在，之后的 prepare 保留它。
func prepareHermesTaskLocalState(hermesHome string) error {
	marker := filepath.Join(hermesHome, hermesTaskLocalStateMarker)
	if fi, err := os.Lstat(marker); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("state marker is not a regular file: %s", marker)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat state marker: %w", err)
	}

	entries, err := os.ReadDir(hermesHome)
	if err != nil {
		return fmt.Errorf("read overlay home: %w", err)
	}
	for _, entry := range entries {
		if !isHermesTaskLocalStateEntry(entry.Name()) {
			continue
		}
		path := filepath.Join(hermesHome, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove legacy task state %s: %w", path, err)
		}
	}
	return writeFileAtomic(marker, []byte("task-local Hermes state\n"), 0o600)
}

// linkSharedHermesEntry 把 dst → src 做成 symlink，跨 Reuse 幂等：已指向 src 的链接
// 原样保留；其它任何东西删除重建，使 overlay 永不与共享 home 漂移。悬空源（用户
// home 里的坏 symlink）跳过而非失败。目录用 createDirLink、文件用 createFileLink，
// 使 Windows 复制回退与条目类型匹配。
func linkSharedHermesEntry(src, dst string) error {
	if fi, err := os.Lstat(dst); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(dst); err == nil && target == src {
				return nil
			}
		}
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("remove stale %s: %w", dst, err)
		}
	}

	info, err := os.Stat(src) // 跟随链接判断是目录还是文件
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 用户 home 里的悬空源——没有可链接的
		}
		return fmt.Errorf("stat %s: %w", src, err)
	}
	if info.IsDir() {
		return createDirLink(src, dst)
	}
	return createFileLink(src, dst)
}

// writeDerivedHermesConfig 写 task-local config.yaml：用户的配置，其中
// `skills.external_dirs` 设为「既有 external 目录 + 共享 ~/.hermes/skills」，全部绝对
// 化。用户没有配置时仍写一份极简配置，让全局技能经外部根保持可达。配置解析不了时
// 原样复制，保住 auth/模型设置——绑定技能仍会从 task-local skills/ 目录加载，这正
// 是修复要点；只有用户全局技能会缺失。文件以 0600（可含内联 api_key 密文）原子替换
// 写入，复用同时修复先前文件的权限。
func writeDerivedHermesConfig(sharedHome, hermesHome string, env map[string]string, logger *slog.Logger) error {
	srcConfig := filepath.Join(sharedHome, "config.yaml")
	dstConfig := filepath.Join(hermesHome, "config.yaml")

	data, err := os.ReadFile(srcConfig)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read shared config: %w", err)
		}
		// overlay 即将从一个「不带任何 provider 配置」的 home 播种，于是任务只用子进程
		// 环境提供的东西运行、别无其它。这是合理设定（一个随附 Hermes、注入
		// OPENAI_API_KEY 的镜像没有 config.yaml 也能正常工作），所以保持警告而非硬失败
		// ——但当它其实是「源 home 解析错」时，这行是两条路径唯一能分辨的地方，而直到
		// GH #6872 前这里什么都不打印。点名源 home：用户自己的配置在别处，Hermes 自己
		// 的报错（"run `hermes model`"）也说不出在哪。
		if fi, statErr := os.Stat(sharedHome); statErr != nil || !fi.IsDir() {
			logger.Warn("execenv: hermes source home does not exist; this task runs without file-backed provider config or credentials, though the environment may still supply them",
				"source_home", sharedHome, "overlay_home", hermesHome)
		} else {
			logger.Warn("execenv: hermes source home has no config.yaml; this task runs without a configured provider unless the environment supplies one",
				"source_home", sharedHome, "overlay_home", hermesHome)
		}
		doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
		if err := setHermesExternalDirs(doc, computeHermesExternalDirs(sharedHome, nil, env)); err != nil {
			return err
		}
		return marshalYAMLToFile(doc, dstConfig)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		logger.Warn("execenv: hermes-home config parse failed; copying verbatim", "error", err)
		return writeFileAtomic(dstConfig, data, 0o600)
	}
	dirs := computeHermesExternalDirs(sharedHome, existingHermesExternalDirs(&doc), env)
	if err := setHermesExternalDirs(&doc, dirs); err != nil {
		logger.Warn("execenv: hermes-home set external_dirs failed; copying verbatim", "error", err)
		return writeFileAtomic(dstConfig, data, 0o600)
	}
	// 禁用任何宿主配置的外部记忆后端（memory.provider），使 Supermemory/Hindsight
	// 等记忆库不会跨受管任务共享；内置 per-task memories/ 目录已在上文隔离。
	disableHermesMemoryProvider(&doc)
	return marshalYAMLToFile(&doc, dstConfig)
}

// disableHermesMemoryProvider 把派生配置中「skills 邻接的」`memory.provider` 强制为
// 空。Hermes 只在 memory.provider 是非空字符串时激活外部记忆插件（agent/agent_init.py），
// 因此 "" 就是显式关断开关。内置 note/user-profile 记忆不受影响——它写进已隔离的
// per-task memories/ 目录。
func disableHermesMemoryProvider(doc *yaml.Node) {
	top := yamlDocumentRoot(doc)
	if top == nil {
		return
	}
	memory := yamlMapValue(top, "memory")
	if memory == nil || memory.Kind != yaml.MappingNode {
		memory = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		yamlSetMapValue(top, "memory", memory)
	}
	yamlSetMapValue(memory, "provider", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: ""})
}

// computeHermesExternalDirs 把用户既有的 external_dirs 归一化成绝对路径，并把共享
// 技能目录作为只读外部根追加。变量/`~` 展开用清洗后的有效环境（同一张叠到 Hermes
// 子进程上的映射），回退 daemon 进程环境——于是 agent 配置的 `${VAR}` 解析成 Hermes
// 实际会看到的值；daemon 拉黑的变量（如 HOME）解析成进程值而非被丢弃的 custom 值。
// 未知变量「保留」为 `${VAR}`（对照 Hermes/Python 的 expandvars），而不是塌缩成空，
// 因此一条本应在运行时解析的路径不会被悄悄改写。仍含未解析 `${` 的条目原样保留
// （Hermes 稍后会展开它）；否则相对路径相对共享 home 解析，对齐重定向前的行为。
// 顺序保留；重复丢弃。
func computeHermesExternalDirs(sharedHome string, existing []string, env map[string]string) []string {
	expand := func(s string) string {
		return os.Expand(s, func(k string) string {
			if v, ok := env[k]; ok {
				return v
			}
			if v, ok := os.LookupEnv(k); ok {
				return v
			}
			return "${" + k + "}" // 未知变量保留，供运行时展开
		})
	}

	out := make([]string, 0, len(existing)+1)
	seen := make(map[string]struct{}, len(existing)+1)
	add := func(p string) {
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, raw := range existing {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		entry = strings.TrimSpace(expand(entry))
		if entry == "" {
			continue
		}
		// 未解析的 ${VAR} 仍在（上文保留了未知变量）——原样放行，让 Hermes 在运行
		// 时展开解析；这里无法安全决定相对还是绝对。
		if strings.Contains(entry, "${") {
			add(entry)
			continue
		}
		if entry == "~" || strings.HasPrefix(entry, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				entry = filepath.Join(home, strings.TrimPrefix(entry, "~"))
			}
		}
		if !filepath.IsAbs(entry) {
			entry = filepath.Join(sharedHome, entry)
		}
		add(filepath.Clean(entry))
	}
	// 共享技能目录：引用而非复制，使用户全局/内置技能保持可见。它不同于 task-local
	// skills 目录，Hermes 不会把它并进本地根；而且排在最后，撞名时让位于绑定技能。
	add(filepath.Join(sharedHome, "skills"))
	return out
}

// writeHermesBoundSkills 从零重建 task-local skills/ 目录，使上次运行后删除的技能
// 不会残留，然后只写 Multica 绑定的技能。它们保留天然 slug（没有用户技能与此目录
// 共享）并因此优先于任何同名的外部技能。
func writeHermesBoundSkills(hermesHome string, workspaceSkills []SkillContextForEnv, logger *slog.Logger) error {
	skillsDir := filepath.Join(hermesHome, "skills")
	if err := os.RemoveAll(skillsDir); err != nil {
		return fmt.Errorf("clear hermes skills dir: %w", err)
	}
	if len(workspaceSkills) == 0 {
		// 防御：调用方把关在非空集合上，但若这变了也要保持正确——空本地目录只意味
		// 外部根是唯一来源，与未重定向的行为一致。
		return os.MkdirAll(skillsDir, 0o700)
	}
	// 技能位于 env.RootDir/hermes-home 下，会被 GC 循环（云端）或环境拆除
	// （local_directory）整棵清掉——不需要 sidecar manifest。
	return writeSkillFiles(skillsDir, workspaceSkills, nil)
}

// existingHermesExternalDirs 从已解析的配置文档读取原始 skills.external_dirs 条目，
// 接受单字符串或列表（Hermes 两者都接受）。缺失时返回 nil。
func existingHermesExternalDirs(doc *yaml.Node) []string {
	top := yamlDocumentRoot(doc)
	if top == nil {
		return nil
	}
	skills := yamlMapValue(top, "skills")
	ed := yamlMapValue(skills, "external_dirs")
	if ed == nil {
		return nil
	}
	if ed.Kind == yaml.ScalarNode {
		return []string{ed.Value}
	}
	if ed.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]string, 0, len(ed.Content))
	for _, c := range ed.Content {
		if c.Kind == yaml.ScalarNode {
			out = append(out, c.Value)
		}
	}
	return out
}

// setHermesExternalDirs 在配置文档上设置 skills.external_dirs，必要时创建 skills
// 映射，并保留所有其它设置。
func setHermesExternalDirs(doc *yaml.Node, dirs []string) error {
	top := yamlDocumentRoot(doc)
	if top == nil {
		return fmt.Errorf("hermes config: unexpected root node")
	}
	skills := yamlMapValue(top, "skills")
	if skills == nil || skills.Kind != yaml.MappingNode {
		skills = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		yamlSetMapValue(top, "skills", skills)
	}
	yamlSetMapValue(skills, "external_dirs", yamlStringSeq(dirs))
	return nil
}

// yamlDocumentRoot 返回已解析文档的顶层映射节点；形状不是映射时返回 nil。
func yamlDocumentRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	node := doc
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	return node
}

// yamlMapValue 返回映射节点中 key 对应的值节点；没有则返回 nil。
func yamlMapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// yamlSetMapValue 在映射节点中把 key 设为 val；键已存在则原地替换，否则追加。
func yamlSetMapValue(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		val,
	)
}

// yamlStringSeq 构建一个字符串标量的 YAML 序列节点。
func yamlStringSeq(vals []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, v := range vals {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
	}
	return seq
}

// marshalYAMLToFile 把 YAML 节点渲染成 dst 下 0600 的文件（可能含内联密文），经
// 原子替换写入。
func marshalYAMLToFile(doc *yaml.Node, dst string) error {
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal hermes config: %w", err)
	}
	return writeFileAtomic(dst, out, 0o600)
}

// writeFileAtomic 把 data 写到目标目录里一份带给定权限的临时文件，再 rename 覆盖
// dst——读者永远不会看到半个文件，先前文件的宽松权限也被替换掉。
func writeFileAtomic(dst string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".hermes-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", dst, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 之后即为空操作
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp for %s: %w", dst, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp for %s: %w", dst, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp for %s: %w", dst, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("rename temp to %s: %w", dst, err)
	}
	return nil
}
