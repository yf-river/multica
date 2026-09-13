// 本文件提供 CloudFront 签名 cookie 的刷新中间件：静态资源走 CDN，需要携带签名的
// CloudFront cookie；这套 cookie 的有效期比用户会话短，中间件在请求没带 cookie 时
// 顺手签发一组新的，避免「会话还没过期、CDN 先 403」。
package middleware

import (
	"net/http"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
)

// RefreshCloudFrontCookies 返回一个中间件：对已鉴权的请求，若发现没带 CloudFront
// 签名 cookie（过期了，或登录后的第一个请求还没来得及签），就补签一组新 cookie
// 并写回响应。作用是把 CDN 因为 cookie 早于用户会话过期而报 403 的情况提前化解。
// signer 为 nil（未配置 CloudFront）时原样透传，保证中间件总是可挂载。
func RefreshCloudFrontCookies(signer *auth.CloudFrontSigner) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if signer == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// CloudFront-Policy 是三件套（Policy/Signature/Key-Pair-Id）里的主 cookie，
			// 拿它当「是否已带签名」的探针。缺 cookie 说明需要补签。
			if _, err := r.Cookie("CloudFront-Policy"); err != nil {
				// 新 cookie 的有效期对齐用户会话剩余时长（按整套授权 token 的 TTL），
				// 而不是签个很短的临时值——否则刷新后又会在会话中途再次过期。
				ttl := auth.AuthTokenTTL()
				for _, cookie := range signer.SignedCookies(time.Now().Add(ttl)) {
					http.SetCookie(w, cookie)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
