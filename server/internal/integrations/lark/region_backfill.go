// region_backfill.go：自托管升级修复路径——把“曾以部署级 URL 覆盖跑在 Lark
// 国际版”的旧安装行的地域补标成 lark。
package lark

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
)

// BackfillRegionFromLegacyOverride 是为自托管部署准备的升级修复路径。这类部署
// 在“按安装记录地域”字段出现之前，整条 Lark 集成都是经部署级
// MULTICA_LARK_HTTP_BASE_URL / MULTICA_LARK_CALLBACK_BASE_URL 覆盖跑在 Lark
// 国际版的。
//
// Migration 116 把所有已存在的行回填成了 'feishu'（中国大陆默认值）。但这类
// 部署上的每个安装其实都是 Lark：一旦地域开始驱动主机，这些行会路由到
// open.feishu.cn——等操作者按新文档清掉覆盖后就立刻坏掉。因此只要两个覆盖
// 主机之一是 Lark 国际版主机，就把这些仍是默认值的行翻成 'lark'。
//
// 用覆盖作为闸门是安全的：覆盖本来就是部署级的，所以它下面的每个存量安装都
// 是 Lark——不存在会被误分类的混合状态。它是幂等的（翻完之后不会再剩
// 'feishu'），而且新安装已经带着设备流探测出的地域，所以这里只会碰到遗留行。
// 中国大陆部署（无覆盖，或覆盖指向 open.feishu.cn / mock）永远不执行 UPDATE。
//
// 调用方应在启动时像 BackfillBotUnionIDs 一样用 goroutine 触发，避免慢 DB 写
// 阻塞监听器启动。
func BackfillRegionFromLegacyOverride(ctx context.Context, queries *ChannelStore, httpOverride, callbackOverride string, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	if queries == nil {
		return
	}
	if !isLarkInternationalHost(httpOverride) && !isLarkInternationalHost(callbackOverride) {
		// 无覆盖、中国大陆覆盖或 mock/staging 主机：migration 的 'feishu'
		// 默认值对这些行就是对的。
		return
	}
	n, err := queries.BackfillLarkInstallationRegionToLark(ctx)
	if err != nil {
		log.Warn("lark region backfill: relabel legacy Lark-international installs failed", "err", err)
		return
	}
	if n > 0 {
		log.Info("lark region backfill: relabelled legacy Lark-international installs to region=lark",
			"rows", n)
	}
}

// isLarkInternationalHost 判断某个配置的 base-URL 覆盖是否指向 Lark 国际版
// 开放平台主机（open.larksuite.com）。它解析 URL 并精确匹配 host，保证中国
// 大陆主机、空值或 staging/mock URL 永不触发这次升级重贴。
func isLarkInternationalHost(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, "open.larksuite.com")
}
