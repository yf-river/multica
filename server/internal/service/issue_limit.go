// issue_limit.go —— Cloud 版按工作区 issue 数量上限的准入/计数逻辑：解析策略、只读预检与事务内串行分配。
package service

import (
	"context"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/entitlement"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// IssueLimitReachedError 把 Cloud 的有效上限与策略版本带给传输适配层。绝不含计划名或订阅状态。
type IssueLimitReachedError struct {
	Limit          int64
	PolicyRevision int64
}

// Error 实现 error 接口：格式化上限数。
func (e *IssueLimitReachedError) Error() string {
	return fmt.Sprintf("workspace issue limit reached: limit %d", e.Limit)
}

// IssueCountPolicy 是从 Cloud 收到的、经校验的机械指令。在开数据库事务【前】解析它，使网络调用永不持有产品
// 库锁。
type IssueCountPolicy struct {
	Action         entitlement.Action
	Limit          int64
	PolicyRevision int64
}

// ResolveIssueCountPolicy 调用 provider 的准入闸门并把决定折叠成 IssueCountPolicy：provider 缺失或工作区无效
// 时返回 ActionOff（不设限）；Enforce 而无正数上限也降级为 Off。
func ResolveIssueCountPolicy(ctx context.Context, provider entitlement.Provider, workspaceID pgtype.UUID) IssueCountPolicy {
	if provider == nil || !workspaceID.Valid {
		return IssueCountPolicy{Action: entitlement.ActionOff}
	}
	decision := provider.Gate(ctx, uuid.UUID(workspaceID.Bytes), entitlement.GateIssueCount)
	policy := IssueCountPolicy{
		Action:         decision.Gate.Action,
		PolicyRevision: decision.PolicyRevision,
	}
	if decision.Gate.Limit != nil {
		policy.Limit = int64(*decision.Gate.Limit)
	}
	switch policy.Action {
	case entitlement.ActionOff:
		policy.Limit = 0
	case entitlement.ActionEnforce:
		if policy.Limit > 0 {
			return policy
		}
		policy.Action = entitlement.ActionOff
		policy.Limit = 0
	default:
		policy.Action = entitlement.ActionOff
		policy.Limit = 0
	}
	return policy
}

// CheckIssueCreateCapacity 为“在 issue 事务开始前先做工作”的流程（如 agent quick create）执行只读准入预检。
// 它刻意不是预留：调用方仍须在最终创建事务里用 AllocateIssueNumber 对 workspace 行串行化并发准入。
func CheckIssueCreateCapacity(ctx context.Context, q *db.Queries, provider entitlement.Provider, workspaceID pgtype.UUID) error {
	policy := ResolveIssueCountPolicy(ctx, provider, workspaceID)
	if policy.Action != entitlement.ActionEnforce {
		return nil
	}
	used, err := CountIssueUsage(ctx, q, workspaceID, policy)
	if err != nil {
		return err
	}
	if used >= policy.Limit {
		return &IssueLimitReachedError{Limit: policy.Limit, PolicyRevision: policy.PolicyRevision}
	}
	return nil
}

// AllocateIssueNumber 在 workspace 行上串行化创建，再在同一事务内检查当前 issue 行数。调用方在收到
// IssueLimitReachedError 时必须回滚事务；这也一并回滚计数器增量。删除 issue 会释放容量，因为
// issue_counter 刻意不作为配额用量。
func AllocateIssueNumber(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, policy IssueCountPolicy) (int32, error) {
	number, err := q.IncrementIssueCounter(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	if policy.Action != entitlement.ActionEnforce {
		return number, nil
	}
	used, err := q.CountIssuesUpTo(ctx, db.CountIssuesUpToParams{
		WorkspaceID: workspaceID,
		Limit:       policy.Limit,
	})
	if err != nil {
		return 0, err
	}
	if used >= policy.Limit {
		return 0, &IssueLimitReachedError{Limit: policy.Limit, PolicyRevision: policy.PolicyRevision}
	}
	return number, nil
}

// CountIssueUsage 对受限策略做有界读取：低于上限时返回值精确，工作区满员后返回值被截到上限。
func CountIssueUsage(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, policy IssueCountPolicy) (int64, error) {
	if policy.Action != entitlement.ActionEnforce {
		return 0, nil
	}
	sampleLimit := policy.Limit
	if sampleLimit < math.MaxInt64 {
		sampleLimit++
	}
	sampled, err := q.CountIssuesUpTo(ctx, db.CountIssuesUpToParams{
		WorkspaceID: workspaceID,
		Limit:       sampleLimit,
	})
	if err != nil {
		return 0, err
	}
	if sampled > policy.Limit {
		return policy.Limit, nil
	}
	return sampled, nil
}
