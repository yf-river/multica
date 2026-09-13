"use client";

import { useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import {
  workspaceKeys,
  workspaceListOptions,
} from "@multica/core/workspace/queries";
import {
  paths,
  resolvePostAuthDestination,
  useHasOnboarded,
} from "@multica/core/paths";
import { AppLink, useNavigation } from "../navigation";
import { useLogout } from "../auth";
import { DragStrip } from "../platform";
import { useT } from "../i18n";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { ArrowLeft, LogOut, Users, Check, X } from "lucide-react";

/**
 * invite 模块：接受/拒绝工作区邀请的全页外壳。
 * Web（Next.js 路由 `/invite/[id]`）与桌面（窗口浮层）共用。返回与退出登录
 * 的顶部操作放在这里，让两个平台拿到一致的体验。平台 chrome（窗口拖拽区、
 * 沉浸模式）由桌面端浮层叠加上去；web 直接渲染本页。
 */

/** 邀请页组件的属性。 */
export interface InvitePageProps {
  invitationId: string;
  /**
   * 可选的「返回」处理器。调用方只在存在合理目的地时传它（用户至少有一个
   * 工作区，或从应用内流程进来）。首次邀请 / 零工作区的路径上省略——那种
   * 情况下 Back 无处可去，退出登录成了唯一出口。
   */
  onBack?: () => void;
}

/**
 * 邀请页主体：拉取邀请详情并处理接受/拒绝；根据状态渲染对应界面。
 */
export function InvitePage({ invitationId, onBack }: InvitePageProps) {
  const { t } = useT("invite");
  const { push } = useNavigation();
  const qc = useQueryClient();
  const [accepting, setAccepting] = useState(false);
  const [declining, setDeclining] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<"accepted" | "declined" | null>(null);

  const { data: invitation, isLoading, error: fetchError } = useQuery({
    queryKey: ["invitation", invitationId],
    queryFn: () => api.getInvitation(invitationId),
  });

  // 兜底「前往仪表盘」目的地用的工作区列表。邀请页是工作区之前的前台全局
  // 路由，不能依赖 WorkspaceSlugProvider。
  const { data: wsList = [] } = useQuery(workspaceListOptions());
  const hasOnboarded = useHasOnboarded();
  const fallbackDest = resolvePostAuthDestination(wsList, hasOnboarded);

  const handleAccept = async () => {
    setAccepting(true);
    setError(null);
    try {
      await api.acceptInvitation(invitationId);
      // 双保险：后端 AcceptInvitation 已在同一事务里设置 onboarded_at，但这里
      // 显式调用 markOnboardingComplete + refreshMe，让本地用户状态立即同步，
      // 下游守卫不会读到过期的 `null`。
      await api.markOnboardingComplete({
        completion_path: "invite_accept",
        workspace_id: invitation?.workspace_id,
      });
      await useAuthStore.getState().refreshMe();
      setDone("accepted");
      // 重新拉取工作区列表，好知道刚加入的工作区的 slug。
      const nextList = await qc.fetchQuery({
        ...workspaceListOptions(),
        staleTime: 0,
      });
      const joined = nextList.find((w) => w.id === invitation?.workspace_id);
      qc.invalidateQueries({ queryKey: workspaceKeys.myInvitations() });
      // 跳进刚加入的工作区。[workspaceSlug]/layout 会从 URL 同步 api client、
      // stores 与 last_workspace_slug cookie。
      const dest = joined
        ? paths.workspace(joined.slug).issues()
        : fallbackDest;
      setTimeout(() => push(dest), 1000);
    } catch (e) {
      setError(e instanceof Error ? e.message : t(($) => $.errors.accept_failed));
    } finally {
      setAccepting(false);
    }
  };

  const handleDecline = async () => {
    setDeclining(true);
    setError(null);
    try {
      await api.declineInvitation(invitationId);
      setDone("declined");
      qc.invalidateQueries({ queryKey: workspaceKeys.myInvitations() });
    } catch (e) {
      setError(e instanceof Error ? e.message : t(($) => $.errors.decline_failed));
    } finally {
      setDeclining(false);
    }
  };

  if (isLoading) {
    return (
      <InviteShell onBack={onBack}>
        <Card className="w-full max-w-md">
          <CardContent className="flex flex-col items-center gap-4 py-12">
            <Skeleton className="h-12 w-12 rounded-full" />
            <Skeleton className="h-5 w-48" />
            <Skeleton className="h-4 w-64" />
            <Skeleton className="h-9 w-32 rounded-md" />
          </CardContent>
        </Card>
      </InviteShell>
    );
  }

  if (fetchError || !invitation) {
    return (
      <InviteShell onBack={onBack}>
        <Card className="w-full max-w-md">
          <CardContent className="flex flex-col items-center gap-4 py-12">
            <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
              <X className="h-6 w-6 text-muted-foreground" />
            </div>
            <h2 className="text-title font-semibold">{t(($) => $.not_found.title)}</h2>
            <p className="text-body text-muted-foreground text-center">
              {t(($) => $.not_found.description)}
            </p>
            <Button
              variant="outline"
              render={<AppLink href={fallbackDest} />}
              nativeButton={false}
            >
              {t(($) => $.not_found.go_to_dashboard)}
            </Button>
          </CardContent>
        </Card>
      </InviteShell>
    );
  }

  if (done === "accepted") {
    return (
      <InviteShell onBack={onBack}>
        <Card className="w-full max-w-md">
          <CardContent className="flex flex-col items-center gap-4 py-12">
            <div className="flex h-12 w-12 items-center justify-center rounded-full bg-primary/10">
              <Check className="h-6 w-6 text-primary" />
            </div>
            <h2 className="text-title font-semibold">
              {t(($) => $.accepted.title, { workspace_name: invitation.workspace_name })}
            </h2>
            <p className="text-body text-muted-foreground">
              {t(($) => $.accepted.redirecting)}
            </p>
          </CardContent>
        </Card>
      </InviteShell>
    );
  }

  if (done === "declined") {
    return (
      <InviteShell onBack={onBack}>
        <Card className="w-full max-w-md">
          <CardContent className="flex flex-col items-center gap-4 py-12">
            <h2 className="text-title font-semibold">{t(($) => $.declined.title)}</h2>
            <p className="text-body text-muted-foreground">{t(($) => $.declined.description)}</p>
            <Button
              variant="outline"
              render={<AppLink href={fallbackDest} />}
              nativeButton={false}
            >
              {t(($) => $.declined.go_to_dashboard)}
            </Button>
          </CardContent>
        </Card>
      </InviteShell>
    );
  }

  const isExpired = invitation.status !== "pending";
  const isAlreadyHandled = invitation.status === "accepted" || invitation.status === "declined";

  return (
    <InviteShell onBack={onBack}>
      <Card className="w-full max-w-md">
        <CardContent className="flex flex-col items-center gap-6 py-12">
          <div className="flex h-14 w-14 items-center justify-center rounded-full bg-primary/10">
            <Users className="h-7 w-7 text-primary" />
          </div>

          <div className="text-center space-y-2">
            <h2 className="text-title-lg font-semibold">
              {t(($) => $.main.join_title, {
                workspace_name: invitation.workspace_name ?? t(($) => $.main.fallback_workspace_name),
              })}
            </h2>
            <p className="text-body text-muted-foreground">
              <strong>{invitation.inviter_name || invitation.inviter_email}</strong>{" "}
              {invitation.role === "admin"
                ? t(($) => $.main.invited_role_admin)
                : t(($) => $.main.invited_role_member)}
            </p>
          </div>

          {isAlreadyHandled ? (
            <div className="text-body text-muted-foreground">
              {invitation.status === "accepted"
                ? t(($) => $.main.already_handled_accepted)
                : t(($) => $.main.already_handled_declined)}
            </div>
          ) : isExpired ? (
            <div className="text-body text-muted-foreground">
              {t(($) => $.main.expired)}
            </div>
          ) : (
            <div className="flex gap-3 w-full">
              <Button
                variant="outline"
                className="flex-1"
                onClick={handleDecline}
                disabled={accepting || declining}
              >
                {declining ? t(($) => $.main.declining) : t(($) => $.main.decline)}
              </Button>
              <Button
                className="flex-1"
                onClick={handleAccept}
                disabled={accepting || declining}
              >
                {accepting ? t(($) => $.main.joining) : t(($) => $.main.accept)}
              </Button>
            </div>
          )}

          {error && (
            <p className="text-body text-destructive text-center">{error}</p>
          )}
        </CardContent>
      </Card>
    </InviteShell>
  );
}

/**
 * InvitePage 所有渲染状态（加载 / 错误 / 默认 / 已接受 / 已拒绝）共用的外壳。
 * 把「返回」和「退出登录」按钮在所有分支与所有平台上都放在同一位置。
 */
function InviteShell({
  onBack,
  children,
}: {
  onBack?: () => void;
  children: ReactNode;
}) {
  const { t } = useT("invite");
  const logout = useLogout();
  return (
    <div className="relative flex min-h-svh flex-col bg-background">
      <DragStrip />
      {onBack && (
        <Button
          variant="ghost"
          size="sm"
          className="absolute top-16 left-12 text-muted-foreground"
          onClick={onBack}
        >
          <ArrowLeft />
          {t(($) => $.header.back)}
        </Button>
      )}
      <Button
        variant="ghost"
        size="sm"
        className="absolute top-16 right-12 text-muted-foreground hover:text-destructive"
        onClick={logout}
      >
        <LogOut />
        {t(($) => $.header.log_out)}
      </Button>
      <div className="flex flex-1 flex-col items-center justify-center px-6 pb-12">
        {children}
      </div>
    </div>
  );
}
