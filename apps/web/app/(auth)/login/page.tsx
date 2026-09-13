"use client";

/**
 * 登录页：渲染共享的 <LoginPage />，并负责登录成功后的落地路由——
 * 处理 `?next=` 跳转、CLI 授权回调、以及按用户 workspace 状态决定去向。
 */
import { Suspense, useEffect, useRef } from "react";
import { useSearchParams, useRouter } from "next/navigation";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { sanitizeNextUrl, useAuthStore } from "@multica/core/auth";
import {
  workspaceKeys,
  workspaceListOptions,
} from "@multica/core/workspace/queries";
import {
  paths,
  resolvePostAuthDestination,
  useHasOnboarded,
} from "@multica/core/paths";
import { api } from "@multica/core/api";
import type { Workspace } from "@multica/core/types";
import { setLoggedInCookie } from "@/features/auth/auth-cookie";
import { LoginPage, validateCliCallback } from "@multica/views/auth";

/**
 * 为「没有显式 `?next=` 的已登录用户」决定落点。
 * 尚未 onboard 且邮箱里有待处理邀请的用户，会被路由到批量 /invitations 页；
 * 其余用户走标准解析器。listMyInvitations 的网络抖动是非致命的——直接往下走，
 * 而不是把用户困在错误页。
 *
 * @param qc - React Query 客户端，用于把邀请列表写进缓存。
 * @param hasOnboarded - 用户是否已完成 onboarding。
 * @param workspaces - 当前用户的 workspace 列表。
 * @returns 解析出的目标路由。
 */
async function resolveLoggedInDestination(
  qc: QueryClient,
  hasOnboarded: boolean,
  workspaces: Workspace[],
): Promise<string> {
  if (!hasOnboarded) {
    try {
      const invites = await api.listMyInvitations();
      if (invites.length > 0) {
        qc.setQueryData(workspaceKeys.myInvitations(), invites);
        return paths.invitations();
      }
    } catch {
      // 忽略查询失败，继续往下走
    }
  }
  return resolvePostAuthDestination(workspaces, hasOnboarded);
}

/** 登录页内容主体：读取用户状态与查询参数，处理到达时已登录、以及表单登录成功两种场景。 */
function LoginPageContent() {
  const router = useRouter();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);
  const isLoading = useAuthStore((s) => s.isLoading);
  const searchParams = useSearchParams();

  const cliCallbackRaw = searchParams.get("cli_callback");
  const cliState = searchParams.get("cli_state") || "";
  // `next` 携带用户原本要去的受保护 URL（例如 /invite/{id}）。
  // 改成 URL 驱动的 workspace 后不再有默认的 "/issues" 落点——如果没有 `next`，
  // 就等登录后根据用户的 workspace 列表来决定。先做净化，避免伪造的 `?next=https://evil`
  // 在登录成功后把用户弹到站外。
  const nextUrl = sanitizeNextUrl(searchParams.get("next"));

  const hasOnboarded = useHasOnboarded();

  // 在本页观察到 auth 已稳定为「未登录」时置位一次。之后出现的任何 `user`
  // 都来自本会话里的登录表单，而不是到达时就已存在的旧会话。
  const settledLoggedOutRef = useRef(false);

  // 「到达时就已经登录」的场景——优先尊重 ?next=，否则回退到第一个 workspace
  // （没有 workspace 则去 /onboarding）。当用户是为了授权 CLI 而来时，整段逻辑全部跳过。
  useEffect(() => {
    if (isLoading) return;
    if (!user) {
      settledLoggedOutRef.current = true;
      return;
    }
    if (cliCallbackRaw) return;
    // 表单新登录（issue #5009）：user 被写入时 handleVerify 还在拉 workspace 列表，
    // 所以这个 effect 过去会读到尚未播种的列表缓存，从而用 replace 到 /workspaces/new
    // 跟 handleSuccess 抢导航。登录后的导航归 handleSuccess 管；
    // 这个 effect 只为「到达时已经登录」的访客服务。
    if (settledLoggedOutRef.current) return;
    if (nextUrl) {
      router.replace(nextUrl);
      return;
    }
    // 用 fetch 而不是读缓存：全新页面加载时缓存是冷的，`getQueryData() ?? []` 会把
    // 实际有 workspace 的用户误路由到 /workspaces/new。fetch 失败时回退到 []——
    // 跟读冷缓存得到的结果一致，而不是把用户困在登录页。
    void qc
      .ensureQueryData(workspaceListOptions())
      .catch(() => [] as Workspace[])
      .then((list) => resolveLoggedInDestination(qc, hasOnboarded, list))
      .then((dest) => router.replace(dest));
  }, [isLoading, user, router, nextUrl, cliCallbackRaw, hasOnboarded, qc]);

  const handleSuccess = async () => {
    // 直接读最新的用户快照——闭包里的 `hasOnboarded` 是在登录完成前捕获的，这里会过期。
    const currentUser = useAuthStore.getState().user;
    const onboarded = currentUser?.onboarded_at != null;
    if (nextUrl) {
      router.push(nextUrl);
      return;
    }
    const list = qc.getQueryData<Workspace[]>(workspaceKeys.list()) ?? [];
    router.push(await resolveLoggedInDestination(qc, onboarded, list));
  };

  return (
    <LoginPage
      onSuccess={handleSuccess}
      cliCallback={
        cliCallbackRaw && validateCliCallback(cliCallbackRaw)
          ? { url: cliCallbackRaw, state: cliState }
          : undefined
      }
      onTokenObtained={setLoggedInCookie}
    />
  );
}

/**
 * 登录页默认导出：用 Suspense 包裹，避免 `useSearchParams` 触发 CSR bailout。
 *
 * @returns 登录页内容。
 */
export default function Page() {
  return (
    <Suspense fallback={null}>
      <LoginPageContent />
    </Suspense>
  );
}
