// Package profiling 提供一个仅监听本机回环地址的 pprof HTTP 服务，便于在
// 出问题时远程抓取 CPU/内存/追踪等性能剖析数据，而无需暴露到公网。
package profiling

import (
	"net/http"
	httppprof "net/http/pprof"
	"time"
)

// Addr 是 pprof 服务监听的地址：仅本机，避免把剖析端点暴露到网络上。
const Addr = "127.0.0.1:6060"

// NewHandler 组装暴露 Go 标准 pprof 各类剖析端点的路由处理器。
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", httppprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", httppprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", httppprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", httppprof.Symbol)
	mux.HandleFunc("POST /debug/pprof/symbol", httppprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", httppprof.Trace)
	return mux
}

// NewServer 构造带超时配置的 pprof HTTP 服务。
func NewServer() *http.Server {
	return &http.Server{
		Addr:              Addr,
		Handler:           NewHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		// CPU profile 与 trace 会按调用方选择的时长运行。故意不设 ReadTimeout /
		// WriteTimeout，以免超长采集被中途截断。
	}
}
