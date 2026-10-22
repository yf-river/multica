package service

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// releaseClaimsBlockedQuery 是发布窗口屏障的唯一判据，读取路径（认领）与写路径
// （begin/abandon）必须一致，否则过期窗口会以不同标准被当成存在或不存在。
// 超过 2 小时未更新的窗口（发布器异常退出、暂停确认丢失、现场文件丢失）不再阻塞认领，
// 避免只能改库恢复；正常发布从 begin 到 resume 远短于这个时长。
const releaseClaimsBlockedQuery = `SELECT EXISTS(SELECT 1 FROM release_window WHERE state <> 'finished' AND updated_at > now() - interval '2 hours')`

// releaseClaimsBlocked 是服务端发布窗口屏障，工作区共用同一服务环境。
func (s *TaskService) releaseClaimsBlocked(ctx context.Context) (bool, error) {
	if s.TxStarter == nil {
		return false, nil
	}
	// 1. 屏障是每次认领（daemon 秒级轮询）都要跑的只读判定，改成单条查询，
	// 不再为它单独开一个事务；构造器传进来的通常是连接池，可直接查询。
	if rower, ok := s.TxStarter.(interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}); ok {
		var blocked bool
		err := rower.QueryRow(ctx, releaseClaimsBlockedQuery).Scan(&blocked)
		return blocked, err
	}
	// 2. 只有事务工厂而非连接池时回落到事务路径，语义与上面完全一致。
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var blocked bool
	err = tx.QueryRow(ctx, releaseClaimsBlockedQuery).Scan(&blocked)
	return blocked, err
}
