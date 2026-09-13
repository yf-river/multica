// 本文件定义 HTTP 层的 Prometheus 指标（请求量、耗时、并发在途数），并把这些指标
// 包装成 chi 中间件。它观测的是「API 服务对外收发的每个请求」，与业务维度指标分开。
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
)

// HTTPMetrics HTTP 请求的观测指标集合。route 标签用「路由模式」而不是真实路径，
// 防止把用户 ID、动态参数等无限变化的值当标签（标签基数会撑爆）；
// 匹配不到已知路由时才落到 "unmatched"。
type HTTPMetrics struct {
	requests                    *prometheus.CounterVec
	duration                    *prometheus.HistogramVec
	daemonWorkspaceResponseSize *prometheus.HistogramVec
	inFlight                    prometheus.Gauge
}

// daemonWorkspaceRoutePattern 需要单独观测响应体大小的 daemon 端点路由模式。
// daemon（本机常驻进程）拉「工作区列表」的接口响应可能很大，走这条专用直方图
// 是为了盯它有没有把用户带宽/内存吃满。
const daemonWorkspaceRoutePattern = "/api/daemon/workspaces"

// NewHTTPMetrics 创建全部 HTTP 指标。耗时直方图桶从 5ms 到 10s 覆盖常见接口延迟；
// 响应体大小直方图用指数桶（128 字节起、每次 ×4、共 9 桶），最到约 8MB，覆盖
// 工作区列表从极小到极大的尺寸。
func NewHTTPMetrics() *HTTPMetrics {
	return &HTTPMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Total HTTP requests served by the API server.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "multica",
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "HTTP request duration observed by the API server.",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"method", "route", "status"}),
		daemonWorkspaceResponseSize: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "multica",
			Subsystem: "http",
			Name:      "daemon_workspace_response_size_bytes",
			Help:      "Response bytes written by the daemon workspace-set endpoint.",
			Buckets:   prometheus.ExponentialBuckets(128, 4, 9),
		}, []string{"status"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "multica",
			Subsystem: "http",
			Name:      "in_flight_requests",
			Help:      "Current number of in-flight HTTP requests served by the API server.",
		}),
	}
}

// Collectors 返回本组全部指标，供调用方一次性注册进 prometheus.Registry。
func (m *HTTPMetrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.requests, m.duration, m.daemonWorkspaceResponseSize, m.inFlight}
}

// Middleware 把 m 包装成 chi 中间件，逐请求埋点。
//
// 埋点流程：健康检查路径（/health、/healthz、/readyz）不打点直接放行——它们是
// 探活用的高频请求，打进指标只会污染分布；其余请求先对在途数 +1（返回前 -1），
// 用 WrapResponseWriter 包一层好截获真实状态码和写出的字节数，随后按 method、路由
// 模式、状态码三标签计数并记录耗时；若是 daemon 工作区列表端点，再额外记录响应体大小。
//
// 接收者 nil（未初始化指标）时原样透传 next，保证中间件始终可挂载。
func (m *HTTPMetrics) Middleware(next http.Handler) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isHealthProbePath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		m.inFlight.Inc()
		defer m.inFlight.Dec()

		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		route := routePattern(r)
		statusLabel := strconv.Itoa(status)
		labels := prometheus.Labels{
			"method": r.Method,
			"route":  route,
			"status": statusLabel,
		}
		m.requests.With(labels).Inc()
		m.duration.With(labels).Observe(time.Since(start).Seconds())
		if route == daemonWorkspaceRoutePattern {
			m.daemonWorkspaceResponseSize.WithLabelValues(statusLabel).Observe(float64(ww.BytesWritten()))
		}
	})
}

// routePattern 从 chi 的路由上下文里取当前请求匹配到的「路由模式」（如 /api/issues/{id}），
// 拿不到时返回 "unmatched"。用它当指标标签，同一路由的不同参数实例会归并到一个序列。
func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if pattern := rctx.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return "unmatched"
}

// isHealthProbePath 判断路径是否为健康检查探活端点。这三条路径被调度器/负载均衡
// 高频轮询，特意从指标里剔除。
func isHealthProbePath(path string) bool {
	switch path {
	case "/health", "/healthz", "/readyz":
		return true
	default:
		return false
	}
}
