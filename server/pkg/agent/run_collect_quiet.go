package agent

// 文件职责：给"一次性输出式"的短命令提供"安静收集"运行器——不等进程退出，而是
// 一旦 stdout 已给出完整答案并安静了一阵就收工。
//
// 动机：openclaw 之类 CLI 的 config file / agents list 会"打印完答案就永不退出"
// （实测 60s 挂着），死等退出会把两条好使的命令变成任务致命错误。完整的设计动机、
// 为什么必须由调用方给"答案完整性"规则、以及调用方可依赖的五条保证，都写在
// RunCollectQuiet 的文档里——那里是唯一详细展开的地方。被谁消费：Prepare →
// prepareOpenclawConfig 等任务 setup 的一次性探测命令。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// DefaultQuietIdleGrace 是"stdout 已经带上一份完整答案之后，还必须安静多久"才让
// RunCollectQuiet 停止等 CLI 退出。
//
// 按"CLI 已刷完答案"的场景定尺寸：这些命令打印一个路径或一段短 JSON，同一条逻辑
// 响应的各次写入背靠背落地——真实主机实测间隔 0.012–0.033ms。400ms 比它大了几个
// 量级，又短到让任务 setup 的这类调用在最坏情况下只多花约一秒，而不是每次都吃满
// openclawCLITimeout。
//
// 这个尺寸*不*覆盖、而必须由调用方的 `complete` 规则覆盖的，是"输出里非答案的
// 部分与答案本身之间的空档"。同一台主机上，一个会产 Doctor/plugin 警告的配置，
// 在警告块结束到 config 路径之间埋了整整 3285ms 的静默。任何会接受"答案之前打印
// 的东西"的规则，就有 3.2s 空档可能对它误触发，这里改成多大的值都救不了——
// review 曾用一条"本身就是路径"的警告行，把 5s 宽限的规则打穿过。
//
// 所以要求落在规则上而不是这个常量上：规则必须"CLI 在答案之前打印的任何东西都
// 无法满足它"。本包唯一够格的规则是 JSONOutputComplete——文档必须整体可解析，且
// 上游把零碎输出完全挡在 `--json` 的 stdout 之外。凭"长得像"去判断人读的输出不
// 够格；曾经试过的 config file 规则被删掉而不是重调——见 internal/daemon/execenv
// 里的 openclawOutputComplete。
//
// 它同时是"迟到的失败"仍能被观察到的窗口：CLI 打印完整答案后以非零码退出，只要
// 发生在宽限内，就会被如实报成失败。超过宽限，它与"打印完就永远挂着"无法区分——
// 而后者正是这个 helper 存在要扛的情形。
const DefaultQuietIdleGrace = 400 * time.Millisecond

// quietPoll 是 RunCollectQuiet 重估退出条件的轮询间隔。
const quietPoll = 50 * time.Millisecond

// OutputComplete 报告"截至目前捕获的字节"是不是产生它们的命令的*已完成*答案。
//
// 这是调用方的判断，不是运行器能推断的：只有调用方知道
// `{"agents":[{"id":"a"},` 是一份截断的文档还是一份合法的。没有它，"有点输出"
// 就会被误当成"有完整输出"，被打断半截的响应被报成成功。
type OutputComplete func(stdout []byte) bool

// JSONOutputComplete 是"整份答案恰为单个 JSON 文档"的命令所用规则。空输出永远
// 不算完整；单独的 `null` 算——那是 CLI 报告"未设置键"的写法。
//
// 注意它要求整个缓冲区可解析：打印 JSON 之前先吐横幅文本的 CLI 永远不满足它。
// 这与调用方本就按整缓冲 json.Unmarshal 来解析结果的方式一致——那样的输出本来
// 也从来没法用，现在不过是改成"在超时时失败"，而不是"在解析时失败"。
func JSONOutputComplete(stdout []byte) bool {
	trimmed := bytes.TrimSpace(stdout)
	return len(trimmed) > 0 && json.Valid(trimmed)
}

// RunCollectQuiet 运行一条一次性 CLI 命令：进程退出即返回；对那种不肯退出的 CLI，
// 则一旦 stdout 产出被 `complete` 接受、随后又安静了 idleGrace，就立即返回。
//
// # 为什么"输出够了"优于"等退出"
//
// 实测自跑 openclaw 2026.5.27 的主机：
//
//	openclaw --version    258ms  干净退出
//	openclaw config file    60s  正确路径已打印，然后永不退出
//	openclaw agents list    60s  正确列表已打印，然后永不退出
//
// 死等退出会把两条好使的命令变成任务致命错误，而它们都卡在任务关键路径上
// （Prepare → prepareOpenclawConfig）。这些命令的契约是"打印一个值"；值到了，
// 进程有没有自我善后不是调用方该管的。
//
// 那也不是这批计时所来自的构建的怪癖。上游有显式的"打印完一次性答案就退出"
// opt-in——requestExitAfterOneShotOutput，在 runCli 末尾两条流都排空后冲刷
// （src/cli/one-shot-exit.ts，v2026.7.1 核对）。已发布的 2026.7.1 只有 models
// list、models status 与 hooks CLI 启用它。config file、config get、agents list
// 都没有：它们的成功路径只打印，结束与否是 Node 事件循环碰巧做什么，而
// runtime.exit 只出现在失败路径上。所以"某个版本它们退得干脆"不是后来版本必须
// 守的承诺。
//
// # 为什么必须有 `complete`，而不是"有输出就行"
//
// 更早的版本只要 stdout 非空就从超时分支返回成功。那会打捞残缺答案：CLI 在超时
// 到达时还在流式输出，其被截断的输出被报成成功（250ms 超时下实测 10 次里 9 次）。
// 现在有两个条件同时把守提前返回，单独哪个都不够：
//
//   - `complete` 接受该缓冲区。光安静不够——CLI 可能在横幅与真正答案之间停顿：
//     `openclaw config file` 会先打印 Doctor 警告 UI（见 MUL-3136），在那里掐掉
//     拿到的是横幅而不是路径。这条规则承担了全部区分工作：review 曾用"本身就是
//     路径的警告行"打穿"按形状判断路径"的规则，所以规则必须是 CLI 答案前的输出
//     根本无法满足的那种。
//   - 随后该缓冲区安静了 idleGrace。光完整不够——后面可能还有输出、还可能改变
//     答案。
//
// 走到 context 超时永远不算成功。捕获到的内容会连同错误一起返回，让持不同规则的
// 调用方自己检查，但本 helper 不替调用方作决定。
//
// `complete` 传 nil 则完全禁用提前返回：调用死等退出，只受 ctx 约束。对"输出
// 形态没有完整性规则"的命令，这是合适的默认。
//
// quiet 报告返回并非来自干净退出，让调用方能记下 CLI 的不规矩行为、而不必因此
// 判失败。
//
// 调用方依赖的保证：
//
//  1. 无论 CLI 留下什么，都会在大致"调用方 context 截止 + collectDrainGrace +
//     collectReapWindow + collectSettleGrace"内返回。
//  2. 返回前会向仍留在首进程进程组里的后代发信号，并且信号在 collectReapWindow
//     内重复发送，让"恰好 fork 到一半"的后代逃不掉。已离开进程组的后代在 Unix 上
//     就够不着了——OpenClaw 的 helper 实测有自己的 PGID 与 SID——所以保证 1 不
//     依赖这次 kill 打没打中。Windows 不同：Job Object 拥有整棵树，与组无关。
//  3. 只要还有东西可能正在写输出，就不抛弃输出：直接子进程退出后，除非 `complete`
//     说答案已在，否则继续等管道 EOF。仅"首进程退出"不算完成。
//  4. 保留有界——答案按 collectStdoutLimit（溢出会上报而不是静默截断），诊断样本
//     按 collectStderrTail 留尾部。
//  5. 如实报告命令真实退出状态——openclawShimDiagnostic 依赖它（它做
//     *exec.ExitError 的类型分支）。
//
// 只把整个输出是短一次性响应的命令交给它。任何增量流式输出的东西（agent 执行）
// 必须自己管理生命周期——那种场景里输出的停顿本身有意义。
func RunCollectQuiet(ctx context.Context, env []string, idleGrace time.Duration, complete OutputComplete, execPath string, args ...string) (stdout []byte, stderr string, quiet bool, err error) {
	// 用 Command.exec 而不是 exec.CommandContext：进程构造归 launch.go 所有，
	// 这样自定义运行时的 fixed_args 前缀就不会丢（GH #7046）。零值 Command 没有
	// 前缀，正好对应这里传裸路径参数的情况。
	return RunCollectQuietCmd(ctx, Command{Path: execPath}.exec(ctx, args...), env, idleGrace, complete)
}

// RunCollectQuietCmd 是给已持有 *exec.Cmd（Command.exec 的返回值）的调用方用的
// RunCollectQuiet。
func RunCollectQuietCmd(ctx context.Context, cmd *exec.Cmd, env []string, idleGrace time.Duration, complete OutputComplete) (stdout []byte, stderr string, quiet bool, err error) {
	if idleGrace <= 0 {
		idleGrace = DefaultQuietIdleGrace
	}

	c, startErr := startCollector(cmd, env)
	if startErr != nil {
		return nil, "", false, startErr
	}

	ticker := time.NewTicker(quietPoll)
	defer ticker.Stop()

	for {
		select {
		case <-c.waitDone:
			// 直接子进程没了，这并不等于"不会再有输出"——包装器可能先退出，真正
			// 的 CLI 还欠着答案。有界地等管道 EOF，除非答案已经在手；见
			// awaitOutputAfterExit。
			c.awaitOutputAfterExit(ctx, complete)
			c.finish()
			out := c.stdout.snapshot()
			if c.stdout.overflowed() {
				return out, string(c.stderr.snapshot()), false, collectStdoutOverflowErr()
			}
			return out, string(c.stderr.snapshot()), false, c.waitErr

		case <-ctx.Done():
			c.finish()
			out := c.stdout.snapshot()
			// 刻意不做"打捞"。走到超时说明我们从没见过 `complete` 接受的输出，
			// 按调用方自己的规则，缓冲区就是不完整的。有进程错误时优先报它：
			// 调用方自己会归因 ctx，而 "signal: killed" 这类细节值得留在消息里。
			if werr, reaped := c.exitErr(); reaped && werr != nil {
				return out, string(c.stderr.snapshot()), true, werr
			}
			return out, string(c.stderr.snapshot()), true, ctx.Err()

		case <-ticker.C:
			if complete == nil {
				continue
			}
			// 轮询安静判定：有产出且安静时长达到宽限，且缓冲区被 `complete`
			// 接受，才提前收工。
			idle, produced := c.stdout.idleFor()
			if !produced || idle < idleGrace {
				continue
			}
			out := c.stdout.snapshot()
			if !complete(out) {
				continue
			}
			// 一份完整的答案、而且安静了：收下并收割整棵树。`out` 是 `complete`
			// 接受的那份缓冲区，刻意不重新快照——晚到的后代进程在判定之后追加的
			// 字节从不是答案的一部分，只会弄坏解析。
			c.finish()
			if c.stdout.overflowed() {
				return out, string(c.stderr.snapshot()), true, collectStdoutOverflowErr()
			}
			return out, string(c.stderr.snapshot()), true, nil
		}
	}
}

// errCollectStdoutTooLarge 报告答案超出了 collectStdoutLimit。
//
// 是上报而不是静默收下：这些 helper 跑的是"响应是路径或短文档"的一次性命令，
// 溢出意味着 CLI 出毛病了；调用方拿一份被掐了头的答案去解析，等于在解析 CLI
// 从没说过的话。stderr 靠保留尾部封顶，因为它是诊断样本而非答案——丢它的开头
// 不会丢任何调用方依赖的东西。
var errCollectStdoutTooLarge = errors.New("collected stdout exceeded its limit")

// collectStdoutOverflowErr 把限额写进消息，同时保留 errCollectStdoutTooLarge 作为
// 调用方匹配的哨兵错误。
func collectStdoutOverflowErr() error {
	return fmt.Errorf("%w (%d bytes)", errCollectStdoutTooLarge, collectStdoutLimit)
}
