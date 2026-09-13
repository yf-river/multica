package agent

// 文件职责：本文件实现「自持管道的进程收集器」——collector 用 os.Pipe 跑命令、自己接管
// stdout/stderr 的排空与上限，供本包「跑一次命令取回答案」的调用方（如版本探测、
// config get 等）使用。它解决 launch.go 的 runOwned/outputOwned/combinedOutputOwned
// 办不到的一件事：当 os/exec 拥有排空权时，Wait 会一直阻塞到输出管道 EOF，而 EOF 要求
// 每个写端都关闭——CLI 若 fork 出继承了 stdout 的助手进程（OpenClaw 会拉 openclaw-config），
// Wait 会被拖住到助手寿命结束。collector 自己拿 *os.File 管道，os/exec 完全不启动拷贝
// goroutine，于是「直接子进程一退出 Wait 就返回」，何时算读完由本包决定。
//
// 关键设计：
//
//   - 答案判据以「管道 EOF」为信号，而不是「组长退出」——包装器可以成功退出而其真正的
//     CLI（继承了 stdout 的后代）还没打印（Windows 上 npm/PowerShell 垫片是常态）。三档
//     时间预算：collectDrainGrace（等 EOF）、collectSettleGrace（收尾等读者）、
//     collectReapWindow（重试收割整棵树）。
//   - stdout 是答案，用上限 collectStdoutLimit（8 MiB）封顶并在溢出时报错，而不是静默截断
//     成「成功的空答案」；stderr 是诊断样本，只留尾部 collectStderrTail（32 KiB）。
//   - finish() 负责收割进程树并尽可能把缓冲收敛到最完整状态；它刻意不回报错误——清理收
//     敛不了只值得记一条日志，绝不能决定调用方拿不拿得到答案（#6084 教训）。
//
// 安全性质见 RunCollectQuiet 的说明（本文件外的收集入口处）。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// collectReapWindow 限制 finish() 反复向进程树发信号的总时长，collectReapStep 是两轮之间
// 的间隔。
//
// 只发一轮不够。若某后代的 fork 恰好发生在「kill 枚举进程组」与「信号送达」之间，它永远
// 收不到这一轮信号，只能靠后面的轮次覆盖——run_collect_test.go 里 fork 桩测试实测 10 轮
// 中漏 3 次，每次都留下一个被 reparent 到 init 的 `sleep 300`。窗口只需要覆盖那次 fork，
// 所以刻意设得很短：重试更久会放大 reapProcessTree 上注明的 pid 复用窗口——进程组 id 就是
// Wait 已经 reap 掉的组长 pid。
const (
	collectReapWindow = 100 * time.Millisecond
	collectReapStep   = 10 * time.Millisecond
)

// collectSettleGrace 限制答案路径上唯一的一次清理等待：进程树被信号处理之后，finish()
// 在把「已到达的内容」交给调用方之前，最多等读取 goroutine 看到 EOF 多久。
//
// 刻意设短，而且到它运行时调用方已判定读取结束——要么答案满足了它的完整性规则，要么
// collectDrainGrace 等它等超时了。这个上限截断的是「继承了管道、一直握着它」的后代——它
// 的尾部输出不是答案。EOF 会短路这次等待，所以正常情况零代价。
const collectSettleGrace = 400 * time.Millisecond

// collectDrainGrace 限制在「直接子进程已退出之后、进程树被收割之前」，等待管道 EOF 的时长。
//
// 它存在是因为组长退出不等于输出结束，把它当结束是数据丢失 bug 而非慢路径。包装器可能
// 成功退出而真正的 CLI——它继承 stdout 的后代——还没打印；npm 与 PowerShell 垫片在
// Windows 上让这成为常态。评审在本分支的早期版本上实测过后果：包装器退出 0，其子进程
// 500ms 后才打印版本；在组长退出时收割返回的是「空答案 + nil 错误」，而 os/exec 自己的
// EOF 等待能取回版本。
//
// 所以 EOF 才是信号，这里是它的上限。2s 与 probeWaitDelay 一致——那是 os/exec 出于同样
// 原因给 launch.go 各辅助函数套的等价上限——永不关管道的后代不能把调用无限拖住。
//
// 调用方的完整性规则会短路它：答案已进缓冲就没什么好等的了，所以正常情况——CLI 打印完
// 答案即退出，无论有没有赖着不走的助手——零代价。
const collectDrainGrace = 2 * time.Second

// collectStdoutLimit 封顶本包会累积的「答案」，collectStderrTail 封顶从 stderr 保留的诊断
// 样本。
//
// 早期版本里两条流都不设上限，评审实测了代价：连续往探测窗口写的 CLI 保留了 13,107,400
// 字节 stderr，而 launch.go 的 outputOwned 只保留最后 probeStderrSampleBytes（32 KiB）。
// 一个陷在日志循环里的坏本地 CLI 可能在任何截止时间起作用前就把 daemon 内存耗尽。
//
// 两个上限刻意「性质不同」，因为两条流性质不同：
//
//   - stderr 是诊断样本，重要的是*尾部*——CLI 真正的失败行在末尾。保留最后 32 KiB 与
//     outputOwned 完全一致，所以调用点在两种机制之间迁移，不会改变失败探测上报的内容。
//   - stdout 是答案，静默丢掉答案的一部分，正是「截断的目录」变成「成功的空目录」的方式。
//     所以它封顶并把溢出报成错误：一次性响应超过上限是故障而非大答案。上限落在哪由这些
//     调用点要过的最大答案推导，见下方 collectStdoutLimit。
const collectStderrTail = probeStderrSampleBytes

// 用 var 而非 const，测试可以把它调小、不用生成几兆字节来够到它——detectVersionTimeout
// 是 var 也是同一原因。生产代码从不重赋它。
//
// 8 MiB 是推导出来而非拍脑袋的，因为这里落在哪是「产品兼容边界」而不只是内存边界：合法的
// 配置若超了上限，任务会直接起不来。推导顺序（按它约束值的先后）：
//
//   - 实际要的是什么。在 OpenClaw 2026.7.1-2 上实测：`config validate --json` 回 715 字节、
//     `config file` 回 2663。任何调用点要过的最大答案是完全解析后的配置（来自
//     `config get --json`），其大小随用户的 agents 与 MCP 服务器数增长。
//   - 上游对两者都没有天花板，所以最坏情形是构造出来的：1000 个 agent + 250 个 MCP
//     服务器、带 OpenClaw 已解析配置含有的字段，序列化后 547,291 字节。真实部署只有个位数
//     agent，那已是超出实际数量级的余量。这个上限在*那之上*再留 15x——TestCollectStdout
//     LimitHasHeadroomOverTheLargestAnswer 就断言这一点；构造逻辑放在测试里，推导就不会
//     静默偏离数值。
//   - 它与 daemon 对同类「整块读进内存的每任务载荷」的上限一致（internal/daemon 的
//     maxLocalSkillBundleSize 同为 8 MiB），两条上限不必分开推理。用命名而非导入：pkg/agent
//     是更底层。
//
// 设个上限是无可避免的，而非设计取舍：每个调用方都把答案当一份文档解析，放不进内存的答案
// 本来也没法用。推导换来的是边界远离任何真实主机产出的答案；TestCollectedStdoutBoundaryIsExact
// 精确钉住边界位置——上限处答案原样返回，超一字节就 fail-closed 抛 errCollectStdoutTooLarge，
// 而不是被静默截短。
var collectStdoutLimit = 8 << 20

// outputBuffer 累积一条流，并记录最后一次写入落在何时。
//
// buf 与 lastWrite 刻意在同一临界区内更新。若在释放锁之后才发布时间戳，读者可能看到「新字节
// + 过期时间戳」，误判流「刚安静下来」——对 RunCollectQuiet 而言等于在答案写到一半时截断它。
//
// max 限制留存。keepTail 选择保留哪一端：tail 缓冲从头丢弃、继续读（stderr，样本型）；
// head 封顶缓冲停止累积并记溢出，让调用方选择失败而不是返回一份静默截短的答案（stdout）。
type outputBuffer struct {
	mu        sync.Mutex
	buf       []byte
	lastWrite time.Time
	max       int
	keepTail  bool
	overflow  bool
}

// absorb 把 r 读进缓冲，直到 EOF 或文件被关闭。除这两者外的读错误会上报；截断的流会以
// 「读到多少给多少」呈现给调用方，与之前 cmd.Output() 的行为一致。
//
// 两种模式下读到超过 max 都继续。停下会让子进程因管道写满而卡在 write() 里——对 head 封顶
// 的流来说，等于把「答案太大」变成一次挂死。
func (o *outputBuffer) absorb(r io.Reader) error {
	chunk := make([]byte, 32*1024)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			now := time.Now()
			o.mu.Lock()
			o.append(chunk[:n], now)
			o.mu.Unlock()
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
				return nil
			}
			return err
		}
	}
}

// snapshot 返回当前缓冲内容的副本（调用方可安全持有）。
func (o *outputBuffer) snapshot() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]byte(nil), o.buf...)
}

// append 在调用方已持锁的前提下记录 p，遵守 max。即使有字节被丢弃也会更新时间戳：流当时
// 确实是活的，一个据此判「空闲」的检查若判错就错了。
func (o *outputBuffer) append(p []byte, now time.Time) {
	o.lastWrite = now
	if o.max <= 0 {
		o.buf = append(o.buf, p...)
		return
	}
	if o.keepTail {
		o.buf = append(o.buf, p...)
		if len(o.buf) > o.max {
			o.buf = append([]byte(nil), o.buf[len(o.buf)-o.max:]...)
			o.overflow = true
		}
		return
	}
	room := o.max - len(o.buf)
	if room <= 0 {
		o.overflow = true
		return
	}
	if len(p) > room {
		p = p[:room]
		o.overflow = true
	}
	o.buf = append(o.buf, p...)
}

// overflowed 报告 max 是否导致丢过任何字节。
func (o *outputBuffer) overflowed() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.overflow
}

// idleFor 报告距最后一次写入过去多久、以及是否曾写过任何东西。
func (o *outputBuffer) idleFor() (time.Duration, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.lastWrite.IsZero() {
		return 0, false
	}
	return time.Since(o.lastWrite), true
}

// collector 用「本包自持的管道」跑命令，而不是把 io.Writer 交给 os/exec、让它去排空。
//
// 这个区别正是全部重点，也是 launch.go 的 runOwned/outputOwned/combinedOutputOwned 办不到
// 的。os/exec 拥有排空权时，Wait 会阻塞到输出管道 EOF，而 EOF 要求每个写端都关闭——于是
// 一个 fork 出继承 stdout 助手的 CLI（OpenClaw 会拉 `openclaw-config`）会让 Wait 一直开
// 到助手寿命结束。取消 context 也没用：它只杀直接子进程，解不开 os/exec 的 io.Copy。
// 改交给 os/exec 一个 *os.File 后，它完全不启动拷贝 goroutine，于是「直接子进程一退出
// Wait 就返回」，何时算读完由本包决定。
//
// 那三个受管辅助函数改用 probeWaitDelay 封顶等待——对它们服务的探测是对的——但上限耗尽时
// 会化成 exec.ErrWaitDelay，于是一个「答案已到、助手还赖着」的 CLI 会被报成调用失败。在
// OpenClaw 路径上不可容忍：`--version` 失败会跳过运行时注册，`config file` 失败会让任务
// 准备失败。RunCollectQuiet 说明买到了什么保证、给不了什么保证。
//
// 由此得到的推论是血的教训：本包决定何时读完，就必须决定得对。直接子进程的退出只证明直接
// 子进程——「包装器退出而其后代还欠着答案」在 Windows 上是常态——所以 awaitOutputAfterExit
// 在有界内等管道 EOF，除非调用方的规则说答案已到手。
type collector struct {
	cmd        *exec.Cmd
	outR, errR *os.File
	stdout     outputBuffer
	stderr     outputBuffer

	readers  chan struct{} // 两个 absorb 循环都返回后关闭
	waitDone chan struct{} // cmd.Wait 返回后关闭
	waitErr  error         // waitDone 关闭后有效

	finishOnce sync.Once
}

// startCollector 收的是调用方已构建好的 *exec.Cmd 而非可执行文件路径，这样带 argv 前缀的
// 启动（Command.exec，即自定义 runtime 的 fixed_args）能保住前缀；在这里现造命令会静默丢掉
// 它。
//
// 调用方不得已启动它，且必须让 Stdout/Stderr 保持未设置：本包会装自己的 *os.File 管道，
// 这正是 Wait 不被后代拖住的全部原因。
//
// 没有 ctx 参数：collector 的生命周期由下方调用方限定，它们自己在 ctx 上 select。这里收一个
// ctx 会暗示由本函数来强制执行它。
func startCollector(cmd *exec.Cmd, env []string) (*collector, error) {
	if env != nil {
		cmd.Env = env
	}
	hideAgentWindow(cmd)
	configureProcessGroup(cmd)

	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("collect stdout pipe: %w", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return nil, fmt.Errorf("collect stderr pipe: %w", err)
	}

	cmd.Stdout = outW
	cmd.Stderr = errW

	// 用 startOwnedProcessTree 而非 cmd.Start：Windows 上它先把子进程挂起并归入 Job
	// Object，再放它跑第一条指令——这样 .cmd 垫片没机会在所有权边界之外拉出真正的 CLI。
	// Unix 上上面的 configureProcessGroup 已做过等价的事，这里就是普通 Start。无论哪种，
	// finish() 收割的都是整棵树而非直接子进程。
	if startErr := startOwnedProcessTree(cmd, slog.Default()); startErr != nil {
		releaseProcessGroup(cmd)
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		return nil, startErr
	}

	// 立刻丢弃父进程的写端：不然无论子进程树收割得多彻底，EOF 都永远到不了。
	outW.Close()
	errW.Close()

	c := &collector{
		cmd:      cmd,
		outR:     outR,
		errR:     errR,
		readers:  make(chan struct{}),
		waitDone: make(chan struct{}),
	}
	// 答案封顶并上报溢出；诊断保留尾部。见 collectStdoutLimit。
	c.stdout.max = collectStdoutLimit
	c.stderr.max = collectStderrTail
	c.stderr.keepTail = true

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = c.stdout.absorb(outR) }()
	go func() { defer wg.Done(); _ = c.stderr.absorb(errR) }()
	go func() { wg.Wait(); close(c.readers) }()
	go func() { c.waitErr = cmd.Wait(); close(c.waitDone) }()

	return c, nil
}

// finish 收割进程树，并把缓冲收敛到 collectSettleGrace 内能达到的最完整状态，调用方随后
// 可直接 snapshot。
//
// 它刻意不回报任何东西。清理收敛不了意味着 OS 拒绝杀某个进程——那值得记一条日志，但不能
// 决定调用方拿不拿得到答案。输出已到、助手内核却不肯杀的版本探测，正是 #6084 里 cmd.
// WaitDelay 判错的场景——它判失败了一次本已成功的探测，而失败的版本探测会整个跳过运行时
// 注册。
//
// 可从调用方任何退出路径重复调用，且安全。
func (c *collector) finish() {
	c.finishOnce.Do(func() {
		// 收割 CLI fork 出来、留在组长组里的任何东西——成功路径也收：一次成功的
		// `openclaw --version` 仍会留下助手，这正是「按定时器探测的主机」上孤儿积累的
		// 方式。调过 setsid 的助手超出这里的能力范围——见 RunCollectQuiet 的保证 2——
		// 但这一步也释放了最后一个写端、让下方读者能看到 EOF，这一点不依赖 kill 是否
		// 命中。
		//
		// 在 collectReapWindow 内重试，因为单轮会漏掉一个「信号发出时正处于 fork 中途」
		// 的后代；见该常量。
		reapKill(c.cmd)
		treeGone := waitProcessGroupGone(c.cmd, collectReapStep)
		for reapDeadline := time.Now().Add(collectReapWindow); !treeGone && time.Now().Before(reapDeadline); {
			reapKill(c.cmd)
			treeGone = waitProcessGroupGone(c.cmd, collectReapStep)
		}

		// 因为管道归本包所有，Wait 不会被后代拖住：直接子进程一没它即返回——上面的 kill
		// 刚刚确保了这一点。
		settleDeadline := time.Now().Add(collectSettleGrace)
		waitReturned := waitUntil(c.waitDone, settleDeadline)
		drained := waitUntil(c.readers, settleDeadline)

		// 无论哪种都停止读取。这里不等待任何东西：Windows 上匿名管道不可 poll，关读端
		// 无法驱逐一个阻塞中的 Read，等 absorb 循环只会让本调用陪存活的幸存后代活那么久。
		// 循环本身无害——snapshot 拿缓冲的互斥锁，一个还在 append 的循环不会与读它的调用
		// 竞争——它们在后代最终退出时自行结束。
		c.outR.Close()
		c.errR.Close()
		// 只有现在：Windows 上关闭 job handle 才会杀掉里面仍在的任何东西，所以不能抢在
		// 进程树可能还在供输出之前跑。
		releaseProcessGroup(c.cmd)

		if !treeGone || !drained || !waitReturned {
			slog.Default().Warn("agent: collect cleanup did not converge",
				"command", c.cmd.Path,
				"tree_gone", treeGone,
				"output_drained", drained,
				"wait_returned", waitReturned,
				"window", collectReapWindow,
				"settle_grace", collectSettleGrace)
		}
	})
}

// reapKill 是进程树 kill，间接化让测试可以制造「单轮漏杀」，证明是 finish() 里的重试兜的底。
// 生产代码从不重赋它。
var reapKill = reapProcessTree

// waitUntil 等到 done 关闭或 deadline 到期；返回是否在到期前等到。
func waitUntil(done <-chan struct{}, deadline time.Time) bool {
	select {
	case <-done:
		return true
	default:
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// exitErr 不阻塞地返回命令的退出错误，以及命令是否已被 reap 过。
func (c *collector) exitErr() (error, bool) {
	select {
	case <-c.waitDone:
		return c.waitErr, true
	default:
		return nil, false
	}
}

// awaitOutputAfterExit 在直接子进程退出后，等待本次调用仍欠着的输出。
//
// 它强制的区分正是本分支早前版本搞错的：组长退出只说明*组长*做完了，不代表输出做完了。
// 包装器可能退出 0，而继承它 stdout 的真正 CLI 还没打印。只有管道 EOF 才意味着每个写端都
// 已关闭、不会再有多余输出，所以这是它等的东西——用 collectDrainGrace 封顶，因为永不关
// 管道的后代不能无限拖住调用。
//
// complete 会短路这次等待：答案已进缓冲就没什么可欠的了，于是「打印完即退出的 CLI」即使
// 在管道上留了个助手，在这里也零代价。rule 为 nil 时无法识别答案，EOF 或上限就是仅有的
// 停止条件。
//
// 注意它*并不*额外要求缓冲已安静。空闲是「写者停了」的代理信号，而组长退出是那一点的直接
// 证据；两个都要，等于让每个守规矩的调用为它已挣到的空闲宽限再付一次。
func (c *collector) awaitOutputAfterExit(ctx context.Context, complete OutputComplete) {
	if complete != nil && complete(c.stdout.snapshot()) {
		return
	}
	deadline := time.Now().Add(collectDrainGrace)
	ticker := time.NewTicker(quietPoll)
	defer ticker.Stop()
	for {
		select {
		case <-c.readers:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if complete != nil && complete(c.stdout.snapshot()) {
				return
			}
			if time.Now().After(deadline) {
				return
			}
		}
	}
}

// reapProcessTree 向 cmd 子进程率领的进程组发 SIGKILL，让子进程 fork 出的助手与它同亡。
// 子进程已退出后调用也安全：configureProcessGroup 让子进程当组长，它的 pid 兼作组 id，
// 只要还有成员在跑，进程组就比组长活得久。空组会得 ESRCH，signalProcessGroup 会吸收掉。
//
// 组级 kill 在 Wait reap 掉组长之后立刻发出，所以原则上组长 pid 已可被复用。顺序 pid 分配
// 使该窗口内（微秒级，且要绕完整个 pid 空间）的复用不成现实问题，别的 backend 的取消路径
// 也活在同一个窗口里。
//
// Windows 上 signalProcessGroup 终止的是 startOwnedProcessTree 把子进程归入的 Job Object，
// 能覆盖同样的后代；见 proc_windows.go。
func reapProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	signalProcessGroup(cmd, syscall.SIGKILL)
}
