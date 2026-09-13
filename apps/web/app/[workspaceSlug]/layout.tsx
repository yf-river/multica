"use client";

/**
 * 工作区级布局（`/{workspaceSlug}/*` 的守卫层）：负责登录检查、onboarding 检查、
 * 按 slug 解析 workspace、把「当前工作区」同步给全局单例与 `last_workspace_slug`
 * cookie，并把子页面包进 WorkspaceSlugProvider。
 */
import { use, useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { useRouter, usePathname } from "next/navigation";
import { WorkspaceSlugProvider, paths } from "@multica/core/paths";
import { workspaceBySlugOptions } from "@multica/core/workspace";
import { setCurrentWorkspace } from "@multica/core/platform";
import { useAuthStore } from "@multica/core/auth";
import { NoAccessPage } from "@multica/views/workspace/no-access-page";
import { WelcomeAfterOnboarding } from "@multica/views/workspace/welcome-after-onboarding";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { useWorkspaceSeen } from "@multica/views/workspace/use-workspace-seen";
import { workspaceSlugFromPathname } from "@/lib/workspace-slug-from-pathname";

/**
 * 工作区布局。
 *
 * @param props.children - 工作区下各页面内容。
 * @param props.params - 动态参数，含 workspace slug（Promise 形式）。
 * @returns 认证守卫后的工作区内容（加载态 / 无权限页 / 正常布局）。
 */
export default function WorkspaceLayout({
  children,
  params,
}: {
  children: React.ReactNode;
  params: Promise<{ workspaceSlug: string }>;
}) {
  const { workspaceSlug } = use(params);
  const user = useAuthStore((s) => s.user);
  const isAuthLoading = useAuthStore((s) => s.isLoading);
  const router = useRouter();
  const pathname = usePathname();

  // 工作区路由要求已登录。未登录时（首次访问无会话、token 过期、另一个标签页登出等）
  // 一律弹回 /login。否则本布局渲染 null，用户会卡在 /{slug}/... 上的空白页。
  useEffect(() => {
    if (!isAuthLoading && !user) router.replace(paths.login());
  }, [isAuthLoading, user, router]);

  // 硬性 onboarding 闸门。已登录但 onboarded_at 为 NULL 表示用户绕过了 /onboarding
  // （直接输 URL、深链等），弹回去把问卷 + Step 3 走完。反向闸门在
  // `apps/web/app/(auth)/onboarding/page.tsx`：已 onboard 的用户访问 /onboarding
  // 会被弹回自己的工作区。这两个 effect 一起让 `onboarded_at` 成为
  // 「能否访问 /<slug>/*」的唯一依据。
  useEffect(() => {
    if (user && user.onboarded_at == null) {
      router.replace(paths.onboarding());
    }
  }, [user, router]);

  // 通过共享的 workspace 列表查询按 slug 解析 workspace：认证引导若已预热则复用缓存，
  // 冷启动的路由直接请求。在身份验证完成前保持禁用。
  const { data: workspace } = useQuery({
    ...workspaceBySlugOptions(workspaceSlug),
    enabled: !!user,
  });

  // 渲染期同步：把 URL 里的 slug 喂给 platform 单例，让第一个子查询的
  // X-Workspace-Slug 请求头从一开始就正确。setCurrentWorkspace 自带去重，
  // 并顺带执行 rehydrate；每次渲染调用都安全。
  //
  // 必须用当前 pathname 守卫：这是从 render 里写模块级全局，而 App Router 可能让
  // 上一个 workspace 的布局和即将进入的一起保持挂载。两个实例各自按自己的查询活动
  // 重新渲染，若无守卫双方会交替写入——每次渲染都把单例改成自己的 slug，无限循环。
  // 每翻转一次，realtime socket 就要拆了重建（packages/core/realtime/provider.tsx
  // 把它绑定到这个 slug），@提及查找也会跟着失效（它同步读同一个单例，所指向的
  // workspace 若没有热缓存就退回空列表——见 packages/views/editor/extensions/
  // mention-suggestion.tsx）。结果就是：用户正在看哪个 workspace，哪个的实时通道就
  // 断、提及列表就空。
  //
  // 与 pathname 比对即可解决，无需所有权握手：URL 是工作区身份的最终事实来源，
  // usePathname() 是所有已挂载实例共享的同一个值，所以恰好只有一个布局——真正被
  // 路由命中的那个——能定义它。桌面端因为「没有地址栏可参考」，需要自己的协议
  // （见 workspace-route-layout.tsx）。
  if (workspace && workspaceSlug === workspaceSlugFromPathname(pathname)) {
    setCurrentWorkspace(workspaceSlug, workspace.id);
  }

  // 写入 last_workspace_slug cookie——proxy 下次加载页面时读它，
  // 把「未带 slug 的 URL」重定向到用户最近访问的工作区。
  useEffect(() => {
    if (!workspace || typeof document === "undefined") return;
    const oneYear = 60 * 60 * 24 * 365;
    const secure = location.protocol === "https:" ? "; Secure" : "";
    document.cookie = `last_workspace_slug=${encodeURIComponent(workspaceSlug)}; path=/; max-age=${oneYear}; SameSite=Lax${secure}`;
  }, [workspace, workspaceSlug]);

  // 记住本会话中这个 slug 是否已成功解析过。用于避免「当前 workspace 被移除」
  // （删除、离开或实时踢出）时 NoAccessPage 闪烁——此时调用方几乎已在跳转，
  // 我们只需短暂返回 null。
  const hasBeenSeen = useWorkspaceSeen(workspaceSlug, !!workspace);

  const loadingIndicator = (
    <div className="flex h-svh items-center justify-center">
      <MulticaIcon className="size-6 animate-pulse" />
    </div>
  );

  if (isAuthLoading) return loadingIndicator;
  // workspace 解析完成前不渲染子内容。useWorkspaceId() 在列表未填充或 slug 未知时
  // 会抛错——在这里挡住，保证所有后代组件都能满足该不变量。
  // selector 在列表解析完成前（含首次请求失败后）返回 undefined；只有「权威列表里
  // 确实没有这个 slug」时才返回 null。
  if (workspace === undefined) return loadingIndicator;
  if (workspace === null) {
    // 本会话此前解析过这个 slug：说明它刚被移出我们的列表（删除/离开/被踢）。
    // 跳转几乎必然在途中——返回 null 避免 NoAccessPage 闪一下。
    if (hasBeenSeen) return null;
    // 否则：URL 指向一个用户从未有权访问的 workspace。给出明确反馈而不是静默重定向。
    // 不区分 404 还是 403，避免让攻击者枚举 slug。
    return <NoAccessPage />;
  }

  return (
    <WorkspaceSlugProvider slug={workspaceSlug}>
      {children}
      {/* 读取 OnboardingFlow.handleRuntimeNext 临时停放的欢迎信号。
       * 走了运行时路径 → 加载遮罩 → 带 Helper 与入门卡片的模态框；
       * 跳过了 → 弹两个预置任务的模态框；没有信号 → 什么都不渲染。 */}
      <WelcomeAfterOnboarding />
    </WorkspaceSlugProvider>
  );
}
