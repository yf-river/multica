// 同一仓库上的 git 管理操作串行化 —— 这是一个"跨进程"问题。
//
// daemon 从不把任务准备跑在自己的进程里：PrepareIsolated 把准备工作放到
// 一个短命的 helper 进程执行（见 isolation.go），因此同一 local_directory
// 上并发的 N 个任务就是 N 个独立进程，全都在对"同一个仓库"发 git 命令。
// worktree 模式刻意跳过了按路径加的任务互斥锁——让兄弟任务并发本就是该
// 模式的意义——所以本层之上没有任何东西替它们串行化。
//
// 因此进程内 sync.Mutex 在生产环境中保护不了任何东西：每个 helper 持有的是
// 自己那份锁，彼此之间永远不会争抢。`git stash create`（需要仓库的索引锁）
// 过去频繁在这场竞争里落败，而 git 对这种失败只给一个裸的退出码 1、stderr
// 完全为空，于是任务失败后毫无线索可查。
//
// 互斥锁仍保留给进程内调用方——Finalize 和 Discard 跑在 daemon 父进程里；
// 而仓库"公共 git 目录"里的一把建议性文件锁，则把同样的互斥扩展到所有
// 触碰该仓库的进程，包括两个资源指向它的两个链接 worktree 的情况。

package execenv

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// gitRootLockFileName 放在仓库的"公共 git 目录"（common git dir）里——
	// 正是这一点让一把锁能覆盖一整个仓库。
	//
	// 它守护的临界区横跨仓库的两种作用域：`git stash create` 会争抢
	// 单工作树自己的 index.lock，而 `worktree add` / `prune` / `remove` 与
	// `branch -D` 会写公共目录里的 refs 与 worktrees/，这些是每个链接
	// worktree 共享的状态。若按"每个 worktree 各自的 git 目录"加锁，那么
	// 指向同一仓库两个链接 worktree 的两个资源会拿到两把不同的锁，却仍会在
	// 那份共享状态上竞争；公共目录是更粗粒度、能同时覆盖两者的键。
	//
	// git 会忽略 $GIT_DIR 下它不认识的文件，而这里的东西也不会出现在工作树
	// 里——`git status` 永远看不见它，`git ls-files --others` 也列不出它。
	gitRootLockFileName = "multica-worktree.lock"

	// gitRootLockPoll 是等待时的重试间隔。flock 没有可移植的"带超时等待"
	// 变体，所以这里的等待是对"非阻塞加锁原语"（与 env-root 占用的实现共用，
	// 见 envlock_unix.go / envlock_windows.go）做轮询。
	gitRootLockPoll = 25 * time.Millisecond
)

// errGitRootLockBusy 表示另一进程持有仓库锁的时长超过了 gitRootLockWait。
// 它与"准备工作失败"不同：busy 意味着互斥机制生效了、而我们选择不再等下去，
// 因此调用方必须失败返回，而不是在无保护的情况下继续。
var errGitRootLockBusy = errors.New("execenv: git repository lock is held by another task")

// gitRootLockWait 限定了等待"兄弟任务"的 git 临界区的时长。那段临界区不过是
// 几条本地 git 命令加上有界的未跟踪文件重放，所以等这么久仍拿不到锁，说明
// 持有者已经卡死而非繁忙：用一条"该等谁"的报错信息让任务失败，好过永远占着
// 一个 daemon 槽位。
//
// 声明为 var 只是为了测试时能改小；运行期没有任何地方写它。
var gitRootLockWait = 3 * time.Minute

// gitRootLocks 在单进程内按仓库串行化 git 管理操作。同一仓库上的并发
// `git worktree add` / `remove` / `prune` 会竞争同一批锁文件（worktrees/、
// packed-refs.lock、config.lock）；与 fetch 不同，这些操作很快，一把普通
// 互斥锁几乎零成本。键为仓库根路径，因此不同仓库上的任务互不等待。
var gitRootLocks sync.Map // gitRoot -> *sync.Mutex

// gitDirCache 按仓库根路径记忆其 git 目录。解析它要花一次 git 调用，而且
// 结果在进程生命周期内不会变：仓库一旦存在，其 git 目录就固定了。
var gitDirCache sync.Map // gitRoot -> string

// gitDirFor 返回支撑 gitRoot 工作树的那个 git 目录的绝对路径。
//
// 不能写成 filepath.Join(gitRoot, ".git")：在链接 worktree 里 .git 是一个
// 指向 $COMMON/.git/worktrees/<name> 的"文件"，而这个目录（不是公共目录）
// 才装着该工作树的 index 和 index.lock。让 git 自己回答，能保证锁文件与
// 真正的竞争发生在同一个位置，无论哪种布局都成立。
func gitDirFor(gitRoot string) (string, error) {
	if v, ok := gitDirCache.Load(gitRoot); ok {
		return v.(string), nil
	}
	dir, err := runGitTrimmed(gitRoot, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", fmt.Errorf("execenv: resolve git dir for %q: %w", gitRoot, err)
	}
	if dir == "" {
		return "", fmt.Errorf("execenv: resolve git dir for %q: empty result", gitRoot)
	}
	// git 在所有平台（含 Windows）上报的都是正斜杠路径。在它变成文件系统
	// 路径前先规范化，保证锁文件及其派生出的路径都是平台原生的。
	dir = filepath.Clean(filepath.FromSlash(dir))
	gitDirCache.Store(gitRoot, dir)
	return dir, nil
}

// gitCommonDirCache 按仓库根路径记忆其公共 git 目录，理由与 gitDirCache 相同：
// 一次 git 调用，得到一个不会变化的结果。
var gitCommonDirCache sync.Map // gitRoot -> string

// gitCommonDirFor 返回 gitRoot 背后那个仓库被"每个工作树共享"的 git 目录。
// 对主工作树而言就是它自己的 git 目录；对链接工作树则是主仓库的那个——
// 两者都要写的 refs 和 worktrees/ 管理状态才真正存在那里。
func gitCommonDirFor(gitRoot string) (string, error) {
	if v, ok := gitCommonDirCache.Load(gitRoot); ok {
		return v.(string), nil
	}
	dir, err := runGitTrimmed(gitRoot, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("execenv: resolve common git dir for %q: %w", gitRoot, err)
	}
	if dir == "" {
		return "", fmt.Errorf("execenv: resolve common git dir for %q: empty result", gitRoot)
	}
	dir = filepath.FromSlash(dir)
	// 当工作树就是主工作树时，git 会返回相对于运行 cwd 的相对路径（裸的
	// ".git"）；从链接 worktree 回答时则是绝对路径。--path-format=absolute
	// 本可一劳永逸，但它要 git 2.31 才有；而且在这里手动拼一下几乎零成本。
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(gitRoot, dir)
	}
	dir = filepath.Clean(dir)
	gitCommonDirCache.Store(gitRoot, dir)
	return dir, nil
}

// gitRootLockPath 返回 gitRoot 仓库的建议锁文件所在路径。
func gitRootLockPath(gitRoot string) (string, error) {
	commonDir, err := gitCommonDirFor(gitRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(commonDir, gitRootLockFileName), nil
}

// acquireGitRootFileLock 取得 gitRoot 的跨进程锁，至多等 gitRootLockWait
// 让当前持有者用完释放。
func acquireGitRootFileLock(gitRoot string, logger *slog.Logger) (*os.File, error) {
	path, err := gitRootLockPath(gitRoot)
	if err != nil {
		return nil, err
	}
	f, err := openLockFile(path)
	if err != nil {
		return nil, fmt.Errorf("execenv: open git repository lock %s: %w", path, err)
	}

	deadline := time.Now().Add(gitRootLockWait)
	announced := false
	for {
		ok, lockErr := lockFileExclusiveNonBlocking(f)
		if lockErr != nil {
			f.Close()
			return nil, fmt.Errorf("execenv: lock %s: %w", path, lockErr)
		}
		if ok {
			return f, nil
		}
		if !announced {
			announced = true
			if logger != nil {
				logger.Info("execenv: waiting for another task's git section on this repository",
					"git_root", gitRoot, "lock", path)
			}
		}
		if time.Now().After(deadline) {
			f.Close()
			// 故意不写"没人在跑就删掉锁文件"。这把锁绑定在"打开的
			// 文件描述"上，而不是路径上：若删掉它，持有者仍锁着旧 inode，
			// 而下个任务会在同名路径上创建并锁定一个"新"文件，于是两者都
			// 以为自己持锁——这正是本锁要杜绝的重叠。内核会在持有进程退出时
			// 自动释放它，所以闲置的锁文件永远不会是"陈旧"的。
			return nil, fmt.Errorf("%w: %q was still locked after %s by another Multica task (%s) — "+
				"wait for that task to finish or stop it, then retry; the lock is released "+
				"automatically when the holding process exits, so the file itself is never stale "+
				"and deleting it would let two tasks into this section at once",
				errGitRootLockBusy, gitRoot, gitRootLockWait, path)
		}
		time.Sleep(gitRootLockPoll)
	}
}

// lockGitRoot 为当前任务取得 gitRoot 的 git 管理状态在"跨进程"范围内的独占
// 所有权，并返回释放函数。
//
// "准备失败"并不致命。一个 git 目录放不下锁文件的仓库（只读挂载、某种怪异的
// 文件系统）至少还能拿到进程内互斥锁——这正是这段代码以前就有的保护：一个
// 拒绝启动的任务，比一个在毫秒级窗口里竞争、且还能靠重试绕开它的任务更糟。
// 而"锁正忙"则不同：互斥生效了、我们也等了，再继续硬闯只会让互斥形同虚设，
// 所以这种情况要原样抛回给调用方。
func lockGitRoot(gitRoot string, logger *slog.Logger) (func(), error) {
	// 文件锁必须"先于"互斥锁取得。若反过来——拿着互斥锁去等文件锁——
	// 等待本身会被串行化：三个排队中的 goroutine 会各自等前面那个放弃之后
	// 才开始自己那轮 gitRootLockWait，于是最后那个可能被阻塞到承诺上限的
	// 三倍时长。直接去等文件锁，则它们能同时等待，各自都受 gitRootLockWait
	// 约束。
	//
	// 两个平台的原语本来就在进程内互斥——flock 把各自打开的独立文件描述
	// 分开对待，LockFileEx 对独立句柄也一样——所以这个先后顺序不会丢失任何
	// 保护。
	f, err := acquireGitRootFileLock(gitRoot, logger)
	if err != nil {
		if errors.Is(err, errGitRootLockBusy) {
			return nil, err
		}
		if logger != nil {
			logger.Warn("execenv: cross-process git lock unavailable; falling back to the in-process lock",
				"git_root", gitRoot, "error", err)
		}
		return lockGitRootInProcess(gitRoot), nil
	}

	// 文件锁在手时这把互斥锁是冗余的，但它是文件锁中途退化时保护本仓库的
	// 兜底。正常路径下不会争抢——既然能越过文件锁，就不可能有别的兄弟
	// goroutine 也越过它。
	unlockLocal := lockGitRootInProcess(gitRoot)
	return func() {
		unlockLocal()
		if unlockErr := unlockFile(f); unlockErr != nil && logger != nil {
			logger.Warn("execenv: release git repository lock failed", "git_root", gitRoot, "error", unlockErr)
		}
		f.Close()
	}, nil
}

// lockGitRootInProcess 取得按仓库划分的进程内互斥锁并返回释放函数。
func lockGitRootInProcess(gitRoot string) func() {
	v, _ := gitRootLocks.LoadOrStore(gitRoot, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
