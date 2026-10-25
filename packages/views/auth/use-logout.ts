"use client";

import { useCallback } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { clearClientSessionData } from "@multica/core/platform";
import { paths } from "@multica/core/paths";
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
    // Shared with the session-expiry path, which has to erase exactly the
    // same client-side state — see core's platform/session-cleanup for what
    // and why.
    clearClientSessionData(queryClient);
    authLogout();

    // 显式跳转到 /login。authLogout() 只清状态不动 URL——没有这步，调用方
    // 可能停留在一个渲染 null（布局以 user 为门槛）的工作区 URL 上，用户就
    // 被卡在白页。
    push(paths.login());
  }, [queryClient, authLogout, push]);
}
