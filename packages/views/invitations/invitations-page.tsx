"use client";

import { useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import {
  myInvitationListOptions,
  workspaceKeys,
  workspaceListOptions,
} from "@multica/core/workspace/queries";
import { paths } from "@multica/core/paths";
import type { Invitation } from "@multica/core/types";
import { AppLink, useNavigation } from "../navigation";
import { useLogout } from "../auth";
import { DragStrip } from "../platform";
import { useT } from "../i18n";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { LogOut, Mail, Users } from "lucide-react";

/**
 * invitations 模块：批量处理邀请的页面。
 * 面向那些因回调/登录检测到邮箱有未处理邀请、而落到这里的新用户。
 *
 * 设计：
 *  - 该路由只有未完成 onboarding 的用户可达（回调/登录入口会把已完成
 *    onboarding 的用户直接送进工作区；这些用户的新邀请改在侧边栏的待处理
 *    邀请下拉里呈现）。
 *  - 用户勾选零个或多个邀请后点「提交」：
 *      • 一个都没选 → 继续进 /onboarding
 *      • 选了 ≥1 个 → 逐个接受、标记 onboarding 完成、跳进第一个接受的工作区。
 *  - 未勾选的邀请刻意在数据库里保持 `pending`。用户之后可以从侧边栏拒绝
 *    它们；这里不自动拒绝，因为关闭/刷新本页不该是一个破坏性动作。
 */
export function InvitationsPage() {
  const { t } = useT("invite");
  const { push } = useNavigation();
  const qc = useQueryClient();
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const {
    data: invitations,
    isLoading,
    error: fetchError,
    refetch,
  } = useQuery(myInvitationListOptions());

  const toggle = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const handleSubmit = async () => {
    setError(null);

    // 一个都没选：交给 onboarding。待处理邀请保持 pending，之后可从侧边栏接。
    if (selected.size === 0) {
      push(paths.onboarding());
      return;
    }

    setSubmitting(true);
    const acceptedIds: string[] = [];
    try {
      for (const id of selected) {
        await api.acceptInvitation(id);
        acceptedIds.push(id);
      }

      const firstAcceptedInvite = invitations?.find(
        (inv) => inv.id === acceptedIds[0],
      );

      // markOnboardingComplete 是前端侧对后端逻辑的双保险：每条 AcceptInvitation
      // 事务已经经 MarkUserOnboarded 设置了 onboarded_at，但从客户端调用能保证
      // 返回的 `User` 是刚写好的，也让 refreshMe 有权威数据可读。
      await api.markOnboardingComplete({
        completion_path: "invite_accept",
        workspace_id: firstAcceptedInvite?.workspace_id,
      });
      await useAuthStore.getState().refreshMe();

      qc.invalidateQueries({ queryKey: workspaceKeys.myInvitations() });
      const wsList = await qc.fetchQuery({
        ...workspaceListOptions(),
        staleTime: 0,
      });

      const targetWs = firstAcceptedInvite
        ? wsList.find((w) => w.id === firstAcceptedInvite.workspace_id)
        : undefined;

      // 如果按 id 解析不到刚接受的工作区（理论上不该发生——后端刚插入成员、
      // 我们又刚重拉过），退回解析器。不要盲目跳到 wsList[0]：那可能把用户
      // 瞬移到另一个他恰好也属于的、毫不相干的旧工作区。
      push(
        targetWs ? paths.workspace(targetWs.slug).issues() : paths.newWorkspace(),
      );
    } catch (e) {
      setError(
        e instanceof Error
          ? e.message
          : t(($) => $.batch.error_generic),
      );
      // 部分成功：失败之前落地的任何接受都已在后端设置 onboarded_at
      // （AcceptInvitation 事务按邀请原子执行）。刷新本地用户 + 工作区状态，
      // 让侧边栏反映部分接受，也避免用户被卡在过期的 `onboarded_at == null`
      // 视图。再次提交是安全的——服务端对重复接受返回 4xx，会走进 catch 分支
      // 把错误亮出来。
      if (acceptedIds.length > 0) {
        await useAuthStore.getState().refreshMe().catch(() => {});
        qc.invalidateQueries({ queryKey: workspaceKeys.list() });
      }
      qc.invalidateQueries({ queryKey: workspaceKeys.myInvitations() });
      refetch();
    } finally {
      setSubmitting(false);
    }
  };

  if (isLoading) {
    return (
      <InvitationsShell>
        <Card className="w-full max-w-lg">
          <CardContent className="flex flex-col gap-4 py-12">
            <Skeleton className="h-6 w-48" />
            <Skeleton className="h-4 w-72" />
            <Skeleton className="h-16 w-full" />
            <Skeleton className="h-16 w-full" />
          </CardContent>
        </Card>
      </InvitationsShell>
    );
  }

  // 空 / 出错：把用户送去 onboarding，绝不把他们卡死。
  // 真正的拉取失败很罕见；把「无邀请」处理成「没邀请」比把用户困在一个无从
  // 操作的错误页更安全。
  if (fetchError || !invitations || invitations.length === 0) {
    return (
      <InvitationsShell>
        <Card className="w-full max-w-md">
          <CardContent className="flex flex-col items-center gap-4 py-12">
            <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
              <Mail className="h-6 w-6 text-muted-foreground" />
            </div>
            <h2 className="text-title font-semibold">{t(($) => $.batch.empty_title)}</h2>
            <p className="text-body text-muted-foreground text-center">
              {t(($) => $.batch.empty_hint)}
            </p>
            <Button
              render={<AppLink href={paths.onboarding()} />}
              nativeButton={false}
            >
              {t(($) => $.batch.empty_continue)}
            </Button>
          </CardContent>
        </Card>
      </InvitationsShell>
    );
  }

  const submitLabel =
    selected.size === 0
      ? t(($) => $.batch.submit_skip)
      : t(($) => $.batch.submit_join, { count: selected.size });

  return (
    <InvitationsShell>
      <Card className="w-full max-w-lg">
        <CardContent className="flex flex-col gap-6 py-10">
          <div className="flex flex-col items-center gap-3 text-center">
            <div className="flex h-12 w-12 items-center justify-center rounded-full bg-primary/10">
              <Users className="h-6 w-6 text-primary" />
            </div>
            <div className="space-y-1">
              <h2 className="text-title-lg font-semibold">
                {t(($) => $.batch.title)}
              </h2>
              <p className="text-body text-muted-foreground">
                {t(($) => $.batch.subtitle)}
              </p>
            </div>
          </div>

          <ul className="flex flex-col gap-2">
            {invitations.map((inv) => (
              <InvitationRow
                key={inv.id}
                invitation={inv}
                checked={selected.has(inv.id)}
                onToggle={() => toggle(inv.id)}
              />
            ))}
          </ul>

          <Button
            className="w-full"
            onClick={handleSubmit}
            disabled={submitting}
          >
            {submitting ? t(($) => $.batch.joining) : submitLabel}
          </Button>

          {error && (
            <p className="text-body text-destructive text-center">{error}</p>
          )}
        </CardContent>
      </Card>
    </InvitationsShell>
  );
}

/** 邀请列表中的单行：勾选框 + 工作区名 + 邀请者与角色说明。 */
function InvitationRow({
  invitation,
  checked,
  onToggle,
}: {
  invitation: Invitation;
  checked: boolean;
  onToggle: () => void;
}) {
  const { t } = useT("invite");
  const inviter =
    invitation.inviter_name ||
    invitation.inviter_email ||
    t(($) => $.batch.row_inviter_fallback);
  const roleLine =
    invitation.role === "admin"
      ? t(($) => $.batch.row_invited_admin, { inviter })
      : t(($) => $.batch.row_invited_member, { inviter });
  return (
    <li>
      <label
        className="flex cursor-pointer items-start gap-3 rounded-md border border-border bg-card p-4 hover:bg-accent/40"
      >
        <Checkbox
          checked={checked}
          onCheckedChange={onToggle}
          className="mt-1"
        />
        <div className="flex-1 min-w-0 space-y-1">
          <div className="font-medium truncate">
            {invitation.workspace_name ?? t(($) => $.batch.row_workspace_fallback)}
          </div>
          <div className="text-caption text-muted-foreground truncate">
            {roleLine}
          </div>
        </div>
      </label>
    </li>
  );
}

/** 邀请页所有状态共用的外壳：居中内容 + 右上角退出登录按钮。 */
function InvitationsShell({ children }: { children: ReactNode }) {
  const { t } = useT("invite");
  const logout = useLogout();
  return (
    <div className="relative flex min-h-svh flex-col bg-background">
      <DragStrip />
      <Button
        variant="ghost"
        size="sm"
        className="absolute top-16 right-12 text-muted-foreground hover:text-destructive"
        onClick={logout}
      >
        <LogOut />
        {t(($) => $.batch.log_out)}
      </Button>
      <div className="flex flex-1 flex-col items-center justify-center px-6 pb-12">
        {children}
      </div>
    </div>
  );
}
