// 本文件给「插件公开 API」按凭证做固定窗口限流：每个不透明的插件凭证（token）在
// 一个窗口内分配一个预算额度，超了返回 429。凭证在用作 Redis key 前先哈希，
// 密钥绝不能进日志或任何运维数据。
package middleware

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
	"github.com/redis/go-redis/v9"
)

// PluginRateLimit 返回一个按插件凭证限流的中间件：每个凭证在固定窗口 window 内最多
// 允许 limit 次请求。token 会先被 SHA-256 哈希再当 Redis key——明文凭证绝不能落到
// 日志/监控里。Redis 客户端缺失时放行（fail open），与既有公网边缘限流器的行为保持一致；
// Redis 运行时报错同样放行并记警告日志，避免 Redis 抖动把插件 API 整体打挂。
func PluginRateLimit(rdb *redis.Client, limit int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if rdb == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 用凭证哈希当 key，窗口内每次请求计数 +1（原子 Lua 脚本，见 rateLimitScript）。
			digest := sha256.Sum256([]byte(BearerToken(r)))
			key := fmt.Sprintf("mul:ratelimit:plugin:%x", digest)
			count, err := rateLimitScript.Run(r.Context(), rdb, []string{key}, int(window.Seconds())).Int64()
			if err != nil {
				slog.Warn("plugin ratelimit: redis error; allowing request", "error", err)
				next.ServeHTTP(w, r)
				return
			}
			if count > int64(limit) {
				// 超限：告诉调用方窗口还剩多久可重试，并回 429。
				w.Header().Set("Retry-After", fmt.Sprintf("%d", int(window.Seconds())))
				publicapiv1.WriteProblem(w, r, http.StatusTooManyRequests, "rate_limited", "Plugin API rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
