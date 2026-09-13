// autopilot_quota_reconciler.go
//
// 职责：后台定时对账"自动驾驶（autopilot）配额预留"。
// 自动驾驶任务会以"并发名额（quota）"的形式限制同一时间能跑多少任务，任务
// 派发前先在表里预留一个名额。正常情况下任务结束会立刻释放；但进程崩溃、
// 手动/API 派发没有重试方等场景会让预留名额一直挂着，占住名额不放。这个
// reconciler 每 60 秒扫一次，把"挂得太久、显然不会再有人接手"的预留结清
// （settle）掉，保证名额能循环利用。真正的结清逻辑在 AutopilotService 里，
// 本文件只管节奏、参数与日志。
package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
)

const (
	autopilotQuotaReconcileInterval   = time.Minute
	autopilotQuotaTerminalRecoveryAge = 10 * time.Minute
	// Manual/API dispatches have no durable retry owner. Six hours is far
	// beyond normal dispatch latency while still releasing a genuinely
	// abandoned slot before the entitlement period rolls over.
	//
	// 中文说明：手动或 API 触发的派发没有"可靠重试"的归属方，一旦失败没人会
	// 来收尾。6 小时远超正常派发延迟，但又赶在配额周期（entitlement period）
	// 翻篇之前释放掉真正被遗弃的名额——既不会误伤还在路上的慢任务，也不会让
	// 死名额占满整个周期。
	autopilotQuotaPartialRecoveryAge = 6 * time.Hour
	autopilotQuotaReconcileBatch     = 100
)

// runAutopilotQuotaReconciler 启动配额对账循环，直到 ctx 取消才退出。
// 每轮先结算：凡超过 terminalRecoveryAge 都没动静的预留视为终结（彻底释放），
// 凡超过 partialRecoveryAge 的按部分恢复处理；每批最多处理 ReconcileBatch 条。
// ctx 取消时不写告警日志，避免优雅退出时刷一堆噪音。
func runAutopilotQuotaReconciler(ctx context.Context, svc *service.AutopilotService) {
	ticker := time.NewTicker(autopilotQuotaReconcileInterval)
	defer ticker.Stop()
	for {
		now := time.Now()
		if settled, err := svc.ReconcileAutopilotQuotaReservations(
			ctx,
			now.Add(-autopilotQuotaTerminalRecoveryAge),
			now.Add(-autopilotQuotaPartialRecoveryAge),
			autopilotQuotaReconcileBatch,
		); err != nil {
			if ctx.Err() == nil {
				slog.Warn("autopilot quota reconciler failed", "error", err)
			}
		} else if settled > 0 {
			slog.Info("autopilot quota reconciler settled reservations", "count", settled)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
