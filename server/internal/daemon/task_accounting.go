package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

const interruptedResultGrace = 5 * time.Second
const taskAccountingTimeout = 5 * time.Second

// backend 收到取消后可回传已观测用量与会话；不配合退出时不能无限占住任务执行槽。
func awaitInterruptedBackendResult(results <-chan agent.Result) (agent.Result, bool) {
	timer := time.NewTimer(interruptedResultGrace)
	defer timer.Stop()
	select {
	case result, ok := <-results:
		return result, ok
	case <-timer.C:
		return agent.Result{}, false
	}
}

// 用真实 daemon 客户端上报，取消执行的 ctx 不能取消记账；两类请求共用一个总时限。
func (d *Daemon) reportTaskAccounting(ctx context.Context, taskID, provider string, result TaskResult, taskLog *slog.Logger) {
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskAccountingTimeout)
	defer cancel()
	if len(result.Usage) > 0 {
		if err := d.client.ReportTaskUsage(reportCtx, taskID, result.Usage); err != nil {
			taskLog.Warn("report task usage failed", "error", err)
		}
	}
	if entries := buildTaskTraceEntries(provider, result.ProviderTraceIDs); len(entries) > 0 {
		if err := d.client.ReportTaskTraces(reportCtx, taskID, entries); err != nil {
			taskLog.Warn("report task traces failed", "error", err)
		}
	}
}
