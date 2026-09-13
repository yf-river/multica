// 本文件是两个搜索 handler（SearchIssues / SearchProjects，见各自所在文件）共用的
// 底层设施：给搜索 SQL 一条短命只读事务 + Postgres 层语句超时作为安全网，并提供
// 超时判定。
//
// 搜索是 LOWER(col) LIKE '%pattern%' 形态，快路径依赖 pg_bigm / pg_trgm 的 GIN 索引。
// 扩展缺失时 Postgres 退化成对 issue 的顺序扫描加对 comment 的关联扫描，大 workspace
// 上会让前端 Loader2 像永远卡住（MUL-4059）。3 秒的语句超时对已正确建索引的搜索
// （通常 <50ms）足够宽裕，又短到前端浏览器的默认请求超时（约 30s）不会先触发；
// 超时时调用方得到 503 而非吊死连接。
package handler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// searchStatementTimeout 在 Postgres 层给每个 /search 请求设置语句超时上限。
//
// 两个搜索 handler（SearchIssues、SearchProjects）跑 LOWER(col) LIKE '%pattern%'
// 查询，其快路径依赖 pg_bigm / pg_trgm GIN 索引（见 migrations 032、033、036、
// 137–142）。扩展缺失时——migration 137 之前每个用随附 pgvector/pgvector:pg17
// 镜像的自托管部署都是如此——Postgres 退化成对 `issue` 的顺序扫描 + 对 `comment`
// 的关联顺序扫描。数千行的 workspace 上查询慢到前端 Loader2 看起来像永远卡住
// （"搜索卡死没有任何反应"，MUL-4059）。
//
// 3 秒上限对正确建索引的搜索（通常 <50ms）足够宽裕，又短到前端隐式请求超时
// （浏览器默认约 30s）不会先触发。超时后调用方看到带描述的 503 而不是吊死的连接
// ——SearchIssues / SearchProjects 把 SQLSTATE 57014 映射成
// http.StatusServiceUnavailable，让前端能把它与普通 500 区分开。
const searchStatementTimeout = 3 * time.Second

// searchStatementTimeoutOverride 非零时在测试期间替换 searchStatementTimeout。只能在
// runSearchQuery 热路径之外读取——见 search_timeout_test.go。
var searchStatementTimeoutOverride time.Duration

// effectiveSearchStatementTimeout 返回当前生效的搜索超时（测试覆盖优先）。
func effectiveSearchStatementTimeout() time.Duration {
	if searchStatementTimeoutOverride > 0 {
		return searchStatementTimeoutOverride
	}
	return searchStatementTimeout
}

// runSearchQuery 在一条短命只读事务里执行搜索 SQL，用 SET LOCAL statement_timeout
// 作安全网。rowsFn 接收每个 pgx.Rows 结果、负责在返回前扫描/累积结果；runSearchQuery
// 负责提交/回滚并返回遇到的第一个错误。
//
// 事务取 IsoLevel ReadCommitted（Postgres 默认）+ AccessMode ReadOnly，卡死的搜索
// 不会持有行锁挡着写方。
func runSearchQuery(
	ctx context.Context,
	txStarter txStarter,
	sql string,
	args []any,
	rowsFn func(pgx.Rows) error,
) error {
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin search tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// 尽力回滚时用全新 context——即使调用方已取消，也要让连接干净归还。
			_ = tx.Rollback(context.Background())
		}
	}()

	// SET LOCAL 是事务作用域的，所以 COMMIT 之后 pgxpool 可以安全地把这条连接交回
	// 池，超时不会泄漏到无关查询上。
	timeoutMs := int(effectiveSearchStatementTimeout() / time.Millisecond)
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", timeoutMs)); err != nil {
		return fmt.Errorf("set search statement_timeout: %w", err)
	}
	// 只读模式在这里用 SET 而非 TxOptions，是为了保持 txStarter 接口签名（只有
	// Begin）。属于双保险——搜索查询本来就只 SELECT。
	if _, err := tx.Exec(ctx, "SET LOCAL transaction_read_only = on"); err != nil {
		return fmt.Errorf("set search transaction_read_only: %w", err)
	}

	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	// 提交前先关 rows，pgx 才不会在 Commit 时报「连接正忙」。
	if err := rowsFn(rows); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

// isSearchStatementTimeout 判断 err 是否为 Postgres 标准的 query_canceled 错误
// （SQLSTATE 57014）。SET LOCAL statement_timeout 触发与客户端侧 context 取消都会以
// 57014 呈现——在客户端侧两者无法区分，这在 pgx 层是有意为之。
func isSearchStatementTimeout(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "57014"
	}
	return false
}
