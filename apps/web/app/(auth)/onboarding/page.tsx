"use client";

/**
 * 首次用户 onboarding 流程的 Web 外壳：负责登录守卫、完成后的路由跳转，
 * 内容渲染共享的 <OnboardingFlow />。
 */
import { useEffect, useRef } from "react";
import { useRouter } from "next/navigation";
import { useAuthStore } from "@multica/core/auth";
import {
  paths,
  resolvePostAuthDestination,
  useHasOnboarded,
} from "@multica/core/paths";
import { useWorkspaceList } from "@multica/core/workspace";
import { CliInstallInstructions, OnboardingFlow } from "@multica/views/onboarding";

/**
 * onboarding 流程的 Web 外壳。这个路由在 Web 端充当平台外壳（对应桌面的 `WindowOverlay`），
 * 内容则是共享的 `<OnboardingFlow />`。保持极简——做登录守卫、渲染、退出。
 *
 * 接了 runtime 的 onboarding 会打开最后一步创建并启动的 Mika 会话；
 * 其他退出路径落到 workspace 的 issues 列表，没有 workspace 时落到根路径。
 *
 * `CliInstallInstructions` 作为 `runtimeInstructions` 插槽传入，
 * 让流程能把它渲染在 CLI 对话框里。它展示的命令是写死的——没有环境变量需要穿透。
 *
 * @returns onboarding 流程页面。
 */
export default function OnboardingPage() {
  const router = useRouter();
  const user = useAuthStore((s) => s.user);
  const isLoading = useAuthStore((s) => s.isLoading);
  const hasOnboarded = useHasOnboarded();
  const { workspaces, ready: workspacesReady } = useWorkspaceList({
    enabled: !!user,
  });
  // bootstrap 路径在返回前会调用 refreshMe()，这会在页面还挂着时就把 hasOnboarded 翻成 true。
  // 没有这个标记，下面的守卫会跟 onComplete 抢跑：守卫的 router.replace（去 issues 列表）
  // 可能抢在 onComplete 的 router.push（去引导 issue）之前，把用户丢到错误的目的地。
  // 在 onComplete 导航前先把页面标成「正在完成」，就能让守卫在途中的跳转保持静默。
  const completingRef = useRef(false);

  useEffect(() => {
    if (isLoading || !user) {
      if (!isLoading && !user) router.replace(paths.login());
      return;
    }
    if (!workspacesReady) return;
    if (completingRef.current) return;
    // 只有当 onboarding 确实不适用时才弹走：也就是用户已经 onboard 了。
    // 这里故意不根据 `workspaces.length` 弹走——流程会在 onboarding 中途创建 workspace，
    // 如果这里按 hasWorkspaces 弹走，用户还没跑完 runtime 和 Mika 设置就被踢出去了。
    // 「登录后该去哪」的判断已经由 callback / login 里的新入口逻辑处理，
    // 所以 OnboardingPage 不再需要自己去猜。
    if (hasOnboarded) {
      router.replace(resolvePostAuthDestination(workspaces, hasOnboarded));
    }
  }, [isLoading, user, hasOnboarded, workspacesReady, workspaces, router]);

  if (isLoading || !user || hasOnboarded) return null;

  // 布局：本页自己负责滚动（根布局为 app-shell 约定设置了 `body { overflow: hidden }`）。
  // OnboardingFlow 在内部管理每步的宽度约束——Welcome 渲染一个宽的两栏 hero，
  // 其余步骤自己包在 max-w-xl 里。
  return (
    <div className="h-full overflow-y-auto bg-background">
      <OnboardingFlow
        onComplete={(ws, destination) => {
          completingRef.current = true;
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
