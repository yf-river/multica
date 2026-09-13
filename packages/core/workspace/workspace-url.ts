// 本文件解析工作区 URL 前缀里展示的「品牌域名」。

// 托管版 Multica Cloud 上作为工作区 URL 前缀展示的品牌域名，也是部署没有暴露
// 任何 app URL 时的兜底。`/api/config` 对托管云刻意省略 `daemon_app_url`
// （对任何没设 MULTICA_APP_URL / FRONTEND_ORIGIN 的自托管也一样），所以这个
// 字面量必须作为最终兜底。
const BRAND_WORKSPACE_HOST = "multica.ai";

/**
 * 在创建工作区与引导 UI 里，作为 `<host>/<slug>` 工作区 URL 前缀渲染的域名。
 *
 * 由部署的 app URL（`/api/config` 的 `daemon_app_url`，经 config store 暴露）
 * 推导，让自托管实例显示自己的域名而不是 `multica.ai`。未配置 app URL 时
 * 回退到品牌域名。
 *
 * @param daemonAppUrl - 部署的 app URL（可为空）
 * @returns 要展示的域名
 */
export function workspaceUrlHost(
  daemonAppUrl: string | null | undefined,
): string {
  const trimmed = daemonAppUrl?.trim();
  if (!trimmed) return BRAND_WORKSPACE_HOST;
  try {
    return new URL(trimmed).host || BRAND_WORKSPACE_HOST;
  } catch {
    // `daemon_app_url` 可能不带 scheme 到达；把它当裸域名处理，剥掉任何
    // 路径/查询/片段，只留 authority 部分。
    const bare = trimmed
      .replace(/^.*?:\/\//, "")
      .replace(/[/?#].*$/, "")
      .trim();
    return bare || BRAND_WORKSPACE_HOST;
  }
}
