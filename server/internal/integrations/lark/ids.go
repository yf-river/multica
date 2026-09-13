// ids.go：uuid 展示的小工具。包内日志与用 uuid 做 map 键的地方共用。
package lark

import (
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
)

// uuidString 把 pgtype.UUID 渲染成其规范字符串形态，供包内日志与 map 键共用。
// （原定义在 hub.go 上，channel-engine 切换后被移除。）
func uuidString(u pgtype.UUID) string { return util.UUIDToString(u) }
