package agent

// 文件职责：捕获 agent CLI 的 stderr 有界尾部，在它"没来得及发结构化错误就退出"
// （例如 Windows 上的 V8 abort、Bun panic、OOM）时把这段上下文带进错误消息。
//
// 凡是监护子 CLI 进程的 backend，都应该把 cmd.Stderr 接进 stderrTail，并在失败时
// 经 withAgentStderr 把 Tail() 拼进 Result.Error。这样不用去翻 daemon 宿主机的日志
// 就能定位 CLI 崩溃的根因。写入仍原样转发给内层 writer（通常是 daemon 日志）。
// 落库前会先过 sanitizeAgentDiagnostic：去掉终端控制字符、把常见的密钥形状涂黑、
// 修正非法 UTF-8，防止诊断内容污染任务行或泄露凭据。

import (
	"io"
	"regexp"
	"strings"
	"sync"

	"github.com/multica-ai/multica/server/pkg/redact"
)

// agentStderrTailBytes 限制被捕获进错误消息的 stderr 尾部长度。要够装下典型的
// CLI 错误行，又不能大到塞爆任务级 Result.Error 字符串。
const agentStderrTailBytes = 2048

var (
	// agentAuthorizationHeaderRe 匹配 "authorization: <值>" 请求头形态（多行模式）。
	agentAuthorizationHeaderRe = regexp.MustCompile(`(?im)(authorization\s*:\s*)[^\r\n]+`)
	// agentJSONSecretRe 匹配 JSON 里形如 "token"/"auth"/"api_key"/"secret"/"password"
	// 等键的字符串值。
	agentJSONSecretRe = regexp.MustCompile(`(?i)("(?:token|auth|authorization|api[_-]?key|secret|password)"\s*:\s*)"(?:\\.|[^"\\])*"`)
	// agentDiagnosticSecretRe 匹配日志里 "key: value" / "key=value" 形态的密钥对。
	agentDiagnosticSecretRe = regexp.MustCompile(`(?i)(authorization|auth|api[_-]?key|token|secret|password)(\s*[:=]\s*)([^\s,;]+)`)
)

// sanitizeAgentDiagnostic 在子进程诊断信息落进 Result.Error 之前，去掉终端控制
// 字符并涂黑常见密钥形态。stderr 仍会转发到本地 daemon 日志；本函数保护的是任务
// 行与用户可见的失败评论。本地路径原样保留——它们是让崩溃诊断可操作的东西，
// 藏路径从来不是要做的边界。
func sanitizeAgentDiagnostic(value string) string {
	// 剥掉除换行/制表符以外的控制字符（如颜色转义），免得诊断文本里混进终端噪声。
	value = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, value)
	value = agentAuthorizationHeaderRe.ReplaceAllString(value, `$1[REDACTED]`)
	value = agentJSONSecretRe.ReplaceAllString(value, `$1"[REDACTED]"`)
	value = agentDiagnosticSecretRe.ReplaceAllString(value, `$1$2[REDACTED]`)
	return redact.Text(value)
}

// stderrTail 把写入转发给内层 writer（通常是 daemon 日志），同时保留最近一段
// 字节的有界尾部。消费方在 agent 进程没来得及发结构化错误就退出时调 Tail()，
// 把这段上下文带进错误消息——否则用户只能看到 "exit status N"，真正原因躺在
// daemon 日志里。
//
// 所有监护子 CLI 进程的 backend 都应把 cmd.Stderr 接到这个类型上，失败时经
// withAgentStderr 把 Tail() 拼进 Result.Error。这让 CLI 崩溃的根因排查不必去翻
// daemon 宿主机的日志文件。
type stderrTail struct {
	inner io.Writer // 原样转发目标（daemon 日志）
	max   int       // 保留的尾部字节上限

	mu    sync.Mutex
	buf   []byte // 有界尾部缓冲
	total int64  // 累计写入总字节数（观测用）
}

func newStderrTail(inner io.Writer, max int) *stderrTail {
	if max <= 0 {
		max = agentStderrTailBytes
	}
	return &stderrTail{inner: inner, max: max}
}

func (s *stderrTail) Write(p []byte) (int, error) {
	if _, err := s.inner.Write(p); err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.total += int64(len(p))
	s.buf = append(s.buf, p...)
	if len(s.buf) > s.max {
		s.buf = s.buf[len(s.buf)-s.max:]
	}
	s.mu.Unlock()
	return len(p), nil
}

// TotalBytes 返回累计写入的原始字节总数。
func (s *stderrTail) TotalBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

// Tail 返回捕获到的 stderr：修正为合法 UTF-8 并去掉首尾空白。按字节截断的尾部
// 可能从某个多字节 rune 中间开始或结束，所以无效片段在诊断落库前被丢弃。内层
// writer 仍然原样收到每一个字节。空串表示"什么都没写进去，或没有捕获到合法非空
// 文本"。
func (s *stderrTail) Tail() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(strings.ToValidUTF8(string(s.buf), ""))
}

// withAgentStderr 在 stderr 尾部非空时把它作为提示追加到错误消息后；为空则原样
// 返回 msg。尾部带一个简短标签，即使原 msg 已经很长，拼出来的串仍可读。
func withAgentStderr(msg, label, tail string) string {
	if tail == "" {
		return msg
	}
	return msg + "; " + label + " stderr: " + tail
}
