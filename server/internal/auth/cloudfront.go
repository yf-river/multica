// 本文件实现 CloudFront 私有分发的签名工具：用 RSA 私钥对一条资源策略（policy）签名，
// 生成三件套 cookie 或带查询参数的签名 URL，让 CDN 只放行携带有效签名的静态资源请求。
// 私钥来源按「AWS Secrets Manager → 环境变量（仅本地开发）」的顺序解析。
package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// CloudFrontSigner 为 CloudFront 私有分发生成签名 cookie。
type CloudFrontSigner struct {
	keyPairID    string
	privateKey   *rsa.PrivateKey
	domain       string // CDN 域名，如 "static.multica.ai"
	cookieDomain string // cookie 作用域，如 ".multica.ai"
}

// NewCloudFrontSignerFromEnv 从环境变量构造签名器。CLOUDFRONT_KEY_PAIR_ID 未设置时
// 返回 nil（禁用签名 cookie，调用方应安全降级）。
//
// 私钥解析顺序：
//  1. AWS Secrets Manager（CLOUDFRONT_PRIVATE_KEY_SECRET——secret 名称/ARN）
//  2. 环境变量回退（CLOUDFRONT_PRIVATE_KEY——base64 编码的 PEM，仅本地开发用）
//
// 其余必需环境变量：
//   - CLOUDFRONT_KEY_PAIR_ID
//   - CLOUDFRONT_DOMAIN       （如 "static.multica.ai"）
//   - COOKIE_DOMAIN           （如 ".multica.ai"）
func NewCloudFrontSignerFromEnv() *CloudFrontSigner {
	keyPairID := os.Getenv("CLOUDFRONT_KEY_PAIR_ID")
	if keyPairID == "" {
		slog.Info("CLOUDFRONT_KEY_PAIR_ID not set, signed cookies disabled")
		return nil
	}

	domain := os.Getenv("CLOUDFRONT_DOMAIN")
	if domain == "" {
		slog.Error("CLOUDFRONT_DOMAIN not set")
		return nil
	}

	cookieDomain := os.Getenv("COOKIE_DOMAIN")
	if cookieDomain == "" {
		slog.Error("COOKIE_DOMAIN not set")
		return nil
	}

	rsaKey, err := loadPrivateKey()
	if err != nil {
		slog.Error("failed to load CloudFront private key", "error", err)
		return nil
	}

	slog.Info("CloudFront cookie signer initialized", "key_pair_id", keyPairID, "domain", domain)
	return &CloudFrontSigner{
		keyPairID:    keyPairID,
		privateKey:   rsaKey,
		domain:       domain,
		cookieDomain: cookieDomain,
	}
}

// loadPrivateKey 从 Secrets Manager 或环境变量回退加载 RSA 私钥。
func loadPrivateKey() (*rsa.PrivateKey, error) {
	// 1. 优先 Secrets Manager
	if secretName := os.Getenv("CLOUDFRONT_PRIVATE_KEY_SECRET"); secretName != "" {
		slog.Info("loading CloudFront private key from Secrets Manager", "secret", secretName)
		return loadKeyFromSecretsManager(secretName)
	}

	// 2. 回退：base64 编码的环境变量（本地开发）
	if pkB64 := os.Getenv("CLOUDFRONT_PRIVATE_KEY"); pkB64 != "" {
		slog.Info("loading CloudFront private key from environment variable (local dev)")
		pemBytes, err := base64.StdEncoding.DecodeString(pkB64)
		if err != nil {
			return nil, fmt.Errorf("base64 decode: %w", err)
		}
		return parseRSAPrivateKey(pemBytes)
	}

	return nil, fmt.Errorf("neither CLOUDFRONT_PRIVATE_KEY_SECRET nor CLOUDFRONT_PRIVATE_KEY is set")
}

// loadKeyFromSecretsManager 从 AWS Secrets Manager 取指定 secret 的字符串值并解析成
// RSA 私钥。
func loadKeyFromSecretsManager(secretName string) (*rsa.PrivateKey, error) {
	cfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	client := secretsmanager.NewFromConfig(cfg)
	result, err := client.GetSecretValue(context.Background(), &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretName),
	})
	if err != nil {
		return nil, fmt.Errorf("get secret %q: %w", secretName, err)
	}

	if result.SecretString == nil {
		return nil, fmt.Errorf("secret %q has no string value", secretName)
	}

	return parseRSAPrivateKey([]byte(*result.SecretString))
}

// parseRSAPrivateKey 把 PEM 字节解析成 RSA 私钥。先试 PKCS8，再试 PKCS1——
// AWS Secrets Manager 导出的密钥通常两种格式都可能是。
func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}

	// 先 PKCS8，再 PKCS1
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
		return nil, fmt.Errorf("PKCS8 key is not RSA")
	}

	rsaKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	return rsaKey, nil
}

// SignedCookies 生成三个 CloudFront 签名 cookie，有效期到 expiry。
// 签名内容是一条策略 JSON：「给定域名下的所有路径 + 不晚于 expiry 的到期时间」；
// RSA-SHA1 签名后与 key-pair-id 一起分装在三个 cookie 里。签发失败返回 nil。
func (s *CloudFrontSigner) SignedCookies(expiry time.Time) []*http.Cookie {
	policy := fmt.Sprintf(`{"Statement":[{"Resource":"https://%s/*","Condition":{"DateLessThan":{"AWS:EpochTime":%d}}}]}`, s.domain, expiry.Unix())

	encodedPolicy := cfBase64Encode([]byte(policy))

	h := sha1.New()
	h.Write([]byte(policy))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.privateKey, crypto.SHA1, h.Sum(nil))
	if err != nil {
		slog.Error("failed to sign CloudFront policy", "error", err)
		return nil
	}
	encodedSig := cfBase64Encode(sig)

	// cookie 统一属性：作用域 cookieDomain、Secure + HttpOnly（防脚本读取）；
	// SameSite=None 是因为静态资源可能被其它站点 iframe/跨站引用，Strict/Lax 会
	// 拦掉这类携带 cookie 的跨站请求。
	cookieAttrs := func(name, value string) *http.Cookie {
		return &http.Cookie{
			Name:     name,
			Value:    value,
			Domain:   s.cookieDomain,
			Path:     "/",
			Expires:  expiry,
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteNoneMode,
		}
	}

	return []*http.Cookie{
		cookieAttrs("CloudFront-Policy", encodedPolicy),
		cookieAttrs("CloudFront-Signature", encodedSig),
		cookieAttrs("CloudFront-Key-Pair-Id", s.keyPairID),
	}
}

// SignedURL 为给定资源 URL 生成 CloudFront 签名 URL。供没有浏览器 cookie 的
// CLI/API 客户端使用：策略直接针对该资源，签名与 key-pair-id 拼进查询参数。
// 签名失败时原样返回 rawURL（调用方走无签名的降级路径）。已带查询串的 URL 会用
// "&" 连接而不是 "?"。
func (s *CloudFrontSigner) SignedURL(rawURL string, expiry time.Time) string {
	policy := fmt.Sprintf(`{"Statement":[{"Resource":"%s","Condition":{"DateLessThan":{"AWS:EpochTime":%d}}}]}`, rawURL, expiry.Unix())

	encodedPolicy := cfBase64Encode([]byte(policy))

	h := sha1.New()
	h.Write([]byte(policy))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.privateKey, crypto.SHA1, h.Sum(nil))
	if err != nil {
		slog.Error("failed to sign CloudFront URL", "error", err)
		return rawURL
	}
	encodedSig := cfBase64Encode(sig)

	separator := "?"
	if strings.Contains(rawURL, "?") {
		separator = "&"
	}
	return fmt.Sprintf("%s%sPolicy=%s&Signature=%s&Key-Pair-Id=%s", rawURL, separator, encodedPolicy, encodedSig, s.keyPairID)
}

// SignedURLWithContentDisposition 生成签名 URL，并把 contentDisposition 作为
// response-content-disposition 查询参数一起签进 URL——这样下载类资源的文件名
// 由服务端决定，同时仍在签名保护内。contentDisposition 为空则退回普通 SignedURL。
func (s *CloudFrontSigner) SignedURLWithContentDisposition(rawURL string, contentDisposition string, expiry time.Time) string {
	if contentDisposition == "" {
		return s.SignedURL(rawURL, expiry)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return s.SignedURL(rawURL, expiry)
	}
	q := u.Query()
	q.Set("response-content-disposition", contentDisposition)
	u.RawQuery = q.Encode()
	return s.SignedURL(u.String(), expiry)
}

// cfBase64Encode 做 CloudFront 规定的 URL 安全 base64 编码（把 +/=
// 换成 -/~_），结果可直接放进 cookie/查询串。
func cfBase64Encode(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	r := strings.NewReplacer("+", "-", "=", "_", "/", "~")
	return r.Replace(encoded)
}
