/**
 * 从当前 pathname 读出路由正命中的 workspace slug。
 *
 * 在 Web 上 URL 是工作区身份的最终来源，因此 `app/[workspaceSlug]/layout.tsx`
 * 在写入 platform workspace 单例前，会用本函数的结果与自己 params 里的 slug 比对。
 * usePathname() 对每个已挂载实例返回同一个值，这让「过期」的布局能识别出自己已
 * 不再是当前被路由命中的那一个。
 *
 * 根路径或没有第一段时返回 null。全局路由（/login、/workspaces/new）会正常返回其
 * 第一段——调用方是工作区布局，只会拿它与自己的 slug 比较，而保留 slug 永远不可能相等。
 */
export function workspaceSlugFromPathname(
  pathname: string | null | undefined,
): string | null {
  const segment = pathname?.split("/").filter(Boolean)[0];
  if (!segment) return null;
  // 路由参数是解码过的，而 pathname 段保持编码状态；不做这一步，需要转义的 slug
  // 永远无法比较相等。单独的 `%` 会直接抛错而不是解码，所以失败时退回原始段。
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
}
