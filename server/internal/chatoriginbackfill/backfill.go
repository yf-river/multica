// Package chatoriginbackfill 给"旧一代第一方 Chat"打上由频道路由迁移引入的
// 持久 origin 信号（即把创建来源标识回填到历史数据里）。
package chatoriginbackfill

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultBatchSize 限制每条语句的大小，避免在一条事务里重写整张 chat_session 表。
const DefaultBatchSize = 1000

// Result 报告一次完整遍历的统计结果。
type Result struct {
	// RowsVisited 扫描过的总行数。
	RowsVisited int64
	// RowsBackfilled 实际补上 explicitly_created_at 的总行数。
	RowsBackfilled int64
	// Pages 遍历了多少页。
	Pages int
}

// HookOptions 的零值即可用于生产。Table 覆盖项仅用于让集成测试把 hook 指向隔离的
// schema；调用方必须提供可信且已加引号的标识符。
type HookOptions struct {
	Logger       *slog.Logger
	BatchSize    int
	SessionTable string
	BindingTable string
	MessageTable string
}

// 每页沿着 chat_session 主键推进，而不是反复扫描已访问过的行。更新依然基于"来源证据"：
// 只要存在一条绑定记录或任何 channel 摄入的消息，就足以让旧 Chat 维持"频道来源"分类，
// 即使其中一条记录后来被删除也不会误判。
const backfillPageSQL = `
WITH page AS MATERIALIZED (
    SELECT id
    FROM %s
    WHERE ($1::uuid IS NULL OR id > $1::uuid)
    ORDER BY id
    LIMIT $2
), updated AS (
    UPDATE %s AS session
    SET explicitly_created_at = session.created_at
    FROM page
    WHERE session.id = page.id
      AND session.explicitly_created_at IS NULL
      AND NOT EXISTS (
          SELECT 1
          FROM %s AS binding
          WHERE binding.chat_session_id = session.id
      )
      AND NOT EXISTS (
          SELECT 1
          FROM %s AS message
          WHERE message.chat_session_id = session.id
            AND message.channel_ingested
      )
    RETURNING session.id
)
SELECT
    COALESCE((SELECT id::text FROM page ORDER BY id DESC LIMIT 1), ''),
    (SELECT count(*) FROM page),
    (SELECT count(*) FROM updated)`

// Hook 以短小、幂等的分页方式遍历旧 Chat。迁移运行器只在迁移 420 已新增
// explicitly_created_at 之后、且常规部署约定保持后端停机时调用它。某批失败时，
// 已更新的行保持有效，下次 migrate 会从主键游标安全续跑。
func Hook(ctx context.Context, pool *pgxpool.Pool, opts HookOptions) (Result, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	sessionTable := opts.SessionTable
	if sessionTable == "" {
		sessionTable = "chat_session"
	}
	bindingTable := opts.BindingTable
	if bindingTable == "" {
		bindingTable = "channel_chat_session_binding"
	}
	messageTable := opts.MessageTable
	if messageTable == "" {
		messageTable = "chat_message"
	}

	query := fmt.Sprintf(backfillPageSQL, sessionTable, sessionTable, bindingTable, messageTable)
	var result Result
	var cursor any
	for {
		var nextCursor string
		var visited, updated int64
		if err := pool.QueryRow(ctx, query, cursor, batchSize).Scan(&nextCursor, &visited, &updated); err != nil {
			return result, fmt.Errorf("backfill explicit Chat origin page: %w", err)
		}
		if visited == 0 {
			break
		}

		result.Pages++
		result.RowsVisited += visited
		result.RowsBackfilled += updated
		cursor = nextCursor
		log.Info("Chat origin backfill: page complete",
			"visited", visited,
			"backfilled", updated,
			"total_visited", result.RowsVisited,
			"total_backfilled", result.RowsBackfilled)
	}

	log.Info("Chat origin backfill: complete",
		"visited", result.RowsVisited,
		"backfilled", result.RowsBackfilled,
		"pages", result.Pages)
	return result, nil
}
