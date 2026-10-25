// agent_ready.go —— agent 就绪判定：用调用方真正分支的词汇给出 readiness 结论（available/waitable/blocked），
// 作为所有准入路径的单一事实来源。
package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/multica-ai/multica/server/internal/dispatch"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AgentAvailability 是就绪检查的结论，用调用方真正分支的词汇表达。
//
// 要紧的区分不是 “就绪与否”，而是 WAITING 是否可行。休眠的笔记本会自己回来，排队工作等它是对的；而绑到
// 空 runtime、或绑到 CLI 无法运行的机器的 agent，在人类介入前永远不会拿起工作。dispatch 的原因码早已画出这条线
// ——本类型是服务端半边，让每条准入路径按同样方式画。
type AgentAvailability int

const (
	// AgentAvailable：agent 现在就能接活。
	AgentAvailable AgentAvailability = iota
	// AgentWaitable：现在跑不了，但没坏——机器离线，为它排队的活会在其回归时运行。
	AgentWaitable
	// AgentBlocked：在有人介入前无人会认领该 agent 的工作。调用方必须拒绝触发并说明原因。
	AgentBlocked
)

// AgentVerdict 是一次就绪决策加上调用方据以行动的全部信息：线协议原因码，以及（daemon 上报时）修复 runtime 的
// 命令。
type AgentVerdict struct {
	Availability AgentAvailability
	// Reason 是非可用结论的 dispatch 原因码，可用结论下为空。
	Reason dispatch.ReasonCode
	// Repair 是 daemon 为不可用 runtime 上报的修复：拥有坏入口的 npm 包与重装它的命令。其它结论以及 daemon
	// 太老没上报时为缺失。
	Repair *RuntimeRepair
	// Detail 是 daemon 自己的描述，供日志与“被阻断的非交互触发”留档。从不解析。
	Detail string
}

// Ready 判断 agent 现在能否接活。
func (v AgentVerdict) Ready() bool { return v.Availability == AgentAvailable }

// Blocked 判断等待是否徒劳、调用方必须拒绝触发而非入队。
func (v AgentVerdict) Blocked() bool { return v.Availability == AgentBlocked }

// RuntimeRepair 是 daemon agent.ExecFormatRepair 的线协议镜像。
type RuntimeRepair struct {
	Package string `json:"package,omitempty"`
	Command string `json:"command,omitempty"`
	// Shell 是 Command 所面向的解释器，由必须运行它的机器上报（“bash”、“powershell”）。作为代码块的 language
	// 渲染，Windows 用户不会被塞进 bash 栅栏里的 POSIX 语法。daemon 太老未上报时为空。
	Shell string `json:"shell,omitempty"`
}

// runtimeOfflineReason 是 daemon 的结构化离线解释，由注销 handler 存在 runtime 行 metadata 上。
type runtimeOfflineReason struct {
	Code   string         `json:"code"`
	Detail string         `json:"detail"`
	Repair *RuntimeRepair `json:"repair"`
}

// runtimeOfflineCodeNotExecutable 是 daemon 对 “OS 拒绝执行该 agent CLI” 的原因码（daemon.RuntimeOfflineCode
// NotExecutable）。按字符串比较，因为服务端不得 import daemon 包。
const runtimeOfflineCodeNotExecutable = "not_executable"

// AgentReadiness 报告 agent 现在能否接新活，以及不能时调用方该怎么办。
//
// lookup 携带“读取所用连接”与“该 runtime 读取归属的来源标签”（MUL-6884），使每条准入路径在
// multica_agent_runtime_lookup_total 中都可区分。
//
// 只有对 runtime 行的 DB 查找失败时 err 非 nil。把瞬时 DB 错误当 “不跳过” 的调用方（autopilot 准入闸门）应
// 吞掉它；需要硬性是/否的调用方（handler 的 squad-leader 入队前检查）应 fail closed。
//
// 这是下列各方的单一事实来源：
//   - service.shouldSkipDispatch（autopilot 准入闸门）
//   - service.dispatchRunOnly（squad-leader runtime 检查，MUL-2429）
//   - handler.isSquadLeaderReady（issue 指派 / 评论触发路径）
//   - 直接 agent 触发路径，只为 BLOCKED 结论查询它：离线机器仍会排队，因为那种等待会自行结束。
//
// 保持它们一致很重要，否则各路径会漂移——例如一条开始允许 “starting” runtime 而另一条不允许，当用户经两个
// 不同入口指派同一 agent 时 bug 才浮现。动这个函数，它们一起动。
func AgentReadiness(ctx context.Context, lookup RuntimeLookup, agent db.Agent) (AgentVerdict, error) {
	if agent.ArchivedAt.Valid {
		return AgentVerdict{
			Availability: AgentBlocked,
			Reason:       dispatch.ReasonTargetUnavailable,
			Detail:       "agent is archived",
		}, nil
	}
	if !agent.RuntimeID.Valid {
		return AgentVerdict{
			Availability: AgentBlocked,
			Reason:       dispatch.ReasonAgentRuntimeRequired,
			Detail:       "agent has no runtime bound",
		}, nil
	}
	rt, err := lookup.Get(ctx, agent.RuntimeID)
	if err != nil {
		return AgentVerdict{}, err
	}
	if rt.Visibility == "private" && (!rt.OwnerID.Valid || !agent.OwnerID.Valid || rt.OwnerID != agent.OwnerID) {
		return AgentVerdict{Availability: AgentBlocked, Reason: dispatch.ReasonRuntimeAccessDenied, Detail: "agent owner does not match private runtime owner"}, nil
	}
	return runtimeVerdict(rt), nil
}

// runtimeVerdict 是只依赖 runtime 行的那半判定，拆出来使每个分支都能无 DB 测试。
func runtimeVerdict(rt db.AgentRuntime) AgentVerdict {
	if rt.Status == "online" {
		return AgentVerdict{Availability: AgentAvailable}
	}
	// 离线且原因属于 daemon 说的“人类须修复”：拒绝而非排队，并携带修复命令供调用方展示。
	if reason, ok := parseRuntimeOfflineReason(rt.Metadata); ok && reason.Code == runtimeOfflineCodeNotExecutable {
		return AgentVerdict{
			Availability: AgentBlocked,
			Reason:       dispatch.ReasonRuntimeUnusable,
			Repair:       reason.Repair,
			Detail:       reason.Detail,
		}
	}
	return AgentVerdict{
		Availability: AgentWaitable,
		Reason:       dispatch.ReasonRuntimeOffline,
		Detail:       "agent runtime is " + rt.Status,
	}
}

// parseRuntimeOfflineReason 从 runtime 行读取 daemon 的解释。无原因或服务端解析不了的 metadata，只是没有解释
// ——绝不会得出不同的结论。
func parseRuntimeOfflineReason(metadata []byte) (runtimeOfflineReason, bool) {
	if len(metadata) == 0 {
		return runtimeOfflineReason{}, false
	}
	var envelope struct {
		OfflineReason *runtimeOfflineReason `json:"offline_reason"`
	}
	if err := json.Unmarshal(metadata, &envelope); err != nil || envelope.OfflineReason == nil {
		return runtimeOfflineReason{}, false
	}
	return *envelope.OfflineReason, true
}

// RuntimeUnusableNotice 是触发被拒（因目标 agent CLI 无法在其机器上运行）时留在 issue 上的持久解释。
//
// 它放在这里、紧邻 verdict，因为有两层会写它：拒绝 @mention 的 handler 与拒绝指派的 service。一个文本、一个
// 修改点。daemon 上报了修复命令时点名它，没上报时依然有用——原生安装的 CLI 没有可重跑的 postinstall，编个命令
// 会把用户送到不存在的地方。
func RuntimeUnusableNotice(agentName string, verdict AgentVerdict) string {
	name := agentName
	if name == "" {
		name = "The assigned agent"
	}
	if verdict.Reason == dispatch.ReasonRuntimeAccessDenied {
		return fmt.Sprintf("%s cannot use this private runtime. Rebind the agent to a runtime its owner can use.", name)
	}
	if verdict.Repair != nil && verdict.Repair.Command != "" {
		return fmt.Sprintf(
			"%s could not start: its CLI is installed but cannot be executed on that machine, so this trigger was not queued.\n\n"+
				"Usually the package's postinstall was blocked (npm 12 allowScripts, pnpm 10 approve-builds, `--ignore-scripts`, `--omit=optional`) and the bin entry is still a placeholder. On that machine, run:\n\n"+
				"```%s\n%s\n```\n\n"+
				"The runtime comes back on its own within a couple of minutes; trigger the agent again after that.",
			name, repairFenceLanguage(verdict.Repair.Shell), verdict.Repair.Command,
		)
	}
	return fmt.Sprintf(
		"%s could not start: its CLI is installed but cannot be executed on that machine, so this trigger was not queued. "+
			"Reinstall the agent CLI on that machine with install scripts enabled; the runtime comes back on its own within a couple of minutes.",
		name,
	)
}

// repairFenceLanguage 用命令所面向的 shell 标记代码块。太老、没上报 shell 的 daemon 早于 Windows 渲染，
// 其命令按构造即 POSIX。
func repairFenceLanguage(shell string) string {
	if shell == "powershell" {
		return "powershell"
	}
	return "bash"
}

// RuntimeBlockedNeedsNotice 让无法自动恢复的准入拒绝留下可操作的说明。
func RuntimeBlockedNeedsNotice(code dispatch.ReasonCode) bool {
	return code == dispatch.ReasonRuntimeUnusable || code == dispatch.ReasonRuntimeAccessDenied
}
