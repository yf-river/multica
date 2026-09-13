// db_ops.go 封装调度器对 sys_cron_executions 表的所有 SQL 原语：插入认领/窃取租约、
// 续期心跳、写成功/失败终态、读取某 (job, scope) 最近一次计划等。管理调度循环
// （manager.go）只依赖这些原语，本身不含任何 SQL。
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// ErrLeaseLost 由心跳 / 终态更新这类原语在「该行已不再归调用方 runner 所有」时返回。
// 处理器应当停下，调度器也不会为已丢失的租约写终态。
var ErrLeaseLost = errors.New("scheduler: lease lost")

// errTerminalIgnored 是 finishSuccess / finishFailure 用来表达「UPDATE 成功但影响 0 行」
// 的哨兵错误。仅本包内部使用。
var errTerminalIgnored = ErrLeaseLost

// dbNow 以 Postgres 的 now() 作为规范时钟。调度器对每个计划的运算与租约窗口都用
// 数据库时间，这样时钟偏斜的各实例对同一个 plan_time 依然能达成一致。
func dbNow(ctx context.Context, pool *pgxpool.Pool) (time.Time, error) {
	var t time.Time
	if err := pool.QueryRow(ctx, "SELECT now()").Scan(&t); err != nil {
		return time.Time{}, fmt.Errorf("scheduler: read db now: %w", err)
	}
	return t.UTC(), nil
}

// claim 是尝试获取某计划租约的结果。下面三个布尔结果同一时刻最多只有一个为真。
type claim struct {
	ID         uuid.UUID
	LeaseToken uuid.UUID
	Attempt    int
	Won        bool // 全新插入成功
	Stole      bool // 窃取陈旧租约，或从 FAILED 状态重试成功
	Conflicted bool // 其它 runner 已占该计划，或尝试次数已用尽
}

// tryClaim 尝试为 (job, scope, plan_time) 获取租约。
// 全新插入成功返回 Won=true；窃取陈旧租约或 FAILED 后重试成功返回 Stole=true；
// 以下情形返回 Conflicted=true：别的 runner 已拥有该计划、行已是 SUCCESS、FAILED
// 行还不到可重试时间（next_retry_at 在未来）、或尝试次数已用尽。调用方把所有
// Conflicted 结果一律同等对待（什么都不做）；调度器靠审计行的指标而不是每次调用的
// 返回值来区分它们。
func tryClaim(
	ctx context.Context,
	pool *pgxpool.Pool,
	job *JobSpec,
	scope Scope,
	planTime time.Time,
	dbTime time.Time,
	runnerID string,
) (claim, error) {
	// 全新插入路径。ON CONFLICT DO NOTHING 意味着没抢到的人不会碰到既有行——只有
	// 当这次插入发生冲突时，我们才继续走下面的窃取/重试路径。
	insertSQL := `
		INSERT INTO sys_cron_executions (
			job_name, scope_kind, scope_id, plan_time,
			status, attempt, max_attempts,
			runner_id, lease_token,
			heartbeat_at, stale_after,
			started_at, updated_at,
			id
		) VALUES (
			$1, $2, $3, $4,
			'RUNNING', 1, $5,
			$6, gen_random_uuid(),
			$7::timestamptz, $7::timestamptz + make_interval(secs => $8),
			$7::timestamptz, $7::timestamptz,
			COALESCE($9::uuid, gen_random_uuid())
		)
		ON CONFLICT ON CONSTRAINT uq_sys_cron_execution DO NOTHING
		RETURNING id, lease_token, attempt
	`
	staleSecs := int64(job.StaleTimeout / time.Second)
	if staleSecs <= 0 {
		staleSecs = 1
	}

	var c claim
	err := pool.QueryRow(ctx, insertSQL,
		job.Name, scope.Kind, scope.ID, planTime,
		job.MaxAttempts,
		runnerID,
		dbTime, staleSecs,
		// 执行行是只追加、高变更率的，所以主键 id 用 v7，保证主键的局部性。
		// lease_token 保持 gen_random_uuid()：它是认领令牌，不能携带可猜测的时间戳。
		dbid.NewV7(),
	).Scan(&c.ID, &c.LeaseToken, &c.Attempt)
	if err == nil {
		c.Won = true
		return c, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return claim{}, fmt.Errorf("scheduler: claim insert: %w", err)
	}

	// 冲突路径：用单条语句尝试「FAILED 后重试」或「窃取陈旧租约」。WHERE 子句
	// 同时编码两条分支，选哪条由服务端根据既有行决定。
	stealSQL := `
		UPDATE sys_cron_executions
		   SET status        = 'RUNNING',
		       attempt       = attempt + 1,
		       runner_id     = $1,
		       lease_token   = gen_random_uuid(),
		       heartbeat_at  = $2::timestamptz,
		       stale_after   = $2::timestamptz + make_interval(secs => $3),
		       started_at    = $2::timestamptz,
		       finished_at   = NULL,
		       duration_ms   = NULL,
		       next_retry_at = NULL,
		       error_code    = NULL,
		       error_msg     = NULL,
		       updated_at    = $2::timestamptz
		 WHERE job_name   = $4
		   AND scope_kind = $5
		   AND scope_id   = $6
		   AND plan_time  = $7
		   AND attempt < max_attempts
		   AND (
		        (status = 'FAILED' AND COALESCE(next_retry_at, $2::timestamptz) <= $2::timestamptz)
		        OR
		        (status = 'RUNNING' AND stale_after < $2::timestamptz AND $8)
		   )
		RETURNING id, lease_token, attempt
	`
	err = pool.QueryRow(ctx, stealSQL,
		runnerID,
		dbTime, staleSecs,
		job.Name, scope.Kind, scope.ID, planTime,
		job.AllowStaleReentry,
	).Scan(&c.ID, &c.LeaseToken, &c.Attempt)
	if err == nil {
		c.Stole = true
		return c, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return claim{}, fmt.Errorf("scheduler: claim steal: %w", err)
	}

	// 更新 0 行——要么别人持有一条新鲜的 RUNNING 租约，要么行已是 SUCCESS，要么
	// 行是 FAILED 但还不到可重试时间，要么尝试次数已用尽。调用方对这些「无事可做」
	// 的情形一视同仁。
	c.Conflicted = true
	return c, nil
}

// markStaleAsFailed 在任务 AllowStaleReentry=false 时，把单条陈旧的 RUNNING 行转成
// FAILED。返回影响行数，清扫器用它对日志记录关闭了多少行。
func markStaleAsFailed(
	ctx context.Context,
	pool *pgxpool.Pool,
	jobName string,
	dbTime time.Time,
) (int64, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE sys_cron_executions
		   SET status      = 'FAILED',
		       finished_at = $2,
		       error_code  = 'stale_timeout',
		       error_msg   = 'lease expired without heartbeat',
		       updated_at  = $2
		 WHERE job_name    = $1
		   AND status      = 'RUNNING'
		   AND stale_after < $2
	`, jobName, dbTime)
	if err != nil {
		return 0, fmt.Errorf("scheduler: mark stale failed: %w", err)
	}
	return tag.RowsAffected(), nil
}

// heartbeat 为持有租约的 runner 延长 stale_after。若行已不再属于我们（被窃走或已
// 终态），返回 ErrLeaseLost。
func heartbeat(
	ctx context.Context,
	pool *pgxpool.Pool,
	id, leaseToken uuid.UUID,
	staleTimeout time.Duration,
) error {
	staleSecs := int64(staleTimeout / time.Second)
	if staleSecs <= 0 {
		staleSecs = 1
	}
	tag, err := pool.Exec(ctx, `
		UPDATE sys_cron_executions
		   SET heartbeat_at = now(),
		       stale_after  = now() + make_interval(secs => $3),
		       updated_at   = now()
		 WHERE id          = $1
		   AND lease_token = $2
		   AND status      = 'RUNNING'
	`, id, leaseToken, staleSecs)
	if err != nil {
		return fmt.Errorf("scheduler: heartbeat: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// finishSuccess 写一条终态 SUCCESS 行。lease_token 守卫防止「已失去租约的前持有者」
// 覆盖新一次尝试的状态。
func finishSuccess(
	ctx context.Context,
	pool *pgxpool.Pool,
	id, leaseToken uuid.UUID,
	dbTime time.Time,
	durationMs int64,
	res HandlerResult,
) error {
	resultJSON, err := encodeResult(res.Result)
	if err != nil {
		return err
	}

	tag, err := pool.Exec(ctx, `
		UPDATE sys_cron_executions
		   SET status        = 'SUCCESS',
		       finished_at   = $3,
		       duration_ms   = $4,
		       rows_affected = $5,
		       result        = $6::jsonb,
		       error_code    = NULL,
		       error_msg     = NULL,
		       updated_at    = $3
		 WHERE id            = $1
		   AND lease_token   = $2
		   AND status        = 'RUNNING'
	`, id, leaseToken, dbTime, durationMs, res.RowsAffected, resultJSON)
	if err != nil {
		return fmt.Errorf("scheduler: finish success: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errTerminalIgnored
	}
	return nil
}

// finishFailure 写一条终态 FAILED 行。nextRetryAt 可为零值——表示无需重试（已达到
// max_attempts）。
func finishFailure(
	ctx context.Context,
	pool *pgxpool.Pool,
	id, leaseToken uuid.UUID,
	dbTime time.Time,
	durationMs int64,
	errorCode, errorMsg string,
	nextRetryAt time.Time,
) error {
	var nextRetry pgtype.Timestamptz
	if !nextRetryAt.IsZero() {
		nextRetry = pgtype.Timestamptz{Time: nextRetryAt, Valid: true}
	}

	if errorCode == "" {
		errorCode = "handler_error"
	}
	// 错误消息列有长度上限，超长截断以免写入失败。
	if len(errorMsg) > 4000 {
		errorMsg = errorMsg[:4000]
	}

	tag, err := pool.Exec(ctx, `
		UPDATE sys_cron_executions
		   SET status        = 'FAILED',
		       finished_at   = $3,
		       duration_ms   = $4,
		       next_retry_at = $5,
		       error_code    = $6,
		       error_msg     = $7,
		       updated_at    = $3
		 WHERE id            = $1
		   AND lease_token   = $2
		   AND status        = 'RUNNING'
	`, id, leaseToken, dbTime, durationMs, nextRetry, errorCode, errorMsg)
	if err != nil {
		return fmt.Errorf("scheduler: finish failure: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errTerminalIgnored
	}
	return nil
}

// encodeResult 把处理器的小型结构化结果序列化成可写进 JSONB 的字符串。空/为 nil 的
// map 编码成 `{}`，这样在没设置结果时仍保留列的默认值。
func encodeResult(in map[string]any) (string, error) {
	if len(in) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("scheduler: marshal result: %w", err)
	}
	// 防御性上限——让结果行大小可预测。更大的载荷应该走结构化日志。
	if len(b) > 16*1024 {
		return "", fmt.Errorf("scheduler: result payload too large (%d bytes); keep it small or use logs", len(b))
	}
	return string(b), nil
}

// LatestPlanInfo 返回 (job, scope) 最近一次已知的 plan_time，外加「追赶计划器」判断
// 同一 plan_time 是否仍可认领（FAILED 待重试）或已结束、下一 plan_time 应越过它所需
// 的字段。
//
// 导出它，是为了让 JobSpec.PlansForScope 钩子能看到与内置 Cadence 计划器同一视角——
// 尤其是用 Found / PlanTime 决定从哪恢复计划枚举，用 RetryEligible 决定是否把游标停在
// 一个 FAILED-待重试的 plan_time 上。
type LatestPlanInfo struct {
	Found       bool
	PlanTime    time.Time
	Status      string
	Attempt     int
	MaxAttempts int
	// NextRetryAt 在「行不在可重试状态」或「下次重试立即可行」（FAILED 但没配退避）时
	// 为零值（DB 里是 NULL）。
	NextRetryAt time.Time
}

// RetryEligible 报告最近存储的那行是否应该在下一 tick 仍按同一个 plan_time 处理。
// 对「还有剩余尝试次数、且 next_retry_at 已到」的 FAILED 行为真；every_plan 计划器
// 用这个判断把游标停在可重试的桶上，好让 tryClaim 的「从 FAILED 重试」分支能触发。
func (i LatestPlanInfo) RetryEligible(now time.Time) bool {
	if !i.Found {
		return false
	}
	if i.Status != "FAILED" {
		return false
	}
	if i.Attempt >= i.MaxAttempts {
		return false
	}
	if i.NextRetryAt.IsZero() {
		// COALESCE 语义：next_retry_at 为 NULL 表示「尽快」，也就是现在。
		return true
	}
	return !i.NextRetryAt.After(now)
}

// latestPlan 查询 (job, scope) 最近一次已存储计划的相关字段，供追赶计划器使用。
func latestPlan(
	ctx context.Context,
	pool *pgxpool.Pool,
	jobName string,
	scope Scope,
) (LatestPlanInfo, error) {
	var info LatestPlanInfo
	var nextRetry pgtype.Timestamptz
	err := pool.QueryRow(ctx, `
		SELECT plan_time, status, attempt, max_attempts, next_retry_at
		  FROM sys_cron_executions
		 WHERE job_name   = $1
		   AND scope_kind = $2
		   AND scope_id   = $3
		 ORDER BY plan_time DESC
		 LIMIT 1
	`, jobName, scope.Kind, scope.ID).Scan(
		&info.PlanTime, &info.Status,
		&info.Attempt, &info.MaxAttempts,
		&nextRetry,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return info, nil
		}
		return info, fmt.Errorf("scheduler: read latest plan: %w", err)
	}
	info.Found = true
	info.PlanTime = info.PlanTime.UTC()
	if nextRetry.Valid {
		info.NextRetryAt = nextRetry.Time.UTC()
	}
	return info, nil
}
