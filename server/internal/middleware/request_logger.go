// 本文件实现结构化 HTTP 请求日志中间件：用 slog 替代 Chi 自带日志，逐请求输出
// 方法/路径/状态码/耗时/请求ID/用户/客户端元信息，并针对两种噪声做了专门处理——
// 一是 webhook 入站路径里带凭证，日志前要打码；二是 daemon 生命周期里常见的
// 「资源已被删」404，不该按 Warn 淹没日志。
package middleware

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// webhookTriggerIDKeyType 用私有空结构体做 context 键的类型：不导出类型，外部包就
// 无法用同类型键撞车——它们只能经 SetWebhookTriggerID 写入。
type webhookTriggerIDKeyType struct{}

var webhookTriggerIDKey = webhookTriggerIDKeyType{}

// SetWebhookTriggerID 把解析好的 trigger ID 存进请求 context，让请求日志能把它写进
// 审计行，同时不必把 bearer token 暴露在 URL 路径里。由 webhook handler 在查到
// trigger 行之后立刻调用。
//
// 注意它直接改 `*r` 指向的对象（就地替换成带新 context 的副本）：外层包装中间件仍
// 握着最初的 `*http.Request`，ServeHTTP 返回后要能从 context 读回这个值。如果只给
// 局部的 `r` 变量重新赋值，新 context 传不回去——旧版实现就踩过这个坑。
func SetWebhookTriggerID(r *http.Request, triggerID string) {
	if triggerID == "" {
		return
	}
	*r = *r.WithContext(context.WithValue(r.Context(), webhookTriggerIDKey, triggerID))
}

// webhookTriggerIDFromContext 读回 SetWebhookTriggerID 存的 trigger ID；没存过返回 ""。
func webhookTriggerIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(webhookTriggerIDKey).(string)
	return v
}

// webhookIngressPathPrefix 是公开 webhook 入站路径。此前缀之后的路径段本身就是
// bearer 凭证，日志必须打码——见 redactWebhookPath。
const webhookIngressPathPrefix = "/api/webhooks/autopilots/"

// redactWebhookPath 返回「可安全进日志」的路径。autopilot webhook 入站路径里把
// 末尾的 token 段替换成 "[redacted]"；其它路径原样返回。
//
// 为什么要打码：成功投递的 webhook 请求，r.URL.Path 形如
// "/api/webhooks/autopilots/awt_<32字节base64>"，而 token 是这条路由唯一的凭据。
// 不打码的话，每次成功投递都会把一条可重放的 URL 打进行结构化日志。
func redactWebhookPath(path string) string {
	if !strings.HasPrefix(path, webhookIngressPathPrefix) {
		return path
	}
	rest := path[len(webhookIngressPathPrefix):]
	if rest == "" {
		return path
	}
	// 保留 token 之后的子路径（当前没有，防御性处理）。
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		return webhookIngressPathPrefix + "[redacted]" + rest[slash:]
	}
	return webhookIngressPathPrefix + "[redacted]"
}

// boundedBuffer 最多从流里收 cap 字节，其余静默丢弃。RequestLogger 用它镜像一小段
// 响应体来做 404 分类——大响应体不能因此撑爆日志进程的内存。
type boundedBuffer struct {
	buf bytes.Buffer
	cap int
}

// Write 实现 io.Writer：达到容量上限后返回 len(p), nil（假装写完了），
// 让上游（net/http）以为一切正常，而实际只截取了前 cap 字节。
func (b *boundedBuffer) Write(p []byte) (int, error) {
	remain := b.cap - b.buf.Len()
	if remain <= 0 {
		return len(p), nil
	}
	if len(p) > remain {
		b.buf.Write(p[:remain])
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

// Bytes 返回已捕获的内容。
func (b *boundedBuffer) Bytes() []byte { return b.buf.Bytes() }

// softNotFoundBodyCaptureLimit 是请求日志为判定「404 是否为预期的过期状态信号」
// （runtime/task 已在服务端删除）而最多检查的响应体字节数。JSON 错误信封很小——
// 256 字节足以看到 "error" 字段——这个上限也保证无界 handler 响应体撑不爆日志内存。
const softNotFoundBodyCaptureLimit = 256

// softNotFoundMarkers 是 daemon 在日常生命周期里例行输出的 404 响应体特征：
// 从 UI 删掉一个 runtime、issue 被删后 task 被 GC……把这批按 Warn 打，会让生产
// stderr 在删除 runtime 时刷屏（见 issue #2391）。它们保持机器可识别地落在 Info 级，
// 而真正的 4xx（路径错、鉴权失败、真 bug）仍然走 Warn。
var softNotFoundMarkers = []string{
	"runtime not found",
	"task not found",
}

// RequestLogger 是 slog 版结构化 HTTP 请求日志中间件，替代 Chi 内置的
// chimw.Logger，输出带颜色的结构化字段。
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 跳过高频的存活探活端点，保持日志可读。
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

		// 捕获一小段响应体前缀，好按内容给 404 分类。chimw.WrapResponseWriter 提供
		// Tee 正是干这个的——响应体照常流向客户端，我们只镜像前 N 字节供检查。
		bodyPrefix := &boundedBuffer{cap: softNotFoundBodyCaptureLimit}
		ww.Tee(bodyPrefix)

		next.ServeHTTP(ww, r)

		duration := time.Since(start)
		status := ww.Status()

		attrs := []any{
			"method", r.Method,
			"path", redactWebhookPath(r.URL.Path),
			"status", status,
			"duration", duration.Round(time.Microsecond).String(),
		}
		if rid := chimw.GetReqID(r.Context()); rid != "" {
			attrs = append(attrs, "request_id", rid)
		}
		if uid := r.Header.Get("X-User-ID"); uid != "" {
			attrs = append(attrs, "user_id", uid)
		}
		if tid := webhookTriggerIDFromContext(r.Context()); tid != "" {
			attrs = append(attrs, "webhook_trigger_id", tid)
		}
		if platform, version, os := ClientMetadataFromContext(r.Context()); platform != "" || version != "" || os != "" {
			if platform != "" {
				attrs = append(attrs, "client_platform", platform)
			}
			if version != "" {
				attrs = append(attrs, "client_version", version)
			}
			if os != "" {
				attrs = append(attrs, "client_os", os)
			}
		}

		// 按状态分日志级别：5xx 用 Error；「软 404」（预期内的过期状态）用 Info；
		// 其余 4xx 用 Warn；正常请求用 Info。
		switch {
		case status >= 500:
			slog.Error("http request", attrs...)
		case status == http.StatusNotFound && isSoftNotFound(bodyPrefix.Bytes()):
			// 生命周期 404——runtime/task 在服务端已被删。daemon 正是捕获这段响应体
			// 触发自己的自愈流程，所以它既不是噪声也不是 bug；打在 Info 级让信号留在
			// 结构化日志里，又不淹没 warn 通道。
			slog.Info("http request", attrs...)
		case status >= 400:
			slog.Warn("http request", attrs...)
		default:
			slog.Info("http request", attrs...)
		}
	})
}

// isSoftNotFound 判断捕获的响应体是否命中 softNotFoundMarkers 里某个预期中的
// 过期状态 404 特征。命中则说明这不是 bug，而是正常生命周期。
func isSoftNotFound(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	lower := strings.ToLower(string(body))
	for _, marker := range softNotFoundMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
