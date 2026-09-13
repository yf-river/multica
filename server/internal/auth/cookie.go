// 本文件实现基于 HttpOnly cookie 的会话机制：签发/清除登录 cookie（multica_auth）
// 与配套的 CSRF cookie（multica_csrf），并校验「改状态请求」携带的 CSRF token。
// CSRF token 用「auth cookie 的值作 HMAC 密钥」绑定签名——攻击者即使能在子域种
// cookie，不知道 auth token 也伪造不出合法 CSRF。cookie 的 Domain/Secure 等属性
// 按环境变量动态推导。
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	AuthCookieName      = "multica_auth"
	CSRFCookieName      = "multica_csrf"
	defaultAuthTokenTTL = 30 * 24 * time.Hour // 30 天
)

var (
	ipCookieDomainWarnOnce sync.Once
	authTokenTTLOnce       sync.Once
	authTokenTTLCached     time.Duration
)

// parseAuthTokenTTL 把原始的 AUTH_TOKEN_TTL 值解析成时长。先试
// time.ParseDuration（如 "8760h"、"720h30m"），再退而求其次按整数秒解析。
// 成功返回 (时长, true)；输入为空或非法返回 (0, false)。超过 10 年的值会
// 告警但照收——基本可断定是配置写错单位。
func parseAuthTokenTTL(raw string) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}

	// 先试 Go 时长串（如 "8760h"、"720h30m"）。
	if d, err := time.ParseDuration(raw); err == nil {
		if d <= 0 {
			return 0, false
		}
		if d > 10*365*24*time.Hour {
			slog.Warn("AUTH_TOKEN_TTL exceeds 10 years; accepting but verify this is intentional",
				"value", raw, "hours", d.Hours())
		}
		return d, true
	}

	// 退回纯整数秒。
	secs, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || secs <= 0 {
		return 0, false
	}
	if secs > int64(math.MaxInt64/int64(time.Second)) {
		return 0, false
	}
	d := time.Duration(secs) * time.Second
	if d > 10*365*24*time.Hour {
		slog.Warn("AUTH_TOKEN_TTL exceeds 10 years; accepting but verify this is intentional",
			"value", raw, "hours", d.Hours())
	}
	return d, true
}

// AuthTokenTTL 返回配置的登录 token 有效期。首次调用时读 AUTH_TOKEN_TTL 环境变量
// （Go 时长串或整数秒）并缓存；变量未设或非法时用默认的 30 天。
func AuthTokenTTL() time.Duration {
	authTokenTTLOnce.Do(func() {
		raw := os.Getenv("AUTH_TOKEN_TTL")
		if ttl, ok := parseAuthTokenTTL(raw); ok {
			authTokenTTLCached = ttl
			slog.Info("auth token TTL configured", "seconds", int(ttl.Seconds()))
			return
		}
		authTokenTTLCached = defaultAuthTokenTTL
		if strings.TrimSpace(raw) != "" {
			slog.Warn("AUTH_TOKEN_TTL is not a valid duration or positive integer; using default",
				"value", raw, "default_seconds", int(defaultAuthTokenTTL.Seconds()))
		}
	})
	return authTokenTTLCached
}

// cookieDomain 返回去掉空白后的 COOKIE_DOMAIN 环境变量值；若它看起来像 IP 地址则
// 返回 ""。RFC 6265 §4.1.2.3 禁止在 cookie 的 Domain 属性里写 IP 字面量——浏览器
// 会静默丢弃带这种 Set-Cookie 的响应。配成 IP 几乎总是配置错误。
func cookieDomain() string {
	raw := strings.TrimSpace(os.Getenv("COOKIE_DOMAIN"))
	if raw == "" {
		return ""
	}
	// 前导点号（"."，表示子域可匹配）语法合法，但不影响剩余部分是不是 IP 字面量。
	if ip := net.ParseIP(strings.TrimPrefix(raw, ".")); ip != nil {
		ipCookieDomainWarnOnce.Do(func() {
			slog.Warn(
				"COOKIE_DOMAIN looks like an IP address; ignoring. RFC 6265 forbids IP literals in the cookie Domain attribute, so browsers would drop the Set-Cookie. Leave COOKIE_DOMAIN empty for single-host deployments, or use a real domain.",
				"value", raw,
			)
		})
		return ""
	}
	return raw
}

// isSecureCookie 决定会话 cookie 是否要带 Secure 标记。依据 FRONTEND_ORIGIN 的协议
// 推导——浏览器会静默丢弃在纯 HTTP 页面上收到的 Secure cookie，所以这个标记必须跟随
// 用户真正面对的协议，而不是一个笼统的环境名。
func isSecureCookie() bool {
	raw := strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN"))
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, "https")
}

// generateCSRFToken 生成一个用 auth token 经 HMAC 绑定的 CSRF token。
// 格式：hex(nonce) + "." + hex(HMAC-SHA256(nonce, authToken))。
// 这样即使攻击者能在某个子域种 cookie，不知道 auth token 也伪造不出合法 CSRF token。
func generateCSRFToken(authToken string) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	nonceHex := hex.EncodeToString(nonce)

	mac := hmac.New(sha256.New, []byte(authToken))
	mac.Write(nonce)
	sig := hex.EncodeToString(mac.Sum(nil))

	return nonceHex + "." + sig, nil
}

// SetAuthCookies 在响应上设置两个 cookie：HttpOnly 的鉴权 cookie 与可被 JS 读到的
// CSRF cookie。auth cookie 带 HttpOnly + SameSite=Strict；CSRF cookie 不带 HttpOnly
// （前端要读它放进请求头）。Secure 与 Domain 按环境推导。
func SetAuthCookies(w http.ResponseWriter, token string) error {
	secure := isSecureCookie()
	domain := cookieDomain()
	ttl := AuthTokenTTL()
	now := time.Now()

	http.SetCookie(w, &http.Cookie{
		Name:     AuthCookieName,
		Value:    token,
		Path:     "/",
		Domain:   domain,
		MaxAge:   int(ttl.Seconds()),
		Expires:  now.Add(ttl),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})

	csrfToken, err := generateCSRFToken(token)
	if err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    csrfToken,
		Path:     "/",
		Domain:   domain,
		MaxAge:   int(ttl.Seconds()),
		Expires:  now.Add(ttl),
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})

	return nil
}

// ClearAuthCookies 清掉鉴权与 CSRF 两个 cookie（MaxAge=-1 + 过期时间归零）。
func ClearAuthCookies(w http.ResponseWriter) {
	domain := cookieDomain()
	secure := isSecureCookie()

	http.SetCookie(w, &http.Cookie{
		Name:     AuthCookieName,
		Value:    "",
		Path:     "/",
		Domain:   domain,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})

	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    "",
		Path:     "/",
		Domain:   domain,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// ValidateCSRF 把 X-CSRF-Token 请求头与鉴权 cookie 对验。CSRF token 是用 auth token
// 做 HMAC 签名的，因此服务端验的是签名，而不是简单地比较 cookie == 请求头。
// 返回 true 表示通过（包括不需要 CSRF 的安全方法：GET/HEAD/OPTIONS 直接放行）。
func ValidateCSRF(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}

	csrfHeader := r.Header.Get("X-CSRF-Token")
	if csrfHeader == "" {
		return false
	}

	authCookie, err := r.Cookie(AuthCookieName)
	if err != nil || authCookie.Value == "" {
		return false
	}

	// 解开 nonce.sig 两段，各自必须是合法十六进制。
	parts := strings.SplitN(csrfHeader, ".", 2)
	if len(parts) != 2 {
		return false
	}

	nonce, err := hex.DecodeString(parts[0])
	if err != nil {
		return false
	}

	expectedSig, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}

	// 用 auth token 重算签名并恒定时间比较，防时序侧信道。
	mac := hmac.New(sha256.New, []byte(authCookie.Value))
	mac.Write(nonce)
	return hmac.Equal(mac.Sum(nil), expectedSig)
}
