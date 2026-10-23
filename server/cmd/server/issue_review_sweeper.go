// 职责定位：「审核中」审查补扫的周期启动。判定与派发在 handler.SweepIssueReviews，
// 这里只负责节奏和串行化，避免补扫与 runtime 巡检互相拖慢。
package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/handler"
)

// issueReviewSweepInterval 是补扫间隔。补扫要覆盖三类漏单：交付入口没派发、审查入队
// 失败、进程重启丢掉的那一次。它们都不是毫秒级故障，两分钟足够快且不会放大开销。
const issueReviewSweepInterval = 2 * time.Minute

// 1. runIssueReviewSweeper 独立跑一轮补扫，见 runPeriodicSweep 的串行化说明。
func runIssueReviewSweeper(ctx context.Context, h *handler.Handler) {
	if h == nil || h.TaskService == nil {
		return
	}
	runPeriodicSweep(ctx, issueReviewSweepInterval, func() {
		stats := h.SweepIssueReviews(ctx)
		if stats.Dispatched > 0 {
			slog.Info("review sweep: dispatched missing reviews",
				"scanned", stats.Scanned,
				"dispatched", stats.Dispatched,
				"skipped", stats.Skipped,
			)
		}
	})
}
