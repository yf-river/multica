// 本文件实现一条「owner_id 是否本库真实用户」的查询函数，供 Auth / DaemonAuth 的
// mcn_ 分支在验签成功、准备缓存结果之前确认：Cloud 说这个 owner_id 归谁，得先核对它
// 确实映射到本地已知用户，否则一律拒绝。
package middleware

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ownerLookupFor 返回一个 auth.OwnerLookupFunc：用生成的 GetUser 查询确认 `ownerID`
// 是否是我们 user 表里的真实一行。供 Auth 与 DaemonAuth 的 mcn_ 分支使用——
// 在验证器返回成功/缓存结果之前，先确认 Cloud 的 owner_id 确实映射到本地已知用户。
//
// 行为约定：
//   - queries==nil → 返回 nil（无查询能力；验证器跳过第 3 步）。只有测试里为验证器
//     接线但不接真库时会走到这条路径；
//   - GetUser 命中 → (true, nil)：owner_id 是已知用户；
//   - pgx.ErrNoRows → (false, nil)：owner_id 未知。验证器把它映射成
//     ErrCloudPATInvalid（reason "owner_unknown"）且不缓存；
//   - 其它错误 → (_, err)：视为基础设施故障；验证器映射成 ErrCloudPATUnavailable，
//     中间件据此返回 503（瞬时错误，可重试）。
//
// UUID 解析走 util.ParseUUID：畸形输入返回零值 UUID。零值 UUID 匹配不到任何真实行，
// 因此随后的 GetUser 会干净地落到 (false, nil)、请求被拒——这里无需再做一层
// 「长得像不像 UUID」的预检。Cloud 在签署 verify 响应前已核实过格式。
func ownerLookupFor(queries *db.Queries) auth.OwnerLookupFunc {
	if queries == nil {
		return nil
	}
	return func(ctx context.Context, ownerID string) (bool, error) {
		uuid, err := util.ParseUUID(ownerID)
		if err != nil {
			// Cloud 返回了解析不成 UUID 的东西：这是契约违约，不是瞬时故障——
			// 像任何 owner_unknown 一样拒绝该 token。
			return false, nil
		}
		_, err = queries.GetUser(ctx, uuid)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
}
