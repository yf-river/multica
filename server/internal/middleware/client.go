// 本文件实现「客户端元信息」中间件：从 X-Client-Platform / X-Client-Version /
// X-Client-OS 三个请求头里读出调用方是谁（Web/CLI/Daemon、版本、操作系统），
// 塞进请求 context。这样日志/指标/功能开关能按客户端区分，而不必去反解
// User-Agent 或升级消息载荷。
package middleware

import (
	"context"
	"net/http"
)

// 客户端元信息的 context 键。
//
// 由 ClientMetadata 中间件从 X-Client-Platform / X-Client-Version / X-Client-OS
// 请求头填充。这三个头由第一方客户端（Web、CLI、Daemon）发送，服务端据此切分日志/
// 指标/开关判定，无需反解 User-Agent 字符串或升级消息载荷。
//
// 三个值都是尽力而为：handler 必须把缺失值当 "unknown"，且绝不能用它们做安全决策
// ——这些头由客户端控制，伪造毫无成本。
type clientMetadataKey int

// 具体三个 context 键（iota 生成私有 int，防止与其它包冲突）。
const (
	ctxKeyClientPlatform clientMetadataKey = iota
	ctxKeyClientVersion
	ctxKeyClientOS
)

// 请求头名——导出给其它包（请求日志、realtime hub）引用，避免各处重抄魔法字符串。
const (
	HeaderClientPlatform = "X-Client-Platform"
	HeaderClientVersion  = "X-Client-Version"
	HeaderClientOS       = "X-Client-OS"
)

// ClientMetadata 从请求头里取三个 X-Client-* 值（缺哪个就不写哪个），存进请求
// context，供下游 handler 与请求日志经 ClientMetadataFromContext 读取。
//
// 在 router.go 里于路由挂载前接线，这样鉴权与非鉴权 handler 都能拿到同一套
// 可观测维度。
func ClientMetadata(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if v := r.Header.Get(HeaderClientPlatform); v != "" {
			ctx = context.WithValue(ctx, ctxKeyClientPlatform, v)
		}
		if v := r.Header.Get(HeaderClientVersion); v != "" {
			ctx = context.WithValue(ctx, ctxKeyClientVersion, v)
		}
		if v := r.Header.Get(HeaderClientOS); v != "" {
			ctx = context.WithValue(ctx, ctxKeyClientOS, v)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ClientMetadataFromContext 从 context 里取出此前存下的 platform/version/os。
// 任何没存过的值都返回空串——调用方必须把缺失当 "unknown"，而不是当错误处理。
func ClientMetadataFromContext(ctx context.Context) (platform, version, os string) {
	platform, _ = ctx.Value(ctxKeyClientPlatform).(string)
	version, _ = ctx.Value(ctxKeyClientVersion).(string)
	os, _ = ctx.Value(ctxKeyClientOS).(string)
	return platform, version, os
}

// SetClientMetadata 显式把客户端元信息写入 context。供 realtime hub 使用——那边
// 的元信息来自 WS 查询参数（client_platform / client_version / client_os），
// 不走请求头。
func SetClientMetadata(ctx context.Context, platform, version, os string) context.Context {
	if platform != "" {
		ctx = context.WithValue(ctx, ctxKeyClientPlatform, platform)
	}
	if version != "" {
		ctx = context.WithValue(ctx, ctxKeyClientVersion, version)
	}
	if os != "" {
		ctx = context.WithValue(ctx, ctxKeyClientOS, os)
	}
	return ctx
}
