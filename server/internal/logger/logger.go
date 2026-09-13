// Package logger 集中初始化 slog 全局/具名 logger：统一读取 LOG_LEVEL、
// 按 stderr 是否为终端决定是否输出 ANSI 颜色，并提供从 HTTP 请求提取可观测字段
// 的辅助函数。
package logger

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/lmittmann/tint"

	"github.com/multica-ai/multica/server/internal/middleware"
)

// isTerminal 判断给定文件描述符是否连着终端。stderr 被重定向到文件
// （如 daemon.log）时据此关闭 ANSI 颜色转义，让日志文件保持干净。
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Init 初始化全局 slog logger。stderr 是终端时启用颜色，否则关闭。
// 读取 LOG_LEVEL 环境变量（debug、info、warn、error）。默认：debug。
func Init() {
	level := parseLevel(os.Getenv("LOG_LEVEL"))
	handler := tint.NewHandler(os.Stderr, &tint.Options{
		Level:      level,
		TimeFormat: "15:04:05.000",
		NoColor:    !isTerminal(os.Stderr),
	})
	slog.SetDefault(slog.New(handler))
}

// NewLogger 创建带名称的 slog logger。颜色规则与 Init 相同（按 TTY 检测）。
// 适合需要组件前缀的独立进程（daemon、migrate）。
func NewLogger(component string) *slog.Logger {
	level := parseLevel(os.Getenv("LOG_LEVEL"))
	handler := tint.NewHandler(os.Stderr, &tint.Options{
		Level:      level,
		TimeFormat: "15:04:05.000",
		NoColor:    !isTerminal(os.Stderr),
	})
	return slog.New(handler).With("component", component)
}

// StderrIsTerminal 报告本进程的 stderr 是否连着终端。daemon 用它区分用户在 shell
// 里跑 `daemon start --foreground`（日志实时打到终端）与分离/后台子进程（stderr
// 被重定向到文件，改为轮转写入 daemon.log）这两种情况。
func StderrIsTerminal() bool {
	return isTerminal(os.Stderr)
}

// NewWriterLoggerDefault 构建一个把结构化、无色输出写到 w 的具名 slog logger，并
// 把同一 handler 设为全局 slog 默认值，这样零散的 slog.Info/Warn/... 调用（例如来自
// LoadConfig）也落到同一个 sink。面向"把日志写文件或轮转 writer 而非终端"的独立进程
// （daemon）：永远不要 ANSI 颜色，且每一行——无论是注入的 logger 还是包级全局调用——
// 都必须进同一个受管文件。同 NewLogger/Init 一样读取 LOG_LEVEL。
func NewWriterLoggerDefault(component string, w io.Writer) *slog.Logger {
	level := parseLevel(os.Getenv("LOG_LEVEL"))
	handler := tint.NewHandler(w, &tint.Options{
		Level:      level,
		TimeFormat: "15:04:05.000",
		NoColor:    true,
	})
	base := slog.New(handler)
	slog.SetDefault(base)
	return base.With("component", component)
}

// RequestAttrs 从 HTTP 请求中提取 request_id、user_id 与 X-Client-* 元数据，
// 供 handler 层结构化日志使用。与全局请求日志对齐，让 handler 日志与访问日志
// 拥有相同的可观测维度。
func RequestAttrs(r *http.Request) []any {
	attrs := make([]any, 0, 10)
	if rid := chimw.GetReqID(r.Context()); rid != "" {
		attrs = append(attrs, "request_id", rid)
	}
	if uid := r.Header.Get("X-User-ID"); uid != "" {
		attrs = append(attrs, "user_id", uid)
	}
	platform, version, os := middleware.ClientMetadataFromContext(r.Context())
	if platform != "" {
		attrs = append(attrs, "client_platform", platform)
	}
	if version != "" {
		attrs = append(attrs, "client_version", version)
	}
	if os != "" {
		attrs = append(attrs, "client_os", os)
	}
	return attrs
}

// parseLevel 把 LOG_LEVEL 字符串解析为 slog.Level，未知值回退到 debug。
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelDebug
	}
}
