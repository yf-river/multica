package taskfailure

// 本文件职责：识别「上下文耗尽」的另一种表观——agent runtime 把一次因上下文
// 窗口满而结束的回合报告成了成功完成。结构化的 terminal_reason 常量与文本侧
// 判定 ContextExhaustedCompletion 是同一个问题的两副药。

import "strings"

// TerminalReasonPromptTooLong 是 Claude Code 在回合因请求放不进模型上下文窗口
// 而结束时，写进 stream-json 结果事件的 `terminal_reason` 的值——可能是 resume
// 时会话本就超限，也可能是自动压缩跑了却没能把它压回线内。
//
// 它是结构化枚举值而非散文：CLI 在 SUCCESS 子类型的结果帧上，与 image_error /
// model_error / max_turns / completed 等一起携带它。它是该帧上唯一说明「回合
// 为何结束」的字段。`is_error` 回答的是另一个问题——CLI 依据它渲染的最后一条
// 消息是不是 API 错误消息来设置——两者独立计算，所以一次 run 的真实终态必须
// 从这个字段读，而不是从那个 flag 推。
//
// 取自 Claude Code 2.1.220（GH #6402 上报的版本）：对一条塞满的 transcript 做
// resume、命中了返回 provider 400 prompt-too-long 形态的端点；帧逐字固化为
// fixture，见 pkg/agent/testdata/claude-code-2.1.220-context-exhausted-resume.jsonl。
// 自动压缩跑过、上报 compact_result=failed / compact_error=exhausted，回合结束：
//
//	{"type":"result","subtype":"success","is_error":true,
//	 "terminal_reason":"prompt_too_long","api_error_status":400,
//	 "result":"Prompt is too long"}
//
// 2.1.221 产出同样形态。由此得出两点，其中第二点正是这个常量存在的原因：
//
//   - 该字段在上报的版本上就存在，所以按它判定能覆盖到正撞 bug 的主机，
//     而不只是以后的主机。
//   - 在这帧上 is_error 与 terminal_reason 同时触发。runtime 先读哪个，
//     失败标签就归谁；而只有 terminal_reason 保证指名了条件——`result`
//     散文在某些形态下是空的，还随版本改写措辞。
//
// 注意抓取没有显示的东西：is_error 为 false 的情形。上报者主机上那个产出
// `completed` 任务的精确逃逸没在这里复现，所以下方文本侧判定只是一个有界的
// 兜底，不是这次修复承重的主体。
const TerminalReasonPromptTooLong = "prompt_too_long"

// contextExhaustedOutputMaxLen 限制「被报为成功的输出」要多长以内还能被重读为
// 上下文耗尽通告。下方每种措辞都是 CLI 在「不给答案」时吐的简短一行；真正干完
// 活再讨论上下文限制的 agent 会写远超这个长度的文字。与 internal/daemon 的
// poisonedOutputMaxLen 同理由同取值——漏判付出修复前行为的代价，误判则把一个
// 真实结果变成失败、并退役一个健康会话。
const contextExhaustedOutputMaxLen = 320

// ContextExhaustedCompletion 报告「agent runtime 报为成功最终答案」的输出，
// 是否其实是 provider 在说该会话的上下文窗口满了。
//
// 这是 TerminalReasonPromptTooLong 的文本侧对应，服务于结构化字段不可得的两个
// 地方：
//
//   - CLI 没有等价结构化字段的后端。
//   - server 的 /complete 边界——那里的调用方可能是老到没带结构化检查的已安装
//     daemon。daemon 按自己的节奏升级，所以只做 daemon 侧的修复要等每台主机
//     更新才有人受益——而一台未升级的主机就意味着一个永久卡死的 (agent,
//     issue) 对，而不只是一行贴错标签的记录（与 NormalizeDaemonReason 同论证，
//     MUL-5370）。
//
// 每条子句都是复合的，没有一条是裸的自然语言短句。GitHub 报告把 CLI 转述为
// "context too long, please run /compact"，而它的两半都不宜单独使用：
// "/compact" 会出现在普通 Claude Code 建议里，"context too long" 也是 agent
// 写自己 run 时完全可能写出的短语。下面每条子句钉住的是取自 Claude Code
// 二进制本身、而非取自报告的完整独特措辞：
//
//	"Prompt is too long · … A single-exchange conversation cannot be compacted; …"   (3 个变体，全带 "cannot be compacted")
//	"Conversation too long. Press esc twice to go up a few messages and try again."
//	"Compaction failed · conversation could not be reduced below the context limit"
//
// CLI 的裸 "Prompt is too long" 刻意不在匹配之列，即便它是真实的终态结果——
// 匹配它意味着把一个任务的整个答案（agent 被问 "is my prompt too long?" 时
// 完全可能合法地写出一句普通英文）宣判为失败。而且这个判定只看「调用方以为
// 是 SUCCESS」的输出，一次命中就要搭上一个真实任务和一个健康会话。
//
// 就现有证据而言漏掉它零成本：在 GH #6402 抓到的 2.1.220 与 2.1.221 帧里，
// 裸形态都带着 is_error，会经 /fail 路由，而 Classify 的规则 1 一直把 "prompt
// is too long" 映射到 context_overflow。那只是关于这几帧的陈述，不是每个构建
// 的承诺——若某帧带着裸句作为干净的 success 出现，这里会漏，而这正是我们
// 刻意选择偏漏的方向。
func ContextExhaustedCompletion(output string) bool {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || len(trimmed) > contextExhaustedOutputMaxLen {
		return false
	}
	lowered := strings.ToLower(trimmed)
	switch {
	case strings.Contains(lowered, "prompt is too long") &&
		strings.Contains(lowered, "cannot be compacted"):
		return true
	case strings.Contains(lowered, "conversation too long") &&
		strings.Contains(lowered, "press esc twice"):
		return true
	case strings.Contains(lowered, "compaction failed") &&
		strings.Contains(lowered, "reduced below the context limit"):
		return true
	}
	return false
}
