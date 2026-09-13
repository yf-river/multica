package taskfailure

// 本文件职责：两个「resume 前哨」判定——判断某 agent 错误是否意味着「会话
// 历史已无法再发给 provider（历史里嵌了空内容消息，每次 resume 都重放同样的
// 拒绝）」或「provider SDK 解析不出自己的凭证（resume 必然再次失败，重开会话
// 才会好）」。两者命中时，daemon 不应把会话当作可 resume 指针继续复用。

import (
	"regexp"
	"strings"
)

// UnresumableHistory 报告某 agent 错误是否意味着「会话历史本身再也无法发给
// provider」：一条已烤进 transcript 的消息带着空内容，于是该会话的每一次
// resume 都重放同一段 body、复现同一次拒绝。
//
// 这个判定刻意与 provider 无关——这正是要点。最初的检测器把 "400" 与
// "invalid_request_error" 配对（见 internal/daemon 的 classifyPoisonedError），
// 那是 Anthropic 的线形态。同样的缺陷被其它 provider 以两个 token 都不在的
// 方式上报：
//
//	Invalid request: the message at position 37 with role 'assistant' must not be empty   (GH #6066)
//	provider.api_error: 400 the message at position 43 with role 'assistant' must not be empty  (GH #5760)
//	messages.37: all messages must have non-empty content ...                             (Anthropic)
//	messages[43].content: content must not be empty
//
// 按状态码或 provider 名做键，就意味着漏掉下一个措辞不同的后端——Multica
// 支持 17 个后端、手里只有一个不透明的会话 id，无法知道是哪个 CLI 写下了被截断
// 的 transcript。而它们全都共同声明了定义这个缺陷的两件事：某处内容为空，
// 以及历史里它是哪条消息。
//
// 两个信号都要，这正让判定保持窄。一个报 "commit message must not be empty"
// 的工具没有消息定位器，不匹配；一段提到 "messages[3]" 却没有空内容控诉的
// diff 也不匹配。宁可偏「不匹配」是安全方向：漏判保留今天的行为（任务失败、
// 用户重试），而误判会丢弃一个健康的会话指针、丢失对话上下文。
func UnresumableHistory(errText string) bool {
	if errText == "" {
		return false
	}
	return emptyContentRe.MatchString(errText) && historyMessageLocatorRe.MatchString(errText)
}

// AuthMethodUnresolved 报告某 agent 错误是否「provider SDK 拒绝解析它自己的
// 凭证」——没有 api_key、没有 auth_token、也没有显式省略鉴权头。在 RESUME
// 的会话上这是确定性的而非瞬时：承载凭证的 provider 身份活在 runtime 重建的
// 会话状态里，重放同一会话就永远复现同一错误。开新会话会从当前配置重新解析
// provider，于是成功（GH #6777）。
//
// 刻意只用 provider 的确切短语、不放宽。每一个其它「长得像鉴权」的错误——
// token 过期、key 被吊销、401——都关于凭证本身，不会因新会话而好转，宽化
// 这个判定会在「重试修不了」的失败上丢掉健康的会话指针。Classify 把这段文本
// 留在 agent_error.unknown（resume 安全），这正是守卫必须按文本而非 reason
// 来判定的原因。
//
// 这是该短语的唯一事实来源。请与 GetLastTaskSession / GetLastChatTaskSession
// 的 resume 查询（pkg/db/queries）保持同步——它们在服务端应用同一守卫，
// 让「老到没带回合内重试的 daemon」写入的行同样被排除出 resume。
func AuthMethodUnresolved(errText string) bool {
	if errText == "" {
		return false
	}
	return strings.Contains(strings.ToLower(errText), authMethodUnresolvedPhrase)
}

// authMethodUnresolvedPhrase 是 AuthMethodUnresolved 匹配的小写 provider 短语。
// 无论失败经 session/resume、session/set_model 还是 session/prompt 到达我们，
// 它都逐字出现在 runtime 的错误里——因为每个 ACP 适配器都用 %v 包裹底层消息
// 而不是替换它。
const authMethodUnresolvedPhrase = "could not resolve authentication method"

// emptyContentRe 匹配 provider 对「某 content 字段为空」的控诉，覆盖各 provider
// 观察到的措辞。
var emptyContentRe = regexp.MustCompile(`(?i)must not be empty|must be non-?empty|must have non-?empty|non-?empty content|cannot be empty|should not be empty`)

// historyMessageLocatorRe 匹配错误里指向「会话历史中某条消息」的部分——一个
// role 名、一个下标或一个位置。缺了它，空内容控诉就只是关于某个别的字段，
// 与 transcript 无关。
//
// 与 GetLastTaskSession / GetLastChatTaskSession resume 查询（pkg/db/queries）
// 里的等价正则保持同步——它们在服务端对「老 daemon 归类成 agent_error.unknown
// 的行」应用同样的文本守卫。
var historyMessageLocatorRe = regexp.MustCompile(`(?i)role[^a-z0-9]{0,2}assistant|assistant message|message at position|messages\.[0-9]|messages\[[0-9]`)
