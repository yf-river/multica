// 本文件实现基于 Redis 的「按 IP 固定窗口」限流中间件（也含插件限流复用的原子脚本），
// 以及「信任哪些代理、才信 X-Forwarded-For」的客户端 IP 解析逻辑。项目默认不信任任何
// XFF 头——只有直连方确实是可信代理时才用 XFF 找真实客户端 IP，防止伪造头绕过限流。
package middleware

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// rateLimitScript 原子地给计数器 +1，并在首次访问时设置 TTL。用 Lua 脚本保证
// INCR 与 EXPIRE 不会被网络故障拆开——只要 INCR 成功，TTL 就一定被设置，
// 避免「key 一直留着却永不过期」变成永久封禁。
var rateLimitScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
    redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return count
`)

// ParseTrustedProxies 把逗号分隔的 CIDR 列表解析成 *net.IPNet 切片。
// 非法条目记警告并跳过。raw 为空返回 nil（默认：永远不信任 X-Forwarded-For）。
func ParseTrustedProxies(raw string) []*net.IPNet {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var nets []*net.IPNet
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		_, cidr, err := net.ParseCIDR(p)
		if err != nil {
			slog.Warn("ratelimit: invalid trusted proxy CIDR, skipping", "cidr", p, "error", err)
			continue
		}
		nets = append(nets, cidr)
	}
	return nets
}

// RateLimit 返回一个基于 Redis 的按 IP 固定窗口限流中间件。rdb 为 nil 时中间件是
// 空操作（fail-open，不拦请求）。
//
// trustedProxies 控制 X-Forwarded-For 的处理：当直连方（RemoteAddr）落在列表里的
// CIDR 时，取 XFF 链上「最右侧的非可信 IP」当作客户端 IP；列表为空（默认）时完全
// 不读 XFF，只用 RemoteAddr。这与项目保守的信任模型一致（见 health_realtime.go）。
func RateLimit(rdb *redis.Client, limit int, window time.Duration, trustedProxies []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if rdb == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := extractIP(r, trustedProxies)
			key := rateLimitKey(r.URL.Path, ip)
			ctx := r.Context()

			count, err := rateLimitScript.Run(ctx, rdb, []string{key}, int(window.Seconds())).Int64()
			if err != nil {
				slog.Warn("ratelimit: redis error; allowing request", "error", err, "ip", ip)
				next.ServeHTTP(w, r)
				return
			}
			if count > int64(limit) {
				// 超限：告诉客户端窗口剩余秒数，回 429 JSON。
				w.Header().Set("Retry-After", fmt.Sprintf("%d", int(window.Seconds())))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				json.NewEncoder(w).Encode(map[string]string{"error": "too many requests"})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// extractIP 为限流确定客户端 IP。只有当 RemoteAddr 来自可信代理时才采用
// X-Forwarded-For，否则一律用直连地址——客户端可随意伪造 XFF，不能直接信。
func extractIP(r *http.Request, trustedProxies []*net.IPNet) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}

	if len(trustedProxies) > 0 {
		remoteIP := net.ParseIP(remoteHost)
		if remoteIP != nil && isTrustedProxy(remoteIP, trustedProxies) {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				// 从右往左找：最右侧那条「不在可信代理名单里」的 IP，就是进入可信
				// 代理链之前的最后一跳，即真实客户端。
				parts := strings.Split(xff, ",")
				for i := len(parts) - 1; i >= 0; i-- {
					candidate := net.ParseIP(strings.TrimSpace(parts[i]))
					if candidate != nil && !isTrustedProxy(candidate, trustedProxies) {
						return candidate.String()
					}
				}
			}
		}
	}

	// 默认：用 RemoteAddr 的规范形式。能解析成 IP 就归一化，否则原样返回主机名。
	if ip := net.ParseIP(remoteHost); ip != nil {
		return ip.String()
	}
	return remoteHost
}

// isTrustedProxy 判断 ip 是否落在任一可信代理 CIDR 内。
func isTrustedProxy(ip net.IP, cidrs []*net.IPNet) bool {
	for _, cidr := range cidrs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// rateLimitKey 拼 Redis key：路径斜杠换成冒号后与 IP 一起做后缀，让不同端点的限流
// 互不影响。
func rateLimitKey(path, ip string) string {
	sanitized := strings.TrimPrefix(path, "/")
	sanitized = strings.ReplaceAll(sanitized, "/", ":")
	return fmt.Sprintf("mul:ratelimit:%s:%s", sanitized, ip)
}
