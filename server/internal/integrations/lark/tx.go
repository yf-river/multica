// tx.go：抽象“开启数据库事务”的能力。
package lark

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// TxStarter 抽象事务的创建。在本包内重声明（而非依赖 internal/service），
// 使 integrations 层不至于反向引用 service——两个包都在长大，要避免这种
// 循环依赖。*pgxpool.Pool 满足该接口。
type TxStarter interface {
	// Begin 开启一个新事务。
	Begin(ctx context.Context) (pgx.Tx, error)
}
