// 本文件提供 JWT 签名密钥的安全读取/校验，以及三类 token 的生成与哈希工具。
// token 家族约定：mul_ 个人访问令牌、mdt_ daemon 鉴权 token、mat_ agent 任务
// token——一律「随机前缀 + 40 位十六进制随机字符」；库里只存 SHA-256 哈希，
// 因此本包同时提供 HashToken。JWT 密钥方面：开发环境允许内置默认值，
// 生产启动必须通过 ValidateJWTSecret 拒绝所有公开已知的弱密钥。
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// defaultJWTSecret 是仅供本地开发的默认 JWT 密钥。任何人拿到它都能伪造 HS256 签名，
// 所以生产环境启动校验（ValidateJWTSecret）会拒绝它。
const defaultJWTSecret = "multica-dev-secret-change-in-production"

var (
	jwtSecret     []byte
	jwtSecretOnce sync.Once
)

// JWTSecret 返回当前进程用于签名的 JWT 密钥：首次调用时从环境变量 JWT_SECRET 读取，
// 没设就用开发默认值，之后缓存（sync.Once 保证只读一次）。
func JWTSecret() []byte {
	jwtSecretOnce.Do(func() {
		secret := os.Getenv("JWT_SECRET")
		if secret == "" {
			secret = defaultJWTSecret
		}
		jwtSecret = []byte(secret)
	})

	return jwtSecret
}

// ErrInsecureJWTSecret 是 ValidateJWTSecret 对「配置的密钥为空或属于仓库/配置模板里
// 公开已知的默认值」时报的错误。拿着这些值任何人都能伪造 HS256 签名，所以生产服务器
// 必须拒绝用它们启动。
var ErrInsecureJWTSecret = errors.New("JWT_SECRET is empty or a known insecure default")

// insecureJWTSecrets 是绝不能进生产部署的密钥黑名单。空串由 JWTSecret() 的仅开发回退
// 覆盖；"change-me-in-production" 曾是 docker-compose.selfhost.yml 与 .env.example 里
// 随模板带的历史占位值。以后模板再带任何占位值，发布前都要加进这个名单。
var insecureJWTSecrets = map[string]struct{}{
	"":                        {},
	defaultJWTSecret:          {},
	"change-me-in-production": {},
}

// ValidateJWTSecret 判断 secret 是否安全到可以用它给生产 token 签名。
// 刻意做得轻量、无副作用，启动路径与测试可以直接调用。
func ValidateJWTSecret(secret string) error {
	if _, weak := insecureJWTSecrets[strings.TrimSpace(secret)]; weak {
		return ErrInsecureJWTSecret
	}
	return nil
}

// GeneratePATToken 生成新的个人访问令牌：形如 "mul_" + 40 位随机十六进制字符。
func GeneratePATToken() (string, error) {
	b := make([]byte, 20) // 20 字节 = 40 个十六进制字符
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate PAT token: %w", err)
	}
	return "mul_" + hex.EncodeToString(b), nil
}

// GenerateDaemonToken 生成新的 daemon 鉴权 token：形如 "mdt_" + 40 位随机十六进制字符。
func GenerateDaemonToken() (string, error) {
	b := make([]byte, 20) // 20 字节 = 40 个十六进制字符
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate daemon token: %w", err)
	}
	return "mdt_" + hex.EncodeToString(b), nil
}

// GenerateAgentTaskToken 生成新的任务级 agent 鉴权 token：形如 "mat_" + 40 位随机
// 十六进制字符。token 是单用途的——服务端把它绑定到特定的 (agent_id, task_id) 对——
// daemon 用它注入 agent 进程、替代自己的 owner PAT。见 MUL-2600。
func GenerateAgentTaskToken() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate agent task token: %w", err)
	}
	return "mat_" + hex.EncodeToString(b), nil
}

// HashToken 返回 token 字符串的十六进制 SHA-256 哈希。token 明文只在签发时刻存在，
// 落库与查库一律用该哈希比对。
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
