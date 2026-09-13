package remotemcp

// 本文件职责：实现「开发者本地调试放行」的两个开关——运维显式点名的 dev
// origin 可跳过「必须公网」这一条要求，并可通过额外 CA 信任本地 HTTPS 服务。
// 未设置时一切都照旧，生产部署完全不受影响。

import (
	"crypto/tls"
	"crypto/x509"
	"net/url"
	"os"
	"strings"
)

// 这里实现的是「让插件作者可以把 mcp 钩子指向自己机器上跑的服务」的运维放行口。
//
// `http` 传输早就有了这个能力：MULTICA_PLUGIN_DEV_ORIGINS 点名一组精确的
// origin，其端点在「必须公网可达」这条上放行。`mcp` 传输之前没有，于是作者
// 在本地什么都不能试、必须先部署到公网；同理，按 schema 摘要钉死已批准工具
// 的启动路径也完全无法被测试覆盖。一个不对称，两种症状。
//
// 在没设置该变量的部署里（也就是所有生产部署），下面每个调用都返回 false，
// 原有的检查原封不动地跑，行为零变化。

// DevOriginsEnv 被 server 与 daemon 两个进程读取。它们彼此独立，所以环境变量
// 每次调用现查，而不是在 init 时缓存一次。
const DevOriginsEnv = "MULTICA_PLUGIN_DEV_ORIGINS"

// DevCAEnv 是要为上述 origin 信任的 CA 包。本地跑的 MCP server 仍然必须说
// HTTPS——manifest 校验器强制要求，而放松这条意味着「管理员同意过的 URL」
// 不再能说明连接是否加密。所以这里的放行语义是「多信任一份额外 CA」，
// 绝不是「跳过证书校验」。
const DevCAEnv = "MULTICA_PLUGIN_DEV_CA"

// devTLSConfig 返回用于 dev origin 的 TLS 配置；nil 表示保持传输层默认。
//
// 读不到文件或文件为空 CA 也返回 nil：在「关不上」这件事上宁可失败——路径
// 写错的结果是一个证书错误，而不是一条悄悄不做校验的连接。
func devTLSConfig() *tls.Config {
	path := strings.TrimSpace(os.Getenv(DevCAEnv))
	if path == "" {
		return nil
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// isDevOrigin 判断运维是否点名了这一个精确 origin。
//
// 按 scheme + host + port 精确匹配。前缀或后缀匹配会让
// "http://127.0.0.1:9000" 顺手放行 "http://127.0.0.1:9000.example.com"。
func isDevOrigin(endpoint *url.URL) bool {
	if endpoint == nil || endpoint.Host == "" {
		return false
	}
	configured := strings.TrimSpace(os.Getenv(DevOriginsEnv))
	if configured == "" {
		return false
	}
	origin := endpoint.Scheme + "://" + endpoint.Host
	for _, entry := range strings.Split(configured, ",") {
		if strings.TrimSpace(entry) == origin {
			return true
		}
	}
	return false
}
