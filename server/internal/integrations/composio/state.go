// state.go：Composio「连接握手」用的有符号状态令牌（signed state）的签名与校验。
// 握手两端之间需要一种无需服务端会话表的短期凭证：BeginConnect 把用户 id、工具包 slug、
// auth_config_id 与过期时刻签进 state，回调回来时 CompleteCallback 仅凭这份签名即可
// 信任并把回调归属到对的人与工具包。签名用 HMAC-SHA256，密钥为部署共享的 StateSecret。
package composio

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// 有符号状态相关的错误。handler 把其中所有错误统一映射成一句通用的 "connect failed"
// 重定向，让被篡改 / 已过期的 state 永远不会泄漏「是哪一项检查没过」。
var (
	// ErrStateMalformed 在 state token 不是预期的 "<payload>.<sig>" base64url 形态时返回。
	ErrStateMalformed = errors.New("composio: state malformed")
	// ErrStateSignature 在 HMAC 签名不匹配时返回——state 被篡改过，或由另一个 secret 签署。
	ErrStateSignature = errors.New("composio: state signature mismatch")
	// ErrStateExpired 在 state 的 exp 声明已落入过去时返回。
	ErrStateExpired = errors.New("composio: state expired")
)

// stateClaims 是签在连接 state 里的载荷。它恰好携带 CompleteCallback 需要的一切，
// 让回调能无服务端会话表地归属到某个用户与工具包——签名保证它可信，短 exp 限制重放
// 窗口。
//
// 字段名取单字母是为了让编码后的 token 紧凑；它是内部线上格式，绝不暴露给客户端。
type stateClaims struct {
	UserID      string `json:"u"`
	ToolkitSlug string `json:"t"`
	// AuthConfigID 是 BeginConnect 时解析、并用来创建 connect link 的那个确切的
	// Composio auth_config_id。把它签进 state，CompleteCallback 就能校验回调返回的账户
	// 是在「本工具包」的 auth config 下创建的，而不必重新解析（重新解析可能 fail-open）。
	// 它是不透明的配置句柄（ac_…），不是凭据。
	AuthConfigID string `json:"a"`
	Exp          int64  `json:"e"`
}

// signState 产出一个 URL 安全的 "<payload>.<sig>" token。payload 是 claims 的
// base64url JSON 编码；sig 是 payload 在服务密钥下 HMAC-SHA256 的 base64url 编码。我们
// 签「编码后的 payload」（而非原始结构体），这样校验时能精确还原出被签名的那些字节。
func signState(secret []byte, claims stateClaims) (string, error) {
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	sig := signPayload(secret, payload)
	return payload + "." + sig, nil
}

// verifyState 校验 signState 产出的 token 的签名与过期时间，返回嵌入的 claims。校验
// 顺序：先用常量时间比较验签名，之后才信任 payload；再以当前时间核对过期时间。
func verifyState(secret []byte, token string, now time.Time) (stateClaims, error) {
	payload, sig, found := strings.Cut(token, ".")
	if !found || payload == "" || sig == "" {
		return stateClaims{}, ErrStateMalformed
	}
	expected := signPayload(secret, payload)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return stateClaims{}, ErrStateSignature
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return stateClaims{}, ErrStateMalformed
	}
	var claims stateClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return stateClaims{}, ErrStateMalformed
	}
	if now.Unix() > claims.Exp {
		return stateClaims{}, ErrStateExpired
	}
	return claims, nil
}

// signPayload 返回 payload 在 secret 下的 base64url 编码 HMAC-SHA256。
func signPayload(secret []byte, payload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
