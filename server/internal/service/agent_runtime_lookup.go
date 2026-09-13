// agent_runtime_lookup.go —— 统一且可归因的 agent_runtime 单行读取（MUL-6884）：
// 让生产代码经单一类型读行并带上来源统计，使 “agent_runtime 是系统最忙读取之一” 可回答是哪种产品行为所致。
package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RuntimeLookup 是生产代码按 id 读取单条 agent_runtime 行的唯一途径（MUL-6884）。
//
// 查询本身是主键点读、从来不是问题。缺的是归因：daemon 心跳、浏览器轮询与十多个就绪门槛都发同一条语句，
// 于是 pg_stat_statements 只会报告 agent_runtime 是系统最忙的读取之一，却说不清是哪个产品行为驱动。把每次
// 读取都经同一类型路由并携带来源，才能在任何人改动心跳间隔之前回答这个问题。
//
// Source 是 metrics 包的闭枚举（obsmetrics.RuntimeLookupSource*）。Metrics 可 nil——测试与自托管部署没有
// 指标监听器时照常运行，计数器按构造即是 best-effort。
type RuntimeLookup struct {
	// Queries 是读取所用的连接。事务内的调用方必须传事务的 queries 而非连接池的，
	// 使读取与事务其余部分看到同一快照。
	Queries *db.Queries
	Metrics *obsmetrics.BusinessMetrics
	Source  string
}

// Get 读取一条 agent_runtime 行并计入该 lookup 来源的计数器。
//
// 行与错误原样返回：调用方本就区分 pgx.ErrNoRows（runtime 已删除——daemon 读作“丢弃你的本地注册”）与瞬时
// DB 故障；在这里合并两者会把一次抖动变成一次虚假的自愈。
func (l RuntimeLookup) Get(ctx context.Context, id pgtype.UUID) (db.AgentRuntime, error) {
	rt, err := l.Queries.GetAgentRuntime(ctx, id)
	l.Metrics.RecordAgentRuntimeLookup(l.Source, runtimeLookupResult(err))
	return rt, err
}

// runtimeLookupResult 把 Get 的错误归类为指标结果：无错→OK，未找到→NotFound，其余→Error。
func runtimeLookupResult(err error) string {
	switch {
	case err == nil:
		return obsmetrics.RuntimeLookupResultOK
	case errors.Is(err, pgx.ErrNoRows):
		return obsmetrics.RuntimeLookupResultNotFound
	default:
		return obsmetrics.RuntimeLookupResultError
	}
}

// runtimeLookup（IssueService）返回来源为 issue 的 lookup，绑定到调用方连接，使创建事务内的读取仍留在事务内。
func (s *IssueService) runtimeLookup(q *db.Queries) RuntimeLookup {
	return RuntimeLookup{Queries: q, Metrics: s.Metrics, Source: obsmetrics.RuntimeLookupSourceIssue}
}

// runtimeLookup（TaskService）返回来源为 task 的 lookup，供分析上下文与用量 provider 回填使用。
func (s *TaskService) runtimeLookup() RuntimeLookup {
	return RuntimeLookup{Queries: s.Queries, Metrics: s.Metrics, Source: obsmetrics.RuntimeLookupSourceTask}
}

// runtimeLookup（AutopilotService）返回来源为 autopilot 的 lookup。AutopilotService 没有自己的指标采集器，
// 复用任务服务的（router 从同一注册表注入）。
func (s *AutopilotService) runtimeLookup() RuntimeLookup {
	var m *obsmetrics.BusinessMetrics
	if s.TaskSvc != nil {
		m = s.TaskSvc.Metrics
	}
	return RuntimeLookup{Queries: s.Queries, Metrics: m, Source: obsmetrics.RuntimeLookupSourceAutopilot}
}
