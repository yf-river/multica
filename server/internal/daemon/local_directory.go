package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// localDirectoryResourceType 是 project_resource 的判别字段值：daemon 用它判断一个
// 任务是否应跑在用户现有目录里，而不是新建 git worktree。镜像服务端同名常量——若类型
// 字符串被改名，这里要同步。
const localDirectoryResourceType = "local_directory"

// local_directory 资源的执行模式。镜像 handler/project_resource.go 里的服务端常量——
// 保持同步。缺省或空值意味着 in_place，因此在 worktree 模式出现之前创建的资源保持
// 原有行为。
const (
	localDirectoryModeInPlace  = "in_place"
	localDirectoryModeWorktree = "worktree"
)

// localDirectoryRef 镜像服务端 local_directory 项目资源的 ref 形状。定义在本地的
// 原因：daemon 不必 import 服务端 handler 包。
type localDirectoryRef struct {
	LocalPath     string `json:"local_path"`
	DaemonID      string `json:"daemon_id"`
	Label         string `json:"label,omitempty"`
	ExecutionMode string `json:"execution_mode,omitempty"`
}

// localDirectoryAssignment 是一个任务的 local_directory 资源解析后的视图：daemon
// 将用作 agent workdir 的绝对路径，外加底层 ref（给仍需要原始 label / daemon id 的
// 调用方，主要是校验日志）。RealPath 是符号链接解析后的绝对路径；路径互斥锁以它为
// 键，让“同一目录的两种不同来路”串行化。
type localDirectoryAssignment struct {
	Ref      localDirectoryRef
	AbsPath  string // 用户给的路径，已 Clean 但未做符号链接解析
	RealPath string // 路径互斥锁的规范键
}

// UsesWorktree 报告该 assignment 是否让每个任务跑在各自的 git worktree 里而非用户的
// 目录里。worktree 任务完全跳过 per-path 互斥锁——这正是该模式的意义——所以每个做
// 串行化、清理 sidecar、或把 env root 从 GC 豁免的调用方都必须按它分支，而不是按
// “是否存在 local_directory assignment”分支。
func (a *localDirectoryAssignment) UsesWorktree() bool {
	return a != nil && strings.TrimSpace(a.Ref.ExecutionMode) == localDirectoryModeWorktree
}

// DisplayName 是该目录面向人类的展示名，可安全渲染到 UI。它刻意*不是*绝对路径：由它
// 拼出的等待原因是存到服务端、推给会话里每个客户端的，而聊天记录里的一枚 chip 最终会
// 出现在屏幕共享和截图里。绝对路径带着账号名——这正是同类的 work_dir chip 存在
// handler.relativeWorkDir 的原因；这里是同一条契约、同一个理由。
//
// 用户给资源设了 label 时 label 优先——那是用户为这个目录起的名字。否则用 basename，
// 它在不点名父目录的前提下能区分兄弟检出（"NuvioTV" vs "multica"）。
func (a *localDirectoryAssignment) DisplayName() string {
	if a == nil {
		return ""
	}
	if label := strings.TrimSpace(a.Ref.Label); label != "" {
		return label
	}
	return filepath.Base(a.AbsPath)
}

// ValidateExecutionMode 拒绝本 daemon 未实现的模式。
//
// 回退成 in_place 是错误方向，即便它是更老、更保守的代码路径。execution_mode 是用户
// 索取*隔离*的手段，不只是并发控制：悄悄改跑 in_place，等于让 agent 编辑用户明确要求
// 它别碰的工作副本。丢并发只是麻烦；无视“别碰我的文件”是食言。因此无法识别的模式会让
// 任务失败，报错信息点名版本错位。
func (a *localDirectoryAssignment) ValidateExecutionMode() error {
	if a == nil {
		return nil
	}
	switch strings.TrimSpace(a.Ref.ExecutionMode) {
	case "", localDirectoryModeInPlace, localDirectoryModeWorktree:
		return nil
	default:
		return fmt.Errorf(
			"local_directory: this daemon does not support execution_mode %q for %q "+
				"(update the daemon, or set the resource's execution mode to %q or %q); "+
				"refusing to run in place, since that would modify a directory the resource asked to isolate",
			a.Ref.ExecutionMode, a.AbsPath, localDirectoryModeInPlace, localDirectoryModeWorktree)
	}
}

// localDirectoryAssignmentForTask 返回一个任务应在其内部执行的 local_directory
// assignment。小队队长任务（squad leader）是协调者：它们可能创建子 issue 或评论，但
// 不该绑定用户的仓库 worktree，也不该在下游 worker 准备写入时攥着路径互斥锁。
//
// 它只回答“任务跑在哪”。其结果还驱动 agent 的工作目录（daemon.runTask 把 AbsPath
// 接进 execenv.PrepareParams.LocalWorkDir）与“把用户自有路径从 env-root 清理中豁免”
// 的 GC-meta 印记。任务是否另外拿 per-path 互斥锁是*另一个*问题，由
// localDirectoryLockExempt 回答——把两者混为一谈曾让一个只读的 chat 回合排队等一场
// 20 分钟的构建（issue #7344）；而为了放锁在这里答“没有 assignment”，则会悄悄把
// chat 一并移出用户目录。
func localDirectoryAssignmentForTask(task Task, daemonID string) (*localDirectoryAssignment, error) {
	if task.IsLeaderTask {
		return nil, nil
	}
	return findLocalDirectoryAssignment(task.ProjectResources, daemonID)
}

// localDirectoryLockExempt 报告一个任务是否可以“不串行在 per-path 互斥锁上”跑在
// in_place local_directory 里。它只会在 assignment 已解析并校验后才会被问，所以豁免
// 的任务仍跑在用户目录里——只是不为它排队。
//
// 互斥锁真正保护的东西：两个长时间编码运行把两套编辑交织进同一棵工作树。它从来不是
// 通用写屏障、也不可能变成——用户自己的编辑器、终端和任何外部脚本都在往同一棵树
// 不同步地写，而且向来如此。所以任务要赢得锁必须回答的问题不是“它会不会写？”（什么
// 都可能写），而是“它是第二个重量级写者吗？”。
//
// 一次 chat 回合不是。它是为回答问题而读树、至多像用户自己按 Cmd+S 那样存个文件的
// 对话——正是这把锁早已声明不覆盖的风险等级。为它做串行化什么也买不到，只会让小队
// 队长在每场构建期间全程哑火（issue #7344）。
//
// 以 ChatSessionID 为键，因为那是 daemon 唯一的 chat 判别字段（见
// Task.ChatSessionID）。IsLeaderTask 在这里派不上用场，尽管上面的注释恰好描述了 chat
// 的语义：服务器在 chat 任务插入路径上从不写那一列（service.EnqueueChatTask →
// db.CreateChatTaskParams 没有该字段），因此它在这条线路上发出的每个 chat 回合上都
// 是 false。
func localDirectoryLockExempt(task Task) bool {
	return task.ChatSessionID != ""
}

// findLocalDirectoryAssignment 扫描任务的 project resources，找一个类型为
// local_directory、daemon_id 与本 daemon 匹配的资源。不存在这样的资源时返回 nil
// （无错误）——任务走常规 github_repo / worktree 代码路径。只有匹配的资源结构损坏
// （JSON 非法、字段缺失）或“不止一个资源钉到本 daemon”时才返回错误——后者违背服务端
// 不变量，而悄悄取第一个匹配会让 agent 写进用户没打算给的任意目录。
//
// 服务端 `findLocalDirectoryConflict` 强制每个 (project, daemon) 最多一个
// local_directory，因此这里出现两个匹配，说明要么约束被绕过（更老的 API 客户端），
// 要么数据被弄坏了。无论哪种都快速失败，而不是靠猜。
func findLocalDirectoryAssignment(resources []ProjectResourceData, daemonID string) (*localDirectoryAssignment, error) {
	var match *localDirectoryAssignment
	for _, r := range resources {
		if r.ResourceType != localDirectoryResourceType {
			continue
		}
		var ref localDirectoryRef
		if err := json.Unmarshal(r.ResourceRef, &ref); err != nil {
			return nil, fmt.Errorf("local_directory: parse resource_ref: %w", err)
		}
		ref.DaemonID = strings.TrimSpace(ref.DaemonID)
		if ref.DaemonID == "" {
			return nil, errors.New("local_directory: resource_ref missing daemon_id")
		}
		if ref.DaemonID != daemonID {
			// 该资源属于另一个 daemon。静默跳过；项目可能有多条 local_directory
			// 资源、每个 daemon 一条，各 daemon 会解析自己的那行。
			continue
		}
		if match != nil {
			// 服务端不变量：每个 (project, daemon) 至多一条 local_directory。这里出现
			// 两个匹配，说明约束被更老的 API 客户端或直接写库绕过。无论哪种，都拒绝
			// 猜测用户指的是哪个目录。
			return nil, fmt.Errorf(
				"local_directory: project has multiple local_directory resources for this daemon (%q and %q); remove the extra in project settings",
				match.AbsPath,
				strings.TrimSpace(ref.LocalPath),
			)
		}
		absPath, err := normalizeLocalPath(ref.LocalPath)
		if err != nil {
			return nil, err
		}
		realPath, err := resolveRealPath(absPath)
		if err != nil {
			return nil, err
		}
		match = &localDirectoryAssignment{
			Ref:      ref,
			AbsPath:  absPath,
			RealPath: realPath,
		}
	}
	return match, nil
}

// normalizeLocalPath 去掉空白并把路径解析成“绝对且 Clean 过”的形态。它*不*碰文件
// 系统（不做符号链接解析、不查存在性）——调用方随后经 validateLocalPath 单独做这些。
func normalizeLocalPath(p string) (string, error) {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return "", errors.New("local_directory: local_path is empty")
	}
	if !filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("local_directory: local_path must be absolute, got %q", trimmed)
	}
	return filepath.Clean(trimmed), nil
}

// resolveRealPath 返回 path 的符号链接解析后的绝对形态。路径互斥锁以该值为键，因此
// 跑在 `/Users/u/proj` 的任务与跑在 `/private/var/folders/.../proj-symlink →
// /Users/u/proj` 的任务会折叠到同一把锁。EvalSymlinks 失败时（路径缺失或还不是真的
// 链接），回退到 Clean 过的绝对形态，让调用方仍能走到会报出更清晰错误的“存在性检查”
// 阶段。
func resolveRealPath(absPath string) (string, error) {
	real, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// validateLocalPath 会用更好的上下文把底层错误呈现出来；作为互斥锁键，Clean
		// 过的绝对路径是安全回退（只是在坏符号链接上去重能力稍弱）。
		return absPath, nil
	}
	return real, nil
}

// validateLocalPath 强制“对着用户给定目录跑 agent”的 daemon 侧前置条件：
//
//   - 路径是绝对路径，且不在系统黑名单里（root、$HOME、/Users、/home、当前用户的
//     $HOME——选其中任何一个等于把 agent 的作用域扩到整个账号，绝不是用户本意）；
//   - 符号链接解析后的目标*也*不在黑名单里——没有这一条，一条
//     /Users/me/proj/home -> /Users/me 的链接就能绕开上面的字面相等检查，同时仍把
//     daemon 的所有写入导进 $HOME；
//   - 路径存在、是目录（不是普通文件或设备）；
//   - daemon 进程能在其中读和写（agent 两者都需要——读用于发现上下文，写用于本次
//     issue 的编辑）。
//
// 每次失败都返回带类型的错误消息，daemon 可以原样转发到任务的失败评论里。
func validateLocalPath(absPath string) error {
	if absPath == "" {
		return errors.New("local_directory: local_path is empty")
	}
	if !filepath.IsAbs(absPath) {
		return fmt.Errorf("local_directory: local_path must be absolute, got %q", absPath)
	}
	if reason, blocked := isBlacklistedLocalPath(absPath); blocked {
		return fmt.Errorf("local_directory: %s (%q)", reason, absPath)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("local_directory: path does not exist: %q", absPath)
		}
		return fmt.Errorf("local_directory: stat %q: %w", absPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("local_directory: path is not a directory: %q", absPath)
	}
	// 解析符号链接后再查一次黑名单。即使 absPath 本身干净，字面检查仍有两条绕法：
	//
	//   1. 用户建的符号链接（或某个父组件）把写入导进被禁目标。例子：
	//      ~/proj/home-link -> /Users/me。
	//   2. 用户直接选了某条“经 OS 级符号链接与被禁根目录互为别名”的规范 OS 路径。
	//      macOS 上的例子：输入 /private/tmp 能绕过 /tmp 条目，因为字面串对不上，
	//      而输入已是规范形，EvalSymlinks 是无操作。这条必须无条件检查——不能以
	//      realPath != absPath 为前提——否则“直接打规范形”的情况会被悄悄放行。
	//
	// EvalSymlinks 会连中间组件一起解析，所以“absPath 非链接但其父目录是链接”也会
	// 闭合失败。
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return fmt.Errorf("local_directory: resolve symlinks for %q: %w", absPath, err)
	}
	realPath = filepath.Clean(realPath)
	if reason, blocked := isBlacklistedRealPath(realPath); blocked {
		if realPath != filepath.Clean(absPath) {
			return fmt.Errorf("local_directory: %s (symlink target of %q is %q)", reason, absPath, realPath)
		}
		return fmt.Errorf("local_directory: %s (canonical path %q)", reason, absPath)
	}
	if err := checkDirReadWrite(absPath); err != nil {
		return fmt.Errorf("local_directory: %w", err)
	}
	return nil
}

// isBlacklistedLocalPath 拒绝“映射到整台机器或整个用户 profile”的路径。意图是防止
// daemon 把上下文文件（.agent_context/、.claude/skills/、.multica/）误打到用户账号或
// 操作系统的根上——UI 侧的误配置应当快速失败，而不是把用户家目录弄脏。
//
// 检查方式是 Clean() 之后的字面相等，不是前缀包含：`/Users/<user>/code/proj` 下的
// 合法项目应当通过。
func isBlacklistedLocalPath(absPath string) (reason string, blocked bool) {
	cleaned := filepath.Clean(absPath)
	if isDriveRoot(cleaned) {
		return fmt.Sprintf("path is a drive root %q", cleaned), true
	}
	// 用户家目录比通用的系统根列表更具体（Linux 上 /root 两者都是）。先查它，让调用
	// 方即使在家目录恰好也被列为受保护根时，也能拿到有用且稳定的原因。
	if home, err := os.UserHomeDir(); err == nil {
		if cleaned == filepath.Clean(home) {
			return "path is the user's home directory", true
		}
	}
	for _, banned := range systemRootBlacklist() {
		if cleaned == banned {
			return fmt.Sprintf("path is a protected system root %q", banned), true
		}
	}
	return "", false
}

// isBlacklistedRealPath 是 isBlacklistedLocalPath 的“感知规范形”版本。它把解析后的
// realPath 与黑名单各项的解析形做比较，这样 OS 级重定向（特别是 macOS 的 /etc ->
// /private/etc、/tmp -> /private/tmp、/var -> /private/var）就不能用来把候选绕过字面
// 黑名单——无论该重定向是经用户建的符号链接（~/proj/home-link -> /Users/me）到达，
// 还是直接键入规范形（/private/tmp，它与 OS 视角的 /tmp 是同一个）到达。
func isBlacklistedRealPath(realPath string) (reason string, blocked bool) {
	realClean := filepath.Clean(realPath)
	if isDriveRoot(realClean) {
		return fmt.Sprintf("path is a drive root %q", realClean), true
	}
	// 家目录同时出现在通用系统根黑名单里时（例如 /root），优先给出具体的“家目录”
	// 解释。
	if home, err := os.UserHomeDir(); err == nil {
		homeClean := filepath.Clean(home)
		if realClean == homeClean {
			return "path is the user's home directory", true
		}
		if r, err := filepath.EvalSymlinks(home); err == nil {
			if filepath.Clean(r) == realClean {
				return "path is the user's home directory", true
			}
		}
	}
	for _, banned := range systemRootBlacklist() {
		bannedClean := filepath.Clean(banned)
		if realClean == bannedClean {
			return fmt.Sprintf("path is a protected system root %q", banned), true
		}
		if r, err := filepath.EvalSymlinks(banned); err == nil {
			if filepath.Clean(r) == realClean {
				return fmt.Sprintf("path is a protected system root %q", banned), true
			}
		}
	}
	return "", false
}

// isDriveRoot 报告 absPath 是不是某个 Windows 卷的根——`C:\`、`D:\`、……、`Z:\` 任何
// 一个，加上较罕见的 `\\server\share`（filepath.VolumeName 把 UNC 根也当卷）。在非
// Windows 上恒为 false，因为 POSIX 没有盘符概念，而 `/` 已由 systemRootBlacklist
// 覆盖。
//
// 用 filepath.VolumeName 而不是静态枚举盘符：可移动/网络盘可能挂载在任意盘符
// （`G:\`、`H:\`、……），且 Windows 安装越来越喜欢把用户 profile 放到非 C 盘。静态
// 列表（C..F）会全漏掉。
func isDriveRoot(absPath string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	vol := filepath.VolumeName(absPath)
	if vol == "" {
		return false
	}
	// VolumeName 返回不带尾部分隔符的卷（`C:` 或 `\\srv\share`）。盘根 = 卷 + 一个
	// 分隔符（或者，经 filepath.Clean 后，裸卷输入就只剩卷）。
	rest := absPath[len(vol):]
	return rest == "" || rest == `\` || rest == "/"
}

// systemRootBlacklist 返回按 OS 区分的“daemon 绝不允许作为 local_directory 根”的
// 路径列表。POSIX 系统给 `/`、`/Users`、`/home`（外加 macOS 的 `/Users/Shared` 以防
// 万一）；Windows 给 C: 下广为人知的账号/共享树。盘根本身由 isDriveRoot 处理，因此
// 不必枚举 G:\、H:\ 等等。列表刻意保守——宁可多拒——桌面 UI 应当给出永远不会产生
// 这些值的友好选择器。
func systemRootBlacklist() []string {
	if runtime.GOOS == "windows" {
		return []string{`C:\Users`, `C:\ProgramData`, `C:\Program Files`, `C:\Program Files (x86)`, `C:\Windows`}
	}
	return []string{"/", "/Users", "/Users/Shared", "/home", "/root", "/var", "/etc", "/tmp", "/usr", "/opt"}
}

// checkDirReadWrite 验证 daemon 进程既能读目录内容、又能在 dir 里创建/删除探测文件。
// 探测文件名长、带点开头、不易与用户文件撞名；用完立即删除，删除错误忽略（尽力清理
// 就够了——最坏情况只是留下一个用户可无视的 0 字节文件）。
func checkDirReadWrite(dir string) error {
	if _, err := os.ReadDir(dir); err != nil {
		return fmt.Errorf("read %q: %w", dir, err)
	}
	probe, err := os.CreateTemp(dir, ".multica-rwcheck-*")
	if err != nil {
		return fmt.Errorf("write %q: %w", dir, err)
	}
	probePath := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probePath)
	return nil
}

// isGitWorkTree 报告 path 是不是某个 git 仓库的工作树。当用户已把项目指向自己的克隆
// 时，daemon 用这个结果跳过分支/worktree 机制——agent 直接在当前分支上原地操作。
// 任何错误（git 不在 PATH、path 不在仓库里、exec 失败）都返回 false，调用方可以把
// “不是 git 树”与“判断不了”一视同仁：跳过 git 专用路径。
func isGitWorkTree(ctx context.Context, path string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// LocalPathLocker 串行化共享同一磁盘路径的 agent 任务。锁在任务的整个生命周期
// （claim → 写上下文 → agent 执行 → 上报结果）持有，而不只是 agent 执行窗口——
// 因为 daemon 在“任务准备”阶段写的上下文文件与技能 scratch 目录，会与同路径上的
// 兄弟任务竞态。
//
// 实现：map 由 mu 保护、值是按路径的 sync.Mutex。任务当场拿不到锁时，等待者阻塞在
// per-key Mutex 本身上——这让 Go 调度器给出近 FIFO 的行为（对我们的负载足够；issue
// 正文要的是等待队列，不是严格优先级队列）。持有者簿记（当前持有者的任务 id）经
// Holder 暴露，调用方可据此构造 UI 友好的 wait_reason。
type LocalPathLocker struct {
	mu    sync.Mutex
	locks map[string]*pathLockEntry
}

// pathLockEntry 是单条路径的锁状态：mu 串行化该键的持有者，mu2 保护竞争下的
// holderID 读写，holderID 给 UI 提示用，空闲时为空。
type pathLockEntry struct {
	mu       sync.Mutex // 串行化该键的持有者
	mu2      sync.Mutex // 竞争下保护 holderID
	holderID string     // 当前属主，用于 UI 提示；空闲时为空
}

// NewLocalPathLocker 返回一个空锁存器。可安全并发使用。
func NewLocalPathLocker() *LocalPathLocker {
	return &LocalPathLocker{locks: make(map[string]*pathLockEntry)}
}

// Holder 返回当前持有 realPath 锁的任务 id；没有任务持有则返回 ""。用于填充 daemon
// 停放任务时发给服务器的 wait_reason 提示——UI 随之显示“等待 <path>（task <短 id>
// 持有中）”。
func (l *LocalPathLocker) Holder(realPath string) string {
	l.mu.Lock()
	entry, ok := l.locks[realPath]
	l.mu.Unlock()
	if !ok {
		return ""
	}
	entry.mu2.Lock()
	defer entry.mu2.Unlock()
	return entry.holderID
}

// Acquire 代表 taskID 获取 realPath 的锁。锁已被持有时，会以当前持有者 id 调用
// onWait（同步地，在本 goroutine 阻塞之前），让调用方能先把任务翻进服务端的
// waiting_local_directory 状态。onWait 可为 nil，供不需要该副作用的调用方使用。
//
// 返回一个调用方必须调用（通常 defer）的释放函数。释放幂等。
//
// Acquire 可经 ctx 取消。当 goroutine 阻塞等锁期间 ctx 被取消，Acquire 返回 ctx.Err()
// 且*不*取锁。这与“sync.Mutex.Lock 配上带 context 的取消”是同一契约——daemon 关停
// 不会被 per-path 等待队列卡死。
func (l *LocalPathLocker) Acquire(ctx context.Context, realPath, taskID string, onWait func(holder string)) (func(), error) {
	if realPath == "" {
		return nil, errors.New("local_directory: realpath required for lock")
	}
	if taskID == "" {
		return nil, errors.New("local_directory: taskID required for lock")
	}

	l.mu.Lock()
	entry, ok := l.locks[realPath]
	if !ok {
		entry = &pathLockEntry{}
		l.locks[realPath] = entry
	}
	l.mu.Unlock()

	// 先试快路径——不分配、不起等待 goroutine。
	if entry.mu.TryLock() {
		entry.mu2.Lock()
		entry.holderID = taskID
		entry.mu2.Unlock()
		return l.releaser(realPath, entry), nil
	}

	// 慢路径：锁被别人持有。先用当前持有者调一次 onWait，让 daemon 盖上服务端等待
	// 状态；然后阻塞到“我们拿到锁”或“ctx 被取消”。
	if onWait != nil {
		entry.mu2.Lock()
		holder := entry.holderID
		entry.mu2.Unlock()
		onWait(holder)
	}

	acquired := make(chan struct{})
	go func() {
		entry.mu.Lock()
		close(acquired)
	}()

	select {
	case <-acquired:
		entry.mu2.Lock()
		entry.holderID = taskID
		entry.mu2.Unlock()
		return l.releaser(realPath, entry), nil
	case <-ctx.Done():
		// 我们输掉了等待——上面那个 goroutine 仍会完成并取到锁。另起一个清理
		// goroutine，让它在 acquire 一返回就立刻释放，免得后来的调用方被幻影持有者
		// 卡住。簿记尽力而为：不设持有者 id，因为本任务从未拥有过这把锁。
		go func() {
			<-acquired
			entry.mu.Unlock()
		}()
		return nil, ctx.Err()
	}
}

// releaser 返回解锁回调。经 once 标志幂等，因此即使调用方在任务结束后已显式释放，
// defer 的释放也安全。
func (l *LocalPathLocker) releaser(realPath string, entry *pathLockEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			entry.mu2.Lock()
			entry.holderID = ""
			entry.mu2.Unlock()
			entry.mu.Unlock()
			// 即使没人排队，也刻意把条目留在 map 里。代价是“每个 daemon 服务过的不同
			// 路径”一条 *pathLockEntry，受“一个 workspace 有多少条 local_directory
			// 项目资源”约束——实际上很小。清理会与“刚查到同一条目、正打算 TryLock”
			// 的兄弟调用方竞态。
			_ = realPath
		})
	}
}

// 部署实例仅能原地写入自己的资源根；外部仓库使用独立 worktree。
func validateEnvironmentWritePath(absPath string) error {
	root := strings.TrimSpace(os.Getenv("MULTICA_ENV_WRITABLE_ROOT"))
	if root == "" {
		return nil
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("environment writable root: %w", err)
	}
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("local_directory: in_place execution must be under this environment's writable root %q; use worktree mode for an external repository", root)
	}
	return nil
}
