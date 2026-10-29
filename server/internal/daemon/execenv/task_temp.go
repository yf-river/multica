// 每任务临时目录（agent 进程的 TMPDIR）的生命周期管理：创建时加一把与
// env-root 相同的 .task_lock 执行锁，GC 则在持有者死后回收这些目录。活跃性
// 由"锁"而非"时钟"判定——只有本进程真正抢到锁，才证明原属主已消亡。

package execenv

import (
	"errors"
	"fmt"
	"github.com/multica-ai/multica/server/internal/openviking"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TaskTempDirPrefix 是 os.MkdirTemp 给 daemon 在任务临时基目录下创建的每个
// 临时目录打上的 basename 前缀。下面的清扫逻辑只回收带此前缀的目录，因此
// 基目录本身——通常是一个共享 /tmp、还装着其他程序的文件的目录——绝不会
// 有被误删的风险。
const TaskTempDirPrefix = "multica-task-"

// taskTempLockClaimFile 是锁标记在被改名到位之前、先以这个名字加锁占用的
// 文件名。这样 .task_lock 在其属主还在启动的过程中，绝不会以"未加锁"的
// 状态出现在磁盘上。
const taskTempLockClaimFile = envRootLockFile + ".claiming"

// LockTaskTempDir 取得临时目录的执行锁并返回被持有的文件；调用方在任务
// 运行的整个生命周期内拥有它。
//
// 这与 env root 用的是同一把 .task_lock，理由也相同：内核会在持有进程死亡时
// 释放它，于是它能在进程之间回答"拥有这个目录的那次执行还活着吗？"——无需
// 心跳、PID 表或清理陈旧状态的路径。正因为这样，PruneTaskTempDirs 才能在某
// 目录的属主消失的瞬间回收它，同时绝不去碰仍在使用中的目录——包括共享同一
// 临时基目录的另一个 daemon 所拥有的目录（任何进程内"活跃集合"都看不见它），
// 也包括本进程里的存活任务（因为锁挂在"打开的文件描述"上，而非进程上）。
func LockTaskTempDir(dir string) (*os.File, error) {
	// 先以清扫逻辑"不读"的名字把锁加上，再用 rename 发布它。若先创建
	// .task_lock、第二步再加锁，会留下一个（无论多短的）窗口：并发的清扫
	// 看到一把它也能抢到的锁，便判定属主已死，把一个即将被任务使用的目录从
	// 任务脚下删掉。单目录内的 rename 是原子的，锁又挂在打开的文件描述而非
	// 名字上，所以"标记可见"与"标记已被持有"是同一瞬间完成的。
	//
	// 在改名之前目录上没有已发布的标记，清扫只能走 legacy 分支够到它。那个
	// 分支里保护它的不是年龄——legacyTTL 接受任意时长、小到一纳秒，而卡住的
	// 属主能比任何 TTL 都活得久——而是它此刻还没有任何任务内容。legacy 分支
	// 只回收带任务内容的目录，见 taskTempDirHoldsContent。
	claim := filepath.Join(dir, taskTempLockClaimFile)
	lock, err := openLockFile(claim)
	if err != nil {
		return nil, fmt.Errorf("open task temp dir lock for %s: %w", dir, err)
	}
	locked, err := lockFileExclusiveNonBlocking(lock)
	if err != nil {
		lock.Close()
		_ = os.Remove(claim)
		return nil, fmt.Errorf("lock task temp dir %s: %w", dir, err)
	}
	if !locked {
		// 实际不可能到达：目录刚由 os.MkdirTemp 以"别人还不知道的名字"创建。
		lock.Close()
		_ = os.Remove(claim)
		return nil, fmt.Errorf("task temp dir %s is already locked", dir)
	}
	if err := os.Rename(claim, filepath.Join(dir, envRootLockFile)); err != nil {
		releaseLockFile(lock)
		_ = os.Remove(claim)
		return nil, fmt.Errorf("publish task temp dir lock for %s: %w", dir, err)
	}
	return lock, nil
}

// ReleaseTaskTempLock 释放临时目录的执行锁并关闭文件。对 nil 安全，也允许
// 重复调用。
func ReleaseTaskTempLock(f *os.File) {
	releaseLockFile(f)
}

// RemoveTaskTempDir 先删除 dir 的内容，再删锁标记，最后删 dir 本身——并在
// 遇到第一个删不掉的内容处停下，把锁标记留在原地。
//
// 这个顺序正是全部关键，也是它不能是 os.RemoveAll 的原因。RemoveAll 会遍历
// 目录并在某次失败后继续删，因此一个 Windows 不放手的负载仍会连累旁边的
// .task_lock：删除失败时恰恰已经删掉了"下轮清扫用来识别这是把锁目录"的那
// 个文件。它会变成一个没有标记的目录——与加锁前的遗留物无法区分，从而只能
// 按年龄（如果还有这个机制的话）回收。
//
// 留下标记意味着一次失败的清理毫无代价：目录仍带着锁、仍未加锁，下一轮 GC
// 抢到锁再试一次，无论要多少轮直到持有者放手。
//
// 这对每一种失败方式都成立，不只是内容遍历。标记必须先消失、目录本身才能被
// 删，所以当最后那步删除失败时要把标记放回去——否则"清理走得最远"的那一种
// 情况反而成为永久泄漏的那一种。
//
// 调用方必须先释放自己的锁再调用本函数——标记在这里被删除，而 Windows 上
// 删除一个本进程仍打开的文件只会"标记待删"，那会反过来阻塞目录本身的删除。
func RemoveTaskTempDir(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, openviking.TempArchiveMarker)); err == nil {
		return fmt.Errorf("task temp originals await archive")
	} else if !os.IsNotExist(err) {
		return err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.Name() == envRootLockFile {
			continue // 锁标记留到最后，且只在其余内容都删光之后
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	lockPath := filepath.Join(dir, envRootLockFile)
	if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(dir); err != nil {
		// 目录活得比它的标记还久：要么是 Windows 进程仍持有指向目录本身的
		// 句柄，要么是上面的遍历走过之后、某个孤儿子进程又重建了内容。把
		// 一把"未加锁"的标记放回去，否则从此以后该目录会被读作"加锁前的
		// 遗留物"——而 legacy 分支默认关闭，那意味着它再也得不到回收。
		// 恢复标记让下一轮仍可重试。
		if restored, rerr := openLockFile(lockPath); rerr == nil {
			restored.Close()
		}
		return err
	}
	return nil
}

// taskTempVerdict 是本次清扫对临时基目录下某个目录能"证明到什么程度"的
// 判定结果。
type taskTempVerdict int

const (
	// taskTempInUse：某次执行仍持锁，或有什么东西阻止了本次清扫查明情况。
	// 两者都意味着"别碰"——清扫在这里失败关闭，因为判错就会删掉一个正在
	// 运行的任务的 TMPDIR。
	taskTempInUse taskTempVerdict = iota
	// taskTempDead：本次清扫抢到了"本应仍被属主持有"的锁，因此内核已经
	// 证明那次执行确实消亡了。
	taskTempDead
	// taskTempLegacy：完全没有锁文件——由早于 LockTaskTempDir 的 daemon
	// 遗留，对它无法证明任何事。
	taskTempLegacy
)

// PruneTaskTempDirs 回收"属主执行已消亡"的每任务临时目录，扫描根是 daemon
// 解析出的任务临时基目录。
//
// 这些目录是 agent 进程的 TMPDIR。它们在 WorkspacesRoot（任务 GC 的扫描根）
// 之外，因此没有任何别的机制会回收它们：它们唯一的另一条出路是 daemon 在
// runTask 结束时推迟执行的清理——而 daemon 被杀时那条清理不会跑、里面还有
// 文件被打开时它也跑不完（Windows 情形见 #7364）。凡是被漏掉的都会在磁盘上
// 永久累积。
//
// 活跃性来自锁而不是时钟。只有本次清扫自己抢到那把"属主应仍持有"的
// .task_lock，目录才会被删除——而内核只会在属主消亡后授予这把锁。
//
// 锁由 daemon 持有、而非 agent 进程持有，因此它回答的是"属主的那次执行还
// 活着吗"，而不是"它派生的每个子进程都走了吗"。daemon 被杀而 agent 进程
// 幸存时，锁当场释放，本次清扫就可能回收一个这样的孤儿仍在写入的目录。
// 这与 env root 的 .task_lock 已承担的暴露相同，而且站在平台角度看任务反正
// 已死；把它收干净是进程组拆除（teardown）的职责，不是 GC 的。
//
// legacyTTL 只适用于锁回答不了的那一种情况：早于 LockTaskTempDir 的 daemon
// 遗留的目录——它们根本没有锁文件。对它们，年龄是唯一可用信号——而年龄无法
// 确立活跃性，这正是 legacyTTL 默认为 0（不要动它们）、回收与否由运维显式
// 决定的原因。见 DefaultGCTaskTempLegacyTTL。
func PruneTaskTempDirs(base string, legacyTTL time.Duration, now time.Time, logger *slog.Logger) (removed int, bytesFreed int64) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0, 0 // 基目录缺失或不可读——没什么可清理的
	}
	legacyKept := 0
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), TaskTempDirPrefix) {
			continue
		}
		dir := filepath.Join(base, e.Name())
		if _, err := os.Stat(filepath.Join(dir, openviking.TempArchiveMarker)); err == nil {
			continue
		}

		switch classifyTaskTempDir(dir, logger) {
		case taskTempInUse:
			continue
		case taskTempLegacy:
			if legacyTTL <= 0 {
				legacyKept++
				continue
			}
			// 这里年龄是唯一信号，而年龄无法区分"加锁前的遗留物"与"本 daemon
			// 正在发布中的目录"：两者都没有 .task_lock，legacyTTL 接受小到一
			// 纳秒的任意时长，而一个卡在 MkdirTemp 与 rename 之间的属主能比任何
			// TTL 都活得久。"内容"能区分两者。发布中的目录只装自己的 claim
			// 文件——任务还没拿到这个路径，自然不可能写过任何东西——而值得
			// 回收的遗留物按定义就是"里面有点东西"的目录。
			//
			// 所以这个分支绝不碰没有任务内容的目录。代价是：一个在任务写任何
			// 内容前就崩溃的"加锁前 daemon"会留下一个无人回收的空目录；若不做
			// 这个限制，另一种选择就是删掉一个正在启动的任务的临时目录。
			if !taskTempDirHoldsContent(dir) {
				continue
			}
			newest, _ := dirStat(dir)
			if newest.IsZero() || now.Sub(newest) <= legacyTTL {
				continue
			}
			// 上面每一步都是独立的观测，而非同一次快照：判定"legacy"发生在
			// 内容遍历之前，其间属主可能已完成发布——留下一个本分支没看过的、
			// 已持有的 .task_lock，而目录里的任务现在正在写它。
			//
			// 在这里重读一次分类即可弥合这个窗口，而且是彻底弥合而非收窄。
			// 内容只会在"任务拿到此路径"之后出现，而那必然发生在
			// LockTaskTempDir 返回之后、即标记已发布并被持有之后。所以"顶部
			// 看到内容 + 此处没看到标记"意味着没有活任务拥有此目录——此后任务
			// 也不可能凭空出现，因为 MkdirTemp 只会以一个全新名字派发一个全新
			// 的空目录。
			if taskTempSweepBeforeRecheck != nil {
				taskTempSweepBeforeRecheck(dir)
			}
			if classifyTaskTempDir(dir, logger) != taskTempLegacy {
				continue
			}
		}

		_, size := dirStat(dir)
		if err := RemoveTaskTempDir(dir); err != nil {
			// 不值得升级处理：标记还在，下一轮会重新抢锁再试。这正是修复应
			// 放在 GC 里、而不是放在任务完成路径上的有界重试里的全部原因——
			// 在这里失败零成本，重试也免费。
			if logger != nil {
				logger.Debug("execenv: prune task temp dir failed", "dir", dir, "error", err)
			}
			continue
		}
		removed++
		bytesFreed += size
	}
	if legacyKept > 0 && logger != nil {
		// 与其静默回收，不如挑明：这些是本 daemon 无法证明任何事的目录，
		// 知道没有"加锁前 daemon"在跑的运维可以显式选择回收。
		logger.Info("gc: pre-lock task temp dirs left in place",
			"base", base,
			"count", legacyKept,
			"hint", "set MULTICA_GC_TASK_TEMP_LEGACY_TTL to reclaim them on age",
		)
	}
	return removed, bytesFreed
}

// taskTempSweepBeforeRecheck 是测试专用接缝，生产环境为 nil。清扫的正确性
// 取决于"决策与删除之间"可能发生什么，而这种交错无法从外部可靠制造。
var taskTempSweepBeforeRecheck func(dir string)

// taskTempDirHoldsContent 报告 dir 里是否有任务放进去的东西，而不是只有本包
// 维护的锁文件。它用来在不借助时钟的前提下区分"加锁前的遗留物"与"发布中的
// 目录"。
func taskTempDirHoldsContent(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false // 不可读：不是我们有资格删的
	}
	for _, e := range entries {
		if e.Name() == envRootLockFile || e.Name() == taskTempLockClaimFile {
			continue
		}
		return true
	}
	return false
}

// classifyTaskTempDir 判定关于某个目录"能证明什么"。
func classifyTaskTempDir(dir string, logger *slog.Logger) taskTempVerdict {
	lockPath := filepath.Join(dir, envRootLockFile)
	if _, err := os.Stat(lockPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			// 连看都看不进去的目录，不是我们有资格删的目录。共享 /tmp 下
			// 其他用户的临时目录会落在这里。
			if logger != nil {
				logger.Debug("execenv: task temp dir lock unreadable; leaving it alone", "dir", dir, "error", err)
			}
			return taskTempInUse
		}
		return taskTempLegacy
	}

	lock, err := openLockFile(lockPath)
	if err != nil {
		return taskTempInUse
	}
	locked, err := lockFileExclusiveNonBlocking(lock)
	if err != nil || !locked {
		lock.Close()
		return taskTempInUse
	}
	// 立即释放：RemoveTaskTempDir 会删掉这个文件本身，而 Windows 上删除
	// 一个本进程仍打开的文件只会标记待删，进而阻塞目录自身的删除。其间没有
	// 谁能认领该目录——名字随机、属主已死——即便有第二个清扫在和我们抢，
	// 它也只会输在 ENOENT 上。
	releaseLockFile(lock)
	return taskTempDead
}
