// Package attributionbackfill 负责在迁移 198 执行校验之前，把 agent_task_queue
// 里的历史行修正到"人类归属"的严格约束上（先把历史欠账补上，否则数据库迁移会卡死）。
//
// 背景（GH #5544、MUL-4302、MUL-4897）。迁移 197 加了一个跨列的严格 CHECK 约束
//
//	originator_user_id IS NULL
//	  OR (accountable_user_id IS NOT NULL AND accountable_user_id = originator_user_id)
//
// （名为 agent_task_queue_accountable_matches_originator_strict，NOT VALID），迁移 198
// 会对它执行 VALIDATE CONSTRAINT。197 的注释假设违规的旧行"已在带外回填过"——这只对
// Multica 自家云上手动跑过回填的情况成立。自托管部署没跑过这一步，于是历史遗留行
// （originator_user_id 来自迁移 185 之前、或由旧版本滚动发布的后端写入，accountable_user_id
// 为 NULL）会让 198 直接失败（SQLSTATE 23514），后端拒绝启动。
//
// 这个问题没法靠"再加一个编号更高的迁移"解决：卡住的实例 197 已应用、198 失败，
// 永远不会执行到 198 之后的迁移。修复必须在 198 之前或当时进行。cmd/migrate 已提供
// preMigrationHook 机制（迁移 103 用过，MUL-2957），可在指定迁移的 SQL 前运行幂等逻辑；
// 本包就是 198 对应的这个 hook。
//
// 处理范围。严格约束只可能被 originator_user_id IS NOT NULL 的行破坏，而对这些行设计
// 不变量很明确：被确定的 originator 就是负责的人类，因此 accountable_user_id 必须等于
// originator_user_id（MUL-4302 §1/§11）。所以本 hook 只把这类行的 originator_user_id
// 镜像到 accountable_user_id，并在 originator_source 为 NULL 处打上 'backfill' 标记——
// 恰好做到让 VALIDATE 通过所需的一切，无需任何人工操作。
//
// originator_user_id 为 NULL 的行（autopilot 的 rule_owner / owner_fallback，或完全无归属
// 的历史数据）本就满足严格约束，保持不动——从触发链补齐其 accountable_user_id 属于审计
// 完整性工作，不阻塞启动，故意不放在这条"失败即关闭"的路径里。
package attributionbackfill

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultBatchSize 限制每条 UPDATE 重写的行数，保证对大表 agent_task_queue 的
// 修正不会用一条超长事务 / 长时间锁。每批在连接池上自动提交；部分进度会在下一批
// （或下次 migrate 运行）自然续上，因为已修正的行不再命中挑选条件。
const DefaultBatchSize = 5000

// Result 报告一次 Hook 运行的结果。导出以便 cmd/migrate 和测试打印或断言。
type Result struct {
	// RowsBackfilled 所有批次里从 originator_user_id 镜像到 accountable_user_id 的总行数。
	RowsBackfilled int64
	// Batches 执行的 UPDATE 批次数（无需修复时为 0）。
	Batches int
	// MismatchNormalized 运行前 originator_user_id 与 accountable_user_id 都非空但不相同的行数。
	// 按设计本不该存在（迁移 190 的过渡约束自上线起就拒绝所有带来源标签的这种写入）；
	// 单独报出该计数作为告警，让真正归属错误的行可见，尽管不变量强制 accountable 跟随 originator。
	MismatchNormalized int64
}

// HookOptions 控制 Hook 的行为。零值即可用于生产；测试按需覆盖字段。
type HookOptions struct {
	// Logger 接收遍历过程的 slog 记录。nil = slog.Default()。
	Logger *slog.Logger
	// BatchSize 覆盖 DefaultBatchSize。<= 0 时使用 DefaultBatchSize。
	BatchSize int
	// Table 覆盖目标表名。空 = "agent_task_queue"。测试可指向一次性夹具表；生产留空。
	// 该值会被拼进 SQL，调用方必须传可信标识符（绝不能是用户输入）——生产传常量，
	// 测试传自己的 schema 限定的夹具表名。
	Table string
}

// countMismatchSQL 统计两个归属用户都设置了但不一致的行——即真正值得告警的"错误归属"形态。
const countMismatchSQL = `
SELECT count(*)
FROM %s
WHERE originator_user_id IS NOT NULL
  AND accountable_user_id IS NOT NULL
  AND accountable_user_id <> originator_user_id`

// backfillBatchSQL 把一有界批次里违规行的 originator_user_id 镜像到
// accountable_user_id，并只在 originator_source 为 NULL 时打上 'backfill' 标记
// （COALESCE 保留已有更精确的来源如 'direct_human'）。被选中的行 originator_user_id
// 都非空，因此 `accountable_user_id IS DISTINCT FROM originator_user_id` 能同时覆盖
// accountable 为 NULL 的遗留行和两边都设置了但不相同的行。修正后的行不再命中条件，
// 循环必然终止，重复执行是空操作。
//
// 并发安全（纵深防御——migrate 循环通常在后端停机时运行，但 hook 跑在连接池上，
// 滚动发布或运维手动跑 migrate 可能与在线写入重叠）。有两个防护关闭"过期挑选"竞态，
// 即某行被选为违规、但在被重写前被并发写者改成合法的 originator-NULL 分叉：
//
//   - CTE 里的 FOR UPDATE 锁住每个候选行，READ COMMITTED 下拿到锁后会按最新已提交
//     版本重新评估条件——已变为 originator-NULL 的行会从批次中剔除，而不是被盲写。
//   - 外层 UPDATE 对 q 重复同样的条件，即使行在选中之后发生变化，也不会写一个已不再
//     需要修正的行。
//
// 没有这些防护，`SET accountable_user_id = q.originator_user_id` 碰到刚被写者改成
// (originator NULL, accountable 已设) 的行，会把写者的 accountable 值用 NULL 覆盖掉。
const backfillBatchSQL = `
WITH batch AS (
    SELECT id
    FROM %s
    WHERE originator_user_id IS NOT NULL
      AND accountable_user_id IS DISTINCT FROM originator_user_id
    LIMIT $1
    FOR UPDATE
)
UPDATE %s q
SET accountable_user_id = q.originator_user_id,
    originator_source   = COALESCE(q.originator_source, 'backfill')
FROM batch
WHERE q.id = batch.id
  AND q.originator_user_id IS NOT NULL
  AND q.accountable_user_id IS DISTINCT FROM q.originator_user_id`

// Hook 是迁移期的入口，在迁移 198 的 VALIDATE 之前被调用。它幂等、可安全重试：
// 修正所有会违反 agent_task_queue_accountable_matches_originator_strict 的行，
// 返回 nil，让迁移循环继续走到 VALIDATE。
//
// 只有当 SQL 语句本身失败时才返回错误（例如遍历中数据库不可达）；这会在
// schema_migrations 记录 198 之前中止 migrate 运行，因此同一版本（含 hook）会在下次
// 调用时干净重试。
//
// hook 跑在连接池上（每批自动提交），而不是迁移循环固定的锁连接上，这与
// taskusagebackfill 的 hook 约定一致。迁移循环已持有 migrationAdvisoryLockKey，
// 且 migrate 在后端停机时运行，因此没有并发写者与这些行竞争。
func Hook(ctx context.Context, pool *pgxpool.Pool, opts HookOptions) (Result, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	table := opts.Table
	if table == "" {
		table = "agent_task_queue"
	}

	var res Result

	// 先把真正错误归属的行（两个用户都设置但不相同）报出来。下面的不变量仍会强制
	// accountable 跟随 originator，但运维 / 审计轨迹应能看到该计数。
	if err := pool.QueryRow(ctx, fmt.Sprintf(countMismatchSQL, table)).Scan(&res.MismatchNormalized); err != nil {
		return res, fmt.Errorf("count attribution mismatches: %w", err)
	}
	if res.MismatchNormalized > 0 {
		log.Warn("attribution backfill: normalizing rows where accountable_user_id disagreed with a non-NULL originator_user_id; originator is authoritative but these are worth auditing",
			"mismatch_rows", res.MismatchNormalized)
	}

	updateSQL := fmt.Sprintf(backfillBatchSQL, table, table)
	for {
		tag, err := pool.Exec(ctx, updateSQL, batchSize)
		if err != nil {
			return res, fmt.Errorf("backfill accountable_user_id batch: %w", err)
		}
		n := tag.RowsAffected()
		if n == 0 {
			break
		}
		res.RowsBackfilled += n
		res.Batches++
		log.Info("attribution backfill: batch reconciled",
			"rows", n,
			"total", res.RowsBackfilled)
	}

	if res.RowsBackfilled == 0 {
		log.Info("attribution backfill: no rows needed reconciliation before migration 198")
	} else {
		log.Info("attribution backfill: complete",
			"rows_backfilled", res.RowsBackfilled,
			"batches", res.Batches,
			"mismatch_normalized", res.MismatchNormalized)
	}
	return res, nil
}
