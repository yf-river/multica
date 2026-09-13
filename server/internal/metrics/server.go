// 本文件把 Prometheus 采集器包装成可对外服务的 HTTP 端点与 HTTP Server，
// 是「开一个端口让 Prometheus 来抓指标」这层胶水代码。
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewHandler 构造指标抓取用的 http.Handler：路由 /metrics 交给 promhttp 渲染。
// 采集数据源由调用方传入的 gatherer 决定（一般传 Registry.Gatherer）。
// EnableOpenMetrics 开启 OpenMetrics 文本格式；抓取过程中自身出错时直接返回 500
// 而不是偷偷吞掉，便于及时暴露「指标系统坏了」这件事本身。
func NewHandler(gatherer prometheus.Gatherer) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
		ErrorHandling:     promhttp.HTTPErrorOnError,
	}))
	return mux
}

// NewServer 构造指标抓取用的完整 http.Server。
//
// 参数 addr 是监听地址（由 ConfigFromEnv 的 METRICS_ADDR 提供），gatherer 是采集器。
// 各超时都设了显式上限，防止恶意的慢连接把端口占死：读请求头 5 秒、整体读 10 秒、
// 写响应 10 秒，连接空闲 30 秒后关闭。
func NewServer(addr string, gatherer prometheus.Gatherer) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           NewHandler(gatherer),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
}
