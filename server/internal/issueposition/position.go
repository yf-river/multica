// Package issueposition 提供计算 issue 在列内手动排序位置的工具，让新 issue 能
// 排到列首（或其他前端要求的相对位置）。
package issueposition

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// queryRower 抽取"只需执行单行查询"的最小接口，便于测试传入桩实现。
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// NextTopPosition 返回一个能在"按 position 升序手动排序"时排在指定工作区/状态列中
// 所有现存 issue 之前的位置（即当前最小 position 再减 1）。
//
// queries/issue.sql 里的 UpdateIssue 与 UpdateIssueStatus 内联了同样的
// MIN(position) - 1 策略，用于把 issue 因状态变化移到另一列时重新定级。改动时
// 请两处一起改。
func NextTopPosition(ctx context.Context, q queryRower, workspaceID pgtype.UUID, status string) (float64, error) {
	var minPos float64
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(MIN(position), 0) FROM issue WHERE workspace_id = $1 AND status = $2`,
		workspaceID, status,
	).Scan(&minPos); err != nil {
		return 0, fmt.Errorf("query min issue position: %w", err)
	}
	return minPos - 1, nil
}
