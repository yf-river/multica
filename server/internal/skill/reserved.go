package skill

import (
	"path/filepath"
	"strings"
)

// ContentFilename 是 skill 主内容（Content 字段）的规范文件名。daemon 准备执行
// 环境时会把 Content 写到这个路径，因此它是保留名：支持文件不能再占用它。
const ContentFilename = "SKILL.md"

// IsReservedContentPath 判断 p 是否指向保留的主内容文件（SKILL.md）。
//
// 比较前先 clean 路径，这样非规范写法如 "./SKILL.md" 或 "sub/../SKILL.md"——
// filepath.Join 依然会落到 daemon 自己写入的那个 SKILL.md 上——也能被拦下。
// 若用精确字符串比较，它们会同时绕过 API 守卫与 daemon 守卫，重复写入会让任务
// 准备阶段以 errPathPreExists 失败（在 nil-manifest 路径上则直接覆盖主内容）。
// 比较不区分大小写，与本包及 daemon 其余处理 SKILL.md 的方式保持一致。
func IsReservedContentPath(p string) bool {
	return strings.EqualFold(filepath.Clean(p), ContentFilename)
}
