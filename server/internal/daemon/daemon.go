// Package daemon 是本机 agent 运行时守护进程的核心实现。
//
// daemon.go 是本包的主体：负责与服务器（HTTP + WebSocket）保持长连接，
// 周期性轮询并认领任务（claim），为每个任务准备好隔离的执行环境
// （execenv）、技能包（skill bundle）、仓库缓存与远端 MCP 代理，随后拉起
// 对应的 agent CLI 子进程执行，并把执行过程中的消息、工具调用和最终结果
// 回传给服务器。同时它还要维护 runtime 的注册/心跳/离线恢复、agent CLI
// 版本探测与降级、自动更新与自重启、环境根目录与持久化存储的 GC 防护等
// 生命周期职责。
//
// 并发模型要点：主入口 Run() 启动若干后台循环（workspace 同步、task
// wakeup、heartbeat、GC、auto-update、token 续期），每个 runtime 有独立
// 心跳 goroutine；认领后的任务经由 slot 信号量限流，在独立 goroutine 里
// 执行 handleTask；daemon 进程退出时会对在途任务做最多 30s 的排空。
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/codediff"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/repocache"
	"github.com/multica-ai/multica/server/internal/selfexec"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
	"github.com/multica-ai/multica/server/pkg/skillbundle"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// ErrRepoNotConfigured 由 ensureRepoReady 返回：当服务器刚刷新过后，所请求
// 的仓库 URL 仍不在该工作区的仓库配置里时抛出。
var ErrRepoNotConfigured = errors.New("repo is not configured for this workspace")

// ErrNoRuntimesToRegister 由 registerRuntimesForWorkspace 返回：守护进程在某个
// 工作区上没有任何可托管的运行时——典型场景是"仅自定义"守护进程把唯一启用的
// 自定义运行时 profile 禁用了，导致内置 agent 与可解析的 profile 均为零。
// 调用方必须按意图区分两种情形：初次注册（syncWorkspacesFromAPI 的新工作区分支）
// 把它当成配置错误并跳过该工作区，直到有变化；而 profile 漂移刷新路径
// （refreshWorkspaceRuntimeProfiles）则把它视为合法的收敛终态，会显式反注册
// 已过期的本地 runtime ID，让服务器立即将其标记为离线，而不必等待 150 秒的
// 心跳过期清扫。
var ErrNoRuntimesToRegister = errors.New("no agent runtimes could be registered")

// errNoWorkspaceRuntimesRegistered is returned by syncWorkspacesFromAPI when a
// round left every workspace without a runtime. At startup that is normally
// fatal — a daemon hosting nothing has nothing to do, and failing loudly beats
// idling — which is why it is a sentinel rather than a bare string: exactly one
// caller is allowed to make an exception to it, and only for the one situation
// where the daemon knows the answer is on its way. See
// startupMayProceedWithoutRuntimes.
var errNoWorkspaceRuntimesRegistered = errors.New("failed to register runtimes")

// errTaskPrepareTimeout distinguishes the daemon's dispatched -> running
// startup deadline from provider execution timeouts. handleTask maps it to the
// platform-side timeout failure reason so the server's existing retry path can
// recover the task on a fresh attempt.
var errTaskPrepareTimeout = errors.New("task preparation timed out")

// errSkillBundleUnavailable 标记一个在准备阶段死掉的任务，原因是守护进程无法
// 下载该 agent 的技能包（skill bundle）。把它做成哨兵错误——而不是让 handleTask
// 去模式匹配被包裹的传输错误——是为了让失败能落到平台侧的
// skill_bundle_unavailable 原因，而不是 agent_error.unknown；后者不在服务器的
// 重试白名单上。（MUL-5370）
var errSkillBundleUnavailable = errors.New("skill bundle unavailable")

const (
	// 等待一个可执行任务槽（slot）的最长时间；超过后回到容量退避等待。
	taskSlotWaitTimeout      = 2 * time.Second
	taskSlotCapacityBackoff  = 5 * time.Second
	repoCheckoutModeEnv      = "MULTICA_REPO_CHECKOUT_MODE"
	repoCheckoutModeIsolated = "isolated"
	// defaultTaskPrepareTimeout 是"认领之后、StartTask 成功之前"全过程的硬性
	// 存活上限：runtime 解析、技能包下载、执行环境搭建以及 StartTask 请求本身都
	// 算在内。它刻意与 AgentTimeout 相互独立——后者只约束任务进入 running 之后
	// 的 provider 进程。
	defaultTaskPrepareTimeout = 5 * time.Minute
	// pendingWorkHeartbeatTimeout 约束服务器推送的 daemon:pending_work 提示所
	// 触发的带外心跳（MUL-5444）。故意设得较短：该提示只是延迟优化，一旦本次
	// 尝试失败，定时心跳仍会把请求捞起来。
	pendingWorkHeartbeatTimeout = 15 * time.Second
	// pendingWorkHintBookkeepingTTL 是某个 runtime 的"最近提示时间戳"在被清扫前
	// 的保留时长——纯粹用于让该 map 保持有界。
	pendingWorkHintBookkeepingTTL = 10 * time.Minute
	// idleWatchdogMaxTick 封顶空闲看门狗的轮询间隔。按 window/2 的基础速率计算，
	// 超调量会随预算线性放大：2 小时的窗口可能每小时才检查一次，真正卡住的任务
	// 会占着槽位长达 3 小时。该上限保证最坏检测时间是"窗口 + 5 分钟"，与运维把
	// 预算设得多大无关。
	//
	// 5 分钟是在以小时计的预算之上、值得容忍的最大超调量，而非出于轮询开销的
	// 考量——一次 tick 只是原子读加一次 channel 长度判断，任何间隔下都近乎免费。
	idleWatchdogMaxTick = 5 * time.Minute
)

// pendingWorkHintMinInterval 是同一 runtime 两次提示驱动心跳之间的最短间隔。
// 它既让"首次打开"保持即时，又避免调用方触发的提示变成心跳放大器。
// 做成 var 以便测试缩短它，与本包其它时间旋钮保持一致。
var pendingWorkHintMinInterval = time.Second

// repoCheckoutModeFor 为任务的 `multica repo checkout` 选择 Git 元数据布局。
// 在 Codex 的 workspace-write 沙箱下，链接工作树（linked worktree）的 gitdir
// 会解析进共享缓存并保持只读——即使任务工作目录是显式可写根目录，`git add` /
// `git commit` 也会从 checkout 内部失败。Linux 在 multica-ai/multica#2925 踩到
// 过这个问题，Codex 原生 Windows 沙箱则见 multica-ai/multica#6449。
//
// 两个平台现在都默认 danger-full-access（即 execenv 的 codexSandboxPolicyFor），
// 实际只有主动选择了 windows.sandbox 的用户才会再触发 Windows 场景。布局仍是
// 按平台而不是按策略选择：它要在任务解析出沙箱配置之前就定下来，一个 workdir
// 会被策略可能不同的多个任务复用，且任务本地元数据在任一种策略下都是正确的。
func repoCheckoutModeFor(provider, goos string) string {
	if provider != "codex" {
		return ""
	}
	switch goos {
	case "linux", "windows":
		return repoCheckoutModeIsolated
	default:
		return ""
	}
}

var (
	taskPrepareLeaseRefresh = 15 * time.Second
	taskPrepareLeaseTimeout = 10 * time.Second
	errInvalidTaskIdentity  = errors.New("invalid task identity")
)

func validateTaskIdentity(task Task) error {
	if strings.TrimSpace(task.AgentID) == "" {
		return fmt.Errorf("%w: task %s has no authoritative agent_id", errInvalidTaskIdentity, task.ID)
	}
	if task.Agent == nil {
		return fmt.Errorf("%w: task %s has no agent payload (agent_id=%s)", errInvalidTaskIdentity, task.ID, task.AgentID)
	}
	if task.Agent.ID != task.AgentID {
		return fmt.Errorf("%w: task %s agent_id=%s but agent.id=%s", errInvalidTaskIdentity, task.ID, task.AgentID, task.Agent.ID)
	}
	return nil
}

func taskScopedAuthToken(task Task) (string, error) {
	token := strings.TrimSpace(task.AuthToken)
	if token == "" {
		return "", errors.New("server did not provide task-scoped auth token")
	}
	if !strings.HasPrefix(token, "mat_") {
		return "", errors.New("server provided non-task-scoped auth token")
	}
	return token, nil
}

func taskMulticaEnvironment(task Task, provider, agentName, token, configRoot, workspacesRoot, serverURL string, healthPort, slot int, tempDir string) map[string]string {
	env := map[string]string{
		"MULTICA_TOKEN":        token,
		cli.TaskConfigRootEnv:  configRoot,
		TaskWorkspacesRootEnv:  workspacesRoot,
		"MULTICA_SERVER_URL":   serverURL,
		"MULTICA_DAEMON_PORT":  strconv.Itoa(healthPort),
		"MULTICA_WORKSPACE_ID": task.WorkspaceID,
		"MULTICA_AGENT_NAME":   agentName,
		"MULTICA_AGENT_ID":     task.AgentID,
		"MULTICA_TASK_ID":      task.ID,
		"MULTICA_TASK_SLOT":    strconv.Itoa(slot),
		"TMPDIR":               tempDir,
		"TMP":                  tempDir,
		"TEMP":                 tempDir,
	}
	if runtime.GOOS != "windows" {
		return env
	}
	// MSYS (Git Bash) resolves its `/tmp usertemp` mount from TMP/TEMP once,
	// in the first process, and every live MSYS process of this user shares
	// that mount table — whichever provider started it. A task-private TMP/TEMP
	// would pin /tmp to a directory this daemon deletes when that task ends,
	// breaking /tmp for every later task (#8824). So on Windows no task
	// overrides TMP/TEMP; they inherit the daemon user's host temp, and only
	// TMPDIR stays task-private.
	delete(env, "TMP")
	delete(env, "TEMP")
	if provider == "claude" {
		// Claude Code reads CLAUDE_CODE_TMPDIR for its own temp files; default
		// it to the task dir so they stay private, unless the user set it.
		if os.Getenv("CLAUDE_CODE_TMPDIR") == "" {
			env["CLAUDE_CODE_TMPDIR"] = tempDir
			if task.Agent != nil {
				for key := range task.Agent.CustomEnv {
					if strings.EqualFold(key, "CLAUDE_CODE_TMPDIR") {
						delete(env, "CLAUDE_CODE_TMPDIR")
					}
				}
			}
		}
	}
	return env
}

// taskRunner 执行单个 agent 任务并返回结果。抽成接口是为了让测试注入假实现，
// 无需真正拉起 agent 进程，同时避免把测试脚手架塞进生产结构体。
type taskRunner interface {
	run(ctx context.Context, task Task, provider string, slot int, log *slog.Logger) (TaskResult, error)
}

// taskRunnerFunc 把普通函数适配成 taskRunner 接口。
type taskRunnerFunc func(context.Context, Task, string, int, *slog.Logger) (TaskResult, error)

func (f taskRunnerFunc) run(ctx context.Context, task Task, provider string, slot int, log *slog.Logger) (TaskResult, error) {
	return f(ctx, task, provider, slot, log)
}

type terminalTaskReportKind uint8

const (
	terminalTaskReportComplete terminalTaskReportKind = iota + 1
	terminalTaskReportFail
	terminalTaskReportSuspend

	// CompleteTask/FailTask 会按 defaultTerminalRetrySchedule 里的五次退避做六次
	// 30 秒的 HTTP 尝试（合计 124 秒）。把这个分离回调自己的截止时间定得高于这
	// 个最坏预算，它才不会悄悄缩短客户端既有的重试契约。守护进程重启时 pollLoop
	// 仍另有一套 30 秒的进程排空边界。
	terminalTaskReportTimeout = 6 * time.Minute
)

// terminalTaskReport 是守护进程侧对"终态回调"的唯一表示。让所有 complete/fail
// 路径都收敛到这个值、再统一走 reportTerminalTask，就为"持久化 outbox"只留了
// 一个插入点，以后新增任务退出路径时不必逐个改动。
type terminalTaskReport struct {
	interaction    *protocol.SuspendedInteraction
	kind           terminalTaskReportKind
	taskID         string
	output         string
	codeDiff       *codediff.Diff
	branchName     string
	errorMessage   string
	sessionID      string
	workDir        string
	durableWorkDir string
	failureReason  string
	// sessionRolloutMissing 表示守护进程因该任务在 store 里没有对应 rollout 而扣下
	// 了它的 Codex session（MUL-5305）。服务器会清除 resume 指针，并在下一次认领时
	// 标记这段连续性缺口。
	sessionRolloutMissing bool
	// retiredSessionID 记录本次运行被告知要 resume、却最终判定为不可恢复而放弃的
	// session（GH #6066）。服务器记下它，使后续任何在该 issue 或 chat 上的运行都
	// 不会再选中它——无论还有多少条干净的记录引用着它。
	retiredSessionID string
}

type terminalReportSendFunc func(context.Context, terminalTaskReport, []time.Duration) error

// executionEnvironmentCommand 用于启动可整体终止的执行环境助手进程。
type executionEnvironmentCommand func() ([]string, error)

func defaultExecutionEnvironmentCommand() ([]string, error) {
	// 默认执行环境助手就是 multica 自己的可执行文件 + 一个专用参数，
	// daemon 重启/任务启动时用它派生"隔离环境准备进程"。
	executable, err := resolveSelfExecutable()
	if err != nil {
		return nil, fmt.Errorf("resolve execution-environment helper: %w", err)
	}
	return []string{executable, execenv.PreparationHelperArg}, nil
}

var (
	isBrewInstall         = cli.IsBrewInstall
	getBrewPrefix         = cli.GetBrewPrefix
	matchKnownBrewPrefix  = cli.MatchKnownBrewPrefix
	resolveSelfExecutable = selfexec.Resolve

	// detectAgentVersion / checkAgentMinVersion 是对真实 agent 助手的间接层，
	// 让测试能走注册路径而不必真的拉起 CLI。与上面 brew 助手是同一模式。
	detectAgentVersion   = agent.DetectVersion
	checkAgentMinVersion = agent.CheckMinVersion

	// listModels 是 agent.ListModels 的间接层，让模型发现测试可以在不拉起真实
	// CLI 的情况下断言 daemon 枚举的是哪个可执行文件路径。与 detectAgentVersion
	// 的 hook 模式一致。
	listModels = agent.ListModels

	// lookPath 是 exec.LookPath 的间接层，让注册测试能在不改动进程 PATH 的情况下
	// 解析自定义 runtime profile 命令。与上面 detectAgentVersion hook 一致。
	lookPath = exec.LookPath

	// profilePathExecutable 报告 path 是否指向一个存在、非目录、且至少带一个可执行
	// 位的文件。它是 appendProfileRuntimes 在信任某个按机器配置的命令路径覆盖
	// （MUL-3284）之前的门槛——过期或写错的覆盖必须回退到 PATH 查找，而不是注册
	// 一个根本起不来的 runtime。做成包级变量以便测试断言覆盖优先级时不真的在磁盘上
	// 造可执行文件。
	profilePathExecutable = func(path string) bool {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return false
		}
		return info.Mode().Perm()&0o111 != 0
	}
)

// workspaceState 记录单个工作区已注册的运行时及其上下文。
//
// allowedRepoURLs 覆盖工作区级的仓库绑定；每次从服务器刷新都会重建它。
// taskRepoURLs 覆盖服务器通过单任务认领（claim）浮现出来的仓库（目前是 project
// 的 github_repo 资源，以后可能还有别的类型化来源）——它们不会出现在
// GetWorkspaceRepos 里，如果共用一个 map，刷新时就会被冲掉。
// taskRepoRefs 记录使每个 project 仓库浮现的那个具体任务的 checkout ref，
// 这样两个使用同一 URL 的项目不会把 ref 互相串掉。
type workspaceState struct {
	workspaceID     string
	runtimeIDs      []string
	reposVersion    string // 预留：版本不变时跳过刷新
	allowedRepoURLs map[string]struct{}
	taskRepoURLs    map[string]struct{}
	taskRepoRefs    map[string]map[string]string // taskID -> repo URL -> checkout ref
	settings        json.RawMessage              // 工作区设置（JSONB）
	lastRepoSyncErr string
	repoRefreshMu   contextLock
	// coAuthorPublishMu 串行化该工作区 Co-authored-by 结论的发布。与上面的字段
	// 不同，它不受 Daemon.mu 保护：正是为了能在不持有中央锁的情况下，把"读取
	// 结论 + 写文件"作为一个原子步骤完成。
	coAuthorPublishMu sync.Mutex
	// profileSetSig 是最后一次从服务器看到的工作区自定义运行时 profile 列表的
	// 内容哈希（MUL-3332）。按需刷新时会把实时签名与这个缓存值对比；任何漂移
	// 都会触发重新注册，于是新增（或编辑/禁用）的自定义运行时无需重启守护进程
	// 即可生效。首次成功拉取 profile 之前为空（旧服务器 / 网络抖动）；与结构体
	// 上其它字段一样受 Daemon.mu 保护。
	profileSetSig string
	// builtinVersions 按内置 provider 记录服务器 ACCEPT 的最近一次注册调用所携带
	// 的版本。这是守护进程"服务器知道什么"的按工作区记录——共享的 agentVersions
	// 缓存刻意不是：每条探测路径都会写那个缓存，但每次注册只覆盖它被调用的那些
	// 工作区。refreshAgentVersions 把这个记录与当前探测轮对比，只对落后的工作区
	// 重新注册。把确认范围限定到工作区，正是让并发的旧代注册变得安全的原因：
	// 在升级前探测、在所有工作区都刷新完之后才落地的注册，只会给自己所在工作区
	// 再造一次不匹配，下一轮就会重访。失败的注册不记录任何内容，于是工作区保持
	// 落后并会被重试。受 Daemon.mu 保护。
	builtinVersions map[string]string
}

// contextLock 是一个零值即可用、且等待可被取消的互斥锁。仓库 checkout 请求用它
// 来做工作区刷新合并，这样客户端断连时，处理器不会被堵在另一个冷缓存刷新后面。
type contextLock struct {
	once  sync.Once
	token chan struct{}
}

func (l *contextLock) Lock(ctx context.Context) error {
	// 先检查 ctx 是否已取消，返回真正的原因而非笼统的取消。
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	// 首次调用时初始化容量为 1 的 token channel（放一个令牌 = 未持有锁）。
	l.once.Do(func() {
		l.token = make(chan struct{}, 1)
		l.token <- struct{}{}
	})
	// 等待获取令牌；等待期间 ctx 可取消，避免断连的客户端把处理器堵在锁上。
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-l.token:
		// 抢到令牌后再确认一次 ctx 状态：从 select 醒来与真正持有之间
		// ctx 可能刚好被取消，此时要把令牌还回去（保持锁状态一致）并退出。
		if err := ctx.Err(); err != nil {
			l.token <- struct{}{}
			return context.Cause(ctx)
		}
		return nil
	}
}

func (l *contextLock) Unlock() {
	l.token <- struct{}{}
}

// repoCacheBackend 抽象仓库缓存后端（生产实现是 repocache 包），
// 提供裸仓库查找/同步/加锁与工作树创建能力；测试可用假实现替换。
type repoCacheBackend interface {
	Lookup(workspaceID, url string) string
	BarePath(workspaceID, url string) string
	Sync(workspaceID string, repos []repocache.RepoInfo) error
	WithRepoLock(barePath string, fn func() error) error
	CreateWorktree(params repocache.WorktreeParams) (*repocache.WorktreeResult, error)
}

// Daemon 是本机的 agent 运行时守护进程：轮询服务器、认领任务并驱动执行。
// 它持有全部工作区状态、runtime 索引、并发控制原语与各类缓存，是
// handleTask / runTask 以及各后台循环（心跳、同步、GC、自动更新）共享的核心对象。
type Daemon struct {
	codexPool     *agent.CodexProcessPool
	codebuddyPool *agent.CodebuddyProcessPool
	cfg           Config
	client        *Client
	repoCache     repoCacheBackend
	skillCache    *SkillBundleCache
	logger        *slog.Logger

	// terminalReports is the durable outbox for complete/fail callbacks. The
	// sender hook is production-wired through Client and overridable in focused
	// tests; terminalReportWakeup coalesces new-report and reconnect nudges.
	terminalReports      *terminalReportStore
	terminalReportSend   terminalReportSendFunc
	terminalReportWakeup chan struct{}
	terminalReportNow    func() time.Time
	terminalReportMu     sync.Mutex
	terminalReportFlight map[string]struct{}

	mu           sync.Mutex
	workspaces   map[string]*workspaceState
	runtimeIndex map[string]Runtime // runtimeID -> Runtime，供 provider 查找
	// profileLaunchSpecs 记录自定义运行时 profile_id -> 该 profile 解析出的
	// 可执行文件绝对路径与固定启动参数（MUL-3284）。在 registerRuntimesForWorkspace
	// 里 profile 命令解析成功后填充；runTask 启动认领任务的 custom 命令时读取。
	// 受 mu 保护。
	profileLaunchSpecs map[string]profileLaunchSpec
	reloading          sync.Mutex         // 防止并发 workspace 同步
	runtimeSet         *runtimeSetWatcher // runtime 集合变化的发布/订阅器（支持多订阅者）

	// lifecycleCtx is the context Run was handed, kept so background work that
	// must outlive a single probe round — the DSH profile install — is tied to
	// the daemon's lifetime instead of to whichever round happened to notice the
	// missing profile. Guarded by d.mu: it is written once in Run and read from
	// the install goroutine, and "the write happens before every reader starts"
	// is a property of statement order in Run that a later edit can quietly
	// break. Read it through daemonLifecycleCtx.
	lifecycleCtx context.Context

	// dshInstallInFlight is true while an automatic DSH profile install is
	// running. Read when a verdict becomes an offline reason, so the server can
	// tell "wait, this resolves itself" from "a human has to act".
	dshInstallInFlight atomic.Bool

	// dshInstallWaits are the runtime rows taken offline while an automatic
	// profile install was still running, so their stated cause claims the wait
	// resolves itself. An install that ends without a profile has to withdraw
	// that claim (withdrawDshInstallWait): the demotion already removed these
	// runtimes from the index, so no later round condemns them again and no
	// later deregistration would correct the row on its own. Guarded by d.mu.
	dshInstallWaits []dshInstallWait

	// dshProvisionOnce makes the automatic DSH runtime-profile install
	// once-per-daemon: it is a network install that writes into the user's DSH
	// home, so a failing registry must not be retried every discovery round.
	// See startDshProfileProvision.
	dshProvisionOnce sync.Once

	// agentDiscoveryKick asks agentDiscoveryLoop for an immediate convergence
	// round. Buffered and written non-blockingly (kickAgentDiscovery), so a
	// producer never waits on the loop and a burst collapses into one round.
	// Needed because the loop's scheduled retry can be
	// agentConvergeMaxBackoff away, which is far too long to wait after a
	// local state change that makes a provider registrable again.
	agentDiscoveryKick chan struct{}

	versionsMu    sync.RWMutex      // 保护 agentVersions
	agentVersions map[string]string // provider -> 注册期间探测到的 CLI 版本

	// registerSerial 为每个工作区保存一把互斥锁（workspace_id -> *sync.Mutex），
	// 用于串行化"发送 Register、记录其所带内容"这一临界区（workspaceRegisterLock）。
	// 条目从不删除——守护进程只跟踪少量工作区，一个已消失工作区的锁只占几个字节，
	// 不值得为它设计生命周期。
	registerSerial sync.Map

	// agentsAvailable 保存当前内置 agent CLI 可用性集合——形状与 cfg.Agents 相同，
	// 并取代它成为读取路径。
	//
	// 它是写时复制的，不是一个可变的 map：refreshAgentAvailability 会整体换入
	// 一张新 map，读者（agents()）只取一次指针然后只读。cfg.Agents 过去会被任务
	// 执行路径（resolveAgentEntry、runTask）不加锁地读取，因此把发现集合改为运行期
	// 可刷新（MUL-5439）之后，若还用它就会造成数据竞争。
	agentsAvailable atomic.Pointer[map[string]AgentEntry]

	// skippedAgents 记录某 provider 未能进入最近一次注册轮的原因（版本无法探测、
	// 低于最低版本）。纯诊断用途：在 /health 上展示，让 UI 能区分"未安装"与
	// "已安装但被剔除"，而不是静默不显示（MUL-5439）。受 skippedAgentsMu 保护。
	skippedAgentsMu sync.RWMutex
	skippedAgents   map[string]string // provider -> 人类可读原因

	// demotedProviders 记住那些版本已被 CONFIRMED 低于最低支持版本、且其 runtime
	// 已被 demoteBelowMinimumRuntimes 下线掉的内置 provider。
	//
	// 只删除行还不够：降级发生时可能已经有一批发出去的注册在途，其载荷仍是降级前
	// 的内容，响应会在之后才回来。每个 apply 路径都把响应当最新事实，于是本地会
	// 重新索引该 provider，而服务器的 upsert 又把行放回在线——复活了一个守护进程
	// 已证明跑不起来的 CLI，直到下一轮刷新发现为止。记住该判定，apply 路径就能
	// 拒绝这份迟到的响应。
	//
	// 按机器级别记录，因为该判定是"这台主机上这个二进制"的属性，而非某个工作区
	// 的属性：工作区 A 的迟到注册也不得为工作区 B 复活该 provider。下一次发现该
	// provider 可用的探测轮会清掉它——那也是让 converge 能重新注册它的一轮。
	//
	// 受 d.mu 保护——刻意与每个注册响应 apply 取的同一把锁保持一致，这样才能
	// 让"记录判定"与"应用响应"全序化，而不是仅仅收窄两者间的窗口。
	demotedProviders map[string]demotionRecord // provider -> 定罪证据与时间

	// condemnedSince 记录每个 provider 首次被观察到"不可执行"的时刻，供
	// confirmNotExecutable 的确认窗口使用。发现 provider 恢复健康的那一轮会清除
	// 条目。受 d.mu 保护。
	condemnedSince map[string]time.Time

	// demotionSeq 是打在每条降级记录上的单调递增计数器，让探测轮能判断自己的
	// 证据是否早于某条判定。
	//
	// 只给 map 写入排序还不够：版本采样发生在 d.mu 之外，因此一轮更早开始、
	// 采到可接受版本、却更晚返回的探测，可能清掉一条更新的 below-minimum 判定
	// 造成的 hold——一旦 hold 消失，迟到的注册响应就能穿过上面所有防线。一轮在
	// 采样任何东西之前先快照该计数器，并且只允许清除快照时刻或之前记录的 hold，
	// 于是"我的证据比那条判定新"成为一个事实而非一个希望。受 d.mu 保护。
	demotionSeq uint64

	// resolvedPathsMu 保护"可执行文件的具体路径 + 为该路径探测到的版本"这一对值。
	// 在 POSIX 上它们是钉住路径消失后自愈缓存的产物（MUL-4486）；在 Windows 上
	// 则是从稳定的安装器 junction 解析出的启动目标——每次启动都会跟随该 junction，
	// 因此重定向在旧版本仍安装时也能生效。路径与版本一起存储，保证不会有读者拿着
	// 过期的版本策略去启动一个新二进制。按 provider 为键。
	resolvedPathsMu sync.RWMutex
	resolvedPaths   map[string]healedAgent
	// healGroup 按 provider 合并并发的自愈重解析请求，刚升级的 agent 被许多排队
	// 任务同时看到时，只需付一次 login-shell 探测 + 版本检测，而不是每个任务一次
	// （MUL-4486）。
	healGroup singleflight.Group

	wsHBMu      sync.RWMutex         // 保护 wsHBLastAck
	wsHBLastAck map[string]time.Time // runtime_id -> 最近一次 WS 心跳 ack 成功的时间戳

	// reconcile 把"立即重查服务器状态"的信号扇出给订阅者
	// （watchTaskCancellation、workspaceSyncLoop），让 WS 连接/重连路径能把粗粒度
	// 的兜底对账间隔缩到亚秒级。见 reconcile.go 与 runTaskWakeupConnection。
	reconcile *reconcileBroadcaster
	// workspaceChanges 是账号维度"成员集合变化"的服务器提示。它与 reconcile
	// 分开，因为成员提示只需要最小的工作区列表，而 WS 重连还需要对账在断连期间
	// 可能变化过的 runtime profile。
	workspaceChanges *workspaceChangeSignal

	// wsRPC 在 task-wakeup WS 连接上承载通用请求/响应式 RPC（例如 tasks.claim，
	// MUL-4257）。它在 runTaskWakeupConnection 中挂到活动连接上、断连时摘除；
	// 摘除后调用方回退到 HTTP。
	wsRPC *wsRPCClient

	// batchClaimUnsupported 在批量认领拿到 404（服务器没有 /api/daemon/tasks/claim
	// 路由——服务器未升级）后置位一次，让后续轮询跳过 WS + 批量，直接走旧的
	// 每 runtime 认领。WS（重）连接时重置，使服务器升级弹掉连接后会重新探测
	// 批量路由（MUL-4257）。
	batchClaimUnsupported atomic.Bool
	// wsClaimHTTPFallbackAfter 在一次结果不确定的 WS 认领之后置位。安全延迟过后，
	// 下一次认领会跳过 WS 用一次 HTTP，避免反复重连的不稳定 WS 无限饿死排队任务。
	wsClaimHTTPFallbackAfter atomic.Int64

	// runtimeGoneMu 保护 runtimeGoneInflight、reregisterNextAttempt 与
	// reregisterLastCompletedAt。该状态让心跳 / 轮询器 / WS-ack 处理器在各自发现
	// runtime 行被服务器删除时，收敛到同一条恢复路径，而不至于三方同时涌向
	// registerRuntimesForWorkspace。
	runtimeGoneMu             sync.Mutex
	runtimeGoneInflight       map[string]struct{}  // runtime_id -> 正在恢复中
	reregisterNextAttempt     map[string]time.Time // workspace_id -> 下一次允许重注册的最早时间
	reregisterLastCompletedAt map[string]time.Time // workspace_id -> 最近一次 SUCCESSFUL 重注册返回的墙上时间（失败故意不盖戳——见 recordRegisterCompletion）

	// pendingWorkMu 保护 pendingWorkInflight 与 pendingWorkLastRun，它们用于
	// 合并与限速服务器推送的"立即心跳"提示（MUL-5444）。多个 UI 面可能在毫秒级
	// 内请求同一 runtime 的模型列表；没有该护栏，每个提示都会各自触发一次带外
	// 心跳，已认证调用方循环调 list-models 端点时就会把心跳放大。
	pendingWorkMu       sync.Mutex
	pendingWorkInflight map[string]struct{}  // runtime_id -> 提示驱动的心跳在途
	pendingWorkLastRun  map[string]time.Time // runtime_id -> 上次提示驱动心跳的开始时间

	cancelFunc context.CancelFunc // 由 Run() 设置；triggerRestart 调用它
	rootCtx    context.Context    // 由 Run() 设置；长生命周期恢复操作使用它，不受每个 runtime ctx 取消的影响
	// restartMu 保护 restartBinary。两个 goroutine 可能到达 triggerRestart——
	// 服务器触发的 handleUpdate 与 autoUpdateLoop——trySelfReload 从后者读取
	// RestartBinary() 以避免与前者竞争导致二次交接。
	restartMu     sync.Mutex
	restartBinary string // 更新成功后非空；指向新二进制文件的路径
	// brewTargetOnce 缓存 brew 侧的 restartTargetBinary 结果。安装方式与 brew
	// prefix 在进程生命周期内不会变，而 trySelfReload 现在每次检查 tick 都会调用
	// restartTargetBinary——没有该缓存，每个 tick 最多会 fork 两次未缓存的
	// `brew --prefix`。
	brewTargetOnce sync.Once
	brewInstall    bool        // 只解析一次：这个二进制是否通过 brew 安装？
	brewTarget     string      // brewInstall 且 prefix 解析成功时为 "<prefix>/bin/multica"
	updating       atomic.Bool // 防止并发更新尝试
	// activeTasks 是当前处于 handleTask 中的任务的"所有权安全"计数。它刻意包含
	// 准备阶段与 local_directory 等待者，因为重启/更新屏障不得杀掉任何已认领任务。
	activeTasks atomic.Int64
	// captureMu 保护 captureRunning：按 workspace 统计正在采集 diff 的运行数。
	// 同一 workspace 有第二个任务同时在跑时，任务目录之外新出现的工作树无法确定
	// 归属，采集会把它标成「未归属」而不是猜给某一个任务。
	captureMu      sync.Mutex
	captureRunning map[string]int
	// runningTasks 统计活跃的 provider 执行会话，只在 backend.Execute 返回后开始
	// 计数。它可能短暂落后于服务器侧 running 状态（后者从 provider 启动前的准备
	// 阶段就开始）。resourceWaitTasks 统计阻塞在 local_directory 路径锁上的任务。
	// 两者都是 /health 的诊断维度，绝不能取代安全屏障中的 activeTasks。
	runningTasks      atomic.Int64
	resourceWaitTasks atomic.Int64
	ready             atomic.Bool // preflight 完成前为 false；控制 /health 状态（starting -> running）
	// reloadPendingReason 解释为什么已确认的 multica 版本变化还没重启守护进程
	// （屏障检查时有任务在跑）。由 trySelfReload 设置与清除，被 /health 读取。
	// 仅诊断用途。
	reloadPendingReason atomic.Pointer[string]

	// claimMu 保护 pauseClaims 与 claimsInFlight。它只在做决定的微秒级时间片内
	// 持有；ClaimTask 本身不加锁运行，因此某个慢的 per-runtime 认领不会拖住
	// 自动更新或其它轮询器。
	//
	// 这一对是自动更新路径对需求"升级过程中如果有 task 进来，会延后升级而不是
	// 中断 task"的护栏：pauseClaims 置位时 runRuntimePoller 拒绝调用 ClaimTask，
	// 而 tryAutoUpdate 在任一 poller 认领中途或任一任务处于 handleTask 时拒绝翻转
	// pauseClaims。两者合起来关闭了"抓取发布元数据期间新任务溜进来、随后被
	// triggerRestart 的 root-ctx 取消"这一取-再认领竞态。
	claimMu         sync.Mutex
	deploymentDrain bool // 发布时暂停认领，已有任务继续完成
	pauseClaims     bool // 为 true 时批量轮询器跳过认领
	claimsInFlight  int  // 已决定认领、但尚未把任务交给 handleTask 的轮询器数量

	activeEnvRootsMu   sync.Mutex
	activeEnvRootsCond *sync.Cond      // 在途 env-root GC 变更结束时唤醒
	activeEnvRoots     map[string]int  // env root 路径 -> 引用计数（处理同一路径被标记两次）
	deletingEnvRoots   map[string]bool // 被 GC 保留待删的 env root；新任务等待其变更完成

	activeStoresMu   sync.Mutex
	activeStoresCond *sync.Cond      // 在途 store 删除完成时唤醒，使阻塞的 markActive 得以继续
	activeStores     map[string]int  // 持久化 store 路径（每个会话的 Codex sessions、每 agent 的 Hermes memories）-> 活跃任务引用计数；防止 GC 在任务中途回收（MUL-4424）
	deletingStores   map[string]bool // 已被 GC 删除保留的 store 路径；markActive 会等它们清空，确保任务不会在删除中途挂载 store

	// repoCheckoutTasks 把 localhost /repo/checkout 端点绑定到正在运行的 agent 的
	// task 级 bearer token。请求体永远不是身份来源：workspace、task、agent 与允许的
	// 工作目录全部来自这个注册表。
	repoCheckoutTasksMu sync.RWMutex
	repoCheckoutTasks   map[string]activeRepoCheckoutTask

	// localPathLocks 串行化 project 资源为"钉在本守护进程上的 local_directory"
	// 的 agent 任务。两个指向同一磁盘路径的任务顺序执行；第二个会阻塞在锁上，
	// 等待期间通过服务器侧的 waiting_local_directory 状态呈现。见 MUL-2663。
	localPathLocks *LocalPathLocker

	// bgSyncs 跟踪 registerTaskRepos 启动的后台 goroutine，让调用方（特别是使用
	// t.TempDir 缓存根的测试）能在拆除守护进程前等待它们排空。没有它，后台
	// goroutine 可能与 t.TempDir 清理竞争，留下部分删除的 bare clone 和无关的
	// `not empty` 清理失败。
	bgSyncs sync.WaitGroup

	runner             taskRunner    // 执行 agent 任务；New() 设为 d.runTask，测试可覆盖
	cancelPollInterval time.Duration // handleTask 轮询服务器侧取消的频率；测试可覆盖
	// envRootBusyWait 是一个有权使用旧 env root 的任务，在放弃并准备全新环境前，
	// 等待上一次运行放开该 root 的时长。New() 会设置它；零值表示"不等待"，这正是
	// 聚焦的单元测试想要的。见 lockReusablePriorEnvRoot（MUL-6880）。
	taskSupplementSignals       taskSupplementSignals
	taskSupplementPollInterval  time.Duration
	taskSupplementReadyInterval time.Duration
	envRootBusyWait             time.Duration
	// executionEnvironmentCommand 解析用于 Prepare/Reuse 的"可被杀死的助手进程"。
	// New 总是设置它；nil 让聚焦的单元测试保持进程内执行。
	executionEnvironmentCommand executionEnvironmentCommand
	// taskPrepareTimeout 是 dispatched -> running 的硬性截止时间。New 设置生产默认
	// 值；零值测试守护进程在 effectiveTaskPrepareTimeout 里回退到同一默认值。
	taskPrepareTimeout time.Duration
	// runUpdateFn 执行 brew 或下载方式的升级。New() 设为 d.runUpdate，测试可覆盖，
	// 以便不触碰真实网络或 brew CLI 就能驱动自动更新轮询器。
	runUpdateFn func(targetVersion string) (string, error)
}

type profileLaunchSpec struct {
	path      string
	version   string
	fixedArgs []string
}

// New 创建一个 Daemon 实例。
func New(cfg Config, logger *slog.Logger) *Daemon {
	cacheRoot := filepath.Join(cfg.WorkspacesRoot, ".repos")
	skillCacheRoot := filepath.Join(cfg.WorkspacesRoot, ".skill-cache", "v1")
	client := NewClient(cfg.ServerBaseURL)
	// 给每个 daemon HTTP 请求打上 daemon 的 CLI 版本标签，服务器才能按客户端
	// 版本拆分日志/指标（与 CLI 侧的做法平行）。
	client.SetVersion(cfg.CLIVersion)
	d := &Daemon{
		cfg:                         cfg,
		client:                      client,
		repoCache:                   repocache.New(cacheRoot, logger),
		skillCache:                  NewSkillBundleCache(skillCacheRoot),
		logger:                      logger,
		terminalReports:             newTerminalReportStore(cfg),
		terminalReportWakeup:        make(chan struct{}, 1),
		terminalReportNow:           time.Now,
		terminalReportFlight:        make(map[string]struct{}),
		workspaces:                  make(map[string]*workspaceState),
		runtimeIndex:                make(map[string]Runtime),
		profileLaunchSpecs:          make(map[string]profileLaunchSpec),
		agentDiscoveryKick:          make(chan struct{}, 1),
		runtimeSet:                  newRuntimeSetWatcher(),
		agentVersions:               make(map[string]string),
		skippedAgents:               make(map[string]string),
		resolvedPaths:               make(map[string]healedAgent),
		wsHBLastAck:                 make(map[string]time.Time),
		activeEnvRoots:              make(map[string]int),
		deletingEnvRoots:            make(map[string]bool),
		activeStores:                make(map[string]int),
		deletingStores:              make(map[string]bool),
		localPathLocks:              NewLocalPathLocker(),
		runtimeGoneInflight:         make(map[string]struct{}),
		pendingWorkInflight:         make(map[string]struct{}),
		pendingWorkLastRun:          make(map[string]time.Time),
		reregisterNextAttempt:       make(map[string]time.Time),
		reregisterLastCompletedAt:   make(map[string]time.Time),
		cancelPollInterval:          5 * time.Second,
		envRootBusyWait:             15 * time.Second,
		taskPrepareTimeout:          defaultTaskPrepareTimeout,
		reconcile:                   newReconcileBroadcaster(),
		workspaceChanges:            newWorkspaceChangeSignal(),
		wsRPC:                       newWSRPCClient(wsRPCResponseGrace),
		taskSupplementPollInterval:  defaultTaskSupplementPollInterval,
		taskSupplementReadyInterval: defaultTaskSupplementReadyInterval,
	}
	d.activeEnvRootsCond = sync.NewCond(&d.activeEnvRootsMu)
	d.activeStoresCond = sync.NewCond(&d.activeStoresMu)
	// 用启动探测的结果给"写时复制"的可用性集合播种。此后调用方一律走
	// d.agents()；cfg.Agents 只是初始值，不会跟踪后续刷新。
	initialAgents := make(map[string]AgentEntry, len(cfg.Agents))
	for name, entry := range cfg.Agents {
		initialAgents[name] = entry
	}
	d.agentsAvailable.Store(&initialAgents)
	d.executionEnvironmentCommand = defaultExecutionEnvironmentCommand
	d.runner = taskRunnerFunc(d.runTask)
	d.runUpdateFn = d.runUpdate
	return d
}

// setAgentVersion 记录某 agent provider 探测到的 CLI 版本，供后面的任务派发代码
// （比如 Codex 沙箱策略）读取。
//
// 空探测结果绝不替换已知道的版本。checkAgentMinVersion 对任何没有 MinVersions
// 条目的 provider 都返回 nil，因此一个 `--version` 退出码是 0、却没打印出任何
// 可解析内容的 CLI，会带着空字符串走到这里——若用它覆盖缓存，等于把这个"每次
// 按键值读版本策略的输入"清空，而该 provider 片刻之前还正常工作。"读不出来"
// 不是"没有版本"。
//
// 这个缓存只是 daemon 的"本地认知"——不是"服务器已被告知什么"的记录。每一条
// 探测路径（converge 轮、workspace sync、任务启动时的自愈）都会写到这里，而每次
// register 调用只覆盖它被调用的那些工作区。服务器到底接受了什么，是按工作区
// 记录在 workspaceState.builtinVersions 里的；refreshAgentVersions 负责对比两者。
func (d *Daemon) setAgentVersion(provider, version string) {
	d.versionsMu.Lock()
	prev := d.agentVersions[provider]
	if version == "" && prev != "" {
		d.versionsMu.Unlock()
		d.logger.Warn("agent CLI reported no version; keeping the previous one",
			"provider", provider, "previous", prev)
		return
	}
	d.agentVersions[provider] = version
	d.versionsMu.Unlock()
}

// agentVersion 返回某 agent provider 最近一次探测到的 CLI 版本；未知返回空串。
func (d *Daemon) agentVersion(provider string) string {
	d.versionsMu.RLock()
	defer d.versionsMu.RUnlock()
	return d.agentVersions[provider]
}

// builtinVersionsFromPayload 从注册载荷的 BUILT-IN 条目里抽出 provider -> version。
// 自定义 profile 条目（带 profile_id）不做版本跟踪——它们的生命周期归漂移路径管。
func builtinVersionsFromPayload(runtimes []map[string]string) map[string]string {
	out := make(map[string]string, len(runtimes))
	for _, rt := range runtimes {
		if rt["profile_id"] != "" {
			continue
		}
		out[rt["type"]] = rt["version"]
	}
	return out
}

// workspaceRegisterLock 返回串行化"单个工作区整段注册流程"的互斥锁：发 Register、
// 应用或拒绝其响应，然后反注册该响应拒绝或丢弃的那些行。
//
// 按工作区记的版本记录（workspaceState.builtinVersions）必须反映服务器"实际按什么
// 顺序处理这些 register 调用"——因为服务器的 upsert 顺序决定了它的行最终持有
// 哪份载荷的版本。注册入口是并发的（refresh、sync、runtime_gone 恢复、profile
// 漂移），没有这把锁时，同一工作区的两次调用可能以与服务器处理顺序相反的顺序
// 收到响应——本地记下更新的载荷，而服务器保留的是更旧那份。refresh 轮随后看到
// 记录与盘上一致，便再也不会重注册：不一致恰恰因为记录本身是错的而变得不可见，
// 于是它一直存活到下次版本变化或重启。把锁一直握到"请求 + 记录"都做完，就能
// 保证"锁的顺序 = 服务器的顺序 = 记录的顺序"。
//
// 临界区要一直延伸到发送之后，因为清理步骤有同款问题、且形态更恶心。反注册是
// 在 d.mu 之下决定的、却在放开 d.mu 之后才发出，因此在那段间隙里完成的恢复式
// register 会重建同一行——通常还顶着同一个 runtime ID——然后更早的那次
// Deregister 落在这行头上。于是 daemon 追踪并心跳着一个服务器已标为离线的
// runtime，而这正是会悄悄搁浅工作的方向：服务器不再给它派活，两边却都察觉不到
// 分歧。事后的点检式重查堵不上这个洞——洞在检查与请求之间——只有把请求本身放
// 进顺序里才行。因此某工作区的每次 apply 与每次清理都在 withWorkspaceRegisterLock
// 下、在这把锁内执行。
func (d *Daemon) workspaceRegisterLock(workspaceID string) *sync.Mutex {
	mu, _ := d.registerSerial.LoadOrStore(workspaceID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// withWorkspaceRegisterLock 把某工作区的整段注册流程当作一个有序步骤执行。任何
// "为某工作区发 Register、把响应并入本地状态、或反注册该响应带来的行"都必须跑在
// fn 里——为什么临界区边界在清理之后而不是发送之后，见 workspaceRegisterLock。
//
// 锁按工作区隔离，不同工作区的流程仍可并发执行，且这里永远不会同时握着两把
// 工作区锁。
func (d *Daemon) withWorkspaceRegisterLock(workspaceID string, fn func() error) error {
	mu := d.workspaceRegisterLock(workspaceID)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}

// recordBuiltinVersionsSent 按 provider 记录某次 SUCCESSFUL register 调用为某个
// 工作区携带的版本（见 workspaceState.builtinVersions）。按 provider 合并而非整体
// 替换：若某 provider 不在本次载荷里（它这轮探测失败了），等于没被重新发送，
// 服务器仍持有上次调用携带的内容，记录也必须继续这么写。调用方只在
// client.Register 成功后才调它——失败的调用什么都不记，这正是让工作区保持落后、
// 供下一轮 refresh 再试的机制——并且调用时须持有该工作区的 register 锁，这样
// 记录的写入顺序才能与服务器处理顺序一致（workspaceRegisterLock）。尚未被跟踪的
// 工作区在这里什么也不记；sync 路径创建 workspaceState 时会给记录播种。
func (d *Daemon) recordBuiltinVersionsSent(workspaceID string, runtimes []map[string]string) {
	sent := builtinVersionsFromPayload(runtimes)
	if len(sent) == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		return
	}
	if ws.builtinVersions == nil {
		ws.builtinVersions = make(map[string]string, len(sent))
	}
	for provider, version := range sent {
		// 注册在途期间该 provider 被降级了：它的行正在被反注册，此时记下载荷
		// 携带的版本，等于告诉 refresh 路径"服务器为一个正在下线的 provider 持有
		// 版本"——而 demoteBelowMinimumRuntimes 刚把那条目删掉。
		if d.providerDemotedLocked(provider) {
			continue
		}
		ws.builtinVersions[provider] = version
	}
}

// refreshHealedVersion 在自愈出来的二进制后来被原地替换时，让 {path, version}
// 这对自愈缓存保持诚实。
//
// resolveAgentEntry 只要前一次自愈仍生效，就刻意返回 healed.version 而不是共享的
// agentVersions 缓存——这样任何观察到自愈路径的读者，必然观察到为它探测的版本。
// 当同一个路径随后被覆盖写时，没有任何东西刷新这对值，于是 daemon 一直拿"自愈时
// 捕获的版本"去给对版本敏感的策略——runTask 里的 Codex 沙箱——做键。重新探测会
// 更新 agentVersions 和报给服务器的版本，任务却静默地继续跑在旧策略下。
//
// 只在路径仍与刚探测的一致时才动那条目：自愈后该 provider 已挪到别处的，它自带
// 一对配对，不归这里管。
func (d *Daemon) refreshHealedVersion(provider, path, version string) {
	if version == "" {
		return
	}
	d.resolvedPathsMu.Lock()
	defer d.resolvedPathsMu.Unlock()
	healed, ok := d.resolvedPaths[provider]
	if !ok || healed.path != path || healed.version == version {
		return
	}
	d.resolvedPaths[provider] = healedAgent{path: path, version: version}
}

// hasDetectedAgentVersions 报告是否探测到过任何 CLI 版本。refreshAgentVersions
// 拿它当"有没有注册过任何东西"的护栏。
func (d *Daemon) hasDetectedAgentVersions() bool {
	d.versionsMu.RLock()
	defer d.versionsMu.RUnlock()
	return len(d.agentVersions) > 0
}

// healedAgent 把自愈后的可执行文件路径与为它探测到的 CLI 版本绑在一起。两者在
// resolvedPathsMu 下一起发布、由 resolveAgentEntry 一起返回，因此观察到新路径的
// 调用方必然观察到匹配的版本——正好堵住"刚升级的二进制却跑在 daemon 旧版本
// 策略下"的窗口（MUL-4486 评审）。
type healedAgent struct {
	path    string
	version string
}

// resolveAgentEntry 返回 entry，但带上可用的可执行文件路径 + 与该路径对应的 CLI
// 版本。它在 Windows 上每次启动都解析可重定向的安装器 junction，在其它平台上则
// 自愈"已消失的钉住路径"（MUL-4486）。
//
// daemon 在启动时把每个 agent 发现到的入口点钉住，之后 PATH 再变也不能把任务
// 启动引向别处。POSIX 侧的发现还会把符号链接解析成具体路径。但版本管理器
// （Homebrew Cask、nvm/fnm）就地升级时会删掉钉住路径指向的旧版本目录，再把
// 稳定命令名重指到新版本——于是 daemon 手里握着一条"已不存在、要等它重启才
// 恢复"的路径。每个消费 entry.Path 的地方（任务启动、模型列举、注册时版本探测）
// 都会硬失败在 "executable not found" 上。
//
// 返回的版本永远与返回的路径匹配，调用方可以直接用它（而不是重读共享版本缓存，
// 后者可能正被一次并发的自愈更新）来给对版本敏感的策略（如 Codex 沙箱）做键。
//
// 行为：
//   - Windows 的稳定入口点 -> 校验并返回它的最终目标 + 探测到的版本；目标变了
//     就以原子方式发布新的一对值。
//   - 上一次自愈仍生效 -> 返回它 + 配对的版本。这条最先检查：一旦我们重新解析到
//     新二进制，一条重新冒出来的陈旧路径（降级/重装把旧版本目录又建回来了）就不
//     能把那台旧二进制和自愈版本重新配成一对——即错配的 {旧路径, 新版本}
//     （MUL-4486 评审）。
//   - 其它情况，钉住的 Path 还在 -> 原样返回，配上注册时探测的版本。对从未自愈
//     的常规情形，"防重定向"保证依然成立：一条活着的钉住二进制绝不被事后怀疑，
//     哪怕 PATH 现在已经指向别处。
//   - 钉住的 Path 已消失且没有生效的自愈 -> 用 entry.Command 重新解析一次
//     （保留 ~/.multica/hooks 排除项和 login-shell 兜底）。采纳重新解析出的二进制
//     前，要先做版本探测、过一遍与注册相同的"最低版本"门槛。这精确复刻了"daemon
//     重启会解析出什么"，因此与文档化的重启绕行同样安全——只是自动化了。
//   - 重新解析失败、候选无法探测版本、或低于最低支持版本 -> 原样返回 entry，
//     候选永不启动，下游错误照常浮出。
func (d *Daemon) resolveAgentEntry(ctx context.Context, provider string, entry AgentEntry) (AgentEntry, string) {
	resolved, version, _ := d.resolveAgentEntryWithHeal(ctx, provider, entry)
	return resolved, version
}

// resolveAgentEntryForLaunch 是严格的任务启动边界。Windows 安装器 junction 必须
// 在第一次启动前就交出经校验的最终目标；否则稳定入口可能在注册之后被重定向，
// 让一个版本与"最低版本策略从未被检查过"的二进制跑起来。
func (d *Daemon) resolveAgentEntryForLaunch(ctx context.Context, provider string, entry AgentEntry) (AgentEntry, string, error) {
	resolved, version, outcome := d.resolveAgentEntryWithHeal(ctx, provider, entry)
	if outcome.rejected != nil {
		return entry, d.agentVersion(provider), outcome.rejected
	}
	if outcome.failure != nil {
		return entry, d.agentVersion(provider), fmt.Errorf("resolve agent executable %q for launch: %w", entry.Path, outcome.failure)
	}
	if outcome.adopted.path != "" {
		return resolved, version, nil
	}
	return resolved, version, nil
}

// healOutcome 是一次自愈尝试得出的结论。两半最多有一个有意义：adopted 指名一个
// 通过了"注册会施加的那些门槛"的二进制；rejected 则携带某个"被发现且探测出
// 版本、却因低于最低支持版本而被拒"的候选的类型化裁决。rejected 只有在裁决真实
// 时才非 nil——它只会由 *agent.BelowMinimumError 设置，而这类错误构造时必带一个
// 能解析出版本的 version。
type healOutcome struct {
	adopted  healedAgent
	rejected *agent.BelowMinimumError
	failure  error
}

// resolveAgentEntryWithHeal 是 resolveAgentEntry 之外还带"自愈得出了什么结论"的
// 版本，供那个必须对拒绝采取行动、而不只是拒绝启动的调用方使用。
//
// 拒绝是对"磁盘"的裁决，不是瞬态失败：钉住路径没了，而且它命令现在解析到的
// 二进制太旧。注册需要听到这个——否则它会继续探测那条已消失的路径、失败、然后
// 上报 "version detection failed"——而后者按设计会让 runtime 保持在线，去认领
// 一个根本拉不起来的 CLI 的任务。
func (d *Daemon) resolveAgentEntryWithHeal(ctx context.Context, provider string, entry AgentEntry) (AgentEntry, string, healOutcome) {
	// Windows 安装器入口是稳定的 junction，其最终目标可以在旧版本仍被安装时改变。
	// 每次启动都解析最终路径，且只在把变更后的目标与"新探测出且受支持"的版本配对
	// 之后才采纳它。其它平台返回 handled=false，沿用下面的钉住路径自愈语义。
	var launchOutcome healOutcome
	if launchPath, handled, err := executablePathForLaunch(entry.Path); handled {
		if err != nil {
			d.logger.Warn("resolve agent executable for launch failed; keeping discovered path",
				"provider", provider, "path", entry.Path, "error", err)
			launchOutcome.failure = err
		} else if outcome, ok := d.resolveAgentLaunchTarget(ctx, provider, entry, launchPath); ok {
			entry.Path = outcome.adopted.path
			return entry, outcome.adopted.version, outcome
		} else {
			launchOutcome = outcome
		}
	}

	// 上一次自愈优先于原始钉住路径：它带的 {path, version} 对是我们已经一起
	// 校验过的，绝不会退化成"重新冒出来的陈旧路径造成的那种错配对"。
	d.resolvedPathsMu.RLock()
	healed, ok := d.resolvedPaths[provider]
	d.resolvedPathsMu.RUnlock()
	if ok && agentExecutablePresent(healed.path) {
		entry.Path = healed.path
		launchOutcome.adopted = healed
		return entry, healed.version, launchOutcome
	}

	if agentExecutablePresent(entry.Path) {
		return entry, d.agentVersion(provider), launchOutcome
	}

	if entry.Command == "" {
		return entry, d.agentVersion(provider), healOutcome{}
	}

	// 合并同一 provider 的并发自愈：第一个任务掏"重新解析 + 版本探测"的钱，其余
	// 的共享其结果，而不是各自派生一个 login shell 和一次 `--version` 探测。
	command := entry.Command
	v, _, _ := d.healGroup.Do(provider, func() (any, error) {
		return d.healAgentPath(ctx, provider, command), nil
	})
	outcome, _ := v.(healOutcome)
	if outcome.adopted.path == "" {
		return entry, d.agentVersion(provider), outcome
	}
	entry.Path = outcome.adopted.path
	return entry, outcome.adopted.version, outcome
}

// resolveAgentLaunchTarget 处理"稳定发现入口可以重定向到另一个仍然存活的
// 可执行文件"的平台。只有拿到经校验的 {path, version} 对时才返回 ok；被拒或
// 暂时读不到的目标会落到现有路径处理上，因此绝不在陈旧版本下发布。
func (d *Daemon) resolveAgentLaunchTarget(ctx context.Context, provider string, entry AgentEntry, launchPath string) (healOutcome, bool) {
	const maxRetargetAttempts = 4
	for attempt := 0; attempt < maxRetargetAttempts; attempt++ {
		d.resolvedPathsMu.RLock()
		cached, cachedOK := d.resolvedPaths[provider]
		d.resolvedPathsMu.RUnlock()
		if cachedOK && cached.path == launchPath && agentExecutablePresent(cached.path) {
			return healOutcome{adopted: cached}, true
		}

		// 只合并"观察到同一份具体版本"的调用方。用仅 provider 的键，会让一个在
		// 重定向之后的调用方继承上一个版本的结果，哪怕两个文件都还在。
		key := provider + "\x00" + launchPath
		v, _, _ := d.healGroup.Do(key, func() (any, error) {
			d.resolvedPathsMu.RLock()
			current, ok := d.resolvedPaths[provider]
			d.resolvedPathsMu.RUnlock()
			if ok && current.path == launchPath && agentExecutablePresent(current.path) {
				return healOutcome{adopted: current}, nil
			}
			return d.adoptAgentPath(ctx, provider, entry.Command, launchPath, "resolved stable entry point for launch"), nil
		})
		outcome, _ := v.(healOutcome)

		// 版本探测期间安装器可能又重定向了。返回前再解析一次，对照此刻可见的
		// 目标重试。
		currentPath, _, err := executablePathForLaunch(entry.Path)
		if err != nil {
			if outcome.adopted.path != "" {
				return outcome, true
			}
			outcome.failure = err
			return outcome, false
		}
		if currentPath != launchPath {
			launchPath = currentPath
			continue
		}
		if outcome.adopted.path != "" {
			return outcome, true
		}
		if cachedOK && agentExecutablePresent(cached.path) {
			outcome.adopted = cached
			return outcome, true
		}
		return outcome, false
	}

	return healOutcome{failure: errors.New("installer entry point changed repeatedly while resolving it")}, false
}

// healAgentPath 为 provider 重新解析 command；找到可用二进制就记录并返回它。
// "可用"指：能解析、能探测出版本、且该版本满足注册强制的那道最低版本门槛。路径
// 与版本在 resolvedPathsMu 下一起发布，任何看到路径的观察者也会看到匹配的版本；
// 共享的 d.agentVersion 缓存也会顺手刷新，供注册卫生用。找不到可用内容时返回零值
// adopted 对，调用方继续保留（陈旧的）钉住条目，候选永不启动。在 healGroup 下
// 运行，每 provider 一次只跑一个调用。
//
// 被最低版本门槛拒绝的候选会原样上报而不是吞掉：不启动它是正确的，但"整个裁决"
// 也正是注册轮把该 provider 的 runtime 拉下线所需的全部信息。
func (d *Daemon) healAgentPath(ctx context.Context, provider, command string) healOutcome {
	// 重查一次缓存：同一 singleflight 键下的前任可能已把它填上，或一次更早的
	// 自愈正好在上面的读取与进入这里之间完成。
	d.resolvedPathsMu.RLock()
	cached, ok := d.resolvedPaths[provider]
	d.resolvedPathsMu.RUnlock()
	if ok && agentExecutablePresent(cached.path) {
		return healOutcome{adopted: cached}
	}

	newPath, found := reresolveAgentCommand(command)
	if !found {
		return healOutcome{}
	}
	if launchPath, handled, err := executablePathForLaunch(newPath); handled {
		if err != nil {
			d.logger.Warn("resolve re-discovered agent executable for launch failed; keeping discovered path",
				"provider", provider, "path", newPath, "error", err)
			return healOutcome{failure: err}
		} else {
			newPath = launchPath
		}
	}
	return d.adoptAgentPath(ctx, provider, command, newPath, "re-resolved after pinned path vanished")
}

func (d *Daemon) adoptAgentPath(ctx context.Context, provider, command, newPath, reason string) healOutcome {
	// 采纳前先校验。所谓"就地升级"若其实是指回一台更老或装坏的机器，绝不能让
	// 它跑在 daemon 陈旧的版本策略下，也绝不能漏过注册路径施加的最低版本门槛
	// （MUL-4486 评审）。
	version, err := detectAgentVersion(ctx, agent.Command{Path: newPath})
	if err != nil {
		d.logger.Warn("re-resolved agent executable failed version detection; keeping pinned path",
			"provider", provider, "command", command, "new_path", newPath, "error", err)
		return healOutcome{failure: err}
	}
	if err := checkAgentMinVersion(provider, version); err != nil {
		var tooOld *agent.BelowMinimumError
		if !errors.As(err, &tooOld) {
			// 读到点什么但没读懂：这不是裁决。拒绝采纳仍然正确，但上报成
			// "拒绝"会让调用方因为一个读不懂的版本而降级 runtime——这正是
			// below-minimum 机制绝不该处理的瞬态情形。
			d.logger.Warn("re-resolved agent executable version could not be validated; keeping pinned path",
				"provider", provider, "command", command, "new_path", newPath, "version", version, "error", err)
			return healOutcome{failure: err}
		}
		d.logger.Warn("re-resolved agent executable is below the minimum supported version; not adopting it",
			"provider", provider, "command", command, "new_path", newPath, "version", version, "error", err)
		return healOutcome{rejected: tooOld}
	}

	adopted := healedAgent{path: newPath, version: version}
	// 原子地发布 路径 + 版本：任何在 resolveAgentEntry 里看到新路径的读者，都能从
	// 同一个结构体值里拿到匹配的版本。
	d.resolvedPathsMu.Lock()
	if d.resolvedPaths == nil {
		d.resolvedPaths = make(map[string]healedAgent)
	}
	d.resolvedPaths[provider] = adopted
	d.resolvedPathsMu.Unlock()
	// 顺手把注册用的版本缓存也刷新掉。任务路径读的是上面"随解析路径一起返回的
	// 版本"，不是这个 map，所以它的陈旧永远不会卡住一次启动——这里只是给注册
	// 上报和将来任何 d.agentVersion 读者做卫生。
	d.setAgentVersion(provider, version)

	d.logger.Info("adopted resolved agent executable",
		"provider", provider, "command", command, "new_path", newPath, "version", version, "reason", reason)
	return healOutcome{adopted: adopted}
}

func (d *Daemon) notifyRuntimeSetChanged() {
	d.runtimeSet.notify()
}

// reregisterCoalesceWindow 限制 daemon 在发现 runtime_not_found 响应后、多久内
// 最多给某工作区重注册一次。许多陈旧的 runtime ID 可能在几秒内先后报上来（一次
// 删除会清掉某 daemon 的全部 runtime），而一次 re-register 调用会替换工作区里的
// 全部 runtime，所以并发的恢复必须收敛成一次 API 调用。
const reregisterCoalesceWindow = 30 * time.Second

// reregisterFailureBackoff 是上一次 re-register 失败后、下一次尝试之前插入的额外
// 等待。当重注册本身一直在失败（工作区被移除、服务器不可达……）时，它防止心跳
// tick（~15s）把"服务器侧日志洪峰"变成"重注册洪峰"。
const reregisterFailureBackoff = 60 * time.Second

// handleRuntimeGone 是 HTTP 心跳路径、runtime 轮询器与 WebSocket runtime_gone ack
// 处理器共享的唯一恢复入口。三者可能相隔几毫秒内都发现同一枚陈旧 runtime，所以本
// 函数：
//
//   - 用 runtimeID 键一个在途集合，第一个调用已在清理时，同 ID 的并发调用直接丢弃；
//   - 用 workspaceID 键一个 per-workspace 的 next-attempt 时间戳，让"由同一个初始
//     事件触发的并发恢复"收敛成一次 registerRuntimesForWorkspace 调用。成功时清空
//     槽位，同一工作区里之后一次不同的 runtime 删除就能不等合并窗口过期而触发自己
//     的恢复；以及
//   - 用 workspaceID 键一个 per-workspace 的 last-completed 时间戳，让一个"因
//     removeStaleRuntime 拖得太久、兄弟已完整跑完并清空了槽位"的掉队者仍能认出
//     自己是同波、直接退出。没有它，成功情形的槽位清空会打开一个竞态：迟到的调用
//     重新认领空槽位、重复注册。
//
// 底层 re-register 失败时，next-attempt 时间戳会被延长 reregisterFailureBackoff，
// 免得用"daemon 侧注册洪峰"替换掉"服务器侧日志洪峰"。workspaceSyncLoop 每个
// DefaultWorkspaceSyncInterval 会独立重试，作为安全网。
//
// 恢复用的 HTTP 调用使用 daemon 根 context，而不是调用方的。心跳路径的 per-runtime
// ctx 在我们剪掉死 UUID 的那一刻就会被 notifyRuntimeSetChanged 取消；若把那个 ctx
// 传进来，在途的 register 会在半路自我取消。
func (d *Daemon) handleRuntimeGone(runtimeID string) {
	if runtimeID == "" {
		return
	}

	// entryAt 锚定函数底部的同波掉队者检查。在最顶部捕获，好让 removeStaleRuntime
	// 的互斥锁争用不能把它推到某兄弟的注册完成之后。
	entryAt := time.Now()

	// 按 runtime ID 做踩踏控制。
	d.runtimeGoneMu.Lock()
	if _, inflight := d.runtimeGoneInflight[runtimeID]; inflight {
		d.runtimeGoneMu.Unlock()
		return
	}
	d.runtimeGoneInflight[runtimeID] = struct{}{}
	d.runtimeGoneMu.Unlock()
	defer func() {
		d.runtimeGoneMu.Lock()
		delete(d.runtimeGoneInflight, runtimeID)
		d.runtimeGoneMu.Unlock()
	}()

	workspaceID, removed := d.removeStaleRuntime(runtimeID)
	if !removed {
		// 本地状态里已经没了——并行的恢复早已清掉它，或 workspaceSyncLoop
		// 把整个工作区剪掉了。
		return
	}

	d.logger.Info("runtime deleted server-side; pruned from local state",
		"runtime_id", runtimeID, "workspace_id", workspaceID)
	d.notifyRuntimeSetChanged()

	if !d.tryClaimRegisterSlot(workspaceID, entryAt, time.Now()) {
		d.logger.Debug("skip re-register: coalescing with recent attempt",
			"workspace_id", workspaceID)
		return
	}

	err := d.reregisterWorkspaceAfterRuntimeGone(d.recoveryContext(), workspaceID)
	d.recordRegisterCompletion(workspaceID, time.Now(), err)
	if err != nil {
		// 用 Warn（而非 Error）记录，因为 workspaceSyncLoop 每个
		// DefaultWorkspaceSyncInterval 都会独立重试——这里的瞬态失败不是卡死，
		// 只是多等一轮。
		d.logger.Warn("re-register after runtime gone failed",
			"workspace_id", workspaceID, "error", err)
	}
}

// tryClaimRegisterSlot 原子地决定调用方要不要跑 registerRuntimesForWorkspace。
// 允许继续时返回 true 并占住在途槽位；需要与同伴合并时返回 false（不改任何
// 状态）。
//
// 在 runtimeGoneMu 下检查两道闸门：
//
//  1. reregisterNextAttempt：未来时间戳表示同伴占着槽位，或上次尝试失败、我们
//     正处于失败退避窗口内。
//  2. reregisterLastCompletedAt：时间戳不早于我们的 entryAt，表示同伴在我们进入
//     handleRuntimeGone 之后注册 SUCCESSFUL 了，工作区状态对本波已经覆盖，
//     可以直接退出。失败故意不盖这个戳（见 recordRegisterCompletion），因此同一
//     波里 entryAt 早于一个失败兄弟的掉队者，在失败退避到期后仍可再试——失败
//     不能为别人"兜底"。
//
// entryAt 是 handleRuntimeGone 顶部捕获的墙上时间。now 由调用方传入（而不是在
// 内部读取），这样测试不用 sleep 就能确定性地驱动闸门。
func (d *Daemon) tryClaimRegisterSlot(workspaceID string, entryAt, now time.Time) bool {
	d.runtimeGoneMu.Lock()
	defer d.runtimeGoneMu.Unlock()
	if next, ok := d.reregisterNextAttempt[workspaceID]; ok && now.Before(next) {
		return false
	}
	if last, ok := d.reregisterLastCompletedAt[workspaceID]; ok && !last.Before(entryAt) {
		return false
	}
	d.reregisterNextAttempt[workspaceID] = now.Add(reregisterCoalesceWindow)
	return true
}

// recordRegisterCompletion 记录一次 register 调用的结果。成功时盖上
// lastCompletedAt（让同波掉队者被 tryClaimRegisterSlot 拦下）并清空在途槽位，
// 使真正更晚的 runtime 删除可以立即认领。失败时把 reregisterNextAttempt 延长
// 一个失败退避，并且故意不盖 lastCompletedAt——失败的 register 没有覆盖任何
// workspace 状态，所以 entryAt 早于那次失败的掉队者，在退避到期后仍应被允许
// 重试。workspaceSyncLoop 只在 workspace 的 runtimeIDs 完全清空时才重试，部分
// 删除的恢复只能靠掉队者路径。
func (d *Daemon) recordRegisterCompletion(workspaceID string, completedAt time.Time, err error) {
	d.runtimeGoneMu.Lock()
	defer d.runtimeGoneMu.Unlock()
	if err != nil {
		d.reregisterNextAttempt[workspaceID] = completedAt.Add(reregisterFailureBackoff)
		return
	}
	d.reregisterLastCompletedAt[workspaceID] = completedAt
	delete(d.reregisterNextAttempt, workspaceID)
}

// recoveryContext 返回给长生命周期恢复类 HTTP 调用（re-register、
// recover-orphans）用的 daemon 根 context——它们必须活过心跳循环拆掉某个
// per-runtime context 的时刻。daemon 不是经由 Run() 启动时（如单元测试夹具）
// 回退到 Background。
func (d *Daemon) recoveryContext() context.Context {
	if d.rootCtx != nil {
		return d.rootCtx
	}
	return context.Background()
}

// removeStaleRuntime 把一个 runtime ID 从其属主 workspace 的 runtimeIDs 列表、
// daemon 级 runtimeIndex 和 WS 心跳新鲜度 map 里删掉。若该 runtime 有被跟踪，
// 返回 workspace ID 和 true；否则返回 "" 和 false。
//
// 调用方绝不能替换 workspaceState 指针——只能原地改字段——因为 ensureRepoReady
// 会在漫长的 repo-sync 调用期间握着 workspaceState.repoRefreshMu。同样约束见
// syncWorkspacesFromAPI。
func (d *Daemon) removeStaleRuntime(runtimeID string) (string, bool) {
	d.mu.Lock()
	var workspaceID string
	for wsID, ws := range d.workspaces {
		found := false
		filtered := ws.runtimeIDs[:0:0]
		for _, rid := range ws.runtimeIDs {
			if rid == runtimeID {
				found = true
				continue
			}
			filtered = append(filtered, rid)
		}
		if found {
			ws.runtimeIDs = filtered
			workspaceID = wsID
			break
		}
	}
	if workspaceID == "" {
		d.mu.Unlock()
		return "", false
	}
	delete(d.runtimeIndex, runtimeID)
	d.mu.Unlock()

	d.wsHBMu.Lock()
	delete(d.wsHBLastAck, runtimeID)
	d.wsHBMu.Unlock()

	return workspaceID, true
}

// workspaceNeedsRuntimeRecovery 报告某个被跟踪的 workspace 现在是否零 runtime
// ID——即 handleRuntimeGone 剪掉全部 runtime 且其内联 re-register 失败后到达的
// 状态。workspaceSyncLoop 每个 tick 都调它，好让 workspace 无需等外部触发就能
// 恢复。
func (d *Daemon) workspaceNeedsRuntimeRecovery(workspaceID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		return false
	}
	return len(ws.runtimeIDs) == 0
}

// reregisterWorkspaceAfterRuntimeGone 调 registerRuntimesForWorkspace 并原地更新
// 既有的 workspaceState。注册响应对这个 workspace 的 runtime 集合是权威的——
// 每个已配置 provider 都包含在内，UpsertAgentRuntime 对存活 provider 返回同一行
// ID、对服务器侧已删除的返回新 ID。必须整体替换而不是追加：否则"多 provider
// workspace 只删了一个 runtime"的部分恢复，会给每个没被删的 provider 造出重复
// 行。
//
// workspaceState 指针绝不替换（见 syncWorkspacesFromAPI 关于 repoRefreshMu 的
// 不变式），只改字段。
//
// applyRegisterResponseInPlace 把一份新鲜的 /api/daemon/register 响应并回
// workspaceState 与 runtimeIndex，而不替换 workspaceState 指针（见
// syncWorkspacesFromAPI 关于 repoRefreshMu 的不变式）。它是 runtime_gone 恢复和
// profile 漂移刷新共享的收敛器；两个调用方只差后续副作用（RecoverOrphans /
// Deregister），所以那些留在调用点处理。
//
// 返回：
//   - newIDs:     本响应返回的 runtime ID，按返回顺序排。这是调用后 daemon 权威
//     的当前 runtime 集合。
//   - droppedIDs: 本次调用前被跟踪、却没能在响应里活下来的 runtime ID。调用方要
//     Deregister 它们，让服务器立刻标离线，而不是等 150 秒的陈旧心跳清扫。在
//     runtime_gone 路径上，触发的那行已在服务器侧被删（本地也已在注册前剪掉），
//     但这里掉下的"兄弟"——比如 daemon 配置里删掉的 provider、或被禁用的
//     profile——服务器上还留着一条活行，必须被反注册。
//   - ok:         当 workspace 在 register 调用与本次 apply 之间被遗忘（如用户
//     离开 workspace、syncWorkspacesFromAPI 把它移除）时为 false。此时调用方必须
//     静默中止——已没有状态可更新。
//
// profileSig 是注册期间捕获的摘要；空值表示 appendProfileRuntimes 显式给出的
// "fetch 失败，保留旧签名"哨兵。
//
// preserveProviders 列出本响应对其不具权威性的内置 provider，按 provider 索引
// （见 preserveProvidersFromProbe）。它们要么因探测失败（瞬态）而缺席载荷、
// 因此也缺席响应，要么被一条没有降级权限的路径确认为低于最低版本。两种情况
// 下，在这里丢掉它们的行都会在"唯一安全执行该操作"的路径之外把一个 runtime
// 拉下线，所以保留它们既有的内置 runtime 行。
//
// 已处于降级 hold 之下的 provider 是另一回事，下面仍会拒绝：那条裁决由
// demoteBelowMinimumRuntimes 在 claim 屏障下达成并已删行，本响应只是比它早发出
// 而已。
func (d *Daemon) applyRegisterResponseInPlace(workspaceID string, resp *RegisterResponse, profileSig string, preserveProviders map[string]string) (newIDs, droppedIDs []string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, exists := d.workspaces[workspaceID]
	if !exists {
		return nil, nil, false
	}
	// 拒绝"自本次 register 发出以来已被降级"的 provider 的条目。载荷早于裁决，
	// 响应便把这个 provider 描述得像健康一样；在这里给它建索引会撤销
	// demoteBelowMinimumRuntimes 的成果。两边都在 d.mu 下做这件事，于是二者
	// 全序：要么降级先跑、这里拒绝迟到的响应，要么 apply 先跑、降级随后删掉它
	// 刚加的行。无论哪种，provider 最终都离线。被拒 ID 并入 droppedIDs，让调用方
	// 去反注册服务器 upsert 回来的那行。
	newIDs = make([]string, 0, len(resp.Runtimes))
	newIDSet := make(map[string]struct{}, len(resp.Runtimes))
	rejected := make(map[string]struct{})
	for _, rt := range resp.Runtimes {
		if rt.ProfileID == "" && d.providerDemotedLocked(rt.Provider) {
			rejected[rt.ID] = struct{}{}
			droppedIDs = append(droppedIDs, rt.ID)
			continue
		}
		newIDs = append(newIDs, rt.ID)
		newIDSet[rt.ID] = struct{}{}
	}
	// 把服务器没返回的旧 runtime ID 的 runtimeIndex 条目删掉——"在既有 provider
	// 上 upsert"通常没有这类条目，但 daemon 配置变更（删 provider）或 profile
	// 被禁用时，不删就会泄漏条目。
	kept := newIDs
	for _, oldID := range ws.runtimeIDs {
		if _, stillThere := newIDSet[oldID]; stillThere {
			continue
		}
		if _, alreadyRejected := rejected[oldID]; alreadyRejected {
			// 服务器给一个已被降级的 provider 返回了这个 ID，且它已在 droppedIDs
			// 里；删索引条目即可，别记第二遍。
			delete(d.runtimeIndex, oldID)
			continue
		}
		if rt, tracked := d.runtimeIndex[oldID]; tracked && rt.ProfileID == "" {
			if _, preserve := preserveProviders[rt.Provider]; preserve {
				kept = append(kept, oldID)
				continue
			}
		}
		delete(d.runtimeIndex, oldID)
		droppedIDs = append(droppedIDs, oldID)
	}
	for _, rt := range resp.Runtimes {
		if _, skip := rejected[rt.ID]; skip {
			continue
		}
		d.runtimeIndex[rt.ID] = rt
	}
	// 响应是权威的——整体替换，不追加。替换还能接住 UpsertAgentRuntime 偶尔给
	// 存活 provider 返回不同 ID 的边角情况（如 schema 变更）；daemon 收敛到服务器
	// 的说法，不留陈旧的保活 goroutine。
	ws.runtimeIDs = kept
	if resp.ReposVersion != "" {
		ws.reposVersion = resp.ReposVersion
		ws.allowedRepoURLs = repoAllowlist(resp.Repos)
	}
	if len(resp.Settings) > 0 {
		ws.settings = resp.Settings
	}
	// 只在 fetch 成功时刷新缓存的 profile 签名；空 sig 表示 GetRuntimeProfiles
	// 失败，此时必须保留旧签名，否则下一个 sync tick 会把真漂移误判成"一切已
	// 同步"。
	if profileSig != "" {
		ws.profileSetSig = profileSig
	}
	return newIDs, droppedIDs, true
}

// mergeBuiltinRegisterResponse 以"追加"方式应用一份只含 built-in 的注册响应：
// 返回的内置 runtime 会被建索引并追加，响应没提到的旧 runtime ID 保持原样。
//
// applyRegisterResponseInPlace 把响应当权威并丢掉未提及的 ID——这对"刻意重新
// 推导某 workspace 整个 runtime 集合"的收敛路径是对的。但 CLI 发现（MUL-5439）
// 不行：那份载荷只带 built-in runtime，按权威规则它会把这个 workspace 的自定义
// profile runtime 从 runtimeIndex 里逐出、掐掉它们的保活，甚至可能发生在任务
// 中途。服务器的 register 端点是对每个条目纯 upsert、什么都不清，所以被省略的
// runtime 服务器侧仍然存在，本地必须保留。
//
// 两处刻意省略，让这条路径不掺和自定义 profile 的事：
//
//   - 带 ProfileID 的条目一律忽略。发现只注册 built-in，所以这是一道不变式
//     护栏：自定义 runtime 绝不能经由这里进入集合。
//   - profileSetSig 不读也不写。把发现期间观察到的签名缓存下来，等于告诉
//     refreshWorkspaceRuntimeProfiles"profile 集合已收敛"，此刻被禁用的 profile
//     会让它的 runtime 永远活着。
//
// ID 轮换仍会被处理：当响应给 workspace 已有的内置 provider 返回了不同 ID 时，
// 那个具体的旧 ID 会被替换，而不是累积出重复的保活。
func (d *Daemon) mergeBuiltinRegisterResponse(workspaceID string, resp *RegisterResponse) (newIDs []string, revived revivedRuntimes, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, exists := d.workspaces[workspaceID]
	if !exists {
		return nil, revivedRuntimes{}, false
	}

	// 按 provider 索引 workspace 当前的内置 runtime，这样轮换后的 ID 会替换它的
	// 前任，而不是把它翻倍。
	existingByProvider := make(map[string]string, len(ws.runtimeIDs))
	kept := make([]string, 0, len(ws.runtimeIDs)+len(resp.Runtimes))
	present := make(map[string]struct{}, len(ws.runtimeIDs))
	for _, id := range ws.runtimeIDs {
		if rt, found := d.runtimeIndex[id]; found && rt.ProfileID == "" {
			existingByProvider[rt.Provider] = id
		}
		kept = append(kept, id)
		present[id] = struct{}{}
	}

	for _, rt := range resp.Runtimes {
		if rt.ProfileID != "" {
			// 不归我们管；自定义 profile 的生命周期归漂移路径。
			continue
		}
		// 自本 register 发出以来被降级：响应早于裁决，在这里把它带回会撤销
		// 降级。两边都在 d.mu 下跑，所以二者全序——见 applyRegisterResponseInPlace
		// 里同样的护栏。调用方负责反注册服务器 upsert 回去的行。
		if d.providerDemotedLocked(rt.Provider) {
			revived.add(d, rt.ID, rt.Provider)
			continue
		}
		d.runtimeIndex[rt.ID] = rt
		if _, already := present[rt.ID]; already {
			continue
		}
		if oldID, rotated := existingByProvider[rt.Provider]; rotated && oldID != rt.ID {
			// 同一 runtime、换了 ID：原地换掉并退役旧条目。
			for i, id := range kept {
				if id == oldID {
					kept[i] = rt.ID
					break
				}
			}
			delete(d.runtimeIndex, oldID)
			delete(present, oldID)
		} else {
			kept = append(kept, rt.ID)
		}
		present[rt.ID] = struct{}{}
		existingByProvider[rt.Provider] = rt.ID
		newIDs = append(newIDs, rt.ID)
	}
	ws.runtimeIDs = kept

	if resp.ReposVersion != "" {
		ws.reposVersion = resp.ReposVersion
		ws.allowedRepoURLs = repoAllowlist(resp.Repos)
	}
	if len(resp.Settings) > 0 {
		ws.settings = resp.Settings
	}
	return newIDs, revived, true
}

// providerDemotedLocked 报告某 provider 当前是否被按"低于最低支持版本"扣着。
// 调用方必须持有 d.mu——降级记录与每条注册响应 apply 共享这把锁，正是为了让
// 迟到的响应永远钻不进两者之间。
func (d *Daemon) providerDemotedLocked(provider string) bool {
	_, demoted := d.demotedProviders[provider]
	return demoted
}

// demotedOfflineReasonLocked 返回为某被降级 provider 记录的结构化原因；该裁决
// 不带原因（below-minimum）或该 provider 未被降级时返回 nil。调用方必须持有
// d.mu。
func (d *Daemon) demotedOfflineReasonLocked(provider string) *RuntimeOfflineReason {
	record, demoted := d.demotedProviders[provider]
	if !demoted {
		return nil
	}
	return record.offline
}

// revivedRuntimes 是一次注册响应替"已被 daemon 定罪"的 provider 带回来的行：
// 待再次拉下线的 ID，以及要逐行重新挂上的原因——因为那次注册的 upsert 刚把它
// 覆盖掉了。
type revivedRuntimes struct {
	ids     []string
	reasons map[string]RuntimeOfflineReason
}

// reasonsFor 把原因收窄到真正要被反注册的行。调用方会先复查跟踪状态——中途合法
// 回来的行会被从列表里剔除——给一行"我们已不再拉下线"的 runtime 发原因，会把
// 原因挂到一个健康的 runtime 上。
func (r revivedRuntimes) reasonsFor(runtimeIDs []string) map[string]RuntimeOfflineReason {
	if len(r.reasons) == 0 {
		return nil
	}
	out := make(map[string]RuntimeOfflineReason, len(runtimeIDs))
	for _, id := range runtimeIDs {
		if reason, ok := r.reasons[id]; ok {
			out[id] = reason
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// add 记录一行"复活"的行。调用方必须持有 d.mu（它会读降级记录）；对没有结构化
// 原因的 provider 是 no-op——那些行仍要反注册，只是没东西可重新挂上。
func (r *revivedRuntimes) add(d *Daemon, runtimeID, provider string) {
	r.ids = append(r.ids, runtimeID)
	reason := d.demotedOfflineReasonLocked(provider)
	if reason == nil {
		return
	}
	if r.reasons == nil {
		r.reasons = make(map[string]RuntimeOfflineReason, 1)
	}
	r.reasons[runtimeID] = *reason
}

// demotionRecord 是关于"盘上二进制"的一条已确认裁决：产出它的证据（被拒的版本，
// 或该 CLI 为何跑不起来），外加确立"何时达成"的 demotionSeq 刻度。
//
// offline 是结构化的一半，保留它是因为服务器可能会丢。在裁决之前发出的
// register 仍会把 runtime 行 upsert 出来，而那次 upsert 会整段覆盖元数据——于是
// 本 daemon 刚存的理由没了，把复活行再次拉下线的清理不得不重新挂上。没有它，
// 服务器最终只会看到 "offline, no reason"，把这次拒绝降级成"等机器吧"
// （MUL-6164）。
type demotionRecord struct {
	evidence string
	offline  *RuntimeOfflineReason
	seq      uint64
}

// setLifecycleCtx records the context Run was handed.
func (d *Daemon) setLifecycleCtx(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lifecycleCtx = ctx
}

// daemonLifecycleCtx returns the context that bounds background work outliving
// a single round. Falls back to Background so a zero-value Daemon (tests, and
// any path reached before Run) still works.
func (d *Daemon) daemonLifecycleCtx() context.Context {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lifecycleCtx == nil {
		return context.Background()
	}
	return d.lifecycleCtx
}

// markProvidersDemoted records a CONFIRMED verdict so a register response still
// in flight cannot revive the provider. Callers must hold d.mu.
func (d *Daemon) markProvidersDemotedLocked(providers map[string]runtimeVerdict) {
	if len(providers) == 0 {
		return
	}
	if d.demotedProviders == nil {
		d.demotedProviders = make(map[string]demotionRecord, len(providers))
	}
	// 每批裁决一个刻度：这里写的每条记录，都比任何"在本调用之前快照过计数器"
	// 的探测轮要新。
	d.demotionSeq++
	for provider, verdict := range providers {
		d.demotedProviders[provider] = demotionRecord{
			evidence: verdict.reason,
			offline:  verdict.offline,
			seq:      d.demotionSeq,
		}
	}
}

// notExecutableConfirmWindow 是一条"OS 拒绝运行这个文件"的裁决必须持续复现多久、
// daemon 才动手的时长。
//
// This gates every condemnable verdict, not just the exec-format one: the
// profile verdicts rest on a single observation too, and demotion is what routes
// live work away from a machine.
// A var so tests can collapse the wait.
var condemnedConfirmWindow = time.Minute

// condemnedKey namespaces a confirmation clock per verdict, so a provider that
// moves between condemnable states starts a fresh window instead of inheriting
// the time earned by a different problem.
func condemnedKey(verdict builtinProbeVerdict, provider string) string {
	return strconv.Itoa(int(verdict)) + ":" + provider
}

// confirmCondemned records that this round found provider in the given
// condemnable state and reports whether the verdict is now old enough to act on.
//
// The first sighting only starts the clock. Two sightings are required no matter
// how the window is configured, so concurrent probe rounds — four callers reach
// detectBuiltinRuntimes — cannot combine into an instant demotion.
func (d *Daemon) confirmCondemned(verdict builtinProbeVerdict, provider string, now time.Time) bool {
	key := condemnedKey(verdict, provider)
	d.mu.Lock()
	defer d.mu.Unlock()
	first, seen := d.condemnedSince[key]
	if !seen {
		if d.condemnedSince == nil {
			d.condemnedSince = make(map[string]time.Time, 1)
		}
		d.condemnedSince[key] = now
		return false
	}
	return now.Sub(first) >= condemnedConfirmWindow
}

// clearCondemned forgets every pending verdict for a provider that probed OK, so
// a later unrelated failure starts its own confirmation window instead of
// inheriting a stale one.
func (d *Daemon) clearCondemned(provider string) {
	suffix := ":" + provider
	d.mu.Lock()
	defer d.mu.Unlock()
	for key := range d.condemnedSince {
		if strings.HasSuffix(key, suffix) {
			delete(d.condemnedSince, key)
		}
	}
}

// demotionSeqSnapshot 返回当前降级计数器。探测轮在采样任何版本之"前"先取它，
// 好让稍后的 clearProviderDemotions 能证明自己的证据晚于某条裁决——而不只是
// "到达得更晚"。
func (d *Daemon) demotionSeqSnapshot() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.demotionSeq
}

// clearProviderDemotions 清掉"版本再次探测到可接受"的 provider 的裁决——这正是
// 让 converge 把它们重新注册回来的前提。
//
// sampledAfter 是调用轮在探测之前快照的计数器。那条快照之后记录的 hold，是比
// 本轮看到的任何东西都"新"的证据，所以要留着：四个调用方并发探测，而采样发生
// 在锁外，"最后返回"说明不了"最后观测"。与降级重叠的那轮干脆不清，下一轮——
// 它在裁决存在之后才开始，采样不可能早于裁决——再放行。恢复最多晚一轮；而拿着
// 陈旧证据去清，会让一份过期的注册响应穿过其它所有防线。
func (d *Daemon) clearProviderDemotions(providers []string, sampledAfter uint64) {
	if len(providers) == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.demotedProviders) == 0 {
		return
	}
	for _, provider := range providers {
		record, was := d.demotedProviders[provider]
		if !was {
			continue
		}
		if record.seq > sampledAfter {
			d.logger.Info("keeping demotion hold: this probe round started before the verdict",
				"provider", provider, "verdict", record.evidence)
			continue
		}
		delete(d.demotedProviders, provider)
		d.logger.Info("agent CLI is usable again",
			"provider", provider, "previous_verdict", record.evidence)
	}
}

// untrackedRuntimeIDs 把 ids 过滤到 daemon 当前未跟踪的那些——这样"片刻之前才
// 决定的反注册"不能把一条"更新的合法注册刚带回来"的行拉下线。
//
// 每次反注册都在 d.mu 下决定、在放开 d.mu 之后才发出——HTTP 调用绝不能握着
// daemon 锁。在那段间隙里完成的恢复式注册会重建同一服务器侧行，通常还顶着同一
// runtime ID，然后更早的那次清理会敲掉刚恢复的行。
//
// 这个过滤只是保证的一半，单独看它是个 TOCTOU：恢复同样可能正好在"过滤"与
// "请求"之间完成。因此调用方必须把两者都放进该工作区的注册锁里跑——那正是让
// "检查"与 Deregister 成为针对该工作区其它一切注册的一个有序步骤的原因。
func (d *Daemon) untrackedRuntimeIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, tracked := d.runtimeIndex[id]; tracked {
			continue
		}
		out = append(out, id)
	}
	return out
}

func (d *Daemon) reregisterWorkspaceAfterRuntimeGone(ctx context.Context, workspaceID string) error {
	var newIDs []string
	// 发送、应用与清理作为一个有序步骤——见 workspaceRegisterLock。
	err := d.withWorkspaceRegisterLock(workspaceID, func() error {
		resp, profileSig, preserve, err := d.registerRuntimesForWorkspaceLocked(ctx, workspaceID)
		if err != nil {
			return fmt.Errorf("register runtimes: %w", err)
		}

		ids, droppedIDs, ok := d.applyRegisterResponseInPlace(workspaceID, resp, profileSig, preserve)
		if !ok {
			return fmt.Errorf("workspace %s no longer tracked", workspaceID)
		}
		newIDs = ids

		for _, rid := range newIDs {
			d.logger.Info("re-registered runtime after server-side deletion",
				"workspace_id", workspaceID, "runtime_id", rid)
		}

		// 这次恢复顺带丢下的兄弟 runtime（从 daemon 配置里删掉的 provider、
		// 被禁用的 profile）在服务器上还留有一条活行——runtime_gone 触发器只删了
		// 它自己的那行。主动把它们标为离线，与漂移路径对齐，而不是让它们继续
		// 可认领直到陈旧心跳清扫。
		d.deregisterDroppedRuntimes(ctx, workspaceID, droppedIDs, "runtime_gone recovery", nil)
		return nil
	})
	if err != nil {
		return err
	}
	d.notifyRuntimeSetChanged()

	// 把上一任（现已删除的）runtime 正在处理的任务告诉服务器，复刻注册路径的
	// recover-orphans 调用。刻意只限定在 runtime_gone 恢复上：那些 runtime 在
	// 服务器侧是真没了，所以仍挂在它们行上的 dispatched/running/
	// waiting_local_directory 任务都是需要"失败并重试"的孤儿。漂移刷新路径
	// （也喂 applyRegisterResponseInPlace）刻意跳过这步，因为其幸存的 runtime ID
	// 可能仍在为用户执行任务（MUL-3332）。
	for _, rid := range newIDs {
		if err := d.client.RecoverOrphans(ctx, rid); err != nil {
			d.logger.Warn("recover-orphans after re-register failed",
				"runtime_id", rid, "error", err)
		}
	}
	return nil
}

// runtimeSetWatcher 是一个微型发布/订阅器，用于广播 runtime 集合变化。
// 它存在是因为不止一个监督循环（taskWakeupLoop、heartbeatLoop、pollLoop）需要
// 对 runtime 集合变化做出反应；单个带缓冲 channel 会互相竞争，导致只有第一个
// 监听者能感知到每次变化。
//
// 每个订阅者各得一个 1 槽 channel；错过的 nudge 会合并成单个信号——订阅者应当
// 通过 allRuntimeIDs() 重新推导当前 runtime 集合，而不是依赖边沿计数。
type runtimeSetWatcher struct {
	mu          sync.Mutex
	subscribers map[chan struct{}]struct{}
}

func newRuntimeSetWatcher() *runtimeSetWatcher {
	return &runtimeSetWatcher{subscribers: make(map[chan struct{}]struct{})}
}

// Subscribe 返回一个每当 runtime 集合变化时收到非阻塞 nudge 的 channel，以及
// 一个用完必须调用的退订函数。
func (w *runtimeSetWatcher) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	w.mu.Lock()
	w.subscribers[ch] = struct{}{}
	w.mu.Unlock()
	return ch, func() {
		w.mu.Lock()
		delete(w.subscribers, ch)
		w.mu.Unlock()
	}
}

func (w *runtimeSetWatcher) notify() {
	w.mu.Lock()
	defer w.mu.Unlock()
	// 向每个订阅者投递一次 nudge；对方 channel 已满说明它还没消费上一次变化，
	// 直接丢弃即可（订阅者会被唤醒后重新拉全量集合）。
	for ch := range w.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// wsHeartbeatFreshness 定义一次 WS 心跳 ack 被视为"足够新鲜"、足以抑制 HTTP
// 心跳的时长。窗口取 HeartbeatInterval 的 2 倍：一次丢失的 WS ack 仍能保持 HTTP
// 被抑制，但连续两次 ack 丢失（约 30 秒 WS 静默）就会重新启用 HTTP——仍然远在
// 服务器侧 45 秒离线阈值之内。
func (d *Daemon) wsHeartbeatFreshness() time.Duration {
	if d.cfg.HeartbeatInterval <= 0 {
		return 30 * time.Second
	}
	return 2 * d.cfg.HeartbeatInterval
}

// recordWSHeartbeatAck 为该 runtime 盖上"最近收到服务器新鲜 WS 心跳 ack"的
// 时间戳。由 WS 读泵调用。
func (d *Daemon) recordWSHeartbeatAck(runtimeID string) {
	if runtimeID == "" {
		return
	}
	d.wsHBMu.Lock()
	d.wsHBLastAck[runtimeID] = time.Now()
	d.wsHBMu.Unlock()
}

// wsHeartbeatRecentlyAcked 报告该 runtime 是否在新鲜窗口内收到过 WS 心跳 ack。
// HTTP 心跳循环据此在 WS 已维持 runtime 存活时跳过重复工作。
func (d *Daemon) wsHeartbeatRecentlyAcked(runtimeID string) bool {
	d.wsHBMu.RLock()
	last, ok := d.wsHBLastAck[runtimeID]
	d.wsHBMu.RUnlock()
	if !ok {
		return false
	}
	return time.Since(last) < d.wsHeartbeatFreshness()
}

// clearWSHeartbeatAcks 清空所有 WS 心跳新鲜度记录。WS 断连时调用，
// 让 HTTP 心跳在下一个 tick 恢复。
func (d *Daemon) clearWSHeartbeatAcks() {
	d.wsHBMu.Lock()
	for k := range d.wsHBLastAck {
		delete(d.wsHBLastAck, k)
	}
	d.wsHBMu.Unlock()
}

// Run 启动守护进程：解析鉴权、注册 runtimes，然后开始轮询并执行任务。
// 它会先占住健康端口以探测是否已有另一个 daemon 在跑；随后做前置鉴权
// （先续期 PAT 再做首次 workspace 同步），再启动各个后台循环，最后进入
// pollLoop 直到 ctx 被取消（例如收到重启信号）。
func (d *Daemon) Run(ctx context.Context) error {
	if err := configureEmbeddedDsh(d.cfg.Profile); err != nil {
		return fmt.Errorf("prepare embedded DSH bridge: %w", err)
	}
	// 包一层 ctx，使 handleUpdate 能通过 cancel 触发 daemon 优雅重启。
	ctx, cancel := context.WithCancel(ctx)
	d.cancelFunc = cancel
	d.setLifecycleCtx(ctx)
	d.rootCtx = ctx

	// 尽早绑定健康端口，用来探测是否已有另一个 daemon 在运行。
	healthLn, err := d.listenHealth()
	if err != nil {
		return err
	}

	agentNames := make([]string, 0, len(d.agents()))
	for name := range d.agents() {
		agentNames = append(agentNames, name)
	}
	logFields := []any{"version", d.cfg.CLIVersion, "agents", agentNames, "server", d.cfg.ServerBaseURL}
	if d.cfg.Profile != "" {
		logFields = append(logFields, "profile", d.cfg.Profile)
	}
	d.logger.Info("starting daemon", logFields...)
	d.logger.Debug("daemon config resolved",
		"daemon_id", d.cfg.DaemonID,
		"device_name", d.cfg.DeviceName,
		"workspaces_root", d.cfg.WorkspacesRoot,
		"health_port", d.cfg.HealthPort,
		"poll_interval", d.cfg.PollInterval,
		"heartbeat_interval", d.cfg.HeartbeatInterval,
		"agent_timeout", d.cfg.AgentTimeout,
		"idle_watchdog", d.cfg.AgentIdleWatchdog,
		// 显式打印：该值通常由 idle_watchdog 推导而来——不打印的话，运维无法从日志
		// 得知实际生效的工具预算。
		"tool_watchdog", d.cfg.AgentToolWatchdog,
		"opencode_idle_watchdog", d.cfg.OpenCodeIdleWatchdog,
		// 同样由看门狗预算推导而来（Codex 自己的计时器不感知工具调用），因此需要和
		// tool_watchdog 一样的处理：不打印的话，生效的 Codex 预算在超时发生之前都
		// 不可见。
		"codex_semantic_inactivity", d.cfg.CodexSemanticInactivityTimeout,
		"max_concurrent_tasks", d.cfg.MaxConcurrentTasks,
		"gc_enabled", d.cfg.GCEnabled,
		"auto_update", d.cfg.AutoUpdateEnabled,
		"launched_by", d.cfg.LaunchedBy,
	)

	// 在任意任务运行前先给 daemon 拥有的 workspaces 树打上标记。沙箱故障可能把
	// agent 子进程的所有 MULTICA_* 环境变量剥掉；此时按 workdir 的标记只能保护
	// workdir 内的 cwd，一旦子进程逃逸到 workdir 的父目录，就会回退到用户配置里的
	// PAT。根标记让 CLI 在整棵树之下都 fail closed。非致命：Prepare 会在每个任务里
	// 再确保一次。
	if err := execenv.EnsureWorkspacesRootMarker(d.cfg.WorkspacesRoot); err != nil {
		d.logger.Warn("workspaces root marker not written; CLI fail-closed guard limited to task workdirs", "error", err)
	}

	// 从 CLI 配置里加载鉴权 token。
	if err := d.resolveAuth(); err != nil {
		return err
	}

	// 在（可能较慢的）preflight 之前先绑定并服务健康端口，这样 `daemon start` 与
	// 桌面端能看到一个活的 "starting" daemon，而不会在 preflightAuth 期间吃连接
	// 拒绝。preflightAuth 的首次 workspace 同步要通过 exec 每个已配置的 agent 来
	// 探测版本，冷缓存 + 多 agent 时约需 20 秒。存活（端口起来了）与就绪
	// （status:"running"）分开上报：/health 在 d.ready 于 preflight 后置位前一直
	// 报告 "starting"，这样慢的或失败的 preflight 绝不会被误报为已启动的 daemon。
	// resolveAuth 已先行执行，缺失 token 会在开始服务前快速失败。
	go d.serveHealth(ctx, healthLn, time.Now())

	// 在第一次 API 调用前续期 PAT，然后做首次 workspace 同步。两步都放在
	// preflightAuth 里，使"先续期"的顺序不变式只在一个地方被强制，而不会散落在
	// Run 中；测试也能在不跑完整 Run 装配的情况下演练失败路径。
	if err := d.preflightAuth(ctx); err != nil {
		return err
	}

	if err := d.initCodexProcessPool(); err != nil {
		return fmt.Errorf("prepare resident Codex processes: %w", err)
	}
	defer d.codexPool.Close()
	if err := d.initCodebuddyProcessPool(); err != nil {
		return fmt.Errorf("prepare resident CodeBuddy processes: %w", err)
	}
	defer d.codebuddyPool.Close()

	// 关停时反注册所有 runtimes（用全新的 ctx，因为此时 ctx 已被取消）。
	defer d.deregisterRuntimes()

	// 启动 workspace 同步循环，发现新建的工作区。
	go d.workspaceSyncLoop(ctx)
	go d.terminalReportReplayLoop(ctx)

	// 发现启动之后新安装的 agent CLI（MUL-5439）。与 workspace 同步循环分开，
	// 因为后者跑在 30 分钟的一致性间隔上——对"装个 CLI 就出现在 Runtimes 里"
	// 来说太慢了。
	go d.agentDiscoveryLoop(ctx)

	taskWakeups := make(chan taskWakeup, 256)
	go d.taskWakeupLoop(ctx, taskWakeups)
	go d.heartbeatLoop(ctx)
	go d.gcLoop(ctx)
	go d.autoUpdateLoop(ctx)
	go d.tokenRenewalLoop(ctx)

	// Preflight 成功、各后台循环已启动：daemon 已经注册好 runtimes，可以认领并
	// 执行任务了。把 /health 从 "starting" 翻到 "running"——这是 `daemon start`
	// 的就绪等待所阻塞的信号，因此只有在启动真正完成后才上报成功，而不是因为
	// 健康端口起来了就上报。
	d.ready.Store(true)
	d.logger.Debug("background loops launched (workspace-sync, terminal-report-replay, task-wakeup, heartbeat, gc, auto-update, token-renewal); health now reporting ready")
	err = d.pollLoop(ctx, taskWakeups)
	d.logger.Debug("daemon main loop returning", "error", err)
	return err
}

// RestartBinary 返回更新成功后需要重启所指向的新二进制路径；无需重启则返回空串。
func (d *Daemon) RestartBinary() string {
	d.restartMu.Lock()
	defer d.restartMu.Unlock()
	return d.restartBinary
}

// deregisterRuntimes 通知服务器所有 runtimes 即将下线。
func (d *Daemon) deregisterRuntimes() {
	runtimeIDs := d.allRuntimeIDs()
	if len(runtimeIDs) == 0 {
		d.logger.Debug("deregister: no runtimes to deregister")
		return
	}

	d.logger.Debug("deregistering runtimes on shutdown", "count", len(runtimeIDs), "runtime_ids", runtimeIDs)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := d.client.Deregister(ctx, runtimeIDs, nil); err != nil {
		d.logger.Warn("failed to deregister runtimes on shutdown", "error", err)
	} else {
		d.logger.Info("deregistered runtimes", "count", len(runtimeIDs))
	}
}

// resolveAuth 从 CLI 配置中为当前 profile 加载鉴权 token。
func (d *Daemon) resolveAuth() error {
	cfg, err := cli.LoadCLIConfigForProfile(d.cfg.Profile)
	if err != nil {
		return fmt.Errorf("load CLI config: %w", err)
	}
	if cfg.Token == "" {
		loginHint := "'multica login'"
		if d.cfg.Profile != "" {
			loginHint = fmt.Sprintf("'multica login --profile %s'", d.cfg.Profile)
		}
		d.logger.Warn("not authenticated — run " + loginHint + " to authenticate, then restart the daemon")
		return fmt.Errorf("not authenticated: run %s first", loginHint)
	}
	d.client.SetToken(cfg.Token)
	d.logger.Info("authenticated")
	d.logger.Debug("auth token loaded", "profile", d.cfg.Profile, "token_len", len(cfg.Token))
	return nil
}

// allRuntimeIDs 返回所有被跟踪工作区中的全部 runtime ID。
func (d *Daemon) allRuntimeIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var ids []string
	for _, ws := range d.workspaces {
		ids = append(ids, ws.runtimeIDs...)
	}
	return ids
}

// findRuntime 按 ID 查找 Runtime 并返回副本。
func (d *Daemon) findRuntime(id string) *Runtime {
	d.mu.Lock()
	defer d.mu.Unlock()
	if rt, ok := d.runtimeIndex[id]; ok {
		return &rt
	}
	return nil
}

// recordProfileLaunch 记住某个自定义运行时 profile 解析出的可执行文件绝对路径
// 与固定启动参数。由 registerRuntimesForWorkspace 调用。map 懒初始化，这样直接
// 用字面量构造、未预置全部 map 的测试夹具不会 panic。
func (d *Daemon) recordProfileLaunch(profileID, path, version string, fixedArgs []string) {
	if profileID == "" || path == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.profileLaunchSpecs == nil {
		d.profileLaunchSpecs = make(map[string]profileLaunchSpec)
	}
	d.profileLaunchSpecs[profileID] = profileLaunchSpec{
		path:      path,
		version:   version,
		fixedArgs: append([]string(nil), fixedArgs...),
	}
}

// customProfileLaunchForRuntime 返回某认领任务 runtime 对应的自定义可执行路径与
// 固定参数，以及该 runtime 是否为自定义 profile runtime。对内置 runtime（无
// profile）以及"profile 命令从未在这台主机解析成功"的 runtime 返回 false。
func (d *Daemon) customProfileLaunchForRuntime(runtimeID string) (profileLaunchSpec, bool) {
	if runtimeID == "" {
		return profileLaunchSpec{}, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rt, ok := d.runtimeIndex[runtimeID]
	if !ok || rt.ProfileID == "" {
		return profileLaunchSpec{}, false
	}
	spec, ok := d.profileLaunchSpecs[rt.ProfileID]
	if !ok || spec.path == "" {
		return profileLaunchSpec{}, false
	}
	spec.fixedArgs = append([]string(nil), spec.fixedArgs...)
	return spec, true
}

// runtimeVersionProbeConcurrency 限制注册期间并发运行的 `<cli> --version` 探测
// 数量。版本探测受"进程启动"约束而非 CPU 约束，且每次探测自带超时，因此即使
// 一台主机装了多个 agent CLI，适度的扇出也是安全的。
const runtimeVersionProbeConcurrency = 8

// runtimeVersionProbeAttempts 限制单轮探测中某个 provider 的 `<cli> --version`
// 最多尝试几次，之后该 provider 会被从注册载荷中剔除。
//
// 现在一轮要服务整批工作区的注册（MUL-5225），一次失败不再只让单个工作区损失
// runtime——它会波及该批注册的所有工作区，而且在重启或独立重注册之前没人会
// 重新探测。这种放大值得重试一次，因为失败通常是瞬时的：cfg.Agents 只保存
// 守护进程启动时能解析到的 CLI，之后探测失败通常是版本管理器原地换二进制
// （路径消失、ETXTBSY）或启动洪峰下 fork/exec 短暂失败。只重试失败的 provider
// 使整轮保持 O(M)——绝不会重新引入按工作区探测。
const runtimeVersionProbeAttempts = 2

// runtimeVersionProbeRetryDelay 拉开重试间隔，让在途的二进制替换或瞬时
// fork/exec 失败有时间平复。provider 是并发探测的，因此一轮只付一次该延迟，
// 而不是每个失败 provider 各付一次。测试可覆盖。
var runtimeVersionProbeRetryDelay = 500 * time.Millisecond

// runtimeVersionProbeRetryWindow 用"失败返回得有多快"来把关重试。烧满超时的探测
// 是挂死的 CLI 而非小故障，重试它会把整轮最坏情况翻倍——正是在探测被并行化之前，
// 把桌面端 runtime 步骤推进"no runtime found"空态的那种延迟（MUL-5119）。
// 只有快速失败（即瞬时失败）才重试。
//
// 窗口按整次尝试（自愈也算在内）度量：消失的钉住路径会让 resolveAgentEntry 对
// 重解析候选做自己的版本探测，一次尝试可能把全部预算耗在那里，随后外层对过期
// 路径的探测仍会瞬间失败。
//
// 被最低版本门槛拒绝的自愈候选也会在该窗口内重试，这是刻意的：该拒绝是对
// "升级窗口期间 PATH 解析到什么"的判定，而重试给升级一拍时间发布新二进制，
// 之后判定才会降级 runtimes（见 probeBuiltinRuntime）。
//
// 测试可覆盖。
var runtimeVersionProbeRetryWindow = time.Second

// builtinProbeVerdict 区分 provider 失败的几种方式，调用方必须区别对待。
//
// "读不到版本"天然是瞬时的——CLI 忙、升级中途或 fork/exec 抖动——正确响应是
// 保持已注册的状态不动、稍后再试。另外两种是对"此刻就在磁盘上的二进制"的已
// 确认判定，任一种还继续注册，都意味着守护进程一直在把工作交给它已证明无法
// 使用的 CLI："读到了版本但低于最低支持版本"，以及"操作系统根本拒绝执行该
// 文件"。把它们折叠成一个 bool 会让已确认判定与小故障无法区分——这正是曾经让
// 降级后的 CLI 能继续认领任务的原因。
type builtinProbeVerdict int

const (
	builtinProbeOK builtinProbeVerdict = iota
	builtinProbeUnavailable
	builtinProbeBelowMinimum
	// builtinProbeNotExecutable：文件能解析到，但操作系统拒绝把它当可运行程序
	// 执行（现场案例是 postinstall 被拦截的 npm 占位 stub——MUL-6164）。与
	// below-minimum 同样确定：在有人重装之前同样的字节每次都会被拒绝，所以重试
	// 解决不了问题。
	builtinProbeNotExecutable
	// builtinProbeMissingProfile: the CLI resolved and runs, but the runtime
	// profile its backend speaks through is not installed for it. DSH is the
	// case in the field: the Multica profile is what gives `dsh` its --stdio
	// protocol, so a bare binary answers `--version` and `--probe` refuses.
	// Deterministic like the two above — the same binary keeps refusing until
	// someone installs the profile — and the reason carries that repair.
	builtinProbeMissingProfile
	// builtinProbeIncompatibleProfile: something answered `--probe`, with a
	// protocol version this daemon does not drive — a bundle older than the
	// check, or a newer one this build has yet to catch up with. Reported, and
	// deliberately neither installable NOR demotable: the profile is there on
	// purpose, so installing over it would replace a considered configuration,
	// and this daemon can equally be the stale side of the skew, so taking a
	// live runtime offline over it is a destructive answer to a version
	// question. It stops a fresh registration and nothing more.
	builtinProbeIncompatibleProfile
)

// demotableBuiltinProbeVerdict reports whether a verdict may take a LIVE
// runtime offline, as opposed to merely keeping an unregistered provider
// unregistered.
//
// The distinction is the whole safety property: demotion routes work away from
// a machine, so it belongs only to verdicts that say the CLI definitely cannot
// serve work AND that this daemon is in a position to judge.
//
// builtinProbeIncompatibleProfile fails the second half and is deliberately
// absent. A protocol version this daemon does not drive can equally mean the
// daemon is the stale side of the skew, and tearing down a live runtime because
// THIS build is behind is a destructive answer to a version question. It still
// blocks a fresh registration and is reported on /health; what it must not do is
// take work away from a runtime already serving it.
//
// builtinProbeUnavailable is absent for the plainer reason: it means nothing was
// learned this round.
func demotableBuiltinProbeVerdict(verdict builtinProbeVerdict) bool {
	switch verdict {
	case builtinProbeBelowMinimum, builtinProbeNotExecutable, builtinProbeMissingProfile:
		return true
	}
	return false
}

// builtinProbeNeedsConfirmation reports whether a demotable verdict must
// reproduce across two probe rounds (confirmCondemned) before it is acted on.
//
// Only builtinProbeBelowMinimum does not: it is a pure function of a version
// string this round already parsed, so a second look reaches the same
// conclusion. The other two are deterministic about the state they describe but
// rest on an observation that is not — an installer overwriting the bin entry in
// place (the very repair we tell users to run) or a DSH upgrade moving the
// profile under a probe. Requiring a second sighting a window later makes those
// windows impossible to mistake for a verdict, and costs a genuinely broken
// provider one extra round.
func builtinProbeNeedsConfirmation(verdict builtinProbeVerdict) bool {
	return verdict != builtinProbeBelowMinimum
}

// dshMissingProfileReason is the user-facing explanation for a
// builtinProbeMissingProfile drop. It names the repair because nothing else in
// the daemon's output would: the CLI itself is installed, resolvable and
// answers `--version`, so "not installed" is true only of the profile.
const dshMissingProfileReason = "the Multica runtime profile is not installed; add the Multica DSH runtime bundle to the `multica` profile with `dsh plugin`, or set MULTICA_DSH_PROFILE_BUNDLE so the daemon installs it"

// dshProfileInstallStartedReason replaces it when the operator configured a
// bundle for the daemon to install (MULTICA_DSH_PROFILE_BUNDLE), so /health
// separates "wait for the install" from "nothing is going to happen".
const dshProfileInstallStartedReason = "the Multica runtime profile is not installed; installing the configured bundle now and re-probing when it finishes"

// dshInstallGaveUpReason replaces the "installing now" detail once the
// automatic install has stopped without producing a profile. It says the
// attempt happened and ended, because a reader who saw the earlier "installing"
// reason needs to know which of the two states they are looking at.
const dshInstallGaveUpReason = "the Multica runtime profile is not installed; the automatic install failed, so it has to be installed by hand"

// dshIncompatibleProfileReason is the /health reason for a profile that answers
// with a protocol this daemon does not drive. It names both sides because
// either can be the stale one.
const dshIncompatibleProfileReason = "the Multica runtime profile answers with a protocol version this daemon does not drive; update the profile bundle or the daemon"

// dshProbeFailedReason is the transient reason: the probe did not answer with a
// probe frame at all — a timeout, a failed exec, or unparseable output. It is
// deliberately not phrased as a version problem, which would send a reader
// looking in the wrong place.
const dshProbeFailedReason = "the DSH runtime profile probe returned no usable answer"

// runtimeVerdict is one provider's confirmed verdict: the human reason that
// goes to /health and the daemon log, plus — when the cause is one the user has
// to act on — the structured record the server stores on the runtime row.
//
// 两半刻意分开。reason 是给读日志的运维看的散文；offline 是稳定的 code 加修复
// 命令，供客户端围绕它本地化自己的句子。dispatch/reason.go 的规则是：reason code
// 在其来源处决定，绝不从人类可读字符串反推——这里就是那个来源。
type runtimeVerdict struct {
	reason  string
	offline *RuntimeOfflineReason
}

// newRuntimeVerdict 把探测判定与"服务器需要知道的内容"配成对。只有需要用户修复
// 的判定才携带 offline reason：低于最低版本的 CLI 维持现状行为（runtime 下线、
// 工作排队），因为是否改变"那种情况何时阻断触发"是与本改动分离的另一个产品
// 决策。
//
// execPath is the pinned entry point, which is the right path for this verdict:
// a file the OS refuses to execute is present, so the self-heal that would have
// re-resolved a vanished path never runs, and the probe failed on this exact
// file.
func newRuntimeVerdict(verdict builtinProbeVerdict, reason, execPath string, installing bool) runtimeVerdict {
	switch verdict {
	case builtinProbeNotExecutable:
		offline := &RuntimeOfflineReason{Code: RuntimeOfflineCodeNotExecutable, Detail: reason}
		if repair, ok := agent.ExecFormatRepairFor(execPath); ok {
			offline.Repair = &repair
		}
		return runtimeVerdict{reason: reason, offline: offline}
	case builtinProbeMissingProfile:
		// The same class of finding as an unrunnable file: the machine is
		// reachable and the CLI cannot serve work, so the server must refuse
		// the trigger and say what is missing rather than queue behind a wait
		// that never ends. The one exception is an install the daemon is
		// running right now, which is stated explicitly instead of being
		// implied by an absent reason — and withdrawn by
		// withdrawDshInstallWait if that install gives up.
		offline := &RuntimeOfflineReason{
			Code:       RuntimeOfflineCodeDshProfile,
			Detail:     reason,
			Installing: installing,
		}
		repair := dshProfileRepair()
		offline.Repair = &repair
		return runtimeVerdict{reason: reason, offline: offline}
	}
	return runtimeVerdict{reason: reason}
}

// probeBuiltinRuntime 解析并探测一个内置 provider 的版本，快速失败最多重试
// runtimeVersionProbeAttempts 次。判定告诉调用方该怎样对待剔除：builtinProbeUnavailable
// 表示版本读不到（或读不懂）——瞬时问题，保持已注册的不动即可；而
// builtinProbeBelowMinimum 是已确认的"太旧"判定，调用方可据此降级。详见
// builtinProbeVerdict。
//
// 第二个返回值在判定非 OK 时给出简短的人类可读原因。它会以 skipped_agents 的
// 形式呈现在 /health 上，让用户能区分"CLI 未安装"与"CLI 已安装但注册时被剔除"，
// 后者过去只在 daemon 日志里可见（MUL-5439）。
func (d *Daemon) probeBuiltinRuntime(ctx context.Context, name string, entry AgentEntry) (string, string, builtinProbeVerdict) {
	var (
		lastErr  error
		attempts int
		// transientReason lets a provider whose probe is not a version probe
		// name its own failure. The generic tail would otherwise report "version
		// detection failed" for a dsh that answered --version perfectly well,
		// which sends a reader looking for the wrong problem.
		transientReason string
	)
probeLoop:
	for attempts < runtimeVersionProbeAttempts {
		if attempts > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(runtimeVersionProbeRetryDelay):
			}
			// 一轮被取消说明正在关停或已过截止；让 lastErr 始终指向真正的探测
			// 失败，而不是那场"阻止我们重试"的取消。
			if ctx.Err() != nil {
				break
			}
		}
		attempts++
		// Cleared per attempt: this names the DSH profile probe's own failure,
		// and only for a round that ENDED there. An attempt that gets past the
		// probe and then fails version detection has a more specific reason,
		// and a stale value from an earlier attempt would overwrite it at the
		// tail — reporting "the probe returned no usable answer" for a probe
		// that answered fine.
		transientReason = ""
		// The attempt is timed from here, not from the detect call below:
		// resolveAgentEntry runs a version probe of its own on the re-resolved
		// candidate, and that probe can burn the whole timeout by itself. Timing
		// only the outer call would read "slow self-heal, then an instant
		// failure on the stale path" as a fast failure and retry it, paying the
		// slow half twice.
		startedAt := time.Now()
		// 自愈"被就地升级删除的钉住可执行路径"（MUL-4486），让版本探测——从而
		// 保持已注册/在线——无需重启 daemon 就能恢复。resolveAgentEntry 已经给
		// 自愈二进制做过版本门槛；下面的 detect + 最低版本检查仍会跑，目的是产出
		// 本次注册要上报的版本字符串。每次尝试都重跑它，因为重试要修复的正是
		// 自愈本身：删掉旧路径的那次升级，在第一次尝试时可能还没发布新路径。
		resolved, _, heal := d.resolveAgentEntryWithHeal(ctx, name, entry)
		// 钉住路径已消失，且它命令现在解析到的二进制太旧。与下面的直接情形
		// 不同，这条裁决是关于"此刻 PATH 解析到什么"的——而钉住路径消失正是
		// 那个"每次尝试都自愈"要覆盖的升级中途窗口，陈旧的兄弟安装可能盖住
		// 尚未发布的新二进制。在返回可降级裁决前，给它和其他结果一样的有限
		// 快速失败重试；一次找到升级后二进制的重试会改而采纳它。这仍是这种
		// 形态到达调用方的唯一途径：探测消失的路径只会产出 "version detection
		// failed"，而它按设计让 runtime 保持在线、去为一个拉不起来的 CLI 认领
		// 任务。
		if heal.rejected != nil {
			if attempts < runtimeVersionProbeAttempts && time.Since(startedAt) < runtimeVersionProbeRetryWindow {
				d.logger.Debug("re-resolved agent version too old; retrying probe",
					"name", name, "attempt", attempts, "version", heal.rejected.Detected)
				continue
			}
			d.logger.Warn("skip registering runtime: re-resolved version too old",
				"name", name, "version", heal.rejected.Detected, "error", heal.rejected.Error())
			return heal.rejected.Detected, heal.rejected.Error(), builtinProbeBelowMinimum
		}
		version, err := detectAgentVersion(ctx, agent.Command{Path: resolved.Path})
		if err != nil {
			lastErr = err
			if time.Since(startedAt) >= runtimeVersionProbeRetryWindow {
				break
			}
			if attempts < runtimeVersionProbeAttempts {
				d.logger.Debug("agent version probe failed; retrying", "name", name, "attempt", attempts, "error", err)
			}
			continue
		}
		if err := checkAgentMinVersion(name, version); err != nil {
			var tooOld *agent.BelowMinimumError
			if errors.As(err, &tooOld) {
				// 裁决只是"某个解析成功版本"的纯函数，重试也会得到同一结论——
				// 现在就把 provider 剔掉。
				d.logger.Warn("skip registering runtime: version too old", "name", name, "version", version, "error", err)
				// 版本照常返回，即使 provider 被剔：调用方要拿它上报自己降级了谁。
				return version, err.Error(), builtinProbeBelowMinimum
			}
			// CLI 跑起来了，但打印的东西（或什么都没打印）让门槛解析不了。那是
			// "我们没学到版本"，不是"我们确认它太旧"——与 exec 失败同一条瞬态
			// 规则，因为 below-minimum 裁决会拆掉 runtime，绝不能建立在这么薄的
			// 证据上。
			lastErr = err
			if time.Since(startedAt) >= runtimeVersionProbeRetryWindow {
				break
			}
			if attempts < runtimeVersionProbeAttempts {
				d.logger.Debug("agent version unparseable; retrying", "name", name, "attempt", attempts, "error", err)
			}
			continue
		}
		// DSH is the one built-in whose binary being present, and runnable, and
		// new enough still does not make it usable: the Multica runtime profile
		// supplies the --stdio protocol the backend drives, so a `dsh` without
		// it resolves, answers `--version`, clears the minimum, and then cannot
		// run a single task. Checked here rather than in probeAgentCLIs so the
		// drop produces a verdict: without it the provider disappeared from the
		// availability set silently, which is indistinguishable to a user from
		// "Multica cannot see my dsh at all".
		//
		// LAST, after version detection has succeeded, and that order is the
		// point. probeDshMulticaProfile cannot tell "the profile refused" from
		// "the thing I ran is not a working CLI", and exec.LookPath is far too
		// weak a proxy for the second: on Windows the CLI is a .cmd shim that
		// LookPath happily resolves and that exits 9009 — cmd.exe's "command
		// not found" — when what it forwards to is missing. Probing the profile
		// first reported that machine as "the Multica runtime profile is not
		// installed", which is a repair for a problem it did not have, and with
		// a bundle configured would have started installing into a DSH that
		// cannot execute. A CLI that cannot answer `--version` is not one this
		// daemon has any business holding an opinion about the profile of, so
		// its failure is reported as what it is by the version-probe path
		// above.
		if name == "dsh" {
			d.prepareManagedDsh(resolved.Path, version)
			// What the probe said decides what may happen next. Only a
			// confirmed-absent profile may install a bundle or condemn the
			// runtime. An incompatible one is reported and neither installed
			// over nor demoted — the profile is deliberately there, and this
			// daemon may be the stale side of the skew. A probe that merely
			// failed is transient, on the same rule every other provider's
			// version probe gets.
			switch probeDshMulticaProfile(ctx, resolved.Path) {
			case dshProbeOK:
				version = managedDshVersion(version)
			case dshProbeMissingProfile:
				d.logger.Warn("skip registering runtime: DSH Multica runtime profile is not installed",
					"name", name, "path", resolved.Path)
				reason := dshMissingProfileReason
				if d.startDshProfileProvision(resolved.Path) {
					reason = dshProfileInstallStartedReason
				}
				return "", reason, builtinProbeMissingProfile
			case dshProbeIncompatible:
				d.logger.Warn("skip registering runtime: DSH Multica runtime profile speaks another protocol",
					"name", name, "path", resolved.Path)
				return "", dshIncompatibleProfileReason, builtinProbeIncompatibleProfile
			default:
				// Transient. Retry inside this round's budget, then leave the
				// loop for the tail, which reports it as the transient drop it
				// is. The labelled break matters: a bare one would leave only
				// the switch and fall through to registering a runtime whose
				// profile answered nothing.
				lastErr = errors.New(dshProbeFailedReason)
				transientReason = dshProbeFailedReason
				if time.Since(startedAt) >= runtimeVersionProbeRetryWindow {
					break probeLoop
				}
				if attempts < runtimeVersionProbeAttempts {
					d.logger.Debug("dsh profile probe failed; retrying", "name", name, "attempt", attempts)
				}
				continue
			}
		}
		d.setAgentVersion(name, version)
		d.refreshHealedVersion(name, resolved.Path, version)
		if version == "" {
			// 没有最低版本下限的 provider，其 `--version` 退出 0 但什么也没打印时
			// 会带着空串走到这里。setAgentVersion 刚拒绝让它覆盖缓存；载荷需要
			// 同样的保护，因为一次由"邻居"升级触发的整集重注册，若把空串发出去
			// 会抹掉服务器已知的版本。复用最近已知的那个；从没探测到过版本的
			// provider 保持空串，和以前一样。
			version = d.agentVersion(name)
		}
		d.logger.Debug("agent version detected", "name", name, "version", version, "path", resolved.Path)
		return version, "", builtinProbeOK
	}
	// OS 拒绝执行该文件不是一次失败的探测，而是一项"发现"：CLI 已安装、可解析、
	// 但跑不起来。把它作为自己的裁决上报，调用方才能把 runtime 拉下线，而不是让
	// 一个起不动的二进制保持在线（MUL-6164）。pkg/agent 里附带的那种诊断会作为
	// reason 一起搭车，所以 /health 带的是修复命令，而不只是 errno。
	if agent.IsExecFormatError(lastErr) {
		d.logger.Warn("skip registering runtime: agent CLI is not executable on this machine",
			"name", name, "attempts", attempts, "error", lastErr)
		return "", fmt.Sprintf("agent CLI is not executable: %v", lastErr), builtinProbeNotExecutable
	}

	d.logger.Warn("skip registering runtime", "name", name, "attempts", attempts, "error", lastErr)
	reason := "version detection failed"
	if lastErr != nil {
		reason = fmt.Sprintf("version detection failed: %v", lastErr)
	}
	if transientReason != "" {
		reason = transientReason
	}
	return "", reason, builtinProbeUnavailable
}

// detectBuiltinRuntimes 对每个已配置的内置 agent CLI 做版本探测，为每个能解析
// 且通过最低版本门槛的 provider 返回一条注册条目。探测并发执行，受
// runtimeVersionProbeConcurrency 限制。
//
// 旧实现串行探测，总延迟是所有 CLI `--version` 调用之和。入网主机装有几个编码
// 工具时，会在 daemon 注册前堆出数秒的死时间——长到桌面端 runtime 步骤在探测
// 仍在进行时就超时进入空的 "no runtime found" 状态（MUL-5119）。把探测扇出后，
// 总延迟跟随最慢的单次探测而非它们的总和，新创建的工作区能在 UI 扫描窗口内
// 点亮其 runtimes。
//
// 每次探测仍会自愈消失的钉住路径（MUL-4486）并重新探测实时版本——Daemon 上
// 什么都不缓存，因此原地升级的 CLI 仍会以当前版本上报。在 probeBuiltinRuntime
// 有界次尝试后版本仍探测不到的 provider，或低于最低支持版本的 provider，会被
// 记录并跳过，和串行循环完全一致。
//
// 结果描述的是机器而非工作区，因此一次注册整批工作区的调用方只调用本函数
// ONCE，再把载荷逐工作区传给 registerRuntimesForWorkspaceBatch（MUL-5225）。
//
// 第二个返回值是本次轮次的已确认判定，provider -> 对其不利的证据。它作为返回值
// 而不是从 skippedAgents 读回，因为后者是"最近发布的那轮"的诊断快照：有四个
// 不同 goroutine 调用本函数（发现循环、workspace 同步、runtime_gone 重注册、
// profile 漂移刷新），基于共享副本行动的调用方可能基于别人的探测行动——而降级
// runtime 不是该拿别的轮次证据做的决定。
//
// 第三个返回值是本次轮次"不可用"的 provider（版本读不到），provider -> 原因。
// 把注册响应当作某工作区整个 runtime 集合的 AUTHORITATIVE 状态处理的调用方
// （runtime_gone 恢复与 profile 漂移刷新，经 applyRegisterResponseInPlace）必须
// 保留这些 provider 既有的 runtime：它们缺席载荷是因为探测失败——那是瞬时的——
// 为此拆掉一个正常工作的 runtime，正是 unavailable/below-minimum 判定拆分要
// 阻止的事。只有 below-minimum 判定才可降级。
func (d *Daemon) detectBuiltinRuntimes(ctx context.Context) ([]map[string]string, map[string]runtimeVerdict, map[string]string) {
	type detected struct {
		name    string
		version string
	}
	// 在任何探测开跑前先快照：下面采样到的所有东西，至少和"记录到此为止的每条
	// 裁决"一样新——这正是 clearProviderDemotions 需要的声明，而光靠时间它做不
	// 出这个判断。
	sampledAfter := d.demotionSeqSnapshot()
	var (
		mu          sync.Mutex
		results     []detected
		skipped     = map[string]string{}
		demotable   = map[string]runtimeVerdict{}
		unavailable = map[string]string{}
		g           errgroup.Group
	)
	g.SetLimit(runtimeVersionProbeConcurrency)
	for name, entry := range d.agents() {
		name, entry := name, entry
		g.Go(func() error {
			version, reason, verdict := d.probeBuiltinRuntime(ctx, name, entry)
			if verdict != builtinProbeOK {
				demote := demotableBuiltinProbeVerdict(verdict) &&
					(!builtinProbeNeedsConfirmation(verdict) ||
						d.confirmCondemned(verdict, name, time.Now()))
				mu.Lock()
				skipped[name] = reason
				if demote {
					demotable[name] = newRuntimeVerdict(verdict, reason, entry.Path, d.dshInstallInFlight.Load())
				} else {
					unavailable[name] = reason
				}
				mu.Unlock()
				return nil
			}
			d.clearCondemned(name)
			mu.Lock()
			results = append(results, detected{name: name, version: version})
			mu.Unlock()
			return nil
		})
	}
	// 没有任何探测返回非 nil 错误——失败已在上面记日志并跳过——所以 Wait 只是
	// 等仍在途的探测跑完。
	_ = g.Wait()

	// 把本轮要剔除的发布到 /health。整体替换（而非合并）才能让诊断保持诚实：
	// 本轮注册成功的 provider 不能继续挂在 skipped 名单上。
	d.setSkippedAgents(skipped)

	// map 的遍历顺序和并行的完成顺序都是不确定的；按 provider 排序，让注册载荷
	// 跨运行保持稳定、依赖顺序的测试保持确定性。
	sort.Slice(results, func(i, j int) bool { return results[i].name < results[j].name })

	// 探测正常的 provider 已不再低于最低版本，放掉针对它的降级 hold——否则
	// converge 正要做的注册会被"保护降级本身"的护栏拒绝。只放行"本轮开始采样
	// 前就已存在"的 hold；"最后返回"不等于"最后采样"，原因见 clearProviderDemotions。
	recovered := make([]string, 0, len(results))
	for _, r := range results {
		recovered = append(recovered, r.name)
	}
	d.clearProviderDemotions(recovered, sampledAfter)

	runtimes := make([]map[string]string, 0, len(results))
	for _, r := range results {
		displayName := providerDisplayName(r.name)
		if d.cfg.DeviceName != "" {
			displayName = fmt.Sprintf("%s (%s)", displayName, d.cfg.DeviceName)
		}
		runtimes = append(runtimes, map[string]string{
			"name":    displayName,
			"type":    r.name,
			"version": r.version,
			"status":  "online",
		})
	}
	return runtimes, demotable, unavailable
}

// cloneRuntimeEntries 深拷贝一份注册 runtime 载荷。收到共享内置载荷的调用方
// （见 registerRuntimesForWorkspaceBatch）先拷贝再追加自己工作区的自定义 profile，
// 这样任何工作区的 profile 都不会泄漏进另一个工作区的注册里。
func cloneRuntimeEntries(in []map[string]string) []map[string]string {
	out := make([]map[string]string, 0, len(in))
	for _, entry := range in {
		cp := make(map[string]string, len(entry))
		for k, v := range entry {
			cp[k] = v
		}
		out = append(out, cp)
	}
	return out
}

// registerRuntimesForWorkspace 为单个工作区注册本机 runtimes，并自行探测内置
// agent CLI 的版本。它是每个独立注册的入口——runtime_gone 重注册、profile 漂移
// 刷新、恢复重试——因此每种情况都会重新探测版本，从而接住原地升级的 CLI。
//
// 一次注册整批工作区（daemon 启动）则走 registerRuntimesForWorkspaceBatch，
// 让整批共享一轮探测。
//
// 第三个返回值是本轮响应不得被当作 authoritative 对待的 provider 集合，
// provider -> 原因——见 preserveProvidersFromProbe。把响应当作 AUTHORITATIVE
// 应用（applyRegisterResponseInPlace）的调用方必须把它传过去。
//
// 调用方必须为本次调用、apply 以及随后的清理持有该工作区的注册锁
// （withWorkspaceRegisterLock）。
func (d *Daemon) registerRuntimesForWorkspaceLocked(ctx context.Context, workspaceID string) (*RegisterResponse, string, map[string]string, error) {
	builtins, belowMinimum, unavailable := d.detectBuiltinRuntimes(ctx)
	resp, profileSig, err := d.registerRuntimesForWorkspaceBatchLocked(ctx, workspaceID, builtins)
	return resp, profileSig, preserveProvidersFromProbe(unavailable, belowMinimum), err
}

// preserveProvidersFromProbe 返回那些"缺席于注册载荷"绝不能被解读为"该工作区
// 应停止托管它们"的 provider，按 provider 为键。
//
// unavailable 是显然的一半：版本读不到，这是瞬时的，删行会在一次失败的探测上
// 拆掉一个正常工作的 runtime。
//
// demotable 是容易搞错的一半，因为该判定确实是 CONFIRMED——读到了版本并拒绝，
// 或操作系统拒绝执行该文件。这些路径上缺的不是证据，而是据此行动的授权。把
// runtime 下线需要两样东西，runtime_gone 恢复与 profile 漂移刷新都不具备：claim
// 屏障（保证行绝不在仍执行的任务底下被抽走），以及带 seq 戳的 hold（保证判定
// 之前发出的注册落地时无法复活该 provider）。demoteUnusableRuntimes 两样都有，
// 是降级的唯一所有者。没有它们就删行的路径得到同样判定却不留任何记录，于是
// 下一份在途响应会悄悄撤销它。
//
// 在此保留至多让一个不可用的 CLI 在线一个刷新 tick——这正是降级前的现状，而非
// 新的暴露面。
func preserveProvidersFromProbe(unavailable map[string]string, demotable map[string]runtimeVerdict) map[string]string {
	if len(demotable) == 0 {
		return unavailable
	}
	preserve := make(map[string]string, len(unavailable)+len(demotable))
	for provider, reason := range unavailable {
		preserve[provider] = reason
	}
	for provider, verdict := range demotable {
		preserve[provider] = verdict.reason
	}
	return preserve
}

// registerRuntimesForWorkspaceBatch 用一份已经探测好的内置 runtime 载荷为单个
// 工作区注册。
//
// 内置 CLI 是机器级事实，而非工作级事实，但注册是按工作区的——因此若每次注册
// 都做内部探测，一个服务 N 个工作区的 daemon 会在启动时派生出 N×M 个
// `<cli> --version` 进程（24 个工作区 × 5 个 agent = 120 次而非 5 次，MUL-5225 /
// #5837）。除了浪费启动时间，某些 CLI 包装器在被执行时有可见副作用，单个慢探测
// 还会被工作区数量放大。
//
// 把载荷作为参数，可以让一轮探测服务整个注册批次，同时保持刷新语义：Daemon
// 上什么都不缓存，因此下一次独立注册会重新探测。
//
// builtins 按只读对待，追加本工作区自定义 profile 前会先拷贝。
//
// 调用方必须为本次调用、apply 以及随后的清理持有该工作区的注册锁
// （withWorkspaceRegisterLock）。
func (d *Daemon) registerRuntimesForWorkspaceBatchLocked(ctx context.Context, workspaceID string, builtins []map[string]string) (*RegisterResponse, string, error) {
	d.logger.Debug("registering runtimes for workspace", "workspace_id", workspaceID, "agent_count", len(d.agents()))
	runtimes := cloneRuntimeEntries(builtins)
	var failedProfiles []map[string]string

	// 追加任何"命令在这台主机能解析"的工作区自定义 runtime profile（MUL-3284）。
	// 这是尽力而为：一次拉取错误（如旧服务器返回 404）绝不能拖垮注册——daemon
	// 只是用已收集的内置 runtimes 继续。command_name 既不在 PATH 上、又没为同协议
	// 族发现过的 profile 会被跳过（这台主机没有它）。
	//
	// profileSig 是这里捕获的工作区 profile 列表的内容哈希，按需的服务器通知可以在
	// "生效 profile 集合已经是最新"时跳过重注册（MUL-3332）。空串表示拉取失败，
	// 调用方必须保留 workspaceState 上之前缓存的任何签名。
	profileSig := d.appendProfileRuntimes(ctx, workspaceID, &runtimes, &failedProfiles)

	if len(runtimes) == 0 && len(failedProfiles) == 0 {
		// 即使什么都没解析出来，profileSig 仍然有意义：刷新路径靠它记住"我们
		// 已经收敛到全部禁用状态"，让重复的变更通知变成 no-op 而不是陷入
		// "反复空注册"循环。不关心签名的初次注册调用方用 _ 丢弃它。
		return nil, profileSig, ErrNoRuntimesToRegister
	}

	req := map[string]any{
		"workspace_id":      workspaceID,
		"daemon_id":         d.cfg.DaemonID,
		"legacy_daemon_ids": d.cfg.LegacyDaemonIDs,
		"device_name":       d.cfg.DeviceName,
		"cli_version":       d.cfg.CLIVersion,
		"launched_by":       d.cfg.LaunchedBy,
		"runtimes":          runtimes,
		"failed_profiles":   failedProfiles,
	}

	resp, err := d.client.Register(ctx, req)
	if err != nil {
		return nil, "", fmt.Errorf("register runtimes: %w", err)
	}
	if len(resp.Runtimes) == 0 && len(failedProfiles) == 0 {
		return nil, "", fmt.Errorf("register runtimes: empty response")
	}
	d.logger.Debug("register response", "workspace_id", workspaceID, "runtimes", len(resp.Runtimes), "repos", len(resp.Repos), "repos_version", resp.ReposVersion)
	d.recordBuiltinVersionsSent(workspaceID, runtimes)
	return resp, profileSig, nil
}

// registerBuiltinRuntimesForWorkspace 只为工作区注册 BUILT-IN runtimes：不拉取、
// 不发送任何自定义 runtime profile，也不产出 profile 签名。
//
// 它是为 CLI 发现路径（MUL-5439）而存在的，该路径绝不能参与自定义 profile 的
// 收敛。走"会追加 profile 的注册"会让发现作为副作用观察到 profile 集合，而缓存
// 该观察会告诉漂移路径"已经收敛"——于是在发现新 CLI 的同一时刻被禁用的 profile
// 会让它的 runtime 永远活着（refreshWorkspaceRuntimeProfiles 在签名一致时短路）。
// 自定义 profile 的增/改/禁仍专属于既有漂移路径。
//
// builtins 按只读对待。
//
// 调用方必须为本次调用、merge 以及随后的清理持有该工作区的注册锁
// （withWorkspaceRegisterLock）。
func (d *Daemon) registerBuiltinRuntimesForWorkspaceLocked(ctx context.Context, workspaceID string, builtins []map[string]string) (*RegisterResponse, error) {
	runtimes := cloneRuntimeEntries(builtins)
	if len(runtimes) == 0 {
		return nil, ErrNoRuntimesToRegister
	}
	req := map[string]any{
		"workspace_id":      workspaceID,
		"daemon_id":         d.cfg.DaemonID,
		"legacy_daemon_ids": d.cfg.LegacyDaemonIDs,
		"device_name":       d.cfg.DeviceName,
		"cli_version":       d.cfg.CLIVersion,
		"launched_by":       d.cfg.LaunchedBy,
		"runtimes":          runtimes,
		// 刻意留空：这次调用不携带任何 profile，自然也不能上报 profile 失败。
		"failed_profiles": []map[string]string{},
	}
	resp, err := d.client.Register(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("register builtin runtimes: %w", err)
	}
	if len(resp.Runtimes) == 0 {
		return nil, fmt.Errorf("register builtin runtimes: empty response")
	}
	d.logger.Debug("builtin register response", "workspace_id", workspaceID, "runtimes", len(resp.Runtimes))
	d.recordBuiltinVersionsSent(workspaceID, runtimes)
	return resp, nil
}

// appendProfileRuntimes 拉取工作区启用的自定义运行时 profile（MUL-3284），为每个
// command_name 在本机可解析的 profile 追加一条注册条目。对每个解析成功的 profile
// 都会以 profile_id 为键记录其命令绝对路径与固定参数（经 recordProfileLaunch），
// 供 runTask 之后为认领任务启动该自定义可执行文件。
//
// 契约上是 best-effort：任何拉取 profile 的错误（旧服务器、网络抖动）都会被记录
// 并吞掉——注册继续用已收集的内置 runtimes 进行。命令解析不了的 profile 会带
// Info 日志跳过（这台主机就是没有该命令）。
//
// 注册条目与内置形状一致：name = display_name（与内置路径一样带上设备名后缀）、
// type = protocol_family（路由 provider）、version = best-effort 探测版本、
// status = "online"，外加服务器校验的 profile_id。
//
// 返回拉取到的 profile 列表的内容签名（MUL-3332）。该签名供按需 profile 刷新在
// 忽略重复变更通知的同时，无需重启 daemon 即可触发重注册。拉取失败时返回空串——
// 调用方必须把它当作"未知，不要覆盖已存签名"（否则一次瞬时 5xx 会让 daemon
// 误以为工作区有零个 profile）。
func (d *Daemon) appendProfileRuntimes(ctx context.Context, workspaceID string, runtimes *[]map[string]string, failedProfiles *[]map[string]string) string {
	resp, err := d.client.GetRuntimeProfiles(ctx, workspaceID)
	if err != nil {
		// 尽力而为：绝不能因为 profile 拉不下来就让注册失败。没有 profiles 路由的
		// 旧服务器会返回 404。
		d.logger.Info("skip custom runtime profiles: fetch failed (continuing with built-in runtimes)",
			"workspace_id", workspaceID, "error", err)
		return ""
	}
	if resp == nil {
		// 空载荷——与"服务器有零个 profile"同形。返回空列表的摘要，sync 循环才
		// 仍能察觉之后的转变（零 → 加了第一个 profile）。
		return profileSetSignature(nil)
	}
	for _, profile := range resp.RuntimeProfiles {
		if profile.CommandName == "" || profile.ProtocolFamily == "" {
			d.logger.Warn("skip custom runtime profile: missing command_name or protocol_family",
				"workspace_id", workspaceID, "profile_id", profile.ID, "display_name", profile.DisplayName)
			continue
		}
		if !agent.IsSupportedType(profile.ProtocolFamily) {
			reason := "unsupported protocol_family: " + profile.ProtocolFamily
			d.logger.Warn("skip custom runtime profile: unsupported protocol_family",
				"workspace_id", workspaceID, "profile_id", profile.ID,
				"display_name", profile.DisplayName, "protocol_family", profile.ProtocolFamily)
			*failedProfiles = append(*failedProfiles, map[string]string{
				"profile_id":   profile.ID,
				"command_name": profile.CommandName,
				"reason":       reason,
			})
			continue
		}
		// 解析该 profile 要启动的可执行文件。按机器配置的路径覆盖（MUL-3284，
		// `multica runtime profile set-path`）在"被设置 AND 指向真实可执行文件"时
		// 优先于 PATH 查找——运维就这样把一个 profile 钉到一个不在 daemon PATH
		// 上的二进制，或在同主机多个安装之间做选择。配了但用不了的覆盖
		// （已删/已挪/不可执行）会记日志并退回 PATH，再退回守护进程启动时已为
		// 同协议族发现的那个 provider 命令。后者对 GUI 启动的 daemon 很要紧：
		// 它的环境直接解析不到某个 CLI，而内置发现在登录 shell 或 provider 安装
		// 路径里找到了它。以上来源都解析不出来时，该 profile 被跳过。
		var resolved string
		var failureReason string
		if override := strings.TrimSpace(d.cfg.ProfileCommandOverrides[profile.ID]); override != "" {
			if profilePathExecutable(override) {
				resolved = override
				d.logger.Info("custom runtime profile: using per-machine command path override",
					"workspace_id", workspaceID, "profile_id", profile.ID, "command_path", resolved)
			} else {
				failureReason = "Configured path override is not executable: " + override
				d.logger.Warn("custom runtime profile: command path override not executable; falling back to PATH",
					"workspace_id", workspaceID, "profile_id", profile.ID,
					"override_path", override, "command_name", profile.CommandName)
			}
		}
		if resolved == "" {
			r, err := lookPath(profile.CommandName)
			if err != nil {
				if discovered, ok := d.agents()[profile.ProtocolFamily]; ok && discovered.Command == profile.CommandName && discovered.Path != "" {
					resolved = discovered.Path
					d.logger.Info("custom runtime profile: using discovered provider command path",
						"workspace_id", workspaceID, "profile_id", profile.ID,
						"protocol_family", profile.ProtocolFamily, "command_path", resolved)
				} else {
					// 这台主机上没有这个命令——对没为这个 profile 装机的主机是
					// 预期情形。跳过而不失败。
					d.logger.Info("skip custom runtime profile: command not found on PATH or provider discovery",
						"workspace_id", workspaceID, "profile_id", profile.ID,
						"command_name", profile.CommandName, "error", err)
					if failureReason != "" {
						failureReason += "; "
					}
					failureReason += "command not found on PATH or provider discovery: " + profile.CommandName
					*failedProfiles = append(*failedProfiles, map[string]string{
						"profile_id":   profile.ID,
						"command_name": profile.CommandName,
						"reason":       failureReason,
					})
					continue
				}
			} else {
				resolved = r
			}
		}
		// 尽力而为的版本探测；空版本可接受。探测带上 profile 的 fixed_args，这样
		// 包装器上报的是它 exec 的那个 CLI 的版本，而不是它自己的：`ccms start
		// q36 --version` 是 Claude Code 的版本，`ccms --version` 是包装器的版本，
		// 只有前者对最低版本门槛有意义（GH #7046）。
		version, verErr := detectAgentVersion(ctx, agent.NewCommand(resolved,
			agent.FilterLaunchPrefix(profile.ProtocolFamily, profile.FixedArgs, d.logger)))
		if verErr != nil {
			d.logger.Debug("custom runtime profile: version probe failed (registering with empty version)",
				"workspace_id", workspaceID, "profile_id", profile.ID, "path", resolved, "error", verErr)
			version = ""
		}
		displayName := profile.DisplayName
		if d.cfg.DeviceName != "" {
			displayName = fmt.Sprintf("%s (%s)", displayName, d.cfg.DeviceName)
		}
		d.recordProfileLaunch(profile.ID, resolved, version, profile.FixedArgs)
		d.logger.Info("registering custom runtime profile",
			"workspace_id", workspaceID, "profile_id", profile.ID,
			"protocol_family", profile.ProtocolFamily, "command_path", resolved)
		*runtimes = append(*runtimes, map[string]string{
			"name":       displayName,
			"type":       profile.ProtocolFamily,
			"version":    version,
			"status":     "online",
			"profile_id": profile.ID,
		})
	}
	return profileSetSignature(resp.RuntimeProfiles)
}

// profileSetSignature 是工作区自定义运行时 profile 列表（MUL-3332）的稳定内容
// 哈希。按需刷新会在服务器报告 create/edit/disable/delete 后把它与缓存值做差异
// 比较；不一致就触发 daemon 重注册，使新的 runtime 实例无需重启即可出现。
//
// 被哈希的投影恰好覆盖影响 daemon Register 载荷的字段：ID、Enabled、ProtocolFamily、
// CommandName、FixedArgs（该 runtime 上每个 agent 继承的启动参数）与 Visibility
// （让假想的未来按创建者过滤也能触发漂移）。先按 ID 排序，使摘要与顺序无关
// （服务器允许按任意顺序返回它们）。
func profileSetSignature(profiles []RuntimeProfile) string {
	if len(profiles) == 0 {
		return "0"
	}
	sorted := append([]RuntimeProfile(nil), profiles...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	h := fnv.New64a()
	// 分隔符选一个绝不会出现在 UUID、slug 或参数里的字符。
	const sep = "\x1f"
	for _, p := range sorted {
		fmt.Fprintf(h, "%s%s%t%s%s%s%s%s%s%s",
			p.ID, sep,
			p.Enabled, sep,
			p.ProtocolFamily, sep,
			p.CommandName, sep,
			p.Visibility, sep,
		)
		for _, a := range p.FixedArgs {
			fmt.Fprintf(h, "%s%s", a, sep)
		}
		// 记下列表结尾，让 [a,b] 与 [ab] 哈希得不一样。
		h.Write([]byte("\x1e"))
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

func newWorkspaceState(workspaceID string, runtimeIDs []string, reposVersion string, repos []RepoData, settings json.RawMessage) *workspaceState {
	// 构造一个新跟踪的工作区状态，允许仓库列表与设置同步到达。
	return &workspaceState{
		workspaceID:     workspaceID,
		runtimeIDs:      runtimeIDs,
		reposVersion:    reposVersion,
		allowedRepoURLs: repoAllowlist(repos),
		settings:        settings,
	}
}

func repoAllowlist(repos []RepoData) map[string]struct{} {
	// 把服务器返回的仓库 URL 集合转成 set，供白名单判断使用。
	allowed := make(map[string]struct{}, len(repos))
	for _, repo := range repos {
		if repo.URL == "" {
			continue
		}
		allowed[repo.URL] = struct{}{}
	}
	return allowed
}

// setWorkspaceRepoSyncError 记录工作区最近一次仓库缓存同步的错误信息（清空传空串）。
func (d *Daemon) setWorkspaceRepoSyncError(workspaceID, syncErr string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ws, ok := d.workspaces[workspaceID]; ok {
		ws.lastRepoSyncErr = syncErr
	}
}

// workspaceRepoAllowed 判断某仓库 URL 是否被工作区允许：既看工作区级绑定
// （allowedRepoURLs），也看任务级（taskRepoURLs）。
func (d *Daemon) workspaceRepoAllowed(workspaceID, repoURL string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		return false
	}
	if _, allowed := ws.allowedRepoURLs[repoURL]; allowed {
		return true
	}
	if _, allowed := ws.taskRepoURLs[repoURL]; allowed {
		return true
	}
	return false
}

// repoBarePathIsLive 报告某个被跟踪的工作区是否仍认领 barePath 处缓存的仓库，
// 这样 GC 可以拒绝驱逐它。
//
// 按路径回答而非物化整个集合，让 GC 能在删除前再次立即询问。每轮只做一次快照，
// 在调用方逐个仓库执行 git 与文件系统操作期间就会过期——而那个窗口正是工作区
// 可能重新挂上某个仓库的窗口。
//
// 它与 workspaceRepoAllowed 一致地对两个来源取并集：allowedRepoURLs（工作区级
// 绑定）与 taskRepoURLs（服务器通过任务认领浮现、从不出现在 GetWorkspaceRepos
// 的 project 仓库）。漏掉第二个集合会让 GC 驱逐任务正在 checkout 的仓库。
//
// 有意从内存状态读取。另一种做法——GC 期间为每个工作区向服务器询问仓库列表——
// 会把瞬时 API 失败伪装成"什么都没有挂上"，而正是这个集合在保护缓存不被删除。
func (d *Daemon) repoBarePathIsLive(barePath string) bool {
	if d.repoCache == nil || barePath == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for workspaceID, ws := range d.workspaces {
		for url := range ws.allowedRepoURLs {
			if d.repoCache.BarePath(workspaceID, url) == barePath {
				return true
			}
		}
		for url := range ws.taskRepoURLs {
			if d.repoCache.BarePath(workspaceID, url) == barePath {
				return true
			}
		}
	}
	return false
}

// workspaceLastRepoSyncErr 返回工作区最近一次仓库同步错误（无则为空串）。
func (d *Daemon) workspaceLastRepoSyncErr(workspaceID string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		return ""
	}
	return ws.lastRepoSyncErr
}

// workspaceCoAuthoredByEnabled 返回该工作区是否应安装 Co-authored-by hook。
// 两个设置任一缺失（新工作区、不发送设置的旧服务器）时默认 true。
//
// hook 由 BOTH GitHub 总开关（github_enabled）与专属 co-author 开关
// （co_authored_by_enabled）门控，因此关掉工作区 GitHub 主开关也会让新 trailer
// 不再进入提交，符合 RFC MUL-2414 §4.8 记录的契约。
func (d *Daemon) workspaceCoAuthoredByEnabled(workspaceID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, ok := d.workspaces[workspaceID]
	if !ok || len(ws.settings) == 0 {
		return true // default: enabled
	}
	var s struct {
		GitHubEnabled       *bool `json:"github_enabled"`
		CoAuthoredByEnabled *bool `json:"co_authored_by_enabled"`
	}
	if err := json.Unmarshal(ws.settings, &s); err != nil {
		return true // default: enabled when payload is malformed
	}
	if s.GitHubEnabled != nil && !*s.GitHubEnabled {
		return false
	}
	if s.CoAuthoredByEnabled == nil {
		return true // default: enabled
	}
	return *s.CoAuthoredByEnabled
}

// registerTaskRepos 把任务级仓库（例如认领处理器抬进 resp.Repos 的 project
// github_repo 资源）并入工作区白名单，并为任何尚未缓存的新 URL 启动一次缓存同步。
//
// 传入工作区自己的仓库也安全——重复是幂等的。runTask 在拉起 agent 前调用它，这样
// `multica repo checkout` 能接受仅 project 级的 URL，而不必多绕一趟 GetWorkspaceRepos
// （它不携带 project 资源）。
func (d *Daemon) registerTaskRepos(workspaceID, taskID string, repos []RepoData) {
	if len(repos) == 0 {
		return
	}

	type repoCandidate struct {
		url     string
		tracked bool
	}

	d.mu.Lock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		d.mu.Unlock()
		return
	}
	if ws.taskRepoURLs == nil {
		ws.taskRepoURLs = make(map[string]struct{}, len(repos))
	}
	if taskID != "" && ws.taskRepoRefs == nil {
		ws.taskRepoRefs = make(map[string]map[string]string)
	}
	candidates := make([]repoCandidate, 0, len(repos))
	for _, repo := range repos {
		url := strings.TrimSpace(repo.URL)
		if url == "" {
			continue
		}
		// 若 URL 已被跟踪（工作区级或任务级）且缓存里已经有了，就别再同步一次。
		_, inWorkspace := ws.allowedRepoURLs[url]
		_, inTask := ws.taskRepoURLs[url]
		ws.taskRepoURLs[url] = struct{}{}
		if taskID != "" {
			if ws.taskRepoRefs[taskID] == nil {
				ws.taskRepoRefs[taskID] = make(map[string]string, len(repos))
			}
			if _, exists := ws.taskRepoRefs[taskID][url]; !exists {
				ws.taskRepoRefs[taskID][url] = strings.TrimSpace(repo.Ref)
			}
		}
		candidates = append(candidates, repoCandidate{
			url:     url,
			tracked: inWorkspace || inTask,
		})
	}
	d.mu.Unlock()

	toSync := make([]RepoData, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.tracked && d.repoCache != nil && d.repoCache.Lookup(workspaceID, candidate.url) != "" {
			continue
		}
		toSync = append(toSync, RepoData{URL: candidate.url})
	}

	if d.repoCache != nil && len(toSync) > 0 {
		// 后台同步——与 workspace 注册时用的形态一致。缓存没就绪时 ensureRepoReady
		// 会给出有意义的错误，agent 的首次 checkout 就能把同步失败浮出来，而不是
		// 被静默当成配置问题。
		d.bgSyncs.Add(1)
		go func() {
			defer d.bgSyncs.Done()
			d.syncWorkspaceRepos(workspaceID, toSync)
		}()
	}
}

// taskRepoDefaultRef 返回某任务为某仓库 URL 登记的默认 checkout ref（没有则空串）。
func (d *Daemon) taskRepoDefaultRef(workspaceID, taskID, repoURL string) string {
	taskID = strings.TrimSpace(taskID)
	repoURL = strings.TrimSpace(repoURL)
	if taskID == "" || repoURL == "" {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, ok := d.workspaces[workspaceID]
	if !ok || ws.taskRepoRefs == nil {
		return ""
	}
	return strings.TrimSpace(ws.taskRepoRefs[taskID][repoURL])
}

// clearTaskRepoRefs 任务结束后清掉该任务登记的 ref（防止任务级状态越积越多）。
func (d *Daemon) clearTaskRepoRefs(workspaceID, taskID string) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if ws, ok := d.workspaces[workspaceID]; ok && ws.taskRepoRefs != nil {
		delete(ws.taskRepoRefs, taskID)
	}
}

// waitBackgroundSyncs 阻塞至 registerTaskRepos 启动的所有后台同步结束。用于测试
// 拆解：把 t.TempDir 仓库缓存交给 daemon 的测试，在返回前必须调用它，否则在途的
// clone/fetch 会与 TempDir 清理竞争，表现为无关的 "directory not empty" 失败。
func (d *Daemon) waitBackgroundSyncs() {
	d.bgSyncs.Wait()
}

// syncWorkspaceRepos 用后台 context 同步工作区仓库缓存（同步错误的记录与告警见
// syncWorkspaceReposContext）。
func (d *Daemon) syncWorkspaceRepos(workspaceID string, repos []RepoData) {
	d.syncWorkspaceReposContext(context.Background(), workspaceID, repos)
}

// syncWorkspaceReposContext 把仓库列表同步进本地缓存；失败时记录错误到
// workspaceState.lastRepoSyncErr 并打 Warn 日志，成功则清空错误记录。
func (d *Daemon) syncWorkspaceReposContext(ctx context.Context, workspaceID string, repos []RepoData) {
	if d.repoCache == nil {
		return
	}
	var err error
	if cache, ok := d.repoCache.(interface {
		SyncContext(context.Context, string, []repocache.RepoInfo) error
	}); ok {
		err = cache.SyncContext(ctx, workspaceID, repoDataToInfo(repos))
	} else {
		err = d.repoCache.Sync(workspaceID, repoDataToInfo(repos))
	}
	if err != nil {
		d.setWorkspaceRepoSyncError(workspaceID, err.Error())
		d.logger.Warn("repo cache sync failed", "workspace_id", workspaceID, "error", err)
		return
	}
	d.setWorkspaceRepoSyncError(workspaceID, "")
}

// refreshWorkspaceRepos 向服务器拉取工作区的最新仓库与设置（30s 超时）并写入
// 本地 workspaceState；随后把 Co-authored-by 结论发布到仓库缓存。
func (d *Daemon) refreshWorkspaceRepos(ctx context.Context, workspaceID string) (*WorkspaceReposResponse, error) {
	refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := d.client.GetWorkspaceRepos(refreshCtx, workspaceID)
	if err != nil {
		return nil, err
	}

	d.mu.Lock()
	if ws, ok := d.workspaces[workspaceID]; ok {
		ws.reposVersion = resp.ReposVersion
		ws.allowedRepoURLs = repoAllowlist(resp.Repos)
		// 让缓存的设置与服务器保持同步。daemon 的功能门控（如
		// workspaceCoAuthoredByEnabled）直接读这个字段，所以在 Web UI 切换某个
		// Setting 必须在这里生效，而不需要重启 daemon。服务器返回空载荷则清掉
		// 覆盖值、退回默认。
		ws.settings = resp.Settings
	}
	d.mu.Unlock()

	// 把刷新后的 Co-authored-by 结论发布给仓库缓存。更早 checkout 装的 hook 在
	// commit 时读那个文件，因此让"已关掉"的设置作用于既有的 checkout、而不是只
	// 作用于下一个（MUL-6921）的，正是这一步。
	d.persistCoAuthoredByState(workspaceID)

	return resp, nil
}

// coAuthoredByPublisher 是 Co-authored-by 接线中仓库缓存的半边：每个已装 hook 在
// commit 时读取的状态文件，以及位于工作区裸缓存里的 hook 本身。做成可选接口，
// 使测试 daemon 能用最小 repoCacheBackend 运行。
type coAuthoredByPublisher interface {
	WriteCoAuthoredByState(workspaceID string, enabled bool) error
	ReconcileCoAuthoredByHooks(workspaceID string, enabled bool) error
	ReconcileCoAuthoredByHookInCheckout(checkoutPath, workspaceID string, enabled bool) error
}

// persistCoAuthoredByState 把工作区当前的 Co-authored-by 结论写进已安装的
// prepare-commit-msg hook 可见的位置，并让早期 daemon 版本写下的 hook（它们完全不
// 读状态）与之对齐。每次设置刷新与每次 workspace 同步后调用；best-effort，因为
// 一次失败的写入只是把旧值留在原处。
//
// daemon 是这份状态的唯一写者，并在 per-workspace 锁下、在锁内读取结论后发布。
// 因此两个发布者竞争（比如一次设置刷新与一次同步 tick）会串行化，后写者必然
// 后读——在设置更新前开始的发布者绝不可能覆盖该更新产生的值。
func (d *Daemon) persistCoAuthoredByState(workspaceID string) {
	d.publishCoAuthoredByState(workspaceID, d.workspaceCoAuthoredByEnabled)
}

// publishCoAuthoredByState 是 persistCoAuthoredByState 注入结论读取者的版本。
// 生产总是传 workspaceCoAuthoredByEnabled；测试传一个"在取锁与读取之间停住发布者"
// 的包装——这正是该锁要保证的唯一一种顺序，其它方式观察不到。
func (d *Daemon) publishCoAuthoredByState(workspaceID string, verdict func(string) bool) {
	if d.repoCache == nil || workspaceID == "" {
		return
	}
	cache, ok := d.repoCache.(coAuthoredByPublisher)
	if !ok {
		return
	}

	d.mu.Lock()
	ws := d.workspaces[workspaceID]
	d.mu.Unlock()
	if ws == nil {
		return
	}

	ws.coAuthorPublishMu.Lock()
	defer ws.coAuthorPublishMu.Unlock()

	enabled := verdict(workspaceID)
	if err := cache.WriteCoAuthoredByState(workspaceID, enabled); err != nil {
		d.logger.Warn("record co-authored-by state failed", "workspace_id", workspaceID, "error", err)
	}
	if err := cache.ReconcileCoAuthoredByHooks(workspaceID, enabled); err != nil {
		d.logger.Warn("reconcile co-authored-by hooks failed", "workspace_id", workspaceID, "error", err)
	}
	d.reconcileIsolatedCoAuthoredByHooks(cache, workspaceID, enabled)
}

// isolatedCheckoutScanDepth 限制清扫在 env root 之下寻找 checkout 的深度。仓库落在
// <env root>/<workdir>/<repo>，因此两层正好覆盖该布局且多留一层余量，同时不会让
// 遍历走进已 checkout 的源码树。
const isolatedCheckoutScanDepth = 2

// reconcileIsolatedCoAuthoredByHooks 把工作区设置应用到位于任务 workdir 内（而
// 非共享裸缓存里）的 hook。
//
// Linux 上的 Codex 与 Windows 沙箱用隔离的 git 元数据 checkout，因此它们的 hook
// 位于 <checkout>/.git/hooks，仓库缓存看不到。放任不管的话，由早期版本准备的
// workdir 会一直保留它的无条件 hook——下次 commit 继续加 trailer——直到该仓库碰巧
// 再次被 checkout。env root 通过 owner 记录归属，因此工作区绝不会改写属于别的
// 工作区的 hook。
func (d *Daemon) reconcileIsolatedCoAuthoredByHooks(cache coAuthoredByPublisher, workspaceID string, enabled bool) {
	root := d.cfg.WorkspacesRoot
	if root == "" || workspaceID == "" {
		return
	}
	wsEntries, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			d.logger.Debug("co-authored-by sweep: read workspaces root failed", "error", err)
		}
		return
	}

	for _, wsEntry := range wsEntries {
		// 点开头的目录是 daemon 内部缓存（.repos、.skill-cache），绝不是工作区
		// 目录——runGC 也按同一规则遍历。
		if !wsEntry.IsDir() || strings.HasPrefix(wsEntry.Name(), ".") {
			continue
		}
		wsDir := filepath.Join(root, wsEntry.Name())
		envRoots, err := os.ReadDir(wsDir)
		if err != nil {
			continue
		}
		for _, envEntry := range envRoots {
			if !envEntry.IsDir() || strings.HasPrefix(envEntry.Name(), ".") {
				continue
			}
			envRoot := filepath.Join(wsDir, envEntry.Name())
			if !d.envRootBelongsToWorkspace(wsEntry.Name(), envRoot, workspaceID) {
				continue
			}
			for _, checkout := range isolatedCheckoutCandidates(envRoot, isolatedCheckoutScanDepth) {
				if err := cache.ReconcileCoAuthoredByHookInCheckout(checkout, workspaceID, enabled); err != nil {
					d.logger.Warn("reconcile co-authored-by hook in checkout failed",
						"workspace_id", workspaceID, "path", checkout, "error", err)
				}
			}
		}
	}
}

// envRootBelongsToWorkspace 依据创建它的那个版本留下的最强证据，判断某个 env
// root 是否属于该工作区。
//
// 从 v0.4.35 起 owner 记录携带工作区 ID，并要求精确匹配。之前版本准备的 env root
// 只记录了任务 ID——更早的版本则完全无标记——它们生活在该版本使用的布局下：
// <workspaces root>/<workspace ID>/<task key>。因此对不写工作区的 owner 记录，
// 目录名就是证据，且它必须等于工作区 ID。取代它的可读布局总是带后缀
// （`<slug>-<id tail>`），所以裸工作区 UUID 只可能是那些老 root，绝不会与新版
// 混淆。
//
// 读不到的标记不归属任何东西：跳过至多留下某个 workdir 里的过期 hook，而猜测
// 则可能改写别的 workspace 的 hook。
func (d *Daemon) envRootBelongsToWorkspace(wsDirName, envRoot, workspaceID string) bool {
	owner, err := execenv.ReadEnvRootOwner(envRoot)
	if err != nil || owner == nil {
		return false
	}
	if owner.WorkspaceID != "" {
		return owner.WorkspaceID == workspaceID
	}
	return wsDirName == workspaceID
}

// isolatedCheckoutCandidates 返回 dir 之下（含 dir）自带 git 元数据目录的目录。
// 链接工作树的 .git 是文件，因此只有隔离 checkout 会命中；命中即停止继续下钻——
// checkout 内部不会再嵌套一个 checkout。
func isolatedCheckoutCandidates(dir string, depth int) []string {
	if depth <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var found []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		child := filepath.Join(dir, entry.Name())
		if info, err := os.Stat(filepath.Join(child, ".git")); err == nil && info.IsDir() {
			found = append(found, child)
			continue
		}
		found = append(found, isolatedCheckoutCandidates(child, depth-1)...)
	}
	return found
}

// publishTrackedCoAuthoredByState 为每个被跟踪的工作区写出当前结论，并对其裸缓存
// 中的 hook 做对账。纯本地——不发任何请求——且两半在磁盘已一致时都跳过写入，
// 因此一台已收敛主机上的同步 tick 只需几次小读取。
func (d *Daemon) publishTrackedCoAuthoredByState() {
	d.mu.Lock()
	ids := make([]string, 0, len(d.workspaces))
	for id := range d.workspaces {
		ids = append(ids, id)
	}
	d.mu.Unlock()

	for _, id := range ids {
		d.persistCoAuthoredByState(id)
	}
}

// trackedSettingsRefreshTimeout 约束一轮 refreshTrackedWorkspaceSettings。轮次
// 没轮到的那个工作区会保留其缓存设置，并在下次仓库 checkout 时再接住变化。
const trackedSettingsRefreshTimeout = 30 * time.Second

// refreshTrackedWorkspaceSettings 重新读取本 daemon 已跟踪工作区的仓库与设置。
// 只有服务器的 workspaces-changed 提示会调用它：周期同步刻意不为已跟踪工作区发
// 仓库请求（见 syncWorkspacesFromAPI），因此没有它，一次设置编辑——GitHub 总
// 开关、Co-authored-by 开关——就会一直无人发现，直到该工作区下次仓库 checkout。
func (d *Daemon) refreshTrackedWorkspaceSettings(ctx context.Context) {
	// sync 循环是单线程的，而且还欠这条提示一次成员对账，所以给整轮设个上限，
	// 别让一个慢工作区把其它所有信号都堵在后面。
	ctx, cancel := context.WithTimeout(ctx, trackedSettingsRefreshTimeout)
	defer cancel()

	d.mu.Lock()
	tracked := make(map[string]*workspaceState, len(d.workspaces))
	for id, ws := range d.workspaces {
		tracked[id] = ws
	}
	d.mu.Unlock()

	for id, ws := range tracked {
		if ctx.Err() != nil {
			return
		}
		// 与 ensureRepoReady 共用那把锁：在途的 checkout 和这次刷新不能交错写
		// 同一个工作区。
		if err := ws.repoRefreshMu.Lock(ctx); err != nil {
			return
		}
		_, err := d.refreshWorkspaceRepos(ctx, id)
		ws.repoRefreshMu.Unlock()
		if err != nil {
			d.logger.Debug("workspace settings refresh failed", "workspace_id", id, "error", err)
		}
	}
}

// refreshWorkspaceRuntimeProfiles 拉取工作区启用的自定义运行时 profile 列表
// （MUL-3332），把它的内容签名与 workspaceState 上的缓存值对比，签名漂移时触发
// 一次重注册。这是 daemon WebSocket 变更通知使用的入口，让经由 Web UI 或 CLI
// 新增/编辑/禁用的 profile 无需重启 daemon 即可生效。
//
// Best-effort：拉取错误（旧服务器、网络抖动）会保留缓存的签名。成功拉到但签名
// 未变的可能来自重复通知，会直接短路、不做任何进一步工作。
//
// 漂移时本函数走的路与 reregisterWorkspaceAfterRuntimeGone 有两处刻意不同：
//
//  1. 它不调用 RecoverOrphans。服务器的 RecoverOrphanedTasksForRuntime 会把某
//     runtime 上所有 dispatched/running/waiting_local_directory 任务硬性判失败，
//     这在 runtime 行确实被服务器删除时是正确的响应，但对 profile 漂移是灾难性的
//     误报：用户只是加了个兄弟自定义 profile，一个仍在积极执行任务的内置 runtime
//     的工作就会被杀掉。
//
//  2. 它容忍 ErrNoRuntimesToRegister（仅自定义 daemon 禁用了唯一 profile）——
//     通过 Deregister 现在过期的本地 runtime ID 并清空本地跟踪。没有它，
//     registerRuntimesForWorkspace 会在空列表上短路，daemon 会继续对理应离线的
//     runtimes 轮询和心跳，服务器则会让它们在线整整 150 秒心跳过期窗口。
//
// workspaceState 指针从不被替换（与 syncWorkspacesFromAPI 和
// reregisterWorkspaceAfterRuntimeGone 上记录的不变式一致）。
func (d *Daemon) refreshWorkspaceRuntimeProfiles(ctx context.Context, workspaceID string) error {
	refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := d.client.GetRuntimeProfiles(refreshCtx, workspaceID)
	if err != nil {
		// 旧服务器（没有 profiles 路由）返回 404；让按需调用方在 debug 级记录失败。
		return err
	}
	var profiles []RuntimeProfile
	if resp != nil {
		profiles = resp.RuntimeProfiles
	}
	live := profileSetSignature(profiles)

	d.mu.Lock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		d.mu.Unlock()
		// 刷新还在途时工作区被移除了——没什么可做。
		return nil
	}
	cached := ws.profileSetSig
	d.mu.Unlock()

	if cached == live {
		return nil
	}

	d.logger.Info("custom runtime profile set changed; refreshing workspace runtimes",
		"workspace_id", workspaceID, "previous_sig", cached, "current_sig", live,
		"profile_count", len(profiles))

	// 发送、应用与清理作为一个有序步骤——见 workspaceRegisterLock。
	return d.withWorkspaceRegisterLock(workspaceID, func() error {
		return d.applyProfileDriftRegistration(ctx, workspaceID)
	})
}

// applyProfileDriftRegistration 是 refreshWorkspaceRuntimeProfiles 的注册半边。
// 调用方必须持有该工作区的注册锁。
func (d *Daemon) applyProfileDriftRegistration(ctx context.Context, workspaceID string) error {
	regResp, profileSig, preserve, err := d.registerRuntimesForWorkspaceLocked(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, ErrNoRuntimesToRegister) {
			// 收敛到零：仅自定义 daemon 的唯一启用 profile 刚被禁用/删除，又没有
			// 内置 agent 兜底。丢掉 daemon 的本地跟踪，并主动 Deregister 服务器侧
			// 的孤儿行，让 runtime 列表收敛为空，而不是干等 150 秒的陈旧心跳清扫。
			//
			// 空载荷只对"探测确实下了结论的那些 provider"证明"无可托管"，而且只
			// 对本路径有权行动的裁决有效。被当作 UNAVAILABLE 丢掉、或被确认低于
			// 最低版本的 provider，缺席的原因本路径不得当作权威处理（见
			// preserveProvidersFromProbe），所以它们既有的内置行被保留——与
			// applyRegisterResponseInPlace 同一规则——而漂移真正针对的 profile
			// runtime 仍然收敛。跳过整个收敛的话，一个永远探测不了的 CLI 会
			// 让被禁用 profile 的 runtime 永远在线。
			return d.convergeWorkspaceRuntimesToZero(ctx, workspaceID, profileSig, preserve)
		}
		return err
	}

	newIDs, droppedIDs, ok := d.applyRegisterResponseInPlace(workspaceID, regResp, profileSig, preserve)
	if !ok {
		return fmt.Errorf("workspace %s no longer tracked", workspaceID)
	}

	for _, rid := range newIDs {
		d.logger.Info("re-registered runtime after profile drift",
			"workspace_id", workspaceID, "runtime_id", rid)
	}
	d.notifyRuntimeSetChanged()

	// 漂移可能缩小了 runtime 集合（某 profile 被禁用、其它 runtime 存活）。主动把
	// 那些服务器侧行标为离线，让 runtime 列表立刻反映现实；这里若 5xx 也没关系，
	// 服务器约 150 秒的陈旧心跳清扫会兜底捡起来。
	d.deregisterDroppedRuntimes(ctx, workspaceID, droppedIDs, "profile drift", nil)

	// 刻意不做 RecoverOrphans：见方法 doc。
	return nil
}

// convergeWorkspaceRuntimesToZero 处理漂移刷新中"registerRuntimesForWorkspaceLocked
// 会因该工作区已无任何可托管内容而短路"的情形。它 Deregister 之前跟踪的 runtime ID
// （best-effort）并清空 daemon 本地跟踪，使 taskWakeup / heartbeat / poll 循环停止
// 对理应离线 runtimes 发起工作。
//
// 调用方必须持有该工作区的注册锁——这是清理，它与其它每个清理有相同的排序要求
// （workspaceRegisterLock）。
//
// preserveProviders 列出那些"本轮缺席载荷不代表什么权威结论"的内置 provider
// （见 preserveProvidersFromProbe），因此它们既有的内置行存活——与
// applyRegisterResponseInPlace 应用的保留规则相同。其它一切（被禁用/删除的
// profile 的 runtime，以及探测已下结论的任何内置）都收敛消失。
//
// workspaceState 指针被保留：该工作区本身仍是用户所属的合法工作区，只是暂时在这
// 台 daemon 上没有 agent。用户重新启用 profile 或安装内置 agent 时，profile 变更
// 通知或下一次 daemon WS 重连都会重新注册它。
func (d *Daemon) convergeWorkspaceRuntimesToZero(ctx context.Context, workspaceID, profileSig string, preserveProviders map[string]string) error {
	d.mu.Lock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		d.mu.Unlock()
		return nil
	}
	// 用全新数组，而不是 ws.runtimeIDs[:0]：health 处理器会在 d.mu 下拷贝这个
	// slice 头、并在放开锁之后序列化它（removeStaleRuntime 保持同一规则）。
	kept := ws.runtimeIDs[:0:0]
	var dropped []string
	for _, rid := range ws.runtimeIDs {
		if rt, tracked := d.runtimeIndex[rid]; tracked && rt.ProfileID == "" {
			if _, preserve := preserveProviders[rt.Provider]; preserve {
				kept = append(kept, rid)
				continue
			}
			// 在这里收敛消失的内置行，本轮已确认不存在；连带删掉它的版本记录，
			// 与降级路径一致。
			delete(ws.builtinVersions, rt.Provider)
		}
		delete(d.runtimeIndex, rid)
		dropped = append(dropped, rid)
	}
	ws.runtimeIDs = kept
	if profileSig != "" {
		// 缓存收敛后的签名，免得之后每个 sync tick 都再收敛一遍、绕圈。
		ws.profileSetSig = profileSig
	}
	d.mu.Unlock()

	d.logger.Info("custom runtime profile drift converged to zero; clearing local tracking",
		"workspace_id", workspaceID, "deregistered_runtime_ids", dropped,
		"preserved_providers", preserveProviders)

	d.deregisterDroppedRuntimes(ctx, workspaceID, dropped, "zero-runtime convergence", nil)
	d.notifyRuntimeSetChanged()
	return nil
}

// ensureRepoReady 保证目标仓库 URL 已进入本地缓存且可 checkout。它在 per-workspace
// 的 repoRefreshMu 下工作：冷未命中时先向服务器刷新仓库与设置，再同步进缓存。
// 工作区不允许该 URL 时返回 ErrRepoNotConfigured；已允许但同步失败时给出明确的
// "configured but not synced" 错误。
func (d *Daemon) ensureRepoReady(ctx context.Context, workspaceID, repoURL string) error {
	if d.repoCache == nil {
		return fmt.Errorf("repo cache not initialized")
	}

	repoURL = strings.TrimSpace(repoURL)

	d.mu.Lock()
	ws, ok := d.workspaces[workspaceID]
	d.mu.Unlock()
	if !ok {
		return fmt.Errorf("workspace is not watched by this daemon: %s", workspaceID)
	}

	// 记下"在我们拿到 per-workspace 锁之前，缓存是否已经有这个仓库"。两种状态
	// 下面的行为不一样：
	//
	//   - cacheHitOnEntry=true：仓库已经 clone 过；但我们仍要刷新
	//     `workspaceState.settings`，因为 /repo/checkout 处理器紧接着就要读
	//     workspaceCoAuthoredByEnabled。周期 workspace 同步刻意不刷新仓库或设置，
	//     所以"刚翻转的 GitHub 总开关 / co_authored_by_enabled"正是在这里生效。
	//
	//   - cacheHitOnEntry=false，但取到锁"之后"命中缓存：某个兄弟 goroutine 在一
	//     次并发的冷未命中上已经刷新并填好了缓存。这次重复刷新可以跳过——兄弟
	//     的刷新对我们要读的门控来说足够新鲜。
	cacheHitOnEntry := d.workspaceRepoAllowed(workspaceID, repoURL) && d.repoCache.Lookup(workspaceID, repoURL) != ""

	if err := ws.repoRefreshMu.Lock(ctx); err != nil {
		return err
	}
	defer ws.repoRefreshMu.Unlock()

	if !cacheHitOnEntry && d.workspaceRepoAllowed(workspaceID, repoURL) && d.repoCache.Lookup(workspaceID, repoURL) != "" {
		return nil
	}

	resp, err := d.refreshWorkspaceRepos(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("refresh workspace repos: %w", err)
	}

	if !d.workspaceRepoAllowed(workspaceID, repoURL) {
		return ErrRepoNotConfigured
	}

	if d.repoCache.Lookup(workspaceID, repoURL) != "" {
		return nil
	}

	d.syncWorkspaceReposContext(ctx, workspaceID, resp.Repos)
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}

	if d.repoCache.Lookup(workspaceID, repoURL) != "" {
		return nil
	}

	if syncErr := d.workspaceLastRepoSyncErr(workspaceID); syncErr != "" {
		return fmt.Errorf("repo is configured but not synced: %s", syncErr)
	}

	return fmt.Errorf("repo is configured but not synced")
}

// DefaultTokenRenewalInterval 是 daemon 请求服务器延长其 PAT 的间隔。服务器侧阈值
// 是剩余 7 天；大约每 3 天轮询一次，在窗口关闭前至少还有两次续期机会，因此单次
// 失败（网络抖动、服务器重启）不会把 token 推出续期窗口。
const DefaultTokenRenewalInterval = 3 * 24 * time.Hour

// preflightAuth 按必需顺序执行两个对鉴权敏感的启动步骤：先同步续期 PAT，再做
// 首次 workspace 同步。顺序很重要——在任何其它 API 调用前先跑 tryRenewToken，
// 才能在 PAT 已被吊销或过期时给出用户可行动的 "run multica login" WARN。如果让
// workspace 同步先跑，它的 401 会在续期循环第一次 tick 之前就让 Run 短路，运维
// 只能看到 workspace 同步日志里的泛化鉴权失败，毫无"重新登录是解药"的提示。
//
// 续期是 best-effort：tryRenewToken 只记录并返回，从不向上传播错误。preflightAuth
// 的退出状态完全由 workspace 同步决定——因此一次瞬时续期失败（网络抖动、500）
// 本身不会阻塞启动。零工作区但同步成功也没问题：刚注册的用户可能先启动 daemon
// 再创建第一个工作区，workspaceSyncLoop 会在工作区出现后注册 runtimes。
func (d *Daemon) preflightAuth(ctx context.Context) error {
	d.tryRenewToken(ctx)
	err := d.syncWorkspacesFromAPI(ctx, false)
	if d.startupMayProceedWithoutRuntimes(err) {
		d.logger.Warn("starting with no runtimes registered: an automatic DSH runtime profile install is still running; " +
			"the runtime registers when it finishes")
		return nil
	}
	return err
}

// startupMayProceedWithoutRuntimes reports whether a startup that registered
// nothing may continue anyway.
//
// Exactly one situation qualifies: the daemon has just started installing the
// DSH runtime profile itself. On a host whose only provider is a dsh without
// that profile, failing here is a deadlock rather than a fail-fast — the probe
// starts the install, this error kills the process milliseconds later, the
// install's process tree dies with it, and the next start repeats all of it.
// The feature exists precisely for that host, so on it the feature could never
// once complete.
//
// Nothing else is forgiven. An unreachable server, a rejected token, and a
// genuinely empty machine all still fail startup, because for those the daemon
// has no reason to believe the answer is coming.
func (d *Daemon) startupMayProceedWithoutRuntimes(err error) bool {
	return errors.Is(err, errNoWorkspaceRuntimesRegistered) && d.dshInstallInFlight.Load()
}

// trackedWorkspaceCount reports how many workspaces the daemon currently hosts.
func (d *Daemon) trackedWorkspaceCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.workspaces)
}

// registerAfterDshProfileInstall brings a newly usable dsh online without
// waiting for a scheduled round.
//
// A converge round is enough in the ordinary case, but not after the bootstrap
// startupMayProceedWithoutRuntimes allows: a daemon that registered nothing
// tracks no workspace, and every converge path iterates the workspaces it
// tracks, so there would be nothing for the round to look at. The workspace has
// to be picked up first, and waiting for the periodic consistency sync to do it
// would leave the runtime offline for its full interval after an install the
// user watched succeed.
func (d *Daemon) registerAfterDshProfileInstall(ctx context.Context) {
	if d.trackedWorkspaceCount() > 0 {
		d.kickAgentDiscovery()
		return
	}
	if err := d.syncWorkspacesFromAPI(ctx, false); err != nil {
		d.logger.Warn("workspace sync after the DSH runtime profile install failed; "+
			"the periodic sync is the backstop", "error", err)
	}
}

// tokenRenewalLoop 通过定期请求服务器就地延长 PAT 的 expires_at，让 daemon 的
// token 保持存活。启动时的续期在 preflightAuth 中同步完成，使宕机一周后重启的
// daemon 能在下一次心跳 401 之前拿到新过期时间；本循环负责此后约 3 天节奏的
// 长期续期。
//
// 服务器对续期阈值拥有最终解释权（它看得到 expires_at，我们看不到），因此本循环
// 刻意保持简单：调用、记录、睡眠、重复。401 时给出清晰的 "re-login required"
// 警告，因为 daemon 无法自动恢复——但循环继续跑，让用户在每个周期都看到同样的
// 警告直到修复，而不是静默退出、逼他们翻滚动缓冲找原因。
func (d *Daemon) tokenRenewalLoop(ctx context.Context) {
	ticker := time.NewTicker(DefaultTokenRenewalInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.tryRenewToken(ctx)
		}
	}
}

// tryRenewToken 用短隔离超时执行一次续期往返。错误只记录从不向上传播——没有
// 调用方处理它们。除 401 外失败均为 debug 级，401 会给出用户可行动的警告。
func (d *Daemon) tryRenewToken(ctx context.Context) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	resp, err := d.client.RenewToken(reqCtx)
	if err != nil {
		if isUnauthorizedError(err) {
			loginHint := "'multica login'"
			if d.cfg.Profile != "" {
				loginHint = fmt.Sprintf("'multica login --profile %s'", d.cfg.Profile)
			}
			d.logger.Warn("auth token rejected by server — run "+loginHint+" to re-authenticate, then restart the daemon", "error", err)
			return
		}
		d.logger.Debug("token renewal failed; will retry on next cycle", "error", err)
		return
	}
	if resp.Renewed {
		d.logger.Info("auth token renewed", "expires_at", resp.ExpiresAt)
	} else {
		d.logger.Debug("auth token not yet eligible for renewal", "expires_at", resp.ExpiresAt)
	}
}

// workspaceSyncLoop 对账用户的 workspace 成员集合。有 runtimes 且支持账号级 WS 的
// daemon 使用 30 分钟加抖的一致性检查；对接旧服务器的 daemon 保留 5 分钟兜底。
// 尚无 runtimes 的引导 daemon 保持更短间隔，以便尽快发现第一个工作区。账号级 WS
// 提示会触发一次立即的最小同步，而 WS 重连还会对账断连期间变化的 runtime profile。
func (d *Daemon) workspaceSyncLoop(ctx context.Context) {
	timer := time.NewTimer(jitterDuration(d.workspaceSyncBaseInterval()))
	defer timer.Stop()

	var reconcileCh <-chan struct{}
	if d.reconcile != nil {
		reconcileCh = d.reconcile.notify()
	}
	var workspaceChangesCh <-chan struct{}
	if d.workspaceChanges != nil {
		workspaceChangesCh = d.workspaceChanges.notify()
	}

	var consecutiveFailures int
	resetTimer := func() {
		interval := workspaceSyncBackoff(d.workspaceSyncBaseInterval(), consecutiveFailures)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(jitterDuration(interval))
	}

	syncNow := func(reconcileProfiles bool) {
		if err := d.syncWorkspacesFromAPI(ctx, reconcileProfiles); err != nil {
			consecutiveFailures++
			d.logger.Debug("workspace sync failed", "error", err)
		} else {
			consecutiveFailures = 0
		}
		resetTimer()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-reconcileCh:
			if d.reconcile != nil {
				reconcileCh = d.reconcile.notify()
			}
			// WebSocket 断连期间发生的一次设置编辑，产生了一条没人收到的
			// workspaces-changed 提示。重连正是 daemon 补上它的机会：不做这一步，
			// 陈旧的裁决只会被下面的 sync 原样重新发布。
			d.refreshTrackedWorkspaceSettings(ctx)
			syncNow(true)
		case <-workspaceChangesCh:
			// 这条提示既为成员变化触发、也为 workspace 编辑触发（包括设置）。先
			// 刷新已跟踪工作区的缓存设置，再做成员集合对账。
			d.refreshTrackedWorkspaceSettings(ctx)
			syncNow(false)
		case <-timer.C:
			syncNow(false)
		}
	}
}

// workspaceSyncBaseInterval 依据当前状态选择同步基础间隔：无任何 runtime 的
// 引导期用最短间隔；只有旧端点时用旧间隔；正常用默认长间隔。
func (d *Daemon) workspaceSyncBaseInterval() time.Duration {
	if len(d.allRuntimeIDs()) == 0 {
		return DefaultWorkspaceBootstrapSyncInterval
	}
	if d.client.usesLegacyWorkspaceEndpoint() {
		return DefaultWorkspaceLegacySyncInterval
	}
	return DefaultWorkspaceSyncInterval
}

// workspaceSyncBackoff 按连续失败次数对基础间隔做指数退避，封顶为
// DefaultWorkspaceSyncMaxBackoff（引导间隔的场景封顶为旧版同步间隔）。
func workspaceSyncBackoff(base time.Duration, consecutiveFailures int) time.Duration {
	maxInterval := DefaultWorkspaceSyncMaxBackoff
	if base == DefaultWorkspaceBootstrapSyncInterval {
		maxInterval = DefaultWorkspaceLegacySyncInterval
	}
	interval := base
	for i := 0; i < consecutiveFailures; i++ {
		if interval >= maxInterval/2 {
			return maxInterval
		}
		interval *= 2
	}
	if interval > maxInterval {
		return maxInterval
	}
	return interval
}

// syncWorkspacesFromAPI 拉取用户所属的全部工作区，并给任何尚未跟踪的工作区注册
// runtimes。reconcileProfiles 为 true 时（daemon WS 连接/重连之后），会对已跟踪
// 工作区对账一次自定义 runtime profile，保证 WS 不可用期间产生的变化不丢失。普通
// 定时器与 workspace-change 提示传 false，不做 runtime-profile 请求。用户已退出的
// 工作区会被清理掉。
func (d *Daemon) syncWorkspacesFromAPI(ctx context.Context, reconcileProfiles bool) error {
	d.reloading.Lock()
	defer d.reloading.Unlock()

	apiCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	workspaces, err := d.client.ListWorkspaces(apiCtx)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	d.logger.Debug("workspace sync: fetched workspaces", "count", len(workspaces))

	apiIDs := make(map[string]string, len(workspaces)) // id -> name
	for _, ws := range workspaces {
		apiIDs[ws.ID] = ws.Name
	}

	d.mu.Lock()
	currentIDs := make(map[string]bool, len(d.workspaces))
	for id := range d.workspaces {
		currentIDs[id] = true
	}
	d.mu.Unlock()

	// 内置 agent CLI 按机器安装，所以一轮探测就能供本 sync 要注册的每个工作区
	// 共享（MUL-5225）。探测是惰性的——一轮没发现任何新东西要注册的 sync 根本
	// 不 shell out，这正是周期 sync 的常态——且只限定在本调用内，所以下一次 sync
	// 会重新探测、原地升级的 CLI 仍会被上报。
	var (
		builtins       []map[string]string
		builtinsProbed bool
	)
	probeBuiltins := func() []map[string]string {
		if !builtinsProbed {
			builtins, _, _ = d.detectBuiltinRuntimes(ctx)
			builtinsProbed = true
		}
		return builtins
	}

	var registered int
	var removed int
	for id, name := range apiIDs {
		if currentIDs[id] {
			if reconcileProfiles {
				if err := d.refreshWorkspaceRuntimeProfiles(ctx, id); err != nil {
					d.logger.Debug("workspace reconcile: profile refresh failed", "workspace_id", id, "error", err)
				}
			}
			// 只有在该工作区丢光了所有 runtimes 时才继续干预（最常见是
			// handleRuntimeGone 剪掉了它们、且其内联 re-register 失败）。这里同样
			// 不替换指针——ensureRepoReady 还握着原始指针的 repoRefreshMu。
			if !d.workspaceNeedsRuntimeRecovery(id) {
				continue
			}
			d.logger.Info("workspace has no runtimes; retrying registration", "workspace_id", id, "name", name)
			if err := d.reregisterWorkspaceAfterRuntimeGone(ctx, id); err != nil {
				d.logger.Warn("retry register failed", "workspace_id", id, "error", err)
				continue
			}
			registered++
			continue
		}
		payload := probeBuiltins()
		var (
			resp       *RegisterResponse
			runtimeIDs []string
		)
		// 发送、发布与清理作为一个有序步骤——见 workspaceRegisterLock。
		err := d.withWorkspaceRegisterLock(id, func() error {
			var profileSig string
			var err error
			resp, profileSig, err = d.registerRuntimesForWorkspaceBatchLocked(ctx, id, payload)
			if err != nil {
				return err
			}
			// 首次注册是响应到达本地状态的第三条路径——它直接建 workspaceState，
			// 不经 applyRegisterResponseInPlace / mergeBuiltinRegisterResponse——
			// 所以它需要同样的降级护栏，且在"发布状态"同一个 d.mu 临界区里取。
			// 一个在某 provider 被扣住"低于最低版本"时加入的工作区，绝不能成为
			// 那个 provider 复活的路。
			d.mu.Lock()
			runtimeIDs = make([]string, 0, len(resp.Runtimes))
			var revived revivedRuntimes
			for _, rt := range resp.Runtimes {
				if rt.ProfileID == "" && d.providerDemotedLocked(rt.Provider) {
					revived.add(d, rt.ID, rt.Provider)
					continue
				}
				runtimeIDs = append(runtimeIDs, rt.ID)
				d.logger.Info("registered runtime", "workspace_id", id, "runtime_id", rt.ID, "provider", rt.Provider)
			}
			ws := newWorkspaceState(id, runtimeIDs, resp.ReposVersion, resp.Repos, resp.Settings)
			// 播种 profile 签名，之后按需的变更通知才能在不因重复而重注册的前提下
			// 察觉漂移（空签名是 appendProfileRuntimes 显式给出的 "unknown——保留
			// 旧值"哨兵；首次注册没有旧值，所以空就保持空）。
			ws.profileSetSig = profileSig
			// 播种"服务器被告知了什么"的按工作区记录（上面的 register 调用跑在
			// 这个 workspaceState 存在之前，所以它内部的 recordBuiltinVersionsSent
			// 无处可写）。
			ws.builtinVersions = builtinVersionsFromPayload(payload)
			for provider := range ws.builtinVersions {
				// 与 recordBuiltinVersionsSent 跳过它们同因：给"行正被拒绝的
				// provider"记版本，会被下一轮 refresh 读成"这个工作区已最新"。
				if d.providerDemotedLocked(provider) {
					delete(ws.builtinVersions, provider)
				}
			}
			d.workspaces[id] = ws
			for _, rt := range resp.Runtimes {
				if rt.ProfileID == "" && d.providerDemotedLocked(rt.Provider) {
					continue
				}
				d.runtimeIndex[rt.ID] = rt
			}
			d.mu.Unlock()

			// 服务器把被拒的行 upsert 回了存在，所以只有它自己会相信这些行在线、
			// 继续把工作路由过去。在其它东西触碰这个工作区之前把它们拉下线——并且下面
			// 绝不给它们做 RecoverOrphans：那会给一个 daemon 不跟踪、永远不会为其认领
			// 任务的 runtime 上报任务。
			d.deregisterRevivedRuntimes(ctx, id, revived)
			return nil
		})
		if err != nil {
			d.logger.Error("failed to register runtimes", "workspace_id", id, "name", name, "error", err)
			continue
		}

		if d.repoCache != nil && len(resp.Repos) > 0 {
			go d.syncWorkspaceRepos(id, resp.Repos)
		}

		// 把上一任 daemon 进程正在这些 runtimes 上跑的任务告诉服务器。没有这步，
		// 一个问题会一直卡在 in_progress，直到慢心跳清扫器或"在途任务超时"
		// （2.5h）介入。
		for _, rid := range runtimeIDs {
			if err := d.client.RecoverOrphans(ctx, rid); err != nil {
				d.logger.Warn("recover-orphans failed", "runtime_id", rid, "error", err)
			}
		}

		d.logger.Info("watching workspace", "workspace_id", id, "name", name, "runtimes", len(runtimeIDs), "repos", len(resp.Repos))
		registered++
	}

	// 移除用户已不再属于的工作区。
	for id := range currentIDs {
		if _, ok := apiIDs[id]; !ok {
			d.mu.Lock()
			if ws, exists := d.workspaces[id]; exists {
				for _, rid := range ws.runtimeIDs {
					delete(d.runtimeIndex, rid)
				}
			}
			delete(d.workspaces, id)
			d.mu.Unlock()
			d.logger.Info("stopped watching workspace", "workspace_id", id)
			removed++
		}
	}
	if registered > 0 || removed > 0 {
		d.notifyRuntimeSetChanged()
	}

	// 重新发布每个被跟踪工作区的 Co-authored-by 结论。这不花任何请求——它把
	// daemon 缓存的结论写进 prepare-commit-msg hook 读取的地方，并迁移早期版本
	// 留下的 hook——而且正好覆盖了任何刷新都够不到的情形：daemon 宕机期间开关
	// 被翻转（设置随上面的注册响应一起到达）、刚升级的主机、以及被仓库缓存 GC
	// 删掉的 state 文件。
	d.publishTrackedCoAuthoredByState()

	if len(d.allRuntimeIDs()) == 0 && registered == 0 && len(workspaces) > 0 {
		return fmt.Errorf("%w for any of the %d workspace(s)", errNoWorkspaceRuntimesRegistered, len(workspaces))
	}
	if registered > 0 || removed > 0 {
		d.logger.Debug("workspace sync done", "registered", registered, "removed", removed, "tracked", len(apiIDs))
	}
	return nil
}

// heartbeatLoop 监督每个 runtime 独立的 HTTP 心跳 goroutine。每个 runtime 有自己
// 的 ticker，因此某 runtime 的一次慢心跳不会阻塞其它 runtime 的心跳——当单个
// daemon 服务多个工作区时这很重要，因为旧的共享循环会把长达 30 秒的 HTTP 超时
// 串行化到集合中的每个 runtime 上。
func (d *Daemon) heartbeatLoop(ctx context.Context) {
	runtimeSetCh, unsub := d.runtimeSet.Subscribe()
	defer unsub()

	cancels := make(map[string]context.CancelFunc)
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()

	sync := func() {
		want := make(map[string]struct{})
		for _, rid := range d.allRuntimeIDs() {
			want[rid] = struct{}{}
		}
		for rid, cancel := range cancels {
			if _, ok := want[rid]; !ok {
				cancel()
				delete(cancels, rid)
			}
		}
		for rid := range want {
			if _, ok := cancels[rid]; ok {
				continue
			}
			rctx, rcancel := context.WithCancel(ctx)
			cancels[rid] = rcancel
			go d.runRuntimeHeartbeat(rctx, rid)
		}
	}

	sync()
	for {
		select {
		case <-ctx.Done():
			return
		case <-runtimeSetCh:
			sync()
		}
	}
}

// runRuntimeHeartbeat 负责单个 runtime 的 HTTP 心跳排期。第一次 tick 在小的加抖
// 延迟后触发（最多一个完整间隔），避免 daemon 一次注册大量 runtimes 时出现
// thundering herd。
func (d *Daemon) runRuntimeHeartbeat(ctx context.Context, rid string) {
	interval := d.cfg.HeartbeatInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	// 加抖的初始延迟；封顶在一个周期内，让第一拍仍发生在一个周期之内。
	if jitter := time.Duration(rand.Int63n(int64(interval))); jitter > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter):
		}
	}

	consecutiveTransientFailures := 0
	tick := func() {
		if d.runHeartbeatTick(ctx, rid) {
			consecutiveTransientFailures++
			if consecutiveTransientFailures == 2 {
				d.client.CloseIdleConnections()
			}
			return
		}
		consecutiveTransientFailures = 0
	}

	tick()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

// runHeartbeatTick 返回 true 表示 HTTP 心跳遇到了一次应计入"过期空闲连接清理"
// 的瞬时失败。
func (d *Daemon) runHeartbeatTick(ctx context.Context, rid string) bool {
	// 对"最近收到过新鲜 WS 心跳 ack"的 runtime 跳过 HTTP 心跳。WS 路径会刷新
	// last_seen_at 并投递动作，此时再走 HTTP 写等于一次重复的 DB 更新。若 WS
	// 心跳静默，新鲜窗口过期，HTTP 会在下一个 tick 自动恢复——这正是 WS 路径
	// 依赖的兜底。
	if d.wsHeartbeatRecentlyAcked(rid) {
		d.logger.Debug("heartbeat: skipping HTTP tick, WS recently acked", "runtime_id", rid)
		return false
	}
	d.logger.Debug("heartbeat: HTTP tick", "runtime_id", rid)
	resp, err := d.client.SendHeartbeat(ctx, rid)
	if err != nil {
		if ctx.Err() == nil {
			if isRuntimeNotFoundError(err) {
				// 服务器说这个 runtime 没了——去恢复，而不是对着死 UUID 死循环。
				// handleRuntimeGone 会合并并发调用方，并在 daemon 根 context 下跑恢复
				// HTTP 调用，因此 notifyRuntimeSetChanged 拆掉本心跳 goroutine 也打断
				// 不了它。
				go d.handleRuntimeGone(rid)
				return false
			}
			d.logger.Warn("heartbeat failed", "runtime_id", rid, "error", err)
		}
		return ctx.Err() == nil && isTransientError(err)
	}
	if resp != nil && resp.RuntimeGone {
		// WS 路径对同一场景返回带 RuntimeGone=true 的成功 ack；万一 HTTP 也开始
		// 浮现这个信号，这里用同样的方式处理。
		go d.handleRuntimeGone(rid)
		return false
	}
	d.handleHeartbeatActions(ctx, rid, resp)
	return false
}

// handleHeartbeatActions 分发任一种传输（HTTP POST /api/daemon/heartbeat 或
// WS daemon:heartbeat_ack）返回的待办动作集合。每个动作都在自己的 goroutine 里
// 分发，慢的处理器不会阻塞后续心跳。
func (d *Daemon) handleHeartbeatActions(ctx context.Context, runtimeID string, resp *HeartbeatResponse) {
	if resp == nil {
		return
	}
	// 1. Historical session extraction is a bounded daemon operation, separate from model execution.
	if request := resp.PendingExecutionEvidence; request != nil {
		if rt := d.findRuntime(runtimeID); rt != nil {
			go d.handleExecutionEvidenceRequest(*rt, *request)
		}
	}
	if resp.PendingUpdate != nil || resp.PendingModelList != nil || resp.PendingLocalSkills != nil || resp.PendingLocalSkillImport != nil {
		d.logger.Debug("heartbeat: pending actions",
			"runtime_id", runtimeID,
			"update", resp.PendingUpdate != nil,
			"model_list", resp.PendingModelList != nil,
			"local_skills", resp.PendingLocalSkills != nil,
			"local_skill_import", resp.PendingLocalSkillImport != nil,
		)
	}
	if resp.PendingMcpCheck != nil {
		go d.handleWorkspaceMcpCheck(ctx, runtimeID, *resp.PendingMcpCheck)
	}
	if resp.PendingUpdate != nil {
		go d.handleUpdate(ctx, runtimeID, resp.PendingUpdate)
	}
	if resp.PendingModelList != nil {
		if rt := d.findRuntime(runtimeID); rt != nil {
			go d.handleModelList(ctx, *rt, resp.PendingModelList.ID)
		}
	}
	if resp.PendingLocalSkills != nil {
		if rt := d.findRuntime(runtimeID); rt != nil {
			go d.handleLocalSkillList(ctx, *rt, resp.PendingLocalSkills.ID)
		}
	}
	// 优先用批量子段（新后端）；退回单数子段（旧后端）。
	if len(resp.PendingLocalSkillImports) > 0 {
		if rt := d.findRuntime(runtimeID); rt != nil {
			for _, imp := range resp.PendingLocalSkillImports {
				go d.handleLocalSkillImport(ctx, *rt, imp)
			}
		}
	} else if resp.PendingLocalSkillImport != nil {
		if rt := d.findRuntime(runtimeID); rt != nil {
			go d.handleLocalSkillImport(ctx, *rt, *resp.PendingLocalSkillImport)
		}
	}
}

// handlePendingWorkHint 对服务器推送的 daemon:pending_work 帧做出反应：为该
// runtime 立即发一次心跳，然后分发这次心跳认领到的一切（MUL-5444）。
//
// 为什么是心跳而不是直接把工作发下去：提示刻意不带请求载荷，因此投递失败时
// 服务器永远不需要撤销任何认领；重复或重放的提示也不可能复制工作——认领在 store
// 的 PopPending 里保持原子。从未到达的提示（daemon 离线、WS 断档、relay 丢包）
// 只损失"等待下一次定时心跳"这个既有成本。
//
// 特意用 HTTP 心跳而不是排队 WS 帧：提示到达读泵，而 WS 写路径可能正被阻塞或
// 正在拆除；这是人工交互、低频的路径，多一个请求比错过一次唤醒更便宜。注意它
// 刻意绕过定时 HTTP tick 遵守的 wsHeartbeatRecentlyAcked 抑制——那个抑制是为
// 避免重复周期写入，而不是为拦截一次被明确请求的拉取。
func (d *Daemon) handlePendingWorkHint(runtimeID, kind string) {
	if runtimeID == "" {
		return
	}
	if d.findRuntime(runtimeID) == nil {
		// 不是我们的 runtime（陈旧的中继扇出，或该 runtime 刚被剪掉）。
		return
	}

	d.pendingWorkMu.Lock()
	if d.pendingWorkInflight == nil {
		d.pendingWorkInflight = make(map[string]struct{})
	}
	if d.pendingWorkLastRun == nil {
		d.pendingWorkLastRun = make(map[string]time.Time)
	}
	// 清掉长期闲置的记账条目，免得这个 map 跟着"本进程见过的每个 runtime"
	// 无限膨胀。
	for id, at := range d.pendingWorkLastRun {
		if time.Since(at) > pendingWorkHintBookkeepingTTL {
			delete(d.pendingWorkLastRun, id)
		}
	}
	if _, inflight := d.pendingWorkInflight[runtimeID]; inflight {
		d.pendingWorkMu.Unlock()
		return
	}
	// 按 runtime 限速。提示由交互式模型/能力端点的调用方触发，没有下限的话，
	// 一个请求循环就会变成心跳放大器。被压掉的提示不花任何成本——只是照旧等
	// 一次定时心跳。
	if last, ok := d.pendingWorkLastRun[runtimeID]; ok && time.Since(last) < pendingWorkHintMinInterval {
		d.pendingWorkMu.Unlock()
		d.logger.Debug("pending work hint throttled", "runtime_id", runtimeID, "kind", kind)
		return
	}
	d.pendingWorkInflight[runtimeID] = struct{}{}
	d.pendingWorkLastRun[runtimeID] = time.Now()
	d.pendingWorkMu.Unlock()
	defer func() {
		d.pendingWorkMu.Lock()
		delete(d.pendingWorkInflight, runtimeID)
		d.pendingWorkMu.Unlock()
	}()

	// 用根 context：handleHeartbeatActions 会把 ctx 交给实际工作，所以它必须活得
	// 比本函数久。能力发现与本地技能导入通常是很快的文件系统操作，而模型发现最
	// 慢的 provider 上可能要 shell out 到 CLI 最多 ~40 秒（见
	// agent.hermesDiscoveryTimeout）。只有心跳请求本身是限定时间的。
	ctx := d.recoveryContext()
	if ctx.Err() != nil {
		return
	}
	hbCtx, cancel := context.WithTimeout(ctx, pendingWorkHeartbeatTimeout)
	resp, err := d.client.SendHeartbeat(hbCtx, runtimeID)
	cancel()
	if err != nil {
		if isRuntimeNotFoundError(err) {
			go d.handleRuntimeGone(runtimeID)
			return
		}
		d.logger.Debug("pending work hint heartbeat failed", "runtime_id", runtimeID, "kind", kind, "error", err)
		return
	}
	if resp == nil {
		return
	}
	if resp.RuntimeGone {
		go d.handleRuntimeGone(runtimeID)
		return
	}
	d.logger.Debug("pending work hint served", "runtime_id", runtimeID, "kind", kind)
	d.handleHeartbeatActions(ctx, runtimeID, resp)
}

// handleModelList 解析 provider 支持的模型（经静态目录或拉起 agent CLI 探测），
// 并把结果回传给服务器。
//
// 发现失败如何上报是 provider 的决定，不是本函数的。拥有安全静态目录的 provider
// 会吞掉错误并返回替身（标记 Fallback）；没有静态目录的——hermes——会返回错误，
// 它会被转发为 status=failed，让选择器能显示原因并保留手动输入（MUL-6606）。
// 两种结果都让可创建下拉菜单保持可用；只有第二种会告诉用户它为什么是空的。
func (d *Daemon) handleModelList(ctx context.Context, rt Runtime, requestID string) {
	d.logger.Info("model list requested", "runtime_id", rt.ID, "request_id", requestID, "provider", rt.Provider)

	// 发现必须枚举这个 runtime 真正会执行的二进制，否则选择器推荐的就是一个
	// 启动后的 CLI 从未认可过的目录（MUL-5789）。复刻 runTask 的解析顺序：自定义
	// runtime profile（MUL-3284）拥有可执行路径，且这种 runtime 可能活在一台"没
	// 装任何同协议族内置 agent"的主机上——所以自定义 runtime 绝不能在"内置查找"
	// 上失败。自定义路径也绝不重新解析：和 runTask 一样，我们不事后怀疑 profile
	// 钉住的路径。
	var execPath string
	// fixedArgs 复刻 runTask 会用的启动前缀。发现必须枚举 profile 实际运行的
	// CLI，所以子命令包装器要按 `ccms start q36 models` 探测，而不是 `ccms models`
	// （GH #7046）。
	var fixedArgs []string
	if customSpec, isCustom := d.customProfileLaunchForRuntime(rt.ID); isCustom {
		execPath = customSpec.path
		fixedArgs = agent.FilterLaunchPrefix(rt.Provider, customSpec.fixedArgs, d.logger)
		d.logger.Info("model list uses custom runtime profile command",
			"runtime_id", rt.ID, "provider", rt.Provider, "command_path", execPath,
			"fixed_args", len(fixedArgs))
	} else if entry, ok := d.agents()[rt.Provider]; ok {
		// 内置 provider：自愈被就地升级删掉的钉住可执行路径（MUL-4486）。
		entry, _ = d.resolveAgentEntry(ctx, rt.Provider, entry)
		execPath = entry.Path
	} else {
		d.reportModelListResult(ctx, rt, requestID, map[string]any{
			"status": "failed",
			"error":  fmt.Sprintf("no agent configured for provider %q", rt.Provider),
		})
		return
	}

	catalog, err := listModels(ctx, rt.Provider, agent.NewCommand(execPath, fixedArgs))
	if err != nil {
		d.reportModelListResult(ctx, rt, requestID, map[string]any{
			"status": "failed",
			"error":  err.Error(),
		})
		return
	}
	models := catalog.Models
	if catalog.Fallback {
		d.logger.Warn("model discovery fell back to a static catalog; reporting it as non-authoritative",
			"runtime_id", rt.ID, "provider", rt.Provider, "path", execPath, "count", len(models))
	}
	if rt.Provider == "codearts" && len(models) == 0 {
		if home, homeErr := os.UserHomeDir(); homeErr == nil {
			configuredModels, configErr := loadCodeArtsConfiguredModels(home)
			if configErr != nil {
				d.logger.Warn("CodeArts custom model discovery failed",
					"runtime_id", rt.ID, "path", codeArtsUserConfigPath(home), "error", configErr)
			} else if len(configuredModels) > 0 {
				models = configuredModels
				d.logger.Info("CodeArts model discovery used user-configured providers",
					"runtime_id", rt.ID, "path", codeArtsUserConfigPath(home), "count", len(models))
			}
		}
	}

	// 线格式与 handler.ModelEntry 对齐。用结构体（而不是 map[string]string），
	// Default bool 和每个模型的 Thinking 目录才能完整往返——否则 UI 会丢掉推荐
	// 项上的 "default" 徽标，以及 claude/codex 的思考级别选择器（MUL-2339）。
	type thinkingLevelWire struct {
		Value       string `json:"value"`
		Label       string `json:"label"`
		Description string `json:"description,omitempty"`
	}
	type modelThinkingWire struct {
		SupportedLevels []thinkingLevelWire `json:"supported_levels"`
		DefaultLevel    string              `json:"default_level,omitempty"`
	}
	type modelServiceTierWire struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}
	type modelWire struct {
		ID                                  string                 `json:"id"`
		Label                               string                 `json:"label"`
		Provider                            string                 `json:"provider,omitempty"`
		Default                             bool                   `json:"default,omitempty"`
		Thinking                            *modelThinkingWire     `json:"thinking,omitempty"`
		ServiceTiers                        []modelServiceTierWire `json:"service_tiers,omitempty"`
		SupportsExplicitStandardServiceTier bool                   `json:"supports_explicit_standard_service_tier,omitempty"`
	}
	// Models the runtime named but will not run here (Claude Code reporting one
	// that needs a newer CLI). Reported in their own list, never inside
	// `models`: a client that does not know this field — an installed desktop
	// build predating it — then cannot offer one as a real model, which a flag
	// on the model itself could not guarantee (MUL-6961).
	type unavailableModelWire struct {
		ID     string `json:"id"`
		Label  string `json:"label"`
		Reason string `json:"reason,omitempty"`
	}
	wire := make([]modelWire, 0, len(models))
	for _, m := range models {
		entry := modelWire{
			ID:                                  m.ID,
			Label:                               m.Label,
			Provider:                            m.Provider,
			Default:                             m.Default,
			SupportsExplicitStandardServiceTier: m.SupportsExplicitStandardServiceTier,
		}
		if m.Thinking != nil {
			levels := make([]thinkingLevelWire, 0, len(m.Thinking.SupportedLevels))
			for _, lvl := range m.Thinking.SupportedLevels {
				levels = append(levels, thinkingLevelWire{
					Value:       lvl.Value,
					Label:       lvl.Label,
					Description: lvl.Description,
				})
			}
			entry.Thinking = &modelThinkingWire{
				SupportedLevels: levels,
				DefaultLevel:    m.Thinking.DefaultLevel,
			}
		}
		for _, tier := range m.ServiceTiers {
			entry.ServiceTiers = append(entry.ServiceTiers, modelServiceTierWire{
				ID:          tier.ID,
				Name:        tier.Name,
				Description: tier.Description,
			})
		}
		wire = append(wire, entry)
	}
	unavailableWire := make([]unavailableModelWire, 0, len(catalog.Unavailable))
	for _, m := range catalog.Unavailable {
		unavailableWire = append(unavailableWire, unavailableModelWire{
			ID:     m.ID,
			Label:  m.Label,
			Reason: m.Reason,
		})
	}
	d.reportModelListResult(ctx, rt, requestID, map[string]any{
		"status":    "completed",
		"models":    wire,
		"supported": agent.ModelSelectionSupported(rt.Provider),
		// Additive: an older server drops the key and the picker simply shows
		// no unavailable section, which is the pre-MUL-6961 behaviour.
		"unavailable_models": unavailableWire,
		// Additive field: the models are still worth rendering, but the server
		// must not persist them as this runtime's real catalog (MUL-5549).
		// Older servers ignore it and keep the previous behaviour.
		"fallback": catalog.Fallback,
	})
}

// handleLocalSkillList 拉取某 runtime 的本地技能与本地 MCP 服务器清单并回传。
func (d *Daemon) handleLocalSkillList(ctx context.Context, rt Runtime, requestID string) {
	d.logger.Info("runtime local skills requested", "runtime_id", rt.ID, "request_id", requestID, "provider", rt.Provider)

	skills, supported, err := listRuntimeLocalSkills(rt.Provider)
	if err != nil {
		d.reportLocalSkillListResult(ctx, rt, requestID, map[string]any{
			"status": "failed",
			"error":  err.Error(),
		})
		return
	}
	mcpServers, mcpSupported, err := listRuntimeLocalMcpServers(rt.Provider)
	if err != nil {
		d.logger.Warn("runtime local MCP discovery failed", "runtime_id", rt.ID, "provider", rt.Provider, "error", err)
		mcpServers = []runtimeLocalMcpServerSummary{}
		mcpSupported = false
	}

	d.reportLocalSkillListResult(ctx, rt, requestID, map[string]any{
		"status":        "completed",
		"skills":        skills,
		"supported":     supported,
		"mcp_servers":   mcpServers,
		"mcp_supported": mcpSupported,
	})
}

// handleLocalSkillImport 加载某 runtime 的一个本地技能包并把导入结果回传。
func (d *Daemon) handleLocalSkillImport(ctx context.Context, rt Runtime, pending PendingLocalSkillImport) {
	d.logger.Info("runtime local skill import requested", "runtime_id", rt.ID, "request_id", pending.ID, "provider", rt.Provider, "skill_key", pending.SkillKey)

	skill, supported, err := loadRuntimeLocalSkillBundle(rt.Provider, pending.SkillKey)
	if err != nil {
		d.reportLocalSkillImportResult(ctx, rt, pending.ID, map[string]any{
			"status": "failed",
			"error":  err.Error(),
		})
		return
	}
	if !supported {
		d.reportLocalSkillImportResult(ctx, rt, pending.ID, map[string]any{
			"status": "failed",
			"error":  fmt.Sprintf("provider %q does not expose runtime local skills", rt.Provider),
		})
		return
	}

	d.reportLocalSkillImportResult(ctx, rt, pending.ID, map[string]any{
		"status": "completed",
		"skill":  skill,
	})
}

// runtimeReportBackoffs 定义投递任何 daemon→服务器异步结果（model list、
// local-skill list、local-skill import）的重试排期。首次尝试立即执行，然后退避。
// 总和（约 6.5 秒）把服务器侧 60 秒 running 超时的大部分留给发现与上报尝试。
// 每次 HTTP 尝试有自己的客户端超时，因此本排期本身不是端到端投递截止时间。
//
// 测试可覆盖，避免真实睡眠。
var runtimeReportBackoffs = []time.Duration{0, 500 * time.Millisecond, 2 * time.Second, 4 * time.Second}

// reportLocalSkillListResult 带瞬时失败重试向服务器投递 list 报告。语义见
// reportRuntimeResultWithRetry。
func (d *Daemon) reportLocalSkillListResult(ctx context.Context, rt Runtime, requestID string, payload map[string]any) {
	d.reportRuntimeResultWithRetry(ctx, "local_skill_list", rt.ID, requestID, func(ctx context.Context) error {
		return d.client.ReportLocalSkillListResult(ctx, rt.ID, requestID, payload)
	})
}

// reportLocalSkillImportResult 带瞬时失败重试向服务器投递 import 报告。
func (d *Daemon) reportLocalSkillImportResult(ctx context.Context, rt Runtime, requestID string, payload map[string]any) {
	d.reportRuntimeResultWithRetry(ctx, "local_skill_import", rt.ID, requestID, func(ctx context.Context) error {
		return d.client.ReportLocalSkillImportResult(ctx, rt.ID, requestID, payload)
	})
}

// reportModelListResult 带瞬时失败重试向服务器投递 model-list 报告。没有它，
// daemon 过去只发一次并吞掉任何 5xx，请求会在服务器上滞留 "running" 直到 60s
// 超时——正好抵消多节点 store 修复想消除的失败模式。
func (d *Daemon) reportModelListResult(ctx context.Context, rt Runtime, requestID string, payload map[string]any) {
	d.reportRuntimeResultWithRetry(ctx, "model_list", rt.ID, requestID, func(ctx context.Context) error {
		return d.client.ReportModelListResult(ctx, rt.ID, requestID, payload)
	})
}

// reportRuntimeResultWithRetry 在 5xx / 网络错误上重试 fn，成功、4xx 或耗尽
// runtimeReportBackoffs 后停止。
//
// 为什么存在：服务器通过 Redis / DB 写持久化报告；一次瞬时 store 故障会正确地
// 返回 500。没有客户端重试，daemon 只发一次就吞掉错误，pending 请求会在服务器上
// 停留 "running" 直到超时——这正是多节点 store 修复要消除的 "daemon did not
// respond" 失败模式。4xx 视为永久性（request-not-found、跨工作区 token 被拒、
// body 损坏）——重试它们只是浪费心跳周期。
func (d *Daemon) reportRuntimeResultWithRetry(ctx context.Context, kind, runtimeID, requestID string, fn func(context.Context) error) {
	var lastErr error
	for attempt, wait := range runtimeReportBackoffs {
		if wait > 0 {
			select {
			case <-ctx.Done():
				d.logger.Error("runtime async report cancelled",
					"kind", kind, "runtime_id", runtimeID, "request_id", requestID,
					"attempt", attempt, "error", ctx.Err())
				return
			case <-time.After(wait):
			}
		}
		err := fn(ctx)
		if err == nil {
			if attempt > 0 {
				d.logger.Info("runtime async report succeeded after retry",
					"kind", kind, "runtime_id", runtimeID, "request_id", requestID,
					"attempt", attempt+1)
			}
			return
		}
		lastErr = err

		// 4xx 是永久的（请求过期、工作区不匹配、body 损坏）。再怎么重试也不会成。
		var reqErr *requestError
		if errors.As(err, &reqErr) && reqErr.StatusCode >= 400 && reqErr.StatusCode < 500 {
			d.logger.Error("runtime async report rejected — not retrying",
				"kind", kind, "runtime_id", runtimeID, "request_id", requestID,
				"status", reqErr.StatusCode, "error", err)
			return
		}

		d.logger.Warn("runtime async report failed — will retry",
			"kind", kind, "runtime_id", runtimeID, "request_id", requestID,
			"attempt", attempt+1, "error", err)
	}
	d.logger.Error("runtime async report exhausted retries",
		"kind", kind, "runtime_id", runtimeID, "request_id", requestID, "error", lastErr)
}

// handleUpdate 在服务器通过心跳触发 CLI 更新时执行更新。
func (d *Daemon) handleUpdate(ctx context.Context, runtimeID string, update *PendingUpdate) {
	// 桌面端托管的 daemon 与 Electron 应用共享同一个 CLI 二进制，由应用负责下发与
	// 替换。让 daemon 自我更新只会被下次桌面启动覆盖，还可能在更新中途弄坏内嵌
	// 二进制。干净地拒绝。
	if d.cfg.LaunchedBy == "desktop" {
		d.logger.Info("refusing CLI self-update: daemon is managed by Desktop", "runtime_id", runtimeID, "update_id", update.ID)
		d.reportUpdateResult(ctx, runtimeID, update.ID, map[string]any{
			"status": "failed",
			"error":  "CLI is managed by Multica Desktop — update the Desktop app to upgrade the CLI",
		})
		return
	}

	switch d.tryBeginServerUpdate(ctx) {
	case serverUpdateAlreadyRunning:
		d.logger.Warn("update deferred: another update is already in progress", "runtime_id", runtimeID, "update_id", update.ID)
		d.reportUpdateResult(ctx, runtimeID, update.ID, map[string]any{
			"status": "failed",
			"error":  "another runtime update is already in progress on this machine",
		})
		return
	case serverUpdateRuntimeBusy:
		d.logger.Info("update deferred: task or claim in progress", "runtime_id", runtimeID, "update_id", update.ID)
		d.reportUpdateResult(ctx, runtimeID, update.ID, map[string]any{
			"status": "failed",
			"error":  "runtime update deferred because agent work is starting or still active; retry when the machine is idle",
		})
		return
	}
	restarting := false
	defer func() {
		if !restarting {
			d.releaseClaimBarrier()
			d.updating.Store(false)
		}
	}()

	d.logger.Info("CLI update requested", "runtime_id", runtimeID, "update_id", update.ID, "target_version", update.TargetVersion)

	// 上报运行中状态。
	d.reportUpdateResult(ctx, runtimeID, update.ID, map[string]any{
		"status": "running",
	})

	output, err := d.runUpdateFn(update.TargetVersion)
	if err != nil {
		d.logger.Error("CLI update failed", "error", err, "output", output)
		d.reportUpdateResult(ctx, runtimeID, update.ID, map[string]any{
			"status": "failed",
			"error":  err.Error(),
		})
		return
	}

	d.logger.Info("CLI update completed successfully", "output", output)
	d.reportUpdateResult(ctx, runtimeID, update.ID, map[string]any{
		"status": "completed",
		"output": fmt.Sprintf("Updated to %s", update.TargetVersion),
	})

	// 触发 daemon 用新二进制重启。
	d.triggerRestart()
	restarting = d.RestartBinary() != ""
}

type serverUpdateAcquireResult uint8

const (
	serverUpdateAcquired serverUpdateAcquireResult = iota
	serverUpdateAlreadyRunning
	serverUpdateRuntimeBusy
)

// tryBeginServerUpdate 原子地认领更新所有权、暂停新认领，并让任何已认领的任务
// 完成交接。因此一次空认领绝不会饿死更新；返回工作的认领会在 exitClaim 前增加
// activeTasks，最终检查仍能安全地推迟更新。
func (d *Daemon) tryBeginServerUpdate(ctx context.Context) serverUpdateAcquireResult {
	if !d.updating.CompareAndSwap(false, true) {
		return serverUpdateAlreadyRunning
	}

	d.claimMu.Lock()
	if d.pauseClaims || d.deploymentDrain || d.activeTasks.Load() > 0 {
		d.claimMu.Unlock()
		d.updating.Store(false)
		return serverUpdateRuntimeBusy
	}
	d.pauseClaims = true
	d.claimMu.Unlock()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		d.claimMu.Lock()
		if d.claimsInFlight == 0 {
			if d.activeTasks.Load() == 0 {
				d.claimMu.Unlock()
				return serverUpdateAcquired
			}
			d.pauseClaims = false
			d.claimMu.Unlock()
			d.updating.Store(false)
			return serverUpdateRuntimeBusy
		}
		d.claimMu.Unlock()

		select {
		case <-ctx.Done():
			d.releaseClaimBarrier()
			d.updating.Store(false)
			return serverUpdateRuntimeBusy
		case <-ticker.C:
		}
	}
}

// runUpdate 针对 targetVersion 执行 brew 或下载方式升级，返回人类可读的输出
// （总是有内容，即使失败时 brew 给了有用的诊断也一样）。调用方负责 `updating`
// CAS 护栏、向服务器上报状态、以及触发重启——抽出来是为了让服务器触发的路径
// （handleUpdate）与自动更新轮询器（autoUpdateLoop）共享完全相同的执行体。
func (d *Daemon) runUpdate(targetVersion string) (string, error) {
	if cli.IsBrewInstall() {
		d.logger.Info("updating CLI via Homebrew...")
		out, err := cli.UpdateViaBrew()
		if err != nil {
			return out, fmt.Errorf("brew upgrade failed: %w", err)
		}
		return out, nil
	}
	d.logger.Info("updating CLI via direct download...", "target_version", targetVersion)
	out, err := cli.UpdateViaDownload(targetVersion)
	if err != nil {
		return out, fmt.Errorf("download update failed: %w", err)
	}
	return out, nil
}

// updateReportBackoffs 定义把 CLI 更新状态回传给服务器的重试排期。它与
// localSkillReportBackoffs 一致，因为两个功能有相同的用户可见失败模式：daemon
// 在本地完成了工作，但一次瞬时上报失败让 UI 一直等到服务器侧请求超时。
//
// 测试可覆盖，避免真实睡眠。
var updateReportBackoffs = []time.Duration{0, 500 * time.Millisecond, 2 * time.Second, 4 * time.Second}

// reportUpdateResult 把更新结果回传给服务器（带重试）。
func (d *Daemon) reportUpdateResult(ctx context.Context, runtimeID, updateID string, payload map[string]any) {
	d.reportUpdateResultWithRetry(ctx, runtimeID, updateID, func(ctx context.Context) error {
		return d.client.ReportUpdateResult(ctx, runtimeID, updateID, payload)
	})
}

// reportUpdateResultWithRetry 在 5xx/网络错误上重试更新结果上报，4xx 直接放弃。
func (d *Daemon) reportUpdateResultWithRetry(ctx context.Context, runtimeID, updateID string, fn func(context.Context) error) {
	var lastErr error
	for attempt, wait := range updateReportBackoffs {
		if wait > 0 {
			select {
			case <-ctx.Done():
				d.logger.Error("CLI update report cancelled",
					"runtime_id", runtimeID, "update_id", updateID,
					"attempt", attempt, "error", ctx.Err())
				return
			case <-time.After(wait):
			}
		}

		err := fn(ctx)
		if err == nil {
			if attempt > 0 {
				d.logger.Info("CLI update report succeeded after retry",
					"runtime_id", runtimeID, "update_id", updateID,
					"attempt", attempt+1)
			}
			return
		}
		lastErr = err

		var reqErr *requestError
		if errors.As(err, &reqErr) && reqErr.StatusCode >= 400 && reqErr.StatusCode < 500 {
			d.logger.Error("CLI update report rejected — not retrying",
				"runtime_id", runtimeID, "update_id", updateID,
				"status", reqErr.StatusCode, "error", err)
			return
		}

		d.logger.Warn("CLI update report failed — will retry",
			"runtime_id", runtimeID, "update_id", updateID,
			"attempt", attempt+1, "error", err)
	}
	d.logger.Error("CLI update report exhausted retries",
		"runtime_id", runtimeID, "update_id", updateID, "error", lastErr)
}

// tryEnterClaim 记录"将要调用 ClaimTask"的意图。返回 true 表示调用方可以继续；
// false 表示自动更新屏障生效。每次成功的调用必须在每个退出路径上都配一次
// exitClaim()——或在一次失败/空认领后立即调用，或通过 handleTask goroutine 的
// defer 在任务交接后调用。
func (d *Daemon) tryEnterClaim() bool {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	if d.pauseClaims || d.deploymentDrain {
		return false
	}
	d.claimsInFlight++
	return true
}

// exitClaim 释放 tryEnterClaim 记录的在途认领。
func (d *Daemon) exitClaim() {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	d.claimsInFlight--
}

// trySetClaimBarrier 在 daemon 完全空闲（无在途认领、无运行任务）时原子地暂停新
// 的 ClaimTask 调用。返回 true 表示调用方现在持有屏障，且必须在每个非重启退出
// 路径上用 releaseClaimBarrier 释放；false 表示 daemon 忙，调用方应推迟到下一个
// tick。tryAutoUpdate 用它关闭"任务在廉价预检查空闲与真正开始升级之间溜进来"
// 的竞态。
func (d *Daemon) trySetClaimBarrier() bool {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	// 屏障已持有时拒绝。没有这个检查，函数会静默地双重获取：两个持有者都以为
	// 自己拥有它，先完成的那个会从另一个脚下把屏障放掉。tryBeginServerUpdate
	// 出于同样原因也做同样的检查。
	if d.pauseClaims || d.deploymentDrain || d.claimsInFlight > 0 || d.activeTasks.Load() > 0 {
		return false
	}
	d.pauseClaims = true
	return true
}

// releaseClaimBarrier 清除自动更新认领屏障，让轮询器恢复认领。只在失败路径上
// 调用——一次成功的升级保持屏障置位，因为 triggerRestart 即将把进程带下去，清除
// 它会在关停期间为新认领开窗口。
func (d *Daemon) releaseClaimBarrier() {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	d.pauseClaims = false
}

// triggerRestart 发起一次优雅重启，进入 restartTargetBinary() 指向的二进制。
// 调用方（cmd_daemon.go）检查 RestartBinary() 并拉起新进程。
//
// 目标路径解析失败时返回 false，因此持有认领屏障的调用方可以释放它，而不是让
// daemon 为一次永远不会发生的重启而一直暂停。已排期的重启算成功：进程无论如何
// 都要下去了。
func (d *Daemon) triggerRestart() bool {
	d.restartMu.Lock()
	defer d.restartMu.Unlock()

	if d.restartBinary != "" {
		d.logger.Debug("daemon restart already scheduled", "new_binary", d.restartBinary)
		return true
	}

	newBin, err := d.restartTargetBinary()
	if err != nil {
		d.logger.Error("could not resolve executable path for restart", "error", err)
		return false
	}

	d.logger.Info("scheduling daemon restart", "new_binary", newBin)
	d.restartBinary = newBin

	// 取消主 context 以触发优雅关停。
	if d.cancelFunc != nil {
		d.cancelFunc()
	}
	return true
}

// restartTargetBinary 解析重启将重新执行的路径。
//
// 对 brew 安装它保留稳定的符号链接路径（例如 /opt/homebrew/bin/multica），让重启
// 后的 daemon 自动拿到新 Cellar 版本：Linux 上 os.Executable() 读 /proc/self/exe，
// 内核把它解析成 Cellar 路径，而 brew cleanup 在升级后会删除该路径。对非 brew
// 安装，它解析为被替换二进制的绝对路径。
//
// 与 trySelfReload 共享，后者必须版本探测"重启会运行的那个二进制"——直接探测
// os.Executable() 在 brew 下会读到旧的 Cellar 路径，完全错过升级。
func (d *Daemon) restartTargetBinary() (string, error) {
	newBin, err := resolveSelfExecutable()
	if err != nil {
		return "", err
	}
	// 安装方式与 brew prefix 在进程生命周期内不变；只解析一次，省得每 tick 的重载
	// 探测每 5 分钟 fork 一次 `brew --prefix`。
	d.brewTargetOnce.Do(func() {
		d.brewInstall = isBrewInstall()
		if !d.brewInstall {
			return
		}
		if brewPrefix := getBrewPrefix(); brewPrefix != "" {
			d.brewTarget = filepath.Join(brewPrefix, "bin", "multica")
		} else if prefix := matchKnownBrewPrefix(newBin); prefix != "" {
			d.brewTarget = filepath.Join(prefix, "bin", "multica")
		}
	})
	if d.brewInstall {
		if d.brewTarget != "" {
			return d.brewTarget, nil
		}
		d.logger.Warn("brew install detected but prefix could not be resolved; restart may fail",
			"executable", newBin)
		return newBin, nil
	}
	if resolved, err := filepath.EvalSymlinks(newBin); err == nil {
		newBin = resolved
	}
	return newBin, nil
}

// pollLoop 运行机器级批量认领轮询器（MUL-4257）：单个 goroutine 每个周期通过
// ClaimTasksWSFirst（优先 WS、HTTP 兜底）跨 daemon 的全部 runtimes 认领，取代
// 旧版每 runtime 一个 HTTP 轮询器的模型。唤醒信号——WS task_available / 追平
// nudge 或 runtime 集合变化——都合并成一次 nudge，因为一次批量认领已覆盖每个
// runtime。关停时先停轮询器，再排空在途任务。
//
// 它用单次请求换来旧模型（MUL-1744）的 per-runtime 隔离；队头风险由
// ClaimTasksWSFirst 的短单请求超时（WS）/客户端超时（HTTP 兜底）约束，且服务器
// 侧批量认领是索引支持的、很快。
func (d *Daemon) pollLoop(ctx context.Context, taskWakeups <-chan taskWakeup) error {
	sem := newTaskSlotSemaphore(d.cfg.MaxConcurrentTasks)
	var taskWG sync.WaitGroup // tracks in-flight handleTask goroutines

	runtimeSetCh, unsub := d.runtimeSet.Subscribe()
	defer unsub()

	wakeup := make(chan struct{}, 1)
	nudge := func() {
		signalPollerWakeup(wakeup)
	}

	pollerCtx, pollerCancel := context.WithCancel(ctx)
	pollerDone := make(chan struct{})
	go func() {
		defer close(pollerDone)
		d.runBatchPoller(pollerCtx, ctx, sem, wakeup, &taskWG)
	}()

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("poll loop stopping, waiting for in-flight tasks", "max_wait", "30s")
			pollerCancel()
			// 等 poller 完全返回后再等 taskWG，避免一个处于
			// ClaimTasksWSFirst 与 taskWG.Add(1) 之间的 poller 在零计数时和
			// taskWG.Wait 竞争。
			<-pollerDone
			waitDone := make(chan struct{})
			go func() { taskWG.Wait(); close(waitDone) }()
			select {
			case <-waitDone:
			case <-time.After(30 * time.Second):
				d.logger.Warn("timed out waiting for in-flight tasks")
			}
			return ctx.Err()
		case <-runtimeSetCh:
			// 批量 poller 每个周期重新推导 allRuntimeIDs()；nudge 它及时接住
			// 注册/移除的 runtime。
			nudge()
		case <-taskWakeups:
			// 定向 runtime 与追平 wakeup 都会触发一次覆盖整个 runtime 集合的
			// 批量认领。
			nudge()
		}
	}
}

// runBatchPoller 是唯一的机器级认领+派发循环。每个周期它先获取当前可用的执行槽位
// （先取槽后认领，保证已认领任务绝不会在本地没有容量可跑的情况下滞留服务器侧
// dispatched、与派发超时清扫竞争），再在一个请求里向服务器为该 daemon 的全部
// runtimes 要最多这么多任务，并把返回的每个任务——由 handleTask 按 runtime 路由——
// 派发进一个槽位。
//
// pollerCtx 在关停时被取消。parentCtx 是传给 handleTask 的 daemon 根 ctx，让在途
// 任务不会因为轮询循环中途停止而被杀。
func (d *Daemon) runBatchPoller(pollerCtx, parentCtx context.Context, sem chan int, wakeup chan struct{}, taskWG *sync.WaitGroup) {
	releaseSlots := func(slots []int) {
		for _, sl := range slots {
			sem <- sl
		}
	}

	for {
		if pollerCtx.Err() != nil {
			return
		}

		runtimeIDs := d.allRuntimeIDs()
		if len(runtimeIDs) == 0 {
			if err := sleepWithContextOrWakeup(pollerCtx, d.cfg.PollInterval, wakeup); err != nil {
				return
			}
			continue
		}

		// 至少获取一个槽位（短暂阻塞），再抓走其它所有空闲槽位，让单次批量认领
		// 能把它们全填满。
		slot, acquired, woke, err := waitForTaskSlot(pollerCtx, sem, wakeup, taskSlotWaitTimeout)
		if err != nil {
			return
		}
		if !acquired {
			if woke {
				continue
			}
			if err := sleepWithContextOrWakeup(pollerCtx, capacityBackoff(d.cfg.PollInterval), wakeup); err != nil {
				return
			}
			continue
		}
		slots := append([]int{slot}, drainAvailableSlots(sem, d.cfg.MaxConcurrentTasks-1)...)

		// 自动更新屏障：更新准备滚动进程期间拒绝认领（与 tryAutoUpdate 中的复查配对）。
		if !d.tryEnterClaim() {
			releaseSlots(slots)
			if err := sleepWithContextOrWakeup(pollerCtx, d.cfg.PollInterval, wakeup); err != nil {
				return
			}
			continue
		}

		tasks, err := d.ClaimTasksWSFirst(pollerCtx, d.cfg.DaemonID, runtimeIDs, len(slots))
		if err != nil {
			d.exitClaim()
			releaseSlots(slots)
			if pollerCtx.Err() == nil {
				d.logger.Warn("batch claim failed", "error", err)
			}
			if err := sleepWithContextOrWakeup(pollerCtx, d.cfg.PollInterval, wakeup); err != nil {
				return
			}
			continue
		}

		// 把每个认领到的任务派发进一个槽位。每个派发任务在 exitClaim 之前增加
		// activeTasks，让自动更新屏障永远看不到"认领与派发之间零认领/零活跃"的窗口。
		dispatched := 0
		for i := range tasks {
			if i >= len(slots) || tasks[i] == nil {
				break
			}
			t := *tasks[i]
			slot := slots[i]
			taskTarget := t.IssueID
			if taskTarget == "" && t.ChatSessionID != "" {
				taskTarget = "chat:" + t.ChatSessionID
			}
			d.logger.Info("task received", "task", t.ID, "target", taskTarget)
			taskWG.Add(1)
			d.activeTasks.Add(1)
			if cache, ok := d.repoCache.(interface{ CancelMaintenance() }); ok {
				// 任务可以复用既有 worktree 而永不进入通常抢占仓库维护的 checkout
				// 路径。在 agent 启动前取消所有低优先级维护，让直接 Git 操作不会与它重叠。
				cache.CancelMaintenance()
			}
			go func(t Task, slot int) {
				defer taskWG.Done()
				defer d.activeTasks.Add(-1)
				defer func() {
					// 先释放本地容量再唤醒 poller。任务终态回调与本地清理此刻都已
					// 完成，因此之前被 agent 容量或每（issue, agent）串行化阻塞的
					// 后继任务可以立即被认领，而不必等 PollInterval。
					sem <- slot
					signalPollerWakeup(wakeup)
				}()
				d.handleTask(parentCtx, t, slot)
			}(t, slot)
			dispatched++
		}
		d.exitClaim()
		if dispatched < len(slots) {
			releaseSlots(slots[dispatched:])
		}

		// 若填满了每个槽位，可能还有更多工作排队——立即循环。否则等下一次
		// 唤醒 / 轮询间隔。
		if dispatched > 0 && dispatched == len(slots) {
			continue
		}
		if err := sleepWithContextOrWakeup(pollerCtx, d.cfg.PollInterval, wakeup); err != nil {
			return
		}
	}
}

// signalPollerWakeup 非阻塞地向轮询唤醒 channel 投递一次唤醒；已满则丢弃
// （poller 醒来后会重新推导状态）。
func signalPollerWakeup(wakeup chan<- struct{}) {
	select {
	case wakeup <- struct{}{}:
	default:
	}
}

// drainAvailableSlots 非阻塞地从信号量里再取最多 max 个槽位；没有空闲槽时立即返回。
func drainAvailableSlots(sem chan int, max int) []int {
	if max <= 0 {
		return nil
	}
	var slots []int
	for len(slots) < max {
		select {
		case s := <-sem:
			slots = append(slots, s)
		default:
			return slots
		}
	}
	return slots
}

// capacityBackoff 在容量耗尽后返回等待时长：轮询间隔合理则用轮询间隔，否则用
// taskSlotCapacityBackoff 作为上限。
func capacityBackoff(pollInterval time.Duration) time.Duration {
	if pollInterval <= 0 || pollInterval > taskSlotCapacityBackoff {
		return taskSlotCapacityBackoff
	}
	return pollInterval
}

// waitForTaskSlot 等待一个任务槽位：先尝试非阻塞取槽；拿不到且 wait>0 时，在
// 槽位 / 唤醒信号 / ctx 取消 / 超时四者间竞争。返回 acquired 表示拿到槽位，woke
// 表示是被唤醒打断的（调用方应立刻再试一轮）。
func waitForTaskSlot(ctx context.Context, sem chan int, wakeup <-chan struct{}, wait time.Duration) (slot int, acquired, woke bool, err error) {
	select {
	case slot = <-sem:
		return slot, true, false, nil
	case <-ctx.Done():
		return 0, false, false, ctx.Err()
	default:
	}

	if wait <= 0 {
		return 0, false, false, nil
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case slot = <-sem:
		return slot, true, false, nil
	case <-wakeup:
		return 0, false, true, nil
	case <-ctx.Done():
		return 0, false, false, ctx.Err()
	case <-timer.C:
		return 0, false, false, nil
	}
}

// newTaskSlotSemaphore 返回一个预填稳定槽位下标 [0, n) 的带缓冲 channel。收一个
// 槽位即获取，把同一槽位发回即释放。pollLoop 用它把 MULTICA_TASK_SLOT 暴露给被
// 派发的任务。
func newTaskSlotSemaphore(maxConcurrentTasks int) chan int {
	sem := make(chan int, maxConcurrentTasks)
	for i := 0; i < maxConcurrentTasks; i++ {
		sem <- i
	}
	return sem
}

// shouldInterruptAgent 依据最近一次 GetTaskStatus 调用结果，决定是否应取消正在
// 运行的 agent。做成纯函数让决策易于测试；watchTaskCancellation 里的轮询 goroutine
// 只是围绕它的 I/O。
//
// 两种条件触发取消：
//
//  1. status 处于终态——"completed"、"failed" 或 "cancelled"（isAgentTaskTerminal）。
//     服务器已终结任务：用户取消、issue 重新分配、离线清扫在断连期间把 running →
//     failed、或已完成它的重复执行。让本地 agent 继续跑纯属浪费——CompleteAgentTask
//     只接受 status == "running"，因此它最终的 CompleteTask/FailTask 回调必然失败，
//     只徒增日志噪音。复用 isAgentTaskTerminal 使该集合与 GC 对终态任务的判断
//     保持同步。
//  2. err 是带 "task not found" 的 404——任务行在 agent 运行期间被删。没有它，
//     本地 agent 会对着一个已死的任务在其整个超时窗口内继续发出工具调用。
//
// 其它所有错误（瞬时网络、5xx……）刻意不触发取消——下一个 tick 会重试，不能让
// 不稳定的链路杀掉一个在途 agent。
func shouldInterruptAgent(status string, err error) bool {
	if err != nil {
		return isTaskNotFoundError(err)
	}
	return status == "release_paused" || isAgentTaskTerminal(status)
}

// watchTaskCancellation 按给定间隔轮询服务器上任务的状态，并返回一个 channel——
// 当运行中的 agent 应被中断时该 channel 被关闭。ctx 取消时轮询 goroutine 停止，
// 因此调用方应传入围绕 agent 运行建立的 runCtx。
func (d *Daemon) watchTaskCancellation(ctx context.Context, taskID string, pollInterval time.Duration, taskLog *slog.Logger) <-chan struct{} {
	cancelled := make(chan struct{})
	// 在启动内部 goroutine 之前订阅 reconcile 广播。若 WS 重连发生在 goroutine
	// 启动与它首次 notify() 之间，信号会被丢掉；ticker 仍兜底最坏情况，但广播的
	// 意义正在于不必等那个 ticker。
	var reconcileCh <-chan struct{}
	if d.reconcile != nil {
		reconcileCh = d.reconcile.notify()
	}
	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		check := func() bool {
			status, err := d.client.GetTaskStatus(ctx, taskID)
			if status == "release_paused" {
				if p, ok := ctx.Value(releasePauseKey{}).(*atomic.Bool); ok {
					p.Store(true)
				}
			}
			if !shouldInterruptAgent(status, err) {
				return false
			}
			if err != nil {
				taskLog.Info("task gone server-side, interrupting agent", "error", err)
			} else {
				taskLog.Info("task reached terminal state server-side, interrupting agent", "status", status)
			}
			close(cancelled)
			return true
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-reconcileCh:
				// 发请求前先刷新订阅，让与 GetTaskStatus 重叠的第二次广播不丢失。
				if d.reconcile != nil {
					reconcileCh = d.reconcile.notify()
				}
				if check() {
					return
				}
			case <-ticker.C:
				if check() {
					return
				}
			}
		}
	}()
	return cancelled
}

// handleTask 是单个已认领任务的完整处理管线：校验 runtime 仍在本地、等待
// local_directory 锁、标记 env root 活跃以防 GC 回收，然后以可取消的 runCtx
// 执行 agent，并根据"轮询是否已把任务判为应中断"来决定回传结果、取消确认还是
// 终态回调。activeTasks 计数在派发点已加 1，本函数结束即代表该任务生命周期结束。
func (d *Daemon) handleTask(ctx context.Context, task Task, slot int) {
	pauseFlag := &atomic.Bool{}
	ctx = context.WithValue(ctx, releasePauseKey{}, pauseFlag)
	d.mu.Lock()
	rt, tracked := d.runtimeIndex[task.RuntimeID]
	d.mu.Unlock()
	// runtime 可能在批量认领带着它的 ID 离开、到认领任务到达这里之间掉线——一次
	// below-minimum 降级或一次漂移收敛到零，都会在认领在途时删行。把失败上报成
	// runtime_offline 正是服务器已经会重试的类型；没有它，零值 Runtime 会带着空
	// provider，任务在几百行之后以 `no agent configured for provider ""` 死掉，
	// 读起来像主机配置错了，而且不会重试。
	if !tracked {
		d.logger.Warn("claimed task targets a runtime this daemon no longer hosts; failing it back for retry",
			"task", task.ID, "runtime_id", task.RuntimeID)
		if err := d.reportTerminalTask(ctx, terminalTaskReport{
			kind:          terminalTaskReportFail,
			taskID:        task.ID,
			errorMessage:  "runtime went offline before the task started",
			failureReason: taskfailure.ReasonRuntimeOffline.String(),
		}); err != nil {
			d.logger.Error("fail task callback failed", "task", task.ID, "error", err)
		}
		return
	}
	provider := rt.Provider

	// Task-scoped logger. The task id goes in whole: it is the key every
	// other surface prints (task JSON, env-root ownership manifest, server
	// logs), so a full id is what lets a log line be joined to them. An
	// 8-char prefix cannot — task ids are UUIDv7, whose leading 32 bits are
	// a millisecond timestamp that only advances every ~65s, so concurrent
	// tasks routinely share one (#7326).
	taskLog := d.logger.With("task", task.ID)
	phaseRecorder := newTaskPhaseRecorder(taskLog.With("task_id", task.ID, "runtime_id", task.RuntimeID), time.Now)
	ctx = withTaskPhaseRecorder(ctx, phaseRecorder)
	phaseRecorder.Mark(taskPhaseClaimed)
	defer phaseRecorder.Mark(taskPhaseFinished)
	agentName := "agent"
	if task.Agent != nil {
		agentName = task.Agent.Name
	}
	if task.ChatSessionID != "" {
		taskLog.Info("picked chat task", "chat_session", task.ChatSessionID, "agent", agentName, "provider", provider)
	} else {
		taskLog.Info("picked task", "issue", task.IssueID, "agent", agentName, "provider", provider)
	}
	taskLog.Debug("task context",
		"workspace_id", task.WorkspaceID,
		"runtime_id", task.RuntimeID,
		"agent_id", task.AgentID,
		"repos", len(task.Repos),
		"project_id", task.ProjectID,
		"autopilot_run_id", task.AutopilotRunID,
		"trigger_comment_id", task.TriggerCommentID,
		"resume_session", task.PriorSessionID != "",
		"reuse_workdir", task.PriorWorkDir != "",
	)

	// 若任务指向钉在本 daemon 上、类型为 local_directory 的 project_resource，
	// 就在 runner.run 之前取该路径锁，让服务器侧状态机按 dispatched →
	// waiting_local_directory → running 推进，而不是从 running 倒退进等待态。
	// release 用 defer 保证 panic 或提前返回也总能给下一个等待者让出锁。
	//
	// StartTask 本身现在住在 runTask 里（见 issue #3999 race A），并且只在
	// execenv.Prepare/Reuse 把 env.WorkDir 落到磁盘后才触发，因此读 status==running
	// 的消费者可以解析 workdir 路径，不会与 daemon 的 os.MkdirAll 竞争。
	localRelease, abort := d.acquireLocalDirectoryLockIfNeeded(ctx, task, taskLog)
	if abort {
		if pauseFlag.Load() {
			if ackErr := d.client.AckReleasePause(ctx, task.ID, TaskResult{}); ackErr != nil {
				taskLog.Error("等待目录任务暂停确认失败", "error", ackErr)
			}
		}
		return
	}
	if localRelease != nil {
		defer localRelease()
	}

	// 在任务剩余生命周期内持有进程级 active-root 护栏，让 GC 循环永远看不到
	// "env root 既无进程内护栏、又无 .gc_meta.json"的窗口（issue #3999 race B）。
	// runTask 内部自己会装引用计数的 mark/unmark；没有这个外层护栏，内部 unmark
	// 会在 runTask 返回时触发，使目录在 reportTaskResult 与 execenv.WriteGCMeta
	// 之间只剩 72 小时孤儿 TTL 保护。markActiveEnvRoot 是引用计数的，因此 runTask
	// 安装的重复 mark 会正确地嵌套在这些之外。
	resolvedEnvRoot, resolveRootErr := execenv.ResolveRootDir(taskRootDirParams(d.cfg.WorkspacesRoot, task))
	if resolveRootErr != nil {
		taskLog.Error("resolve stable task env root", "error", resolveRootErr)
	}
	if resolvedEnvRoot != "" {
		d.markActiveEnvRoot(resolvedEnvRoot)
		defer d.unmarkActiveEnvRoot(resolvedEnvRoot)
	}
	if task.PriorWorkDir != "" {
		if priorRoot := filepath.Dir(task.PriorWorkDir); priorRoot != "" && priorRoot != resolvedEnvRoot {
			d.markActiveEnvRoot(priorRoot)
			defer d.unmarkActiveEnvRoot(priorRoot)
		}
	}

	// 建立可取消的 ctx，当服务器信号"任务应停止"时能中断正在运行的 agent——
	// 任务到达终态（completed/failed/cancelled）或任务行被删（404）都会触发。
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	// 轮询间隔是 d.cancelPollInterval（生产 5s，测试经直接字段覆盖缩短）。对零值
	// 做护栏，避免配置错误的 daemon 让 time.NewTicker panic。
	pollInterval := d.cancelPollInterval
	if pollInterval == 0 {
		pollInterval = 5 * time.Second
	}
	cancelledByPoll := d.watchTaskCancellation(runCtx, task.ID, pollInterval, taskLog)
	go func() {
		select {
		case <-cancelledByPoll:
			runCancel()
		case <-runCtx.Done():
		}
	}()

	result, err := d.runner.run(runCtx, task, provider, slot, taskLog)
	if errors.Is(err, errStartClaimRejected) {
		// The row belongs to another claim (or is terminal). A task-id-only
		// failure callback from this stale delivery could kill its new owner.
		taskLog.Info("discarding rejected start claim", "error", err)
		return
	}

	// 取消与停止路径同样记录已经发生的用量及链路；上报有独立的总时限。
	d.reportTaskAccounting(ctx, task.ID, provider, result, taskLog)

	if pauseFlag.Load() {
		if !releaseCheckpointReady(task, result) {
			// 1. 保存不了检查点就不要静默返回：把原因回传，窗口能把该任务判为 skipped
			// 继续收尾，否则这条 requested 记录会让 resume 永远 409、全平台停止认领。
			taskLog.Error("发布暂停检查点未保存，禁止确认停止")
			if ackErr := d.client.AckReleasePauseFailure(ctx, task.ID, "发布暂停检查点未保存，现场需人工核对"); ackErr != nil {
				taskLog.Error("发布暂停失败结果回传失败", "error", ackErr)
			}
			return
		}
		if ackErr := d.client.AckReleasePause(ctx, task.ID, result); ackErr != nil {
			taskLog.Error("发布暂停确认失败", "error", ackErr)
		}
		return
	}

	// 检查我们是否被轮询 goroutine 取消了。
	select {
	case <-cancelledByPoll:
		taskLog.Info("task cancelled during execution, discarding result",
			"branch_name", result.BranchName, "error", err)
		// runner.run 已返回，说明 transcript 冲刷已完成——告诉服务器可以结算它
		// 推迟的 chat 收尾（#5219）。清扫器宽限期覆盖丢失 ack 的 chat settle，但
		// 不覆盖载荷：branch 之所以能带上，是因为本检查前 worktree 已被 Finalize；
		// 而当 Finalize 改为 ABORTED 时，preserved-worktree 错误是留给 agent 工作的
		// 唯一指针——本路径上的其它一切都被丢弃。其它 run 错误保持丢弃：在已取消的
		// 运行上它们是可预期的噪音（context canceled、进程被杀），保存它们会给每个
		// 普通的中途取消盖上一个莫须有的原因。
		ack := TaskCancelAck{BranchName: result.BranchName, DurableWorkDir: result.DurableWorkDir}
		var preserved *worktreePreservedError
		if errors.As(err, &preserved) {
			ack.ErrorMessage = preserved.Error()
			ack.FailureReason = "local_directory_error"
		}
		if ackErr := d.client.AckTaskCancelled(ctx, task.ID, ack); ackErr != nil {
			taskLog.Warn("cancel ack failed; server sweeper will finalize", "error", ackErr)
		}
		return
	default:
	}

	if err != nil {
		taskLog.Error("task failed", "error", err)
		// runTask 可能在返回错误前已走到 worktree 收尾。保留 defer 附加到具名结果
		// 上的投递元数据，尤其是实际/保留的 workdir 与已投递的 branch。
		// MUL-2946：把裸错误字符串交给规范分类器，使 failure_reason 列反映失败的
		// 实际形态（provider 5xx、网络、进程崩溃……），而不是粗粒度的旧
		// "agent_error" 桶。
		if failErr := d.reportTerminalTask(ctx, terminalTaskReport{
			kind:           terminalTaskReportFail,
			taskID:         task.ID,
			errorMessage:   err.Error(),
			branchName:     result.BranchName,
			workDir:        result.WorkDir,
			durableWorkDir: result.DurableWorkDir,
			failureReason:  taskRunFailureReason(err),
		}); failErr != nil {
			taskLog.Error("fail task callback failed", "error", failErr)
		}
		return
	}

	_ = d.client.ReportProgress(ctx, task.ID, "Finishing task", 2, 2)

	// 完成前的最终检查：若服务器已把任务移到终态（completed/failed/cancelled）
	// 或整体删除该行，则跳过上报——complete/fail 回调反正也会失败。复用
	// shouldInterruptAgent，让该护栏与在途 watcher 遵循相同信号。
	if status, err := d.client.GetTaskStatus(ctx, task.ID); shouldInterruptAgent(status, err) {
		if status == "release_paused" {
			pauseFlag.Store(true)
			assignment, _ := localDirectoryAssignmentForTask(task, d.cfg.DaemonID)
			pausedEnv := &execenv.Environment{RootDir: result.EnvRoot, WorkDir: result.WorkDir, LocalDirectory: assignment != nil && !assignment.UsesWorktree()}
			if saveErr := d.preserveReleaseTask(runCtx, task, pausedEnv, result.SessionID); saveErr != nil {
				taskLog.Error("保存发布暂停结果失败", "error", saveErr)
				return
			}
			if ackErr := d.client.AckReleasePause(ctx, task.ID, result); ackErr != nil {
				taskLog.Error("发布暂停确认失败", "error", ackErr)
			}
			return
		}
		taskLog.Info("task cancelled during execution, discarding result",
			"status", status, "error", err, "branch_name", result.BranchName)
		// 与轮询取消路径同一契约：transcript 已冲刷，让服务器结算其推迟的 chat
		// 收尾，并带上已收尾的 branch 使取消的工作可被发现。这里无错误可带——只有
		// err == nil 才到达本分支。它对任何观察到的终态都会触发；服务器只对已取消
		// 行应用载荷（status CAS），因为对 completed/failed 行，complete/fail 回调
		// 才是权威通道，过期运行的迟到 ack 不得触碰它们。
		if ackErr := d.client.AckTaskCancelled(ctx, task.ID, TaskCancelAck{BranchName: result.BranchName, DurableWorkDir: result.DurableWorkDir}); ackErr != nil {
			taskLog.Warn("cancel ack failed; server sweeper will finalize", "error", ackErr)
		}
		return
	}

	d.reportTaskResult(ctx, task.ID, result, taskLog)

	// 任务结束后写 GC 元数据，让周期性 GC 循环之后能查到父记录（issue / chat
	// session / autopilot run / quick-create 时任务自身）。放在最后写：任务中途
	// 崩溃会把目录留成孤儿（由 GCOrphanTTL 清理）。
	if result.EnvRoot != "" {
		if meta, ok := gcMetaForTask(task); ok {
			// 命中了本 daemon 的 local_directory project_resource，说明 agent
			// 跑在用户自己的树里。盖这个标记，让 GC 循环绝不 RemoveAll envRoot 的
			// 兄弟 workdir（那是用户路径）或 envRoot 本身（我们希望 output/ 与
			// logs/ 为取证而留存）。
			//
			// Worktree 模式被排除：它的 workdir 是 envRoot 内可丢弃的 worktree，
			// 已由 Finalize 移除，交付物以分支形式活在用户仓库里。盖标记会让每个
			// worktree 任务都拿到永久豁免的 env root，目录会一个任务积累一个 env
			// root 永不停歇——正是该豁免为了换回用户自己的文件而要避免的成本。
			if assignment, _ := localDirectoryAssignmentForTask(task, d.cfg.DaemonID); assignment != nil && !assignment.UsesWorktree() {
				meta.LocalDirectory = true
			}
			if err := execenv.WriteGCMeta(result.EnvRoot, meta, taskLog); err != nil {
				taskLog.Warn("write gc meta failed (non-fatal)", "error", err)
			}
		}
	}
}

// worktreePreservedError 标记一种必须穿越取消路径的任务错误：它的消息点名了
// 保存 agent 未提交工作的 worktree。已取消运行上的其它所有错误都是可预期的噪音
// （context canceled、进程被杀）并保持丢弃；只有这条是真实工作的唯一指针，随
// cancel ack 一起搭车到服务器。
type worktreePreservedError struct{ err error }

func (e *worktreePreservedError) Error() string { return e.err.Error() }
func (e *worktreePreservedError) Unwrap() error { return e.err }

// environmentSetupError marks a failure to build or re-open the task's
// execution environment: everything execenv.Prepare / execenv.Reuse does on
// this host before the agent process exists — the workspace directory and its
// overlay homes, plus the per-provider local config written or validated
// inside it. Its causes are local, and not only filesystem ones: a full
// volume, a read-only or permission-denied workspaces root, a directory
// another process still holds open, an I/O error on the disk underneath, or a
// local runtime config the daemon refuses to boot past.
//
// It carries the wrapped error's message unchanged and exists purely so
// taskRunFailureReason can recognise the phase structurally. Without it the
// OS error text falls through to taskfailure.Classify, a classifier written
// to read agent and provider output, and lands in agent_error.* — today the
// catchall, historically provider_server_error, which pointed one bug report
// at an LLM vendor for hours (#7913). The task never reached an agent, so no
// reason in that namespace can be right.
type environmentSetupError struct{ err error }

func (e *environmentSetupError) Error() string { return e.err.Error() }
func (e *environmentSetupError) Unwrap() error { return e.err }

// asEnvironmentSetupFailure tags err as an execution-environment setup
// failure. The message is untouched, so the wrapper is invisible to everything
// except taskRunFailureReason.
func asEnvironmentSetupFailure(err error) error { return &environmentSetupError{err: err} }

func taskRunFailureReason(err error) string {
	if errors.Is(err, errInvalidTaskIdentity) {
		return taskfailure.ReasonInvalidTaskIdentity.String()
	}
	if errors.Is(err, errTaskPrepareTimeout) {
		return taskfailure.ReasonTimeout.String()
	}
	// 在 prepare 截止时间之后才检查：整段 prepare 预算耗尽时，runTask 已把错误
	// 折叠成 errTaskPrepareTimeout，那个分类更准确。本分支针对"某次技能包下载
	// 截止先触发、而 prepare 预算仍有富余"的情形（MUL-5370）。
	if errors.Is(err, errSkillBundleUnavailable) {
		return taskfailure.ReasonSkillBundleUnavailable.String()
	}
	// Local worktree finalization ran after the agent process was already
	// gone: its error is the daemon's own git bookkeeping, never agent or
	// provider output. Left to Classify, its text — worktree paths, short
	// SHAs, branch names — lands in agent_error.*: provider_server_error when
	// a path segment reads like a 5xx, the agent_error.unknown catchall
	// otherwise, and either one tells the user to look at the model (#9447).
	//
	// Only when it is the whole failure, though. Finalize runs on every exit
	// path and joins its error onto an earlier one; that earlier one is the
	// primary cause and keeps its own classification, judged without the
	// worktree text so a path cannot decide it either.
	var preserved *worktreePreservedError
	if errors.As(err, &preserved) {
		primary := withoutWorktreePreserved(err)
		if primary == nil {
			return "local_directory_error"
		}
		return taskRunFailureReason(primary)
	}
	// Everything else that failed while building or re-opening the execution
	// environment. Last of the structural branches: the four sentinels above
	// each name a cause the daemon already recognises on its own (task
	// identity, the prepare budget, skill downloads, the local runtime CLI)
	// and stay the better label. What is left is the rest of preparation on
	// this host — filesystem faults, and the per-provider local config
	// Prepare writes or validates — whose text Classify can only read as
	// agent output (#7913).
	var envSetupErr *environmentSetupError
	if errors.As(err, &envSetupErr) {
		return taskfailure.ReasonEnvironmentPrepareFailed.String()
	}
	return taskfailure.Classify(err.Error()).String()
}

// acquireLocalDirectoryLockIfNeeded 检查任务 project resources 里是否有钉在本
// daemon 上的 local_directory，校验路径，并取该路径互斥锁。返回释放回调（无
// local_directory 资源时 nil）与 abort=true（调用方必须在不启动任务的情况下退出，
// 此时辅助函数已向服务器上报过失败）。
//
// 辅助函数覆盖四种不同失败模式：
//
//  1. project_resource JSON 结构损坏——快速判任务失败。
//  2. 路径校验失败（缺失、非目录、无读写权限、系统黑名单）——携带用户可见原因
//     快速判任务失败。
//  3. 互斥锁被别的任务持有——调用 MarkTaskWaitingLocalDirectory，让行在阻塞等待
//     锁期间翻到 waiting_local_directory，抢到后再返回释放回调。
//  4. 阻塞等待被取消（daemon 关停、服务器侧取消）——用 ctx 错误判任务失败。
func (d *Daemon) acquireLocalDirectoryLockIfNeeded(ctx context.Context, task Task, taskLog *slog.Logger) (release func(), abort bool) {
	if len(task.ProjectResources) == 0 || d.cfg.DaemonID == "" {
		return nil, false
	}
	assignment, err := localDirectoryAssignmentForTask(task, d.cfg.DaemonID)
	if err != nil {
		taskLog.Error("local_directory: resolve resource failed", "error", err)
		if failErr := d.reportTerminalTask(ctx, terminalTaskReport{
			kind:          terminalTaskReportFail,
			taskID:        task.ID,
			errorMessage:  err.Error(),
			failureReason: "local_directory_error",
		}); failErr != nil {
			taskLog.Error("fail task after local_directory resolve error", "error", failErr)
		}
		return nil, true
	}
	if assignment == nil {
		return nil, false
	}
	taskLog = taskLog.With("local_directory", assignment.AbsPath)
	// 先查模式再查路径：本 daemon 无法兑现的模式是"用户升级即可解决"的版本错位
	// 问题，先报路径投诉会把人引去错误的方向。
	if err := assignment.ValidateExecutionMode(); err != nil {
		taskLog.Error("local_directory: unsupported execution mode", "error", err)
		if failErr := d.reportTerminalTask(ctx, terminalTaskReport{
			kind:          terminalTaskReportFail,
			taskID:        task.ID,
			errorMessage:  err.Error(),
			failureReason: "local_directory_error",
		}); failErr != nil {
			taskLog.Error("fail task after local_directory mode check", "error", failErr)
		}
		return nil, true
	}
	var pathErr error
	if !assignment.UsesWorktree() {
		pathErr = validateEnvironmentWritePath(assignment.AbsPath)
	}
	if pathErr == nil {
		pathErr = validateLocalPath(assignment.AbsPath)
	}
	if err := pathErr; err != nil {
		taskLog.Error("local_directory: path validation failed", "error", err)
		if failErr := d.reportTerminalTask(ctx, terminalTaskReport{
			kind:          terminalTaskReportFail,
			taskID:        task.ID,
			errorMessage:  err.Error(),
			failureReason: "local_directory_error",
		}); failErr != nil {
			taskLog.Error("fail task after local_directory validation error", "error", failErr)
		}
		return nil, true
	}

	// Worktree 模式本身就是"不串行化"的意义所在：每个任务在自己的 env root 里
	// 拿到仓库的独立 checkout，用户路径上没有需要保护的共享可变状态。这里跳过
	// 互斥锁，正是让同一目录上的兄弟任务能并发跑的原因。上面的路径校验仍然适用
	// ——git 需要往用户的仓库里写 worktree 注册信息。
	if assignment.UsesWorktree() {
		taskLog.Info("local_directory: worktree mode, skipping path mutex")
		return nil, false
	}

	// 对话不是第二个写者。上面的检查全都照走——模式查过、路径校验过、assignment
	// 成立，所以本任务仍把用户目录当作工作目录。只有排队被跳过，从而不会让一句
	// chat 对话干坐在一场 20 分钟的构建后面、对它毫无贡献（issue #7344）。互斥锁
	// 为什么"不欠"这个任务一个槽位，见 localDirectoryLockExempt。
	if localDirectoryLockExempt(task) {
		taskLog.Info("local_directory: chat task, skipping path mutex")
		return nil, false
	}

	// 锁被争用期间，daemon 会不带任何服务器侧信号地干堵在路径互斥锁上——每任务
	// watcher 要到锁到手之后才启动。若用户在这段等待里取消了 issue 或任务被重新
	// 分配，我们需要及时察觉，免得 daemon 槽位被一个幽灵等待者占着。取消 watcher
	// 在 onWait 里懒启动，让"无争用的快速路径"仍然零成本。
	waitCtx, waitCancel := context.WithCancel(ctx)
	defer waitCancel()
	pollInterval := d.cancelPollInterval
	if pollInterval == 0 {
		pollInterval = 5 * time.Second
	}
	var (
		watcherOnce      sync.Once
		prepareLeaseOnce sync.Once
		cancelledByPoll  <-chan struct{}
		stopPrepareLease func()
		waitCounted      bool
	)
	defer func() {
		if waitCounted {
			d.resourceWaitTasks.Add(-1)
		}
	}()
	defer func() {
		if stopPrepareLease != nil {
			stopPrepareLease()
		}
	}()

	onWait := func(holder string) {
		// LocalPathLocker 同步调用 onWait，一次 Acquire 至多一次。即便下面那次
		// 尽力而为的服务器状态更新失败，也要把这次真实的互斥锁等待计数。
		d.resourceWaitTasks.Add(1)
		waitCounted = true
		// 这段要渲染给用户，所以指名目录而非它的路径（见
		// localDirectoryAssignment.DisplayName）。绝对路径留在 daemon 自己的日志里，
		// 排查卡死锁的运维正是去那里找的。
		reason := assignment.DisplayName()
		if holder != "" {
			// 已知的粗糙边：这段短语是英文的，而客户端会把它渲染进本地化的
			// "Waiting for {reason}" 标签，zh/ja/ko 用户会看到混合语言。正经修法
			// 是把目录和持有者分成两个字段发送、由客户端本地化拼接——这个提示若
			// 会长大就值得做，为一个括号不值得。
			reason = fmt.Sprintf("%s (held by task %s)", reason, shortID(holder))
		}
		taskLog.Info("local_directory: waiting on path mutex", "holder", holder)
		if waitErr := d.client.MarkTaskWaitingLocalDirectory(ctx, task.ID, reason); waitErr != nil {
			// 非致命：即使服务器侧状态位没更新成功，我们仍要阻塞在锁上、空闲时
			// 继续。UI 只是看不到那条显式的 "waiting" 徽标。
			taskLog.Warn("local_directory: mark waiting status failed", "error", waitErr)
		}
		prepareLeaseOnce.Do(func() {
			stopPrepareLease = d.startTaskPrepareLeaseExtender(waitCtx, task, taskLog)
		})
		// 真正 park 之后才开始轮询。watchTaskCancellation 里的 shouldInterruptAgent
		// 已同时处理服务器侧终态（completed/failed/cancelled）与行被删的重新分配
		// 情形（404）——这就是等待期间需要响应的全套"这任务不该再跑了"信号。
		watcherOnce.Do(func() {
			cancelledByPoll = d.watchTaskCancellation(waitCtx, task.ID, pollInterval, taskLog)
			go func() {
				select {
				case <-cancelledByPoll:
					waitCancel()
				case <-waitCtx.Done():
				}
			}()
		})
	}
	release, err = d.localPathLocks.Acquire(waitCtx, assignment.RealPath, task.ID, onWait)
	if err != nil {
		// 等待被截断是因为服务器已终结任务（终态）或删了行，那么行本身已在终态
		// ——和 run 阶段的轮询器在 ~2104 行一样静默返回。此时发 FailTask 最好只是
		// no-op，最糟是一条让人困惑的冗余日志。
		if cancelledByPoll != nil {
			select {
			case <-cancelledByPoll:
				taskLog.Info("local_directory: wait aborted by server-side terminal state")
				return nil, true
			default:
			}
		}
		taskLog.Error("local_directory: lock acquire failed", "error", err)
		failureReason := "local_directory_error"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			failureReason = "cancelled"
		}
		if failErr := d.reportTerminalTask(ctx, terminalTaskReport{
			kind:          terminalTaskReportFail,
			taskID:        task.ID,
			errorMessage:  fmt.Sprintf("local_directory wait cancelled: %s", err.Error()),
			failureReason: failureReason,
		}); failErr != nil {
			taskLog.Error("fail task after local_directory lock cancel", "error", failErr)
		}
		return nil, true
	}
	taskLog.Info("local_directory: lock acquired")
	return release, false
}

// reportTaskResult 把任务最终处置写回服务器。
//
// Fail closed：只有显式的 "completed" 状态被当作成功上报。其它一切——"blocked"、
// "cancelled" 或任何我们忘了枚举的未来状态——都必须走 FailTask，这样从未产生过
// 真实结果的一次运行永远不会在 UI 里显示为 "Completed"（例如 provider 429 /
// 配额用尽 / runtime 崩溃）。每个路径都向前带 SessionID/WorkDir：agent 可能在卡住
// 之前建好了真实 session，我们希望下一个 chat 轮次能回到那里继续，而不是从头开始、
// "忘掉"这段对话。
func (d *Daemon) reportTaskResult(ctx context.Context, taskID string, result TaskResult, taskLog *slog.Logger) {
	switch result.Status {
	case "waiting_plan", "waiting_input":
		if result.PendingInteraction == nil {
			taskLog.Error("suspended task has no interaction")
			return
		}
		if err := d.reportTerminalTask(ctx, terminalTaskReport{
			kind: terminalTaskReportSuspend, taskID: taskID, output: result.Comment, codeDiff: result.CodeDiff,
			sessionID: result.SessionID, workDir: result.WorkDir, interaction: result.PendingInteraction,
		}); err != nil {
			taskLog.Error("suspended result remains in durable report queue", "error", err)
		}
	case "completed":
		taskLog.Info("task completed", "status", result.Status)
		err := d.reportTerminalTask(ctx, terminalTaskReport{
			kind:                  terminalTaskReportComplete,
			taskID:                taskID,
			output:                result.Comment,
			codeDiff:              result.CodeDiff,
			branchName:            result.BranchName,
			sessionID:             result.SessionID,
			workDir:               result.WorkDir,
			durableWorkDir:        result.DurableWorkDir,
			sessionRolloutMissing: result.SessionRolloutMissing,
			retiredSessionID:      result.RetiredSessionID,
		})
		if err == nil {
			return
		}
		// The original completion is already durable. Never overwrite it with a
		// synthetic failure: a temporary auth/config skew can make a 4xx recover
		// after restart just as a transport outage can make a 5xx recover, and the
		// user's successful output must remain authoritative in both cases.
		taskLog.Error("complete task callback not acknowledged; durable report remains queued", "error", err)
	default:
		failureReason := result.FailureReason
		if failureReason == "" {
			if result.Status == "cancelled" {
				// "cancelled" 是一种"假扮成 failure_reason 的刻意非失败终态"——
				// 它被留在规范分类法之外，好让 UI 能把它和真正的失败渲染得不一样。
				failureReason = "cancelled"
			} else {
				// MUL-2946：分类 agent 的评论文本，让 failure_reason 落进精细分类
				// 法（provider_auth_or_access、context_overflow、process_failure……
				// ）而不是旧 "agent_error" 粗桶。空评论落在 ReasonAgentUnknown。
				failureReason = taskfailure.Classify(result.Comment).String()
			}
		}
		taskLog.Info("task did not complete, reporting failure", "status", result.Status, "failure_reason", failureReason)
		if err := d.reportTerminalTask(ctx, terminalTaskReport{
			kind:           terminalTaskReportFail,
			taskID:         taskID,
			errorMessage:   result.Comment,
			codeDiff:       result.CodeDiff,
			sessionID:      result.SessionID,
			workDir:        result.WorkDir,
			durableWorkDir: result.DurableWorkDir,
			// Worktree 模式会在拆 worktree 前提交 agent 的遗留改动，所以失败运行通常
			// 也仍带着一条分支。这正是用户最需要它的时候：任务出了岔子，他们想看它
			// 走到哪一步。
			branchName:            result.BranchName,
			failureReason:         failureReason,
			sessionRolloutMissing: result.SessionRolloutMissing,
			retiredSessionID:      result.RetiredSessionID,
		}); err != nil {
			taskLog.Error("report failed task failed", "error", err)
		}
	}
}

// reportTerminalTask 是发送 complete/fail 回调的唯一路径。它刻意保留 context
// 值、丢弃取消与父级截止时间：daemon 关停会在 pollLoop 的 30 秒排空之前取消根
// context，但终态回调仍要使用剩下的窗口。显式超时让这份"脱离主体"的工作在正常
// 运行期间保持有界。
func (d *Daemon) reportTerminalTask(parentCtx context.Context, report terminalTaskReport) error {
	if _, err := persistedTerminalReport(report, time.Now()); err != nil {
		return err
	}
	release, ok := d.beginTerminalReportDelivery(report.taskID)
	if !ok {
		return fmt.Errorf("terminal task report for %s is already being delivered", report.taskID)
	}
	defer release()

	persisted := false
	if d.terminalReports != nil {
		if err := d.terminalReports.enqueue(report); err != nil {
			// Durability is an availability improvement, not a prerequisite for
			// the online callback. A read-only/full disk must not turn a request
			// that the server could accept right now into a stuck task.
			d.logger.Error("persist terminal task report; continuing with direct delivery",
				"task", report.taskID,
				"kind", report.kind,
				"error", err,
			)
		} else {
			persisted = true
		}
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), terminalTaskReportTimeout)
	defer cancel()
	err := d.sendTerminalTaskReport(ctx, report, defaultTerminalRetrySchedule)
	if err != nil {
		quarantined := false
		if persisted {
			item := pendingTerminalTaskReport{fileName: terminalReportFileName(report.taskID), report: report}
			quarantined = d.handleTerminalReportDeliveryError(ctx, item, err)
		}
		if persisted && !quarantined {
			d.signalTerminalReportReplay()
		}
		return err
	}
	if !persisted {
		return nil
	}
	item := pendingTerminalTaskReport{fileName: terminalReportFileName(report.taskID), report: report}
	if err := d.terminalReports.acknowledge(item); err != nil {
		d.signalTerminalReportReplay()
		return fmt.Errorf("acknowledge terminal task report: %w", err)
	}
	return nil
}

func (d *Daemon) beginTerminalReportDelivery(taskID string) (func(), bool) {
	d.terminalReportMu.Lock()
	if d.terminalReportFlight == nil {
		d.terminalReportFlight = make(map[string]struct{})
	}
	if _, exists := d.terminalReportFlight[taskID]; exists {
		d.terminalReportMu.Unlock()
		return nil, false
	}
	d.terminalReportFlight[taskID] = struct{}{}
	d.terminalReportMu.Unlock()
	return func() {
		d.terminalReportMu.Lock()
		delete(d.terminalReportFlight, taskID)
		d.terminalReportMu.Unlock()
	}, true
}

func (d *Daemon) terminalReportClock() time.Time {
	if d.terminalReportNow != nil {
		return d.terminalReportNow()
	}
	return time.Now()
}

func (d *Daemon) sendTerminalTaskReport(ctx context.Context, report terminalTaskReport, schedule []time.Duration) error {
	if d.terminalReportSend != nil {
		return d.terminalReportSend(ctx, report, schedule)
	}
	switch report.kind {
	case terminalTaskReportComplete:
		return d.client.completeTaskWithRetrySchedule(ctx, report.taskID, report.output, report.branchName, report.sessionID, report.workDir, report.sessionRolloutMissing, report.retiredSessionID, report.durableWorkDir, report.codeDiff, schedule)
	case terminalTaskReportSuspend:
		return d.client.suspendTaskWithRetrySchedule(ctx, report, schedule)
	case terminalTaskReportFail:
		return d.client.failTaskWithRetrySchedule(ctx, report.taskID, report.errorMessage, report.sessionID, report.workDir, report.branchName, report.failureReason, report.sessionRolloutMissing, report.retiredSessionID, report.durableWorkDir, report.codeDiff, schedule)
	default:
		return fmt.Errorf("unsupported terminal task report kind %d", report.kind)
	}
}

// gcMetaForTask 给一个已结束的任务分类，并产出对应类型的 GCMeta。判别顺序很
// 重要：同时带 issue_id 与 chat_session_id 的任务（理论上有、今天不会产生）应按
// chat 任务对待，因为 chat session 是更长命的父记录。
//
// ok=false 表示任务没有可识别的父记录（例如完全无 ID 的内部任务）。此时调用方
// 跳过写 meta 文件，目录回退到基于 mtime 的孤儿清理。
func gcMetaForTask(task Task) (execenv.GCMeta, bool) {
	meta := execenv.GCMeta{WorkspaceID: task.WorkspaceID, TaskID: task.ID}
	switch {
	case task.ChatSessionID != "":
		meta.Kind = execenv.GCKindChat
		meta.ChatSessionID = task.ChatSessionID
	case task.AutopilotRunID != "":
		meta.Kind = execenv.GCKindAutopilotRun
		meta.AutopilotRunID = task.AutopilotRunID
	case task.IssueID != "":
		meta.Kind = execenv.GCKindIssue
		meta.IssueID = task.IssueID
	case task.QuickCreatePrompt != "":
		// quick-create 任务在服务器跑 LinkTaskToIssue 之前就会到 WriteGCMeta，所以
		// 这里的 IssueID 永远为空。改存任务 ID，让 GC 循环通过任务 gc-check 端点向
		// 服务器询问终态。
		meta.Kind = execenv.GCKindQuickCreate
		meta.TaskID = task.ID
	default:
		return execenv.GCMeta{}, false
	}
	return meta, true
}

// taskRootDirParams 把任务关键身份组装成 execenv.RootDirParams，供解析/认领/
// 复用 env root 使用。
func taskRootDirParams(workspacesRoot string, task Task) execenv.RootDirParams {
	return execenv.RootDirParams{
		WorkspacesRoot:  workspacesRoot,
		WorkspaceID:     task.WorkspaceID,
		WorkspaceSlug:   task.WorkspaceSlug,
		TaskID:          task.ID,
		IssueIdentifier: task.IssueIdentifier,
	}
}

// runtimeDisplayNameOverrides 记录 provider key 到人面对 runtime 名称的映射，用于
// 简单标题化会读着别扭的情形。未列出的 provider 回退到把 key 首字母大写
// （claude → "Claude"，codex → "Codex"）。内置 runtime 身份（来自
// agent.BuiltinRuntimes）在 init 时被种进该 map，使它们的显示名与描述符保持同步。
var runtimeDisplayNameOverrides = map[string]string{
	"codearts":   "CodeArts",
	"dsh":        "DeepSeek Harness",
	"traecli":    "Trae",
	"grok":       "Grok",
	"qoderclicn": "Qoder CN",
	"qwen":       "Qwen Code",
	"qwenpaw":    "QwenPaw",
	"mcode":      "MiniMax Code",
	"zeroclaw":   "ZeroClaw",
}

func init() {
	// 从描述符种入内置 runtime 身份的显示名，新增 fork 时不需手工编辑这个 map。
	for _, desc := range agent.BuiltinRuntimes {
		runtimeDisplayNameOverrides[desc.ID] = desc.DisplayName
	}
}

// providerDisplayName 返回某个 provider key 的人面对 runtime 名称。
func providerDisplayName(name string) string {
	if name == "" {
		return name
	}
	if friendly, ok := runtimeDisplayNameOverrides[name]; ok {
		return friendly
	}
	parts := strings.Split(name, "-")
	for i, part := range parts {
		if part != "" {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

// providerNeedsInlineSystemPrompt 报告运行时 brief 是否必须随回合本身走
// （agent.ExecOptions.SystemPrompt）——因为该 CLI 不会读取 execenv 写进 workdir
// 的每任务上下文文件。这是唯一决定它的地方，也是其它每个 backend 看到空
// SystemPrompt 的原因。
//
// 只有在该 CLI 真的忽略其上下文文件时，把 provider 加到这里才是真修复——
// traecli 被加进来是因为它读 .trae/rules/ 而非 AGENTS.md，它的 agent 一直静默
// 缺失 workflow 段，把 issue 卡在 `todo` 里、既无评论也无报错。加一个确实会读
// 文件的 provider 只是在每个回合里重复 brief。
//
// 已确认会加载上下文文件、因此刻意不在此列。MUL-5392 用 canary 实测过每个
// provider 的真实启动路径与"不内联投递"行为：claude 2.1.220（CLAUDE.md）、
// codex 0.144.6 驱动 app-server（AGENTS.md）、opencode 1.17.7（AGENTS.md）、
// pi 0.67.2（AGENTS.md）、hermes 0.18.2 over ACP（AGENTS.md）。MCode 0.1.2 也
// 按原生 runtime 契约加载 AGENTS.md。kiro 更早由 kiro-cli 2.13.0 ACP 冒烟确认——
// 见调用点。仍未探测：grok、qoder、codebuddy。
func providerNeedsInlineSystemPrompt(provider string) bool {
	switch provider {
	case "kimi", "traecli", "qwenpaw":
		return true
	default:
		return false
	}
}

// gateResumeToReachableSession 除非本次运行确实能到达 prior session 所在的 store，
// 否则清除任务的 prior session，并报告该条件是否成立。多数 CLI 后端把会话 store
// 绑到 cwd（Claude Code 在 ~/.claude/projects/<encoded-cwd>/ 下查 session），因此
// 来自不同 workdir 的 session id 永远解析不到：CLI 一秒内退出、还没干活就失败——
// 而且是永久性的，因为失败运行不记录任何 session，下一次认领又会喂同一根过期的
// 指针。凡是 prior workdir 已不存在（issue done 后被 GC、daemon 重装、手工清理）
// 且 execenv.Reuse 回退到全新 Prepare 时（GitHub #3854），本函数都会触发。
//
// Pi 与 OMP 是例外。它们的不透明 session id 是 ~/.multica/pi-sessions 下的绝对
// JSONL 路径，backend 直接把它传给 --session。仅任务 workdir 变化时 transcript
// 仍可恢复，因此把恢复绑到 workdir 复用会丢弃健康的对话历史，逼模型用
// `multica chat history` 重建它。
//
// 匹配的 workdir 本身还不够。Hermes 把 session 绑到 HERMES_HOME——envRoot 下的
// 每任务 overlay——而不是 cwd，两者恰好在 local_directory 流程里分家：那里禁用
// 复用（shouldReusePriorWorkdir），每个任务都建全新 overlay（state.db 为空），而
// envWorkDir 保持为用户自己的目录、因此仍等于 PriorWorkDir。护栏读成 "已复用" 并
// 转发了不可能解析的 session id，Hermes 对不可解析的 resume 的回答是静默重启
// （GH #6806）。sessionHomeReachable 是 provider 自己回答"这里还能找到 prior
// session 吗？"——对 Hermes 即会话 store 是否被挂载（execenv.Environment
// HermesSessionStore）——返回 false 时会像 workdir 不匹配那样丢弃 resume。
// sameExistingDir 报告两个路径是否指向同一现存目录。任一个无法 stat 即返回
// false，这对以 cwd 为键的 provider 是安全答案：prior workdir 不存在就意味着
// 没有可恢复的东西。
func sameExistingDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// codexWorktreeResumeEligible permits a changed cwd only when the prepared
// local_directory worktree continues this conversation's branch and Codex has
// a stable conversation-scoped session store. In-place and managed tasks keep
// the existing same-directory rule.
func codexWorktreeResumeEligible(env *execenv.Environment, store string) bool {
	return env != nil && env.LocalWorktree != nil && env.LocalWorktree.Continued && env.CodexHome != "" && store != ""
}

func gateResumeToReachableSession(task *Task, taskCtx *execenv.TaskContextForEnv, provider, envWorkDir string, sessionHomeReachable, codexWorktreeResumeEligible bool, taskLog *slog.Logger) bool {
	var reachable bool
	if providerUsesPiSessionFile(provider) {
		reachable = piSessionFilePresent(task.PriorSessionID)
	} else if provider == "codex" && codexWorktreeResumeEligible {
		reachable = sessionHomeReachable
	} else {
		// 比较目录而不是字面拼写。复用跑在它校验并锁定的规范路径上，那未必与
		// 服务器发来的 PriorWorkDir 逐字符一致——一个符号链接的 workspaces root
		// 就足以让两者不同。字符串比较会在这台机器上的每个后续任务里静默丢掉
		// prior session。
		reachable = task.PriorWorkDir != "" && sameExistingDir(envWorkDir, task.PriorWorkDir) && sessionHomeReachable
	}
	if !reachable && task.PriorSessionID != "" {
		taskLog.Info("dropping prior session: session store not reachable from this run",
			"provider", provider,
			"session_id", task.PriorSessionID,
			"prior_workdir", task.PriorWorkDir,
			"workdir", envWorkDir,
			"session_home_reachable", sessionHomeReachable,
		)
		task.PriorSessionID = ""
		taskCtx.PriorSessionResumed = false
		// 用户期望这次运行继续先前的对话；把丢失摆到台面上，而不是静默重启
		// （MUL-4424）。要同时设到两个载体上：通知由 BuildPrompt 从 `task` 渲染
		// （MUL-5377 把它移出了 brief），而 taskCtx 仍驱动 execenv。
		taskCtx.PriorSessionResumeUnavailable = true
		task.PriorSessionResumeUnavailable = true
	}
	return reachable
}

// providerUsesPiSessionFile 判断 provider 是否为 pi 协议族（pi 或协议族派生的
// 内置身份）——它们用绝对 session 文件而非 cwd 键控的 store。
func providerUsesPiSessionFile(provider string) bool {
	if provider == "pi" {
		return true
	}
	desc, ok := agent.BuiltinRuntimeByID(provider)
	return ok && desc.ProtocolFamily == "pi"
}

// piSessionFilePresent 证明存在可恢复的持久化历史。它不断言 transcript 空闲：
// Pi backend 在整个子进程生命周期里持有独占锁，并把忙碌的 resume 报为被拒，让
// runTask 回退到既有的"一次性新会话"路径。
func piSessionFilePresent(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	info, err := os.Stat(sessionID)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// sessionHomeReachable 报告本会话早前某任务记录的 session，能否从本次运行的
// 环境里被找到——用于把 session 键在 cwd 之外的 provider。
//
// 今天只有 Hermes 是这样：它的 transcript 在 `<HERMES_HOME>/state.db`，即 envRoot
// 下的每任务 overlay。把 session id 转发进一个不含它的数据库，正是导致每轮对话
// 都从零重启的原因（GH #6806），因此问题必须针对数据库而非管线本身：
//
//   - 挂载了会话 store 时，答案取决于该 store 是否真的持有 transcript。挂载到空
//     store 是首轮的正常形态——也是 GC 在轮次之间回收 store、Hermes profile 被
//     切换、或旧 overlay 留下悬空链接时的形态。把 "已挂载" 读成 "可恢复" 会把死
//     id 转发进上述每种情形。
//   - 没有 store 时，transcript 是 overlay 自己的任务本地文件，它恰好在本次运行
//     复用了先前任务的 env root 时存活。
//
// 其它每个 provider 都以 cwd 为键，或在 gateResumeToReachableSession 里自行处理
// 独立寻址的 store，因此本谓词对它们恒返回 true。
func sessionHomeReachable(provider string, env *execenv.Environment, envReused bool) bool {
	if provider != "hermes" {
		return true
	}
	if env.HermesSessionStore != "" {
		return env.HermesSessionHistoryPresent
	}
	return envReused
}

// shouldReusePriorWorkdir 在不破坏 local_directory 锁与跨 agent 隔离不变式的前提
// 下，允许受管 follow-up 不必开新 provider session。每个受管的 issue 或 chat 任务
// 只能复用"解析到两段式受管根形状、带同一 workspace/scope/agent 的 Prepare 期
// 受管环境来源、并带匹配 daemon 任务上下文标记"的目录。其它任务类型没有可用来
// 证明归属的持久 scope，因此都从全新开始。
//
// 复用资格刻意键控 .managed_env.json（由 execenv.Prepare 写入），而不是
// .gc_meta.json（只在任务到达终态后写入）。服务器的 task-complete 处理器会在
// 先前任务 daemon 处理器写完 GC 文件之前对账 follow-up 并唤醒 runtime，因此后继
// 任务可以在那个窗口内被认领；键控终态文件会竞争并丢掉 session（MUL-4886）。本
// 函数读的两个证明——env-root 来源与 workdir 任务上下文标记——都在 Prepare 期
// 写入，因此都不依赖完成顺序。
func shouldReusePriorWorkdir(task Task, localAssignment *localDirectoryAssignment, workspacesRoot string) (string, bool) {
	if task.PriorWorkDir == "" || localAssignment != nil {
		return "", false
	}

	// util.ResolveSymlinks, not filepath.EvalSymlinks: on Windows the latter
	// cannot pass through a directory junction, so a junctioned workspaces
	// root silently declined every reuse and each follow-up lost its session
	// (#8946).
	root, err := util.ResolveSymlinks(workspacesRoot)
	if err != nil {
		return "", false
	}
	workdir, err := util.ResolveSymlinks(task.PriorWorkDir)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(workdir)
	if err != nil || !info.IsDir() {
		return "", false
	}
	rel, err := filepath.Rel(root, workdir)
	if err != nil || !filepath.IsLocal(rel) {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] != "workdir" {
		return "", false
	}
	if task.AgentID == "" || (task.IssueID == "" && task.ChatSessionID == "") {
		return "", false
	}
	// managed-env 溯源只写给"非 local 的可复用 env"，所以它的存在（加上
	// workspace/scope/agent 匹配）即证明这是安全的 daemon 受管复用目标，而不是
	// 残留的 local_directory 路径。
	prov, err := execenv.ReadManagedEnvProvenance(filepath.Dir(workdir))
	if err != nil || prov.ManagedBy != execenv.ManagedEnvProvenanceManagedBy ||
		prov.WorkspaceID != task.WorkspaceID ||
		prov.AgentID != task.AgentID {
		return "", false
	}

	data, err := os.ReadFile(filepath.Join(workdir, execenv.TaskContextMarkerRelPath))
	if err != nil {
		return "", false
	}
	var marker struct {
		ManagedBy     string `json:"managed_by"`
		AgentID       string `json:"agent_id"`
		IssueID       string `json:"issue_id"`
		ChatSessionID string `json:"chat_session_id"`
	}
	if json.Unmarshal(data, &marker) != nil {
		return "", false
	}
	if marker.ManagedBy != execenv.TaskContextMarkerManagedBy || marker.AgentID != task.AgentID {
		return "", false
	}
	if task.IssueID != "" {
		if prov.IssueID != task.IssueID || marker.IssueID != task.IssueID {
			return "", false
		}
		return workdir, true
	}
	if prov.ChatSessionID != task.ChatSessionID || marker.ChatSessionID != task.ChatSessionID {
		return "", false
	}
	return workdir, true
}

// gateCodexResumeToRolloutPresence 当 prior Codex session 的 rollout 实际不存在于
// 任务的 CODEX_HOME sessions 时，丢弃该 prior session。被复用的 workdir 会保留
// PriorSessionID（gateResumeToReachableSession），但 Codex session 隔离（MUL-4424）
// 意味着 rollout 可能缺失：迁移后的旧 home 找不到它，或 local_directory 任务被
// 裁剪了共享历史。Codex 会静默地重新开线程，因此我们从 backend（PriorSessionID）
// 与 brief（PriorSessionResumed）两处都清除 resume 声明，而不是假装对话仍在继续。
// 对非 Codex provider 或没有可恢复内容时为空操作。
func gateCodexResumeToRolloutPresence(task *Task, taskCtx *execenv.TaskContextForEnv, provider, codexHome string, taskLog *slog.Logger) {
	if provider != "codex" || task.PriorSessionID == "" || codexHome == "" {
		return
	}
	if execenv.CodexResumeRolloutPresent(codexHome, task.PriorSessionID) {
		return
	}
	taskLog.Warn("dropping prior codex session: rollout not present in task CODEX_HOME; starting a fresh thread",
		"session_id", task.PriorSessionID, "codex_home", codexHome)
	task.PriorSessionID = ""
	taskCtx.PriorSessionResumed = false
	// 用户期望这次运行继续先前的对话；把丢失摆到台面上，而不是静默重启（MUL-4424）。
	// 要同时设到两个载体上：通知由 BuildPrompt 从 `task` 渲染（MUL-5377 把它移出了
	// brief），而 taskCtx 仍驱动 execenv。
	taskCtx.PriorSessionResumeUnavailable = true
	task.PriorSessionResumeUnavailable = true
}

const (
	// codexRolloutFlushWait 限制 daemon 在把某 session 判定为不可恢复之前，等待
	// Codex 把 session 的 rollout 写进每任务 store 的时长。Codex 在线程运行期间流式
	// 写 rollout，因此常见情形下文件已存在、检查立即返回；只有在 rollout 永远不来
	// 的罕见路径上（早期崩溃/报错）等待才增加延迟——而那正是我们绝不可持久化的指针。
	codexRolloutFlushWait    = 2 * time.Second
	codexRolloutPollInterval = 50 * time.Millisecond
)

// codexSessionResumable 报告某 Codex session 的 rollout 是否存在于任务的每 issue
// session store，这样 daemon 绝不记录一个"下次 follow-up 只会发现不可恢复"、随后
// 又要经 gateCodexResumeToRolloutPresence 丢弃的 session 指针——从而丢失对话
// （MUL-5305）。它在"写"的时刻镜像 daemon 在"恢复"时刻已应用的在场门槛：只有
// rollout 已在磁盘的 session 才值得持久化为可恢复指针。
//
// 非 Codex provider 没有可查的 rollout store（codexHome == ""），恒按可恢复对待，
// 保持既有行为。Codex 会获得一小段有界窗口来结束冲刷，然后再放弃，因此普通冲刷
// 延迟不会被误判成 rollout 丢失。
func codexSessionResumable(codexHome, sessionID string, wait time.Duration) bool {
	if codexHome == "" || sessionID == "" {
		return true
	}
	deadline := time.Now().Add(wait)
	for {
		if execenv.CodexResumeRolloutPresent(codexHome, sessionID) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(codexRolloutPollInterval)
	}
}

// waitCodexRolloutPresent 阻塞直到 sessionID 的 rollout 出现在 codexHome 的
// 每 issue store，或 ctx 结束，返回它是否变得在场。它支撑"在途中钉住"：Codex 在
// 单个 task_started 状态上揭示 session id，因此固定的一次性检查会漏掉稍后才冲刷
// 出来的 rollout；在整个运行期间（以 ctx 为界）轮询既能抓到它，又永不超过任务
// 的生命周期。非 Codex provider（codexHome == ""）没有要验证的 rollout，立即返回
// （MUL-5305）。
func waitCodexRolloutPresent(ctx context.Context, codexHome, sessionID string) bool {
	if codexHome == "" || sessionID == "" {
		return true
	}
	if execenv.CodexResumeRolloutPresent(codexHome, sessionID) {
		return true
	}
	ticker := time.NewTicker(codexRolloutPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// rollout 可能恰好在运行结束那一刻落地——再查最后一次。
			return execenv.CodexResumeRolloutPresent(codexHome, sessionID)
		case <-ticker.C:
			if execenv.CodexResumeRolloutPresent(codexHome, sessionID) {
				return true
			}
		}
	}
}

// ensureTaskSkillBundles 把任务 agent 引用的技能包全部解析到磁盘缓存并组装成
// task.Agent.Skills。先在缓存里查，miss 的包逐个单独下载并立即落缓存，使一次
// 不完整的派发也会给下一个任务留下已缓存的技能。
func (d *Daemon) ensureTaskSkillBundles(ctx context.Context, task *Task) error {
	if task == nil || task.Agent == nil || len(task.Agent.SkillRefs) == 0 {
		return nil
	}
	resolved := make(map[string]SkillData, len(task.Agent.SkillRefs))
	misses := make([]SkillRefData, 0)
	for _, ref := range task.Agent.SkillRefs {
		ref := ref
		var bundle SkillData
		if err := d.skillCache.WithRefLock(task.WorkspaceID, ref, func() error {
			if cached, ok := d.skillCache.Load(task.WorkspaceID, ref); ok {
				bundle = cached
				return nil
			}
			misses = append(misses, ref)
			return nil
		}); err != nil {
			return fmt.Errorf("load skill bundle cache: %w", err)
		}
		if bundle.ID != "" {
			resolved[skillRefKey(ref.Source, ref.ID)] = bundle
		}
	}

	// 每个 miss 的包用独立请求解析，到达即写缓存。下载在不稳定链路上是最慢环节，
	// 以前把整套包放在一个原子 body 读取里，单次超时就会丢掉全部进度、缓存永远无法
	// 收敛——每次派发都重新下载一切并再次超时。按技能分开后，每个下载落在自己的、
	// 按大小缩放的截止时间内并被独立持久化，因此即使最终失败的派发也把已取到的
	// 技能留给下一次。（GitHub #4505 / MUL-3650）
	for _, ref := range misses {
		started := time.Now()
		bundle, stats, err := d.resolveSkillBundle(ctx, task, ref)
		if err != nil {
			if isSkillBundleTransferFailure(err) {
				// 只有传输失败和不完整的 2xx body 才做"网络诊断"。HTTP 错误响应与
				// 畸形但完整的载荷保留服务器语义，而不是被重新贴成连接性问题
				// （GitHub #7386）。
				return fmt.Errorf("%w: %s: %w",
					errSkillBundleUnavailable,
					describeSkillBundleFailure(ref, stats, time.Since(started)),
					err)
			}
			return fmt.Errorf("%w: %w", errSkillBundleUnavailable, err)
		}
		resolved[skillRefKey(bundle.Source, bundle.ID)] = bundle
	}

	skills := make([]SkillData, 0, len(task.Agent.SkillRefs))
	for _, ref := range task.Agent.SkillRefs {
		bundle, ok := resolved[skillRefKey(ref.Source, ref.ID)]
		if !ok {
			return fmt.Errorf("skill bundle missing after resolve: skill_id=%s source=%s hash=%s", ref.ID, ref.Source, ref.Hash)
		}
		skills = append(skills, bundle)
	}
	task.Agent.Skills = skills
	return nil
}

// resolveSkillBundle 下载一个技能包并在返回前写入磁盘缓存。请求跑在自己的截止
// 时间内，按包声明的大小缩放，而不是用 daemon 固定 30s 控制面超时——大包在慢链
// 路上给足空间完成，而不是 body 中途被掐断。成功即写缓存，这让解析能跨多次派发
// 收敛。（GitHub #4505 / MUL-3650）
func (d *Daemon) resolveSkillBundle(ctx context.Context, task *Task, ref SkillRefData) (SkillData, TransferStats, error) {
	reqCtx, cancel := context.WithTimeout(ctx, skillBundleResolveTimeout(ref.SizeBytes))
	defer cancel()

	bundle, stats, err := d.client.ResolveSkillBundle(reqCtx, task.RuntimeID, task.ID, ref)
	if err != nil {
		return SkillData{}, stats, err
	}
	// resolve 端点服务 agent 的*当前*包与哈希，可能因技能在认领与准备之间被编辑
	// 而与认领时的 ref 不同（见 ResolveTaskSkillBundles）。因此只确认服务器返回了
	// 我们要的技能（source/id），再针对从自身派生的 ref 校验包的自我一致性——钉死
	// 在可能过期的请求哈希上会拒绝一次合法的更新。
	if bundle.Source != ref.Source || bundle.ID != ref.ID {
		return SkillData{}, stats, fmt.Errorf("resolve skill bundle returned wrong skill: requested source=%s id=%s, got source=%s id=%s", ref.Source, ref.ID, bundle.Source, bundle.ID)
	}
	bundleRef := skillRefFromBundle(bundle)
	validationRef := bundleRef
	if ref.Source == skillbundle.SourcePlugin {
		validationRef = ref
	}
	if !validateSkillBundle(validationRef, bundle) {
		return SkillData{}, stats, fmt.Errorf("resolve skill bundle returned invalid bundle: skill_id=%s source=%s hash=%s", bundle.ID, bundle.Source, bundle.Hash)
	}
	if err := d.skillCache.WithRefLock(task.WorkspaceID, validationRef, func() error {
		return d.skillCache.Store(task.WorkspaceID, bundle)
	}); err != nil {
		if d.logger != nil {
			d.logger.Warn("skill bundle cache store failed; continuing with downloaded bundle",
				"workspace_id", task.WorkspaceID,
				"skill_id", bundle.ID,
				"source", bundle.Source,
				"hash", bundle.Hash,
				"error", err,
			)
		}
	}
	return bundle, stats, nil
}

// isSkillBundleTransferFailure 识别那些"字节级网络诊断有意义"的错误。requestError
// 是显式服务器响应；畸形但完整的 JSON 与包校验错误是服务器载荷问题。两者都不该被
// 描述成慢或死的网络链路。特别是 json.Decoder 在其源以干净 EOF 结束后也能合成
// io.ErrUnexpectedEOF，因此仅凭 decoder 错误不足以证明是传输问题。
func isSkillBundleTransferFailure(err error) bool {
	var reqErr *requestError
	if errors.As(err, &reqErr) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// describeSkillBundleFailure 渲染一次失败技能包下载的诊断半边：在取什么、收到
// 多少个 HTTP 响应体字节、单独声明的解码内容大小，以及这台主机是否配置了代理。
//
// 三个事实回答旧文本诱发的三个错误转向。"下载技能 X 时网络错误"而不是
// "技能 X 不可用"会让读者不再怪罪技能。响应字节数把死链路与部分响应区分开，同时
// 不假装 JSON 线上字节与解码后的技能内容字节构成同一个进度比。代理提示接住 GitHub
// #7386 背后的情形：跨边链路里未继承代理的 daemon 一个字节都发不出去，同一主机
// 经本地代理却能成功。
func describeSkillBundleFailure(ref SkillRefData, stats TransferStats, elapsed time.Duration) string {
	elapsed = elapsed.Round(time.Millisecond)
	var transfer string
	switch {
	case !stats.ResponseStarted:
		transfer = fmt.Sprintf("no successful response from server after %s overall", elapsed)
	case stats.BytesRead == 0:
		transfer = fmt.Sprintf("a successful response started but delivered no body bytes before failing after %s overall", elapsed)
	default:
		// BytesRead 是单次尝试的高水位，而 elapsed 覆盖含重试与退避的整个逻辑
		// 调用。两者都讲清楚，而不是拿错配的量去推一个速率。
		transfer = fmt.Sprintf("received up to %s of response body data in one attempt; failed after %s overall",
			formatBytes(stats.BytesRead), elapsed)
	}
	return fmt.Sprintf("network error downloading skill %q (id=%s): %s; declared skill content size %s; %s; the skill content is not at fault",
		ref.Name, ref.ID, transfer, formatBytes(ref.SizeBytes), proxyEnvSummary())
}

// proxyEnvSummary 报告 daemon 是否继承了某个代理设置。它只点名变量、绝不给出值：
// 代理 URL 常内嵌凭据，而这段字符串会落进整个工作区都能读到的任务失败文本。
//
// Go 的 transport 只认这几个变量——不读 Windows 系统代理——而且只在进程启动时读，
// 因此一个在变量设置之前就运行的 daemon 仍会显示无代理（GitHub #7386）。
func proxyEnvSummary() string {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return fmt.Sprintf("proxy configured (%s)", key)
		}
	}
	return "no proxy configured (HTTPS_PROXY unset)"
}

// formatBytes 把字节数渲染成人类读失败消息的格式。
func formatBytes(n int64) string {
	switch {
	case n < 0:
		return "unknown size"
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.0f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.2f MB", float64(n)/(1024*1024))
	}
}

const (
	// skillBundleResolveMinTimeout 给按技能解析截止时间设下限，让极小包也容忍连接
	// 建立与往返延迟。
	skillBundleResolveMinTimeout = 30 * time.Second
	// skillBundleResolveMaxTimeout 封顶，使卡死的下载无法无限钉住 prepare 中的任务。
	skillBundleResolveMaxTimeout = 5 * time.Minute
	// skillBundleResolveMinThroughput 是用于把截止时间缩放到包大小的悲观吞吐下限
	// （字节/秒）——刻意设低，覆盖慢且抖动的链路而非理想带宽。
	skillBundleResolveMinThroughput = 50 * 1024
)

// skillBundleResolveTimeout 返回下载给定大小包的截止预算：至少
// skillBundleResolveMinTimeout，按 skillBundleResolveMinThroughput 向上缩放，
// 封顶于 skillBundleResolveMaxTimeout。
func skillBundleResolveTimeout(sizeBytes int64) time.Duration {
	if sizeBytes <= 0 {
		return skillBundleResolveMinTimeout
	}
	scaled := time.Duration(sizeBytes/skillBundleResolveMinThroughput) * time.Second
	if scaled < skillBundleResolveMinTimeout {
		return skillBundleResolveMinTimeout
	}
	if scaled > skillBundleResolveMaxTimeout {
		return skillBundleResolveMaxTimeout
	}
	return scaled
}

// startTaskPrepareLeaseExtender 在任务准备阶段（可能很长：仓库 checkout、技能下载、
// env 搭建）周期性地向服务器续期"任务准备租约"，防止服务器把仍在本地准备的任务当
// 成卡死而超时回收。返回的 stop 函数幂等：调用即取消续期 goroutine 并等它退出。
func (d *Daemon) startTaskPrepareLeaseExtender(ctx context.Context, task Task, taskLog *slog.Logger) func() {
	leaseCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(taskPrepareLeaseRefresh)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				reqCtx, reqCancel := context.WithTimeout(leaseCtx, taskPrepareLeaseTimeout)
				err := d.client.ExtendTaskPrepareLease(reqCtx, task.RuntimeID, task.ID)
				reqCancel()
				if err != nil {
					taskLog.Warn("extend task prepare lease failed", "error", err)
				}
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

// lockReusablePriorEnvRoot 决定本任务是否可以在先前任务的 env root 里继续，若是
// 则对其加排它锁。
//
// 顺序很重要，这正是本函数的意义所在。锁要在目录里 WRITE 一个文件，因此必须先
// 证明资格：shouldReusePriorWorkdir 解析符号链接、检查 {workspace}/{task}/workdir
// 形状并验证受管来源，只有它返回的规范路径才会被打开。在此之前对
// filepath.Dir(task.PriorWorkDir) 加锁，会把 .task_lock 丢进该路径当时恰好指向的
// 任何地方——workspaces root 之外的符号链接目标，或用户自己的 local_directory。
//
// 锁下的复查封闭了"证明资格"与"据此行动"之间的间隙。拒绝永远是安全的：调用方
// 回退到全新 Prepare，代价是会话连续性而非正确性。
//
// 错误返回值是唯一一种 NOT "拒绝并继续"的结果：只有"在等待先前 env root 期间
// 运行被取消"时它才非 nil，且携带 context 的原因，让调用方能结束任务，而不是为
// 一份已无人等待的工作准备环境。
func (d *Daemon) lockReusablePriorEnvRoot(ctx context.Context, task Task, localAssignment *localDirectoryAssignment, heldRoot string) (*execenv.EnvRootClaim, string, os.FileInfo, bool, error) {
	// 先钉住 workspaces root，再验证任何东西。若在名字验证通过之后再打开它，
	// 会重新解析那个名字：把 root 改到一边、留一个指向相似树的符号链接，os.Root
	// 会忠实地钉住替换品。Root 保证"无法逃出它打开的那棵树"——而不是"它打开的是
	// 你意想中的那棵树"。先持有句柄让这种顺序不可能发生。
	wsRoot, err := os.OpenRoot(d.cfg.WorkspacesRoot)
	if err != nil {
		return nil, "", nil, false, nil
	}
	defer wsRoot.Close()

	workDir, ok := shouldReusePriorWorkdir(task, localAssignment, d.cfg.WorkspacesRoot)
	if !ok {
		return nil, "", nil, false, nil
	}
	priorRoot := filepath.Dir(workDir)
	// workDir came back through util.ResolveSymlinks, so the root it is
	// measured against has to be resolved the same way — otherwise a symlinked
	// workspaces root (macOS /tmp -> /private/tmp, a home on a linked volume,
	// a Windows junction to another drive) makes the two look unrelated and
	// every reuse is refused.
	canonicalWorkspacesRoot, err := util.ResolveSymlinks(d.cfg.WorkspacesRoot)
	if err != nil {
		return nil, "", nil, false, nil
	}
	rel, err := filepath.Rel(canonicalWorkspacesRoot, priorRoot)
	if err != nil || !filepath.IsLocal(rel) {
		return nil, "", nil, false, nil
	}
	// 已由本次运行自己的认领覆盖（任务被重新派发到它自己的目录上）；再对它取
	// 第二把锁只会跟自己死锁。
	if priorRoot == heldRoot {
		return nil, workDir, nil, true, nil
	}

	// 钉住刚通过校验的那个目录的身份。之后的每一步都对照"这个"，而不是对照
	// "这个名字下次解析到什么"：路径字符串区分不了"我校验过的目录"与"现在顶着
	// 这个名字的另一目录"。
	validatedInfo, err := os.Stat(priorRoot)
	if err != nil {
		return nil, "", nil, false, nil
	}

	// TOCTOU 回归的确定性接缝：测试在"证明资格"与"取锁"之间、在这里换掉已校验
	// 的目录。
	if reuseLockTestHook != nil {
		reuseLockTestHook()
	}

	lockStartedAt := time.Now()
	claim, lockedInfo, err := d.lockEnvRootForReuseWaitingOutTheBusyWindow(ctx, wsRoot, rel, priorRoot, task)
	switch {
	case errors.Is(err, errPriorEnvRootWaitAborted):
		// 运行已经结束。拒绝复用会把调用方推去全新 Prepare——那正是这里绝不能
		// 发生的事。
		return nil, "", nil, false, context.Cause(ctx)
	case errors.Is(err, execenv.ErrEnvRootBusy):
		d.logger.Info("prior workdir is still in use after waiting for it; starting a fresh environment",
			"task", task.ID, "prior_root", filepath.Base(priorRoot),
			"waited", time.Since(lockStartedAt).Round(time.Millisecond), "budget", d.envRootBusyWait)
		return nil, "", nil, false, nil
	case err != nil:
		d.logger.Warn("could not lock prior workdir; starting a fresh environment",
			"task", task.ID, "error", err)
		return nil, "", nil, false, nil
	case claim == nil:
		return nil, "", nil, false, nil
	}
	// 锁必须落在校验批准的那个目录上。
	if !os.SameFile(validatedInfo, lockedInfo) {
		d.logger.Info("prior workdir changed identity before it could be claimed; starting a fresh environment",
			"task", task.ID)
		claim.Release()
		return nil, "", nil, false, nil
	}

	// 持锁状态下重新校验，并要求答案是"同一个"目录。单看规范路径相等还不够：
	// 树可能被重排，让另一个同样合法、却不同的 env root 现在顶着那个名字——结果
	// 我们锁着一个目录、却把另一个交给 Reuse。os.SameFile 比较身份，不比拼写。
	recheckedWorkDir, stillOK := shouldReusePriorWorkdir(task, localAssignment, d.cfg.WorkspacesRoot)
	if !stillOK || recheckedWorkDir != workDir {
		claim.Release()
		return nil, "", nil, false, nil
	}
	// ……而我们要交给 Reuse 的目录必须就是那同一个，让"被锁的对象"与"被用的
	// 对象"不可能分家。
	currentInfo, err := os.Stat(filepath.Dir(recheckedWorkDir))
	if err != nil || !os.SameFile(lockedInfo, currentInfo) {
		d.logger.Info("prior workdir changed identity while being claimed; starting a fresh environment",
			"task", task.ID)
		claim.Release()
		return nil, "", nil, false, nil
	}
	return claim, workDir, lockedInfo, true, nil
}

// envRootBusyRetryInterval 是下面的等待循环重试锁的间隔。它覆盖的窗口只有几秒长，
// 因此只要相对窗口足够小即可，不必很紧。
const envRootBusyRetryInterval = 250 * time.Millisecond

// errPriorEnvRootWaitAborted 标记等待因运行自身结束（而非锁被放开）而终止。它
// 包裹 ctx 的原因，让任务能用真实原因失败。
var errPriorEnvRootWaitAborted = errors.New("waiting for the prior env root was aborted")

// lockEnvRootForReuseWaitingOutTheBusyWindow 取复用锁，并等待本 (issue, agent) 的
// 上一次运行还没放开的锁。
//
// 健康系统里这里的忙锁只有一个成因：服务器只在没有其它任务为同一 (issue, agent)
// 处于 dispatched/running 时（ClaimAgentTask 的串行化）才把任务发给 daemon，因此本
// 任务诞生时，就服务器所见而言它的前任已经结束。进程却不是：它要从自己的轮询
// tick 才知道被取消、还要几秒才退出，并且在退出前一直持有 .task_lock。这就是
// 整个间隙——一个机器本地的事实，在这里就能看到。
//
// 立刻放弃会丢掉 workdir，连带住在里面的 provider session：agent 重启时对用户
// 正编辑到一半的对话毫无记忆（MUL-6880）。等待只花一个任务槽的几秒，远小于拒绝
// 后被迫做的全新 Prepare（含仓库 checkout）。
//
// 等待有界，超时后恰好回到旧行为：真正卡死的前任、或共享 workspaces root 上被
// 别的东西持有的锁，最终都会落在全新环境里，而不是一个卡住的任务。
func (d *Daemon) lockEnvRootForReuseWaitingOutTheBusyWindow(
	ctx context.Context,
	wsRoot *os.Root,
	rel, priorRoot string,
	task Task,
) (*execenv.EnvRootClaim, os.FileInfo, error) {
	start := time.Now()
	deadline := start.Add(d.envRootBusyWait)
	waited := false
	for {
		claim, info, err := execenv.LockEnvRootForReuse(wsRoot, rel, priorRoot)
		if !errors.Is(err, execenv.ErrEnvRootBusy) || !time.Now().Before(deadline) {
			// 记真实等待时长而不是预算：15s 是有依据的猜测（一个取消轮询间隔加
			// agent 退出），而这几行日志正是让它在生产里被校验、而不是一直停留在
			// 猜测的方式。
			if waited && err == nil {
				d.logger.Info("prior workdir freed while waiting for the previous run to exit",
					"task", task.ID, "prior_root", filepath.Base(priorRoot),
					"waited", time.Since(start).Round(time.Millisecond))
			}
			return claim, info, err
		}
		if !waited {
			waited = true
			d.logger.Info("prior workdir is still held by the previous run; waiting for it",
				"task", task.ID, "prior_root", filepath.Base(priorRoot),
				"budget", d.envRootBusyWait)
		}
		select {
		case <-ctx.Done():
			d.logger.Info("stopped waiting for the prior workdir: the run was cancelled",
				"task", task.ID, "prior_root", filepath.Base(priorRoot),
				"waited", time.Since(start).Round(time.Millisecond))
			// 不是循环里带着的那条 busy 错误。已取消的运行是第三种结局，把它折叠
			// 成 "still busy" 会把调用方推去为一份没人等的工作做全新 Prepare，还会
			// 在"预算就是用来被评判"的日志里，把一次取消同时记成 "cancelled" 与
			// "budget exhausted"。
			return nil, nil, fmt.Errorf("%w: %w", errPriorEnvRootWaitAborted, context.Cause(ctx))
		case <-time.After(envRootBusyRetryInterval):
		}
	}
}

// reuseLockTestHook 在复用资格验证与取 prior-root 锁之间运行。测试外为 nil；
// TOCTOU 回归测试用它让"换目录"变得确定，而不是去竞争它。
var reuseLockTestHook func()

// reuseBeforeUseTestHook 在 prior-root 认领尘埃落定之后、Reuse 按名字解析该路径
// 之前运行。测试外为 nil。
var reuseBeforeUseTestHook func()

// prepareExecutionEnvironment 派发到隔离助手进程执行 execenv.Prepare，或当没有
// 配置助手（聚焦单元测试）时进程内直接 Prepare。
func (d *Daemon) prepareExecutionEnvironment(ctx context.Context, params execenv.PrepareParams) (*execenv.Environment, error) {
	if d.executionEnvironmentCommand == nil {
		// 聚焦的 runTask 测试构造零值 Daemon，把准备留在进程内。New 创建的生产
		// Daemon 始终使用隔离助手。
		return execenv.Prepare(params, d.logger)
	}
	command, err := d.executionEnvironmentCommand()
	if err != nil {
		return nil, err
	}
	return execenv.PrepareIsolated(ctx, command, params, d.logger)
}

// reuseExecutionEnvironment 与 prepareExecutionEnvironment 对应，走隔离助手进程的
// execenv.Reuse；无助手时进程内直接复用。
func (d *Daemon) reuseExecutionEnvironment(ctx context.Context, params execenv.ReuseParams) (*execenv.Environment, error) {
	if d.executionEnvironmentCommand == nil {
		return execenv.Reuse(params, d.logger), nil
	}
	command, err := d.executionEnvironmentCommand()
	if err != nil {
		return nil, err
	}
	return execenv.ReuseIsolated(ctx, command, params, d.logger)
}

// effectiveTaskPrepareTimeout 返回任务准备阶段生效的硬性截止时间（显式设置优先，
// 否则用生产默认值）。
func (d *Daemon) effectiveTaskPrepareTimeout() time.Duration {
	if d.taskPrepareTimeout > 0 {
		return d.taskPrepareTimeout
	}
	return defaultTaskPrepareTimeout
}

// skillRefKey 拼接 (source, id) 作为技能引用的唯一 map 键（用 NUL 分隔避免碰撞）。
func skillRefKey(source, id string) string {
	return source + "\x00" + id
}

// skillRefFromBundle 由已下载的 SkillData 重新派生 SkillRefData（含清单哈希与文件
// 指纹），供后续的引用锁定与校验使用。
func skillRefFromBundle(bundle SkillData) SkillRefData {
	files := make([]skillbundle.File, 0, len(bundle.Files))
	for _, file := range bundle.Files {
		files = append(files, skillbundle.File{Path: file.Path, Content: file.Content, Kind: file.Kind, Mode: file.Mode, SHA256: file.SHA256, SizeBytes: file.SizeBytes})
	}
	manifest := skillbundle.BuildManifest(skillbundle.Skill{
		ID:          bundle.ID,
		Source:      bundle.Source,
		Name:        bundle.Name,
		Description: bundle.Description,
		Content:     bundle.Content,
		Files:       files,
	})
	fileRefs := make([]SkillFileRefData, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		fileRefs = append(fileRefs, SkillFileRefData{Path: file.Path, SHA256: file.SHA256, SizeBytes: file.SizeBytes})
	}
	return SkillRefData{
		ID:        bundle.ID,
		Source:    bundle.Source,
		Hash:      manifest.Hash,
		SizeBytes: manifest.SizeBytes,
		FileCount: manifest.FileCount,
		Files:     fileRefs,
	}
}

// taskModelSelection 是任务实际启动时使用的模型选择：model 选择器加通过校验的
// 能力覆盖项（thinking 等级、service tier）。
type taskModelSelection struct {
	Model         string
	ThinkingLevel string
	ServiceTier   string
}

// resolveTaskModelSelection 把模型选择器及其能力覆盖项对着 runtime 自己的模型目录
// 落定，每个任务最多读一次目录——且什么都不需要时不读。
//
// 单次读取是关键。发现是 CLI 子进程，上限 15-30s，且 cachedDiscovery 刻意不
// memoize 空或 fallback 的结果（#3729、MUL-5549），让瞬时失败能立即重试。因此
// 未登录或超时的 runtime 每次读取都要付满该上限；任务若为限定 model 读一次目录、
// 又为校验 thinking_level 再读一次，就要在 agent 启动前付两次（MUL-6471 review）。
//
// 谁要读目录：
//   - opencode 及其 DevEco fork 无法执行未限定的选择器，因此钉住的 model 必须在
//     启动前对着目录解析。
//   - thinking_level / service_tier 属于目录，且以目录自身的 model id 为键，因此
//     只要设置了就需要目录。
//
// 无能力覆盖的 pi 任务两者都不要：pi 自己的解析器接受任何形态的持久化 id，daemon
// 无事可加、完全跳过发现。claude、codex 以及任何不钉 model 的任务同理。
//
// 先做限定（qualify），因为下面两个检查都按目录的规范 id 匹配：未限定的 id 会在
// 每次查找里静默失败，丢掉一个完全有效的等级（GH #7300）。
func resolveTaskModelSelection(
	ctx context.Context,
	provider string,
	runtimeCmd agent.Command,
	sel taskModelSelection,
	taskLog *slog.Logger,
) taskModelSelection {
	capabilityChecksPending := sel.ThinkingLevel != "" || sel.ServiceTier != ""

	read := false
	var (
		catalog    agent.Catalog
		catalogErr error
	)
	loadCatalog := func() (agent.Catalog, error) {
		if !read {
			read = true
			catalog, catalogErr = listModels(ctx, provider, runtimeCmd)
		}
		return catalog, catalogErr
	}

	sel.Model = qualifyTaskModel(provider, sel.Model, capabilityChecksPending, loadCatalog, taskLog)

	// service_tier 归目录所有、目前仅 Codex。与 thinking_level 一样，陈旧或不兼容
	// 的持久化值降级到 runtime 默认，而不是让任务失败。目录查询错误放行——瞬态
	// 发现失败不能悄悄禁用一个之前有效的用户选择。
	if sel.ServiceTier != "" {
		ok, err := agent.ValidateServiceTierWith(loadCatalog, provider, sel.Model, sel.ServiceTier)
		if err != nil {
			taskLog.Warn("service_tier: catalog cannot validate; passing through",
				"provider", provider,
				"model", sel.Model,
				"service_tier", sel.ServiceTier,
				"error", err,
			)
		} else if !ok {
			taskLog.Warn("service_tier: not valid for this (provider, model); skipping injection",
				"provider", provider,
				"model", sel.Model,
				"service_tier", sel.ServiceTier,
			)
			sel.ServiceTier = ""
		}
	}
	// 按模型的护栏：服务器会对字面 token 校验 provider 的枚举，但按模型的口子
	// （Claude 在非 Opus 模型上的 `xhigh`、Codex 的按模型 `supported_reasoning_levels`）
	// 只有在这里、对着 daemon 本地 CLI 目录才能解。非法组合记警告并丢弃等级，而
	// 不是让任务失败，因此陈旧的持久化值永远不会阻塞执行。空 model 由
	// ValidateThinkingLevelWith 解析为 provider 默认模型，让默认模型任务不被误判——
	// codex 除外，它的空 model 跟随 config.toml（任意模型），因此 fail closed，在这
	// 里不读目录就丢弃等级。对已解析的模型，发现错误 fail open：列不出模型就保留
	// 持久化等级、让 CLI 自己反对。
	if sel.ThinkingLevel != "" {
		ok, err := agent.ValidateThinkingLevelWith(loadCatalog, provider, sel.Model, sel.ThinkingLevel)
		if err != nil {
			taskLog.Warn("thinking_level: catalog cannot validate; passing through",
				"provider", provider,
				"model", sel.Model,
				"thinking_level", sel.ThinkingLevel,
				"error", err,
			)
		} else if !ok {
			taskLog.Warn("thinking_level: not valid for this (provider, model); skipping injection",
				"provider", provider,
				"model", sel.Model,
				"thinking_level", sel.ThinkingLevel,
			)
			sel.ThinkingLevel = ""
		}
	}

	return sel
}

// qualifyTaskModel 把持久化的 model id 提升为 runtime 目录宣传的规范
// `<provider>/<id>` 选择器，并返回实际启动所用的 model。
//
// agent.model 装着持久化的任何东西；对 gateway 式 provider，裸 model id 本身就带
// 斜杠（provider `multica-anthropic` 下的 `claude/claude-opus-5`），因此分隔符无法
// 区分"缺 provider"与"有 provider"——只有目录知道（GH #7300）。
//
// 它读取目录有两个不同理由，都不是"因为能做到"：要么 runtime 拒绝不带限定选择器
// 启动，要么一次能力检查反正马上要读目录、而 id 不规范的话什么都匹配不上。两者都
// 不成立时，持久化值原样交给 CLI，不派生子进程。
func qualifyTaskModel(
	provider, model string,
	capabilityChecksPending bool,
	loadCatalog func() (agent.Catalog, error),
	taskLog *slog.Logger,
) string {
	if model == "" {
		return model
	}
	if !agent.ModelSelectorMustBeProviderQualified(provider) && !capabilityChecksPending {
		return model
	}
	catalog, err := loadCatalog()
	if err != nil {
		// 与能力检查同款 fail-open 姿态：目录不可达绝不能挡住一个"model 可能恰好
		// 正是 CLI 想要的"的任务。
		taskLog.Warn("model: catalog lookup failed; using the configured model as-is",
			"provider", provider,
			"model", model,
			"error", err,
		)
		return model
	}
	qualified, rewritten := agent.QualifyModelID(catalog, model)
	if !rewritten {
		return model
	}
	taskLog.Info("model: qualified against the runtime catalog",
		"provider", provider,
		"configured_model", model,
		"model", qualified,
	)
	return qualified
}

// runTask 执行单个任务的实际工作：校验身份、准备/复用隔离执行环境（env root、
// workdir、skill bundles、远端 MCP broker 与插件 hook），把任务上下文注入
// agent 环境，解析 model/thinking/service-tier，再经 backend 拉起 agent CLI、
// 排空消息流直至终态。任何失败都会折叠成分类后的错误与 TaskResult 状态
// （completed / blocked / cancelled 等），由 handleTask 统一上报。
// prepare 阶段受 taskPrepareTimeout 硬性截止约束；执行阶段真正跑在 provider
// 进程里，由 runCtx（可被服务器侧取消信号中断）与空闲看门狗监督。
func (d *Daemon) runTask(ctx context.Context, task Task, provider string, slot int, taskLog *slog.Logger) (taskResult TaskResult, returnErr error) {
	phaseRecorder := taskPhaseRecorderFromContext(ctx)
	phaseRecorder.Mark(taskPhasePrepareStarted)
	runStartedAt := time.Now()
	// 认领既在顶层携带任务的 agent id，也在展开的 agent 配置里携带一份。顶层 id
	// 权威，因为它还被绑进 task 级 token。两个身份不一致时绝不准备或复用 workdir。
	if err := validateTaskIdentity(task); err != nil {
		return TaskResult{}, err
	}

	// 拒绝在没有 workspace 的情况下拉起 agent。这里的空 workspace_id 会让
	// MULTICA_WORKSPACE_ID 在 agent 环境里为空，CLI 会静默回退到用户全局配置——
	// 多工作区共享一台主机时，这条路可能把操作泄漏进无关工作区。
	if task.WorkspaceID == "" {
		return TaskResult{}, fmt.Errorf("refusing to spawn agent: task has no workspace_id (task_id=%s)", task.ID)
	}

	prepareTimeout := d.effectiveTaskPrepareTimeout()
	prepareCtx, cancelPrepare := context.WithTimeoutCause(ctx, prepareTimeout, errTaskPrepareTimeout)
	prepareComplete := false
	defer func() {
		cancelPrepare()
		if prepareComplete || returnErr == nil || errors.Is(returnErr, errStartClaimRejected) || !errors.Is(context.Cause(prepareCtx), errTaskPrepareTimeout) {
			return
		}
		// 把每种截止形态（context deadline、HTTP 取消、或显式的
		// waitForExecutionEnvironment 原因）折叠成一个哨兵，handleTask 就能把它
		// 归类为可重试的平台超时。
		taskResult = TaskResult{}
		returnErr = fmt.Errorf("%w after %s", errTaskPrepareTimeout, prepareTimeout)
	}()

	// task.Repos 是本任务权威的仓库列表——认领的任务属于带 github_repo 资源的
	// project 时，服务器已经把它收窄到仅 project 仓库。确保这些 URL 在 per-workspace
	// 白名单和本地缓存里，否则 `multica repo checkout` 会拒绝那些"只绑在 project
	// 级、没绑在 workspace 级"的 URL。
	d.registerTaskRepos(task.WorkspaceID, task.ID, task.Repos)
	defer d.clearTaskRepoRefs(task.WorkspaceID, task.ID)

	entry, ok := d.agents()[provider]
	// 自定义 runtime profile（MUL-3284）覆盖可执行路径：runtime 的 protocol_family
	// 就是 provider（因此 agent.New 仍会选择正确 backend），但 PATH 上的实际二进制
	// 是 profile 的 command_name，注册时解析好、按 RuntimeID 在这里索引。关键点：
	// 自定义 runtime 可以活在一台"没装任何同 provider 内置 agent"的主机上，因此
	// runtime 为 custom 时我们合成 AgentEntry，而不是在 !ok 查找上硬失败。
	var profileFixedArgs []string
	// resolvedVersion 是内置二进制 entry.Path 解析出的 CLI 版本，由 resolveAgentEntry
	// 与路径配对，保证刚升级的 codex 绝不会在旧版本策略下启动（MUL-4486）。
	var resolvedVersion string
	// usesCustomProfileCommand 区分"provider 自己的二进制"与"说同一 provider 协议的
	// 别的二进制"。backend 需要它来做只对特定厂商 CLI 验证过的兼容性例外，这些例外
	// 绝不能延伸到共享协议族的任意命令上。
	var usesCustomProfileCommand bool
	if customSpec, isCustom := d.customProfileLaunchForRuntime(task.RuntimeID); isCustom {
		usesCustomProfileCommand = true
		entry.Path = customSpec.path
		resolvedVersion = customSpec.version
		// 在这里过滤而不是依赖 agent.New 去做，好让启动与下面的目录查询共用同一个
		// 前缀。它们共享一个以命令为键的发现 memo，同一 runtime 的两种拼写会键出
		// 两个条目。
		profileFixedArgs = agent.FilterLaunchPrefix(provider, customSpec.fixedArgs, d.logger)
		ok = true
		d.logger.Info("task uses custom runtime profile command",
			"task_id", task.ID, "runtime_id", task.RuntimeID,
			"provider", provider, "command_path", customSpec.path,
			"fixed_args", len(profileFixedArgs))
	} else if ok {
		// 内置 provider：自愈被就地升级删掉的钉住可执行路径（MUL-4486）。只在没有
		// 自定义 profile 接管启动时才到这里，所以自定义 runtime 的路径绝不事后怀疑，
		// 仅自定义的主机也不必白付一次重解析。
		var resolveErr error
		entry, resolvedVersion, resolveErr = d.resolveAgentEntryForLaunch(prepareCtx, provider, entry)
		if resolveErr != nil {
			return TaskResult{}, resolveErr
		}
	}
	if !ok {
		return TaskResult{}, fmt.Errorf("no agent configured for provider %q", provider)
	}

	stopPrepareLease := d.startTaskPrepareLeaseExtender(prepareCtx, task, taskLog)
	defer stopPrepareLease()

	if err := d.ensureTaskSkillBundles(prepareCtx, &task); err != nil {
		return TaskResult{}, err
	}
	phaseRecorder.Mark(taskPhaseSkillsReady)

	agentName := "agent"
	var skills []SkillData
	var instructions string
	agentName = task.Agent.Name
	skills = task.Agent.Skills
	instructions = supervisionInstructions(task)

	// 准备隔离执行环境。
	// 仓库只作为元数据传入——agent 按需通过 `multica repo checkout <url>` 检出。
	taskCtx := execenv.TaskContextForEnv{
		IssueID:             task.IssueID,
		TriggerCommentID:    task.TriggerCommentID,
		TriggerThreadID:     task.TriggerThreadID,
		CommentReplyTargets: commentReplyThreads(task),
		NewCommentCount:     task.NewCommentCount,
		NewCommentsSince:    task.NewCommentsSince,
		PriorSessionResumed: task.PriorSessionID != "",
		// MUL-5305：服务器在"更近的 Codex session 被扣下（rollout 缺失）而
		// PriorSessionID 是更旧的兜底（或缺失）"时设置它。brief 的连续性披露由此
		// 播种；下面的本地 resume 闸门只会把它 OR 成 true，所以这个信号是单调的。
		PriorSessionResumeUnavailable:    task.PriorSessionResumeUnavailable,
		AgentID:                          task.AgentID,
		AgentName:                        agentName,
		AgentInstructions:                instructions,
		CodexBriefViaAPI:                 provider == "codex" && task.SupervisionPrompt != "",
		AgentSkills:                      convertSkillsForEnv(skills),
		DisabledRuntimeSkills:            convertDisabledRuntimeSkillsForEnv(task.Agent, task.RuntimeID, provider),
		Repos:                            convertReposForEnv(task.Repos),
		ProjectID:                        task.ProjectID,
		ProjectTitle:                     task.ProjectTitle,
		ProjectDescription:               task.ProjectDescription,
		ProjectResources:                 convertProjectResourcesForEnv(task.ProjectResources),
		ChatSessionID:                    task.ChatSessionID,
		ChatChannelType:                  task.ChatChannelType,
		ChatChannelDeliversFiles:         task.ChatChannelDeliversFiles,
		AutopilotRunID:                   task.AutopilotRunID,
		AutopilotID:                      task.AutopilotID,
		AutopilotTitle:                   task.AutopilotTitle,
		AutopilotDescription:             task.AutopilotDescription,
		AutopilotSource:                  task.AutopilotSource,
		AutopilotTriggerPayload:          strings.TrimSpace(string(task.AutopilotTriggerPayload)),
		QuickCreatePrompt:                task.QuickCreatePrompt,
		HandoffNote:                      task.HandoffNote,
		IsSquadLeader:                    taskIsSquadLeader(task),
		RequestingUserName:               task.RequestingUserName,
		RequestingUserProfileDescription: task.RequestingUserProfileDescription,
		InitiatorType:                    task.InitiatorType,
		InitiatorID:                      task.InitiatorID,
		InitiatorName:                    task.InitiatorName,
		InitiatorEmail:                   task.InitiatorEmail,
		WorkspaceContext:                 task.WorkspaceContext,
		IssueStatuses:                    convertIssueStatusesForEnv(task.IssueStatuses),
		IssueStatusesOmitted:             task.IssueStatusesOmitted,
		ConnectedApps:                    task.ConnectedApps,
	}

	// A local_directory task gets a fresh CODEX_HOME but mounts the same
	// per-conversation sessions store as the task it replaces. Codex scopes its
	// own thread writer lock to CODEX_HOME, so those two processes otherwise use
	// different locks while appending to the same rollout. Hold a store-scoped,
	// cross-process lock for the complete task lifetime. Take it before the env
	// root claim so shutdown releases that claim first; the next task can then
	// safely reuse the prior environment after it acquires this lock.
	if provider == "codex" {
		if store := execenv.CodexSessionStorePath(d.cfg.Profile, taskCtx); store != "" {
			// Mark before waiting so the session-store GC cannot remove the store
			// between this task being queued behind its predecessor and mounting it.
			d.markActiveStore(store)
			defer d.unmarkActiveStore(store)

			waitCtx, waitCancel := context.WithCancel(prepareCtx)
			var cancelledByPoll <-chan struct{}
			writerLock, lockErr := execenv.AcquireCodexSessionWriterLock(waitCtx, store, func() {
				taskLog.Info("codex: waiting for prior task to release conversation session writer")
				pollInterval := d.cancelPollInterval
				if pollInterval == 0 {
					pollInterval = 5 * time.Second
				}
				cancelledByPoll = d.watchTaskCancellation(waitCtx, task.ID, pollInterval, taskLog)
				go func() {
					select {
					case <-cancelledByPoll:
						waitCancel()
					case <-waitCtx.Done():
					}
				}()
			})
			waitCancel()
			if lockErr != nil {
				return TaskResult{}, fmt.Errorf("acquire Codex conversation session writer: %w", lockErr)
			}
			defer writerLock.Release()
		}
	}

	// Mark candidate env roots as active before any env work so the GC loop
	// can't reclaim artifacts inside them mid-execution. We mark both the
	// stable root for a fresh Prepare and the prior root for Reuse — they
	// usually differ (Reuse keeps the original task's directory).
	resolvedRoot, err := execenv.ResolveRootDir(taskRootDirParams(d.cfg.WorkspacesRoot, task))
	if err != nil {
		return TaskResult{}, fmt.Errorf("resolve stable task env root: %w", err)
	}
	d.markActiveEnvRoot(resolvedRoot)
	defer d.unmarkActiveEnvRoot(resolvedRoot)
	if task.PriorWorkDir != "" {
		priorRoot := filepath.Dir(task.PriorWorkDir)
		if priorRoot != resolvedRoot {
			d.markActiveEnvRoot(priorRoot)
			defer d.unmarkActiveEnvRoot(priorRoot)
		}
	}

	// 就在这里、在 daemon 父进程里认领 env root，并持有到整个任务运行结束——与
	// 上面的 unmarkActiveEnvRoot 同一生命周期。
	//
	// 它不能放在准备阶段内部认领：生产环境的准备跑在短命助手进程里
	// （prepareExecutionEnvironment -> PrepareIsolated），那里取的锁会随助手一起
	// 死掉，*os.File 也无法跨它的 JSON 响应回到我们手上。在那边认领会留下一个
	// 毫无保护的运行中的 agent——而那正是这条护栏要防的重新派发窗口。
	envClaim, err := execenv.ClaimEnvRoot(taskRootDirParams(d.cfg.WorkspacesRoot, task))
	if err != nil {
		return TaskResult{}, fmt.Errorf("claim execution environment: %w", err)
	}
	defer envClaim.Release()

	// 尝试复用同一（agent, issue）对先前任务留下的 workdir。
	var env *execenv.Environment
	// 对内置 codex 任务，用"与已解析路径配对的版本"，就地升级才不会让沙箱策略
	// 停在旧版本上（MUL-4486）。自定义 codex runtime 跳过自愈，所以 resolvedVersion
	// 为空、沿用既有的缓存版本兜底——它的二进制是 profile 自己的命令，daemon 从不
	// 钉住或做版本探测。非 codex provider 带着这个值走但不会消费它。
	codexVersion := d.agentVersion("codex")
	if provider == "codex" && resolvedVersion != "" {
		codexVersion = resolvedVersion
	}
	// 这里再解析一次 local_directory assignment，让 runTask 能把 LocalWorkDir 接进
	// execenv。handleTask 已经为 worker 任务校验并锁过路径；leader 任务刻意跳过
	// assignment。
	localAssignment, _ := localDirectoryAssignmentForTask(task, d.cfg.DaemonID)
	if task.ReviewCommit != "" {
		task.PriorWorkDir = ""
		task.PriorSessionID = ""
	}
	// local_directory 任务刻意跳过 Reuse：prior WorkDir 是用户自己的路径（总是存
	// 在），但复用路径会丢掉 GC 循环需要的 envRoot 关联，而且对着稳定用户路径重跑
	// Prepare 很便宜（没有 clone、没有拷贝）。Leader 任务没有 localAssignment；
	// shouldReusePriorWorkdir 另要求 Prepare 期的受管 env 溯源 + daemon 所有标记才
	// 放行复用，所以修复前记录在 local_directory 上的 leader session 仍然 fail
	// closed。
	var agentMcpConfig json.RawMessage
	var effectiveMcpConfig json.RawMessage
	var cursorMcpAuthSource string
	remoteMCPConfig, remoteMCPDiagnostics, remoteMCPBrokers, remoteMCPErr := startTaskRemoteMCPBrokers(
		prepareCtx, ctx, task.ID, provider, task.RemoteMCPConnections,
		func(resolveCtx context.Context, contributionID string) (http.Header, error) {
			return d.client.ResolveRemoteMCPCredential(resolveCtx, task.RemoteMCPDaemonToken, task.ID, contributionID)
		},
		taskLog,
	)
	if remoteMCPErr != nil {
		return TaskResult{}, fmt.Errorf("prepare Remote MCP broker: %w", remoteMCPErr)
	}
	if remoteMCPBrokers != nil {
		defer remoteMCPBrokers.Close()
	}
	for _, diagnostic := range remoteMCPDiagnostics {
		taskLog.Warn("Remote MCP degraded", "reason", diagnostic)
	}

	// Agent 触发的插件 hook，作为 broker 之外的第二个本地 MCP 服务器。启动失败
	// 降级为"没有插件工具"而不是让任务失败：一个够不到插件的 agent 仍应能干活，
	// 与"失败的工具调用是工具错误而不是任务错误"同一条规则。
	pluginHookConfig, pluginHookServer, pluginHookErr := startTaskPluginHookMCP(
		ctx, task.ID, task.PluginHookTools,
		func(callCtx context.Context, taskID, installationID, hookKey string, input json.RawMessage) (json.RawMessage, error) {
			return d.client.InvokeAgentPluginHook(callCtx, task.RemoteMCPDaemonToken, taskID, installationID, hookKey, input)
		},
		taskLog,
	)
	if pluginHookErr != nil {
		taskLog.Warn("plugin hook tools unavailable", "error", pluginHookErr)
	}
	if pluginHookServer != nil {
		defer pluginHookServer.Close()
	}
	if len(pluginHookConfig) > 0 {
		merged, mergeErr := mergeTaskRemoteMCPConfig(remoteMCPConfig, pluginHookConfig)
		if mergeErr != nil {
			taskLog.Warn("could not merge plugin hook MCP config", "error", mergeErr)
		} else {
			remoteMCPConfig = merged
		}
	}
	if task.Agent != nil {
		agentMcpConfig = task.Agent.McpConfig
		effectiveMcpConfig = agentMcpConfig
		if merged, mergeErr := mergeRuntimeAndAgentMcpConfig(provider, agentMcpConfig); mergeErr != nil {
			taskLog.Warn("mcp_config: runtime merge failed; using agent configuration only",
				"provider", provider,
				"error", mergeErr,
			)
		} else {
			effectiveMcpConfig = merged
		}
		if len(remoteMCPConfig) > 0 {
			merged, mergeErr := mergeTaskRemoteMCPConfig(effectiveMcpConfig, remoteMCPConfig)
			if mergeErr != nil {
				return TaskResult{}, fmt.Errorf("merge Remote MCP broker configuration: %w", mergeErr)
			}
			effectiveMcpConfig = merged
		}
		if provider == "cursor" {
			cursorMcpAuthSource = strings.TrimSpace(task.Agent.CustomEnv[execenv.CursorMcpAuthSourceEnv])
		}
	}
	var agentEnvOverrides map[string]string
	var agentCustomArgs []string
	if task.Agent != nil {
		agentEnvOverrides = task.Agent.CustomEnv
		agentCustomArgs = task.Agent.CustomArgs
	}
	// 任务实际启动时用的 Codex CLI 参数，经过与 buildCodexArgs 相同的
	// agent.NormalizeCodexLaunchArgs 管线（shell 反引号去引 + 屏蔽标志过滤）规范化，
	// 保留 ExtraArgs（profile 固定参数 + daemon 默认值）与 CustomArgs（每 agent 的
	// custom_args）的切分，让过滤与真实启动完全一致。穿进 execenv，让 Windows
	// 沙箱决策能尊重一个永远不会落进 config.toml 的 `-c windows.sandbox=...`
	// 覆盖——哪怕它以 shell 引号形式到达——而不是静默降级用户的隔离 opt-in
	// （MUL-4957）。
	var codexSandboxArgs []string
	if provider == "codex" {
		// profileFixedArgs 即便已不再经 ExtraArgs 走，也仍该进这次重建：它现在是
		// 启动前缀，仍会出现在 codex 的 argv 上，写在里面的 `-c
		// windows.sandbox=...` 必须对沙箱决策可见。
		extraArgs := append(append([]string{}, profileFixedArgs...), defaultArgsForProvider(d.cfg, provider)...)
		codexSandboxArgs = agent.NormalizeCodexLaunchArgs(extraArgs, agentCustomArgs, effectiveMcpConfig, d.logger)
	}
	// Hermes：通过一个解析契约落定 overlay 的来源 home——custom_args 里解析出的
	// 选择（agent.ParseHermesProfileArgs）加上 agent 的 custom_env HERMES_HOME 喂给
	// execenv.ResolveHermesProfile，后者复刻 Hermes 自己的 profile 语义（root 推导、
	// 显式 vs 粘性选择、保留/非法失败）。保留/非法的选择让任务 fail closed，与
	// Hermes 的 sys.exit(1) 一致。选中的来源 home 导出到 hermesEnv["HERMES_HOME"]，
	// 让 profile 的 skills.external_dirs 里的 ${HERMES_HOME} 对着选中 profile 的
	// home 展开——就像原生 Hermes 在加载 config.yaml 之前做的那样。解析到的那次
	// occurrence 会在启动时从 acp argv 里剥掉（仅在 overlay 建成时），让该标志
	// 无法把 HERMES_HOME 指到 overlay 之外。
	var hermesSourceHome string
	var hermesSourceMustExist bool
	var hermesEnv map[string]string
	var hermesMemoryStore string
	var hermesSessionStore string
	if provider == "hermes" {
		// 从 hermes 真正会解析的 argv 解析——启动前缀、`acp`、再是过滤后的
		// custom args——agent.HermesLaunchArgv 与 backend 用同样的方式组装它。自定义
		// runtime profile 的 fixed_args 现在是启动前缀，所以它们在 custom_args 之前
		// 被扫描，backend 自己的 `acp` token 位于两者之间、参与扫描。近似那条 argv
		// 会读出与进程不同的 profile，overlay 就会从错误的 home 播种（GH #7046）。
		sel := agent.ParseHermesProfileArgs(agent.HermesLaunchArgv(profileFixedArgs, agentCustomArgs, d.logger))
		res := execenv.ResolveHermesProfile(agentEnvOverrides["HERMES_HOME"], sel.Name, sel.Found, sel.Inline)
		if res.Err != nil {
			return TaskResult{}, fmt.Errorf("resolve hermes profile: %w", res.Err)
		}
		hermesSourceHome = res.SourceHome
		hermesSourceMustExist = res.MustExist
		// overlay 从哪个 home 播种，决定任务能不能看到用户的 provider 配置，而它
		// 是从 daemon 进程环境推导的——在用户手动测试 `hermes acp` 的 shell 里看不
		// 见，这正是"手搓能跑、Multica 下就挂"（GH #6872）读起来像那样的原因。一
		// 行 Info，让答案在任何东西失败之前就躺在 daemon 日志里，而不是事后反推。
		taskLog.Info("hermes home resolved",
			"source_home", hermesSourceHome,
			"from_custom_env", strings.TrimSpace(agentEnvOverrides["HERMES_HOME"]) != "",
			"must_exist", hermesSourceMustExist,
		)
		hermesEnv = sanitizeAgentEnv(agentEnvOverrides)
		if hermesEnv == nil {
			hermesEnv = map[string]string{}
		}
		hermesEnv["HERMES_HOME"] = res.SourceHome
		// overlay 在这里链接 memories/，让 agent 的长期记忆跨任务存活，而不是被每次
		// 运行重置（#6638）。以已解析的来源 home 为键，切换 agent 的 profile 即切换
		// 它的记忆线——符合 Hermes 自己的"profile 是隔离实例"模型。整个任务期间受
		// GC 保护，与下面的 Codex store 相同。
		if store := execenv.HermesMemoryStorePath(d.cfg.Profile, task.AgentID, res.SourceHome); store != "" {
			hermesMemoryStore = store
			d.markActiveStore(store)
			defer d.unmarkActiveStore(store)
		}
		// overlay 在这里链接 state.db，让对话 transcript 跨任务存活、后续轮次真的
		// 能续上（GH #6806）。以（agent、已解析来源 home、对话）为键：同一对话的
		// 任务是串行的，所以分片只有一个写者，而两个 issue 从不共享一个数据库。
		// 整个任务期间受 GC 保护，与上面和下面的 store 相同。
		if store := execenv.HermesSessionStorePath(d.cfg.Profile, task.AgentID, res.SourceHome, taskCtx); store != "" {
			hermesSessionStore = store
			d.markActiveStore(store)
			defer d.unmarkActiveStore(store)
		}
	}
	// Reasonix 从环境定位它的用户配置（REASONIX_HOME，以及它背后的平台配置目录），
	// agent 的 custom_env 可能重指或清空它。每任务的 reasonix.toml 必须重述"子进程
	// 最终加载的那份配置"里的权限，让 runtime 属主在那里设的拒绝规则熬过覆盖它的
	// 任务作用域配置——所以用的是"与子进程启动时相同的清洗后 env"。
	var reasonixEnv map[string]string
	if provider == "reasonix" {
		reasonixEnv = sanitizeAgentEnv(agentEnvOverrides)
	}
	envReused := false
	savedPause, pauseRestoreErr := releaseCheckpointForTask(task, d.cfg.WorkspacesRoot)
	if pauseRestoreErr != nil {
		return TaskResult{}, fmt.Errorf("恢复发布暂停任务: %w", pauseRestoreErr)
	}
	if savedPause != nil {
		wsRoot, openErr := os.OpenRoot(d.cfg.WorkspacesRoot)
		if openErr != nil {
			return TaskResult{}, openErr
		}
		defer wsRoot.Close()
		rel, relErr := filepath.Rel(d.cfg.WorkspacesRoot, savedPause.Root)
		if relErr != nil {
			return TaskResult{}, relErr
		}
		pauseClaim, _, lockErr := execenv.LockEnvRootForReuse(wsRoot, rel, savedPause.Root)
		if lockErr != nil {
			return TaskResult{}, lockErr
		}
		defer pauseClaim.Release()
		var pauseReuseErr error
		env, pauseReuseErr = d.reuseExecutionEnvironment(prepareCtx, execenv.ReuseParams{WorkspacesRoot: d.cfg.WorkspacesRoot, Profile: d.cfg.Profile, WorkDir: savedPause.WorkDir, RootDir: savedPause.Root, LocalDirectory: savedPause.LocalDirectory, Provider: provider, CodexVersion: codexVersion, ResumeSessionID: task.PriorSessionID, McpConfig: effectiveMcpConfig, CursorMcpAuthSource: cursorMcpAuthSource, HermesSourceHome: hermesSourceHome, HermesSourceMustExist: hermesSourceMustExist, HermesEnv: hermesEnv, HermesMemoryStore: hermesMemoryStore, HermesSessionStore: hermesSessionStore, ReasonixEnv: reasonixEnv, CodexCustomArgs: codexSandboxArgs, Task: taskCtx})
		if pauseReuseErr != nil || env == nil {
			return TaskResult{}, fmt.Errorf("恢复发布暂停环境失败: %v", pauseReuseErr)
		}
		env.LocalWorktree = savedPause.Worktree
		envReused = true
	}
	savedInteraction, interactionRestoreErr := loadDSHInteractionCheckpoint(task, d.cfg.WorkspacesRoot)
	if interactionRestoreErr != nil {
		return TaskResult{}, fmt.Errorf("恢复 DSH 人工交互环境: %w", interactionRestoreErr)
	}
	if savedInteraction != nil {
		if savedPause != nil {
			return TaskResult{}, errors.New("任务不能同时恢复发布暂停和人工交互")
		}
		wsRoot, err := os.OpenRoot(d.cfg.WorkspacesRoot)
		if err != nil {
			return TaskResult{}, err
		}
		defer wsRoot.Close()
		rel, err := filepath.Rel(d.cfg.WorkspacesRoot, savedInteraction.Root)
		if err != nil {
			return TaskResult{}, err
		}
		claim, _, err := execenv.LockEnvRootForReuse(wsRoot, rel, savedInteraction.Root)
		if err != nil {
			return TaskResult{}, err
		}
		defer claim.Release()
		// 取锁后再次核对检查点，避免等待期间读到已被替换的执行目录。
		rechecked, err := loadDSHInteractionCheckpoint(task, d.cfg.WorkspacesRoot)
		if err != nil || rechecked.Root != savedInteraction.Root {
			return TaskResult{}, errors.New("DSH 人工交互环境在等待期间已变化")
		}
		env, err = d.reuseExecutionEnvironment(prepareCtx, execenv.ReuseParams{WorkspacesRoot: d.cfg.WorkspacesRoot, Profile: d.cfg.Profile,
			WorkDir: savedInteraction.WorkDir, RootDir: savedInteraction.Root, LocalDirectory: savedInteraction.LocalDirectory,
			Provider: provider, ResumeSessionID: task.PriorSessionID, McpConfig: effectiveMcpConfig, Task: taskCtx})
		if err != nil || env == nil {
			return TaskResult{}, fmt.Errorf("恢复 DSH 人工交互环境失败: %v", err)
		}
		env.LocalWorktree = savedInteraction.Worktree
		envReused = true
	}
	normalReuseTask := task
	if savedPause != nil || savedInteraction != nil {
		normalReuseTask.PriorWorkDir = ""
	}
	priorClaim, priorWorkDir, lockedPriorInfo, reusable, reuseErr := d.lockReusablePriorEnvRoot(ctx, normalReuseTask, localAssignment, envClaim.RootDir())
	if reuseErr != nil {
		// 等待上一运行放开目录期间被取消。在这里结束就是正确行为：继续往下会给
		// 一份已经没人等的工作准备整套环境——包括仓库 checkout。
		return TaskResult{}, reuseErr
	}
	if reusable && env == nil {
		defer priorClaim.Release()
		// 最后一窗口回归的确定性接缝：测试在认领尘埃落定之后、Reuse 按名字解析
		// 路径之前，在这里换目录。
		if reuseBeforeUseTestHook != nil {
			reuseBeforeUseTestHook()
		}
		var err error
		env, err = d.reuseExecutionEnvironment(prepareCtx, execenv.ReuseParams{
			WorkspacesRoot: d.cfg.WorkspacesRoot,
			Profile:        d.cfg.Profile,
			// 取锁时用的规范路径。把原始 PriorWorkDir 交给 Reuse 会让它重新解析，
			// 我们锁的目录与我们用的目录就可能不一致。
			WorkDir:               priorWorkDir,
			Provider:              provider,
			CodexVersion:          codexVersion,
			ResumeSessionID:       task.PriorSessionID,
			McpConfig:             effectiveMcpConfig,
			CursorMcpAuthSource:   cursorMcpAuthSource,
			HermesSourceHome:      hermesSourceHome,
			HermesSourceMustExist: hermesSourceMustExist,
			HermesEnv:             hermesEnv,
			HermesMemoryStore:     hermesMemoryStore,
			HermesSessionStore:    hermesSessionStore,
			ReasonixEnv:           reasonixEnv,
			CodexCustomArgs:       codexSandboxArgs,
			Task:                  taskCtx,
		})
		if err != nil {
			return TaskResult{}, asEnvironmentSetupFailure(fmt.Errorf("reuse execution environment: %w", err))
		}
		// Reuse 按名字解析 priorWorkDir，所以确认它实际打开的还是我们持锁的那个
		// 目录。fd 无法跨进准备助手进程，名字是唯一能交接的东西；这一步把"静默
		// 跑到了别处"变成"拒绝并全新启动"。仍覆盖不到的，见 lockReusablePriorEnvRoot。
		if env != nil && lockedPriorInfo != nil {
			usedInfo, statErr := os.Stat(filepath.Dir(env.WorkDir))
			if statErr != nil || !os.SameFile(lockedPriorInfo, usedInfo) {
				// No "task" field here: taskLog already carries the full id.
				taskLog.Info("reused workdir is not the directory that was claimed; starting a fresh environment")
				env = nil
			}
		}
		// Reuse 可能拒绝（返回 nil）并掉进下面的全新 Prepare。它拒绝与否，决定
		// 是否有"env-root 作用域的 session store"——Hermes overlay 的任务本地
		// state.db——从先前任务承接过来。
		envReused = env != nil
	}
	if env == nil {
		var err error
		prepParams := execenv.PrepareParams{
			WorkspacesRoot:  d.cfg.WorkspacesRoot,
			Profile:         d.cfg.Profile,
			WorkspaceID:     task.WorkspaceID,
			WorkspaceSlug:   task.WorkspaceSlug,
			TaskID:          task.ID,
			IssueIdentifier: task.IssueIdentifier,
			AgentName:       agentName,
			// 本次运行已持有认领（上面的 envClaim）及其隐含的重置；准备阶段绝不能
			// 再取一次。
			EnvRootPreclaimed:     true,
			Provider:              provider,
			CodexVersion:          codexVersion,
			McpConfig:             effectiveMcpConfig,
			CursorMcpAuthSource:   cursorMcpAuthSource,
			HermesSourceHome:      hermesSourceHome,
			HermesSourceMustExist: hermesSourceMustExist,
			HermesEnv:             hermesEnv,
			HermesMemoryStore:     hermesMemoryStore,
			HermesSessionStore:    hermesSessionStore,
			ReasonixEnv:           reasonixEnv,
			CodexCustomArgs:       codexSandboxArgs,
			Task:                  taskCtx,
		}
		if localAssignment.UsesWorktree() {
			prepParams.LocalWorktree = &execenv.LocalWorktreeParams{LocalPath: localAssignment.AbsPath, BaseRef: localAssignment.Ref.BaseRef, RepositoryURL: localAssignment.Ref.RepositoryURL}
			// 仅为快照而取 per-path 互斥锁，随即交还——够读到一棵一致树，又够短
			// 到 worktree 任务在运行期间仍然重叠。
			//
			// worktree 任务执行时跳过这把锁，但快照是它唯一"读用户目录"的时刻，而
			// 同一个真实路径可能作为 in_place 资源挂在另一个 project 上（每个
			// project 可以各挂一次，所以能有好几个）。在一场运行中的 in_place 任务
			// 底下拍快照，会抓下一棵写了一半的树加那个任务的在途 sidecar。
			//
			// 等待走与 acquireLocalDirectoryLockIfNeeded 里 in-place 获取相同的可见性
			// 管线，因为持有者可能是一场能跑数小时的 in-place 任务：没有状态更新，
			// 用户只看到光秃秃的 "preparing"、毫无"任务排在目录后面"的提示；没有
			// 轮询器，用户取消的任务会一直占着 daemon 槽位直到 prepare 超时——
			// run 阶段的取消 watcher 只在启动之后才开始。本阶段全程已有 prepare-
			// lease 续期器在跑，所以这里只需镜像状态、计数与取消。
			waitCtx, waitCancel := context.WithCancel(prepareCtx)
			defer waitCancel()
			pollInterval := d.cancelPollInterval
			if pollInterval == 0 {
				pollInterval = 5 * time.Second
			}
			// LocalPathLocker 在本 goroutine 里同步调用 onWait，一次 Acquire 至多
			// 一次——见 in-place 调用点。
			waitCounted := false
			release, lockErr := d.localPathLocks.Acquire(waitCtx, localAssignment.RealPath, task.ID, func(holder string) {
				d.resourceWaitTasks.Add(1)
				waitCounted = true
				reason := fmt.Sprintf("local_directory %s", localAssignment.AbsPath)
				if holder != "" {
					reason = fmt.Sprintf("%s (held by task %s)", reason, shortID(holder))
				}
				taskLog.Info("local_directory: worktree snapshot waiting for holder",
					"holder", holder)
				if waitErr := d.client.MarkTaskWaitingLocalDirectory(waitCtx, task.ID, reason); waitErr != nil {
					// 非致命：等待照常发生，UI 只是不显示那条显式的 "waiting" 徽标。
					taskLog.Warn("local_directory: mark waiting status failed", "error", waitErr)
				}
				cancelled := d.watchTaskCancellation(waitCtx, task.ID, pollInterval, taskLog)
				go func() {
					select {
					case <-cancelled:
						waitCancel()
					case <-waitCtx.Done():
					}
				}()
			})
			if waitCounted {
				d.resourceWaitTasks.Add(-1)
			}
			if lockErr != nil {
				return TaskResult{}, fmt.Errorf("local_directory worktree: wait for a consistent snapshot of %s: %w",
					localAssignment.AbsPath, lockErr)
			}
			env, err = d.prepareExecutionEnvironment(prepareCtx, prepParams)
			release()
			if err != nil {
				return TaskResult{}, asEnvironmentSetupFailure(fmt.Errorf("prepare execution environment: %w", err))
			}
		} else {
			if localAssignment != nil {
				prepParams.LocalWorkDir = localAssignment.AbsPath
			}
			env, err = d.prepareExecutionEnvironment(prepareCtx, prepParams)
			if err != nil {
				return TaskResult{}, asEnvironmentSetupFailure(fmt.Errorf("prepare execution environment: %w", err))
			}
		}
	}
	var codexNativeState execenv.CodexNativeState
	if provider == "codex" && !usesCustomProfileCommand && runtime.GOOS == "linux" && agent.CodexResidentSupported(codexVersion) {
		var stateErr error
		codexNativeState, stateErr = d.prepareCodexNativeState(prepareCtx, task, env, execenv.CodexSessionStorePath(d.cfg.Profile, taskCtx))
		if stateErr != nil {
			return TaskResult{}, asEnvironmentSetupFailure(fmt.Errorf("prepare Codex native state: %w", stateErr))
		}
	}
	phaseRecorder.Mark(taskPhaseEnvironmentReady)
	// Belt-and-suspenders: also mark whatever root we ended up with, in case
	// future changes diverge from ResolveRootDir.
	if env.RootDir != resolvedRoot && env.RootDir != "" {
		d.markActiveEnvRoot(env.RootDir)
		defer d.unmarkActiveEnvRoot(env.RootDir)
	}

	// 在"每个"退出路径上收尾 worktree，成功失败都算：先提交 agent 留下的未提交
	// 改动，再把 worktree 从用户仓库注销。挂到具名返回值上，让中途失败的任务仍能
	// 交还持有其部分工作的分支，而不是被 `git worktree remove --force` 删掉。任务
	// 失败时恰恰是用户最想看 agent 走到哪一步的时候。
	//
	// 原地（in-place）local_directory 运行绝不进这个块：它们的 WorkDir 本就持久，
	// DurableWorkDir 刻意保持缺席，而不是在同一路径下顶着两个生命周期含义。
	if env.LocalWorktree != nil {
		defer func() {
			if taskResult.Status == "waiting_plan" || taskResult.Status == "waiting_input" {
				return
			}
			if taskResult.WorkDir == "" {
				taskResult.WorkDir = env.WorkDir
			}
			if taskResult.EnvRoot == "" {
				taskResult.EnvRoot = env.RootDir
			}
			if pauseErr := d.preserveReleaseTask(ctx, task, env, taskResult.SessionID); pauseErr != nil {
				returnErr = errors.Join(returnErr, pauseErr)
				return
			}
			if releasePaused(ctx) {
				return
			}
			outcome, finalizeErr := env.LocalWorktree.Finalize(taskLog)
			if outcome.Branch != "" {
				taskResult.BranchName = outcome.Branch
			}
			if finalizeErr == nil {
				// 只有 Finalize 确认一次性任务 worktree 真的没了，配置的
				// local_directory 才成为权威。
				if localAssignment != nil {
					taskResult.DurableWorkDir = localAssignment.AbsPath
				}
				return
			}
			// Finalize 没能完成它的交付契约，任务 worktree 因而仍是权威。这既覆盖
			// 未提交的改动集，也覆盖"分支已提交、但无法确认 worktree 移除成功"。
			// 让任务失败：在这里报成功或报一个持久的 project 目录，会藏起那条仍
			// 需留意的路径。
			//
			// 包进 worktreePreservedError，取消路径才能认出它：已取消任务会丢弃结果
			// 和错误，但这条错误点名了保存 agent 工作的 worktree，必须搭 cancel ack
			// 而不是消失进日志。用 Join 而不是替换更早的失败——那个通常是更有用的
			// 主因，但保留路径也不能被它挤掉。
			taskLog.Error("local_directory: worktree finalize incomplete; keeping the task worktree authoritative",
				"error", finalizeErr, "preserved_path", outcome.PreservedPath)
			wrapped := &worktreePreservedError{err: fmt.Errorf("local_directory worktree: %w", finalizeErr)}
			if returnErr == nil {
				returnErr = wrapped
			} else {
				returnErr = errors.Join(returnErr, wrapped)
			}
		}()
	}
	defer func() {
		if taskResult.Status == "waiting_plan" || taskResult.Status == "waiting_input" {
			if err := saveDSHInteractionCheckpoint(task, taskResult, env, envClaim.RootDir()); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("保存 DSH 人工交互环境失败: %w", err))
			}
		}
	}()
	if task.ReviewCommit != "" {
		reviewDirectory := env.WorkDir
		if localAssignment == nil {
			if len(task.Repos) != 1 || d.repoCache == nil {
				return TaskResult{}, fmt.Errorf("审查候选未绑定唯一可访问仓库，停止启动模型")
			}
			checked, err := d.repoCache.CreateWorktree(repocache.WorktreeParams{WorkspaceID: task.WorkspaceID, RepoURL: task.Repos[0].URL, WorkDir: env.WorkDir, Ref: task.ReviewCommit, TaskID: task.ID, AgentName: "review", IsolatedGitMetadata: true})
			if err != nil {
				return TaskResult{}, fmt.Errorf("准备审查候选: %w", err)
			}
			reviewDirectory = checked.Path
		}
		head, err := codediff.GitCommand(ctx, reviewDirectory, "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(head) != task.ReviewCommit {
			return TaskResult{}, fmt.Errorf("审查目录提交与固定候选不一致，停止启动模型")
		}
		task.HandoffNote += fmt.Sprintf("\n审查仓库：%s；固定提交：%s。只读取这个候选。\n", reviewDirectory, task.ReviewCommit)
	}
	defer func() {
		if pauseErr := d.preserveReleaseTask(ctx, task, env, taskResult.SessionID); pauseErr != nil {
			returnErr = errors.Join(returnErr, pauseErr)
			return
		}
		if !releasePaused(ctx) && savedPause != nil {
			if removeErr := os.Remove(filepath.Join(env.RootDir, ".release-pause.json")); removeErr != nil && !os.IsNotExist(removeErr) {
				returnErr = errors.Join(returnErr, removeErr)
			}
		}
		if releasePaused(ctx) {
			taskResult.WorkDir = env.WorkDir
			taskResult.EnvRoot = env.RootDir
		}
	}()
	// 云模式下，workdir 留给同（agent, issue）对的未来任务复用；work_dir 路径在
	// 任务完成时存进 DB、下次认领时经 PriorWorkDir 传回，所以原地重写标记块正是
	// 正确行为。
	//
	// local_directory 模式下 workdir 是用户自己的仓库、复用已在上面禁用（见
	// localAssignment == nil），否则 brief 会留在用户仓库里——之后用户在那个目录
	// 手动跑 `claude` / `codex` 会捡起陈旧的 Multica 指令（issue id、trigger comment
	// id、回复规则），开始按上一个任务的上下文行动。离开前把标记块切除。
	//
	// worktree 模式跑同样的清理，理由不同：worktree 是一次性的，但它的分支是交付
	// 物，Finalize 会提交仍在盘上的东西。没有这一步，sidecar 会落进每个任务的
	// diff。repocache 给 github_repo worktree 用的 .git/info/exclude 手法在这里不可
	// 用——linked worktree 会把 info/exclude 解析到用户自己的 common git dir，用了
	// 就会悄悄改变用户 checkout 里 `git status` 隐藏的内容。删除我们写的文件既更窄
	// 也更精确；它也保留了 agent 对已跟踪 CLAUDE.md 的真编辑，因为
	// CleanupRuntimeConfig 只切除我们的标记块。
	//
	// 顺序：紧跟上面的 Finalize defer 注册，LIFO 让清理先跑、Finalize 提交一棵已
	// 干净的工作树。它还必须早于从这里到 provider 启动之间每一个提前返回（temp-dir
	// 搭建、StartTask）：那些路径仍会跑 Finalize，没有这一步，Finalize 会把 Prepare
	// 刚写的 sidecar 自动提交、交付一条内容只有 Multica 运行时文件的分支——或原地
	// 把它们留在用户树里。
	if env.LocalDirectory || env.LocalWorktree != nil {
		defer func() {
			var cleanupErr error
			if cerr := execenv.CleanupRuntimeConfig(env.WorkDir, provider); cerr != nil {
				cleanupErr = cerr
				d.logger.Warn("execenv: cleanup runtime config failed", "error", cerr)
			}
			// 切除 Prepare 写进用户仓库的 sidecar 树（.agent_context/、.multica/、
			// provider 专属的 .claude/skills/ 等）。没有这一步，用户的树每个任务都
			// 会积一层目录——见 MUL-2784。CleanupRuntimeConfig 负责 CLAUDE.md /
			// AGENTS.md 里的运行时 brief；CleanupSidecars 负责 Prepare 放在 WorkDir
			// 下的其它每个文件。两者合力把 workdir 精确往返到任务前的字节。
			if cerr := execenv.CleanupSidecars(env.RootDir); cerr != nil {
				if cleanupErr == nil {
					cleanupErr = cerr
				}
				d.logger.Warn("execenv: cleanup sidecars failed", "error", cerr)
			}
			// worktree 模式下，一次失败的清理是不可存活的：Finalize 马上要
			// `git add -A`，清理没能移除的东西会被提交、作为任务分支交付——一条
			// 内容全是 Multica 运行时文件的 diff，正是这个模式承诺绝不产出的东西。
			// 改叫 Finalize 中止，这样什么都不提交、worktree 保留下来供检查。（原地
			// 模式没有提交、没有分支，清理失败停在警告即可：残留文件就在用户自己
			// 的树里、可以手工移除。）
			if cleanupErr != nil && env.LocalWorktree != nil {
				env.LocalWorktree.AbortWithReason(fmt.Errorf(
					"could not remove the runtime's own files from the worktree before committing: %w", cleanupErr))
			}
		}()
	}
	taskTempDir, taskTempLock, err := ensureTaskTempDir(env.RootDir, task.WorkspaceID, task.ID)
	if err != nil {
		return TaskResult{}, fmt.Errorf("prepare task temp dir: %w", err)
	}
	defer func() {
		// 先放执行锁、再删目录：锁被持有时 GC 清扫正确地拒绝碰这个目录，所以若删
		// 除在这里失败（内部还有文件开着——#7364 里的 Windows 情形），目录会一直
		// 钉到 daemon 退出。先放掉，下一个 GC 周期会拿到锁、看到属主已不在并回收
		// 它。任务路径上没有任何人在等。
		//
		// 用 RemoveTaskTempDir 而不是 os.RemoveAll：这样在删除失败时 .task_lock
		// 标记能留下——没有它，下一次清扫无法把该目录与"上锁之前"的残留区分开。
		execenv.ReleaseTaskTempLock(taskTempLock)
		if pauseErr := d.preserveReleaseTask(ctx, task, env, taskResult.SessionID); pauseErr != nil {
			returnErr = errors.Join(returnErr, pauseErr)
			return
		}
		if releasePaused(ctx) {
			return
		}
		if cerr := execenv.RemoveTaskTempDir(taskTempDir); cerr != nil {
			taskLog.Warn("task temp dir cleanup failed", "path", taskTempDir, "error", cerr)
		}
	}()

	// Issue #3999 race A：现在 env.WorkDir 已在盘上，把服务器侧状态机从 dispatched
	// （或 waiting_local_directory）翻到 running。在 Prepare/Reuse 之前调 StartTask，
	// 会让任何"读到 status==running 后解析
	// /multica_workspaces/{ws}/{short-id}/workdir"的消费者，在 os.MkdirAll 跑之前
	// 的微秒窗口里撞上 FileNotFoundError。
	//
	// On error we return early. A rejected claim is discarded by handleTask;
	// other errors use its existing FailTask + taskfailure.Classify path with the same
	// "start task failed: <…>" string and the same failure_reason
	// taxonomy as before — see MUL-2946 for the classifier contract.
	var taskCapabilities []string
	if agent.SupportsTaskSupplement(provider, resolvedVersion) && task.IssueID != "" {
		taskCapabilities = append(taskCapabilities, protocol.DaemonCapabilityTaskSupplementV1)
	}
	taskSupplementNegotiated, err := d.client.StartTask(prepareCtx, task, taskCapabilities...)
	if err != nil {
		stopPrepareLease()
		return TaskResult{}, fmt.Errorf("start task failed: %w", err)
	}
	stopPrepareLease()
	if taskSupplementNegotiated {
		// Register before provider launch so a hint cannot arrive in the gap
		// between the committed server transition and turn/started. The row is
		// durable, so a coalesced hint is sufficient; the five-second fallback
		// covers a notification sent before this start response arrived.
		_, unsubscribeSupplements := d.taskSupplementSignals.subscribe(task.ID)
		defer unsubscribeSupplements()
	}
	var runtimeInputCapabilities []string
	if task.StartClaimSupported && !runtimeCommandOnly(task.Command, task.ChatMessage) && (task.IssueID != "" || task.ChatSessionID != "") &&
		(task.Agent == nil || (task.Agent.SystemKey != "quality_supervision" && task.Agent.SystemKey != "issue_review")) {
		if offered := agent.RuntimeInputCapabilities(provider, resolvedVersion, task.NativeGoal); len(offered) > 0 {
			call, stop := context.WithTimeout(prepareCtx, 3*time.Second)
			runtimeInputCapabilities, err = d.client.RegisterTaskRuntimeCapability(call, task, offered)
			stop()
			if err != nil {
				taskLog.Debug("runtime input capability not negotiated", "error", err)
			}
		}
	}
	prepareComplete = true
	cancelPrepare()
	_ = d.client.ReportProgress(ctx, task.ID, fmt.Sprintf("Launching %s", provider), 1, 2)

	resumeReachable := gateResumeToReachableSession(&task, &taskCtx, provider, env.WorkDir, sessionHomeReachable(provider, env, envReused), provider == "codex" && codexWorktreeResumeEligible(env, execenv.CodexSessionStorePath(d.cfg.Profile, taskCtx)), taskLog)
	// 复用的 workdir 对 Codex resume 是必要而非充分：先前线程的 rollout 必须真的
	// 出现在本任务的 CODEX_HOME sessions 里（MUL-4424 隔离了它们）。不在的话，在
	// 下面生成 brief 之前就丢掉 resume，我们绝不会告诉 agent"它在继续一段 Codex
	// 会静默从头重启的对话"。
	if resumeReachable {
		gateCodexResumeToRolloutPresence(&task, &taskCtx, provider, env.CodexHome, taskLog)
	}

	// 注入 runtime 专属配置（meta skill），让 agent 发现 .agent_context/。
	if provider == "codebuddy" {
		hookEnv := map[string]string{}
		layerCustomEnvAndHermesHome(hookEnv, agentEnvOverrides, "", nil)
		hookConfig := agent.Config{CLIVersion: resolvedVersion, BuiltinRuntime: !usesCustomProfileCommand, Env: hookEnv}
		d.configureCodebuddyResident(task, env, &hookConfig)
		taskCtx.CodebuddyBriefViaHook = task.SupervisionPrompt == "" && agent.CodebuddyNativeHooksEnabled(hookConfig, agent.ExecOptions{Cwd: env.WorkDir, ExtraArgs: defaultArgsForProvider(d.cfg, provider), CustomArgs: agentCustomArgs, CodebuddySettingsPath: env.CodebuddySettingsPath, NativeGoal: task.NativeGoal, Command: task.Command, CommandOnly: runtimeCommandOnly(task.Command, task.ChatMessage)})
	}
	runtimeBrief, err := execenv.InjectRuntimeConfig(env.WorkDir, provider, taskCtx)
	if err != nil {
		if provider == "codex" || taskCtx.CodebuddyBriefViaHook {
			return TaskResult{}, fmt.Errorf("inject runtime config: %w", err)
		}
		d.logger.Warn("execenv: inject runtime config failed (non-fatal)", "error", err)
	}
	if err := execenv.WriteRuntimeProvenance(env.RootDir, env.WorkDir, provider, runtimeBrief, taskCtx); err != nil {
		d.logger.Warn("write runtime instruction provenance failed", "error", err)
	}
	// 豁免轮次在用户目录里运行、却没为它排队，所以现在可能正有一个兄弟编码任务
	// 在往同一棵树里写。这是它光凭自己的上下文推不出来的唯一一件事——告诉它。
	// worktree 模式被排除：那里的树是本任务私有的 checkout。
	var promptOptions []PromptOption
	if localAssignment != nil && !localAssignment.UsesWorktree() && localDirectoryLockExempt(task) {
		promptOptions = append(promptOptions, WithSharedLocalDirectory())
	}
	// worktree 模式交出的树，在"用户上一轮以来的编辑"与"分支自己的工作"冲突时是
	// 半合并状态。冲突刻意留给 agent 解决，所以提示必须承担"告知它"的职责
	// （MUL-6881）。
	if env.LocalWorktree != nil && len(env.LocalWorktree.ReplayConflicts) > 0 {
		promptOptions = append(promptOptions, WithWorktreeReplayConflicts(env.LocalWorktree.ReplayConflicts))
	}
	// 1. Keep final composition shared with the JSON-supervision regression boundary.
	prompt := buildExecutionPrompt(task, provider, env, envReused, localAssignment == nil, promptOptions...)
	// Prompt-size telemetry: brief and prompt bytes are the two costs every run
	// pays before any work starts. Keep them observable so prompt refactors can be
	// measured on real tasks instead of estimated from source.
	taskLog.Info("prompt composed", "provider", provider, "brief_bytes", len(runtimeBrief), "agent_instructions_bytes", len(taskCtx.AgentInstructions), "prompt_bytes", len(prompt))

	// 传任务级鉴权凭据与上下文，好让被拉起的 agent CLI 能调 Multica API 与本地
	// daemon（如 `multica repo checkout`）。MULTICA_TASK_SLOT 从 daemon 级并发池
	// 分配，不是每 agent 一个；一个 daemon 托管多个 agent 时，槽位索引共享的
	// daemon 级资源（如 GPU）。MULTICA_TOKEN 由服务器绑定到 (agent, task)。这里
	// 绝不回退到 daemon 自己的凭据：那样 agent CLI 的写入会以 runtime 属主的成员
	// 身份落地，还可能把同一个 agent 再次触发。
	agentToken, err := taskScopedAuthToken(task)
	if err != nil {
		taskLog.Error("task auth token invalid; refusing to start agent", "error", err)
		return TaskResult{}, err
	}
	agentEnv := taskMulticaEnvironment(task, provider, agentName, agentToken, env.MulticaConfigRoot, d.cfg.WorkspacesRoot, d.cfg.ServerBaseURL, d.cfg.HealthPort, slot, taskTempDir)
	if checkoutMode := repoCheckoutModeFor(provider, runtime.GOOS); checkoutMode != "" {
		agentEnv[repoCheckoutModeEnv] = checkoutMode
	}
	if task.AutopilotRunID != "" {
		agentEnv["MULTICA_AUTOPILOT_RUN_ID"] = task.AutopilotRunID
	}
	if task.AutopilotID != "" {
		agentEnv["MULTICA_AUTOPILOT_ID"] = task.AutopilotID
	}
	// quick-create 标记——设置时，multica CLI 的 `issue create` 命令会以
	// origin_type=quick_create + origin_id=<task_id> 给新 issue 盖章，完成处理器就能
	// 确定性地找到它（见 GetIssueByOrigin）。
	if task.QuickCreatePrompt != "" {
		agentEnv["MULTICA_QUICK_CREATE_TASK_ID"] = task.ID
		if len(task.QuickCreateAttachmentIDs) > 0 {
			if raw, err := json.Marshal(task.QuickCreateAttachmentIDs); err == nil {
				agentEnv["MULTICA_QUICK_CREATE_ATTACHMENT_IDS"] = string(raw)
			} else {
				taskLog.Warn("quick-create attachment ids: marshal failed; skipping env injection", "error", err)
			}
		}
	}
	// 确保 multica CLI 在 agent 环境的 PATH 上。有些 runtime（如 Codex）跑在可能
	// 不继承 daemon PATH 的隔离沙箱里。把正在运行的 multica 二进制所在目录加在最
	// 前面，agent 里的 `multica` 命令就总能解析到。
	if selfBin, err := resolveSelfExecutable(); err == nil {
		binDir := filepath.Dir(selfBin)
		agentEnv["PATH"] = binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	// 把 Codex 指到每任务的 CODEX_HOME，让它原生发现技能、又不污染系统的
	// ~/.codex/skills/。
	if env.CodexHome != "" {
		agentEnv["CODEX_HOME"] = env.CodexHome
	}
	if codexNativeState.SQLiteHome != "" {
		agentEnv["CODEX_SQLITE_HOME"] = codexNativeState.SQLiteHome
	}
	// HOME 与 XDG base 目录在这里刻意不碰：gh、aws、kubectl、npm 等 provider 工具
	// 继续解析 daemon 用户的既有状态（MUL-5578）。Multica CLI 是例外：
	// MULTICA_TASK_CONFIG_ROOT 把它的隐式 profile 查找重定向到私有任务本地状态、
	// 阻止 Owner-profile 兜底。（Hermes 的 HERMES_HOME 在 custom_env 之后应用，好
	// 让每任务 overlay 能压过用户设的 HERMES_HOME；见
	// layerCustomEnvAndHermesHome。）
	// 有受管 MCP 时，把 Cursor 指到每任务的 project 状态。workdir 里的
	// .cursor/mcp.json 携带受管服务器列表，CURSOR_DATA_DIR 则把匹配的 project
	// 审批与用户持久的 ~/.cursor/projects 状态隔离开。
	if env.CursorDataDir != "" {
		agentEnv["CURSOR_DATA_DIR"] = env.CursorDataDir
	}
	// 注入用户配置的自定义环境变量（如 router/proxy 模式下的 ANTHROPIC_API_KEY、
	// ANTHROPIC_BASE_URL，或 Bedrock 的 CLAUDE_CODE_USE_BEDROCK）。这些通过 agent
	// 设置 UI 按 agent 设置。关键内部变量在阻止名单上，防止意外或恶意的覆盖把
	// daemon 设好的值改掉。
	var agentCustomEnv map[string]string
	if task.Agent != nil {
		agentCustomEnv = task.Agent.CustomEnv
	}
	layerCustomEnvAndHermesHome(agentEnv, agentCustomEnv, env.HermesHome, d.logger)
	if provider == "reasonix" {
		reasonixStateHome, err := prepareReasonixTaskStateHome(d.cfg.Profile, task.RuntimeID, task.AgentID)
		if err != nil {
			return TaskResult{}, fmt.Errorf("prepare reasonix state home: %w", err)
		}
		agentEnv["REASONIX_STATE_HOME"] = reasonixStateHome
	}
	if provider == "dsh" {
		dshSessionRoot, err := prepareDshTaskSessionRoot(d.cfg.Profile, task.RuntimeID, task.AgentID)
		if err != nil {
			return TaskResult{}, fmt.Errorf("prepare dsh session root: %w", err)
		}
		agentEnv["MULTICA_DSH_SESSION_ROOT"] = dshSessionRoot
		agentEnv["DSH_TELEMETRY_DISABLED"] = "1"
	}
	if err := configureCodexTaskShellEnvironment(provider, env.CodexHome, os.Environ(), agentEnv, agentCustomEnv, d.logger); err != nil {
		return TaskResult{}, err
	}
	if provider == "codebuddy" && task.SupervisionPrompt != "" {
		directory, setupErr := agent.PrepareCodebuddySupervisionConfig(env.RootDir, agentEnv)
		if setupErr != nil {
			return TaskResult{}, fmt.Errorf("prepare CodeBuddy supervision configuration: %w", setupErr)
		}
		agentEnv["CODEBUDDY_CONFIG_DIR"] = directory
		agentEnv["CODEBUDDY_PLUGIN_DIRS"] = ""
	}
	// overlay 一旦建成就是权威，所以命令行上的任何东西都不能把 HERMES_HOME 指回
	// overlay 之外。两个 argv 区段对着"解析器读过的那份同一 argv"一起剥：一次选择
	// 可能横跨它们（一个以裸 `-p` 结尾的前缀会把 backend 的 `acp` 吞进去），按区段
	// 剥是看不见这种跨段的。
	var hermesOverlayCustomArgs []string
	hermesOverlayActive := provider == "hermes" && env != nil && env.HermesHome != ""
	if hermesOverlayActive {
		var rawCustomArgs []string
		if task.Agent != nil {
			rawCustomArgs = task.Agent.CustomArgs
		}
		profileFixedArgs, hermesOverlayCustomArgs = agent.StripHermesProfileSelectors(
			profileFixedArgs, rawCustomArgs, d.logger)
	}
	// 通过统一 runtime 解析器解析 backend：内置 runtime 身份（如 "omp"）走
	// NewRuntime 分派，协议族走 New。这是唯一的生产边界——daemon 从不直接调
	// agent.New 或 agent.NewRuntime，两个工厂各自保持"恰好一个含义"。
	backendConfig := agent.Config{
		ExecutablePath:  entry.Path,
		LaunchPrefix:    profileFixedArgs,
		CLIVersion:      resolvedVersion,
		Env:             agentEnv,
		Logger:          d.logger,
		TaskID:          task.ID,
		RuntimeID:       task.RuntimeID,
		DaemonVersion:   d.cfg.CLIVersion,
		CodexVersion:    codexVersion,
		BuiltinRuntime:  !usesCustomProfileCommand,
		CodexResumePath: codexNativeState.ResumePath,
	}
	if provider == "codex" {
		d.configureCodexResident(task, env, execenv.CodexSessionStorePath(d.cfg.Profile, taskCtx), &backendConfig)
	} else if provider == "codebuddy" {
		d.configureCodebuddyResident(task, env, &backendConfig)
	}
	backend, err := agent.ResolveBackend(provider, backendConfig)
	if err != nil {
		return TaskResult{}, fmt.Errorf("create agent backend: %w", err)
	}

	// 两段式模型解析：显式的 agent.model 优先，其次是 daemon 级
	// MULTICA_<PROVIDER>_MODEL 环境变量。两者都为空时我们刻意把 "" 传过去——每个
	// backend 会从 CLI 调用里省略 `--model`，让 provider 选自己的默认（Claude Code
	// 出厂默认、codex app-server 的账号级默认等）。在 Go 侧烘一个 "recommended
	// default" 正是 cursor 回归发生的方式——静态猜测会漂离上游 CLI 实际接受的
	// 东西。
	//
	// 在启动日志之前解析，而不是首次使用时：在那边记 entry.Model 只会报环境变量那
	// 一层，于是每个"model 来自 agent.model"的任务——通常是绝大多数——都拿空 model
	// 自报家门，看起来像选择被丢了（GH #7300）。
	model := ""
	if task.Agent != nil && task.Agent.Model != "" {
		model = task.Agent.Model
	}
	if model == "" {
		model = entry.Model
	}

	taskLog.Info("starting agent",
		"provider", provider,
		"workdir", env.WorkDir,
		"model", model,
		"resume_reachable", resumeReachable,
	)
	if task.PriorSessionID != "" {
		taskLog.Info("resuming session", "session_id", task.PriorSessionID)
	}

	// 1. 运行前取一次基线：workdir 的完整文件快照，加上任务相关仓库的工作树清单。
	//    快照不再是 git 树快照——非 git 目录、被 ignore 的文件、未跟踪文件都要算进来，
	//    而 git 基线只用来回答"checkout 出来的那些文件本来就在，不算改动"。
	agentEnv["MULTICA_INPUT_BASELINES"] = filepath.Join(env.RootDir, "input-baselines", task.ID)
	agentEnv["MULTICA_INPUT_ISSUE_ID"] = task.IssueID
	agentEnv["MULTICA_INPUT_STARTED_AT"] = runStartedAt.UTC().Format(time.RFC3339Nano)
	capture := d.startRunCapture(ctx, task, env)
	defer func() {
		diff := capture.finish()
		if diff == nil {
			taskLog.Warn("code diff capture unavailable", "error", "capture not started")
			taskResult.CodeDiff = codediff.Unavailable("diff 采集未能开始")
			return
		}
		taskResult.CodeDiff = diff
		taskLog.Info("code diff captured",
			"roots", len(diff.Roots),
			"files", len(diff.Files),
			"truncated", diff.Truncated,
		)
	}()
	agentStartedAt := time.Now()
	taskLog.Info("agent preparation completed",
		"preparation_duration", agentStartedAt.Sub(runStartedAt).Round(time.Millisecond),
	)

	var customArgs []string
	// profileFixedArgs 刻意不进这里。它改走 agent.Config.LaunchPrefix，因为
	// ExtraArgs 只有 21 个 backend 中的 6 个会认，而且在认的那些里还落在协议标志
	// *之后*——于是包装器的子命令要么被丢在地上、要么被拼到 `-p` 后面（GH #7046）。
	extraArgs := defaultArgsForProvider(d.cfg, provider)
	var mcpConfig json.RawMessage
	if task.Agent != nil {
		customArgs = task.Agent.CustomArgs
		mcpConfig = effectiveMcpConfig
	}
	if hermesOverlayActive {
		// 上面已连同启动前缀一起剥掉。没有技能、没有 overlay 可保护的 hermes 任务
		// 保持标志原样。
		customArgs = hermesOverlayCustomArgs
	}
	thinkingLevel := ""
	serviceTier := ""
	if task.Agent != nil {
		thinkingLevel = task.Agent.ThinkingLevel
		serviceTier = task.Agent.ServiceTier
	}
	selection := resolveTaskModelSelection(ctx, provider, agent.NewCommand(entry.Path, profileFixedArgs),
		taskModelSelection{Model: model, ThinkingLevel: thinkingLevel, ServiceTier: serviceTier}, taskLog)
	model, thinkingLevel, serviceTier = selection.Model, selection.ThinkingLevel, selection.ServiceTier

	var idleWatchdogTimeout time.Duration
	if provider == "opencode" || provider == "codearts" {
		idleWatchdogTimeout = d.cfg.OpenCodeIdleWatchdog
	}
	if task.InteractionContinuation && (provider != "dsh" || task.PriorSessionID == "" || task.PriorWorkDir != env.WorkDir || os.Getenv("MULTICA_DSH_INTERACTIONS") != "1") {
		return TaskResult{}, errors.New("原生交互无法恢复，请启用 DSH 交互并恢复原会话后重试")
	}
	execOpts := agent.ExecOptions{
		RuntimeInputClaim:          &protocol.RuntimeInputClaim{RuntimeID: task.RuntimeID, DispatchedAt: task.DispatchedAt},
		RuntimeInputCapabilities:   runtimeInputCapabilities,
		HumanInteractions:          provider == "dsh" && os.Getenv("MULTICA_DSH_INTERACTIONS") == "1",
		PlanDecision:               task.PlanDecision,
		ApprovalPolicy:             "ask",
		EnableTaskSupplement:       taskSupplementNegotiated,
		Cwd:                        env.WorkDir,
		Model:                      model,
		ThreadName:                 deriveTaskThreadName(task),
		Timeout:                    d.cfg.AgentTimeout,
		SemanticInactivityTimeout:  d.cfg.CodexSemanticInactivityTimeout,
		FirstTurnNoProgressTimeout: d.cfg.CodexFirstTurnNoProgressTimeout,
		IdleWatchdogTimeout:        idleWatchdogTimeout,
		HandshakeTimeout:           d.cfg.CodexHandshakeTimeout,
		TurnInterruptTimeout:       d.cfg.CodexTurnInterruptTimeout,
		ThreadHandshakeTimeout:     d.cfg.CodexThreadHandshakeTimeout,
		Command:                    task.Command,
		PlanMode:                   task.PlanMode,
		NativeGoal:                 task.NativeGoal,
		CommandOnly:                runtimeCommandOnly(task.Command, task.ChatMessage),
		ResumeSessionID:            task.PriorSessionID,
		// 门控后的意图：这里的 PriorSessionID 已经反映过 pre-flight resume 闸门
		// （被丢弃的 resume 改经 prompt 浮出）。它能活到这里，backend 就必须在
		// 活体 resume 仍失败时披露丢失——即使跨过下面的 fresh-session 重试，后者会
		// 清掉 ResumeSessionID 但不清这个（MUL-4424）。
		//
		// 那段披露说些什么、是否对着用户说，取决于这个面的对话是否仍可读——只有
		// daemon 知道——所以交给 backend 的是成品文案而不是一个标志。prompt 已带
		// 通知时为空，一轮对话绝不会为此付两次（MUL-5722）。
		ResumeExpected:         task.PriorSessionID != "",
		ResumeContinuityNotice: backendResumeContinuityNotice(task),
		ExtraArgs:              extraArgs,
		CustomArgs:             customArgs,
		McpConfig:              mcpConfig,
		ThinkingLevel:          thinkingLevel,
		ServiceTier:            serviceTier,
		ClaudeSettingsPath:     env.ClaudeSettingsPath,
		CodebuddySettingsPath:  env.CodebuddySettingsPath,
		QwenpawWorkspace:       env.QwenpawWorkspace,
	}
	if taskCtx.CodebuddyBriefViaHook {
		execOpts.CodebuddyRuntimeBrief = runtimeBrief
	}
	if task.SupervisionPrompt != "" {
		if provider == "codex" || provider == "codebuddy" {
			execOpts.MCPToolsOnly = true
			execOpts.ResumeSessionID, execOpts.ResumeExpected = "", false
			execOpts.SystemPrompt = instructions
		}
		// 监督角色经原生指令投递，不依赖工作目录中的指令文件。
		execOpts.Timeout = 10 * time.Minute
		execOpts.McpConfig = json.RawMessage(`{}`)
		if len(task.SupervisionInvestigation) == 0 {
			task.SupervisionInvestigation = json.RawMessage(`[]`)
		}
		{
			config, sourcePrompt, setupErr := supervisionMCPConfig(task, env.RootDir, d.cfg.WorkspacesRoot, provider)
			if setupErr != nil {
				return TaskResult{}, fmt.Errorf("prepare supervision investigation: %w", setupErr)
			}
			execOpts.McpConfig = config
			execOpts.MaxTurns = 40
			var input map[string]json.RawMessage
			if err := json.Unmarshal([]byte(prompt), &input); err != nil {
				return TaskResult{}, fmt.Errorf("decode supervision input: %w", err)
			}
			var sources map[string]json.RawMessage
			if err := json.Unmarshal([]byte(sourcePrompt), &sources); err != nil {
				return TaskResult{}, fmt.Errorf("decode supervision sources: %w", err)
			}
			input["investigation_sources"] = sources["investigation_sources"]
			body, err := json.Marshal(input)
			if err != nil {
				return TaskResult{}, err
			}
			prompt = string(body)
		}
	}
	// 有些 provider 无法可靠地加载我们写进任务 workdir 的每任务 runtime 配置文件。
	// Kimi 套在自己 CLI 后面、其 cwd 处理不透明，连基于文件的路径也不可信。
	// 把完整 runtime brief 内联传入（CLI 目录 + workflow 步骤 + agent 身份/人设 +
	// 技能 + project 上下文），backend 就会前置同样一份"基于文件的 runtime 会从盘
	// 上捡起来"的载荷。没有它，这些 provider 会静默错过 workflow 段、从不调
	// `multica issue status` / `multica issue comment add`，issue 会一直卡在 `todo`。
	//
	// Hermes 与 Kiro 刻意排除：它们的 ACP session 在任务 cwd 启动、自己加载
	// AGENTS.md。Kiro 文档说 root AGENTS.md 总是被包含，真实 kiro-cli 2.13.0 ACP
	// 冒烟也证实了。把完整 runtime brief 前置进 ACP 用户 prompt 会重复那段上下文、
	// 每轮都臃肿。
	if providerNeedsInlineSystemPrompt(provider) {
		execOpts.SystemPrompt = runtimeBrief
	}
	if (provider == "codex" || provider == "codebuddy") && task.SupervisionPrompt == "" && task.ReviewCommit == "" && task.LifeJobID == "" && (task.ChatSessionID != "" || task.IssueID != "") {
		execOpts.RequestInteraction = d.client.taskInteractionRequester(task.ID, provider)
	}

	// 来自"早于服务器侧生成"服务器的 quick-actions 刷新任务（MUL-5573）。这个
	// daemon 已经没有可拿来跑它的建议通道，而且它绝不能掉进下面的普通 chat 路径：
	// 任务不携带任何用户消息，agent 会回答一段没人写的 prompt，那个服务器还会把
	// 结果持久化成一条真实的助手回复。改为空完成——与退役通道在这种任务上产出的
	// 形态一致，那个服务器不会为它写任何行。用户的刷新转圈由客户端自己的超时解除。
	if task.RegenerateQuickActionsFor != "" {
		taskLog.Warn("refusing quick-actions refresh task from an older server; complete the daemon upgrade by updating the server",
			"target_task", task.RegenerateQuickActionsFor,
		)
		return TaskResult{Status: "completed", Comment: "", WorkDir: env.WorkDir, EnvRoot: env.RootDir}, nil
	}

	// 用与子进程相同的任务级 token 鉴权 localhost repo-checkout 端点。端点从这个
	// 内存记录推导身份与分支归属，而不是信任请求字段或环境里的进程环境。只在
	// provider 执行窗口内注册，之后总是移除该凭据。
	d.registerActiveRepoCheckoutTask(agentToken, activeRepoCheckoutTask{
		WorkspaceID: task.WorkspaceID,
		TaskID:      task.ID,
		AgentID:     task.AgentID,
		AgentName:   task.Agent.Name,
		WorkDir:     env.WorkDir,
	})
	defer d.clearActiveRepoCheckoutTask(agentToken)

	taskLog.Debug("invoking backend",
		"provider", provider,
		"model", model,
		"prompt_bytes", len(prompt),
		"custom_args", len(customArgs),
		"extra_args", len(extraArgs),
		"mcp_config", len(mcpConfig) > 0,
		"inline_system_prompt", execOpts.SystemPrompt != "",
		"resume_session", execOpts.ResumeSessionID != "",
		"timeout", execOpts.Timeout,
		"idle_watchdog", execOpts.IdleWatchdogTimeout,
	)

	// 供下面的 resume 重试共享，让重试的 transcript 行给同一任务持续用递增的 seq。
	var msgSeq atomic.Int32
	// 1. Bind local evidence to this process interval, including failure and fresh-session retries.
	evidenceStarted := time.Now()
	result, tools, err := d.executeAndDrain(ctx, backend, prompt, execOpts, taskLog, task.ID, env.CodexHome, &msgSeq)
	d.reportExecutionEvidence(task, provider, agentEnv, env.WorkDir, result, evidenceStarted, execOpts.ResumeSessionID != "", &msgSeq)
	if err != nil {
		// 执行失败（含被服务器取消、进程被杀）时，backend 往往已经跑过若干回合、
		// 累计了真实 token 和根请求 id。返回空 TaskResult 会让这些用量和链路在下面
		// 的上报点变成"无"——生产里确有取消的执行在 AgentLens 留下数百万 input
		// token，而 task_usage 一条都没有。这里把已观测到的部分随错误一起带出去，
		// 状态不参与决策（调用方只看 err），只用于记账。
		return TaskResult{
			Status:           "failed",
			SessionID:        result.SessionID,
			Usage:            buildTaskUsageEntries(provider, result.Usage),
			ProviderTraceIDs: traceListForResult(result),
		}, err
	}

	// retiredSessionID 是本次运行被告知要 resume、随后被放弃的那个 session。在重试
	// 清掉 task.PriorSessionID 之前捕获，并在"每一条"终态路径上报——重试成功恰是
	// 那个被弃 id 原本会存活下来的时刻：本任务行上无人引用它，却仍能经 issue 上更
	// 早的完成行、或 chat_session 指针够到它（GH #6066）。
	var retiredSessionID string
	defer func() { taskResult.RetiredSessionID = retiredSessionID }()

	// providerTraceIDs 收集本次执行里每个真正跑过的子进程自报的根请求 id，并把每个
	// id 与该子进程自己的 session 成对保留。换新会话重试会起第二个进程，于是同一次
	// 执行对应两条链路、两个 session；只有一条时是常态，子进程没起来（或 backend 不
	// 暴露）时是零条。不能只在最后用 result.SessionID 统一补齐——重试成功时那是第二个
	// 进程的 session，会把首进程的 trace 记到别人的会话上。
	providerTraceIDs := traceListForResult(result)
	defer func() { taskResult.ProviderTraceIDs = providerTraceIDs }()

	if backendConfig.CodexResumePath == "" && !task.InteractionContinuation && shouldRetryWithFreshSession(result, task.PriorSessionID, tools, provider) {
		firstResult := result
		firstUsage := result.Usage
		firstTools := tools
		if !result.ResumeRejectedTransient {
			retiredSessionID = task.PriorSessionID
		}
		taskLog.Warn("session resume failed, retrying with fresh session", "error", result.Error)

		// 在唯一一次重试前重建"冷 session"上下文。先前的 provider transcript 没了
		// （缺失、账号不匹配、或——GH #5975——携带了 provider 现在拒绝的历史），
		// 所以新进程绝不能被告知"它在 resume 一段对话"：
		//   - taskCtx.PriorSessionResumed=false + 重新注入 runtime brief 重写盘上的
		//     AGENTS.md，让它不再声称 "You're resuming the prior session"（基于文件
		//     的 backend，如 Kiro，会自己加载它）。
		//   - 清掉 task.PriorSessionID，让 prompt 走"冷评论阅读"路径而不是"温
		//     resume"路径重建。
		//   - PriorSessionResumeUnavailable=true 让 BuildPrompt 给这个面追加连续性
		//     通知，agent 才知道别假设自己已不再拥有的连续性。它现在是重试路径上
		//     唯一的注入者：backend 自己的那份在下面被抑制，因为 MUL-5722 之前两者
		//     都会触发、同一段话被带了两遍。
		// task 与 taskCtx 都是本地的（runTask 按值拿 task），所以这些改动只影响重试。
		execOpts.ResumeSessionID = ""
		task.PriorSessionID = ""
		task.PriorSessionResumeUnavailable = true
		execOpts.ResumeContinuityNotice = ""
		taskCtx.PriorSessionResumed = false
		if freshBrief, briefErr := execenv.InjectRuntimeConfig(env.WorkDir, provider, taskCtx); briefErr != nil {
			if taskCtx.CodebuddyBriefViaHook || taskCtx.CodexBriefViaAPI {
				return TaskResult{Status: "failed", SessionID: firstResult.SessionID, Usage: buildTaskUsageEntries(provider, firstUsage)}, fmt.Errorf("prepare fresh native runtime instructions: %w", briefErr)
			}
			taskLog.Warn("execenv: re-inject cold runtime config for fresh retry failed (non-fatal)", "error", briefErr)
		} else {
			runtimeBrief = freshBrief
			if taskCtx.CodebuddyBriefViaHook {
				execOpts.CodebuddyRuntimeBrief = runtimeBrief
			}
			if taskCtx.CodexBriefViaAPI {
				execOpts.SystemPrompt = runtimeBrief
			}
			if taskCtx.CodebuddyBriefViaHook || taskCtx.CodexBriefViaAPI {
				if err := execenv.WriteRuntimeProvenance(env.RootDir, env.WorkDir, provider, runtimeBrief, taskCtx); err != nil {
					taskLog.Warn("write fresh native instruction provenance failed", "error", err)
				}
			}
			if providerNeedsInlineSystemPrompt(provider) {
				execOpts.SystemPrompt = runtimeBrief
			}
		}
		freshPrompt := BuildPrompt(task, provider, promptOptions...)

		// 2. A replacement session gets its own source and interval rather than inheriting the rejected session.
		evidenceStarted = time.Now()
		retryResult, retryTools, retryErr := d.executeAndDrain(ctx, backend, freshPrompt, execOpts, taskLog, task.ID, env.CodexHome, &msgSeq)
		d.reportExecutionEvidence(task, provider, agentEnv, env.WorkDir, retryResult, evidenceStarted, false, &msgSeq)
		if retryErr != nil {
			taskLog.Error("fresh session also failed to start; keeping the original poisoned result", "error", retryErr)
		} else if retryResult.Status != "completed" && retryResult.SessionID == "" {
			taskLog.Warn("fresh session retry also failed without establishing a new session; keeping the original poisoned result",
				"retry_status", retryResult.Status,
				"retry_error", retryResult.Error,
			)
		}
		// 被毒化的 prior session id 只活在 firstResult 上（被分类为不可恢复，所以
		// GetLastTaskSession 排除它）。reconcile 从不把它嫁接到重试结果上：建立新
		// session 的重试以自己的 id 胜出；没有新 session 就失败的重试保留 firstResult，
		// 让坏 session 保持被排除，而不是被一条看起来良性的二次错误重新贴回可恢复。
		result, tools = reconcileFreshRetryResult(firstResult, firstUsage, firstTools, retryResult, retryTools, retryErr)
		// retryErr 非空时重试进程仍可能已经发出过请求，trace id 照样要留；空值会被
		// appendTraceID 忽略，重复值不会重复记。session 用重试进程自己的，不是合并后的
		// result.SessionID——两者相同只是因为重试成功了，逻辑上仍属于那个进程。
		// 重试是第二个进程：它的父链路与子链路都按自己的结果追加，字段整条保留。
		providerTraceIDs = mergeTraceLists(providerTraceIDs, traceListForResult(retryResult))
	}
	phaseRecorder.Mark(taskPhaseTurnCompleted)

	elapsed := time.Since(agentStartedAt).Round(time.Second)
	taskLog.Info("agent finished",
		"status", result.Status,
		"duration", elapsed.String(),
		"preparation_duration", agentStartedAt.Sub(runStartedAt).Round(time.Millisecond),
		"total_duration", time.Since(runStartedAt).Round(time.Millisecond),
		"tools", tools,
	)
	taskLog.Debug("agent result detail",
		"status", result.Status,
		"output_bytes", len(result.Output),
		"session_id", result.SessionID,
		"models_with_usage", len(result.Usage),
		"agent_error", result.Error,
	)

	// 把 agent 用量表转成任务用量条目。
	usageEntries := buildTaskUsageEntries(provider, result.Usage)

	// MUL-5305：对"rollout 从没到达每 issue store"的 Codex session 一律扣下 resume
	// 指针，任何终态都算——包括 `completed`，因为"完成的轮次却缺 rollout"正是
	// #5934 情形，绝不能把它记成下一个 follow-up 只会丢弃的 resume 指针。清空 id
	// 让 GetLastTaskSession 回退到"rollout 真实存在"的最后那个 session；
	// SessionRolloutMissing 告诉服务器清掉该行的 session 并记录一段连续性缺口，于是
	// 下一次认领即便 resume 那枚更旧的健康 session，也仍会披露这次丢失
	// （PriorSessionResumeUnavailable，MUL-4424 透明性）。非 Codex provider
	// （env.CodexHome == ""）以及没有 session 时是 no-op。
	var sessionRolloutMissing bool
	if result.SessionID != "" && !codexSessionResumable(env.CodexHome, result.SessionID, codexRolloutFlushWait) {
		taskLog.Warn("codex session rollout not present in task CODEX_HOME; withholding resume pointer and flagging continuity gap",
			"session_id", result.SessionID, "codex_home", env.CodexHome, "status", result.Status)
		result.SessionID = ""
		sessionRolloutMissing = true
	}
	// 把扣下标志盖到下面状态切换返回的那个 TaskResult 上（SessionID 上面已清空）；
	// reportTaskResult 会把它作为 session_rollout_missing 转发到终态回调（MUL-5305）。
	defer func() { taskResult.SessionRolloutMissing = sessionRolloutMissing }()

	switch result.Status {
	case "waiting_plan", "waiting_input":
		if result.PendingInteraction == nil {
			return TaskResult{}, errors.New("DSH interaction result is missing its prompt")
		}
		return TaskResult{Status: result.Status, Comment: result.Output, SessionID: result.SessionID, WorkDir: env.WorkDir,
			EnvRoot: env.RootDir, Usage: usageEntries, PendingInteraction: result.PendingInteraction}, nil
	case "completed":
		if result.Output == "" {
			// agent 成功完成却没产出文本。这是合法的——agent 可能全程都靠工具调用
			// 干活（比如经 CLI 发评论、推代码）。按正常完成对待，任务就不会被错误
			// 标成 blocked。
			return TaskResult{
				Status:    "completed",
				Comment:   "",
				SessionID: result.SessionID,
				WorkDir:   env.WorkDir,
				EnvRoot:   env.RootDir,
				Usage:     usageEntries,
			}, nil
		}
		// 探测"被毒化"的终态输出：agent 没达成真正的结论，却吐出了一个已知兜底
		// 标记（迭代上限、兜底 meta 消息）。走带特定 failure_reason 的 blocked 路径，
		// 服务器才能把这个 session 从 (agent_id, issue_id) resume 查找里排除——否则
		// 手动重跑会继承同一枚被毒化 session、重现同样的坏输出。
		if reason, ok := classifyPoisonedOutput(result.Output); ok {
			taskLog.Warn("agent finished with poisoned fallback output, classifying as blocked",
				"failure_reason", reason,
			)
			return TaskResult{
				Status:        "blocked",
				Comment:       result.Output,
				SessionID:     result.SessionID,
				WorkDir:       env.WorkDir,
				EnvRoot:       env.RootDir,
				Usage:         usageEntries,
				FailureReason: reason,
			}, nil
		}
		taskResult = TaskResult{
			Status:    "completed",
			Comment:   result.Output,
			SessionID: result.SessionID,
			WorkDir:   env.WorkDir,
			EnvRoot:   env.RootDir,
			Usage:     usageEntries,
		}
		return taskResult, nil
	case "timeout":
		// 带出 session_id/work_dir，让 chat resume 指针在"agent 建好 session 后才
		// 超时"时也保持同步。标成 "blocked"（而不是返回硬错误），让 handleTask 走
		// 那条转发 session 信息的 FailTask 路径。
		comment := result.Error
		if comment == "" {
			comment = fmt.Sprintf("%s timed out after %s", provider, d.cfg.AgentTimeout)
		}
		failureReason := "timeout"
		if reason, ok := classifyResumeUnsafeTimeout(provider, comment); ok {
			taskLog.Warn("agent timed out with resume-unsafe session, classifying as blocked",
				"failure_reason", reason,
			)
			failureReason = reason
		}
		return TaskResult{
			Status:        "blocked",
			Comment:       comment,
			SessionID:     result.SessionID,
			WorkDir:       env.WorkDir,
			EnvRoot:       env.RootDir,
			FailureReason: failureReason,
			Usage:         usageEntries,
		}, nil
	case "idle_watchdog":
		// 空闲看门狗强停了运行，因为 backend 静默了（例如 claude 卡在对冻结子进程
		// 的一次工具调用上）。走带专用 failure_reason 的 blocked 路径，让运行离开
		// "running" 状态、运维能区分"空闲停止"与真超时。
		comment := result.Error
		if comment == "" {
			comment = idleWatchdogReason(d.cfg.AgentIdleWatchdog)
		}
		return TaskResult{
			Status:        "blocked",
			Comment:       comment,
			SessionID:     result.SessionID,
			WorkDir:       env.WorkDir,
			EnvRoot:       env.RootDir,
			FailureReason: "idle_watchdog",
			Usage:         usageEntries,
		}, nil
	case "cancelled":
		// 服务器取消了任务（如 issue 重新分配、用户取消）。handleTask 的
		// cancelledByPoll 分支已会丢弃这个结果，所以此分支主要是防御性的——并且为
		// "agent finished" 日志行保留 "cancelled" 状态串，让运维能区分"被服务器取消"
		// 与真正的超时。
		return TaskResult{
			Status:    "cancelled",
			SessionID: result.SessionID,
			WorkDir:   env.WorkDir,
			EnvRoot:   env.RootDir,
			Usage:     usageEntries,
		}, nil
	default:
		errMsg := result.Error
		if errMsg == "" {
			errMsg = fmt.Sprintf("%s execution %s", provider, result.Status)
		}
		// 在 blocked 路径上转发 SessionID/WorkDir：backend 常在失败前（限流、工具
		// 错误、模型拒绝……）就吐出一个真实 session_id。没有它，chat_session 的
		// resume 指针要么保持陈旧、要么被服务器覆写成 NULL，下一次 chat 轮次就会
		// 丢上下文。
		//
		// 用专用 failure_reason 分类上游 API 400 invalid_request_error 失败，让
		// GetLastTaskSession 把该任务从 (agent_id, issue_id) resume 查找中排除。没有
		// 这个分类器，一张烤进对话的坏图或超大载荷会永久卡死这个 issue：每次
		// follow-up 都 resume 同一枚被毒化的 session、撞上同一个 400。
		failureReason, _ := classifyPoisonedError(errMsg)
		if failureReason == "" {
			// 一次读不回来的 resume 会把同一枚超大线程记成这个 issue 的 resume 指针。
			// 走到这里说明"轮内 fresh-session 重试"没救回这次运行（它以 tools == 0
			// 为门，也可能自己失败），所以分类它、让下一条任务绕开那条线程，而不是
			// 永远重放过载（MUL-5722）。
			failureReason, _ = classifyResumeUnsafeTransport(provider, errMsg)
			if failureReason != "" && retiredSessionID == "" && task.PriorSessionID != "" {
				// 指名那条线程。失败发生在轮次开始之前，backend 没有 session id 可
				// 上报，这行会以 session_id NULL 落地——于是上面的 reason 或任何针对
				// 这行的错误文本过滤，都无法指出"要避开哪个 session"。
				// retired_session_id 是唯一不依赖失败行携带 session 的通道，resume
				// 查找与 chat 指针清理都以它为键。
				//
				// 双保险，而非活路径：过载 resume 在任何工具跑之前就失败，所以
				// shouldRetryWithFreshSession 的 tools == 0 门总是满足，上面的重试已经
				// 记过同一个 id。这里覆盖"将来某条件拦住重试不触发"的情形，session
				// 仍被退役、而不是被悄悄留着。
				retiredSessionID = task.PriorSessionID
			}
		}
		if failureReason != "" {
			taskLog.Warn("agent failed with a resume-unsafe error, retiring the session",
				"failure_reason", failureReason,
			)
		} else {
			// MUL-2946：classifyPoisonedError 只匹配"毒化 session 的 Anthropic 400"
			// 形态。其它一切落到 taskfailure.Classify，把裸错误串映射到 14 个
			// agent_error.* 子原因之一（provider 鉴权、容量、上下文过载、runner 崩
			// 溃……）或 ReasonAgentUnknown。这让 failure_reason 列在写入时刻就待在
			// 规范精细分类里，而不是等 MUL-1949 的离线回填事后重新分类。
			failureReason = taskfailure.Classify(errMsg).String()
		}
		// 在上面的分类器读完 errMsg 之后做。这段提示是固定的散文、刻意匹配不上任何
		// resume 护栏（见其 const），所以顺序不是它安全的原因——但这样机器决策读的
		// 恰好是 runtime 报告的原样，而批注留在外面，将来任何编辑都能明显看出是在改
		// 给人看的文案、而不是在改分类器输入。
		errMsg = annotateHermesProviderUnconfigured(errMsg, provider, env.HermesHome != "")
		errMsg = annotateCodexRetiredCompaction(errMsg, provider)
		return TaskResult{
			Status:        "blocked",
			Comment:       errMsg,
			SessionID:     result.SessionID,
			WorkDir:       env.WorkDir,
			EnvRoot:       env.RootDir,
			Usage:         usageEntries,
			FailureReason: failureReason,
		}, nil
	}
}

// shouldRetryWithFreshSession 报告一次失败的、请求了 --resume 的运行是否应该用
// 全新 session 再试一次。
//
// 两个独立问题都必须同时答"是"，把它们混为一谈正是以前翻车的原因：
//
//  1. 新 session 真能修好它吗？只有当 resume 本身被拒绝时才可能——永久拒绝是因为
//     transcript 消失或属于另一个 provider 账号，瞬时拒绝是因为另一个活着的运行
//     拥有它。result.ResumeRejected 与 result.ResumeRejectedTransient 是 backend 对
//     这两种情形的正面证据。两者都允许全新重试，但只有永久信号会让 prior session
//     从之后的查找中退休。
//     单靠排除法回答会把举证责任倒过来：新 session 能治愈的失败是一小撮可枚举的
//     集合，不能治愈的却是开放式的。网络掉线、429、配额用尽或 provider 5xx 与
//     session 毫无关系，重置它等于丢弃唯一可恢复的东西——对话指针——并为毫无意义
//     的重新执行付账。特别是 provider_network 在 internal/service/task.go 里被记为
//     resume-safe（retryableReasons，MUL-4910）：平台的自身重试本应继承该 session
//     并继续被截断的对话。
//
//     不过不是每个 backend 都能回答，而假 ResumeRejected 的含义取决于谁制造的：
//     "检查过、不是拒绝"出自有能力的 backend，"无法判断"出自
//     agent.ResumeRejectionUndetectable 里的 backend。这就是 provider 作为参数的
//     原因——没有它，下面的兼容分支会静默应用于每个 backend，用排除法去二猜
//     有能力的 backend。
//
//  2. 重跑安全吗？只有当 agent 没有执行任何工具时才安全。tools == 0 并不能证明
//     运行未改变任何东西——它只证明我们没观察到工具使用，而这是此处可得的最强
//     信号——但它正是重试与复制副作用之间的那道闸。评论创建没有幂等键，重复会
//     再次触发其 @mention；重试也复用同一 workdir，它绝不在两次尝试间重置，因此
//     在真实工作之后的重试会把自己半成品的提交当作重新规划的底子。
func shouldRetryWithFreshSession(result agent.Result, priorSessionID string, tools int32, provider string) bool {
	if result.Status != "failed" || priorSessionID == "" || tools > 0 {
		return false
	}
	// 正面证据：backend 证明了 resume 被拒。
	if result.ResumeRejected || result.ResumeRejectedTransient {
		return true
	}
	// 另一种正面证据：resume 没被拒——transcript 正常加载——provider 随后却因为
	// 其中某条消息为空而拒绝重放。这里所有 backend 的 ResumeRejected 都是 false，
	// 恰是因为没有任何东西拒绝 resume——所以它需要自己的分支，而不是往拒绝列表里
	// 加一句短语。
	//
	// 它适用于全部 18 个 backend，而不是下面的 ResumeRejectionUndetectable 子集，
	// 而且这是刻意的：这是唯一一种"无需 backend 探测任何东西、丢 session 也被证明
	// 是修复"的失败类。证据就在 provider 自己的错误文本里，公共层已经有了。把它留
	// 给每个 adapter 去搞，正是同一个 bug 在 Kiro、Kimi、Anthropic 上各修一遍、
	// 其它 backend 却一直坏着的原因。
	//
	// 上面的 tools == 0 门仍原样适用——已经用过工具的运行绝不重跑，不管历史是否
	// 被毒化。这样的任务仍会退役它的 session，只是改由上报时的 classifyPoisonedError
	// 来做，而不是轮内重试。
	if taskfailure.UnresumableHistory(result.Error) {
		return true
	}
	// 第三种正面证据，同款论证形态：resume 没被拒——runtime 愉快地重建了 session
	// ——但它重建出的 provider 身份已经解析不了自己的凭据，于是这次轮次以 "Could
	// not resolve authentication method"（GH #6777）死掉。凭据没问题；坏掉的只是
	// session 里那份 provider 副本，而全新 session 恰好会从当前配置重新解析它。
	//
	// 这就是 Result.ResumeRejected 的 doc 点名的例外：adapter 绝不能把鉴权错误标记
	// 上去，因为一次真实的凭据失败要保留 session、让平台自身重试能续上对话。区分
	// 点是"resume 还是全新"，不是错误文本——而上面的 priorSessionID 已经确立了
	// "这次运行确实是一次 resume"。冷跑上同样的错误意味着配置真有问题，这道门根本
	// 看不到它。
	//
	// 在这里而不是在每个 ACP adapter 里做决定，才能对 ACP 生命周期的每一步都正确：
	// 失败浮现在 session/resume、session/set_model（resumed session 中被规范化的
	// provider 会收到一次冗余 set_model、被重路由到错误的 provider——MUL-5029）或
	// session/prompt，三者里只有两个今天携带 resume-failure 信号。最终错误文本在
	// 全部三处都带着那句短语。
	//
	// 最坏情形是配置真的坏了：全新尝试同样失败、用户同样只看到一次错误，单次重试
	// 预算封住了代价。这与上面那个分支已经付出的权衡相同。
	if taskfailure.AuthMethodUnresolved(result.Error) {
		return true
	}
	// 以下都是给"完全答不了问题 1"的 backend 的有界兼容路径。对其它每个 backend，
	// 假 ResumeRejected 是一个真实答案——它检查过、这不是拒绝——所以这道门在这里
	// 停住，而不是用排除法去二猜它。
	if !agent.ResumeRejectionUndetectable(provider) {
		return false
	}
	// antigravity、codearts、copilot、cursor、deveco 与 opencode 从流输出里刮
	// SessionID、到处都没捕获拒绝串，所以空 SessionID 是它们唯一能给的。它证明本次
	// 运行没建立 session——正是 ResumeRejected 存在之前这道门依赖的每 backend 信号；
	// 保留它就是在保留它们的恢复路径，而不是静默移除。
	//
	// 替这些 backend 编拒绝短语是另一种选择，而且更糟：它们没有一个捕获过真实输出，
	// 一次误报会丢弃一枚可恢复的 session 指针。
	return result.SessionID == "" && freshSessionMayHelp(result.Error)
}

// reconcileFreshRetryResult 在单次全新 session 重试之后挑选权威结果（见 runTask
// 里的重试块）。它的一条硬性不变式：被毒化的 prior session id——只存在于 `first`，
// 其失败被分类为不可恢复，因此 GetLastTaskSession 会排除它——绝不能嫁接到重试结果
// 上，否则后来的任务会再次恢复坏 session、重新形成本修复要打破的循环（GH #5975
// review）。
//
//   - retryErr != nil：全新尝试从未产出结果。保留 `first`，让被毒化的 session 保持
//     记录为不可恢复。
//   - retry 建立了新 session id：它完全胜出，携带它自己的 id。新 session 上良性的
//     重试失败可以如实记录——它不是被毒化的那个。
//   - retry 完成但没有 session id（例如全走工具）：采纳它，但保持 id 为空。绝不把
//     被毒化的 id 复活成可恢复的成功。
//   - retry 失败且未建立新 session：保留 `first`。这里采纳第二个结果正是那个 bug——
//     缺少超大图标记的第二个错误会被分类为 resume-safe，被毒化的 id（若附着）会被
//     重新选中。我们保留不可恢复的第一个结果，只合并用量。
//
// 每个分支都跨两次尝试合并用量，保证计费完整。
func reconcileFreshRetryResult(first agent.Result, firstUsage map[string]agent.TokenUsage, firstTools int32, retry agent.Result, retryTools int32, retryErr error) (agent.Result, int32) {
	switch {
	case retryErr != nil:
		first.Usage = firstUsage
		return first, firstTools
	case retry.SessionID != "":
		retry.Usage = mergeUsage(firstUsage, retry.Usage)
		return retry, retryTools
	case retry.Status == "completed":
		retry.Usage = mergeUsage(firstUsage, retry.Usage)
		return retry, retryTools
	default:
		first.Usage = mergeUsage(firstUsage, retry.Usage)
		return first, firstTools
	}
}

// freshSessionMayHelp 报告重启对话是否可能修复 errText。它回答"这个失败到底跟
// session 有没有关系？"，因此每个具有明确非 session 解药的原因——等待、退避、
// 充值、重新认证、修配置、装二进制——都被排除。这些保留 session 指针，让平台
// 自身的重试能恢复被截断的对话。
//
// 放行的是刻意收窄的集合：unknown、进程失败与不可解析的输出。来自这些 backend 的
// 真实 resume 拒绝最可能正好以那种形态浮出——非零退出或解析不了的输出——因为它们
// 没有一个会显式上报拒绝。context overflow 也被放行，因为从头开始确实能清掉它。
func freshSessionMayHelp(errText string) bool {
	switch taskfailure.Classify(errText) {
	case taskfailure.ReasonAgentProviderNetwork,
		taskfailure.ReasonAgentProviderCapacityOrRateLimit,
		taskfailure.ReasonAgentProviderQuotaLimit,
		taskfailure.ReasonAgentProviderServerError,
		taskfailure.ReasonAgentProviderAuthOrAccess,
		taskfailure.ReasonAgentMissingConfig,
		taskfailure.ReasonAgentModelNotFoundOrUnavailable,
		taskfailure.ReasonAgentRuntimeMissingExecutable,
		taskfailure.ReasonAgentRuntimeVersionUnsupported,
		// 防御性：超时通常自带终态状态、从不会走到这道门；但如果它真从某条
		// "failed" 结果里被分类出来，把整任务重跑一遍不是答案。
		taskfailure.ReasonAgentTimeout:
		return false
	default:
		return true
	}
}

// executeAndDrain 运行 backend、排空它的消息流（转发给服务器），并等待最终结果。
// msgSeq 为上报的任务消息编号，归调用方所有，让同任务的 retry 延续序列而不是从 1
// 重来——服务器只按 seq 排序 transcript，重复 seq 会把两次尝试的行交错在一起。
func (d *Daemon) executeAndDrain(ctx context.Context, backend agent.Backend, prompt string, opts agent.ExecOptions, taskLog *slog.Logger, taskID, codexHome string, msgSeq *atomic.Int32) (agent.Result, int32, error) {
	phaseRecorder := taskPhaseRecorderFromContext(ctx)
	// Wrap the caller's ctx so the idle watchdog (below) can interrupt both
	// the agent subprocess (via the ctx passed to backend.Execute) AND the
	// drain loop with a single cancel. Without this layer the backend would
	// stay tied to the parent ctx and our cancellation could only abort
	// drain, leaving the subprocess running.
	agentCtx, agentCancel := context.WithCancel(ctx)
	defer agentCancel()

	session, err := backend.Execute(agentCtx, prompt, opts)
	if err != nil {
		// 一个与 provider 无关的启动边界：每个 backend 的 cmd.Start() 失败都汇聚
		// 到这里，因此在 claude、opencode 以及之后新增的任何 CLI 上诊断 ENOEXEC，
		// 都不必在每个 backend 里包一层（MUL-6164）。
		err = agent.ExplainExecError(err)
		taskLog.Debug("backend execute returned error", "error", err)
		return agent.Result{}, 0, err
	}
	// 该计数刻意从更窄的"provider 会话"边界开始，而不是更早的服务器侧 StartTask
	// 转换处。
	d.runningTasks.Add(1)
	defer d.runningTasks.Add(-1)
	backendStartedAt := time.Now()
	var firstEventLogged atomic.Bool
	phaseRecorder.Mark(taskPhaseRuntimeStarted)
	taskLog.Debug("backend started, draining messages")
	if opts.RuntimeInputClaim != nil && len(opts.RuntimeInputCapabilities) > 0 {
		inputCtx, stopInputs := context.WithCancel(agentCtx)
		inputsDone := make(chan struct{})
		go func() {
			defer close(inputsDone)
			d.runTaskRuntimeInputLoop(inputCtx, session, taskID, *opts.RuntimeInputClaim, opts.RuntimeInputCapabilities, taskLog)
		}()
		defer func() {
			stopInputs()
			<-inputsDone
		}()
	}

	// Only negotiated sessions may claim additions. Stop and join delivery
	// before the caller reports the task's terminal state to the server.
	if opts.EnableTaskSupplement && session.Supplement != nil && session.SupplementReady != nil {
		supplementCtx, cancelSupplements := context.WithCancel(agentCtx)
		supplementsDone := make(chan struct{})
		wakeup, unsubscribe := d.taskSupplementSignals.subscribe(taskID)
		go func() {
			defer unsubscribe()
			defer close(supplementsDone)
			d.runTaskSupplementLoop(supplementCtx, session, taskID, wakeup, taskLog)
		}()
		defer func() {
			cancelSupplements()
			<-supplementsDone
		}()
	}

	// Bound the drain loop only when there is a wall-clock cap. With a positive
	// opts.Timeout, give the drain a slightly longer deadline than the backend
	// so it can still collect the backend's own timeout Result if the scanner
	// is stuck on a hung stdout pipe (the extra 30 s covers cleanup after the
	// backend's own deadline fires). With no cap (opts.Timeout <= 0) the
	// inactivity watchdog is the only liveness net, so the drain must NOT
	// impose its own deadline either — otherwise an actively streaming long run
	// would be cut off here regardless of progress (MUL-3064).
	var drainCtx context.Context
	var drainCancel context.CancelFunc
	if opts.Timeout > 0 {
		drainCtx, drainCancel = context.WithTimeout(agentCtx, opts.Timeout+30*time.Second)
	} else {
		drainCtx, drainCancel = context.WithCancel(agentCtx)
	}
	defer drainCancel()

	// 取消后继续消费已有消息，直到末结果回收结束，避免 backend 因消息管道满而无法退出。
	messageCtx, stopMessages := context.WithCancel(context.WithoutCancel(ctx))
	defer stopMessages()

	var toolCount atomic.Int32
	// lastActivityAt 记录（纳秒）排空循环最近一次从 backend 收到消息的时间。
	// 空闲看门狗读它来决定 agent 是否静默过久。初始化为起点，这样从不发任何
	// 消息的 backend 也会触发看门狗。
	var lastActivityAt atomic.Int64
	lastActivityAt.Store(time.Now().UnixNano())
	// inFlightTools 统计尚未配对 tool_result 的 tool_use 消息数。非零表示 agent
	// 正在合法地等待某工具（如 `npm install`、`docker build`），它可能跑得远久于
	// 空闲窗口而不发任何消息——因此工具在途时，看门狗用更大的 AgentToolWatchdog
	// 预算，而不是把这种静默当成挂起。
	var inFlightTools atomic.Int32
	var idleWatchdogFired atomic.Bool
	// idleWatchdogThreshold 记录（纳秒）实际触发看门狗的静默预算——空闲窗口还是
	// 更大的工具在途窗口——让失败消息报告真实时长。
	idleWindow := d.cfg.AgentIdleWatchdog
	// provider 可以选用更短的每次运行无消息预算。全局零值仍权威，保证
	// MULTICA_AGENT_IDLE_WATCHDOG=0 仍能禁用整套看门狗。工具调用继续用
	// AgentToolWatchdog。
	if idleWindow > 0 && opts.IdleWatchdogTimeout > 0 && opts.IdleWatchdogTimeout < idleWindow {
		idleWindow = opts.IdleWatchdogTimeout
	}
	var idleWatchdogThreshold atomic.Int64
	idleWatchdogThreshold.Store(int64(idleWindow))
	if idleWindow > 0 {
		go d.runIdleWatchdog(agentCtx, idleWindow, d.cfg.AgentToolWatchdog, &lastActivityAt, &inFlightTools, &idleWatchdogFired, &idleWatchdogThreshold, agentCancel, session.Messages, taskLog, session.WaitingForInput)
	}

	// drainFinished 在排空 goroutine 冲刷完最后一批消息后关闭，让下面的结果交接
	// 可以等待 transcript 尾部被持久化。
	drainFinished := make(chan struct{})
	go func() {
		defer close(drainFinished)
		var mu sync.Mutex
		var pendingText strings.Builder
		var pendingThinking strings.Builder
		var pendingTextAt time.Time
		var pendingThinkingAt time.Time
		var batch []TaskMessageData
		callIDToTool := map[string]string{}
		callIDStartedAt := map[string]time.Time{}
		// Provider IDs can restart on a same-task retry (for example item_0).
		// Allocate opaque transcript IDs per execution, including orphan results,
		// so neither a retry nor a missing call can steal another call's result.
		transcriptCallIDs := map[string]string{}
		transcriptCallID := func(providerID string) string {
			if providerID == "" {
				return ""
			}
			if id, ok := transcriptCallIDs[providerID]; ok {
				return id
			}
			id := uuid.NewString()
			transcriptCallIDs[providerID] = id
			return id
		}

		flush := func() {
			// 把积累的 thinking/text 与工具消息按序打包上报；失败只记 debug 日志，
			// 服务器会容忍偶发缺失，下一批 flush 会带上下文继续。
			mu.Lock()
			if pendingThinking.Len() > 0 {
				s := msgSeq.Add(1)
				batch = append(batch, TaskMessageData{
					Seq:       int(s),
					Type:      "thinking",
					Content:   pendingThinking.String(),
					CreatedAt: pendingThinkingAt,
				})
				pendingThinking.Reset()
				pendingThinkingAt = time.Time{}
			}
			if pendingText.Len() > 0 {
				s := msgSeq.Add(1)
				batch = append(batch, TaskMessageData{
					Seq:       int(s),
					Type:      "text",
					Content:   pendingText.String(),
					CreatedAt: pendingTextAt,
				})
				pendingText.Reset()
				pendingTextAt = time.Time{}
			}
			toSend := batch
			batch = nil
			mu.Unlock()

			if len(toSend) > 0 {
				sendCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := d.client.ReportTaskMessages(sendCtx, taskID, toSend); err != nil {
					taskLog.Debug("failed to report task messages", "error", err)
				} else {
					taskLog.Debug("reported task messages", "count", len(toSend), "last_seq", toSend[len(toSend)-1].Seq)
				}
				cancel()
			}
		}

		firstVisible := make(chan struct{}, 1)
		var firstVisibleOnce sync.Once
		flushFirstVisible := func() { firstVisibleOnce.Do(func() { firstVisible <- struct{}{} }) }
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		done := make(chan struct{})
		tickerDone := make(chan struct{})
		go func() {
			defer close(tickerDone)
			for {
				select {
				case <-ticker.C:
					flush()
				case <-firstVisible:
					flush()
				case <-done:
					return
				}
			}
		}()

		var sessionPinned atomic.Bool
		for {
			select {
			case msg, ok := <-session.Messages:
				if !ok {
					goto drainDone
				}
				if isTaskOutputReceived(msg) {
					phaseRecorder.Mark(taskPhaseFirstOutputReceived)
				}
				if isTaskToolUse(msg) {
					phaseRecorder.Mark(taskPhaseFirstToolUse)
				}
				// 消息一落地就盖活动时间戳。空闲看门狗读它判断 backend 是否静默——
				// 在处理前盖戳，保证慢的下游调用（mu.Lock 争用、batch 扩容）不会被
				// 误判成 backend 静默。
				observedAt := time.Now().UTC()
				lastActivityAt.Store(observedAt.UnixNano())
				if firstEventLogged.CompareAndSwap(false, true) {
					taskLog.Info("backend first event",
						"first_event_duration", time.Since(backendStartedAt).Round(time.Millisecond),
						"message_type", msg.Type,
					)
				}
				switch msg.Type {
				case agent.MessageStatus:
					// backend 一揭示 session/work_dir 就持久化。没有它，daemon 中途崩溃
					// 会丢失 resume 指针，自动重试在没有上下文的情况下触发。
					// MUL-5305：只在 session 的 rollout 真正进入 store 之后才钉 resume
					// 指针，让 daemon 无法恢复的崩溃恢复指针永不毒化下一次 follow-up
					// （FailAgentTask 经 COALESCE 保留钉住的 session_id，因此坏的
					// 中途钉会在之后的终态失败中存活）。Codex 在单个 task_started 状态
					// 上揭示 session id，因此后台等待者会在整个运行期间轮询 rollout，
					// rollout 一落地就钉住——此状态之后才冲刷出的 rollout 也会在途中被钉
					// （崩溃恢复得到保留），而 rollout 永不落地的 session 绝不会被钉。
					// 终态报告是权威写者。非 Codex provider（codexHome == ""）立即钉住。
					if msg.SessionID != "" && !sessionPinned.Swap(true) {
						sid := msg.SessionID
						wd := opts.Cwd
						go func() {
							if !waitCodexRolloutPresent(drainCtx, codexHome, sid) {
								taskLog.Debug("skip pinning codex session: rollout not present before run ended",
									"session_id", sid, "codex_home", codexHome)
								return
							}
							pinCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
							defer cancel()
							if err := d.client.PinTaskSession(pinCtx, taskID, sid, wd); err != nil {
								taskLog.Debug("pin session failed", "error", err)
							}
						}()
					}
				case agent.MessageToolUse:
					n := toolCount.Add(1)
					inFlightTools.Add(1)
					taskLog.Info(fmt.Sprintf("tool #%d: %s", n, msg.Tool))
					if msg.CallID != "" {
						mu.Lock()
						callIDToTool[msg.CallID] = msg.Tool
						callIDStartedAt[msg.CallID] = time.Now()
						mu.Unlock()
					}
					s := msgSeq.Add(1)
					mu.Lock()
					batch = append(batch, TaskMessageData{
						Seq:       int(s),
						Type:      "tool_use",
						CallID:    transcriptCallID(msg.CallID),
						Tool:      msg.Tool,
						CreatedAt: observedAt,
						// Redact before the payload leaves this process, not
						// only on arrival. The server redacts again in its
						// ingest handler, but that is the *remote* side: a
						// daemon that self-updated ahead of the server — or one
						// talking to a server mid-rollout — would otherwise ship
						// whole-file edit contents (a deleted .env, a patched
						// credential) to a peer that does not scrub nested
						// values yet. Deployment order is not a control we
						// have, so this side has to be safe on its own.
						Input: redact.InputMap(msg.Input),
					})
					mu.Unlock()
				case agent.MessageToolResult:
					// 只在计数保持 >= 0 时递减。没有匹配 tool_use 的游离 tool_result
					// （backend bug 或流中途重连）不应把计数推成负数——那会在下一次调用时
					// 提前一个 tool_use 重新武装看门狗。
					for {
						cur := inFlightTools.Load()
						if cur <= 0 {
							break
						}
						if inFlightTools.CompareAndSwap(cur, cur-1) {
							break
						}
					}
					s := msgSeq.Add(1)
					output := msg.Output
					toolName := msg.Tool
					if toolName == "" && msg.CallID != "" {
						mu.Lock()
						toolName = callIDToTool[msg.CallID]
						mu.Unlock()
					}
					if msg.CallID != "" {
						mu.Lock()
						if startedAt, ok := callIDStartedAt[msg.CallID]; ok {
							taskLog.Info("tool completed",
								"tool", toolName,
								"tool_duration", time.Since(startedAt).Round(time.Millisecond),
							)
							delete(callIDStartedAt, msg.CallID)
						}
						mu.Unlock()
					}
					taskLog.Info("tool_result observed", "seq", s, "tool", toolName, "call_id", msg.CallID)
					mu.Lock()
					batch = append(batch, TaskMessageData{
						Seq:       int(s),
						Type:      "tool_result",
						CallID:    transcriptCallID(msg.CallID),
						Tool:      toolName,
						Output:    output,
						CreatedAt: observedAt,
					})
					mu.Unlock()
				case agent.MessageThinking:
					if msg.Content != "" {
						mu.Lock()
						pendingThinking.WriteString(msg.Content)
						if pendingThinkingAt.IsZero() {
							pendingThinkingAt = observedAt
						}
						mu.Unlock()
					}
				case agent.MessageText:
					if msg.Content != "" {
						taskLog.Debug("agent", "text", truncateLog(msg.Content, 200))
						mu.Lock()
						pendingText.WriteString(msg.Content)
						if pendingTextAt.IsZero() {
							pendingTextAt = observedAt
						}
						mu.Unlock()
					}
				case agent.MessageError:
					taskLog.Error("agent error", "content", msg.Content)
					s := msgSeq.Add(1)
					mu.Lock()
					batch = append(batch, TaskMessageData{
						Seq:       int(s),
						Type:      "error",
						Content:   msg.Content,
						CreatedAt: observedAt,
					})
					mu.Unlock()
				case agent.MessageRuntime:
					// Runtime telemetry (context usage, compaction) is persisted next
					// to tool/text messages so the run view can build a context panel
					// without a separate ingestion endpoint.
					s := msgSeq.Add(1)
					mu.Lock()
					batch = append(batch, TaskMessageData{
						Seq:       int(s),
						Type:      "runtime",
						Content:   msg.Content,
						CreatedAt: observedAt,
					})
					mu.Unlock()
				}
				switch msg.Type {
				case agent.MessageToolUse, agent.MessageToolResult, agent.MessageError:
					flushFirstVisible()
				case agent.MessageText, agent.MessageThinking:
					if msg.Content != "" {
						flushFirstVisible()
					}
				}
			case <-messageCtx.Done():
				goto drainDone
			}
		}
	drainDone:
		close(done)
		// 让 tick 驱动的冲刷在最后一次之前跑完：若一次冲刷还在途，它会在本
		// goroutine 宣告"transcript 尾部已持久化"之后继续发批次。
		<-tickerDone
		flush()
	}()

	// waitForDrain 阻塞到排空 goroutine 冲刷完 transcript 尾部，因此下面的每个终态
	// 返回都只在任务已上报消息被持久化之后才交还控制权——在终态切换处读它们的消费
	// 者，否则会看到"非空但被截断"的 transcript，与完整版无法区分。有界，让永
	// 不关闭消息 channel 的 backend 无法卡住终态切换：10s 后取消排空循环，并给一个
	// 足够覆盖其最坏退出路径的窗口——在途 tick 冲刷加最终一次冲刷，各自被 5s
	// ReportTaskMessages 超时封顶，且都不受取消中断（它们用 context.Background() 上报）。
	waitForDrain := func() {
		select {
		case <-drainFinished:
			return
		case <-time.After(10 * time.Second):
		case <-drainCtx.Done():
		}
		stopMessages()
		select {
		case <-drainFinished:
		case <-time.After(12 * time.Second):
			taskLog.Warn("transcript drain did not stop after cancel; completing anyway")
		}
	}

	interrupted := func(result agent.Result, interruption error) agent.Result {
		if idleWatchdogFired.Load() {
			result.Status = "idle_watchdog"
			result.Error = idleWatchdogReason(time.Duration(idleWatchdogThreshold.Load()))
		} else if errors.Is(interruption, context.Canceled) {
			result.Status = "cancelled"
			result.Error = "task cancelled by upstream context (server cancel or daemon shutdown)"
		} else if interruption != nil {
			result.Status = "timeout"
			result.Error = "agent did not produce result within drain timeout"
		}
		return result
	}
	select {
	case result := <-session.Result:
		// 以结果收到时的截止状态分类；尾部冲刷超时不能抹掉已经正常到达的结果。
		interruption := drainCtx.Err()
		waitForDrain()
		return interrupted(result, interruption), toolCount.Load(), nil
	case <-drainCtx.Done():
		agentCancel()
		result, collected := awaitInterruptedBackendResult(session.Result)
		if !collected {
			taskLog.Warn("backend final result unavailable after interruption; accounting may be incomplete", "grace", interruptedResultGrace)
		}
		stopMessages()
		waitForDrain()
		return interrupted(result, drainCtx.Err()), toolCount.Load(), nil
	}
}

// idleWatchdogReason 格式化 idle_watchdog 处置呈现给用户的解释。集中在一处，
// 让 executeAndDrain 的结果到达分支与排空超时分支吐出完全一致的措辞。
func idleWatchdogReason(window time.Duration) string {
	return fmt.Sprintf("agent produced no new messages for %s and message queue was empty; force-stopped by idle watchdog", window)
}

// idleWatchdogTickInterval 选择空闲看门狗复查静默预算的频率。基础速率是窗口一半，
// 封顶于 idleWatchdogMaxTick，使运行在 window + tick 内被强停，而不是 window*1.5。
// 亚纳秒的窗口一半回退到窗口自身，让测试能传极小预算仍得到有效 ticker。
//
// 这里曾有过 `window >= time.Minute && interval < 30*time.Second` 的下限，意在让
// 生产轮询不快于 30s。它不可达——window >= 1min 意味着 window/2 >= 30s——唯一
// 作用是让周围的测试读起来像在锻炼一个下限。生产窗口以分钟或小时计，window/2
// 早已越过 30s。
func idleWatchdogTickInterval(window time.Duration) time.Duration {
	interval := window / 2
	if interval > idleWatchdogMaxTick {
		interval = idleWatchdogMaxTick
	}
	if interval <= 0 {
		interval = window
	}
	return interval
}

// runIdleWatchdog ticks until either agentCtx is cancelled or the backend has
// been silent past the applicable budget. On firing, it records the tripped
// threshold, sets fired, and calls cancel, which propagates to the agent
// subprocess (via the ctx passed to backend.Execute) and to drainCtx. The
// silence budget depends on whether a tool call is in flight:
//
//  1. No tool in flight — a silent backend is a hang after `window`.
//  2. A tool in flight (tool_use with no matching tool_result yet) —
//     `toolWindow` applies instead. It defaults to `window`, so the two are
//     normally identical and this branch only changes which duration the
//     failure message reports; an operator who deliberately sets
//     MULTICA_AGENT_TOOL_WATCHDOG higher buys long tools extra room, and
//     toolWindow <= 0 keeps the historical behavior of never force-stopping
//     while a tool is in flight. Without this in-flight budget a backend that
//     emits tool_use and never the matching tool_result would run forever now
//     that there is no wall-clock cap (MUL-3064).
//
// In both cases the watchdog also requires the session.Messages buffer to be
// empty — a buffered-but-undrained message means the drain loop is behind, not
// the backend.
//
// Polling rate comes from idleWatchdogTickInterval, so a run is force-stopped
// somewhere between its budget and budget + tick, never earlier.
func (d *Daemon) runIdleWatchdog(agentCtx context.Context, window, toolWindow time.Duration, lastActivityAt *atomic.Int64, inFlightTools *atomic.Int32, fired *atomic.Bool, firedThreshold *atomic.Int64, cancel context.CancelFunc, messages <-chan agent.Message, taskLog *slog.Logger, waitingForInput ...func() bool) {
	ticker := time.NewTicker(idleWatchdogTickInterval(window))
	defer ticker.Stop()
	for {
		select {
		case <-agentCtx.Done():
			return
		case <-ticker.C:
			if len(waitingForInput) > 0 && waitingForInput[0] != nil && waitingForInput[0]() {
				continue
			}
			// 挑选静默预算。在途工具预期会静默（一场长构建/安装/测试在 tool_use 与
			// tool_result 之间不发任何东西），所以它用更大的 toolWindow；toolWindow
			// <= 0 则完全禁用"在途"上界。
			threshold := window
			toolInFlight := inFlightTools.Load() > 0
			if toolInFlight {
				if toolWindow <= 0 {
					continue
				}
				threshold = toolWindow
			}
			last := time.Unix(0, lastActivityAt.Load())
			idleFor := time.Since(last)
			if idleFor < threshold {
				continue
			}
			// 一条"被缓冲却未排空"的消息说明排空循环落后，不是 backend 落后。多等
			// 一拍，而不是杀掉一个仍在产出的 backend。
			if len(messages) > 0 {
				continue
			}
			// No "task" field here: taskLog already carries the full id.
			taskLog.Warn("idle watchdog firing: no agent activity, force-stopping run",
				"idle_for", idleFor.Round(time.Second).String(),
				"threshold", threshold.String(),
				"tool_in_flight", toolInFlight,
			)
			firedThreshold.Store(int64(threshold))
			fired.Store(true)
			cancel()
			return
		}
	}
}

// buildTaskTraceEntries 把子进程自报的根请求 id 变成上报条目。会话 id 同源
// （子进程输出），但只作辅助字段：重试可能换会话，链路归属以 trace id 为准。
func buildTaskTraceEntries(provider string, traces []ProviderTrace) []TaskTraceEntry {
	if len(traces) == 0 {
		return nil
	}
	entries := make([]TaskTraceEntry, 0, len(traces))
	for _, trace := range traces {
		if trace.TraceID == "" {
			continue
		}
		entry := TaskTraceEntry{
			Provider:        provider,
			SessionID:       trace.SessionID,
			ProviderTraceID: trace.TraceID,
			Correlation:     "exact",
			Source:          "stream_json",
		}
		// 三件套必须一起给，且 source_path 是"id 在记录里的位置"这个固定标记，
		// 不是本地文件路径——服务端就是按这个契约校验的（providerData.traceId）。
		// 只带一部分会被整批 400，反而把这条准确关联也丢了。
		if trace.SDKTraceID != "" && trace.SDKTraceSourceSHA256 != "" {
			entry.Source = "local_session_jsonl"
			entry.SDKTraceID = trace.SDKTraceID
			entry.SDKTraceSourceSHA256 = trace.SDKTraceSourceSHA256
			entry.SDKTraceSourcePath = "providerData.traceId"
		}
		entries = append(entries, entry)
	}
	return entries
}

// buildTaskUsageEntries 把 agent 的按模型用量转成上报条目。全零的分片不产生条目，
// 因为"没用量"和"用量恰好为零"在账面上没有区别，但一条空行会让汇总多一个维度。
func buildTaskUsageEntries(provider string, usage map[string]agent.TokenUsage) []TaskUsageEntry {
	if len(usage) == 0 {
		return nil
	}
	entries := make([]TaskUsageEntry, 0, len(usage))
	for model, u := range usage {
		if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 && u.CacheWriteTokens == 0 && u.CostUSDTicks <= 0 {
			continue
		}
		entries = append(entries, TaskUsageEntry{
			Provider:         provider,
			Model:            model,
			InputTokens:      u.InputTokens,
			OutputTokens:     u.OutputTokens,
			CacheReadTokens:  u.CacheReadTokens,
			CacheWriteTokens: u.CacheWriteTokens,
			CostUSDTicks:     u.CostUSDTicks,
		})
	}
	return entries
}

// appendTraceID 把非空、未出现过的 provider 根请求 id 追加到列表。同一次执行可能
// 因为换新会话重试起两个进程，也可能一个都没有；这里只去重，不做任何"必须有一条"
// 的假设。
// traceListForResult 是"一次执行 → 链路列表"的唯一归并点：父链路带自己的 SDK 溯源，
// 子 Agent 调用作为额外 0:N 链路追加。三条退出路径（正常、重试、提前失败）都走它，
// 避免某条路径漏掉子链路。
func traceListForResult(result agent.Result) []ProviderTrace {
	list := attachSDKProvenance(appendTraceID(nil, result.ProviderTraceID, result.SessionID), result)
	for _, extra := range result.ExtraTraces {
		list = append(list, ProviderTrace{
			SessionID:            extra.SessionID,
			TraceID:              extra.TraceID,
			SDKTraceID:           extra.SDKTraceID,
			SDKTraceSourceSHA256: extra.SDKSourceSHA,
		})
	}
	return list
}

// mergeTraceLists 按 CID 去重合并两条链路列表，保留先到那条的全部字段（含 SDK 溯源），
// 只补列表中还没有的链路。
func mergeTraceLists(list, extra []ProviderTrace) []ProviderTrace {
	for _, candidate := range extra {
		found := false
		for _, existing := range list {
			if existing.TraceID == candidate.TraceID {
				found = true
				break
			}
		}
		if !found {
			list = append(list, candidate)
		}
	}
	return list
}

// attachSDKProvenance 把本轮 SDK 文件溯源挂到 CID 相同的那条链路上。归属不唯一时
// Result 里本来就是空的，这里什么都不做——未知保持未知，不挂到别的链路上。
func attachSDKProvenance(list []ProviderTrace, result agent.Result) []ProviderTrace {
	if result.SDKTraceID == "" || result.ProviderTraceID == "" {
		return list
	}
	for i := range list {
		if list[i].TraceID == result.ProviderTraceID {
			list[i].SDKTraceID = result.SDKTraceID
			list[i].SDKTraceSourceSHA256 = result.SDKTraceSourceSHA256
			list[i].SDKTraceSourcePath = result.SDKTraceSourcePath
		}
	}
	return list
}

func appendTraceID(list []ProviderTrace, traceID, sessionID string) []ProviderTrace {
	if traceID == "" {
		return list
	}
	for _, existing := range list {
		if existing.TraceID == traceID {
			return list
		}
	}
	return append(list, ProviderTrace{SessionID: sessionID, TraceID: traceID})
}

// mergeUsage 把两次尝试的 token 用量按模型合并（都为空时直接返回对方，避免分配）。
func mergeUsage(a, b map[string]agent.TokenUsage) map[string]agent.TokenUsage {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	merged := make(map[string]agent.TokenUsage, len(a)+len(b))
	for model, u := range a {
		merged[model] = u
	}
	for model, u := range b {
		existing := merged[model]
		existing.InputTokens += u.InputTokens
		existing.OutputTokens += u.OutputTokens
		existing.CacheReadTokens += u.CacheReadTokens
		existing.CacheWriteTokens += u.CacheWriteTokens
		existing.CostUSDTicks += u.CostUSDTicks
		merged[model] = existing
	}
	return merged
}

// repoDataToInfo 把 daemon 的 RepoData 转成 repocache.RepoInfo（仅保留 URL 字段）。
func repoDataToInfo(repos []RepoData) []repocache.RepoInfo {
	info := make([]repocache.RepoInfo, len(repos))
	for i, r := range repos {
		info[i] = repocache.RepoInfo{URL: r.URL}
	}
	return info
}

// convertIssueStatusesForEnv 把服务器下发的 issue 状态列表转成 execenv 需要的形态。
func convertIssueStatusesForEnv(statuses []IssueStatusData) []execenv.IssueStatusForEnv {
	if len(statuses) == 0 {
		return nil
	}
	result := make([]execenv.IssueStatusForEnv, len(statuses))
	for i, s := range statuses {
		result[i] = execenv.IssueStatusForEnv{Key: s.Key, Name: s.Name, Category: s.Category, Description: s.Description}
	}
	return result
}

// convertReposForEnv 把任务仓库列表转成 execenv 上下文形态（URL/描述/ref）。
func convertReposForEnv(repos []RepoData) []execenv.RepoContextForEnv {
	if len(repos) == 0 {
		return nil
	}
	result := make([]execenv.RepoContextForEnv, len(repos))
	for i, r := range repos {
		result[i] = execenv.RepoContextForEnv{URL: r.URL, Description: r.Description, Ref: r.Ref}
	}
	return result
}

// convertProjectResourcesForEnv 把 project resources 转成 execenv 形态。
func convertProjectResourcesForEnv(resources []ProjectResourceData) []execenv.ProjectResourceForEnv {
	if len(resources) == 0 {
		return nil
	}
	result := make([]execenv.ProjectResourceForEnv, len(resources))
	for i, r := range resources {
		result[i] = execenv.ProjectResourceForEnv{
			ID:           r.ID,
			ResourceType: r.ResourceType,
			ResourceRef:  r.ResourceRef,
			Label:        r.Label,
		}
	}
	return result
}

// markActiveEnvRoot 记录某任务当前正使用给定 env root，让 GC 循环不会在任务执行
// 中途回收其产物。调用按引用计数，因此被标记两次的复用路径（预测 + prior）要到
// 两个 unmark 都调用后才变为非活跃。
func (d *Daemon) markActiveEnvRoot(envRoot string) {
	if envRoot == "" {
		return
	}
	d.activeEnvRootsMu.Lock()
	defer d.activeEnvRootsMu.Unlock()
	d.ensureActiveEnvRootStateLocked()
	// GC 已保留该 root 待删时，等删除完成再计数（任务绝不能进到一个正在被删的目录）。
	for d.deletingEnvRoots[envRoot] {
		d.activeEnvRootsCond.Wait()
	}
	d.activeEnvRoots[envRoot]++
}

// unmarkActiveEnvRoot 递减引用计数；归零则删除条目（可能唤醒等待的 GC）。
func (d *Daemon) unmarkActiveEnvRoot(envRoot string) {
	if envRoot == "" {
		return
	}
	d.activeEnvRootsMu.Lock()
	defer d.activeEnvRootsMu.Unlock()
	d.ensureActiveEnvRootStateLocked()
	if d.activeEnvRoots[envRoot] <= 1 {
		delete(d.activeEnvRoots, envRoot)
		return
	}
	d.activeEnvRoots[envRoot]--
}

// isActiveEnvRoot 报告某 env root 当前是否有活跃任务引用。
func (d *Daemon) isActiveEnvRoot(envRoot string) bool {
	d.activeEnvRootsMu.Lock()
	defer d.activeEnvRootsMu.Unlock()
	d.ensureActiveEnvRootStateLocked()
	return d.activeEnvRoots[envRoot] > 0
}

// ensureActiveEnvRootStateLocked 保证 env root 相关的 map/cond 已初始化（懒初始化，
// 防止零值 Daemon 测试夹具 panic）。
func (d *Daemon) ensureActiveEnvRootStateLocked() {
	if d.activeEnvRoots == nil {
		d.activeEnvRoots = make(map[string]int)
	}
	if d.deletingEnvRoots == nil {
		d.deletingEnvRoots = make(map[string]bool)
	}
	if d.activeEnvRootsCond == nil {
		d.activeEnvRootsCond = sync.NewCond(&d.activeEnvRootsMu)
	}
}

// reserveEnvRootForGC 原子地确认没有活跃任务使用 envRoot，并阻止新任务进入，直到
// release 运行。这封闭了 GC 循环与任务启动之间的 check-then-remove 竞态：要么 GC
// 看到活跃任务而跳过，要么任务启动等待变更结束、再使用 GC 之后的环境。
func (d *Daemon) reserveEnvRootForGC(envRoot string) (release func(), ok bool) {
	if envRoot == "" {
		return nil, false
	}
	d.activeEnvRootsMu.Lock()
	defer d.activeEnvRootsMu.Unlock()
	d.ensureActiveEnvRootStateLocked()
	if d.activeEnvRoots[envRoot] > 0 || d.deletingEnvRoots[envRoot] {
		return nil, false
	}
	d.deletingEnvRoots[envRoot] = true
	return func() {
		d.activeEnvRootsMu.Lock()
		delete(d.deletingEnvRoots, envRoot)
		d.activeEnvRootsCond.Broadcast()
		d.activeEnvRootsMu.Unlock()
	}, true
}

// markActiveStore 记录某任务即将使用给定的持久化 store——每 issue 的 Codex
// session store 或每 agent 的 Hermes memory store——让 GC 绝不在任务中途回收它。
// 这些 store 位于 env root 之外，因此 isActiveEnvRoot 覆盖不到（MUL-4424）。若
// 某个 GC 删除已保留该 store，我们会等待那次移除完成后再认领，因此任务绝不会在
// 移除中途挂载 store；之后由 Prepare 重新创建全新 store。与 env-root 护栏一样按
// 引用计数。
func (d *Daemon) markActiveStore(store string) {
	if store == "" {
		return
	}
	d.activeStoresMu.Lock()
	defer d.activeStoresMu.Unlock()
	for d.deletingStores[store] {
		d.activeStoresCond.Wait()
	}
	d.activeStores[store]++
}

// unmarkActiveStore 递减 store 引用计数；归零则删除条目。
func (d *Daemon) unmarkActiveStore(store string) {
	if store == "" {
		return
	}
	d.activeStoresMu.Lock()
	defer d.activeStoresMu.Unlock()
	if d.activeStores[store] <= 1 {
		delete(d.activeStores, store)
		return
	}
	d.activeStores[store]--
}

// reserveStoreForDeletion 原子地确认没有活跃任务持有 store，若是则把它标记为保留，
// 让任何任务在调用方运行返回的 commit（实际删除之后）之前都无法认领它。
// ok=false 表示某任务正持有它——不要删。这是 store 剪枝器
// （PruneCodexSessionStores、PruneHermesMemoryStores）需要的排它协议：
// "确认非活跃"与"打标记"在同一个锁获取下完成，因此 markActiveStore 要么输掉
// 检查（store 留下），要么阻塞在保留上——封闭了 stat->remove 竞态（MUL-4424）。
func (d *Daemon) reserveStoreForDeletion(store string) (commit func(), ok bool) {
	d.activeStoresMu.Lock()
	defer d.activeStoresMu.Unlock()
	if d.activeStores[store] > 0 || d.deletingStores[store] {
		return nil, false
	}
	d.deletingStores[store] = true
	return func() {
		d.activeStoresMu.Lock()
		delete(d.deletingStores, store)
		d.activeStoresCond.Broadcast()
		d.activeStoresMu.Unlock()
	}, true
}

// shortID 返回 ID 的前 8 个字符，供可读日志使用。
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// truncateLog 把字符串截断到 maxLen，截断时追加 "…"；同时把换行折叠成空格，
// 保证单行日志输出。
func truncateLog(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

// convertSkillsForEnv 把任务技能包列表转成 execenv 需要的上下文形态。
func convertSkillsForEnv(skills []SkillData) []execenv.SkillContextForEnv {
	if len(skills) == 0 {
		return nil
	}
	result := make([]execenv.SkillContextForEnv, len(skills))
	for i, s := range skills {
		result[i] = execenv.SkillContextForEnv{
			Name:        s.Name,
			Description: s.Description,
			Content:     s.Content,
		}
		for _, f := range s.Files {
			result[i].Files = append(result[i].Files, execenv.SkillFileContextForEnv{
				Data: f.Data, Mode: f.Mode,
				Path:    f.Path,
				Content: f.Content,
			})
		}
	}
	return result
}

// convertDisabledRuntimeSkillsForEnv 筛选出属于该 (runtime, provider) 的被禁运行时
// 技能，并转成 execenv 形态（供隔离环境排除这些技能）。
func convertDisabledRuntimeSkillsForEnv(agentData *AgentData, runtimeID, provider string) []execenv.RuntimeSkillRefForEnv {
	if agentData == nil || len(agentData.DisabledRuntimeSkills) == 0 {
		return nil
	}
	result := make([]execenv.RuntimeSkillRefForEnv, 0, len(agentData.DisabledRuntimeSkills))
	for _, skill := range agentData.DisabledRuntimeSkills {
		if skill.RuntimeID != runtimeID || skill.Provider != provider {
			continue
		}
		result = append(result, execenv.RuntimeSkillRefForEnv{
			Root:   skill.Root,
			Key:    skill.Key,
			Name:   skill.Name,
			Plugin: skill.Plugin,
		})
	}
	return result
}

// ensureTaskTempDir 创建本任务私有的临时目录并带着它的执行锁一起返回。调用方在
// 整个运行期间持有该锁，删除目录前必须先释放——见 runTask 里的清理 defer，以及
// execenv.PruneTaskTempDirs 里该锁买到的保证。
func ensureTaskTempDir(envRoot string, workspaceID string, taskID string) (string, *os.File, error) {
	envRoot = strings.TrimSpace(envRoot)
	if envRoot == "" {
		return "", nil, errors.New("env root is empty")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return "", nil, errors.New("workspace id is empty")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return "", nil, errors.New("task id is empty")
	}
	base, overrideConfigured, err := taskTempBaseDir()
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(base, execenv.TaskTempDirPrefix)
	if err != nil {
		if overrideConfigured {
			return "", nil, fmt.Errorf("MULTICA_AGENT_TEMP_BASE: create task temp dir: %w", err)
		}
		return "", nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	lock, err := execenv.LockTaskTempDir(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	return dir, lock, nil
}

// taskTempBaseDir 解析 Linux/macOS 上私有每任务临时目录的父目录。daemon 运维可用
// MULTICA_AGENT_TEMP_BASE 重定位它，该值必须是绝对路径、指向存在且可写的目录；
// 非法值让任务启动失败，而不是静默回退。Windows 忽略该变量。未设置则完全保持
// 平台默认，直到具体做的系统调用。
// 运维应选短路径：子工具可能在 $TMPDIR 下绑 AF_UNIX socket（Linux 上 sun_path
// 为 108 字节，macOS 为 104）。
func taskTempBaseDir() (string, bool, error) {
	if runtime.GOOS == "windows" {
		return socketSafeTempBaseDir(), false, nil
	}
	base := strings.TrimSpace(os.Getenv("MULTICA_AGENT_TEMP_BASE"))
	if base == "" {
		return socketSafeTempBaseDir(), false, nil
	}
	if !filepath.IsAbs(base) {
		return "", true, fmt.Errorf("MULTICA_AGENT_TEMP_BASE must be an absolute path, got %q", base)
	}
	return base, true, nil
}

// socketSafeTempBaseDir 优先返回 /tmp（POSIX 路径短、AF_UNIX socket 安全），
// 否则回退到 os.TempDir()。
func socketSafeTempBaseDir() string {
	if os.PathSeparator == '/' {
		if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
			return "/tmp"
		}
	}
	return os.TempDir()
}

// isBlockedEnvKey 返回某个键是否绝不能被用户配置的 custom_env 覆盖。这防止了
// 意外或恶意地覆盖 daemon 内部变量与关键系统路径。
func isBlockedEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	if strings.HasPrefix(upper, "MULTICA_") {
		return true
	}
	switch upper {
	case "HOME", "PATH", "USER", "SHELL", "TERM", "TMPDIR", "TMP", "TEMP", "CODEX_HOME", "REASONIX_STATE_HOME", "CURSOR_DATA_DIR", execenv.CursorMcpAuthSourceEnv:
		return true
	}
	return false
}

// layerCustomEnvAndHermesHome 把 agent 的 custom_env 套到子进程环境上（跳过
// daemon 内部黑名单键），然后在构建了每任务 overlay 时用其覆盖 HERMES_HOME。
// HERMES_HOME 刻意不在黑名单里：绑定技能时 overlay 从用户的 HERMES_HOME 构建、
// 必须在此获胜；无技能绑定（overlayHome 为空）时用户自己的 HERMES_HOME 原样
// 通过，让无技能的 Hermes 任务保持原行为。
// sanitizeAgentEnv 返回移除 daemon 黑名单键后的 agent custom_env——即 Hermes
// 子进程实际看到的有效环境，用于一致地展开 external_dirs 里的 ${VAR}（HOME 等
// 被拦截的键解析为 daemon 进程值，而非被丢弃的 custom 值）。与
// layerCustomEnvAndHermesHome 共用同一个 isBlockedEnvKey 规则，保证两者一致。
func sanitizeAgentEnv(customEnv map[string]string) map[string]string {
	if len(customEnv) == 0 {
		return nil
	}
	out := make(map[string]string, len(customEnv))
	for k, v := range customEnv {
		if isBlockedEnvKey(k) {
			continue
		}
		out[k] = v
	}
	return out
}

// hermesProviderUnconfiguredHint 原样追加到 "no LLM provider configured" 失败里。
// 它是 CONSTANT，这是一条正确性属性而非风格选择——见
// annotateHermesProviderUnconfigured。
//
// 它必须远离 resume 护栏匹配的每个短语，因为这段文本持久化在
// agent_task_queue.error 里、会在那被无限期重扫：service.ResumeUnsafeFailure、
// taskfailure.Classify，以及 pkg/db/queries/agent.sql 的 ILIKE/regex 护栏
// （GetLastTaskSession / GetLastChatTaskSession）。TestAnnotationCannotChangeMachineDecisions
// 钉死了这一点。
const hermesProviderUnconfiguredHint = " [multica] hermes did not read the HERMES_HOME your shell uses: " +
	"this task ran against a per-task overlay, seeded from the home the daemon process resolved. " +
	"The daemon log line \"hermes home resolved\" for this task names that source home — if your hermes " +
	"config lives somewhere else, set HERMES_HOME in the agent's custom_env to point at it."

// annotateHermesProviderUnconfigured 解释 Hermes 自身解释不了的 "no LLM provider
// configured" 失败。
//
// Hermes 对照它启动时用的 HERMES_HOME 报告该错误，并让用户跑 `hermes model`——
// 但 Multica 下它启动时带的是每任务 overlay，种子来自 daemon 从它自己进程环境
// 解析出的源 home。当这与用户存放配置的位置不一致时，Hermes 给出的补救会编辑一个
// 任务永远不会读的文件，每次尝试都同样失败。那就是 GH #6872：八个有文档的
// workaround，没一个能起作用。
//
// 两个路径刻意不在这里插值。它们由用户控制（HERMES_HOME 来自 agent 的
// custom_env，overlay root 来自 MULTICA_WORKSPACES_ROOT），而这段字符串作为任务
// 错误文本持久化，resume 护栏会在行存活期内持续匹配它。位于 /srv/400-invalid_request_error/
// 之下的源 home 会触发 ResumeUnsafeFailure 与 SQL 护栏，丢掉一个健康的 session
// 指针——目录名绝不该决定 session 能否恢复。因此提示是固定散文，并点名携带路径
// 的那行日志。
//
// 纯文本：调用方已分类好失败，本函数不改变任何 reason、status 或控制流。
func annotateHermesProviderUnconfigured(errMsg, provider string, overlayActive bool) string {
	if provider != "hermes" || !overlayActive || !taskfailure.ProviderUnconfigured(errMsg) {
		return errMsg
	}
	return errMsg + hermesProviderUnconfiguredHint
}

// codexRetiredCompactionHint is appended verbatim to a Codex compaction
// failure against the retired route. Like hermesProviderUnconfiguredHint
// above it is a CONSTANT, for the same correctness reason — see
// annotateCodexRetiredCompaction — and is worded to stay clear of every
// phrase the resume guards and taskfailure.Classify match, since it is
// persisted in agent_task_queue.error and re-scanned there indefinitely.
// TestCodexCompactionAnnotationCannotChangeMachineDecisions pins that.
//
// It names every place the setting can be off, because being a constant means
// it cannot know which one applied. The shared config is the common case and
// the only one the upstream reports mention, but the launch arguments reach
// the same state: nothing strips `--disable remote_compaction_v2` or
// `-c features.remote_compaction_v2=false` out of an agent's custom args, a
// daemon's extra args, or a custom runtime profile's launch prefix — only
// fast_mode gets that treatment, and only when a service tier is selected.
// Naming just the file would send anyone in that case to edit a line that is
// not there, which is the failure mode this hint exists to prevent.
//
// For the same reason the closing line excludes only the generated per-task
// copy, rather than claiming nothing on the Multica side needs changing. Once
// the hint sends people to look at custom_args, a daemon's extra args, or a
// profile's fixed args, "nothing to change here" contradicts the instruction
// directly above it and strands exactly the users the argument half was added
// for. The per-task copy is the one target that is genuinely wrong to edit: it
// is rebuilt from the shared config every run, so an edit there is discarded.
const codexRetiredCompactionHint = " [multica] codex could not compact this conversation: it called a " +
	"compaction endpoint OpenAI has retired. That route is selected by turning `remote_compaction_v2` " +
	"off, so look in both places it can be off: `[features]` in the codex config this agent uses " +
	"(~/.codex/config.toml by default), and the codex launch arguments on the agent, the daemon, or a " +
	"custom runtime profile (`--disable remote_compaction_v2`, `-c features.remote_compaction_v2=false`). " +
	"Remove it wherever it appears — or set it to true — and run this task again; both sources are re-read " +
	"on the next run. The one place not to edit is the per-task codex config this run used: it is " +
	"regenerated from your shared one every run, so a change there is lost. A thread stuck this way " +
	"continues where it left off once compaction works."

// annotateCodexRetiredCompaction explains a retired-route compaction failure
// that Codex itself cannot explain.
//
// Codex reports the failing URL and status and stops there. The setting that
// chose that URL — `remote_compaction_v2` — appears nowhere in the text, so the
// error names no file, no key and no remedy; the reporter in GH #8000 had to
// open an issue to learn the fix was deleting one line. Nor does the failure
// resolve on its own: the conversation stays above the auto-compact threshold,
// so every following turn retries the same retired call.
//
// Text only. This changes no reason, status, or control flow — in particular
// it does NOT retire the session. That is a deliberate choice, not an
// omission: unlike the resume-unsafe failures classified above, this thread is
// recoverable, and it recovers by itself once the setting is right. Dropping
// the session pointer would discard the conversation the fix brings back.
//
// Scoped to the codex provider, which is exactly the set of runs that can
// produce this text (only "codex" resolves to codexBackend), so the check
// costs nothing and keeps a lookalike string from another runtime out.
func annotateCodexRetiredCompaction(errMsg, provider string) string {
	if strings.ToLower(strings.TrimSpace(provider)) != "codex" ||
		!agent.CodexRetiredCompactionError(errMsg) {
		return errMsg
	}
	return errMsg + codexRetiredCompactionHint
}

func layerCustomEnvAndHermesHome(agentEnv, customEnv map[string]string, overlayHome string, logger *slog.Logger) {
	for k, v := range customEnv {
		if isBlockedEnvKey(k) {
			if logger != nil {
				logger.Warn("custom_env: blocked key skipped", "key", k)
			}
			continue
		}
		agentEnv[k] = v
	}
	if overlayHome != "" {
		agentEnv["HERMES_HOME"] = overlayHome
	}
}

// prepareReasonixTaskStateHome 按 (runtime, agent) 隔离持久化 transcript 与租约，
// 同时保持 REASONIX_HOME 不动。当前 Reasonix 从 REASONIX_HOME 读凭据/配置、从
// REASONIX_STATE_HOME 读状态，因此 `reasonix setup` 仍是凭据的唯一所有者，Multica
// 从不把 API key 拷进任务管理文件。
func prepareReasonixTaskStateHome(profile, runtimeID, agentID string) (string, error) {
	profileDir, err := cli.ProfileDir(profile)
	if err != nil {
		return "", err
	}
	runtimeSegment, err := validateReasonixStateSegment("runtime", runtimeID)
	if err != nil {
		return "", err
	}
	agentSegment, err := validateReasonixStateSegment("agent", agentID)
	if err != nil {
		return "", err
	}
	path := filepath.Join(profileDir, "reasonix-state", runtimeSegment, agentSegment)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return "", err
	}
	return path, nil
}

// prepareDshTaskSessionRoot 把 DSH transcript 保持为单个 Multica runtime/agent 对
// 私有。凭据与用户的 DSH profile 留在普通 DSH_HOME；只有会话持久化被重定向。
func prepareDshTaskSessionRoot(profile, runtimeID, agentID string) (string, error) {
	profileDir, err := cli.ProfileDir(profile)
	if err != nil {
		return "", err
	}
	runtimeSegment, err := validateReasonixStateSegment("runtime", runtimeID)
	if err != nil {
		return "", err
	}
	agentSegment, err := validateReasonixStateSegment("agent", agentID)
	if err != nil {
		return "", err
	}
	path := filepath.Join(profileDir, "dsh-sessions", runtimeSegment, agentSegment)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return "", err
	}
	return path, nil
}

// validateReasonixStateSegment 校验 runtime/agent ID 只能含 URL 安全字符（字母数字、
// 连字符、下划线），防止路径注入到 profile 派生目录里。
func validateReasonixStateSegment(name, value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("%s ID is required", name)
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			continue
		default:
			return "", fmt.Errorf("%s ID contains an unsafe path character", name)
		}
	}
	return value, nil
}

// codexShellAuthorizedCustomEnvNames 返回当前 agent custom_env 中通过 daemon
// 黑名单的键名。只返回名字，让凭据值不进入受管的 Codex 配置路径。
func codexShellAuthorizedCustomEnvNames(customEnv map[string]string) []string {
	names := make([]string, 0, len(customEnv))
	for key := range customEnv {
		if key == "" || isBlockedEnvKey(key) {
			continue
		}
		names = append(names, key)
	}
	return names
}

// configureCodexTaskShellEnvironment 只在任务与 agent 自定义环境全部组装完成后，
// 写入受管的 shell 策略。这让白名单反映将要真正启动的子进程环境，包括平台特定
// 的必需项与经黑名单检查的 custom_env 凭据。
// 失败是致命的：用无主或畸形的策略启动，可能再次丢 task 级 token，或暴露继承的
// daemon 凭据。
func configureCodexTaskShellEnvironment(provider, codexHome string, inherited []string, agentEnv, agentCustomEnv map[string]string, logger *slog.Logger) error {
	if provider != "codex" {
		return nil
	}
	if strings.TrimSpace(codexHome) == "" {
		return errors.New("configure Codex shell environment: task CODEX_HOME is missing")
	}
	authorizedExplicit := codexShellAuthorizedCustomEnvNames(agentCustomEnv)
	includeOnly := execenv.CodexShellEnvAllowlist(inherited, agentEnv, authorizedExplicit)
	configPath := filepath.Join(codexHome, "config.toml")
	if err := execenv.EnsureCodexShellEnvPolicyConfig(configPath, includeOnly, logger); err != nil {
		return fmt.Errorf("configure Codex shell environment: %w", err)
	}
	return nil
}

func defaultArgsForProvider(cfg Config, provider string) []string {
	var args []string
	switch provider {
	case "claude":
		args = cfg.ClaudeArgs
	case "codex":
		args = cfg.CodexArgs
	case "codebuddy":
		args = cfg.CodebuddyArgs
	case "qwen":
		args = cfg.QwenArgs
	case "qwenpaw":
		args = cfg.QwenpawArgs
	default:
		return nil
	}
	return append([]string(nil), args...)
}

// withoutWorktreePreserved strips the worktree finalization error out of a
// run error, returning what is left: nil when finalization was the only
// failure, the remaining errors when runTask joined it onto an earlier one.
// A single-chain error that wraps it is finalization's alone and yields nil;
// errors that do not contain it are returned unchanged.
func withoutWorktreePreserved(err error) error {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		var preserved *worktreePreservedError
		if errors.As(err, &preserved) {
			return nil
		}
		return err
	}
	var rest []error
	for _, e := range joined.Unwrap() {
		if e = withoutWorktreePreserved(e); e != nil {
			rest = append(rest, e)
		}
	}
	return errors.Join(rest...)
}
