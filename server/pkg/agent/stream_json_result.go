package agent

// 文件职责：stream-json 家族 backend（Claude Code、CodeBuddy 等）共享的"终局裁定"
// 逻辑——把一次运行里看到的各类证据归并成最终的 status/output/错误文本。
//
// 一个贯穿始终的契约是"fail-closed + 空输出"：进程干净退出并不等于协议完整走完，
// 成功必须见过 result 事件；失败的运行一律返回空 output，绝不能让上游的 issue/聊天
// 兜底把一段残缺转写误当最终答案。成功但没有可交付文本时也返回空 output——"空"
// 是平台约定俗成的信号，各投递面都自己拿它做决策（issue 任务跳过兜底评论、直聊
// 写 no_response 结果、Slack/Lark 直接丢掉本回合）；在这里硬塞散文会让 output 变
// 非空，三个投递面同时被悄悄架空——英语哨兵句当年就是这样混进 Lark 帖子的
// （GH #6462）。用户看到什么词、用什么语言，属于各投递面；流解析器只报告"有没有
// 文本"。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"time"
)

// assistantTurn 是对单条 assistant 流事件的事后归类。
//
// 各 stream-json backend 解析的线上形态不同，但每个事件都问同一个问题：早前捕获
// 的兜底答案还能不能活过这个事件？
type assistantTurn struct {
	text     string // 本事件里的文本
	toolUses int    // 本事件里的工具调用次数
	// understood 为 false 表示事件正文解析失败，或带了 backend 不处理的内容块类型。
	// 两种情况 backend 都无法判断模型是否在那个事件里交付了答案，所以更早的兜底
	// 绝不能活过它。
	understood bool
}

// resolveFallback 返回本回合之后的"空结果兜底"，入参是本回合之前持有的兜底。三个
// stream-json backend 在这里统一编码"哪些回合可以留下答案"。
func (t assistantTurn) resolveFallback(prior string) string {
	switch {
	case t.toolUses > 0 || !t.understood:
		// 调用了工具的回合即使同时含旁白也是中间态；读不懂的回合什么都没告诉我们
		// ——模型可能以我们解析不了的形式答了。两者都不能给终局结果留下过期答案：
		// 否则工具调用前的旁白就会以"最终答案"的面目到用户手里（#6006），下面那个
		// fail-closed 契约就是为掐掉它而存在的。
		return ""
	case t.text != "":
		return t.text
	default:
		// 读懂了、没文本也没工具——一个纯思考回合。它既不带答案、也不带中间态
		// 信号，所以必须原样保留模型已经交出的答案。
		return prior
	}
}

// streamTerminalState 把面向用户的最终答案与流式 assistant 回合分开保存。
// assistant 消息仍会经 Session.Messages 通道发出去供实时进度/转写落库，但只有
// 终局结果（或"显式成功的空结果之后最后一条完整的 assistant 消息"）才能成为
// Result.Output。
type streamTerminalState struct {
	lastAssistantText string // 最后一段完整 assistant 文本（空结果时的兜底输出）
	finalResultText   string // result 事件带的终局文本
	sawResult         bool   // 是否见过终局 result 事件
	resultIsError     bool   // 终局结果是否失败
	scanErr           error  // stdout 读取错误
	// terminalReasonError 非空时，是 backend 从终局 result 事件的某个"结构化字段"
	// 里读出的失败。
	//
	// 这并不表示 is_error 漏判了失败——在至今捕获的每个帧上两者是一起触发的。它
	// 表示的是"两者中谁点名了失败"：Claude Code 的 is_error 来自"它渲染的最后一条
	// 消息是不是 API 错误"，而 terminal_reason 来自"回合为什么结束"，所以只有后者
	// 不依赖 CLI 散文就能指认情形（GH #6402）。不读这类字段的 backend 保持本值为空、
	// 沿用旧契约。
	terminalReasonError string
}

// finalizeStreamResult 应用 Claude Code 与 CodeBuddy 共享的 fail-closed 终局契约。
// 进程干净退出不等于 stream-json 协议完整走完：成功必须见过 result 事件。失败的
// 运行一律返回空 output，让上游 issue/聊天兜底永远不会把残缺转写误当最终答案。
//
// 成功但无交付文本的运行出于同一原因也返回空 output。空是平台"本回合没有产出最终
// 文本"的信号，每个投递面都已有挂在它上面的决策：issue 任务跳过合成兜底评论、直聊
// 写本地化的 no_response 结果、Slack/Lark 频道丢回合而不外推占位文本。在这里返回
// 散文会让 output 非空、同时静默架空这三者——英文哨兵句就是这么到达 Lark 线程的
// （GH #6462）。用户看到哪句话、用什么语言，属于各投递面；流解析器只报告文本存在
// 与否。
func finalizeStreamResult(
	provider string,
	timeout time.Duration,
	runErr error,
	writeErr error,
	exitErr error,
	sessionID string,
	state streamTerminalState,
	completionGuardError string,
) (status, output, errMsg string) {
	status = "completed"
	switch {
	case state.terminalReasonError != "":
		// 认得的结构化终局原因直接胜出——包括压在 is_error 之上，这正是实践里
		// 要紧的次序。
		//
		// 在 Claude Code 2.1.220/2.1.221 真实捕获的形态里，上下文耗尽（context
		// exhausted）的回合会同时带 is_error:true 和 terminal_reason:prompt_too_long。
		// 若让 is_error 先行，结构化分支在我们手上唯一的帧上就是死代码，并把失败
		// 拱手交给 CLI 恰好写进 `result` 的散文：今天那句散文碰巧分对了类，但一旦
		// result 为空或被改写，就会退化成 "returned an error result without details"
		// → agent_error.unknown——没有哪张续跑黑名单覆盖它，于是已饱和的会话被
		// 一直钉住，卡顿卷土重来（GH #6402）。
		//
		// 让它排在最先不花任何代价：该分支只在"backend 明确认出某个原因"时才触发，
		// 拼出的消息还带着 CLI 自己的 `result` 文本作细节，诊断不丢信息。
		status = "failed"
		errMsg = state.terminalReasonError
	case state.resultIsError:
		status = "failed"
		errMsg = state.finalResultText
		if errMsg == "" {
			errMsg = provider + " returned an error result without details"
		}
	}

	// 上一段只把"已经认出的失败"定下来；这一段处理进程/传输层面的异常。
	switch {
	case status == "completed" && errors.Is(runErr, context.DeadlineExceeded):
		status = "timeout"
		errMsg = fmt.Sprintf("%s timed out after %s", provider, timeout)
	case status == "completed" && errors.Is(runErr, context.Canceled):
		status = "aborted"
		errMsg = "execution cancelled"
	case state.scanErr != nil && status == "completed":
		status = "failed"
		errMsg = fmt.Sprintf("%s stdout read error: %v", provider, state.scanErr)
	case writeErr != nil && status == "completed" && sessionID == "":
		// prompt 写入失败：只有还没建立起会话时才值得判失败——已有会话 id 说明
		// 进程早已开始工作，写入报错多半是进程退出所致，由下面 exitErr 分支去归因。
		status = "failed"
		errMsg = fmt.Sprintf("write %s input: %v", provider, writeErr)
	case exitErr != nil && status == "completed":
		status = "failed"
		errMsg = fmt.Sprintf("%s exited with error: %v", provider, exitErr)
	case !state.sawResult && status == "completed":
		status = "failed"
		errMsg = provider + " stream ended without terminal result"
	}

	// 完成守卫错误：跑完了，但守护条件（如收尾确认）不满足，按失败处理。
	if status == "completed" && completionGuardError != "" {
		status = "failed"
		errMsg = completionGuardError
	}

	if status != "completed" {
		return status, "", errMsg
	}
	if state.finalResultText != "" {
		return status, state.finalResultText, ""
	}
	// 显式成功的空 result：退回最后一段完整 assistant 文本（也可能是空）。
	return status, state.lastAssistantText, ""
}

// streamProtocolObservation 是一次运行里"协议层元数据"的观测样本，用于诊断
// "缺了终局事件"这类问题。
type streamProtocolObservation struct {
	provider                   string
	cliVersion                 string
	model                      string
	exitCode                   int
	eventCount                 int
	invalidEventCount          int
	assistantEventCount        int
	toolUseCount               int
	sawResult                  bool
	resultIsError              bool
	resultBytes                int
	lastAssistantBytes         int
	scannerError               bool
	lastEventType              string
	anthropicBaseURLConfigured bool
	// unhandledEventTypeCount / unhandledEventTypes / unhandledSubtypeCount
	// 报告解析器没有转成消息的事件。它们放在这一行而不是单独告警，是为了能与
	// toolUseCount、invalidEventCount 放在一起读。
	//
	// 它们是证据而非定论：非零表示流里带了我们不处理的事件，是识别协议变更的
	// 起点；零只表示"顶层没观测到"，并不能确立"agent 没用工具"——CLI 可能执行了
	// 工具却根本不把更新序列化出来，新形态也可能嵌在我们已识别的某个类型内部。
	// 由会追踪它们的 provider 设置；别处保持零。
	unhandledEventTypeCount int
	unhandledEventTypes     string
	unhandledSubtypeCount   int
	// unreadableAssistantCount 报告正文解析失败、或带了 backend 不处理内容块类型的
	// assistant 事件。与 unhandledEventTypeCount（我们跳过的*顶层*事件类型）不同，
	// 这里数的是"收下了却读不全"的事件。
	//
	// 非零意味着这次运行防御性地丢掉了空结果兜底，因此"以无最终文本结束的任务"
	// 可能本有一个我们看不见的答案。当某个原本正常的 agent 在 CLI 升级后开始返回
	// 空输出，这是第一个要查的数字。
	unreadableAssistantCount int
}

// logStreamProtocolObservation 只记录协议元数据。它刻意排除 assistant/result 文本、
// 工具输入输出、配置的 base URL 与环境值——排查"缺了终局事件"时，不能把任务转写
// 或 provider 凭据漏进 daemon 日志。
func logStreamProtocolObservation(logger *slog.Logger, obs streamProtocolObservation) {
	logger.Info("agent stream protocol summary",
		"provider", obs.provider,
		"cli_version", obs.cliVersion,
		"model", obs.model,
		"exit_code", obs.exitCode,
		"event_count", obs.eventCount,
		"invalid_event_count", obs.invalidEventCount,
		"assistant_event_count", obs.assistantEventCount,
		"tool_use_count", obs.toolUseCount,
		"saw_result", obs.sawResult,
		"result_is_error", obs.resultIsError,
		"result_bytes", obs.resultBytes,
		"last_assistant_bytes", obs.lastAssistantBytes,
		"scanner_error", obs.scannerError,
		"last_event_type", obs.lastEventType,
		"unhandled_event_type_count", obs.unhandledEventTypeCount,
		"unhandled_event_types", obs.unhandledEventTypes,
		"unhandled_subtype_count", obs.unhandledSubtypeCount,
		"unreadable_assistant_count", obs.unreadableAssistantCount,
		"anthropic_base_url_configured", obs.anthropicBaseURLConfigured,
	)
}

// streamProcessExitCode 从进程退出错误里取退出码；正常退出返回 0，拿不到退出码
// 返回 -1。
func streamProcessExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
