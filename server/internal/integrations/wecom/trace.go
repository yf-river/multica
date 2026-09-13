// trace.go —— 一份可开关的本适配器收发帧逐条记录。
//
// 为什么存在：包里没有任何其他代码会逐帧记日志。失败的才记，而且只记失败——
// 坏信封、非零的服务端 ack。而一次「安静地出错」的运行，服务器上不会留下任何
// 痕迹：把回复发给了群里而不是某个人、命令被悄悄吞掉、回执发了两次。今天想
// 验证这类问题，得靠某个人描述他在手机上看到了什么——又慢又有损，而且凡是涉及
// 顺序、时序的问题根本无法描述。
//
// 打开 MULTICA_WECOM_TRACE=1 之后，服务器会记录足够的细节，事后足以核对一次
// 真机会话：帧往哪个方向走、发给哪个会话、该会话是群还是个人、服务端回了什么。
// 这个开关还额外覆盖帧本身不携带的一类信息——附件自己响应里说它叫什么名字
// （traceMediaHeaders），一旦五分钟的媒体 URL 过期，事后任何检查都无法再还原。
//
// 为什么不用兄弟包用于逐帧行的 slog.Debug（slack_channel.go:162、
// lark/ws_connector.go:339）：logger.parseLevel 把 LOG_LEVEL 默认成 *debug*，
// 所以只要没显式设置 LOG_LEVEL，Debug 调用在每套部署里都是开着的。而消息正文
// 默认绝不能落日志，所以这个开关必须是独立的一个，并且默认关闭。
//
// 非测试会话里，关着才是对的。这里记录的内容包含消息正文的一段有界前缀，因为
// 「到底是哪句文案」光看长度回答不了——所以它是日志里的消息内容，应当故意打开、
// 用完即关。前缀有上限而不是全文，上限按 rune 计是为了中文消息不会被从字符中间
// 截断；每条消息字符串里的 bearer token 都会被抹掉；这里也绝不读凭证字段：机器
// 人密钥藏在 aibot_subscribe 的 body 里，traceOutFields 不会深入它。
//
// 有两类字符串不经过脱敏器，它们都来自附件的 Content-Disposition：header 本身
// 和从里面读出的文件名。traceMediaHeaders 用自己的上限原样记录它们，并解释了
// 为什么脱敏会毁掉这行记录存在的唯一意义，以及为什么那个 header 里没有凭证。
package wecom

import (
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"
)

// tracing 在启动时设置一次，之后每帧都要读它。
var tracing atomic.Bool

// SetTrace 打开或关闭逐帧记录。由服务端接线处用 MULTICA_WECOM_TRACE 调用；
// 返回实际设置的值，方便调用方写日志。
func SetTrace(on bool) bool {
	tracing.Store(on)
	return on
}

// tracingOn 报告当前是否在逐帧记录。
func tracingOn() bool { return tracing.Load() }

// tracePreviewRunes 限制消息正文最多有多少进日志。足够长，能区分两句不同文案、
// 看出是什么语言；又足够短，日志里拼不回一份完整转写。
const tracePreviewRunes = 120

// traceHeaderRunes 限制附件的 Content-Disposition、以及从它解析出的文件名进入
// trace 时的长度。它是套在远端字符串上的防失控护栏，而不是预览上限——这正是它
// 不叫 tracePreviewRunes 的原因。
//
// 用 120 会毁掉这行记录：`attachment; filename=""` 光自己就有 23 个 rune，而
// 一个百分号转义的 CJK 字符占 9 个，所以十一个中文字符的名字就超过预览上限
// 了——而非 ASCII 名字恰恰是这行记录存在的全部理由，因为一个满是转义的名字
// 最容易出错。比丢掉尾巴更糟的是：截断会落在转义中间（"…%E7%89%8…"），半个
// 转义永远无法解码回它代表的字符，所以被截断的行连它本来要回答的问题都答不了。
//
// 2048 是针对「这东西合法时最多能多大」定出来的，不是随手挑的整数。POSIX
// 文件系统把名字限在 255 字节；每个字节百分号转义就变成 765，一个同时携带两种
// 参数形态（filename= 和 filename*=）的 header，算上外壳大约 1570 个 rune。
//
// NTFS 改用 255 个 UTF-16 码元计数，所以 255 个中文字符的名字转义前就有 765
// 字节，双参数形态下可能超出本上限。不过那种名字没有哪个界面能敲得出来、也没
// 有用户真的拥有它；上限是对着「实际到达的东西」定的，不是对着「最长的文件
// 系统能收什么」定的——而一条日志行也不是无界远端字符串该去的地方。
const traceHeaderRunes = 2048

// tracePreview 返回 s 的裁剪过的单行前缀，顺带抹掉所有 bearer token。换行变成
// 空格，保证一帧就是一行日志；截断总落在 rune 边界上。
//
// 脱敏不是可选项。OutboundReplier.sendBindingPrompt 拼出
// "👋 请先绑定你的 Multica 账号，才能与我对话：\n" + appURL + "/wecom/bind?token=" +
// 一截 43 字符的 token；在普通 MULTICA_APP_URL 下，token 的最后一个字符落在
// 第 107~112 个 rune——就在上限以内。没有这层脱敏，为调试会话打开 trace 就会
// 把活体绑定凭证完整写进日志。绑定 token 是 bearer 凭证（RedeemAndBind 只校验
// 兑换者属于 token 所在工作区，而绑定页在加载时就会以当前登录者身份去兑换），
// 所以能读日志的人就能抢在用户自己点链接之前，把发送者的企微身份绑到自己名下
// ——正是 replier.go:150 通过拒绝把链接发进群来防范的同一类劫持。
//
// 匹配的是查询参数而不是绑定路径，因为这是包级函数，够不到配置出来的路径
// （路径归 OutboundReplier 所有，且 BindingPath 可配置）。URL 里的 "token="
// 无论出现在哪，都是值得藏起来的样子。
func tracePreview(s string) string {
	return traceBound(redactBearerTokens(s), tracePreviewRunes)
}

// traceBound 是 tracePreview 的下半截单拎出来：单行拍平 + 上限，但不做脱敏。
// 上限做成参数，因为它的两个调用方出于不同理由在限定不同的东西——消息预览是
// 刻意要短（tracePreviewRunes），附件 header 只是为了防止远端字符串失控才加
// 界（traceHeaderRunes）。traceMediaHeaders 是唯一跳过脱敏的调用方，它在那里
// 解释了原因。
func traceBound(s string, limit int) string {
	out := make([]rune, 0, limit)
	cut := false
	for _, r := range s {
		if len(out) == limit {
			cut = true
			break
		}
		if r == '\n' || r == '\r' {
			r = ' '
		}
		out = append(out, r)
	}
	if cut {
		return string(out) + "…"
	}
	return string(out)
}

// 一条出站帧会被记录两次：作为一次尝试和它的结局，两者共享同一个 seq——
// 即将上线时记 "dir=out"，SetWriteDeadline 和 WriteMessage 各自给出结论后再记
// "dir=out.done"，带 ok=true/false。
//
// 一行做不到。trace 存在的意义是回答「这帧到底上线了没有」，而在写之前写下的一
// 行无法回答它——被拒的帧和送达的帧看起来一模一样。写完之后再落一行，又只对
// 「能正常返回的写」有效：写挂了、或进程写到一半被杀掉，就什么都不剩——这正是
// 这个开关要消除的那种静默失败。两行日志让读者按 seq 配对，把「只有尝试没有
// 结局」的帧一眼认出来。
//
// 成对记录也正是 seq 存在的原因，而不是只按 req_id 配对：pong 会回显服务端的
// req_id，它可能为空、可能重复，所以 req_id 不能当键。seq 在 writer 互斥锁内
// 分配、从不上线，它代表的是这帧在线上顺序里的位置。

// 一次写操作的各个阶段。失败结局里会点名阶段，好区分「帧被服务端拒了」和
// 「连 deadline 都没能设上」这两种情况。
const (
	traceStageDeadline = "set_write_deadline"
	traceStageWrite    = "write_message"
)

// outTrace 是 trace 在「抽出某条出站帧字段」与「发出它的两行日志」之间暂存的
// 内容。outTrace 为 nil 表示这帧没在追踪；两行日志都受同一次决策管辖，所以
// 日志里绝不可能出现「写进行到一半开关被翻转、把结局吞掉」的孤零零尝试。
type outTrace struct {
	attrs []any
	cmd   string
	reqID string
}

// traceOutFields 抽取 trace 要记录的一帧发往企业微信前的信息。wsSender.write
// 在取 writer 互斥锁之前就调用它：这里才是开销大的一半——对消息正文做一遍
// 正则脱敏 + 逐 rune 裁剪——而这一切都不需要与 socket 串行化。需要串行的只有
// 最终落日志，那是 traceOutAttempt 的活。
//
// 它只读命名的字段，而不是把整帧倒出来：aibot_subscribe 的 body 带着智能机器人
// 密钥，整体倒出等于把它写进日志。在这里加字段前，先检查每种 cmd 会往该字段下
// 放什么东西。
func traceOutFields(log *slog.Logger, frame map[string]any) *outTrace {
	if !tracingOn() || log == nil {
		return nil
	}
	t := &outTrace{}
	if cmd, ok := frame["cmd"].(string); ok {
		t.cmd = cmd
		t.attrs = append(t.attrs, "cmd", cmd)
	}
	if h, ok := frame["headers"].(frameHeaders); ok && h.ReqID != "" {
		t.reqID = h.ReqID
		t.attrs = append(t.attrs, "req_id", h.ReqID)
	}
	if body, ok := frame["body"].(map[string]any); ok {
		if v, ok := body["chatid"].(string); ok {
			t.attrs = append(t.attrs, "chatid", v)
		}
		if v, ok := body["chat_type"].(int); ok {
			t.attrs = append(t.attrs, "chat_type", v)
		}
		if v, ok := body["msgtype"].(string); ok {
			t.attrs = append(t.attrs, "msgtype", v)
		}
		if md, ok := body["markdown"].(map[string]string); ok {
			t.attrs = append(t.attrs, "len", len(md["content"]), "text", tracePreview(md["content"]))
		}
	}
	return t
}

// traceOutAttempt 在一帧即将被写出那一刻记录它。wsSender.write 在 writer
// 互斥锁内调用它——正是在这里，并发的发送方（ping 循环、agent 回复、收件箱
// 推送）才变成有序的。所以这些日志行的顺序就是帧到达 WriteMessage 的顺序：
// 由构造保证，而不是靠事后关联。
func traceOutAttempt(log *slog.Logger, seq uint64, t *outTrace) {
	if t == nil {
		return
	}
	attrs := make([]any, 0, len(t.attrs)+4)
	attrs = append(attrs, "dir", "out", "seq", seq)
	attrs = append(attrs, t.attrs...)
	log.Info("wecom trace", attrs...)
}

// traceOutResult 记录携带同一 seq 的那次尝试最终怎么了。同样在 writer 互斥锁
// 内，保证一次尝试和它的结局不会被别的 goroutine 的帧插进来拆开。
//
// 错误文本走一遍 tracePreview：那是 socket 的原话，不是我们的，所以要像这里
// 其他消息字符串一样裁剪 + 脱敏。本文件里「有界但不脱敏」的字符串是附件的
// Content-Disposition 和从它读出的文件名，原因在 traceMediaHeaders 里说明。
func traceOutResult(log *slog.Logger, seq uint64, t *outTrace, stage string, err error) {
	if t == nil {
		return
	}
	attrs := make([]any, 0, 12)
	attrs = append(attrs, "dir", "out.done", "seq", seq)
	if t.cmd != "" {
		attrs = append(attrs, "cmd", t.cmd)
	}
	if t.reqID != "" {
		attrs = append(attrs, "req_id", t.reqID)
	}
	if err != nil {
		attrs = append(attrs, "ok", false, "stage", stage, "error", tracePreview(err.Error()))
	} else {
		attrs = append(attrs, "ok", true)
	}
	log.Info("wecom trace", attrs...)
}

// traceIn 记录一条来自企业微信的帧，包括服务端对我们发出内容的裁定——errcode
// 正是让静默失败显形的地方。dispatchFrame 只在匿名 ack 场景对非零 errcode 打
// warning，所以没有它的话，被拒绝的 aibot_send_msg 就是唯一会被看见的拒绝。
func traceIn(log *slog.Logger, env frameEnvelope) {
	if !tracingOn() || log == nil {
		return
	}
	log.Info("wecom trace",
		"dir", "in",
		"cmd", env.Cmd,
		"req_id", env.Headers.ReqID,
		"errcode", env.ErrCode,
		"errmsg", tracePreview(env.ErrMsg),
	)
}

// traceInbound 记录一条已解码的用户消息：适配器认为「谁发的、落在哪」，这正是
// 最容易搞混的那一对（一个字段装群 id、另一个装个人 id）。光靠 traceIn 看不
// 出来——那一刻回调 body 还是原始 JSON。
func traceInbound(log *slog.Logger, mc aibotMsgCallback, text string) {
	if !tracingOn() || log == nil {
		return
	}
	log.Info("wecom trace",
		"dir", "in.msg",
		"msg_id", mc.MsgID,
		"chatid", mc.ChatID,
		"chat_type", mc.ChatType,
		"sender", mc.From.UserID,
		"msgtype", mc.MsgType,
		"len", len(text),
		"text", tracePreview(text),
	)
}

// traceMediaHeaders 记录附件响应里关于它自己的说法：原样到达的
// Content-Disposition，以及本包从里面解析出的名字。两者并排就是「文件名看起来
// 不对」时的全部诊断材料，而且事后无法再取回——它们来自的 URL 只有五分钟寿命，
// 所以一个明天被质疑文件名的附件，永远不可能重新抓来对质。
//
// 两个值都会被拍平成一行、以 traceHeaderRunes 为上限，而且都不会像其他被追踪
// 的字符串那样过 redactBearerTokens。两处刻意的例外，出于同一个理由：这一行的
// 价值就在于「精确的原始字节」。
//
// 脱敏器会改写任何形如 "token=…" 的内容，而一个文件完全可能合法地叫这名字——
// 被脱敏的文件名，就是没人能诊断的文件名。何况这里本来就没有凭证：header 装的
// 是一个名字，而真正带凭证的预签名 URL 刻意不出现在这一行、也不出现在
// media_download.go 发出的任何其他地方。
//
// 上限用 traceHeaderRunes 而不是消息预览的上限，是因为一个百分号转义的名字每
// 个中文字符要花九个 rune；预览上限会把普通文件名截成两半、还留下一截残转义。
// traceHeaderRunes 已说明它按什么来定尺寸。
//
// 即使 header 不存在也会触发记录，只是记一个空值。如果某次运行什么行都没产生，
// 读者就无法区分「服务端没发 Content-Disposition」和「开关没开」。
func traceMediaHeaders(log *slog.Logger, msgID string, index int, h mediaHeaders) {
	if !tracingOn() || log == nil {
		return
	}
	log.Info("wecom trace",
		"dir", "in.media",
		"msg_id", msgID,
		"index", index,
		"content_disposition", traceBound(h.Disposition, traceHeaderRunes),
		"filename", traceBound(h.Filename, traceHeaderRunes),
	)
}

// tokenParamPattern 找出作为查询参数携带的 token，不管它挂在什么路径上。
// 刻意放宽：access_token 或 code 出现在日志里，并不比绑定 token 更安全。
var tokenParamPattern = regexp.MustCompile(`(?i)\b((?:binding_token|access_token|token|code)=)([^\s&"'<>]+)`)

// redactBearerTokens 把任何形如 token 的查询参数值替换成固定标记，同时保留
// 足够多的行内容让它仍然值得记。
func redactBearerTokens(s string) string {
	if !strings.Contains(s, "=") {
		return s
	}
	return tokenParamPattern.ReplaceAllString(s, "${1}[redacted]")
}
