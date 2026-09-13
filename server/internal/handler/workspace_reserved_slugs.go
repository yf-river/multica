// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（workspace_reserved_slugs.go）维护"系统保留的 workspace 名字
// （slug）名单"。前端 URL 形态是 /{workspaceSlug}/...，若某个 workspace 的
// slug 撞上顶层路由或系统功能名，页面会路由错乱，因此这份名单在创建
// workspace 时用来拒绝这些名字。
package handler

import (
	_ "embed"
	"encoding/json"
)

// reservedSlugs 里装的是会与前端顶层路由、平台功能或 web 标准冲突的
// workspace slug。名单在编译期从 reserved_slugs.json 里加载（见
// loadReservedSlugs），是创建 workspace 时的一票否决名单。

//go:embed reserved_slugs.json
var reservedSlugsJSON []byte

// reservedSlugs 是包级一次性解析好的"保留 slug → true"查找表，供
// isReservedSlug 直接命中判断。
//
// 名单真源是仓库里的 reserved_slugs.json（编译期嵌入），与 TypeScript 侧
// 共享同一份数据。要增删保留名只许改 JSON；前端侧对应的
// packages/core/paths/reserved-slugs.ts 由 `pnpm generate:reserved-slugs`
// 重新生成，CI 会检查两侧是否漂移。
var reservedSlugs = loadReservedSlugs()

// reservedSlugsFile 对应 reserved_slugs.json 的顶层结构：JSON 里是 groups
// 数组，每个分组内是一批 slugs。
type reservedSlugsFile struct {
	Groups []struct {
		Slugs []string `json:"slugs"`
	} `json:"groups"`
}

// loadReservedSlugs 解析嵌入的 JSON，把全部分组的 slug 摊平成一张
// map[string]bool 查找表，供后续 O(1) 判断。
func loadReservedSlugs() map[string]bool {
	var data reservedSlugsFile
	if err := json.Unmarshal(reservedSlugsJSON, &data); err != nil {
		// reserved_slugs.json 入库并被编译进二进制，解析失败属于编程错误。若
		// 让它拖到第一个创建 workspace 的请求才暴露就太晚了——那些本该被拒的
		// slug 可能已经被建出来。所以直接 panic，让二进制拒绝启动。
		panic("handler: parse reserved_slugs.json: " + err.Error())
	}
	out := make(map[string]bool)
	for _, g := range data.Groups {
		for _, slug := range g.Slugs {
			out[slug] = true
		}
	}
	return out
}

// isReservedSlug 判断 slug 是否命中保留名单——命中即不能用作 workspace 名。
func isReservedSlug(slug string) bool {
	return reservedSlugs[slug]
}
