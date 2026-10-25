// errors.go 定义 cli（multica 命令行客户端）的「用户可读错误」体系：把网络层、
// HTTP 状态层五花八门的底层错误归类成有限的 ErrorKind，再映射成友好的中/英文提示
// 与分层的进程退出码。CLI 内部那些错误串（"resolve issue: ..."、裸的 net/http 报错、
// JSON 响应体）对终端用户没有意义，FormatError 是把它们翻译成人话的唯一入口，
// 在 main 顶层对冒出来的错误调用一次。
package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
)

// ErrorKind 是面向用户的错误粗分类。CLI 内部错误串太多太杂，FormatError 会先把它们
// 归到下面某一类，再渲染成友好的（本地化的）文案。
//
// 注意：零值并不是某种「默认网络错误」，一定要显式分类，不要依赖零值兜底。
type ErrorKind int

const (
	// 网络 / 传输层（http.Client.Do 返回的那些错误）。
	KindNetworkTimeout ErrorKind = iota // 报错文案含 context deadline exceeded / i/o timeout
	KindNetworkDNS                      // no such host（域名解析不到）
	KindNetworkRefused                  // connection refused（连接被拒）
	KindNetworkTLS                      // x509 / tls 握手失败
	KindNetworkOffline                  // 兜底：主机不可达、连接被重置等
	// KindNetworkStalled 表示「传输中途停止产出字节」（见 StallError）。它和
	// KindNetworkTimeout 的治疗方案不同，所以要区分：timeout 的意思是「太慢了」，
	// stall 的意思是「没动静了」——只有后者不受「调高时限」影响。
	KindNetworkStalled
	// KindNetworkTLSHandshakeTimeout is a TLS handshake that never completed
	// after the TCP connection opened. Distinct from KindNetworkTimeout
	// because the remedy is different: the request budget
	// (MULTICA_HTTP_TIMEOUT) does not govern the handshake, and the usual
	// cause is a network path that drops a ClientHello spanning two TCP
	// packets — which every Go client sends by default since Go 1.24 (the
	// post-quantum key share makes it ~1.5 KB) while curl on the same machine
	// gets through (GH #8654).
	KindNetworkTLSHandshakeTimeout

	// HTTP 状态码层。
	KindAuthRequired // 401
	// KindTaskTokenRejected 是「task 专用令牌」收到的 401。HTTPError.Kind() 永远
	// 不会返回它——单看状态码区分不了普通 401 与 task token 401——所以它由
	// userMessage 里在「知道请求实际用的是哪种凭证」的地方挑出来。退出码归类不变：
	// 仍然算认证失败。
	KindTaskTokenRejected
	KindForbidden   // 403
	KindNotFound    // 404
	KindConflict    // 409
	KindValidation  // 400 / 422
	KindRateLimited // 429
	KindServerError // 5xx

	// 无法归入上述任何一类的错误。
	KindUnknown
)

// 分层的进程退出码。保持稳定，这样脚本可以拿它分支处理。
const (
	ExitGeneric    = 1 // 下面没有覆盖到的任何错误
	ExitNetwork    = 2 // 任意 KindNetwork*
	ExitAuth       = 3 // 401 / 403
	ExitNotFound   = 4 // 404
	ExitValidation = 5 // 400 / 422
)

// IsNetwork 报告该类别是否属于传输层失败。
func (k ErrorKind) IsNetwork() bool {
	switch k {
	case KindNetworkTimeout, KindNetworkDNS, KindNetworkRefused, KindNetworkTLS, KindNetworkOffline, KindNetworkStalled, KindNetworkTLSHandshakeTimeout:
		return true
	default:
		return false
	}
}

// String 返回该类别稳定、snake_case 的标识。它用于 --debug 输出，可以安全地打日志
// 或做分支判断；但不是给人看的文案（那部分见 kindMessages / messageFor）。
func (k ErrorKind) String() string {
	switch k {
	case KindNetworkTimeout:
		return "network_timeout"
	case KindNetworkDNS:
		return "network_dns"
	case KindNetworkRefused:
		return "network_refused"
	case KindNetworkTLS:
		return "network_tls"
	case KindNetworkOffline:
		return "network_offline"
	case KindNetworkStalled:
		return "network_stalled"
	case KindNetworkTLSHandshakeTimeout:
		return "network_tls_handshake_timeout"
	case KindAuthRequired:
		return "auth_required"
	case KindTaskTokenRejected:
		return "task_token_rejected"
	case KindForbidden:
		return "forbidden"
	case KindNotFound:
		return "not_found"
	case KindConflict:
		return "conflict"
	case KindValidation:
		return "validation"
	case KindRateLimited:
		return "rate_limited"
	case KindServerError:
		return "server_error"
	case KindUnknown:
		return "unknown"
	default:
		return fmt.Sprintf("ErrorKind(%d)", int(k))
	}
}

// NetworkError 包装传输层错误（http.Client.Do 在拿到任何 HTTP 状态码之前返回的
// 错误）。它把原始 URL 从面向用户的文案里剥掉，同时保留原始错误给 --debug 输出和
// errors.Is/As 检查用。
type NetworkError struct {
	Kind ErrorKind
	Op   string // 例如 "GET /api/issues/abc"——只在 --debug 里显示
	Err  error  // 原始的 net/http 错误
}

// Error 拼出错误文本：带 Op 时前缀操作说明，否则直接透传底层错误文本。
func (e *NetworkError) Error() string {
	if e.Op != "" {
		return fmt.Sprintf("%s: %s", e.Op, e.Err.Error())
	}
	return e.Err.Error()
}

// Unwrap 支持 errors.Is/As 穿透到原始错误。
func (e *NetworkError) Unwrap() error { return e.Err }

// UserMessageError 给底层错误附加一条「命令专用、面向用户」的消息。FormatError
// 会优先原样展示 Msg（而不是根据被包装的 *NetworkError / *HTTPError 推导出来的
// 泛型文案），这样命令级提示——例如 `multica login` 失败时比通用的 401/超时提示
// 更有用的那句话——能在默认（非 --debug）输出里显示出来。
//
// 它保留了 Unwrap()，因此 ExitCodeFor 仍按底层类型化错误分类，--debug 也仍能打印
// 完整的原始错误链。
type UserMessageError struct {
	Msg string
	Err error
}

func (e *UserMessageError) Error() string {
	if e.Err != nil {
		return e.Msg + ": " + e.Err.Error()
	}
	return e.Msg
}

func (e *UserMessageError) Unwrap() error { return e.Err }

// WithUserMessage 给 err 包一层 FormatError 默认会展示的用户文案。err 为 nil 时也
// 返回 nil，因此可以直接内联写在 return 里而不必先判空。
func WithUserMessage(msg string, err error) error {
	if err == nil {
		return nil
	}
	return &UserMessageError{Msg: msg, Err: err}
}

// WithUserMessageUnlessNetwork is WithUserMessage for a command whose custom
// copy explains an HTTP-level refusal — a rejected token, a server that would
// not issue one — and would be wrong for a transport failure. When err is a
// *NetworkError it is returned unchanged, so FormatError renders the
// kind-based copy, which is the only place that names the actual remedy
// (DNS, proxy, TLS handshake).
//
// `multica login` needs this: with WithUserMessage, a TLS handshake that
// never completed was reported as "the server could not issue an access
// token" and "make sure the token is valid and not expired", and nothing in
// the default output pointed at the network (GH #8654).
func WithUserMessageUnlessNetwork(msg string, err error) error {
	if err == nil {
		return nil
	}
	var netErr *NetworkError
	if errors.As(err, &netErr) {
		return err
	}
	return &UserMessageError{Msg: msg, Err: err}
}

// Kind maps an HTTPError's status code onto an ErrorKind.
func (e *HTTPError) Kind() ErrorKind {
	switch e.StatusCode {
	case 401:
		return KindAuthRequired
	case 403:
		return KindForbidden
	case 404:
		return KindNotFound
	case 409:
		return KindConflict
	case 400, 422:
		return KindValidation
	case 429:
		return KindRateLimited
	default:
		if e.StatusCode >= 500 {
			return KindServerError
		}
		return KindUnknown
	}
}

// classifyNetworkError 检查一个传输层错误并返回匹配的网络类 ErrorKind。优先做类型
// 化检查（errors.As / errors.Is），标准库没有暴露成独立类型的情况再退到字符串匹配。
func classifyNetworkError(err error) ErrorKind {
	if err == nil {
		return KindUnknown
	}

	// 卡死(停滞)要先查：它的实现是取消请求 context，不先查的话底层错误会读成普通的
	// 「请求被取消」，真实原因就丢了。
	var stalled *StallError
	if errors.As(err, &stalled) {
		return KindNetworkStalled
	}

	// 超时（context deadline 或 socket 的 i/o timeout）。
	if errors.Is(err, context.DeadlineExceeded) {
		return KindNetworkTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		// net/http reports a TLS handshake that never completed with an
		// unexported type that also satisfies Timeout(), so only its message
		// tells it apart from a socket timeout. The remedy differs (see
		// KindNetworkTLSHandshakeTimeout), so it must not be folded into the
		// generic timeout. Error() is only consulted here, on a timeout: the
		// typed x509 checks below must keep running before any message is
		// rendered, because a hostname error renders its certificate.
		if isTLSHandshakeTimeout(err) {
			return KindNetworkTLSHandshakeTimeout
		}
		return KindNetworkTimeout
	}

	// DNS 解析失败。
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return KindNetworkDNS
	}

	// TLS / 证书类失败。
	var certVerifyErr *tls.CertificateVerificationError
	if errors.As(err, &certVerifyErr) {
		return KindNetworkTLS
	}
	var unknownAuthorityErr x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthorityErr) {
		return KindNetworkTLS
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return KindNetworkTLS
	}
	var certInvalidErr x509.CertificateInvalidError
	if errors.As(err, &certInvalidErr) {
		return KindNetworkTLS
	}

	// 连接被拒。
	if errors.Is(err, syscall.ECONNREFUSED) {
		return KindNetworkRefused
	}

	// 字符串兜底：处理标准库没给独立类型的那些情况。
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "tls handshake timeout"):
		return KindNetworkTLSHandshakeTimeout
	case strings.Contains(msg, "context deadline exceeded"), strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"):
		return KindNetworkTimeout
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "server misbehaving"), strings.Contains(msg, "name resolution"):
		return KindNetworkDNS
	case strings.Contains(msg, "connection refused"):
		return KindNetworkRefused
	case strings.Contains(msg, "x509"), strings.Contains(msg, "certificate"), strings.Contains(msg, "tls"):
		return KindNetworkTLS
	}
	return KindNetworkOffline
}

// isTLSHandshakeTimeout reports whether err is net/http's TLS handshake
// timeout, which is only identifiable by its message.
func isTLSHandshakeTimeout(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "tls handshake timeout")
}

// wrapTransport converts a raw transport error returned by http.Client.Do
// into a *NetworkError. It returns nil when err is nil so call sites can
// reassign unconditionally:
//
//	resp, err := c.HTTPClient.Do(req)
//	err = wrapTransport(req, err)
//	if err != nil { return err }
func wrapTransport(req *http.Request, err error) error {
	if err == nil {
		return nil
	}
	op := ""
	if req != nil && req.URL != nil {
		op = req.Method + " " + req.URL.Path
	}
	return &NetworkError{Kind: classifyNetworkError(err), Op: op, Err: err}
}

// wrapBodyRead 对「读/解码响应体」阶段抛出的错误做分类。
//
// 背景：wrapTransport 只能看到 http.Client.Do 的错误，而 Do 在响应头一到达就返回了。
// 之后的一切问题——也就是 GH #7498 报告的真实场景：body 永远传不完——是从 JSON
// 解码器里冒出来的，过去会以一行裸的 "context deadline exceeded ... while reading
// body" 砸到用户脸上。解码器先发现「传输失败」，那它本质上还是传输失败；真正的
// 畸形 JSON 错误则原样返回。
//
// io.ErrUnexpectedEOF 被有意划在网络这一侧：解码器遇到 body 戛然而止（连接被掐、
// 代理切断响应）时报的就是它，而这远比「服务器发出句法上截断的 JSON」常见。格式
// 正确但内容不对（普通服务器 bug）抛 *json.SyntaxError，保持原样不管。
func wrapBodyRead(req *http.Request, err error) error {
	if err == nil {
		return nil
	}
	var stalled *StallError
	if errors.As(err, &stalled) {
		return wrapTransport(req, err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return wrapTransport(req, err)
	}
	return err
}

// Language 是 FormatError 渲染文案用的语言。
type Language int

const (
	LangEN Language = iota
	LangZH
)

// DetectLanguage 从环境变量里选出输出语言。默认英文（与 CLI 帮助文本一致）；
// 按 LC_ALL、LC_MESSAGES、LANG 的优先级顺序（locale 变量）出现中文前缀即切中文。
func DetectLanguage() Language {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
		if v == "" {
			continue
		}
		if strings.HasPrefix(v, "zh") {
			return LangZH
		}
		// 优先级最高的已设置的 locale 变量说了算；它不是中文就直接落到英文，
		// 不再往下看低优先级的变量。
		return LangEN
	}
	return LangEN
}

// kindMessages 存各类别对应的 {英文, 中文} 用户文案。
var kindMessages = map[ErrorKind][2]string{
	KindNetworkTimeout: {
		"Request timed out: the server did not respond in time. Check your network connection or try again later. You can raise the limit with MULTICA_HTTP_TIMEOUT.",
		"请求超时：服务器未在规定时间内响应。请检查网络连接或稍后重试。可通过 MULTICA_HTTP_TIMEOUT 调高超时时间。",
	},
	KindNetworkTLSHandshakeTimeout: {
		"TLS handshake timed out: the connection to the Multica server opened, but the secure handshake never completed. Something on this network path (security software, a VPN, a router, or a firewall) is probably dropping large TLS handshakes; curl or a browser on the same machine may still work. Retry with the environment variable GODEBUG=tlsmlkem=0 set, and keep it set for the CLI and the daemon if that fixes it. MULTICA_HTTP_TIMEOUT does not affect the handshake.",
		"TLS 握手超时：已连上 Multica 服务器，但安全握手一直没有完成。通常是网络路径上的安全软件、VPN、路由器或防火墙丢弃了较大的 TLS 握手包，同一台机器上的 curl 或浏览器可能仍然正常。请设置环境变量 GODEBUG=tlsmlkem=0 后重试；若因此恢复，请为 CLI 和守护进程长期保留该设置。MULTICA_HTTP_TIMEOUT 对握手无效。",
	},
	KindNetworkStalled: {
		"Transfer stalled: the connection stopped sending data before the response was complete. Check your network connection or try again. You can raise the no-progress budget with MULTICA_HTTP_STALL_TIMEOUT.",
		"传输中断：响应尚未接收完毕，连接就停止发送数据。请检查网络连接或重试。可通过 MULTICA_HTTP_STALL_TIMEOUT 调高无进展等待时间。",
	},
	KindNetworkDNS: {
		"Could not resolve the Multica server address. Check your network connection or the --server-url setting.",
		"无法解析 Multica 服务器地址。请检查网络连接或 --server-url 配置。",
	},
	KindNetworkRefused: {
		"Could not connect to the Multica server. Make sure the server address is correct and reachable.",
		"无法连接到 Multica 服务器。请确认服务器地址正确且网络可达。",
	},
	KindNetworkTLS: {
		"Could not establish a secure connection to the Multica server (TLS/certificate error). Check your system clock and CA certificates.",
		"无法与 Multica 服务器建立安全连接（TLS/证书错误）。请检查系统时间和 CA 证书。",
	},
	KindNetworkOffline: {
		"Could not reach the Multica server. Check your network connection.",
		"无法访问 Multica 服务器。请检查网络连接。",
	},
	KindAuthRequired: {
		"Your session has expired or you are not signed in. Run `multica login` to sign in again. On a self-hosted or non-OAuth setup, ask your administrator for valid credentials.",
		"登录已过期或尚未登录。请运行 `multica login` 重新登录。自托管或非 OAuth 场景请联系管理员获取有效凭证。",
	},
	KindTaskTokenRejected: {
		"This task token was rejected and is no longer usable. Stop here: do not retry, and do not fall back to a profile or member credential, because anything done with one would run as that person rather than as this task. Only the runtime that started this task can supply a valid task token.",
		"这个 task token 已被拒绝，不再可用。请到此为止：不要重试，也不要改用 profile 或成员凭证 —— 用它们执行的任何操作都会以那个成员的身份运行，而不是以这次 task 的身份运行。只有启动这次 task 的运行时才能提供有效的 task token。",
	},
	KindForbidden: {
		"You do not have permission to access this resource. Check that you are in the right workspace, or ask an administrator to grant access.",
		"无权访问该资源。请确认当前 workspace 是否正确，或联系管理员授予权限。",
	},
	KindNotFound: {
		"The requested resource was not found. Check the ID, or run the matching `list` command to see what exists in this workspace.",
		"未找到请求的资源。请核对 ID，或运行对应的 list 命令查看当前 workspace 中已有的内容。",
	},
	KindConflict: {
		"The request conflicts with the current state of the resource (it may already exist or have changed since you last fetched it). Re-fetch the latest state and try again.",
		"请求与资源的当前状态冲突（可能已存在，或自上次获取后已被修改）。请重新获取最新状态后再试。",
	},
	KindValidation: {
		"The request was invalid. Check the values you provided; run the command with --help to see the expected format.",
		"请求无效。请检查所填写的参数；可用 --help 查看期望的格式。",
	},
	KindRateLimited: {
		"Too many requests. Please wait a moment and try again; if this keeps happening, reduce how frequently you call the API.",
		"请求过于频繁。请稍候重试；若持续出现，请降低 API 调用频率。",
	},
	KindServerError: {
		"The Multica service is temporarily unavailable (server error). Please try again later; if it persists, contact support. Re-run with --debug to see the raw server response.",
		"Multica 服务暂时不可用（服务器错误）。请稍后重试；若持续出现请联系支持。可加 --debug 查看服务器原始响应。",
	},
	KindUnknown: {
		"An unexpected error occurred.",
		"发生未知错误。",
	},
}

// serverMessagePrefixes 列出那些「响应体里自带手写、可操作提示（值得优先于泛型文案
// 展示）」的类别，并记录引入这些服务端消息时用的 {英文, 中文} 引导语。
//
// 409 之所以在这里：泛型冲突文案不仅含糊，还把方向指反了——它读起来像「瞬时竞态，
// 重新拉取最新状态再试」，而这套 API 返回的每个冲突其实都是「确定性的拒绝」，并
// 且都自带修法（"a skill with this name already exists"、"set parent_id (--parent)
// to <id>"）。智能体曾把「重试」提示当真，白白花几小时重发一模一样的请求
// （GH #6264、GH #5948），而 MUL-4417 加的服务端措辞从未到达过它们。
var serverMessagePrefixes = map[ErrorKind][2]string{
	KindValidation: {"Invalid request: ", "请求无效："},
	KindConflict:   {"Request conflict: ", "请求冲突："},
}

// messageFor 返回某类别在当前语言下的文案。
func messageFor(kind ErrorKind, lang Language) string {
	m, ok := kindMessages[kind]
	if !ok {
		m = kindMessages[KindUnknown]
	}
	if lang == LangZH {
		return m[1]
	}
	return m[0]
}

// FormatError 把错误翻译成一行面向用户的文案（debug 打开时则是多行详情块）。它是
// 唯一面向用户的翻译入口，约定只调用一次、在顶层（main.go）对命令冒出来的错误调用。
//
// debug 为 false 时，跳过内部的操作链（"resolve issue: ..."）和原始 URL/JSON 响应体，
// 只显示友好文案。debug 为 true（或设了 MULTICA_DEBUG）时，额外打印完整原始错误链
// 供排障。
func FormatError(err error, debug bool) string {
	if err == nil {
		return ""
	}
	lang := DetectLanguage()
	base := userMessage(err, lang)
	if debug || debugEnabled() {
		return base + "\n\n" + debugDetail(err)
	}
	return base
}

// userMessage 针对 err 的「根因」产出一条友好文案。
func userMessage(err error, lang Language) string {
	// 命令自己提供的用户文案优先于泛型类别文案，这样命令级指引（例如登录失败）默认
	// 就能看到。Unwrap() 被保留，ExitCodeFor 和 --debug 仍能看见底层的类型化错误。
	var um *UserMessageError
	if errors.As(err, &um) {
		return um.Msg
	}

	// 传输层失败。
	var netErr *NetworkError
	if errors.As(err, &netErr) {
		return messageFor(netErr.Kind, lang)
	}

	// HTTP 状态码失败。
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		kind := httpErr.Kind()
		// task token 收到 401 不是登录问题，下面的泛型文案对读它的人是错误指引：它叫
		// 人重新登录或找管理员要凭证。自主运行的智能体真的会照着做——曾经就有一次，
		// task token 在运行中途失效后，它读走了 daemon 主人的 profile PAT，以成员身份
		// 继续跑（GH #7522）。
		//
		// 文案绝不能去猜「为什么」。常见原因是 task 进入终态，但同一个 401 也涵盖
		// 畸形 token、发错服务器、被无关清理逻辑删掉等。所有情况里「停止」都是对的；
		// 「task 跑完了」则未必。
		if kind == KindAuthRequired && httpErr.TaskScoped {
			return messageFor(KindTaskTokenRejected, lang)
		}
		// validation 和 conflict 错误带一句服务端写好的有用提示，优先展示它而不是
		// 泛型文案。认不出的响应体仍会落回模板，所以绝不会把裸响应倒给用户。
		if prefix, ok := serverMessagePrefixes[kind]; ok {
			if serverMsg := extractServerMessage(httpErr.Body); serverMsg != "" {
				if lang == LangZH {
					return prefix[1] + serverMsg
				}
				return prefix[0] + serverMsg
			}
		}
		return messageFor(kind, lang)
	}

	// 不是认识的结构化错误：通常是本地/业务错误，其文案本就是给用户看的（例如缺参数、
	// 命令里拼出来的校验信息）。原样展示。
	return strings.TrimSpace(err.Error())
}

// extractServerMessage 尝试从形如 {"error":"..."} 或 {"message":"..."} 的 JSON 错误体
// 里抠出一条人能读的消息。body 不是 JSON 或没有可识别的消息字段时返回 ""。
//
// 少数端点把稳定的机器码放在 "error"、把说明文字放在 "message"（issue-table 游标
// 响应就是这样）。散文优先；只有实在没有句子可展示时才退而保留裸代码，至少让用户
// 有个能 grep 的东西。
func extractServerMessage(body string) string {
	body = strings.TrimSpace(body)
	if body == "" || body[0] != '{' {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ""
	}
	var code string
	for _, key := range []string{"error", "message", "detail", "title"} {
		v, ok := parsed[key]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if looksLikeMachineCode(s) {
			if code == "" {
				code = s
			}
			continue
		}
		return s
	}
	return code
}

// looksLikeMachineCode 判断 s 是否是「裸标识符」（如 "cursor_query_mismatch"）而不是
// 写给人的句子。判定刻意做得很窄——只允许小写字母数字下划线等——因此任何语言的普通
// 散文（包括没有 ASCII 空格的汉语）都不会被误判成机器码。
func looksLikeMachineCode(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// debugDetail 把完整的原始错误链以及结构化错误里的细节拼出来，供 --debug /
// MULTICA_DEBUG 输出。
func debugDetail(err error) string {
	var sb strings.Builder
	sb.WriteString("[debug] ")
	sb.WriteString(err.Error())

	var netErr *NetworkError
	if errors.As(err, &netErr) {
		fmt.Fprintf(&sb, "\n[debug] network: op=%q kind=%s cause=%v", netErr.Op, netErr.Kind, netErr.Err)
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		fmt.Fprintf(&sb, "\n[debug] http: %s %s status=%d body=%s",
			httpErr.Method, httpErr.Path, httpErr.StatusCode, strings.TrimSpace(httpErr.Body))
	}
	return sb.String()
}

// debugEnabled 报告环境变量 MULTICA_DEBUG 是否请求调试输出。
func debugEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MULTICA_DEBUG"))) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// ExitCodeFor 把错误映射成分层的进程退出码，方便脚本分支：网络=2、认证(401/403)=3、
// 未找到(404)=4、校验失败(400/422)=5、其余=1。
func ExitCodeFor(err error) int {
	if err == nil {
		return 0
	}

	var netErr *NetworkError
	if errors.As(err, &netErr) {
		return ExitNetwork
	}

	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Kind() {
		case KindAuthRequired, KindForbidden:
			return ExitAuth
		case KindNotFound:
			return ExitNotFound
		case KindValidation:
			return ExitValidation
		default:
			return ExitGeneric
		}
	}

	return ExitGeneric
}
