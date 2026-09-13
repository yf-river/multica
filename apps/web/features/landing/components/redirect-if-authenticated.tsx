"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceList } from "@multica/core/workspace";
import { resolvePostAuthDestination, useHasOnboarded } from "@multica/core/paths";
import { isOfficialMarketingHost } from "@/lib/public-host";

/**
 * 已登录访客访问官网落地页时的客户端兜底重定向。
 *
 * 登录用户在应用域名访问 `/` 的主路径是服务端重定向（由 Next.js proxy/中间件
 * 依据 `last_workspace_slug` cookie 驱动，该 cookie 每次访问工作区都会更新）。
 * 但「首次登录」时——用户还没进过任何工作区——cookie 不存在，proxy 会落到落地页。
 * 本组件专门补上应用/自托管域名下的这个空档。
 *
 * 官方营销域名下 `/` 即使对登录用户也必须保持公开，显式的工作区路由仍可进入应用。
 *
 * 本组件不渲染任何内容；用 `router.replace` 让落地页不会进入已登录用户的浏览器历史。
 */
export function RedirectIfAuthenticated() {
  const router = useRouter();
  const user = useAuthStore((s) => s.user);
  const isLoading = useAuthStore((s) => s.isLoading);
  const hasOnboarded = useHasOnboarded();

  const { workspaces, ready } = useWorkspaceList({ enabled: !!user });

  useEffect(() => {
    if (isLoading || !user || !ready) return;
    if (isOfficialMarketingHost(window.location.hostname)) return;
    router.replace(resolvePostAuthDestination(workspaces, hasOnboarded));
  }, [isLoading, user, ready, workspaces, hasOnboarded, router]);

  return null;
}
