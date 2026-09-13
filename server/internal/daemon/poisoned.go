package daemon

import (
	"strings"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// 下面这些 FailureReason 对应"会话被毒化"的任务——即后续任务若恢复同一段对话，
// 会确定性地复现同样的失败。列在这里是为了让服务器侧的 GetLastTaskSession 查询
// 能把它们过滤掉，使下一个任务从全新 agent 会话开始，而不是继承坏状态。
//
// 四种形态：
//   - 输出侧：agent "completed"，但其输出实际上是已知的回退标记（中途放弃、发了
//     元消息）或 provider 的上下文窗口耗尽提示（GH #6402）。由
//     classifyPoisonedOutput 检测。
//   - 错误侧：LLM API 本身以 400 invalid_request_error 拒绝请求（载荷过大、
//     畸形图片等）。坏消息已经烙进对话历史，因此每次恢复都会撞上同一个 400。
//     由 classifyPoisonedError 检测。
//   - 超时侧：Codex 在会话卡住、无 agent 进展后上报 semantic inactivity。恢复该
//     Codex 会话可能重放同一卡死状态，而手动全新重跑或许能成功。由
//     classifyResumeUnsafeTimeout 检测。
//   - 传输侧：一次 Codex thread/resume 的响应大到无法读回。线程只会继续膨胀，
//     因此之后每次恢复都会以同样的方式溢出。由 classifyResumeUnsafeTransport
//     检测。
//
// MUL-2946：ReasonIterationLimit 与 ReasonAPIInvalidRequest 别名到规范的
// taskfailure 值，让 daemon 与在途分类器（其它每个失败路径都用它）共享单一事实
// 来源。agent_fallback_message 与 codex_semantic_inactivity 是不在规范 21 类里
// 的既有运维原因——先以字符串字面量留在这里，等后续 PR 迁移或扩展分类法。
const (
	FailureReasonIterationLimit          = string(taskfailure.ReasonIterationLimit)
	FailureReasonAgentFallbackMsg        = "agent_fallback_message"
	FailureReasonAPIInvalidRequest       = string(taskfailure.ReasonAPIInvalidRequest)
	FailureReasonCodexSemanticInactivity = "codex_semantic_inactivity"
	FailureReasonCodexResumeOversized    = "codex_resume_oversized"
)

// poisonedOutputMaxLen 限制一段输出最长多少仍可被分类为毒化回退。真正的回退消息
// 短小，是一句话；一段恰好提及某标记的长输出几乎肯定是真实结论（例如引用这些
// 字符串的代码评审回复，就像当前 MUL-1630 里引用的那个）。该上限刻意偏向于
// 不分类——漏判一个毒化任务可由用户操作重试，而误判会把一次成功任务变成失败
// 和一条系统评论。
const poisonedOutputMaxLen = 320

// poisonedMarkers 把已知 agent 回退终态消息的"子串指纹"映射到失败原因分类器。
// 匹配大小写不敏感、基于子串；上面的上限防止引用标记的长输出被误分类。
var poisonedMarkers = []struct {
	Substring string
	Reason    string
}{
	{"i reached the iteration limit", FailureReasonIterationLimit},
	{"put your final update inside the content string", FailureReasonAgentFallbackMsg},
}

// classifyPoisonedOutput 报告 output 是否匹配已知 agent 回退终态消息，若匹配则
// 返回应持久化到任务行的 failure_reason。长输出永不被分类：真正的回退是 agent
// 该回合唯一的话语，因此超过约一段的内容即使含标记子串也按真实结果对待。
func classifyPoisonedOutput(output string) (string, bool) {
	// GH #6402：provider 的"本会话上下文窗口已满"提示被当作本次运行的"成功答案"
	// 上报。与下面的标记同一毒化形态——恢复会永远复现它，因为已经超限的会话
	// 不可能压缩回限内——但由共享分类器识别，让服务器的 /complete 边界对"老到
	// 没有这项检查"的 daemon 也施加完全一致的规则。
	//
	// 注意什么不是这里的条件：运行执行了多少工具。上下文窗口在任务中途填满远
	// 比首轮常见，因此按 tools == 0 门控会漏掉常见情形。工具计数只决定某次运行
	// 能否被 REPLAY，而这里什么都不会重放——agent_error.context_overflow 不在
	// retryableReasons 里，因此任务只失败一次、其会话退役、下一个触发器从全新
	// 开始。
	if taskfailure.ContextExhaustedCompletion(output) {
		return string(taskfailure.ReasonAgentContextOverflow), true
	}
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || len(trimmed) > poisonedOutputMaxLen {
		return "", false
	}
	lowered := strings.ToLower(trimmed)
	for _, m := range poisonedMarkers {
		if strings.Contains(lowered, m.Substring) {
			return m.Reason, true
		}
	}
	return "", false
}

// classifyPoisonedError 报告某 agent 错误消息是否说明 LLM API 本身拒绝了请求体
// ——即对话历史包含 API 不接受的内容（超大图、畸形 base64、prompt 过长等）。
// 这段对话无法恢复：每次重试都重放同一请求体、复现同一个 400。分类器返回
// FailureReasonAPIInvalidRequest，使 GetLastTaskSession 把该任务从 (agent_id,
// issue_id) 恢复查找中排除，该 issue 的下一个任务从全新会话开始，而不是永久
// 继承坏状态。
//
// 匹配形态：Claude Code SDK 与类似 backend 原样浮出上游 API 失败，例如
//
//	API Error: 400 {"type":"error","error":{"type":"invalid_request_error","message":"Could not process image"},"request_id":"..."}
//
// 同时匹配 "400" 与 "invalid_request_error" 让分类器保持收窄：429 限流、5xx
// 过载与工具形态错误都是瞬时的，重试时 SHOULD 恢复。
//
// 但那是 Anthropic 的形态，并非 provider 上报"不可处理 transcript"的唯一方式。
// 最后一条子句委托给 taskfailure.UnresumableHistory，它检测任意 backend 烙进对话
// 的空消息——为何必须靠措辞而非状态码或 provider 来识别缺陷，见其 doc 注释。
func classifyPoisonedError(errMsg string) (string, bool) {
	if errMsg == "" {
		return "", false
	}
	lowered := strings.ToLower(errMsg)
	// Kiro/ACP 会重放烙进已恢复对话历史里的图片；超过 provider 最大像素尺寸的
	// 图片会在每次 session/prompt 上被拒绝，无法靠恢复绕开（GH #5975）。daemon
	// 的任务内全新会话重试救得了当前这一回合，但这里把对话标记为 resume-unsafe，
	// 使 GetLastTaskSession 排除它、之后的任务不再选中被毒化的会话。出错的
	// messages[n].content[m] 路径与像素上限留在浮出的错误里；base64 载荷永不
	// 记录。要求图片内容标记与尺寸短语同时出现，保持判定收窄——提及尺寸的无关
	// 错误不会误触发。
	if strings.Contains(lowered, "image dimensions exceed max allowed size") &&
		strings.Contains(lowered, "image.source.base64.data") {
		return FailureReasonAPIInvalidRequest, true
	}
	// 两个标记必须同时出现："400" 单独太泛（工具可能从任何地方浮出一个 400），
	// "invalid_request_error" 单独理论上也可能出现在非毒化语境。组合是规范
	// Anthropic 错误形态，指示请求体——即对话历史——就是问题所在。
	if strings.Contains(lowered, "invalid_request_error") && strings.Contains(lowered, "400") {
		return FailureReasonAPIInvalidRequest, true
	}
	// 同一缺陷被换一种措辞的 provider 上报。上面的子句只对 Anthropic 形态生效，
	// 因此任意其它 backend 烙进 transcript 的空消息过去会落到 taskfailure.Classify
	// 的 agent_error.unknown——因遗漏而 resume-safe，这永久卡死了 (agent, issue)
	// 对（GH #6066、GH #5760）。taskfailure.UnresumableHistory 按 provider 说的
	// "什么错了"识别缺陷，而非按"哪个 provider 说的"。
	if taskfailure.UnresumableHistory(errMsg) {
		return FailureReasonAPIInvalidRequest, true
	}
	return "", false
}

// classifyResumeUnsafeTransport 报告某传输级失败是否意味着已记录的会话不得再次
// 恢复。
//
// 今天只有一个案例：Codex thread/resume 的响应溢出我们的 stdout 行缓冲。该会话
// 不只是倒霉，它已经完了：codex 把整个 thread 序列化进那一个响应，而它的 rollout
// 文件只会只增不减，因此未来对同一 thread 的每次恢复都会复现同一溢出。把它留作
// (agent, issue) 的恢复指针，正是这一事故变成永久卡死的原因（MUL-5722）。
//
// 这是回合内恢复的后盾，而不是替代。codex backend 把同一失败上报为
// Result.ResumeRejected，让 daemon 在全新会话上重试当前回合（见
// shouldRetryWithFreshSession）——那才是用户真正想要的结果。但该重试以 tools ==
// 0 为门槛、自身也可能失败，而且无论如何任务都会带着仍被记录的巨型 thread
// 落到这里。分类它能让后续任务即使"当前回合没能救回"，也不会再碰那个 thread。
//
// 刻意按 provider 限定，与下面 classifyResumeUnsafeTimeout 一样：没有别的 backend
// 会通过单行重放全部历史，因此对它们来说超长行是一次性事件，session 仍然可用。
func classifyResumeUnsafeTransport(provider, errMsg string) (string, bool) {
	if strings.ToLower(strings.TrimSpace(provider)) != "codex" {
		return "", false
	}
	if agent.CodexResumeOverflowError(errMsg) {
		return FailureReasonCodexResumeOversized, true
	}
	return "", false
}

// classifyResumeUnsafeTimeout 报告某次超时是否意味着已记录的会话不应再被恢复。
// 刻意保持按 provider 限定：普通的 daemon/backend 超时是基础设施形态的，应保留
// 恢复指针，让重试能继续那段在途对话。
func classifyResumeUnsafeTimeout(provider, errMsg string) (string, bool) {
	if strings.ToLower(strings.TrimSpace(provider)) != "codex" || errMsg == "" {
		return "", false
	}
	lowered := strings.ToLower(errMsg)
	if strings.Contains(lowered, strings.ToLower(agent.CodexSemanticInactivityMarker)) ||
		strings.Contains(lowered, strings.ToLower(agent.CodexFirstTurnNoProgressMarker)) {
		return FailureReasonCodexSemanticInactivity, true
	}
	return "", false
}
