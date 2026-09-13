"use client";

import { useCallback } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { clearWorkspaceStorage, defaultStorage } from "@multica/core/platform";
import { resetAllRegisteredDrafts } from "@multica/core/drafts/cleanup-registry";
import { paths } from "@multica/core/paths";
import type { Workspace } from "@multica/core/types";
import { useNavigation } from "../navigation";

/**
 * auth 模块：执行一次完整的退出登录——清空各工作区的客户端存储、遗留的
 * cookie、整个 React Query 缓存与内存中的 auth store，最后跳到 /login。
 * 把原本散在 app-sidebar 退出处理里的逻辑收拢成一个，让 NoAccessPage 的
 * 「以其他用户身份登录」以及任何未来的入口都能复用同一套流程。
 *
 * 没有这个统一退出，那些只 `navigate('/login')` 的调用方会保留 auth cookie +
 * React Query 缓存 + local storage——AuthInitializer 随后会在登录页静默地重新
 * 认证该用户，并把他们弹回原来的地方。
 */
export function useLogout() {
  const queryClient = useQueryClient();
  const authLogout = useAuthStore((s) => s.logout);
  const { push } = useNavigation();

  return useCallback(() => {
    // 先重置草稿 store 的内存状态，再删持久化的 key。每次重置都是 Zustand
    // 的 setState，persist 中间件会把新的（空）状态在仍生效的工作区 slug 下
    // 直接写回存储——所以在删除之后重置，会把刚删的 key 连同重置状态里仍
    // 带着的东西一起复活。无论下面的工作区列表如何，内存都必须被清空：
    // 客户端跳转到 /login 不重新加载页面，否则这些单例会在下一次登录后把
    // 上一个用户的草稿亮出来。
    resetAllRegisteredDrafts();

    // 然后清空该用户能访问的每个工作区的工作区级存储——必须先于清空 React
    // Query 缓存（它持有工作区列表）。否则各工作区的草稿/聊天等会泄漏给本机
    // 的下一个用户。
    const cachedWorkspaces =
      queryClient.getQueryData<Workspace[]>(workspaceKeys.list()) ?? [];
    for (const ws of cachedWorkspaces) {
      clearWorkspaceStorage(defaultStorage, ws.slug);
    }

    // 清掉 last-workspace-slug cookie。否则共享设备上，下一个用户会被代理
    // 重定向到上一个用户最后的工作区，再被弹到 NoAccessPage——很困惑。
    if (typeof document !== "undefined") {
      document.cookie =
        "last_workspace_slug=; path=/; max-age=0; SameSite=Lax";
    }

    queryClient.clear();
    authLogout();

    // 显式跳转到 /login。authLogout() 只清状态不动 URL——没有这步，调用方
    // 可能停留在一个渲染 null（布局以 user 为门槛）的工作区 URL 上，用户就
    // 被卡在白页。
    push(paths.login());
  }, [queryClient, authLogout, push]);
}
