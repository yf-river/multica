// union_id_backfill.go：给缺失 bot_union_id 的存量安装做一次性回填。
package lark

import (
	"context"
	"log/slog"
	"time"
)

// BackfillBotUnionIDs 遍历每条 bot_union_id 为 NULL 的活跃 lark_installation
// 行，调用 APIClient.GetBotInfo 抓取并持久化它。这是为 migration 112 之前创建
// 的安装准备的迁移胶水——见 MUL-2671 群聊 @ 分流。新安装已在设备流 finalize
// 时写入 bot_union_id，所以这是一次性桥接，不是常驻任务。
//
// 行为：
//
//   - 函数是 best-effort 且按安装幂等的：某行的瞬时 HTTP / 解密 / DB 错误不会
//     阻塞后续行，也不会中断服务器启动。每次结果都带安装 id 记日志，操作者可
//     在发布后审计覆盖情况。
//
//   - union_id 缺失时软失败（app 联系人范围受限时飞书会回 code=0 但 union_id
//     为空）：记日志继续；decoder 的过渡期 open_id 兜底让该安装在单 Bot 部署下
//     保持可用。
//
//   - 调用方应在启动时放独立 goroutine 触发，避免一次慢的 Lark 往返阻塞 HTTP
//     监听器启动。回填在行与行之间尊重 ctx 取消。
//
// 调度：router 在每个进程启动时调用一次。没有理由按清扫节奏重复——一旦
// bot_union_id 非 NULL，decoder 就直接用它；重装路径已通过 UpsertLarkInstallation
// 覆盖两个标识。
func BackfillBotUnionIDs(
	ctx context.Context,
	queries *ChannelStore,
	api APIClient,
	creds CredentialsDecrypter,
	log *slog.Logger,
) {
	if log == nil {
		log = slog.Default()
	}
	if api == nil || !api.IsConfigured() {
		log.Info("lark backfill: APIClient not configured; skipping union_id backfill")
		return
	}
	rows, err := queries.ListActiveLarkInstallations(ctx)
	if err != nil {
		log.Warn("lark backfill: list installations failed", "err", err)
		return
	}
	var attempted, filled, missed, errored int
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		if row.BotUnionID.Valid && row.BotUnionID.String != "" {
			continue
		}
		attempted++
		secret, err := creds.DecryptAppSecret(row)
		if err != nil {
			log.Warn("lark backfill: decrypt app_secret failed",
				"installation_id", uuidString(row.ID),
				"app_id", row.AppID,
				"err", err)
			errored++
			continue
		}
		// 给 Lark 往返加时限，避免某条安装行卡死回填 goroutine。10 秒与 http
		// client 的 defaultRequestTimeout 对齐。
		fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		info, err := api.GetBotInfo(fetchCtx, InstallationCredentials{
			AppID:     row.AppID,
			AppSecret: secret,
			TenantKey: row.TenantKey.String,
			Region:    RegionOrDefault(row.Region),
		})
		cancel()
		if err != nil {
			log.Warn("lark backfill: GetBotInfo failed",
				"installation_id", uuidString(row.ID),
				"app_id", row.AppID,
				"err", err)
			errored++
			continue
		}
		if info.UnionID == "" {
			log.Warn("lark backfill: union_id absent in Lark response; leaving NULL",
				"installation_id", uuidString(row.ID),
				"app_id", row.AppID,
				"bot_open_id", string(info.OpenID))
			missed++
			continue
		}
		if err := queries.SetLarkInstallationBotUnionID(ctx, SetInstallationBotUnionIDParams{
			ID:         row.ID,
			BotUnionID: textOrNull(info.UnionID),
		}); err != nil {
			log.Warn("lark backfill: persist union_id failed",
				"installation_id", uuidString(row.ID),
				"err", err)
			errored++
			continue
		}
		filled++
		log.Info("lark backfill: stamped union_id",
			"installation_id", uuidString(row.ID),
			"app_id", row.AppID,
			"bot_open_id", string(info.OpenID))
	}
	log.Info("lark backfill: union_id pass complete",
		"attempted", attempted,
		"filled", filled,
		"missed", missed,
		"errored", errored)
}

// CredentialsDecrypter 是 BackfillBotUnionIDs 从 InstallationService 需要的
// 窄接口。在这里定义（而不是直接 import 具体 *InstallationService），是为了让
// 回填能用“返回固定明文”的 stub 做单元测试，不必拉起整套 secretbox 机制。
type CredentialsDecrypter interface {
	DecryptAppSecret(inst Installation) (string, error)
}
