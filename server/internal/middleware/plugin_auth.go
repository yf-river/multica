// 本文件给「公开的插件 Action API」做机器凭证（token）边界的预检：只认我们自己签发的
// 插件类 token（前缀 mpi_/mpc_），并把裸 token 从 Authorization 头里取出来。
// 这里只做前缀分流、不做完整校验，真正的凭证核验交给后面的 Action handler。
package middleware

import (
	"net/http"
	"strings"

	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

// PluginBearerOnly 把公开的 Action API 圈在「机器凭证信任边界」内。浏览器会话走另一条
// 独立的、按会话鉴权的桥接路由；公开 /v1 契约只接受安装级（installation）与回调级
// （callback）token。
//
// 前缀匹配只负责路由分流。Action handler 仍会把 token 拿到自己的存储里解析，
// 并拒绝无效或过期的凭证。
func PluginBearerOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsPluginBearerToken(BearerToken(r)) {
			publicapiv1.WriteProblem(w, r, http.StatusUnauthorized, "plugin_bearer_required", "plugin bearer token required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// BearerToken 从 Authorization 请求头里抠出原始凭证：识别大小写不敏感的 "Bearer " 前缀，
// 返回其后的内容并去空白；头缺失或前缀不对返回空串。
func BearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// IsPluginBearerToken 判断一个凭证是不是「该由我们解析」的那一类。
//
// 只做前缀匹配、不做校验：这里只决定哪个代码路径来看这个 token，无效 token 被路由到
// 这里后，片刻后会被 handler 拒绝。按前缀分流，是为了避免插件 token 被拿去试 PAT 缓存、
// 也避免 PAT 被拿去试安装表——两类凭证的解析路径互不串门。
func IsPluginBearerToken(token string) bool {
	return strings.HasPrefix(token, "mpi_") || strings.HasPrefix(token, "mpc_")
}
