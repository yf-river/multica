package daemon

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// 对真实 release / 版本工具的间接层，让测试能确定性地跑自动更新循环，不必真的去
// GitHub 或 shell 调用 brew/curl。镜像 daemon.go 顶部对 `isBrewInstall` /
// `getBrewPrefix` / `matchKnownBrewPrefix` 的做法。
var (
	fetchLatestRelease = cli.FetchLatestRelease
	isReleaseVersion   = cli.IsReleaseVersion
	isNewerVersion     = cli.IsNewerVersion
)

// detectSelfVersion 执行 `<path> --version` 并返回版本字段。做成间接层，测试就不用
// fork 真实进程。
var detectSelfVersion = func(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, path, "--version")
	// 取消 context 只会杀掉直接子进程；继承了 stdout 管道、还赖在 Output() 上的孙进程
	// 会拖过截止时间。WaitDelay 在 kill 之后强制关闭管道——没有它，一个行为异常的被替换
	// 二进制会带着 updating 标志把 autoUpdateLoop 的 goroutine 永久挂起。与
	// detectCLIVersion 相同的护栏。
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("run %s --version: %w", path, err)
	}
	return ParseSelfVersion(string(out)), nil
}

// ParseSelfVersion 从 `multica --version` 输出里取出版本号；该输出的第一行由
// cmd/multica 的版本模板渲染：
//
//	multica 0.3.7 (commit: abc1234, built: 2026-07-29T10:00:00Z)
//	go: go1.26.1, os/arch: darwin/arm64
//
// 抽出的字段正是 ldflags 写进 main.version、也会落到 Config.CLIVersion 的东西——所以
// 两者可直接比较。任何不符合模板形态的内容都会被去空白后原样返回——比较结果不等，
// 于是会上报而不是被悄悄忽略。
//
// 之所以导出，纯粹为了让 cmd/multica 从生产端锁死这个契约：本解析器与版本模板必须
// 一致，而如果模板改动悄悄弄坏了自动重载探测，没有别的东西会注意到。
func ParseSelfVersion(raw string) string {
	line, _, _ := strings.Cut(raw, "\n")
	line = strings.TrimSpace(line)
	if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "multica" {
		return fields[1]
	}
	return line
}

// autoUpdateInitialDelay 是循环在 Run() 返回后、做首次版本检查前等待的时长。daemon
// 启动时有大量事要做（鉴权、注册、同步 workspace、发起心跳），不想再叠加一次对 GitHub
// 的出站 HTTPS 调用。同时这延迟也足够短——有更新可用的全新安装会在几分钟内自更新，
// 而不用等满整个检查间隔。
var autoUpdateInitialDelay = 2 * time.Minute

// selfReloadCheckInterval 是循环把“编译进本进程的版本”与“重启会 exec 的那个二进制的
// --version 输出”做比较的频率。每个 tick 一次 fork/exec，机器级（不是每 workspace、
// 不是每 runtime），因此成本固定，不受 daemon 负载大小影响。测试可覆盖。
//
// 这个值约束的是“用户自己替换二进制之后要等多久”，不是发布频率：“有没有新 release”
// 归 DefaultAutoUpdateCheckInterval 管，只有它该跟随发版节奏。它还会累加——落在 daemon
// 忙碌期间的 tick 会顺延到下一个而不是打断任务——所以真实等待是“第一个既到点又空闲的
// tick”。十分钟让平均等待在五分钟上下，繁忙主机上倍数也可接受；一小时则不行，那会
// 让我们重新回到“我升级了却啥也没发生”这个本检查想消除的问题上。
var selfReloadCheckInterval = 10 * time.Minute

// selfReloadProbeTimeout 约束 `--version` 的 fork/exec。给得很宽裕：超时的意义是防止
// 卡死的二进制挂住循环 goroutine，而不是去给慢磁盘计时。
var selfReloadProbeTimeout = 10 * time.Second

// autoUpdateLoop 拥有所有“以把 daemon 重启进另一个 multica 二进制而告终”的逻辑。它
// 在同一个 goroutine 上跑两个相互独立的检查——这本身就阻止了它们互相竞争冲进
// triggerRestart：
//
//   - tryAutoUpdate: 轮询 GitHub 找更新的 release，daemon 空闲时执行与“服务器触发
//     路径”相同的 brew-or-download 升级。
//   - trySelfReload: 察觉磁盘上的二进制被越权替换，重新 exec 进它。
//
// GitHub 那半边在以下情况额外跳过：运维显式关闭（--no-auto-update /
// MULTICA_DAEMON_AUTO_UPDATE=false）、服务器是自托管（默认关，MUL-2381）、或运行版本
// 不是打 tag 的 release——源码构建（`make daemon`）报的是 `git describe` 风格版本，
// 把它们升级到公开 release 会悄悄丢掉机器上的开发成果。这些都不适用于磁盘半边，它
// 跟随的是运维亲手装的二进制；见 trySelfReload。
//
// 快乐路径下每个 tick 保持安静，让一跑就是数周的 daemon 日志不被刷屏。
func (d *Daemon) autoUpdateLoop(ctx context.Context) {
	pullEnabled := d.cfg.AutoUpdateEnabled
	switch {
	case !pullEnabled:
		d.logger.Info("auto-update: disabled")
	case !isReleaseVersion(d.cfg.CLIVersion):
		d.logger.Info("auto-update: skipped (not a release build)", "version", d.cfg.CLIVersion)
		pullEnabled = false
	}
	reloadEnabled := d.cfg.AutoReloadEnabled
	if !reloadEnabled {
		d.logger.Info("auto-reload: disabled")
	}
	if !pullEnabled && !reloadEnabled {
		return
	}

	pullInterval := d.cfg.AutoUpdateCheckInterval
	if pullInterval <= 0 {
		pullInterval = DefaultAutoUpdateCheckInterval
	}
	// 在启动延迟前就把各半边要做什么记进日志，让 `daemon start` 之后立刻读 daemon.log
	// 的用户马上看到。
	if pullEnabled {
		d.logger.Info("auto-update: started", "interval", pullInterval, "current", d.cfg.CLIVersion)
	}
	if reloadEnabled {
		d.logger.Info("auto-reload: watching the multica binary on disk",
			"interval", selfReloadCheckInterval, "current", d.cfg.CLIVersion)
	}

	if err := sleepWithContext(ctx, autoUpdateInitialDelay); err != nil {
		return
	}
	if pullEnabled {
		d.tryAutoUpdate(ctx)
	}
	if reloadEnabled {
		d.trySelfReload(ctx)
	}

	// Ticker 到此刻才开始，排在首次检查之后。若在延迟前就启动，比 autoUpdateInitialDelay
	// 短的间隔会缓冲一个 tick，在第一次检查后立刻再触发一次。
	//
	// 被禁用的半边 channel 保持 nil，在 select 里永远不会触发。
	var pullTick, reloadTick <-chan time.Time
	if pullEnabled {
		ticker := time.NewTicker(pullInterval)
		defer ticker.Stop()
		pullTick = ticker.C
	}
	if reloadEnabled {
		ticker := time.NewTicker(selfReloadCheckInterval)
		defer ticker.Stop()
		reloadTick = ticker.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-pullTick:
			d.tryAutoUpdate(ctx)
		case <-reloadTick:
			d.trySelfReload(ctx)
		}
	}
}

// tryAutoUpdate 跑一轮“检查并可能升级”。出现以下任一情况提前返回：已在升级中（服务器
// 触发的升级在途）、有活动任务（顺延到下一 tick——绝不打断运行中的 agent）、版本拉取
// 失败、或没有更新的 release。函数从不返回错误：今天失败的检查下个 tick 会重试，我们
// 也不希望一次瞬时网络抖动升级成进程级关停。
func (d *Daemon) tryAutoUpdate(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	// 别和服务器触发的升级路径竞速。Runtimes 页的“手动更新”若已在途，就让它跑完，
	// 下个 tick 再检查（届时要么已在跑新二进制，要么失败了可重试）。
	if d.updating.Load() {
		d.logger.Debug("auto-update: skip — update already in progress")
		return
	}
	// 便宜的预取空闲检查：下面的 release 元数据拉取要打一次到 GitHub 的 HTTPS，既然
	// 已经知道会顺延，就没必要花这个代价（以及限流预算）。本 Load 与下面的屏障检查之间
	// 新开始的任务，会被 trySetClaimBarrier 里 claimMu 之下的严格复检拦住。
	if running := d.activeTasks.Load(); running > 0 {
		d.logger.Debug("auto-update: skip — tasks running", "active", running)
		return
	}

	release, err := fetchLatestRelease()
	if err != nil {
		d.logger.Warn("auto-update: fetch latest release failed — will retry", "error", err)
		return
	}
	if release == nil || release.TagName == "" {
		return
	}
	if !isNewerVersion(release.TagName, d.cfg.CLIVersion) {
		return
	}

	// 用 CAS 置位 updating 标志，防止“服务器触发的 handleUpdate 恰好落在心跳 tick 上”
	// 造成双触发。在 triggerRestart 之前的每条退出路径上都释放——一旦触发，daemon ctx
	// 被取消，标志随进程一起消亡。
	if !d.updating.CompareAndSwap(false, true) {
		d.logger.Debug("auto-update: skip — update already in progress (raced)")
		return
	}
	released := false
	defer func() {
		if !released {
			d.updating.Store(false)
		}
	}()
	// 严格屏障：从便宜的预取空闲检查到现在，release 拉取花了少则几十毫秒（常见）、多则
	// 几秒（慢链路、GitHub 抖动）——足够一个轮询器认领全新任务。trySetClaimBarrier 在
	// claimMu 下检查 claimsInFlight + activeTasks，只有两者都为零才把 pauseClaims 翻成
	// true；所以一旦它返回 true，就能放心执行升级——不会有在途任务被 triggerRestart
	// 取消。
	if !d.trySetClaimBarrier() {
		d.logger.Info("auto-update: deferring — task or claim in flight at barrier check")
		return
	}
	barrierReleased := false
	defer func() {
		if !barrierReleased {
			d.releaseClaimBarrier()
		}
	}()

	d.logger.Info("auto-update: newer release available, upgrading",
		"current", d.cfg.CLIVersion, "target", release.TagName)

	output, err := d.runUpdateFn(release.TagName)
	if err != nil {
		d.logger.Warn("auto-update: upgrade failed — will retry", "error", err, "output", output)
		return
	}

	d.logger.Info("auto-update: upgrade completed, restarting", "target", release.TagName, "output", output)
	if !d.triggerRestart() {
		// 升级落地了，但交接目标解析不出来，不会有进程退出到来。落到两个 deferred
		// 恢复：为一次永远不会发生的重启死抱着 updating 标志与 claim 屏障，会让本
		// daemon 从此再也认领不了任何任务。下个 tick 会重试——而且既然磁盘上的二进制
		// 已经是新的，trySelfReload 也会一直重试。
		d.logger.Error("auto-update: upgrade completed but restart could not be scheduled — resuming claims")
		return
	}
	// triggerRestart 会取消根 context，使 Run() 返回、父进程（cmd_daemon.go）重新 exec
	// 新二进制。让 updating 标志与 claim 屏障保持置位——进程退出在即，清掉任一个都会
	// 在关停途中给“新认领 / 第二个自动更新 tick”开窗。
	released = true
	barrierReleased = true
}

// trySelfReload 在磁盘上的 multica 二进制不再报告“编译进本进程的版本”时重启 daemon。
//
// 这是自更新的“越权”半边。`brew upgrade multica`、重新下载或开发者的 `make build`
// 都会在同一路径替换二进制，而 daemon 仍继续服务它启动时那个版本：自己的版本字符串
// 在编译期就已冻结，而 resolveAgentEntry 里的“固定路径自愈”只在路径*消失*时触发——
// 原地覆盖永远不会让路径消失。
//
// tryAutoUpdate 最终能覆盖其中最常见的一种形态——运行版本比最新 release 老，所以它
// 重跑升级（brew 下是无操作）并重启。它覆盖不了的是其余：自动更新默认关闭的自托管
// daemon（MUL-2381）、被 isReleaseVersion 跳过的开发构建、以及安装 GitHub 不认为是
// “更新”的东西（刻意降级，或中间版本）。而且它最多慢一个完整检查间隔。本检查补齐了
// 所有这些，因此它有自己的开关（--no-auto-reload /
// MULTICA_DAEMON_AUTO_RELOAD=false / disable_auto_reload），而不是挂在
// MULTICA_DAEMON_AUTO_UPDATE 上。
//
// 重启机制与 tryAutoUpdate 共用而不是另起炉灶：同一个 trySetClaimBarrier 保证不打断
// 运行中的任务，同一个 triggerRestart 执行交接。刻意没有“待重启”状态机、没有 drain
// 钩子——繁忙 daemon 直接顺延到下一 tick，本检查绝不可能在等什么东西唤醒它时一直压着
// 认领。顺延会写进 reloadPendingReason，纯粹为了让 /health 能解释 daemon 为何还在旧
// 版本上。
//
// 一次探测失败绝不会被当成版本变化。二进制最可能不可读的窗口，恰恰是本检查要探测的
// 那次升级的几百毫秒（路径消失、ETXTBSY——runtimeVersionProbeAttempts 记录的同类），
// 而“读不出版本”不是重启进它的理由。
func (d *Daemon) trySelfReload(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if d.RestartBinary() != "" {
		return
	}
	// 用 CAS 抢升级所有权而不是采样。纯 Load() 只是提示：handleUpdate 可能在“读取”与
	// triggerRestart 之间的缝隙里赢下 CAS，本路径随后会趁二进制还在被替换时就取消根
	// context——exec 到半写文件、并中途切断服务器触发升级的状态上报。真正仲裁的只有
	// CAS，因为 claim 屏障不管这事：handleUpdate 经 tryBeginServerUpdate 到屏障，两者
	// 从不看对方的状态。与 tryAutoUpdate 同款。
	//
	// 除成功交接外，下面每条退出路径都会释放；交接时进程即将退出，清掉它会让第二次尝试
	// 在关停中途启动。
	if !d.updating.CompareAndSwap(false, true) {
		d.logger.Debug("auto-reload: skip — update already in progress")
		return
	}
	released := false
	defer func() {
		if !released {
			d.updating.Store(false)
		}
	}()

	// 探测“重启实际会 exec 的那个二进制”，不是 os.Executable()：brew 下两者不同，而
	// 跟随稳定符号链接正是 restartTargetBinary 的意义所在。
	target, err := d.restartTargetBinary()
	if err != nil {
		d.logger.Warn("auto-reload: could not resolve own executable — will retry", "error", err)
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, selfReloadProbeTimeout)
	defer cancel()
	onDisk, err := detectSelfVersion(probeCtx, target)
	if err != nil {
		d.logger.Warn("auto-reload: version probe failed — will retry, not treating it as a change",
			"binary", target, "error", err)
		return
	}
	// 任一侧为空都会让比较失去意义，据此行动比什么都不做更糟：空磁盘版本是一次“技术上
	// 退出 0 却什么也没告诉我们的”探测，空运行版本则会重启进一个同样读出空白的后继者——
	// 从此每个 tick 都重启一次，永无休止。
	if onDisk == "" || d.cfg.CLIVersion == "" {
		d.logger.Warn("auto-reload: version unavailable — will retry, not treating it as a change",
			"binary", target, "on_disk", onDisk, "running", d.cfg.CLIVersion)
		return
	}
	if onDisk == d.cfg.CLIVersion {
		d.clearReloadPending()
		return
	}

	reason := fmt.Sprintf("multica binary on disk reports %s, running %s", onDisk, d.cfg.CLIVersion)
	d.setReloadPending(reason)

	if !d.trySetClaimBarrier() {
		d.logger.Info("auto-reload: deferring — task or claim in flight at barrier check", "reason", reason)
		return
	}
	barrierReleased := false
	defer func() {
		if !barrierReleased {
			d.releaseClaimBarrier()
		}
	}()

	d.logger.Info("auto-reload: restarting into the binary on disk", "reason", reason, "binary", target)
	if !d.triggerRestart() {
		// 交接时解析失败。两个 deferred 恢复都会跑：一次失败的重启绝不能夺走 daemon 的
		// 认领能力，也不能让 updating 标志替一场不会来的重启守着。下个 tick 从零重新
		// 探测，所以不存在需要复检的过期基线。
		return
	}
	released = true
	barrierReleased = true
}

// setReloadPending 记录“multica 版本变化已确认，但重启在等 daemon 空闲”。纯诊断——
// 在 /health 与 `daemon status` 上可见——不 gate 任何东西，因此绝不可能像 pending
// 状态机那样把 daemon 挂死。
func (d *Daemon) setReloadPending(reason string) {
	d.reloadPendingReason.Store(&reason)
}

// clearReloadPending 在磁盘版本重新匹配后（运维在 daemon 空闲前回退了二进制）丢掉
// 该记录。
func (d *Daemon) clearReloadPending() {
	d.reloadPendingReason.Store(nil)
}

// reloadPending 返回顺延重启的原因；没有则为空串。
func (d *Daemon) reloadPending() string {
	if reason := d.reloadPendingReason.Load(); reason != nil {
		return *reason
	}
	return ""
}
