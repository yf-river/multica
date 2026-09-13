// 本文件负责 Prometheus 指标服务的开关配置：从环境变量 METRICS_ADDR 读出监听地址，
// 并提供一个「该地址是否只能本机访问」的判断工具。是启动指标 HTTP 服务前的配置入口。
package metrics

import (
	"net"
	"os"
	"strings"
)

// Config 指标服务的启动配置。Addr 是 Prometheus 抓取端点要监听的地址
// （形如 "127.0.0.1:9091"）；为空表示不开指标服务。
type Config struct {
	Addr string
}

// ConfigFromEnv 从环境变量构造配置。只认 METRICS_ADDR 一个变量，
// 读入后去掉首尾空白，其余值原样保留。
func ConfigFromEnv() Config {
	return Config{Addr: strings.TrimSpace(os.Getenv("METRICS_ADDR"))}
}

// Enabled 报告指标服务是否应该启动。判断标准是监听地址去掉空白后不为空——
// 没配 METRICS_ADDR 时地址为空串，视为不启用。
func (c Config) Enabled() bool {
	return strings.TrimSpace(c.Addr) != ""
}

// IsLoopbackAddr 判断给定的监听地址是否只会绑定到本机回环。
//
// 用途：指标接口会暴露进程内部运行数据，只该让本机或内网运维工具访问，不能随手
// 暴露到公网。调用方用它确认配置里的地址是 localhost/127.0.0.1/::1 之类的回环
// 地址，不是才拒绝启动。
//
// 实现说明：先把 "host:port" 拆出主机名；拆不开（说明用户没写端口）就把整个地址当
// 主机名。之后剥掉 IPv6 的方括号，把 "localhost"（不区分大小写）或任一回环 IP 视为
// 回环地址，其余一律返回 false。
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		host = strings.TrimSpace(addr)
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
