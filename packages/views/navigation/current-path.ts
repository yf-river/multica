import type { NavigationAdapter } from "./types";

/**
 * navigation 模块：把适配器的当前位置重建为单个应用内路径——
 * `/pathname?search#fragment`——也就是 `getShareableUrl()` 期望的形式。
 *
 * 任何要把「用户在哪」转成链接的调用方都必须走这里。只拼接 `pathname` +
 * `searchParams` 会悄悄丢掉 fragment，把 `#comment-…` 这样的深链降级成整个
 * issue 的地址。
 */
export function currentPath(
  navigation: Pick<NavigationAdapter, "pathname" | "searchParams" | "hash">,
): string {
  const search = navigation.searchParams.toString();
  return `${navigation.pathname}${search ? `?${search}` : ""}${navigation.hash}`;
}
