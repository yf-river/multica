"use client";

/**
 * Google OAuth 回调页：拿到授权码后完成登录，并按用户状态路由到正确的落点
 * （普通登录、CLI 登录、待接受邀请的 onboarding 用户、已有 workspace 的返回用户等）。
 */
import { Suspense, useEffect, useState } from "react";
import { useSearchParams, useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { sanitizeNextUrl, useAuthStore } from "@multica/core/auth";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { paths, resolvePostAuthDestination } from "@multica/core/paths";
import { api } from "@multica/core/api";
import { validateCliCallback, redirectToCliCallback } from "@multica/views/auth";
import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
  CardContent,
} from "@multica/ui/components/ui/card";
import { Loader2 } from "lucide-react";

/** 回调逻辑主体：负责解析 OAuth 参数、完成登录并决定跳转目标。 */
function CallbackContent() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const qc = useQueryClient();
  const loginWithGoogle = useAuthStore((s) => s.loginWithGoogle);
  const [error, setError] = useState("");

  useEffect(() => {
    const code = searchParams.get("code");
    if (!code) {
      setError("缺少授权码");
      return;
    }

    const errorParam = searchParams.get("error");
    if (errorParam) {
      setError(errorParam === "access_denied" ? "访问被拒绝" : errorParam);
      return;
    }

    const state = searchParams.get("state") || "";
    const stateParts = state.split(",");
    const nextPart = stateParts.find((p) => p.startsWith("next:"));
    // 去掉 "next:" 前缀，然后丢掉任何不是安全相对路径的内容，
    // 避免攻击者伪造 `state=next:https://evil` 把用户重定向到站外。
    const nextUrl = sanitizeNextUrl(nextPart ? nextPart.slice(5) : null);

    // CLI 回调参数——它要跟着 Google OAuth 往返一趟，这样无头/WSL2 环境下的
    // `multica login` 才能在浏览器端 Google 认证完成后拿到 JWT。
    const cliCallbackPart = stateParts.find((p) => p.startsWith("cli_callback:"));
    const cliStatePart = stateParts.find((p) => p.startsWith("cli_state:"));
    const cliCallbackRaw = cliCallbackPart
      ? decodeURIComponent(cliCallbackPart.slice("cli_callback:".length))
      : null;
    const cliState = cliStatePart
      ? decodeURIComponent(cliStatePart.slice("cli_state:".length))
      : "";

    const redirectUri = `${window.location.origin}/auth/callback`;

    // 重定向前先校验 CLI 回调 URL——state 参数经过了 Google OAuth，必须当作攻击者可控。
    const cliCallback =
      cliCallbackRaw && validateCliCallback(cliCallbackRaw)
        ? cliCallbackRaw
        : null;

    if (cliCallback) {
      // CLI 登录流程：用 Google 授权码换 JWT，再把 token 重定向回 CLI 的本地 HTTP 监听器（例如 WSL2 主机）。
      api
        .googleLogin(code, redirectUri)
        .then(({ token }) => {
          redirectToCliCallback(cliCallback, token, cliState);
        })
        .catch((err) => {
          setError(err instanceof Error ? err.message : "登录失败");
        });
    } else {
      // 普通 Web 登录流程
      loginWithGoogle(code, redirectUri)
        .then(async (loggedInUser) => {
          const wsList = await api.listWorkspaces();
          qc.setQueryData(workspaceKeys.list(), wsList);
          const onboarded = loggedInUser.onboarded_at != null;

          // 1. nextUrl 优先：`next=/invite/<id>` 总能撑过 OAuth 往返——
          //    用户点击了某个具体链接，我们就应该精确落到那个目的地。
          if (nextUrl) {
            router.push(nextUrl);
            return;
          }

          // 2. 尚未 onboard 的用户即使没带 `next=`，邮箱里也可能有待处理的邀请
          //    （比如是直接在 multica.ai 上全新登录，而不是点邮件链接进来的，
          //    或者 state 在往返途中丢了）。按邮箱查一下，如果有就路由到批量 /invitations 页。
          //    已经 onboard 的用户跳过这个查询——他们的新邀请会出现在侧边栏下拉里，
          //    而不是被一道强制墙挡住。
          if (!onboarded) {
            try {
              const invites = await api.listMyInvitations();
              if (invites.length > 0) {
                qc.setQueryData(workspaceKeys.myInvitations(), invites);
                router.push(paths.invitations());
                return;
              }
            } catch {
              // 邀请查询时的网络抖动是非致命的——继续走到正常的登录后目的地，
              // 避免用户卡在空白回调页。最坏情况他们落到 /onboarding，之后侧边栏会再展示邀请。
            }
          }

          // 3. 默认：交给解析器（首次用户去 onboarding，回访用户去第一个 workspace，
          //    已 onboard 但没有 workspace 的用户去 /workspaces/new）。
          //    已 onboard 但没记录来源的用户，其来源归因回填由 dashboard 外壳里的
          //    `<SourceBackfillModal />` 处理——不是一次路由绕行，所以我们直接路由到目标。
          router.push(resolvePostAuthDestination(wsList, onboarded));
        })
        .catch((err) => {
          setError(err instanceof Error ? err.message : "登录失败");
        });
    }
  }, [searchParams, loginWithGoogle, router, qc]);

  if (error) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Card className="w-full max-w-sm">
          <CardHeader className="text-center">
            <CardTitle className="text-display-sm">登录失败</CardTitle>
            <CardDescription>{error}</CardDescription>
          </CardHeader>
          <CardContent className="flex justify-center">
            <a href={paths.login()} className="text-primary underline-offset-4 hover:underline">
              Back to login
            </a>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center">
      <Card className="w-full max-w-sm">
        <CardHeader className="text-center">
          <CardTitle className="text-display-sm">正在登录……</CardTitle>
          <CardDescription>请稍候，正在完成登录</CardDescription>
        </CardHeader>
        <CardContent className="flex justify-center">
          <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
        </CardContent>
      </Card>
    </div>
  );
}

/**
 * 回调页的默认导出：用 Suspense 包裹内容组件。
 *
 * `useSearchParams` 要求组件被 Suspense 边界包裹（否则会在构建期触发 CSR bailout），
 * 所以把真正读参数的内容拆到 CallbackContent，这里只负责包一层。
 *
 * @returns 回调页内容（登录中加载态 / 失败提示）。
 */
export default function CallbackPage() {
  return (
    <Suspense fallback={null}>
      <CallbackContent />
    </Suspense>
  );
}
