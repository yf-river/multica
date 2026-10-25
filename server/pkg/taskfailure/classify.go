package taskfailure

// 本文件职责：实现错误字符串分类器 Classify 及其配套守卫——把 agent
// runtime / CLI 吐出的自由文本错误映射到 14 个 agent_error.* 子原因之一，
// 以及把老版本 daemon 上报的粗标签在服务端升级成这套细分体系
// （NormalizeDaemonReason）。

import (
	"regexp"
	"strings"
)

// providerHTTP5xxRe 匹配「以 5 开头的三位数（HTTP 5xx 状态码）」且两侧不是
// 数字。镜像 MUL-1949 里的 SQL 正则 `(^|[^0-9a-z])5[0-9][0-9]([^0-9a-z]|$)`——防止
// "1500ms" 或 "1.5.0" 这种词意外落进 provider_server_error。
//
// 在包初始化时编译：分类器处于每个失败任务的在途写入路径上，
// 启动时付一次正则编译成本、而不是每次调用都编，值得。
var providerHTTP5xxRe = regexp.MustCompile(`(^|[^0-9a-z])5[0-9][0-9]([^0-9a-z]|$)`)

// httpAuthCodeRe / httpQuotaCodeRe / httpCapacityCodeRe 只在特定 3 位 HTTP
// 状态码「没有嵌进更长的数字」时才匹配，用与 providerHTTP5xxRe 相同的数字边界
// 护栏。没有这道护栏，裸子串 "401"/"402"/"403"/"429"/"529" 会命中无关数字——
// 例如 "402913 tokens"、"15290ms"、"exit status 4030"——把进程失败或未知失败
// 误判成 provider 计费/限流错误。这会污染失败可观测性：一次真实的进程崩溃被
// 记到 provider 桶下，在失败面板上掩盖了真正原因。（这里即便误判也不会引发
// 多余的重试：这些正则守护的 auth / quota / capacity 桶全都不可重试。internal/
// service/task.go 的 retryableReasons 白名单里唯一的 agent_error.* 原因是
// provider_network——MUL-4910——而这些正则永远不会路由到它。）5xx 桶当初正是
// 为此加了锚点（MUL-1949）；这几个码当时没加。
var (
	httpAuthCodeRe     = regexp.MustCompile(`(^|[^0-9a-z])(401|403)([^0-9a-z]|$)`)
	httpQuotaCodeRe    = regexp.MustCompile(`(^|[^0-9a-z])402([^0-9a-z]|$)`)
	httpCapacityCodeRe = regexp.MustCompile(`(^|[^0-9a-z])(429|529)([^0-9a-z]|$)`)
)

// Classify 把来自 agent runtime / CLI 的自由文本错误映射到 14 个
// agent_error.* 子原因之一。永远返回一个合法 Reason；没有任何规则命中或输入
// 为空时回落到 ReasonAgentUnknown。
//
// 规则顺序镜像 MUL-1949 的 SQL CASE 表达式（db-boy 的离线回填查询）。SQL 才是
// 事实来源：两者一旦分叉，错的是这个 Go 分类器，应改它对齐。保持两者同步是
// 必须的，这样在途写入的行与历史上回填的行共享同一套分类体系。
//
// 匹配方式是对小写化输入做大小写不敏感的子串匹配。更具体的规则排在更笼统的
// 规则之前（例如 context_overflow 排在 provider_quota_limit 之前，否则
// "token limit" 会因 "limit" 被配额桶抢走）。
//
// 为什么用子串分类器而不是结构化错误码：11 个后端包装器（server/pkg/agent/*）
// 都把上游 API 失败逐字透传在 Result.Error 里，常常是 `API Error: 400 {...}`
// 或原始 stderr 尾巴。坚持要结构化错误码就得挨个改所有后端；字符串分类器
// 今天就能给出细分的 failure_reason，也允许各后端的结构化升级各自独立落地。
func Classify(rawError string) Reason {
	trimmed := strings.TrimSpace(rawError)
	if trimmed == "" {
		// SQL 把 NULL/空串映射到单独的桶（"empty_error"），但那个桶不属于
		// 规范分类体系。在途调用方绝不该递空输入给我们——万一递了，最安全的
		// 落脚点就是这个 catchall。
		return ReasonAgentUnknown
	}
	lower := strings.ToLower(trimmed)

	switch {
	// 1. 上下文 / token 窗口溢出。提前检查，免得 "token limit" 被下面更宽泛
	//    的 "limit" / "quota" 规则吞掉。
	case containsAny(lower,
		"context length",
		"context_length_exceeded",
		"maximum context",
		"prompt is too long",
		"context size has been exceeded",
	),
		containsAny(lower, contextWindowExceededWitnesses...),
		// SQL 里是 `%token%limit%`——两个 token 之间用 ILIKE 通配。这里用
		// 「两个子串都在」来近似，能抓住 "token limit"、"tokens per minute
		// limit" 等，又不会产生裸 `Contains("token") || Contains("limit")`
		// 那种误报。
		strings.Contains(lower, "token") && strings.Contains(lower, "limit"):
		return ReasonAgentContextOverflow

	// 2. 缺配置 / API key。抢在 auth 之前检查，因为 "missing API key" 与
	//    "invalid api key" 措辞部分重叠，但结构上是配置错误，不是鉴权拒绝。
	case strings.Contains(lower, "missing environment variable"),
		strings.Contains(lower, "missing") && strings.Contains(lower, "api_key"),
		strings.Contains(lower, "api key") && strings.Contains(lower, "required"),
		strings.Contains(lower, providerUnconfiguredPhrase),
		strings.Contains(lower, "no provider configured"):
		return ReasonAgentMissingConfig

	// 3. 鉴权 / 访问权限。401 / 403 / "Not logged in" / 无效 token / 无模型
	//    访问权限。状态码带数字边界，避免 "4030" / "1401ms" 误落进来。
	case httpAuthCodeRe.MatchString(lower),
		containsAny(lower,
			"unauthorized",
			"login required",
			"not logged in",
			"please login again",
			"refresh token",
			"invalid api key",
			"access token",
			"subscription access",
			"does not have access",
			"you may not have access",
		):
		return ReasonAgentProviderAuthOrAccess

	// 4. 配额 / 计费。402 / insufficient_balance / 月度用量上限 / 积分耗尽。
	case httpQuotaCodeRe.MatchString(lower),
		containsAny(lower,
			"insufficient_balance",
			"balance is too low",
			"monthly usage limit",
			"usage limit",
			"you've hit your limit",
			// 弯引号变体：provider 与复制粘贴的错误串有时用 U+2019 而不是
			// ASCII 撇号。SQL ILIKE 同样匹配不到弯引号形态，所以这是对 SQL
			// 分类器的一处小改进。
			"you\u2019ve hit your limit",
			"credits",
			"quota",
		):
		return ReasonAgentProviderQuotaLimit

	// 5. 容量 / 限流。429 / 529 / overloaded / rate limit。
	case httpCapacityCodeRe.MatchString(lower),
		containsAny(lower,
			"rate limit",
			"overloaded",
			"no capacity available",
		):
		return ReasonAgentProviderCapacityOrRateLimit

	// 6. Provider 5xx / server error。5xx 正则在这里检查而不是做普通字符串
	//    匹配，因为 SQL 用的是带锚点的正则——见 providerHTTP5xxRe 的 doc。
	case containsAny(lower,
		"server had an error",
		"provider returned error",
		"internal error",
		"service unavailable",
		"bad gateway",
	),
		providerHTTP5xxRe.MatchString(lower):
		return ReasonAgentProviderServerError

	// 7. Provider 网络。流中断、拨号失败、HTTP 层之下的 DNS / I/O 超时。
	//    "connection closed" / "mid-response" 负责抓 Claude Code CLI 的流中途
	//    断开（"API Error: Connection closed mid-response. ..."），让瞬时断流
	//    落到可重试的 provider_network 桶（配会话 resume），而不是一路漏到
	//    agent_error.unknown / process_failure 把任务终结（MUL-4910）。抢在
	//    规则 13 前检查，保证 "... exited with error: exit status N ..." 变体
	//    仍路由到这里。
	//
	//    "deadline exceeded" 覆盖所有以文本形式到达分类器的 Go 侧 context
	//    deadline——被取消请求的 `context deadline exceeded`，以及 net/http 的
	//    `Client.Timeout exceeded while awaiting headers` 变体。在 MUL-5370
	//    之前这些全落到 agent_error.unknown（不在重试白名单上），于是一次瞬时
	//    卡顿变成无可用标签的终结性失败。注意这只抓「以裸字符串到达」的
	//    deadline；手里持有错误值的调用方应做结构化分类（见 daemon/daemon.go
	//    的 taskRunFailureReason）。
	//    OpenCode/CodeArts 的 "stream ended" 前缀覆盖共享终端信号守卫
	//    （pkg/agent/opencode.go）会抛出的每一种失败：EOF 处还开着的步骤、
	//    从未开始的续写、以及在一个没有文本/没有工具调用/没有上报用量 的步骤上
	//    结束的一次 run。三者含义相同——provider 流死了而 `opencode run` 仍以
	//    0 退出——按定义就是这个桶；而且因为可 resume 安全，重试会继续被截断
	//    的会话而不是重做工作。此前它们分别因 "terminal signal" 里的 "signal"
	//    误撞规则 13 落到 agent_error.process_failure，或落到
	//    agent_error.unknown；两者都不在重试白名单上，于是一次瞬时断流直接
	//    终结任务，max_attempts 从没生效（#6522）。
	//    Pi 的 OpenAI 兼容 SDK 会把一次被丢掉的 LiteLLM/OpenAI 调用以裸串
	//    "Connection error." 和 "Request timed out." 浮现在 turn_end.errorMessage
	//    上（然后以 1 退出）。这些过去会经 "exit status 1" 漏进
	//    process_failure、不重试就终结任务（BHD-135）。isPiProviderNetworkError
	//    只精确匹配这两条裸消息和稳定的 Pi/OMP 退出组合串，而不会把本地工具或
	//    MCP server 里同样宽泛的子串当作可重试处理。
	//    把这些 Pi 消息形态同步进 MUL-1949 离线回填 SQL。
	case isPiProviderNetworkError(lower),
		containsAny(lower,
			"stream disconnected",
			opencodeStreamEndedPrefix,
			codeartsStreamEndedPrefix,
			"connection closed",
			"mid-response",
			"error sending request",
			"unable to connect",
			"dial tcp",
			"connection refused",
			"connectionrefused",
			"dns",
			"i/o timeout",
			"deadline exceeded",
			"timeout exceeded while awaiting",
		):
		return ReasonAgentProviderNetwork

	// 8. 模型不存在 / 不可用。SQL 用 `%model%not%found%`，匹配中间夹任意内容
	//    的 "model … not found"；这里用「两个子串都在」近似，能覆盖 "model X
	//    not found"、"the requested model was not found" 这类常见说法。
	case strings.Contains(lower, "model") && strings.Contains(lower, "not found"),
		containsAny(lower,
			"unknown model",
			"selected model",
			"http 404",
			"404 page not found",
		):
		return ReasonAgentModelNotFoundOrUnavailable

	// 9. agent CLI 自己返回空输出 / 解析不了的输出。这些字符串来自
	//    server/pkg/agent/*.go 包装器，是稳定的。
	case containsAny(lower,
		"returned empty output",
		"returned no parseable output",
	):
		return ReasonAgentEmptyOrUnparseableOutput

	// 10. agent 子进程硬超时（每任务墙钟时间）。
	case strings.Contains(lower, "timed out after"):
		return ReasonAgentTimeout

	// 11. runner CLI 二进制缺失，或在当前机器上装了却跑不起来——包 postinstall
	//     被拦时留下的 npm 占位桩能通过一切「是否已安装」检查，只在 execve 时
	//     才失败（MUL-6164）。运维上两者结论相同：这台机器没有可用的 CLI，
	//     重跑也修不好。
	case containsAny(lower,
		"executable not found",
		"exec format error",
	):
		return ReasonAgentRuntimeMissingExecutable

	// 12. runner CLI 版本太老 / 协议不兼容。
	case containsAny(lower,
		"below the minimum supported version",
		"requires a newer version",
	):
		return ReasonAgentRuntimeVersionUnsupported

	// 13. agent / runner 进程级失败。在具体规则里放最后检查，因为 "exit
	//     status" / "signal" 会与更具体的上游错误同时出现，而后者应当胜出
	//    （例如一个「因为被 provider 限流而崩溃」的 agent 应归类为限流，而
	//    不是进程失败）。
	case containsAny(lower,
		"exit status",
		"signal",
		"panic",
		"sigsegv",
		"process exited",
		"start codex:",
		"pipe has been ended",
		"file already closed",
		"initialize failed",
	):
		return ReasonAgentProcessFailure
	}

	return ReasonAgentUnknown
}

// providerUnconfiguredPhrase 是 runtime 对「我一个 LLM provider 都没解析出来」
// 的措辞——结构上是配置缺口，不是被拒的凭证。这个 const 是它两个读者的唯一
// 事实来源：上面 Classify 的规则 2（映射到 ReasonAgentMissingConfig）以及下面
// 的 ProviderUnconfigured。让两者挨在一起；一旦出现第二份字面量拷贝，二者就
// 会漂移。
const providerUnconfiguredPhrase = "no llm provider configured"

// ProviderUnconfigured 报告某 agent 错误是否「runtime 拒绝启动，因为它根本没
// 解析出任何 provider」。调用方用它来附加上下文，说明 runtime 到底去哪找了——
// 失败文本只给补救办法（"run `hermes model`"），却不说是哪个家的配置该改；
// 一个读了与用户编辑的不同配置目录的 runtime，会把用户引去修错文件（GH #6872）。
//
// 用子串而非相等：该短语到达时通常裹在 ACP 传输层自己的框架里
// （`hermes session/new failed: session/new: Internal error (code=-32603,
// data={"details":"No LLM provider configured. …`）。
//
// 这不说缺的是哪个凭证、也不说补一个是否管用——它严格表示「runtime 没解析出
// provider」。过期的 key、401、或用户配了但打错的 provider 都是不同的失败，
// 绝不能宽化进本判定。
func ProviderUnconfigured(errText string) bool {
	if errText == "" {
		return false
	}
	return strings.Contains(strings.ToLower(errText), providerUnconfiguredPhrase)
}

// contextWindowExceededWitnesses 是上下文溢出「出现在响应侧」而非「请求被 400」
// 时的两种措辞：provider 接受了调用，却在回合结束时给 stop_reason
// "model_context_window_exceeded"，Claude Code 2.1.x 把它原样浮显成
// "API Error: The model has reached its context window limit."（GH #6360）。
// 两个都匹配，这样「后端转发原始 stop reason」与「转发 CLI 的转写」分类结果一致。
//
// 两者都不带 "token"、也不含规则 1 的其它短语，所以此前这种失败会落到
// agent_error.unknown——没有任何 resume 黑名单覆盖它，于是过满的会话被钉成
// resume 指针，该 issue 上的每条后续评论都会重放同一次溢出。
//
// 每一个本身都是无歧义的证人，这正是 NormalizeDaemonReason 能复用它、把老
// daemon 的 catchall 升级到服务端的原因。匹配目标是已小写化的文本。
// 把这些子串同步进 MUL-1949 离线回填 SQL。
//
// terminal_reason=prompt_too_long 因 GH #6402 加入：它是 Claude Code 在「回合
// 因上下文窗口满了而结束」时放到结果帧上的结构化枚举值，daemon 会把它逐字
// 引述进它上报的错误里（见 pkg/agent/claude.go 的 claudeTerminalReasonFailure）。
// 作为枚举 token 而非散文，它至少和上面两条一样无歧义——没有任何自由文本的
// provider 消息会碰巧产出它——所以由它分类出的 run 会落在 context_overflow，
// 即便 CLI 配套的文字在版本间为空或被改写。
var contextWindowExceededWitnesses = []string{
	"context window limit",
	"model_context_window_exceeded",
	TerminalReasonPromptTooLong,
}

// legacySkillBundlePrefix 是 pre-MUL-5370 daemon 给「skill 包下载失败」套的
// 包装前缀。它是无歧义的证人：没有任何其它代码路径产出过它，而当前 daemon
// 写的是 "skill bundle unavailable: ..."。
const legacySkillBundlePrefix = "resolve skill bundles:"

// legacySkillBundleReasons 是老版本 daemon 自己的分类器可能把这个失败落进的
// 桶。三个都只表示「我们只知道是某种传输故障」：agent_error.unknown 来自
// 早于 deadline 规则的 daemon，agent_error.provider_network 来自已带该规则的
// daemon，pre-MUL-1949 的粗 agent_error 来自更早。它们都不带「升级会丢掉」
// 的信息。
var legacySkillBundleReasons = map[string]bool{
	string(ReasonAgentUnknown):         true,
	string(ReasonAgentProviderNetwork): true,
	"agent_error":                      true,
}

// legacyContextOverflowReasons 是老 daemon 会把「响应侧上下文溢出」落进的桶：
// 它自己的分类器给出的 agent_error.unknown（其规则 1 早于
// contextWindowExceededWitnesses），以及 pre-MUL-1949 的粗 agent_error。
//
// 刻意比 legacySkillBundleReasons 更窄。一个细分原因意味着老 daemon 在同样的
// 文本上已经命中了更早的规则——崩溃标记上的 process_failure、断流上的
// provider_network——那比「同一坨错误里某处出现一个证人词」更说明到底是什么
// 结束了这次 run。把它们升级会丢掉信息；保持原样至多维持既有行为。
var legacyContextOverflowReasons = map[string]bool{
	string(ReasonAgentUnknown): true,
	"agent_error":              true,
}

func isPiProviderNetworkError(lower string) bool {
	for _, message := range []string{"connection error.", "request timed out."} {
		if lower == message ||
			strings.HasPrefix(lower, message+"; pi exited with error: ") ||
			strings.HasPrefix(lower, message+"; omp exited with error: ") {
			return true
		}
	}
	return false
}

// opencodeStreamEndedPrefix 是 OpenCode 终端信号守卫（pkg/agent/opencode.go）
// 抛出的每种失败的开头。产出它的代码路径只有一条，而且它是整条错误的 PREFIX
// 而非嵌在中间的短语，所以它的存在就能直接指认这个失败。
const (
	opencodeStreamEndedPrefix = "opencode stream ended"
	codeartsStreamEndedPrefix = "codearts stream ended"
)

// legacyOpencodeStreamEndedReasons 是早于规则 7 该条目的 daemon 会把这类错误
// 落进的桶：两个 "terminal signal" 变体因其规则 13 偶然匹配 "signal" 而落
// process_failure，它的规则没抓住的落 unknown，以及 pre-MUL-1949 的粗
// agent_error。
//
// 刻意比 legacyContextOverflowReasons 宽，理由在证人身上。那条规则不碰细分的
// 原因，是因为「错误坨里某处出现短语」比「更早规则已选中的桶」信息量小。
// 而这里证人是守卫自己的消息、从第一个字符就是它，所以旧桶不可能是对某个
// 其它、已被更好识别的原因的误述——就是同一个失败，只是贴了张早于我们认清
// 它之前的标签。
var legacyOpencodeStreamEndedReasons = map[string]bool{
	string(ReasonAgentProcessFailure): true,
	string(ReasonAgentUnknown):        true,
	"agent_error":                     true,
}

// NormalizeDaemonReason 把老版本 daemon 上报的 failure_reason 升级到本 server
// 理解的分类体系上，用原始错误文本作证人。没有任何规则适用时原样返回原因。
//
// 为什么存在（MUL-5370）：已安装的 daemon 按自己的节奏升级，所以一个只在
// daemon 侧正确贴标签的修复，要等每台主机都更新才有人受益。daemon 上报了
// 非空 reason，因此 FailTask 的「空则分类」守卫不会触发，server 会把过期标签
// 原样落库——没有自动重试，聊天气泡也回落到通用文案。在 server 一部署的瞬间
// 就识别老 daemon 产出的线形态，可以立刻堵上这个洞。
//
// 这是边界兼容垫片，不是内部兜底逻辑：每条规则一旦等不到还能产出那种线形态
// 的老 daemon 在报，就可以删除。
func NormalizeDaemonReason(reason, rawError string) Reason {
	if legacySkillBundleReasons[reason] &&
		strings.HasPrefix(strings.TrimSpace(rawError), legacySkillBundlePrefix) {
		return ReasonSkillBundleUnavailable
	}
	// GH #6360：同一种混合版本差，但这次等每台主机更新代价更高。一个规则 1
	// 早于 contextWindowExceededWitnesses 的 daemon 上报 catchall，而 catchall
	// 不在任何 resume 黑名单上——于是过满的会话保持被钉为 resume 指针，该
	// issue 的每条后续评论都重放同一次溢出。一台未升级的主机就意味着一个永久
	// 卡死的 (agent, issue) 对，而不只是缺一个标签；在服务端升级这里，会在
	// server 部署的瞬间退役那个会话。
	if legacyContextOverflowReasons[reason] &&
		containsAny(strings.ToLower(rawError), contextWindowExceededWitnesses...) {
		return ReasonAgentContextOverflow
	}
	// #6522：同样的问题又一次出现。规则 7 只决定「由本 server 分类时」落哪，
	// 而本 server 只在 daemon 没给 reason 时才分类。早于该条目的已安装 daemon
	// 会上报非空的 agent_error.process_failure，而 FailTask 里「空 reason」分支
	// 会刻意跳过它——于是在最容易撞上不稳定 provider 的那批未升级主机上，
	// 这次 run 恰恰留在了重试白名单之外。在服务端升级这里，重试在 server 部署
	// 的瞬间就开始生效，不用等整个 daemon 集群。
	lowerError := strings.ToLower(strings.TrimSpace(rawError))
	if legacyOpencodeStreamEndedReasons[reason] &&
		(strings.HasPrefix(lowerError, opencodeStreamEndedPrefix) ||
			strings.HasPrefix(lowerError, codeartsStreamEndedPrefix)) {
		return ReasonAgentProviderNetwork
	}
	return Reason(reason)
}

// containsAny 报告 s 是否包含所给子串中的任意一个。
// 调用方负责提前把 s 小写化，让这个辅助函数在热路径上保持便宜——
// 预先小写化一次，比每次子串扫描内部都做一次大小写折叠要快。
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
