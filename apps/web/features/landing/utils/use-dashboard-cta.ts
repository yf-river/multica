"use client";

import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceList } from "@multica/core/workspace";
import {
  paths,
  resolvePostAuthDestination,
  useHasOnboarded,
} from "@multica/core/paths";
import type { Workspace } from "@multica/core/types";

// 兜底地址：workspace 列表还没返回时 CTA 先指向 /issues——proxy 会把它改写为
// 最近访问的 workspace。这样 hydration 期间按钮照常可点，不用隐藏或禁用（避免闪烁）。
// 没有任何 workspace 历史的新访客在这个窗口内会先落到落地页，等列表就绪后即可再点一次。
const LOADING_FALLBACK_HREF = "/issues";

/**
 * 登录用户在官网点击的「进入控制台」CTA 的真实去向（纯函数，便于测试）。
 *
 * 规则：未登录 → 登录页；workspace 列表还没就绪 → 先指向 /issues（见上方说明）；
 * 就绪后 → 交给与 RedirectIfAuthenticated 相同的解析器决定落点，保证
 * 「控制台在哪里」只有一处来源。
 *
 * @param isAuthenticated - 是否已登录。
 * @param workspaceListReady - workspace 列表是否已加载完成。
 * @param workspaces - 已加载的 workspace 列表。
 * @param hasOnboarded - 用户是否已完成 onboarding。
 * @returns CTA 应跳转的路径。
 */
export function resolveDashboardCtaHref({
  isAuthenticated,
  workspaceListReady,
  workspaces,
  hasOnboarded,
}: {
  isAuthenticated: boolean;
  workspaceListReady: boolean;
  workspaces: Workspace[];
  hasOnboarded: boolean;
}): string {
  if (!isAuthenticated) return paths.login();
  if (!workspaceListReady) return LOADING_FALLBACK_HREF;
  return resolvePostAuthDestination(workspaces, hasOnboarded);
}

/**
 * 官网「进入控制台」CTA 的去向 hook（在头部与页脚等多处使用）。
 *
 * 历史背景：这些 CTA 曾指向 `/`，指望 proxy 把登录访客从根路径弹回他们的 workspace；
 * 但官方营销域名保持 `/` 公开后这个「弹跳」不再发生，CTA 解析到的正是访客已停留的页面——
 * 点击毫无效果。因此改为在这里解析真实落点，并与 RedirectIfAuthenticated 共用同一解析器，
 * 让「控制台在哪」只有一处定义。
 *
 * 与 RedirectIfAuthenticated 共享 workspaceListOptions() 的 query key，
 * 落地页上该列表通常已在请求中或被缓存，本 hook 不会额外发请求。
 *
 * @returns CTA 应跳转的路径字符串。
 */
export function useDashboardCtaHref(): string {
  const user = useAuthStore((s) => s.user);
  const hasOnboarded = useHasOnboarded();

  const { workspaces, ready } = useWorkspaceList({ enabled: !!user });

  return resolveDashboardCtaHref({
    isAuthenticated: !!user,
    workspaceListReady: ready,
    workspaces,
    hasOnboarded,
  });
}
