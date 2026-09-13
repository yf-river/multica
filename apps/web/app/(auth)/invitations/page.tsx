"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { useAuthStore } from "@multica/core/auth";
import { paths } from "@multica/core/paths";
import { InvitationsPage } from "@multica/views/invitations";

/**
 * 批量邀请页路由：仅做登录守卫，然后渲染共享的 <InvitationsPage />。
 *
 * @returns 邀请列表页面。
 */
export default function InvitationsRoutePage() {
  const router = useRouter();
  const user = useAuthStore((s) => s.user);
  const isLoading = useAuthStore((s) => s.isLoading);

  // 未登录用户在这里没有有意义的落点——先送去登录再带回来。
  // 登录页最终会跑它自己的 listMyInvitations() 检查，再把他们路由回这里。
  useEffect(() => {
    if (!isLoading && !user) {
      router.replace(
        `${paths.login()}?next=${encodeURIComponent(paths.invitations())}`,
      );
    }
  }, [isLoading, user, router]);

  if (isLoading || !user) return null;

  return <InvitationsPage />;
}
