"use client";

import { useEffect } from "react";
import { useNavigationStore } from "@multica/core/navigation";
import { useAuthStore } from "@multica/core/auth";
import {
  paths,
  resolvePostAuthDestination,
  useCurrentWorkspace,
  useHasOnboarded,
} from "@multica/core/paths";
import { useWorkspaceList } from "@multica/core/workspace";
import { useRecentIssuesStore } from "@multica/core/issues/stores";
import { useNavigation } from "../navigation";

/**
 * 布局模块：仪表盘的认证 + 工作区守卫 Hook。
 * 根据登录状态、工作区列表和 URL slug 决定是否重定向，并返回用户/加载状态。
 *
 * 重定向逻辑：
 *  - 认证仍在加载 → 等待
 *  - 未登录 → /login
 *  - 已登录但工作区列表还没加载 → 等待（不要过早跳转）
 *  - 已登录但 URL slug 解析不到任何工作区 →
 *    `resolvePostAuthDestination(list, hasOnboarded)`（先看有没有工作区；
 *    完整表见 paths/resolve.ts）
 *
 * 这个守卫只在 URL slug 解析不到时才重定向。onboarding 本身会在导航进入
 * 工作区之前把用户标记为已 onboard，所以进入仪表盘不再依赖后续的 Helper 弹窗。
 *
 * （旧注释声称这种状态在物理上不可能，因为 CreateWorkspace 和
 * AcceptInvitation 会原子地标记 onboarded。CreateWorkspace 现在不再标记；
 * AcceptInvitation 仍会——被邀请人完全跳过弹窗。）
 *
 * 我们直接读工作区列表查询状态（而不是依赖 useCurrentWorkspace 的 null
 * 返回），是为了区分「列表加载中」和「slug 没找到」。否则用户可能在工作区
 * 列表到达之前看到一次瞬时的重定向。
 */
export function useDashboardGuard() {
  const { pathname, replace } = useNavigation();
  const user = useAuthStore((s) => s.user);
  const isLoading = useAuthStore((s) => s.isLoading);
  const workspace = useCurrentWorkspace();
  const hasOnboarded = useHasOnboarded();
  const { workspaces, ready: workspaceListReady } = useWorkspaceList({
    enabled: !!user,
  });

  useEffect(() => {
    if (isLoading) return;
    if (!user) {
      replace(paths.login());
      return;
    }
    if (!workspaceListReady) return;
    if (!workspace) {
      replace(resolvePostAuthDestination(workspaces, hasOnboarded));
    }
  }, [user, isLoading, workspaceListReady, workspace, workspaces, hasOnboarded, replace]);

  useEffect(() => {
    useNavigationStore.getState().onPathChange(pathname);
  }, [pathname]);

  // 清理用户已不再属于的工作区的 recent-issues 桶。工作区列表解析后运行，
  // 成员关系变化（工作区被删、用户被踢、用户退出）时再次运行。
  useEffect(() => {
    if (!workspaceListReady) return;
    useRecentIssuesStore
      .getState()
      .pruneWorkspaces(workspaces.map((w) => w.id));
  }, [workspaceListReady, workspaces]);

  return { user, isLoading, workspace };
}
