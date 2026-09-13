"use client";

import type { ReactNode } from "react";
import { useDashboardGuard } from "./use-dashboard-guard";

/**
 * 布局模块：仪表盘布局的共享守卫组件。
 * 在用户登录且工作区解析完成后才渲染子内容，否则显示加载占位。
 */
interface DashboardGuardProps {
  children: ReactNode;
  /** 认证或工作区加载期间渲染的占位内容 */
  loadingFallback?: ReactNode;
}

/**
 * 仪表盘布局的守卫：认证检查 → 工作区检查 → 渲染子内容。
 * Web 和桌面端的布局都在它内部组合各自的 UI 结构。
 *
 * WorkspaceIdProvider 已移除——useWorkspaceId() 现在通过 useCurrentWorkspace()
 * 从 URL slug 推导。守卫仍然等待工作区解析完成，这样下游组件才能安全地
 * 调用 useWorkspaceId()。
 * @param children 通过守卫后渲染的内容。
 * @param loadingFallback 加载期间的占位内容，默认 null。
 */
export function DashboardGuard({
  children,
  loadingFallback = null,
}: DashboardGuardProps) {
  const { user, isLoading, workspace } = useDashboardGuard();

  if (isLoading || !workspace) return <>{loadingFallback}</>;
  if (!user) return null;

  return <>{children}</>;
}
