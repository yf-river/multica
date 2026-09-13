"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { paths } from "@multica/core/paths";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import { CliInstallInstructions, OnboardingFlow } from "@multica/views/onboarding";

/**
 * 新建 workspace 的路由：复用 onboarding 流程，直接从 workspace 创建步骤进入。
 *
 * 创建 workspace 会走一遍 onboarding 流程，入口落在 workspace 那一步。
 * 第二个 workspace 需要的东西跟第一个完全一样——连一个 runtime、还有它自己的 Mika——
 * 所以唯一的区别就是这位成员已经认识产品了。共用同一套流程才能让两条路径不会各自漂移。
 *
 * `/onboarding` 保留自己的守卫、只服务首次用户；本路由是「已 onboard 用户」的显式入口，
 * 因此无需绕过那个守卫。
 *
 * @returns 新建 workspace 的 onboarding 页面。
 */
export default function Page() {
  const router = useRouter();
  const user = useAuthStore((s) => s.user);
  const isLoading = useAuthStore((s) => s.isLoading);
  const { data: wsList = [] } = useQuery({
    ...workspaceListOptions(),
    enabled: !!user,
  });

  useEffect(() => {
    if (!isLoading && !user) router.replace(paths.login());
  }, [isLoading, user, router]);

  if (isLoading || !user) return null;

  // 取消时去根路径——workspace 布局会从那里再重定向到用户的默认 workspace。
  // 只有「有地方可回」时才提供取消；一个 workspace 都没有的用户必须走完流程。
  const onCancel =
    wsList.length > 0 ? () => router.push(paths.root()) : undefined;

  return (
    <div className="h-full overflow-y-auto bg-background">
      <OnboardingFlow
        mode="new_workspace"
        onCancel={onCancel}
        onComplete={(ws, destination) => {
          if (ws && destination?.kind === "chat") {
            router.push(
              paths.workspace(ws.slug).chatSession(destination.sessionId),
            );
          } else if (ws && destination?.kind === "issue") {
            router.push(
              paths.workspace(ws.slug).issueDetail(destination.issueId),
            );
          } else if (ws) {
            router.push(paths.workspace(ws.slug).issues());
          } else {
            router.push(paths.root());
          }
        }}
        runtimeInstructions={<CliInstallInstructions />}
      />
    </div>
  );
}
