// Package dbid 为应用插入的行铸造主键。
//
// 这里产出的每个 id 都是 UUIDv7（RFC 9562）：48 位大端毫秒时间戳后跟随机位。
// 因此连续插入会聚集在窄的连续键区间里，而不是像随机 v4 键那样散落在主键 B 树上。
// 对已含 v4 id 的表来说，该区间未必是树的右缘。
//
// 适用范围与使用规则：
//
//   - 只用于"我们插入并保留的行"的标识列——任务队列条目、消息、评论、活动与审计行。
//     不用于 lease/claim 令牌、幂等键或任何当机密用的东西：那些需要不可预测性且不能
//     内嵌时间戳，所以继续用 gen_random_uuid()/v4。
//   - 这些列上的库端 `DEFAULT gen_random_uuid()` 保留不变，每个从这里取 id 的查询
//     都用 `COALESCE(sqlc.narg('id')::uuid, gen_random_uuid())` 包一层。因此表里
//     永远混着 v4 与 v7 id——Postgres 的 uuid 类型与本仓库所有解析器都接受——未设的
//     id 退化为今天的行为而不是违反 NOT NULL。
//   - 库端兜底决定了 NewV7 只适合"id 仅用作插入行的身份"的场景。如果应用还必须把
//     id 当对象键、文件名、关联 id 或任何外部引用，请直接调 uuid.NewV7 并处理其错误；
//     否则随机源失败后数据库可能铸造出不同的 id。
//   - v7 内嵌的创建时间只有毫秒精度，且跨写者只是近似有序。它不能替代 created_at，
//     也不得用于推导排序保证。
//   - 在 INSERT ... ON CONFLICT 里，传入的 id 只在行真的被插入时生效；冲突的行保留
//     自己的 id。调用方必须从 RETURNING 读回 id，而不是假定自己铸造的值赢了。
package dbid

import (
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// NewV7 返回一个新的 UUIDv7，包装成 pgtype.UUID，可直接赋给 sqlc 生成参数结构体
// 的 ID 字段。
//
// 刻意没有 error 返回。uuid.NewV7 只会在系统熵源出问题时失败，健康主机不会出现；
// 万一真的失败，这里返回零值（NULL）pgtype.UUID，查询的
// COALESCE(..., gen_random_uuid()) 兜底会让 Postgres 代为铸造 id。这让插入继续工作，
// 而不是为一个 id 风格问题拒绝用户请求，也让约 60 处调用点省掉一条永远不会走的
// 错误路径。
func NewV7() pgtype.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		slog.Error("dbid: uuidv7 generation failed, falling back to the database default", "error", err)
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: [16]byte(id), Valid: true}
}
