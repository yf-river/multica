// health_realtime.go
//
// 职责：提供 /health/realtime 健康/指标端点的 HTTP handler。
// 该端点暴露实时推送层的运行计数器（各类事件/作用域的发送量、Redis 中继状态、
// daemon WebSocket 连接状态等），这些数据不能让匿名公网客户端随便摸到，因此
// 端点默认只对"本机回环"请求可见，可配 Bearer token 保护（见 MUL-1342）。
package main

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/realtime"
)

// realtimeMetricsHandler 返回 /health/realtime 的 HTTP handler。
//
// 端点暴露的是运维类计数器（按事件/按作用域的发送量、Redis 中继状态等），
// 不应被匿名公网客户端访问到。参见 MUL-1342。
//
// 访问策略：
//   - 若 token != ""：要求请求带 Authorization: Bearer <token>，其余调用者
//     一律 401 拒绝。
//   - 若 token == ""：只放行直接来自回环地址（127.0.0.1 / ::1）且不带任何
//     转发头的请求；其余一律回 404（让端点无法被枚举探测）。这样本地开发
//     无需任何配置就能用，同时保证指标面不会暴露在公网监听上——即使服务器
//     躲在终止 TLS 的反向代理（Caddy/Nginx）后面也一样安全，因为那种部署下
//     所有请求在本机看都像回环地址（参见 MUL-1342 review）。
func realtimeMetricsHandler(token string) http.HandlerFunc {
	token = strings.TrimSpace(token)
	return func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			if !hasBearerToken(r, token) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		} else if !isDirectLoopbackRequest(r) {
			// 没配 token 时，对非回环调用者（以及无法判定来源的代理请求）
			// 隐藏端点：回 404 而不是 403，避免向远程扫描器暴露它存在。
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		snapshot := realtime.M.Snapshot()
		snapshot["daemonws"] = daemonws.M.Snapshot()
		_ = json.NewEncoder(w).Encode(snapshot)
	}
}

// hasBearerToken 校验请求的 Authorization 头是否为 Bearer <want>。
// 用 constant-time 比较避免时序侧信道；大小写不敏感匹配 Bearer 前缀，
// 但 token 本身区分大小写。空 token 一律视为不合法。
func hasBearerToken(r *http.Request, want string) bool {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return false
	}
	got := strings.TrimSpace(auth[len(prefix):])
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// isDirectLoopbackRequest 判断请求是否"直接来自本机回环、没有经过任何代理"。
func isDirectLoopbackRequest(r *http.Request) bool {
	// 只要有任何迹象表明请求被代理转发过，就取消回环捷径的资格：在
	// Caddy/Nginx -> localhost:8080 这种部署里，公网调用者到这里会伪装成
	// 127.0.0.1。我们刻意不"信任"这些头来识别真实客户端——只是把它们出现
	// 当作"这请求被代理过，按失败关闭处理"的信号。
	if hasForwardingHeader(r) {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// hasForwardingHeader 检查请求是否带了任何代理转发头（X-Forwarded-*、
// X-Real-Ip、Forwarded 等）。
func hasForwardingHeader(r *http.Request) bool {
	for _, h := range []string{
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Proto",
		"X-Real-Ip",
		"Forwarded",
	} {
		if strings.TrimSpace(r.Header.Get(h)) != "" {
			return true
		}
	}
	return false
}
