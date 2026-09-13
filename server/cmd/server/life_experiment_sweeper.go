// life_experiment_sweeper.go
//
// 职责：后台定时清理"生命实验"里超时未结束的轮次。
// 生命实验会开启多轮（round），如果某轮到期（比如设置了结束时间）却因为
// 异常或无人处理而一直停在运行态，这里会兜底把它置为已停止，避免状态永远
// 悬挂。实现方式很简单：起一个 30 秒一次的 ticker 循环，每次调数据库里
// 的批量 SQL 把"已过期但仍未停止"的轮次一次性收尾。
package main

import (
	"context"
	"log/slog"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// lifeExperimentSweepInterval 清扫周期：每 30 秒检查一次过期轮次。
const lifeExperimentSweepInterval = 30 * time.Second

// runLifeExperimentSweeper 启动清扫循环，直到 ctx 被取消才退出。
// 它自己不带退出的业务判断，只负责按固定节奏调用 stopExpiredLifeExperimentRounds。
func runLifeExperimentSweeper(ctx context.Context, queries *db.Queries) {
	ticker := time.NewTicker(lifeExperimentSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stopExpiredLifeExperimentRounds(ctx, queries)
		}
	}
}

// stopExpiredLifeExperimentRounds 执行一次清扫：把数据库中所有已过期但仍
// 处于运行状态的实验轮次标记为停止，并记录影响数量。批量语句由 SQL 层完成，
// 这里只负责调用与记日志，出错不致命（等下一个周期再试）。
func stopExpiredLifeExperimentRounds(ctx context.Context, queries *db.Queries) {
	rounds, err := queries.StopAllExpiredLifeExperimentRounds(ctx)
	if err != nil {
		slog.Warn("life experiment sweeper: failed to stop expired rounds", "error", err)
		return
	}
	if len(rounds) > 0 {
		slog.Info("life experiment sweeper: stopped expired rounds", "count", len(rounds))
	}
}
