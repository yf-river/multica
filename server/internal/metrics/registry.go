// 包 metrics 是 Multica 服务端 Prometheus 指标的统一出口：这里定义并组装各类
// 指标的采集器（HTTP、业务、数据库连接池、媒体对账、频道租约、企微推送等），
// 生成一个可被 /metrics 抓取的 Registry。业务代码只负责「往各自的指标对象里
// 记数」，抓取与注册由本包集中管理。
//
// 本文件是包内「装配中心」：把散落在各文件里的指标组统统注册进同一个
// prometheus.Registry，并附带上 Go 运行时、进程与构建信息三类通用指标。
package metrics

import (
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/realtime"
)

// RegistryOptions 构造 Registry 的输入。除连接池外大多可为 nil——
// 不提供就少注册一类指标，保证「只想要 HTTP 指标」也能用。
type RegistryOptions struct {
	// Pool 主业务数据库连接池；非空时注册 DB 连接池指标。
	Pool *pgxpool.Pool
	// OutboxPool 用于 outbox 采样统计的专用连接池。它可以是独立池，这样抓取指标时
	// 用的连接不会跟业务查询抢（避免「指标采样把业务查询饿死」）；不配则退回应用
	// 共用的那个池（默认是安全的）。
	OutboxPool *pgxpool.Pool
	// Realtime 实时模块的指标；nil 时不注册实时指标采集器。
	Realtime *realtime.Metrics
	// DaemonWS daemon WebSocket 模块的指标；nil 时不注册。
	DaemonWS *daemonws.Metrics
	// Version / Commit 构建信息，落进 multica_build_info 标签。空串会被占位值替代。
	Version string
	Commit  string
}

// Registry 组装完成的指标注册表。Gatherer 是抓取端点真正读取的数据源；
// 其余字段暴露各业务模块自己的指标句柄，让调用方可以随时往里记数。
type Registry struct {
	Gatherer     prometheus.Gatherer
	HTTP         *HTTPMetrics
	Business     *BusinessMetrics
	ChannelMedia *ChannelMediaReconcilerMetrics
	ChannelLease *ChannelLeaseMetrics
	Wecom        *WecomMetrics
}

// NewRegistry 按选项装配注册表：先建独立 Registry（不引入默认的 Go/进程指标），
// 显式注册 Go 与进程级采集器，再注册构建信息与各业务指标组；可选地注册 DB、
// realtime、daemonws、outbox 采集器。返回的 Registry 里 Gatherer 交给 HTTP 端点用，
// 各指标句柄交给业务代码记数用。
func NewRegistry(opts RegistryOptions) *Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// multica_build_info 恒为 1，标签里带版本与 commit，用于「当前跑的是哪个构建」。
	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "multica_build_info",
		Help: "Build information for the Multica server binary.",
	}, []string{"version", "commit"})
	buildInfo.WithLabelValues(defaultLabel(opts.Version, "dev"), defaultLabel(opts.Commit, "unknown")).Set(1)
	reg.MustRegister(buildInfo)

	httpMetrics := NewHTTPMetrics()
	reg.MustRegister(httpMetrics.Collectors()...)

	businessMetrics := NewBusinessMetrics()
	reg.MustRegister(businessMetrics.Collectors()...)

	channelMedia := NewChannelMediaReconcilerMetrics()
	reg.MustRegister(channelMedia.Collectors()...)

	channelLease := NewChannelLeaseMetrics()
	reg.MustRegister(channelLease.Collectors()...)

	wecomMetrics := NewWecomMetrics()
	reg.MustRegister(wecomMetrics.Collectors()...)

	if opts.Pool != nil {
		reg.MustRegister(NewDBCollector(opts.Pool))
	}
	if opts.Realtime != nil {
		reg.MustRegister(NewRealtimeCollector(opts.Realtime))
	}
	if opts.DaemonWS != nil {
		reg.MustRegister(NewDaemonWSCollector(opts.DaemonWS))
	}
	if outbox := NewOutboxCollector(opts.OutboxPoolOrPool()); outbox != nil {
		reg.MustRegister(outbox)
	}

	return &Registry{
		Gatherer:     reg,
		HTTP:         httpMetrics,
		Business:     businessMetrics,
		ChannelMedia: channelMedia,
		ChannelLease: channelLease,
		Wecom:        wecomMetrics,
	}
}

// OutboxPoolOrPool 返回 outbox 采样要用的池：配了专用池用专用池，没配就用主业务池。
func (opts RegistryOptions) OutboxPoolOrPool() *pgxpool.Pool {
	if opts.OutboxPool != nil {
		return opts.OutboxPool
	}
	return opts.Pool
}

// defaultLabel 去掉 value 首尾空白，空则回退到 fallback。用于给构建信息这类
// 「可能没传」的标签填一个可读的占位值，而不是空白。
func defaultLabel(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
