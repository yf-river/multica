package service

import "context"

// releaseClaimsBlocked 是服务端发布窗口屏障，工作区共用同一服务环境。
func (s *TaskService) releaseClaimsBlocked(ctx context.Context) (bool, error) {
	if s.TxStarter == nil {
		return false, nil
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var blocked bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM release_window WHERE state <> 'finished')`).Scan(&blocked)
	return blocked, err
}
