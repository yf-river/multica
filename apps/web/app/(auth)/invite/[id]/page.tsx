"use client";

import { useEffect } from "react";
import { useRouter, useParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { paths } from "@multica/core/paths";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import { InvitePage } from "@multica/views/invite";

/**
 * 接受单个邀请的路由：根据 URL 里的邀请 id 渲染 <InvitePage />，
 * 未登录则先去登录，登录后跳回本页。
 *
 * @returns 邀请接受页面。
 */
export default function InviteAcceptPage() {
  const router = useRouter();
  const params = useParams<{ id: string }>();
  const user = useAuthStore((s) => s.user);
  const isLoading = useAuthStore((s) => s.isLoading);
  const { data: wsList = [] } = useQuery({
    ...workspaceListOptions(),
    enabled: !!user,
  });

  // 未登录就重定向到登录页，并带一个「登录后跳回本页」的参数。
  useEffect(() => {
    if (!isLoading && !user) {
      router.replace(
        `${paths.login()}?next=${encodeURIComponent(paths.invite(params.id))}`,
      );
    }
  }, [isLoading, user, router, params.id]);

  if (isLoading || !user) return null;

  const onBack =
    wsList.length > 0 ? () => router.push(paths.root()) : undefined;

  return <InvitePage invitationId={params.id} onBack={onBack} />;
}
