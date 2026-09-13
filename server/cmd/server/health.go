// health.go
//
// 职责：提供 HTTP 存活/就绪探针（/health/live 与 /health/ready）。
// live 只管"进程活着、端口有人应答"；ready 才是真正的就绪判定：数据库能
// ping 通、且本二进制依赖的所有迁移版本都已应用，缺一即回 503。就绪检查
// 结果做了 3 秒短缓存（防止探针流量在抖动时把数据库打趴），并带 PID 与
// 启动时间，方便本地工具确认应答来自刚拉起的进程、而不是没绑上端口仍在前
// 台服务的旧实例。
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/migrations"
)

// readinessQuery 统计本二进制要求的迁移版本里有多少条已被记录为"已应用"。
// 之所以比较"数量"而不是只查一条"最新版本"行，是为了能发现中间缺了迁移
// （编号排在某个已应用的较新版本之下）的情况——单查最新会把它掩盖掉。
const readinessQuery = `SELECT COUNT(*) FROM schema_migrations WHERE version = ANY($1)`

// readinessCacheTTL 就绪结果的缓存有效期：探针通常几秒一轮，没必要每次都
// 真去查库。
const readinessCacheTTL = 3 * time.Second

// readinessDB 是就绪检查需要的数据库能力子集（ping + 行查询），抽象成接口
// 便于测试注入假实现。
type readinessDB interface {
	Ping(ctx context.Context) error
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// serverHealth 是健康探针背后的状态体：持有数据库句柄、要求应用的迁移版本
// 列表，以及就绪结果的短缓存。
type serverHealth struct {
	db                 readinessDB
	requiredMigrations []string
	initErr            error
	cacheTTL           time.Duration
	refreshMu          sync.Mutex
	cache              atomic.Pointer[cachedReadiness]
	// startedAt and pid identify the process answering /health. A 200 alone
	// only proves something is listening on the port: when a restart fails to
	// bind, the previous instance keeps serving and every readiness check
	// still passes, so a caller can configure or test the wrong build without
	// any visible error. Local tooling compares started_at against its own
	// launch time to prove the answer came from the process it just started.
	//
	// 中文说明：startedAt 和 pid 用来标识正在应答 /health 的到底是哪个进程。
	// 光一个 200 只能证明"端口上有东西在听"：重启时如果端口绑定失败，旧实例
	// 会继续服务，所有就绪检查依然通过——调用方可能在毫无提示的情况下对着
	// 错误的构建做配置或测试。本地工具会把 started_at 跟自己的拉起时间比对，
	// 以证明应答确实来自它刚启动的进程。
	startedAt time.Time
	pid       int
}

// cachedReadiness 是一次就绪检查结果的缓存条目。
type cachedReadiness struct {
	response   readinessResponse
	statusCode int
	expiresAt  time.Time
}

// liveResponse 是存活探针的应答体。PID/Commit/StartedAt 用 omitempty，
// 在没有对应信息时保持 JSON 精简。
type liveResponse struct {
	Status    string `json:"status"`
	PID       int    `json:"pid,omitempty"`
	Commit    string `json:"commit,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
}

// readinessResponse 是就绪探针的应答体，Checks 里逐项给出检查结论。
type readinessResponse struct {
	Status string          `json:"status"`
	Checks readinessChecks `json:"checks"`
}

// readinessChecks 逐项记录各依赖的检查状态。
type readinessChecks struct {
	DB         string `json:"db"`
	Migrations string `json:"migrations"`
}

// newServerHealth 组装健康检查器：枚举出本二进制要求的全部迁移版本，记录
// 启动时间与 PID；迁移版本枚举失败时会把错误暂存到 initErr，就绪检查会
// 因此报不通过。
func newServerHealth(pool *pgxpool.Pool) *serverHealth {
	requiredMigrations, err := migrations.AllVersions()
	return &serverHealth{
		db:                 pool,
		requiredMigrations: requiredMigrations,
		initErr:            err,
		cacheTTL:           readinessCacheTTL,
		startedAt:          time.Now().UTC(),
		pid:                os.Getpid(),
	}
}

// liveHandler 应答存活探针：进程能跑起来就回 ok。
func (h *serverHealth) liveHandler(w http.ResponseWriter, _ *http.Request) {
	resp := liveResponse{Status: "ok", PID: h.pid, Commit: commit}
	if !h.startedAt.IsZero() {
		resp.StartedAt = h.startedAt.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

// readyHandler 应答就绪探针，按 readiness 的判定结果选择 200/503。
func (h *serverHealth) readyHandler(w http.ResponseWriter, r *http.Request) {
	resp, status := h.readiness(r.Context())
	writeJSON(w, status, resp)
}

// readiness 返回就绪判定与对应 HTTP 状态码。开了缓存时优先读缓存；缓存
// 失效则抢 refreshMu 后双检一次再真算（computeReadiness），避免探针并发
// 齐刷把数据库打满。
func (h *serverHealth) readiness(parent context.Context) (readinessResponse, int) {
	if h.cacheTTL <= 0 {
		return h.computeReadiness(parent)
	}

	now := time.Now()
	if cached := h.loadCachedReadiness(now); cached != nil {
		return cached.response, cached.statusCode
	}

	h.refreshMu.Lock()
	defer h.refreshMu.Unlock()

	now = time.Now()
	if cached := h.loadCachedReadiness(now); cached != nil {
		return cached.response, cached.statusCode
	}

	resp, status := h.computeReadiness(parent)
	h.cache.Store(&cachedReadiness{
		response:   resp,
		statusCode: status,
		expiresAt:  now.Add(h.cacheTTL),
	})
	return resp, status
}

// loadCachedReadiness 读缓存，未命中或已过期返回 nil。
func (h *serverHealth) loadCachedReadiness(now time.Time) *cachedReadiness {
	cached := h.cache.Load()
	if cached == nil || !now.Before(cached.expiresAt) {
		return nil
	}
	return cached
}

// computeReadiness 真正执行就绪判定（无缓存时），逐项检查：
//  1. 数据库句柄存在且能 Ping 通；
//  2. 启动时能成功枚举迁移版本列表；
//  3. 表 schema_migrations 里已应用的必需版本数达标。
//
// 任一不满足即整机判 not_ready 并回 503。数据库健康时仍对查询套 2 秒超时，
// 防止数据库假死把就绪请求拖死。
func (h *serverHealth) computeReadiness(parent context.Context) (readinessResponse, int) {
	resp := readinessResponse{
		Status: "ok",
		Checks: readinessChecks{
			DB:         "ok",
			Migrations: "ok",
		},
	}

	if h.db == nil {
		resp.Status = "not_ready"
		resp.Checks.DB = "error"
		resp.Checks.Migrations = "unknown"
		return resp, http.StatusServiceUnavailable
	}

	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()

	if err := h.db.Ping(ctx); err != nil {
		resp.Status = "not_ready"
		resp.Checks.DB = "error"
		resp.Checks.Migrations = "unknown"
		return resp, http.StatusServiceUnavailable
	}

	if h.initErr != nil || len(h.requiredMigrations) == 0 {
		resp.Status = "not_ready"
		resp.Checks.Migrations = "error"
		return resp, http.StatusServiceUnavailable
	}

	var appliedCount int
	if err := h.db.QueryRow(ctx, readinessQuery, h.requiredMigrations).Scan(&appliedCount); err != nil {
		resp.Status = "not_ready"
		resp.Checks.Migrations = "error"
		return resp, http.StatusServiceUnavailable
	}

	// version 是 schema_migrations 的主键，所以每个必需版本至多命中一行；
	// 计数小于必需总数，说明本二进制依赖的某个迁移还没应用。
	if appliedCount < len(h.requiredMigrations) {
		resp.Status = "not_ready"
		resp.Checks.Migrations = "out_of_date"
		return resp, http.StatusServiceUnavailable
	}

	return resp, http.StatusOK
}

// writeJSON 统一写 JSON 应答。先整体序列化到内存里再写，是为了能带上精确的
// Content-Length；若直接往 ResponseWriter 边编码边写，WriteHeader 之后就会
// 被迫走 chunked 传输、丢掉这个头。
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		body = []byte(`{"error":"failed to encode response"}`)
		status = http.StatusInternalServerError
	}
	body = append(body, '\n')
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
