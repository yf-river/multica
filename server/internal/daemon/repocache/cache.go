// Package repocache 管理工作区仓库的裸 git clone 缓存。守护进程用这些缓存当
// 数据源，为每个任务创建 worktree。
package repocache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/processtree"
)

// gitEnv 构造给"需要联网的" git 子进程用的环境变量。它把守护进程的完整环境
// 原样传下去，好让凭据助手（如 gh）能找到自己的配置；同时关掉 TTY 交互提示，
// 这样鉴权失败时报出的是明确错误，而不是卡在没有终端的地方等人输入。
//
// safe.directory=* 通过 GIT_CONFIG_* 环境变量注入，让 git 信任所有目录、不去
// 校验属主。缓存和 worktree 都是守护进程自己建的，属主检查在这里起不到任何
// 安全作用，反而会坑到 CI——runner 的 UID 往往和目录属主不一致。
func gitEnv() []string {
	base := os.Environ()

	// 先读出现有的 GIT_CONFIG_COUNT，好把我们的配置追加在下一个下标，而不是
	// 覆盖掉环境里已有的 git config（比如 auth、URL 重写、extra headers 等）。
	existing := 0
	for _, e := range base {
		if strings.HasPrefix(e, "GIT_CONFIG_COUNT=") {
			if n, err := strconv.Atoi(strings.TrimPrefix(e, "GIT_CONFIG_COUNT=")); err == nil {
				existing = n
			}
		}
	}

	idx := strconv.Itoa(existing)
	return append(base,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT="+strconv.Itoa(existing+1),
		"GIT_CONFIG_KEY_"+idx+"=safe.directory",
		"GIT_CONFIG_VALUE_"+idx+"=*",
	)
}

var agentGitExcludePatterns = []string{
	".agent_context",
	"CLAUDE.md",
	"AGENTS.md",
	".claude",
	".opencode",
	".codeartsdoer",
	".deveco",
	"CODEBUDDY.md",
	".codebuddy",
	".pi",
	".omp",
}

const repoCacheGitTimeout = 10 * time.Minute

func newGitCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	// 守护进程可能活得比启动它的那个 checkout 还久，所以别继承一个也许已被
	// 删掉的 cwd，干脆从文件系统根目录跑 git。
	cmd.Dir = filepath.VolumeName(os.TempDir()) + string(os.PathSeparator)
	cmd.Env = gitEnv()
	return cmd
}

func runGitCombinedOutput(args ...string) ([]byte, error) {
	return runGitCombinedOutputContext(context.Background(), args...)
}

func runGitCombinedOutputContext(ctx context.Context, args ...string) ([]byte, error) {
	return runGitCombinedOutputWithTimeoutContext(ctx, repoCacheGitTimeout, args...)
}

func runGitCombinedOutputWithTimeout(timeout time.Duration, args ...string) ([]byte, error) {
	return runGitCombinedOutputWithTimeoutContext(context.Background(), timeout, args...)
}

func runGitCombinedOutputWithTimeoutContext(parent context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := newGitCommand(args...)
	out, err := processtree.CombinedOutput(ctx, cmd, 5*time.Second)
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("git command timed out after %s: %w", timeout, ctx.Err())
	}
	return out, err
}

func runGitOutput(args ...string) ([]byte, error) {
	return runGitOutputContext(context.Background(), args...)
}

func runGitOutputContext(ctx context.Context, args ...string) ([]byte, error) {
	return runGitOutputWithTimeoutContext(ctx, repoCacheGitTimeout, args...)
}

func runGitOutputWithTimeout(timeout time.Duration, args ...string) ([]byte, error) {
	return runGitOutputWithTimeoutContext(context.Background(), timeout, args...)
}

func runGitOutputWithTimeoutContext(parent context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := newGitCommand(args...)
	out, err := processtree.Output(ctx, cmd, 5*time.Second)
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("git command timed out after %s: %w", timeout, ctx.Err())
	}
	return out, err
}

func runGit(args ...string) error {
	return runGitContext(context.Background(), args...)
}

func runGitContext(ctx context.Context, args ...string) error {
	return runGitWithTimeoutContext(ctx, repoCacheGitTimeout, args...)
}

func runGitWithTimeout(timeout time.Duration, args ...string) error {
	return runGitWithTimeoutContext(context.Background(), timeout, args...)
}

func runGitWithTimeoutContext(parent context.Context, timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := newGitCommand(args...)
	err := processtree.Run(ctx, cmd, 5*time.Second)
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("git command timed out after %s: %w", timeout, ctx.Err())
	}
	return err
}

// RepoInfo 描述一个需要缓存的仓库。
type RepoInfo struct {
	URL string
}

// CachedRepo 描述一个已缓存、可以拿来建 worktree 的裸 clone。
type CachedRepo struct {
	URL       string // 远程 URL
	LocalPath string // 裸 clone 的绝对路径
}

// Cache 管理工作区仓库的裸 git clone。
type Cache struct {
	root   string // 所有缓存的根目录（如 ~/multica_workspaces/.repos）
	logger *slog.Logger
	// repoLocks 把每个裸仓库路径映射到一把专属互斥锁。任何对同一裸仓库的写
	// 操作（clone、fetch、worktree add、ref 更新）都必须先拿这把锁——git 自己
	// 的锁文件（packed-refs.lock、config.lock、worktree 管理目录）受不了对同一
	// 仓库并发改；不同仓库之间互相独立，可以并发跑。
	repoLocks sync.Map // barePath -> *repoLock
}

// ErrRepoBusy 表示前台 checkout 在调用方限定的等待时间内没拿到仓库锁。声明过
// 支持重试的调用方可以把它转成可重试的 HTTP 响应，而不是一直干等到传输层超时。
var ErrRepoBusy = errors.New("repository is busy")

// Activity 是不带路径的仓库协调状态，通过守护进程健康检查接口暴露。仅供诊断。
type Activity struct {
	MaintenanceActive int
	ForegroundWaiters int
}

// repoLock 是一把"前台优先"的互斥锁。普通缓存变更和原来 sync.Mutex 一样串行；
// 低优先级维护不同：它只在仓库空闲时启动，且拿到的是一个"一旦有前台操作排队
// 就会被取消"的 context。持有维护锁的一方有责任在解锁前停掉自己的 git 进程树。
type repoLock struct {
	mu                sync.Mutex
	held              bool
	maintenance       bool
	maintenanceCancel context.CancelCauseFunc
	foregroundWaiters int
	changed           chan struct{}
}

// ErrMaintenancePreempted 是前台工作需要同一个仓库时，传给正在跑的维护回调的
// 取消原因。
var ErrMaintenancePreempted = errors.New("repository maintenance preempted by foreground work")

func newRepoLock() *repoLock {
	return &repoLock{changed: make(chan struct{})}
}

func (l *repoLock) LockContext(ctx context.Context) error {
	l.mu.Lock()
	l.foregroundWaiters++
	for {
		if err := ctx.Err(); err != nil {
			l.foregroundWaiters--
			l.signalLocked()
			l.mu.Unlock()
			return context.Cause(ctx)
		}
		if l.maintenanceCancel != nil {
			l.maintenanceCancel(ErrMaintenancePreempted)
		}
		if !l.held {
			l.held = true
			l.maintenance = false
			l.foregroundWaiters--
			l.mu.Unlock()
			return nil
		}
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		l.mu.Lock()
	}
}

func (l *repoLock) Lock() {
	_ = l.LockContext(context.Background())
}

func (l *repoLock) Unlock() {
	l.mu.Lock()
	if !l.held {
		l.mu.Unlock()
		panic("repocache: unlock of unlocked repository")
	}
	if l.maintenanceCancel != nil {
		l.maintenanceCancel(nil)
		l.maintenanceCancel = nil
	}
	l.held = false
	l.maintenance = false
	l.signalLocked()
	l.mu.Unlock()
}

func (l *repoLock) tryLockMaintenance(parent context.Context) (context.Context, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if parent.Err() != nil || l.held || l.foregroundWaiters > 0 {
		return nil, false
	}
	ctx, cancel := context.WithCancelCause(parent)
	l.held = true
	l.maintenance = true
	l.maintenanceCancel = cancel
	return ctx, true
}

func (l *repoLock) signalLocked() {
	close(l.changed)
	l.changed = make(chan struct{})
}

func (l *repoLock) activity() (maintenance bool, waiters int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held && l.maintenance, l.foregroundWaiters
}

func (l *repoLock) cancelMaintenanceAndWait() {
	l.mu.Lock()
	for l.maintenance {
		if l.maintenanceCancel != nil {
			l.maintenanceCancel(ErrMaintenancePreempted)
		}
		changed := l.changed
		l.mu.Unlock()
		<-changed
		l.mu.Lock()
	}
	l.mu.Unlock()
}

// New 在给定目录下新建一个仓库缓存。
func New(root string, logger *slog.Logger) *Cache {
	return &Cache{root: root, logger: logger}
}

// lockForRepo 取给定裸仓库路径专属的那把锁。语义见 Cache.repoLocks 字段注释。
func (c *Cache) lockForRepo(barePath string) *repoLock {
	if l, ok := c.repoLocks.Load(barePath); ok {
		return l.(*repoLock)
	}
	newLock := newRepoLock()
	actual, _ := c.repoLocks.LoadOrStore(barePath, newLock)
	return actual.(*repoLock)
}

// Activity 汇总仓库协调状态，不暴露任何缓存路径。操作正在开始或结束时调用它也
// 是安全的。
func (c *Cache) Activity() Activity {
	var activity Activity
	c.repoLocks.Range(func(_, value any) bool {
		maintenance, waiters := value.(*repoLock).activity()
		if maintenance {
			activity.MaintenanceActive++
		}
		activity.ForegroundWaiters += waiters
		return true
	})
	return activity
}

// CancelMaintenance 停掉所有正在进行的低优先级仓库操作，并等它们把仓库所有权
// 让出来。任务派发在拉起 agent 前用它当一道屏障：任务可能直接复用现成的
// worktree、根本不经过 CreateWorktree 的闸门；同时等维护被中断后的锁清理完成，
// 也能保证 agent 直接跑 git 时不会撞上别人残留的锁文件。
func (c *Cache) CancelMaintenance() {
	c.repoLocks.Range(func(_, value any) bool {
		value.(*repoLock).cancelMaintenanceAndWait()
		return true
	})
}

// Sync 确保一个 workspace 的所有仓库都已被 clone（已在缓存里则 fetch 最新）。
// 列表里已经没有的仓库原地保留（留着不花什么钱，还能避免仓库临时移除又加回时
// 重新 clone）。
//
// 对同一裸路径的变更通过 lockForRepo 与 CreateWorktree 串行。一次 Sync 调用内
// 不同仓库是顺序处理的，但并发多次 Sync（不同 workspace，或同一 workspace 在
// checkout 进行中再次 sync）互不阻塞。
func (c *Cache) Sync(workspaceID string, repos []RepoInfo) error {
	return c.SyncContext(context.Background(), workspaceID, repos)
}

// SyncContext 是 Sync 的可取消版本：取消会一路贯穿仓库锁等待、clone、fetch
// 以及 ref 布局迁移。
func (c *Cache) SyncContext(ctx context.Context, workspaceID string, repos []RepoInfo) error {
	wsDir := filepath.Join(c.root, workspaceID)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		return fmt.Errorf("create workspace cache dir: %w", err)
	}

	var firstErr error
	for _, repo := range repos {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		if repo.URL == "" {
			continue
		}
		barePath := filepath.Join(wsDir, bareDirName(repo.URL))

		repoLock := c.lockForRepo(barePath)
		if err := repoLock.LockContext(ctx); err != nil {
			return err
		}
		if isBareRepo(barePath) {
			// 已有缓存 —— 拉最新。
			c.logger.Info("repo cache: fetching", "url", repo.URL, "path", barePath)
			if err := gitFetchContext(ctx, barePath); err != nil {
				c.logger.Warn("repo cache: fetch failed", "url", repo.URL, "error", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		} else {
			// 没有缓存 —— 从头裸 clone。
			c.logger.Info("repo cache: cloning", "url", repo.URL, "path", barePath)
			if err := gitCloneBareContext(ctx, repo.URL, barePath); err != nil {
				c.logger.Error("repo cache: clone failed", "url", repo.URL, "error", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		repoLock.Unlock()
	}
	return firstErr
}

// Lookup 返回某 workspace 内给定仓库 URL 对应的本地裸 clone 路径；没缓存返回 ""。
func (c *Cache) Lookup(workspaceID, url string) string {
	barePath := c.BarePath(workspaceID, url)
	if isBareRepo(barePath) {
		return barePath
	}
	return ""
}

// BarePath 返回某仓库裸缓存"应该在"的位置，不管它现在存不存在。Lookup 回答
// "缓存了吗？"，这里回答"将来会在哪？"——GC 需要靠它把一串仍在使用的仓库
// URL 映射到它正准备考虑淘汰的目录上。
func (c *Cache) BarePath(workspaceID, url string) string {
	return filepath.Join(c.root, workspaceID, bareDirName(url))
}

// lastUsedFile 记录某任务最后一次从这个裸仓库要 worktree 的时间。它放在裸仓库
// 内部，仓库删了它也就跟着没了。
//
// 目录 mtime 回答不了这个问题：守护进程每次重启都会把已注册 workspace 的仓库
// 列表整个再 sync 一遍，而 sync 会对每个缓存的仓库做 fetch（见 Sync），把几个月
// 没任务 checkout 过的仓库 mtime 也刷新了。atime 更糟：Linux 普遍 noatime，
// Windows 默认直接关掉。所以这个信号必须显式写出来，而且要写在唯一能证明仓库
// 真被用过的地方——CreateWorktree。
const lastUsedFile = ".multica_last_used"

// MarkUsed 记录该裸仓库刚刚被用来 checkout 过。调用方必须先持有仓库锁。这里
// 是尽力而为：写失败了顶多让仓库之后显得很"闲"，GC 对缺失时间戳的宽限期
// （见 LastUsed）会兜住这种误差。
func MarkUsed(barePath string, logger *slog.Logger) {
	if barePath == "" {
		return
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if err := os.WriteFile(filepath.Join(barePath, lastUsedFile), []byte(stamp), 0o644); err != nil && logger != nil {
		logger.Warn("repo cache: write last-used stamp failed", "repo", barePath, "error", err)
	}
}

// LastUsed 报告该裸仓库上次被 checkout 使用的时间，以及是否存在时间戳。
//
// ok=false 表示"未知"，绝不是"很久没用"。所有在这个时间戳机制之前建好的缓存
// 都报未知——要是把未知当成无限久远，升级后的第一次 GC 就会把机器上所有仓库
// 缓存清光、逼着每个仓库重新 clone 一遍。调用方应当给未知的仓库打上当前时间戳，
// 让它从现在开始老化。
func LastUsed(barePath string) (time.Time, bool) {
	data, err := os.ReadFile(filepath.Join(barePath, lastUsedFile))
	if err != nil {
		return time.Time{}, false
	}
	stamp, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}, false
	}
	return stamp, true
}

// WithRepoLock 让调用方自带的裸仓库改动与所有走同一把锁的同仓库操作（Sync、
// Fetch、CreateWorktree，以及守护进程 GC 维护）串行。
func (c *Cache) WithRepoLock(barePath string, fn func() error) error {
	return c.WithRepoLockContext(context.Background(), barePath, fn)
}

// WithRepoLockContext 是 WithRepoLock 的可取消版本。
func (c *Cache) WithRepoLockContext(ctx context.Context, barePath string, fn func() error) error {
	repoLock := c.lockForRepo(barePath)
	if err := repoLock.LockContext(ctx); err != nil {
		return err
	}
	defer repoLock.Unlock()
	return fn()
}

// WithRepoMaintenance 只在仓库空闲时才跑 fn。一旦有前台操作排队，就会取消传给
// fn 的 context，然后等 fn 停掉自己的进程树、把仓库交出来。ran=false 表示本次
// 维护被跳过——前台工作已经持有或在等待这个仓库。
func (c *Cache) WithRepoMaintenance(ctx context.Context, barePath string, fn func(context.Context) error) (ran bool, err error) {
	repoLock := c.lockForRepo(barePath)
	maintenanceCtx, ok := repoLock.tryLockMaintenance(ctx)
	if !ok {
		return false, nil
	}
	defer repoLock.Unlock()
	return true, fn(maintenanceCtx)
}

// Fetch 在已缓存的裸 clone 上执行 `git fetch origin` 拉最新 ref。
func (c *Cache) Fetch(barePath string) error {
	return c.WithRepoLock(barePath, func() error {
		return gitFetch(barePath)
	})
}

// bareDirName 为 rawURL 的裸 clone 生成一个对文件系统安全、且不会撞名的目录名。
// 名字由 host 加每一段路径用 '+' 拼接而成。'+' 在 GitHub/GitLab 的路径段里是
// 不允许出现的字符，所以两个 URL 只有在"指向同一主机的同一仓库"时才会生成同名。
//
// 示例：
//
//	https://github.com/org/my-repo.git           -> github.com+org+my-repo.git
//	git@github.com:org/my-repo                   -> github.com+org+my-repo.git
//	git@github.com:foo/bar-baz.git               -> github.com+foo+bar-baz.git
//	git@github.com:foo-bar/baz.git               -> github.com+foo-bar+baz.git
//	git@github.com:org/repo.git                  -> github.com+org+repo.git
//	git@gitlab.example.com:org/repo.git          -> gitlab.example.com+org+repo.git
//	ssh://git@gitlab.example.com:22/g/s/r.git    -> gitlab.example.com%3A22+g+s+r.git
//	git@gitlab.example.com-22:org/repo.git       -> gitlab.example.com-22+org+repo.git
//	my-repo                                      -> my-repo.git（裸名兜底）
func bareDirName(rawURL string) string {
	rawURL = strings.TrimRight(rawURL, "/")

	host, path := splitHostAndPath(rawURL)
	host = strings.ToLower(strings.TrimSpace(host))
	// 把 ':' 编码成 '%3A' 才能无损地保留 host:port。若天真地做 ':'->'-'，会把
	// `gitlab.example.com:22` 撞成字面主机名 `gitlab.example.com-22`，把本函数
	// 想防的"静默指向错误远端"那类问题又引回来。'%' 在合法主机名（RFC 952 /
	// RFC 1123）以及 GitHub/GitLab 路径段里都是禁用的，所以这个编码标记绝不
	// 可能来自合法输入。
	host = strings.ReplaceAll(host, ":", "%3A")

	var parts []string
	if host != "" {
		parts = append(parts, host)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg != "" {
			parts = append(parts, seg)
		}
	}

	name := strings.Join(parts, "+")
	if !strings.HasSuffix(name, ".git") {
		name += ".git"
	}
	if name == "" || name == ".git" {
		name = "repo.git"
	}
	return name
}

// splitHostAndPath 从受支持的 git URL 形式里拆出 host 与"带命名空间的路径"：
//
//   - URL 形式（ssh://user@host[:port]/path、https://host/path）——原样返回
//     u.Host（可能带 :port），u.Path 去掉开头的斜杠。
//   - scp 风格（[user@]host:path）——在可选的 'user@' 之后的第一个 ':' 处切开。
//   - 其它任何形式（裸仓库名、绝对文件系统路径）——返回空 host，整个输入当路径。
func splitHostAndPath(rawURL string) (host, path string) {
	if u, err := url.Parse(rawURL); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Host, strings.TrimPrefix(u.Path, "/")
	}
	s := rawURL
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// isBareRepo 判断一个路径看起来是不是裸 git 仓库。
func isBareRepo(path string) bool {
	// 裸仓库的特征：根目录下有个 HEAD 文件。
	_, err := os.Stat(filepath.Join(path, "HEAD"))
	return err == nil
}

// modernFetchRefspec 是"远程跟踪"式 refspec：它把拉到的 heads 挡在裸仓库的
// refs/heads/* 之外。那块命名空间是留给 `git worktree add -b ...` 创建的
// 每任务 worktree 分支的，任何以 refs/heads/* 为目标的 mirror 式 fetch 都可能
// 撞上那些已被锁定的 ref，导致整次 fetch 失败。
const modernFetchRefspec = "+refs/heads/*:refs/remotes/origin/*"

func gitCloneBare(url, dest string) error {
	return gitCloneBareContext(context.Background(), url, dest)
}

func gitCloneBareContext(ctx context.Context, url, dest string) error {
	if out, err := runGitCombinedOutputContext(ctx, "clone", "--bare", url, dest); err != nil {
		// 清掉半成品 clone。
		os.RemoveAll(dest)
		return fmt.Errorf("git clone --bare: %s: %w", strings.TrimSpace(string(out)), err)
	}
	// `git clone --bare` 会以快照形式填 refs/heads/*，且默认是 mirror 式 fetch
	// refspec。这里马上把裸仓库转成标准的 remote-tracking 布局，让之后的 fetch
	// 写到 refs/remotes/origin/*，也就不会跟 worktree 锁定的 heads 冲突。
	if err := ensureRemoteTrackingLayoutContext(ctx, dest); err != nil {
		os.RemoveAll(dest)
		return fmt.Errorf("configure fetch refspec: %w", err)
	}
	return nil
}

// gitFetch 在裸缓存上跑 `git fetch origin`；如果它还在用这个包老版本留下的
// mirror 式布局，会先把 fetch refspec 迁到 remote-tracking 布局。fetch 成功后
// 还会刷新 refs/remotes/origin/HEAD，让远端默认分支的变更（比如已有仓库
// master→main）能真正在 getRemoteDefaultBranch 里生效。裸的 `git fetch origin`
// 不会自己动那个 symref——没有这次调用，远端翻转默认分支后，旧缓存会永远把
// 新 worktree 建立在最初的默认分支上。
func gitFetch(barePath string) error {
	return gitFetchContext(context.Background(), barePath)
}

func gitFetchContext(ctx context.Context, barePath string) error {
	if err := ensureRemoteTrackingLayoutContext(ctx, barePath); err != nil {
		return fmt.Errorf("ensure refspec: %w", err)
	}
	if err := runGitFetchContext(ctx, barePath); err != nil {
		return err
	}
	// 每次 fetch 成功后刷新 refs/remotes/origin/HEAD。set-head --auto 很轻量
	// （只做一次 ls-remote HEAD 往返），且不致命：就算失败，getRemoteDefaultBranch
	// 里还有第 2-5 步兜底，但"现代缓存的默认分支变更"这一路径（唯一没法用别
	// 的方式恢复的路径）就指着这次调用。
	_ = runGitContext(ctx, "-C", barePath, "remote", "set-head", "origin", "--auto")
	return nil
}

// runGitFetch 是裸的 `git fetch origin` 包装。调用方应走 gitFetch——它会先迁移
// 老缓存。
func runGitFetch(barePath string) error {
	return runGitFetchContext(context.Background(), barePath)
}

func runGitFetchContext(ctx context.Context, barePath string) error {
	if out, err := runGitCombinedOutputContext(ctx, "-C", barePath, "fetch", "origin"); err != nil {
		return fmt.Errorf("git fetch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// ensureRemoteTrackingLayout 把裸仓库从老式 mirror refspec
// （+refs/heads/*:refs/heads/*）升级成标准的 remote-tracking refspec
// （+refs/heads/*:refs/remotes/origin/*）。幂等：对已经是现代布局的缓存只做一次
// `git config --get`。对老缓存则改写 refspec、做一次回填 fetch 来填出
// refs/remotes/origin/*，并跑 `git remote set-head origin --auto` 让
// getRemoteDefaultBranch 能解析出远端默认分支。
func ensureRemoteTrackingLayout(barePath string) error {
	return ensureRemoteTrackingLayoutContext(context.Background(), barePath)
}

func ensureRemoteTrackingLayoutContext(ctx context.Context, barePath string) error {
	cur, err := readFetchRefspecContext(ctx, barePath)
	if err != nil {
		return err
	}
	if cur == modernFetchRefspec || cur == strings.TrimPrefix(modernFetchRefspec, "+") {
		return nil // 已经是现代布局
	}
	if err := setFetchRefspecContext(ctx, barePath, modernFetchRefspec); err != nil {
		return err
	}
	// 用新 refspec 做一次回填 fetch 来填出 refs/remotes/origin/*。它写到
	// origin/* 命名空间，即使 refs/heads/* 上有被 worktree 锁定的分支也不会撞。
	if err := runGitFetchContext(ctx, barePath); err != nil {
		return fmt.Errorf("backfill fetch after refspec migration: %w", err)
	}
	// 设 refs/remotes/origin/HEAD 好让 getRemoteDefaultBranch 能读到。非致命：
	// 若失败就退回 origin/main、origin/master。
	_ = runGitContext(ctx, "-C", barePath, "remote", "set-head", "origin", "--auto")
	return nil
}

// readFetchRefspec 返回当前 remote.origin.fetch 配置值；没设置则返回空串。区分
// "key 不存在"（退出码 1）和真正的 git 错误。
func readFetchRefspec(barePath string) (string, error) {
	return readFetchRefspecContext(context.Background(), barePath)
}

func readFetchRefspecContext(ctx context.Context, barePath string) (string, error) {
	out, err := runGitOutputContext(ctx, "-C", barePath, "config", "--get", "remote.origin.fetch")
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return "", nil // key 不存在，不算错误
		}
		return "", fmt.Errorf("read remote.origin.fetch: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func setFetchRefspec(barePath, refspec string) error {
	return setFetchRefspecContext(context.Background(), barePath, refspec)
}

func setFetchRefspecContext(ctx context.Context, barePath, refspec string) error {
	out, err := runGitCombinedOutputContext(ctx, "-C", barePath, "config", "remote.origin.fetch", refspec)
	if err != nil {
		return fmt.Errorf("set remote.origin.fetch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// WorktreeParams 是"从缓存裸仓库建 worktree"的输入参数。
type WorktreeParams struct {
	WorkspaceID         string // 拥有该仓库的 workspace
	RepoURL             string // 要在缓存里查找的远程 URL
	WorkDir             string // worktree 的父目录（如任务工作目录）
	Ref                 string // 可选：worktree 基于的分支、tag 或 commit
	AgentName           string // 用于分支命名
	TaskID              string // 用于保证分支名唯一
	CoAuthoredByEnabled bool   // 是否装 prepare-commit-msg 钩子以追加 Co-authored-by 尾注
	// LockWaitTimeout 只限制"等另一笔同仓库操作"的时长。零值保持历史行为：
	// 内部调用和旧调用方无限期等待；支持重试的 HTTP checkout 请求会设一个有限值。
	LockWaitTimeout time.Duration
	// IsolatedGitMetadata 表示要建一个"本地 clone"式 checkout：.git 目录放在
	// WorkDir 里面，而不是建一个 gitdir 落在共享缓存下的 linked worktree。
	// Codex 任务需要它，因为 workspace-write 即使显式把一个已解析的外部 worktree
	// gitdir 列为可写根，也仍会把它当只读——Linux 上（multica-ai/multica#2925）
	// 和 Windows 原生沙箱里（multica-ai/multica#6449）都是这样。
	IsolatedGitMetadata bool
}

// WorktreeResult 描述一个成功建好的 worktree。
type WorktreeResult struct {
	Path       string `json:"path"`        // worktree 的绝对路径
	BranchName string `json:"branch_name"` // 为该 worktree 创建的 git 分支
}

// CreateWorktree 在缓存里查仓库、拉最新代码，然后在 agent 工作目录里建一个 git
// worktree。若目标路径上已有一个 worktree（环境被复用过），则把现有 worktree
// 更新到最新远端默认分支，而不是报错。
func (c *Cache) CreateWorktree(params WorktreeParams) (*WorktreeResult, error) {
	return c.CreateWorktreeContext(context.Background(), params)
}

// CreateWorktreeContext 是 CreateWorktree 的可取消版本：取消会贯穿锁获取和每个
// git 子进程。git 操作只在拿到锁之后才开始，所以一个卡在维护后面而超时的客户端
// 不会留下一个迟到、多余的 checkout。
func (c *Cache) CreateWorktreeContext(ctx context.Context, params WorktreeParams) (*WorktreeResult, error) {
	barePath := c.Lookup(params.WorkspaceID, params.RepoURL)
	if barePath == "" {
		return nil, fmt.Errorf("repo not found in cache: %s (workspace: %s)", params.RepoURL, params.WorkspaceID)
	}

	// 同一裸仓库上的并发 CreateWorktree 要串行。git 自己的锁文件
	// （packed-refs.lock、config.lock、worktree 管理目录）受不了在同一仓库上
	// 并行做 fetch + worktree 改动。
	repoLock := c.lockForRepo(barePath)
	lockCtx := ctx
	cancel := func() {}
	if params.LockWaitTimeout > 0 {
		lockCtx, cancel = context.WithTimeout(ctx, params.LockWaitTimeout)
	}
	err := repoLock.LockContext(lockCtx)
	cancel()
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %s", ErrRepoBusy, params.RepoURL)
		}
		return nil, err
	}
	defer repoLock.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}

	// 在动手前就打时间戳，而不是成功后：任务来要 worktree 这件事本身就代表
	// "这个缓存还有人要"，无论 checkout 最终成不成。只在成功时打的话，一个
	// checkout 老失败的仓库会从仍在尝试使用它的任务脚下被老化清走。
	MarkUsed(barePath, c.logger)

	// 从 origin 拉最新。首次运行还会顺带把裸缓存的 refspec 迁到现代
	// remote-tracking 布局，这样后续 fetch 永远不会撞上建 worktree 时在这个裸
	// 仓库里锁定的 refs/heads/agent/* 分支。
	if err := gitFetchContext(ctx, barePath); err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		// 非致命：保留缓存里已有状态继续往下走，但要把告警喊得够响，保证在
		// 守护进程日志里找得到。agent 拿到的是比远端 head 旧一点的快照。
		c.logger.Warn("repo checkout: fetch failed, agent will see possibly stale code",
			"url", params.RepoURL,
			"error", err,
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}

	// 决定 worktree 基于哪个 ref。默认用远端默认分支（内部由
	// getRemoteDefaultBranch 解析：origin/HEAD → origin/main、origin/master →
	// bare-HEAD 提示进 origin/<同款> → 扫描 origin/* 且只有一项 → origin/* 为空
	// 时用 bare HEAD）。调用方可以指定分支、tag 或 commit，这样 review/QA agent
	// 能检查到确切的版本，而不必自己去改守护进程托管的 worktree 元数据。
	baseRef, err := resolveBaseRefContext(ctx, barePath, params.Ref)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}

	// 这里为空说明 params.Ref 没设，且 getRemoteDefaultBranch 也解不出默认分支——
	// 缓存处于一种我们拒绝瞎猜的状态（没有 origin/HEAD、没有 main/master、bare
	// HEAD 匹配不上任何 origin/* 项、origin/* 又有多项）。指定 ref 的路径在这之前
	// 就会返回显式错误，所以只有默认分支这条路径会走到这里。
	if baseRef == "" {
		return nil, fmt.Errorf("cannot resolve default branch for %s: bare cache at %s has no usable refs (origin/* is empty or ambiguous and bare HEAD has no match). The cache may be corrupted; delete it and retry", params.RepoURL, barePath)
	}

	// 组分支名：agent/{清洗过的名字}/{task-id}
	branchName := fmt.Sprintf("agent/%s/%s", sanitizeName(params.AgentName), taskKey(params.TaskID))

	// 从仓库 URL 推导目录名。
	dirName := repoNameFromURL(params.RepoURL)
	worktreePath := filepath.Join(params.WorkDir, dirName)

	// 一旦某个 workdir 切到了 isolated 元数据模式，就继续沿用这种更稳的形态，
	// 哪怕后来的任务来自没带这个模式提示的老 CLI 或别的 runtime。这也让复用的
	// workdir 在不同 provider 之间切换时保持向后兼容。
	if params.IsolatedGitMetadata || isIsolatedCheckoutContext(ctx, worktreePath) {
		actualBranch, err := c.createOrUpdateIsolatedCheckoutContext(
			ctx,
			barePath,
			params.RepoURL,
			worktreePath,
			branchName,
			baseRef,
		)
		if err != nil {
			return nil, fmt.Errorf("create isolated checkout: %w", err)
		}

		for _, pattern := range agentGitExcludePatterns {
			_ = excludeFromGitContext(ctx, worktreePath, pattern)
		}
		c.applyCoAuthoredBySettingContext(ctx, worktreePath, params)
		if err := ctx.Err(); err != nil {
			return nil, context.Cause(ctx)
		}

		c.logger.Info("repo checkout: isolated checkout ready",
			"url", params.RepoURL,
			"path", worktreePath,
			"branch", actualBranch,
			"base", baseRef,
		)
		return &WorktreeResult{Path: worktreePath, BranchName: actualBranch}, nil
	}

	// worktree 已存在（复用之前任务的环境）的话，把它更新到最新远端代码，而不
	// 是新建一个。
	if isGitWorktree(worktreePath) {
		actualBranch, err := updateExistingWorktreeContext(ctx, worktreePath, branchName, baseRef)
		if err != nil {
			return nil, fmt.Errorf("update existing worktree: %w", err)
		}

		for _, pattern := range agentGitExcludePatterns {
			_ = excludeFromGitContext(ctx, worktreePath, pattern)
		}

		// 把 Co-authored-by 钩子和 workspace 记录的开关对齐。钩子活在裸仓库的
		// 共享 hooks 目录里，所以关掉时必须主动删——否则用户把开关切掉之后，
		// 之前装的钩子还会继续往每个 commit 追加尾注。
		c.applyCoAuthoredBySettingContext(ctx, worktreePath, params)
		if err := ctx.Err(); err != nil {
			return nil, context.Cause(ctx)
		}

		c.logger.Info("repo checkout: existing worktree updated",
			"url", params.RepoURL,
			"path", worktreePath,
			"branch", actualBranch,
			"base", baseRef,
		)

		return &WorktreeResult{
			Path:       worktreePath,
			BranchName: actualBranch,
		}, nil
	}

	// 新建 worktree。createWorktree 可能为了避免和上次运行残留的陈旧 per-task
	// ref 撞名而给分支改名。
	actualBranch, err := createWorktreeContext(ctx, barePath, worktreePath, branchName, baseRef)
	if err != nil {
		return nil, fmt.Errorf("create worktree: %w", err)
	}

	// 把 agent 上下文文件排除出 git 跟踪。
	for _, pattern := range agentGitExcludePatterns {
		_ = excludeFromGitContext(ctx, worktreePath, pattern)
	}

	// 把 Co-authored-by 钩子和 workspace 记录的开关对齐。开关被关掉时必须主动
	// 删钩子，理由见上面"更新既有 worktree"分支里的说明。
	c.applyCoAuthoredBySettingContext(ctx, worktreePath, params)
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}

	c.logger.Info("repo checkout: worktree created",
		"url", params.RepoURL,
		"path", worktreePath,
		"branch", actualBranch,
		"base", baseRef,
	)

	return &WorktreeResult{
		Path:       worktreePath,
		BranchName: actualBranch,
	}, nil
}

const (
	isolatedCheckoutConfigKey   = "multica.checkout-mode"
	isolatedCheckoutConfigValue = "isolated"
	isolatedCacheRemoteName     = "multica-cache"
)

// createOrUpdateIsolatedCheckout 让 git 元数据待在任务 workdir 里。全新路径用
// local clone：不可变 git 对象会从守护进程缓存硬链接过来（Windows 上则是拷贝，
// 见 localCloneArgs），而 refs、index、日志、config 和新对象都归任务 checkout
// 私有。随后那个临时的缓存 remote 会被换成真实仓库 URL，这样 agent 平时跑
// fetch / push 打到的仍是 GitHub，而不是守护进程托管的裸缓存。
func (c *Cache) createOrUpdateIsolatedCheckout(barePath, repoURL, checkoutPath, branchName, baseRef string) (string, error) {
	return c.createOrUpdateIsolatedCheckoutContext(context.Background(), barePath, repoURL, checkoutPath, branchName, baseRef)
}

func (c *Cache) createOrUpdateIsolatedCheckoutContext(ctx context.Context, barePath, repoURL, checkoutPath, branchName, baseRef string) (string, error) {
	baseCommit, err := resolveCommitContext(ctx, barePath, baseRef)
	if err != nil {
		return "", err
	}

	if isIsolatedCheckoutContext(ctx, checkoutPath) {
		if err := setIsolatedCheckoutOriginContext(ctx, checkoutPath, repoURL); err != nil {
			return "", err
		}
		// 幂等，而且对"缓存还是完整 clone 时首次建出来的 workdir"是必须的：
		// 没有它，靠 blobless 缓存支撑的 checkout 会把缺失的 blob 解析成空，
		// 而不会去 fetch。
		if isPartialCloneContext(ctx, barePath) {
			if err := configurePromisorRemoteContext(ctx, checkoutPath); err != nil {
				return "", err
			}
		}
		if err := syncIsolatedCheckoutRefsContext(ctx, barePath, checkoutPath, baseRef); err != nil {
			return "", err
		}
		actualBranch, err := updateExistingWorktreeContext(ctx, checkoutPath, branchName, baseCommit)
		if err != nil {
			return "", err
		}
		// 丢掉之前任务留下的 agent/* head，别让一个复用的 workdir 每次 checkout
		// 都长一个新本地分支。非致命：残留分支只是无害杂物，绝不能拖垮 checkout。
		if err := deleteStaleAgentBranchesContext(ctx, checkoutPath, actualBranch); err != nil {
			c.logger.Warn("repo checkout: prune stale branches failed (non-fatal)", "error", err)
		}
		return actualBranch, nil
	}
	// 守护进程升级后可能接住一个修复前 Codex 留下的、仍是 linked worktree 的
	// workdir。用 git 命令移除它（把共享 admin 记录一并清掉），再用本地元数据把
	// 同一 checkout 路径重建出来。
	if isGitWorktree(checkoutPath) {
		if err := removeLinkedWorktreeContext(ctx, barePath, checkoutPath); err != nil {
			return "", err
		}
	}
	if _, err := os.Stat(checkoutPath); err == nil {
		return "", fmt.Errorf("checkout path already exists and is not a Multica isolated checkout: %s", checkoutPath)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat checkout path: %w", err)
	}

	return createIsolatedCheckoutContext(ctx, barePath, repoURL, checkoutPath, branchName, baseRef, baseCommit)
}

func removeLinkedWorktree(barePath, checkoutPath string) error {
	return removeLinkedWorktreeContext(context.Background(), barePath, checkoutPath)
}

func removeLinkedWorktreeContext(ctx context.Context, barePath, checkoutPath string) error {
	out, err := runGitOutputContext(ctx, "-C", checkoutPath, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("resolve linked worktree common dir: %w", err)
	}
	commonDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(checkoutPath, commonDir)
	}
	if !sameResolvedPath(commonDir, barePath) {
		return fmt.Errorf("linked worktree common dir %s does not match cache %s", commonDir, barePath)
	}
	if out, err := runGitCombinedOutputContext(ctx, "-C", barePath, "worktree", "remove", "--force", checkoutPath); err != nil {
		return fmt.Errorf("remove linked worktree: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// sameResolvedPath 判断 a、b 在解析成绝对、无符号链接、清洗后的形式后是否指向
// 同一位置。它做的是"路径相等"检查（不是"同一设备"检查）：迁移防护用它在删
// worktree 前确认 linked worktree 的 git-common-dir 确实指向这个缓存裸仓库。
func sameResolvedPath(a, b string) bool {
	clean := func(path string) string {
		abs, err := filepath.Abs(path)
		if err == nil {
			path = abs
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		return filepath.Clean(path)
	}
	return clean(a) == clean(b)
}

// localCloneArgs 拼出 `git clone --local` 的命令行参数，用来从共享裸缓存"播种"
// 出一个全新的 isolated checkout。
//
// Windows 上 clone 还会带 --no-hardlinks。local clone 能硬链接 .git/objects 就
// 会硬链接，但 NTFS 硬链接只存在于同一卷内，而且每个链接共享同一个底层文件、
// 同一份安全描述符。改成拷贝对象可以让"缓存和 workdir 落在不同盘"照样工作，
// 也能避免任务的 checkout——其文件树被沙箱设成可写——反过来改写守护进程缓存的
// 对象文件权限。代价是 Windows 上首次 checkout 更慢、更费盘。
func localCloneArgs(goos, barePath, checkoutPath string) []string {
	args := []string{"clone", "--local", "--no-checkout", "--no-tags"}
	if goos == "windows" {
		args = append(args, "--no-hardlinks")
	}
	return append(args, "--origin", isolatedCacheRemoteName, barePath, checkoutPath)
}

func createIsolatedCheckout(barePath, repoURL, checkoutPath, branchName, baseRef, baseCommit string) (_ string, retErr error) {
	return createIsolatedCheckoutContext(context.Background(), barePath, repoURL, checkoutPath, branchName, baseRef, baseCommit)
}

func createIsolatedCheckoutContext(ctx context.Context, barePath, repoURL, checkoutPath, branchName, baseRef, baseCommit string) (_ string, retErr error) {
	if out, err := runGitCombinedOutputContext(
		ctx,
		localCloneArgs(runtime.GOOS, barePath, checkoutPath)...,
	); err != nil {
		// 这里不要删 checkoutPath。若在我们预先检查之后，另一个同名但不同的
		// 仓库赢了路径竞争，git 会安全地失败；此时删路径等于把人家刚建好的
		// checkout 毁掉。
		return "", fmt.Errorf("git clone --local: %s: %w", strings.TrimSpace(string(out)), err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(checkoutPath)
		}
	}()

	// 缓存是 partial clone 时，origin 的替换必须先于第一次 checkout。
	// `git clone --local` 只硬链接它看得到的对象、不会继承 promisor 配置，于是
	// blobless 缓存造出的 checkout 里 blob 全部不可达——而 git 会把这事报成成功，
	// 每个文件都显示成 "deleted" 而不是报错。先把 origin 指到真实远端并恢复
	// promisor 配置，下面的 checkout 才能按需懒拉它缺的东西。
	if out, err := runGitCombinedOutputContext(ctx, "-C", checkoutPath, "remote", "remove", isolatedCacheRemoteName); err != nil {
		return "", fmt.Errorf("remove cache remote: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if out, err := runGitCombinedOutputContext(ctx, "-C", checkoutPath, "remote", "add", "origin", repoURL); err != nil {
		return "", fmt.Errorf("add origin remote: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if isPartialCloneContext(ctx, barePath) {
		if err := configurePromisorRemoteContext(ctx, checkoutPath); err != nil {
			return "", err
		}
	}

	if out, err := runGitCombinedOutputContext(ctx, "-C", checkoutPath, "checkout", "--detach", baseCommit); err != nil {
		return "", fmt.Errorf("git checkout --detach: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if err := deleteAllLocalBranchesContext(ctx, checkoutPath); err != nil {
		return "", err
	}
	if err := syncIsolatedCheckoutRefsContext(ctx, barePath, checkoutPath, baseRef); err != nil {
		return "", err
	}
	if out, err := runGitCombinedOutputContext(ctx, "-C", checkoutPath, "config", isolatedCheckoutConfigKey, isolatedCheckoutConfigValue); err != nil {
		return "", fmt.Errorf("mark isolated checkout: %s: %w", strings.TrimSpace(string(out)), err)
	}

	actualBranch, err := checkoutNewBranchContext(ctx, checkoutPath, branchName, baseCommit)
	if err != nil {
		return "", err
	}
	cleanup = false
	return actualBranch, nil
}

func resolveCommit(repoPath, ref string) (string, error) {
	return resolveCommitContext(context.Background(), repoPath, ref)
}

func resolveCommitContext(ctx context.Context, repoPath, ref string) (string, error) {
	out, err := runGitOutputContext(ctx, "-C", repoPath, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve checkout base %q: %w", ref, err)
	}
	commit := strings.TrimSpace(string(out))
	if commit == "" {
		return "", fmt.Errorf("resolve checkout base %q: empty commit", ref)
	}
	return commit, nil
}

func isIsolatedCheckout(path string) bool {
	return isIsolatedCheckoutContext(context.Background(), path)
}

func isIsolatedCheckoutContext(ctx context.Context, path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	if err != nil || !info.IsDir() {
		return false
	}
	out, err := runGitOutputContext(ctx, "-C", path, "config", "--get", isolatedCheckoutConfigKey)
	return err == nil && strings.TrimSpace(string(out)) == isolatedCheckoutConfigValue
}

// partialCloneFilter 是建 blobless partial clone 时用的对象过滤器；任何继承了
// 这种 clone 的"不完整对象库"的仓库，都必须把这个值恢复回去。
const partialCloneFilter = "blob:none"

// isPartialClone 报告某个仓库是不是 partial clone 建出来的——也就是说 git 会
// 不会从它的 promisor remote 按需懒拉缺失对象。
func isPartialClone(repoPath string) bool {
	return isPartialCloneContext(context.Background(), repoPath)
}

func isPartialCloneContext(ctx context.Context, repoPath string) bool {
	out, err := runGitOutputWithTimeoutContext(ctx, 30*time.Second, "-C", repoPath, "config", "--get", "remote.origin.promisor")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// configurePromisorRemote 把 origin 标记为一个对象库不完整的仓库的 promisor
// remote，让 git 从真实远端懒拉缺失 blob，而不是直接失败。它复刻了
// `git clone --filter=blob:none` 写下的两个配置键；`git clone --local` 不会把
// 它们带过来，所以得手工恢复。
func configurePromisorRemote(repoPath string) error {
	return configurePromisorRemoteContext(context.Background(), repoPath)
}

func configurePromisorRemoteContext(ctx context.Context, repoPath string) error {
	settings := [][2]string{
		{"remote.origin.promisor", "true"},
		{"remote.origin.partialclonefilter", partialCloneFilter},
	}
	for _, kv := range settings {
		if out, err := runGitCombinedOutputContext(ctx, "-C", repoPath, "config", kv[0], kv[1]); err != nil {
			return fmt.Errorf("set %s: %s: %w", kv[0], strings.TrimSpace(string(out)), err)
		}
	}
	return nil
}

func setIsolatedCheckoutOrigin(path, repoURL string) error {
	return setIsolatedCheckoutOriginContext(context.Background(), path, repoURL)
}

func setIsolatedCheckoutOriginContext(ctx context.Context, path, repoURL string) error {
	out, err := runGitCombinedOutputContext(ctx, "-C", path, "remote", "set-url", "origin", repoURL)
	if err != nil {
		return fmt.Errorf("set origin remote: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// syncIsolatedCheckoutRefs 把缓存里真实的 origin/* 与 tag ref 镜像进任务本地
// 仓库。它还会直接 fetch 选中的 base ref，这样复用的 checkout 能挪到新拉到的
// commit 上，且本函数返回后不再依赖缓存。
func syncIsolatedCheckoutRefs(barePath, checkoutPath, baseRef string) error {
	return syncIsolatedCheckoutRefsContext(context.Background(), barePath, checkoutPath, baseRef)
}

func syncIsolatedCheckoutRefsContext(ctx context.Context, barePath, checkoutPath, baseRef string) error {
	refspecs := []string{
		"+refs/remotes/origin/*:refs/remotes/origin/*",
		"+refs/tags/*:refs/tags/*",
	}
	args := []string{"-C", checkoutPath, "fetch", "--force", "--no-tags", barePath}
	args = append(args, refspecs...)
	if out, err := runGitCombinedOutputContext(ctx, args...); err != nil {
		return fmt.Errorf("sync cache refs: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if out, err := runGitCombinedOutputContext(ctx, "-C", checkoutPath, "fetch", "--force", "--no-tags", barePath, baseRef); err != nil {
		return fmt.Errorf("fetch checkout base: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// deleteAllLocalBranches 删掉从裸缓存带进全新本地 clone 的那些 heads。这个
// clone 是 detached 的，还没有任何需要保留的任务分支。
func deleteAllLocalBranches(repoPath string) error {
	return deleteAllLocalBranchesContext(context.Background(), repoPath)
}

func deleteAllLocalBranchesContext(ctx context.Context, repoPath string) error {
	return deleteLocalBranchesUnderContext(ctx, repoPath, "refs/heads/", "")
}

// deleteStaleAgentBranches 清理更早 Multica 任务留下的分支，同时保留当前任务
// 分支和所有用户自建的本地分支。
func deleteStaleAgentBranches(repoPath, keepBranch string) error {
	return deleteStaleAgentBranchesContext(context.Background(), repoPath, keepBranch)
}

func deleteStaleAgentBranchesContext(ctx context.Context, repoPath, keepBranch string) error {
	return deleteLocalBranchesUnderContext(ctx, repoPath, "refs/heads/agent/", "refs/heads/"+keepBranch)
}

func deleteLocalBranchesUnder(repoPath, namespace, keepRef string) error {
	return deleteLocalBranchesUnderContext(context.Background(), repoPath, namespace, keepRef)
}

func deleteLocalBranchesUnderContext(ctx context.Context, repoPath, namespace, keepRef string) error {
	out, err := runGitOutputContext(ctx, "-C", repoPath, "for-each-ref", "--format=%(refname)", namespace)
	if err != nil {
		return fmt.Errorf("list local branches: %w", err)
	}
	for _, ref := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" || ref == keepRef {
			continue
		}
		if out, err := runGitCombinedOutputContext(ctx, "-C", repoPath, "update-ref", "-d", ref); err != nil {
			return fmt.Errorf("delete local branch %s: %s: %w", ref, strings.TrimSpace(string(out)), err)
		}
	}
	return nil
}

func checkoutNewBranch(repoPath, branchName, baseRef string) (string, error) {
	return checkoutNewBranchContext(context.Background(), repoPath, branchName, baseRef)
}

func checkoutNewBranchContext(ctx context.Context, repoPath, branchName, baseRef string) (string, error) {
	out, err := runGitCombinedOutputContext(ctx, "-C", repoPath, "checkout", "-b", branchName, baseRef)
	if err == nil {
		return branchName, nil
	}
	wrapped := fmt.Errorf("git checkout -b: %s: %w", strings.TrimSpace(string(out)), err)
	if !isBranchCollisionError(wrapped) {
		return "", wrapped
	}
	branchName = fmt.Sprintf("%s-%d", branchName, time.Now().Unix())
	if out2, err2 := runGitCombinedOutputContext(ctx, "-C", repoPath, "checkout", "-b", branchName, baseRef); err2 != nil {
		return "", fmt.Errorf("git checkout -b (retry): %s: %w", strings.TrimSpace(string(out2)), err2)
	}
	return branchName, nil
}

func resolveBaseRef(barePath, requestedRef string) (string, error) {
	return resolveBaseRefContext(context.Background(), barePath, requestedRef)
}

func resolveBaseRefContext(ctx context.Context, barePath, requestedRef string) (string, error) {
	ref := strings.TrimSpace(requestedRef)
	if ref == "" {
		return getRemoteDefaultBranchContext(ctx, barePath), nil
	}

	// 优先用远程跟踪分支解析人类可读的分支名；其次才允许裸缓存里存在完整本地
	// ref、tag 和裸 commit。
	candidates := []string{
		"refs/remotes/origin/" + ref,
		"refs/tags/" + ref,
		ref,
	}
	for _, candidate := range candidates {
		if gitRefExistsContext(ctx, barePath, candidate+"^{commit}") {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("cannot resolve requested ref %q in repo cache at %s", ref, barePath)
}

func gitRefExists(repoPath, ref string) bool {
	return gitRefExistsContext(context.Background(), repoPath, ref)
}

func gitRefExistsContext(ctx context.Context, repoPath, ref string) bool {
	return runGitContext(ctx, "-C", repoPath, "rev-parse", "--verify", "--quiet", ref) == nil
}

// createWorktree 在给定路径用一个新分支建 git worktree。返回实际用到的分支名——
// 若撞名后以追加时间戳后缀消解，可能与请求的 branchName 不同。
func createWorktree(gitRoot, worktreePath, branchName, baseRef string) (string, error) {
	return createWorktreeContext(context.Background(), gitRoot, worktreePath, branchName, baseRef)
}

func createWorktreeContext(ctx context.Context, gitRoot, worktreePath, branchName, baseRef string) (string, error) {
	// 预先检查：目标路径若已存在，`git worktree add` 会报一句让人摸不着头脑的
	// "already exists"——过去它还会被误判成分支撞名，导致重试逻辑往裸仓库里漏
	// 出分支。这里干脆干净地失败。调用方应当通过 isGitWorktree 把复用的 workdir
	// 路由到 updateExistingWorktree。
	if _, err := os.Stat(worktreePath); err == nil {
		return "", fmt.Errorf("worktree path already exists and is not a valid git worktree: %s", worktreePath)
	}

	err := runWorktreeAddContext(ctx, gitRoot, worktreePath, branchName, baseRef)
	if err != nil && isBranchCollisionError(err) {
		// 分支名撞了：加时间戳，再试一次。
		branchName = fmt.Sprintf("%s-%d", branchName, time.Now().Unix())
		err = runWorktreeAddContext(ctx, gitRoot, worktreePath, branchName, baseRef)
	}
	if err != nil {
		return "", err
	}
	return branchName, nil
}

func runWorktreeAdd(gitRoot, worktreePath, branchName, baseRef string) error {
	return runWorktreeAddContext(context.Background(), gitRoot, worktreePath, branchName, baseRef)
}

func runWorktreeAddContext(ctx context.Context, gitRoot, worktreePath, branchName, baseRef string) error {
	if out, err := runGitCombinedOutputContext(ctx, "-C", gitRoot, "worktree", "add", "-b", branchName, worktreePath, baseRef); err != nil {
		return fmt.Errorf("git worktree add: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// isBranchCollisionError 判断 err 是否专门指"分支名已存在"。git 其它"已存在"
// 类报错（尤其是 `git worktree add` 的路径撞名）绝不能当成分支撞名处理，否则
// "加时间戳重试"的逻辑会在原路径冲突依旧失败的情况下白白漏出分支。
func isBranchCollisionError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// git 的报错原文是 "fatal: a branch named 'X' already exists"。
	return strings.Contains(msg, "a branch named")
}

// isGitWorktree 判断一个路径是不是已存在的 git worktree。worktree 的特征是
// .git 是文件而非目录，文件内容指向主仓库。
func isGitWorktree(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && !info.IsDir()
}

// updateExistingWorktree 把 worktree 重置到干净状态，再从默认分支检出一个新
// 分支。调用方要自己提前 fetch 裸缓存（worktree 与裸缓存共享同一对象库）。返回
// 实际用到的分支名（撞名时可能与入参不同）。
func updateExistingWorktree(worktreePath, branchName, baseRef string) (string, error) {
	return updateExistingWorktreeContext(context.Background(), worktreePath, branchName, baseRef)
}

func updateExistingWorktreeContext(ctx context.Context, worktreePath, branchName, baseRef string) (string, error) {
	// 丢掉上个任务残留的未提交改动。
	if out, err := runGitCombinedOutputContext(ctx, "-C", worktreePath, "reset", "--hard"); err != nil {
		return "", fmt.Errorf("git reset --hard: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// 清掉未跟踪文件（比如上个任务的构建产物）。
	if out, err := runGitCombinedOutputContext(ctx, "-C", worktreePath, "clean", "-fd"); err != nil {
		return "", fmt.Errorf("git clean -fd: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// 从解析好的默认分支 ref 新建分支并切过去。baseRef 是 getRemoteDefaultBranch
	// 返回的 ref 路径——通常是 "refs/remotes/origin/<分支>"，但老式/待迁移的缓存
	// 也可能是 "refs/heads/<分支>"。两种形式都能当 checkout 起点用。
	out, err := runGitCombinedOutputContext(ctx, "-C", worktreePath, "checkout", "-b", branchName, baseRef)
	if err == nil {
		return branchName, nil
	}
	wrapped := fmt.Errorf("git checkout -b: %s: %w", strings.TrimSpace(string(out)), err)
	if !isBranchCollisionError(wrapped) {
		return "", wrapped
	}
	// 分支名撞了：加时间戳，再试一次。
	branchName = fmt.Sprintf("%s-%d", branchName, time.Now().Unix())
	if out2, err2 := runGitCombinedOutputContext(ctx, "-C", worktreePath, "checkout", "-b", branchName, baseRef); err2 != nil {
		return "", fmt.Errorf("git checkout -b (retry): %s: %w", strings.TrimSpace(string(out2)), err2)
	}
	return branchName, nil
}

// getRemoteDefaultBranch 返回裸缓存中指向远端默认分支的 ref 路径（如
// "refs/remotes/origin/main"），可直接当 `git worktree add` / `git checkout -b`
// 的起点用。
//
// 解析顺序：
//  1. refs/remotes/origin/HEAD（先做校验；由 `git remote set-head origin --auto`
//     设置）。
//  2. refs/remotes/origin/main、refs/remotes/origin/master（常见默认名）。
//  3. 把裸仓库自己的 HEAD 映射进 refs/remotes/origin/<同名> —— `git clone --bare`
//     会把 HEAD 设成远端默认分支，所以当 `git remote set-head --auto` 没填出
//     refs/remotes/origin/HEAD 时，这是自定义默认分支（trunk、develop……）的
//     可靠线索。
//  4. 扫描 refs/remotes/origin/* —— 只有当恰好存在一个非 HEAD ref 时才返回。
//     多个 ref 单看 refname 顺序消解不了歧义（git for-each-ref 按字母排），所以
//     拒绝瞎猜；猜错默认分支会静默地把新的 agent 工作建立在某条功能分支上。
//  5. 兜底的旧式路径：对还没填出 refs/remotes/origin/* 的缓存（比如回填 fetch
//     失败的待迁移缓存），直接用裸仓库自己的 HEAD 当 refs/heads/* ref。以
//     refs/remotes/origin/* 完全为空作为门槛——缓存明明有真实 remote-tracking
//     ref、只是我们挑不出来时，绝不回退到陈旧的快照。
//
// 只有上述都解不出时才返回 ""——调用方会把它当成带清晰 "cache has no usable
// refs" 报错的硬错误。
func getRemoteDefaultBranch(barePath string) string {
	return getRemoteDefaultBranchContext(context.Background(), barePath)
}

func getRemoteDefaultBranchContext(ctx context.Context, barePath string) string {
	// 1) 首选：ensureRemoteTrackingLayout 期间由 `git remote set-head origin
	//    --auto` 填出的 refs/remotes/origin/HEAD。要校验目标确实存在——一次不完整
	//    的 set-head 或被人为弄坏的仓库可能留下指向已删 ref 的 symref，直接把它
	//    返回会让后面的 `git worktree add` 报出莫名其妙的 "invalid reference"。
	if out, err := runGitOutputContext(ctx, "-C", barePath, "symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
		ref := strings.TrimSpace(string(out))
		if ref != "" {
			if err := runGitContext(ctx, "-C", barePath, "rev-parse", "--verify", ref); err == nil {
				return ref
			}
		}
	}
	// 2) origin 命名空间下常见的默认分支名。
	for _, candidate := range []string{"refs/remotes/origin/main", "refs/remotes/origin/master"} {
		if err := runGitContext(ctx, "-C", barePath, "rev-parse", "--verify", candidate); err == nil {
			return candidate
		}
	}
	// 3) 拿裸仓库自己的 HEAD 当线索。`git clone --bare` 把 HEAD 设成远端默认
	//    分支，所以当 set-head --auto 没填出 refs/remotes/origin/HEAD 时，它
	//    能可靠地认出自定义默认分支名（trunk、develop……）。只有对应的
	//    origin/<同名> 存在时才返回，这样拿到的是最新代码而不是陈旧的本地 head。
	bareRef := bareHeadBranchContext(ctx, barePath)
	if bareRef != "" {
		originRef := "refs/remotes/origin/" + strings.TrimPrefix(bareRef, "refs/heads/")
		if err := runGitContext(ctx, "-C", barePath, "rev-parse", "--verify", originRef); err == nil {
			return originRef
		}
	}
	// 4) 扫描 refs/remotes/origin/* —— 只有恰好存在一个非 HEAD 候选时才返回。
	//    多个候选没法单靠 refname 顺序消歧；返回按字母排第一个的条目会把新的
	//    agent 工作静默建立在功能分支而非真正的默认分支上。在这里数一下条目，
	//    好让第 5 步能区分"旧式空布局"和"多候选歧义"。
	originCount := 0
	var singleton string
	if out, err := runGitOutputContext(ctx, "-C", barePath, "for-each-ref", "--format=%(refname)", "refs/remotes/origin/"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || line == "refs/remotes/origin/HEAD" {
				continue
			}
			originCount++
			if singleton == "" {
				singleton = line
			}
		}
		if originCount == 1 {
			return singleton
		}
	}
	// 5) 兜底：旧式/待迁移缓存的 refs/heads/* 和 bare HEAD 还是 mirror 布局。
	//    以 refs/remotes/origin/* 完全为空作为门槛——如果 origin/* 有多条 ref
	//    只是都不匹配 bare HEAD，说明缓存处于歧义状态，此时返回本地 head 会拿
	//    陈旧快照掩盖问题。让调用方大声失败就好。
	if originCount == 0 && bareRef != "" {
		return bareRef
	}
	return ""
}

// bareHeadBranch 返回裸仓库的本地 HEAD ref（如 "refs/heads/main"），前提是
// HEAD 是指向某条现存分支的符号引用。HEAD 处于 detached、缺失或指向不存在的
// ref 时返回 ""。
//
// 只被 getRemoteDefaultBranch 用作对"还没成功填出 refs/remotes/origin/*"的缓存
// 的最后兜底。健康的现代缓存走 origin/* 解析先成功，永远到不了这里。
func bareHeadBranch(barePath string) string {
	return bareHeadBranchContext(context.Background(), barePath)
}

func bareHeadBranchContext(ctx context.Context, barePath string) string {
	out, err := runGitOutputContext(ctx, "-C", barePath, "symbolic-ref", "HEAD")
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(out))
	if ref == "" {
		return ""
	}
	if err := runGitContext(ctx, "-C", barePath, "rev-parse", "--verify", ref); err != nil {
		return ""
	}
	return ref
}

// multicaHookMarker 是守护进程装的每个 prepare-commit-msg 钩子里都要嵌进去的
// 哨兵注释。removeCoAuthoredByHook 靠它认出"自己装的"钩子，绝不会误删用户或
// 其它工具装的钩子。改动它必须同步更新识别逻辑。
const multicaHookMarker = "# multica:prepare-commit-msg:co-authored-by"

// daemonInstalledHookSignatures 列出一组能识别 prepare-commit-msg 钩子是否由
// 守护进程安装的子串。removeCoAuthoredByHook 只要看到钩子内容命中其中任意一个
// 子串，就把它当作 Multica 所有。列表特意包含 multicaHookMarker 出现之前老版本
// 守护进程用过的旧注释，这样在存量安装上关掉开关时，也能清掉更早守护进程种下的
// 老钩子。往里加条目可以——永远不要删条目——这样将来调整 prepareCommitMsgHook
// 时，仍能认出每一种历史上发布过的变体。
var daemonInstalledHookSignatures = []string{
	multicaHookMarker,
	"# Installed by the Multica daemon.",
}

// coAuthoredByStateFile 记录 workspace 当前的 Co-authored-by 开关，供已落盘的
// 钩子读取。它放在 workspace 的裸缓存旁边（`<root>/<workspace-id>/`），守护进程
// 每次获知开关变化都会重写它——所以在开关翻转前就建好的 checkout，提交时也会
// 按当前值来。没有它，这个决定会在 checkout 时刻被冻结进钩子文件里（MUL-6921）。
const (
	coAuthoredByStateFile     = ".multica_co_authored_by"
	coAuthoredByStateEnabled  = "1"
	coAuthoredByStateDisabled = "0"
)

// CoAuthoredByStatePath 返回 prepare-commit-msg 钩子在提交时查询的那个文件路径，
// 按 workspace 区分。workspace 未知时返回空——此时装的是不带门控的钩子，即
// MUL-6921 之前的行为。
func (c *Cache) CoAuthoredByStatePath(workspaceID string) string {
	if workspaceID == "" {
		return ""
	}
	return filepath.Join(c.root, workspaceID, coAuthoredByStateFile)
}

// WriteCoAuthoredByState 把 workspace 当前开关发布给守护进程为它装过的每一个
// prepare-commit-msg 钩子，包括这次调用并不知晓的那些 checkout 里的钩子。守护
// 进程每次刷新 workspace 设置都会调它——这就是开关无需等仓库再次 checkout 就能
// 生效的原因。
//
// 写入是原子的（临时文件 + rename），并发读的钩子只会看到旧值或新值，绝不会
// 看到写到一半的文件。
func (c *Cache) WriteCoAuthoredByState(workspaceID string, enabled bool) error {
	path := c.CoAuthoredByStatePath(workspaceID)
	if path == "" {
		return nil
	}
	value := coAuthoredByStateDisabled
	if enabled {
		value = coAuthoredByStateEnabled
	}
	if current, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(current)) == value {
		// 已经发布过了。守护进程每次 workspace sync 都会重新发布，好让这个文件
		// 扛过缓存 GC 和守护进程重启；跳过重写能把这一步保持得很廉价。
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create workspace cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, coAuthoredByStateFile+".*")
	if err != nil {
		return fmt.Errorf("create co-authored-by state temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(value + "\n"); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write co-authored-by state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("write co-authored-by state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("publish co-authored-by state: %w", err)
	}
	return nil
}

// applyCoAuthoredBySettingContext 把本 checkout 的钩子文件和 workspace 开关对齐。
//
// 它刻意不发布 state 文件。params.CoAuthoredByEnabled 是 handler 在 fetch 和等锁
// 之前拍的快照，一笔 checkout 可能在这之后很久才跑完，而它抓到的值早已过时；把
// 这份快照写进 workspace 级的 state 文件，等于让一笔慢 checkout 把用户已经关掉
// 的尾注"复活"回来。只有守护进程能发布——它握着设置更新的先后顺序——这里只读
// 守护进程发布的值，仅当还没人发布过（没有守护进程驱动的缓存，或 state 文件被
// 人在脚下删了）才退回用那份快照。
func (c *Cache) applyCoAuthoredBySettingContext(ctx context.Context, worktreePath string, params WorktreeParams) {
	enabled := params.CoAuthoredByEnabled
	if published, ok := c.readCoAuthoredByState(params.WorkspaceID); ok {
		enabled = published
	}
	if enabled {
		if err := installCoAuthoredByHookContext(ctx, worktreePath, c.CoAuthoredByStatePath(params.WorkspaceID)); err != nil {
			c.logger.Warn("repo checkout: install co-authored-by hook failed (non-fatal)", "error", err)
		}
		return
	}
	if err := removeCoAuthoredByHookContext(ctx, worktreePath); err != nil {
		c.logger.Warn("repo checkout: remove co-authored-by hook failed (non-fatal)", "error", err)
	}
}

// readCoAuthoredByState 报告守护进程最近为某 workspace 发布的开关。什么都没发布
// 过时 ok 为 false——"未知"意味着什么由调用方自己定。
func (c *Cache) readCoAuthoredByState(workspaceID string) (enabled, ok bool) {
	path := c.CoAuthoredByStatePath(workspaceID)
	if path == "" {
		return false, false
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	switch strings.TrimSpace(string(contents)) {
	case coAuthoredByStateEnabled:
		return true, true
	case coAuthoredByStateDisabled:
		return false, true
	default:
		return false, false
	}
}

// ReconcileCoAuthoredByHooks 让某 workspace 各裸缓存里的钩子与 enabled 对齐，
// 不必等这些仓库再次被 checkout。
//
// 这是给更早版本守护进程装的钩子准备的迁移路径：它们早于 state 文件，提交时
// 什么都不读，所以光发布新值够不着它们。enabled 会把它们改写成当前带门控的
// 脚本（之后切到 off 就能在提交时生效）；disabled 则直接删掉。不是守护进程装的
// 钩子一律不碰。
//
// 这里只能枚举裸缓存——这个类型只知道缓存根目录。isolated checkout 的钩子放在
// 任务 workdir 里；守护进程找到它们后，通过 ReconcileCoAuthoredByHookInCheckout
// 逐个对齐。
func (c *Cache) ReconcileCoAuthoredByHooks(workspaceID string, enabled bool) error {
	if workspaceID == "" {
		return nil
	}
	wsDir := filepath.Join(c.root, workspaceID)
	entries, err := os.ReadDir(wsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read workspace cache dir: %w", err)
	}

	var firstErr error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		barePath := filepath.Join(wsDir, entry.Name())
		if !isBareRepo(barePath) {
			continue
		}
		if err := c.reconcileHookAt(filepath.Join(barePath, "hooks"), workspaceID, enabled); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ReconcileCoAuthoredByHookInCheckout 把 workspace 开关应用到单个"自持 git 元
// 数据"的 checkout——即 isolated checkout，它的 .git 在任务 workdir 里而非共享
// 裸缓存里，因此 ReconcileCoAuthoredByHooks 看不见它。守护进程知道这些 workdir
// 在哪，每找到一个就调一次本函数。
//
// linked worktree 的 .git 是个指向裸缓存的 FILE，这里会跳过它——它的钩子改走
// 裸缓存那条路径对齐。
func (c *Cache) ReconcileCoAuthoredByHookInCheckout(checkoutPath, workspaceID string, enabled bool) error {
	if checkoutPath == "" || workspaceID == "" {
		return nil
	}
	gitDir := filepath.Join(checkoutPath, ".git")
	info, err := os.Stat(gitDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	return c.reconcileHookAt(filepath.Join(gitDir, "hooks"), workspaceID, enabled)
}

// reconcileHookAt 把 workspace 开关应用到单个 hooks 目录。不是守护进程装的钩子
// 保持原样，找不到的 hooks 目录也绝不新建。
func (c *Cache) reconcileHookAt(hooksDir, workspaceID string, enabled bool) error {
	hookPath := filepath.Join(hooksDir, "prepare-commit-msg")
	current, err := os.ReadFile(hookPath)
	switch {
	case err == nil && !isDaemonInstalledHook(current):
		return nil // 用户或第三方钩子：不归我们管
	case err != nil && !os.IsNotExist(err):
		return fmt.Errorf("read prepare-commit-msg hook: %w", err)
	}
	exists := err == nil

	if !enabled {
		if !exists {
			return nil
		}
		if err := os.Remove(hookPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove prepare-commit-msg hook: %w", err)
		}
		return nil
	}

	want := prepareCommitMsgHook(c.CoAuthoredByStatePath(workspaceID))
	if exists && string(current) == want {
		return nil
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}
	if err := writeHookFile(hookPath, want); err != nil {
		return fmt.Errorf("write prepare-commit-msg hook: %w", err)
	}
	return nil
}

// prepareCommitMsgHook 生成 prepare-commit-msg 钩子脚本，往每次提交信息里追加
// 一行 Multica Agent 的 Co-authored-by 尾注。
//
// 脚本每次提交都重新读 statePath，而不是信任自己"已在盘上"这件事：钩子装在 git
// common 目录里，活得比创建它的 checkout 还久，所以 checkout 之后才把开关关掉的
// workspace，也必须让尾注在紧接着的下一笔提交就停——包括守护进程永远看不到的
// 提交；也包括 `git commit --no-verify`：它虽然绕过 pre-commit 和 commit-msg，
// 却绕不过 prepare-commit-msg。
//
// state 文件缺失或不可读时依然保留尾注：钩子之所以存在，是因为某次 checkout 是
// 在开关打开的状态下跑的，所以"没有记录到开关"必须和"state 文件出现之前"意思
// 一致。
func prepareCommitMsgHook(statePath string) string {
	var gate string
	if statePath != "" {
		gate = fmt.Sprintf(`# Current workspace setting, refreshed by the daemon. "0" means the
# Co-authored-by toggle is off — leave the message alone.
STATE_FILE=%s
if [ -f "$STATE_FILE" ]; then
  case "$(cat "$STATE_FILE" 2>/dev/null)" in
    0) exit 0 ;;
  esac
fi

`, shellSingleQuoted(filepath.ToSlash(statePath)))
	}
	return `#!/bin/sh
# multica:prepare-commit-msg:co-authored-by
# Multica: add Co-authored-by trailer for the Multica Agent.
# Installed by the Multica daemon. Do not edit — it will be overwritten.

COMMIT_MSG_FILE="$1"
COMMIT_SOURCE="$2"

# Skip merge and squash commits.
case "$COMMIT_SOURCE" in
  merge|squash) exit 0 ;;
esac

` + gate + `TRAILER="Co-authored-by: multica-agent <github@multica.ai>"

# Don't add if already present.
if grep -qF "$TRAILER" "$COMMIT_MSG_FILE"; then
  exit 0
fi

# Use git interpret-trailers for proper formatting.
git interpret-trailers --in-place --trailer "$TRAILER" "$COMMIT_MSG_FILE"
`
}

// shellSingleQuoted 把 s 渲染成 POSIX shell 单引号字面量，让带空格或 shell 元
// 字符的路径在嵌进钩子后依然安全。
func shellSingleQuoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// installCoAuthoredByHook 装一个会给提交追加 Multica Agent Co-authored-by 尾注
// 的 prepare-commit-msg 钩子。钩子装在 git common 目录（对 worktree 就是裸仓库），
// 所以对从这个缓存建出来的所有 worktree 都生效。
//
// statePath 是钩子每次提交都要重读、以决定是否还要尾注的那个文件；传 "" 表示装
// 不带门控的钩子。
func installCoAuthoredByHook(worktreePath, statePath string) error {
	return installCoAuthoredByHookContext(context.Background(), worktreePath, statePath)
}

func installCoAuthoredByHookContext(ctx context.Context, worktreePath, statePath string) error {
	out, err := runGitOutputContext(ctx, "-C", worktreePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("resolve git common dir: %w", err)
	}
	commonDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktreePath, commonDir)
	}

	hooksDir := filepath.Join(commonDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}

	hookPath := filepath.Join(hooksDir, "prepare-commit-msg")
	if err := writeHookFile(hookPath, prepareCommitMsgHook(statePath)); err != nil {
		return fmt.Errorf("write prepare-commit-msg hook: %w", err)
	}
	return nil
}

// writeHookFile 用 rename 发布钩子脚本。直接截断再写的话，恰好在那时发生的提交
// 会看到一个写了一半的脚本；而这条路径会在"仓库上正挂着 checkout 在跑"的情况下
// 重写钩子。
func writeHookFile(hookPath, contents string) error {
	dir := filepath.Dir(hookPath)
	tmp, err := os.CreateTemp(dir, ".multica-hook-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(contents); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, hookPath); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// isDaemonInstalledHook 判断盘上的 prepare-commit-msg 钩子是不是 Multica 守护
// 进程装的（当前或任何历史发布版本都算）。钩子不带任何已知守护进程签名时返回
// false——这样同路径下用户自装的钩子不会被碰。
func isDaemonInstalledHook(contents []byte) bool {
	body := string(contents)
	for _, sig := range daemonInstalledHookSignatures {
		if strings.Contains(body, sig) {
			return true
		}
	}
	return false
}

// removeCoAuthoredByHook 移除 installCoAuthoredByHook 装的 prepare-commit-msg
// 钩子。只有内容命中已知守护进程签名（当前标记或任何历史发布过的钩子内容）时
// 才删文件，用户自装的 prepare-commit-msg 钩子绝不碰。没有钩子、或路径被无关
// 钩子占用时返回 nil。
func removeCoAuthoredByHook(worktreePath string) error {
	return removeCoAuthoredByHookContext(context.Background(), worktreePath)
}

func removeCoAuthoredByHookContext(ctx context.Context, worktreePath string) error {
	out, err := runGitOutputContext(ctx, "-C", worktreePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("resolve git common dir: %w", err)
	}
	commonDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktreePath, commonDir)
	}

	hookPath := filepath.Join(commonDir, "hooks", "prepare-commit-msg")
	contents, err := os.ReadFile(hookPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read prepare-commit-msg hook: %w", err)
	}
	if !isDaemonInstalledHook(contents) {
		// 无关钩子（用户或第三方装的）：别碰。
		return nil
	}
	if err := os.Remove(hookPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove prepare-commit-msg hook: %w", err)
	}
	return nil
}

// excludeFromGit 往 worktree 的 .git/info/exclude 文件里加一条忽略模式。
func excludeFromGit(worktreePath, pattern string) error {
	return excludeFromGitContext(context.Background(), worktreePath, pattern)
}

func excludeFromGitContext(ctx context.Context, worktreePath, pattern string) error {
	out, err := runGitOutputContext(ctx, "-C", worktreePath, "rev-parse", "--git-dir")
	if err != nil {
		return fmt.Errorf("resolve git dir: %w", err)
	}

	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreePath, gitDir)
	}

	excludePath := filepath.Join(gitDir, "info", "exclude")

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("create info dir: %w", err)
	}

	existing, _ := os.ReadFile(excludePath)
	if strings.Contains(string(existing), pattern) {
		return nil
	}

	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open exclude file: %w", err)
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "\n%s\n", pattern); err != nil {
		return fmt.Errorf("write exclude pattern: %w", err)
	}
	return nil
}

// repoNameFromURL 从 git 远端 URL 里抽出简短目录名。如
// "https://github.com/org/my-repo.git" → "my-repo"
func repoNameFromURL(url string) string {
	url = strings.TrimRight(url, "/")
	url = strings.TrimSuffix(url, ".git")

	if i := strings.LastIndex(url, "/"); i >= 0 {
		url = url[i+1:]
	}
	if i := strings.LastIndex(url, ":"); i >= 0 {
		url = url[i+1:]
		if j := strings.LastIndex(url, "/"); j >= 0 {
			url = url[j+1:]
		}
	}

	name := strings.TrimSpace(url)
	if name == "" {
		return "repo"
	}
	return name
}

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeName 从一段人类可读的名字生成对 git 分支安全的名称。
func sanitizeName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 30 {
		s = s[:30]
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		s = "agent"
	}
	return s
}

// taskKeyLen 与 execenv.taskKeyLen 保持一致——那段为什么短见那边的常量说明：
// 分支名在任务 checkout 里会变成 .git/refs/heads/ 下的路径，而 Windows 对那里
// 有 MAX_PATH 限制。
const taskKeyLen = 12

// taskKey 返回标识一个任务的、对 git 安全的短段：取 id 末尾 taskKeyLen 个十六
// 进制字符。与 execenv.taskKey 保持一致——UUIDv7 开头的 8 位十六进制是时间戳
// 位，大约每 65.5 秒才推进一次，取开头会让两个几乎同时建的任务撞同名分支
// （#7326）；尾部是随机的。
func taskKey(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	if len(s) > taskKeyLen {
		return s[len(s)-taskKeyLen:]
	}
	return s
}

// shortID 返回 UUID 字符串（去掉连字符）的前 8 个字符。只用于展示和日志——需要
// 唯一性的地方请用 taskKey。
func shortID(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
